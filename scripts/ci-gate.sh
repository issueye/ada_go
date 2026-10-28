#!/bin/sh
# ada-go 本地一键门禁：与 .github/workflows/ci.yml 调用的是同一份脚本（开发计划 §7.1、设计 10 §11.2）。
#
# 用法：
#   sh scripts/ci-gate.sh                 # 按顺序跑全部步骤
#   sh scripts/ci-gate.sh gofmt vet ...   # 只跑指定步骤（CI 矩阵逐步调用，保持输出对齐）
#
# 步骤（顺序固定，失败即阻断；不允许 || true 静默）：
#   gofmt vet staticcheck generate modtidy test conformance nodecompat verify doctor
#
# 环境变量：
#   DEP_MODE   workspace|module（默认自动：GOWORK=off 视为 module，否则存在 go.work 视为 workspace）
#              module 模式会 export GOWORK=off（11 §13.1：发布与独立构建口径）。
#
# 约定：目标在后续里程碑才存在时（如 internal/conformance 属 M1、cmd/adag 属 M0.4），
# 打印显式 "SKIP [step] 原因"；SKIP 只在守卫条件（包/命令确实不存在）时发生，
# 存在即执行、执行失败就阻断。
set -eu

# 步骤名白名单
ALL_STEPS="gofmt vet staticcheck generate modtidy test conformance nodecompat verify doctor"

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
CDPATH= cd -- "$script_dir/.." && pwd >/dev/null

# ---------- 模式与平台探测 ----------
IS_WINDOWS=0
case "$(uname -s)" in
	MINGW*|MSYS*|CYGWIN*|Windows_NT) IS_WINDOWS=1 ;;
esac

DEP_MODE="${DEP_MODE:-}"
if [ -z "$DEP_MODE" ]; then
	if [ "${GOWORK:-}" = "off" ]; then
		DEP_MODE=module
	elif [ -f go.work ]; then
		DEP_MODE=workspace
	else
		DEP_MODE=module
	fi
fi
case "$DEP_MODE" in
	module) export GOWORK=off ;;
	workspace) ;;
	*) echo "ci-gate: DEP_MODE 只支持 workspace|module，收到：$DEP_MODE" >&2; exit 2 ;;
esac

say()  { printf '%s\n' "$*"; }
skip() { printf 'SKIP  [%s] %s\n' "$1" "$2"; }

git_has_head() { git rev-parse --verify -q HEAD >/dev/null 2>&1; }

# ---------- 步骤实现 ----------

step_gofmt() {
	unformatted=$(gofmt -l .)
	if [ -n "$unformatted" ]; then
		say "FAIL  [gofmt] 以下文件未通过 gofmt -l（请 go fmt 后重跑）："
		printf '%s\n' "$unformatted"
		exit 1
	fi
	say "PASS  [gofmt] gofmt -l 为空"
}

step_vet() {
	go vet ./...
	say "PASS  [vet] go vet ./..."
}

step_staticcheck() {
	# 统一用 `go run honnef.co/go/tools/cmd/staticcheck@latest ./...` 而不是 PATH 上的旧二进制：
	# 预编译的旧 staticcheck 发行版内嵌 Go 1.24 的 export-data 解码器，读不了 Go 1.27
	# 编译产物（"internal error in importing ..."，本机实测），`go run @latest` 会用
	# 当前工具链现编，行为与 CI 里 `go install ...@latest` 一致且跨平台。
	# 钉版本说明：@latest 会随上游发版漂移、随时可能引入新检查阻断合并；
	# 按开发计划，staticcheck 的版本钉定（@vX.Y.Z + 工具清单）由 M5 发布加固时统一处理。
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...
	say "PASS  [staticcheck] staticcheck ./...（配置见 staticcheck.conf，TOML、无点前缀文件名）"
}

step_generate() {
	# 生成物必须与源码同 PR 提交（开发计划 §6）：go generate 后工作区必须干净。
	go generate ./...
	if git_has_head; then
		if ! git diff --exit-code; then
			say "FAIL  [generate] go generate 后 git diff 非空：请把生成物与源码一起提交"
			exit 1
		fi
		say "PASS  [generate] 生成物同步（git diff --exit-code）"
	else
		skip generate "本地仓库还没有提交 HEAD，无法比对；CI 上有 HEAD，本步会强制 git diff --exit-code"
	fi
}

