#!/bin/sh
# 用暂存的真实模型跑 CLI：读取 .adago/test-model.json 的 provider/model/baseUrl。
# 密钥由 CLI 自己从 ~/.adago/secrets.toml 或环境变量读取，本脚本不碰密钥。
#
# 用法：
#   sh scripts/real-model.sh print --workspace <dir> --prompt "hello"
#   sh scripts/real-model.sh chat  --workspace <dir>
#
# 显式传的 --model / --base-url 会覆盖文件里的值（放在后面，flag 后者生效）。
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

cfg=".adago/test-model.json"
if [ ! -f "$cfg" ]; then
	echo "real-model: 缺少 $cfg（先写入 model/baseUrl）" >&2
	exit 1
fi

json_value() {
	sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$cfg" | head -n 1
}

model=$(json_value model)
base_url=$(json_value baseUrl)
provider=$(json_value provider)
if [ -z "$model" ] || [ -z "$base_url" ]; then
	echo "real-model: $cfg 缺少 model 或 baseUrl" >&2
	exit 1
fi
if [ -z "$provider" ]; then
	provider=openai
fi

if [ "$#" -lt 1 ]; then
	echo "用法：sh scripts/real-model.sh <print|chat> [adag 参数...]" >&2
	exit 2
fi
cmd=$1
shift
case "$cmd" in
	print | chat) ;;
	*)
		echo "real-model: 只支持 print / chat（当前 $cmd）" >&2
		exit 2
		;;
esac

exec go run ./cmd/adag "$cmd" --provider "$provider" --model "$model" --base-url "$base_url" "$@"
