// Package toolcall - prompted.go
//
// Prompted 策略用于**不支持原生 tool_call API 的小模型**。
// 模型输出纯文本，工具调用用 XML-like 块包起来：
//
//	<tool_call>
//	{"name": "read", "arguments": {"path": "/etc/hosts"}}
//	</tool_call>
//
// v1.0.5 实现：流式解析，识别到完整 tool_call 块即 emit ToolCallEnd。
// 不支持嵌套（<tool_call> 里不能有 <tool_call>）。
package toolcall

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"

	"acorncode/internal/llm"
	"acorncode/internal/tool"
)

// Prompted 是 Prompted toolcall 策略
type Prompted struct {
	callSeq atomic.Uint64
}

// NewPrompted 创建 Prompted strategy
func NewPrompted() *Prompted {
	return &Prompted{}
}

// Name 返回 strategy 名
func (p *Prompted) Name() string { return "prompted" }

// Prepare 在 system prompt 注入 tool 使用说明 + JSON schemas
func (p *Prompted) Prepare(req *llm.ChatRequest, tools []tool.Definition) error {
	if len(tools) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("You have access to the following tools. To call a tool, output a JSON block wrapped in <tool_call></tool_call> tags:\n\n")
	for _, t := range tools {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", t.ID, t.Description))
		if len(t.JSONSchema) > 0 {
			sb.WriteString(fmt.Sprintf("  Arguments schema: %s\n", string(t.JSONSchema)))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Example:\n")
	sb.WriteString("<tool_call>\n")
	sb.WriteString(`{"name": "tool_id", "arguments": {"arg1": "value"}}\n`)
	sb.WriteString("</tool_call>\n")

	req.System = append(req.System, sb.String())
	return nil
}

var (
	toolCallStartRe = regexp.MustCompile(`<tool_call>`)
	toolCallEndRe   = regexp.MustCompile(`</tool_call>`)
)

// ParseStream 流式解析 raw chunk → typed event
//
// 状态机：
//   - 普通态：扫 <tool_call>，找到时 flush 之前的 text
//   - 工具态：累积 JSON 文本，扫 </tool_call>，找到时 parse + emit
//
// 流式：partial `<tool_call>` / `</tool_call>` 可能跨 chunk，扫到才处理。
func (p *Prompted) ParseStream(ctx context.Context, raw <-chan llm.RawChunk) <-chan llm.StreamEvent {
	out := make(chan llm.StreamEvent, 64)

	type state struct {
		buf    strings.Builder
		inCall bool
		callID string
	}

	go func() {
		defer close(out)
		s := &state{}
		em := newEmitter(ctx, out)

		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-raw:
				if !ok {
					// 流结束：flush 残留 text
					if !s.inCall {
						em.Text(s.buf.String())
					}
					return
				}
				if chunk.Type == "text" {
					s.buf.WriteString(chunk.Data)
				} else if chunk.Type == "finish" {
					// 流结束
					if !s.inCall {
						em.Text(s.buf.String())
					}
					var usage llm.Usage
					if chunk.Data != "" {
						_ = json.Unmarshal([]byte(chunk.Data), &usage)
					}
					em.Event(llm.FinishEvent{Reason: "stop", Usage: usage})
					return
				} else if chunk.Type == "error" {
					em.Event(llm.ErrorEvent{Err: fmt.Errorf("%s", chunk.Data)})
					return
				}
				// 其他类型（tool_call 等）忽略

				// 处理 buf
				for s.buf.Len() > 0 {
					if !s.inCall {
						loc := toolCallStartRe.FindStringIndex(s.buf.String())
						if loc == nil {
							break
						}
						// flush text 在 <tool_call> 之前
						if loc[0] > 0 {
							if !em.Text(s.buf.String()[:loc[0]]) {
								return
							}
						}
						// 进入工具态
						s.inCall = true
						s.callID = fmt.Sprintf("call_%d", p.callSeq.Add(1))
						// 切 buf 留下 <tool_call> 之后的内容
						remainder := s.buf.String()[loc[1]:]
						s.buf.Reset()
						s.buf.WriteString(remainder)
					} else {
						loc := toolCallEndRe.FindStringIndex(s.buf.String())
						if loc == nil {
							break
						}
						// 提取 JSON（在 </tool_call> 之前）
						jsonStr := strings.TrimSpace(s.buf.String()[:loc[0]])
						// 切 buf 留下 </tool_call> 之后的内容
						remainder := ""
						if loc[1] < len(s.buf.String()) {
							remainder = s.buf.String()[loc[1]:]
						}
						s.buf.Reset()
						s.buf.WriteString(remainder)
						s.inCall = false

						// parse JSON
						var call struct {
							Name      string          `json:"name"`
							Arguments json.RawMessage `json:"arguments"`
						}
						if err := json.Unmarshal([]byte(jsonStr), &call); err != nil {
							slog.Warn("prompted: tool_call JSON 解析失败", "err", err, "json", jsonStr)
							continue
						}
						if call.Arguments == nil {
							call.Arguments = json.RawMessage("{}")
						}
						if !em.Event(llm.ToolCallEnd{
							CallID: s.callID,
							Name:   call.Name,
							Args:   call.Arguments,
						}) {
							return
						}
						s.callID = ""
					}
				}
			}
		}
	}()
	return out
}

// RetryHint 解析失败时给模型提示
func (p *Prompted) RetryHint(failed FailedCall, _ []tool.Definition) (llm.Message, llm.Message) {
	return buildRetryHint(failed, retryHints{
		JSONParse: "Your last <tool_call> block was malformed JSON: %s. Re-output a single valid <tool_call>{...}</tool_call> block.",
		Schema:    "Your last tool call didn't match the schema: %s. Check field names and types.",
		Unknown:   "You called a tool that doesn't exist: %s. Available: read, edit, bash, ...",
	})
}
