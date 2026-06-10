// Package toolcall - 工具调用策略（Native / Prompted / Grammar）
// 真实设计: docs/acorncode-architect.md §8
package toolcall

import (
	"context"
	"fmt"

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

// retryHints 是按失败原因分类的提示文案模板（R5：消除三策略 RetryHint 结构重复）。
// 每个字段是一个 fmt 模板，含一个 %s 占位给 failed.Detail。
type retryHints struct {
	JSONParse string // Reason == "json_parse_error"
	Schema    string // Reason == "schema_violation"
	Unknown   string // Reason == "unknown_tool"
}

// buildRetryHint 构造「自纠正」消息对：assistant 回放失败原文 + user 给出纠正提示。
// 三个策略（native/prompted/grammar）共用此结构，仅文案不同（通过 hints 传入）。
func buildRetryHint(failed FailedCall, hints retryHints) (asst, user llm.Message) {
	asst = llm.Message{Role: "assistant", Content: failed.RawText}
	var hint string
	switch failed.Reason {
	case "json_parse_error":
		hint = fmt.Sprintf(hints.JSONParse, failed.Detail)
	case "schema_violation":
		hint = fmt.Sprintf(hints.Schema, failed.Detail)
	case "unknown_tool":
		hint = fmt.Sprintf(hints.Unknown, failed.Detail)
	default:
		hint = "Your last tool call failed: " + failed.Detail
	}
	user = llm.Message{Role: "user", Content: hint}
	return asst, user
}
