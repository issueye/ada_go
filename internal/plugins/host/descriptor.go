package host

// 本文件是「已提交贡献 → 引擎描述符」的查询面（02 §5.4、06 §6.4）。
//
// runtime.go 的预算档位要按工具描述符里的 timeoutMs 取软预算、按钩子点位取覆盖值；
// 这两件事都只读已提交的 Contribution，不碰 Runtime，因此可以单独成文件：
// 装配期（staging）还在收集时，这里返回「没有」而不是半成品。

import (
	"time"

	"github.com/adag/ada-go/internal/domain"
)

// descriptorOf 返回已提交工具的描述符（预算与元数据都取它）。
//
// 名字对不上（例如调用方拿的是旧句柄）时返回 false，调用方回落到默认预算，
// 而不是把一次调用判成「工具不存在」——句柄槽位才是权威，名字只用于档位。
func (p *Instance) descriptorOf(name string) (domain.Descriptor, bool) {
	if name == "" {
		return domain.Descriptor{}, false
	}
	for _, t := range p.toolsOf() {
		if t.Name == name {
			return t.Descriptor(p.id), true
		}
	}
	return domain.Descriptor{}, false
}

// hookTimeoutOf 返回某个钩子点位的预算覆盖值。
//
// 契约里没有点位级超时：06 §6.6 只给了「默认 200ms、上限 2s」，清单也没有 per-hook
// timeout 字段（plugins.Manifest 的 backend.hooks 只有动作等级）。因此这里恒为 false，
// 软预算统一走 Budgets.Hook，并被 Budgets.HookCap 夹住。保留这个读取点是为了让
// runtime.go 的预算分档只有一个入口：将来契约允许点位级覆盖时，只改这里。
func (p *Instance) hookTimeoutOf(name string) (time.Duration, bool) {
	_ = name
	return 0, false
}

// toolDefOf 返回已提交的工具定义（调用参数校验与诊断用）。
func (p *Instance) toolDefOf(name string) (ToolDef, bool) {
	for _, t := range p.toolsOf() {
		if t.Name == name {
			return t, true
		}
	}
	return ToolDef{}, false
}
