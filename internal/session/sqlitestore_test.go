package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore 返回 in-memory SQLiteStore
func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore err: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLiteStore_NewInMemory(t *testing.T) {
	s, err := NewSQLiteStore("")
	if err != nil {
		t.Fatalf("NewSQLiteStore err: %v", err)
	}
	defer s.Close()
}

func TestSQLiteStore_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore err: %v", err)
	}
	defer s.Close()

	// 重新打开验证持久化
	s2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen err: %v", err)
	}
	defer s2.Close()
}

// ========== Session ==========

func TestSQLiteStore_CreateAndGetSession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sess := &Session{
		ID:        "sess_1",
		Title:     "test",
		Directory: "/tmp",
		Agent:     "build",
	}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("Create err: %v", err)
	}

	got, err := s.GetSession(ctx, "sess_1")
	if err != nil {
		t.Fatalf("Get err: %v", err)
	}
	if got.Title != "test" || got.Directory != "/tmp" || got.Agent != "build" {
		t.Errorf("Get 返错: %+v", got)
	}
}

func TestSQLiteStore_CreateEmptyID(t *testing.T) {
	s := newTestStore(t)
	err := s.CreateSession(context.Background(), &Session{})
	if err == nil {
		t.Errorf("空 ID 应 error")
	}
}

func TestSQLiteStore_CreateDuplicate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, &Session{ID: "s1"})

	err := s.CreateSession(ctx, &Session{ID: "s1"})
	if err == nil {
		t.Errorf("重复 ID 应 error")
	}
}

func TestSQLiteStore_GetNotExist(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetSession(context.Background(), "no_such")
	if err == nil {
		t.Errorf("不存在应 error")
	}
}

func TestSQLiteStore_ListSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_ = s.CreateSession(ctx, &Session{ID: "s1", Title: "first"})
	time.Sleep(50 * time.Millisecond) // 确保 created_at 不同（秒级精度）
	_ = s.CreateSession(ctx, &Session{ID: "s2", Title: "second"})

	list, err := s.ListSessions(ctx)
	if err != nil {
		t.Fatalf("List err: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("List len = %d, 期望 2", len(list))
	}
	// 按 created_at DESC，最新（s2）应该先
	if list[0].ID != "s2" {
		t.Errorf("最新应在最前: list[0] = %s, 期望 s2", list[0].ID)
	}
}

// ========== Message ==========

func TestSQLiteStore_AppendAndGetMessages(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, &Session{ID: "s1"})

	// 追加 3 条消息
	for i, role := range []string{"user", "assistant", "user"} {
		msg := &Message{
			ID:        "msg_" + string(rune('a'+i)),
			SessionID: "s1",
			Role:      role,
		}
		if err := s.AppendMessage(ctx, msg); err != nil {
			t.Fatalf("Append err: %v", err)
		}
	}

	msgs, err := s.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages err: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("len = %d, 期望 3", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Errorf("顺序错: %+v", msgs)
	}
}

func TestSQLiteStore_MessagesLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, &Session{ID: "s1"})

	for i := 0; i < 5; i++ {
		_ = s.AppendMessage(ctx, &Message{ID: "m" + string(rune('0'+i)), SessionID: "s1", Role: "user"})
	}

	msgs, err := s.Messages(ctx, "s1", 3)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("limit 3 应返 3 条, got %d", len(msgs))
	}
	// 保留最近 3 条
	if msgs[0].ID != "m2" {
		t.Errorf("limit 应保留后 3 条: msgs[0] = %s, 期望 m2", msgs[0].ID)
	}
}

func TestSQLiteStore_AppendEmptyID(t *testing.T) {
	s := newTestStore(t)
	err := s.AppendMessage(context.Background(), &Message{})
	if err == nil {
		t.Errorf("空 ID 应 error")
	}
}

func TestSQLiteStore_SetFinishReason(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, &Session{ID: "s1"})
	_ = s.AppendMessage(ctx, &Message{ID: "m1", SessionID: "s1", Role: "assistant"})

	if err := s.SetFinishReason(ctx, "m1", "stop"); err != nil {
		t.Fatalf("SetFinishReason err: %v", err)
	}
	msgs, _ := s.Messages(ctx, "s1", 0)
	if msgs[0].FinishReason != "stop" {
		t.Errorf("FinishReason = %q, 期望 stop", msgs[0].FinishReason)
	}
}

// ========== Part ==========

func TestSQLiteStore_UpsertTextPart(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, &Session{ID: "s1"})

	p := &TextPart{ID: "p1", MessageID: "m1", SessionID: "s1", Text: "hello"}
	if err := s.UpsertPart(ctx, p); err != nil {
		t.Fatalf("Upsert err: %v", err)
	}

	got, err := s.GetPart(ctx, "p1")
	if err != nil {
		t.Fatalf("Get err: %v", err)
	}
	tp, ok := got.(*TextPart)
	if !ok {
		t.Fatalf("类型错: %T", got)
	}
	if tp.Text != "hello" {
		t.Errorf("Text = %q, 期望 hello", tp.Text)
	}
}

