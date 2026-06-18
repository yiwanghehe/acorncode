// Package tui - Bubble Tea 终端 UI
//
// v0.4 起替代 stdout REPL。订阅 Bus 的 5 类事件：
//   - part.delta      → 当前流式回复文本
//   - part.updated    → 工具开始/完成（状态栏显示 → tool_name）
//   - agent.state.change → Loop 状态切换
//   - error           → 错误显示
//   - permission.asked → 权限弹窗
//
// v1.12：正文区改用 bubbles/viewport 做可滚动视口——对话历史累积不再被覆盖，
// 支持 PgUp/PgDn/↑↓/鼠标滚轮滚动，新内容自动跟随到底部。
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"acorncode/internal/agent"
	"acorncode/internal/bus"
	"acorncode/internal/permission"
	"acorncode/internal/session"
)

// Config 启动 TUI 所需的依赖
type Config struct {
	SessionID string
	ModelName string
	Bus       *bus.Bus
	Loop      *agent.Loop
	Broker    *permission.Broker
	Ctx       context.Context
}

// Model 是 tea.Model 实现
type Model struct {
	cfg Config

	// Bus 订阅（5 个 topic，v1.0.1 加 permission.asked）
	subs subscribes

	// UI 状态
	width  int
	height int
	status string

	// 对话内容（v1.12 滚动式）
	//   - history:  已定格的对话记录（历次问答），追加式累积，不再被覆盖
	//   - stream:   当前正在流式输出的助手文本（part.delta 整体替换）
	//   - viewport: 可滚动视口，正文 = history + stream
	history  strings.Builder
	stream   strings.Builder
	viewport viewport.Model
	vpReady  bool
	// streamPartID 记录当前正在流式渲染的文本 part ID。
	// EventPartDelta 携带的是该 part 的【全量累积文本】（processor 端 currentText.Text += delta），
	// 因此 TUI 必须按 part「整体替换」而非追加，否则会出现 1+2+...+N 的文本雪球堆叠。
	streamPartID string

	input    string
	inputOn  bool
	quitting bool

	// Permission dialog 状态（v1.0.1）
	permReq    *permRequest // nil = 不在 dialog
	permChoice int          // 0=Allow, 1=Always, 2=Deny

	// 样式
	statusStyle lipgloss.Style
	toolStyle   lipgloss.Style
	textStyle   lipgloss.Style
	inputStyle  lipgloss.Style
	permStyle   lipgloss.Style
	permHotKey  lipgloss.Style
}

// permRequest 是 in-flight permission 弹窗
type permRequest struct {
	reqID    string
	tool     string
	pattern  string
	metadata map[string]any
}

// subscribes 记录 5 个 topic 的 channel + id
type subscribes struct {
	partDelta     <-chan bus.Event
	partUpdated   <-chan bus.Event
	stateChange   <-chan bus.Event
	errorEvent    <-chan bus.Event
	permissionAsk <-chan bus.Event
}

// NewModel 构造 Model
func NewModel(cfg Config) *Model {
	return &Model{
		cfg:     cfg,
		status:  "Idle",
		inputOn: true,
		statusStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Bold(true),
		toolStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("yellow")),
		textStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("white")),
		inputStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("cyan")),
		permStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("magenta")).
			Bold(true),
		permHotKey: lipgloss.NewStyle().
			Foreground(lipgloss.Color("magenta")).
			Underline(true),
	}
}

// Init 实现 tea.Model。订阅 bus 5 个 topic
func (m *Model) Init() tea.Cmd {
	m.subs.partDelta, _ = m.cfg.Bus.SubscribeID(bus.EventPartDelta)
	m.subs.partUpdated, _ = m.cfg.Bus.SubscribeID(bus.EventPartUpdated)
	m.subs.stateChange, _ = m.cfg.Bus.SubscribeID(bus.EventAgentStateChange)
	m.subs.errorEvent, _ = m.cfg.Bus.SubscribeID(bus.EventError)
	m.subs.permissionAsk, _ = m.cfg.Bus.SubscribeID(bus.EventPermissionAsked)

	// 启动 5 个 listener
	return tea.Batch(
		listenCmd(m.cfg.Ctx, m.subs.partDelta),
		listenCmd(m.cfg.Ctx, m.subs.partUpdated),
		listenCmd(m.cfg.Ctx, m.subs.stateChange),
		listenCmd(m.cfg.Ctx, m.subs.errorEvent),
		listenCmd(m.cfg.Ctx, m.subs.permissionAsk),
	)
}

