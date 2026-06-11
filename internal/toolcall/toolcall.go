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

// emitter 封装「向 out 通道发送事件、同时尊重 ctx 取消」的样板（R7：
// prompted/grammar 的 emit/emitText 闭包逐字相同，native 也有同款 select）。
// 所有方法返回 false 表示 ctx 已取消，调用方应停止生产。
type emitter struct {
	ctx context.Context
	out chan<- llm.StreamEvent
}

// newEmitter 创建 emitter。
func newEmitter(ctx context.Context, out chan<- llm.StreamEvent) *emitter {
	return &emitter{ctx: ctx, out: out}
}

// Event 发送一个事件。ctx 取消时返回 false。
func (e *emitter) Event(ev llm.StreamEvent) bool {
	select {
	case e.out <- ev:
		return true
	case <-e.ctx.Done():
		return false
	}
}

// Text 发送一段文本增量。空串跳过（视作成功）。ctx 取消时返回 false。
func (e *emitter) Text(text string) bool {
	if text == "" {
		return true
	}
	return e.Event(llm.TextDelta{Text: text})
}
