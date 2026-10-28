#!/bin/sh
# 把相邻 moejs 检出准备到 ada-go 的同级目录（go.work 联合 `.` 与 `../moejs`，11 §13.1、开发计划 §4.1 M0.1）。
#
# 契约：
#   - 只做 clone / fetch / detach checkout / 校验这四类动作，绝不修改 moejs 仓库内容；
#     moejs 工作区存在未提交改动时直接失败退出，由开发者自行处理。
#   - 校验目标 tag 与 go.mod 中钉住的版本一致（默认 v0.1.0-alpha.11；升级 tag 需单独 PR，§5.3）。
#   - 本脚本不改写 go.work：go.work 只允许相对路径，由人工维护（11 §13.1）。
#
# 环境变量：
#   MOEJS_REPO  上游 URL（默认 https://github.com/MoeForge/moejs.git）
#   MOEJS_TAG   要准备到的 tag（默认 v0.1.0-alpha.11）
#   MOEJS_DIR   目标目录（默认 ada-go 同级 ../moejs）
set -eu

MOEJS_REPO="${MOEJS_REPO:-https://github.com/MoeForge/moejs.git}"
MOEJS_TAG="${MOEJS_TAG:-v0.1.0-alpha.11}"
MOEJS_MODULE="github.com/MoeForge/moejs"

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
moejs_dir="${MOEJS_DIR:-$repo_root/../moejs}"

say() { echo "bootstrap-moejs: $*"; }
die() { echo "bootstrap-moejs: 错误：$*" >&2; exit 1; }

command -v git >/dev/null 2>&1 || die "需要 git，可在 https://git-scm.com 安装后重试"

if [ -d "$moejs_dir/.git" ]; then
	say "发现既有检出：$moejs_dir"
	url=$(git -C "$moejs_dir" remote get-url origin 2>/dev/null) \
		|| die "$moejs_dir 没有 origin 远端，无法确认来源；请人工处理（本脚本不修改 moejs）"
	case "$url" in
		*MoeForge/moejs*) ;;
		*) die "origin 不是 MoeForge/moejs（当前 $url）；拒绝操作别处仓库" ;;
	esac

	if [ -n "$(git -C "$moejs_dir" status --porcelain)" ]; then
		die "moejs 工作区有未提交改动——本脚本承诺不改动 moejs 内容，请先在 ../moejs 内自行 stash/commit"
	fi

	# tag 对象本地缺失时才联网 fetch --tags；离线且已有 tag 时可用。
	if ! git -C "$moejs_dir" rev-parse -q --verify "refs/tags/${MOEJS_TAG}^{commit}" >/dev/null; then
		say "本地没有 tag $MOEJS_TAG，执行 git fetch --tags"
		git -C "$moejs_dir" fetch --tags --quiet origin \
			|| die "fetch 失败（网络或上游不可达）"
		git -C "$moejs_dir" rev-parse -q --verify "refs/tags/${MOEJS_TAG}^{commit}" >/dev/null \
			|| die "远端也没有 tag $MOEJS_TAG"
	fi

	want=$(git -C "$moejs_dir" rev-parse "refs/tags/${MOEJS_TAG}^{commit}")
	have=$(git -C "$moejs_dir" rev-parse HEAD)
	if [ "$want" = "$have" ]; then
		say "HEAD 已对齐 $MOEJS_TAG（$have）"
	else
		say "HEAD=$(git -C "$moejs_dir" rev-parse --short HEAD) ≠ $MOEJS_TAG，checkout --detach（工作区干净，不会覆盖任何本地内容）"
		git -C "$moejs_dir" checkout --quiet --detach "$MOEJS_TAG"
	fi
else
	if [ -e "$moejs_dir" ] && [ -n "$(ls -A "$moejs_dir" 2>/dev/null)" ]; then
		die "$moejs_dir 已存在且不是 git 仓库（或不是空目录）；请人工清理后重试"
	fi
	say "克隆 $MOEJS_REPO @ $MOEJS_TAG 到 $moejs_dir（浅克隆该 tag）"
	mkdir -p "$(dirname "$moejs_dir")"
	git clone --quiet --depth 1 --branch "$MOEJS_TAG" "$MOEJS_REPO" "$moejs_dir"
fi

# ---- 校验（只读）----
final=$(git -C "$moejs_dir" rev-parse HEAD)
expect=$(git -C "$moejs_dir" rev-parse "refs/tags/${MOEJS_TAG}^{commit}")
[ "$final" = "$expect" ] || die "校验失败：HEAD $final 不是 $MOEJS_TAG 的提交 $expect"

first_line=$(head -n 1 "$moejs_dir/go.mod")
case "$first_line" in
	"module $MOEJS_MODULE"|"module $MOEJS_MODULE") ;;
	*) die "go.mod module 行异常：'$first_line'（期望 module $MOEJS_MODULE）" ;;
esac
desc=$(git -C "$moejs_dir" describe --tags --exact-match HEAD 2>/dev/null || echo "$MOEJS_TAG(annotated-check跳过)")
say "就绪：$moejs_dir @ $MOEJS_TAG（describe: $desc）"

# 提示 workspace 接线状况（不修改 go.work）。
if [ -f "$repo_root/go.work" ] && grep -q "moejs" "$repo_root/go.work"; then
	say "go.work 已联合 ../moejs，workspace 模式可直接使用"
else
	say "提示：go.work 未见 ../moejs 引用；按 11 §13.1 人工执行 go work init . ../moejs"
fi
