// Package tui - Bubble Tea 终端 UI
//
// v0.4 简化版：scrollback 显示当前 turn 文本 + input box + status bar。
// 详细设计见 docs/architecture.md §11.2（v0.4 实现 §11.2 简化子集）。
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"acorncode/internal/agent"
	"acorncode/internal/bus"
	"acorncode/internal/session"
)

// Config 启动 TUI 所需的依赖
type Config struct {
	SessionID string
	ModelName string
	Bus       *bus.Bus
	Loop      *agent.Loop
	Ctx       context.Context
}

// Model 是 tea.Model 实现
type Model struct {
	cfg Config

	// Bus 订阅
	busSubID int
	busCh    <-chan bus.Event

	// UI 状态
	width    int
	height   int
	status   string // 顶部状态栏
	text     strings.Builder // 当前 turn 累积文本
	toolName string // 正在执行的 tool（"read" / "edit" 等）
	input    string // input box 当前内容
	inputOn  bool   // false = 禁用（loop 忙）
	quitting bool

	// 样式
	statusStyle lipgloss.Style
	toolStyle   lipgloss.Style
	textStyle   lipgloss.Style
	inputStyle  lipgloss.Style
}

// NewModel 构造 Model
func NewModel(cfg Config) *Model {
	return &Model{
		cfg:       cfg,
		busCh:     nil, // Init() 里订阅
		status:    "Idle",
		inputOn:   true,
		statusStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Bold(true),
		toolStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("yellow")),
		textStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("white")),
		inputStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("cyan")),
	}
}

// Init 实现 tea.Model。订阅 bus
func (m *Model) Init() tea.Cmd {
	// 订阅 part.delta + part.updated + agent.state.change + error
	ch, id := m.cfg.Bus.SubscribeID(bus.EventPartDelta)
	m.busCh = ch
	m.busSubID = id

	// 也订阅 state change + error
	// 简化：一个订阅者订阅多 topic（bus 支持）
	// 实际 bus.SubscribeID 是按 topic 单独订阅，v0.4 简化：只监听 part.delta
	// state change 通过 WaitMsg 的方式轮询（v0.4 简化：忽略 state，只在 part.delta 更新文本）

	return tea.Batch(
		textMsg(m.cfg.Ctx, m.busCh), // 启动订阅监听
	)
}

// Update 实现 tea.Model
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// 全局快捷键
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if !m.inputOn {
			// 循环忙时只接受 ctrl+c
			return m, nil
		}
		switch msg.String() {
		case "esc":
			m.quitting = true
			return m, tea.Quit
		case "enter":
			text := strings.TrimSpace(m.input)
			if text == "" {
				return m, nil
			}
			m.input = ""
			if text == "/exit" || text == "/quit" {
				m.quitting = true
				return m, tea.Quit
			}
			if text == "/clear" {
				m.text.Reset()
				m.status = "Idle"
				return m, nil
			}
			if text == "/session" {
				m.setStatus(fmt.Sprintf("Session: %s", m.cfg.SessionID))
				return m, nil
			}
			if text == "/help" {
				m.text.Reset()
				m.text.WriteString("Commands:\n")
				m.text.WriteString("  /exit  /quit  退出\n")
				m.text.WriteString("  /clear        清屏\n")
				m.text.WriteString("  /session      显示 session ID\n")
				m.text.WriteString("  /help         本帮助\n")
				return m, nil
			}
			// 发到 loop
			m.inputOn = false
			m.setStatus("Building")
			m.text.Reset()
			return m, m.runLoop(text)
		case "backspace":
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
			return m, nil
		default:
			// 普通字符
			if len(msg.String()) == 1 {
				m.input += msg.String()
			}
			return m, nil
		}

	case busEventMsg:
		// Bus 事件到达
		ev := bus.Event(msg)
		m.handleBusEvent(ev)
		// 继续订阅
		return m, textMsg(m.cfg.Ctx, m.busCh)

	case loopDoneMsg:
		// Loop 完成（成功或失败）
		if msg.Err != nil {
			m.setStatus(fmt.Sprintf("Error: %v", msg.Err))
		} else {
			m.setStatus("Idle")
		}
		m.inputOn = true
		m.toolName = ""
		return m, nil
	}

	return m, nil
}

// View 渲染 UI
func (m *Model) View() string {
	if m.quitting {
		return "Bye.\n"
	}

	var sb strings.Builder
	// 状态栏
	status := m.statusStyle.Render(fmt.Sprintf("[%s] %s", m.cfg.ModelName, m.status))
	if m.toolName != "" {
		status += " " + m.toolStyle.Render("→ "+m.toolName)
	}
	sb.WriteString(status)
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", max(m.width, 20)))
	sb.WriteString("\n")

	// 文本区
	body := m.text.String()
	if body == "" {
		body = "(waiting for response...)"
	}
	sb.WriteString(m.textStyle.Render(body))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", max(m.width, 20)))
	sb.WriteString("\n")

	// Input
	if m.inputOn {
		sb.WriteString(m.inputStyle.Render("> " + m.input))
	} else {
		sb.WriteString(m.statusStyle.Render("(busy, press Ctrl+C to abort)"))
	}
	sb.WriteString("\n")

	return sb.String()
}

// ========== 自定义 msg + cmd ==========

// busEventMsg 把 bus.Event 包成 tea.Msg
type busEventMsg bus.Event

// textMsg 监听 bus channel 的 Cmd
func textMsg(ctx context.Context, ch <-chan bus.Event) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			return busEventMsg(ev)
		}
	}
}

// loopDoneMsg 表示 loop 跑完
type loopDoneMsg struct {
	Err error
}

// runLoop 异步跑 loop，跑完返 loopDoneMsg
func (m *Model) runLoop(userText string) tea.Cmd {
	return func() tea.Msg {
		err := m.cfg.Loop.Run(m.cfg.Ctx, &session.UserMessage{Text: userText})
		return loopDoneMsg{Err: err}
	}
}

// handleBusEvent 处理 bus 事件
func (m *Model) handleBusEvent(ev bus.Event) {
	switch ev.Type {
	case bus.EventPartDelta:
		// 文本增量
		if tp, ok := ev.Data.(*session.TextPart); ok {
			m.text.WriteString(tp.Text)
		} else if tp, ok := ev.Data.(session.TextPart); ok {
			m.text.WriteString(tp.Text)
		}
		m.setStatus("Streaming")
	case bus.EventPartUpdated:
		// 工具完成
		if tp, ok := ev.Data.(*session.ToolPart); ok {
			m.setStatus(fmt.Sprintf("Tool %s done", tp.ToolID))
			m.toolName = ""
		}
	case bus.EventAgentStateChange:
		// 状态变化
		if s, ok := ev.Data.(string); ok {
			m.setStatus(s)
		}
	case bus.EventError:
		m.setStatus(fmt.Sprintf("Error: %v", ev.Data))
	}
}

// setStatus 简洁包装
func (m *Model) setStatus(s string) {
	m.status = s
}

// max 避免内建冲突
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
