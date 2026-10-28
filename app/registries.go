package app

// 前端动作与前端贡献的目录（M4）：装配期一次性登记，冻结后只读。
//
// Actions 是 action.invoke 的数据面：Gateway 用它把界面动作绑定解析到插件后端动作。
// Frontend 记录 backend 插件自带的 frontend 段（如有），保证 staging 的贡献不因
// 缺目录而失败；frontend-only 插件不进本侧装配，由 Gateway 渲染桥负责。

import (
	"fmt"
	"strings"
	"sync"

	"github.com/adag/ada-go/internal/plugins/host"
)

// actionRegistry 是 host.ActionRegistry 的实现（方法名 Add*：Go 侧目录是
// host 契约的消费者，不是「平行 Go 注册入口」，命名避开门禁正则）。
type actionRegistry struct {
	mu   sync.RWMutex
	byID map[string]map[string]host.ActionDef
}

func newActionRegistry() *actionRegistry {
	return &actionRegistry{byID: map[string]map[string]host.ActionDef{}}
}

var _ host.ActionRegistry = (*actionRegistry)(nil)
var _ host.PluginRegistry = (*actionRegistry)(nil)

func (r *actionRegistry) AddAction(reg host.ActionRegistration) error {
	if reg.PluginID == "" || reg.Def.Name == "" {
		return fmt.Errorf("app: 动作登记缺少 pluginId/name")
	}
	if reg.Def.Handler.Slot == 0 {
		return fmt.Errorf("app: 动作 %s/%s 没有脚本句柄", reg.PluginID, reg.Def.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	byName := r.byID[reg.PluginID]
	if byName == nil {
		byName = map[string]host.ActionDef{}
	}
	if _, dup := byName[reg.Def.Name]; dup {
		return fmt.Errorf("app: 插件 %s 重复登记动作 %q", reg.PluginID, reg.Def.Name)
	}
	byName[reg.Def.Name] = reg.Def
	r.byID[reg.PluginID] = byName
	return nil
}

func (r *actionRegistry) RemovePlugin(pluginID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[pluginID]; !ok {
		return 0
	}
	n := len(r.byID[pluginID])
	delete(r.byID, pluginID)
	return n
}

// Lookup 按插件与动作名取定义。
func (r *actionRegistry) Lookup(pluginID, name string) (host.ActionDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.byID[pluginID][name]
	return def, ok
}

// promptRegistry 是 host.PromptRegistry 的实现：登记各插件 registerPrompt 的贡献，
// 供引擎的 PromptSource（安全前言之后的插件人格段）读取。
type promptRegistry struct {
	mu    sync.RWMutex
	byID  map[string][]host.PromptDef
	order []string
}

func newPromptRegistry() *promptRegistry {
	return &promptRegistry{byID: map[string][]host.PromptDef{}}
}

var _ host.PromptRegistry = (*promptRegistry)(nil)
var _ host.PluginRegistry = (*promptRegistry)(nil)

func (r *promptRegistry) AddPrompt(reg host.PromptRegistration) error {
	if reg.PluginID == "" {
		return fmt.Errorf("app: 提示词登记缺少 pluginId")
	}
	if strings.TrimSpace(reg.Def.Content) == "" {
		return fmt.Errorf("app: 插件 %s 的提示词 %q 内容为空", reg.PluginID, reg.Def.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[reg.PluginID]; !dup {
		r.order = append(r.order, reg.PluginID)
	}
	r.byID[reg.PluginID] = append(r.byID[reg.PluginID], reg.Def)
	return nil
}

func (r *promptRegistry) RemovePlugin(pluginID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[pluginID]; !ok {
		return 0
	}
	n := len(r.byID[pluginID])
	delete(r.byID, pluginID)
	for i, id := range r.order {
		if id == pluginID {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return n
}

// Fragments 按登记顺序展开全部提示词片段（引擎侧再按 priority/pluginID 稳定排序）。
func (r *promptRegistry) Fragments() []host.PromptDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]host.PromptDef, 0, 8)
	for _, id := range r.order {
		out = append(out, r.byID[id]...)
	}
	return out
}

// frontendRegistry 是 host.FrontendRegistry 的实现：只记录贡献，供诊断与测试。
type frontendRegistry struct {
	mu    sync.RWMutex
	byID  map[string]host.FrontendContribution
	order []string
}

func newFrontendRegistry() *frontendRegistry {
	return &frontendRegistry{byID: map[string]host.FrontendContribution{}}
}

var _ host.FrontendRegistry = (*frontendRegistry)(nil)
var _ host.PluginRegistry = (*frontendRegistry)(nil)

func (r *frontendRegistry) AddFrontend(reg host.FrontendRegistration) error {
	if reg.PluginID == "" {
		return fmt.Errorf("app: 前端贡献缺少 pluginId")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[reg.PluginID]; !dup {
		r.order = append(r.order, reg.PluginID)
	}
	r.byID[reg.PluginID] = reg.Contribution
	return nil
}

func (r *frontendRegistry) RemovePlugin(pluginID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[pluginID]; !ok {
		return 0
	}
	delete(r.byID, pluginID)
	for i, id := range r.order {
		if id == pluginID {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return 1
}
