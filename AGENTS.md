# AcornCode — AI Agent 工作约定

> 必读。新加 tool、发现新坑、改动代码风格，**立即**更新本文件。
>
> 当前 **v1.11**：6 个内置 tool + MCP 外部工具 + 3 toolcall 策略（Grammar 含 GBNF + Ollama format + Anthropic tool_choice + `--force-tool` + HTTP 请求级 `force_tool`）+ HTTP 鉴权 + 多 session + Compaction 持久化闭环 + 真实 tokenizer 估算 + **工具裁剪** + **SSRF 域名解析防护** + **UTF-8 安全截断** + **407 测试**（**3 第三方依赖**）。v1.9 完成代码结构重构（单一职责 R1–R8）。详见 [README.md](README.md) §已实现。

## 1. 硬规则

### 1.1 注释
- **所有注释中文**（包、函数、行内、测试说明）
- 变量名、字符串字面量、JSON tag 保留英文
- 导出符号必须有 godoc（中文），私有符号不强制

### 1.2 命名
- 文件名：小写下划线；包名：单数名词；接口：业务语义或 `-er` 后缀
- 错误：类型化 `XxxError`；日志：`slog`，key 用点分

### 1.3 失败处理
- 工具返回 `Result{}` 时**永远不返回 err**，把错误放 `Output`/`Status="error"`
- JSON Schema 验证失败**不要 panic**，返 `Result{Error: "..."}`
- 同类型问题失败 3 次 → **停下来报告用户**

### 1.4 依赖边界
- **不要在 tool 里调 LLM**（死循环）
- tool **不要 import** 任何 LLM/provider 包
- v1.8 实际依赖（3 个）：bubbletea / lipgloss / modernc-sqlite（session 用标准库 `database/sql`）
- v1.12：加 bubbles（与 bubbletea 同源，TUI viewport 滚动），共 4 个第三方依赖
- 加新依赖前先想"用 stdlib 能不能做"

### 1.5 结构约定（v1.9 重构后）
- **ID 生成**：统一用 `internal/id`（`New(prefix)` / `Short()`），不要再写 base36 时间戳
- **熔断逻辑**：在 `internal/agent/circuit.go` 的 `circuitBreaker`，不要塞回 `Loop`
- **toolcall 流式发送**：用 `emitter`（`Event`/`Text`），不要再内联 ctx-aware `select`
- **server handler**：保持薄入口（解析校验）+ `serveChatStream` 编排，单函数单职责
- **token 估算**：统一走 `internal/tokenizer.Count`，不要再写 `len(s)/4`（v1.10）
- **Compaction 写回**：压缩结果必须经 `store.ReplaceMessages` 原子写回，不要只打日志（v1.10）
- 发现某函数 > ~60 行或承担 ≥3 个明显职责，先拆再加功能

## 2. Tool 实现模板

照 `internal/llm/ollama.go`（Ollama 真实实现）和 `internal/tool/read.go`（22 测试）写新 tool。

```go
func (r *Xxx) Execute(ctx context.Context, args json.RawMessage, tc Context) (Result, error) {
    // 1. 解析 + 校验参数
    var p Params
    if err := json.Unmarshal(args, &p); err != nil {
        return Result{Status: "error", Error: "schema: " + err.Error()}, nil
    }
    // 2. 路径 normalize（base = tc.Cwd）
    if !filepath.IsAbs(p.Path) { p.Path = filepath.Join(tc.Cwd, p.Path) }
    p.Path = filepath.Clean(p.Path)
    // 3. 危险操作走权限
    if tc.Ask != nil {
        if err := tc.Ask(ctx, permission.Request{...}); err != nil {
            return Result{Status: "error", Error: err.Error()}, nil
        }
    }
    // 4. ctx 中断
    select { case <-ctx.Done(): return Result{Status: "error", Error: "canceled"}, nil; default: }
    // 5. 执行 + 返 Result
}
```

**Test 模板**（≥10 测试）：正常 / 异常 / 边界 / ctx 取消 / 权限拒绝 / JSON 校验失败。httptest 用 `<-r.Context().Done()` 等待关闭，**不要 `select {}`**（卡 Close）。

## 3. 已知坑

