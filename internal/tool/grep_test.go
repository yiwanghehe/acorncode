package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// makeTestTree 在 t.TempDir() 下建测试文件树，返回根路径
func makeTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// 主 Go 文件
	mustWrite(t, filepath.Join(root, "main.go"), `package main

import "fmt"

func Hello() {
	fmt.Println("hello world")
}

func main() {
	Hello()
}
`)

	// 另一个 Go 文件
	mustWrite(t, filepath.Join(root, "util.go"), `package main

// TODO: refactor this
func Helper() string {
	return "HELPER"
}
`)

	// 文本文件
	mustWrite(t, filepath.Join(root, "README.md"), `# Project

This is a Hello project.
TODO list:
- write docs
`)

	// 子目录
	subdir := filepath.Join(root, "sub")
	mustMkdir(t, subdir)
	mustWrite(t, filepath.Join(subdir, "deep.txt"), "deep nested file with Hello\n")

	// .git 目录（应该被跳过）
	gitdir := filepath.Join(root, ".git")
	mustMkdir(t, gitdir)
	mustWrite(t, filepath.Join(gitdir, "config"), "git config with Hello in it\n")

	// node_modules（应该被跳过）
	nm := filepath.Join(root, "node_modules")
	mustMkdir(t, nm)
	mustWrite(t, filepath.Join(nm, "lib.js"), "Hello from node_modules\n")

	// 二进制文件（按扩展名跳过）
	mustWrite(t, filepath.Join(root, "image.png"), "fake binary Hello content\x00\x00")

	// 空文件
	mustWrite(t, filepath.Join(root, "empty.txt"), "")

	return root
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// newGrepArgs 构造 GrepArgs 序列化为 args
func newGrepArgs(t *testing.T, args GrepArgs) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func runGrep(t *testing.T, g *Grep, args json.RawMessage) Result {
	t.Helper()
	res, err := g.Execute(context.Background(), args, Context{Cwd: g.Cwd})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	return res
}

// ========== 正常路径 ==========

func TestGrep_BasicMatch(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "Hello",
		Path:    root,
	}))

	if res.Status != "success" {
		t.Fatalf("Status = %q, 期望 success. Output: %s", res.Status, res.Output)
	}
	// 期望找到 4 个匹配（main.go 有 2 处、README.md、sub/deep.txt）
	if !strings.Contains(res.Output, "main.go") {
		t.Errorf("Output 应含 main.go: %s", res.Output)
	}
	if !strings.Contains(res.Output, filepath.Join("sub", "deep.txt")) {
		t.Errorf("Output 应含 sub/deep.txt: %s", res.Output)
	}
	if strings.Contains(res.Output, ".git") {
		t.Errorf("Output 不应含 .git: %s", res.Output)
	}
	if strings.Contains(res.Output, "node_modules") {
		t.Errorf("Output 不应含 node_modules: %s", res.Output)
	}
}

func TestGrep_LineNumbers(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern:     "Hello",
		Path:        root,
		LineNumbers: true,
	}))

	// 输出形如 "path\tline:content"
	lines := strings.Split(strings.TrimSpace(res.Output), "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "[...") {
			continue
		}
		parts := strings.SplitN(l, "\t", 2)
		if len(parts) != 2 {
			t.Errorf("格式错误（期望 path\\tline:content）: %s", l)
			continue
		}
		// 第二段应是 "line:content"
		lc := strings.SplitN(parts[1], ":", 2)
		if len(lc) != 2 {
			t.Errorf("第二段格式错误（期望 line:content）: %s", l)
			continue
		}
		if !isAllDigits(lc[0]) {
			t.Errorf("行号应为数字: %s", l)
		}
	}
}

func TestGrep_IgnoreCase(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	// 不忽略大小写
	res1 := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "helper",
		Path:    root,
	}))
	if strings.Contains(res1.Output, "util.go") {
		t.Errorf("大小写敏感模式不应匹配 HELPER. Output: %s", res1.Output)
	}

	// 忽略大小写
	res2 := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern:    "helper",
		Path:       root,
		IgnoreCase: true,
	}))
	if !strings.Contains(res2.Output, "util.go") {
		t.Errorf("忽略大小写应匹配 HELPER. Output: %s", res2.Output)
	}
}

func TestGrep_IncludeFilter(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	// 只在 .go 文件里搜
	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "Hello",
		Path:    root,
		Include: "*.go",
	}))

	if !strings.Contains(res.Output, "main.go") {
		t.Errorf("应含 main.go: %s", res.Output)
	}
	if strings.Contains(res.Output, "README.md") {
		t.Errorf("不应含 README.md（被 *.go 过滤）: %s", res.Output)
	}
	if strings.Contains(res.Output, "deep.txt") {
		t.Errorf("不应含 deep.txt: %s", res.Output)
	}
}

func TestGrep_NoMatch(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "NoSuchPatternXyzzy",
		Path:    root,
	}))

	if res.Status != "success" {
		t.Errorf("Status = %q, 无匹配也算 success", res.Status)
	}
	if !strings.Contains(res.Output, "无匹配") {
		t.Errorf("Output 应含 '无匹配': %s", res.Output)
	}
}

