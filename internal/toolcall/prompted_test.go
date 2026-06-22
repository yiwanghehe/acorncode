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
	// v1.12：补强字段类型（避免小模型把 string 写成 array 等）
	if !strings.Contains(req.System[1], "Field types (STRICT") {
		t.Errorf("system 应含字段类型强调行: %s", req.System[1])
	}
	if !strings.Contains(req.System[1], "path: string") {
		t.Errorf("system 应含 path: string 类型说明: %s", req.System[1])
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

// ========== v1.12 fallback：模型漏写 <tool_call> 包裹的裸 JSON 解析 ==========

// TestPrompted_FallbackBareJSON_ValidTool 验证「裸 JSON + name 命中注册表」
// 在 EOF 时被识别为 tool call——核心场景（用户截图里的 qwen2.5-coder:1.5b）。
func TestPrompted_FallbackBareJSON_ValidTool(t *testing.T) {
	p := NewPrompted()
	// 注入工具名集合（与真实 Prepare 流程一致）
	if err := p.Prepare(&llm.ChatRequest{}, []tool.Definition{
		{ID: "read", JSONSchema: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}

	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: `{"name": "read", "arguments": {"path": "/etc/hosts"}}`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))

	var toolCalls []llm.ToolCallEnd
	for _, ev := range evs {
		if tc, ok := ev.(llm.ToolCallEnd); ok {
			toolCalls = append(toolCalls, tc)
		}
	}
	if len(toolCalls) != 1 {
		t.Fatalf("应识别 fallback tool call, got %d events: %+v", len(toolCalls), evs)
	}
	if toolCalls[0].Name != "read" {
		t.Errorf("name = %q, 期望 read", toolCalls[0].Name)
	}
	var args map[string]any
	_ = json.Unmarshal(toolCalls[0].Args, &args)
	if args["path"] != "/etc/hosts" {
		t.Errorf("args.path = %v, 期望 /etc/hosts", args["path"])
	}
}

// TestPrompted_FallbackBareJSON_UnknownTool 验证 name 未注册时**不当 tool call**——
// 防误伤的关键测试：用户让模型输出 JSON 文本、name 不在 tool list 时必须当文本流。
func TestPrompted_FallbackBareJSON_UnknownTool(t *testing.T) {
	p := NewPrompted()
	_ = p.Prepare(&llm.ChatRequest{}, []tool.Definition{
		{ID: "read", JSONSchema: json.RawMessage(`{}`)},
	})

	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: `{"name": "tool_id", "arguments": {"arg1": "value"}}`}, // tool_id 未注册
		{Type: "finish", Data: "{}"},
	})

	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))

	var toolCalls int
	var hasText bool
	for _, ev := range evs {
		if _, ok := ev.(llm.ToolCallEnd); ok {
			toolCalls++
		}
		if td, ok := ev.(llm.TextDelta); ok && strings.Contains(td.Text, "tool_id") {
			hasText = true
		}
	}
	if toolCalls != 0 {
		t.Errorf("未注册 name 不应识别为 tool call, got %d", toolCalls)
	}
	if !hasText {
		t.Errorf("应作为文本流输出（含 tool_id 字面）")
	}
}

// TestPrompted_FallbackBareJSON_UserWantsJSONText 验证用户要 JSON 文本的常见形态：
// 输出 {"tasks":[...]}（无 name 字段）→ 当文本流，不当 tool call。
func TestPrompted_FallbackBareJSON_UserWantsJSONText(t *testing.T) {
	p := NewPrompted()
	_ = p.Prepare(&llm.ChatRequest{}, []tool.Definition{
		{ID: "read", JSONSchema: json.RawMessage(`{}`)},
	})

	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: `{"tasks": [{"id": 1, "title": "task1"}]}`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))

	var toolCalls int
	var hasText bool
	for _, ev := range evs {
		if _, ok := ev.(llm.ToolCallEnd); ok {
			toolCalls++
		}
		if td, ok := ev.(llm.TextDelta); ok && strings.Contains(td.Text, "tasks") {
			hasText = true
		}
	}
	if toolCalls != 0 {
		t.Errorf("缺 name 字段不应 fallback, got %d", toolCalls)
	}
	if !hasText {
		t.Errorf("应作为文本流输出")
	}
}

