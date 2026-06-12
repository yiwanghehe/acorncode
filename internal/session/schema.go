// Package session - 会话/消息/Part 的类型定义
// 参考: docs/architecture.md §3.5 / §7
package session

import (
	"encoding/json"
	"time"
)

// UserMessage 是用户输入的消息
type UserMessage struct {
	Text string
}

// Session 是一个会话的元信息
type Session struct {
	ID        string
	ParentID  string
	Title     string
	Directory string
	Agent     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Message 是一条会话消息
type Message struct {
	ID           string
	SessionID    string
	Role         string // "user" | "assistant"
	Parts        []Part
	FinishReason string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Part 是 sum type。实现：TextPart、ToolPart、ReasoningPart
type Part interface {
	partType() string
}

// TextPart 文本 part
type TextPart struct {
	ID        string `json:"id"`
	MessageID string `json:"message_id"`
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

func (t *TextPart) partType() string { return "text" }

// ToolState 工具 part 状态
type ToolState string

const (
	ToolPending  ToolState = "pending"   // args 收集中
	ToolRunning  ToolState = "running"   // tool.Execute 跑着
	ToolComplete ToolState = "completed" // 成功
	ToolErrored  ToolState = "error"     // tool 自身错误
	ToolRejected ToolState = "rejected"  // permission 拒
)

// ToolPart 工具调用 part
type ToolPart struct {
	ID        string          `json:"id"`
	MessageID string          `json:"message_id"`
	SessionID string          `json:"session_id"`
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

func (t *ToolPart) partType() string { return "tool" }

// ArgsMap 把 Args 解析为 map（用于 Loop 内访问）
func (t *ToolPart) ArgsMap() map[string]any {
	var m map[string]any
	if len(t.Args) == 0 {
		return nil
	}
	_ = json.Unmarshal(t.Args, &m)
	return m
}

// ReasoningPart 思考 part
type ReasoningPart struct {
	ID        string
	MessageID string
	SessionID string
	Text      string
}

func (r *ReasoningPart) partType() string { return "reasoning" }
