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
	err := run(cliArgs{
		ModelName:     "test-model",
		DBPath:        t.TempDir() + "/test.db",
		ProviderName:  "ollama",
		ToolcallStrat: "native",
	})
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
		args []string
		want cliArgs
	}{
		{[]string{}, cliArgs{ModelName: "qwen2.5-coder:7b", DBPath: ".acorncode.db", ProviderName: "ollama", ToolcallStrat: "native"}},
		{[]string{"llama3.1:8b"}, cliArgs{ModelName: "llama3.1:8b", DBPath: ".acorncode.db", ProviderName: "ollama", ToolcallStrat: "native"}},
		{[]string{"--db=/tmp/x.db"}, cliArgs{ModelName: "qwen2.5-coder:7b", DBPath: "/tmp/x.db", ProviderName: "ollama", ToolcallStrat: "native"}},
		{[]string{"--provider=anthropic"}, cliArgs{ModelName: "qwen2.5-coder:7b", DBPath: ".acorncode.db", ProviderName: "anthropic", ToolcallStrat: "native"}},
		{[]string{"--server=:8080"}, cliArgs{ModelName: "qwen2.5-coder:7b", DBPath: ".acorncode.db", ProviderName: "ollama", ServerAddr: ":8080", ToolcallStrat: "native"}},
		{[]string{"--toolcall=prompted"}, cliArgs{ModelName: "qwen2.5-coder:7b", DBPath: ".acorncode.db", ProviderName: "ollama", ToolcallStrat: "prompted"}},
		{[]string{"--toolcall=grammar"}, cliArgs{ModelName: "qwen2.5-coder:7b", DBPath: ".acorncode.db", ProviderName: "ollama", ToolcallStrat: "grammar"}},
		{[]string{"--api-key=mysecret"}, cliArgs{ModelName: "qwen2.5-coder:7b", DBPath: ".acorncode.db", ProviderName: "ollama", ToolcallStrat: "native", APIKey: "mysecret"}},
		{[]string{"--force-tool"}, cliArgs{ModelName: "qwen2.5-coder:7b", DBPath: ".acorncode.db", ProviderName: "ollama", ToolcallStrat: "native", ForceToolCall: true}},
		{[]string{"claude-3-5-sonnet-latest", "--provider=anthropic", "--server=:9000", "--toolcall=grammar", "--api-key=xx", "--force-tool"},
			cliArgs{ModelName: "claude-3-5-sonnet-latest", DBPath: ".acorncode.db", ProviderName: "anthropic", ServerAddr: ":9000", ToolcallStrat: "grammar", APIKey: "xx", ForceToolCall: true}},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, "_"), func(t *testing.T) {
			// 清掉 env 避免干扰
			t.Setenv("ACORN_API_KEY", "")

			got := parseArgs(tt.args)
			if got.ModelName != tt.want.ModelName {
				t.Errorf("ModelName = %q, 期望 %q", got.ModelName, tt.want.ModelName)
			}
			if got.DBPath != tt.want.DBPath {
				t.Errorf("DBPath = %q, 期望 %q", got.DBPath, tt.want.DBPath)
			}
			if got.ProviderName != tt.want.ProviderName {
				t.Errorf("ProviderName = %q, 期望 %q", got.ProviderName, tt.want.ProviderName)
			}
			if got.ServerAddr != tt.want.ServerAddr {
				t.Errorf("ServerAddr = %q, 期望 %q", got.ServerAddr, tt.want.ServerAddr)
			}
			if got.ToolcallStrat != tt.want.ToolcallStrat {
				t.Errorf("ToolcallStrat = %q, 期望 %q", got.ToolcallStrat, tt.want.ToolcallStrat)
			}
			if got.APIKey != tt.want.APIKey {
				t.Errorf("APIKey = %q, 期望 %q", got.APIKey, tt.want.APIKey)
			}
			if got.ForceToolCall != tt.want.ForceToolCall {
				t.Errorf("ForceToolCall = %v, 期望 %v", got.ForceToolCall, tt.want.ForceToolCall)
			}
		})
	}
}

func TestParseArgs_APIKeyFromEnv(t *testing.T) {
	t.Setenv("ACORN_API_KEY", "env-secret")
	got := parseArgs([]string{})
	if got.APIKey != "env-secret" {
		t.Errorf("APIKey = %q, 期望 env-secret", got.APIKey)
	}
	if got.ModelName != "qwen2.5-coder:7b" {
		t.Errorf("ModelName = %q", got.ModelName)
	}
}

// TestParseArgs_ForceToolDefault 验证默认不开启强制工具调用。
func TestParseArgs_ForceToolDefault(t *testing.T) {
	t.Setenv("ACORN_API_KEY", "")
	got := parseArgs([]string{"--toolcall=grammar"})
	if got.ForceToolCall {
		t.Error("默认不应开启 ForceToolCall")
	}
}
