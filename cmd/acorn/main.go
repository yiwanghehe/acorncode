// AcornCode CLI 入口
// 用法：acorn [model_name]
//   跑 Bubble Tea TUI：用户输入 → Ollama → 调工具 → 流式渲染
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"acorncode/internal/agent"
	"acorncode/internal/bus"
	"acorncode/internal/instruction"
	"acorncode/internal/llm"
	"acorncode/internal/permission"
	"acorncode/internal/session"
	"acorncode/internal/tool"
	"acorncode/internal/toolcall"
	"acorncode/internal/tui"
)

func main() {
	// 解析 args（v0.1 简单版：第一个非 flag 参数是 model name）
	modelName := "qwen2.5-coder:7b"
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		modelName = os.Args[1]
	}

	if err := run(modelName); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(modelName string) error {
	// 1. 初始化基础设施
	eventBus := bus.New()
	defer eventBus.Close()

	store := session.NewMemoryStore()
	loader := instruction.NewLoader(".")

	// 2. LLM provider
	provider := llm.NewOllama(llm.OllamaConfig{
		Endpoint: envOr("OLLAMA_ENDPOINT", "http://localhost:11434"),
		Model:    modelName,
		Timeout:  5 * time.Minute,
	})
	strategy := toolcall.NewNative()

	// 3. Tool registry
	cwd, _ := os.Getwd()
	tools := tool.NewRegistry()
	tools.RegisterRead(cwd)
	tools.RegisterEdit(cwd)
	tools.RegisterBash(cwd)
	tools.RegisterGrep(cwd)
	tools.RegisterGlob(cwd)
	tools.RegisterWebFetch()

	// 4. Permission broker（v0.3 加载 acorncode.json 规则）
	broker := permission.NewBroker(nil)
	permCfg, permErr := permission.LoadConfig("acorncode.json")
	if permErr != nil {
		fmt.Fprintf(os.Stderr, "[警告: acorncode.json 解析失败: %v]\n", permErr)
	} else if permCfg != nil {
		broker.AddRules(permCfg.Permissions.Rules)
		fmt.Fprintf(os.Stderr, "[已加载 acorncode.json: %d 规则]\n", len(permCfg.Permissions.Rules))
	}

	// 5. 加载 AGENTS.md（v0.1 容错：找不到不报错）
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	instrContent, _ := loader.Load(ctx)
	if instrContent != "" {
		fmt.Fprintf(os.Stderr, "[已加载 AGENTS.md: %d 字节]\n", len(instrContent))
	}

	// 6. 创建 session
	sess := &session.Session{
		ID:        "sess_" + newShortID(),
		Title:     "interactive",
		Directory: cwd,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateSession(ctx, sess); err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	// 7. 创建 loop
	loopCfg := agent.LoopConfig{
		AgentName:    "build",
		Model:        llm.Model{ID: modelName, ProviderID: "ollama"},
		MaxTurns:     20,
		MaxTokens:    32000,
		MaxBashFails: 5,
		MaxToolRetry: 3,
		MaxSameError: 3,
		MaxTools:     10,
	}
	loop := agent.NewLoop(sess.ID, loopCfg, store, eventBus, provider, strategy, tools, broker, loader)

	// 8. 启动 Bubble Tea TUI
	model := tui.NewModel(tui.Config{
		SessionID: sess.ID,
		ModelName: modelName,
		Bus:       eventBus,
		Loop:      loop,
		Ctx:       ctx,
	})

	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := prog.Run()
	return err
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// newShortID 生成 8 字符短 ID
func newShortID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	now := time.Now().UnixNano()
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[now%int64(len(chars))]
		now /= int64(len(chars))
		if now == 0 {
			now = time.Now().UnixNano()
		}
	}
	return string(b)
}
