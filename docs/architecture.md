# AcornCode 架构（v1.1 完整版）

> 当前真实实现的架构。v1.2+ 推迟的内容（GBNF 完整版、MCP、分布式）不在此文档。

## 1. 一句话

本地小模型优先的 Go 编码 agent，能自举开发（让模型自己写新工具）。

**对比 opencode**：单二进制 / 4 第三方依赖 / 双 provider（Ollama + Anthropic）/ **三 toolcall 策略**（Native + Prompted + Grammar）/ Bubble Tea TUI / **HTTP/SSE API + Bearer 鉴权 + 多 session**。详见 [README.md §对比表](../README.md)。

## 2. 分层

```
┌─────────────────────────────────────────────────────┐
│  CLI (cmd/acorn/main.go)                            │  Bubble Tea TUI | HTTP/SSE
│  - TTY 检测（TUI 模式才有）                          │
│  - 5 flag: --provider / --toolcall / --server / --db / model
│  - 启 SQLite + Provider + 工具 + Broker + TUI/Server │
└────────────────────┬────────────────────────────────┘
                     │
┌────────────────────▼────────────────────────────────┐
│  Agent Loop (internal/agent)                        │  8 状态机 + 3 熔断
│  ├─ loop.go:        状态机 + LLM 调度 + Compaction  │
│  ├─ processor.go:   流式 chunk → part 状态          │
│  └─ helpers.go:     token 估算 / 消息转换 / system  │
└─┬──────┬──────┬──────┬──────┬──────┬─────────────────┘
  │      │      │      │      │      │
  ▼      ▼      ▼      ▼      ▼      ▼
 LLM   Tool   Perm   Sess   Bus   Instr
 (Ollama/Anthropic)  (6 tools)  (broker)  (SQLite)  (6 events)  (AGENTS.md)
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
    ListModels(ctx context.Context) ([]string, error)
}
```

`RawChunk` 类型：`"text"` / `"tool_call"` / `"finish"` / `"error"` / `"thinking"` / `"tool_call_delta"`。

**两个实现**（v1.0）：
- **Ollama**（`internal/llm/ollama.go`）：NDJSON 流式 + `tools` 字段 + ctx 取消
- **Anthropic**（`internal/llm/anthropic.go`，v1.0.2）：Anthropic Messages API + SSE + tool schema 转换（OpenAI function → Anthropic tools）

### 3.3 Toolcall Strategy

```go
type Strategy interface {
    Name() string
    Prepare(req *ChatRequest, tools []Definition) error
    ParseStream(ctx, raw <-chan RawChunk) <-chan StreamEvent
    RetryHint(failed FailedCall, tools []Definition) (asst, user Message)
}
```

**三个实现**（v1.1）：

| 策略 | 适用 | 文件 | 版本 |
|------|------|------|------|
| **Native** | Ollama/Anthropic/OpenAI 自带 | `native.go` | v0.1 |
| **Prompted** | 小模型（无原生 tool_call） | `prompted.go` | v1.0.5 |
| **Grammar** | 同 Prompted + 严格 JSON Schema 验证 | `grammar.go` | v1.0.6 |

### 3.4 Bus

```go
const (
    EventPartDelta        = "part.delta"          // 文本增量
    EventPartUpdated      = "part.updated"        // 工具 part 状态变化
    EventPermissionAsked  = "permission.asked"    // v1.0.1
    EventPermissionReply  = "permission.replied"  // v1.0.1
    EventAgentStateChange = "agent.state.change"  // 状态机切换
    EventError            = "error"               // 错误（含 fatal 标志）
)
```

v1.0 简化：
- 64-buffered channels
- 慢消费者 **drop + warn**（不阻塞 LLM 流）
- 无 replay buffer

**Consumer**（v1.0.1+）：
- `tui/model.go` 订阅 5 topic（`permission.asked` / `permission.replied` 加 v1.0.1）
- `server/server.go` 转发 4 topic → SSE 流

### 3.5 Session

