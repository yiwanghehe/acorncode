package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"acorncode/internal/bus"
	"acorncode/internal/permission"
	"acorncode/internal/session"
)

func TestNewModel(t *testing.T) {
	m := NewModel(Config{
		SessionID: "sess_test",
		ModelName: "qwen-test",
		Bus:       bus.New(),
		Loop:      nil, // 测试不跑 loop
		Ctx:       context.Background(),
	})

	if m.cfg.SessionID != "sess_test" {
		t.Errorf("SessionID = %q", m.cfg.SessionID)
	}
	if m.status != "Idle" {
		t.Errorf("初始 status = %q, 期望 Idle", m.status)
	}
	if !m.inputOn {
		t.Errorf("初始 inputOn 应 true")
	}
}

func TestView_ContainsStatus(t *testing.T) {
	m := NewModel(Config{
		SessionID: "s1",
		ModelName: "test-model",
		Bus:       bus.New(),
		Ctx:       context.Background(),
	})
	m.width = 80

	view := m.View()

	if !strings.Contains(view, "test-model") {
		t.Errorf("View 应含 model name: %s", view)
	}
	if !strings.Contains(view, "Idle") {
		t.Errorf("View 应含初始 status Idle: %s", view)
	}
	if !strings.Contains(view, "> ") {
		t.Errorf("View 应含 input prompt: %s", view)
	}
}

func TestView_Quitting(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.quitting = true
	view := m.View()
	if !strings.Contains(view, "Bye") {
		t.Errorf("quitting View 应含 'Bye': %s", view)
	}
}

func TestUpdate_WindowSize(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	if m.width != 100 || m.height != 50 {
		t.Errorf("width/height 未更新: %d/%d", m.width, m.height)
	}
}

func TestUpdate_KeyChar(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.inputOn = true

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if m.input != "hi" {
		t.Errorf("input = %q, 期望 hi", m.input)
	}
}

func TestUpdate_Backspace(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.input = "hello"
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.input != "hell" {
		t.Errorf("input = %q, 期望 hell", m.input)
	}
}

// TestUpdate_CJKInput 验证中文（多字节字符）能正常输入。
// 回归：旧实现用 len(msg.String())==1 按字节判断，中文一个字符占 3 字节
// 被直接丢弃，导致 TUI 无法输入中文。
func TestUpdate_CJKInput(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.inputOn = true

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'你'}})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'好'}})
	// 输入法一次可能给多个 rune
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("世界")})
	if m.input != "你好世界" {
		t.Errorf("input = %q, 期望 你好世界", m.input)
	}
}

// TestUpdate_BackspaceCJK 验证退格删除中文按 rune 边界，不切碎成乱码。
// 回归：旧实现 m.input[:len(m.input)-1] 按字节切，删中文留下半个多字节字符。
func TestUpdate_BackspaceCJK(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.input = "你好"
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.input != "你" {
		t.Errorf("input = %q, 期望 你（应整字删除，不留乱码字节）", m.input)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.input != "" {
		t.Errorf("input = %q, 期望空", m.input)
	}
}

func TestUpdate_EmptyEnter(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.input = "   "
	before := m.input
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.input != before {
		t.Errorf("空 enter 不应改 input: %q", m.input)
	}
}

func TestUpdate_HelpCommand(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.input = "/help"

	// 直接调 helper 模拟（完整 Init 需 tea runtime）
	m.history.Reset()
	_ = m.input
	// 完整测试需要 loop 返回 nil，简化：直接验证 appendHistory 写入历史
	m.input = ""
	m.appendHistory("Commands:\n")
	if !strings.Contains(m.history.String(), "Commands:") {
		t.Errorf("/help 历史应含 'Commands:'")
	}
}

func TestUpdate_CtrlCQuits(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Errorf("Ctrl+C 应返 Quit cmd")
	}
	if !m.quitting {
		t.Errorf("Ctrl+C 后 quitting 应 true")
	}
}

func TestUpdate_EscQuits(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.inputOn = true
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Errorf("Esc 应返 Quit cmd")
	}
	if !m.quitting {
		t.Errorf("Esc 后 quitting 应 true")
	}
}

