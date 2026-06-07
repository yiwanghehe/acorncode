// Package tool - edit.go
// 参考: docs/acorncode-architect.md §16.4.2
package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"acorncode/internal/permission"
)

//go:embed schemas/edit.json
var editSchema []byte

// EditArgs 是 edit 工具的入参
type EditArgs struct {
	FilePath   string `json:"filePath"`
	OldString  string `json:"oldString"`
	NewString  string `json:"newString"`
	ReplaceAll bool   `json:"replaceAll,omitempty"`
}

// Edit 是 edit 工具的实现
type Edit struct {
	Cwd string // 默认 cwd（当 path 相对时使用）
}

// Definition 返回 edit 工具的元数据
func (e *Edit) Definition() Definition {
	return Definition{
		ID: "edit",
		Description: "Edit a file by replacing an exact string match. " +
			"oldString must be unique in the file unless replaceAll=true. " +
			"ALWAYS ask for permission before calling this tool.",
		Keywords:   []string{"edit", "modify", "write", "update", "change", "patch", "替换", "编辑"},
		JSONSchema: editSchema,
	}
}

// Execute 是 edit 工具的入口
func (e *Edit) Execute(ctx context.Context, args json.RawMessage, tc Context) (Result, error) {
	var a EditArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{
			Status: "error",
			Title:  "edit",
			Output: "参数解析失败: " + err.Error(),
		}, nil
	}

	if a.FilePath == "" {
		return Result{
			Status: "error",
			Title:  "edit",
			Output: "filePath 不能为空",
		}, nil
	}
	if a.OldString == "" {
		return Result{
			Status: "error",
			Title:  "edit",
			Output: "oldString 不能为空（会替换整个文件）",
		}, nil
	}

	// 1. 路径 normalize
	path := a.FilePath
	if !filepath.IsAbs(path) {
		cwd := tc.Cwd
		if cwd == "" {
			cwd = e.Cwd
		}
		if cwd == "" {
			cwd = "."
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)

	// 2. 权限询问（v0.1 broker 始终允许，v1 加 session allow）
	if tc.Ask != nil {
		if err := tc.Ask(ctx, permission.Request{
			ID:         "edit-" + path,
			SessionID:  tc.SessionID,
			Permission: "edit",
			Patterns:   []string{path},
			Tool: &permission.ToolRef{
				MessageID: tc.MessageID,
				CallID:    tc.CallID,
			},
		}); err != nil {
			return Result{
				Status: "error",
				Title:  "Edit " + path,
				Output: "permission denied: " + err.Error(),
			}, nil
		}
	}

	// 3. 检查文件存在
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{
				Status: "error",
				Title:  "Edit " + path,
				Output: "文件不存在: " + path,
			}, nil
		}
		return Result{
			Status: "error",
			Title:  "Edit " + path,
			Output: "stat 失败: " + err.Error(),
		}, nil
	}
	if info.IsDir() {
		return Result{
			Status: "error",
			Title:  "Edit " + path,
			Output: "是目录，不能编辑",
		}, nil
	}

	// 4. 读文件
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{
			Status: "error",
			Title:  "Edit " + path,
			Output: "读文件失败: " + err.Error(),
		}, nil
	}
	content := string(data)

	// 5. 找匹配
	count := strings.Count(content, a.OldString)
	if count == 0 {
		return Result{
			Status: "error",
			Title:  "Edit " + path,
			Output: "oldString 在文件中未找到",
		}, nil
	}
	if count > 1 && !a.ReplaceAll {
		return Result{
			Status: "error",
			Title:  "Edit " + path,
			Output: fmt.Sprintf(
				"oldString 匹配 %d 处。请添加更多上下文使其唯一，或设置 replaceAll=true",
				count),
		}, nil
	}

	// 6. 应用替换
	var newContent string
	if a.ReplaceAll {
		newContent = strings.ReplaceAll(content, a.OldString, a.NewString)
	} else {
		newContent = strings.Replace(content, a.OldString, a.NewString, 1)
	}

	// 7. 写回（保留原 mode）
	if err := os.WriteFile(path, []byte(newContent), info.Mode()); err != nil {
		return Result{
			Status: "error",
			Title:  "Edit " + path,
			Output: "写文件失败: " + err.Error(),
		}, nil
	}

	// 8. metadata
	if tc.Metadata != nil {
		tc.Metadata(fmt.Sprintf("Edited %s", path), map[string]any{
			"path":      path,
			"replaced":  count,
			"old_bytes": len(content),
			"new_bytes": len(newContent),
		})
	}

	return Result{
		Status: "success",
		Title:  fmt.Sprintf("Edit %s (%d 处替换)", path, count),
		Output: fmt.Sprintf(
			"已替换 %d 处。\n旧长度: %d 字节\n新长度: %d 字节",
			count, len(content), len(newContent)),
	}, nil
}
