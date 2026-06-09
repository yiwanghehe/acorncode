// Package server - forcetool_test.go
//
// 验证 v1.7：请求级 force_tool 开关的策略选择逻辑。
package server

import (
	"testing"

	"acorncode/internal/toolcall"
)

// newServerWithStrategy 构造一个仅含指定策略的 Server（不启 HTTP，只测内部逻辑）。
func newServerWithStrategy(s toolcall.Strategy) *Server {
	return &Server{cfg: Config{Strategy: s}}
}

// TestStrategyForRequest_NoForce 不强制时返回共享策略本身。
func TestStrategyForRequest_NoForce(t *testing.T) {
	base := toolcall.NewGrammar()
	s := newServerWithStrategy(base)
	got := s.strategyForRequest(false)
	if got != base {
		t.Errorf("force=false 应返回共享策略本身")
	}
}

// TestStrategyForRequest_GrammarForce 强制 + grammar：返回独立的 ForceToolCall 实例。
func TestStrategyForRequest_GrammarForce(t *testing.T) {
	base := toolcall.NewGrammar()
	s := newServerWithStrategy(base)
	got := s.strategyForRequest(true)

	g, ok := got.(*toolcall.Grammar)
	if !ok {
		t.Fatalf("应返回 *Grammar, got %T", got)
	}
	if !g.ForceToolCall {
		t.Error("force=true 时新实例应开启 ForceToolCall")
	}
	// 必须是独立实例，不污染共享策略
	if got == toolcall.Strategy(base) {
		t.Error("应返回独立实例，不能复用共享策略")
	}
	if base.ForceToolCall {
		t.Error("共享策略不应被修改")
	}
}

// TestStrategyForRequest_NonGrammarForce 非 grammar 策略：force 被忽略，返回原策略。
func TestStrategyForRequest_NonGrammarForce(t *testing.T) {
	base := toolcall.NewNative()
	s := newServerWithStrategy(base)
	got := s.strategyForRequest(true)
	if got != toolcall.Strategy(base) {
		t.Error("非 grammar 策略 force 应被忽略，返回原策略")
	}
}

// TestStrategyForRequest_PromptedForce prompted 策略同样忽略 force。
func TestStrategyForRequest_PromptedForce(t *testing.T) {
	base := toolcall.NewPrompted()
	s := newServerWithStrategy(base)
	got := s.strategyForRequest(true)
	if got != toolcall.Strategy(base) {
		t.Error("prompted 策略 force 应被忽略")
	}
}

// TestStrategyForRequest_ConcurrentIsolation 并发请求各拿独立实例，互不干扰。
func TestStrategyForRequest_ConcurrentIsolation(t *testing.T) {
	base := toolcall.NewGrammar()
	s := newServerWithStrategy(base)
	a := s.strategyForRequest(true)
	b := s.strategyForRequest(true)
	if a == b {
		t.Error("两次强制请求应各拿独立 Grammar 实例")
	}
}