// Update 实现 tea.Model
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// 正文视口高度 = 总高 - 状态栏(1) - 上分隔(1) - 下分隔(1) - 输入行(1) = -4
		vpHeight := msg.Height - 4
		if vpHeight < 1 {
			vpHeight = 1
		}
		if !m.vpReady {
			m.viewport = viewport.New(msg.Width, vpHeight)
			m.vpReady = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = vpHeight
		}
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		// Permission 弹窗模式：所有键都进 dialog（除非 ctrl+c）
		if m.permReq != nil {
			if msg.String() == "ctrl+c" {
				m.quitting = true
				return m, tea.Quit
			}
			return m, m.handlePermKey(msg.String())
		}

		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		// 滚动键：转发给 viewport（无论 inputOn 与否都允许翻看历史）
		switch msg.String() {
		case "pgup", "pgdown", "ctrl+u", "ctrl+d", "home", "end":
			return m, m.forwardToViewport(msg)
		}
		if !m.inputOn {
			// 忙碌时方向键也用于滚动历史
			if msg.String() == "up" || msg.String() == "down" {
				return m, m.forwardToViewport(msg)
			}
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
				m.history.Reset()
				m.stream.Reset()
				m.streamPartID = ""
				m.refreshViewport()
				m.setStatus("Idle")
				return m, nil
			}
			if text == "/session" {
				m.setStatus(fmt.Sprintf("Session: %s", m.cfg.SessionID))
				return m, nil
			}
			if text == "/help" {
				m.appendHistory("Commands:\n" +
					"  /exit  /quit  退出\n" +
					"  /clear        清屏\n" +
					"  /session      显示 session ID\n" +
					"  /help         本帮助\n")
				return m, nil
			}
			// 把用户提问定格进对话历史，再发到 loop
			m.appendHistory(m.inputStyle.Render("> "+text) + "\n")
			m.inputOn = false
			m.setStatus("Building")
			return m, m.runLoop(text)
		case "backspace":
			// 按 rune 删除最后一个字符（中文 1 字符占多字节，不能按字节切，
			// 否则会把多字节字符切碎成乱码）。
			if len(m.input) > 0 {
				rs := []rune(m.input)
				m.input = string(rs[:len(rs)-1])
			}
			return m, nil
		default:
			// 文本输入（含中文等多字节字符）走 KeyRunes/KeySpace。
			// 旧实现用 len(msg.String())==1 按【字节】判断，中文一个字符占 3 字节
			// 永远不等于 1 而被丢弃，导致无法输入中文。改为按 Runes 追加。
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				m.input += string(msg.Runes)
			}
			return m, nil
		}

	case tea.MouseMsg:
		// 鼠标滚轮滚动正文
		return m, m.forwardToViewport(msg)

	case busEventMsg:
		ev := bus.Event(msg)
		m.handleBusEvent(ev)
		// 重启 listener（每个 channel 单独 re-issue）
		return m, tea.Batch(
			listenCmd(m.cfg.Ctx, m.subs.partDelta),
			listenCmd(m.cfg.Ctx, m.subs.partUpdated),
			listenCmd(m.cfg.Ctx, m.subs.stateChange),
			listenCmd(m.cfg.Ctx, m.subs.errorEvent),
			listenCmd(m.cfg.Ctx, m.subs.permissionAsk),
		)

	case loopDoneMsg:
		// 一轮结束：把当前流式回复定格进历史，清空流式缓冲。
		m.flushStreamToHistory()
		if msg.Err != nil {
			m.setStatus(fmt.Sprintf("Error: %v", msg.Err))
		} else {
			m.setStatus("Idle")
		}
		m.inputOn = true
		return m, nil
	}

	return m, nil
}

// View 渲染 UI
func (m *Model) View() string {
	if m.quitting {
		return "Bye.\n"
	}

	// Permission 弹窗优先显示
	if m.permReq != nil {
		return m.renderPermissionDialog()
	}

	divider := strings.Repeat("─", max(m.width, 20))

	var sb strings.Builder
	// 状态栏
	sb.WriteString(m.statusStyle.Render(fmt.Sprintf("[%s] %s", m.cfg.ModelName, m.status)))
	sb.WriteString("\n")
	sb.WriteString(divider)
	sb.WriteString("\n")

	// 文本区（可滚动视口）
	if m.vpReady {
		sb.WriteString(m.viewport.View())
	} else {
		// 尚未收到 WindowSizeMsg：退化为直接渲染正文
		sb.WriteString(m.textStyle.Render(m.bodyContent()))
	}
	sb.WriteString("\n")
	sb.WriteString(divider)
	sb.WriteString("\n")

	// Input
	if m.inputOn {
		sb.WriteString(m.inputStyle.Render("> " + m.input))
	} else {
		sb.WriteString(m.statusStyle.Render("(busy · ↑↓/PgUp/PgDn 滚动历史, Ctrl+C 中止)"))
	}

	return sb.String()
}

