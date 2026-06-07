package session

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStore_CreateAndGetSession(t *testing.T) {
	store := NewMemoryStore()
	sess := &Session{
		ID:        "sess_1",
		Title:     "test",
		Directory: "/tmp",
		CreatedAt: time.Now(),
	}
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.GetSession(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "test" {
		t.Errorf("Title = %q", got.Title)
	}
}

func TestMemoryStore_DuplicateSession(t *testing.T) {
	store := NewMemoryStore()
	sess := &Session{ID: "dup", Title: "x", CreatedAt: time.Now()}
	_ = store.CreateSession(context.Background(), sess)
	if err := store.CreateSession(context.Background(), sess); err == nil {
		t.Error("重复 ID 应报错")
	}
}

func TestMemoryStore_Messages(t *testing.T) {
	store := NewMemoryStore()
	_ = store.CreateSession(context.Background(), &Session{ID: "s1", CreatedAt: time.Now()})

	for i := 0; i < 5; i++ {
		msg := &Message{
			ID:        newTestID("msg", i),
			SessionID: "s1",
			Role:      "user",
		}
		if err := store.AppendMessage(context.Background(), msg); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	msgs, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 5 {
		t.Errorf("count = %d, 期望 5", len(msgs))
	}
}

func TestMemoryStore_MessagesLimit(t *testing.T) {
	store := NewMemoryStore()
	_ = store.CreateSession(context.Background(), &Session{ID: "s1", CreatedAt: time.Now()})

	for i := 0; i < 10; i++ {
		_ = store.AppendMessage(context.Background(), &Message{
			ID:        newTestID("msg", i),
			SessionID: "s1",
			Role:      "user",
		})
	}

	// limit=3 应返回最后 3 条
	msgs, _ := store.Messages(context.Background(), "s1", 3)
	if len(msgs) != 3 {
		t.Errorf("limit=3 count = %d, 期望 3", len(msgs))
	}
}

func TestMemoryStore_UpsertPart(t *testing.T) {
	store := NewMemoryStore()
	part := &TextPart{ID: "prt_1", MessageID: "m_1", SessionID: "s1", Text: "hi"}

	if err := store.UpsertPart(context.Background(), part); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.GetPart(context.Background(), "prt_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	tp, ok := got.(*TextPart)
	if !ok || tp.Text != "hi" {
		t.Errorf("got = %+v", got)
	}
}

func TestMemoryStore_SetFinishReason(t *testing.T) {
	store := NewMemoryStore()
	_ = store.CreateSession(context.Background(), &Session{ID: "s1", CreatedAt: time.Now()})
	_ = store.AppendMessage(context.Background(), &Message{
		ID: "m_1", SessionID: "s1", Role: "assistant",
	})

	if err := store.SetFinishReason(context.Background(), "m_1", "stop"); err != nil {
		t.Fatalf("SetFinishReason: %v", err)
	}

	msgs, _ := store.Messages(context.Background(), "s1", 0)
	if msgs[0].FinishReason != "stop" {
		t.Errorf("FinishReason = %q", msgs[0].FinishReason)
	}
}

func TestMemoryStore_Concurrent(t *testing.T) {
	store := NewMemoryStore()
	_ = store.CreateSession(context.Background(), &Session{ID: "s1", CreatedAt: time.Now()})

	const n = 50
	done := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_ = store.AppendMessage(context.Background(), &Message{
				ID:        newTestID("msg", i),
				SessionID: "s1",
				Role:      "user",
			})
			done <- true
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}

	msgs, _ := store.Messages(context.Background(), "s1", 0)
	if len(msgs) != n {
		t.Errorf("并发后 count = %d, 期望 %d", len(msgs), n)
	}
}

func TestMemoryStore_Stats(t *testing.T) {
	store := NewMemoryStore()
	_ = store.CreateSession(context.Background(), &Session{ID: "s1", CreatedAt: time.Now()})
	_ = store.AppendMessage(context.Background(), &Message{ID: "m1", SessionID: "s1", Role: "user"})
	_ = store.UpsertPart(context.Background(), &TextPart{ID: "p1", MessageID: "m1", SessionID: "s1"})

	s, m, p := store.Stats()
	if s != 1 || m != 1 || p != 1 {
		t.Errorf("Stats = (%d, %d, %d), 期望 (1,1,1)", s, m, p)
	}
}

// 辅助：生成测试用 ID
var testSeq int

func newTestID(prefix string, i int) string {
	testSeq++
	return prefix + "_" + newTestID2(testSeq) + "_" + newTestID2(i)
}

func newTestID2(i int) string {
	if i < 0 {
		i = 0
	}
	const chars = "0123456789"
	if i < 10 {
		return string(chars[i])
	}
	return newTestID2(i/10) + string(chars[i%10])
}
