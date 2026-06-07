// Package permission - 权限 broker
//
// v0.3 起：支持 acorncode.json 规则匹配 + session-level allow list。
// v0.4 起：TUI 弹窗（v0.3 的 ask 仍默认 allow，记日志）。
package permission

import (
	"context"
	"log/slog"
	"regexp"
	"sync"
)

// Broker 权限中介
type Broker struct {
	mu       sync.RWMutex
	rules    []Rule           // 从 acorncode.json 加载
	approved map[ruleKey]bool // session-level 已批准（key = tool + pattern）
}

// ruleKey 是 session allow 的索引
type ruleKey struct {
	Tool    string
	Pattern string
}

func NewBroker(rules []Rule) *Broker {
	return &Broker{
		rules:    rules,
		approved: make(map[ruleKey]bool),
	}
}

// AddRules 追加规则（v0.3 启动时 main.go 调）
func (b *Broker) AddRules(rules []Rule) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rules = append(b.rules, rules...)
}

// SessionApprove 标记 (tool, pattern) 为 session 级别已批准
func (b *Broker) SessionApprove(tool, pattern string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.approved[ruleKey{Tool: tool, Pattern: pattern}] = true
}

// Ask 检查权限：
//
//  1. 找匹配 rule（tool + pattern）
//  2. action=allow → nil
//  3. action=deny  → ErrDenied
//  4. action=ask   → v0.3 默认 allow + slog.Warn；v0.4 TUI 弹窗
//  5. 无 rule      → 检查 session allow，有则 nil；无则 nil（兼容 v0.1）
//
// pattern == "" → 只匹配 tool
func (b *Broker) Ask(ctx context.Context, req Request) error {
	tool := req.Permission
	pattern := ""
	if len(req.Patterns) > 0 {
		pattern = req.Patterns[0] // v0.3 单 pattern 简化
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	// 1. 规则匹配
	for _, r := range b.rules {
		if r.Permission != tool {
			continue
		}
		// pattern 过滤（rule 没设 pattern 则匹配任何）
		if r.Pattern != "" {
			matched, err := regexp.MatchString(r.Pattern, pattern)
			if err != nil {
				// 坏 regex 跳过此 rule
				slog.Warn("permission: 坏 pattern 跳过", "tool", tool, "pattern", r.Pattern, "err", err)
				continue
			}
			if !matched {
				continue
			}
		}
		// 匹配到
		switch r.Action {
		case ActionAllow:
			return nil
		case ActionDeny:
			return ErrDenied
		case ActionAsk:
			slog.Warn("permission: ask（v0.3 占位：默认 allow；v0.4 TUI 弹窗）", "tool", tool, "pattern", pattern)
			return nil
		}
	}

	// 2. 无 rule：检查 session allow
	if pattern != "" && b.approved[ruleKey{Tool: tool, Pattern: pattern}] {
		return nil
	}
	// tool-level session allow
	if b.approved[ruleKey{Tool: tool, Pattern: ""}] {
		return nil
	}

	// 3. 默认：allow（v0.1 兼容；v1 改成 deny）
	return nil
}
