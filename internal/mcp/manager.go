// Package mcp - manager.go：管理多个 MCP server client 的生命周期与工具注册。
package mcp

import (
	"context"
	"log/slog"

	"acorncode/internal/tool"
)

// Manager 持有多个已连接的 MCP client，便于统一关闭。
type Manager struct {
	clients []*Client
}

// SetupFromConfigs 按配置启动所有 MCP server，握手、拉取工具并注册进 reg。
//
// 单个 server 启动/握手失败不致命：记日志并跳过，继续其余 server
// （让一个坏 server 不拖垮整个 agent）。返回的 Manager 含所有成功连接的 client。
func SetupFromConfigs(ctx context.Context, cfgs []Config, reg *tool.Registry) (*Manager, []string, error) {
	mgr := &Manager{}
	var allIDs []string
	for _, cfg := range cfgs {
		c, err := NewClient(ctx, cfg)
		if err != nil {
			slog.Warn("mcp server 启动失败，跳过", "server", cfg.Name, "err", err)
			continue
		}
		if err := c.Initialize(ctx); err != nil {
			slog.Warn("mcp server 握手失败，跳过", "server", cfg.Name, "err", err)
			_ = c.Close()
			continue
		}
		ids, err := RegisterTools(ctx, c, reg)
		if err != nil {
			slog.Warn("mcp server 拉取工具失败，跳过", "server", cfg.Name, "err", err)
			_ = c.Close()
			continue
		}
		mgr.clients = append(mgr.clients, c)
		allIDs = append(allIDs, ids...)
		slog.Info("mcp server 已连接", "server", cfg.Name, "tools", len(ids))
	}
	return mgr, allIDs, nil
}

// Close 关闭所有 client。幂等。
func (m *Manager) Close() {
	for _, c := range m.clients {
		_ = c.Close()
	}
	m.clients = nil
}
