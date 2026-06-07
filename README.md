# AcornCode

> 本地小模型优先的 Go 编码 Agent · 单二进制 · 自举开发 · **v1.0**

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://www.apache.org/licenses/LICENSE-2.0)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![Tests 227](https://img.shields.io/badge/tests-227%20passing-brightgreen)](https://github.com/yiwanghehe/acorncode)

AcornCode 是一个**纯 Go 原生**的 coding agent，类似 [opencode](https://github.com/sst/opencode) 但做了关键简化：

| 维度 | opencode | **AcornCode** |
|------|----------|---------------|
| 运行时 | Node.js + Deno | **单二进制，4 依赖** |
| LLM | Anthropic/GPT | **本地小模型优先**（7B 跑得动） |
| 工具调用 | 仅 Native | **Native + Prompted** |
| 持久化 | Drizzle + SQLite | SQLite（modernc，纯 Go） |
| TUI | Ink | Bubble Tea |
| Permission | 隐式 | 配置文件 + 弹窗 |
| HTTP API | 有 | **v1.0 有** |

**TL;DR**：能跑 7B 模型做真活，能让模型自己写新工具自举迭代。

## 快速开始

### 前置

- Go 1.25+（编译时）
- [Ollama](https://ollama.ai) + `qwen2.5-coder:7b`（或 Anthropic API key）

```bash
# 选项 A：Ollama（本地，免费）
ollama pull qwen2.5-coder:7b

# 选项 B：Anthropic Claude
export ANTHROPIC_API_KEY=sk-ant-...
```

### 跑

```bash
go build -o acorn ./cmd/acorn

# TUI 模式（默认）
./acorn

# HTTP server 模式（CI / headless）
./acorn --server=:8080

# 用 Anthropic
./acorn --provider=anthropic claude-3-5-sonnet-latest

# 小模型用 Prompted toolcall 策略
./acorn --toolcall=prompted
```

### 测试

```bash
make test              # 全部，~5 秒
make test-llm          # LLM 客户端
make test-tool         # 工具
make test-agent        # 端到端集成
make ci                # fmt + vet + test
make e2e               # 端到端（需本地 ollama）
```

## 已实现（v1.0）

| 模块 | 文件 | 测试 |
|------|------|------|
| **Ollama Provider**（NDJSON 流式） | `internal/llm/ollama.go` | 10 |
| **Anthropic Provider**（v1.0.2） | `internal/llm/anthropic.go` | 7 |
| **Native toolcall** 策略 | `internal/toolcall/native.go` | 7 |
| **Prompted toolcall** 策略（v1.0.5） | `internal/toolcall/prompted.go` | 8 |
| Agent Loop（8 状态 + 3 熔断） | `internal/agent/loop.go` | 5 集成 |
| **Compaction**（v1.0.3） | `internal/compaction/simple.go` | 6 |
| **Permission ask 弹窗**（v1.0.1） | `internal/permission/broker.go` | 22 |
| **read** tool | `internal/tool/read.go` | 22 |
| **edit** tool | `internal/tool/edit.go` | 12 |
| **bash** tool | `internal/tool/bash.go` | 16 |
| **grep** tool | `internal/tool/grep.go` | 17 |
| **glob** tool | `internal/tool/glob.go` | 18 |
| **webfetch** tool | `internal/tool/webfetch.go` | 19 |
| **SQLiteStore** | `internal/session/sqlitestore.go` | 19 |
| Bus（6 事件，5 消费） | `internal/bus/event.go` | - |
| AGENTS.md Loader | `internal/instruction/loader.go` | - |
| **Bubble Tea TUI**（4 topic 订阅） | `internal/tui/model.go` | 28 |
| **HTTP/SSE Server**（v1.0.4） | `internal/server/server.go` | 6 |
| CLI（TTY 检测 + 5 flag） | `cmd/acorn/main.go` | 8 |

**总计**：**227 测试**，< 5 秒。**4 第三方依赖**。

## 当前状态

**v1.0.0 完整版** — 6 个 tool + TUI + SQLite + 多 provider + 多 toolcall 策略 + HTTP API + Permission 弹窗 + Compaction。

### CLI 完整

```
acorn [model]
  --provider=NAME        ollama | anthropic（默认 ollama）
  --toolcall=NAME        native | prompted（默认 native）
  --server=ADDR          启 HTTP server（v1.0.4；如 ":8080"）
  --db=path              SQLite 路径（默认 .acorncode.db）
```

### HTTP API（v1.0.4）

```bash
# 起 server
./acorn --server=:8080

# 发请求
curl -X POST http://localhost:8080/v1/chat \
  -H 'Content-Type: application/json' \
  -d '{"message": "list Go files"}'

# 收 SSE 流
# event: session
# data: {"session_id": "sess_xxx"}
#
# event: text
# data: {"text": "I'll grep..."}
#
# event: finish
# data: {"reason": "stop"}
```

### 配置文件 `acorncode.json`

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

`action`: `allow` / `deny` / `ask`（ask 真弹窗 v1.0.1）。`pattern` 是 Go 正则。

### 限制

- Permission ask 在 HTTP server 模式默认 allow（headless 无 TUI）；需用 `allow` / `deny` rule
- WebFetch 默认禁私有 IP（含 AWS metadata 169.254/16）
- Windows bash timeout 测试跳过

## 关键设计决策

完整理由见 [docs/architecture.md](docs/architecture.md)。核心四点：

1. **Go 原生 + 4 依赖**：单二进制 ~10MB；模型学 1 个 stdlib + 4 API
2. **ToolCall 双策略**（v1.0 Native + Prompted）：覆盖有/无原生 tool_call 能力的模型
3. **Compaction 自动化**：长 session 自动摘要（v1.0.3）
4. **v0.1 范围最小化**：Tracer bullet → v1.0 完整

## 自举开发

AcornCode 的核心差异化 = **让模型自己写新工具**。Grep + Glob 是首次自举成功案例（v0.2）。

## 文档

| 文件 | 受众 |
|------|------|
| [README.md](README.md) | 入口（你正在看） |
| [docs/architecture.md](docs/architecture.md) | 架构（贡献者） |
| [AGENTS.md](AGENTS.md) | AI agent 工作约定（**AI 必读**） |
| [CHANGELOG.md](CHANGELOG.md) | 版本历史 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 贡献流程 |

## 贡献

见 [CONTRIBUTING.md](CONTRIBUTING.md)。**所有注释必须中文**（[AGENTS.md §1.1](AGENTS.md)）。

## 许可证

[Apache 2.0](LICENSE)。

## 致谢

- [opencode](https://github.com/sst/opencode) — 架构灵感
- [Ollama](https://ollama.ai) — 本地 LLM 平台
- [Anthropic](https://anthropic.com) — Claude API
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) — Go TUI

