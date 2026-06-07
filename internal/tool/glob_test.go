package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// makeGlobTree 建 Glob 测试用目录树
func makeGlobTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// src/*.go
	mustWrite(t, filepath.Join(root, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(root, "util.go"), "package main\n")
	mustWrite(t, filepath.Join(root, "README.md"), "# Project\n")

	// sub/*.go
	sub := filepath.Join(root, "sub")
	mustMkdir(t, sub)
	mustWrite(t, filepath.Join(sub, "deep.go"), "package sub\n")
	mustWrite(t, filepath.Join(sub, "test.txt"), "test\n")

	// sub/nested/very.go
	nested := filepath.Join(sub, "nested")
	mustMkdir(t, nested)
	mustWrite(t, filepath.Join(nested, "very.go"), "package nested\n")

	// empty dir
	empty := filepath.Join(root, "empty_dir")
	mustMkdir(t, empty)

	// .git
	gitdir := filepath.Join(root, ".git")
	mustMkdir(t, gitdir)
	mustWrite(t, filepath.Join(gitdir, "config"), "git config\n")

	// node_modules
	nm := filepath.Join(root, "node_modules")
	mustMkdir(t, nm)
	mustWrite(t, filepath.Join(nm, "lib.js"), "lib\n")

	return root
}

func newGlobArgs(t *testing.T, args GlobArgs) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func runGlob(t *testing.T, g *Glob, args json.RawMessage) Result {
	t.Helper()
	res, err := g.Execute(context.Background(), args, Context{Cwd: g.Cwd})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	return res
}

// ========== globToRegex 单测（unit） ==========

func TestGlobToRegex(t *testing.T) {
	tests := []struct {
		glob     string
		input    string
		expected bool
	}{
		// *
		{"*.go", "main.go", true},
		{"*.go", "src/main.go", false}, // 单段不含 /
		{"*.go", "main.txt", false},

		// **
		{"**/*.go", "main.go", true},
		{"**/*.go", "src/main.go", true},
		{"**/*.go", "src/sub/main.go", true},
		{"**/*.go", "main.txt", false},

		// ?（单字符不含 /）
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
		{"?.go", "a/b.go", false},

		// 字符类
		{"[abc].go", "a.go", true},
		{"[abc].go", "b.go", true},
		{"[abc].go", "d.go", false},

		// 转义
		{"file.txt", "file.txt", true},
		{"file.txt", "fileXtxt", false},

		// 路径
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/sub/main.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.glob+"/"+tt.input, func(t *testing.T) {
			re, err := globToRegex(tt.glob)
			if err != nil {
				t.Fatalf("compile %q: %v", tt.glob, err)
			}
			got := re.MatchString(tt.input)
			if got != tt.expected {
				t.Errorf("%s match %q = %v, 期望 %v", tt.glob, tt.input, got, tt.expected)
			}
		})
	}
}

func TestGlobToRegex_InvalidClass(t *testing.T) {
	_, err := globToRegex("[abc")
	if err == nil {
		t.Errorf("未闭合字符类应返错")
	}
}

// ========== 正常路径 ==========

func TestGlob_StarGo(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "*.go",
		Path:    root,
	}))

	if res.Status != "success" {
		t.Fatalf("Status = %q, Output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "main.go") || !strings.Contains(res.Output, "util.go") {
		t.Errorf("应含 main.go + util.go: %s", res.Output)
	}
	if strings.Contains(res.Output, "sub") {
		t.Errorf("单段 * 不应匹配 sub/... : %s", res.Output)
	}
}

func TestGlob_DoubleStarRecursive(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "**/*.go",
		Path:    root,
	}))

	if res.Status != "success" {
		t.Fatalf("Status = %q, Output: %s", res.Status, res.Output)
	}
	// 应含 main.go / util.go / sub/deep.go / sub/nested/very.go
	mustContain := []string{"main.go", "util.go", "deep.go", "very.go"}
	for _, name := range mustContain {
		if !strings.Contains(res.Output, name) {
			t.Errorf("应含 %s: %s", name, res.Output)
		}
	}
}

func TestGlob_DoubleStarZeroLevel(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	// **/*.go 应同时匹配根目录的 *.go（零段也算）
	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "**/*.go",
		Path:    root,
	}))

	if !strings.Contains(res.Output, "main.go") {
		t.Errorf("'**/*.go' 应匹配根目录 main.go（零段路径）: %s", res.Output)
	}
}

func TestGlob_QuestionMark(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "?.go",
		Path:    root,
	}))

	// 应匹配单字符 .go
	if !strings.Contains(res.Output, "main.go") || !strings.Contains(res.Output, "util.go") {
		// 等等，main.go / util.go 是 4 字符 basename，"?.go" 只匹配 1 字符
	}
	// 实际上 "?.go" 匹配 "a.go" 这种 — 我们没造这种文件，应返无匹配
	if !strings.Contains(res.Output, "无匹配") {
		t.Errorf("单字符 ? 不应匹配多字符 basename: %s", res.Output)
	}
}

