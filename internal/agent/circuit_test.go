// Package agent - circuit_test.go
package agent

import (
	"errors"
	"testing"

	"acorncode/internal/tool"
)

func okResult() tool.Result  { return tool.Result{Status: "success", Output: "ok"} }
func errResult() tool.Result { return tool.Result{Status: "error", Output: "boom failed"} }

// TestCircuit_默认阈值归一化 验证 0 值被替换为默认
func TestCircuit_默认阈值归一化(t *testing.T) {
	c := newCircuitBreaker(circuitConfig{})
	if c.cfg.MaxToolRetry != 3 || c.cfg.MaxBashFails != 5 || c.cfg.MaxSameError != 3 {
		t.Errorf("默认阈值错误: %+v", c.cfg)
	}
}

// TestCircuit_成功不触发 多次成功调用永不熔断
func TestCircuit_成功不触发(t *testing.T) {
	c := newCircuitBreaker(circuitConfig{})
	for i := 0; i < 10; i++ {
		call := ToolCall{CallID: "c1", ToolID: "read"}
		if err := c.Check(call, okResult()); err != nil {
			// 注意：同 callID 重试规则也会累加，read 第 4 次会触发规则 1
			if i <= 2 {
				t.Errorf("第 %d 次成功不应触发: %v", i, err)
			}
		}
	}
}

// TestCircuit_规则1_同call重试上限 同 callID 超 MaxToolRetry 触发
func TestCircuit_规则1_同call重试上限(t *testing.T) {
	c := newCircuitBreaker(circuitConfig{MaxToolRetry: 3})
	call := ToolCall{CallID: "same", ToolID: "read"}
	var lastErr error
	for i := 0; i < 4; i++ {
		lastErr = c.Check(call, tool.Result{Status: "success", Output: "x"})
	}
	if !errors.Is(lastErr, errTurnAborted) {
		t.Errorf("第 4 次同 call 应触发 errTurnAborted, got %v", lastErr)
	}
}

// TestCircuit_规则2_bash连续失败 bash 失败超 MaxBashFails 触发
func TestCircuit_规则2_bash连续失败(t *testing.T) {
	c := newCircuitBreaker(circuitConfig{MaxBashFails: 5, MaxToolRetry: 100, MaxSameError: 100})
	var lastErr error
	for i := 0; i < 6; i++ {
		// 每次不同 callID + 不同 output，只测 bash 规则
		call := ToolCall{CallID: string(rune('a' + i)), ToolID: "bash"}
		lastErr = c.Check(call, tool.Result{Status: "error", Output: "fail" + string(rune('0'+i))})
	}
	if !errors.Is(lastErr, errTurnAborted) {
		t.Errorf("bash 第 6 次失败应触发, got %v", lastErr)
	}
}

// TestCircuit_规则3_同错误签名 同签名超 MaxSameError 触发
func TestCircuit_规则3_同错误签名(t *testing.T) {
	c := newCircuitBreaker(circuitConfig{MaxSameError: 3, MaxToolRetry: 100, MaxBashFails: 100})
	var lastErr error
	for i := 0; i < 4; i++ {
		call := ToolCall{CallID: string(rune('a' + i)), ToolID: "edit"}
		lastErr = c.Check(call, errResult()) // 相同 output → 相同签名
	}
	if !errors.Is(lastErr, errTurnAborted) {
		t.Errorf("同错误签名第 4 次应触发, got %v", lastErr)
	}
}

// TestErrSignature_截断 验证签名首行截 80 字符
func TestErrSignature_截断(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "x"
	}
	sig := errSignature(ToolCall{ToolID: "read"}, tool.Result{Status: "error", Output: long})
	// 签名 = "read|error|" + 80 个 x
	want := "read|error|"
	if len(sig) != len(want)+80 {
		t.Errorf("签名长度 = %d, want %d", len(sig), len(want)+80)
	}
}

// TestErrSignature_仅取首行 验证多行只取第一行
func TestErrSignature_仅取首行(t *testing.T) {
	sig := errSignature(ToolCall{ToolID: "bash"}, tool.Result{Status: "error", Output: "line1\nline2\nline3"})
	if sig != "bash|error|line1" {
		t.Errorf("签名 = %q, want bash|error|line1", sig)
	}
}
