package host

// 本文件是「agent 对象与结算」（05 §6.3 的 AgentAPI、06 §6.5 的装配期注册）：
//
//   - 入口工厂收到的 agent 对象、以及与它同一份形状的 agent:host 模块绑定；
//   - 每次调用的入参（payload + ctx），ctx 按能力安装（没声明的属性不存在）；
//   - 返回值结算：工具结果、钩子动作、前端动作、路由响应；
//   - 钩子处理器：把 HookChain 的一次调用排进本实例的 Runtime 队列。
//
// 边界：本文件只在 owner goroutine 上被调用（Runtime 的值不跨 goroutine），
// 跨出去的东西一律是 JSON 字节或宿主长调用（p.hostAsync）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/ports"
)

// runFactory 执行入口工厂并把贡献结算进 staging（06 §6.5）。
//
// 入口的默认导出必须是函数（05 §6.3）；返回 Promise 时等它结算，
// 不结算按「Promise 永不结算」报错，而不是让装配挂住或者静默成功。
func (p *Instance) runFactory(st *Staging) error {
	if p.rt == nil || p.loop == nil {
		return fmt.Errorf("host: 插件 %s 尚未装配运行时", p.id)
	}
	// 让 agent:host 模块的绑定在工厂运行期间能找到本实例（模块体在 loadEntry 时已求值，
	// 但它的导出函数要等到这时才被调用）。
	if cs, ok := stateFor(p.rt); ok {
		cs.inst = p
	}
	api := buildAgentAPI(p.rt, func() *Instance { return p })

	var (
		settled    bool
		factoryErr error
	)
	call := &call{kind: callInit}
	err := p.loop.Run(func(rt *mojsRuntime) error {
		factory, ok := rt.Export("default")
		if !ok || !engine.IsCallable(factory) {
			return fmt.Errorf("host: 插件 %s 的入口没有 `export default function (agent) {}`（05 §6.3）", p.id)
		}
		res, cerr := rt.CallFunction(factory, moejs.Undefined(), api)
		if cerr != nil {
			return cerr
		}
		r := rt.Realm()
		if !res.IsObject() {
			// 工厂没有返回 Promise（05 §6.3 允许 void）：直接算结算完成。
			settled = true
			return nil
		}
		then, terr := r.GetMethod(res, r.KeyFromGoString("then"))
		if terr != nil {
			return terr
		}
		if !engine.IsCallable(then) {
			settled = true
			return nil
		}
		onOk := rt.Function("__ada_factory_ok", 1, func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
			settled = true
			return moejs.Undefined(), nil
		})
		onErr := rt.Function("__ada_factory_err", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			settled = true
			factoryErr = p.valueError(moejs.Arg(args, 0), call)
			return moejs.Undefined(), nil
		})
		_, cerr = rt.CallFunction(then, res, onOk, onErr)
		return cerr
	})
	if err != nil {
		return fmt.Errorf("host: 插件 %s 的入口工厂执行失败: %w", p.id, p.exportError(err))
	}
	if factoryErr != nil {
		return fmt.Errorf("host: 插件 %s 的入口工厂被 reject: %w", p.id, factoryErr)
	}
	if !settled {
		return fmt.Errorf("host: 插件 %s 的入口工厂返回的 Promise 没有结算（%v，06 §6.4）", p.id, ErrUnsettled)
	}
	return nil
}

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

	put("route", func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		p, st, err := inst()
		if err != nil {
			return moejs.Undefined(), err
		}
		if frozen(p, "route") {
			return moejs.Undefined(), nil
		}
		method := strings.ToUpper(strings.TrimSpace(moejs.Arg(args, 0).String()))
		path := strings.TrimSpace(moejs.Arg(args, 1).String())
		handler := moejs.Arg(args, 2)
		if method != "GET" && method != "POST" {
			return moejs.Undefined(), r.TypeError("route(method, path, handler): method 只能是 GET 或 POST（7.6）")
		}
		if !strings.HasPrefix(path, "/") {
			return moejs.Undefined(), r.TypeError("route(method, path, handler): path 必须以 / 开头（7.6）")
		}
		if !engine.IsCallable(handler) {
			return moejs.Undefined(), r.TypeError("route(%s %s): handler 必须是函数", method, path)
		}
		slot, err := st.add(slotKindRoute, routeIdent(method, path), handler)
		if err != nil {
			return moejs.Undefined(), r.TypeError("%v", err)
		}
		st.data.Routes = append(st.data.Routes, RouteDef{Method: method, Path: path, Handler: HandlerRef{Slot: slot}})
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
		if _, ok := plugins.LookupHookPoint(point); !ok {
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
			def.Action = string(plugins.ActionObserve)
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
	point, ok := plugins.LookupHookPoint(def.Point)
	if !ok {
		return nil, fmt.Errorf("未知钩子点位 %q", def.Point)
	}
	action := plugins.HookAction(def.Action)
	if action == "" {
		action = plugins.ActionObserve
	}
	if !point.AllowsAction(action, p.caps.Trust == plugins.TrustBuiltin) {
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

// callArgs 构造一次脚本调用的实参：第一个是载荷（JSON 进），第二个是 ctx。
func (p *Instance) callArgs(rt *mojsRuntime, c *call) ([]moejs.Value, error) {
	payload := moejs.Undefined()
	if len(c.args) > 0 {
		v, err := rt.ParseJSON([]byte(c.args))
		if err != nil {
			return nil, fmt.Errorf("host: 插件 %s 的 %s 入参不是合法 JSON: %w", p.id, c.kind, err)
		}
		payload = v
	}
	return []moejs.Value{payload, p.contextObject(rt, c)}, nil
}

// contextObject 是一次调用的 ctx：身份字段恒有，能力成员按清单安装（05 §6.3）。
//
// 「没声明的属性不存在」是硬要求：这里不是给 undefined，而是根本不设置该属性，
// 脚本侧 `ctx.exec` 因此是 undefined，与设计 6.3 的「按能力安装」一致。
func (p *Instance) contextObject(rt *mojsRuntime, c *call) moejs.Value {
	r := rt.Realm()
	obj := r.NewObject()
	hc := c.hc
	if hc.PluginID == "" {
		hc.PluginID = p.id
	}
	if hc.Workspace == "" {
		hc.Workspace = p.caps.WorkspaceRoot
	}
	set := func(key string, v moejs.Value) {
		_ = obj.SetProp(r, r.KeyFromGoString(key), v)
	}
	setStr := func(key, val string) {
		if val == "" {
			return
		}
		set(key, moejs.String(val))
	}

	setStr("workspace", p.caps.Mount)
	setStr("threadId", hc.ThreadID)
	setStr("runId", hc.RunID)
	setStr("callId", hc.CallID)
	now := hc.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	set("now", moejs.String(now.UTC().Format(time.RFC3339)))
	set("signal", p.signalObject(rt, c, hc))
	// 插件自己的设置：按 id 透传，基座不解释（13 §附录 B）。
	if len(hc.Settings) > 0 {
		if v, err := rt.FromGo(hc.Settings); err == nil {
			set("settings", v)
		}
	}

	set("log", rt.Function("log", 3, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		if hc.Log == nil {
			return moejs.Undefined(), nil
		}
		level := strings.ToLower(strings.TrimSpace(moejs.Arg(args, 0).String()))
		switch level {
		case "debug", "info", "warn", "error":
		default:
			level = "info"
		}
		msg := moejs.Arg(args, 1).String()
		fields := p.goFields(rt, moejs.Arg(args, 2))
		hc.Log(level, msg, fields)
		return moejs.Undefined(), nil
	}))

	// state：capability: state。KV 是线程级命名空间，宿主负责落库（04 §5.4）。
	if p.caps.Has(plugins.CapState) && hc.State != nil {
		state := r.NewObject()
		op := func(kind string) engine.NativeFunc {
			return func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
				key := moejs.Arg(args, 0).String()
				var value json.RawMessage
				if len(args) > 1 {
					raw, err := rt.AppendJSON(nil, moejs.Arg(args, 1))
					if err != nil {
						return moejs.Undefined(), r.TypeError("state.%s(%q): 值不可 JSON 序列化: %v", kind, key, err)
					}
					value = raw
				}
				return p.hostAsync(c, "state."+kind, func(ctx context.Context) (any, error) {
					return hc.State(ctx, StateOp{Kind: kind, Key: key, Value: value})
				}), nil
			}
		}
		for _, kind := range []string{"get", "set", "delete"} {
			_ = state.SetProp(r, r.KeyFromGoString(kind), rt.Function("state."+kind, 2, op(kind)))
		}
		set("state", engine.ObjectValue(state))
	}

	// secret：只在清单声明过名字时存在（06 §6.2：密钥不得写进 env）。
	if len(p.caps.Secrets) > 0 && hc.Secret != nil {
		declared := map[string]bool{}
		for _, name := range p.caps.Secrets {
			declared[name] = true
		}
		set("secret", rt.Function("secret", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			name := moejs.Arg(args, 0).String()
			if !declared[name] {
				return moejs.Undefined(), nil
			}
			v, ok := hc.Secret(name)
			if !ok {
				return moejs.Undefined(), nil
			}
			return moejs.String(v), nil
		}))
	}

	// ui：capability: ui.ask，只授予 builtin/verified（05 §6.3）。
	if p.caps.Has(plugins.CapUIAsk) {
		set("ui", p.uiObject(rt, c, hc))
	}

	// fetch：capability: net.fetch。出网只经宿主的 HTTP 桥（03 §5.5）。
	if p.caps.Has(plugins.CapNetFetch) && hc.HTTP != nil {
		set("fetch", rt.Function("fetch", 2, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			return p.fetchPromise(rt, c, hc, args)
		}))
	}

	// 能力安装方补成员（ctx.fs / ctx.exec）：caps.go 的 fs guard 与 exec 适配器
	// 通过 InstallContextMembers 挂进来，agentapi 不重复实现能力细节。
	if InstallContextMembers != nil {
		put := func(name string, v moejs.Value) error {
			return obj.SetProp(r, r.KeyFromGoString(name), v)
		}
		if err := InstallContextMembers(rt, put, c.kind, p.caps, hc); err != nil {
			p.warn(fmt.Sprintf("插件 %s 的 ctx 能力成员安装失败: %v", p.id, err))
		}
	}
	return engine.ObjectValue(obj)
}

