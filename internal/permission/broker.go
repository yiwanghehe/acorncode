// Package permission - 权限 broker
//
// v0.3 起：支持 acorncode.json 规则匹配 + session-level allow list。
// v1.0.1 起：rule=ask 真阻塞，等 TUI Reply（v1.0 前默认 allow + log）。
package permission

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

// DefaultAskTimeout 是 ask 弹窗等用户回复的超时
const DefaultAskTimeout = 60 * time.Second

// Broker 权限中介
type Broker struct {
	mu       sync.RWMutex
	rules    []Rule           // 从 acorncode.json 加载
	approved map[ruleKey]bool // session-level 已批准（key = tool + pattern）

	// ask 等待表：reqID → chan error（v1.0.1 起）
	askWaits map[string]chan error
	bus      Publisher // 可选：发 EventPermissionAsked（v1.0.1 起）
}

// Publisher 是 broker 用的事件发布接口（bus.Publisher 子集）
type Publisher interface {
	Publish(ev Event)
}

// Event 是 bus.Event 的子集（避免 import 循环）
type Event struct {
	Type      string
	SessionID string
	Data      any
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
		askWaits: make(map[string]chan error),
	}
}

// SetPublisher 注入事件发布器（v1.0.1 起）
func (b *Broker) SetPublisher(p Publisher) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bus = p
}

// AddRules 追加规则
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

// AskWaitCount 返回当前等待中的 ask 请求数（测试 / 调试用）
func (b *Broker) AskWaitCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.askWaits)
}

// Reply 是 Ask 的解阻调用（TUI 用户选完后调）
//
// decision: "allow" / "session_allow" → nil；"deny" → ErrDenied
func (b *Broker) Reply(reqID string, decision string, _ string) error {
	b.mu.Lock()
	ch, ok := b.askWaits[reqID]
	if !ok {
		b.mu.Unlock()
		return fmt.Errorf("permission: unknown reqID: %s", reqID)
	}
	delete(b.askWaits, reqID)
	b.mu.Unlock()

	var result error
	switch decision {
	case "allow", "session_allow":
		result = nil
	case "deny":
		result = ErrDenied
	default:
		result = fmt.Errorf("permission: unknown decision: %s", decision)
	}

	select {
	case ch <- result:
	default:
		// ch 没人等（ctx 已超时），忽略
	}
	return nil
}

// Ask 检查权限：
//
//  1. 找匹配 rule（tool + pattern）
//  2. action=allow → nil
//  3. action=deny  → ErrDenied
//  4. action=ask   → 发 EventPermissionAsked + 阻塞等 Reply（v1.0.1 起）
//  5. 无 rule      → 检查 session allow，有则 nil；无则 nil（v0.1 兼容）
//
// pattern == "" → 只匹配 tool
func (b *Broker) Ask(ctx context.Context, req Request) error {
	tool := req.Permission
	pattern := ""
	if len(req.Patterns) > 0 {
		pattern = req.Patterns[0]
	}

	b.mu.RLock()
	// 1. 规则匹配
	for _, r := range b.rules {
		if r.Permission != tool {
			continue
		}
		if r.Pattern != "" {
			matched, err := regexp.MatchString(r.Pattern, pattern)
			if err != nil {
				slog.Warn("permission: 坏 pattern 跳过", "tool", tool, "pattern", r.Pattern, "err", err)
				continue
			}
			if !matched {
				continue
			}
		}
		action := r.Action
		b.mu.RUnlock()

		switch action {
		case ActionAllow:
			return nil
		case ActionDeny:
			return ErrDenied
		case ActionAsk:
			return b.askUser(ctx, req)
		}
		return nil
	}

	// 2. 无 rule：检查 session allow
	approved := pattern != "" && b.approved[ruleKey{Tool: tool, Pattern: pattern}]
	if !approved && b.approved[ruleKey{Tool: tool, Pattern: ""}] {
		approved = true
	}
	b.mu.RUnlock()

	if approved {
		return nil
	}

	// 3. 默认：allow（v0.1 兼容；v1 改成 deny）
	return nil
}

// askUser 阻塞等用户回复（rule=ask 时）
func (b *Broker) askUser(ctx context.Context, req Request) error {
	// 1. 注册等待 chan
	reqID := fmt.Sprintf("ask-%s-%s", req.Permission, randomShort())
	ch := make(chan error, 1)

	b.mu.Lock()
	if b.askWaits == nil {
		b.askWaits = make(map[string]chan error)
	}
	b.askWaits[reqID] = ch
	publisher := b.bus
	b.mu.Unlock()

	// 2. 发事件（让 TUI 弹窗）
	if publisher != nil {
		publisher.Publish(Event{
			Type:      "permission.asked",
			SessionID: req.SessionID,
			Data: map[string]any{
				"req_id":   reqID,
				"tool":     req.Permission,
				"patterns": req.Patterns,
				"metadata": req.Metadata,
			},
		})
	} else {
		// 没 publisher 走 fallback：log + allow（兼容无 TUI 场景）
		slog.WarnContext(ctx, "permission: ask 无 publisher，fallback allow", "tool", req.Permission)
		b.mu.Lock()
		delete(b.askWaits, reqID)
		b.mu.Unlock()
		return nil
	}

	// 3. 阻塞等 Reply
	askCtx, cancel := context.WithTimeout(ctx, DefaultAskTimeout)
	defer cancel()

	select {
	case <-askCtx.Done():
		// 超时 / ctx 取消 → 拒绝（保守）
		b.mu.Lock()
		delete(b.askWaits, reqID)
		b.mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrDenied
	case result := <-ch:
		return result
	}
}

// randomShort 生成 8 字符短 ID
func randomShort() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	now := time.Now().UnixNano()
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[now%int64(len(chars))]
		now /= int64(len(chars))
		if now == 0 {
			now = time.Now().UnixNano()
		}
	}
	return string(b)
}
