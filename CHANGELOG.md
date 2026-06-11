# Changelog

格式：[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。版本遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [1.10.0] - 2026-06

### Compaction 持久化闭环 + 真实 tokenizer 估算

补齐两个长期标注「留待后续」的 P0 缺口，**0 新第三方依赖**（纯 stdlib），测试全绿。

**Compaction 持久化（闭合 v1.0.3 留的坑）**：

- 之前 `agent.Loop.compact()` 调 compactor 拿到摘要后**只打日志、不写回**，长 session
  压缩等于无效。现在压缩结果会**原子写回 store**，后续 turn 读到的就是压缩后的短历史，
  真正释放 token 预算。
- **`SessionStore.ReplaceMessages(ctx, sessionID, msgs)`**：新增接口方法，原子替换某 session
  的全部消息（含 parts）。`SQLiteStore` 用**事务**实现（先 `DELETE` 旧 messages/parts 再按序
  `INSERT`，失败整体回滚，避免半压缩状态）；`MemoryStore` 持写锁实现等价语义。
- **`rebuildMessages`**：把 compactor 返回的 `[]llm.Message` 重建为可持久化的
  `[]*session.Message`（每条一个 TextPart，ID 走 `internal/id`）。
- **`toModelMessages` 支持 `system` 角色**：压缩写回的摘要以 system 消息存在历史里，
  之前 `toModelMessages` 只处理 user/assistant 会把摘要丢掉，现已修复。
- 保守策略：压缩无收益（数量未减）跳过写回；compactor 出错或写回失败均保留原消息、不阻断当前 turn。

**真实 tokenizer（替换 len/4 粗估）**：

- 新增 **`internal/tokenizer`** 包（纯 stdlib）：启发式逼近主流 BPE 分词器（GPT/Qwen/Llama）
  的统计规律——ASCII 单词 4 字符/token、数字 3 位/token、CJK 每字 1 token、标点各 1 token、
  emoji 等 2 token。把中文场景从 `len/4` 的严重低估收敛到约 ±15%。
- **`agent.estimateTokens` 接入 tokenizer**，并补算**工具调用的 args/output 与工具 JSON Schema**
  的 token（之前完全漏算，导致 compact 触发偏晚）。

**测试**：tokenizer +11、session ReplaceMessages +4、agent compact +4（**约 389 运行单元**，全绿）。

## [1.9.0] - 2026-06

### 代码结构重构（单一职责 / 高内聚低耦合）

全面梳理代码结构，按单一职责原则拆解职责过重的函数、消除重复，**0 行为变化、0 新依赖**，
每步独立提交且测试全绿。

**8 项重构（R1–R8）**：

- **R1 拆解 `server.handleChat` 上帝函数**（~85 行/7 职责 → 8 个单一职责函数）：
  `decodeChatRequest` / `setupSSE` / `resolveSessionID` / `ensureSession` /
  `appendUserMessage` / `newChatLoop` / `runChatLoop` / `serveChatStream`
- **R2 统一 ID 生成**：新增零依赖 `internal/id` 包（`New`/`Short`），消除 agent/server/
  permission/cmd 中 4 份重复的 base36 时间戳实现；counter 防同纳秒冲突
- **R3 抽出 `circuitBreaker`**：三道熔断（同 call 重试 / bash 连续失败 / 同错误签名）的
  计数与判定从 `Loop` 剥离到 `internal/agent/circuit.go`，Loop 专注状态机调度
- **R4 提取 `sessionToInfo`**：消除 create/list/get 三处 `SessionInfo` 重复映射
- **R5 提取 `buildRetryHint`**：native/prompted/grammar 三策略同构的 RetryHint 共用结构，仅传文案
- **R6 去除 `handleChatByID` 的 body hack**：不再 marshal 回 `r.Body`，解析后直接调 `serveChatStream(req)`
- **R7 提取 `emitter` helper**：封装 ctx-aware channel 发送，统一 native/prompted/grammar
  中重复的 `emit`/`emitText` 闭包与内联 select
- **R8 清理 `subscribeAndForward` 空 teardown**：返回的 teardown 是空操作，改为不返回，
  goroutine 靠 ctx 取消 / bus 关闭退出

**测试**：id +5、circuit +7（**369 测试**，含子测试，全绿）。

## [1.8.0] - 2026-06

### 移除 sqlx 依赖（做减法）+ 文档同步

把核心第三方依赖从 4 个降到 3 个，并把漂移的文档全部对齐到当前实现。

**减法**：

- **移除 `github.com/jmoiron/sqlx`**：`SQLiteStore` 改用标准库 `database/sql`
  - `sqlx.Open` → `sql.Open`；`db *sqlx.DB` → `db *sql.DB`
  - `GetContext`+struct → `QueryRowContext().Scan(字段...)`（GetSession / GetPart）
  - `QueryxContext`+`StructScan` → `QueryContext`+`Scan`（ListSessions / Messages）
  - 行为零变化，session 包 19 测试全过；显式列名替代 `SELECT *`，更稳
- 核心第三方依赖 **4 → 3**（bubbletea / lipgloss / modernc-sqlite），贴合「1 stdlib > 10 库」哲学

**文档同步**（自举项目命脉）：

- `AGENTS.md`：v1.6/293 → v1.7/298，补 v1.7 并发隔离坑（#18）、依赖边界
- `docs/architecture.md`：整篇从 v1.1 → v1.7，补 MCP §5.5 / GBNF / force-tool / Format+ToolChoice 契约 / 依赖图
- `README.md`：标题与「已实现/当前状态」→ v1.7，对比表与总计 → 3 依赖
- `CHANGELOG.md`：清理已完成的 v1.7 计划段

**测试**：298（无新增，纯减法 + 文档）。

## [1.7.0] - 2026-06

### HTTP server 请求级 force_tool

把强制工具调用能力带到 HTTP API：每个请求可独立决定是否强制。

**新增**：

- **`force_tool` 请求字段**：`POST /v1/chat` 与 `/v1/sessions/{id}/chat` 的 body 支持
  `{"force_tool": true}`，本次请求强制工具调用（仅 server 以 grammar 策略启动时生效）
- **请求级隔离**：`strategyForRequest` 在 force 时返回**独立的** Grammar 实例
  （ForceToolCall=true），不修改共享 `cfg.Strategy`，并发请求互不干扰
- 非 grammar 策略带 force_tool 时记日志忽略，退回共享策略

**测试**：server +5（无 force / grammar force / 非 grammar 忽略 / prompted 忽略 / 并发隔离）。

**总计**：298 测试。

## [1.6.0] - 2026-06

### --force-tool CLI flag（暴露强制工具调用）

把 v1.4/v1.5 的 `Grammar.ForceToolCall` 能力暴露给终端用户。

**新增**：

- **`--force-tool` flag**：开启后 grammar 策略强制工具调用
  （Ollama 走 `format` 约束 + Anthropic 走 `tool_choice=any`）。
  仅对 `--toolcall=grammar` 生效，其余策略给出忽略提示
- **`parseArgs` 重构为 `cliArgs` struct**：替代 6 个位置返回值，加字段不再破坏调用方，
  更易扩展（这也修复了「位置返回值随版本增长越来越脆」的可维护性问题）

**测试**：cmd +2（--force-tool 解析 / 默认关闭）；TestParseArgs 改造为 struct 断言。

**总计**：293 测试。

## [1.5.0] - 2026-06

### Anthropic 结构化输出（tool_choice 强制）

让 Anthropic 的强制工具调用对齐 v1.4 的 Ollama `format` 约束，两个 provider 行为统一。

**新增**：

- **`ChatRequest.ToolChoice`**（provider 无关）：工具调用强制策略
  - `""`/`"auto"` → 默认（模型自行决定）
  - `"any"` → 强制调用某个工具
  - `"<name>"` → 强制调用指定工具
- **Anthropic 映射 `tool_choice`**：`buildAnthropicToolChoice` 把 ToolChoice 转成
  Anthropic 的 `{"type":"any"}` / `{"type":"tool","name":...}`；默认不发（等价 auto）
- **Grammar.ForceToolCall 对齐两端**：开启后同时设 `req.Format`（Ollama 约束）
  和 `req.ToolChoice="any"`（Anthropic 强制），让两个 provider 都强制工具调用

**测试**：anthropic +4（tool_choice any/named/default + 映射单测）。

**总计**：291 测试。

## [1.4.0] - 2026-06

### Provider 约束生成 + 修复 strategy.Prepare 接线

让 v1.0.5/v1.0.6/v1.3 的 toolcall 策略真正端到端生效。

**修复（关键）**：

- **strategy.Prepare 从未被调用**：`agent.Loop.buildRequest` 之前直接构造 `ChatRequest`
  却没调 `l.strategy.Prepare()`，导致 Prompted/Grammar 策略注入的 system 工具说明、
  v1.3 的 GBNF 生成**全部没生效**。现在 `buildRequest` 末尾调用 `Prepare(req, pickedTools)`，
  Native 策略为 no-op，向后兼容。

**新增**：

- **`ChatRequest.Format`**（JSON Schema）：结构化输出约束字段，为空时不约束
- **Ollama 转发 `format`**：`ollamaRequest` 加 `format,omitempty`，把 `req.Format`
  转发给 Ollama 的结构化输出能力，在解码阶段强制 JSON 格式
- **`Grammar.ForceToolCall`**（默认 false）：开启后 `Prepare` 自动构造「工具调用 wrapper」
  JSON Schema（`{name: enum<tool IDs>, arguments: object}`）并设置 `req.Format`，
  强制模型输出合法工具调用。默认关闭保持自由文本 + 工具混合输出，向后兼容。

**测试**：grammar +2（ForceToolCall/NoForce）、ollama +2（Format 转发/省略）。

**总计**：287 测试。

## [1.3.0] - 2026-06

### Grammar GBNF 完整版（schema→GBNF 约束生成）

把 Grammar 策略从「事后 JSON Schema 验证」升级到「解码期约束生成」：
新增 `internal/toolcall/gbnf.go` 转换器，把 JSON Schema 转成 GBNF（GGML BNF）语法，
让支持 GBNF 的后端（llama.cpp / Ollama）在生成阶段就强制输出符合 schema 的 JSON。
**0 新第三方依赖**（纯 stdlib）。

**新增**：

- **schema→GBNF 转换器**（`gbnf.go`）：`SchemaToGBNF(schema)` 递归转换，支持
  object（properties/required/可选属性）、array（items/minItems）、
  string/number/integer/boolean/null、enum（字面量交替）；附完整 JSON 原语规则
  （value/string/char/number/ws 等），输出自洽可解析、稳定可测（属性排序）
- **降级策略**：不支持的结构（$ref/oneOf/pattern/数值范围等）降级为「任意 JSON 值」规则，
  坏 schema 不报错、不 panic、不阻断生成
- **递归深度上限**（32）防恶意/超深 schema
- **Grammar 策略接入**（`grammar.go`）：`Prepare` 为每个工具生成 GBNF 并缓存，
  注入更严格的 system 引导；新增 `Grammars()` 暴露 tool ID → GBNF 供 provider 约束

**测试**：gbnf 19 测试 + grammar 新增 2 测试。

**总计**：283 测试。

## [1.2.0] - 2026-06

### MCP stdio Client（让模型调外部工具）

新增 `internal/mcp` 包：通过 stdio + JSON-RPC 2.0 调用外部 MCP server，
把它们的工具自动注册进 agent。**0 新第三方依赖**（纯 stdlib）。

**新增**：

- **MCP stdio client**（`client.go`）：启动 server 子进程，`initialize` 握手 →
  `tools/list` → `tools/call`，并发安全的请求/响应分发（pending map + per-request channel），
  支持 ctx 取消、超时、坏 JSON 行跳过、优雅关闭（2s 后强杀）
- **Tool adapter**（`adapter.go`）：把 MCP 工具包成 `tool.Tool`，ID 加 server 名前缀
  （`fs_read_file`），统一走 Permission `ask`
- **配置加载**（`config.go`）：从 `acorncode.json` 的 `mcpServers` 段读取（格式兼容主流 MCP 客户端），
  支持 `disabled` 字段
- **多 server 管理**（`manager.go`）：`SetupFromConfigs` 批量启动；单个 server 失败不致命（记日志跳过）
- CLI 接线：启动时自动加载 MCP server，退出时统一关闭

**测试**：mcp 包 15 测试（re-exec 自身作 mock stdio server，无外部依赖）。

**总计**：262 测试。

## [1.1.0] - 2026-06

### HTTP 鉴权 + 多 session API

v1.0 完整版上加 2 个能力，让 server 模式可上生产。

**新增**：

- **v1.1.1 HTTP Bearer 鉴权**：`--api-key=KEY` / `ACORN_API_KEY` env；设了就要 `Authorization: Bearer <key>`，否则 401。`/healthz` 永远开放（k8s liveness）
- **v1.1.2 Multi-session API**：
  - `POST /v1/sessions` → 创建（返 session_id）
  - `GET /v1/sessions` → 列表
  - `GET /v1/sessions/{id}` → 详情
  - `POST /v1/sessions/{id}/chat` → 续聊（多轮对话）
- **v1.1.3 Bash 工具跨平台**：
  - `resolveShell()` 按平台选 shell：Unix 用 `sh -c`；Windows 优先 POSIX shell（Git Bash/WSL），无则回退 `cmd /c`（探测结果用 `sync.Once` 缓存）
  - 修正 cwd 优先级：每次调用的 `tc.Cwd` 覆盖工具级默认 `b.Cwd`（原逻辑反了，被 Unix 错误消息巧合掩盖）
  - bash 测试改为平台自适应：Windows 回退 cmd 时用等价命令（`findstr`/`dir`/`cmd` 语法）
  - 普通命令执行现已支持纯 Windows 环境；仅 timeout/cancel 用例仍在 Windows 跳过

**向后兼容**：原 `/v1/chat` 仍可用（自动创建 session），不破现有客户端。

**CLI flags**（v1.1 完整）：
- `--provider=ollama|anthropic`
- `--toolcall=native|prompted|grammar`
- `--server=:8080`
- `--api-key=KEY`（v1.1.1）
- `--db=path`
- `[model]`

**总计**：247 测试，< 5 秒。

## [1.0.0] - 2026-06

### 🎉 v1.0 完整版

5 个增量 (v1.0.1-v1.0.5) 把 tracer bullet 升级到完整可用版本。

**新增**：

- **v1.0.1 Permission ask 弹窗**
- **v1.0.2 Anthropic Provider**
- **v1.0.3 Compaction**
- **v1.0.4 HTTP/SSE API**
- **v1.0.5 Prompted toolcall**

**总计**：227 测试。

## [0.5.x] - 2026-06

### 整合 + Persistence

- v0.5.1：TUI 多 topic 订阅 + TTY 检测
- v0.5.0：SQLite 持久化

## [0.4.0] - 2026-06

### Bubble Tea TUI

## [0.3.0] - 2026-06

### Session-level allow + WebFetch

## [0.2.0] - 2026-06

### Grep + Glob（首次自举成功）

## [0.1.0] - 2026-06

### Tracer Bullet 首发

## 计划 (v2.x)

- 分布式部署

[1.10.0]: https://github.com/yiwanghehe/acorncode/compare/v1.9.0...v1.10.0
[1.9.0]: https://github.com/yiwanghehe/acorncode/compare/v1.8.0...v1.9.0
[1.8.0]: https://github.com/yiwanghehe/acorncode/compare/v1.7.0...v1.8.0
[1.7.0]: https://github.com/yiwanghehe/acorncode/compare/v1.6.0...v1.7.0
[1.6.0]: https://github.com/yiwanghehe/acorncode/compare/v1.5.0...v1.6.0
[1.5.0]: https://github.com/yiwanghehe/acorncode/compare/v1.4.0...v1.5.0
[1.4.0]: https://github.com/yiwanghehe/acorncode/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/yiwanghehe/acorncode/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/yiwanghehe/acorncode/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/yiwanghehe/acorncode/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/yiwanghehe/acorncode/releases/tag/v1.0.0
[0.5.1]: https://github.com/yiwanghehe/acorncode/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/yiwanghehe/acorncode/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/yiwanghehe/acorncode/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/yiwanghehe/acorncode/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/yiwanghehe/acorncode/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/yiwanghehe/acorncode/releases/tag/v0.1.0
