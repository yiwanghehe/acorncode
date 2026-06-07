// Package tool - grep.go 内容搜索工具
package tool

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed schemas/grep.json
var grepSchema []byte

// GrepArgs 是 grep 工具的入参
type GrepArgs struct {
	Pattern     string `json:"pattern"`
	Path        string `json:"path,omitempty"`
	Include     string `json:"include,omitempty"`
	IgnoreCase  bool   `json:"ignore_case,omitempty"`
	LineNumbers bool   `json:"line_numbers,omitempty"`
	MaxResults  int    `json:"max_results,omitempty"`
}

// Grep 是 grep 工具的实现
type Grep struct {
	Cwd        string
	MaxResults int // 默认 100
}

// Definition 返回 grep 工具元数据
func (g *Grep) Definition() Definition {
	return Definition{
		ID:          "grep",
		Description: "Search file contents with a regular expression. Walks path recursively, optionally filters by file glob, skips binary files and heavy dirs (.git, node_modules, vendor). Output format: `path:line:content` (one match per line). Use to locate definitions, usages, TODOs across the project.",
		Keywords:    []string{"grep", "search", "find", "regex", "match", "搜索", "查找", "正则"},
		JSONSchema:  grepSchema,
	}
}

// 默认要跳过的目录（重 / 不需要搜）
var grepSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".idea":        true,
	".vscode":      true,
	"dist":         true,
	"build":        true,
	"target":       true,
}

// Execute 是 grep 工具入口
func (g *Grep) Execute(ctx context.Context, args json.RawMessage, tc Context) (Result, error) {
	var a GrepArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Status: "error", Title: "grep", Output: "参数解析失败: " + err.Error()}, nil
	}

	if a.Pattern == "" {
		return Result{Status: "error", Title: "grep", Output: "pattern 不能为空"}, nil
	}

	// 编译正则
	flags := ""
	if a.IgnoreCase {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + a.Pattern)
	if err != nil {
		return Result{Status: "error", Title: "grep", Output: "正则编译失败: " + err.Error()}, nil
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
			return Result{Status: "error", Title: "grep", Output: "目录不存在: " + root}, nil
		}
		return Result{Status: "error", Title: "grep", Output: "stat 失败: " + err.Error()}, nil
	}
	if !info.IsDir() {
		return Result{Status: "error", Title: "grep", Output: "path 不是目录: " + root}, nil
	}

	maxResults := a.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}

	// 走文件
	matches, truncated, err := g.walkAndSearch(ctx, root, re, a.Include, a.LineNumbers, maxResults)
	if err != nil {
		if ctx.Err() != nil {
			return Result{Status: "error", Title: "grep", Output: "ctx 已取消: " + ctx.Err().Error()}, nil
		}
		return Result{Status: "error", Title: "grep", Output: err.Error()}, nil
	}

	// metadata
	if tc.Metadata != nil {
		tc.Metadata(fmt.Sprintf("grep %s (%d 匹配)", a.Pattern, len(matches)), map[string]any{
			"pattern":   a.Pattern,
			"root":      root,
			"count":     len(matches),
			"truncated": truncated,
		})
	}

	if len(matches) == 0 {
		return Result{
			Status: "success",
			Title:  "grep (no matches)",
			Output: "无匹配",
		}, nil
	}

	output := strings.Join(matches, "\n")
	if truncated {
		output += fmt.Sprintf("\n\n[... 已截断，超出 %d 上限]", maxResults)
	}

	return Result{
		Status:      "success",
		Title:       fmt.Sprintf("grep (%d 匹配)", len(matches)),
		Output:      output,
		IsTruncated: truncated,
	}, nil
}

// walkAndSearch 走目录并搜匹配。返回 ["path:line:content", ...] 和是否截断。
func (g *Grep) walkAndSearch(ctx context.Context, root string, re *regexp.Regexp, include string, lineNumbers bool, maxResults int) ([]string, bool, error) {
	var out []string
	truncated := false

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		// ctx 优先检查
		if err := ctx.Err(); err != nil {
			return err
		}

		if walkErr != nil {
			// 单个文件/目录错误不杀整 walk
			return nil
		}

		if d.IsDir() {
			if grepSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		// include 过滤
		if include != "" {
			matched, err := filepath.Match(include, filepath.Base(path))
			if err != nil {
				// pattern 语法错：忽略单个文件
				return nil
			}
			if !matched {
				return nil
			}
		}

		// 跳过明显二进制文件（按扩展名）
		if isBinaryExt(filepath.Ext(path)) {
			return nil
		}

		// 打开 + 按行扫
		matches, err := grepFile(ctx, path, re, lineNumbers)
		if err != nil {
			// 单文件错不杀 walk
			return nil
		}
		out = append(out, matches...)

		// 超限标记 + 截断
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

// grepFile 单文件搜索
func grepFile(ctx context.Context, path string, re *regexp.Regexp, lineNumbers bool) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// 用 1MB buffer 防止恶意单行 OOM
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var out []string
	lineNum := 0

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		lineNum++
		line := scanner.Text()

		// 内容前 512 字节含 NUL 视为二进制，跳过（防误报）
		if lineNum == 1 && containsNUL(line) {
			return nil, nil
		}

		if re.MatchString(line) {
			if lineNumbers {
				// 用 tab 分隔 path 和 line:content（Windows 路径含 : 冲突）
				out = append(out, fmt.Sprintf("%s\t%d:%s", path, lineNum, line))
			} else {
				out = append(out, path+"\t"+line)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// isBinaryExt 常见二进制扩展名
func isBinaryExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".exe", ".dll", ".so", ".dylib", ".bin", ".png", ".jpg", ".jpeg", ".gif", ".pdf", ".zip", ".tar", ".gz", ".bz2", ".7z", ".rar", ".ico", ".woff", ".woff2", ".ttf", ".otf", ".mp3", ".mp4", ".mov", ".avi", ".webm", ".ogg":
		return true
	}
	return false
}

// containsNUL 检查字符串前 512 字节是否含 NUL（二进制标志）
func containsNUL(s string) bool {
	checkLen := len(s)
	if checkLen > 512 {
		checkLen = 512
	}
	return strings.ContainsRune(s[:checkLen], 0)
}