// TestPrompted_FallbackBareJSON_WithMarkdownFence 验证模型按引导用 ```json 包裹的
// JSON 文本不被识别为 tool call——系统 prompt 加 markdown 引导语的契约测试。
func TestPrompted_FallbackBareJSON_WithMarkdownFence(t *testing.T) {
	p := NewPrompted()
	_ = p.Prepare(&llm.ChatRequest{}, []tool.Definition{
		{ID: "read", JSONSchema: json.RawMessage(`{}`)},
	})

	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: "```json\n{\"name\": \"read\", \"arguments\": {\"path\": \"/x\"}}\n```"},
		{Type: "finish", Data: "{}"},
	})

	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))

	var toolCalls int
	for _, ev := range evs {
		if _, ok := ev.(llm.ToolCallEnd); ok {
			toolCalls++
		}
	}
	if toolCalls != 0 {
		t.Errorf("markdown 包裹的 JSON 不应识别为 tool call, got %d", toolCalls)
	}
}

// TestPrompted_FallbackBareJSON_NoNameField 缺 name 字段的 JSON 不 fallback
func TestPrompted_FallbackBareJSON_NoNameField(t *testing.T) {
	p := NewPrompted()
	_ = p.Prepare(&llm.ChatRequest{}, []tool.Definition{
		{ID: "read", JSONSchema: json.RawMessage(`{}`)},
	})

	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: `{"arguments": {"path": "/x"}}`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))

	var toolCalls int
	for _, ev := range evs {
		if _, ok := ev.(llm.ToolCallEnd); ok {
			toolCalls++
		}
	}
	if toolCalls != 0 {
		t.Errorf("缺 name 字段不应 fallback, got %d", toolCalls)
	}
}

// TestPrompted_FallbackBareJSON_MalformedJSON 非 JSON 文本不被 fallback
func TestPrompted_FallbackBareJSON_MalformedJSON(t *testing.T) {
	p := NewPrompted()
	_ = p.Prepare(&llm.ChatRequest{}, []tool.Definition{
		{ID: "read", JSONSchema: json.RawMessage(`{}`)},
	})

	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: "hello world, this is plain text not JSON"},
		{Type: "finish", Data: "{}"},
	})

	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))

	var toolCalls int
	for _, ev := range evs {
		if _, ok := ev.(llm.ToolCallEnd); ok {
			toolCalls++
		}
	}
	if toolCalls != 0 {
		t.Errorf("非 JSON 不应 fallback, got %d", toolCalls)
	}
}

// TestPrompted_Fallback_EmptyArgsAllowed 验证 arguments 缺省时默认 {}——某些工具
// 真的没有必填参数（fallback 路径要兼容）。
func TestPrompted_Fallback_EmptyArgsAllowed(t *testing.T) {
	p := NewPrompted()
	_ = p.Prepare(&llm.ChatRequest{}, []tool.Definition{
		{ID: "bash", JSONSchema: json.RawMessage(`{}`)},
	})

	raw := feedPromptedChunks([]llm.RawChunk{
		{Type: "text", Data: `{"name": "bash"}`}, // 没有 arguments 字段
		{Type: "finish", Data: "{}"},
	})

	evs := collectPromptedEvents(t, p.ParseStream(context.Background(), raw))

	var toolCalls []llm.ToolCallEnd
	for _, ev := range evs {
		if tc, ok := ev.(llm.ToolCallEnd); ok {
			toolCalls = append(toolCalls, tc)
		}
	}
	if len(toolCalls) != 1 {
		t.Fatalf("缺 arguments 应默认 {}, got %d events", len(toolCalls))
	}
	if string(toolCalls[0].Args) != "{}" {
		t.Errorf("缺 arguments 应默认为 {}, got %q", string(toolCalls[0].Args))
	}
}

// TestPrompted_Prepare_PopulatesToolIDs 验证 Prepare 后 toolIDs 被填充；空 list 时清空。
func TestPrompted_Prepare_PopulatesToolIDs(t *testing.T) {
	p := NewPrompted()
	_ = p.Prepare(&llm.ChatRequest{}, []tool.Definition{
		{ID: "read"}, {ID: "bash"}, {ID: "edit"},
	})
	if len(p.toolIDs) != 3 {
		t.Errorf("toolIDs 应有 3 项, got %d", len(p.toolIDs))
	}
	for _, id := range []string{"read", "bash", "edit"} {
		if _, ok := p.toolIDs[id]; !ok {
			t.Errorf("toolIDs 缺 %s", id)
		}
	}

	// 二次 Prepare 空 list：toolIDs 应被清空，避免上一次残留导致 fallback 误命中
	_ = p.Prepare(&llm.ChatRequest{}, nil)
	if p.toolIDs != nil {
		t.Errorf("空 tool list 应清空 toolIDs, got %v", p.toolIDs)
	}
}
