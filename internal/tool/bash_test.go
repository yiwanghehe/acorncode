// Package tool - bash_test.go
package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// skipOnWindows 标记：Windows 下子进程对取消信号的响应不可靠
// （sleep 类命令收不到等价 SIGKILL），导致 timeout/cancel 类测试不稳定，故跳过。
// 注意：普通命令执行已支持 Windows（cmd /c 回退），这里仅针对超时/取消用例。
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 下子进程取消信号不可靠，跳过 timeout/cancel 用例")
	}
}

// winShell 返回 true 表示当前 Windows 环境回退到了 cmd /c（PATH 上无 sh）。
// 此时测试需用 cmd 语义的等价命令。
func winShell() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	shell, _ := resolveShell()
	return shell == "cmd"
}

func TestBash_SimpleCommand(t *testing.T) {
	dir := t.TempDir()
	b := &Bash{DefaultTimeoutSec: 5, Cwd: dir}
	raw, _ := json.Marshal(BashArgs{Command: "echo hello"})

	res, err := b.Execute(context.Background(), raw, Context{Cwd: dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	if !strings.Contains(res.Output, "hello") {
		t.Errorf("Output 应含 'hello': %s", res.Output)
	}
	if !strings.Contains(res.Output, "=== EXIT ===") {
		t.Errorf("Output 应含 '=== EXIT ===' 标记")
	}
	if !strings.Contains(res.Output, "=== STDOUT ===") {
		t.Errorf("Output 应含 '=== STDOUT ===' 标记")
	}
}

func TestBash_WithStderr(t *testing.T) {
	dir := t.TempDir()
	b := &Bash{DefaultTimeoutSec: 5, Cwd: dir}
	command := "echo to_out; echo to_err >&2"
	if winShell() {
		// cmd 语义：分号不分隔命令，用 & 链接；重定向语法相同
		command = "echo to_out & echo to_err 1>&2"
	}
	raw, _ := json.Marshal(BashArgs{Command: command})

	res, _ := b.Execute(context.Background(), raw, Context{Cwd: dir})
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	if !strings.Contains(res.Output, "to_out") {
		t.Errorf("stdout 应含 'to_out': %s", res.Output)
	}
	if !strings.Contains(res.Output, "to_err") {
		t.Errorf("stderr 应含 'to_err': %s", res.Output)
	}
}

func TestBash_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	b := &Bash{DefaultTimeoutSec: 5, Cwd: dir}
	command := "false" // exit 1
	if winShell() {
		command = "exit /b 1"
	}
	raw, _ := json.Marshal(BashArgs{Command: command})

	res, _ := b.Execute(context.Background(), raw, Context{Cwd: dir})
	// v0.1 设计：非零退出仍返 success，让模型看 stderr 自然修复
	if res.Status != "success" {
		t.Errorf("Status = %q, 期望 success（v0.1 设计）", res.Status)
	}
	if !strings.Contains(res.Output, "=== EXIT ===\n1") {
		t.Errorf("Output 应含 '=== EXIT === 1': %s", res.Output)
	}
}

func TestBash_CommandNotFound(t *testing.T) {
	dir := t.TempDir()
	b := &Bash{DefaultTimeoutSec: 5, Cwd: dir}
	raw, _ := json.Marshal(BashArgs{Command: "nonexistent_command_xyz"})

	res, _ := b.Execute(context.Background(), raw, Context{Cwd: dir})
	// v0.1: 非零退出返 success，stderr 应含 not found / not recognized
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	// sh: "not found" / "command not found"；cmd: "is not recognized"
	if !strings.Contains(res.Output, "not found") &&
		!strings.Contains(res.Output, "command not found") &&
		!strings.Contains(res.Output, "not recognized") {
		t.Errorf("Output 应含命令未找到提示: %s", res.Output)
	}
}

func TestBash_Timeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skip timeout test in short mode")
	}
	skipOnWindows(t)
	dir := t.TempDir()
	b := &Bash{DefaultTimeoutSec: 1, Cwd: dir}
	raw, _ := json.Marshal(BashArgs{Command: "sleep 10"})

	start := time.Now()
	res, _ := b.Execute(context.Background(), raw, Context{Cwd: dir})
	elapsed := time.Since(start)

	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	// 应在 ~1s 退出，不应等满 10s
	if elapsed > 3*time.Second {
		t.Errorf("elapsed = %v, 期望 < 3s（1s timeout + 余量）", elapsed)
	}
	if !strings.Contains(res.Output, "=== TIMEOUT ===") {
		t.Errorf("Output 应含 TIMEOUT 标记: %s", res.Output)
	}
}

