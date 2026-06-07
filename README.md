# AcornCode

> 本地小模型优先的 Go 编码 Agent · 单二进制 · 0 依赖 · 自举开发

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://www.apache.org/licenses/LICENSE-2.0)
[![Go 1.22+](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev/)

AcornCode 是一个**纯 Go 原生**的 coding agent，类似 [opencode](https://github.com/sst/opencode) 但做了关键简化：

| 维度 | opencode | **AcornCode** |
|------|----------|---------------|
| 运行时 | Node.js + Deno | **单二进制，0 依赖** |
| LLM | Anthropic/GPT | **本地小模型优先**（7B 跑得动） |
| 工具调用 | 仅 Native | Native（v1.0 加 Grammar/Prompted） |
| 持久化 | Drizzle + SQLite | 内存 store（v0.2 换 SQLite） |
| TUI | Ink | stdout REPL（v0.2 上 Bubble Tea） |

**TL;DR**：能跑 7B 模型做真活，能让模型自己写新工具自举迭代。

## 快速开始

### 前置

- Go 1.22+
- [Ollama](https://ollama.ai) + `qwen2.5-coder:7b`（或 `llama3.1:8b`、`deepseek-coder-v2`）

```bash
ollama pull qwen2.5-coder:7b
```

### 跑

```bash
go build -o acorn ./cmd/acorn
./acorn
# 默认 endpoint: http://localhost:11434
# 输入消息，模型会调 read / edit / bash
```

环境变量 `OLLAMA_ENDPOINT` 可改服务地址。

### 测试

```bash
make test              # 全部，~5 秒
make test-llm          # LLM 客户端
make test-tool         # 工具
make test-agent        # 端到端集成
make ci                # fmt + vet + test
make e2e               # 端到端（需本地 ollama）
```

## 已实现

| 模块 | 文件 | 状态 |
|------|------|------|
| Ollama Provider（NDJSON 流式） | `internal/llm/ollama.go` | 10 测试 |
| Native toolcall 策略 | `internal/llm/toolcall/native.go` | 7 测试 |
| Agent Loop（8 状态 + 3 熔断） | `internal/agent/loop.go` | 5 集成测试 |
| **read** tool | `internal/tool/read.go` | 22 测试 |
| **edit** tool | `internal/tool/edit.go` | 12 测试 |
| **bash** tool | `internal/tool/bash.go` | 16 测试 |
| **grep** tool | `internal/tool/grep.go` | 17 测试 |
| **glob** tool | `internal/tool/glob.go` | 18 测试 |
| **webfetch** tool | `internal/tool/webfetch.go` | 19 测试 |
| In-Memory Store | `internal/session/memstore.go` | 7 测试 |
| Bus（6 事件） | `internal/bus/event.go` | - |
| Permission Broker | `internal/permission/broker.go` | 15 测试（v0.3 起支持 acorncode.json 规则） |
| AGENTS.md Loader | `internal/instruction/loader.go` | - |
| CLI REPL | `cmd/acorn/main.go` | - |

**总计**：149 测试，< 5 秒。

## 当前状态

**v0.3 Session-allow + WebFetch** — 6 个 tool（read/edit/bash/grep/glob/webfetch），Broker 支持 acorncode.json 规则 + session-level allow list，WebFetch 带 SSRF 防护。

### 限制

- 仅 Ollama（Anthropic/OpenAI 在 v1.0）
- stdout REPL（无 TUI，v0.4 上 Bubble Tea）
- 无持久化（In-Memory Store，v0.5 换 SQLite）
- Permission ask 规则默认 allow（v0.4 TUI 弹窗）
- WebFetch 默认禁私有 IP（含 AWS metadata）
- Windows 上 bash timeout 测试跳过（exec.CommandContext 行为差异）

### 下一步

- **v0.4**：Bubble Tea TUI（加 bubbletea + lipgloss 依赖）
- **v0.5**：SQLite 持久化（加 modernc/sqlite + sqlx 依赖）
- **v1.0**：Grammar/Prompted toolcall + Anthropic Provider + Compaction

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

`action`: `allow` / `deny` / `ask`（v0.3 ask 默认 allow，v0.4 TUI 弹窗）。`pattern` 是 Go 正则，按 tool 匹配第一条命中。

## 关键设计决策

完整理由见 [docs/architecture.md](docs/architecture.md)。核心三点：

1. **Go 原生 + 0 依赖**：单二进制 ~10MB；模型学 1 个 stdlib 比学 10 个第三方库快。
2. **ToolCall 三策略架构**（v0.1 只 Native）：渐进支持不同模型；Native 失败时 fallback Prompted。
3. **v0.1 范围最小化**：Tracer bullet ≠ 完整产品；2-3 周完成，端到端可演示，再扩。

## 自举开发

AcornCode 的核心差异化 = **让模型自己写新工具**。Grep + Glob 是首次自举成功案例：模型按 `read → 写 → 测试 → 改 AGENTS` 的流程在 v0.1 上加出新 tool，零人工干预。详见 [docs/architecture.md §6](docs/architecture.md#6-自举开发模式)。

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
