// Package tool - read.go 读文件工具
// 参考: docs/acorncode-architect.md §16.4.1
package tool

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

//go:embed schemas/read.json
var readSchema []byte

// ReadArgs 是 read 工具的入参
type ReadArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"` // 0-based 行号
	Limit  int    `json:"limit,omitempty"`  // 0 = 读剩余全部
}

// Read 是 read 工具的实现
type Read struct {
	MaxBytes int    // 单次返回最大字节数（v1 默认 50000）
	Cwd      string // 默认 cwd（当 path 相对时使用）
}

// Definition 返回 read 工具的元数据
func (r *Read) Definition() Definition {
	return Definition{
		ID:          "read",
		Description: "Read a file's contents. Returns the file text, optionally limited to a line range. Use offset/limit for large files. Always returns the file as UTF-8 text; binary files will be returned with replacement bytes.",
		Keywords:    []string{"read", "open", "view", "cat", "file", "查看", "读"},
		JSONSchema:  readSchema,
	}
}

// Execute 是 read 工具的入口（见 §16.4.1）
func (r *Read) Execute(ctx context.Context, args json.RawMessage, tc Context) (Result, error) {
	var a ReadArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{
			Status: "error",
			Title:  "read",
			Output: "参数解析失败: " + err.Error(),
		}, nil
	}

	if a.Path == "" {
		return Result{
			Status: "error",
			Title:  "read",
			Output: "path 不能为空",
		}, nil
	}

	// 1. 路径 normalize
	path := a.Path
	if !filepath.IsAbs(path) {
		cwd := tc.Cwd
		if cwd == "" {
			cwd = r.Cwd
		}
		if cwd == "" {
			cwd = "."
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)

	// 2. 检查文件存在
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{
				Status: "error",
				Title:  "read " + path,
				Output: "文件不存在: " + path,
			}, nil
		}
		return Result{
			Status: "error",
			Title:  "read " + path,
			Output: "stat 失败: " + err.Error(),
		}, nil
	}
	if info.IsDir() {
		return Result{
			Status: "error",
			Title:  "read " + path,
			Output: "是目录，请用 bash 'ls' 或 glob 工具",
		}, nil
	}

	// 3. 读
	maxBytes := r.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 50000
	}

	content, truncated, err := r.readContent(ctx, path, a.Offset, a.Limit, maxBytes)
	if err != nil {
		// ctx 取消单独处理
		if ctx.Err() != nil {
			return Result{
				Status: "error",
				Title:  "read " + path,
				Output: "ctx 已取消: " + ctx.Err().Error(),
			}, nil
		}
		return Result{
			Status: "error",
			Title:  "read " + path,
			Output: err.Error(),
		}, nil
	}

	// 4. metadata 通知（TUI 用，可选）
	if tc.Metadata != nil {
		tc.Metadata(fmt.Sprintf("read %s (%d 行)", path, countLines(content)), map[string]any{
			"path":       path,
			"line_count": countLines(content),
			"byte_size":  len(content),
			"truncated":  truncated,
			"offset":     a.Offset,
			"limit":      a.Limit,
		})
	}

	output := content
	if truncated {
		output += "\n\n[... 已截断，超出 " + fmt.Sprint(maxBytes) + " 字节上限]"
	}

	return Result{
		Status:      "success",
		Title:       "read " + path,
		Output:      output,
		IsTruncated: truncated,
	}, nil
}

// readContent 实际读文件逻辑
//
//	offset=0, limit=0 → 整文件
//	offset>0         → 跳过前 offset 行
//	limit>0          → 最多再读 limit 行
func (r *Read) readContent(ctx context.Context, path string, offset, limit, maxBytes int) (string, bool, error) {
	// 整文件路径：直接 os.ReadFile（简单但 O(file size) 内存）
	if offset == 0 && limit == 0 {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false, err
		}
		// 检查 ctx（防止大文件读完后才检查）
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		// 去掉单个尾部换行，与 offset/limit 路径保持一致
		// （offset/limit 用 bufio.Scanner 时已经不含尾换行）
		if len(data) > 0 && data[len(data)-1] == '\n' {
			data = data[:len(data)-1]
		}
		if len(data) > maxBytes {
			// UTF-8 安全截断，回退到字符边界（避免切碎中文/emoji）
			return truncateUTF8(string(data), maxBytes), true, nil
		}
		return string(data), false, nil
	}

	// offset/limit 路径：用 bufio.Scanner 流式读
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// 单行最大 1MB（防止恶意单行 OOM）
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var sb strings.Builder
	sb.Grow(maxBytes) // 预分配
	lineNum := 0
	written := 0

	for scanner.Scan() {
		// ctx 优先检查
		if err := ctx.Err(); err != nil {
			return "", false, err
		}

		// 跳过 offset 之前的行
		if lineNum < offset {
			lineNum++
			continue
		}

		// 达到 limit 上限
		if limit > 0 && written >= limit {
			break
		}

		if written > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(scanner.Text())
		written++
		lineNum++
	}

	if err := scanner.Err(); err != nil {
		return "", false, err
	}

	result := sb.String()
	truncated := false
	if len(result) > maxBytes {
		// 按字节截断，但回退到最近的 UTF-8 字符边界，避免切碎多字节字符（如中文）
		result = truncateUTF8(result, maxBytes)
		truncated = true
	}

	// 检查 offset 是否超出文件范围
	if written == 0 && offset > 0 {
		return "", false, fmt.Errorf("offset %d 超出文件行数（0 行被读取）", offset)
	}

	return result, truncated, nil
}

// countLines 数文本行数
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// truncateUTF8 把 s 截断到不超过 maxBytes 字节，且回退到最近的
// UTF-8 字符边界，保证不切碎多字节字符（如中文/emoji）。
// 若 maxBytes 落在多字节字符中间，向前回退到该字符起始处之前。
func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	// 若截断点正好是完整字符边界，直接返回
	if utf8.ValidString(cut) {
		return cut
	}
	// 否则从 maxBytes 向前回退，丢掉末尾不完整的字符
	for i := maxBytes - 1; i >= 0; i-- {
		if utf8.RuneStart(s[i]) {
			// s[i] 是某个字符的首字节，截到 i（不含该不完整字符）
			return s[:i]
		}
	}
	return ""
}