func TestGrep_MaxResults(t *testing.T) {
	root := t.TempDir()
	// 写 10 个文件，每个含 "MATCH"
	for i := 0; i < 10; i++ {
		mustWrite(t, filepath.Join(root, "f"+string(rune('a'+i))+".txt"), "MATCH line 1\n")
	}
	g := &Grep{Cwd: root}

	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern:    "MATCH",
		Path:       root,
		MaxResults: 3,
	}))

	if !res.IsTruncated {
		t.Errorf("IsTruncated 应为 true（超 MaxResults=3）")
	}
	// 数实际匹配行数
	count := 0
	for _, l := range strings.Split(res.Output, "\n") {
		if strings.HasPrefix(l, "[...") {
			continue
		}
		if strings.Contains(l, "MATCH") {
			count++
		}
	}
	if count != 3 {
		t.Errorf("匹配数 = %d, 期望 3", count)
	}
}

func TestGrep_RegexSpecial(t *testing.T) {
	root := makeTestTree(t)
	mustWrite(t, filepath.Join(root, "special.txt"), "foo bar\nfoo\nfoobar\n")
	g := &Grep{Cwd: root}

	// \b 单词边界：应匹配独立 "foo" 和 "foo bar"，不匹配 "foobar"
	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern:     `\bfoo\b`,
		Path:        root,
		LineNumbers: true,
	}))

	if !strings.Contains(res.Output, "foo bar") {
		t.Errorf("应匹配 'foo bar'. Output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "2:foo") {
		t.Errorf("应匹配独立 'foo' 行（行号 2）. Output: %s", res.Output)
	}
	// "foobar" 不应被算（"foobar" 整词匹配应不在结果里，但 "foo bar" 在）
	if strings.Contains(res.Output, "3:foobar") {
		t.Errorf("'foobar' 不应匹配 \\bfoo\\b. Output: %s", res.Output)
	}
}

// ========== 异常路径 ==========

func TestGrep_EmptyPattern(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "",
		Path:    root,
	}))

	if res.Status != "error" {
		t.Errorf("空 pattern 应返 error. Status: %s", res.Status)
	}
}

func TestGrep_BadRegex(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "[unclosed",
		Path:    root,
	}))

	if res.Status != "error" {
		t.Errorf("坏 regex 应返 error. Status: %s, Output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "正则") {
		t.Errorf("Output 应解释正则错: %s", res.Output)
	}
}

func TestGrep_NotExistPath(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "Hello",
		Path:    filepath.Join(root, "no_such_dir"),
	}))

	if res.Status != "error" {
		t.Errorf("不存在目录应返 error. Status: %s", res.Status)
	}
}

func TestGrep_PathIsFile(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "Hello",
		Path:    filepath.Join(root, "main.go"),
	}))

	if res.Status != "error" {
		t.Errorf("path 是文件应返 error. Status: %s, Output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "不是目录") {
		t.Errorf("Output 应说明不是目录: %s", res.Output)
	}
}

func TestGrep_InvalidArgs(t *testing.T) {
	g := &Grep{Cwd: "."}

	res, err := g.Execute(context.Background(), []byte("not json"), Context{})
	if err != nil {
		t.Fatalf("不应返 Go err: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("坏 JSON 应返 error. Status: %s", res.Status)
	}
}

func TestGrep_EmptyFile(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	// 空文件不 panic
	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "anything",
		Path:    root,
		Include: "empty.txt",
	}))

	if res.Status != "success" {
		t.Errorf("空文件应 success（无匹配）. Status: %s, Output: %s", res.Status, res.Output)
	}
}

func TestGrep_BinaryFileSkipped(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "Hello",
		Path:    root,
		Include: "image.png",
	}))

	// 二进制应跳过（按扩展名）
	if strings.Contains(res.Output, "image.png") {
		t.Errorf("二进制应跳过: %s", res.Output)
	}
}

func TestGrep_SkipsNodeModules(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "Hello",
		Path:    root,
	}))

	if strings.Contains(res.Output, "node_modules") {
		t.Errorf("应跳过 node_modules: %s", res.Output)
	}
}

func TestGrep_SkipsGitDir(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "Hello",
		Path:    root,
	}))

	if strings.Contains(res.Output, ".git") {
		t.Errorf("应跳过 .git: %s", res.Output)
	}
}

func TestGrep_RelativePath(t *testing.T) {
	root := makeTestTree(t)
	g := &Grep{Cwd: root}

	// 相对路径（应被 tc.Cwd 解析）
	res := runGrep(t, g, newGrepArgs(t, GrepArgs{
		Pattern: "Hello",
		Path:    ".",
	}))

	if res.Status != "success" {
		t.Errorf("相对路径应 success. Status: %s, Output: %s", res.Status, res.Output)
	}
}

// ========== ctx 取消 ==========

func TestGrep_CtxCancel(t *testing.T) {
	// Windows 上 ctx 取消测试不靠谱（filepath.WalkDir 在 Windows 异步行为差异）
	if runtime.GOOS == "windows" {
		t.Skip("Windows: ctx 取消行为差异")
	}

	root := t.TempDir()
	// 造很多文件增加 walk 耗时
	for i := 0; i < 1000; i++ {
		mustWrite(t, filepath.Join(root, "f"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+".txt"), "match content\n")
	}
	g := &Grep{Cwd: root}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	res, err := g.Execute(ctx, newGrepArgs(t, GrepArgs{
		Pattern: "match",
		Path:    root,
	}), Context{Cwd: root})

	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	// ctx 已取消时，walkAndSearch 会返 ctx.Err → Execute 返 error
	if res.Status != "error" {
		t.Errorf("ctx 取消应返 error. Status: %s, Output: %s", res.Status, res.Output)
	}
}

// ========== 辅助 ==========

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
