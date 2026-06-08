// Package mcp - adapter.go：把 MCP 工具包装成 acorn 的 tool.Tool 接口。
//
// 每个 MCP server 暴露的工具，包成一个 mcpTool 注册进 tool.Registry，
// 工具 ID 加 server 名前缀（如 "fs_read_file"）避免多 server 重名冲突。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"acorncode/internal/permission"
	"acorncode/internal/tool"
)

// mcpTool 把一个 MCP 工具适配为 tool.Tool。
type mcpTool struct {
	client   *Client
	prefix   string // server 名前缀，如 "fs"
	rawName  string // MCP 侧原始工具名，如 "read_file"
	toolID   string // acorn 侧工具 ID，如 "fs_read_file"
	desc     string
	schema   json.RawMessage
	keywords []string
}

// Definition 返回 acorn 工具元数据。
func (m *mcpTool) Definition() tool.Definition {
	return tool.Definition{
		ID:          m.toolID,
		Description: m.desc,
		Keywords:    m.keywords,
		JSONSchema:  m.schema,
	}
}

// Execute 把调用转发给 MCP server 的 tools/call。
func (m *mcpTool) Execute(ctx context.Context, args json.RawMessage, tc tool.Context) (tool.Result, error) {
	// 1. MCP 工具统一走权限询问（外部进程，视作潜在危险）
	if tc.Ask != nil {
		if err := tc.Ask(ctx, permission.Request{
			ID:         "mcp-" + m.toolID,
			SessionID:  tc.SessionID,
			Permission: "mcp",
			Patterns:   []string{m.toolID},
			Tool: &permission.ToolRef{
				MessageID: tc.MessageID,
				CallID:    tc.CallID,
			},
		}); err != nil {
			return tool.Result{
				Status: "error",
				Title:  m.toolID,
				Output: "permission denied: " + err.Error(),
			}, nil
		}
	}

	// 2. ctx 中断检查
	select {
	case <-ctx.Done():
		return tool.Result{Status: "error", Title: m.toolID, Output: "canceled"}, nil
	default:
	}

	// 3. 转发到 MCP server
	res, err := m.client.CallTool(ctx, m.rawName, args)
	if err != nil {
		return tool.Result{
			Status: "error",
			Title:  m.toolID,
			Output: "MCP 调用失败: " + err.Error(),
		}, nil
	}

	// 4. 拼接 text 内容块
	output := flattenContent(res.Content)
	status := "success"
	// MCP 用 isError 标记工具级错误；按 acorn 约定仍返 Result（不返 Go err）
	if res.IsError {
		status = "error"
	}
	return tool.Result{
		Status: status,
		Title:  m.toolID,
		Output: output,
	}, nil
}

// flattenContent 把多个内容块的 text 拼成一段输出。
func flattenContent(blocks []ContentBlock) string {
	var sb strings.Builder
	for i, b := range blocks {
		if b.Type != "text" {
			// 非 text（image/resource 等）v1.2 仅标注类型，不内联二进制
			sb.WriteString(fmt.Sprintf("[非文本内容: %s]", b.Type))
		} else {
			sb.WriteString(b.Text)
		}
		if i < len(blocks)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// RegisterTools 在已 Initialize 的 client 上拉取工具列表，
// 全部包装并注册进给定 registry，返回注册的工具 ID 列表。
func RegisterTools(ctx context.Context, c *Client, reg *tool.Registry) ([]string, error) {
	infos, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		toolID := info.Name
		if c.cfg.Name != "" {
			toolID = c.cfg.Name + "_" + info.Name
		}
		schema := info.InputSchema
		if len(schema) == 0 {
			// 没给 schema 时给个最小对象 schema，避免下游 nil
			schema = json.RawMessage(`{"type":"object"}`)
		}
		mt := &mcpTool{
			client:   c,
			prefix:   c.cfg.Name,
			rawName:  info.Name,
			toolID:   toolID,
			desc:     info.Description,
			schema:   schema,
			keywords: []string{"mcp", c.cfg.Name, info.Name},
		}
		reg.Register(mt)
		ids = append(ids, toolID)
	}
	return ids, nil
}
