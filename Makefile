# MMX —— 构建与开发入口
#
# 常用：
#   make build        编译 master + agent
#   make check        vet + build + test（提交前必跑）
#   make e2e          端到端联调（主控 + Agent + 断言）
#   make web          构建前端（P9）
SHELL := /bin/bash

MODULE   := github.com/mmx-project/mmx
VERSION  ?= 0.1.0-dev
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILT_AT := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
  -X $(MODULE)/internal/version.Version=$(VERSION) \
  -X $(MODULE)/internal/version.Commit=$(COMMIT) \
  -X $(MODULE)/internal/version.BuildTime=$(BUILT_AT)

BIN := bin

.PHONY: all build master agent check vet test fmt tidy clean e2e web lint help

all: build

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: master agent ## 编译两个二进制

master: ## 编译主控
	@mkdir -p $(BIN)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mmx-master ./cmd/mmx-master

agent: ## 编译 Agent
	@mkdir -p $(BIN)
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mmx-agent ./cmd/mmx-agent

agent-all: ## 交叉编译各平台 Agent 到 bin/agent-dist/
	@mkdir -p $(BIN)/agent-dist
	@for p in linux/amd64 linux/arm64; do \
	  os=$${p%%/*}; arch=$${p##*/}; \
	  echo "  -> $$os-$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
	    -o $(BIN)/agent-dist/mmx-agent-$$os-$$arch ./cmd/mmx-agent; \
	done

vet: ## 静态检查
	go vet ./...

test: ## 单元测试
	go test ./...

fmt: ## 格式化
	gofmt -s -w .

tidy: ## 整理依赖
	go mod tidy

check: vet build test ## 提交前检查

lint: ## golangci-lint（需自行安装）
	golangci-lint run ./...

e2e: build ## 端到端联调
	bash scripts/e2e.sh

run-master: master ## 本地起主控（数据在 ./data）
	MMX_DATA_DIR=./data MMX_LISTEN=127.0.0.1:12889 ./$(BIN)/mmx-master

web: ## 构建前端（P9 起可用）
	cd web && npm ci && npm run build

clean: ## 清理产物
	rm -rf $(BIN) web/dist
