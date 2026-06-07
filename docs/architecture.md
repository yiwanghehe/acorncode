# AcornCode 架构（v0.5 整合版）

> 当前真实实现的架构。Phase 2+ 推迟的内容（GBNF、Compaction、HTTP Server 等）不在此文档。

## 1. 一句话

本地小模型优先的 Go 编码 agent，能自举开发（让模型自己写新工具）。

**对比 opencode**：单二进制 / 4 第三方依赖（bubbletea / lipgloss / sqlite / sqlx）/ Ollama + Native toolcall / Bubble Tea TUI。详见 [README.md §对比表](../README.md)。

## 2. 分层

```
┌─────────────────────────────────────────────────────┐
│  CLI (cmd/acorn/main.go)                            │  Bubble Tea TUI
│  - TTY 检测（无 TTY → 清晰错误）                     │
│  - 启动 SQLite + Ollama + 工具 + Broker + TUI       │
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
 (Ollama)  (read/edit/bash/grep/glob/webfetch)  (broker)  (SQLite)  (6 events)  (AGENTS.md)
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
    Stream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}

type ChatRequest struct {
    Model   Model
    System  []string
    History []Message
    Tools   []ToolSchema
}
```

`StreamEvent` 是 sealed interface（`TextDelta` / `ToolCallStart` / `ToolCallDelta` / `ToolCallEnd` / `ReasoningDelta` / `FinishEvent`）。

**Ollama 实现**（`internal/llm/ollama.go`）：NDJSON 流式 + `tools` 字段 + ctx 取消。

### 3.3 Toolcall Strategy

```go
type Strategy interface {
    Name() string
    Extract(resp *RawResponse) ([]ToolCall, error)
}
```

v0.5 只实现 **Native**（`internal/toolcall/native.go`）：解析 `message.tool_calls` 数组。
- `arguments` 是**字符串化的 JSON**，需要二次 `json.Unmarshal`
- Ollama 单 chunk 返回**完整对象**（非 delta），不需要累积

### 3.4 Bus

```go
const (
    EventPartDelta        = "part.delta"         // 文本增量
    EventPartUpdated      = "part.updated"       // 工具 part 状态变化
    EventPermissionAsked  = "permission.asked"   // v1.0
    EventPermissionReply  = "permission.replied" // v1.0
    EventAgentStateChange = "agent.state.change" // 状态机切换
    EventError            = "error"              // 错误（含 fatal 标志）
)
```

**v0.5 简化**：
- 64-buffered channels
- 慢消费者 **drop + warn**（不阻塞 LLM 流）
- 无 replay buffer
- 6 个事件类型

**Producer**：`agent/loop.go` + `agent/processor.go` 在状态变化时发。
**Consumer**：`tui/model.go` 订阅 `part.delta` / `part.updated` / `agent.state.change` / `error`（4 个 topic，2 个 permission event 留 v1.0）。

### 3.5 Session

```go
type Store interface {
    CreateSession(ctx, *Session) error
    GetSession(ctx, id) (*Session, error)
    ListSessions(ctx) ([]*Session, error)
    Messages(ctx, sessionID, limit) ([]*Message, error)
    AppendMessage(ctx, *Message) error
    SetFinishReason(ctx, messageID, reason) error
    UpsertPart(ctx, Part) error
    GetPart(ctx, id) (Part, error)
}
```

**v0.5+ 默认**：`SQLiteStore`（WAL + 单连接 + 毫秒精度）。`MemoryStore` 保留供测试。

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

