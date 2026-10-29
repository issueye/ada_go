package app

// 插件动态 HTTP 路由（route）的运行时目录与调用入口。
//
// 插件在装配期登记 (method, path, handler)；前端插件 / 浏览器脚本经 Gateway 的
// /ext/{id}/api/* 调用，Gateway 通过 Core RPC `plugin.http` 转发到这里。
// 路由按插件隔离：一个插件只能命中自己登记的路由。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/plugins/host"
)

// ErrRouteNotFound / ErrRouteMethodNotAllowed 供 Core RPC 映射 404/405。
var (
	ErrRouteNotFound         = errors.New("app: 没有匹配的插件 HTTP 路由")
	ErrRouteMethodNotAllowed = errors.New("app: 该路径不支持此 HTTP 方法")
)

type routeRegistry struct {
	mu       sync.RWMutex
	byPlugin map[string]map[string]map[string]host.RouteDef // plugin → method → path
}

func newRouteRegistry() *routeRegistry {
	return &routeRegistry{byPlugin: map[string]map[string]map[string]host.RouteDef{}}
}

// AddRoute 实现 host.RouteRegistry。同一插件同一方法同一路径重复登记是装配错误。
func (r *routeRegistry) AddRoute(reg host.RouteRegistration) error {
	id := strings.TrimSpace(reg.PluginID)
	method := strings.ToUpper(strings.TrimSpace(reg.Def.Method))
	path := strings.TrimSpace(reg.Def.Path)
	if id == "" {
		return errors.New("app: HTTP 路由缺少插件 id")
	}
	if method != "GET" && method != "POST" {
		return fmt.Errorf("app: 插件 %s 的 HTTP 路由 %q 方法非法: %s", id, path, method)
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return fmt.Errorf("app: 插件 %s 的 HTTP 路由路径非法: %q", id, path)
	}
	// 与 Gateway 一致：除根路径外去掉末尾斜杠，避免 "/x/" 永远匹配不到 "/x"。
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	byMethod := r.byPlugin[id]
	if byMethod == nil {
		byMethod = map[string]map[string]host.RouteDef{}
		r.byPlugin[id] = byMethod
	}
	routes := byMethod[method]
	if routes == nil {
		routes = map[string]host.RouteDef{}
		byMethod[method] = routes
	}
	if _, dup := routes[path]; dup {
		return fmt.Errorf("app: 插件 %s 重复登记 HTTP 路由 %s %s", id, method, path)
	}
	routes[path] = reg.Def
	return nil
}

// match 先精确匹配，再匹配最长前缀路由（path 以 "/*" 结尾）。
// 路径存在但方法不支持时返回 ErrRouteMethodNotAllowed。
func (r *routeRegistry) match(pluginID, method, path string) (host.RouteDef, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byMethod, ok := r.byPlugin[pluginID]
	if !ok {
		return host.RouteDef{}, ErrRouteNotFound
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	path = normalizeRoutePath(path)
	if routes := byMethod[method]; routes != nil {
		if def, ok := routes[path]; ok {
			return def, nil
		}
		best := ""
		for candidate := range routes {
			if routePrefixMatch(candidate, path) && len(candidate) > len(best) {
				best = candidate
			}
		}
		if best != "" {
			return routes[best], nil
		}
	}
	// 同一路径登记在别的方法下 → 405，而不是 404。
	for other, routes := range byMethod {
		if other == method {
			continue
		}
		if _, ok := routes[path]; ok {
			return host.RouteDef{}, ErrRouteMethodNotAllowed
		}
		for candidate := range routes {
			if routePrefixMatch(candidate, path) {
				return host.RouteDef{}, ErrRouteMethodNotAllowed
			}
		}
	}
	return host.RouteDef{}, ErrRouteNotFound
}

func normalizeRoutePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}

func routePrefixMatch(route, path string) bool {
	if !strings.HasSuffix(route, "/*") {
		return false
	}
	prefix := strings.TrimSuffix(route, "/*")
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// CallHTTPAPI 执行一次插件动态路由。req 是交给插件 handler 的 JSON 请求包
// （method/path/query/body 等，由 Core RPC 组装）。
func (a *Assembly) CallHTTPAPI(ctx context.Context, pluginID, method, path string, req json.RawMessage, threadID string) (json.RawMessage, error) {
	if a == nil || a.Routes == nil {
		return nil, ErrRouteNotFound
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return nil, errors.New("app: 缺少插件 id")
	}
	def, err := a.Routes.match(pluginID, method, path)
	if err != nil {
		return nil, err
	}
	inst, ok := a.Plugin(pluginID)
	if !ok {
		return nil, fmt.Errorf("app: 插件 %s 未加载", pluginID)
	}
	hc := inst.BaseContext()
	hc.PluginID = pluginID
	hc.ThreadID = strings.TrimSpace(threadID)
	hc.Signal = ctx
	hc.Now = time.Now().UTC()
	return inst.CallRoute(ctx, def.Handler, req, hc)
}
