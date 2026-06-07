# AcornCode

> 本地小模型优先的 Go 编码 Agent · 单二进制 · 自举开发

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://www.apache.org/licenses/LICENSE-2.0)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)

AcornCode 是一个**纯 Go 原生**的 coding agent，类似 [opencode](https://github.com/sst/opencode) 但做了关键简化：

| 维度 | opencode | **AcornCode** |
|------|----------|---------------|
| 运行时 | Node.js + Deno | **单二进制，4 依赖** |
| LLM | Anthropic/GPT | **本地小模型优先**（7B 跑得动） |
| 工具调用 | 仅 Native | Native（v1.0 加 Grammar/Prompted） |
| 持久化 | Drizzle + SQLite | SQLite（v0.5+） |
| TUI | Ink | Bubble Tea（v0.4+） |

**TL;DR**：能跑 7B 模型做真活，能让模型自己写新工具自举迭代。

## 快速开始

### 前置

- Go 1.25+（编译时）
- [Ollama](https://ollama.ai) + `qwen2.5-coder:7b`（或 `llama3.1:8b`、`deepseek-coder-v2`）

```bash
ollama pull qwen2.5-coder:7b
```

### 跑

```bash
go build -o acorn ./cmd/acorn
./acorn
# 默认 endpoint: http://localhost:11434
# 默认 db: ./.acorncode.db
```

⚠️ **必须从 TTY 跑**（stdin 要是 terminal），不能 pipe。CI 场景等 v1.0 HTTP API。

环境变量 `OLLAMA_ENDPOINT` 可改服务地址。`--db=path` 改数据库路径。

### 测试

```bash
make test              # 全部，~5 秒
make test-llm          # LLM 客户端
make test-tool         # 工具
make test-agent        # 端到端集成
make ci                # fmt + vet + test
make e2e               # 端到端（需本地 ollama）
```

## 已实现（v0.5 整合）

| 模块 | 文件 | 测试 |
|------|------|------|
| Ollama Provider（NDJSON 流式） | `internal/llm/ollama.go` | 10 |
| Native toolcall 策略 | `internal/llm/toolcall/native.go` | 7 |
| Agent Loop（8 状态 + 3 熔断） | `internal/agent/loop.go` | 5 集成测试 |
| **read** tool | `internal/tool/read.go` | 22 |
| **edit** tool | `internal/tool/edit.go` | 12 |
| **bash** tool | `internal/tool/bash.go` | 16 |
| **grep** tool | `internal/tool/grep.go` | 17 |
| **glob** tool | `internal/tool/glob.go` | 18 |
| **webfetch** tool | `internal/tool/webfetch.go` | 19 |
| **SQLiteStore**（v0.5+） | `internal/session/sqlitestore.go` | 19 |
| Bus（6 事件，4 消费） | `internal/bus/event.go` | - |
| Permission Broker | `internal/permission/broker.go` | 15 |
| AGENTS.md Loader | `internal/instruction/loader.go` | - |
| **Bubble Tea TUI** | `internal/tui/model.go` | 20 |
| CLI（含 TTY 检测） | `cmd/acorn/main.go` | 2 |

**总计**：185 测试，< 5 秒。

## 当前状态

**v0.5 整合版** — 6 个 tool + Bubble Tea TUI + SQLite 持久化 + acorncode.json 权限 + TTY 检测。**端到端链路已缕清走通**。

### 限制

- 仅 Ollama（Anthropic/OpenAI 在 v1.0）
- Permission ask 规则默认 allow + log（v1.0 弹窗）
- WebFetch 默认禁私有 IP（含 AWS metadata）
- 必须从 TTY 跑（CI 场景等 v1.0 HTTP API）
- Windows 上 bash timeout 测试跳过（exec.CommandContext 行为差异）

### 依赖（4 个）

- `bubbletea` / `lipgloss` — TUI
- `modernc.org/sqlite` / `sqlx` — 持久化（无 CGo）

### 下一步

- **v1.0**：Compaction + Anthropic Provider + Grammar/Prompted toolcall + HTTP/SSE API
- **v2+**：MCP stdio Client

### 配置文件 `acorncode.json`

放在项目根，可选：

```json
{
  "permissions": {
    "rules": [
      { "tool": "read", "action": "allow" },
      { "tool": "edit", "action": "ask" },
      { "tool": "bash", "pattern": "^go (build|test|vet)", "action": "allow" },
      { "tool": "bash", "action": "deny" },
      { "tool": "webfetch", "action": "ask" }
    ]
  }
}
```

`action`: `allow` / `deny` / `ask`（v0.5 ask 默认 allow + log，v1.0 弹窗）。`pattern` 是 Go 正则，按 tool 匹配第一条命中。

### 数据库

`./acorn` 自动在当前目录建 `.acorncode.db`（SQLite WAL）。换路径：

```bash
./acorn --db=/tmp/my.db
```

## 关键设计决策

完整理由见 [docs/architecture.md](docs/architecture.md)。核心三点：

1. **Go 原生 + 最小依赖**：0 → 4 第三方依赖；单二进制 ~10MB；模型学 1 个 stdlib + 4 API 比学 10 个第三方库快
2. **ToolCall 三策略架构**（v0.5 只 Native）：渐进支持不同模型；Native 失败时 fallback Prompted
3. **v0.1 范围最小化**：Tracer bullet → v0.5 整合 → v1.0 完整

## 自举开发

AcornCode 的核心差异化 = **让模型自己写新工具**。Grep + Glob 是首次自举成功案例：模型按 `read → 写 → 测试 → 改 AGENTS` 的流程在 v0.1 上加出新 tool，零人工干预。详见 [docs/architecture.md §8](docs/architecture.md#8-自举开发模式)。

```
人: "加 Grep 工具，参考 Read"
  ↓
模型 (用 AcornCode 自己):
  - Read(read.go) + Read(AGENTS.md)
  - Write(grep.go + grep_test.go + schemas/grep.json)
  - Bash("go build" + "go test")
  - Edit(AGENTS.md) 更新"已实现"
  ↓
人: review 5 分钟，合并
```

## 文档

| 文件 | 受众 |
|------|------|
| [README.md](README.md) | 入口（你正在看） |
| [docs/architecture.md](docs/architecture.md) | 架构详细（贡献者） |
| [AGENTS.md](AGENTS.md) | AI agent 工作约定（**AI 必读**） |
| [CHANGELOG.md](CHANGELOG.md) | 版本历史 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 贡献流程 |

## 贡献

见 [CONTRIBUTING.md](CONTRIBUTING.md)。**所有注释必须中文**（[AGENTS.md §1.1](AGENTS.md)）。

## 许可证

[Apache 2.0](LICENSE) — 与 opencode 一致，企业友好。

## 致谢

- [opencode](https://github.com/sst/opencode) — 架构灵感
- [Ollama](https://ollama.ai) — 本地 LLM 平台
