package host

// 环境块合并：把环境变量模块的用户变量叠加到宿主/白名单环境上。

import (
	"runtime"
	"sort"
	"strings"
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

// envKey 是环境变量名比较用的规范形式（Windows 大小写不敏感）。
func envKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}
