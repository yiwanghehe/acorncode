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
	// 用 /dev/null 重定向 stdin
	// Windows 上 stdin 是 CON，不会是 TTY
	err := run("test-model", t.TempDir()+"/test.db")
	if err == nil {
		t.Skip("当前环境是 TTY（罕见）；跳过")
	}
	if !strings.Contains(err.Error(), "TTY") {
		t.Errorf("错误应含 'TTY'，got: %v", err)
	}
}

// TestParseArgs 验证 CLI 参数解析
func TestParseArgs(t *testing.T) {
	// 直接测试 main 里的解析逻辑（提取出来）
	tests := []struct {
		args      []string
		wantModel string
		wantDB    string
	}{
		{[]string{}, "qwen2.5-coder:7b", ".acorncode.db"},
		{[]string{"llama3.1:8b"}, "llama3.1:8b", ".acorncode.db"},
		{[]string{"--db=/tmp/x.db"}, "qwen2.5-coder:7b", "/tmp/x.db"},
		{[]string{"deepseek", "--db=./d.db"}, "deepseek", "./d.db"},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, "_"), func(t *testing.T) {
			model, db := parseArgs(tt.args)
			if model != tt.wantModel {
				t.Errorf("model = %q, 期望 %q", model, tt.wantModel)
			}
			if db != tt.wantDB {
				t.Errorf("db = %q, 期望 %q", db, tt.wantDB)
			}
		})
	}
}
