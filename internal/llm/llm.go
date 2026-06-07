// Package llm - LLM provider 抽象
// 设计: docs/acorncode-architect.md §5.5
package llm

import (
	"context"
	"encoding/json"
)

// Model 标识一个具体的模型
type Model struct {
	ID         string
	ProviderID string
	Variant    string
}

// Usage 累计 token 使用
type Usage struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
	CacheRead       int
	CacheWrite      int
}

// Message 是传给 LLM 的单条消息
type Message struct {
	Role    string
	Content string
}

// ChatRequest 是一次完整请求的输入
type ChatRequest struct {
	Model   Model
	System  []string
	Tools   []Definition
	History []Message
}

// Definition 描述一个工具
type Definition struct {
	ID          string
	Description string
	Keywords    []string
	JSONSchema  json.RawMessage
}

// RawChunk 是网络层原始 token。Provider 返回这些。
// Strategy（如 native）消费后转为类型化的 StreamEvent。
//
// Type 取值：
//
//	"text"           - 文本增量
//	"tool_call"      - 完整工具调用（Ollama 风格，非流式 delta）
//	"tool_call_delta" - 工具调用参数增量（Anthropic 风格；v1 可能不用）
//	"thinking"       - 思考内容（如 o1）
//	"finish"         - 流结束；Meta 含 Usage JSON
//	"error"          - 协议错误；Data 是错误信息
type RawChunk struct {
	Type string
	Data string
	Meta any
}

// Provider 是所有 LLM 后端必须实现的接口
type Provider interface {
	// Stream 返回原始 chunk 通道。通道在流结束时关闭（成功或错误）。
	// 调用方负责消费到 close。
	Stream(ctx context.Context, req ChatRequest) (<-chan RawChunk, error)

	// ListModels 返回后端可用的模型名列表。
	// 用于 `acorn probe` 和配置校验。
	ListModels(ctx context.Context) ([]string, error)
}

// StreamEvent 是类型化（解析后）的事件。Processor 消费这些。
type StreamEvent interface{ isStreamEvent() }

// TextDelta 文本增量
type TextDelta struct{ Text string }

// ReasoningDelta 思考内容增量
type ReasoningDelta struct{ Text string }

// ToolCallStart 工具调用开始（仅在流式协议中出现）
type ToolCallStart struct{ CallID, Name string }

// ToolCallDelta 工具调用参数增量
type ToolCallDelta struct{ CallID, ArgsChunk string }

// ToolCallEnd 工具调用参数收齐
type ToolCallEnd struct {
	CallID string
	Name   string
	Args   json.RawMessage
}

// FinishEvent 流结束事件
type FinishEvent struct {
	Reason string
	Usage  Usage
}

// ErrorEvent 错误事件
type ErrorEvent struct{ Err error }

// 编译时断言：所有事件类型属于 StreamEvent
func (TextDelta) isStreamEvent()      {}
func (ReasoningDelta) isStreamEvent() {}
func (ToolCallStart) isStreamEvent()  {}
func (ToolCallDelta) isStreamEvent()  {}
func (ToolCallEnd) isStreamEvent()    {}
func (FinishEvent) isStreamEvent()    {}
func (ErrorEvent) isStreamEvent()     {}
