# AcornCode

> 本地小模型优先的 Go 编码 Agent · 单二进制 · 自举开发 · **v1.4**

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://www.apache.org/licenses/LICENSE-2.0)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![Tests 287](https://img.shields.io/badge/tests-287%20passing-brightgreen)](https://github.com/yiwanghehe/acorncode)

AcornCode 是一个**纯 Go 原生**的 coding agent，类似 [opencode](https://github.com/sst/opencode) 但做了关键简化：

| 维度 | opencode | **AcornCode** |
|------|----------|---------------|
| 运行时 | Node.js + Deno | **单二进制，4 依赖** |
| LLM | Anthropic/GPT | **本地小模型优先**（7B 跑得动） |
| 工具调用 | 仅 Native | **Native + Prompted + Grammar** |
| 持久化 | Drizzle + SQLite | SQLite（modernc，纯 Go） |
| TUI | Ink | Bubble Tea |
| Permission | 隐式 | **配置文件 + 弹窗** |
| HTTP API | 有 | **Bearer 鉴权 + 多 session** |

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

# HTTP server 模式（CI / headless / 远程）
./acorn --server=:8080

# 用 Anthropic
./acorn --provider=anthropic claude-3-5-sonnet-latest

# 小模型用 Prompted toolcall 策略
./acorn --toolcall=prompted

# 启鉴权（v1.1.1）
./acorn --server=:8080 --api-key=secret
# 也可：export ACORN_API_KEY=secret

# Grammar 策略（v1.0.6 验证；v1.3 schema→GBNF 约束生成）
./acorn --toolcall=grammar
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

## 已实现（v1.1）

| 模块 | 文件 | 测试 |
|------|------|------|
| **Ollama Provider**（NDJSON 流式） | `internal/llm/ollama.go` | 10 |
| **Anthropic Provider**（v1.0.2） | `internal/llm/anthropic.go` | 7 |
| **Native toolcall** 策略 | `internal/toolcall/native.go` | 7 |
| **Prompted toolcall** 策略（v1.0.5） | `internal/toolcall/prompted.go` | 8 |
| **Grammar toolcall** 策略（v1.0.6） | `internal/toolcall/grammar.go` | 11 |
| **schema→GBNF 转换器**（v1.3，约束生成） | `internal/toolcall/gbnf.go` | 19 |
| **Provider 约束生成**（v1.4，Ollama `format` + Prepare 接线修复） | `internal/llm/ollama.go` | +2 |
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
| **Bubble Tea TUI**（5 topic 订阅） | `internal/tui/model.go` | 28 |
| **HTTP/SSE Server + Bearer 鉴权 + 多 session**（v1.1） | `internal/server/server.go` | 17 |
| **MCP stdio Client**（v1.2，让模型调外部工具） | `internal/mcp/client.go` | 15 |
| CLI（TTY 检测 + 6 flag） | `cmd/acorn/main.go` | 11 |

**总计**：**287 测试**，< 5 秒。**4 第三方依赖**（MCP / GBNF 均 0 新依赖，纯 stdlib）。

## 当前状态

**v1.1.0 完整版** — 6 个 tool + TUI + SQLite + 多 provider + 三 toolcall 策略 + HTTP API（鉴权 + 多 session）+ Permission 弹窗 + Compaction。

### CLI 完整

```
acorn [model]
  --provider=NAME        ollama | anthropic（默认 ollama）
  --toolcall=NAME        native | prompted | grammar（默认 native）
  --server=ADDR          启 HTTP server（如 ":8080"）
  --api-key=KEY          v1.1.1：HTTP Bearer 鉴权（也读 ACORN_API_KEY env）
  --db=path              SQLite 路径（默认 .acorncode.db）
```

### HTTP API（v1.1）

```bash
# 起 server（带鉴权）
./acorn --server=:8080 --api-key=secret

# 1. 创建 session
curl -X POST http://localhost:8080/v1/sessions \
  -H 'Authorization: Bearer secret' \
  -H 'Content-Type: application/json' \
  -d '{"title": "my session"}'
# → {"id": "sess_xxx", ...}

# 2. 列出所有 session
curl http://localhost:8080/v1/sessions -H 'Authorization: Bearer secret'
# → {"sessions": [...], "count": 3}

# 3. 取 session 详情
curl http://localhost:8080/v1/sessions/sess_xxx -H 'Authorization: Bearer secret'

# 4. 续聊（多轮）
curl -X POST http://localhost:8080/v1/sessions/sess_xxx/chat \
  -H 'Authorization: Bearer secret' \
  -H 'Content-Type: application/json' \
  -d '{"message": "继续"}'
# → SSE 流（同 /v1/chat，向后兼容）
```

**不鉴权模式**：省略 `--api-key` / `ACORN_API_KEY`，server 开放（dev 用）。

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

### MCP 外部工具（v1.2）

AcornCode 可启动外部 **MCP（Model Context Protocol）server** 子进程，把它们暴露的工具
自动注册进 agent，让模型调用文件系统、git、数据库等外部能力。在 `acorncode.json`
里加 `mcpServers` 段（格式兼容主流 MCP 客户端）：

```json
{
  "mcpServers": {
    "fs": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/dir"]
    },
    "git": {
      "command": "uvx",
      "args": ["mcp-server-git"],
      "env": { "GIT_AUTHOR_NAME": "acorn" },
      "disabled": false
    }
  }
}
```

- 工具 ID 加 server 名前缀避免冲突：`fs` server 的 `read_file` → `fs_read_file`
- 协议：JSON-RPC 2.0 over stdio（`initialize` 握手 → `tools/list` → `tools/call`）
- MCP 工具统一走 Permission `ask`（外部进程视作潜在危险）
- 单个 server 启动/握手失败不致命：记日志跳过，不拖垮 agent
- `disabled: true` 可临时禁用某个 server

### 限制

- TUI 模式必须从 TTY 跑（CI 用 server 模式）
- HTTP server 模式 Permission ask 默认 allow（headless 无 TUI）
- WebFetch 默认禁私有 IP（含 AWS metadata 169.254/16）
- **Bash 工具跨平台**（v1.1.3）：Unix 用 `sh -c`；Windows 有 POSIX shell（Git Bash/WSL）则用 `sh -c`，否则回退 `cmd /c`。纯 Windows 环境下需用 cmd 语义命令
- Bash 的 timeout/cancel 测试在 Windows 跳过（子进程取消信号不可靠）

## 关键设计决策

完整理由见 [docs/architecture.md](docs/architecture.md)。核心五点：

1. **Go 原生 + 4 依赖**：单二进制 ~10MB；模型学 1 个 stdlib + 4 API
2. **ToolCall 三策略**（v1.0 Native + Prompted + Grammar）：覆盖有/无原生 tool_call 的模型
3. **Compaction 自动化**：长 session 自动摘要（v1.0.3）
4. **HTTP API + 鉴权 + 多 session**（v1.1）：可上生产
5. **v0.1 范围最小化**：Tracer bullet → v1.0 完整 → v1.1 可用

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

