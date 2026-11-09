package processmgr

// 被管进程的控制凭据。
//
// 背景：cron 的单例 scheduler 是网关拉起的独立进程，它到点必须经 Dispatcher
// 投递消息。但被管进程的环境会剥掉宿主凭据（ADAG_/ADAGO_），所以它默认拿不到
// 控制令牌。这里的做法是：
//
//   - 网关在**每次启动**被管进程时给它单独签一枚令牌（只带发送范围），
//     经环境变量交给那一个进程；
//   - 令牌身份里写死 PluginID，sender 由网关盖章，进程改不了归属；
//   - 进程停止/注销时回收令牌；同 key 重签会作废旧令牌，崩溃重启不会累积。
//
// 边界：进程拿到的令牌不能反向调 /internal/processes/*（范围不同），
// 也不能冒充别的插件。插件脚本依然看不到任何令牌。

import (
	"strings"
)

// 注入给被管进程的环境变量名（与 supervisor 给 Core 的那组同名同义）。
const (
	// ControlURLEnv 是网关内部端点根地址。
	ControlURLEnv = "ADAGO_GATEWAY_CONTROL_URL"
	// ControlTokenEnv 是被管进程自己的控制令牌。
	ControlTokenEnv = "ADAGO_GATEWAY_CONTROL_TOKEN"
)

// ControlIssuer 给一个被管进程签发/回收发送凭据。
//
// 实现由网关组合根注入（controlPlane）；nil 表示本部署不提供（CLI 直连），
// 此时被管进程没有控制凭据，它只能做没有外部副作用的本地工作。
type ControlIssuer interface {
	// IssueForProcess 返回控制面地址与该进程的令牌（同一进程重签会作废旧令牌）。
	IssueForProcess(owner, name, workspace string) (url, token string)
	// RevokeProcess 回收该进程的令牌。
	RevokeProcess(owner, name, workspace string)
}

// controlKeyOf 是签发键的组成部分：workspace 只对 workspace scope 有意义。
func controlKeyOf(spec ProcessSpec) (owner, name, workspace string) {
	owner = strings.TrimSpace(spec.Owner)
	name = strings.TrimSpace(spec.Name)
	if spec.Scope == ScopeWorkspace {
		workspace = strings.TrimSpace(spec.Workspace)
	}
	return owner, name, workspace
}

// injectControlEnv 把控制凭据追加到已经构造好的环境块上。
//
// 必须在 buildEnv 之后追加：buildEnv 会剥掉 ADAG_/ADAGO_ 前缀的宿主变量，
// 这是为了防止「一次 exec 就拿到宿主凭据」；而这里给的是进程自己的、
// 权限被限制在发送端点的令牌，属于显式授予而不是继承。
func injectControlEnv(env []string, url, token string) []string {
	url = strings.TrimSpace(url)
	token = strings.TrimSpace(token)
	if url == "" || token == "" {
		return env
	}
	out := make([]string, 0, len(env)+2)
	out = append(out, env...)
	out = append(out, ControlURLEnv+"="+url, ControlTokenEnv+"="+token)
	return out
}