func TestSQLiteStore_UpsertToolPart(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, &Session{ID: "s1"})

	p := &ToolPart{
		ID:        "tp1",
		MessageID: "m1",
		SessionID: "s1",
		CallID:    "call_1",
		ToolID:    "read",
		Args:      []byte(`{"path":"/foo"}`),
		Output:    "file content",
		State:     ToolComplete,
	}
	if err := s.UpsertPart(ctx, p); err != nil {
		t.Fatalf("Upsert err: %v", err)
	}

	got, err := s.GetPart(ctx, "tp1")
	if err != nil {
		t.Fatalf("Get err: %v", err)
	}
	tp, ok := got.(*ToolPart)
	if !ok {
		t.Fatalf("类型错: %T", got)
	}
	if tp.ToolID != "read" || tp.State != ToolComplete || tp.Output != "file content" {
		t.Errorf("ToolPart 数据错: %+v", tp)
	}
}

func TestSQLiteStore_UpsertUpdate(t *testing.T) {
	// Upsert 应能更新已存在的 part
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, &Session{ID: "s1"})

	p1 := &TextPart{ID: "p1", SessionID: "s1", MessageID: "m1", Text: "v1"}
	if err := s.UpsertPart(ctx, p1); err != nil {
		t.Fatalf("Upsert 1 err: %v", err)
	}

	p2 := &TextPart{ID: "p1", SessionID: "s1", MessageID: "m1", Text: "v2"}
	if err := s.UpsertPart(ctx, p2); err != nil {
		t.Fatalf("Upsert 2 err: %v", err)
	}

	got, _ := s.GetPart(ctx, "p1")
	tp := got.(*TextPart)
	if tp.Text != "v2" {
		t.Errorf("Upsert 未更新: got %q, 期望 v2", tp.Text)
	}
}

func TestSQLiteStore_GetPartNotExist(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetPart(context.Background(), "no_such")
	if err == nil {
		t.Errorf("不存在应 error")
	}
}

func TestSQLiteStore_PartAttachedToMessage(t *testing.T) {
	// Message 加载时附带 parts
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, &Session{ID: "s1"})
	_ = s.AppendMessage(ctx, &Message{ID: "m1", SessionID: "s1", Role: "assistant"})

	_ = s.UpsertPart(ctx, &TextPart{ID: "p1", MessageID: "m1", SessionID: "s1", Text: "hello"})
	_ = s.UpsertPart(ctx, &ToolPart{ID: "p2", MessageID: "m1", SessionID: "s1", ToolID: "read", State: ToolComplete})

	msgs, _ := s.Messages(ctx, "s1", 0)
	if len(msgs) != 1 {
		t.Fatalf("len = %d", len(msgs))
	}
	if len(msgs[0].Parts) != 2 {
		t.Errorf("parts = %d, 期望 2", len(msgs[0].Parts))
	}
	// 检查 part 类型
	var hasText, hasTool bool
	for _, p := range msgs[0].Parts {
		switch p.(type) {
		case *TextPart:
			hasText = true
		case *ToolPart:
			hasTool = true
		}
	}
	if !hasText || !hasTool {
		t.Errorf("parts 类型不完整: text=%v, tool=%v", hasText, hasTool)
	}
}

// ========== 持久化 ==========

func TestSQLiteStore_PersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persist.db")

	ctx := context.Background()
	{
		s, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("open err: %v", err)
		}
		_ = s.CreateSession(ctx, &Session{ID: "s1", Title: "hello"})
		_ = s.AppendMessage(ctx, &Message{ID: "m1", SessionID: "s1", Role: "user"})
		_ = s.UpsertPart(ctx, &TextPart{ID: "p1", MessageID: "m1", SessionID: "s1", Text: "data"})
		_ = s.Close()
	}

	// 重开
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen err: %v", err)
	}
	defer s.Close()

	sess, err := s.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("Get err: %v", err)
	}
	if sess.Title != "hello" {
		t.Errorf("Title 持久化失败: %s", sess.Title)
	}

	msgs, _ := s.Messages(ctx, "s1", 0)
	if len(msgs) != 1 {
		t.Errorf("messages 持久化失败")
	}

	got, _ := s.GetPart(ctx, "p1")
	if tp, ok := got.(*TextPart); !ok || tp.Text != "data" {
		t.Errorf("part 持久化失败: %+v", got)
	}
}

// ========== 错误路径 ==========

func TestSQLiteStore_EmptyPartID(t *testing.T) {
	s := newTestStore(t)
	err := s.UpsertPart(context.Background(), &TextPart{SessionID: "s1", MessageID: "m1"})
	if err == nil {
		t.Errorf("空 Part ID 应 error")
	}
}

