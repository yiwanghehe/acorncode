// Package agent 实现单 session 的 Agent Loop 状态机。
// 参考: docs/acorncode-architect.md §7.1
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

// ============================================================
// 类型定义
// ============================================================

// LoopState 状态枚举（参考 §7.1.1）
type LoopState int32

const (
	StateIdle LoopState = iota
	StateBuildingRequest
	StateStreaming
	StateToolExecuting
	StateWaitingPermission
	StateCompacting
	StateErrored
	StateStopped
)

// String 把状态名用于日志和 Bus 事件
func (s LoopState) String() string {
	return [...]string{
		"Idle", "BuildingRequest", "Streaming",
		"ToolExecuting", "WaitingPermission",
		"Compacting", "Errored", "Stopped",
	}[s]
}

// LoopConfig 控制 loop 行为
type LoopConfig struct {
	AgentName    string
	Model        llm.Model
	MaxTurns     int // 0 = 不限
	MaxTokens    int // 上下文预算（触发 compact）
	MaxBashFails int // 默认 5（见 §9.5.5）
	MaxToolRetry int // 默认 3
	MaxSameError int // 默认 3
	MaxTools     int // 每轮工具预算（见 §8.6）
}

// SessionStore 是 Loop 需要的 session.Service 接口子集
type SessionStore interface {
	GetSession(ctx context.Context, id string) (*session.Session, error)
	Messages(ctx context.Context, sessionID string, limit int) ([]*session.Message, error)
	AppendMessage(ctx context.Context, msg *session.Message) error
	UpsertPart(ctx context.Context, p session.Part) error
	GetPart(ctx context.Context, id string) (session.Part, error)
	SetFinishReason(ctx context.Context, messageID string, reason string) error
}

// Loop 是单 session 的状态机实例。
// 重要：每个 Loop 由单 goroutine 拥有，无并发访问。
// state 字段不需要原子操作，因为所有转移都发生在 Run() 同一 goroutine 上。
type Loop struct {
	sessionID string
	cfg       LoopConfig
	store     SessionStore
	bus       *bus.Bus
	llm       llm.Provider
	strategy  toolcall.Strategy
	tools     *tool.Registry
	perm      *permission.Broker
	instr     *instruction.Loader
	compactor compaction.Compactor // 可选：v1.0.3 起

	state LoopState
	turn  int
	breaker *circuitBreaker
}

// NewLoop 构造 Loop（手工 DI 模式，见 §3.4）
func NewLoop(sessionID string, cfg LoopConfig, store SessionStore, b *bus.Bus, p llm.Provider, s toolcall.Strategy, t *tool.Registry, pb *permission.Broker, il *instruction.Loader) *Loop {
	return &Loop{
		sessionID: sessionID,
		cfg:       cfg,
		store:     store,
		bus:       b,
		llm:       p,
		strategy:  s,
		tools:     t,
		perm:      pb,
		instr:     il,
		state:     StateIdle,
		breaker:   newCircuitBreaker(circuitConfig{}),
	}
}

// SetCompactor 注入 compactor（v1.0.3 起）
func (l *Loop) SetCompactor(c compaction.Compactor) {
	l.compactor = c
}

// CurrentState 返回当前状态（用于测试和 TUI 显示）
func (l *Loop) CurrentState() LoopState {
	return l.state
}

// ============================================================
// 错误定义
// ============================================================

var (
	errNeedCompact = errors.New("上下文预算超限，需要 compact")
	errTurnAborted = errors.New("turn 中止：失败次数过多")
	errFatal       = errors.New("fatal 错误")
)

// ============================================================
// 核心：Run 主循环
// ============================================================