// signalObject 给脚本一个只读的 AbortSignal 形状：aborted/reason/addEventListener。
//
// 真正的取消不走它：轮次取消是 loop.Interrupt（脚本不可 catch，INV-8），
// 这里只让脚本能读到 aborted 并注册回调（child_process 用它杀进程树）。
func (p *Instance) signalObject(rt *mojsRuntime, c *call, hc Context) moejs.Value {
	r := rt.Realm()
	sig := r.NewObject()
	sigCtx := hc.Signal
	if sigCtx == nil {
		sigCtx = c.signal
	}
	aborted := sigCtx != nil && sigCtx.Err() != nil
	_ = sig.SetProp(r, r.KeyFromGoString("aborted"), engine.Bool(aborted))
	_ = sig.SetProp(r, r.KeyFromGoString("reason"), moejs.Undefined())
	_ = sig.SetProp(r, r.KeyFromGoString("addEventListener"), rt.Function("addEventListener", 2,
		func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			// 取消由宿主侧 context 贯穿；脚本注册的回调在 v1 不会被主动调用。
			if !engine.IsCallable(moejs.Arg(args, 1)) {
				return moejs.Undefined(), r.TypeError("signal.addEventListener(type, fn): fn 必须是函数")
			}
			return moejs.Undefined(), nil
		}))
	_ = sig.SetProp(r, r.KeyFromGoString("removeEventListener"), rt.Function("removeEventListener", 2,
		func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
			return moejs.Undefined(), nil
		}))
	return engine.ObjectValue(sig)
}

