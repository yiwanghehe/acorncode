// Package server - HTTP/SSE API（v1.0.4）
//
// 让 AcornCode 在 headless 环境跑（CI、远程调用），不需 TTY。
//
// 端点：
//
//	POST /v1/chat
//	  Body:    {"message": "...", "session_id": "sess_xxx（可选）"}
//	  Headers: Accept: text/event-stream
//	  Reply:   SSE 流
//	    event: text\ndata: {"text": "..."}\n\n
//	    event: part.updated\ndata: {"tool": "read", "state": "complete"}\n\n
//	    event: finish\ndata: {"reason": "stop"}\n\n
//
//	GET /healthz
//	  返 200 OK
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"acorncode/internal/agent"
	"acorncode/internal/bus"
	"acorncode/internal/compaction"
	"acorncode/internal/id"
	"acorncode/internal/instruction"
	"acorncode/internal/llm"
	"acorncode/internal/permission"
	"acorncode/internal/session"
	"acorncode/internal/tool"
	"acorncode/internal/toolcall"
)

// Store 是 server 需要的 session store 子集（与 agent.SessionStore 兼容）
type Store interface {
	CreateSession(ctx context.Context, sess *session.Session) error
	GetSession(ctx context.Context, id string) (*session.Session, error)
	AppendMessage(ctx context.Context, msg *session.Message) error
	UpsertPart(ctx context.Context, p session.Part) error
	GetPart(ctx context.Context, id string) (session.Part, error)
	SetFinishReason(ctx context.Context, messageID string, reason string) error
	Messages(ctx context.Context, sessionID string, limit int) ([]*session.Message, error)
}

// Config 启动 server 所需的依赖
type Config struct {
	Addr     string // ":8080"
	Provider llm.Provider
	Strategy toolcall.Strategy
	Store    Store
	Tools    *tool.Registry
	Broker   *permission.Broker
	Loader   *instruction.Loader
	Model    llm.Model
	APIKey   string // v1.1.1：设了就要 Authorization: Bearer <key>，空则开放
}

// Server 持有依赖 + http.Server
type Server struct {
	cfg Config
	srv *http.Server
}

// New 创建 server
func New(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	s := &Server{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth) // 健康检查无鉴权
	// v1/ 端点包鉴权中间件
	mux.Handle("/v1/chat", s.withAuth(http.HandlerFunc(s.handleChat)))
	mux.Handle("/v1/sessions", s.withAuth(http.HandlerFunc(s.handleSessions)))
	mux.Handle("/v1/sessions/", s.withAuth(http.HandlerFunc(s.handleSessionByID)))
	s.srv = &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// withAuth 鉴权中间件（v1.1.1）
//
//   - cfg.APIKey == ""  → 开放（dev 模式）
//   - 否则要求 "Authorization: Bearer <key>"，key 不匹配返 401
//
// 用 crypto/subtle.ConstantTimeCompare 防 timing attack。
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIKey == "" {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			s.unauthorized(w, "missing or invalid Authorization scheme (want Bearer)")
			return
		}
		got := auth[len(prefix):]
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.APIKey)) != 1 {
			s.unauthorized(w, "invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// unauthorized 写 401 响应
func (s *Server) unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("www-authenticate", `Bearer realm="acorn"`)
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":   "unauthorized",
		"message": msg,
	})
}

// Start 阻塞启动 server
func (s *Server) Start() error {
	slog.Info("server starting", "addr", s.cfg.Addr)
	return s.srv.ListenAndServe()
}

// Stop 优雅关闭
func (s *Server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// handleHealth 健康检查
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK\n"))
}