// Run 驱动一条用户消息完成整个 turn。
// 返回时机：ctx 取消、收到 stop、达到 max turns、或不可恢复错误。
func (l *Loop) Run(ctx context.Context, userMsg *session.UserMessage) error {
	slog.InfoContext(ctx, "loop.Run 启动", "session_id", l.sessionID)
	defer func() {
		slog.InfoContext(ctx, "loop.Run 结束", "session_id", l.sessionID, "state", l.CurrentState().String())
	}()

	if err := l.guard(ctx, StateIdle); err != nil {
		return err
	}

	l.turn = 0
	l.breaker = newCircuitBreaker(circuitConfig{
		MaxToolRetry: l.cfg.MaxToolRetry,
		MaxBashFails: l.cfg.MaxBashFails,
		MaxSameError: l.cfg.MaxSameError,
	})

	for {
		// ctx 取消：立即退出
		if err := ctx.Err(); err != nil {
			l.setState(ctx, StateStopped)
			return err
		}

		// 1. 构造请求
		l.setState(ctx, StateBuildingRequest)
		req, err := l.buildRequest(ctx, userMsg)
		if err != nil {
			if errors.Is(err, errNeedCompact) {
				l.setState(ctx, StateCompacting)
				if cerr := l.compact(ctx); cerr != nil {
					return l.fatal(ctx, fmt.Errorf("compact 失败: %w", cerr))
				}
				continue
			}
			return l.fatal(ctx, err)
		}

		// 2. 流式调 LLM
		l.setState(ctx, StateStreaming)
		calls, finish, err := l.streamAndProcess(ctx, req)
		if err != nil {
			return l.handleError(ctx, err)
		}

		// 3. 退出条件
		if finish != nil && finish.Reason == "stop" && len(calls) == 0 {
			l.setState(ctx, StateStopped)
			return nil
		}

		// 4. 执行 tool calls
		if len(calls) > 0 {
			l.setState(ctx, StateToolExecuting)
			if err := l.executeToolCalls(ctx, calls); err != nil {
				if errors.Is(err, errTurnAborted) {
					// 熔断：让下一轮模型看到错误，循环继续
					l.turn++
					continue
				}
				return l.fatal(ctx, err)
			}
		}

		// 5. 检查 turn 上限
		l.turn++
		if l.cfg.MaxTurns > 0 && l.turn >= l.cfg.MaxTurns {
			l.setState(ctx, StateStopped)
			return nil
		}

		// 第二轮起不再重复用户消息
		userMsg = nil
	}
}

// ============================================================
// 构造请求
// ============================================================

func (l *Loop) buildRequest(ctx context.Context, userMsg *session.UserMessage) (*llm.ChatRequest, error) {
	// 1. 读历史
	history, err := l.store.Messages(ctx, l.sessionID, 0)
	if err != nil {
		return nil, fmt.Errorf("读历史失败: %w", err)
	}

	// 2. 加载 AGENTS.md
	instrContent, err := l.instr.Load(ctx)
	if err != nil {
		slog.WarnContext(ctx, "加载 instruction 失败", "err", err)
	}

	// 3. 组装 system prompt
	system := l.buildSystemPrompt(instrContent)

	// 4. 工具裁剪
	pickedTools := l.tools.PickForTurn(
		l.cfg.AgentName,
		l.cfg.MaxTools,
		lastUserText(history),
		recentToolCalls(history),
	)

	// 5. 估算 token，超限则触发 compact
	if estimateTokens(history, pickedTools) > l.cfg.MaxTokens {
		return nil, errNeedCompact
	}

	// 6. 构造 ChatRequest
	req := &llm.ChatRequest{
		Model:   l.cfg.Model,
		System:  system,
		Tools:   toolDefsToLLM(pickedTools),
		History: toModelMessages(history),
	}

	// 7. 追加新用户消息
	if userMsg != nil {
		req.History = append(req.History, llm.Message{
			Role:    "user",
			Content: userMsg.Text,
		})
	}

	// 8. 让 toolcall 策略预处理请求（v1.4 修复接线）。
	//    Prompted/Grammar 借此注入 system 工具说明；Grammar 还会生成 GBNF
	//    并按需设置 req.Format 做约束生成。Native 策略为 no-op。
	if l.strategy != nil {
		if err := l.strategy.Prepare(req, pickedTools); err != nil {
			slog.WarnContext(ctx, "strategy.Prepare 失败", "strategy", l.strategy.Name(), "err", err)
		}
	}

	return req, nil
}

// buildSystemPrompt 拼接 system prompt（builtin + AGENTS.md）
func (l *Loop) buildSystemPrompt(agentsMD string) []string {
	var sections []string
	sections = append(sections, builtinBasePrompt)
	if agentsMD != "" {
		sections = append(sections, "# Project Conventions\n\n"+agentsMD)
	}
	return sections
}

// ============================================================
// 流式处理
// ============================================================

// streamAndProcess 调 LLM 拿到事件流，由 Processor 应用到 assistant message
func (l *Loop) streamAndProcess(ctx context.Context, req *llm.ChatRequest) ([]ToolCall, *llm.FinishEvent, error) {
	rawCh, err := l.llm.Stream(ctx, *req)
	if err != nil {
		return nil, nil, fmt.Errorf("llm stream 失败: %w", err)
	}

	// Strategy.ParseStream 把 raw chunks 转 typed events
	typedCh := l.strategy.ParseStream(ctx, rawCh)

	assistant := l.createAssistantMessage()
	processor := newProcessor(assistant, l.bus, l.store)

	for ev := range typedCh {
		if err := processor.Apply(ctx, ev); err != nil {
			return nil, nil, err
		}
	}

	// 保存 assistant message
	if err := l.store.AppendMessage(ctx, processor.Message()); err != nil {
		return nil, nil, err
	}

	// 收集 tool calls
	calls := processor.PendingToolCalls()
	var finish *llm.FinishEvent
	if f := processor.FinishEvent(); f != nil {
		finish = f
	}

	return calls, finish, nil
}