// uiObject 构造 ctx.ui（05 §6.3 UIContext）。
func (p *Instance) uiObject(rt *mojsRuntime, c *call, hc Context) moejs.Value {
	r := rt.Realm()
	ui := r.NewObject()
	_ = ui.SetProp(r, r.KeyFromGoString("available"), engine.Bool(hc.UI != nil))
	ask := func(kind domain.InteractionKind) engine.NativeFunc {
		return func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			req := UIRequest{Kind: kind, Title: moejs.Arg(args, 0).String()}
			switch kind {
			case domain.InteractionSelect:
				req.Options = p.stringList(rt, moejs.Arg(args, 1))
			case domain.InteractionInput:
				req.Message = moejs.Arg(args, 1).String()
			default:
				req.Message = moejs.Arg(args, 1).String()
			}
			if opts := moejs.Arg(args, 2); opts.IsObject() {
				if v, err := rt.Get(opts, "timeoutMs"); err == nil {
					if n, ok := jsInt(v); ok && n > 0 {
						req.TimeoutMS = int(n)
					}
				}
			}
			return p.hostAsync(c, "ui."+string(kind), func(ctx context.Context) (any, error) {
				if hc.UI == nil {
					return nil, nil // 未授予：按拒绝处理（INV-4 失败关闭）
				}
				res, err := hc.UI(ctx, req)
				if err != nil {
					return nil, err
				}
				switch res.Status {
				case domain.InteractionAccepted:
					switch kind {
					case domain.InteractionConfirm:
						return true, nil
					case domain.InteractionSelect:
						return res.Value, nil
					default:
						return res.Value, nil
					}
				default:
					return nil, nil
				}
			}), nil
		}
	}
	for _, item := range []struct {
		name string
		kind domain.InteractionKind
	}{
		{"confirm", domain.InteractionConfirm},
		{"select", domain.InteractionSelect},
		{"input", domain.InteractionInput},
	} {
		_ = ui.SetProp(r, r.KeyFromGoString(item.name), rt.Function(item.name, 3, ask(item.kind)))
	}
	_ = ui.SetProp(r, r.KeyFromGoString("notify"), rt.Function("notify", 2,
		func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			if hc.Log == nil {
				return moejs.Undefined(), nil
			}
			level := strings.TrimSpace(moejs.Arg(args, 1).String())
			switch level {
			case "warn", "error":
			default:
				level = "info"
			}
			hc.Log(level, "plugin.notice: "+moejs.Arg(args, 0).String(), map[string]any{"pluginId": p.id})
			return moejs.Undefined(), nil
		}))
	return engine.ObjectValue(ui)
}

