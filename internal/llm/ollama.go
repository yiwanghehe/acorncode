// Package llm - Ollama provider（真实实现）
// 参考: https://github.com/ollama/ollama/blob/main/docs/api.md
//
// Ollama /api/chat 协议：
//
//	POST /api/chat
//	请求:  {"model": "...", "messages": [...], "tools": [...], "stream": true}
//	响应:  NDJSON，每行一个 JSON 对象：
//
//	  {"model":"...","created_at":"...","message":{"role":"assistant","content":"hi","tool_calls":[...]},"done":false}
//	  {"model":"...","created_at":"...","message":{...},"done":true,"total_duration":...,"eval_count":N}
//
// 说明：
//   - Ollama 0.1.14+ 支持 system role
//   - 工具调用在每个 chunk 里是完整对象（非流式 delta）
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// OllamaConfig 是 Ollama provider 的配置
type OllamaConfig struct {
	Endpoint string        // 默认 "http://localhost:11434"
	Model    string        // 默认 "qwen2.5-coder:7b"
	Timeout  time.Duration // HTTP 超时，默认 5 分钟
	APIKey   string        // 可选，作为 Authorization: Bearer 发送
}

func (c *OllamaConfig) applyDefaults() {
	if c.Endpoint == "" {
		c.Endpoint = "http://localhost:11434"
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Minute
	}
}

// Ollama 是 Ollama HTTP API 的 LLM provider 实现
type Ollama struct {
	cfg    OllamaConfig
	client *http.Client
}

// NewOllama 创建 Ollama provider 实例
func NewOllama(cfg OllamaConfig) *Ollama {
	cfg.applyDefaults()
	return &Ollama{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// 编译时断言：Ollama 必须实现 Provider 接口
var _ Provider = (*Ollama)(nil)

// ============================================================================
// Stream
// ============================================================================

// Stream 发起流式聊天请求，返回原始 chunk 通道。
// 通道在流结束时关闭（成功或错误都关闭），由调用方负责消费到 close。
func (o *Ollama) Stream(ctx context.Context, req ChatRequest) (<-chan RawChunk, error) {
	body, err := o.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: 构造请求失败: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		o.cfg.Endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: 创建请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	}

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: HTTP 请求失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("ollama: 状态码 %d: %s", resp.StatusCode, string(body))
	}

	out := make(chan RawChunk, 64)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		o.parseStream(ctx, resp.Body, out)
	}()
	return out, nil
}

// ============================================================================
// 请求构造
// ============================================================================

