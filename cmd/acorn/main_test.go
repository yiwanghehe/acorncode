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
	t.Setenv("ACORN_API_KEY", "")
	err := run("test-model", t.TempDir()+"/test.db", "ollama", "", "native", "")
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
		wantTC       string
		wantAPIKey   string
	}{
		{[]string{}, "qwen2.5-coder:7b", ".acorncode.db", "ollama", "", "native", ""},
		{[]string{"llama3.1:8b"}, "llama3.1:8b", ".acorncode.db", "ollama", "", "native", ""},
		{[]string{"--db=/tmp/x.db"}, "qwen2.5-coder:7b", "/tmp/x.db", "ollama", "", "native", ""},
		{[]string{"--provider=anthropic"}, "qwen2.5-coder:7b", ".acorncode.db", "anthropic", "", "native", ""},
		{[]string{"--server=:8080"}, "qwen2.5-coder:7b", ".acorncode.db", "ollama", ":8080", "native", ""},
		{[]string{"--toolcall=prompted"}, "qwen2.5-coder:7b", ".acorncode.db", "ollama", "", "prompted", ""},
		{[]string{"--toolcall=grammar"}, "qwen2.5-coder:7b", ".acorncode.db", "ollama", "", "grammar", ""},
		{[]string{"--api-key=mysecret"}, "qwen2.5-coder:7b", ".acorncode.db", "ollama", "", "native", "mysecret"},
		{[]string{"claude-3-5-sonnet-latest", "--provider=anthropic", "--server=:9000", "--toolcall=grammar", "--api-key=xx"},
			"claude-3-5-sonnet-latest", ".acorncode.db", "anthropic", ":9000", "grammar", "xx"},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, "_"), func(t *testing.T) {
			// 清掉 env 避免干扰
			t.Setenv("ACORN_API_KEY", "")

			model, db, provider, serverAddr, tc, apiKey := parseArgs(tt.args)
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
			if tc != tt.wantTC {
				t.Errorf("toolcall = %q, 期望 %q", tc, tt.wantTC)
			}
			if apiKey != tt.wantAPIKey {
				t.Errorf("apiKey = %q, 期望 %q", apiKey, tt.wantAPIKey)
			}
		})
	}
}

func TestParseArgs_APIKeyFromEnv(t *testing.T) {
	t.Setenv("ACORN_API_KEY", "env-secret")
	model, _, _, _, _, apiKey := parseArgs([]string{})
	if apiKey != "env-secret" {
		t.Errorf("apiKey = %q, 期望 env-secret", apiKey)
	}
	if model != "qwen2.5-coder:7b" {
		t.Errorf("model = %q", model)
	}
}
