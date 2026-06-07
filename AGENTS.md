# AcornCode — AI Agent 工作约定

> 必读。新加 tool、发现新坑、改动代码风格，**立即**更新本文件。

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
- v0.1 强约束：**0 第三方依赖**

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
| 9 | Windows sh 不响应 SIGKILL | `if runtime.GOOS == "windows" { t.Skip(...) }` |
| 10 | Bash 长输出爆内存 | 头尾各半（50KB 总） |

## 4. 跑命令

```bash
make test              # 全部（~5 秒，185 测试）
make test-llm          # LLM 客户端（10 测试）
make test-tool         # 工具（22+12+16+17+18+19+2 = 106 测试）
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
- **加抽象 / 框架 = 禁止** — v0.1 强约束 0 依赖
- **改架构没 ADR** — 在本文件或 README "Key Decisions" 段写一段
- **跳过测试 = 禁止**（Windows 例外）

完整架构见 [docs/architecture.md](docs/architecture.md)。当前状态见 [README.md §当前状态](README.md#当前状态)。
