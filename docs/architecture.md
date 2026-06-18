# AcornCode 架构（v1.10 完整版）

> 当前真实实现的架构。涵盖 v1.0 完整版 + v1.1 HTTP 可用 + v1.2 MCP + v1.3 GBNF +
> v1.4~v1.7 工具调用约束端到端打通 + v1.10 Compaction 持久化闭环 + tokenizer。v2+ 推迟内容（分布式）不在此文档。

## 1. 一句话

本地小模型优先的 Go 编码 agent，能自举开发（让模型自己写新工具）。

**对比 opencode**：单二进制 / 4 第三方依赖 / 双 provider（Ollama + Anthropic）/ **三 toolcall 策略**（Native + Prompted + Grammar）/ Bubble Tea TUI / **HTTP/SSE API + Bearer 鉴权 + 多 session**。详见 [README.md §对比表](../README.md)。

## 2. 分层

```
┌─────────────────────────────────────────────────────┐
│  CLI (cmd/acorn/main.go)                            │  Bubble Tea TUI | HTTP/SSE
│  - TTY 检测（TUI 模式才有）                          │
│  - 7 flag: --provider / --toolcall / --server / --api-key / --db / --force-tool / model
│  - 启 SQLite + Provider + 工具 + MCP + Broker + TUI/Server │
└────────────────────┬────────────────────────────────┘
                     │
┌────────────────────▼────────────────────────────────┐
│  Agent Loop (internal/agent)                        │  8 状态机 + 3 熔断
│  ├─ loop.go:        状态机 + LLM 调度 + Compaction  │
│  ├─ processor.go:   流式 chunk → part 状态          │
│  └─ helpers.go:     token 估算 / 消息转换 / system  │
└─┬──────┬──────┬──────┬──────┬──────┬──────┬──────────┘
  │      │      │      │      │      │      │
  ▼      ▼      ▼      ▼      ▼      ▼      ▼
 LLM   Tool   Perm   Sess   Bus   Instr  MCP
 (Ollama/Anthropic)  (6 tools + MCP 外部)  (broker)  (SQLite)  (6 events)  (AGENTS.md)  (stdio client)
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

**ChatRequest 约束字段**（v1.4/v1.5）：
- `Format`（JSON Schema，v1.4）：结构化输出约束。Ollama 转发到 `format` 字段，解码期强制 JSON
- `ToolChoice`（provider 无关，v1.5）：`""`/`"auto"` 默认 | `"any"` 强制某工具 | `"<name>"` 强制指定工具。Anthropic 映射为 `tool_choice`

**两个实现**（v1.0）：
- **Ollama**（`internal/llm/ollama.go`）：NDJSON 流式 + `tools` 字段 + `format` 约束 + ctx 取消
- **Anthropic**（`internal/llm/anthropic.go`，v1.0.2）：Messages API + SSE + tool schema 转换 + `tool_choice` 强制（v1.5）

### 3.3 Toolcall Strategy

```go
type Strategy interface {
    Name() string
    Prepare(req *ChatRequest, tools []Definition) error
    ParseStream(ctx, raw <-chan RawChunk) <-chan StreamEvent
}
```

> **v1.12 减法**：移除从未接线的 `RetryHint` 方法（及 `FailedCall`/`buildRetryHint`）。
> 工具调用失败的恢复实际由 Agent Loop 的 `circuitBreaker`（三道熔断）+ `errTurnAborted`
> 重试路径承担，错误以 stderr 回灌模型，不依赖策略层回放。

> **关键接线**（v1.4 修复）：`agent.Loop.buildRequest` 末尾必须调 `strategy.Prepare(req, tools)`，
> 否则 Prompted/Grammar 注入的 system 说明与 GBNF 约束全部失效（曾长期未接线）。

**三个实现**：

| 策略 | 适用 | 文件 | 版本 |
|------|------|------|------|
| **Native** | Ollama/Anthropic/OpenAI 自带 | `native.go` | v0.1 |
| **Prompted** | 小模型（无原生 tool_call） | `prompted.go` | v1.0.5 |
| **Grammar** | 同 Prompted + GBNF 约束 + JSON Schema 验证 | `grammar.go` + `gbnf.go` | v1.0.6 / v1.3 |

**Grammar 约束链**（v1.3~v1.7）：
- `gbnf.go`：`SchemaToGBNF` 把 JSON Schema 递归转成 GBNF 语法（object/array/string/number/enum），降级安全、深度上限 32
- `Grammar.ForceToolCall`（默认 false）：开启后 `Prepare` 构造「工具调用 wrapper」schema 并同时设
  `req.Format`（Ollama）+ `req.ToolChoice="any"`（Anthropic），两端统一强制工具调用
- 暴露入口：CLI `--force-tool`（v1.6）/ HTTP 请求级 `force_tool`（v1.7，每请求独立 Grammar 实例）

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
    ReplaceMessages   // v1.10：Compaction 原子写回压缩结果
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

### Compaction（v1.0.3 摘要；v1.10 持久化闭环）

`errNeedCompact` 触发 → `l.compactor.Compact(ctx, history)` → 摘要老消息保留最近 N 条 → 继续 turn。
当前实现：`SimpleCompactor`（调同 LLM 摘要），失败时返原 history 不阻断。

**v1.10 持久化闭环**：压缩结果不再只打日志，而是经 `store.ReplaceMessages(sessionID, msgs)`
**原子写回**——`rebuildMessages` 把 `[]llm.Message` 重建为 `[]*session.Message`（每条 1 个 TextPart），
SQLite 用事务（DELETE 旧 + INSERT 新，整体回滚）替换。后续 turn 的 `buildRequest` 读到的就是
压缩后的短历史，真正释放 token 预算。摘要以 `system` 角色存历史，`toModelMessages` 增 system 分支
保证不丢。保守策略：无收益跳过写回、出错保留原消息。

### Token 估算（v1.10 tokenizer）

`agent.estimateTokens` 决定何时触发 compact。v1.10 起改用 `internal/tokenizer.Count`（纯 stdlib
启发式：ASCII 4 字符/token、数字 3 位/token、CJK 每字 1 token、标点 1 token、emoji 2 token），
替换旧的 `len/4`（中文场景严重低估），并补算工具调用 args/output 与 JSON Schema 的 token。

## 5. 6 个 Tool（v0.5+，v1.0 不变）

| Tool | 文件 | 测试 | 关键设计 |
|------|------|------|----------|
| read | `internal/tool/read.go` | 22 | 路径 normalize + JSON schema + 行号 + ctx |
| edit | `internal/tool/edit.go` | 12 | 字符串替换 + 原子写 + 模糊匹配 |
| bash | `internal/tool/bash.go` | 16 | 30s timeout + 截断 + 非零仍 success |
| grep | `internal/tool/grep.go` | 17 | path/pattern/include/ignore_case + 跳重目录 |
| glob | `internal/tool/glob.go` | 18 | 自实现 `*` `**` `?` `[abc]`（0 依赖） |
| webfetch | `internal/tool/webfetch.go` | 23 | HTTP + 30s + 1MB + **SSRF 禁私有 IP + 域名解析校验**（v1.11） |

所有 tool 第一步 normalize 路径：`tc.Cwd` base + `filepath.Clean`。

**工具裁剪**（v1.11）：`Registry.PickForTurn(agent, budget, userMsg, recent)` 按相关性打分
取 top-budget 暴露给模型——关键词命中 +10 / ID 字面 +8 / 最近调用 +5..+1 / 核心工具
（read/bash）基础分 +2 兜底；budget<=0 或工具数≤budget 时返回全部。让 `MaxTools` 真正生效，
减少小模型 prompt 膨胀。

**Read UTF-8 安全截断**（v1.11）：超 `maxBytes` 时 `truncateUTF8` 回退到最近字符边界，
不切碎中文/emoji。整文件与 offset/limit 两条路径均接入。

## 5.5 MCP 外部工具（v1.2）

`internal/mcp` 让 AcornCode 启动外部 MCP server 子进程，把它们的工具自动注册进 agent。

```
acorncode.json mcpServers → SetupFromConfigs → 每个 server 一个 stdio Client
  Client: 启子进程 → initialize 握手 → tools/list → 包成 tool.Tool 注册进 Registry
  调用:   tools/call（JSON-RPC 2.0 over stdio）
