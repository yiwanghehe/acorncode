# Pull Request

## 描述

做什么（一两句话）。

## 关联

- Issue / Discussion:
- 相关 ADR / 设计变更:

## 变更类型

- [ ] Bug fix
- [ ] New feature
- [ ] Breaking change
- [ ] Docs only
- [ ] Refactor

## 自检

- [ ] `make ci` 全过（fmt + vet + test）
- [ ] 注释全部中文（[AGENTS.md §1.1](AGENTS.md)）
- [ ] 导出符号有 godoc（中文）
- [ ] 没 import LLM 包到 tool（[AGENTS.md §1.4](AGENTS.md)）
- [ ] 测试**没**用 `t.Skip` 逃避
- [ ] CHANGELOG.md 已更新（如有可见行为变化）
- [ ] AGENTS.md / docs/architecture.md 已更新（如改了约定或架构）
