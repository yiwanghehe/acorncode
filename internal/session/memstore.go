// Package session - 内存版 Store（v0.1 专用，Phase 1 替换为 SQLite）
// 目的：tracer bullet 跑通端到端，不依赖 SQLite
// 参考: docs/acorncode-architect.md §6.5
package session

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryStore 是 in-memory 实现，满足 agent.SessionStore 接口
//
// 线程安全：所有方法持锁；写操作不阻塞其他写
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	messages map[string]*Message // messageID → Message
	parts    map[string]Part     // partID → Part
	order    map[string][]string // sessionID → 有序 messageIDs
}

// NewMemoryStore 创建空 store
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string]*Session),
		messages: make(map[string]*Message),
		parts:    make(map[string]Part),
		order:    make(map[string][]string),
	}
}

// CreateSession 保存 session
func (m *MemoryStore) CreateSession(ctx context.Context, sess *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess.ID == "" {
		return fmt.Errorf("session ID 不能为空")
	}
	if _, ok := m.sessions[sess.ID]; ok {
		return fmt.Errorf("session 已存在: %s", sess.ID)
	}
	m.sessions[sess.ID] = sess
	return nil
}

// GetSession 按 ID 取 session
func (m *MemoryStore) GetSession(ctx context.Context, id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session 不存在: %s", id)
	}
	return s, nil
}

// ListSessions 列出所有 session
func (m *MemoryStore) ListSessions(ctx context.Context) ([]*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out, nil
}

// Messages 返回 session 的所有消息（按插入顺序）
func (m *MemoryStore) Messages(ctx context.Context, sessionID string, limit int) ([]*Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := m.order[sessionID]
	out := make([]*Message, 0, len(ids))
	for _, id := range ids {
		if msg, ok := m.messages[id]; ok {
			out = append(out, msg)
		}
	}
	// 已经在插入顺序
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// AppendMessage 追加消息
func (m *MemoryStore) AppendMessage(ctx context.Context, msg *Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg.ID == "" {
		return fmt.Errorf("message ID 不能为空")
	}
	if msg.SessionID == "" {
		return fmt.Errorf("session ID 不能为空")
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	msg.UpdatedAt = time.Now()
	m.messages[msg.ID] = msg
	m.order[msg.SessionID] = append(m.order[msg.SessionID], msg.ID)
	return nil
}

// UpsertPart 插入或更新 part
func (m *MemoryStore) UpsertPart(ctx context.Context, p Part) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := partID(p)
	if id == "" {
		return fmt.Errorf("part ID 不能为空")
	}
	m.parts[id] = p
	return nil
}

// GetPart 按 ID 取 part
func (m *MemoryStore) GetPart(ctx context.Context, id string) (Part, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.parts[id]
	if !ok {
		return nil, fmt.Errorf("part 不存在: %s", id)
	}
	return p, nil
}

// partID 从 Part 接口里抽 ID（避免在接口上暴露 .ID() 方法名冲突）
func partID(p Part) string {
	switch v := p.(type) {
	case *TextPart:
		return v.ID
	case *ToolPart:
		return v.ID
	case *ReasoningPart:
		return v.ID
	}
	return ""
}

// SetFinishReason 设置 message 的 finish_reason
func (m *MemoryStore) SetFinishReason(ctx context.Context, messageID string, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	msg, ok := m.messages[messageID]
	if !ok {
		return fmt.Errorf("message 不存在: %s", messageID)
	}
	msg.FinishReason = reason
	msg.UpdatedAt = time.Now()
	return nil
}

// Stats 返回 store 内部状态（用于调试）
func (m *MemoryStore) Stats() (sessions, messages, parts int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions), len(m.messages), len(m.parts)
}
