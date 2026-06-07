// Package compaction - 长 session 摘要压缩（v1.0.3）
//
// 当消息历史超过阈值时，把老消息摘要成 1 段，保留最近 N 条。
// 失败时返原 history（保守策略：不丢消息）。
package compaction

import (
	"context"
	"fmt"
	"log/slog"

	"acorncode/internal/llm"
)

// Compactor 是压缩器接口
type Compactor interface {
	// Compact 把 history 压成更短的列表。失败返原 history + error。
	Compact(ctx context.Context, history []llm.Message) ([]llm.Message, error)
}

// SimpleCompactor 是基础实现：调 LLM 摘要
type SimpleCompactor struct {
	Provider  llm.Provider
	Model     llm.Model
	KeepRecent int   // 保留最近 N 条不压
	MaxSummary int   // 摘要最大 token（粗估 4 字符 = 1 token）
}

// summarizePrompt 调 LLM 摘要
const summarizePrompt = `你是一个对话摘要助手。请用 1 段话（不超过 %d 字）总结以下对话的关键信息：

1. 用户的核心目标
2. 已完成的工作（不要细节）
3. 关键决策和发现
4. 接下来要做什么

只输出摘要本身，不要加 "摘要：" 之类前缀。

对话：
%s`

// Compact 把 history 中除最近 KeepRecent 条外的全部压成 1 条 system 消息
func (c *SimpleCompactor) Compact(ctx context.Context, history []llm.Message) ([]llm.Message, error) {
	if c.Provider == nil {
		return history, fmt.Errorf("compaction: Provider 未设置")
	}
	if len(history) <= c.KeepRecent {
		return history, nil // 不够长，不压
	}

	toCompact := history[:len(history)-c.KeepRecent]
	keep := history[len(history)-c.KeepRecent:]

	// 1. 拼要摘要的对话
	dialogue := ""
	for _, m := range toCompact {
		dialogue += fmt.Sprintf("[%s] %s\n", m.Role, m.Content)
	}

	// 2. 调 LLM 摘要
	maxChars := c.MaxSummary * 4
	if maxChars <= 0 {
		maxChars = 800 // 默认 200 token
	}
	prompt := fmt.Sprintf(summarizePrompt, maxChars, dialogue)

	req := llm.ChatRequest{
		Model:  c.Model,
		System: []string{"You are a conversation summarizer."},
		History: []llm.Message{
			{Role: "user", Content: prompt},
		},
	}
	rawCh, err := c.Provider.Stream(ctx, req)
	if err != nil {
		slog.WarnContext(ctx, "compaction: Provider.Stream 失败", "err", err)
		return history, fmt.Errorf("provider: %w", err)
	}

	summary, err := collectText(ctx, rawCh)
	if err != nil {
		slog.WarnContext(ctx, "compaction: collect text 失败", "err", err)
		return history, fmt.Errorf("collect: %w", err)
	}

	if summary == "" {
		// LLM 没返任何东西，保守返原 history
		return history, nil
	}

	// 3. 拼新 history：1 条 system 摘要 + 保留的最近
	summaryMsg := llm.Message{
		Role:    "system",
		Content: fmt.Sprintf("[Earlier conversation summary]\n%s", summary),
	}
	out := make([]llm.Message, 0, 1+len(keep))
	out = append(out, summaryMsg)
	out = append(out, keep...)
	return out, nil
}

// collectText 从 RawChunk 流收集 text（不管 tool_call / finish）
func collectText(ctx context.Context, ch <-chan llm.RawChunk) (string, error) {
	var sb []byte
	for c := range ch {
		if c.Type == "text" {
			sb = append(sb, c.Data...)
		}
		if c.Type == "error" {
			return string(sb), fmt.Errorf("stream error: %s", c.Data)
		}
	}
	// ctx 取消检查
	if err := ctx.Err(); err != nil {
		return string(sb), err
	}
	return string(sb), nil
}
