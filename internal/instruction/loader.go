// Package instruction - AGENTS.md 加载器（真实实现）
// 参考: docs/acorncode-architect.md §9.5.2
package instruction

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

// Loader 在指定目录及其子目录查找 AGENTS.md 并返回内容
type Loader struct {
	workdir   string
	searchDir []string // 要搜的目录列表
	mu        sync.RWMutex
	cache     string
	cached    bool
}

// NewLoader 创建 Loader，在 workdir 及其子目录查找 AGENTS.md
func NewLoader(workdir string) *Loader {
	return &Loader{
		workdir: workdir,
		searchDir: []string{
			workdir,
			filepath.Join(workdir, ".acorncode"),
		},
	}
}

// Load 返回 AGENTS.md 内容。找到多个时返回最长那个。
// 未找到返空字符串（不报错）。
func (l *Loader) Load(ctx context.Context) (string, error) {
	l.mu.RLock()
	if l.cached {
		defer l.mu.RUnlock()
		return l.cache, nil
	}
	l.mu.RUnlock()

	// 1. 在 workdir 找
	for _, dir := range l.searchDir {
		path := filepath.Join(dir, "AGENTS.md")
		data, err := os.ReadFile(path)
		if err == nil {
			l.mu.Lock()
			l.cache = string(data)
			l.cached = true
			l.mu.Unlock()
			return string(data), nil
		}
	}

	// 2. 在 workdir 下一层找
	if l.workdir != "" {
		entries, err := os.ReadDir(l.workdir)
		if err == nil {
			var best string
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if e.Name() == "AGENTS.md" {
					data, _ := os.ReadFile(filepath.Join(l.workdir, e.Name()))
					if len(data) > len(best) {
						best = string(data)
					}
				}
			}
			if best != "" {
				l.mu.Lock()
				l.cache = best
				l.cached = true
				l.mu.Unlock()
				return best, nil
			}
		}
	}

	return "", nil
}

// Invalidate 清除缓存（AGENTS.md 修改后调用）
func (l *Loader) Invalidate() {
	l.mu.Lock()
	l.cached = false
	l.cache = ""
	l.mu.Unlock()
}
