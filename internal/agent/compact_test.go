// Package agent - compact_test.go
// 验证 Compaction 持久化闭环（v1.10）：compact() 把压缩结果原子写回 store。
package agent

import (
	"context"
	"testing"

	"acorncode/internal/bus"
	"acorncode/internal/llm"
	"acorncode/internal/session"
)

// fakeCompactor 是测试用压缩器：把历史压成「1 条 system summary + 最近 keep 条」
type fakeCompactor struct {
	keep    int
	summary string
	err     error
}

func (f *fakeCompactor) Compact(ctx context.Context, history []llm.Message) ([]llm.Message, error) {
	if f.err != nil {
		return history, f.err
	}
	if len(history) <= f.keep {
		return history, nil
	}
	keep := history[len(history)-f.keep:]
	out := make([]llm.Message, 0, 1+len(keep))
	out = append(out, llm.Message{Role: "system", Content: f.summary})
	out = append(out, keep...)
	return out, nil
}

// newCompactLoop 构造一个仅供 compact() 测试的最小 Loop（store + bus + compactor）
func newCompactLoop(t *testing.T, store SessionStore, c *fakeCompactor) *Loop {
	t.Helper()
	l := &Loop{
		sessionID: "s_compact",
		store:     store,
		bus:       bus.New(),
		breaker:   newCircuitBreaker(circuitConfig{}),
	}
	l.SetCompactor(c)
	return l
}

// seedMessages 往 store 写 n 条带 TextPart 的消息
func seedMessages(t *testing.T, store SessionStore, sessionID string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		part := &session.TextPart{ID: "p" + itoa(i), MessageID: "m" + itoa(i), SessionID: sessionID, Text: "msg" + itoa(i)}
		msg := &session.Message{ID: "m" + itoa(i), SessionID: sessionID, Role: role, Parts: []session.Part{part}}
		if err := store.AppendMessage(ctx, msg); err != nil {
			t.Fatalf("AppendMessage err: %v", err)
		}
		if err := store.UpsertPart(ctx, part); err != nil {
			t.Fatalf("UpsertPart err: %v", err)
		}
	}
}

func itoa(i int) string { return string(rune('0' + i)) }

// TestCompact_PersistsResult 验证压缩后 store 里的消息真的被替换为短历史
func TestCompact_PersistsResult(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	_ = store.CreateSession(ctx, &session.Session{ID: "s_compact"})
	seedMessages(t, store, "s_compact", 6) // 6 条消息

	l := newCompactLoop(t, store, &fakeCompactor{keep: 2, summary: "压缩摘要"})

	if err := l.compact(ctx); err != nil {
		t.Fatalf("compact err: %v", err)
	}

	// 期望：6 条 → 3 条（1 summary + 最近 2）
	got, err := store.Messages(ctx, "s_compact", 0)
	if err != nil {
		t.Fatalf("Messages err: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("压缩后消息数 = %d, 期望 3", len(got))
	}
	if got[0].Role != "system" {
		t.Errorf("首条 role = %q, 期望 system", got[0].Role)
	}
	if tp, ok := got[0].Parts[0].(*session.TextPart); !ok || tp.Text != "压缩摘要" {
		t.Errorf("summary 内容错: %+v", got[0].Parts[0])
	}
	// 最近 2 条应是原始 msg4 / msg5
	if tp, ok := got[1].Parts[0].(*session.TextPart); !ok || tp.Text != "msg4" {
		t.Errorf("got[1] 文本错: %+v", got[1].Parts[0])
	}
}

// TestCompact_NoCompactor 验证未注入 compactor 时返错
func TestCompact_NoCompactor(t *testing.T) {
	store := session.NewMemoryStore()
	_ = store.CreateSession(context.Background(), &session.Session{ID: "s_compact"})
	l := &Loop{sessionID: "s_compact", store: store, bus: bus.New()}

	if err := l.compact(context.Background()); err == nil {
		t.Error("未注入 compactor 应返错")
	}
}

// TestCompact_NoGain_SkipsWriteback 验证压缩无收益（数量未减）时不写回
func TestCompact_NoGain_SkipsWriteback(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	_ = store.CreateSession(ctx, &session.Session{ID: "s_compact"})
	seedMessages(t, store, "s_compact", 2)

	// keep=5 > 2 → fakeCompactor 原样返回，数量不减
	l := newCompactLoop(t, store, &fakeCompactor{keep: 5, summary: "x"})
	if err := l.compact(ctx); err != nil {
		t.Fatalf("compact err: %v", err)
	}

	got, _ := store.Messages(ctx, "s_compact", 0)
	if len(got) != 2 {
		t.Errorf("无收益时应保留原 2 条, 实际 = %d", len(got))
	}
	// 原始 part ID 仍在（未被 rebuild 替换）
	if _, err := store.GetPart(ctx, "p0"); err != nil {
		t.Errorf("无收益时原 part 应仍在: %v", err)
	}
}

// TestCompact_CompactorError_KeepsOriginal 验证 compactor 出错时保留原消息
func TestCompact_CompactorError_KeepsOriginal(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	_ = store.CreateSession(ctx, &session.Session{ID: "s_compact"})
	seedMessages(t, store, "s_compact", 4)

	l := newCompactLoop(t, store, &fakeCompactor{keep: 2, err: context.DeadlineExceeded})
	if err := l.compact(ctx); err != nil {
		t.Fatalf("compact 不应向上抛错（保守保留原消息）: %v", err)
	}
	got, _ := store.Messages(ctx, "s_compact", 0)
	if len(got) != 4 {
		t.Errorf("compactor 出错时应保留原 4 条, 实际 = %d", len(got))
	}
}
