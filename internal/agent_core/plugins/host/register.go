package host

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	"github.com/adag/ada-go/internal/agent_core/plugins"
	"github.com/adag/ada-go/internal/common/pluginapi"
	"github.com/adag/ada-go/internal/common/ports"
)

// buildAgentAPI 构造 agent 对象。owner 每次调用时求值，因此同一个对象既可以是
// 入口工厂的参数（owner 直接返回实例），也可以是 agent:host 的模块绑定
// （owner 从 capsState 里取实例：模块体在装配早期就求值了，那时实例还没接上）。
func buildAgentAPI(rt *mojsRuntime, owner func() *Instance) moejs.Value {
	r := rt.Realm()
	obj := r.NewObject()
	put := func(name string, fn engine.NativeFunc) {
		v := rt.Function(name, 1, fn)
		_ = obj.SetProp(r, r.KeyFromGoString(name), v)
	}
	inst := func() (*Instance, *Staging, error) {
		p := owner()
		if p == nil {
			return nil, nil, r.TypeError("host: agent API 在装配期之外被调用（宿主还没接上实例）")
		}
		if p.staging == nil {
			return nil, nil, r.TypeError("host: agent API 已冻结：装配期之外不能再注册贡献（06 §6.5）")
		}
		return p, p.staging, nil
	}
	frozen := func(p *Instance, what string) bool {
		if p == nil || !p.frozen {
			return false
		}
		// 06 §6.5：运行期再 register* 只记警告，不改变当前目录。
		p.warn(fmt.Sprintf("插件 %s 在装配完成后调用了 %s，已忽略（要生效必须重载）", p.id, what))
		return true
	}

	put("registerTool", func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		p, st, err := inst()
		if err != nil {
			return moejs.Undefined(), err
		}
		if frozen(p, "registerTool") {
			return moejs.Undefined(), nil
		}
		defV := moejs.Arg(args, 0)
		if !defV.IsObject() {
			return moejs.Undefined(), r.TypeError("registerTool(def): def 必须是对象（05 §6.3）")
		}
		var def ToolDef
		if err := decodeJSValue(rt, defV, &def); err != nil {
			return moejs.Undefined(), r.TypeError("registerTool(%s): %v", def.Name, err)
		}
		fn, _, err := jsFunc(rt, defV, "execute", "handler")
		if err != nil {
			return moejs.Undefined(), r.TypeError("registerTool(%s): %v", def.Name, err)
		}
		if strings.TrimSpace(def.Name) == "" {
			return moejs.Undefined(), r.TypeError("registerTool: 缺少 name（05 §6.3）")
		}
		slot, err := st.add(slotKindTool, def.Name, fn)
		if err != nil {
			return moejs.Undefined(), r.TypeError("%v", err)
		}
		def.Handler = HandlerRef{Slot: slot}
		st.data.Tools = append(st.data.Tools, def)
		return moejs.Undefined(), nil
	})

	put("registerProvider", func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		p, st, err := inst()
		if err != nil {
			return moejs.Undefined(), err
		}
		if frozen(p, "registerProvider") {
			return moejs.Undefined(), nil
		}
		defV := moejs.Arg(args, 0)
		if !defV.IsObject() {
			return moejs.Undefined(), r.TypeError("registerProvider(def): def 必须是对象")
		}
		var def ProviderDef
		if err := decodeJSValue(rt, defV, &def); err != nil {
			return moejs.Undefined(), r.TypeError("registerProvider(%s): %v", def.ID, err)
		}
		fn, _, err := jsFunc(rt, defV, "stream")
		if err != nil {
			return moejs.Undefined(), r.TypeError("registerProvider(%s): %v", def.ID, err)
		}
		slot, err := st.add(slotKindProvider, def.ID, fn)
		if err != nil {
			return moejs.Undefined(), r.TypeError("%v", err)
		}
		def.Stream = HandlerRef{Slot: slot}
		st.data.Providers = append(st.data.Providers, def)
		return moejs.Undefined(), nil
	})

	put("registerPrompt", func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		p, st, err := inst()
		if err != nil {
			return moejs.Undefined(), err
		}
		if frozen(p, "registerPrompt") {
			return moejs.Undefined(), nil
		}
		defV := moejs.Arg(args, 0)
		if !defV.IsObject() {
			return moejs.Undefined(), r.TypeError("registerPrompt(def): def 必须是对象")
		}
		var def PromptDef
		if err := decodeJSValue(rt, defV, &def); err != nil {
			return moejs.Undefined(), r.TypeError("registerPrompt(%s): %v", def.ID, err)
		}
		st.data.Prompts = append(st.data.Prompts, def)
		return moejs.Undefined(), nil
	})

	put("registerAction", func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		p, st, err := inst()
		if err != nil {
			return moejs.Undefined(), err
		}
		if frozen(p, "registerAction") {
			return moejs.Undefined(), nil
		}
		defV := moejs.Arg(args, 0)
		if !defV.IsObject() {
			return moejs.Undefined(), r.TypeError("registerAction(def): def 必须是对象")
		}
		var def ActionDef
		if err := decodeJSValue(rt, defV, &def); err != nil {
			return moejs.Undefined(), r.TypeError("registerAction(%s): %v", def.Name, err)
		}
		fn, _, err := jsFunc(rt, defV, "execute", "handler")
		if err != nil {
			return moejs.Undefined(), r.TypeError("registerAction(%s): %v", def.Name, err)
		}
		slot, err := st.add(slotKindAction, def.Name, fn)
		if err != nil {
			return moejs.Undefined(), r.TypeError("%v", err)
		}
		def.Handler = HandlerRef{Slot: slot}
		st.data.Actions = append(st.data.Actions, def)
		return moejs.Undefined(), nil
	})

	// route：登记一条动态 HTTP 路由（GET/POST）。
	// 兼容两种调用：
	//   route("GET", "/path", handler)
	//   route({ method: "POST", path: "/path", handler })
	registerHTTP := func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		p, st, err := inst()
		if err != nil {
			return moejs.Undefined(), err
		}
		if frozen(p, "route") {
			return moejs.Undefined(), nil
		}
		method := "GET"
		var path string
		sse := false
		handler := moejs.Undefined()
		first := moejs.Arg(args, 0)
		if first.IsObject() && !engine.IsCallable(first) {
			if v, gerr := rt.Get(first, "method"); gerr == nil && !v.IsNullish() {
				method = strings.ToUpper(strings.TrimSpace(v.String()))
			}
			if v, gerr := rt.Get(first, "path"); gerr == nil {
				path = strings.TrimSpace(v.String())
			}
			if v, gerr := rt.Get(first, "sse"); gerr == nil {
				sse = v.IsTrue()
			}
			if v, gerr := rt.Get(first, "handler"); gerr == nil && engine.IsCallable(v) {
				handler = v
			} else if v, gerr := rt.Get(first, "execute"); gerr == nil && engine.IsCallable(v) {
				handler = v
			}
		} else {
			method = strings.ToUpper(strings.TrimSpace(first.String()))
			path = strings.TrimSpace(moejs.Arg(args, 1).String())
			handler = moejs.Arg(args, 2)
		}
		if method != "GET" && method != "POST" {
			return moejs.Undefined(), r.TypeError("route: method 只能是 GET 或 POST（7.6）")
		}
		if sse && method != "GET" {
			return moejs.Undefined(), r.TypeError("route: sse 路由只支持 GET（EventSource 是 GET）")
		}
		if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.Contains(path, "?") {
			return moejs.Undefined(), r.TypeError("route: path 必须以 / 开头，且不能包含 .. 或查询串（7.6）")
		}
		if !engine.IsCallable(handler) {
			return moejs.Undefined(), r.TypeError("route(%s %s): handler 必须是函数", method, path)
		}
		slot, err := st.add(slotKindRoute, routeIdent(method, path), handler)
		if err != nil {
			return moejs.Undefined(), r.TypeError("%v", err)
		}
		st.data.Routes = append(st.data.Routes, RouteDef{
			Method: method, Path: path, SSE: sse, Handler: HandlerRef{Slot: slot},
		})
		return moejs.Undefined(), nil
	}
	put("route", registerHTTP)

	// every：登记一条通用周期任务（不是 cron：表达式、任务存储、运行历史归插件）。
	// 宿主用独立 goroutine 按固定间隔调用 handler，每次调用注入新的 Context。
	put("every", func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		p, st, err := inst()
		if err != nil {
			return moejs.Undefined(), err
		}
		if frozen(p, "every") {
			return moejs.Undefined(), nil
		}
		ms, ok := jsInt(moejs.Arg(args, 0))
		if !ok || ms < 1000 || ms > 3600000 {
			return moejs.Undefined(), r.TypeError("every(ms, fn): ms 必须是 1000…3600000 的整数")
		}
		fn := moejs.Arg(args, 1)
		if !engine.IsCallable(fn) {
			return moejs.Undefined(), r.TypeError("every(ms, fn): fn 必须是函数")
		}
		if len(st.data.Timers) >= 8 {
			return moejs.Undefined(), r.TypeError("every: 每个插件最多 8 条周期任务")
		}
		ident := fmt.Sprintf("timer#%d", len(st.data.Timers))
		slot, err := st.add(slotKindTimer, ident, fn)
		if err != nil {
			return moejs.Undefined(), r.TypeError("%v", err)
		}
		st.data.Timers = append(st.data.Timers, TimerDef{IntervalMS: int(ms), Handler: HandlerRef{Slot: slot}})
		return moejs.Undefined(), nil
	})

	put("on", func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		p, st, err := inst()
		if err != nil {
			return moejs.Undefined(), err
		}
		if frozen(p, "on") {
			return moejs.Undefined(), nil
		}
		point := strings.TrimSpace(moejs.Arg(args, 0).String())
		fn := moejs.Arg(args, 1)
		if _, ok := pluginapi.LookupHookPoint(point); !ok {
			return moejs.Undefined(), r.TypeError("on(%q): 未知钩子点位（06 §6.6 只有 15 个）", point)
		}
		if !engine.IsCallable(fn) {
			return moejs.Undefined(), r.TypeError("on(%q, fn): fn 必须是函数", point)
		}
		def := HookDef{Point: point}
		if opts := moejs.Arg(args, 2); opts.IsObject() {
			if v, err := rt.Get(opts, "priority"); err == nil && !v.IsNullish() {
				if n, ok := jsInt(v); ok {
					def.Priority = int(n)
				}
			}
			if v, err := rt.Get(opts, "action"); err == nil && !v.IsNullish() {
				def.Action = v.String()
			}
		}
		if def.Action == "" {
			def.Action = string(pluginapi.ActionObserve)
		}
		ordinal := st.nextHookOrdinal(point)
		slot, err := st.add(slotKindHook, fmt.Sprintf("%s#%d", point, ordinal), fn)
		if err != nil {
			return moejs.Undefined(), r.TypeError("%v", err)
		}
		def.Handler = HandlerRef{Slot: slot}
		st.data.Hooks = append(st.data.Hooks, def)
		return moejs.Undefined(), nil
	})

	return engine.ObjectValue(obj)
}