| # | 坑 | 解法 |
|---|----|------|
| 1 | tool 调 LLM 死循环 | tool 是无状态函数 |
| 2 | Bash 无 timeout 卡死 | `exec.CommandContext` + 30s 默认 |
| 3 | 路径不 normalize | `filepath.IsAbs` + `filepath.Clean` |
| 4 | Schema 验证 panic | 返 `Result{Error: "..."}` |
| 5 | bufio.Scanner 默认 64KB 装不下 thinking | `scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)` |
| 6 | SQLite 写锁竞争 | WAL + `SetMaxOpenConns(1)` |
| 7 | NDJSON 一行坏杀整流 | `slog.Warn` 跳过 |
| 8 | 测试 `select {}` 卡 Close | `<-r.Context().Done()` |
| 9 | Windows sh 不响应 SIGKILL | `if runtime.GOOS == "windows" { t.Skip(...) }`（仅 timeout/cancel 用例） |
| 10 | Bash 长输出爆内存 | 头尾各半（50KB 总） |
| 11 | Bash 硬编码 `sh -c` 在纯 Windows 跑不了 | `resolveShell()` 按平台选：Unix=`sh -c`；Windows 有 sh 用 `sh -c`，否则 `cmd /c`（v1.1.3） |
| 12 | cmd `/c` 调 powershell 内层引号被吞 | 表达式不要加内层双引号，如 `powershell -Command [string]::new('a',N)` |
| 13 | MCP server 一个 stdout 坏行杀整流 | `dispatch` 跳过坏 JSON 行 + `slog.Warn`（同 NDJSON 坑） |
| 14 | MCP server 退出后请求永久阻塞 | 读循环 EOF 时 `failAllPending` 唤醒所有等待者 |
| 15 | MCP 多 server 一个坏拖垮全部 | `SetupFromConfigs` 单 server 失败记日志跳过，不致命 |
| 16 | strategy.Prepare 没接线 → system 注入/GBNF 全失效 | `loop.buildRequest` 末尾必须调 `strategy.Prepare(req, tools)`（v1.4 修复） |
| 17 | Ollama `format` 强制整段 JSON 与自由文本冲突 | `Grammar.ForceToolCall` 默认 false；仅显式开启才设 `req.Format` |
| 18 | HTTP 并发请求共享 `cfg.Strategy` 改 ForceToolCall 会互相污染 | `strategyForRequest(force)` 在 force 时返回**独立** Grammar 实例，不动共享策略（v1.7） |
| 19 | Compaction 只摘要不写回 → 长 session 压缩无效 | `compact()` 调 `store.ReplaceMessages` 原子写回压缩结果（v1.10） |
| 20 | 压缩写回的 summary 是 `system` 角色，`toModelMessages` 只认 user/assistant 会丢摘要 | `toModelMessages` 增加 `system` 分支（v1.10） |
| 21 | `ReplaceMessages` 半途失败留下半压缩状态 | SQLite 用事务（DELETE+INSERT 整体回滚）；Memory 持写锁（v1.10） |
| 22 | `estimateTokens` 用 len/4 中文严重低估、且漏算工具 args/schema | 接入 `internal/tokenizer` 启发式 + 补算 ToolPart/JSONSchema（v1.10） |
| 23 | WebFetch SSRF 只挡字面 IP，内网域名直接绕过 | `checkSSRF` 解析域名校验所有 IP，任一私有即拒；解析失败保守拒（v1.11） |
| 24 | `read` 按字节硬截断切碎中文/emoji 成乱码 | `truncateUTF8` 回退到 UTF-8 字符边界，两条读取路径都接（v1.11） |
| 25 | `PickForTurn` 是 stub 永远返回全部工具，MaxTools 形同虚设 | 按相关性打分取 top-budget，核心工具基础分兜底（v1.11） |
| 26 | TUI 输入框无法输入中文：旧实现 `len(msg.String())==1` 按【字节】判断，中文 1 字符占 3 字节被丢弃 | 改判 `msg.Type==tea.KeyRunes/KeySpace` 并追加 `string(msg.Runes)`；退格按 `[]rune` 切边界，不按字节（v1.12） |
| 27 | TUI 流式正文雪球堆叠（"我是我是 Ac我是 AcornCode..."）：`part.delta` 的 Data 是【全量累积文本】，TUI 却 `WriteString` 追加 | 按 part ID 整体替换：每次 delta 先 `m.text.Reset()` 再写全量；记 `streamPartID` 标记当前 part（v1.12） |
| 28 | 第二轮对话报「期望状态 Idle, 当前 Stopped」：Loop 被复用跑多轮，但正常完成/turn 上限/errTurnAborted 把状态停在 Stopped/BuildingRequest，下一轮 `guard(StateIdle)` 失败 | 本轮正常收尾一律归位 `StateIdle`，`StateStopped` 只留给 ctx 取消与 `fatal()`（v1.12） |
| 29 | TUI 每轮对话覆盖、无法回看历史 | 正文改用 `bubbles/viewport`：`history`(定格问答)+`stream`(当前流式) 拼成内容；每轮 `flushStreamToHistory` 定格。自动跟随仅在 `AtBottom()` 时 `GotoBottom`，否则会打断用户手动上滚（v1.12） |
| 30 | Prompted 策略小模型漏写 `<tool_call>` 包裹时，裸 JSON 直接当文本输出 | EOF 时尝试 fallback：三层条件（schema 严格 + name 命中注册表 + 不在 markdown 包裹内）全中才识别为 tool call；系统 prompt 引导"想输出 JSON 文本"用 ```json 代码块，避免与工具调用冲突（v1.12） |

## 4. 跑命令

```bash
make test              # 全部（~5 秒，389 测试）
make test-llm          # LLM 客户端（10 + 11 anthropic = 21 测试）
make test-tool         # 工具（read/edit/bash/grep/glob/webfetch + PickForTurn 裁剪 + UTF-8 截断）
make test-agent        # Agent 集成（5 测试）
make vet               # go vet
make fmt-check         # gofmt 检查
make ci                # fmt + vet + test 完整
make e2e               # 端到端（需 ollama + qwen2.5-coder:7b，需 TTY）
```

## 5. 工作流

1. 每次 task 开始**主动读**本文件
2. 写新 tool 后：**先** build，**再** test，**再** 更新 [README.md §已实现](README.md#已实现) 表
3. 发现新坑：**先**记录到 §3，**再**告诉用户
4. **不遵守中文注释**必被重写
5. 测试必须能 `go test -run` 通过，不要 `t.Skip` 逃避

## 6. Claude-specific

如果你在用 Claude：

- **1 个 stdlib > 10 个第三方库**（项目约束）
- **写 5-10 行就跑一次测试** — 不要 100 行不测
- **失败 3 次停下问** — 别循环
- **加抽象 / 框架 = 禁止** — 用 stdlib 或现有依赖
- **改架构没 ADR** — 在本文件或 README "Key Decisions" 段写一段
- **跳过测试 = 禁止**（Windows 例外）

完整架构见 [docs/architecture.md](docs/architecture.md)。当前状态见 [README.md §当前状态](README.md#当前状态)。