// handleSessions 路由 /v1/sessions
//
//	GET  /v1/sessions        → 列表
//	POST /v1/sessions        → 创建（body 可选 {title, directory}）
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listSessions(w, r)
	case http.MethodPost:
		s.createSession(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSessionByID 路由 /v1/sessions/{id}/*
//
//	GET  /v1/sessions/{id}              → 详情
//	POST /v1/sessions/{id}/chat         → 续聊（SSE 流）
func (s *Server) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	// 解析 path
	// /v1/sessions/{id}/chat
	// /v1/sessions/{id}
	path := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	sessID := parts[0]

	// 验证 session 存在
	if _, err := s.cfg.Store.GetSession(r.Context(), sessID); err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]any{
			"error":      "session not found",
			"session_id": sessID,
		})
		return
	}

	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		s.getSession(w, r, sessID)
	case len(parts) == 2 && parts[1] == "chat" && r.Method == http.MethodPost:
		s.handleChatByID(w, r, sessID)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// CreateSessionRequest POST /v1/sessions 的入参
type CreateSessionRequest struct {
	Title     string `json:"title,omitempty"`
	Directory string `json:"directory,omitempty"`
}

// SessionInfo 是返回的 session 元信息
type SessionInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// sessionToInfo 把 session.Session 转为 API 返回的 SessionInfo（R4：消除 3 处重复映射）。
func sessionToInfo(sess *session.Session) SessionInfo {
	return SessionInfo{
		ID:        sess.ID,
		Title:     sess.Title,
		Directory: sess.Directory,
		CreatedAt: sess.CreatedAt.Unix(),
		UpdatedAt: sess.UpdatedAt.Unix(),
	}
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	sessID := "sess_" + randomID()
	cwd, _ := os.Getwd()
	if req.Directory == "" {
		req.Directory = cwd
	}
	now := time.Now()
	sess := &session.Session{
		ID:        sessID,
		Title:     req.Title,
		Directory: req.Directory,
		Agent:     "build",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.cfg.Store.CreateSession(r.Context(), sess); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	s.writeJSON(w, http.StatusCreated, sessionToInfo(sess))
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	// SQLiteStore 用 ListSessions 返 []*Session；v1.1.2 简化只返元信息
	type storeLister interface {
		ListSessions(ctx context.Context) ([]*session.Session, error)
	}
	sl, ok := s.cfg.Store.(storeLister)
	if !ok {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "store 不支持 ListSessions",
		})
		return
	}

	sessions, err := sl.ListSessions(r.Context())
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	out := make([]SessionInfo, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, sessionToInfo(sess))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"sessions": out,
		"count":    len(out),
	})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request, sessID string) {
	sess, err := s.cfg.Store.GetSession(r.Context(), sessID)
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, sessionToInfo(sess))
}

// handleChatByID 处理 /v1/sessions/{id}/chat：解析 body 后强制 session_id = sessID，
// 直接调用 serveChatStream（R6：不再 marshal 回 r.Body 的 hack）。
func (s *Server) handleChatByID(w http.ResponseWriter, r *http.Request, sessID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, ok := s.decodeChatRequest(w, r)
	if !ok {
		return
	}
	req.SessionID = sessID // path 优先
	s.serveChatStream(w, r, req)
}

// writeJSON 写 JSON 响应
func (s *Server) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// ChatRequest 是 /v1/chat 的入参
type ChatRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
	// ForceTool 为 true 时，本次请求强制工具调用（仅 grammar 策略生效，v1.7）。
	// 请求级开关：不影响其他并发请求（每请求用独立 Grammar 实例）。
	ForceTool bool `json:"force_tool,omitempty"`
}

// decodeChatRequest 解析并校验 ChatRequest。失败时已写好错误响应并返回 ok=false。
func (s *Server) decodeChatRequest(w http.ResponseWriter, r *http.Request) (ChatRequest, bool) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return req, false
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message 不能为空", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// handleChat 处理 /v1/chat：薄入口，解析校验后交给 serveChatStream（R1）。
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, ok := s.decodeChatRequest(w, r)
	if !ok {
		return
	}
	s.serveChatStream(w, r, req)
}

// setupSSE 写 SSE 响应头并返回 flusher（R1 拆分）。
func (s *Server) setupSSE(w http.ResponseWriter) http.Flusher {
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("connection", "keep-alive")
	w.Header().Set("x-accel-buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	return flusher
}

// resolveSessionID 返回请求的 session_id，为空则生成一个 http 会话 ID。
func resolveSessionID(reqID string) string {
	if reqID != "" {
		return reqID
	}
	return "sess_http_" + id.Short()
}

// ensureSession 确保 session 存在：不存在则创建（已存在的 UNIQUE 冲突忽略）。
// 返回写 SSE 错误的标志。
func (s *Server) ensureSession(ctx context.Context, sessID string, w http.ResponseWriter, flusher http.Flusher) bool {
	if _, getErr := s.cfg.Store.GetSession(ctx, sessID); getErr == nil {
		return true // 已存在
	}
	sess := &session.Session{
		ID:        sessID,
		Title:     "http",
		Directory: ".",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if createErr := s.cfg.Store.CreateSession(ctx, sess); createErr != nil {
		if !strings.Contains(createErr.Error(), "已存在") &&
			!strings.Contains(createErr.Error(), "UNIQUE constraint") {
			s.writeSSEError(w, flusher, "create session: "+createErr.Error())
			return false
		}
	}
	return true
}

// appendUserMessage 把用户消息落库（带一个 TextPart）。
func (s *Server) appendUserMessage(ctx context.Context, sessID, text string) {
	msgID := "msg_user_" + id.Short()
	_ = s.cfg.Store.AppendMessage(ctx, &session.Message{
		ID:        msgID,
		SessionID: sessID,
		Role:      "user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Parts: []session.Part{
			&session.TextPart{
				ID:        "prt_user_" + id.Short(),
				MessageID: msgID,
				SessionID: sessID,
				Text:      text,
			},
		},
	})
}

// newChatLoop 创建本次请求的 agent.Loop（注入 compactor + 请求级策略）。
func (s *Server) newChatLoop(sessID string, eventBus *bus.Bus, forceTool bool) *agent.Loop {
	loopCfg := agent.LoopConfig{
		AgentName:    "build",
		Model:        s.cfg.Model,
		MaxTurns:     20,
		MaxTokens:    32000,
		MaxBashFails: 5,
		MaxToolRetry: 3,
		MaxSameError: 3,
		MaxTools:     10,
	}
	loop := agent.NewLoop(sessID, loopCfg, s.cfg.Store, eventBus, s.cfg.Provider, s.strategyForRequest(forceTool), s.cfg.Tools, s.cfg.Broker, s.cfg.Loader)
	loop.SetCompactor(&compaction.SimpleCompactor{
		Provider:   s.cfg.Provider,
		Model:      s.cfg.Model,
		KeepRecent: 6,
		MaxSummary: 500,
	})
	return loop
}

// runChatLoop 用独立 context 跑 loop（client 断开 100ms 后才取消，让 loop 写完 finish），
// 并写最终 finish/error SSE 事件。
func (s *Server) runChatLoop(r *http.Request, loop *agent.Loop, userMsg *session.UserMessage, w http.ResponseWriter, flusher http.Flusher) {
	loopCtx, loopCancel := context.WithCancel(context.Background())
	defer loopCancel()
	go func() {
		<-r.Context().Done()
		time.Sleep(100 * time.Millisecond)
		loopCancel()
	}()
	if err := loop.Run(loopCtx, userMsg); err != nil {
		if errors.Is(err, context.Canceled) {
			s.writeSSE(w, flusher, "finish", map[string]any{"reason": "cancelled"})
			return
		}
		s.writeSSEError(w, flusher, err.Error())
		return
	}
	s.writeSSE(w, flusher, "finish", map[string]any{"reason": "stop"})
}

// serveChatStream 是聊天 SSE 流的编排核心（R1：从原 handleChat 上帝函数拆出）。
// 两个入口（/v1/chat 与 /v1/sessions/{id}/chat）都解析好 req 后调用它。
func (s *Server) serveChatStream(w http.ResponseWriter, r *http.Request, req ChatRequest) {
	flusher := s.setupSSE(w)
	sessID := resolveSessionID(req.SessionID)

	if !s.ensureSession(r.Context(), sessID, w, flusher) {
		return
	}

	eventBus := bus.New()
	defer eventBus.Close()
	s.subscribeAndForward(r.Context(), eventBus, sessID, w, flusher)

	s.writeSSE(w, flusher, "session", map[string]any{"session_id": sessID})
	s.appendUserMessage(r.Context(), sessID, req.Message)

	loop := s.newChatLoop(sessID, eventBus, req.ForceTool)
	s.runChatLoop(r, loop, &session.UserMessage{Text: req.Message}, w, flusher)
}

// strategyForRequest 返回本次请求应使用的 toolcall 策略（v1.7）。
//
// 默认返回共享的 s.cfg.Strategy。当请求要求 force_tool 且基础策略是 grammar 时，
// 返回一个**独立的** Grammar 实例（ForceToolCall=true），避免修改共享策略的状态、
// 不影响其他并发请求。非 grammar 策略时 force_tool 无效，按原策略返回。
func (s *Server) strategyForRequest(forceTool bool) toolcall.Strategy {
	if !forceTool {
		return s.cfg.Strategy
	}
	if _, ok := s.cfg.Strategy.(*toolcall.Grammar); !ok {
		// 非 grammar 策略：force_tool 无意义，记日志并退回共享策略
		slog.Warn("server: force_tool 仅 grammar 策略生效，已忽略",
			"strategy", s.cfg.Strategy.Name())
		return s.cfg.Strategy
	}
	g := toolcall.NewGrammar()
	g.ForceToolCall = true
	return g
}

// subscribeAndForward 订阅 bus 4 topic → 转发到 SSE。
//
// 转发 goroutine 在 ctx 取消或 bus 关闭（调用方 defer eventBus.Close()）时自动退出，
// 因此无需返回显式的 teardown（R8：原先返回的 teardown 是空操作）。
func (s *Server) subscribeAndForward(ctx context.Context, b *bus.Bus, sessID string, w http.ResponseWriter, flusher http.Flusher) {
	type sub struct {
		topic string
		ch    <-chan bus.Event
	}
	subs := []sub{
		{bus.EventPartDelta, nil},
		{bus.EventPartUpdated, nil},
		{bus.EventAgentStateChange, nil},
		{bus.EventError, nil},
	}
	for i := range subs {
		ch, _ := b.SubscribeID(subs[i].topic)
		subs[i].ch = ch
	}

	// 每个 topic 起 goroutine 转发
	for _, sub := range subs {
		go func(topic string, ch <-chan bus.Event) {
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-ch:
					if !ok {
						return
					}
					if ev.SessionID != "" && ev.SessionID != sessID {
						continue
					}
					s.writeSSE(w, flusher, eventName(topic), ev.Data)
				}
			}
		}(sub.topic, sub.ch)
	}
}

// eventName 把 bus 事件名 → SSE event 字段
func eventName(topic string) string {
	switch topic {
	case bus.EventPartDelta:
		return "text"
	case bus.EventPartUpdated:
		return "part"
	case bus.EventAgentStateChange:
		return "state"
	case bus.EventError:
		return "error"
	}
	return topic
}

// writeSSE 写一条 SSE 事件
//
// 用 \n 分隔（裸 LF）。Go 的 chunked encoding 会处理，无需 \r\n。
// 每个字段后一个 \n，事件之间用空行（\n\n）。
func (s *Server) writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, data any) {
	jsonData, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(jsonData))
	if flusher != nil {
		flusher.Flush()
	}
}

// writeSSEError 写一条 error SSE 事件
func (s *Server) writeSSEError(w http.ResponseWriter, flusher http.Flusher, msg string) {
	s.writeSSE(w, flusher, "error", map[string]any{"message": msg})
}

// randomID 生成 8 字符短 ID（统一走 internal/id 包，R2 去重）。
func randomID() string {
	return id.Short()
}
