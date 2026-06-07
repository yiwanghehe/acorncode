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
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"acorncode/internal/agent"
	"acorncode/internal/bus"
	"acorncode/internal/compaction"
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

	s.writeJSON(w, http.StatusCreated, SessionInfo{
		ID:        sess.ID,
		Title:     sess.Title,
		Directory: sess.Directory,
		CreatedAt: sess.CreatedAt.Unix(),
		UpdatedAt: sess.UpdatedAt.Unix(),
	})
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
		out = append(out, SessionInfo{
			ID:        sess.ID,
			Title:     sess.Title,
			Directory: sess.Directory,
			CreatedAt: sess.CreatedAt.Unix(),
			UpdatedAt: sess.UpdatedAt.Unix(),
		})
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
	s.writeJSON(w, http.StatusOK, SessionInfo{
		ID:        sess.ID,
		Title:     sess.Title,
		Directory: sess.Directory,
		CreatedAt: sess.CreatedAt.Unix(),
		UpdatedAt: sess.UpdatedAt.Unix(),
	})
}

// handleChatByID 处理 /v1/sessions/{id}/chat
//
// 复用 handleChat 的逻辑（session_id 来自 path）
func (s *Server) handleChatByID(w http.ResponseWriter, r *http.Request, sessID string) {
	// 复用 handleChat 但强制 session_id = sessID
	// 简单做法：解析 body，把 session_id 强制覆盖
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message 不能为空", http.StatusBadRequest)
		return
	}
	// 强制 session_id
	req.SessionID = sessID
	// 替换 body 里的 session_id（虽然 handler 不会读它，但保险）
	body, _ := json.Marshal(req)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	r.ContentLength = int64(len(body))

	s.handleChat(w, r)
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
}

// handleChat 处理 /v1/chat，返回 SSE 流
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 解析请求
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message 不能为空", http.StatusBadRequest)
		return
	}

	// SSE headers
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("connection", "keep-alive")
	w.Header().Set("x-accel-buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	// 创建或恢复 session
	sessID := req.SessionID
	if sessID == "" {
		sessID = "sess_http_" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	sess := &session.Session{
		ID:        sessID,
		Title:     "http",
		Directory: ".",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	// v1.1.2：by-ID 路径已验证 session 存在；这里也再查一次（兼容 /v1/chat 直调）
	// 不存在才 create，避免 UNIQUE constraint
	if _, getErr := s.cfg.Store.GetSession(r.Context(), sessID); getErr != nil {
		if createErr := s.cfg.Store.CreateSession(r.Context(), sess); createErr != nil {
			if !strings.Contains(createErr.Error(), "已存在") &&
				!strings.Contains(createErr.Error(), "UNIQUE constraint") {
				s.writeSSEError(w, flusher, "create session: "+createErr.Error())
				return
			}
		}
	}

	// 起 bus
	eventBus := bus.New()
	defer eventBus.Close()

	// 订阅 4 个 topic 转发到 SSE
	teardown := s.subscribeAndForward(r.Context(), eventBus, sessID, w, flusher)
	defer teardown()

	// 写第一个 event：session started
	s.writeSSE(w, flusher, "session", map[string]any{
		"session_id": sessID,
	})

	// 写 user message
	userMsg := &session.UserMessage{Text: req.Message}
	msgID := "msg_user_" + randomID()
	_ = s.cfg.Store.AppendMessage(r.Context(), &session.Message{
		ID:        msgID,
		SessionID: sessID,
		Role:      "user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Parts: []session.Part{
			&session.TextPart{
				ID:        "prt_user_" + randomID(),
				MessageID: msgID,
				SessionID: sessID,
				Text:      req.Message,
			},
		},
	})

	// 创建 loop
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
	loop := agent.NewLoop(sessID, loopCfg, s.cfg.Store, eventBus, s.cfg.Provider, s.cfg.Strategy, s.cfg.Tools, s.cfg.Broker, s.cfg.Loader)
	loop.SetCompactor(&compaction.SimpleCompactor{
		Provider:   s.cfg.Provider,
		Model:      s.cfg.Model,
		KeepRecent: 6,
		MaxSummary: 500,
	})

	// 跑 loop（同步等结束）
	// 用独立 context（不依赖 r.Context），client 断开不影响 loop
	// 客户端通过 SSE 看到 finish event 即可
	loopCtx, loopCancel := context.WithCancel(context.Background())
	defer loopCancel()
	go func() {
		<-r.Context().Done()
		// client 断开 100ms 后取消 loop（让 loop 写完 finish event）
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

// subscribeAndForward 订阅 bus 4 topic → 转发到 SSE
func (s *Server) subscribeAndForward(ctx context.Context, b *bus.Bus, sessID string, w http.ResponseWriter, flusher http.Flusher) func() {
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

	return func() {
		for _, sub := range subs {
			_ = sub // 留给 ctx cancel 关
		}
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

// randomShortID 生成 8 字符短 ID（带 counter 防同纳秒冲突）
var idCounter uint64

func randomID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	idCounter++
	now := time.Now().UnixNano() + int64(idCounter)
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[now%int64(len(chars))]
		now /= int64(len(chars))
		if now == 0 {
			now = time.Now().UnixNano() + int64(idCounter)
		}
	}
	return string(b)
}
