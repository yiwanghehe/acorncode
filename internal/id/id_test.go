// Package id 的测试
package id

import (
	"strings"
	"testing"
)

// TestShort_长度 验证 Short 返回 8 字符
func TestShort_长度(t *testing.T) {
	got := Short()
	if len(got) != 8 {
		t.Errorf("Short() 长度 = %d, want 8", len(got))
	}
}

// TestNew_前缀与长度 验证 New 带前缀且后段 16 字符
func TestNew_前缀与长度(t *testing.T) {
	got := New("msg")
	if !strings.HasPrefix(got, "msg_") {
		t.Errorf("New(\"msg\") = %q, 应以 msg_ 开头", got)
	}
	suffix := strings.TrimPrefix(got, "msg_")
	if len(suffix) != 16 {
		t.Errorf("New 后段长度 = %d, want 16", len(suffix))
	}
}

// TestEncode_仅含 base36 字符 验证输出字符集
func TestEncode_仅含base36字符(t *testing.T) {
	got := encode(20)
	for _, c := range got {
		if !strings.ContainsRune(chars, c) {
			t.Errorf("encode 输出含非 base36 字符: %q", c)
		}
	}
}

// TestShort_唯一性 连续生成应不重复（counter 防同纳秒冲突）
func TestShort_唯一性(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		s := Short()
		if seen[s] {
			t.Fatalf("Short() 第 %d 次重复: %q", i, s)
		}
		seen[s] = true
	}
}

// TestNew_唯一性 连续生成带前缀 ID 应不重复
func TestNew_唯一性(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		s := New("prt")
		if seen[s] {
			t.Fatalf("New() 第 %d 次重复: %q", i, s)
		}
		seen[s] = true
	}
}
