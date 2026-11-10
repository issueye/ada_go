// Package pluginid 是插件 id 的唯一校验规则（05 §6.2）。
//
// 它放在 internal/common 而不是插件宿主里：common 组（globalconfig、binpaths 等）
// 与 gateway 组的进程管理都要用这条规则，而 common 不能反向依赖 agent_core。
//
// 规则：`^[a-z][a-z0-9-]{1,63}$`，且必须与插件目录名一致（目录名的一致性由发现层
// 另做一次检查，见 internal/agent_core/plugins/discovery）。
package pluginid

import "regexp"

// Pattern 是插件 id 的约束，与 spec/manifest.schema.json 的 id pattern 逐字一致。
var Pattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)

// Valid 报告插件 id 是否合法。
func Valid(id string) bool { return Pattern.MatchString(id) }
