package permission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

// ========== v1.0.1: Ask 真阻塞 + Reply 解阻 ==========

// fakePublisher 记录 Publish 调用
type fakePublisher struct {
	mu  sync.Mutex
	evs []Event
}

func (f *fakePublisher) Publish(ev Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evs = append(f.evs, ev)
}

func (f *fakePublisher) Events() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Event, len(f.evs))
	copy(out, f.evs)
	return out
}

func TestAsk_BlocksUntilReply(t *testing.T) {
	b := NewBroker([]Rule{{Permission: "bash", Action: ActionAsk}})
	b.SetPublisher(&fakePublisher{})

	// 启 goroutine 调 Ask
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- b.Ask(context.Background(), Request{
			Permission: "bash",
			Patterns:   []string{"ls"},
		})
	}()

	// 等 publisher 收到事件
	time.Sleep(50 * time.Millisecond)
	if b.AskWaitCount() != 1 {
		t.Errorf("等待数 = %d, 期望 1", b.AskWaitCount())
	}

	// 找 reqID 并 reply
	evs := b.bus.(*fakePublisher).Events()
	if len(evs) != 1 {
		t.Fatalf("publisher 应收 1 个事件, got %d", len(evs))
	}
	data := evs[0].Data.(map[string]any)
	reqID := data["req_id"].(string)

	// Reply allow
	if err := b.Reply(reqID, "allow", ""); err != nil {
		t.Fatalf("Reply err: %v", err)
	}

	// Ask 应返 nil
	select {
	case err := <-resultCh:
		if err != nil {
			t.Errorf("Ask 应返 nil, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Ask 未在 1s 内返")
	}

	if b.AskWaitCount() != 0 {
		t.Errorf("Reply 后等待数 = %d, 期望 0", b.AskWaitCount())
	}
}

func TestAsk_ReplyDeny(t *testing.T) {
	b := NewBroker([]Rule{{Permission: "bash", Action: ActionAsk}})
	b.SetPublisher(&fakePublisher{})

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- b.Ask(context.Background(), Request{
			Permission: "bash",
			Patterns:   []string{"rm -rf /"},
		})
	}()

	time.Sleep(50 * time.Millisecond)
	evs := b.bus.(*fakePublisher).Events()
	reqID := evs[0].Data.(map[string]any)["req_id"].(string)

	if err := b.Reply(reqID, "deny", "no"); err != nil {
		t.Fatalf("Reply err: %v", err)
	}

	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrDenied) {
			t.Errorf("Ask 应返 ErrDenied, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Ask 未返")
	}
}

func TestAsk_UnknownReqID(t *testing.T) {
	b := NewBroker(nil)
	err := b.Reply("no-such", "allow", "")
	if err == nil {
		t.Errorf("未知 reqID 应返 err")
	}
}

func TestAsk_NoPublisherFallsBackToAllow(t *testing.T) {
	// 没 publisher 时，ask 走 fallback log + allow
	b := NewBroker([]Rule{{Permission: "bash", Action: ActionAsk}})
	// 故意不 SetPublisher

	err := b.Ask(context.Background(), Request{Permission: "bash"})
	if err != nil {
		t.Errorf("无 publisher 应 fallback allow, got %v", err)
	}
}

func TestAsk_TimeoutDenies(t *testing.T) {
	// 缩短超时（v1.0.1 简化：用默认 60s，但测试用 ctx 取消代替）
	b := NewBroker([]Rule{{Permission: "bash", Action: ActionAsk}})
	b.SetPublisher(&fakePublisher{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	err := b.Ask(ctx, Request{Permission: "bash"})
	if err == nil {
		t.Errorf("ctx 取消应返 err")
	}
}

func TestAsk_AllowShortCircuits(t *testing.T) {
	// allow 规则不发 permission.asked
	b := NewBroker([]Rule{{Permission: "read", Action: ActionAllow}})
	b.SetPublisher(&fakePublisher{})

	err := b.Ask(context.Background(), Request{Permission: "read"})
	if err != nil {
		t.Errorf("allow 应 nil, got %v", err)
	}

	if len(b.bus.(*fakePublisher).Events()) != 0 {
		t.Errorf("allow 不应发事件")
	}
}

func TestAsk_DenyShortCircuits(t *testing.T) {
	b := NewBroker([]Rule{{Permission: "bash", Action: ActionDeny}})
	b.SetPublisher(&fakePublisher{})

	err := b.Ask(context.Background(), Request{Permission: "bash"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("deny 应 ErrDenied, got %v", err)
	}
}