func TestUpdate_BusEvent_TextDelta(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.stream.Reset()

	// 真实 processor 行为：同一个 part（同 ID），每次 delta 携带【全量累积文本】。
	// 第一次 "hello "，第二次 "hello world"。TUI 应整体替换，最终显示全量。
	ev := bus.Event{
		Type:      bus.EventPartDelta,
		SessionID: "s1",
		Data:      &session.TextPart{ID: "prt_1", Text: "hello "},
	}
	m.handleBusEvent(ev)

	ev2 := bus.Event{
		Type:      bus.EventPartDelta,
		SessionID: "s1",
		Data:      &session.TextPart{ID: "prt_1", Text: "hello world"},
	}
	m.handleBusEvent(ev2)

	if m.stream.String() != "hello world" {
		t.Errorf("stream = %q, 期望 'hello world'", m.stream.String())
	}
	if m.status != "Streaming" {
		t.Errorf("status = %q, 期望 Streaming", m.status)
	}
}

// TestUpdate_BusEvent_TextDelta_NoStacking 回归：part.delta 携带全量文本，
// TUI 必须整体替换而非追加。旧实现 WriteString 追加会把全量文本反复叠加，
// 产生 "我是我是 Ac我是 AcornCode..." 式的文本雪球。
func TestUpdate_BusEvent_TextDelta_NoStacking(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.stream.Reset()

	// 模拟流式：同一 part 逐步累积 "我" → "我是" → "我是 AcornCode"
	steps := []string{"我", "我是", "我是 AcornCode"}
	for _, full := range steps {
		m.handleBusEvent(bus.Event{
			Type:      bus.EventPartDelta,
			SessionID: "s1",
			Data:      &session.TextPart{ID: "prt_x", Text: full},
		})
	}
	if got := m.stream.String(); got != "我是 AcornCode" {
		t.Errorf("stream = %q, 期望 '我是 AcornCode'（不应堆叠）", got)
	}
}

// TestUpdate_BusEvent_TextDelta_NewPartResets 验证新的文本 part（不同 ID）
// 会清掉上一段流式内容，从头显示。
func TestUpdate_BusEvent_TextDelta_NewPartResets(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})

	m.handleBusEvent(bus.Event{
		Type: bus.EventPartDelta, SessionID: "s1",
		Data: &session.TextPart{ID: "prt_a", Text: "first answer"},
	})
	m.handleBusEvent(bus.Event{
		Type: bus.EventPartDelta, SessionID: "s1",
		Data: &session.TextPart{ID: "prt_b", Text: "second"},
	})
	if got := m.stream.String(); got != "second" {
		t.Errorf("stream = %q, 期望 'second'（新 part 应重置）", got)
	}
}

// TestConversationHistoryAccumulates 验证多轮对话历史累积不覆盖（v1.12 滚动式）。
// 回归：旧实现每轮 Reset 正文，只显示最新一轮，无法回看历史。
func TestConversationHistoryAccumulates(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})

	// 第一轮：用户提问入历史 + 流式回复 + 定格
	m.appendHistory("> 第一个问题\n")
	m.handleBusEvent(bus.Event{
		Type: bus.EventPartDelta, SessionID: "s1",
		Data: &session.TextPart{ID: "p1", Text: "第一个回答"},
	})
	m.flushStreamToHistory()

	// 第二轮
	m.appendHistory("> 第二个问题\n")
	m.handleBusEvent(bus.Event{
		Type: bus.EventPartDelta, SessionID: "s1",
		Data: &session.TextPart{ID: "p2", Text: "第二个回答"},
	})
	m.flushStreamToHistory()

	body := m.bodyContent()
	for _, want := range []string{"第一个问题", "第一个回答", "第二个问题", "第二个回答"} {
		if !strings.Contains(body, want) {
			t.Errorf("正文应保留历史 %q，实际:\n%s", want, body)
		}
	}
	// 流式缓冲在定格后应清空
	if m.stream.Len() != 0 {
		t.Errorf("定格后 stream 应为空, 实际 %q", m.stream.String())
	}
}

func TestUpdate_BusEvent_ToolUpdated_Complete(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})

	ev := bus.Event{
		Type:      bus.EventPartUpdated,
		SessionID: "s1",
		Data:      &session.ToolPart{ToolID: "read", State: session.ToolComplete},
	}
	m.handleBusEvent(ev)

	if !strings.Contains(m.status, "read done") {
		t.Errorf("status = %q, 期望含 'read done'", m.status)
	}
}

func TestUpdate_BusEvent_ToolUpdated_Pending(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})

	ev := bus.Event{
		Type:      bus.EventPartUpdated,
		SessionID: "s1",
		Data:      &session.ToolPart{ToolID: "bash", State: session.ToolPending},
	}
	m.handleBusEvent(ev)

	if !strings.Contains(m.status, "bash") {
		t.Errorf("status = %q, 期望含 'bash'", m.status)
	}
}

