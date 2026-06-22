// Package toolcall - 工具调用策略（Native / Prompted / Grammar）
// 真实设计: docs/acorncode-architect.md §8
package toolcall

import (
	"context"
	"encoding/json"
	"strings"

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

// tryFallbackToolCall 校验 buf 是否是「漏写 tool_call 包裹的」裸 JSON 工具调用。
// 由 Prompted / Grammar 等需要 `<tool_call>{...}</tool_call>` 包裹的策略共用，
// 在流结束但 buf 残留内容时尝试兜底识别——小模型漏写包裹时自救。
//
// 三层条件全中才返回 ok=true，避免误伤用户让模型输出的 JSON 文本：
//  1. schema 严格：必须是 {name, arguments} 结构（其他如 {tasks:[...]} 不命中）
//  2. name 命中已注册工具：防止任意 JSON 被误识别
//  3. markdown 代码块豁免：以 ``` 开头的输出视为 JSON 文本示例，不当 tool call
func tryFallbackToolCall(s string, toolIDs map[string]struct{}) (name string, args json.RawMessage, ok bool) {
	s = strings.TrimSpace(s)
	// 必须是纯 JSON 对象（trim 后首尾是 {}），无其他夹杂
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") || len(s) < 2 {
		return "", nil, false
	}
	// markdown 代码块包裹的 JSON 是文本示例，不是工具调用
	if strings.HasPrefix(s, "```") {
		return "", nil, false
	}
	var probe struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(s), &probe); err != nil {
		return "", nil, false
	}
	if probe.Name == "" {
		return "", nil, false
	}
	// name 必须命中已注册工具
	if _, exists := toolIDs[probe.Name]; !exists {
		return "", nil, false
	}
	if probe.Arguments == nil {
		probe.Arguments = json.RawMessage("{}")
	}
	return probe.Name, probe.Arguments, true
}
