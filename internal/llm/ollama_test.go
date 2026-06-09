package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ============================================================================
// 测试辅助
// ============================================================================

// newTestOllama 启动一个 httptest.Server 模拟 Ollama /api/chat。
// handlerFunc 在每个请求被调用，返回 NDJSON 行。
func newTestOllama(t *testing.T, handler http.HandlerFunc) (*Ollama, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Ollama{
		cfg: OllamaConfig{
			Endpoint: srv.URL,
			Timeout:  5 * time.Second,
		},
		client: srv.Client(),
	}, srv
}

// writeNDJSON 把每行作为独立的 NDJSON event 写入响应
func writeNDJSON(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, line := range lines {
		_, _ = w.Write([]byte(line + "\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// ============================================================================
// 测试用例
// ============================================================================

// TestOllama_Stream_TextChunks 验证多个文本 chunk 能正确拼接，
// finish 事件能正确解析 usage。
func TestOllama_Stream_TextChunks(t *testing.T) {
	o, _ := newTestOllama(t, func(w http.ResponseWriter, r *http.Request) {
		writeNDJSON(w,
			`{"model":"qwen2.5","created_at":"2026-01-01","message":{"role":"assistant","content":"Hello "},"done":false}`,
			`{"model":"qwen2.5","created_at":"2026-01-01","message":{"role":"assistant","content":"world!"},"done":false}`,
			`{"model":"qwen2.5","created_at":"2026-01-01","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":12,"eval_count":2,"total_duration":1500000000}`,
		)
	})

	ctx := context.Background()
	ch, err := o.Stream(ctx, ChatRequest{
		Model: Model{ID: "qwen2.5-coder:7b"},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var texts []string
	var finish *RawChunk
	for c := range ch {
		switch c.Type {
		case "text":
			texts = append(texts, c.Data)
		case "finish":
			finish = &c
		case "error":
			t.Fatalf("意外错误 chunk: %s", c.Data)
		}
	}

	got := strings.Join(texts, "")
	if got != "Hello world!" {
		t.Errorf("文本拼接 = %q, 期望 %q", got, "Hello world!")
	}

	if finish == nil {
		t.Fatal("未收到 finish chunk")
	}
	var usage Usage
	if err := json.Unmarshal([]byte(finish.Data), &usage); err != nil {
		t.Fatalf("解析 usage 失败: %v", err)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 2 {
		t.Errorf("usage = %+v, 期望 in=12 out=2", usage)
	}
}

// TestOllama_Stream_ToolCall 验证 tool_call chunk 正确解析 name 和 arguments
func TestOllama_Stream_ToolCall(t *testing.T) {
	o, _ := newTestOllama(t, func(w http.ResponseWriter, r *http.Request) {
		writeNDJSON(w,
			`{"model":"qwen2.5","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read","arguments":{"path":"main.go"}}}]},"done":false}`,
			`{"model":"qwen2.5","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":8,"eval_count":1}`,
		)
	})

	ch, _ := o.Stream(context.Background(), ChatRequest{Model: Model{ID: "x"}})
	var calls []RawChunk
	for c := range ch {
		if c.Type == "tool_call" {
			calls = append(calls, c)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("收到 %d 个 tool call, 期望 1", len(calls))
	}
	if name, _ := calls[0].Meta.(map[string]any)["name"].(string); name != "read" {
		t.Errorf("工具名 = %q, 期望 read", name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Data), &args); err != nil {
		t.Fatalf("参数不是合法 JSON: %v", err)
	}
	if args["path"] != "main.go" {
		t.Errorf("args[path] = %v, 期望 main.go", args["path"])
	}
}

// TestOllama_Stream_ContextCancel 验证 ctx 取消时 channel 干净关闭
func TestOllama_Stream_ContextCancel(t *testing.T) {
	// 服务端返回 headers 后等客户端断开
	o, _ := newTestOllama(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// 等到 ctx 取消就返回
		<-r.Context().Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	ch, err := o.Stream(ctx, ChatRequest{Model: Model{ID: "x"}})
	if err != nil {
		// 部分 Go 版本在 ctx cancel 时直接返 transport 错误，可接受
		t.Logf("Stream 返回错误: %v（可接受）", err)
		return
	}

	// 应该在 500ms 内关闭
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // 关闭即成功
			}
		case <-timeout:
			t.Fatal("ctx 取消后 channel 未关闭")
		}
	}
}

// TestOllama_Stream_NonOKStatus 验证 HTTP 错误状态码透传
func TestOllama_Stream_NonOKStatus(t *testing.T) {
	o, _ := newTestOllama(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"model not found"}`))
	})

	_, err := o.Stream(context.Background(), ChatRequest{Model: Model{ID: "nonexistent"}})
	if err == nil {
		t.Fatal("500 状态下应返回错误")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("错误 = %v, 应包含 500", err)
	}
}

// TestOllama_Stream_ServerError 验证流中段错误也能捕获
func TestOllama_Stream_ServerError(t *testing.T) {
	o, _ := newTestOllama(t, func(w http.ResponseWriter, r *http.Request) {
		writeNDJSON(w,
			`{"model":"x","message":{"role":"assistant","content":""},"done":false}`,
			`{"error":"context length exceeded"}`,
		)
	})

	ch, _ := o.Stream(context.Background(), ChatRequest{Model: Model{ID: "x"}})
	var got string
	for c := range ch {
		if c.Type == "error" {
			got = c.Data
		}
	}
	if !strings.Contains(got, "context length exceeded") {
		t.Errorf("错误 chunk = %q, 应包含 context length exceeded", got)
	}
}

// TestOllama_Stream_RequestBody 验证请求体结构（model/system/messages/tools）
func TestOllama_Stream_RequestBody(t *testing.T) {
	var capturedBody []byte
	var capturedPath string
	var capturedHeaders http.Header

	o, _ := newTestOllama(t, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedHeaders = r.Header
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读 body 失败: %v", err)
		}
		writeNDJSON(w,
			`{"model":"x","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","eval_count":1,"prompt_eval_count":1}`,
		)
	})

	req := ChatRequest{
		Model:  Model{ID: "qwen2.5-coder:7b"},
		System: []string{"You are helpful.", "Be concise."},
		History: []Message{
			{Role: "user", Content: "Hello"},
		},
		Tools: []Definition{
			{
				ID:          "read",
				Description: "Read a file",
				JSONSchema:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
			},
		},
	}
	ch, _ := o.Stream(context.Background(), req)
	// drain
	for range ch {
	}

	// 验证 path
	if capturedPath != "/api/chat" {
		t.Errorf("path = %q, 期望 /api/chat", capturedPath)
	}

	// 验证 Content-Type
	if ct := capturedHeaders.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, 期望 application/json", ct)
	}

	// 验证 body 结构
	var got struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(capturedBody, &got); err != nil {
		t.Fatalf("解析 body 失败: %v", err)
	}

	if got.Model != "qwen2.5-coder:7b" {
		t.Errorf("model = %q", got.Model)
	}
	if !got.Stream {
		t.Error("stream 应为 true")
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages 数量 = %d, 期望 2 (system+user): %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "system" || !strings.Contains(got.Messages[0].Content, "You are helpful.") {
		t.Errorf("system 消息错误: %+v", got.Messages[0])
	}
	if got.Messages[0].Content != "You are helpful.\n\nBe concise." {
		t.Errorf("system 内容 = %q", got.Messages[0].Content)
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Content != "Hello" {
		t.Errorf("user 消息错误: %+v", got.Messages[1])
	}
	if len(got.Tools) != 1 {
		t.Fatalf("tools 数量 = %d, 期望 1", len(got.Tools))
	}
	if got.Tools[0].Type != "function" || got.Tools[0].Function.Name != "read" {
		t.Errorf("tool 错误: %+v", got.Tools[0])
	}
}

// TestOllama_Stream_FormatForwarded 验证 v1.4：ChatRequest.Format 转发到 Ollama format 字段。
func TestOllama_Stream_FormatForwarded(t *testing.T) {
	var capturedBody []byte
	o, _ := newTestOllama(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		writeNDJSON(w,
			`{"model":"x","message":{"role":"assistant","content":"{}"},"done":true,"done_reason":"stop","eval_count":1,"prompt_eval_count":1}`,
		)
	})

	formatSchema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	req := ChatRequest{
		Model:   Model{ID: "qwen2.5-coder:7b"},
		History: []Message{{Role: "user", Content: "hi"}},
		Format:  formatSchema,
	}
	ch, _ := o.Stream(context.Background(), req)
	for range ch {
	}

	var got struct {
		Format json.RawMessage `json:"format"`
	}
	if err := json.Unmarshal(capturedBody, &got); err != nil {
		t.Fatalf("解析 body 失败: %v", err)
	}
	if len(got.Format) == 0 {
		t.Fatal("format 字段应被转发")
	}
	var fs struct {
		Type     string   `json:"type"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(got.Format, &fs); err != nil {
		t.Fatalf("format 应是合法 JSON: %v", err)
	}
	if fs.Type != "object" {
		t.Errorf("format.type = %q, 期望 object", fs.Type)
	}
}

// TestOllama_Stream_NoFormat 验证不设 Format 时 body 里无 format 字段（omitempty）。
func TestOllama_Stream_NoFormat(t *testing.T) {
	var capturedBody []byte
	o, _ := newTestOllama(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		writeNDJSON(w,
			`{"model":"x","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","eval_count":1,"prompt_eval_count":1}`,
		)
	})
	req := ChatRequest{Model: Model{ID: "x"}, History: []Message{{Role: "user", Content: "hi"}}}
	ch, _ := o.Stream(context.Background(), req)
	for range ch {
	}
	if strings.Contains(string(capturedBody), `"format"`) {
		t.Errorf("无 Format 时不应出现 format 字段: %s", capturedBody)
	}
}

// TestOllama_Stream_BearerAuth 验证 APIKey 走 Authorization: Bearer
func TestOllama_Stream_BearerAuth(t *testing.T) {
	var gotAuth string
	o, _ := newTestOllama(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		writeNDJSON(w,
			`{"model":"x","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","eval_count":0,"prompt_eval_count":0}`,
		)
	})
	o.cfg.APIKey = "secret-token"

	ch, _ := o.Stream(context.Background(), ChatRequest{Model: Model{ID: "x"}})
	for range ch {
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, 期望 Bearer secret-token", gotAuth)
	}
}

// TestOllama_ListModels 验证 /api/tags 解析
func TestOllama_ListModels(t *testing.T) {
	o, _ := newTestOllama(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, 期望 /api/tags", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"name":"qwen2.5-coder:7b","size":4600000000},
			{"name":"llama3.1:8b","size":4800000000}
		]}`))
	})

	models, err := o.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %d, 期望 2", len(models))
	}
	if models[0] != "qwen2.5-coder:7b" || models[1] != "llama3.1:8b" {
		t.Errorf("models = %v", models)
	}
}

// ============================================================================
// 编译时断言
// ============================================================================

// TestOllama_ImplementsProvider 编译时验证 Ollama 满足 Provider 接口
func TestOllama_ImplementsProvider(t *testing.T) {
	var _ Provider = (*Ollama)(nil)
}

// ============================================================================
// 并发安全（冒烟测试）
// ============================================================================

// TestOllama_Stream_ConcurrentProducers 验证 5 个并发 stream 互不干扰
func TestOllama_Stream_ConcurrentProducers(t *testing.T) {
	o, _ := newTestOllama(t, func(w http.ResponseWriter, r *http.Request) {
		writeNDJSON(w,
			`{"model":"x","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop","eval_count":1,"prompt_eval_count":1}`,
		)
	})

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, err := o.Stream(context.Background(), ChatRequest{Model: Model{ID: "x"}})
			if err != nil {
				t.Errorf("并发 Stream: %v", err)
				return
			}
			for c := range ch {
				if c.Type == "error" {
					t.Errorf("并发错误: %s", c.Data)
				}
			}
		}()
	}
	wg.Wait()
}

// 编译时引用 fmt 防止 import 被优化掉
var _ = fmt.Sprintf