step_modtidy() {
	# 依赖整理只在 module 模式做（11 §13.1：workspace 的本地替换会掩盖发布依赖问题）。
	if [ "$DEP_MODE" != "module" ]; then
		skip modtidy "module 模式专属步骤；workspace 模式跳过，CI 的 module 矩阵腿负责执行"
		return 0
	fi
	GOWORK=off go mod tidy
	if git_has_head; then
		if ! git diff --exit-code go.mod go.sum; then
			say "FAIL  [modtidy] GOWORK=off go mod tidy 改动了 go.mod/go.sum：请把整理结果单独提交"
			exit 1
		fi
	else
		skip modtidy "无 HEAD，暂略 tidy 后的 diff 检查（CI 会执行）"
	fi
	GOWORK=off go mod verify
	say "PASS  [modtidy] GOWORK=off go mod tidy + diff + verify"
}

step_test() {
	if [ "$IS_WINDOWS" = 1 ]; then
		# M0 已知限制：-race 需要 cgo（Windows 上要 MSVC/clang 工具链），项目口径是无 cgo
		# （11 §13.2），GitHub 的 windows runner 亦不在 M0 提供 race。
		# Windows 只跑不带 -race 的 go test ./...；禁止用 continue-on-error 掩盖，
		# M5 发布加固时补 windows race（临时开启 CGO 的独立测试 job 或 WSL）。
		say "NOTE  [test] Windows 腿：M0 期间跳过 -race（已知限制，M5 补 windows race），只跑 go test ./..."
		go test ./...
	else
		go test -race ./...
	fi
	say "PASS  [test] 测试通过"
}

# run_guarded_test <step名> <包> <测试函数> <所属里程碑>
# 包不存在或测试函数尚未写出时显式 SKIP；存在则必须通过，否则阻断。
run_guarded_test() {
	step=$1; pkg=$2; name=$3; milestone=$4
	if ! go list "$pkg" >/dev/null 2>&1; then
		skip "$step" "$pkg 尚不存在（属 $milestone 交付物）；包一旦落地本步自动生效"
		return 0
	fi
	# 先 list 测试函数：能编译但函数缺失时也不能伪装成通过。
	if ! out=$(go test -list "^${name}$" "$pkg" 2>&1); then
		printf '%s\n' "$out"
		say "FAIL  [$step] $pkg 存在但无法编译/枚举——这是真实失败，不是 SKIP"
		exit 1
	fi
	if printf '%s\n' "$out" | grep -qxF "$name"; then
		go test "$pkg" -run "^${name}$"
		say "PASS  [$step] $pkg -run $name"
	else
		skip "$step" "$pkg 已存在但还没有 $name（属 $milestone）"
	fi
}

step_conformance() {
	run_guarded_test conformance ./internal/conformance TestInvariants "M1-A 不变量测试（INV-1～INV-12）"
}

step_nodecompat() {
	# Node 双跑语料属 M1-D/M3；包在 M0 还不存在，必须显式 SKIP 并说明原因。
	run_guarded_test nodecompat ./internal/conformance/nodecompat TestNodeCompat "M1-D Node 20/moejs 双跑语料"
}

step_verify() {
	# 命令面（另一位工程师按此实现，M0.4）：
	#   go run ./cmd/adag verify [generated|dispatch|hooks|slots|capabilities|manifest|
	#     vnode|products|docs|nodecompat|ui|boundary|all]
	if [ ! -d cmd/adag ]; then
		skip verify "cmd/adag 尚未落地（M0.4，另一工程师实现中）；目录一旦出现本步自动生效"
		return 0
	fi
	go run ./cmd/adag verify
	go run ./cmd/adag verify nodecompat
	go run ./cmd/adag verify ui
	go run ./cmd/adag verify boundary
	say "PASS  [verify] adag verify（全量）+ nodecompat + ui + boundary"
}

step_doctor() {
	# 无模型依赖的启动自检：用 products/skeleton.agent.json 驱动空基座（M0 退出标准）。
	if [ ! -d cmd/adag ]; then
		skip doctor "cmd/adag 尚未落地（M0.4）；doctor --fake 目标随 cmd/adag 一起生效"
		return 0
	fi
	go run ./cmd/adag doctor --fake
	say "PASS  [doctor] adag doctor --fake"
}

# ---------- 调度 ----------
usage() {
	say "用法：sh scripts/ci-gate.sh [步骤...]"
	say "步骤：$ALL_STEPS"
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
	usage
	exit 0
fi

if [ "$#" -eq 0 ]; then
	steps=$ALL_STEPS
else
	steps="$*"
fi

say "ci-gate 开始：DEP_MODE=$DEP_MODE GOWORK=${GOWORK:-<auto>} windows=$IS_WINDOWS cwd=$(pwd)"
for s in $steps; do
	case " $ALL_STEPS " in
		*" $s "*) ;;
		*) say "未知步骤：$s"; usage; exit 2 ;;
	esac
	printf '\n==> [%s] 开始\n' "$s"
	"step_$s"
done
say ""
say "ci-gate 完成：已执行步骤全部通过（SKIP 行均带原因，见上）。"
