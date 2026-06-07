package compaction

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"acorncode/internal/llm"
)

// fakeProvider 是测试用的简单 provider（只返 text）
type fakeProvider struct {
	text  string
	err   error
	calls int
}

func (f *fakeProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.RawChunk, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make(chan llm.RawChunk, 2)
	go func() {
		defer close(out)
		out <- llm.RawChunk{Type: "text", Data: f.text}
		out <- llm.RawChunk{Type: "finish", Data: "stop"}
	}()
	return out, nil
}

func (f *fakeProvider) ListModels(ctx context.Context) ([]string, error) {
	return nil, nil
}

func TestSimpleCompactor_TooShort_NoOp(t *testing.T) {
	fp := &fakeProvider{text: "summary"}
	c := &SimpleCompactor{Provider: fp, KeepRecent: 5}

	history := []llm.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	out, err := c.Compact(context.Background(), history)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len = %d, 期望 2（不变）", len(out))
	}
	if fp.calls != 0 {
		t.Errorf("Provider.Stream 不应被调: %d", fp.calls)
	}
}

func TestSimpleCompactor_Basic(t *testing.T) {
	fp := &fakeProvider{text: "用户问 Go 编译，模型给了 go build 建议。"}
	c := &SimpleCompactor{
		Provider:   fp,
		Model:      llm.Model{ID: "test"},
		KeepRecent: 2,
	}

	history := []llm.Message{
		{Role: "user", Content: "How to compile Go?"},
		{Role: "assistant", Content: "Use go build."},
		{Role: "user", Content: "What about cross-compile?"},
		{Role: "assistant", Content: "Set GOOS=..."},
	}
	out, err := c.Compact(context.Background(), history)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 3 {
		t.Errorf("len = %d, 期望 3 (1 summary + 2 recent)", len(out))
	}
	if out[0].Role != "system" {
		t.Errorf("首条 role = %q, 期望 system", out[0].Role)
	}
	if !strings.Contains(out[0].Content, "用户问 Go 编译") {
		t.Errorf("summary 不含原文: %q", out[0].Content)
	}
	// 最近 2 条应是原始 history 的最后 2 条
	if out[1].Content != "What about cross-compile?" {
		t.Errorf("out[1] = %q", out[1].Content)
	}
	if out[2].Content != "Set GOOS=..." {
		t.Errorf("out[2] = %q", out[2].Content)
	}
}

func TestSimpleCompactor_ProviderError_ReturnsOriginal(t *testing.T) {
	fp := &fakeProvider{err: errors.New("network fail")}
	c := &SimpleCompactor{Provider: fp, KeepRecent: 1}

	history := []llm.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
		{Role: "assistant", Content: "d"},
	}
	out, err := c.Compact(context.Background(), history)
	if err == nil {
		t.Error("Provider 失败应返 err")
	}
	// 失败时返原 history
	if len(out) != 4 {
		t.Errorf("失败时返原 history: len = %d, 期望 4", len(out))
	}
}

func TestSimpleCompactor_NoProvider(t *testing.T) {
	c := &SimpleCompactor{Provider: nil, KeepRecent: 1}
	_, err := c.Compact(context.Background(), []llm.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
	})
	if err == nil {
		t.Error("无 provider 应返 err")
	}
}

func TestSimpleCompactor_EmptyResponse_NoOp(t *testing.T) {
	// LLM 返空（罕见）— 返原 history，不 panic
	fp := &fakeProvider{text: ""}
	c := &SimpleCompactor{Provider: fp, KeepRecent: 1}

	history := []llm.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
	}
	out, err := c.Compact(context.Background(), history)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// 返原 history
	if len(out) != 3 {
		t.Errorf("空响应时返原 history: len = %d, 期望 3", len(out))
	}
}

func TestSimpleCompactor_IntegrationWithOllama(t *testing.T) {
	// 用 httptest mock Ollama 验证 summary prompt 拼对
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		// 返 text chunk + done
		w.Write([]byte(`{"message":{"role":"assistant","content":"compacted"}}` + "\n"))
		w.Write([]byte(`{"done":true}` + "\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	c := &SimpleCompactor{
		Provider:   llm.NewOllama(llm.OllamaConfig{Endpoint: srv.URL, Model: "test"}),
		Model:      llm.Model{ID: "test"},
		KeepRecent: 1,
	}
	history := []llm.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
	}
	out, err := c.Compact(context.Background(), history)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len = %d, 期望 2", len(out))
	}
	if !strings.Contains(out[0].Content, "compacted") {
		t.Errorf("summary 应含 'compacted': %q", out[0].Content)
	}
}
