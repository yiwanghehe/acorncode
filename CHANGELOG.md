# Changelog

格式：[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。版本遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [0.5.1] - 2026-06

### 整合：TUI 多 topic 订阅 + TTY 检测

**v0.5 端到端链路缕清 + 文档同步**。

**修复**：
- **TUI 订阅 4 个 topic**（之前只听 `part.delta`）：现在听 `part.updated` / `agent.state.change` / `error`，工具状态变化、错误、状态切换都能在 TUI 状态栏看到
- **TTY 检测**：无 TTY 跑 `./acorn` 返清晰错误（之前会 panic 在 Bubble Tea 启动）
- **main.go 集成测试**：parseArgs + run 的 TTY 错误路径
- **TUI PartUpdated 状态映射**：pending → `→ tool` / running → `Running tool` / complete → `✓ tool done` / errored → `✗ tool error: ...` / rejected → `⊘ tool rejected`
- **TUI ErrorEvent 渲染**：fatal → `FATAL: ...` / 非 fatal → `Error: ...`

**文档**：
- `docs/architecture.md` 重写：修 §6 / §7 heading 重复；D1 更新（0 → 4 依赖）；v0.5 实际范围（6 tool / 4 依赖 / 185 测试）
- `README.md` 同步：v0.5 整合状态 / 依赖列表 / TTY 警告 / 文档索引
- `CHANGELOG.md` 加 v0.5.1 段

**测试**：185 个，< 5 秒（+17：TUI 8 新测试，cmd/acorn 2 新测试，registry_test 已存在）。

## [0.5.0] - 2026-06

### SQLite 持久化

v0.4 基础之上加 SQLite 持久化，session 不再随进程退出丢失。

**新增**：
- **SQLiteStore**（19 测试）：modernc.org/sqlite + sqlx；3 表（sessions / messages / parts），WAL + 单连接 + 毫秒精度时间戳
- **main.go** 加 `--db=path` flag（默认 `.acorncode.db`）

**总计**：168 测试，< 5 秒。

## [0.4.0] - 2026-06

### Bubble Tea TUI

v0.3 基础之上加终端 UI。**首次引入第三方依赖**。

**新增**：
- **Bubble Tea TUI**（15 测试）：状态栏 + 流式正文 + input box
- 快捷键：Ctrl+C / Esc 退出，Enter 发送，Backspace 删除
- 命令：`/exit` `/quit` `/clear` `/session` `/help`
- 依赖：`github.com/charmbracelet/bubbletea` + `github.com/charmbracelet/lipgloss`
- Go 1.22 → 1.25（bubbletea 要求）

**总计**：149 测试，< 5 秒。

## [0.3.0] - 2026-06

### Session-level allow + WebFetch

v0.2 基础之上加配置化权限 + 外部网络工具。

**新增**：
- **webfetch** tool（19 测试）：HTTP/HTTPS 抓取；**SSRF 防护默认禁** localhost / 127/8 / 10/8 / 172.16/12 / 192.168/16 / 169.254/16 / 0.0.0.0
- **Broker 规则匹配**：从 `acorncode.json` 加载 `{tool, pattern, action}` 规则；allow/deny/ask 三种
- **Session allow list**：session 内 `SessionApprove(tool, pattern)`，跨 turn 保留
- **LoadConfig**：配置文件加载（不存在不报错）

**总计**：134 测试，< 5 秒。**0 第三方依赖**。

## [0.2.0] - 2026-06

### Grep + Glob（首次自举成功）

v0.1 基础之上加 2 个新 tool，**由模型用 AcornCode 自己写**（README §自举开发的首次完整实践）。

**新增**：
- **grep** tool（17 测试）：内容搜索，path/pattern/include/ignore_case/line_numbers/max_results；跳过 .git/node_modules/二进制
- **glob** tool（18 测试）：文件匹配，pattern/path/type/max_results；自实现 `*` `**` `?` `[abc]`，0 依赖

**总计**：115 测试，< 5 秒。

## [0.1.0] - 2026-06

### Tracer Bullet 首发

端到端可跑的最小版本。所有核心模块真实实现 + 81 测试全过（< 5 秒）+ 0 第三方依赖。

**已实现**：
- Ollama 真实 NDJSON 客户端（10 测试）
- Agent Loop 状态机（8 状态 + 3 道熔断，5 集成测试）
- Native toolcall 策略（7 测试）
- read / edit / bash 三个 tool（22 + 12 + 16 测试）
- In-Memory Store + 真实 Broker（始终允许）+ AGENTS.md Loader
- stdout REPL

## [Unreleased]

### 计划
- v1.0：Compaction + Anthropic Provider + Grammar/Prompted toolcall + HTTP/SSE API + Permission ask 弹窗

[0.5.1]: https://github.com/yiwanghehe/acorncode/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/yiwanghehe/acorncode/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/yiwanghehe/acorncode/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/yiwanghehe/acorncode/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/yiwanghehe/acorncode/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/yiwanghehe/acorncode/releases/tag/v0.1.0
