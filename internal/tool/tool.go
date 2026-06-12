// Package tool - Tool 接口与 Registry
package tool

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

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

// PickForTurn 返回本轮要暴露给模型的工具子集（v1.11 起真实裁剪）。
//
// 动机：工具过多会膨胀小模型 prompt、增加选错工具的概率。本方法按预算 budget
// 挑出最相关的工具：
//   - budget <= 0 或工具总数 ≤ budget：返回全部（不裁剪），保持向后兼容。
//   - 否则按相关性打分取 top-budget。
//
// 打分（启发式，确定性可测）：
//   - userMsg 命中工具关键词：每个命中 +10
//   - userMsg 含工具 ID 字面：+8
//   - 最近调用过：按新近度加权（最近一次 +5，依次递减，下限 +1）
//   - 核心工具（read/bash）：基础分 +2 当安全网——无人命中时它们兜底入选，
//     但**不会盖过**被关键词强命中的工具（命中分远高于基础分）
//   - 同分时按工具 ID 字典序稳定排序，保证结果确定
func (r *Registry) PickForTurn(agent string, budget int, userMsg string, recent []string) []Definition {
	defs := r.Definitions()
	if budget <= 0 || len(defs) <= budget {
		// 不裁剪：仍按 ID 排序保证输出稳定（map 遍历无序）
		sortDefsByID(defs)
		return defs
	}

	lowerMsg := strings.ToLower(userMsg)

	// 最近调用的新近度权重：recent[0] 视为最新
	recentScore := make(map[string]int, len(recent))
	for i, id := range recent {
		w := 5 - i // 5,4,3,...
		if w < 1 {
			w = 1
		}
		if w > recentScore[id] {
			recentScore[id] = w
		}
	}

	scoredDefs := make([]scoredDef, 0, len(defs))
	for _, d := range defs {
		s := 0
		for _, kw := range d.Keywords {
			if kw != "" && strings.Contains(lowerMsg, strings.ToLower(kw)) {
				s += 10
			}
		}
		if d.ID != "" && strings.Contains(lowerMsg, strings.ToLower(d.ID)) {
			s += 8
		}
		s += recentScore[d.ID]
		if isCoreTool(d.ID) {
			s += 2 // 核心工具基础分：无命中时兜底，但不盖过强命中
		}
		scoredDefs = append(scoredDefs, scoredDef{def: d, score: s})
	}

	// 按分数降序、ID 升序排序（确定性）
	sort.Slice(scoredDefs, func(i, j int) bool {
		if scoredDefs[i].score != scoredDefs[j].score {
			return scoredDefs[i].score > scoredDefs[j].score
		}
		return scoredDefs[i].def.ID < scoredDefs[j].def.ID
	})

	// 取 top-budget
	picked := make([]Definition, 0, budget)
	for _, sd := range scoredDefs[:budget] {
		picked = append(picked, sd.def)
	}

	sortDefsByID(picked)
	return picked
}

// scoredDef 是 PickForTurn 内部打分用的工具+分数对
type scoredDef struct {
	def   Definition
	score int
}

// coreToolIDs 是任何编码任务几乎都需要的基础工具，裁剪时给基础分兜底
var coreToolIDs = []string{"read", "bash"}

func isCoreTool(id string) bool {
	for _, c := range coreToolIDs {
		if c == id {
			return true
		}
	}
	return false
}

// sortDefsByID 按 ID 字典序排序（稳定输出）
func sortDefsByID(defs []Definition) {
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
}