```

| 文件 | 职责 |
|------|------|
| `client.go` | stdio JSON-RPC 2.0 客户端：握手 / 请求-响应分发（pending map）/ ctx 取消 / 优雅关闭（2s 强杀） |
| `adapter.go` | MCP 工具包成 `tool.Tool`，ID 加 server 名前缀（`fs_read_file`），统一走 Permission `ask` |
| `config.go` | 从 `acorncode.json` 的 `mcpServers` 段读取（兼容主流 MCP 客户端），支持 `disabled` |
| `manager.go` | `SetupFromConfigs` 批量启动；单 server 失败记日志跳过，不致命 |

关键容错：坏 JSON 行跳过（`slog.Warn`）/ server 退出 EOF 时 `failAllPending` 唤醒所有等待者 / 单 server 失败不拖垮全部。测试用「re-exec 自身」作 mock stdio server，无外部依赖（15 测试）。

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

## 11. v1.7 完整命令

```
acorn [model]
  --provider=NAME        ollama | anthropic（默认 ollama）
  --toolcall=NAME        native | prompted | grammar（默认 native）
  --server=ADDR          启 HTTP server（如 ":8080"）
  --api-key=KEY          v1.1.1：HTTP Bearer 鉴权（也读 ACORN_API_KEY env）
  --force-tool           v1.6：强制工具调用（仅 grammar；Ollama format + Anthropic tool_choice）
  --db=path              SQLite 路径（默认 .acorncode.db）
