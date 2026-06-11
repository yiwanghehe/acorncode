// Package toolcall - native.go
// 参考: docs/acorncode-architect.md §8.3
package toolcall

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"acorncode/internal/llm"
	"acorncode/internal/tool"
)

// Native 是 Ollama/Anthropic/OpenAI 等带原生 tool_call API 的 strategy。
// 它把 Provider 的 RawChunk 流（type="text"/"tool_call"/"finish"/"error"）
// 翻译为类型化的 StreamEvent 流。
type Native struct {
	callSeq atomic.Uint64 // 生成唯一 call_id
}

// NewNative 创建 Native strategy
func NewNative() *Native {
	return &Native{}
}

// Name 返回 strategy 名称
func (n *Native) Name() string { return "native" }

// Prepare 是 Native 策略的预处理：对 native 工具调用，tools 已经在
// ChatRequest.Tools 里由 Provider 直接处理，v1 不需要额外动作。
func (n *Native) Prepare(req *llm.ChatRequest, tools []tool.Definition) error {
	return nil
}

// ParseStream 把 raw chunk 流转为 typed event 流
//
// Native 模式下的映射（Ollama 协议）：
//
//	"text"      → TextDelta{Text}
//	"tool_call" → ToolCallEnd{CallID, Name, Args}（Ollama 一次给完整对象）
//	"finish"    → FinishEvent{Reason, Usage}
//	"error"     → ErrorEvent{Err}
func (n *Native) ParseStream(ctx context.Context, raw <-chan llm.RawChunk) <-chan llm.StreamEvent {
	out := make(chan llm.StreamEvent, 64)
	go func() {
		defer close(out)
		em := newEmitter(ctx, out)
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-raw:
				if !ok {
					return
				}
				if ev, emit := n.translate(chunk); emit {
					if !em.Event(ev) {
						return
					}
				}
			}
		}
	}()
	return out
}

// translate 把一个 chunk 转为 event。返 (event, true) 表示需要 emit。
func (n *Native) translate(chunk llm.RawChunk) (llm.StreamEvent, bool) {
	switch chunk.Type {
	case "text":
		if chunk.Data == "" {
			return nil, false
		}
		return llm.TextDelta{Text: chunk.Data}, true

	case "tool_call":
		// Ollama 的 tool_call 在一个 chunk 里是完整对象，不需要 start/delta
		// 我们生成自己的 call_id（Ollama 不提供）
		callID := fmt.Sprintf("call_%d", n.callSeq.Add(1))

		// 从 Meta 拿 name
		name := ""
		if m, ok := chunk.Meta.(map[string]any); ok {
			if v, ok := m["name"].(string); ok {
				name = v
			}
		}

		// Args 是已经序列化的 JSON 对象
		var args json.RawMessage
		if chunk.Data != "" {
			args = json.RawMessage(chunk.Data)
		} else {
			args = json.RawMessage("{}")
		}

		return llm.ToolCallEnd{
			CallID: callID,
			Name:   name,
			Args:   args,
		}, true

	case "finish":
		// Data 是 usage JSON
		var usage llm.Usage
		if chunk.Data != "" {
			_ = json.Unmarshal([]byte(chunk.Data), &usage)
		}

		// Meta 里有 reason
		reason := "stop"
		if m, ok := chunk.Meta.(map[string]any); ok {
			if v, ok := m["reason"].(string); ok && v != "" {
				reason = v
			}
		}

		return llm.FinishEvent{Reason: reason, Usage: usage}, true

	case "error":
		return llm.ErrorEvent{Err: fmt.Errorf("%s", chunk.Data)}, true

	case "thinking":
		if chunk.Data == "" {
			return nil, false
		}
		return llm.ReasoningDelta{Text: chunk.Data}, true

	case "tool_call_delta":
		// Ollama 不发 delta，留作 Anthropic 扩展
		if chunk.Data == "" {
			return nil, false
		}
		return llm.ToolCallDelta{CallID: "", ArgsChunk: chunk.Data}, true
	}

	// 未知类型忽略
	return nil, false
}

// RetryHint 解析失败时构造给模型的"自纠正"消息对
func (n *Native) RetryHint(failed FailedCall, _ []tool.Definition) (llm.Message, llm.Message) {
	return buildRetryHint(failed, retryHints{
		JSONParse: "Your last tool call was malformed JSON: %s. Output a single valid <tool_call>{...}</tool_call> block.",
		Schema:    "Your last tool call didn't match the schema: %s. Check field names and types.",
		Unknown:   "You called a tool that doesn't exist: %s. Available tools: read, edit, bash, ...",
	})
}
