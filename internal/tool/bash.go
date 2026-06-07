// Package tool - bash.go
// 参考: docs/acorncode-architect.md §9.5.4
package tool

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"acorncode/internal/permission"
)

//go:embed schemas/bash.json
var bashSchema []byte

// BashArgs 是 bash 工具的入参
type BashArgs struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // 秒，0 = 用 default
}

// Bash 是 bash 工具的实现
type Bash struct {
	DefaultTimeoutSec int    // 默认 30s
	MaxOutputBytes    int    // 默认 50000
	Cwd               string // 默认 cwd
}

// Definition 返回 bash 工具的元数据
func (b *Bash) Definition() Definition {
	return Definition{
		ID: "bash",
		Description: "Execute a shell command via 'sh -c'. " +
			"Returns combined stdout/stderr/exit code/timing. " +
			"Non-zero exit does NOT fail the tool call — output is fed back to the model so it can fix issues. " +
			"Default timeout 30s. Use for builds, tests, git, etc. " +
			"DO NOT use for interactive commands.",
		Keywords:   []string{"bash", "shell", "sh", "exec", "command", "run", "build", "test", "执行"},
		JSONSchema: bashSchema,
	}
}

// Execute 是 bash 工具的入口
func (b *Bash) Execute(ctx context.Context, args json.RawMessage, tc Context) (Result, error) {
	var a BashArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{
			Status: "error",
			Title:  "bash",
			Output: "参数解析失败: " + err.Error(),
		}, nil
	}

	if a.Command == "" {
		return Result{
			Status: "error",
			Title:  "bash",
			Output: "command 不能为空",
		}, nil
	}

	// 1. 权限询问
	if tc.Ask != nil {
		if err := tc.Ask(ctx, permission.Request{
			ID:         "bash-" + truncateForID(a.Command),
			SessionID:  tc.SessionID,
			Permission: "bash",
			Patterns:   []string{a.Command},
			Tool: &permission.ToolRef{
				MessageID: tc.MessageID,
				CallID:    tc.CallID,
			},
		}); err != nil {
			return Result{
				Status: "error",
				Title:  "bash",
				Output: "permission denied: " + err.Error(),
			}, nil
		}
	}

	// 2. 超时配置
	timeout := b.DefaultTimeoutSec
	if timeout <= 0 {
		timeout = 30
	}
	if a.Timeout > 0 {
		timeout = a.Timeout
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// 3. 执行命令
	cmd := exec.CommandContext(cmdCtx, "sh", "-c", a.Command)
	cwd := b.Cwd
	if cwd == "" {
		cwd = tc.Cwd
	}
	if cwd != "" {
		cmd.Dir = cwd
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	timedOut := cmdCtx.Err() == context.DeadlineExceeded

	// 4. 格式化输出
	maxBytes := b.MaxOutputBytes
	if maxBytes <= 0 {
		maxBytes = 50000
	}
	output := formatBashOutput(stdout.String(), stderr.String(), exitCode, timedOut, timeout, elapsed)

	// 5. 截断（如超过 maxBytes 保留头尾各一半）
	truncated := false
	if len(output) > maxBytes {
		output = truncateMiddle(output, maxBytes)
		truncated = true
	}

	// 6. 标题
	title := fmt.Sprintf("$ %s", a.Command)
	if len(title) > 80 {
		title = title[:77] + "..."
	}

	// 7. metadata
	if tc.Metadata != nil {
		tc.Metadata(title, map[string]any{
			"command":   a.Command,
			"exit_code": exitCode,
			"timed_out": timedOut,
			"duration":  elapsed.Seconds(),
			"stdout":    stdout.Len(),
			"stderr":    stderr.Len(),
		})
	}

	// 8. 错误处理
	if err != nil {
		// ctx 取消
		if ctx.Err() != nil {
			return Result{
				Status: "error",
				Title:  title,
				Output: "ctx 已取消: " + ctx.Err().Error() + "\n\n" + output,
			}, nil
		}
		// 错误信息写到 log（output 还是 success 状态让模型看到）
		slog.DebugContext(ctx, "bash 退出非 0", "command", a.Command, "exit_code", exitCode, "err", err)
	}

	// v0.1 设计：无论 exit code 都返 success（让模型看到 stderr 自然修复，见 §9.5.4）
	return Result{
		Status:      "success",
		Title:       title,
		Output:      output,
		IsTruncated: truncated,
	}, nil
}

// formatBashOutput 把 stdout/stderr/exit/timing 拼成统一格式（v0.1 §9.5.4 协议）
func formatBashOutput(stdout, stderr string, exitCode int, timedOut bool, timeoutSec int, elapsed time.Duration) string {
	var sb strings.Builder
	sb.WriteString("=== STDOUT ===\n")
	sb.WriteString(stdout)
	if !strings.HasSuffix(stdout, "\n") && stdout != "" {
		sb.WriteString("\n")
	}
	sb.WriteString("=== STDERR ===\n")
	sb.WriteString(stderr)
	if !strings.HasSuffix(stderr, "\n") && stderr != "" {
		sb.WriteString("\n")
	}
	if timedOut {
		sb.WriteString(fmt.Sprintf("=== TIMEOUT ===\n%d seconds\n", timeoutSec))
	} else {
		sb.WriteString(fmt.Sprintf("=== EXIT ===\n%d\n", exitCode))
	}
	sb.WriteString(fmt.Sprintf("=== TIMING ===\n%.1fs\n", elapsed.Seconds()))
	return sb.String()
}

// truncateMiddle 保留头尾各一半，中间标记截断
func truncateMiddle(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	half := maxBytes / 2
	truncatedBytes := len(s) - maxBytes
	marker := fmt.Sprintf("\n\n... [truncated %d bytes] ...\n\n", truncatedBytes)
	return s[:half] + marker + s[len(s)-half:]
}

// truncateForID 把 command 截短作为 permission request id 后缀
func truncateForID(s string) string {
	if len(s) > 50 {
		return s[:50]
	}
	return s
}