边角：Errored（任何错） / Stopped（ctx cancel） / WaitPerm（v1.0）
```

### 三道熔断

| 触发 | 默认 | 行为 |
|------|------|------|
| 同一 call 失败 N 次 | 3 | 跳过该 call，进 Errored |
| 连续 bash 失败 N 次 | 5 | 跳出 bash，进 Errored |
| 同一错误签名重复 N 次 | 3 | 跳出循环，进 Errored |

`Errored` 状态记到 session，UI 显示给用户。

## 5. Tool 实现（v0.5 6 个）

| Tool | 文件 | 测试 | 关键设计 |
|------|------|------|----------|
| read | `internal/tool/read.go` | 22 | 路径 normalize + JSON schema 验证 + 行号偏移 + ctx 取消 |
| edit | `internal/tool/edit.go` | 12 | 字符串替换 + 原子写 + 模糊匹配（old_text 缩进不一致也能改） |
| bash | `internal/tool/bash.go` | 16 | 30s 默认 timeout + 输出截断（头尾各半，50KB 总） + **非零退出仍 success**（让模型看 stderr 修复） |
| grep | `internal/tool/grep.go` | 17 | path/pattern/include/ignore_case/line_numbers/max_results；跳过重目录 + 二进制；输出 `path\tline:content` |
| glob | `internal/tool/glob.go` | 18 | 自实现 `*` `**` `?` `[abc]`（0 依赖）；type 过滤 file/dir/any |
| webfetch | `internal/tool/webfetch.go` | 19 | HTTP/HTTPS + 30s timeout + 1MB body + **SSRF 禁私有 IP** + 重定向 5 次限制 + 自定义 headers |

所有 tool 的 `Execute()` 第一步都是路径 normalize：

```go
if !filepath.IsAbs(path) {
    path = filepath.Join(tc.Cwd, path)
}
path = filepath.Clean(path)
```

## 6. TUI（v0.4+，v0.6 完善）

Bubble Tea + Lipgloss。**v0.6 起订阅 4 个 Bus topic**：

| 事件 | 渲染效果 |
|------|---------|
| `part.delta` | 中部正文追加文本 |
| `part.updated` (tool pending) | 状态栏 `→ tool_name` |
| `part.updated` (tool complete) | 状态栏 `✓ tool done` |
| `part.updated` (tool errored) | 状态栏 `✗ tool error: ...` |
| `agent.state.change` | 状态栏直接显示新 state |
| `error` (fatal) | 状态栏 `FATAL: ...` |

**布局**：
- 顶部状态栏：`[model] status`
- 中部正文：当前 turn 累积文本
- 底部 input box：`> ` + 用户输入

**快捷键**：Ctrl+C / Esc 退出；Enter 发送；Backspace 删除

**命令**：`/exit` `/quit` `/clear` `/session` `/help`

**TTY 要求**（v0.6 整合）：无 TTY 时返清晰错误，CI 场景需 v1.0 HTTP API。

## 7. Session 存储（v0.5+）

`SQLiteStore`（v0.5 起默认）实现：

```sql
CREATE TABLE sessions (id TEXT PK, parent_id, title, directory, agent, created_at_ms, updated_at_ms);
CREATE TABLE messages (id TEXT PK, session_id, role, finish_reason, created_at_ms, updated_at_ms);
CREATE TABLE parts (id TEXT PK, message_id, session_id, type TEXT, data BLOB, created_at_ms);
```

关键：
- WAL + 单连接（写多读少）
- 时间戳毫秒精度
- Part 用 `type` 列 + JSON BLOB 存 data
- `MemoryStore` 仍保留供测试
- 默认 db 文件：`.acorncode.db`（`--db=path` 改）

## 8. 自举开发模式

核心差异化：让模型用 AcornCode 写新 AcornCode 工具。

### 8.1 流程

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

### 8.2 关键支撑

1. **`AGENTS.md`** — 项目约定硬规则（中文注释 / 失败处理 / Tool 模板 / 已知坑）
2. **README.md §当前状态 / §已实现** — 当前阶段 + 已实现模块
3. **Tool 接口统一** — 模型学 1 个 example 就能写下一个
4. **错误回灌** — Bash 非零退出不返回 hard error，模型看 stderr 自助
5. **测试当文档** — 每个 tool ≥10 测试覆盖

## 9. v0.5 范围约束

| 不做 | 推到 | 原因 |
|------|------|------|
| Permission ask 弹窗 | v1.0 | 简化 TUI，ask 默认 allow + log |
| Compaction | v1.0 | 长 session 才需要 |
| Grammar/Prompted 策略 | v1.0 | Native 跑通 |
| Anthropic/OpenAI | v1.0 | Ollama 跑通 |
| HTTP/SSE Server | v1.0 | CLI 优先 |
| MCP stdio Client | v2+ | 不是核心 |
| Capability Probe | 不做 | Provider 自己声明 |

**v0.5 实际依赖**（4 个）：bubbletea / lipgloss / modernc-sqlite / sqlx。

## 10. 关键设计决策

### D1 — Go 原生 + 最小依赖

- 0 → 4 第三方依赖：v0.1-v0.3 纯 stdlib，v0.4 起加 TUI，v0.5 加 SQLite
- 单二进制 ~10MB（无 CGo）
- 模型学 1 个 stdlib + 4 个 API 比学 10 个第三方库快

### D2 — ToolCall 三策略（v0.5 只 Native）

| 策略 | 适用 | 复杂度 |
|------|------|--------|
| Native | Ollama/Anthropic/OpenAI 自带 | 低（已实现） |
| Grammar | llama.cpp GBNF | 中 |
| Prompted | 小模型兜底（`<tool_call>{...}</tool_call>` regex） | 高 |

v1.0 再加 Grammar / Prompted。

### D3 — v0.1 范围最小化

Tracer bullet ≠ 完整产品。2-3 周完成，81 测试全过，端到端可演示。
进入 v0.2 才重新评估。v0.5 走通后进入 v1.0。

### D4 — 自我引导为差异化核心

AcornCode 的核心价值 = 让模型自己写新工具迭代。`AGENTS.md` / `README.md §当前状态` / 测试作为文档 / 错误回灌 / 工具接口统一 = 关键支撑。

## 11. 依赖完整图

```
acorncode (Go 1.25)
├── github.com/charmbracelet/bubbletea v1.3.10  // TUI 框架
├── github.com/charmbracelet/lipgloss v1.1.0   // TUI 样式
├── github.com/jmoiron/sqlx v1.4.0              // SQL helper
└── modernc.org/sqlite v1.52.0                 // 纯 Go SQLite（无 CGo）
```

indirect deps：bubbletea/lipgloss 拉 ~15 个 small libs（term / ansi / cellbuf / 等）。
