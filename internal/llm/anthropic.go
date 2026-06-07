// Package llm - Anthropic Claude provider（v1.0.2 真实实现）
//
// 协议：Anthropic Messages API
//
//	POST https://api.anthropic.com/v1/messages
//	Headers:
//	  x-api-key: <key>
//	  anthropic-version: 2023-06-01
//	  content-type: application/json
//	Body: {
//	  "model": "claude-3-5-sonnet-latest",
//	  "max_tokens": 1024,
//	  "system": "...",                  // 独立 system 字段（Ollama 放 messages 里）
//	  "messages": [{"role": "user", "content": "..."}],
//	  "tools": [{                       // tool 定义（Ollama 是 type:function / function:{}）
//	    "name": "read",
//	    "description": "...",
//	    "input_schema": {...}
//	  }],
//	  "stream": true
//	}
//
// 响应：SSE
//
//	event: message_start\n
//	data: {"type":"message_start","message":{...}}\n\n
//	event: content_block_start\n
//	data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}\n\n
//	event: content_block_delta\n
//	data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}\n\n
//	event: content_block_stop\n
//	data: {"type":"content_block_stop","index":0}\n\n
//	event: content_block_start (tool)
//	data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"...","name":"read","input":{}}}\n\n
//	event: content_block_delta (input_json_delta)
//	data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\\"pa"}}\n\n
//	event: message_delta\n
//	data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}\n\n
//	event: message_stop\n
//	data: {"type":"message_stop"}\n\n
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

// AnthropicConfig 是 Anthropic provider 的配置
type AnthropicConfig struct {
	Endpoint   string        // 默认 "https://api.anthropic.com"
	APIKey     string        // 必填
	Model      string        // 默认 "claude-3-5-sonnet-latest"
	MaxTokens  int           // 默认 8192
	APIVersion string        // 默认 "2023-06-01"
	Timeout    time.Duration // HTTP 超时，默认 5 分钟
}

func (c *AnthropicConfig) applyDefaults() {
	if c.Endpoint == "" {
		c.Endpoint = "https://api.anthropic.com"
	}
	if c.Model == "" {
		c.Model = "claude-3-5-sonnet-latest"
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 8192
	}
	if c.APIVersion == "" {
		c.APIVersion = "2023-06-01"
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Minute
	}
}

// Anthropic 是 Anthropic Messages API 的 LLM provider 实现
type Anthropic struct {
	cfg    AnthropicConfig
	client *http.Client
}

func NewAnthropic(cfg AnthropicConfig) *Anthropic {
	cfg.applyDefaults()
	return &Anthropic{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// 编译时断言
var _ Provider = (*Anthropic)(nil)

// Stream 发起 SSE 流式聊天请求
func (a *Anthropic) Stream(ctx context.Context, req ChatRequest) (<-chan RawChunk, error) {
	if a.cfg.APIKey == "" {
		return nil, fmt.Errorf("anthropic: APIKey 不能为空")
	}

	body, err := a.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: 构造请求失败: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		a.cfg.Endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: 构造 HTTP 请求失败: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", a.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", a.cfg.APIVersion)
	httpReq.Header.Set("accept", "text/event-stream")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: HTTP 失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic: 状态 %d: %s", resp.StatusCode, string(errBody))
	}

	out := make(chan RawChunk, 64)
	go a.parseSSE(ctx, resp.Body, out)
	return out, nil
}

// buildRequestBody 把 ChatRequest 转为 Anthropic body
func (a *Anthropic) buildRequestBody(req ChatRequest) ([]byte, error) {
	// system: 第一个 system section；其他忽略（或拼一起，Anthropic 接受 string 或 []）
	system := ""
	if len(req.System) > 0 {
		system = strings.Join(req.System, "\n\n")
	}

	// messages: OpenAI 风格的 [user/assistant, content] → Anthropic 风格
	// v1.0.2 简化：不处理 tool_result（tool 调用结果在 tool message 里）
	// 当前 ChatRequest.History 只能放 text message，所以这里只翻 role + content
	msgs := make([]map[string]any, 0, len(req.History))
	for _, m := range req.History {
		msgs = append(msgs, map[string]any{
			"role":    m.Role,
			"content": m.Content,
		})
	}

	body := map[string]any{
		"model":      req.Model.ID,
		"max_tokens": a.cfg.MaxTokens,
		"messages":   msgs,
		"stream":     true,
	}
	if system != "" {
		body["system"] = system
	}
	if len(req.Tools) > 0 {
		body["tools"] = a.convertTools(req.Tools)
	}

	return json.Marshal(body)
}