```go
type Store interface {
    CreateSession / GetSession / ListSessions
    Messages / AppendMessage / SetFinishReason
    UpsertPart / GetPart
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
            └────┬───┘
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
             │ Idle │
             └──────┘

边角：Errored / Stopped / Compacting（v1.0.3）
```

### 三道熔断

| 触发 | 默认 | 行为 |
|------|------|------|
| 同一 call 失败 N 次 | 3 | 跳过该 call，进 Errored |
| 连续 bash 失败 N 次 | 5 | 跳出 bash，进 Errored |
| 同一错误签名重复 N 次 | 3 | 跳出循环，进 Errored |

### Compaction（v1.0.3）

`errNeedCompact` 触发 → `l.compactor.Compact(ctx, history)` → 摘要老消息保留最近 6 条 → 继续 turn。
当前实现：`SimpleCompactor`（调同 LLM 摘要），失败时返原 history 不阻断。

## 5. 6 个 Tool（v0.5+，v1.0 不变）

| Tool | 文件 | 测试 | 关键设计 |
|------|------|------|----------|
| read | `internal/tool/read.go` | 22 | 路径 normalize + JSON schema + 行号 + ctx |
| edit | `internal/tool/edit.go` | 12 | 字符串替换 + 原子写 + 模糊匹配 |
| bash | `internal/tool/bash.go` | 16 | 30s timeout + 截断 + 非零仍 success |
| grep | `internal/tool/grep.go` | 17 | path/pattern/include/ignore_case + 跳重目录 |
| glob | `internal/tool/glob.go` | 18 | 自实现 `*` `**` `?` `[abc]`（0 依赖） |
| webfetch | `internal/tool/webfetch.go` | 19 | HTTP + 30s + 1MB + **SSRF 禁私有 IP** |

所有 tool 第一步 normalize 路径：`tc.Cwd` base + `filepath.Clean`。

## 6. TUI（v0.4+，v1.0.1 多 topic）

Bubble Tea + Lipgloss。订阅 5 个 Bus topic：

| 事件 | 渲染 |
|------|------|
| `part.delta` | 中部正文追加 |
| `part.updated` (pending) | 状态栏 `→ tool` |
| `part.updated` (complete) | 状态栏 `✓ done` |
| `part.updated` (errored) | 状态栏 `✗ error: ...` |
| `agent.state.change` | 状态栏显示新 state |
| `permission.asked` | **弹窗**（v1.0.1）3 选项 |
| `error` (fatal) | 状态栏 `FATAL: ...` |

**布局**：状态栏 / 正文 / input box。**权限弹窗**（v1.0.1）：1/Allow / 2/Always / 3/Deny + 左/右循环。

**TTY 要求**：无 TTY → 清晰错误，CI 用 `--server=:8080`。

## 7. Session 存储（v0.5+ SQLite）

```sql
CREATE TABLE sessions (id TEXT PK, ..., created_at_ms, updated_at_ms);
CREATE TABLE messages (id TEXT PK, session_id, role, finish_reason, ...);
CREATE TABLE parts (id TEXT PK, message_id, session_id, type TEXT, data BLOB, ...);
```

关键：WAL + 单连接 + 毫秒精度 + `type` 列 + JSON BLOB。

## 8. HTTP/SSE Server（v1.1）

`./acorn --server=:8080 --api-key=secret` 启 HTTP。

### 端点

| 端点 | 方法 | 描述 | 鉴权 |
|------|------|------|------|
| `/healthz` | GET | 健康检查 | 否（k8s liveness） |
| `/v1/sessions` | POST | 创建 session | 是 |
| `/v1/sessions` | GET | 列出 session | 是 |
| `/v1/sessions/{id}` | GET | session 详情 | 是 |
| `/v1/sessions/{id}/chat` | POST | 续聊（SSE） | 是 |
| `/v1/chat` | POST | 自动建 session + SSE（向后兼容） | 是 |

### 鉴权（v1.1.1）

