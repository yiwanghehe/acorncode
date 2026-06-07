// Package main - 集成测试
//
// 不测 TUI（需 TTY）；只测无 TTY 时的快速失败。
package main

import (
	"strings"
	"testing"
)

// TestRun_RequiresTTY 验证无 TTY 时返清晰错误
//
// Go 测试默认 stdin 不是 TTY，所以这条测试天然适用
func TestRun_RequiresTTY(t *testing.T) {
	err := run("test-model", t.TempDir()+"/test.db", "ollama", "")
	if err == nil {
		t.Skip("当前环境是 TTY（罕见）；跳过")
	}
	if !strings.Contains(err.Error(), "TTY") {
		t.Errorf("错误应含 'TTY'，got: %v", err)
	}
}

// TestParseArgs 验证 CLI 参数解析
func TestParseArgs(t *testing.T) {
	tests := []struct {
		args         []string
		wantModel    string
		wantDB       string
		wantProvider string
		wantServer   string
	}{
		{[]string{}, "qwen2.5-coder:7b", ".acorncode.db", "ollama", ""},
		{[]string{"llama3.1:8b"}, "llama3.1:8b", ".acorncode.db", "ollama", ""},
		{[]string{"--db=/tmp/x.db"}, "qwen2.5-coder:7b", "/tmp/x.db", "ollama", ""},
		{[]string{"--provider=anthropic"}, "qwen2.5-coder:7b", ".acorncode.db", "anthropic", ""},
		{[]string{"--server=:8080"}, "qwen2.5-coder:7b", ".acorncode.db", "ollama", ":8080"},
		{[]string{"claude-3-5-sonnet-latest", "--provider=anthropic", "--server=:9000"},
			"claude-3-5-sonnet-latest", ".acorncode.db", "anthropic", ":9000"},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, "_"), func(t *testing.T) {
			model, db, provider, serverAddr := parseArgs(tt.args)
			if model != tt.wantModel {
				t.Errorf("model = %q, 期望 %q", model, tt.wantModel)
			}
			if db != tt.wantDB {
				t.Errorf("db = %q, 期望 %q", db, tt.wantDB)
			}
			if provider != tt.wantProvider {
				t.Errorf("provider = %q, 期望 %q", provider, tt.wantProvider)
			}
			if serverAddr != tt.wantServer {
				t.Errorf("server = %q, 期望 %q", serverAddr, tt.wantServer)
			}
		})
	}
}
