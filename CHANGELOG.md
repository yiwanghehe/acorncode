# Changelog

格式：[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。版本遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [1.0.0] - 2026-06

### 🎉 v1.0 完整版

5 个增量 (v1.0.1-v1.0.5) 把 tracer bullet 升级到完整可用版本。

**新增**：

- **v1.0.1 Permission ask 弹窗**：Broker.Ask(rule=ask) 真阻塞（60s 超时默认 deny），TUI 弹窗 3 选项（Allow / Always / Deny），左/右循环
- **v1.0.2 Anthropic Provider**：原生 Anthropic Messages API + SSE 解析 + tool schema 转换（OpenAI function → Anthropic tools），0 依赖
- **v1.0.3 Compaction**：SimpleCompactor 调 LLM 摘要老消息（保留最近 6 条），失败返原 history
- **v1.0.4 HTTP/SSE API**：`acorn --server=:8080` 起 HTTP server，POST /v1/chat 返 SSE 流，CI / headless 模式
- **v1.0.5 Prompted toolcall**：解析 `<tool_call>{...}</tool_call>` 文本块，给不支持原生 tool_call 的小模型用

**CLI flags**（v1.0 完整）：
- `--provider=ollama|anthropic`（默认 ollama）
- `--toolcall=native|prompted`（默认 native）
- `--server=:8080`（v1.0.4；启 HTTP server）
- `--db=path`（SQLite 路径，默认 `.acorncode.db`）
- `[model]`（默认 qwen2.5-coder:7b）

**总计**：227 测试，< 5 秒。**4 第三方依赖**：bubbletea / lipgloss / modernc-sqlite / sqlx。

## [0.5.1] - 2026-06

### 整合：TUI 多 topic 订阅 + TTY 检测

v0.5 端到端链路缕清 + 文档同步。

**修复**：
- TUI 订阅 4 个 topic（之前只听 `part.delta`）
- TTY 检测（无 TTY 返清晰错误）
- main.go 集成测试

## [0.5.0] - 2026-06

### SQLite 持久化

## [0.4.0] - 2026-06

### Bubble Tea TUI

## [0.3.0] - 2026-06

### Session-level allow + WebFetch

## [0.2.0] - 2026-06

### Grep + Glob（首次自举成功）

## [0.1.0] - 2026-06

### Tracer Bullet 首发

## 计划 (v1.x)

- v1.0.6：Grammar toolcall 策略（GBNF 规则生成）
- v1.1：HTTP/SSE 鉴权（API key）+ 多 session API
- v1.2：MCP stdio Client（让模型调外部工具）
- v2：分布式部署

[1.0.0]: https://github.com/yiwanghehe/acorncode/releases/tag/v1.0.0
[0.5.1]: https://github.com/yiwanghehe/acorncode/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/yiwanghehe/acorncode/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/yiwanghehe/acorncode/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/yiwanghehe/acorncode/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/yiwanghehe/acorncode/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/yiwanghehe/acorncode/releases/tag/v0.1.0