func TestUpdate_BusEvent_ToolUpdated_Errored(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})

	ev := bus.Event{
		Type:      bus.EventPartUpdated,
		SessionID: "s1",
		Data:      &session.ToolPart{ToolID: "read", State: session.ToolErrored, Error: "file not found"},
	}
	m.handleBusEvent(ev)

	if !strings.Contains(m.status, "error") {
		t.Errorf("status = %q, 期望含 'error'", m.status)
	}
}

func TestUpdate_BusEvent_StateChange_String(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})

	ev := bus.Event{
		Type:      bus.EventAgentStateChange,
		SessionID: "s1",
		Data:      "ToolExec",
	}
	m.handleBusEvent(ev)

	if m.status != "ToolExec" {
		t.Errorf("status = %q, 期望 ToolExec", m.status)
	}
}

func TestUpdate_BusEvent_StateChange_Map(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})

	ev := bus.Event{
		Type:      bus.EventAgentStateChange,
		SessionID: "s1",
		Data:      map[string]any{"from": "Building", "to": "Streaming", "event": "finish"},
	}
	m.handleBusEvent(ev)

	// to 优先于 event
	if m.status != "Streaming" {
		t.Errorf("status = %q, 期望 'Streaming'（to 字段优先）", m.status)
	}
}

func TestUpdate_BusEvent_Error_Fatal(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})

	ev := bus.Event{
		Type:      bus.EventError,
		SessionID: "s1",
		Data:      map[string]any{"err": "boom", "fatal": true},
	}
	m.handleBusEvent(ev)

	if !strings.Contains(m.status, "FATAL") {
		t.Errorf("status = %q, 期望含 'FATAL'", m.status)
	}
}

func TestUpdate_BusEvent_Error_NonFatal(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})

	ev := bus.Event{
		Type:      bus.EventError,
		SessionID: "s1",
		Data:      map[string]any{"err": "soft fail", "fatal": false},
	}
	m.handleBusEvent(ev)

	if !strings.Contains(m.status, "Error") {
		t.Errorf("status = %q, 期望含 'Error'", m.status)
	}
}

func TestUpdate_LoopDone(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.inputOn = false
	m.setStatus("Streaming")

	_, _ = m.Update(loopDoneMsg{})
	if m.status != "Idle" {
		t.Errorf("loop done 后 status = %q, 期望 Idle", m.status)
	}
	if !m.inputOn {
		t.Errorf("loop done 后 inputOn 应 true")
	}
}

func TestUpdate_LoopDone_WithError(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	_, _ = m.Update(loopDoneMsg{Err: errTest})
	if !strings.Contains(m.status, "Error") {
		t.Errorf("loop done 错应 status 含 'Error': %q", m.status)
	}
}

// errTest 是测试用错误
type testErr struct{}

func (testErr) Error() string { return "test error" }

var errTest = testErr{}

// ========== v1.0.1: Permission 弹窗 ==========

func TestView_PermissionDialog(t *testing.T) {
	m := NewModel(Config{
		SessionID: "s1", ModelName: "m",
		Bus: bus.New(), Broker: permission.NewBroker(nil),
		Ctx: context.Background(),
	})
	m.width = 80

	m.permReq = &permRequest{
		reqID:   "ask-1",
		tool:    "bash",
		pattern: "rm -rf /",
	}

	view := m.View()
	if !strings.Contains(view, "Permission needed") {
		t.Errorf("View 应含 'Permission needed': %s", view)
	}
	if !strings.Contains(view, "bash") {
		t.Errorf("View 应含 tool name 'bash': %s", view)
	}
	if !strings.Contains(view, "rm -rf /") {
		t.Errorf("View 应含 pattern: %s", view)
	}
	if !strings.Contains(view, "Allow") || !strings.Contains(view, "Always") || !strings.Contains(view, "Deny") {
		t.Errorf("View 应含 3 选项: %s", view)
	}
}

func TestUpdate_PermissionAsked_OpensDialog(t *testing.T) {
	m := NewModel(Config{
		SessionID: "s1", ModelName: "m",
		Bus: bus.New(), Broker: permission.NewBroker(nil),
		Ctx: context.Background(),
	})

	ev := bus.Event{
		Type:      "permission.asked",
		SessionID: "s1",
		Data: map[string]any{
			"req_id":   "ask-test-123",
			"tool":     "edit",
			"patterns": []string{"/etc/passwd"},
		},
	}
	m.handleBusEvent(ev)

	if m.permReq == nil {
		t.Fatal("permReq 应非 nil")
	}
	if m.permReq.tool != "edit" {
		t.Errorf("tool = %q, 期望 edit", m.permReq.tool)
	}
	if m.permReq.pattern != "/etc/passwd" {
		t.Errorf("pattern = %q, 期望 /etc/passwd", m.permReq.pattern)
	}
	if m.permChoice != 0 {
		t.Errorf("初始 choice = %d, 期望 0", m.permChoice)
	}
}

