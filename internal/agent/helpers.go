// Package agent - helpers.go
// 参考: docs/acorncode-architect.md §16.5
package agent

import (
	"acorncode/internal/llm"
	"acorncode/internal/session"
	"acorncode/internal/tool"
)

// builtinBasePrompt 是硬编码的基础 system prompt（见 §9.5.1）
const builtinBasePrompt = `# AcornCode

You are AcornCode, a coding assistant that operates inside the user's project.
You help with coding tasks by reading files, editing files, and running shell commands.

## Behavioral guidelines
1. Read relevant code before making changes
2. Make minimal, targeted edits (don't refactor unrelated code)
3. Always explain what you're doing and why
4. Stop and ask the user if requirements are unclear
5. When you finish a task, summarize what you did

## Output format
- Use markdown for readability
- Reference file paths with backticks: ` + "`path/to/file.go`" + `
- Keep responses focused and concise
`

// lastUserText 返回历史中最近一条 user 消息的文本
func lastUserText(history []*session.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			// 简化：返回 part 中第一个 TextPart 的 text
			for _, p := range history[i].Parts {
				if tp, ok := p.(*session.TextPart); ok {
					return tp.Text
				}
			}
		}
	}
	return ""
}

// recentToolCalls 返回最近 5 个工具调用 ID（用于 §8.6 评分）
func recentToolCalls(history []*session.Message) []string {
	var calls []string
	for i := len(history) - 1; i >= 0 && len(calls) < 5; i-- {
		for _, p := range history[i].Parts {
			if tp, ok := p.(*session.ToolPart); ok {
				calls = append(calls, tp.ToolID)
			}
		}
	}
	return calls
}

// toModelMessages 把 session.Message 转为 llm.Message
func toModelMessages(history []*session.Message) []llm.Message {
	out := make([]llm.Message, 0, len(history))
	for _, m := range history {
		msg := llm.Message{Role: m.Role}
		// 简化：把 part 序列化为单段 content
		// 实际实现要看 llm.Provider 接口如何要求
		switch m.Role {
		case "user":
			for _, p := range m.Parts {
				if tp, ok := p.(*session.TextPart); ok {
					msg.Content = tp.Text
				}
			}
		case "assistant":
			for _, p := range m.Parts {
				if tp, ok := p.(*session.TextPart); ok {
					msg.Content += tp.Text
				}
			}
		}
		out = append(out, msg)
	}
	return out
}

// estimateTokens 粗估 token 数。
// TODO: 替换为真正的 tokenizer（tiktoken-go 或类似）
func estimateTokens(history []*session.Message, tools []tool.Definition) int {
	// 粗估：每 4 字符 ≈ 1 token
	total := 0
	for _, m := range history {
		for _, p := range m.Parts {
			if tp, ok := p.(*session.TextPart); ok {
				total += len(tp.Text) / 4
			}
		}
	}
	for _, t := range tools {
		total += len(t.Description) / 4
	}
	return total
}
