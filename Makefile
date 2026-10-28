# ada-go M0 Makefile（开发计划 §4.1 M0.1/M0.5、§7.1 门禁命令）
#
# 平台说明：
#   - Windows 上没有原生 make：使用 Git for Windows / MSYS2 自带的 mingw32-make，
#     并确保 sh.exe 在 PATH（Git Bash 环境天然满足）。SHELL 强制 POSIX sh，
#     这样 `GOWORK=off go mod tidy` 这类内联环境变量赋值在三个平台语义一致。
#   - 所有目标在仓库根目录执行；路径一律用 `/`。

SHELL := sh

GO            ?= go
BIN           ?= bin
MODULE        := github.com/adag/ada-go
VERSION_PKG   := $(MODULE)/internal/version
REVISION      := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILD_TIME    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
# 版本注入与 cmd/adag 的命令面约定一致（另一位工程师实现）：
#   -X internal/version.Revision=... -X internal/version.BuildTime=...
LDFLAGS       := -X $(VERSION_PKG).Revision=$(REVISION) -X $(VERSION_PKG).BuildTime=$(BUILD_TIME)

MOEJS_TAG     ?= v0.1.0-alpha.11

# 发布版本号：git tag 优先于 rev（M5）。HEAD 恰在 tag 上用 tag（如 v1.0.0-rc.1），
# 否则落回短哈希；没有 git 时用 version 包默认值。打 RC tag 后 make release 自动拾取，
# 本地验证可用 RELEASE_VERSION=v1.0.0-rc.1 覆盖。
# 注意：recipe 内的字符串保持 ASCII——mingw32-make 在 Windows 上按系统代码页传参，
# 中文 recipe 字符串会让 sh 的引号配对错乱（注释不受影响）。
RELEASE_VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || git rev-parse --short HEAD 2>/dev/null || echo 1.0.0-rc.1)
RELEASE_DIR     := $(BIN)/release
RELEASE_LDFLAGS := $(LDFLAGS) -X $(VERSION_PKG).Version=$(RELEASE_VERSION)

.PHONY: help setup build test race vet lint gen verify doctor gate tidy cross release

help:
	@echo "setup     准备相邻 moejs 检出（校验 tag）+ 下载依赖"
	@echo "build     构建全部包（ldflags 注入 Revision/BuildTime）"
	@echo "test      go test ./..."
	@echo "race      go test -race ./...（Windows 腿见 gate/test 的 M0 已知限制说明）"
	@echo "vet       go vet ./..."
	@echo "lint      staticcheck ./...（配置 staticcheck.conf；@latest 钉版本由 M5 处理）"
	@echo "gen       go generate ./...（生成物必须与源码同 PR 提交）"
	@echo "verify    adag verify + verify nodecompat/ui/boundary（cmd/adag 属 M0.4）"
	@echo "doctor    adag doctor --fake（products/skeleton.agent.json 驱动空基座）"
	@echo "gate      运行与 CI 完全相同的门禁 scripts/ci-gate.sh"
	@echo "tidy      GOWORK=off go mod tidy（发布口径，11 §13.1）"
	@echo "cross     GOWORK=off CGO_ENABLED=0 三平台二进制到 $(BIN)/（无 cgo，可交叉编译）"
	@echo "release   三平台二进制到 $(RELEASE_DIR)/ + sha256sum 清单（注入 Version，git tag 优先）"

setup:
	sh scripts/bootstrap-moejs.sh
	$(GO) mod download
	@echo "提示：staticcheck 无需预装二进制；lint 与 gate 都用 go run honnef.co/go/tools/cmd/staticcheck@latest 现编，保证与当前 Go 工具链一致。"

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" ./...

test:
	$(GO) test ./...

# M0 已知限制：-race 依赖 cgo，Windows 无 MSVC/clang 工具链时跑不了（11 §13.2 无 cgo）；
# CI 与 ci-gate.sh 的 Windows 腿自动降级为 test，M5 发布加固时补 windows race。
race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@latest ./...

gen:
	$(GO) generate ./...

verify:
	$(GO) run ./cmd/adag verify
	$(GO) run ./cmd/adag verify nodecompat
	$(GO) run ./cmd/adag verify ui
	$(GO) run ./cmd/adag verify boundary

doctor:
	$(GO) run ./cmd/adag doctor --fake

gate:
	sh scripts/ci-gate.sh

tidy:
	GOWORK=off $(GO) mod tidy

# 三平台（linux/darwin/windows）；发布与安装包一律 GOWORK=off（开发计划 §7.1），
# 这里与 .github/workflows/release-build.yml 的矩阵保持一致。CGO_ENABLED=0 保证纯 Go 交叉编译。
cross:
	@mkdir -p $(BIN)
	@set -e; for t in linux/amd64 darwin/amd64 darwin/arm64 windows/amd64; do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "building $(BIN)/adag-$$os-$$arch$$ext (GOWORK=off CGO_ENABLED=0)"; \
		CGO_ENABLED=0 GOWORK=off GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/adag-$$os-$$arch$$ext ./cmd/adag; \
	done

# M5 发布目标（docs/release-m5.md）：三平台二进制 + sha256 清单 + 版本注入。
#   - 产物命名 adag-<version>-<os>-<arch>[.exe]，Version 注入 git tag（优先）或短哈希；
#   - 某平台交叉编译失败只告警不阻塞（本机工具链差异；CI release 矩阵是权威口径），
#     但至少要有一个产物，否则报错——不允许「成功地什么都没发布」。
#   - sha256sums.txt 与产物同目录，随产物一起上传（升级校验用）。
release:
	@mkdir -p $(RELEASE_DIR)
	@rm -f $(RELEASE_DIR)/adag-* $(RELEASE_DIR)/sha256sums.txt
	@echo "release version = $(RELEASE_VERSION)"
	@for t in linux/amd64 darwin/amd64 darwin/arm64 windows/amd64; do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		name="adag-$(RELEASE_VERSION)-$$os-$$arch$$ext"; \
		echo "building $(RELEASE_DIR)/$$name (GOWORK=off CGO_ENABLED=0)"; \
		if CGO_ENABLED=0 GOWORK=off GOOS=$$os GOARCH=$$arch $(GO) build -trimpath \
			-ldflags "$(RELEASE_LDFLAGS)" -o $(RELEASE_DIR)/$$name ./cmd/adag; then \
			:; \
		else \
			echo "WARN: $$os/$$arch cross-compile failed (non-blocking; CI release matrix is authoritative)"; \
		fi; \
	done
	@files=$$(ls $(RELEASE_DIR)/adag-* 2>/dev/null); \
	if [ -z "$$files" ]; then \
		echo "ERROR: release produced no binaries"; exit 1; \
	fi
	@cd $(RELEASE_DIR) && sha256sum adag-* > sha256sums.txt && cat sha256sums.txt