func TestSQLiteStore_UnknownPartType(t *testing.T) {
	// 写一个未知类型的 part（手动 SQL）
	// 这种 case 在正常流程不会发生，但应不 panic
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, &Session{ID: "s1"})

	// 手动 insert 一个未知 type
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO parts (id, message_id, session_id, type, data, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"px", "", "s1", "unknown_type", []byte("{}"), time.Now().Unix())
	if err != nil {
		t.Fatalf("insert err: %v", err)
	}

	_, err = s.GetPart(ctx, "px")
	if err == nil {
		t.Errorf("未知 type 应 error")
	}
	if !errors.Is(err, err) { //nolint
		// 任何 err 即可
	}
}

// ========== ReplaceMessages（Compaction 写回，v1.10）==========

// TestSQLiteStore_ReplaceMessages_Basic 验证替换后旧消息消失、新消息可读
func TestSQLiteStore_ReplaceMessages_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sess := &Session{ID: "sess_rm", Title: "rm"}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("Create err: %v", err)
	}

	// 写入 4 条旧消息（各带一个 TextPart）
	for i, role := range []string{"user", "assistant", "user", "assistant"} {
		msg := &Message{ID: "old_" + itoa(i), SessionID: "sess_rm", Role: role}
		if err := s.AppendMessage(ctx, msg); err != nil {
			t.Fatalf("Append err: %v", err)
		}
		part := &TextPart{ID: "oldp_" + itoa(i), MessageID: msg.ID, SessionID: "sess_rm", Text: "old" + itoa(i)}
		if err := s.UpsertPart(ctx, part); err != nil {
			t.Fatalf("UpsertPart err: %v", err)
		}
	}

	// 替换为 2 条新消息（1 system summary + 1 recent）
	newMsgs := []*Message{
		{ID: "new_0", SessionID: "sess_rm", Role: "system", Parts: []Part{
			&TextPart{ID: "newp_0", MessageID: "new_0", SessionID: "sess_rm", Text: "summary"},
		}},
		{ID: "new_1", SessionID: "sess_rm", Role: "user", Parts: []Part{
			&TextPart{ID: "newp_1", MessageID: "new_1", SessionID: "sess_rm", Text: "recent"},
		}},
	}
	if err := s.ReplaceMessages(ctx, "sess_rm", newMsgs); err != nil {
		t.Fatalf("ReplaceMessages err: %v", err)
	}

	// 验证：只剩 2 条，顺序正确，parts 完整
	got, err := s.Messages(ctx, "sess_rm", 0)
	if err != nil {
		t.Fatalf("Messages err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("替换后消息数 = %d, 期望 2", len(got))
	}
	if got[0].Role != "system" || got[1].Role != "user" {
		t.Errorf("顺序错: [0]=%s [1]=%s", got[0].Role, got[1].Role)
	}
	if len(got[0].Parts) != 1 {
		t.Fatalf("summary 消息 parts 数 = %d, 期望 1", len(got[0].Parts))
	}
	if tp, ok := got[0].Parts[0].(*TextPart); !ok || tp.Text != "summary" {
		t.Errorf("summary 文本错: %+v", got[0].Parts[0])
	}

	// 旧 part 应已删除
	if _, err := s.GetPart(ctx, "oldp_0"); err == nil {
		t.Error("旧 part 应已删除")
	}
}

// TestSQLiteStore_ReplaceMessages_Empty 验证替换为空列表会清空全部消息
func TestSQLiteStore_ReplaceMessages_Empty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, &Session{ID: "sess_e"})
	_ = s.AppendMessage(ctx, &Message{ID: "m0", SessionID: "sess_e", Role: "user"})

	if err := s.ReplaceMessages(ctx, "sess_e", nil); err != nil {
		t.Fatalf("ReplaceMessages err: %v", err)
	}
	got, _ := s.Messages(ctx, "sess_e", 0)
	if len(got) != 0 {
		t.Errorf("清空后消息数 = %d, 期望 0", len(got))
	}
}

// TestSQLiteStore_ReplaceMessages_EmptyID 验证空 message ID 报错（事务回滚）
func TestSQLiteStore_ReplaceMessages_EmptyID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, &Session{ID: "sess_bad"})
	_ = s.AppendMessage(ctx, &Message{ID: "keep", SessionID: "sess_bad", Role: "user"})

	err := s.ReplaceMessages(ctx, "sess_bad", []*Message{{ID: "", Role: "user"}})
	if err == nil {
		t.Fatal("空 message ID 应报错")
	}
	// 事务回滚：原消息仍在
	got, _ := s.Messages(ctx, "sess_bad", 0)
	if len(got) != 1 {
		t.Errorf("回滚后消息数 = %d, 期望 1（原消息仍在）", len(got))
	}
}

// itoa 测试用小工具（避免引入 strconv 影响阅读）
func itoa(i int) string {
	return string(rune('0' + i))
}
