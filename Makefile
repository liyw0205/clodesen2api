APP      := clodesen2api
CMD      := ./cmd/clodesen2api
GO       := go
VERSION  := $(shell tr -d '\r\n' < VERSION)
BIN_DIR  := bin
BIN      := $(BIN_DIR)/$(APP)

# 默认配置文件，可被环境变量覆盖
CONFIG   ?= config.toml
ADDR     ?= 0.0.0.0:6687

.PHONY: all build run tidy vet fmt clean help

all: build

## build: 编译二进制到 bin/clodesen2api
build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) $(CMD)

## run: 直接 go run（读取 CONFIG 指定的配置文件，默认 config.toml）
run:
	$(GO) run $(CMD) -c $(CONFIG)

## run-addr: go run 并用命令行指定监听地址
run-addr:
	$(GO) run $(CMD) -c $(CONFIG) -addr $(ADDR)

## bin: 运行已编译的二进制
bin: build
	./$(BIN) -c $(CONFIG)

## tidy: 整理依赖
tidy:
	$(GO) mod tidy

## vet: 静态检查
vet:
	$(GO) vet ./...

## fmt: 格式化
fmt:
	$(GO) fmt ./...

## clean: 清理构建产物
clean:
	@rm -rf $(BIN_DIR)

## help: 打印帮助
help:
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n\nTargets:\n"} \
	/^[a-zA-Z_-]+:.*?##/ { printf "  %-10s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
