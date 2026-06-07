# 贡献指南

## 前置

- Go 1.22+
- 跑 `make test` 确认 81 测试全过
- 推荐 Ollama + `qwen2.5-coder:7b` 跑端到端

## 工作流

1. Fork → `git checkout -b feat/xxx`
2. 写代码 + 测试
3. `make ci`（fmt + vet + test 全过）
4. 提 PR

## 硬规则（详见 [AGENTS.md](AGENTS.md)）

- **所有注释中文**（违反必被重写）
- 测试**不能** `t.Skip` 逃避（Windows 例外）
- tool **不能** import LLM 包
- 失败 3 次同类型问题 → 停下来报告

## 提交前自检

- [ ] `gofmt -l .` 无输出
- [ ] `go vet ./...` 0 错误
- [ ] `go test ./...` 全过
- [ ] 注释全部中文
- [ ] CHANGELOG.md 已更新（如有可见行为变化）
- [ ] AGENTS.md / docs/architecture.md 已更新（如改了约定或架构）

## Commit 格式

```
type(scope): 简短说明（中文优先）

- 关键变更点
- 测试覆盖
- 关联 issue
```

`type`: `feat` / `fix` / `docs` / `refactor` / `test` / `chore`

## 给 AI Agent

如果你是 AI 在做贡献，**先读 [AGENTS.md](AGENTS.md)**。里面包含：
- 硬规则（§1）
- Tool 实现模板（§3）
- 已知坑（§4）
- 强制中文注释