type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Options  map[string]any  `json:"options,omitempty"`
	// Format 是结构化输出约束（v1.4）：Ollama 支持传 JSON Schema 强制输出格式。
	Format json.RawMessage `json:"format,omitempty"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaTool struct {
	Type     string         `json:"type"` // 固定为 "function"
	Function ollamaFunction `json:"function"`
}

type ollamaFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ollamaToolCall struct {
	Function ollamaToolCallFn `json:"function"`
}

type ollamaToolCallFn struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// buildRequestBody 把 ChatRequest 转为 Ollama 协议 JSON
func (o *Ollama) buildRequestBody(req ChatRequest) ([]byte, error) {
	body := ollamaRequest{
		Model:  firstNonEmpty(req.Model.ID, o.cfg.Model),
		Stream: true,
		Format: req.Format, // v1.4：结构化输出约束（为空则 omitempty 不发）
	}

	// system prompt：多条用双换行拼接
	if len(req.System) > 0 {
		body.Messages = append(body.Messages, ollamaMessage{
			Role:    "system",
			Content: strings.Join(req.System, "\n\n"),
		})
	}

	// 历史消息
	for _, m := range req.History {
		body.Messages = append(body.Messages, ollamaMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	// 工具列表
	for _, t := range req.Tools {
		if t.ID == "" {
			continue
		}
		// Ollama 要求 parameters 是 JSON 对象，空则传 {}
		params := t.JSONSchema
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		body.Tools = append(body.Tools, ollamaTool{
			Type: "function",
			Function: ollamaFunction{
				Name:        t.ID,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}

	return json.Marshal(body)
}

// firstNonEmpty 返回第一个非空字符串
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ============================================================================
// 响应解析
// ============================================================================

type ollamaResponse struct {
	Model      string        `json:"model"`
	CreatedAt  string        `json:"created_at"`
	Message    ollamaMessage `json:"message"`
	Done       bool          `json:"done"`
	DoneReason string        `json:"done_reason,omitempty"`

	// 最终统计（仅在 done=true 时存在）
	TotalDuration      int64 `json:"total_duration,omitempty"` // 纳秒
	LoadDuration       int64 `json:"load_duration,omitempty"`
	PromptEvalCount    int   `json:"prompt_eval_count,omitempty"` // 输入 token
	PromptEvalDuration int64 `json:"prompt_eval_duration,omitempty"`
	EvalCount          int   `json:"eval_count,omitempty"` // 输出 token
	EvalDuration       int64 `json:"eval_duration,omitempty"`

	// 错误（仅在出错时存在）
	Error string `json:"error,omitempty"`
}

// parseStream 解析 NDJSON 流，转为 RawChunk 通道
func (o *Ollama) parseStream(ctx context.Context, body io.Reader, out chan<- RawChunk) {
	scanner := bufio.NewScanner(body)
	// 单行最大 4MB（Ollama 长 thinking 输出可能很大）
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		// 优先响应 ctx 取消
		if err := ctx.Err(); err != nil {
			o.sendError(out, err)
			return
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var resp ollamaResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			// 一行解析失败只 warn，不中断流
			slog.Warn("ollama: NDJSON 行解析失败",
				"err", err,
				"line_preview", preview(line, 200),
			)
			continue
		}

		// 服务端错误：发出并停止
		if resp.Error != "" {
			out <- RawChunk{Type: "error", Data: resp.Error}
			return
		}

		// 文本内容（只有 tool_call 时可能为空）
		if resp.Message.Content != "" {
			out <- RawChunk{
				Type: "text",
				Data: resp.Message.Content,
			}
		}

		// 工具调用（完整对象，非 delta）
		for _, tc := range resp.Message.ToolCalls {
			args := tc.Function.Arguments
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			out <- RawChunk{
				Type: "tool_call",
				Data: string(args),
				Meta: map[string]any{
					"name": tc.Function.Name,
				},
			}
		}

		// 最终 chunk：发出 finish 事件
		if resp.Done {
			usage := Usage{
				InputTokens:  resp.PromptEvalCount,
				OutputTokens: resp.EvalCount,
			}
			usageJSON, _ := json.Marshal(usage)
			reason := resp.DoneReason
			if reason == "" {
				reason = "stop"
			}
			out <- RawChunk{
				Type: "finish",
				Data: string(usageJSON),
				Meta: map[string]any{
					"reason":            reason,
					"total_duration_ms": resp.TotalDuration / 1e6,
					"load_duration_ms":  resp.LoadDuration / 1e6,
				},
			}
		}
	}

	if err := scanner.Err(); err != nil {
		// ctx cancel 引发的读取错误不算 stream 错误
		if ctx.Err() == nil {
			o.sendError(out, fmt.Errorf("ollama: 读流失败: %w", err))
		}
	}
}

// sendError 尝试发错误 chunk，channel 满/关时只能 log
func (o *Ollama) sendError(out chan<- RawChunk, err error) {
	select {
	case out <- RawChunk{Type: "error", Data: err.Error()}:
	default:
		slog.Error("ollama: 错误 chunk 发送失败（channel 满或已关）", "err", err)
	}
}

// preview 返回字节切片前 n 字节的字符串表示
func preview(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// ============================================================================
// ListModels
// ============================================================================

// ListModels 调用 /api/tags 列出可用模型名
func (o *Ollama) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET",
		o.cfg.Endpoint+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("ollama: 创建请求失败: %w", err)
	}
	if o.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	}

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: 列表失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: 列表状态 %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama: 列表解码失败: %w", err)
	}

	names := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	return names, nil
}
