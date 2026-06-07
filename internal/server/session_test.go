package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"acorncode/internal/llm"
	"acorncode/internal/tool"
	"acorncode/internal/toolcall"
)

// ========== v1.1.2: Multi-session API ==========

func TestServer_CreateSession(t *testing.T) {
	ts, cleanup := setupTestServer(t, "hi")
	defer cleanup()

	resp, err := http.Post(ts.URL+"/v1/sessions", "application/json",
		strings.NewReader(`{"title": "test session"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		t.Errorf("status = %d, 期望 201", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var info SessionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("bad JSON: %s", string(body))
	}
	if !strings.HasPrefix(info.ID, "sess_") {
		t.Errorf("ID 应以 sess_ 开头: %s", info.ID)
	}
	if info.Title != "test session" {
		t.Errorf("title = %q, 期望 'test session'", info.Title)
	}
}

func TestServer_ListSessions(t *testing.T) {
	ts, cleanup := setupTestServer(t, "hi")
	defer cleanup()

	// 创建 2 个 session
	for i := 0; i < 2; i++ {
		resp, _ := http.Post(ts.URL+"/v1/sessions", "application/json",
			strings.NewReader(`{"title": "x"}`))
		resp.Body.Close()
	}

	// 列
	resp, err := http.Get(ts.URL + "/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var data struct {
		Sessions []SessionInfo `json:"sessions"`
		Count    int           `json:"count"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Count != 2 {
		t.Errorf("count = %d, 期望 2", data.Count)
	}
	if len(data.Sessions) != 2 {
		t.Errorf("sessions len = %d", len(data.Sessions))
	}
}

func TestServer_GetSession(t *testing.T) {
	ts, cleanup := setupTestServer(t, "hi")
	defer cleanup()

	// 创建
	resp, _ := http.Post(ts.URL+"/v1/sessions", "application/json",
		strings.NewReader(`{"title": "my-sess"}`))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var created SessionInfo
	json.Unmarshal(body, &created)

	// 取
	resp2, err := http.Get(ts.URL + "/v1/sessions/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != 200 {
		t.Errorf("status = %d", resp2.StatusCode)
	}
	var got SessionInfo
	body2, _ := io.ReadAll(resp2.Body)
	json.Unmarshal(body2, &got)
	if got.ID != created.ID {
		t.Errorf("id = %q", got.ID)
	}
	if got.Title != "my-sess" {
		t.Errorf("title = %q", got.Title)
	}
}

func TestServer_GetSession_NotFound(t *testing.T) {
	ts, cleanup := setupTestServer(t, "hi")
	defer cleanup()

	resp, err := http.Get(ts.URL + "/v1/sessions/sess_nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, 期望 404", resp.StatusCode)
	}
}

func TestServer_ChatByID_FullFlow(t *testing.T) {
	ts, cleanup := setupTestServer(t, "Hello from fake")
	defer cleanup()

	// 1. 创建 session
	resp, _ := http.Post(ts.URL+"/v1/sessions", "application/json",
		strings.NewReader(`{"title": "chat-test"}`))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var created SessionInfo
	json.Unmarshal(body, &created)

	// 2. 用 session_id 调 chat
	chatBody := `{"message": "hi"}`
	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /v1/sessions/%s/chat HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		created.ID,
		strings.TrimPrefix(ts.URL, "http://"),
		len(chatBody),
		chatBody)

	// 读 response
	br := bufio.NewReader(conn)
	statusLine, _ := br.ReadString('\n')
	if !strings.Contains(statusLine, "200") {
		t.Fatalf("status = %s", statusLine)
	}
	// 跳 headers
	for {
		line, _ := br.ReadString('\n')
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	// 读 SSE body
	body, _ = io.ReadAll(br)
	bodyStr := string(body)

	// 验证有 session event + text event
	if !strings.Contains(bodyStr, "event: session") {
		t.Errorf("应含 session event: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "event: text") {
		t.Errorf("应含 text event: %s", bodyStr)
	}
	// session_id 应是创建的那个
	if !strings.Contains(bodyStr, created.ID) {
		t.Errorf("应含 session_id %s: %s", created.ID, bodyStr)
	}
}

func TestServer_ChatByID_RequiresExistingSession(t *testing.T) {
	ts, cleanup := setupTestServer(t, "hi")
	defer cleanup()

	resp, err := http.Post(ts.URL+"/v1/sessions/sess_nonexistent/chat", "application/json",
		strings.NewReader(`{"message": "hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("不存在 session 应 404, got %d", resp.StatusCode)
	}
}

func TestServer_Sessions_MethodNotAllowed(t *testing.T) {
	ts, cleanup := setupTestServer(t, "hi")
	defer cleanup()

	// DELETE 不支持
	req, _ := http.NewRequest("DELETE", ts.URL+"/v1/sessions", nil)
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Errorf("DELETE 应 405, got %d", resp.StatusCode)
	}
}

// helpers：避免 unused
var _ = llm.RawChunk{}
var _ = tool.Definition{}
var _ = toolcall.Strategy(nil)
var _ = context.Background
