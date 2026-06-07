// Package agent - processor.go
// 参考: docs/acorncode-architect.md §7.2
package agent

import (
	"context"

	"acorncode/internal/bus"
	"acorncode/internal/llm"
	"acorncode/internal/session"
)

// Processor 消费 llm.StreamEvent 流并把状态变化应用到一个 assistant message。
// 同时持久化到 Store、广播到 Bus。
//
// 参考: §7.2
type Processor struct {
	bus         *bus.Bus
	store       SessionStore
	msg         *session.Message
	currentText *session.TextPart
	toolCalls   map[string]*session.ToolPart // callID → part
	finish      *llm.FinishEvent
	usage       llm.Usage
}

// newProcessor 为新 assistant 消息创建 Processor
func newProcessor(msg *session.Message, b *bus.Bus, s SessionStore) *Processor {
	return &Processor{
		bus:       b,
		store:     s,
		msg:       msg,
		toolCalls: make(map[string]*session.ToolPart),
	}
}

// Apply 消费一个事件并更新内部状态。
// 只在持久化失败时返错误。
func (p *Processor) Apply(ctx context.Context, ev llm.StreamEvent) error {
	switch e := ev.(type) {
	case llm.TextDelta:
		if p.currentText == nil {
			p.currentText = &session.TextPart{
				ID:        newID("prt"),
				MessageID: p.msg.ID,
				SessionID: p.msg.SessionID,
			}
			p.msg.Parts = append(p.msg.Parts, p.currentText)
		}
		p.currentText.Text += e.Text
		p.bus.Publish(bus.Event{
			Type: bus.EventPartDelta, SessionID: p.msg.SessionID,
			Data: p.currentText,
		})
		return p.persistPart(ctx, p.currentText)

	case llm.ReasoningDelta:
		// 简化：reasoning 暂不单独持久化（v1）
		return nil

	case llm.ToolCallStart:
		part := &session.ToolPart{
			ID:        newID("prt"),
			MessageID: p.msg.ID,
			SessionID: p.msg.SessionID,
			CallID:    e.CallID,
			ToolID:    e.Name,
			State:     session.ToolPending,
		}
		p.toolCalls[e.CallID] = part
		p.msg.Parts = append(p.msg.Parts, part)
		p.bus.Publish(bus.Event{
			Type: bus.EventPartUpdated, SessionID: p.msg.SessionID,
			Data: part,
		})
		return p.persistPart(ctx, part)

	case llm.ToolCallDelta:
		if part, ok := p.toolCalls[e.CallID]; ok {
			// 暂不持久化 args delta（v1 简化）
			_ = part
		}
		return nil

	case llm.ToolCallEnd:
		// 优先复用 ToolCallStart 创的 part
		// 兜底：有些 provider（Ollama）只发 End 不发 Start，这里现场创建
		part, ok := p.toolCalls[e.CallID]
		if !ok {
			part = &session.ToolPart{
				ID:        newID("prt"),
				MessageID: p.msg.ID,
				SessionID: p.msg.SessionID,
				CallID:    e.CallID,
				ToolID:    e.Name,
				State:     session.ToolPending,
			}
			p.toolCalls[e.CallID] = part
			p.msg.Parts = append(p.msg.Parts, part)
			p.bus.Publish(bus.Event{
				Type: bus.EventPartUpdated, SessionID: p.msg.SessionID,
				Data: part,
			})
		}
		part.Args = e.Args
		part.State = session.ToolPending // 等待 Loop 执行
		return p.persistPart(ctx, part)

	case llm.FinishEvent:
		p.finish = &e
		p.usage = e.Usage
		p.msg.FinishReason = e.Reason
		p.bus.Publish(bus.Event{
			Type: bus.EventAgentStateChange, SessionID: p.msg.SessionID,
			Data: map[string]any{"event": "finish", "reason": e.Reason},
		})
		return nil
	}
	return nil
}

// Message 返回（已变化的）assistant 消息
func (p *Processor) Message() *session.Message {
	return p.msg
}

// PendingToolCalls 返回所有 ToolPending 状态的工具调用
func (p *Processor) PendingToolCalls() []ToolCall {
	var out []ToolCall
	for _, part := range p.toolCalls {
		out = append(out, ToolCall{
			MessageID: part.MessageID,
			CallID:    part.CallID,
			ToolID:    part.ToolID,
			Args:      part.ArgsMap(),
			PartID:    part.ID, // 让 Loop 能找到 processor 创的 part 并复用
		})
	}
	return out
}

// FinishEvent 返回 finish 事件（如果有）
func (p *Processor) FinishEvent() *llm.FinishEvent {
	return p.finish
}

// Usage 返回累计 token 使用
func (p *Processor) Usage() llm.Usage {
	return p.usage
}

// persistPart 持久化一个 part。错误会返回，但调用方决定是否中止。
// v1 中对瞬时错误只 log 不中断。
func (p *Processor) persistPart(ctx context.Context, part session.Part) error {
	return p.store.UpsertPart(ctx, part)
}