// bodyContent 返回正文完整内容（对话历史 + 当前流式回复）。
func (m *Model) bodyContent() string {
	body := m.history.String() + m.stream.String()
	if body == "" {
		if m.inputOn {
			return "(waiting for input...)"
		}
		return "(thinking...)"
	}
	return body
}

// refreshViewport 把最新正文写进 viewport 并跟随到底部（若用户未手动上滚）。
func (m *Model) refreshViewport() {
	if !m.vpReady {
		return
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(m.bodyContent())
	// 新内容到达时，仅当用户原本就在底部才自动跟随，避免打断手动翻看历史。
	if atBottom {
		m.viewport.GotoBottom()
	}
}

// appendHistory 把一段文本定格进对话历史并刷新视口。
func (m *Model) appendHistory(s string) {
	m.history.WriteString(s)
	m.refreshViewport()
}

// flushStreamToHistory 把当前流式回复定格进历史，清空流式缓冲。
func (m *Model) flushStreamToHistory() {
	if m.stream.Len() > 0 {
		m.history.WriteString(m.stream.String())
		if !strings.HasSuffix(m.stream.String(), "\n") {
			m.history.WriteString("\n")
		}
	}
	m.stream.Reset()
	m.streamPartID = ""
	m.refreshViewport()
}

// forwardToViewport 把滚动类消息转发给 viewport 并返回其 cmd。
func (m *Model) forwardToViewport(msg tea.Msg) tea.Cmd {
	if !m.vpReady {
		return nil
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return cmd
}

// renderPermissionDialog 渲染权限弹窗
func (m *Model) renderPermissionDialog() string {
	var sb strings.Builder
	sb.WriteString(m.permStyle.Render("⚠ Permission needed"))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", max(m.width, 20)))
	sb.WriteString("\n\n")

	// 显示工具 + pattern
	sb.WriteString(m.textStyle.Render(fmt.Sprintf("Tool:    %s\n", m.permReq.tool)))
	if m.permReq.pattern != "" {
		sb.WriteString(m.textStyle.Render(fmt.Sprintf("Pattern: %s\n", m.permReq.pattern)))
	}
	if m.permReq.metadata != nil {
		for k, v := range m.permReq.metadata {
			sb.WriteString(m.textStyle.Render(fmt.Sprintf("%s: %v\n", k, v)))
		}
	}
	sb.WriteString("\n")

	// 3 个选项
	options := []string{"Allow", "Always", "Deny"}
	keys := []string{"1", "2", "3"}
	decisions := []string{"(once)", "(session)", ""}

	for i, opt := range options {
		var style lipgloss.Style
		if i == m.permChoice {
			style = m.permHotKey
		} else {
			style = m.statusStyle
		}
		sb.WriteString(style.Render(fmt.Sprintf("  [%s] %s %s", keys[i], opt, decisions[i])))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(m.statusStyle.Render("←/→ 选择，Enter 确认，Esc 拒绝"))
	sb.WriteString("\n")

	return sb.String()
}

// ========== 自定义 msg + cmd ==========

// busEventMsg 把 bus.Event 包成 tea.Msg
type busEventMsg bus.Event

// listenCmd 监听 bus channel → tea.Msg
func listenCmd(ctx context.Context, ch <-chan bus.Event) tea.Cmd {
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

// runLoop 异步跑 loop
func (m *Model) runLoop(userText string) tea.Cmd {
	return func() tea.Msg {
		err := m.cfg.Loop.Run(m.cfg.Ctx, &session.UserMessage{Text: userText})
		return loopDoneMsg{Err: err}
	}
}

// handleBusEvent 处理 4 类 bus 事件
func (m *Model) handleBusEvent(ev bus.Event) {
	switch ev.Type {
	case bus.EventPartDelta:
		// EventPartDelta 的 Data 携带【全量累积文本】（非增量），因此用全量
		// 文本整体替换当前流式段，而不是追加——追加会导致文本雪球堆叠。
		var partID, fullText string
		if tp, ok := ev.Data.(*session.TextPart); ok {
			partID, fullText = tp.ID, tp.Text
		} else if tp, ok := ev.Data.(session.TextPart); ok {
			partID, fullText = tp.ID, tp.Text
		} else {
			break
		}
		// 同一 part 的后续 delta 用全量文本整体替换当前流式段。
		m.stream.Reset()
		m.streamPartID = partID
		m.stream.WriteString(fullText)
		m.refreshViewport()
		m.setStatus("Streaming")

	case bus.EventPartUpdated:
		// 工具 part 状态变化（start / complete / error / reject）
		if tp, ok := ev.Data.(*session.ToolPart); ok {
			switch tp.State {
			case session.ToolPending:
				m.setStatus(fmt.Sprintf("→ %s", tp.ToolID))
			case session.ToolRunning:
				m.setStatus(fmt.Sprintf("Running %s", tp.ToolID))
			case session.ToolComplete:
				m.setStatus(fmt.Sprintf("✓ %s done", tp.ToolID))
			case session.ToolErrored:
				m.setStatus(fmt.Sprintf("✗ %s error: %s", tp.ToolID, tp.Error))
			case session.ToolRejected:
				m.setStatus(fmt.Sprintf("⊘ %s rejected: %s", tp.ToolID, tp.Error))
			}
		}

	case bus.EventAgentStateChange:
		// data 可能是 string（直接状态名）或 map（{from, to, event, ...}）
		switch d := ev.Data.(type) {
		case string:
			m.setStatus(d)
		case map[string]any:
			if to, ok := d["to"].(string); ok {
				m.setStatus(to)
			} else if event, ok := d["event"].(string); ok {
				m.setStatus(event)
			}
		}

	case bus.EventError:
		// data 通常是 map{err, fatal}
		if data, ok := ev.Data.(map[string]any); ok {
			if err, ok := data["err"].(string); ok {
				fatal, _ := data["fatal"].(bool)
				if fatal {
					m.setStatus(fmt.Sprintf("FATAL: %s", err))
				} else {
					m.setStatus(fmt.Sprintf("Error: %s", err))
				}
				return
			}
		}
		m.setStatus(fmt.Sprintf("Error: %v", ev.Data))

	case "permission.asked":
		// v1.0.1：弹窗
		if data, ok := ev.Data.(map[string]any); ok {
			m.permReq = &permRequest{
				reqID:    data["req_id"].(string),
				tool:     data["tool"].(string),
				pattern:  firstPattern(data),
				metadata: metaOf(data),
			}
			m.permChoice = 0
			m.setStatus("Permission needed")
		}
	}
}

// firstPattern 从 permission.asked data 拿第一个 pattern
func firstPattern(data map[string]any) string {
	ps, ok := data["patterns"].([]string)
	if !ok || len(ps) == 0 {
		return ""
	}
	return ps[0]
}

// metaOf 从 data 拿 metadata（best effort）
func metaOf(data map[string]any) map[string]any {
	if m, ok := data["metadata"].(map[string]any); ok {
		return m
	}
	return nil
}

// handlePermKey 处理 permission 弹窗的键盘
//
// 1 / a / → / Enter → Allow
// 2 / s / Always → 标记 session 级 + Allow
// 3 / d / Esc → Deny
func (m *Model) handlePermKey(key string) tea.Cmd {
	if m.permReq == nil {
		return nil
	}
	switch key {
	case "1", "a", "A", "enter":
		// Allow once
		m.cfg.Broker.Reply(m.permReq.reqID, "allow", "")
		m.permReq = nil
		m.setStatus("Allowed")
		return nil
	case "2", "s", "S":
		// Always (session-level)
		m.cfg.Broker.SessionApprove(m.permReq.tool, m.permReq.pattern)
		m.cfg.Broker.Reply(m.permReq.reqID, "session_allow", "")
		m.permReq = nil
		m.setStatus("Allowed (session)")
		return nil
	case "3", "d", "D", "esc":
		m.cfg.Broker.Reply(m.permReq.reqID, "deny", "user denied")
		m.permReq = nil
		m.setStatus("Denied")
		return nil
	case "left":
		// 循环选项
		m.permChoice = (m.permChoice + 2) % 3
	case "tab", "right":
		m.permChoice = (m.permChoice + 1) % 3
	}
	return nil
}

func (m *Model) setStatus(s string) {
	m.status = s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
