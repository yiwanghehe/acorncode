// Package agent - helpers.go
// 参考: docs/acorncode-architect.md §16.5
package agent

import (
	"acorncode/internal/llm"
	"acorncode/internal/session"
	"acorncode/internal/tokenizer"
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
		case "system":
			// Compaction 写回的「历史摘要」以 system 角色存在历史里，
			// 必须转出来让后续 turn 的模型看到，否则压缩等于丢上下文。
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

// estimateTokens 估算一次请求的 token 数（history + 工具定义）。
// v1.10 起改用 internal/tokenizer 启发式估算（纯 stdlib，0 依赖），
// 比旧的 len/4 在中文与工具调用场景准得多。仅用于 compact 触发判断，
// 不要求精确，只要单调可比即可。
func estimateTokens(history []*session.Message, tools []tool.Definition) int {
	total := 0
	for _, m := range history {
		for _, p := range m.Parts {
			switch tp := p.(type) {
			case *session.TextPart:
				total += tokenizer.Count(tp.Text)
			case *session.ReasoningPart:
				total += tokenizer.Count(tp.Text)
			case *session.ToolPart:
				// 工具调用的 args 与输出也占上下文，之前被完全漏算
				total += tokenizer.Count(string(tp.Args))
				total += tokenizer.Count(tp.Output)
			}
		}
	}
	for _, t := range tools {
		total += tokenizer.Count(t.Description)
		// schema 体积不小，也计入预算
		total += tokenizer.Count(string(t.JSONSchema))
	}
	return total
}
