// Package mcp 实现一个最小的 MCP（Model Context Protocol）stdio 客户端。
//
// MCP 让 AcornCode 能调用外部工具进程（如 filesystem / git / 数据库 server）。
// 通信走子进程的 stdin/stdout，协议是 JSON-RPC 2.0，每条消息一行（换行分隔）。
//
// 设计原则（见 docs/architecture.md）：
//   - 0 新第三方依赖：纯 stdlib（os/exec + encoding/json + bufio + sync）
//   - 不在此包 import LLM/tool 包（adapter.go 反过来依赖 tool 包，避免环）
//   - 失败不 panic：返回类型化错误，由调用方决定降级
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// 协议常量
const (
	// protocolVersion 是本客户端声明支持的 MCP 协议版本。
	protocolVersion = "2024-11-05"
	// jsonRPCVersion 是 JSON-RPC 版本号。
	jsonRPCVersion = "2.0"
	// defaultRequestTimeout 是单次请求默认超时。
	defaultRequestTimeout = 30 * time.Second
)

// rpcRequest 是一条 JSON-RPC 2.0 请求。
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcNotification 是无 ID 的 JSON-RPC 通知（不期待响应）。
type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse 是一条 JSON-RPC 2.0 响应。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError 是 JSON-RPC 错误对象。
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("MCP RPC 错误 %d: %s", e.Code, e.Message)
}

// ToolInfo 描述 MCP server 暴露的一个工具（来自 tools/list 响应）。
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// CallResult 是 tools/call 的结果。
type CallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

// ContentBlock 是 MCP 返回的一段内容（v1.2 仅处理 text 类型）。
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Config 是启动一个 stdio MCP server 的配置。
type Config struct {
	Name    string            // 逻辑名（用于工具前缀，如 "fs"）
	Command string            // 可执行文件（如 "npx"）
	Args    []string          // 参数
	Env     map[string]string // 额外环境变量（追加到当前环境）
	Timeout time.Duration     // 单次请求超时，0 = 默认 30s
}

// Client 是一个 stdio MCP 客户端，管理一个 server 子进程的生命周期。
type Client struct {
	cfg     Config
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	timeout time.Duration

	mu       sync.Mutex // 保护 nextID 与 pending
	nextID   int64      // 自增请求 ID
	pending  map[int64]chan rpcResponse
	closed   bool
	readDone chan struct{} // 读循环退出信号

	serverInfo ServerInfo
}

// ServerInfo 是 initialize 握手返回的 server 信息。
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// NewClient 按配置启动 MCP server 子进程并建立读循环。
// 调用方拿到 Client 后须先调 Initialize，再 ListTools。用完调 Close。
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("mcp: Command 不能为空")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	// 追加额外环境变量（继承当前进程环境）
	if len(cfg.Env) > 0 {
		env := append([]string{}, cmd.Environ()...)
		for k, v := range cfg.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	// stderr 直接丢给 server 日志（用 slog 记录），不参与 RPC
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: 启动 %q: %w", cfg.Command, err)
	}

	c := &Client{
		cfg:      cfg,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   bufio.NewReaderSize(stdout, 1024*1024), // 1MB 行缓冲，防大响应截断
		timeout:  timeout,
		pending:  make(map[int64]chan rpcResponse),
		readDone: make(chan struct{}),
	}

	go c.readLoop()
	go c.drainStderr(stderr)

	return c, nil
}

// drainStderr 把 server 的 stderr 转成 slog（避免管道写满阻塞 server）。
func (c *Client) drainStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		slog.Debug("mcp server stderr", "server", c.cfg.Name, "line", sc.Text())
	}
}

