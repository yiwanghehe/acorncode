// Package mcp - client_test.go
//
// 测试策略：用「re-exec 自身」模式起一个 mock MCP server 子进程。
// 测试二进制带环境变量 MCP_MOCK_SERVER=1 重新执行时，进入 mockServerMain，
// 从 stdin 读 JSON-RPC、按协议回包。这样无需任何外部依赖即可端到端测真实 stdio。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"acorncode/internal/permission"
	"acorncode/internal/tool"
)

// TestMain 拦截 MCP_MOCK_SERVER=1，把测试二进制变成 mock server。
func TestMain(m *testing.M) {
	switch os.Getenv("MCP_MOCK_SERVER") {
	case "1":
		mockServerMain(false) // 正常 server
		return
	case "broken":
		mockServerMain(true) // 工具调用返回 isError
		return
	case "crash":
		os.Exit(1) // 立即退出，模拟启动崩溃
		return
	}
	os.Exit(m.Run())
}

// mockServerMain 是一个最小 MCP server：支持 initialize / tools/list / tools/call。
func mockServerMain(toolErr bool) {
	in := bufio.NewReader(os.Stdin)
	out := os.Stdout
	for {
		line, err := in.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			return
		}
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if e := json.Unmarshal(line, &req); e != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			writeResult(out, req.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "mock", "version": "0.0.1"},
				"capabilities":    map[string]any{},
			})
		case "notifications/initialized":
			// 通知无需响应
		case "tools/list":
			writeResult(out, req.ID, map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo",
						"description": "回显输入的 text 参数",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"text": map[string]any{"type": "string"}},
							"required":   []string{"text"},
						},
					},
				},
			})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			var args struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(p.Arguments, &args)
			writeResult(out, req.ID, map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "echo: " + args.Text},
				},
				"isError": toolErr,
			})
		default:
			writeError(out, req.ID, -32601, "method not found: "+req.Method)
		}
		if err != nil {
			return
		}
	}
}

func writeResult(w *os.File, id int64, result any) {
	raw, _ := json.Marshal(result)
	resp := map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(raw)}
	b, _ := json.Marshal(resp)
	fmt.Fprintln(w, string(b))
}

func writeError(w *os.File, id int64, code int, msg string) {
	resp := map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
	b, _ := json.Marshal(resp)
	fmt.Fprintln(w, string(b))
}

// newMockClient 起一个 re-exec 自身的 mock server client。
func newMockClient(t *testing.T, mode string) *Client {
	t.Helper()
	c, err := NewClient(context.Background(), Config{
		Name:    "mock",
		Command: os.Args[0], // 测试二进制自身
		Env:     map[string]string{"MCP_MOCK_SERVER": mode},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestClient_InitializeAndListTools(t *testing.T) {
	c := newMockClient(t, "1")
	ctx := context.Background()

	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if c.ServerInfo().Name != "mock" {
		t.Errorf("ServerInfo.Name = %q, 期望 mock", c.ServerInfo().Name)
	}

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("工具数 = %d, 期望 1", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("工具名 = %q, 期望 echo", tools[0].Name)
	}
}

func TestClient_CallTool(t *testing.T) {
	c := newMockClient(t, "1")
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	res, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Error("IsError 应为 false")
	}
	if len(res.Content) != 1 || res.Content[0].Text != "echo: hi" {
		t.Errorf("Content = %+v, 期望 'echo: hi'", res.Content)
	}
}

func TestClient_CallTool_EmptyArgs(t *testing.T) {
	c := newMockClient(t, "1")
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	// 空 args 应被补成 {}
	res, err := c.CallTool(ctx, "echo", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Content[0].Text != "echo: " {
		t.Errorf("空 args 应返 'echo: ', 实际 %q", res.Content[0].Text)
	}
}

func TestClient_UnknownMethod(t *testing.T) {
	c := newMockClient(t, "1")
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	// 直接调一个 server 不认的方法
	_, err := c.call(ctx, "nonexistent/method", map[string]any{})
	if err == nil {
		t.Fatal("未知方法应返错误")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("错误应含 'method not found': %v", err)
	}
}

func TestClient_ContextCancel(t *testing.T) {
	c := newMockClient(t, "1")
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	_, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"x"}`))
	if err == nil {
		t.Fatal("已取消的 ctx 应返错误")
	}
}

func TestClient_CrashOnStart(t *testing.T) {
	// server 立即退出，Initialize 应失败而非卡死
	c, err := NewClient(context.Background(), Config{
		Name:    "crash",
		Command: os.Args[0],
		Env:     map[string]string{"MCP_MOCK_SERVER": "crash"},
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	if err := c.Initialize(context.Background()); err == nil {
		t.Error("server 崩溃时 Initialize 应返错误")
	}
}

func TestClient_EmptyCommand(t *testing.T) {
	_, err := NewClient(context.Background(), Config{Name: "x"})
	if err == nil {
		t.Fatal("空 Command 应返错误")
	}
}

func TestClient_CloseIdempotent(t *testing.T) {
	c := newMockClient(t, "1")
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("第一次 Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("第二次 Close 应幂等: %v", err)
	}
}

func TestClient_CallAfterClose(t *testing.T) {
	c := newMockClient(t, "1")
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	_, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"x"}`))
	if err == nil {
		t.Error("Close 后调用应返错误")
	}
}

