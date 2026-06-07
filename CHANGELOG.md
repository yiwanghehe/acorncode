# Changelog

格式：[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。版本遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [0.3.0] - 2026-06

### Session-level allow + WebFetch

v0.2 基础之上加配置化权限 + 外部网络工具。

**新增**：
- **webfetch** tool（19 测试）：HTTP/HTTPS 抓取；**SSRF 防护默认禁** localhost / 127/8 / 10/8 / 172.16/12 / 192.168/16 / 169.254/16 / 0.0.0.0
- **Broker 规则匹配**：从 `acorncode.json` 加载 `{tool, pattern, action}` 规则；allow/deny/ask 三种
- **Session allow list**：session 内 `SessionApprove(tool, pattern)`，跨 turn 保留
- **LoadConfig**：配置文件加载（不存在不报错）

**总计**：149 测试，< 5 秒。**0 第三方依赖**（仍）。

## [0.2.0] - 2026-06

### Grep + Glob（首次自举成功）

v0.1 基础之上加 2 个新 tool，**由模型用 AcornCode 自己写**（README §自举开发的首次完整实践）。

**新增**：
- **grep** tool（17 测试）：内容搜索，path/pattern/include/ignore_case/line_numbers/max_results；跳过 .git/node_modules/二进制
- **glob** tool（18 测试）：文件匹配，pattern/path/type/max_results；自实现 `*` `**` `?` `[abc]`，0 依赖

**Bug fix（顺手）**：
- `.gitignore` 的 `acorn` 行误把 `cmd/acorn/main.go` 也忽略了（v0.1 commit 缺入口文件）→ 改成 `/acorn` 锚定根

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

**已知限制**：仅 Ollama / 无 TUI / 无持久化 / 无 Session-level allow（见 [README.md §当前状态](README.md#当前状态)）。

## [Unreleased]

### 计划
- v0.4：Bubble Tea TUI（加 bubbletea + lipgloss 依赖）
- v0.5：SQLite 持久化（加 modernc/sqlite + sqlx 依赖）
- v1.0：Grammar/Prompted toolcall + Anthropic Provider + Compaction

[0.3.0]: https://github.com/yiwanghehe/acorncode/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/yiwanghehe/acorncode/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/yiwanghehe/acorncode/releases/tag/v0.1.0
