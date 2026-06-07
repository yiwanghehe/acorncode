// Package tool - Tool 接口与 Registry
package tool

import (
	"context"
	"encoding/json"

	"acorncode/internal/permission"
)

// Definition 描述一个工具
type Definition struct {
	ID          string
	Description string
	Keywords    []string
	JSONSchema  json.RawMessage
}

// Context 是 Execute 时传给工具的上下文
type Context struct {
	SessionID string
	MessageID string
	Agent     string
	CallID    string
	Cwd       string
	Metadata  func(title string, meta map[string]any)
	Ask       func(ctx context.Context, req permission.Request) error
}

// Result 工具返回值。永远返回 Result（不返回 err），错误放 Status/Error
type Result struct {
	Status      string // "success" | "error"
	Title       string
	Output      string
	IsTruncated bool
	Error       string
}

// Tool 是所有工具必须实现的接口
type Tool interface {
	Definition() Definition
	Execute(ctx context.Context, args json.RawMessage, tc Context) (Result, error)
}

// Registry 维护已注册的工具
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Get(id string) (Tool, bool) {
	t, ok := r.tools[id]
	return t, ok
}

func (r *Registry) Definitions() []Definition {
	out := make([]Definition, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Definition())
	}
	return out
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Definition().ID] = t
}

func (r *Registry) RegisterRead(cwd string) *Read {
	read := &Read{Cwd: cwd}
	r.Register(read)
	return read
}

func (r *Registry) RegisterEdit(cwd string) *Edit {
	edit := &Edit{Cwd: cwd}
	r.Register(edit)
	return edit
}

func (r *Registry) RegisterBash(cwd string) *Bash {
	bash := &Bash{DefaultTimeoutSec: 30, MaxOutputBytes: 50000, Cwd: cwd}
	r.Register(bash)
	return bash
}

func (r *Registry) RegisterGrep(cwd string) *Grep {
	g := &Grep{Cwd: cwd}
	r.Register(g)
	return g
}

func (r *Registry) RegisterGlob(cwd string) *Glob {
	g := &Glob{Cwd: cwd}
	r.Register(g)
	return g
}

func (r *Registry) RegisterWebFetch() *WebFetch {
	w := &WebFetch{}
	r.Register(w)
	return w
}

// PickForTurn 返回本轮工具子集。v0.1 简化：返回全部
func (r *Registry) PickForTurn(agent string, budget int, userMsg string, recent []string) []Definition {
	return r.Definitions()
}
