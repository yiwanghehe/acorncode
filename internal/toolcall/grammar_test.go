package toolcall

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"acorncode/internal/llm"
	"acorncode/internal/tool"
)

func feedGrammarChunks(chunks []llm.RawChunk) <-chan llm.RawChunk {
	out := make(chan llm.RawChunk, len(chunks))
	for _, c := range chunks {
		out <- c
	}
	close(out)
	return out
}

func collectGrammarEvents(t *testing.T, ch <-chan llm.StreamEvent) []llm.StreamEvent {
	t.Helper()
	var out []llm.StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func grammarTestTools() []tool.Definition {
	readSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"}
		},
		"required": ["path"]
	}`)
	editSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"old_text": {"type": "string"},
			"new_text": {"type": "string"}
		},
		"required": ["path", "old_text", "new_text"]
	}`)
	return []tool.Definition{
		{ID: "read", Description: "Read a file", JSONSchema: readSchema},
		{ID: "edit", Description: "Edit a file", JSONSchema: editSchema},
	}
}

func TestGrammar_ValidToolCall(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `Let me read. <tool_call>{"name": "read", "arguments": {"path": "/tmp/x"}}</tool_call> done.`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	var calls []llm.ToolCallEnd
	for _, ev := range evs {
		if t, ok := ev.(llm.ToolCallEnd); ok {
			calls = append(calls, t)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("应 1 个 call, got %d", len(calls))
	}
	if calls[0].Name != "read" {
		t.Errorf("name = %s", calls[0].Name)
	}
}

func TestGrammar_UnknownTool_Rejected(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `<tool_call>{"name": "delete_everything", "arguments": {}}</tool_call>`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	for _, ev := range evs {
		if tc, ok := ev.(llm.ToolCallEnd); ok {
			t.Errorf("未知 tool 不应 emit, got %s", tc.Name)
		}
	}
}

func TestGrammar_MissingRequiredField_Rejected(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	// read 需要 path 字段，这里给空 arguments
	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `<tool_call>{"name": "read", "arguments": {}}</tool_call>`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	for _, ev := range evs {
		if tc, ok := ev.(llm.ToolCallEnd); ok {
			t.Errorf("缺 required field 不应 emit, got %s", tc.Name)
		}
	}
}

func TestGrammar_WrongType_Rejected(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	// read.path 应是 string，给 number
	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `<tool_call>{"name": "read", "arguments": {"path": 123}}</tool_call>`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	for _, ev := range evs {
		if tc, ok := ev.(llm.ToolCallEnd); ok {
			t.Errorf("错类型不应 emit, got %s", tc.Name)
		}
	}
}

func TestGrammar_MalformedJSON_Skipped(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `<tool_call>{not valid}</tool_call>`},
		{Type: "finish", Data: "{}"},
	})

	// 不应 panic
	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	var hasFinish bool
	for _, ev := range evs {
		if _, ok := ev.(llm.FinishEvent); ok {
			hasFinish = true
		}
	}
	if !hasFinish {
		t.Errorf("坏 JSON 不应阻塞流结束")
	}
}

func TestGrammar_MultipleValidCalls(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `<tool_call>{"name":"read","arguments":{"path":"/a"}}</tool_call><tool_call>{"name":"edit","arguments":{"path":"/b","old_text":"x","new_text":"y"}}</tool_call>`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	var calls []llm.ToolCallEnd
	for _, ev := range evs {
		if t, ok := ev.(llm.ToolCallEnd); ok {
			calls = append(calls, t)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("应 2 个 call, got %d", len(calls))
	}
	if calls[0].Name != "read" {
		t.Errorf("calls[0] = %s", calls[0].Name)
	}
	if calls[1].Name != "edit" {
		t.Errorf("calls[1] = %s", calls[1].Name)
	}
}

func TestGrammar_MixedValidAndInvalid(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	// 第一个 valid，第二个 schema 错（edit 缺 old_text），第三个 valid
	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `<tool_call>{"name":"read","arguments":{"path":"/a"}}</tool_call><tool_call>{"name":"edit","arguments":{"path":"/b"}}</tool_call><tool_call>{"name":"read","arguments":{"path":"/c"}}</tool_call>`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	var calls []llm.ToolCallEnd
	for _, ev := range evs {
		if t, ok := ev.(llm.ToolCallEnd); ok {
			calls = append(calls, t)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("应 2 个有效 call（1 跳过），got %d", len(calls))
	}
}

func TestGrammar_NoTools_NoEmit(t *testing.T) {
	g := NewGrammar()
	// 不 Prepare（没 tool）

	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `<tool_call>{"name": "read", "arguments": {"path": "/x"}}</tool_call>`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	for _, ev := range evs {
		if _, ok := ev.(llm.ToolCallEnd); ok {
			t.Errorf("没 tools 时不应 emit")
		}
	}
}

func TestGrammar_Name(t *testing.T) {
	if NewGrammar().Name() != "grammar" {
		t.Errorf("Name 应是 'grammar'")
	}
}

// TestGrammar_PrepareGeneratesGBNF 验证 v1.3：Prepare 后每个工具都有 GBNF。
func TestGrammar_PrepareGeneratesGBNF(t *testing.T) {
	g := NewGrammar()
	req := &llm.ChatRequest{}
	if err := g.Prepare(req, grammarTestTools()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	grammars := g.Grammars()
	if len(grammars) != 2 {
		t.Fatalf("应为 2 个工具生成 GBNF, got %d", len(grammars))
	}
	readGBNF, ok := grammars["read"]
	if !ok {
		t.Fatal("缺 read 的 GBNF")
	}
	if !strings.HasPrefix(readGBNF, "root ::= ") {
		t.Errorf("GBNF 应以 root 开头:\n%s", readGBNF)
	}
	if !strings.Contains(readGBNF, `"path"`) {
		t.Errorf("read GBNF 应含 path 属性:\n%s", readGBNF)
	}
}

// TestGrammar_PrepareInjectsSystemPrompt 验证 v1.3：Prepare 注入 system 引导。
func TestGrammar_PrepareInjectsSystemPrompt(t *testing.T) {
	g := NewGrammar()
	req := &llm.ChatRequest{}
	_ = g.Prepare(req, grammarTestTools())
	if len(req.System) == 0 {
		t.Fatal("Prepare 应注入 system prompt")
	}
	joined := strings.Join(req.System, "\n")
	if !strings.Contains(joined, "<tool_call>") {
		t.Errorf("system 应含 tool_call 说明:\n%s", joined)
	}
}