// readLoop 持续读取 server stdout 的每一行 JSON-RPC 响应，分发给等待的请求。
func (c *Client) readLoop() {
	defer close(c.readDone)
	for {
		line, err := c.stdout.ReadBytes('\n')
		if len(line) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			if err != io.EOF {
				slog.Warn("mcp 读循环退出", "server", c.cfg.Name, "err", err)
			}
			// 进程退出/管道关闭：唤醒所有等待者
			c.failAllPending(fmt.Errorf("mcp: server %q 连接关闭", c.cfg.Name))
			return
		}
	}
}

// dispatch 解析一行响应并投递给对应的 pending channel。
func (c *Client) dispatch(line []byte) {
	var resp rpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		// 坏行跳过（可能是 server 误打印的非 JSON），不杀整流
		slog.Warn("mcp 跳过坏 JSON 行", "server", c.cfg.Name, "err", err)
		return
	}
	// 无 ID 的消息（server 发来的通知）当前忽略
	if resp.ID == 0 && resp.Result == nil && resp.Error == nil {
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[resp.ID]
	if ok {
		delete(c.pending, resp.ID)
	}
	c.mu.Unlock()
	if ok {
		ch <- resp
	}
}

// failAllPending 让所有未完成请求收到错误并清空 pending。
func (c *Client) failAllPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		ch <- rpcResponse{ID: id, Error: &rpcError{Code: -1, Message: err.Error()}}
		delete(c.pending, id)
	}
}

// call 发一条请求并等待响应（带 ctx 取消 + 超时）。
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp: client 已关闭")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := rpcRequest{JSONRPC: jsonRPCVersion, ID: id, Method: method, Params: params}
	if err := c.writeMessage(req); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	timer := time.NewTimer(c.timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-timer.C:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp: %s 请求超时（%s）", method, c.timeout)
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// notify 发一条无需响应的通知。
func (c *Client) notify(method string, params any) error {
	n := rpcNotification{JSONRPC: jsonRPCVersion, Method: method, Params: params}
	return c.writeMessage(n)
}

// writeMessage 把一条消息序列化为一行 JSON 写入 stdin。
func (c *Client) writeMessage(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("mcp: 序列化: %w", err)
	}
	data = append(data, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("mcp: client 已关闭")
	}
	if _, err := c.stdin.Write(data); err != nil {
		return fmt.Errorf("mcp: 写 stdin: %w", err)
	}
	return nil
}

// Initialize 执行 MCP 握手：发 initialize 请求，再发 initialized 通知。
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "acorncode",
			"version": "1.2",
		},
	}
	raw, err := c.call(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("mcp initialize: %w", err)
	}
	var res struct {
		ServerInfo ServerInfo `json:"serverInfo"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		// 握手成功但解析 serverInfo 失败不致命，仅记日志
		slog.Warn("mcp 解析 serverInfo 失败", "server", c.cfg.Name, "err", err)
	}
	c.serverInfo = res.ServerInfo

	// 协议要求握手后发 initialized 通知
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		return fmt.Errorf("mcp initialized 通知: %w", err)
	}
	return nil
}

// ListTools 调 tools/list 拿到 server 暴露的全部工具。
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}
	var res struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcp 解析 tools/list: %w", err)
	}
	return res.Tools, nil
}

// CallTool 调 tools/call 执行指定工具。args 是工具入参（JSON 对象）。
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallResult, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	params := map[string]any{
		"name":      name,
		"arguments": args,
	}
	raw, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("mcp tools/call %q: %w", name, err)
	}
	var res CallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcp 解析 tools/call 结果: %w", err)
	}
	return &res, nil
}

// ServerInfo 返回握手时拿到的 server 信息。
func (c *Client) ServerInfo() ServerInfo {
	return c.serverInfo
}

// Name 返回此 client 的逻辑名。
func (c *Client) Name() string {
	return c.cfg.Name
}

// Close 关闭 stdin、终止子进程并等待读循环退出。幂等。
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	// 关 stdin 让 server 优雅退出
	_ = c.stdin.Close()

	// 给 server 一点退出时间，超时则强杀
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
	<-c.readDone
	return nil
}