// newHookHandler 把一条已注册的钩子变成钩子链能直接调用的处理器（06 §6.6）。
//
// 处理器本身不做超时裁决：HookChain 负责点位等级与失败方向，本实例只负责
// 把调用排进 Runtime 队列、按 Context 注入身份与能力、把脚本结果结算成 HookOutcome。
func (p *Instance) newHookHandler(def HookDef) (*plugins.HookHandler, error) {
	if def.Handler.Slot == 0 {
		return nil, fmt.Errorf("钩子 %s 没有脚本句柄", def.Point)
	}
	point, ok := pluginapi.LookupHookPoint(def.Point)
	if !ok {
		return nil, fmt.Errorf("未知钩子点位 %q", def.Point)
	}
	action := pluginapi.HookAction(def.Action)
	if action == "" {
		action = pluginapi.ActionObserve
	}
	if !point.AllowsAction(action, p.caps.Trust == pluginapi.TrustBuiltin) {
		return nil, fmt.Errorf("插件 %s（trust=%s）不能在 %s 上使用 %s", p.id, p.caps.Trust, point.Name, action)
	}
	declared, ok := p.caps.Hooks[def.Point]
	if !ok {
		return nil, fmt.Errorf("清单未声明点位 %q", def.Point)
	}
	if action.Rank() > declared.Rank() {
		return nil, fmt.Errorf("点位 %s 声明到 %s，钩子要求 %s", def.Point, declared, action)
	}
	ref := def.Handler
	return &plugins.HookHandler{
		PluginID:  p.id,
		Trust:     p.caps.DomainTrust(),
		Point:     ports.HookPoint(def.Point),
		Priority:  def.Priority,
		MaxAction: declared,
		Fn: func(ctx context.Context, evt ports.HookEvent) (ports.HookOutcome, error) {
			hc := p.hookContext(ctx, evt)
			return p.CallHook(ctx, ref, evt, hc)
		},
	}, nil
}

// hookContext 把一次钩子事件摊成插件侧的 Context。
func (p *Instance) hookContext(ctx context.Context, evt ports.HookEvent) Context {
	hc := p.hc
	hc.PluginID = p.id
	if hc.Workspace == "" {
		hc.Workspace = p.caps.WorkspaceRoot
	}
	hc.ThreadID = evt.ThreadID
	hc.RunID = evt.RunID
	hc.CallID = evt.CallID
	hc.Now = time.Now().UTC()
	hc.Signal = ctx
	return hc
}
