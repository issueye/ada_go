package web

// /frontend-registry —— SPA 前端贡献注册表（dev-plan §6，Go ↔ JS 唯一接口）。
//
// 数据由 frontend.Bridge 提供（web.RegistrySource），包含当前工作区已启用
// 插件的插槽/页面/导航/概览卡片/动作声明；component URL 带内容指纹。
// 浏览器侧 host.mjs 拉取本端点构建响应式注册表，并在 /events/workspace 的
// evt.plugin.changed 到达时重拉。

import (
	"encoding/json"
	"net/http"
)

// RegistrySlot 是一条插槽贡献。
type RegistrySlot struct {
	Slot      string `json:"slot"`
	Component string `json:"component"` // /ext/{id}/static/xxx.vue?v=<指纹>
	Order     int    `json:"order,omitempty"`
}

// RegistryPage 是一条插件页面贡献。
type RegistryPage struct {
	Slug      string `json:"slug"`
	Component string `json:"component"`
	Title     string `json:"title,omitempty"`
}

// RegistryNav 是一条侧栏导航贡献。
type RegistryNav struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	Icon  string `json:"icon,omitempty"`
	Order int    `json:"order,omitempty"`
}

// RegistryDashboard 是一条概览卡片贡献。
type RegistryDashboard struct {
	Component string `json:"component"`
	Order     int    `json:"order,omitempty"`
}

// RegistryAction 是动作桥的客户端可见声明（写动作的 confirm 文案由宿主保证先确认）。
type RegistryAction struct {
	Confirm string `json:"confirm,omitempty"`
	// Read 为 true 时该绑定是只读预取动作：客户端必须走 GET（POST 是写动作通道）。
	Read bool `json:"read,omitempty"`
}

// RegistryPlugin 是一个已启用插件的前端贡献集合。
type RegistryPlugin struct {
	ID        string                    `json:"id"`
	Name      string                    `json:"name"`
	Trust     string                    `json:"trust,omitempty"`
	Version   string                    `json:"version,omitempty"`
	Slots     []RegistrySlot            `json:"slots,omitempty"`
	Pages     []RegistryPage            `json:"pages,omitempty"`
	Nav       []RegistryNav             `json:"nav,omitempty"`
	Dashboard []RegistryDashboard       `json:"dashboard,omitempty"`
	Actions   map[string]RegistryAction `json:"actions,omitempty"`
	// Styles 是插件全局样式（assets 内 CSS 文件的带指纹 URL）；宿主按插件
	// 挂载/摘除（禁用即下线），与 SFC scoped 样式互补。
	Styles []string `json:"styles,omitempty"`
}

// RegistrySource 由插件桥实现：注册表数据源。
type RegistrySource interface {
	// Registry 返回当前工作区已启用插件的前端贡献（按插件 id 稳定排序）。
	Registry(r *http.Request) []RegistryPlugin
	// RegistryGen 是注册表代数（启停变更递增），作为响应里的 rev。
	RegistryGen() int64
}

// registryPayload 是 /frontend-registry 的响应信封。
type registryPayload struct {
	Rev     int64            `json:"rev"`
	Plugins []RegistryPlugin `json:"plugins"`
}

func (s *Server) handleFrontendRegistry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	payload := registryPayload{Plugins: []RegistryPlugin{}}
	if s.cfg.Registry != nil {
		payload.Rev = s.cfg.Registry.RegistryGen()
		payload.Plugins = s.cfg.Registry.Registry(r)
		if payload.Plugins == nil {
			payload.Plugins = []RegistryPlugin{}
		}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}
