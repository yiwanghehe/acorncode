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

// TestGrammar_ForceToolCall_SetsFormat 验证 v1.4：开启 ForceToolCall 后设置 req.Format。
func TestGrammar_ForceToolCall_SetsFormat(t *testing.T) {
	g := NewGrammar()
	g.ForceToolCall = true
	req := &llm.ChatRequest{}
	if err := g.Prepare(req, grammarTestTools()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(req.Format) == 0 {
		t.Fatal("ForceToolCall=true 应设置 req.Format")
	}
	// Format 应是合法 JSON Schema，含 name 的 enum（工具 ID）
	var schema struct {
		Type       string `json:"type"`
		Properties struct {
			Name struct {
				Enum []string `json:"enum"`
			} `json:"name"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(req.Format, &schema); err != nil {
		t.Fatalf("Format 应是合法 JSON: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("Format.type = %q, 期望 object", schema.Type)
	}
	if len(schema.Properties.Name.Enum) != 2 {
		t.Errorf("name.enum 应含 2 个工具, got %v", schema.Properties.Name.Enum)
	}
	// v1.5：ForceToolCall 同时设置 ToolChoice=any
	if req.ToolChoice != "any" {
		t.Errorf("ForceToolCall 应设 ToolChoice=any, got %q", req.ToolChoice)
	}
}

// TestGrammar_NoForce_NoFormat 验证默认不强制：不设置 req.Format（向后兼容）。
func TestGrammar_NoForce_NoFormat(t *testing.T) {
	g := NewGrammar()
	req := &llm.ChatRequest{}
	_ = g.Prepare(req, grammarTestTools())
	if len(req.Format) != 0 {
		t.Errorf("默认 ForceToolCall=false 不应设置 Format, got %s", req.Format)
	}
	if req.ToolChoice != "" {
		t.Errorf("默认不应设置 ToolChoice, got %q", req.ToolChoice)
	}
}

// ========== v1.12 fallback：与 Prompted 共享同一套裸 JSON 兜底识别 ==========

// TestGrammar_FallbackBareJSON_ValidTool 验证 Grammar 在 EOF 时也能兜底识别裸 JSON。
// 场景：Ollama --force-tool 约束下模型只输出 JSON 而漏写 `<tool_call>` 包裹。
func TestGrammar_FallbackBareJSON_ValidTool(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `{"name": "read", "arguments": {"path": "/tmp/x"}}`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	var calls []llm.ToolCallEnd
	for _, ev := range evs {
		if tc, ok := ev.(llm.ToolCallEnd); ok {
			calls = append(calls, tc)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("应识别 fallback tool call, got %d events: %+v", len(calls), evs)
	}
	if calls[0].Name != "read" {
		t.Errorf("name = %q, 期望 read", calls[0].Name)
	}
	var args map[string]any
	_ = json.Unmarshal(calls[0].Args, &args)
	if args["path"] != "/tmp/x" {
		t.Errorf("args.path = %v, 期望 /tmp/x", args["path"])
	}
}

// TestGrammar_FallbackBareJSON_SchemaValidationFallsBackToText 验证 Grammar 在
// fallback 命中但 schema 校验失败时退回到文本流——不丢内容、让模型下一轮看到错误。
func TestGrammar_FallbackBareJSON_SchemaValidationFallsBackToText(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	// read 工具要求 path 必填，这里缺 path → schema 校验失败
	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `{"name": "read", "arguments": {}}`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	var calls int
	var hasText bool
	for _, ev := range evs {
		if _, ok := ev.(llm.ToolCallEnd); ok {
			calls++
		}
		if td, ok := ev.(llm.TextDelta); ok && strings.Contains(td.Text, "read") {
			hasText = true
		}
	}
	if calls != 0 {
		t.Errorf("schema 校验失败不应 emit tool call, got %d", calls)
	}
	if !hasText {
		t.Errorf("schema 校验失败应退回文本流（保留原始 JSON 给模型下一轮看）")
	}
}

// TestGrammar_FallbackBareJSON_UnknownTool 验证 name 不在注册表时不当 tool call。
func TestGrammar_FallbackBareJSON_UnknownTool(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `{"name": "tool_id", "arguments": {"arg1": "value"}}`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	var calls int
	var hasText bool
	for _, ev := range evs {
		if _, ok := ev.(llm.ToolCallEnd); ok {
			calls++
		}
		if td, ok := ev.(llm.TextDelta); ok && strings.Contains(td.Text, "tool_id") {
			hasText = true
		}
	}
	if calls != 0 {
		t.Errorf("未注册 name 不应识别, got %d", calls)
	}
	if !hasText {
		t.Errorf("应作为文本流输出")
	}
}

// TestGrammar_FallbackBareJSON_UserWantsJSONText 用户让模型输出 JSON 文本（无 name）→ 当文本流
func TestGrammar_FallbackBareJSON_UserWantsJSONText(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `{"tasks": [{"id": 1}, {"id": 2}]}`},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	var calls int
	var hasText bool
	for _, ev := range evs {
		if _, ok := ev.(llm.ToolCallEnd); ok {
			calls++
		}
		if td, ok := ev.(llm.TextDelta); ok && strings.Contains(td.Text, "tasks") {
			hasText = true
		}
	}
	if calls != 0 {
		t.Errorf("缺 name 字段不应 fallback, got %d", calls)
	}
	if !hasText {
		t.Errorf("应作为文本流输出")
	}
}

// TestGrammar_FallbackBareJSON_WithMarkdownFence ```json 包裹的文本不被识别
func TestGrammar_FallbackBareJSON_WithMarkdownFence(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: "```json\n{\"name\": \"read\", \"arguments\": {\"path\": \"/x\"}}\n```"},
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	var calls int
	for _, ev := range evs {
		if _, ok := ev.(llm.ToolCallEnd); ok {
			calls++
		}
	}
	if calls != 0 {
		t.Errorf("markdown 包裹不应识别, got %d", calls)
	}
}

// TestGrammar_Fallback_InCallTruncated 在 <tool_call> 块被截断（缺 </tool_call>）时
// 警告并丢弃残留 JSON 而不 fallback——半成品 JSON 不能安全解析。
func TestGrammar_Fallback_InCallTruncated(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())

	raw := feedGrammarChunks([]llm.RawChunk{
		{Type: "text", Data: `<tool_call>{"name": "read", "arguments": {"path": "/x"}}`}, // 缺 </tool_call>
		{Type: "finish", Data: "{}"},
	})

	evs := collectGrammarEvents(t, g.ParseStream(context.Background(), raw))

	var calls int
	var hasFinish bool
	for _, ev := range evs {
		if _, ok := ev.(llm.ToolCallEnd); ok {
			calls++
		}
		if _, ok := ev.(llm.FinishEvent); ok {
			hasFinish = true
		}
	}
	if calls != 0 {
		t.Errorf("截断的 tool_call 块不应 emit, got %d", calls)
	}
	if !hasFinish {
		t.Errorf("截断块仍应 emit finish 事件")
	}
}

// TestGrammar_Prepare_PopulatesToolIDs 验证 Prepare 后 toolIDs 被填充；空 list 时清空
func TestGrammar_Prepare_PopulatesToolIDs(t *testing.T) {
	g := NewGrammar()
	_ = g.Prepare(&llm.ChatRequest{}, grammarTestTools())
	if len(g.toolIDs) != 2 {
		t.Errorf("toolIDs 应有 2 项, got %d", len(g.toolIDs))
	}
	for _, id := range []string{"read", "edit"} {
		if _, ok := g.toolIDs[id]; !ok {
			t.Errorf("toolIDs 缺 %s", id)
		}
	}

	// 二次 Prepare 空 list：toolIDs 应被清空，避免上一次残留
	_ = g.Prepare(&llm.ChatRequest{}, nil)
	if g.toolIDs != nil {
		t.Errorf("空 tool list 应清空 toolIDs, got %v", g.toolIDs)
	}
}
