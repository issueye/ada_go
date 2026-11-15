package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	"github.com/adag/ada-go/internal/common/domain"
)

// skillMetasJSON 把技能元数据转成脚本侧对象数组（ctx.skills 用）。
func skillMetasJSON(metas []domain.SkillMeta) []map[string]string {
	out := make([]map[string]string, 0, len(metas))
	for _, m := range metas {
		item := map[string]string{"name": m.Name, "description": m.Description, "origin": m.Origin}
		if m.Path != "" {
			item["path"] = m.Path
		}
		if m.PluginID != "" {
			item["pluginId"] = m.PluginID
		}
		out = append(out, item)
	}
	return out
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
// 脚本侧 `ctx.exec` 因此是 undefined（宿主未注入该成员，不是能力位裁剪）。
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
	if hc.State != nil {
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
					return hc.State(ctx, StateOp{
						PluginID: p.id, ThreadID: hc.ThreadID, Kind: kind, Key: key, Value: value,
					})
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
		set("secret", p.secretFunction(rt, hc))
	}

	// ui：capability: ui.ask，只授予 builtin/verified（05 §6.3）。
	if true {
		set("ui", p.uiObject(rt, c, hc))
	}

	// skills：只读的技能元数据表（6.7）。它不是能力位：只暴露 name/description/path/origin，
	// 不授予任何文件或执行权限；宿主调用方（Session）没有提供时该成员不存在。
	if hc.Skills != nil {
		set("skills", rt.Function("skills", 0, func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			return rt.FromGo(skillMetasJSON(hc.Skills()))
		}))
	}

	// providers：capability: model.admin。只暴露目录读取与当前模型切换；
	// 不暴露密钥、端点客户端或任何出网能力。
	if hc.Providers != nil {
		providers := r.NewObject()
		_ = providers.SetProp(r, r.KeyFromGoString("list"), rt.Function("providers.list", 0,
			func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
				return p.hostAsync(c, "providers.list", func(context.Context) (any, error) {
					return hc.Providers(), nil
				}), nil
			}))
		_ = providers.SetProp(r, r.KeyFromGoString("select"), rt.Function("providers.select", 2,
			func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
				providerID := strings.TrimSpace(moejs.Arg(args, 0).String())
				modelID := strings.TrimSpace(moejs.Arg(args, 1).String())
				if hc.SelectModel == nil {
					return moejs.Undefined(), r.TypeError("providers.select: 宿主没有提供模型切换")
				}
				if providerID == "" || modelID == "" {
					return moejs.Undefined(), r.TypeError("providers.select(providerId, modelId): 两个参数都不能为空")
				}
				return p.hostAsync(c, "providers.select", func(ctx context.Context) (any, error) {
					return hc.SelectModel(ctx, providerID, modelID, hc.ThreadID)
				}), nil
			}))
		set("providers", engine.ObjectValue(providers))
	}

	// pluginAdmin：capability: plugin.admin。只暴露全局安装与健康检查入口；
	// id/路径/大小校验与「不覆盖」由宿主实现，脚本拿不到任意文件写权限。
	if hc.InstallPlugin != nil || hc.CheckPlugin != nil {
		admin := r.NewObject()
		if hc.InstallPlugin != nil {
			_ = admin.SetProp(r, r.KeyFromGoString("install"), rt.Function("pluginAdmin.install", 1,
				func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
					raw, err := rt.AppendJSON(nil, moejs.Arg(args, 0))
					if err != nil {
						return moejs.Undefined(), r.TypeError("pluginAdmin.install(spec): spec 不可 JSON 序列化: %v", err)
					}
					var spec PluginInstallSpec
					if err := json.Unmarshal(raw, &spec); err != nil {
						return moejs.Undefined(), r.TypeError("pluginAdmin.install(spec): spec 形状非法: %v", err)
					}
					return p.hostAsync(c, "pluginAdmin.install", func(ctx context.Context) (any, error) {
						return hc.InstallPlugin(ctx, spec)
					}), nil
				}))
		}
		if hc.CheckPlugin != nil {
			_ = admin.SetProp(r, r.KeyFromGoString("check"), rt.Function("pluginAdmin.check", 1,
				func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
					pluginID := strings.TrimSpace(moejs.Arg(args, 0).String())
					return p.hostAsync(c, "pluginAdmin.check", func(ctx context.Context) (any, error) {
						return hc.CheckPlugin(ctx, pluginID)
					}), nil
				}))
		}
		if hc.RemovePlugin != nil {
			_ = admin.SetProp(r, r.KeyFromGoString("remove"), rt.Function("pluginAdmin.remove", 1,
				func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
					pluginID := strings.TrimSpace(moejs.Arg(args, 0).String())
					return p.hostAsync(c, "pluginAdmin.remove", func(ctx context.Context) (any, error) {
						return hc.RemovePlugin(ctx, pluginID)
					}), nil
				}))
		}
		set("pluginAdmin", engine.ObjectValue(admin))
	}

	// SSE 路由的 ctx.sse.send(data, event?)：只在流式调用上安装，普通 route
	// 看不到它（防止把长连接误当成请求/响应接口）。
	if c.kind == callRouteStream {
		sse := r.NewObject()
		_ = sse.SetProp(r, r.KeyFromGoString("send"), rt.Function("sse.send", 2,
			func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
				eventName := ""
				if ev := moejs.Arg(args, 1); !ev.IsNullish() {
					eventName = strings.TrimSpace(ev.String())
				}
				if len(eventName) > 64 || strings.ContainsAny(eventName, "\r\n:") {
					return moejs.Undefined(), r.TypeError("sse.send(data, event): 事件名最多 64 字符，且不能包含换行或冒号")
				}
				raw, err := rt.AppendJSON(nil, moejs.Arg(args, 0))
				if err != nil {
					return moejs.Undefined(), r.TypeError("sse.send(data): data 不可 JSON 序列化: %v", err)
				}
				if len(raw) > p.budgets.RouteBytes {
					return moejs.Undefined(), r.TypeError("sse.send(data): 单帧 %d 字节，超过上限 %d", len(raw), p.budgets.RouteBytes)
				}
				if c.onRouteEvent == nil {
					return moejs.Undefined(), errors.New("host: SSE 路由没有事件回调")
				}
				if err := c.onRouteEvent(RouteEvent{Event: eventName, Data: raw}); err != nil {
					return moejs.Undefined(), err
				}
				if c.budget != nil {
					// 每帧重置空闲窗口：只要还在推帧，就不受 5s 路由预算限制；总时长仍受墙钟约束。
					c.budget.window(p.budgets.ProviderIdle)
				}
				return moejs.Undefined(), nil
			}))
		set("sse", engine.ObjectValue(sse))
	}

	// provider 的 ctx.emit / ctx.http 只在 callProvider 上安装（03 §5.5）：
	// emit 是归一化增量的唯一出口，http 是 provider 出网的唯一出口。
	if c.kind == callProvider {
		if c.onDelta != nil {
			set("emit", p.emitFunction(rt, c))
		}
		if hc.HTTP != nil {
			set("http", p.httpObject(rt, c, hc))
		}
	}

	// fetch：capability: net.fetch。出网只经宿主的 HTTP 桥（03 §5.5）。
	if hc.HTTP != nil {
		set("fetch", p.fetchFunction(rt, c, hc))
	}

	// 成员安装方补成员（ctx.fs / ctx.exec）：caps.go 的 fs guard 与 exec 适配器
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

