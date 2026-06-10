// Package agent - circuit.go
//
// circuitBreaker 把「一个 turn 内的失败熔断」逻辑从 Loop 中剥离（R3），
// 让 Loop 专注状态机调度，熔断计数与判定独立、可单测。
//
// 三道熔断（见 docs/architecture.md §4）：
//  1. 同一 tool call 重试超 MaxToolRetry → 中止
//  2. bash 连续失败超 MaxBashFails → 中止
//  3. 同一错误签名重复超 MaxSameError → 中止
package agent

import (
	"fmt"
	"strings"

	"acorncode/internal/tool"
)

// circuitConfig 是三道熔断的阈值（0 表示用默认值）。
type circuitConfig struct {
	MaxToolRetry int // 默认 3
	MaxBashFails int // 默认 5
	MaxSameError int // 默认 3
}

// circuitBreaker 跟踪一个 turn 内的失败并按三道规则判定是否中止。
// 由单 goroutine（Loop.Run）使用，无需加锁。
type circuitBreaker struct {
	cfg          circuitConfig
	bashFails    int
	toolAttempts map[string]int
	sameErrCount map[string]int
}

// newCircuitBreaker 创建熔断器，按 cfg 归一化默认阈值。
func newCircuitBreaker(cfg circuitConfig) *circuitBreaker {
	if cfg.MaxToolRetry == 0 {
		cfg.MaxToolRetry = 3
	}
	if cfg.MaxBashFails == 0 {
		cfg.MaxBashFails = 5
	}
	if cfg.MaxSameError == 0 {
		cfg.MaxSameError = 3
	}
	return &circuitBreaker{
		cfg:          cfg,
		toolAttempts: make(map[string]int),
		sameErrCount: make(map[string]int),
	}
}

// Check 记录一次工具调用结果并判定是否触发熔断。
// 触发时返回带 errTurnAborted 的错误（供 Loop 走重试路径），否则返回 nil。
func (c *circuitBreaker) Check(call ToolCall, result tool.Result) error {
	// 规则 1：同 tool call 重试上限
	c.toolAttempts[call.CallID]++
	if c.toolAttempts[call.CallID] > c.cfg.MaxToolRetry {
		return fmt.Errorf("%w: 工具 %s 超过重试上限（换思路或问用户）", errTurnAborted, call.ToolID)
	}

	// 规则 2：Bash 连续失败上限
	if call.ToolID == "bash" && result.Status == "error" {
		c.bashFails++
		if c.bashFails > c.cfg.MaxBashFails {
			return fmt.Errorf("%w: bash 在本 turn 失败 %d 次，可能陷入修复循环，STOP 并问用户", errTurnAborted, c.cfg.MaxBashFails)
		}
	}

	// 规则 3：同一错误签名连续上限
	sig := errSignature(call, result)
	c.sameErrCount[sig]++
	if c.sameErrCount[sig] > c.cfg.MaxSameError {
		return fmt.Errorf("%w: 同一错误 %q 出现 %d 次，STOP 换思路", errTurnAborted, sig, c.cfg.MaxSameError)
	}
	return nil
}

// errSignature 用 tool + status + 错误首行（截 80 字符）作为熔断签名。
func errSignature(call ToolCall, result tool.Result) string {
	firstLine := strings.SplitN(result.Output, "\n", 2)[0]
	if len(firstLine) > 80 {
		firstLine = firstLine[:80]
	}
	return call.ToolID + "|" + result.Status + "|" + firstLine
}
