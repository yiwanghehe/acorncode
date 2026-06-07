// Package tool - glob.go 文件模式匹配工具
//
// 支持 glob 模式：*（单层）、**（多层）、?（单字符）、[abc]（字符集）
// 区别于 filepath.Glob（不支持 **），本实现自己写匹配。
package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed schemas/glob.json
var globSchema []byte

// GlobArgs 是 glob 工具的入参
type GlobArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Type       string `json:"type,omitempty"` // "file" | "dir" | "any"
	MaxResults int    `json:"max_results,omitempty"`
}

// Glob 是 glob 工具的实现
type Glob struct {
	Cwd        string
	MaxResults int // 默认 100
}

// Definition 返回 glob 工具元数据
func (g *Glob) Definition() Definition {
	return Definition{
		ID:          "glob",
		Description: "Find files and directories by glob pattern. Supports `*` (single segment), `**` (multi-segment), `?` (single char), `[abc]` (char class). Returns absolute paths, one per line, sorted. Skips heavy dirs (.git, node_modules, vendor, etc.) by default. Use to enumerate source files matching a pattern, e.g. `**/*.go` for all Go files.",
		Keywords:    []string{"glob", "find", "list", "match", "pattern", "files", "匹配", "文件列表"},
		JSONSchema:  globSchema,
	}
}

// Execute 是 glob 工具入口
func (g *Glob) Execute(ctx context.Context, args json.RawMessage, tc Context) (Result, error) {
	var a GlobArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Status: "error", Title: "glob", Output: "参数解析失败: " + err.Error()}, nil
	}

	if a.Pattern == "" {
		return Result{Status: "error", Title: "glob", Output: "pattern 不能为空"}, nil
	}

	// 路径 normalize
	root := a.Path
	if root == "" {
		root = "."
	}
	if !filepath.IsAbs(root) {
		cwd := tc.Cwd
		if cwd == "" {
			cwd = g.Cwd
		}
		if cwd == "" {
			cwd = "."
		}
		root = filepath.Join(cwd, root)
	}
	root = filepath.Clean(root)

	// 验证根目录存在
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Status: "error", Title: "glob", Output: "目录不存在: " + root}, nil
		}
		return Result{Status: "error", Title: "glob", Output: "stat 失败: " + err.Error()}, nil
	}
	if !info.IsDir() {
		return Result{Status: "error", Title: "glob", Output: "path 不是目录: " + root}, nil
	}

	maxResults := a.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}

	// 编译 glob → regex
	re, err := globToRegex(a.Pattern)
	if err != nil {
		return Result{Status: "error", Title: "glob", Output: "pattern 非法: " + err.Error()}, nil
	}

	// 走目录
	matches, truncated, err := g.walkAndMatch(ctx, root, re, a.Type, maxResults)
	if err != nil {
		if ctx.Err() != nil {
			return Result{Status: "error", Title: "glob", Output: "ctx 已取消: " + ctx.Err().Error()}, nil
		}
		return Result{Status: "error", Title: "glob", Output: err.Error()}, nil
	}

	// metadata
	if tc.Metadata != nil {
		tc.Metadata(fmt.Sprintf("glob %s (%d 匹配)", a.Pattern, len(matches)), map[string]any{
			"pattern":   a.Pattern,
			"root":      root,
			"count":     len(matches),
			"truncated": truncated,
		})
	}

	if len(matches) == 0 {
		return Result{
			Status: "success",
			Title:  "glob (no matches)",
			Output: "无匹配",
		}, nil
	}

	output := strings.Join(matches, "\n")
	if truncated {
		output += fmt.Sprintf("\n\n[... 已截断，超出 %d 上限]", maxResults)
	}

	return Result{
		Status:      "success",
		Title:       fmt.Sprintf("glob (%d 匹配)", len(matches)),
		Output:      output,
		IsTruncated: truncated,
	}, nil
}

// walkAndMatch 走 root，匹配 re，过滤 type
func (g *Glob) walkAndMatch(ctx context.Context, root string, re *regexp.Regexp, typeFilter string, maxResults int) ([]string, bool, error) {
	var out []string
	truncated := false

	// 决定要不要走目录
	wantFiles := typeFilter == "" || typeFilter == "file" || typeFilter == "any"
	wantDirs := typeFilter == "dir" || typeFilter == "any"

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return nil
		}

		// 跳过重目录
		if d.IsDir() {
			if path != root && grepSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
		}

		// 按 type 过滤是否考虑
		if d.IsDir() && !wantDirs {
			return nil
		}
		if !d.IsDir() && !wantFiles {
			return nil
		}

		// 算 path 相对 root，用 / 分隔符（regex 假设 /）
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)

		if !re.MatchString(relSlash) {
			return nil
		}

		out = append(out, path)
		if len(out) >= maxResults {
			truncated = true
			out = out[:maxResults]
			return filepath.SkipAll
		}
		return nil
	})

	if err != nil && !truncated {
		return nil, false, err
	}
	return out, truncated, nil
}

// globToRegex 把 glob 模式编译成正则。
//
// 语法：
//   - → 匹配单段内的任意字符（不含 /）
//     **       → 匹配任意段（含 /）
//     ?        → 匹配单字符（不含 /）
//     [abc]    → 字符类
//     [!abc]   → 否定字符类
//     其他     → 字面字符（. 等需转义）
//
// 例：
//
//	*.go         → ^[^/]*\.go$
//	**/*.go      → ^(.*/)?[^/]*\.go$
//	foo/*/bar.go → ^foo/[^/]*/bar\.go$
func globToRegex(glob string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	i := 0
	for i < len(glob) {
		c := glob[i]
		switch c {
		case '*':
			// 检查下一个字符是不是 *
			if i+1 < len(glob) && glob[i+1] == '*' {
				// **：匹配任意（含 /）
				// 处理 /**/：允许匹配零或多段
				if i+2 < len(glob) && glob[i+2] == '/' {
					sb.WriteString("(?:.*/)?")
					i += 3
				} else {
					sb.WriteString(".*")
					i += 2
				}
			} else {
				// *：匹配单段（不含 /）
				sb.WriteString("[^/]*")
				i++
			}
		case '?':
			sb.WriteString("[^/]")
			i++
		case '[':
			// 字符类 [abc] 或 [!abc]
			end := strings.IndexByte(glob[i:], ']')
			if end == -1 {
				return nil, fmt.Errorf("unterminated character class at %d", i)
			}
			class := glob[i+1 : i+end]
			if strings.HasPrefix(class, "!") {
				sb.WriteString("[^" + regexp.QuoteMeta(class[1:]) + "]")
			} else {
				sb.WriteString("[" + regexp.QuoteMeta(class) + "]")
			}
			i += end + 1
		case '.', '+', '(', ')', '|', '^', '$', '\\', '{', '}':
			// 需转义的字符
			sb.WriteByte('\\')
			sb.WriteByte(c)
			i++
		default:
			sb.WriteByte(c)
			i++
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}
