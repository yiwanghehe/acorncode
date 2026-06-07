// Package toolcall - grammar.go
//
// Grammar 策略（v1.0.6）= Prompted + JSON Schema 验证。
// 解析 `<tool_call>{...}</tool_call>` 块，对 name / arguments 做严格校验。
// 失败时不 emit 工具调用（让 Loop 走 tool 错误路径，模型看到 stderr 重试）。
//
// **v1.0.6 范围**：JSON Schema 验证（无 GBNF 生成）。
// **v1.0.7 计划**：完整 schema→GBNF 转换器 + Ollama `format` 字段约束。
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

// Grammar 是带 JSON Schema 验证的 Prompted 变体
type Grammar struct {
	callSeq atomic.Uint64
	tools   []tool.Definition // Prepare 时存
}

// NewGrammar 创建 Grammar 策略
func NewGrammar() *Grammar {
	return &Grammar{}
}

// Name 返回 strategy 名
func (g *Grammar) Name() string { return "grammar" }

// Prepare 存 tool defs（v1.0.6 仅存，不注入 system prompt）
//
// v1.0.6 不修改 system prompt：schema 验证在 ParseStream 时做。
// v1.0.7 起会拼 schema 到 system prompt 让模型预先知道。
func (g *Grammar) Prepare(req *llm.ChatRequest, tools []tool.Definition) error {
	g.tools = tools
	return nil
}

var (
	grammarStartRe = regexp.MustCompile(`<tool_call>`)
	grammarEndRe   = regexp.MustCompile(`</tool_call>`)
)

// ParseStream 流式解析 + schema 验证
//
// 与 Prompted 相同的 2 状态机 + 额外 schema 校验。
func (g *Grammar) ParseStream(ctx context.Context, raw <-chan llm.RawChunk) <-chan llm.StreamEvent {
	out := make(chan llm.StreamEvent, 64)

	type state struct {
		buf    strings.Builder
		inCall bool
		callID string
	}

	go func() {
		defer close(out)
		s := &state{}

		emitText := func(text string) bool {
			if text == "" {
				return true
			}
			select {
			case out <- llm.TextDelta{Text: text}:
				return true
			case <-ctx.Done():
				return false
			}
		}
		emit := func(ev llm.StreamEvent) bool {
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-raw:
				if !ok {
					if !s.inCall {
						emitText(s.buf.String())
					}
					return
				}
				if chunk.Type == "text" {
					s.buf.WriteString(chunk.Data)
				} else if chunk.Type == "finish" {
					if !s.inCall {
						emitText(s.buf.String())
					}
					var usage llm.Usage
					if chunk.Data != "" {
						_ = json.Unmarshal([]byte(chunk.Data), &usage)
					}
					emit(llm.FinishEvent{Reason: "stop", Usage: usage})
					return
				} else if chunk.Type == "error" {
					emit(llm.ErrorEvent{Err: fmt.Errorf("%s", chunk.Data)})
					return
				}

				for s.buf.Len() > 0 {
					if !s.inCall {
						loc := grammarStartRe.FindStringIndex(s.buf.String())
						if loc == nil {
							break
						}
						if loc[0] > 0 {
							if !emitText(s.buf.String()[:loc[0]]) {
								return
							}
						}
						s.inCall = true
						s.callID = fmt.Sprintf("call_%d", g.callSeq.Add(1))
						remainder := s.buf.String()[loc[1]:]
						s.buf.Reset()
						s.buf.WriteString(remainder)
					} else {
						loc := grammarEndRe.FindStringIndex(s.buf.String())
						if loc == nil {
							break
						}
						jsonStr := strings.TrimSpace(s.buf.String()[:loc[0]])
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
							slog.Warn("grammar: tool_call JSON 解析失败", "err", err, "json", jsonStr)
							continue
						}

						// v1.0.6 核心：schema 验证
						if err := g.validateCall(call.Name, call.Arguments); err != nil {
							slog.Warn("grammar: tool_call schema 验证失败",
								"tool", call.Name, "err", err)
							continue
						}

						if call.Arguments == nil {
							call.Arguments = json.RawMessage("{}")
						}
						if !emit(llm.ToolCallEnd{
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

// validateCall 验证 (tool_name, arguments) 是否合法
//
// 检查：
//  1. name 必须在 g.tools 里
//  2. arguments 必须是合法 JSON 对象
//  3. arguments 必须匹配 tool 的 JSONSchema（best effort）
func (g *Grammar) validateCall(name string, args json.RawMessage) error {
	// 1. 找 tool
	var found *tool.Definition
	for i := range g.tools {
		if g.tools[i].ID == name {
			found = &g.tools[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("unknown tool: %s", name)
	}

	// 2. arguments 必须是对象（除非 schema 显式允许其他）
	if len(args) == 0 {
		return nil // 空 args 视作 {}
	}
	var raw map[string]any
	if err := json.Unmarshal(args, &raw); err != nil {
		return fmt.Errorf("arguments not a JSON object: %w", err)
	}

	// 3. best-effort schema 校验（v1.0.6：只校验 required 字段）
	if len(found.JSONSchema) == 0 {
		return nil
	}
	var schema struct {
		Required []string `json:"required"`
		// Properties 简化：只取 type
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(found.JSONSchema, &schema); err != nil {
		// schema 本身坏：信任工具，跳过
		return nil
	}
	for _, req := range schema.Required {
		if _, ok := raw[req]; !ok {
			return fmt.Errorf("missing required field: %s", req)
		}
	}
	// 类型校验（粗略）
	for k, v := range raw {
		prop, ok := schema.Properties[k]
		if !ok {
			continue // 额外字段允许
		}
		if !typeMatches(v, prop.Type) {
			return fmt.Errorf("field %s: expected %s", k, prop.Type)
		}
	}
	return nil
}

// typeMatches 检查 value 是否匹配 JSON Schema type
func typeMatches(v any, typ string) bool {
	if typ == "" {
		return true
	}
	switch typ {
	case "string":
		_, ok := v.(string)
		return ok
	case "number", "integer":
		_, ok := v.(float64)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	}
	return true // 未知 type 信任
}

// RetryHint 校验失败时给模型提示
func (g *Grammar) RetryHint(failed FailedCall, _ []tool.Definition) (llm.Message, llm.Message) {
	asst := llm.Message{Role: "assistant", Content: failed.RawText}
	var hint string
	switch failed.Reason {
	case "json_parse_error":
		hint = fmt.Sprintf("Your last <tool_call> block was malformed JSON: %s. Re-output a single valid <tool_call>{\"name\":\"<id>\",\"arguments\":{...}}</tool_call> block.", failed.Detail)
	case "schema_violation":
		hint = fmt.Sprintf("Your last tool call failed schema validation: %s. Check required fields and types.", failed.Detail)
	case "unknown_tool":
		hint = fmt.Sprintf("You called a tool that doesn't exist: %s. Available: read, edit, bash, ...", failed.Detail)
	default:
		hint = "Your last tool call failed: " + failed.Detail
	}
	user := llm.Message{Role: "user", Content: hint}
	return asst, user
}
