package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockAnthropicSSE 写一个 SSE 响应（用于测试）
func mockAnthropicSSE(t *testing.T, events string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求头
		if r.Header.Get("x-api-key") == "" {
			t.Error("请求缺 x-api-key")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("请求缺 anthropic-version")
		}
		if r.Header.Get("content-type") != "application/json" {
			t.Error("请求 content-type 错")
		}

		// 验证 body（粗略）
		body, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(body, &b)
		if b["model"] == nil {
			t.Errorf("body 缺 model: %s", body)
		}
		if b["system"] == nil {
			t.Error("body 缺 system")
		}
		if b["stream"] != true {
			t.Error("body stream 应 true")
		}

		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)

		// 按行写
		for _, line := range strings.Split(events, "\n") {
			if line == "" {
				// 写空行 flush
				_, _ = w.Write([]byte("\n"))
				if flusher != nil {
					flusher.Flush()
				}
				continue
			}
			_, _ = w.Write([]byte(line + "\n"))
		}
		if flusher != nil {
			flusher.Flush()
		}
	}))
}

func collectChunks(t *testing.T, ch <-chan RawChunk) []RawChunk {
	t.Helper()
	var out []RawChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

func TestAnthropic_RequiresAPIKey(t *testing.T) {
	a := NewAnthropic(AnthropicConfig{
		// APIKey 故意空
		Model: "claude-test",
	})
	_, err := a.Stream(context.Background(), ChatRequest{Model: Model{ID: "claude-test"}})
	if err == nil {
		t.Fatal("空 APIKey 应返 err")
	}
	if !strings.Contains(err.Error(), "APIKey") {
		t.Errorf("err 应含 APIKey: %v", err)
	}
}

func TestAnthropic_BasicText(t *testing.T) {
	srv := mockAnthropicSSE(t, strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[]}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n"))
	defer srv.Close()

	a := NewAnthropic(AnthropicConfig{
		Endpoint: srv.URL,
		APIKey:   "test-key",
		Model:    "claude-test",
		Timeout:  10 * time.Second,
	})

	ch, err := a.Stream(context.Background(), ChatRequest{
		Model:  Model{ID: "claude-test"},
		System: []string{"You are a helpful assistant"},
		History: []Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("Stream err: %v", err)
	}

	chunks := collectChunks(t, ch)
	if len(chunks) < 2 {
		t.Fatalf("应至少 2 chunks（text + finish），got %d", len(chunks))
	}

	// 检查有 text chunk 含 "Hello world"
	var foundText bool
	for _, c := range chunks {
		if c.Type == "text" && c.Data == "Hello world" {
			foundText = true
			break
		}
	}
	if !foundText {
		t.Errorf("应找到 text='Hello world'，chunks: %+v", chunks)
	}

	// 检查 finish
	var foundFinish bool
	for _, c := range chunks {
		if c.Type == "finish" && c.Data == "stop" {
			foundFinish = true
			if u, ok := c.Meta.(Usage); ok {
				if u.OutputTokens != 2 {
					t.Errorf("OutputTokens = %d, 期望 2", u.OutputTokens)
				}
			}
		}
	}
	if !foundFinish {
		t.Errorf("应找到 finish chunk: %+v", chunks)
	}
}

func TestAnthropic_ToolUse(t *testing.T) {
	srv := mockAnthropicSSE(t, strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1"}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/tmp/x\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n"))
	defer srv.Close()

	a := NewAnthropic(AnthropicConfig{
		Endpoint: srv.URL,
		APIKey:   "test-key",
		Model:    "claude-test",
		Timeout:  10 * time.Second,
	})

	ch, err := a.Stream(context.Background(), ChatRequest{
		Model:  Model{ID: "claude-test"},
		System: []string{"sys"},
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks := collectChunks(t, ch)

	// 找 tool_call chunk
	var toolCall *RawChunk
	for i, c := range chunks {
		if c.Type == "tool_call" {
			toolCall = &chunks[i]
			break
		}
	}
	if toolCall == nil {
		t.Fatalf("应找到 tool_call chunk, got: %+v", chunks)
	}

	if toolCall.Data != `{"path":"/tmp/x"}` {
		t.Errorf("args = %q, 期望 {\"path\":\"/tmp/x\"}", toolCall.Data)
	}

	meta, ok := toolCall.Meta.(map[string]any)
	if !ok {
		t.Fatalf("Meta 应是 map, got %T", toolCall.Meta)
	}
	if meta["id"] != "toolu_1" {
		t.Errorf("id = %v, 期望 toolu_1", meta["id"])
	}
	if meta["name"] != "read" {
		t.Errorf("name = %v, 期望 read", meta["name"])
	}

	// finish reason 应是 tool_use
	for _, c := range chunks {
		if c.Type == "finish" {
			if c.Data != "tool_use" {
				t.Errorf("finish reason = %q, 期望 tool_use", c.Data)
			}
		}
	}
}

func TestAnthropic_ErrorEvent(t *testing.T) {
	srv := mockAnthropicSSE(t, strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"type":"rate_limit","message":"rate limited"}}`,
		``,
	}, "\n"))
	defer srv.Close()

	a := NewAnthropic(AnthropicConfig{
		Endpoint: srv.URL,
		APIKey:   "test-key",
		Model:    "claude-test",
		Timeout:  10 * time.Second,
	})

	ch, err := a.Stream(context.Background(), ChatRequest{Model: Model{ID: "claude-test"}, System: []string{"s"}})
	if err != nil {
		t.Fatal(err)
	}
	chunks := collectChunks(t, ch)

	var foundErr bool
	for _, c := range chunks {
		if c.Type == "error" {
			foundErr = true
			if !strings.Contains(c.Data, "rate limited") {
				t.Errorf("err msg = %q, 期望含 'rate limited'", c.Data)
			}
		}
	}
	if !foundErr {
		t.Errorf("应找到 error chunk, got: %+v", chunks)
	}
}

func TestAnthropic_HttpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"type":"error","error":{"type":"authentication","message":"invalid key"}}`))
	}))
	defer srv.Close()

	a := NewAnthropic(AnthropicConfig{
		Endpoint: srv.URL,
		APIKey:   "bad",
		Model:    "claude-test",
		Timeout:  10 * time.Second,
	})

	_, err := a.Stream(context.Background(), ChatRequest{Model: Model{ID: "claude-test"}, System: []string{"s"}})
	if err == nil {
		t.Fatal("401 应返 err")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err 应含 401: %v", err)
	}
}

