package permission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestBroker_EmptyRules_Allow(t *testing.T) {
	// v0.1 兼容：没规则默认 allow
	b := NewBroker(nil)
	err := b.Ask(context.Background(), Request{Permission: "bash", Patterns: []string{"rm -rf /"}})
	if err != nil {
		t.Errorf("空规则应 allow, got %v", err)
	}
}

func TestBroker_AllowRule(t *testing.T) {
	b := NewBroker([]Rule{
		{Permission: "read", Action: ActionAllow},
	})
	err := b.Ask(context.Background(), Request{Permission: "read", Patterns: []string{"/etc/passwd"}})
	if err != nil {
		t.Errorf("allow rule 应 pass, got %v", err)
	}
}

func TestBroker_DenyRule(t *testing.T) {
	b := NewBroker([]Rule{
		{Permission: "bash", Action: ActionDeny},
	})
	err := b.Ask(context.Background(), Request{Permission: "bash", Patterns: []string{"ls"}})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("deny rule 应返 ErrDenied, got %v", err)
	}
}

func TestBroker_AskRule_DefaultsToAllow(t *testing.T) {
	// v0.3 简化：ask 默认 allow + warn（v0.4 TUI 弹窗）
	b := NewBroker([]Rule{
		{Permission: "edit", Action: ActionAsk},
	})
	err := b.Ask(context.Background(), Request{Permission: "edit", Patterns: []string{"/foo"}})
	if err != nil {
		t.Errorf("v0.3 ask 规则默认 allow, got %v", err)
	}
}

func TestBroker_PatternMatch(t *testing.T) {
	b := NewBroker([]Rule{
		{Permission: "bash", Pattern: "^go (build|test|vet)", Action: ActionAllow},
		{Permission: "bash", Action: ActionDeny},
	})

	tests := []struct {
		cmd      string
		expected error // nil = allow, ErrDenied = deny
	}{
		{"go build ./...", nil},
		{"go test ./...", nil},
		{"go vet ./...", nil},
		{"go run main.go", ErrDenied},
		{"rm -rf /", ErrDenied},
		{"ls -la", ErrDenied},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			err := b.Ask(context.Background(), Request{Permission: "bash", Patterns: []string{tt.cmd}})
			if tt.expected == nil && err != nil {
				t.Errorf("期望 allow, got %v", err)
			}
			if tt.expected != nil && !errors.Is(err, tt.expected) {
				t.Errorf("期望 %v, got %v", tt.expected, err)
			}
		})
	}
}

func TestBroker_PatternMismatch_NoMatch(t *testing.T) {
	// rule 不匹配 → 落到 default（v0.3 allow）
	b := NewBroker([]Rule{
		{Permission: "edit", Pattern: `^/safe/.*`, Action: ActionDeny},
	})
	err := b.Ask(context.Background(), Request{Permission: "edit", Patterns: []string{"/unsafe/path"}})
	if err != nil {
		t.Errorf("不匹配的 pattern 应 fallthrough 到 default allow, got %v", err)
	}
}

func TestBroker_BadPattern_SkipsRule(t *testing.T) {
	// 坏 regex 不应 panic
	b := NewBroker([]Rule{
		{Permission: "bash", Pattern: "[unclosed", Action: ActionDeny},
		{Permission: "bash", Action: ActionAllow}, // fallback
	})
	err := b.Ask(context.Background(), Request{Permission: "bash", Patterns: []string{"ls"}})
	if err != nil {
		t.Errorf("坏 regex 应跳过该 rule, fallthrough 到 fallback allow, got %v", err)
	}
}

func TestBroker_SessionApprove(t *testing.T) {
	b := NewBroker(nil)

	// 第一次：default allow
	err1 := b.Ask(context.Background(), Request{Permission: "edit", Patterns: []string{"/foo"}})
	if err1 != nil {
		t.Errorf("default 应 allow, got %v", err1)
	}

	// 标记 session approve
	b.SessionApprove("edit", "/foo")

	// 第二次：仍 allow
	err2 := b.Ask(context.Background(), Request{Permission: "edit", Patterns: []string{"/foo"}})
	if err2 != nil {
		t.Errorf("session approved 后应 allow, got %v", err2)
	}
}

func TestBroker_SessionApprove_ToolLevel(t *testing.T) {
	b := NewBroker(nil)
	b.SessionApprove("read", "")

	err := b.Ask(context.Background(), Request{Permission: "read", Patterns: []string{"/anywhere"}})
	if err != nil {
		t.Errorf("tool-level session approve 应 apply, got %v", err)
	}
}

func TestBroker_Concurrent(t *testing.T) {
	b := NewBroker([]Rule{{Permission: "read", Action: ActionAllow}})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Ask(context.Background(), Request{Permission: "read", Patterns: []string{"/x"}})
		}()
	}
	wg.Wait()
}

func TestBroker_AddRules(t *testing.T) {
	b := NewBroker(nil)

	// 初始：default allow
	if err := b.Ask(context.Background(), Request{Permission: "bash", Patterns: []string{"x"}}); err != nil {
		t.Errorf("初始应 allow, got %v", err)
	}

	// 加 deny rule
	b.AddRules([]Rule{{Permission: "bash", Action: ActionDeny}})

	if err := b.Ask(context.Background(), Request{Permission: "bash", Patterns: []string{"x"}}); !errors.Is(err, ErrDenied) {
		t.Errorf("加 deny rule 后应 deny, got %v", err)
	}
}

// ========== LoadConfig ==========

func TestLoadConfig_NotExist_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(filepath.Join(dir, "no-such-file.json"))
	if err != nil {
		t.Errorf("文件不存在应返 nil err, got %v", err)
	}
	if cfg != nil {
		t.Errorf("文件不存在应返 nil cfg, got %v", cfg)
	}
}

func TestLoadConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acorncode.json")
	data := `{
		"permissions": {
			"rules": [
				{"tool": "read", "action": "allow"},
				{"tool": "bash", "pattern": "^go test", "action": "allow"},
				{"tool": "bash", "action": "deny"}
			]
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig err: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig 返 nil")
	}
	if len(cfg.Permissions.Rules) != 3 {
		t.Errorf("rules 数 = %d, 期望 3", len(cfg.Permissions.Rules))
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{bad json"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Errorf("坏 JSON 应返 err")
	}
}
