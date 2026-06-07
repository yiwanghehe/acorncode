# Changelog

格式：[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。版本遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

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

- v1.2：MCP stdio Client（让模型调外部工具）
- v1.3：Grammar GBNF 完整版（schema→GBNF 转换器）
- v2：分布式部署

[1.1.0]: https://github.com/yiwanghehe/acorncode/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/yiwanghehe/acorncode/releases/tag/v1.0.0
[0.5.1]: https://github.com/yiwanghehe/acorncode/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/yiwanghehe/acorncode/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/yiwanghehe/acorncode/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/yiwanghehe/acorncode/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/yiwanghehe/acorncode/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/yiwanghehe/acorncode/releases/tag/v0.1.0