```

HTTP 请求级 `force_tool`（v1.7）：`POST /v1/chat` body 加 `{"force_tool": true}`，每请求独立生效。

## 12. 关键设计决策

### D1 — Go 原生 + 极少依赖（v1.0 平衡）

- 0 依赖（v0.1-v0.3）→ 4 依赖（v1.0）→ **3 依赖**（v1.8 移除 sqlx，session 改用 `database/sql`）
- 单二进制 ~10MB
- 模型学 1 个 stdlib + 3 API 比学 10 个第三方库快

### D2 — ToolCall 三策略（v1.0 Native + Prompted，v1.0.6 Grammar，v1.3 GBNF）

| 策略 | 适用 | 复杂度 | 版本 |
|------|------|--------|------|
| Native | Ollama/Anthropic/OpenAI | 低 | v0.1 |
| Prompted | 小模型（无 tool_call） | 中 | v1.0.5 |
| Grammar | 同 Prompted + GBNF 约束 + JSON Schema 验证 | 中 | v1.0.6 / v1.3 |

完整 GBNF（schema→GBNF 转换器 `gbnf.go`，~260 行）于 v1.3 落地，0 新依赖。
v1.4~v1.7 把约束端到端打通：Prepare 接线修复 + Ollama `format` + Anthropic `tool_choice` +
`--force-tool` CLI flag + HTTP 请求级 `force_tool`。

### D3 — 渐进式交付

- v0.1 tracer bullet → v0.2 自举 → v0.3 配置化权限 → v0.4 TUI → v0.5 持久化
- v1.0 完整版（5 增量：Permission 弹窗 / Anthropic / Compaction / HTTP API / Prompted）
- v1.1 可上生产（HTTP 鉴权 + 多 session API）
- v1.2 MCP → v1.3 GBNF → v1.4~v1.7 工具调用约束端到端打通
- 每次 1-2 周，最小可用单元
- 始终保持 main 可跑 + 100% 测试

### D4 — 自我引导为核心

`AGENTS.md` / `README §当前状态` / 测试当文档 / 错误回灌 / Tool 接口统一 = 让模型能自举。

## 13. 依赖完整图（v1.0）

```
acorncode (Go 1.25)
├── github.com/charmbracelet/bubbletea v1.3.10  // TUI 框架
├── github.com/charmbracelet/lipgloss v1.1.0   // TUI 样式
└── modernc.org/sqlite v1.52.0                 // 纯 Go SQLite（无 CGo）
```

> v1.8 移除 `jmoiron/sqlx`，session 存储改用标准库 `database/sql`（3 个核心第三方依赖）。
> `golang.org/x/term`（TTY 检测）按项目口径视作 stdlib 扩展，不计入第三方。

indirect deps：sqlite 编译工具链 + TUI term/ansi/cellbuf 等 small libs。