func TestBash_ContextCancel(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	b := &Bash{DefaultTimeoutSec: 30, Cwd: dir} // 长 timeout 防止误触
	raw, _ := json.Marshal(BashArgs{Command: "sleep 30"})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	res, _ := b.Execute(ctx, raw, Context{Cwd: dir})
	if res.Status != "error" {
		t.Errorf("Status = %q, 期望 error (ctx cancel)", res.Status)
	}
	if !strings.Contains(res.Output, "ctx 已取消") {
		t.Errorf("Output 应含 'ctx 已取消': %s", res.Output)
	}
}

func TestBash_LongOutputTruncated(t *testing.T) {
	dir := t.TempDir()
	b := &Bash{
		DefaultTimeoutSec: 5,
		MaxOutputBytes:    1000, // 1KB 上限
		Cwd:               dir,
	}
	// 输出 100KB
	command := "yes a | head -c 100000"
	if winShell() {
		// cmd 无 yes/head；用 PowerShell 重复字符串生成 100000 个字符
		// 注意：不加内层引号，避免 cmd /c 的引号解析吞掉表达式
		command = `powershell -NoProfile -Command [string]::new('a',100000)`
	}
	raw, _ := json.Marshal(BashArgs{Command: command})

	res, _ := b.Execute(context.Background(), raw, Context{Cwd: dir})
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	if !res.IsTruncated {
		t.Error("应被标记 truncated")
	}
	if !strings.Contains(res.Output, "truncated") {
		t.Errorf("Output 应含 'truncated' 标记: %s", res.Output)
	}
	if len(res.Output) > 2000 {
		t.Errorf("Output 长度 = %d, 应 < 2000（截断后）", len(res.Output))
	}
}

func TestBash_Pipes(t *testing.T) {
	dir := t.TempDir()
	b := &Bash{DefaultTimeoutSec: 5, Cwd: dir}
	command := "echo foo | grep foo && echo matched"
	if winShell() {
		// cmd: findstr 等价 grep，&& 语义相同
		command = "echo foo | findstr foo && echo matched"
	}
	raw, _ := json.Marshal(BashArgs{Command: command})

	res, _ := b.Execute(context.Background(), raw, Context{Cwd: dir})
	if res.Status != "success" {
		t.Errorf("Status = %q, Output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "matched") {
		t.Errorf("Output 应含 'matched': %s", res.Output)
	}
}

func TestBash_ChainedCommands(t *testing.T) {
	// Windows 路径含反斜杠，shell 拼接会转义失败
	if runtime.GOOS == "windows" {
		t.Skip("Windows 下路径含反斜杠，与 sh -c 拼接不兼容")
	}
	dir := t.TempDir()
	b := &Bash{DefaultTimeoutSec: 5, Cwd: dir}
	raw, _ := json.Marshal(BashArgs{Command: "cd " + dir + " && pwd"})

	res, _ := b.Execute(context.Background(), raw, Context{Cwd: dir})
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	if !strings.Contains(res.Output, dir) {
		t.Errorf("Output 应含 cwd 路径 %q: %s", dir, res.Output)
	}
}

func TestBash_OverrideTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skip timeout test in short mode")
	}
	skipOnWindows(t)
	dir := t.TempDir()
	b := &Bash{DefaultTimeoutSec: 30, Cwd: dir}                      // default 30s
	raw, _ := json.Marshal(BashArgs{Command: "sleep 5", Timeout: 1}) // 强制 1s

	start := time.Now()
	_, _ = b.Execute(context.Background(), raw, Context{Cwd: dir})
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("elapsed = %v, 期望 < 3s（1s timeout + 余量）", elapsed)
	}
}

func TestBash_EmptyCommand(t *testing.T) {
	b := &Bash{}
	raw, _ := json.Marshal(BashArgs{Command: ""})
	res, _ := b.Execute(context.Background(), raw, Context{})
	if res.Status != "error" {
		t.Errorf("Status = %q, 期望 error", res.Status)
	}
}