// createAssistantMessage 创建 assistant 消息骨架
func (l *Loop) createAssistantMessage() *session.Message {
	return &session.Message{
		ID:        newID("msg"),
		SessionID: l.sessionID,
		Role:      "assistant",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// ============================================================
// 工具执行（含 Permission）
// ============================================================

// executeToolCalls 串行执行一组 tool calls
func (l *Loop) executeToolCalls(ctx context.Context, calls []ToolCall) error {
	for _, call := range calls {
		// 1. 权限询问
		l.setState(ctx, StateWaitingPermission)
		if err := l.perm.Ask(ctx, permission.Request{
			ID:         newID("perm"),
			SessionID:  l.sessionID,
			Permission: call.ToolID,
			Patterns:   call.Patterns(),
			Tool: &permission.ToolRef{
				MessageID: call.MessageID,
				CallID:    call.CallID,
			},
		}); err != nil {
			slog.InfoContext(ctx, "工具权限被拒",
				"tool", call.ToolID, "call_id", call.CallID)
			l.recordToolRejected(ctx, call, err.Error())
			continue
		}

		// 2. 执行
		l.setState(ctx, StateToolExecuting)
		t, ok := l.tools.Get(call.ToolID)
		if !ok {
			l.recordToolRejected(ctx, call, fmt.Sprintf("未知工具: %s", call.ToolID))
			continue
		}

		args, _ := json.Marshal(call.Args)
		tc := tool.Context{
			SessionID: l.sessionID,
			MessageID: call.MessageID,
			Agent:     l.cfg.AgentName,
			CallID:    call.CallID,
			Cwd:       l.sessionCwd(),
			Ask:       l.perm.Ask, // 工具内可递归调 Ask
		}

		result, _ := t.Execute(ctx, args, tc)

		// 3. 熔断检查（见 §9.5.5）
		if err := l.breaker.Check(call, result); err != nil {
			return err
		}

		// 4. 更新已有 part（processor 创的）而不是新建
		l.updateToolPart(ctx, call, result)
	}
	return nil
}

// ============================================================
// Compaction（v1.0.3 实现）
// ============================================================

// compact 是压缩入口。v1.0.3 起调 compactor。
// 若 compactor 未注入，走 v0.1 stub：返错让 Loop 退到 errFatal。
func (l *Loop) compact(ctx context.Context) error {
	slog.InfoContext(ctx, "compact 触发", "session_id", l.sessionID)
	if l.compactor == nil {
		return errors.New("compactor 尚未注入，留待后续")
	}

	// 1. 取所有消息
	msgs, err := l.store.Messages(ctx, l.sessionID, 0)
	if err != nil {
		return fmt.Errorf("读消息失败: %w", err)
	}

	// 2. 构 llm.Message 列表
	llmMsgs := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		llmMsgs = append(llmMsgs, llm.Message{
			Role:    m.Role,
			Content: extractText(m),
		})
	}

	// 3. 调 compactor
	newMsgs, err := l.compactor.Compact(ctx, llmMsgs)
	if err != nil {
		slog.WarnContext(ctx, "compact 失败，使用原消息", "err", err)
		// 不阻断：让 Loop 继续用原 history（可能超 token，但 try）
		return nil
	}

	// 4. 写回 store（v1.0.3 简化：仅打日志，不持久化压缩结果）
	// 真正持久化要清空原 messages 再追加新 summary，留给 v1.0.4
	slog.InfoContext(ctx, "compact 完成",
		"原消息数", len(llmMsgs),
		"压缩后数", len(newMsgs),
	)
	return nil
}

// extractText 从 message 抽 text
func extractText(m *session.Message) string {
	for _, p := range m.Parts {
		if tp, ok := p.(*session.TextPart); ok {
			return tp.Text
		}
	}
	return ""
}

// ============================================================
// 状态机辅助
// ============================================================

// guard 检查当前状态是否为期望状态
func (l *Loop) guard(ctx context.Context, expected LoopState) error {
	cur := l.CurrentState()
	if cur != expected {
		return fmt.Errorf("%w: 期望状态 %s, 当前 %s", errFatal, expected, cur)
	}
	return nil
}

// setState 转移状态并发出 Bus 事件（同状态转移不发）
func (l *Loop) setState(ctx context.Context, to LoopState) {
	from := l.state
	if from == to {
		return
	}
	l.state = to
	l.publishStateChange(ctx, from, to)
}

// publishStateChange 发出状态变更事件
func (l *Loop) publishStateChange(ctx context.Context, from, to LoopState) {
	l.bus.Publish(bus.Event{
		Type:      bus.EventAgentStateChange,
		SessionID: l.sessionID,
		Data: map[string]any{
			"from": from.String(),
			"to":   to.String(),
		},
	})
}

// handleError 处理可恢复错误（errTurnAborted 走重试路径，其他走 fatal）
func (l *Loop) handleError(ctx context.Context, err error) error {
	if errors.Is(err, errTurnAborted) {
		l.setState(ctx, StateBuildingRequest)
		return nil
	}
	return l.fatal(ctx, err)
}

// fatal 终止 Loop，发出错误事件
func (l *Loop) fatal(ctx context.Context, err error) error {
	slog.ErrorContext(ctx, "loop fatal", "err", err, "session_id", l.sessionID)
	l.bus.Publish(bus.Event{
		Type:      bus.EventError,
		SessionID: l.sessionID,
		Data:      map[string]any{"err": err.Error(), "fatal": true},
	})
	l.setState(ctx, StateErrored)
	l.setState(ctx, StateStopped)
	return err
}

// ============================================================
// 结果写回
// ============================================================

// recordToolRejected 记录工具被拒（permission 拒 / 未知工具）
func (l *Loop) recordToolRejected(ctx context.Context, call ToolCall, errMsg string) {
	part := l.lookupOrNewToolPart(ctx, call)
	part.State = session.ToolRejected
	part.Error = errMsg
	part.EndedAt = time.Now().UnixMilli()
	_ = l.store.UpsertPart(ctx, part)
	l.bus.Publish(bus.Event{
		Type: bus.EventPartUpdated, SessionID: l.sessionID,
		Data: part,
	})
}

// updateToolPart 更新 processor 创的 part 状态（工具完成后）
func (l *Loop) updateToolPart(ctx context.Context, call ToolCall, result tool.Result) {
	state := session.ToolComplete
	if result.Status == "error" {
		state = session.ToolErrored
	}

	part := l.lookupOrNewToolPart(ctx, call)
	part.Output = result.Output
	part.Title = result.Title
	part.State = state
	part.EndedAt = time.Now().UnixMilli()
	part.Error = result.Error
	_ = l.store.UpsertPart(ctx, part)
	l.bus.Publish(bus.Event{
		Type: bus.EventPartUpdated, SessionID: l.sessionID,
		Data: part,
	})
}

// lookupOrNewToolPart 优先用 call.PartID 找 store 里已有 part（processor 创的），
// 找不到就新建。这样保证 ToolPart 在 turn 中只创建一次。
func (l *Loop) lookupOrNewToolPart(ctx context.Context, call ToolCall) *session.ToolPart {
	if call.PartID != "" {
		if p, err := l.store.GetPart(ctx, call.PartID); err == nil {
			if tp, ok := p.(*session.ToolPart); ok {
				return tp
			}
		}
	}
	return &session.ToolPart{
		ID:        newID("prt"),
		MessageID: call.MessageID,
		SessionID: l.sessionID,
		CallID:    call.CallID,
		ToolID:    call.ToolID,
		Args:      marshalArgs(call.Args),
	}
}

// ============================================================
// 辅助函数
// ============================================================

// sessionCwd 返回 session 工作目录（从 store 读 session.Directory）
func (l *Loop) sessionCwd() string {
	sess, err := l.store.GetSession(context.Background(), l.sessionID)
	if err == nil && sess != nil && sess.Directory != "" {
		return sess.Directory
	}
	return "."
}

// ToolCall 是 Loop 内部对 LLM 工具调用的视图
type ToolCall struct {
	MessageID string
	CallID    string
	ToolID    string
	Args      map[string]any
	PartID    string // 对应的 ToolPart.ID（processor 创建时填）
}

// Patterns 返回 permission 匹配的 pattern 列表
func (c ToolCall) Patterns() []string {
	return []string{c.ToolID}
}

// marshalArgs 把 args map 转为 json.RawMessage
func marshalArgs(args map[string]any) json.RawMessage {
	if args == nil {
		return nil
	}
	b, _ := json.Marshal(args)
	return b
}

// newID 生成带前缀的唯一 ID（统一走 internal/id 包，R2 去重）。
func newID(prefix string) string {
	return id.New(prefix)
}

// toolDefsToLLM 把 tool.Definition 转为 llm.Definition
func toolDefsToLLM(in []tool.Definition) []llm.Definition {
	out := make([]llm.Definition, len(in))
	for i, t := range in {
		out[i] = llm.Definition{
			ID:          t.ID,
			Description: t.Description,
			Keywords:    t.Keywords,
			JSONSchema:  t.JSONSchema,
		}
	}
	return out
}