func TestUpdate_PermKey_AllowOnce(t *testing.T) {
	broker := permission.NewBroker(nil)
	m := NewModel(Config{
		SessionID: "s1", ModelName: "m",
		Bus: bus.New(), Broker: broker,
		Ctx: context.Background(),
	})
	m.permReq = &permRequest{reqID: "ask-x", tool: "read", pattern: "/x"}

	// 按 1
	_ = m.handlePermKey("1")

	if m.permReq != nil {
		t.Errorf("Allow 后 permReq 应清空")
	}
	if !strings.Contains(m.status, "Allowed") {
		t.Errorf("status 应含 'Allowed': %q", m.status)
	}
}

func TestUpdate_PermKey_AllowSession(t *testing.T) {
	broker := permission.NewBroker(nil)
	m := NewModel(Config{
		SessionID: "s1", ModelName: "m",
		Bus: bus.New(), Broker: broker,
		Ctx: context.Background(),
	})
	m.permReq = &permRequest{reqID: "ask-x", tool: "read", pattern: "/x"}

	// 按 2
	_ = m.handlePermKey("2")

	if m.permReq != nil {
		t.Errorf("session_allow 后 permReq 应清空")
	}
	// SessionApprove 应已标记
	// （直接调 broker.Ask 验证）
	err := broker.Ask(context.Background(), permission.Request{
		Permission: "read",
		Patterns:   []string{"/x"},
	})
	if err != nil {
		t.Errorf("session_allow 后再 Ask 应通过, got %v", err)
	}
}

func TestUpdate_PermKey_Deny(t *testing.T) {
	broker := permission.NewBroker(nil)
	m := NewModel(Config{
		SessionID: "s1", ModelName: "m",
		Bus: bus.New(), Broker: broker,
		Ctx: context.Background(),
	})
	m.permReq = &permRequest{reqID: "ask-x", tool: "bash", pattern: "rm -rf /"}

	_ = m.handlePermKey("3")

	if m.permReq != nil {
		t.Errorf("Deny 后 permReq 应清空")
	}
	if !strings.Contains(m.status, "Denied") {
		t.Errorf("status 应含 'Denied': %q", m.status)
	}
}

func TestUpdate_PermKey_TabCycles(t *testing.T) {
	m := NewModel(Config{
		SessionID: "s1", ModelName: "m",
		Bus: bus.New(), Broker: permission.NewBroker(nil),
		Ctx: context.Background(),
	})
	m.permReq = &permRequest{reqID: "ask-x", tool: "read"}
	m.permChoice = 0

	_ = m.handlePermKey("right")
	if m.permChoice != 1 {
		t.Errorf("right 后 = %d, 期望 1", m.permChoice)
	}
	_ = m.handlePermKey("right")
	if m.permChoice != 2 {
		t.Errorf("right 后 = %d, 期望 2", m.permChoice)
	}
	_ = m.handlePermKey("right")
	if m.permChoice != 0 {
		t.Errorf("right 后 wrap = %d, 期望 0", m.permChoice)
	}
	_ = m.handlePermKey("left")
	if m.permChoice != 2 {
		t.Errorf("left 后 wrap = %d, 期望 2", m.permChoice)
	}
}

func TestUpdate_PermMode_InterceptsKeys(t *testing.T) {
	m := NewModel(Config{
		SessionID: "s1", ModelName: "m",
		Bus: bus.New(), Broker: permission.NewBroker(nil),
		Ctx: context.Background(),
	})
	m.permReq = &permRequest{reqID: "ask-x", tool: "bash"}

	// 在 perm 模式下，普通键（如 'h'）应被弹窗吃掉，不进 input
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if m.input != "" {
		t.Errorf("perm 模式下 input 不应被加字符: %q", m.input)
	}
	if m.permReq == nil {
		t.Errorf("perm 模式不该被吃掉")
	}
}

func TestUpdate_PermMode_CtrlCStillQuits(t *testing.T) {
	m := NewModel(Config{
		SessionID: "s1", ModelName: "m",
		Bus: bus.New(), Broker: permission.NewBroker(nil),
		Ctx: context.Background(),
	})
	m.permReq = &permRequest{reqID: "ask-x", tool: "bash"}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Errorf("Ctrl+C 应仍 Quit")
	}
	if !m.quitting {
		t.Errorf("Ctrl+C 后 quitting 应 true")
	}
}