func TestBash_InvalidJSON(t *testing.T) {
	b := &Bash{}
	res, _ := b.Execute(context.Background(), json.RawMessage(`{bad`), Context{})
	if res.Status != "error" {
		t.Errorf("Status = %q", res.Status)
	}
}

func TestBash_PermissionDenied(t *testing.T) {
	dir := t.TempDir()
	b := &Bash{DefaultTimeoutSec: 5, Cwd: dir}
	raw, _ := json.Marshal(BashArgs{Command: "echo hi"})

	res, _ := b.Execute(context.Background(), raw, Context{
		Cwd: dir,
		Ask: denyAllBroker{}.Ask,
	})
	if res.Status != "error" {
		t.Errorf("Status = %q, 期望 error", res.Status)
	}
	if !strings.Contains(res.Output, "permission denied") {
		t.Errorf("Output = %q", res.Output)
	}
}

func TestBash_CwdRespected(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	// 在 subdir 写一个 marker 文件
	if err := os.WriteFile(filepath.Join(subdir, "marker.txt"), []byte("found"), 0644); err != nil {
		t.Fatal(err)
	}

	b := &Bash{DefaultTimeoutSec: 5, Cwd: dir}
	command := "ls marker.txt"
	if winShell() {
		// 在 subdir 列出 marker.txt 验证 cwd（cd 进 subdir 再列）
		command = "cd sub && dir /b marker.txt"
	}
	raw, _ := json.Marshal(BashArgs{Command: command})

	res, _ := b.Execute(context.Background(), raw, Context{Cwd: dir})
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	if !strings.Contains(res.Output, "marker.txt") {
		t.Errorf("Output 应含 marker.txt: %s", res.Output)
	}
}

func TestBash_ContextCwdOverride(t *testing.T) {
	// 验证 tc.Cwd 覆盖 b.Cwd
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "tc.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	b := &Bash{DefaultTimeoutSec: 5, Cwd: dir1} // b.Cwd = dir1
	command := "ls tc.txt"
	if winShell() {
		command = "dir /b tc.txt"
	}
	raw, _ := json.Marshal(BashArgs{Command: command})
	// tc.Cwd = dir2 应覆盖 b.Cwd = dir1
	res, _ := b.Execute(context.Background(), raw, Context{Cwd: dir2})
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	if !strings.Contains(res.Output, "tc.txt") {
		t.Errorf("tc.Cwd 应覆盖 b.Cwd: %s", res.Output)
	}
}

func TestBash_Definition(t *testing.T) {
	b := &Bash{}
	def := b.Definition()
	if def.ID != "bash" {
		t.Errorf("ID = %q", def.ID)
	}
	if def.JSONSchema == nil {
		t.Error("JSONSchema 不能为 nil")
	}
}

func TestBash_ImplementsTool(t *testing.T) {
	var _ Tool = (*Bash)(nil)
}

func TestFormatBashOutput(t *testing.T) {
	// 单元测试 helper
	out := formatBashOutput("hello\n", "warn\n", 0, false, 30, 100*time.Millisecond)
	if !strings.Contains(out, "=== STDOUT ===") {
		t.Error("缺 STDOUT 段")
	}
	if !strings.Contains(out, "hello") {
		t.Error("缺 stdout 内容")
	}
	if !strings.Contains(out, "=== STDERR ===") {
		t.Error("缺 STDERR 段")
	}
	if !strings.Contains(out, "warn") {
		t.Error("缺 stderr 内容")
	}
	if !strings.Contains(out, "=== EXIT ===\n0") {
		t.Error("缺 EXIT 段或 exit code 不对")
	}
	if !strings.Contains(out, "=== TIMING ===") {
		t.Error("缺 TIMING 段")
	}
}

func TestTruncateMiddle(t *testing.T) {
	// 不截断
	if got := truncateMiddle("short", 100); got != "short" {
		t.Errorf("不截断: %q", got)
	}
	// 截断
	big := strings.Repeat("x", 1000)
	got := truncateMiddle(big, 100)
	if !strings.Contains(got, "truncated") {
		t.Errorf("应含 truncated 标记: %q", got)
	}
	if len(got) > 200 {
		t.Errorf("截断后长度 = %d, 应 < 200", len(got))
	}
}