- 启用：`--api-key=KEY` 或 `ACORN_API_KEY` env
- 验证：`Authorization: Bearer <key>` 头
- 失败：401 + `WWW-Authenticate: Bearer` 头
- 用 `crypto/subtle.ConstantTimeCompare` 防 timing attack

### SSE 事件序列

`session` → `text` ×N → `part` ×N（tool）→ `state` → `finish`。

`Permission.Ask` 在 server 模式无 publisher → fallback allow（headless 无 TUI）。

## 9. Permission Broker（v1.0.1 真 ask）

```go
func (b *Broker) Ask(ctx, req) error {
    // 1. 匹配 rule
    //    allow → nil
    //    deny  → ErrDenied
    //    ask   → 发 permission.asked + 阻塞等 Reply（60s 超时默认 deny）
    // 2. 无 rule：检查 session allow list
    // 3. 默认 allow（v0.1 兼容）
}
```

`Publisher` 接口让 broker 发事件，**不依赖具体 bus**（避免 import 循环）。

## 10. 自举开发模式

核心差异化：让模型用 AcornCode 写新 AcornCode 工具。

```
人: "加 Grep 工具，参考 Read"
  ↓
模型 (用 AcornCode 自己):
  - Read(read.go) + Read(AGENTS.md)
  - Write(grep.go + grep_test.go + schemas/grep.json)
  - Bash("go build" + "go test")
  - Edit(AGENTS.md)
  ↓
人: review 5 分钟，合并
```

关键支撑：`AGENTS.md`（硬规则）/ 工具接口统一 / 错误回灌 / 测试当文档。

## 11. v1.1 完整命令

```
acorn [model]
  --provider=NAME        ollama | anthropic（默认 ollama）
  --toolcall=NAME        native | prompted | grammar（默认 native）
  --server=ADDR          启 HTTP server（如 ":8080"）
  --api-key=KEY          v1.1.1：HTTP Bearer 鉴权（也读 ACORN_API_KEY env）
  --db=path              SQLite 路径（默认 .acorncode.db）
```

## 12. 关键设计决策

### D1 — Go 原生 + 4 依赖（v1.0 平衡）

- 0 依赖（v0.1-v0.3）→ 4 依赖（v1.0）：bubbletea + lipgloss + sqlite + sqlx
- 单二进制 ~10MB
- 模型学 1 个 stdlib + 4 API 比学 10 个第三方库快

### D2 — ToolCall 三策略（v1.0 Native + Prompted，v1.0.6 Grammar）

| 策略 | 适用 | 复杂度 | 版本 |
|------|------|--------|------|
| Native | Ollama/Anthropic/OpenAI | 低 | v0.1 |
| Prompted | 小模型（无 tool_call） | 中 | v1.0.5 |
| Grammar | 同 Prompted + JSON Schema 验证 | 中 | v1.0.6 |

完整 GBNF（llama.cpp 集成）推迟到 v1.3（需 schema→GBNF 转换器，~500 行）。

### D3 — 渐进式交付

- v0.1 tracer bullet → v0.2 自举 → v0.3 配置化权限 → v0.4 TUI → v0.5 持久化
- v1.0 完整版（5 增量：Permission 弹窗 / Anthropic / Compaction / HTTP API / Prompted）
- v1.1 可上生产（HTTP 鉴权 + 多 session API）
- 每次 1-2 周，最小可用单元
- 始终保持 main 可跑 + 100% 测试

### D4 — 自我引导为核心

`AGENTS.md` / `README §当前状态` / 测试当文档 / 错误回灌 / Tool 接口统一 = 让模型能自举。

## 13. 依赖完整图（v1.0）

```
acorncode (Go 1.25)
├── github.com/charmbracelet/bubbletea v1.3.10  // TUI 框架
├── github.com/charmbracelet/lipgloss v1.1.0   // TUI 样式
├── github.com/jmoiron/sqlx v1.4.0              // SQL helper
└── modernc.org/sqlite v1.52.0                 // 纯 Go SQLite（无 CGo）
```

indirect deps：~20 个 small libs（term / ansi / cellbuf / golang.org/x/* 等）。
