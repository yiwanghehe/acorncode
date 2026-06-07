// Package toolcall - 工具调用策略（v1 仅 Native，stub）
// 真实设计: docs/acorncode-architect.md §8
package toolcall

import (
	"context"

	"acorncode/internal/llm"
	"acorncode/internal/tool"
)

// FailedCall 描述一次失败的工具调用（用于 RetryHint）
type FailedCall struct {
	RawText string
	Reason  string // "json_parse_error" | "schema_violation" | "unknown_tool"
	Detail  string // 给模型看的具体错误
}

// Strategy 把 Provider 的 RawChunk 流转为类型化的 StreamEvent 流
type Strategy interface {
	Name() string
	Prepare(req *llm.ChatRequest, tools []tool.Definition) error
	ParseStream(ctx context.Context, raw <-chan llm.RawChunk) <-chan llm.StreamEvent
	RetryHint(failed FailedCall, tools []tool.Definition) (asst, user llm.Message)
}
