# Changelog

格式：[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。版本遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

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

## 计划 (v1.x)

- v1.6：Loop 暴露 ForceToolCall 开关（CLI flag / 配置），让用户按需开启强制工具调用
- v2：分布式部署

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
