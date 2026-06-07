package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"acorncode/internal/instruction"
	"acorncode/internal/llm"
	"acorncode/internal/permission"
	"acorncode/internal/session"
	"acorncode/internal/tool"
	"acorncode/internal/toolcall"
)

// fakeProvider 返固定 text
type fakeProvider struct {
	text string
}

func (f *fakeProvider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.RawChunk, error) {
	out := make(chan llm.RawChunk, 4)
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

// setupTestServer 构造一个最小可跑 server
func setupTestServer(t *testing.T, providerText string) (*httptest.Server, func()) {
	t.Helper()

	dir := t.TempDir()
	store, err := session.NewSQLiteStore(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}

	tools := tool.NewRegistry()
	broker := permission.NewBroker(nil)
	loader := instruction.NewLoader(".")

	srv := New(Config{
		Addr:     ":0",
		Provider: &fakeProvider{text: providerText},
		Strategy: toolcall.NewNative(),
		Store:    store,
		Tools:    tools,
		Broker:   broker,
		Loader:   loader,
		Model:    llm.Model{ID: "test-model"},
	})

	// httptest.Server 替我们启 HTTP listener
	ts := httptest.NewServer(srv.srv.Handler)

	cleanup := func() {
		ts.Close()
		_ = store.Close()
	}
	return ts, cleanup
}

func TestServer_Health(t *testing.T) {
	ts, cleanup := setupTestServer(t, "hi")
	defer cleanup()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "OK") {
		t.Errorf("body = %s", string(body))
	}
}

func TestServer_Chat_MethodNotAllowed(t *testing.T) {
	ts, cleanup := setupTestServer(t, "hi")
	defer cleanup()

	resp, err := http.Get(ts.URL + "/v1/chat")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, 期望 405", resp.StatusCode)
	}
}

func TestServer_Chat_EmptyMessage(t *testing.T) {
	ts, cleanup := setupTestServer(t, "hi")
	defer cleanup()

	resp, err := http.Post(ts.URL+"/v1/chat", "application/json",
		strings.NewReader(`{"message": ""}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, 期望 400", resp.StatusCode)
	}
}

func TestServer_Chat_BadJSON(t *testing.T) {
	ts, cleanup := setupTestServer(t, "hi")
	defer cleanup()

	resp, err := http.Post(ts.URL+"/v1/chat", "application/json",
		strings.NewReader(`{bad json`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, 期望 400", resp.StatusCode)
	}
}

func TestServer_Chat_SSEResponse(t *testing.T) {
	ts, cleanup := setupTestServer(t, "Hello from fake")
	defer cleanup()

	// 不用 http.Client：Go 的 chunked decoder 对裸 LF 严格
	// 改用 raw HTTP/1.1 解析
	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /v1/chat HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 26\r\nConnection: close\r\n\r\n%s",
		strings.TrimPrefix(ts.URL, "http://"),
		`{"message": "say hello"}`)

	// 读 response
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusLine, "200") {
		t.Errorf("status line = %q", statusLine)
	}

	// 读 headers
	var contentType string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // 头部结束
		}
		if strings.HasPrefix(strings.ToLower(line), "content-type:") {
			contentType = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		}
	}
	if !strings.Contains(contentType, "text/event-stream") {
		t.Errorf("content-type = %q, 期望 SSE", contentType)
	}

	// 读 body 直到 close
	body, _ := io.ReadAll(br)
	bodyStr := string(body)

	// 验证事件序列
	var events []string
	for _, line := range strings.Split(bodyStr, "\n") {
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}

	if len(events) < 3 {
		t.Fatalf("应至少 3 个 event, got %d: %v", len(events), events)
	}
	if events[0] != "session" {
		t.Errorf("首 event = %q, 期望 session", events[0])
	}
	if events[len(events)-1] != "finish" {
		t.Errorf("末 event = %q, 期望 finish", events[len(events)-1])
	}
	// 中间应有 text
	var hasText bool
	for _, e := range events {
		if e == "text" {
			hasText = true
		}
	}
	if !hasText {
		t.Errorf("应含 text event: %v", events)
	}
}

func TestServer_Chat_WithSessionID(t *testing.T) {
	ts, cleanup := setupTestServer(t, "hi")
	defer cleanup()

	sessID := "sess_test_abc"
	resp, err := http.Post(ts.URL+"/v1/chat", "application/json",
		strings.NewReader(`{"message": "hi", "session_id": "`+sessID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// 读 SSE 直到看到含 session_id 的 event
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var ev map[string]any
			if err := json.Unmarshal([]byte(data), &ev); err == nil {
				if sid, ok := ev["session_id"].(string); ok && sid == sessID {
					return // 找到
				}
			}
		}
	}
	t.Error("未找到含 session_id 的 event")
}

// helper：避免 time 包 unused
var _ = time.Now
