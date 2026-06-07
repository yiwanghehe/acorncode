package toolcall

import (
	"context"
	"errors"
	"testing"

	"acorncode/internal/llm"
)

// 辅助：建一个 n 个 raw chunk 的 channel
func rawChunks(chunks ...llm.RawChunk) <-chan llm.RawChunk {
	out := make(chan llm.RawChunk, len(chunks))
	for _, c := range chunks {
		out <- c
	}
	close(out)
	return out
}

// 辅助：消费 stream 到结束，收集所有 event
func collectEvents(t *testing.T, ch <-chan llm.StreamEvent) []llm.StreamEvent {
	t.Helper()
	var out []llm.StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// TestNative_TextAndFinish 验证 text chunk + finish 的翻译
func TestNative_TextAndFinish(t *testing.T) {
	n := NewNative()
	raw := rawChunks(
		llm.RawChunk{Type: "text", Data: "Hello "},
		llm.RawChunk{Type: "text", Data: "world!"},
		llm.RawChunk{Type: "text", Data: ""}, // 空 text 应忽略
		llm.RawChunk{Type: "finish", Data: `{"InputTokens":10,"OutputTokens":2}`,
			Meta: map[string]any{"reason": "stop"}},
	)

	events := collectEvents(t, n.ParseStream(context.Background(), raw))

	if len(events) != 3 {
		t.Fatalf("收到 %d 个 event, 期望 3", len(events))
	}

	if td, ok := events[0].(llm.TextDelta); !ok || td.Text != "Hello " {
		t.Errorf("event 0 = %T %+v", events[0], events[0])
	}
	if td, ok := events[1].(llm.TextDelta); !ok || td.Text != "world!" {
		t.Errorf("event 1 = %T %+v", events[1], events[1])
	}
	fe, ok := events[2].(llm.FinishEvent)
	if !ok {
		t.Fatalf("event 2 = %T, 期望 FinishEvent", events[2])
	}
	if fe.Reason != "stop" {
		t.Errorf("Reason = %q", fe.Reason)
	}
	if fe.Usage.InputTokens != 10 || fe.Usage.OutputTokens != 2 {
		t.Errorf("Usage = %+v", fe.Usage)
	}
}

// TestNative_ToolCall 验证 tool_call chunk 翻译为 ToolCallEnd
func TestNative_ToolCall(t *testing.T) {
	n := NewNative()
	raw := rawChunks(
		llm.RawChunk{Type: "tool_call", Data: `{"path":"main.go"}`,
			Meta: map[string]any{"name": "read"}},
		llm.RawChunk{Type: "finish", Data: ``,
			Meta: map[string]any{"reason": "stop"}},
	)

	events := collectEvents(t, n.ParseStream(context.Background(), raw))

	if len(events) != 2 {
		t.Fatalf("收到 %d 个 event, 期望 2", len(events))
	}

	tc, ok := events[0].(llm.ToolCallEnd)
	if !ok {
		t.Fatalf("event 0 = %T, 期望 ToolCallEnd", events[0])
	}
	if tc.Name != "read" {
		t.Errorf("Name = %q", tc.Name)
	}
	if tc.CallID == "" {
		t.Error("CallID 不能为空")
	}
	if string(tc.Args) != `{"path":"main.go"}` {
		t.Errorf("Args = %s", tc.Args)
	}

	// 再发一个，验证 callID 不重复
	raw2 := rawChunks(
		llm.RawChunk{Type: "tool_call", Data: `{"path":"x.go"}`,
			Meta: map[string]any{"name": "read"}},
	)
	events2 := collectEvents(t, n.ParseStream(context.Background(), raw2))
	tc2 := events2[0].(llm.ToolCallEnd)
	if tc2.CallID == tc.CallID {
		t.Errorf("callID 应递增: 两次都是 %s", tc.CallID)
	}
}

// TestNative_Error 验证 error chunk 翻译为 ErrorEvent
func TestNative_Error(t *testing.T) {
	n := NewNative()
	raw := rawChunks(
		llm.RawChunk{Type: "error", Data: "context length exceeded"},
	)

	events := collectEvents(t, n.ParseStream(context.Background(), raw))

	if len(events) != 1 {
		t.Fatalf("收到 %d 个 event, 期望 1", len(events))
	}
	ee, ok := events[0].(llm.ErrorEvent)
	if !ok {
		t.Fatalf("event 0 = %T", events[0])
	}
	if ee.Err == nil || ee.Err.Error() != "context length exceeded" {
		t.Errorf("Err = %v", ee.Err)
	}
}

// TestNative_ContextCancel 验证 ctx 取消立即停止
func TestNative_ContextCancel(t *testing.T) {
	n := NewNative()

	// 永不结束的 channel
	raw := make(chan llm.RawChunk)
	defer close(raw)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	out := n.ParseStream(ctx, raw)
	// 应该在很短时间内关闭
	for range out {
		t.Fatal("不该有 event")
	}
	// 到达此处即通过
}

// TestNative_UnknownType 验证未知 type 不会 panic
func TestNative_UnknownType(t *testing.T) {
	n := NewNative()
	raw := rawChunks(
		llm.RawChunk{Type: "weird_type", Data: "foo"},
		llm.RawChunk{Type: "text", Data: "ok"},
		llm.RawChunk{Type: "another_unknown"},
	)

	events := collectEvents(t, n.ParseStream(context.Background(), raw))

	if len(events) != 1 {
		t.Fatalf("收到 %d 个 event, 期望 1（未知 type 忽略）", len(events))
	}
	if td, ok := events[0].(llm.TextDelta); !ok || td.Text != "ok" {
		t.Errorf("event 0 = %T %+v", events[0], events[0])
	}
}

// TestNative_EmptyData 验证空 data 正确处理
func TestNative_EmptyData(t *testing.T) {
	n := NewNative()
	raw := rawChunks(
		llm.RawChunk{Type: "text", Data: ""},      // 空 text 忽略
		llm.RawChunk{Type: "tool_call", Data: ""}, // 空 args → {}
	)

	events := collectEvents(t, n.ParseStream(context.Background(), raw))

	if len(events) != 1 {
		t.Fatalf("收到 %d 个 event, 期望 1", len(events))
	}
	tc := events[0].(llm.ToolCallEnd)
	if string(tc.Args) != "{}" {
		t.Errorf("Args = %s, 期望 {}", tc.Args)
	}
}

// TestNative_RetryHint 验证 RetryHint 生成正确的"自纠正"消息对
func TestNative_RetryHint(t *testing.T) {
	n := NewNative()
	tests := []struct {
		name    string
		failed  FailedCall
		contain string
	}{
		{
			name:    "json_parse_error",
			failed:  FailedCall{RawText: "{bad", Reason: "json_parse_error", Detail: "unexpected EOF"},
			contain: "malformed JSON",
		},
		{
			name:    "schema_violation",
			failed:  FailedCall{Reason: "schema_violation", Detail: "missing field 'path'"},
			contain: "schema",
		},
		{
			name:    "unknown_tool",
			failed:  FailedCall{Reason: "unknown_tool", Detail: "foo"},
			contain: "foo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asst, user := n.RetryHint(tt.failed, nil)
			if asst.Role != "assistant" || asst.Content != tt.failed.RawText {
				t.Errorf("asst = %+v", asst)
			}
			if user.Role != "user" {
				t.Errorf("user role = %q", user.Role)
			}
			if !contains(user.Content, tt.contain) {
				t.Errorf("user content = %q, 应含 %q", user.Content, tt.contain)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestNative_ImplementsStrategy 编译时断言
func TestNative_ImplementsStrategy(t *testing.T) {
	var _ Strategy = (*Native)(nil)
}

// 错误检查器，防止 unused import
var _ = errors.New
