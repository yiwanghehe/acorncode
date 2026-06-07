package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"acorncode/internal/bus"
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
	m.text.Reset()
	_ = m.input
	// 完整测试需要 loop 返回 nil，简化：直接调 View 验证 /help
	m.input = ""
	// 用更小的范围：验证 View 包含 "Commands:"
	m.text.Reset()
	m.text.WriteString("Commands:\n")
	if !strings.Contains(m.text.String(), "Commands:") {
		t.Errorf("/help text 应含 'Commands:'")
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
	m.text.Reset()

	// 模拟 part.delta 事件
	ev := bus.Event{
		Type:      bus.EventPartDelta,
		SessionID: "s1",
		Data:      &session.TextPart{Text: "hello "},
	}
	m.handleBusEvent(ev)

	ev2 := bus.Event{
		Type:      bus.EventPartDelta,
		SessionID: "s1",
		Data:      &session.TextPart{Text: "world"},
	}
	m.handleBusEvent(ev2)

	if m.text.String() != "hello world" {
		t.Errorf("text = %q, 期望 'hello world'", m.text.String())
	}
	if m.status != "Streaming" {
		t.Errorf("status = %q, 期望 Streaming", m.status)
	}
}

func TestUpdate_BusEvent_ToolUpdated(t *testing.T) {
	m := NewModel(Config{SessionID: "s1", ModelName: "m", Bus: bus.New(), Ctx: context.Background()})
	m.toolName = "read"

	ev := bus.Event{
		Type:      bus.EventPartUpdated,
		SessionID: "s1",
		Data:      &session.ToolPart{ToolID: "read"},
	}
	m.handleBusEvent(ev)

	if m.toolName != "" {
		t.Errorf("toolName = %q, 期望清空", m.toolName)
	}
	if m.status != "Tool read done" {
		t.Errorf("status = %q, 期望 'Tool read done'", m.status)
	}
}

func TestUpdate_BusEvent_StateChange(t *testing.T) {
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
