// Package permission - 类型
package permission

import "errors"

type Action string

const (
	ActionAllow Action = "allow"
	ActionAsk   Action = "ask"
	ActionDeny  Action = "deny"
)

type Rule struct {
	Permission string
	Pattern    string
	Action     Action
}

type Request struct {
	ID         string
	SessionID  string
	Permission string
	Patterns   []string
	Metadata   map[string]any
	Tool       *ToolRef
}

type ToolRef struct {
	MessageID string
	CallID    string
}

var ErrDenied = errors.New("permission denied")