func TestAnthropic_ToolSchemaConversion(t *testing.T) {
	// 验证 buildRequestBody 把 tool schema 转成 Anthropic 格式
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		// 空响应
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	a := NewAnthropic(AnthropicConfig{
		Endpoint: srv.URL,
		APIKey:   "test",
		Model:    "claude-test",
	})

	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	_, _ = a.Stream(context.Background(), ChatRequest{
		Model:  Model{ID: "claude-test"},
		System: []string{"s"},
		Tools: []Definition{
			{ID: "read", Description: "Read a file", JSONSchema: schema},
		},
	})

	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, 期望 1", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "read" {
		t.Errorf("name = %v", tool["name"])
	}
	if tool["description"] != "Read a file" {
		t.Errorf("description = %v", tool["description"])
	}
	if _, ok := tool["input_schema"]; !ok {
		t.Errorf("应有 input_schema: %v", tool)
	}
	// 不应有 type/function（OpenAI 风格）
	if _, ok := tool["type"]; ok {
		t.Errorf("不应有 type: %v", tool)
	}
}

func TestAnthropic_ListModels(t *testing.T) {
	a := NewAnthropic(AnthropicConfig{APIKey: "test"})
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) < 3 {
		t.Errorf("应至少返 3 个 model, got %d", len(models))
	}
}
