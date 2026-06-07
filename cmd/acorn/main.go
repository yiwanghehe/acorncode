// AcornCode CLI 入口（tracer bullet 版本）
// 用法：acorn
// 跑一个交互式 REPL：用户输入 → Ollama → 调工具 → 输出到 stdout
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"acorncode/internal/agent"
	"acorncode/internal/bus"
	"acorncode/internal/instruction"
	"acorncode/internal/llm"
	"acorncode/internal/permission"
	"acorncode/internal/session"
	"acorncode/internal/tool"
	"acorncode/internal/toolcall"
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

	// 4. Permission broker（v0.1 始终允许）
	broker := permission.NewBroker(nil)

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

	// 8. 启动事件桥接：把 Bus 事件转 stdout
	bridgeDone := make(chan struct{})
	go bridgeBusToStdout(ctx, eventBus, sess.ID, bridgeDone)
	defer func() { <-bridgeDone }()

	// 9. REPL
	fmt.Printf("AcornCode v0.1 (model: %s)\n", modelName)
	fmt.Printf("Session: %s\n", sess.ID)
	fmt.Printf("Tools: read, edit, bash, grep, glob\n")
	fmt.Printf("输入你的消息（'exit' 退出）:\n\n")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		if line == "/session" {
			fmt.Printf("Session: %s\n", sess.ID)
			continue
		}

		turnCtx, turnCancel := context.WithTimeout(ctx, 5*time.Minute)
		err := loop.Run(turnCtx, &session.UserMessage{Text: line})
		turnCancel()

		if err != nil {
			fmt.Fprintf(os.Stderr, "\n[loop error: %v]\n", err)
		}
		fmt.Println()
	}

	return nil
}

// bridgeBusToStdout 把 Bus 事件流输出到 stdout
// 订阅 part.delta 实时输出文本，part.updated 标记工具完成
func bridgeBusToStdout(ctx context.Context, b *bus.Bus, sessionID string, done chan<- struct{}) {
	defer close(done)

	ch, id := b.SubscribeID(bus.EventPartDelta)
	defer b.Unsubscribe(bus.EventPartDelta, id)

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.SessionID != sessionID {
				continue
			}
			// 尝试从 Data 拿 Text
			text := extractText(ev.Data)
			if text != "" {
				fmt.Print(text)
			}
		}
	}
}

// extractText 尝试从 Part 数据里拿 Text 字段
func extractText(data any) string {
	switch v := data.(type) {
	case *session.TextPart:
		return v.Text
	case session.TextPart:
		return v.Text
	case *session.ToolPart:
		// ToolPart 进度用 title 字段
		return ""
	}
	return ""
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
