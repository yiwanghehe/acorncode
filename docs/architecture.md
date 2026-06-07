# AcornCode 架构（v0.1）

> v0.1 真实实现的架构。Phase 2+ 推迟的内容（GBNF、Compaction、SQLite、HTTP Server 等）不在此文档。

## 1. 一句话

本地小模型优先的 Go 编码 agent，能自举开发（让模型自己写新工具）。

**对比 opencode**：单二进制 / 0 第三方依赖 / Ollama + Native toolcall / stdout REPL。详见 [README.md §对比表](../README.md)。

## 2. 分层

```
┌─────────────────────────────────────────────────────┐
│  CLI (cmd/acorn/main.go)                            │  stdout REPL
└────────────────────┬────────────────────────────────┘
                     │
┌────────────────────▼────────────────────────────────┐
│  Agent Loop (internal/agent)                        │  8 状态机 + 3 熔断
│  ├─ loop.go:        状态机 + LLM 调度               │
│  ├─ processor.go:   流式 chunk → part 状态          │
│  └─ helpers.go:     token 估算 / 消息转换 / system  │
└─┬──────┬──────┬──────┬──────┬──────┬─────────────────┘
  │      │      │      │      │      │
  ▼      ▼      ▼      ▼      ▼      ▼
 LLM   Tool   Perm   Sess   Bus   Instr
 (Ollama)  (read/edit/bash)  (broker)  (mem)  (6 events)  (AGENTS.md)
```

## 3. 核心契约

### 3.1 Tool

```go
type Tool interface {
    Definition() Definition
    Execute(ctx context.Context, args json.RawMessage, tc Context) (Result, error)
}

type Result struct {
    Status      string  // "success" | "error"
    Title       string
    Output      string
    IsTruncated bool
    Error       string
}
```

**约定**：工具永远返回 `Result{}`，不返回 `Go err`。错误放 `Result.Error` 或 `Status="error"`。

### 3.2 LLM Provider

```go
type Provider interface {
    Stream(ctx context.Context, req ChatRequest) (<-chan RawChunk, error)
}

type ChatRequest struct {
    Model   Model
    System  []string
    History []Message
    Tools   []ToolSchema
}

type RawChunk struct {
    Type     string  // "text" | "tool_call" | "usage" | "error" | "done"
    Text     string
    ToolCall *ToolCall
    Usage    *Usage
    Err      error
}
```

**Ollama 实现**（`internal/llm/ollama.go`）：NDJSON 流式 + `tools` 字段 + ctx 取消。

### 3.3 Toolcall Strategy

```go
type Strategy interface {
    Name() string
    Extract(resp *RawResponse) ([]ToolCall, error)
}
```

v0.1 只实现 **Native**（`internal/toolcall/native.go`）：解析 `message.tool_calls` 数组。
- `arguments` 是**字符串化的 JSON**，需要二次 `json.Unmarshal`
- Ollama 单 chunk 返回**完整对象**（非 delta），不需要累积

### 3.4 Bus

```go
const (
    EventPartDelta        = "part.delta"
    EventPartUpdated      = "part.updated"
    EventPermissionAsked  = "permission.asked"
    EventPermissionReply  = "permission.replied"
    EventAgentStateChange = "agent.state.change"
    EventError            = "error"
)

type Event struct {
    Type      string
    SessionID string
    Data      any
}
```

v0.1 简化：
- 64-buffered channels
- 慢消费者 **drop + warn**（不阻塞 LLM 流）
- 无 replay buffer
- 6 个事件类型

### 3.5 Session

```go
type Store interface {
    CreateSession(ctx, *Session) error
    Sessions(ctx, dir string) ([]*Session, error)
    Messages(ctx, sessionID string, limit int) ([]*Message, error)
    AppendMessage(ctx, sessionID string, msg *Message) error
}
```

v0.1 实现：`MemoryStore`（in-memory，append-only + snapshot 读）。v0.2 换 SQLite，**接口不变**。

## 4. Agent Loop 状态机

8 状态 + 7 转换 + 3 道重试熔断。详见 `internal/agent/loop.go`。

```
                ┌──────┐
                │ Idle │
                └──┬───┘
                   │ 收到 user 消息
                   ▼
              ┌────────┐
              │Building│  ← 构造 ChatRequest
              └───┬────┘
                  │ 调 LLM.Stream
                  ▼
            ┌──────────┐
            │Streaming │  ← 收 RawChunk
            └────┬─────┘
                 │ 收到 tool_call
                 ▼
            ┌──────────┐
            │ ToolExec │  ← Execute + 权限询问
            └────┬─────┘
                 │ 完成
                 ▼
            ┌──────────┐
            │Streaming │  ← 继续生成
            └────┬─────┘
                 │ 收到 done
                 ▼
              ┌──────┐
              │ Idle │  ← 写消息，等下一轮
              └──────┘

边角：Errored（任何错） / Stopped（ctx cancel） / WaitPerm（v1）
```

