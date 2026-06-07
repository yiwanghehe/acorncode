// Package bus - 简化 pub/sub（6 事件，64-buffered channels）
package bus

import (
	"log/slog"
	"sync"
)

const (
	EventPartDelta        = "part.delta"
	EventPartUpdated      = "part.updated"
	EventPermissionAsked  = "permission.asked"
	EventPermissionReply  = "permission.replied"
	EventAgentStateChange = "agent.state.change"
	EventError            = "error"
)

// Event 是 Bus 传递的事件
type Event struct {
	Type      string
	SessionID string
	Data      any
}

type subscription struct {
	id int
	ch chan Event
}

// Bus 是 64-buffered 的 pub/sub
type Bus struct {
	mu     sync.RWMutex
	subs   map[string]map[int]*subscription
	nextID int
}

// New 创建 Bus
func New() *Bus {
	return &Bus{subs: make(map[string]map[int]*subscription)}
}

// SubscribeID 返回 channel 和订阅 id（用于后续 Unsubscribe）
func (b *Bus) SubscribeID(topic string) (<-chan Event, int) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[topic] == nil {
		b.subs[topic] = make(map[int]*subscription)
	}
	id := b.nextID
	b.nextID++
	b.subs[topic][id] = &subscription{id: id, ch: ch}
	return ch, id
}

// Unsubscribe 关闭给定 topic+id 对应的 channel 并从 map 删除
func (b *Bus) Unsubscribe(topic string, id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if subs, ok := b.subs[topic]; ok {
		if s, ok := subs[id]; ok {
			close(s.ch)
		}
		delete(b.subs[topic], id)
	}
}

// Publish 同步 fan-out。慢消费者 drop + warn（不阻塞 LLM 流）
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	subs := b.subs[e.Type]
	snapshot := make([]*subscription, 0, len(subs))
	for _, s := range subs {
		snapshot = append(snapshot, s)
	}
	b.mu.RUnlock()

	for _, s := range snapshot {
		select {
		case s.ch <- e:
		default:
			slog.Warn("bus: 订阅者处理慢，丢弃事件", "topic", e.Type)
		}
	}
}

// Close 关闭所有订阅
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, subs := range b.subs {
		for _, s := range subs {
			close(s.ch)
		}
	}
	b.subs = make(map[string]map[int]*subscription)
}
