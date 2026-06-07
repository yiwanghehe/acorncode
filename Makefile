# AcornCode Makefile
# 中文注释 — 项目约定：所有文档中文优先

BINARY := acorn
PKG := ./...
COVERAGE_FILE := coverage.out

# 默认目标：显示帮助
.DEFAULT_GOAL := help

# 颜色（make 不原生支持，简化版）
GREEN  := \033[0;32m
YELLOW := \033[0;33m
RESET  := \033[0m

.PHONY: help
help: ## 显示所有命令
	@echo "$(GREEN)AcornCode$(RESET) — 本地小模型优先的 Go 编码 Agent"
	@echo ""
	@echo "可用命令："
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  $(YELLOW)%-15s$(RESET) %s\n", $$1, $$2}'
	@echo ""

# === 核心构建 ===

.PHONY: build
build: ## 编译二进制到 ./$(BINARY)
	@echo "==> 编译 $(BINARY)..."
	@go build -ldflags="-s -w" -o $(BINARY) ./cmd/acorn
	@echo "✓ 完成：./$(BINARY)"

.PHONY: run
run: build ## 编译并运行
	@./$(BINARY)

.PHONY: clean
clean: ## 清理构建产物
	@rm -f $(BINARY) $(COVERAGE_FILE) coverage.html
	@go clean -testcache
	@echo "✓ 清理完成"

# === 测试 ===

.PHONY: test
test: ## 跑所有测试
	@echo "==> 跑测试..."
	@go test -count=1 -timeout 60s $(PKG)
	@echo "✓ 全部通过"

.PHONY: test-verbose
test-verbose: ## 跑测试（详细输出）
	@go test -count=1 -timeout 60s -v $(PKG)

.PHONY: test-race
test-race: ## 跑测试（含 race 检测）
	@go test -count=1 -race -timeout 120s $(PKG)

.PHONY: test-coverage
test-coverage: ## 跑测试 + 覆盖率报告
	@go test -count=1 -timeout 60s -coverprofile=$(COVERAGE_FILE) $(PKG)
	@go tool cover -html=$(COVERAGE_FILE) -o coverage.html
	@echo "✓ coverage.html 已生成"
	@go tool cover -func=$(COVERAGE_FILE) | tail -1

.PHONY: test-llm
test-llm: ## 跑 LLM 客户端测试
	@go test -count=1 -timeout 30s -v ./internal/llm/...

.PHONY: test-tool
test-tool: ## 跑工具测试
	@go test -count=1 -timeout 30s -v ./internal/tool/...

.PHONY: test-agent
test-agent: ## 跑 agent 集成测试
	@go test -count=1 -timeout 60s -v ./internal/agent/...

# === 代码质量 ===

.PHONY: fmt
fmt: ## gofmt 格式化
	@echo "==> 格式化..."
	@gofmt -w .
	@echo "✓ 格式化完成"

.PHONY: fmt-check
fmt-check: ## 检查 gofmt（CI 用，不修改文件）
	@echo "==> 检查 gofmt..."
	@output=$$(gofmt -l .); \
	  if [ -n "$$output" ]; then \
	    echo "需要格式化的文件："; echo "$$output"; exit 1; \
	  fi
	@echo "✓ 格式正确"

.PHONY: vet
vet: ## go vet 类型检查
	@echo "==> 类型检查..."
	@go vet $(PKG)
	@echo "✓ 0 错误"

.PHONY: lint
lint: ## golangci-lint（可选）
	@which golangci-lint > /dev/null 2>&1 || { \
	  echo "golangci-lint 未安装，跳过（装：go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest）"; \
	  exit 0; \
	}
	@golangci-lint run --timeout=5m

# === 依赖 ===

.PHONY: deps
deps: ## 下载依赖
	@go mod download
	@go mod tidy

.PHONY: deps-update
deps-update: ## 更新依赖
	@go get -u ./...
	@go mod tidy

# === CI 完整流程 ===

.PHONY: ci
ci: fmt-check vet test ## 完整 CI 流程（fmt + vet + test）
	@echo "$(GREEN)✅ All checks passed$(RESET)"

# === 端到端（需要本地 Ollama）===

.PHONY: e2e
e2e: build ## 跑端到端 demo（需本地 ollama + 模型）
	@which ollama > /dev/null 2>&1 || { \
	  echo "❌ ollama 未安装"; exit 1; \
	}
	@ollama list | grep -q qwen2.5-coder || { \
	  echo "❌ 缺模型，先跑：ollama pull qwen2.5-coder:7b"; exit 1; \
	}
	@echo "==> 跑端到端..."
	@echo "read main.go" | ./$(BINARY)

# === 文档 / 元信息 ===

.PHONY: stats
stats: ## 显示代码统计
	@echo "==> 代码统计"
	@echo "Go 文件数：$$(find . -name '*.go' -not -path './.git/*' | wc -l)"
	@echo "代码行数：$$(find . -name '*.go' -not -path './.git/*' -exec cat {} \; | wc -l)"
	@echo "测试行数：$$(find . -name '*_test.go' -not -path './.git/*' -exec cat {} \; | wc -l)"
	@echo "二进制大小：$$(ls -lh $(BINARY) 2>/dev/null | awk '{print $$5}')"

# === 内部维护 ===

.PHONY: verify-deps
verify-deps: ## 验证依赖最小化（v0.1 强约束）
	@echo "==> 检查 v0.1 依赖约束..."
	@deps=$$(go list -m -mod=mod -f '{{if not .Main}}{{.Path}}{{end}}' all 2>/dev/null | wc -l); \
	  echo "当前依赖数：$$deps"
	@echo "v0.1 期望：0 第三方依赖（TUI/SQLite 推迟到 v0.2）"