// ---- adapter 测试 ----

func TestRegisterTools(t *testing.T) {
	c := newMockClient(t, "1")
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	ids, err := RegisterTools(context.Background(), c, reg)
	if err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	if len(ids) != 1 || ids[0] != "mock_echo" {
		t.Fatalf("注册的 ID = %v, 期望 [mock_echo]", ids)
	}
	// 注册进 registry 后能 Get 到
	got, ok := reg.Get("mock_echo")
	if !ok {
		t.Fatal("registry 应有 mock_echo")
	}
	def := got.Definition()
	if def.ID != "mock_echo" {
		t.Errorf("Definition.ID = %q", def.ID)
	}
	if def.JSONSchema == nil {
		t.Error("schema 不应为 nil")
	}
}

func TestMcpTool_Execute(t *testing.T) {
	c := newMockClient(t, "1")
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	if _, err := RegisterTools(context.Background(), c, reg); err != nil {
		t.Fatal(err)
	}
	mt, _ := reg.Get("mock_echo")

	res, err := mt.Execute(context.Background(), json.RawMessage(`{"text":"world"}`), tool.Context{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	if !strings.Contains(res.Output, "echo: world") {
		t.Errorf("Output = %q, 期望含 'echo: world'", res.Output)
	}
}

func TestMcpTool_Execute_IsError(t *testing.T) {
	c := newMockClient(t, "broken") // server 返 isError=true
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	if _, err := RegisterTools(context.Background(), c, reg); err != nil {
		t.Fatal(err)
	}
	mt, _ := reg.Get("mock_echo")

	res, _ := mt.Execute(context.Background(), json.RawMessage(`{"text":"x"}`), tool.Context{})
	if res.Status != "error" {
		t.Errorf("isError=true 时 Status 应为 error, 实际 %q", res.Status)
	}
}

func TestMcpTool_PermissionDenied(t *testing.T) {
	c := newMockClient(t, "1")
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	if _, err := RegisterTools(context.Background(), c, reg); err != nil {
		t.Fatal(err)
	}
	mt, _ := reg.Get("mock_echo")

	deny := func(ctx context.Context, req permission.Request) error {
		return fmt.Errorf("denied")
	}
	res, _ := mt.Execute(context.Background(), json.RawMessage(`{"text":"x"}`), tool.Context{Ask: deny})
	if res.Status != "error" {
		t.Errorf("权限拒绝时 Status 应为 error, 实际 %q", res.Status)
	}
	if !strings.Contains(res.Output, "permission denied") {
		t.Errorf("Output 应含 'permission denied': %q", res.Output)
	}
}

func TestMcpTool_ImplementsTool(t *testing.T) {
	var _ tool.Tool = (*mcpTool)(nil)
}

func TestFlattenContent(t *testing.T) {
	out := flattenContent([]ContentBlock{
		{Type: "text", Text: "a"},
		{Type: "text", Text: "b"},
	})
	if out != "a\nb" {
		t.Errorf("flatten = %q, 期望 'a\\nb'", out)
	}
	// 非文本块标注类型
	out2 := flattenContent([]ContentBlock{{Type: "image", Text: ""}})
	if !strings.Contains(out2, "非文本内容") {
		t.Errorf("非文本块应标注: %q", out2)
	}
}