func TestGlob_TypeFile(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "**/*",
		Path:    root,
		Type:    "file",
	}))

	if res.Status != "success" {
		t.Fatalf("Status: %s, Output: %s", res.Status, res.Output)
	}
	// 不应含目录路径（虽然 empty_dir 也是 "match"，但 type=file 过滤掉）
	if strings.Contains(res.Output, "empty_dir") {
		t.Errorf("type=file 不应含目录: %s", res.Output)
	}
}

func TestGlob_TypeDir(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "**/*",
		Path:    root,
		Type:    "dir",
	}))

	if res.Status != "success" {
		t.Fatalf("Status: %s, Output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "sub") {
		t.Errorf("type=dir 应含子目录 sub: %s", res.Output)
	}
	if strings.Contains(res.Output, "main.go") {
		t.Errorf("type=dir 不应含文件: %s", res.Output)
	}
}

func TestGlob_NoMatch(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "*.nonexistent",
		Path:    root,
	}))

	if res.Status != "success" {
		t.Errorf("无匹配也算 success: %s", res.Status)
	}
	if !strings.Contains(res.Output, "无匹配") {
		t.Errorf("Output 应含 '无匹配': %s", res.Output)
	}
}

func TestGlob_MaxResults(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		mustWrite(t, filepath.Join(root, "f"+string(rune('a'+i))+".txt"), "x\n")
	}
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern:    "*.txt",
		Path:       root,
		MaxResults: 3,
	}))

	if !res.IsTruncated {
		t.Errorf("IsTruncated 应为 true")
	}
	count := 0
	for _, l := range strings.Split(res.Output, "\n") {
		if strings.HasPrefix(l, "[...") {
			continue
		}
		if strings.HasSuffix(l, ".txt") {
			count++
		}
	}
	if count != 3 {
		t.Errorf("匹配数 = %d, 期望 3", count)
	}
}

// ========== 异常路径 ==========

func TestGlob_EmptyPattern(t *testing.T) {
	g := &Glob{Cwd: "."}
	res := runGlob(t, g, newGlobArgs(t, GlobArgs{Pattern: ""}))

	if res.Status != "error" {
		t.Errorf("空 pattern 应 error. Status: %s", res.Status)
	}
}

func TestGlob_NotExistPath(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "*.go",
		Path:    filepath.Join(root, "no_such"),
	}))

	if res.Status != "error" {
		t.Errorf("不存在路径应 error: %s", res.Status)
	}
}

func TestGlob_PathIsFile(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "*.go",
		Path:    filepath.Join(root, "main.go"),
	}))

	if res.Status != "error" {
		t.Errorf("path 是文件应 error: %s", res.Status)
	}
}

func TestGlob_InvalidArgs(t *testing.T) {
	g := &Glob{Cwd: "."}
	res, err := g.Execute(context.Background(), []byte("not json"), Context{})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("坏 JSON 应 error: %s", res.Status)
	}
}

func TestGlob_InvalidPattern(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "[unclosed",
		Path:    root,
	}))

	if res.Status != "error" {
		t.Errorf("坏 pattern 应 error: %s", res.Status)
	}
}

func TestGlob_SkipsNodeModules(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "**/*.js",
		Path:    root,
	}))

	if strings.Contains(res.Output, "node_modules") {
		t.Errorf("应跳过 node_modules: %s", res.Output)
	}
}

func TestGlob_SkipsGitDir(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "**/*",
		Path:    root,
	}))

	if strings.Contains(res.Output, ".git") {
		t.Errorf("应跳过 .git: %s", res.Output)
	}
}

func TestGlob_RelativePath(t *testing.T) {
	root := makeGlobTree(t)
	g := &Glob{Cwd: root}

	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "*.go",
		Path:    ".",
	}))

	if res.Status != "success" {
		t.Errorf("相对路径应 success: %s", res.Status)
	}
}

// ========== ctx 取消 ==========

func TestGlob_CtxCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows: ctx 取消行为差异")
	}

	root := t.TempDir()
	for i := 0; i < 1000; i++ {
		mustWrite(t, filepath.Join(root, "f"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+".txt"), "x\n")
	}
	g := &Glob{Cwd: root}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := g.Execute(ctx, newGlobArgs(t, GlobArgs{
		Pattern: "*.txt",
		Path:    root,
	}), Context{Cwd: root})

	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("ctx 取消应 error: %s, %s", res.Status, res.Output)
	}
}

// ========== 字符类 ==========

func TestGlob_CharClass(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "")
	mustWrite(t, filepath.Join(root, "b.txt"), "")
	mustWrite(t, filepath.Join(root, "c.txt"), "")
	mustWrite(t, filepath.Join(root, "d.txt"), "")

	g := &Glob{Cwd: root}
	res := runGlob(t, g, newGlobArgs(t, GlobArgs{
		Pattern: "[ab].txt",
		Path:    root,
	}))

	if !strings.Contains(res.Output, "a.txt") {
		t.Errorf("应含 a.txt: %s", res.Output)
	}
	if !strings.Contains(res.Output, "b.txt") {
		t.Errorf("应含 b.txt: %s", res.Output)
	}
	if strings.Contains(res.Output, "c.txt") {
		t.Errorf("不应含 c.txt: %s", res.Output)
	}
	if strings.Contains(res.Output, "d.txt") {
		t.Errorf("不应含 d.txt: %s", res.Output)
	}
}
