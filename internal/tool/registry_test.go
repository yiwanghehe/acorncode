package tool

import (
	"testing"
)

func TestRegistry_RegisterEditAndGet(t *testing.T) {
	r := NewRegistry()
	edit := r.RegisterEdit("/tmp")
	if edit == nil {
		t.Fatal("RegisterEdit 返 nil")
	}

	got, ok := r.Get("edit")
	if !ok {
		t.Fatal("Get('edit') 返 false")
	}
	if got != edit {
		t.Errorf("Get('edit') 返不同的指针: %T vs %T", got, edit)
	}
}

func TestRegistry_RegisterAllAndList(t *testing.T) {
	r := NewRegistry()
	r.RegisterRead("/tmp")
	r.RegisterEdit("/tmp")
	r.RegisterBash("/tmp")

	defs := r.Definitions()
	if len(defs) != 3 {
		t.Errorf("Definitions = %d, 期望 3", len(defs))
	}
	for _, def := range defs {
		t.Logf("tool: %s", def.ID)
	}
}

// ========== PickForTurn 工具裁剪（v1.11）==========

// regWithAll 注册全部 6 个工具，返回 registry
func regWithAll(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	r.RegisterRead("/tmp")
	r.RegisterEdit("/tmp")
	r.RegisterBash("/tmp")
	r.RegisterGrep("/tmp")
	r.RegisterGlob("/tmp")
	r.RegisterWebFetch()
	return r
}

func idsOf(defs []Definition) map[string]bool {
	m := make(map[string]bool, len(defs))
	for _, d := range defs {
		m[d.ID] = true
	}
	return m
}

// TestPickForTurn_NoBudget 预算 0 返回全部
func TestPickForTurn_NoBudget(t *testing.T) {
	r := regWithAll(t)
	got := r.PickForTurn("build", 0, "随便", nil)
	if len(got) != 6 {
		t.Errorf("budget=0 应返全部 6, 实际 %d", len(got))
	}
}

// TestPickForTurn_BudgetGEQTotal 预算 >= 工具数返回全部
func TestPickForTurn_BudgetGEQTotal(t *testing.T) {
	r := regWithAll(t)
	got := r.PickForTurn("build", 10, "随便", nil)
	if len(got) != 6 {
		t.Errorf("budget>=total 应返全部 6, 实际 %d", len(got))
	}
}

// TestPickForTurn_KeywordMatch 关键词命中的工具应入选
func TestPickForTurn_KeywordMatch(t *testing.T) {
	r := regWithAll(t)
	// 提到 "下载 url" 应命中 webfetch 的关键词
	got := r.PickForTurn("build", 3, "帮我下载这个 url 的内容", nil)
	if len(got) != 3 {
		t.Fatalf("budget=3 应返 3, 实际 %d", len(got))
	}
	if !idsOf(got)["webfetch"] {
		t.Errorf("命中下载/url 关键词的 webfetch 应入选: %v", idsOf(got))
	}
}

// TestPickForTurn_RecentBoost 最近调用过的工具应加分入选
func TestPickForTurn_RecentBoost(t *testing.T) {
	r := regWithAll(t)
	// userMsg 无明显关键词，靠 recent 把 grep 顶上来
	got := r.PickForTurn("build", 3, "继续", []string{"grep", "glob"})
	ids := idsOf(got)
	if !ids["grep"] {
		t.Errorf("最近调用的 grep 应入选: %v", ids)
	}
}

// TestPickForTurn_CoreToolsFallback 无关键词命中时核心工具 read/bash 靠基础分兜底入选
func TestPickForTurn_CoreToolsFallback(t *testing.T) {
	r := regWithAll(t)
	// userMsg 无任何工具关键词、无 recent → 仅核心工具有基础分
	got := r.PickForTurn("build", 2, "你好", nil)
	ids := idsOf(got)
	if !ids["read"] || !ids["bash"] {
		t.Errorf("无命中时核心工具 read/bash 应兜底入选: %v", ids)
	}
	if len(got) != 2 {
		t.Errorf("预算 2 应返 2, 实际 %d", len(got))
	}
}

// TestPickForTurn_StrongMatchBeatsCore 强命中工具优先于核心安全网
func TestPickForTurn_StrongMatchBeatsCore(t *testing.T) {
	r := regWithAll(t)
	// 同时强命中 webfetch 与 grep，预算 2 → 命中分(>=10)盖过核心基础分(+2)
	got := r.PickForTurn("build", 2, "下载 url 然后 grep 搜索内容", nil)
	ids := idsOf(got)
	if !ids["webfetch"] || !ids["grep"] {
		t.Errorf("强命中的 webfetch/grep 应优先于核心工具: %v", ids)
	}
}

// TestPickForTurn_Deterministic 同输入多次结果一致（确定性）
func TestPickForTurn_Deterministic(t *testing.T) {
	r := regWithAll(t)
	first := r.PickForTurn("build", 3, "读文件并编辑", nil)
	for i := 0; i < 5; i++ {
		again := r.PickForTurn("build", 3, "读文件并编辑", nil)
		if len(first) != len(again) {
			t.Fatalf("长度不稳定: %d vs %d", len(first), len(again))
		}
		for j := range first {
			if first[j].ID != again[j].ID {
				t.Errorf("第 %d 项不稳定: %s vs %s", j, first[j].ID, again[j].ID)
			}
		}
	}
}
