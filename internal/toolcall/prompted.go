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
	// toolIDs 是 Prepare 注入的已知工具名集合，供 fallback 校验（v1.12）。
	// 命名不在注册表的会被拒绝，避免误伤用户让模型输出的 JSON 文本。
	toolIDs map[string]struct{}
}

// NewPrompted 创建 Prompted strategy
func NewPrompted() *Prompted {
	return &Prompted{}
}

// Name 返回 strategy 名
func (p *Prompted) Name() string { return "prompted" }

// Prepare 在 system prompt 注入 tool 使用说明 + JSON schemas。
// v1.12 起同步：
//   - 构建 toolIDs 集合供 EOF 时 fallback 校验
//   - 追加 markdown 包裹指引，把"想输出 JSON 文本"引导到代码块路径
func (p *Prompted) Prepare(req *llm.ChatRequest, tools []tool.Definition) error {
	if len(tools) == 0 {
		// 没工具时清空 toolIDs，避免上一次 Prepare 的残留导致 fallback 误命中
		p.toolIDs = nil
		return nil
	}

	// 记录本轮已注册工具名（fallback 校验用）
	p.toolIDs = make(map[string]struct{}, len(tools))
	for _, t := range tools {
		p.toolIDs[t.ID] = struct{}{}
	}

	var sb strings.Builder
	sb.WriteString("You have access to the following tools. To call a tool, output a JSON block wrapped in <tool_call></tool_call> tags:\n\n")
	for _, t := range tools {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", t.ID, t.Description))
		if len(t.JSONSchema) > 0 {
			sb.WriteString(fmt.Sprintf("  Arguments schema: %s\n", string(t.JSONSchema)))
			// v1.12：强调字段类型，避免小模型把 string 字段写成 array 等
			if types := extractFieldTypes(t.JSONSchema); types != "" {
				sb.WriteString(fmt.Sprintf("  Field types (STRICT, must match): %s\n", types))
			}
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Example:\n")
	sb.WriteString("<tool_call>\n")
	sb.WriteString(`{"name": "tool_id", "arguments": {"arg1": "value"}}\n`)
	sb.WriteString("</tool_call>\n")

	// v1.12：让模型知道"想要 JSON 文本"应走 markdown 代码块包裹，与工具调用语法隔离。
	sb.WriteString("\nImportant: When the user asks you to output JSON-formatted text (not call a tool), " +
		"wrap your response in a markdown code fence ```json ... ``` to avoid ambiguity with tool call syntax.\n")

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
// 流结束（finish chunk 或 channel close）时若 buf 残留内容，先尝试 fallback
// 解析（v1.12），未命中再当文本流——小模型漏写 <tool_call> 包裹时自救。
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

		// flushAtEOF 在 EOF 时统一处理残留 buf：先尝试 fallback 解析，
		// 命中当 tool call，否则当文本流。返回 false 表示 ctx 已取消。
		flushAtEOF := func(usage llm.Usage) bool {
			if s.inCall {
				// tool_call 块被截断（缺 </tool_call>），记录警告并丢弃残留 JSON
				slog.Warn("prompted: tool_call 块未闭合，丢弃残留", "buf", s.buf.String())
				return em.Event(llm.FinishEvent{Reason: "stop", Usage: usage})
			}
			remaining := strings.TrimSpace(s.buf.String())
			if remaining == "" {
				return em.Event(llm.FinishEvent{Reason: "stop", Usage: usage})
			}
			// v1.12 fallback：模型漏了 <tool_call> 包裹但意图明确时自救
			if name, args, ok := tryFallbackToolCall(remaining, p.toolIDs); ok {
				slog.Info("prompted: fallback 解析裸 JSON 为 tool call",
					"name", name, "args", string(args))
				if !em.Event(llm.ToolCallEnd{
					CallID: fmt.Sprintf("call_%d", p.callSeq.Add(1)),
					Name:   name,
					Args:   args,
				}) {
					return false
				}
				return em.Event(llm.FinishEvent{Reason: "stop", Usage: usage})
			}
			// fallback 不命中 → 当文本流
			if !em.Text(remaining) {
				return false
			}
			return em.Event(llm.FinishEvent{Reason: "stop", Usage: usage})
		}

		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-raw:
				if !ok {
					_ = flushAtEOF(llm.Usage{})
					return
				}
				if chunk.Type == "text" {
					s.buf.WriteString(chunk.Data)
				} else if chunk.Type == "finish" {
					var usage llm.Usage
					if chunk.Data != "" {
						_ = json.Unmarshal([]byte(chunk.Data), &usage)
					}
					_ = flushAtEOF(usage)
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