### 三道熔断

| 触发 | 默认 | 行为 |
|------|------|------|
| 同一 call 失败 N 次 | 3 | 跳过该 call，进 Errored |
| 连续 bash 失败 N 次 | 5 | 跳出 bash，进 Errored |
| 同一错误签名重复 N 次 | 3 | 跳出循环，进 Errored |

`Errored` 状态记到 session，UI 显示给用户。

## 5. Tool 实现

| Tool | 文件 | 测试 | 关键设计 |
|------|------|------|----------|
| read | `internal/tool/read.go` | 22 | 路径 normalize + JSON schema 验证 + 行号偏移 + ctx 取消 |
| edit | `internal/tool/edit.go` | 12 | 字符串替换 + 原子写 + 模糊匹配（old_text 缩进不一致也能改） |
| bash | `internal/tool/bash.go` | 16 | 30s 默认 timeout + 输出截断（头尾各半，50KB 总） + **非零退出仍 success**（让模型看 stderr 修复） |
| grep | `internal/tool/grep.go` | 17 | path/pattern/include/ignore_case/line_numbers/max_results；跳过重目录 + 二进制；输出 `path\tline:content` |
| glob | `internal/tool/glob.go` | 18 | 自实现 `*` `**` `?` `[abc]`（0 依赖）；type 过滤 file/dir/any |

所有 tool 的 `Execute()` 第一步都是路径 normalize：

```go
if !filepath.IsAbs(path) {
    path = filepath.Join(tc.Cwd, path)
}
path = filepath.Clean(path)
```

## 6. 自举开发模式

核心差异化：让模型用 AcornCode 写新 AcornCode 工具。

### 6.1 流程

```
人: "加 Grep 工具，参考 Read"
  ↓
模型 (用 AcornCode 自己):
  - Read(read.go)
  - Read(AGENTS.md)         ← 项目约定
  - Write(grep.go)
  - Write(grep_test.go)     ← ≥10 测试
  - Write(schemas/grep.json)
  - Bash("go build")        ← stderr 自然修复
  - Bash("go test")
  - Edit(AGENTS.md)         ← 更新"已实现"
  ↓
人: review 5 分钟，合并
```

### 6.2 关键支撑

1. **`AGENTS.md`** — 项目约定硬规则（中文注释 / 失败处理 / Tool 模板 / 已知坑）
2. **`PROJECT_STATE` 段在 README** — 当前阶段 / 已实现 / 下一步
3. **Tool 接口统一** — 模型学 1 个 example 就能写下一个
4. **错误回灌** — Bash 非零退出不返回 hard error，模型看 stderr 自助
5. **测试当文档** — 每个 tool ≥10 测试覆盖

## 7. v0.1 范围约束

| 不做 | 推到 | 原因 |
|------|------|------|
| Bubble Tea TUI | v0.2 | stdout REPL 够用 |
| SQLite 持久化 | v0.2 | 内存 store 够用 |
| Session-level allow | v0.3 | 简化 broker |
| Compaction | v1.0 | 长 session 才需要 |
| Grammar/Prompted 策略 | v1.0 | Native 跑通 |
| Anthropic/OpenAI | v1.0 | Ollama 跑通 |
| MCP / HTTP Server | v2+ | 不是核心 |
| Capability Probe | 不做 | Provider 自己声明 |

**v0.1 强约束**：0 第三方依赖（仅用 Go stdlib）。

## 8. 关键设计决策

> **v0.1 简化**：ADR 内容已并入本节 + README "Key Decisions" 段。
> 当决策数量 > 10 时，再独立 `docs/decisions/` 目录。

### D1 — Go 原生 + 0 依赖

- 0 第三方依赖，单二进制 ~10MB
- 模型学 1 个 stdlib 比学 10 个第三方库快
- v0.2 必要时再加（不强求"零依赖"）

### D2 — ToolCall 三策略（v0.1 只 Native）

| 策略 | 适用 | 复杂度 |
|------|------|--------|
| Native | Ollama/Anthropic/OpenAI 自带 | 低 |
| Grammar | llama.cpp GBNF | 中 |
| Prompted | 小模型兜底（`<tool_call>{...}</tool_call>` regex） | 高 |

v1.0 再加 Grammar / Prompted。

### D3 — v0.1 范围最小化

Tracer bullet ≠ 完整产品。2-3 周完成，81 测试全过，端到端可演示。
进入 v0.2 才重新评估。
