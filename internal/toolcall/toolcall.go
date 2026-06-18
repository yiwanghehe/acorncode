// Package toolcall - 工具调用策略（Native / Prompted / Grammar）
// 真实设计: docs/acorncode-architect.md §8
package toolcall

import (
	"context"

	"acorncode/internal/llm"
	"acorncode/internal/tool"
)

// Strategy 把 Provider 的 RawChunk 流转为类型化的 StreamEvent 流
type Strategy interface {
	Name() string
	Prepare(req *llm.ChatRequest, tools []tool.Definition) error
	ParseStream(ctx context.Context, raw <-chan llm.RawChunk) <-chan llm.StreamEvent
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
