package host

// 环境块合并：把环境变量模块的用户变量叠加到宿主/白名单环境上。

import (
	"runtime"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/binpaths"
)

// mergeEnv 把用户管理环境变量（overrides）叠加到 base：
//   - 同名覆盖；Windows 的变量名大小写不敏感，按大写比较，保留用户给的写法；
//   - 输出按变量名排序，保证确定性（同一个输入永远得到同一份环境块）；
//   - base 里没有 = 的项忽略（不是合法环境变量）。
func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	type entry struct{ name, value string }
	values := make(map[string]entry, len(base)+len(overrides))
	for _, item := range base {
		i := strings.IndexByte(item, '=')
		if i <= 0 {
			continue
		}
		values[envKey(item[:i])] = entry{name: item[:i], value: item[i+1:]}
	}
	for name, value := range overrides {
		values[envKey(name)] = entry{name: name, value: value}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		item := values[key]
		out = append(out, item.name+"="+item.value)
	}
	return out
}

// applyBinPath 把服务程序目录前置到 PATH：有 PATH 时保留原值并去重前置，
// 没有 PATH 时只用这些目录填一个（不让子进程凭空继承宿主 PATH）。
func applyBinPath(env []string, dirs []binpaths.Dir) []string {
	if len(dirs) == 0 {
		return env
	}
	for i, item := range env {
		j := strings.IndexByte(item, '=')
		if j <= 0 || !isPathName(item[:j]) {
			continue
		}
		env[i] = item[:j] + "=" + binpaths.PrependPath(item[j+1:], dirs)
		return env
	}
	return append(env, "PATH="+binpaths.PrependPath("", dirs))
}

// isPathName 判断变量名是不是 PATH（Windows 大小写不敏感）。
func isPathName(name string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(name, "PATH")
	}
	return name == "PATH"
}

// envKey 是环境变量名比较用的规范形式（Windows 大小写不敏感）。
func envKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}