// convertTools 把 OpenAI function 风格 → Anthropic tools 风格
//
// OpenAI:  {"type":"function","function":{"name":"read","description":"...","parameters":{...}}}
// Anthropic: {"name":"read","description":"...","input_schema":{...}}
func (a *Anthropic) convertTools(tools []Definition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		// input_schema 必须是 object 类型
		schema := json.RawMessage(t.JSONSchema)
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, map[string]any{
			"name":         t.ID,
			"description":  t.Description,
			"input_schema": schema,
		})
	}
	return out
}

// parseSSE 把 SSE 流解析为 RawChunk
func (a *Anthropic) parseSSE(ctx context.Context, body io.Reader, out chan<- RawChunk) {
	defer close(out)

	// 1MB buffer 装长 thinking/tool input
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	// 当前 content_block 状态
	type block struct {
		Type   string // "text" | "tool_use"
		ID     string
		Name   string
		Buffer strings.Builder // tool_use 的 input 累积
	}
	blocks := make(map[int]*block)
	stopReason := ""
	outputTokens := 0
	finishReason := "stop"

	for scanner.Scan() {
		// ctx 优先
		if err := ctx.Err(); err != nil {
			out <- RawChunk{Type: "error", Data: "ctx 取消: " + err.Error()}
			return
		}

		line := scanner.Text()
		// SSE: 空行是分隔符
		if line == "" {
			continue
		}
		// event: 行
		if strings.HasPrefix(line, "event: ") {
			// 我们靠 data: 行的 type 字段判断，event: 行忽略
			continue
		}
		// data: 行
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var ev map[string]any
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			slog.Warn("anthropic: SSE JSON 解析失败", "err", err, "data", data)
			continue
		}
		evType, _ := ev["type"].(string)

		switch evType {
		case "message_start":
			// 初始化
			blocks = make(map[int]*block)
			stopReason = ""

		case "content_block_start":
			cb, _ := ev["content_block"].(map[string]any)
			idx, _ := ev["index"].(float64)
			cbType, _ := cb["type"].(string)
			b := &block{Type: cbType}
			if cbType == "tool_use" {
				b.ID, _ = cb["id"].(string)
				b.Name, _ = cb["name"].(string)
			}
			blocks[int(idx)] = b

		case "content_block_delta":
			delta, _ := ev["delta"].(map[string]any)
			idx, _ := ev["index"].(float64)
			b := blocks[int(idx)]
			if b == nil {
				continue
			}
			deltaType, _ := delta["type"].(string)
			switch deltaType {
			case "text_delta":
				text, _ := delta["text"].(string)
				b.Buffer.WriteString(text)
			case "input_json_delta":
				chunk, _ := delta["partial_json"].(string)
				b.Buffer.WriteString(chunk)
			}

		case "content_block_stop":
			idx, _ := ev["index"].(float64)
			b := blocks[int(idx)]
			if b == nil {
				continue
			}
			switch b.Type {
			case "text":
				// 完整文本：发一个 tool_call? 不，文本发 "text" chunk
				text := b.Buffer.String()
				if text != "" {
					out <- RawChunk{Type: "text", Data: text}
				}
			case "tool_use":
				// 工具 input 是累积的 partial_json
				argsStr := b.Buffer.String()
				if argsStr == "" {
					argsStr = "{}"
				}
				meta := map[string]any{
					"id":   b.ID,
					"name": b.Name,
				}
				out <- RawChunk{
					Type: "tool_call",
					Data: argsStr,
					Meta: meta,
				}
			}

		case "message_delta":
			delta, _ := ev["delta"].(map[string]any)
			if sr, ok := delta["stop_reason"].(string); ok {
				stopReason = sr
			}
			// usage
			if u, ok := ev["usage"].(map[string]any); ok {
				if ot, ok := u["output_tokens"].(float64); ok {
					outputTokens = int(ot)
				}
			}

		case "message_stop":
			// 流结束
			switch stopReason {
			case "end_turn":
				finishReason = "stop"
			case "tool_use":
				finishReason = "tool_use"
			case "max_tokens":
				finishReason = "length"
			}
			usage := Usage{OutputTokens: outputTokens}
			out <- RawChunk{
				Type: "finish",
				Meta: usage,
				Data: finishReason,
			}

		case "error":
			// Anthropic 错误事件
			errMsg := data
			if e, ok := ev["error"].(map[string]any); ok {
				if m, ok := e["message"].(string); ok {
					errMsg = m
				}
			}
			out <- RawChunk{Type: "error", Data: errMsg}
		}
	}

	if err := scanner.Err(); err != nil {
		out <- RawChunk{Type: "error", Data: "SSE 读失败: " + err.Error()}
	}
}

// ListModels 返回硬编码列表（Anthropic API 暂无 list endpoint）
func (a *Anthropic) ListModels(ctx context.Context) ([]string, error) {
	return []string{
		"claude-3-5-sonnet-latest",
		"claude-3-5-haiku-latest",
		"claude-3-opus-latest",
		"claude-3-sonnet-20240229",
		"claude-3-haiku-20240307",
	}, nil
}
