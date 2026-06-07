// AcornCode CLI 入口
// 用法：acorn [model_name]
//
//	跑 Bubble Tea TUI：用户输入 → Ollama → 调工具 → 流式渲染
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"acorncode/internal/agent"
	"acorncode/internal/bus"
	"acorncode/internal/compaction"
	"acorncode/internal/instruction"
	"acorncode/internal/llm"
	"acorncode/internal/permission"
	"acorncode/internal/server"
	"acorncode/internal/session"
	"acorncode/internal/tool"
	"acorncode/internal/toolcall"
	"acorncode/internal/tui"
)

func main() {
	modelName, dbPath, providerName, serverAddr, toolcallStrat := parseArgs(os.Args[1:])

	if err := run(modelName, dbPath, providerName, serverAddr, toolcallStrat); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

// parseArgs 解析 CLI 参数
//
//	--db=path              SQLite db 路径（默认 .acorncode.db）
//	--provider=NAME        provider 名（ollama | anthropic，默认 ollama）
//	--server=ADDR          启 HTTP server（v1.0.4；如 ":8080"），默认不起
//	--toolcall=NAME        toolcall 策略（native | prompted，默认 native）
//	[model]                模型名（默认 qwen2.5-coder:7b）
func parseArgs(args []string) (modelName, dbPath, providerName, serverAddr, toolcallStrat string) {
	modelName = "qwen2.5-coder:7b"
	dbPath = ".acorncode.db"
	providerName = "ollama"
	toolcallStrat = "native"
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case strings.HasPrefix(arg, "--provider="):
			providerName = strings.TrimPrefix(arg, "--provider=")
		case strings.HasPrefix(arg, "--server="):
			serverAddr = strings.TrimPrefix(arg, "--server=")
		case strings.HasPrefix(arg, "--toolcall="):
			toolcallStrat = strings.TrimPrefix(arg, "--toolcall=")
		case !strings.HasPrefix(arg, "-"):
			modelName = arg
		}
	}
	return
}

func run(modelName, dbPath, providerName, serverAddr, toolcallStrat string) error {
	// 0. TTY 检测（v0.6 整合）：无 TTY 时 TUI 起不来，给清晰错误
	// 例外：--server 模式不需要 TTY
	if serverAddr == "" && !isTTY() {
		return fmt.Errorf("acorn 需要交互式 TTY（stdin 必须是 terminal）。\n" +
			"提示：直接跑 `./acorn`；CI 场景用 `--server=:8080` 起 HTTP API")
	}

	// 1. 初始化基础设施
	eventBus := bus.New()
	defer eventBus.Close()

	// SQLite 持久化（v0.5）
	dbAbs, _ := filepath.Abs(dbPath)
	store, err := session.NewSQLiteStore(dbAbs)
	if err != nil {
		return fmt.Errorf("open db %s: %w", dbAbs, err)
	}
	defer store.Close()
	fmt.Fprintf(os.Stderr, "[已加载 SQLite: %s]\n", dbAbs)

	loader := instruction.NewLoader(".")

	// 2. LLM provider（v1.0.2 起支持 ollama + anthropic）
	var provider llm.Provider
	var strategy toolcall.Strategy
	providerID := "ollama"
	switch providerName {
	case "ollama", "":
		provider = llm.NewOllama(llm.OllamaConfig{
			Endpoint: envOr("OLLAMA_ENDPOINT", "http://localhost:11434"),
			Model:    modelName,
			Timeout:  5 * time.Minute,
		})
	case "anthropic":
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("anthropic provider 需 ANTHROPIC_API_KEY 环境变量")
		}
		provider = llm.NewAnthropic(llm.AnthropicConfig{
			APIKey: apiKey,
			Model:  modelName,
		})
		providerID = "anthropic"
	default:
		return fmt.Errorf("未知 provider: %s（支持 ollama / anthropic）", providerName)
	}

	// toolcall 策略（v1.0.5 起：native / prompted；v1.0.6：grammar）
	switch toolcallStrat {
	case "native", "":
		strategy = toolcall.NewNative()
	case "prompted":
		strategy = toolcall.NewPrompted()
	case "grammar":
		strategy = toolcall.NewGrammar()
	default:
		return fmt.Errorf("未知 toolcall 策略: %s（支持 native / prompted / grammar）", toolcallStrat)
	}

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
	broker.SetPublisher(&permissionBusAdapter{bus: eventBus})

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
		Model:        llm.Model{ID: modelName, ProviderID: providerID},
		MaxTurns:     20,
		MaxTokens:    32000,
		MaxBashFails: 5,
		MaxToolRetry: 3,
		MaxSameError: 3,
		MaxTools:     10,
	}
	loop := agent.NewLoop(sess.ID, loopCfg, store, eventBus, provider, strategy, tools, broker, loader)

	// 7.5 注入 compactor（v1.0.3）：用同一 provider 摘要老消息
	compactor := &compaction.SimpleCompactor{
		Provider:   provider,
		Model:      loopCfg.Model,
		KeepRecent: 6, // 保留最近 6 条不压
		MaxSummary: 500,
	}
	loop.SetCompactor(compactor)
	fmt.Fprintf(os.Stderr, "[已启用 compactor: keep_recent=6]\n")

	// 8. 启动 Bubble Tea TUI
	model := tui.NewModel(tui.Config{
		SessionID: sess.ID,
		ModelName: modelName,
		Bus:       eventBus,
		Loop:      loop,
		Broker:    broker,
		Ctx:       ctx,
	})

	// 9. 启 TUI 或 HTTP server
	if serverAddr != "" {
		// server 模式：Broker 不设 publisher → ask fallback allow
		fmt.Fprintf(os.Stderr, "[启动 HTTP server: %s]\n", serverAddr)
		return startServer(serverAddr, provider, strategy, store, tools, broker, loader, llm.Model{ID: modelName, ProviderID: providerID})
	}

	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = prog.Run()
	return err
}

// startServer 启动 HTTP server（v1.0.4）
func startServer(addr string, provider llm.Provider, strategy toolcall.Strategy, store *session.SQLiteStore, tools *tool.Registry, broker *permission.Broker, loader *instruction.Loader, model llm.Model) error {
	srv := server.New(server.Config{
		Addr:     addr,
		Provider: provider,
		Strategy: strategy,
		Store:    store,
		Tools:    tools,
		Broker:   broker,
		Loader:   loader,
		Model:    model,
	})
	return srv.Start()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// isTTY 检查 stdin 是否是 TTY
func isTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// permissionBusAdapter 把 bus.Bus 适配成 permission.Publisher（避免 import 循环）
type permissionBusAdapter struct {
	bus *bus.Bus
}

func (a *permissionBusAdapter) Publish(ev permission.Event) {
	a.bus.Publish(bus.Event{
		Type:      ev.Type,
		SessionID: ev.SessionID,
		Data:      ev.Data,
	})
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