// fetchPromise 把 ctx.fetch(url, init) 转成宿主的 HTTP 桥调用。
//
// 返回值故意做成最小的 Response 形状：status/headers/text()。流式（net.sse）由
// ctx.http 在 provider 插件里使用，不在这里（03 §5.5）。
func (p *Instance) fetchPromise(rt *mojsRuntime, c *call, hc Context, args []moejs.Value) (moejs.Value, error) {
	r := rt.Realm()
	url := moejs.Arg(args, 0).String()
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return moejs.Undefined(), throwCoded(r, codeAccessDenied, "ctx.fetch 只接受 http(s) URL（12 §6.12.3）",
			map[string]any{"url": baseForLog(url)})
	}
	method, headers, body := "GET", map[string]string{}, ""
	if init := moejs.Arg(args, 1); init.IsObject() {
		if v, err := rt.Get(init, "method"); err == nil && !v.IsNullish() {
			method = strings.ToUpper(v.String())
		}
		if v, err := rt.Get(init, "headers"); err == nil && v.IsObject() {
			if g, gerr := rt.ToGo(v); gerr == nil {
				if m, ok := g.(map[string]any); ok {
					for k, val := range m {
						headers[k] = fmt.Sprint(val)
					}
				}
			}
		}
		if v, err := rt.Get(init, "body"); err == nil && !v.IsNullish() {
			body = v.String()
		}
	}
	return p.hostAsync(c, "fetch", func(ctx context.Context) (any, error) {
		res, err := hc.HTTP.JSON(ctx, method, url, headers, body, currentLimits().MaxFetchBytes)
		if err != nil {
			return nil, err
		}
		text := string(res.Body)
		return map[string]any{
			"status":  res.Status,
			"headers": res.Headers,
			"ok":      res.Status >= 200 && res.Status < 300,
			"text":    func() string { return text },
		}, nil
	}), nil
}

