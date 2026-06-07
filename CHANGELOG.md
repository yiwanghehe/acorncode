# Changelog

格式：[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。版本遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

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
- v0.2：Bubble Tea TUI + SQLite 持久化
- v0.3：Grep / Glob / WebFetch tools + Session-level allow
- v1.0：Grammar/Prompted toolcall + Anthropic Provider + Compaction

[0.1.0]: https://github.com/yourname/acorncode/releases/tag/v0.1.0
