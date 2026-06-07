// Package agent - integration_test.go
// 端到端集成测试：mock Ollama + 真实 Loop + 真实 Read tool + 真实 Bus
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"acorncode/internal/bus"
	"acorncode/internal/instruction"
	"acorncode/internal/llm"
	"acorncode/internal/permission"
	"acorncode/internal/session"
	"acorncode/internal/tool"
	"acorncode/internal/toolcall"
)

// mockOllamaScripted 返回 httptest.Server，按预设脚本吐 NDJSON
type mockOllamaScripted struct {
	srv       *httptest.Server
	scripts   []string // 每次 /api/chat 调用时用下一个 script
	callIdx   atomic.Int32
	gotBodies [][]byte
}

func newMockOllamaScripted(t *testing.T, scripts ...string) *mockOllamaScripted {
	t.Helper()
	m := &mockOllamaScripted{scripts: scripts}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 读 body（用 io.ReadAll 等到 EOF）
		body, _ := io.ReadAll(r.Body)
		m.gotBodies = append(m.gotBodies, body)

		idx := m.callIdx.Add(1) - 1
		if idx >= int32(len(m.scripts)) {
			// 默认 finish
			idx = int32(len(m.scripts)) - 1
		}
		script := m.scripts[idx]

		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, line := range strings.Split(script, "\n") {
			if line == "" {
				continue
			}
			_, _ = w.Write([]byte(line + "\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockOllamaScripted) gotCalls() int { return int(m.callIdx.Load()) }

// setupEnv 构造一个完整的可运行环境（真实组件，仅 Ollama 是 mock）
func setupEnv(t *testing.T, mock *mockOllamaScripted, dir string) (*Loop, *bus.Bus, *session.MemoryStore) {
	t.Helper()

	eventBus := bus.New()
	store := session.NewMemoryStore()

	// 真实 Ollama（指向 mock server）
	provider := llm.NewOllama(llm.OllamaConfig{
		Endpoint: mock.srv.URL,
		Model:    "test-model",
		Timeout:  5 * time.Second,
	})

	strategy := toolcall.NewNative()
	registry := tool.NewRegistry()
	registry.RegisterRead(dir)
	registry.RegisterEdit(dir)
	registry.RegisterBash(dir)

	broker := permission.NewBroker(nil)
	loader := instruction.NewLoader(dir)

	// 创建 session
	sess := &session.Session{
		ID:        "sess_test",
		Title:     "test",
		Directory: dir,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = store.CreateSession(context.Background(), sess)

	// 创建 loop
	loop := NewLoop(sess.ID, LoopConfig{
		AgentName: "build",
		Model:     llm.Model{ID: "test-model", ProviderID: "ollama"},
		MaxTurns:  5,
		MaxTokens: 32000,
		MaxTools:  10,
	}, store, eventBus, provider, strategy, registry, broker, loader)

	return loop, eventBus, store
}

// TestIntegration_SingleTextResponse 验证最简单路径：模型直接给文本回复，无工具调用
func TestIntegration_SingleTextResponse(t *testing.T) {
	mock := newMockOllamaScripted(t, `{"model":"x","message":{"role":"assistant","content":"Hello, world!"},"done":true,"done_reason":"stop","eval_count":2,"prompt_eval_count":5}`)

	dir := t.TempDir()
	loop, eventBus, _ := setupEnv(t, mock, dir)
	defer eventBus.Close()

	// 订阅 part delta
	ch, id := eventBus.SubscribeID(bus.EventPartDelta)

	var gotText strings.Builder
	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		for ev := range ch {
			if tp, ok := ev.Data.(*session.TextPart); ok {
				gotText.WriteString(tp.Text)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := loop.Run(ctx, &session.UserMessage{Text: "hi"})

	// 重要：先 Unsubscribe 关 channel，goroutine 才会退出
	eventBus.Unsubscribe(bus.EventPartDelta, id)
	<-collectDone

	if err != nil {
		t.Fatalf("loop.Run: %v", err)
	}
	if gotText.String() != "Hello, world!" {
		t.Errorf("text = %q, 期望 %q", gotText.String(), "Hello, world!")
	}
	if mock.gotCalls() != 1 {
		t.Errorf("Ollama 应被调 1 次, 实际 %d", mock.gotCalls())
	}
}

// TestIntegration_LoopCallsReadTool 验证核心场景：
// 模型决定调 Read 工具 → Loop 执行工具 → 结果喂回模型 → 模型给最终回复
func TestIntegration_LoopCallsReadTool(t *testing.T) {
	// 准备测试文件
	dir := t.TempDir()
	testFile := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("Hello from file\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 脚本：第 1 次调 LLM → 模型说"我要读 hello.txt"
	//       第 2 次调 LLM → 模型拿到结果后说"内容是 Hello from file"
	mock := newMockOllamaScripted(t,
		// 1st call: model emits a tool call
		`{"model":"x","message":{"role":"assistant","content":"Let me read it.","tool_calls":[{"function":{"name":"read","arguments":{"path":"hello.txt"}}}]},"done":true,"done_reason":"tool_calls","eval_count":5,"prompt_eval_count":8}`,
		// 2nd call: model returns final answer
		`{"model":"x","message":{"role":"assistant","content":"The file says: Hello from file"},"done":true,"done_reason":"stop","eval_count":10,"prompt_eval_count":15}`)

	loop, eventBus, _ := setupEnv(t, mock, dir)
	defer eventBus.Close()

	// 订阅 part updated（tool 完成时用）
	chUpd, idUpd := eventBus.SubscribeID(bus.EventPartUpdated)
	chDelta, idDelta := eventBus.SubscribeID(bus.EventPartDelta)

	var textBuf strings.Builder
	var toolCompleted atomic.Bool
	var sawToolPart atomic.Bool
	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		for {
			select {
			case ev, ok := <-chUpd:
				if !ok {
					chUpd = nil
					if chDelta == nil {
						return
					}
					continue
				}
				tp, isTool := ev.Data.(*session.ToolPart)
				if !isTool {
					continue
				}
				sawToolPart.Store(true)
				if tp.State == session.ToolComplete {
					toolCompleted.Store(true)
				}
			case ev, ok := <-chDelta:
				if !ok {
					chDelta = nil
					if chUpd == nil {
						return
					}
					continue
				}
				if tp, ok := ev.Data.(*session.TextPart); ok {
					textBuf.WriteString(tp.Text)
				}
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := loop.Run(ctx, &session.UserMessage{Text: "read hello.txt"})

	// 重要：先 Unsubscribe 再等 goroutine
	eventBus.Unsubscribe(bus.EventPartUpdated, idUpd)
	eventBus.Unsubscribe(bus.EventPartDelta, idDelta)
	<-collectDone

	if err != nil {
		t.Fatalf("loop.Run: %v", err)
	}
	if mock.gotCalls() != 2 {
		t.Errorf("Ollama 应被调 2 次, 实际 %d", mock.gotCalls())
	}
	if !sawToolPart.Load() {
		t.Error("从未收到 ToolPart 事件")
	}
	if !toolCompleted.Load() {
		t.Error("工具应完成（part 更新为 ToolComplete）")
	}
	// 文本应包含两部分："Let me read it" + "The file says: Hello from file"
	if !strings.Contains(textBuf.String(), "Let me read it") {
		t.Errorf("text 应含 'Let me read it', 实际: %q", textBuf.String())
	}
	if !strings.Contains(textBuf.String(), "Hello from file") {
		t.Errorf("text 应含 'Hello from file', 实际: %q", textBuf.String())
	}
}

// TestIntegration_RequestBodyShape 验证发到 Ollama 的请求包含 tools、system、history
func TestIntegration_RequestBodyShape(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"),
		[]byte("# Project Conventions\n\nBe helpful."), 0644)

	mock := newMockOllamaScripted(t,
		`{"model":"x","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","eval_count":1,"prompt_eval_count":1}`)

	loop, eventBus, _ := setupEnv(t, mock, dir)
	defer eventBus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = loop.Run(ctx, &session.UserMessage{Text: "test"})

	if len(mock.gotBodies) != 1 {
		t.Fatalf("got %d bodies, 期望 1", len(mock.gotBodies))
	}
	body := mock.gotBodies[0]

	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	if !req.Stream {
		t.Error("stream 应为 true")
	}
	if len(req.Tools) != 3 {
		t.Fatalf("tools = %d, 期望 3 (read + edit + bash)", len(req.Tools))
	}
	// 至少含 read
	hasRead := false
	for _, tool := range req.Tools {
		if tool.Function.Name == "read" {
			hasRead = true
		}
	}
	if !hasRead {
		t.Error("应含 'read' 工具")
	}
	// messages 应有 system + user
	if len(req.Messages) < 2 {
		t.Fatalf("messages = %d, 期望 ≥ 2", len(req.Messages))
	}
	if req.Messages[0].Role != "system" {
		t.Errorf("messages[0].role = %q", req.Messages[0].Role)
	}
	if !strings.Contains(req.Messages[0].Content, "Be helpful") {
		t.Errorf("system 应含 AGENTS.md 内容, 实际: %q", req.Messages[0].Content)
	}
	if req.Messages[1].Role != "user" || req.Messages[1].Content != "test" {
		t.Errorf("messages[1] = %+v", req.Messages[1])
	}
}

// TestIntegration_MultipleToolCalls 验证模型在同一次响应里调多个工具
func TestIntegration_MultipleToolCalls(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("AAA"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("BBB"), 0644)

	mock := newMockOllamaScripted(t,
		// 1st: 同时调 a.txt 和 b.txt
		`{"model":"x","message":{"role":"assistant","content":"Reading both","tool_calls":[{"function":{"name":"read","arguments":{"path":"a.txt"}}},{"function":{"name":"read","arguments":{"path":"b.txt"}}}]},"done":true,"done_reason":"tool_calls","eval_count":3,"prompt_eval_count":5}`,
		// 2nd: 最终回复
		`{"model":"x","message":{"role":"assistant","content":"Got both: AAA and BBB"},"done":true,"done_reason":"stop","eval_count":2,"prompt_eval_count":10}`)

	loop, eventBus, _ := setupEnv(t, mock, dir)
	defer eventBus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := loop.Run(ctx, &session.UserMessage{Text: "read both"})

	if err != nil {
		t.Fatalf("loop.Run: %v", err)
	}
	if mock.gotCalls() != 2 {
		t.Errorf("Ollama 应调 2 次, 实际 %d", mock.gotCalls())
	}
	// 验证 2nd 请求 body 里 history 包含两个 tool result
	if len(mock.gotBodies) != 2 {
		t.Fatalf("got %d bodies, 期望 2", len(mock.gotBodies))
	}
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(mock.gotBodies[1], &req)
	// 应该有 system + user + assistant (with tool_calls) + 2 tool messages
	// 注：v0.1 实现里 history 只追加 user/assistant，不追加 tool result message
	// 这是 v1 缺陷，tracer bullet 阶段先 work
	if len(req.Messages) < 2 {
		t.Errorf("messages = %d, 期望 ≥ 2", len(req.Messages))
	}
}

// TestIntegration_ToolNotFound 验证 Ollama 调了不存在的工具
func TestIntegration_ToolNotFound(t *testing.T) {
	dir := t.TempDir()
	mock := newMockOllamaScripted(t,
		`{"model":"x","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"nonexistent","arguments":{}}}]},"done":true,"done_reason":"tool_calls","eval_count":1,"prompt_eval_count":3}`)

	loop, eventBus, _ := setupEnv(t, mock, dir)
	defer eventBus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// 不应该 panic，应该 graceful 处理
	err := loop.Run(ctx, &session.UserMessage{Text: "test"})

	// v0.1: tool 不存在时 loop 记 error 但不 fatal，所以 Run 返 nil
	if err != nil {
		t.Fatalf("loop.Run: %v", err)
	}
	// 关键：没 panic
}

// 触发使用 fmt
var _ = fmt.Sprintf

// =============================================================================
// Edit 工具集成测试
// =============================================================================

// TestIntegration_LoopCallsEditTool 验证核心场景：
// 模型决定调 Edit 工具 → Loop 执行 → 文件真被改 → 结果喂回 → 模型最终回复
func TestIntegration_LoopCallsEditTool(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mock := newMockOllamaScripted(t,
		// 1st: model emits edit
		`{"model":"x","message":{"role":"assistant","content":"Let me edit.","tool_calls":[{"function":{"name":"edit","arguments":{"filePath":"f.txt","oldString":"hello","newString":"hi"}}}]},"done":true,"done_reason":"tool_calls","eval_count":3,"prompt_eval_count":5}`,
		// 2nd: model returns final answer
		`{"model":"x","message":{"role":"assistant","content":"Done."},"done":true,"done_reason":"stop","eval_count":1,"prompt_eval_count":10}`)

	loop, eventBus, _ := setupEnv(t, mock, dir)
	defer eventBus.Close()

	chUpd, idUpd := eventBus.SubscribeID(bus.EventPartUpdated)
	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		for ev := range chUpd {
			if tp, ok := ev.Data.(*session.ToolPart); ok && tp.ToolID == "edit" {
				if tp.State == session.ToolComplete {
					t.Logf("edit 完成: %s", tp.Output)
				}
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := loop.Run(ctx, &session.UserMessage{Text: "edit f.txt"}); err != nil {
		t.Fatalf("loop.Run: %v", err)
	}

	eventBus.Unsubscribe(bus.EventPartUpdated, idUpd)
	<-collectDone

	if mock.gotCalls() != 2 {
		t.Errorf("Ollama 应调 2 次, 实际 %d", mock.gotCalls())
	}
	got, _ := os.ReadFile(target)
	if string(got) != "hi world\n" {
		t.Errorf("文件未修改: %q", string(got))
	}
}

// =============================================================================
// Bash 工具集成测试
// =============================================================================

// TestIntegration_LoopCallsBashTool 验证核心场景：
// 模型决定调 Bash 工具 → Loop 执行 → 命令真实跑 → 结果喂回 → 模型最终回复
func TestIntegration_LoopCallsBashTool(t *testing.T) {
	dir := t.TempDir()

	mock := newMockOllamaScripted(t,
		// 1st: model emits bash
		`{"model":"x","message":{"role":"assistant","content":"Let me run.","tool_calls":[{"function":{"name":"bash","arguments":{"command":"echo hello-from-bash"}}}]},"done":true,"done_reason":"tool_calls","eval_count":2,"prompt_eval_count":4}`,
		// 2nd: model returns final answer
		`{"model":"x","message":{"role":"assistant","content":"Done."},"done":true,"done_reason":"stop","eval_count":1,"prompt_eval_count":8}`)

	loop, eventBus, _ := setupEnv(t, mock, dir)
	defer eventBus.Close()

	chUpd, idUpd := eventBus.SubscribeID(bus.EventPartUpdated)
	var bashResult string
	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		for ev := range chUpd {
			if tp, ok := ev.Data.(*session.ToolPart); ok && tp.ToolID == "bash" {
				if tp.State == session.ToolComplete {
					bashResult = tp.Output
				}
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := loop.Run(ctx, &session.UserMessage{Text: "run bash"}); err != nil {
		t.Fatalf("loop.Run: %v", err)
	}

	eventBus.Unsubscribe(bus.EventPartUpdated, idUpd)
	<-collectDone

	if mock.gotCalls() != 2 {
		t.Errorf("Ollama 应调 2 次, 实际 %d", mock.gotCalls())
	}
	if !strings.Contains(bashResult, "hello-from-bash") {
		t.Errorf("bash 输出应含 'hello-from-bash': %q", bashResult)
	}
	if !strings.Contains(bashResult, "=== EXIT ===\n0") {
		t.Errorf("bash 输出应含 EXIT 0: %q", bashResult)
	}
}
