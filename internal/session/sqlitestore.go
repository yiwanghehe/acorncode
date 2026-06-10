// Package session - SQLite 持久化（v0.5）
//
// 用 modernc.org/sqlite（纯 Go，无 CGo）+ 标准库 database/sql（v1.8 移除 sqlx 依赖）。
// 接口与 MemoryStore 一致，agent.SessionStore 可互换。
package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // 注册 driver
)

// SQLiteStore 是 SQLite 实现
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建 / 打开 SQLite db
//
// path 为空 → ":memory:" 模式（仅测试）
// path 为文件 → 自动创建父目录
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = ":memory:"
	} else {
		// 确保父目录存在
		dir := filepath.Dir(path)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("mkdir %s: %w", dir, err)
			}
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// WAL + 单连接（写多读少，写锁）
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("pragma WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return nil, fmt.Errorf("pragma busy_timeout: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close 关闭 db
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// migrate 建表
func (s *SQLiteStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			parent_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			directory TEXT NOT NULL DEFAULT '',
			agent TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			finish_reason TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS parts (
			id TEXT PRIMARY KEY,
			message_id TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL,
			type TEXT NOT NULL,
			data BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_parts_session ON parts(session_id, created_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// ========== Session 操作 ==========

// CreateSession 保存 session
func (s *SQLiteStore) CreateSession(ctx context.Context, sess *Session) error {
	if sess.ID == "" {
		return fmt.Errorf("session ID 不能为空")
	}
	now := time.Now()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	sess.UpdatedAt = now

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, parent_id, title, directory, agent, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.ParentID, sess.Title, sess.Directory, sess.Agent,
		sess.CreatedAt.UnixMilli(), sess.UpdatedAt.UnixMilli(),
	)
	return err
}

// GetSession 按 ID 取 session
func (s *SQLiteStore) GetSession(ctx context.Context, id string) (*Session, error) {
	var (
		rowID, parentID, title, directory, agent string
		createdAt, updatedAt                     int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, parent_id, title, directory, agent, created_at, updated_at
		 FROM sessions WHERE id = ?`, id).
		Scan(&rowID, &parentID, &title, &directory, &agent, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session 不存在: %s", id)
	}
	if err != nil {
		return nil, err
	}
	return &Session{
		ID:        rowID,
		ParentID:  parentID,
		Title:     title,
		Directory: directory,
		Agent:     agent,
		CreatedAt: time.UnixMilli(createdAt),
		UpdatedAt: time.UnixMilli(updatedAt),
	}, nil
}

// ListSessions 列出所有 session（按 created_at DESC）
func (s *SQLiteStore) ListSessions(ctx context.Context) ([]*Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, parent_id, title, directory, agent, created_at, updated_at
		 FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Session
	for rows.Next() {
		var (
			id, parentID, title, directory, agent string
			createdAt, updatedAt                  int64
		)
		if err := rows.Scan(&id, &parentID, &title, &directory, &agent, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		out = append(out, &Session{
			ID:        id,
			ParentID:  parentID,
			Title:     title,
			Directory: directory,
			Agent:     agent,
			CreatedAt: time.UnixMilli(createdAt),
			UpdatedAt: time.UnixMilli(updatedAt),
		})
	}
	return out, rows.Err()
}

// ========== Message 操作 ==========

// Messages 返回 session 的所有消息（按 created_at 顺序）
func (s *SQLiteStore) Messages(ctx context.Context, sessionID string, limit int) ([]*Message, error) {
	// Step 1: 取所有 message 元数据（不取 parts）
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, role, finish_reason, created_at, updated_at
		 FROM messages WHERE session_id = ? ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}

	var out []*Message
	for rows.Next() {
		var (
			id, sid, role, finishReason string
			createdAt, updatedAt        int64
		)
		if err := rows.Scan(&id, &sid, &role, &finishReason, &createdAt, &updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, &Message{
			ID:           id,
			SessionID:    sid,
			Role:         role,
			FinishReason: finishReason,
			CreatedAt:    time.UnixMilli(createdAt),
			UpdatedAt:    time.UnixMilli(updatedAt),
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// Step 2: 加载每个 message 的 parts（rows 已关，conn 已还）
	for _, msg := range out {
		parts, err := s.partsForMessage(ctx, msg.ID, sessionID)
		if err != nil {
			return nil, err
		}
		msg.Parts = parts
	}

	// limit: 保留最近 N 条
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// AppendMessage 追加消息
func (s *SQLiteStore) AppendMessage(ctx context.Context, msg *Message) error {
	if msg.ID == "" {
		return fmt.Errorf("message ID 不能为空")
	}
	if msg.SessionID == "" {
		return fmt.Errorf("session ID 不能为空")
	}
	now := time.Now()
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = now
	}
	msg.UpdatedAt = now

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, role, finish_reason, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.SessionID, msg.Role, msg.FinishReason,
		msg.CreatedAt.UnixMilli(), msg.UpdatedAt.UnixMilli(),
	)
	return err
}

// SetFinishReason 设置 message 的 finish_reason
func (s *SQLiteStore) SetFinishReason(ctx context.Context, messageID string, reason string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`UPDATE messages SET finish_reason = ?, updated_at = ? WHERE id = ?`,
		reason, now.UnixMilli(), messageID)
	return err
}

// ========== Part 操作 ==========

// UpsertPart 插入或更新 part
func (s *SQLiteStore) UpsertPart(ctx context.Context, p Part) error {
	id, sessID, msgID, partType, data, err := encodePart(p)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("part ID 不能为空")
	}

	now := time.Now()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO parts (id, message_id, session_id, type, data, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   message_id = excluded.message_id,
		   type = excluded.type,
		   data = excluded.data`,
		id, msgID, sessID, partType, data, now.UnixMilli(),
	)
	return err
}

// GetPart 按 ID 取 part
func (s *SQLiteStore) GetPart(ctx context.Context, id string) (Part, error) {
	var (
		messageID, sessionID, partType string
		data                           []byte
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT message_id, session_id, type, data FROM parts WHERE id = ?`, id).
		Scan(&messageID, &sessionID, &partType, &data)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("part 不存在: %s", id)
	}
	if err != nil {
		return nil, err
	}
	return decodePart(id, messageID, sessionID, partType, data)
}

// partsForMessage 取 message 的所有 parts（按 created_at）
func (s *SQLiteStore) partsForMessage(ctx context.Context, messageID, sessionID string) ([]Part, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, data FROM parts WHERE message_id = ? ORDER BY created_at ASC`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Part
	for rows.Next() {
		var id, t string
		var data []byte
		if err := rows.Scan(&id, &t, &data); err != nil {
			return nil, err
		}
		p, err := decodePart(id, messageID, sessionID, t, data)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// encodePart 把 Part 接口 → (id, sessID, msgID, type, jsonData)
func encodePart(p Part) (id, sessID, msgID, partType string, data []byte, err error) {
	switch v := p.(type) {
	case *TextPart:
		id = v.ID
		sessID = v.SessionID
		msgID = v.MessageID
		partType = "text"
		data, err = json.Marshal(struct {
			Text string `json:"text"`
		}{Text: v.Text})
	case *ToolPart:
		id = v.ID
		sessID = v.SessionID
		msgID = v.MessageID
		partType = "tool"
		data, err = json.Marshal(struct {
			CallID    string          `json:"call_id"`
			ToolID    string          `json:"tool_id"`
			Args      json.RawMessage `json:"args"`
			Output    string          `json:"output"`
			Title     string          `json:"title"`
			State     ToolState       `json:"state"`
			Error     string          `json:"error,omitempty"`
			StartedAt int64           `json:"started_at,omitempty"`
			EndedAt   int64           `json:"ended_at,omitempty"`
		}{
			CallID: v.CallID, ToolID: v.ToolID, Args: v.Args,
			Output: v.Output, Title: v.Title, State: v.State, Error: v.Error,
			StartedAt: v.StartedAt, EndedAt: v.EndedAt,
		})
	case *ReasoningPart:
		id = v.ID
		sessID = v.SessionID
		msgID = v.MessageID
		partType = "reasoning"
		data, err = json.Marshal(struct {
			Text string `json:"text"`
		}{Text: v.Text})
	default:
		err = fmt.Errorf("unknown part type: %T", p)
	}
	return
}

// decodePart 把 (id, msgID, sessID, type, data) → Part
func decodePart(id, msgID, sessID, partType string, data []byte) (Part, error) {
	switch partType {
	case "text":
		var d struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		return &TextPart{ID: id, MessageID: msgID, SessionID: sessID, Text: d.Text}, nil
	case "tool":
		var d struct {
			CallID    string          `json:"call_id"`
			ToolID    string          `json:"tool_id"`
			Args      json.RawMessage `json:"args"`
			Output    string          `json:"output"`
			Title     string          `json:"title"`
			State     ToolState       `json:"state"`
			Error     string          `json:"error,omitempty"`
			StartedAt int64           `json:"started_at,omitempty"`
			EndedAt   int64           `json:"ended_at,omitempty"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		return &ToolPart{
			ID: id, MessageID: msgID, SessionID: sessID,
			CallID: d.CallID, ToolID: d.ToolID, Args: d.Args,
			Output: d.Output, Title: d.Title, State: d.State, Error: d.Error,
			StartedAt: d.StartedAt, EndedAt: d.EndedAt,
		}, nil
	case "reasoning":
		var d struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		return &ReasoningPart{ID: id, MessageID: msgID, SessionID: sessID, Text: d.Text}, nil
	}
	return nil, fmt.Errorf("unknown part type: %s", partType)
}
