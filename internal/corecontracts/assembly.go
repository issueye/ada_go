package corecontracts

// 装配层契约：让 internal/cli 只依赖接口，不直接 import 组合根 app。
// app 提供实现（app.Assembler），cmd/adag 注入；Gateway 的渲染侧不走这里。

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/plugins/provider"
	"github.com/adag/ada-go/internal/toolkit"
)

// Deps 是一次装配的输入（字段语义见 app.Deps 的历史注释）。
type Deps struct {
	Context host.Context
	HostEnv map[string]string
	// EnvVars 是环境变量模块解析出的用户变量（全局 + 工作区，工作区覆盖），
	// 由宿主注入插件子进程；见 internal/envstore。
	EnvVars     map[string]string
	Secrets     func(name string) bool
	Settings    map[string]map[string]any
	OnWarning   func(string)
	Scripts     []host.Script
	BaseURL     string
	FrontendIDs map[string]bool
}

// Assembly 是装配结果对会话层暴露的最小面。
type Assembly interface {
	PluginInstances() []*host.Instance
	Plugin(id string) (*host.Instance, bool)
	ToolCatalog() *toolkit.Catalog
	// ToolManager 是工具可见性视图与执行路由的唯一入口（工具管理集中化）。
	ToolManager() *toolkit.ToolManager
	ToolInvoker() toolkit.Invoker
	ProviderRegistry() *provider.Registry
	ProviderCount() int
	HookChain() *plugins.HookChain
	PolicyPluginActive() bool
	Skills() []domain.SkillMeta
	SkillConflicts() []string
	ActionDef(pluginID, name string) (host.ActionDef, bool)

	CallHTTPAPI(ctx context.Context, pluginID, method, path string, req json.RawMessage, threadID string) (json.RawMessage, error)
	StreamHTTPAPI(ctx context.Context, pluginID, method, path string, req json.RawMessage, threadID string, onEvent func(host.RouteEvent) error) error
	CheckHTTPAPIStream(pluginID, method, path string) error
	Close(ctx context.Context) error
}

// Assembler 是组合根对会话层暴露的装配入口。
type Assembler interface {
	Assemble(ctx context.Context, p *coreapp.Product, scripts []host.Script, deps Deps) (Assembly, error)
	AssembleWithExtras(ctx context.Context, p *coreapp.Product, base, extras []host.Script, disabled, optIn map[string]bool, deps Deps) (Assembly, error)
	OfficialScripts() ([]host.Script, error)
	FrontendScripts() ([]host.Script, error)
	OfficialRoot() (string, error)
	IsPolicyPlugin(*plugins.Manifest) bool
}

var (
	ErrRouteNotFound         = errors.New("app: 没有匹配的插件 HTTP 路由")
	ErrRouteMethodNotAllowed = errors.New("app: 该路径不支持此 HTTP 方法")
	ErrRouteStreaming        = errors.New("app: 该路由是 SSE，需要流式调用")
	ErrRouteNotStreaming     = errors.New("app: 该路由不是 SSE")
)