// decodeToolResult 把脚本返回值结算成引擎侧 Result（05 §6.3 ToolReturn）。
func decodeToolResult(pluginID string, raw json.RawMessage, started time.Time) (domain.Result, error) {
	var body struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Mime string `json:"mime"`
			Data string `json:"data"`
		} `json:"content"`
		Details   map[string]any `json:"details"`
		Terminate bool           `json:"terminate"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return domain.Result{}, fmt.Errorf("host: 插件 %s 的工具结果不是 ToolReturn 形状: %w", pluginID, err)
		}
	}
	out := domain.Result{Details: body.Details}
	for _, part := range body.Content {
		switch part.Type {
		case "text":
			out.Content = append(out.Content, domain.Part{Type: domain.PartText, Text: part.Text})
		case "image":
			if part.Mime == "" || part.Data == "" {
				return domain.Result{}, fmt.Errorf("host: 插件 %s 的工具结果里 image 部件缺 mime/data", pluginID)
			}
			out.Content = append(out.Content, domain.Part{Type: domain.PartImage, Mime: part.Mime, Data: part.Data})
		case "":
			// 忽略空部件，不把形状错误当成内容错误。
		default:
			return domain.Result{}, fmt.Errorf("host: 插件 %s 的工具结果里有未知部件类型 %q（只能 text/image）", pluginID, part.Type)
		}
	}
	now := time.Now().UTC()
	out.Receipt = domain.Receipt{
		Status:     domain.ReceiptOK,
		StartedAt:  started.UTC(),
		FinishedAt: now,
		DurationMS: now.Sub(started).Milliseconds(),
	}
	return out, nil
}

// decodeHookOutcome 把脚本返回值结算成钩子动作（03 §5.6 的 { action, reason }）。
func decodeHookOutcome(pluginID string, raw json.RawMessage) (ports.HookOutcome, error) {
	out := ports.HookOutcome{PluginID: pluginID, Action: ports.HookObserve}
	if len(raw) == 0 {
		return out, nil
	}
	var body struct {
		Action      string          `json:"action"`
		Reason      string          `json:"reason"`
		Note        string          `json:"note"`
		Replacement json.RawMessage `json:"replacement"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return ports.HookOutcome{}, fmt.Errorf("host: 插件 %s 的钩子结果不是 {action, reason} 形状: %w", pluginID, err)
	}
	switch ports.HookAction(strings.TrimSpace(body.Action)) {
	case "", ports.HookObserve:
		out.Action = ports.HookObserve
	case ports.HookNote:
		out.Action = ports.HookNote
	case ports.HookDeny:
		out.Action = ports.HookDeny
	case ports.HookReplace:
		out.Action = ports.HookReplace
	case ports.HookAbort:
		out.Action = ports.HookAbort
	default:
		return ports.HookOutcome{}, fmt.Errorf("host: 插件 %s 返回了未知钩子动作 %q（只有 observe/note/deny/replace/abort）", pluginID, body.Action)
	}
	out.Reason = body.Reason
	out.Note = body.Note
	if len(body.Replacement) > 0 && string(body.Replacement) != "null" {
		out.Replacement = body.Replacement
	}
	return out, nil
}

// helpers ------------------------------------------------------------------

// decodeJSValue 用 JSON 边界把脚本对象读成 Go 结构（05 §6.3 的 JSON 边界）。
func decodeJSValue(rt *mojsRuntime, v moejs.Value, out any) error {
	raw, err := rt.AppendJSON(nil, v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// jsFunc 在对象上按候选名找一个可调用成员。
func jsFunc(rt *mojsRuntime, obj moejs.Value, names ...string) (moejs.Value, string, error) {
	for _, name := range names {
		v, err := rt.Get(obj, name)
		if err != nil {
			continue
		}
		if engine.IsCallable(v) {
			return v, name, nil
		}
	}
	return moejs.Value{}, "", fmt.Errorf("缺少可调用成员（期望 %s）", strings.Join(names, " / "))
}

// jsInt 把脚本数字读成 int64（不是数字时 ok=false）。
func jsInt(v moejs.Value) (int64, bool) {
	if !v.IsNumber() {
		return 0, false
	}
	return int64(v.AsNumber()), true
}

// goFields 把脚本的 fields 对象摊成 map[string]any（日志与审计字段）。
func (p *Instance) goFields(rt *mojsRuntime, v moejs.Value) map[string]any {
	if !v.IsObject() {
		return nil
	}
	raw, err := rt.AppendJSON(nil, v)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) > 32 {
		// 日志字段不做无界放大：超出的键名按字典序截断。
		keys := make([]string, 0, len(out))
		for k := range out {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		trimmed := make(map[string]any, 32)
		for _, k := range keys[:32] {
			trimmed[k] = out[k]
		}
		return trimmed
	}
	return out
}

// stringList 读一个字符串数组（select 的 options）。
func (p *Instance) stringList(rt *mojsRuntime, v moejs.Value) []string {
	if !v.IsObject() {
		return nil
	}
	raw, err := rt.AppendJSON(nil, v)
	if err != nil {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// errAgentAPIUnavailable 是 agent:host 在装配期之外被调用时的错误（诊断用）。
var errAgentAPIUnavailable = errors.New("host: agent API 未就绪")
