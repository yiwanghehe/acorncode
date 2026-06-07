// Package tool - testhelpers_test.go
package tool

import (
	"context"

	"acorncode/internal/permission"
)

// denyAllBroker 测试用：永远拒绝所有 ask
type denyAllBroker struct{}

func (d denyAllBroker) Ask(_ context.Context, _ permission.Request) error {
	return permission.ErrDenied
}
