// Package permission - 权限 broker
//
// v0.1：始终允许。
// v1：检查 session allow list + 政策 + 阻塞等用户回复。
package permission

import (
	"context"
)

// Broker 权限中介。v0.1 始终允许。
type Broker struct {
	rules []Rule
}

func NewBroker(rules []Rule) *Broker {
	return &Broker{rules: rules}
}

func (b *Broker) Ask(ctx context.Context, req Request) error {
	return nil
}
