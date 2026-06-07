package toolcall

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"acorncode/internal/llm"
	"acorncode/internal/tool"
)

// feedChunks 把 chunks 喂给 channel，返回 channel
func feedPromptedChunks(chunks []llm.RawChunk) <-chan llm.RawChunk {
	out := make(chan llm.RawChunk, len(chunks))
	for _, c := range chunks {
		out <- c
	}
	close(out)
	return out
}

// collectPromptedEvents 消费 channel 到 close
func collectPromptedEvents(t *testing.T, ch <-chan llm.StreamEvent) []llm.StreamEvent {
	t.Helper()
	var out []llm.StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestPrompted_SingleToolCall(t *testing.T) {
	p := NewPrompted()
	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: `Let me read it. <tool_call>{"name": "read", "arguments": {"path": "/etc/hosts"}}</tool_call> Done.`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))

	// 期望：TextDelta("Let me read it. ") + ToolCallEnd + TextDelta(" Done.") + FinishEvent
	if len(evs) < 4 {
		t.Fatalf("应至少 4 events, got %d: %+v", len(evs), evs)
	}

	td, ok := evs[0].(llm.TextDelta)
	if !ok {
		t.Fatalf("evs[0] = %T, 期望 TextDelta", evs[0])
	}
	if !strings.Contains(td.Text, "Let me read it.") {
		t.Errorf("text = %q, 应含 'Let me read it.'", td.Text)
	}

	tc, ok := evs[1].(llm.ToolCallEnd)
	if !ok {
		t.Fatalf("evs[1] = %T, 期望 ToolCallEnd", evs[1])
	}
	if tc.Name != "read" {
		t.Errorf("name = %q, 期望 read", tc.Name)
	}
	if tc.Args == nil {
		t.Fatal("Args 应非 nil")
	}

	// 验 args
	var args map[string]any
	_ = json.Unmarshal(tc.Args, &args)
	if args["path"] != "/etc/hosts" {
		t.Errorf("path = %v", args["path"])
	}
}

func TestPrompted_MultipleToolCalls(t *testing.T) {
	p := NewPrompted()
	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: `<tool_call>{"name":"read","arguments":{"path":"/a"}}</tool_call><tool_call>{"name":"edit","arguments":{"path":"/b","content":"x"}}</tool_call>`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))

	var calls []llm.ToolCallEnd
	for _, ev := range evs {
		if t, ok := ev.(llm.ToolCallEnd); ok {
			calls = append(calls, t)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("应 2 个 tool call, got %d", len(calls))
	}
	if calls[0].Name != "read" {
		t.Errorf("call[0] = %s", calls[0].Name)
	}
	if calls[1].Name != "edit" {
		t.Errorf("call[1] = %s", calls[1].Name)
	}
}

func TestPrompted_SplitAcrossChunks(t *testing.T) {
	// 模型在 <tool_call> 中间断开
	p := NewPrompted()
	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: "I will <tool_call>"},
		{Type: "text", Data: `{"name":"read","arguments":{"path":"/x"}}</tool_call> done.`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))

	var hasTool bool
	for _, ev := range evs {
		if tc, ok := ev.(llm.ToolCallEnd); ok {
			hasTool = true
			if tc.Name != "read" {
				t.Errorf("name = %q", tc.Name)
			}
		}
	}
	if !hasTool {
		t.Errorf("应识别出 tool call, evs: %+v", evs)
	}
}

func TestPrompted_NoToolCall(t *testing.T) {
	p := NewPrompted()
	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: "Just plain text, no tool call."},
		{Type: "finish", Data: "{}"},
	})

	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))

	// 应只有 TextDelta + FinishEvent
	var toolCalls int
	var textLen int
	for _, ev := range evs {
		switch v := ev.(type) {
		case llm.ToolCallEnd:
			toolCalls++
		case llm.TextDelta:
			textLen += len(v.Text)
		}
	}
	if toolCalls != 0 {
		t.Errorf("不应有 tool call, got %d", toolCalls)
	}
	if textLen < 10 {
		t.Errorf("text 太少: %d", textLen)
	}
}

func TestPrompted_MalformedJSON(t *testing.T) {
	p := NewPrompted()
	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: `<tool_call>{not valid json}</tool_call> ok`},
		{Type: "finish", Data: "{}"},
	})

	// 不应 panic；坏 JSON 跳过继续
	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))
	// 应有 finish event（说明解析没卡住）
	var hasFinish bool
	for _, ev := range evs {
		if _, ok := ev.(llm.FinishEvent); ok {
			hasFinish = true
		}
	}
	if !hasFinish {
		t.Errorf("应收到 finish event（坏 JSON 不应阻塞）")
	}
}

func TestPrompted_Prepare(t *testing.T) {
	p := NewPrompted()
	req := &llm.ChatRequest{
		System: []string{"You are helpful."},
	}
	tools := []tool.Definition{
		{ID: "read", Description: "Read a file", JSONSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
	}
	if err := p.Prepare(req, tools); err != nil {
		t.Fatal(err)
	}
	if len(req.System) != 2 {
		t.Errorf("system 应追加 1 段, got %d", len(req.System))
	}
	if !strings.Contains(req.System[1], "read") {
		t.Errorf("system 应含 tool 描述: %s", req.System[1])
	}
	if !strings.Contains(req.System[1], "<tool_call>") {
		t.Errorf("system 应含格式示例: %s", req.System[1])
	}
}

func TestPrompted_EmptyToolList_Prepare(t *testing.T) {
	p := NewPrompted()
	req := &llm.ChatRequest{System: []string{"hello"}}
	if err := p.Prepare(req, nil); err != nil {
		t.Fatal(err)
	}
	if len(req.System) != 1 {
		t.Errorf("空 tool list 不应追加，got %d", len(req.System))
	}
}

func TestPrompted_Name(t *testing.T) {
	if NewPrompted().Name() != "prompted" {
		t.Errorf("Name = %s, 期望 prompted", NewPrompted().Name())
	}
}