// assemblyContextObject 是入口工厂的第二个参数：装配期只读上下文。
//
// 与 ToolContext 的区别是刻意收窄：没有 state/ui/exec，也没有 fs.writeText。
// process 成员同样由宿主注入（stdio MCP 在装配期启动长驻 server），这是唯一的装配期副作用。
// 它只解决「插件在 registerTool 之前需要读配置、访问网络、取密钥」这一类装配期需求
// （MCP 的 tools/list 是第一个消费者），不能把装配期变成有副作用的运行期。
func (p *Instance) assemblyContextObject(rt *mojsRuntime, c *call) moejs.Value {
	r := rt.Realm()
	obj := r.NewObject()
	if c == nil {
		c = &call{kind: callInit}
	}
	hc := p.baseContext(c.signal)
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
	set("signal", p.signalObject(rt, c, hc))
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
		fields := p.goFields(rt, moejs.Arg(args, 2))
		hc.Log(level, moejs.Arg(args, 1).String(), fields)
		return moejs.Undefined(), nil
	}))

	// fs 只安装读成员：装配期读 .adago/mcp.json、~/.adago/mcp.json，不允许写。
	if st, ok := stateFor(rt); ok && st.fs != nil {
		set("fs", ctxFSReadObject(st, hc))
	}
	// fetch 与运行时 ctx.fetch 同一条宿主 HTTP 桥；没有 net.fetch 就不存在。
	if hc.HTTP != nil {
		set("fetch", p.fetchFunction(rt, c, hc))
	}
	// secret 只暴露清单声明过的名字。
	if len(p.caps.Secrets) > 0 && hc.Secret != nil {
		set("secret", p.secretFunction(rt, hc))
	}
	// process：stdio 类插件在装配期启动长驻 server。
	if st, ok := stateFor(rt); ok && true {
		_ = addProcessContextMember(st, hc, func(name string, v moejs.Value) error {
			set(name, v)
			return nil
		})
	}
	// processes：cron scheduler 这类长驻进程在装配期就交给网关拉起（on_enable）。
	if st, ok := stateFor(rt); ok && true {
		_ = addProcessSuperviseContextMember(st, hc, func(name string, v moejs.Value) error {
			set(name, v)
			return nil
		})
	}
	return engine.ObjectValue(obj)
}
