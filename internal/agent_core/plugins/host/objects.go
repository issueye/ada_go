package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	"github.com/adag/ada-go/internal/common/domain"
)

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
			req := UIRequest{PluginID: p.id, Kind: kind, Title: moejs.Arg(args, 0).String()}
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
					return nil, nil // 宿主没给 UI：返回 nil，由调用方按 INV-4 处理
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
	// form：多题分步表单。插件传题目数组的 JSON 串（JSON.stringify(questions)），
	// 界面按「上一题/下一题/提交」翻阅；回答是 {"answers":[...]} 的 JSON 串。
	_ = ui.SetProp(r, r.KeyFromGoString("form"), rt.Function("form", 2,
		func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			raw := strings.TrimSpace(moejs.Arg(args, 0).String())
			if raw == "" || !json.Valid([]byte(raw)) {
				return moejs.Undefined(), r.TypeError("ui.form(questions): 需要题目数组的 JSON 串")
			}
			req := UIRequest{PluginID: p.id, Kind: domain.InteractionForm,
				Title: "请回答以下问题", Payload: json.RawMessage(raw)}
			if opts := moejs.Arg(args, 1); opts.IsObject() {
				if v, err := rt.Get(opts, "timeoutMs"); err == nil {
					if n, ok := jsInt(v); ok && n > 0 {
						req.TimeoutMS = int(n)
					}
				}
			}
			return p.hostAsync(c, "ui.form", func(ctx context.Context) (any, error) {
				if hc.UI == nil {
					return nil, nil // 宿主没给 UI：返回 nil，由调用方按 INV-4 处理
				}
				res, err := hc.UI(ctx, req)
				if err != nil {
					return nil, err
				}
				if res.Status != domain.InteractionAccepted {
					return nil, nil
				}
				return res.Value, nil
			}), nil
		}))
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

// secretFunction 构造脚本侧 ctx.secret(name)：只有清单声明过的名字才返回值。
func (p *Instance) secretFunction(rt *mojsRuntime, hc Context) moejs.Value {
	declared := map[string]bool{}
	for _, name := range p.caps.Secrets {
		declared[name] = true
	}
	return rt.Function("secret", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		name := moejs.Arg(args, 0).String()
		if !declared[name] {
			return moejs.Undefined(), nil
		}
		v, ok := hc.Secret(name)
		if !ok {
			return moejs.Undefined(), nil
		}
		return moejs.String(v), nil
	})
}

// fetchFunction 构造脚本侧 ctx.fetch(url, init)：只在有 net.fetch 且宿主有 HTTP 桥时安装。
func (p *Instance) fetchFunction(rt *mojsRuntime, c *call, hc Context) moejs.Value {
	return rt.Function("fetch", 2, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		return p.fetchPromise(rt, c, hc, args)
	})
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
	// timeoutMs 是可选的整体期限：脚本侧（如 MCP 的 server.timeoutMs）需要它
	// 把「自己放弃等待」变成「真的取消请求」，否则挂起的请求会一直占着
	// 事件循环的 hold，让插件调用（乃至装配）等到桥的超时才结束。
	timeout := defaultHostCallTimeout
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
		if v, err := rt.Get(init, "timeoutMs"); err == nil {
			if n, ok := jsInt(v); ok && n > 0 {
				timeout = time.Duration(n) * time.Millisecond
			}
		}
	}
	if timeout <= 0 || timeout > domain.ProviderTotalTimeout {
		timeout = domain.ProviderTotalTimeout
	}
	return p.hostAsyncTimeout(c, "fetch", timeout, func(ctx context.Context) (any, error) {
		res, err := hc.HTTP.JSON(ctx, method, url, headers, body, currentLimits().MaxFetchBytes)
		if err != nil {
			return nil, err
		}
		// text 必须是字符串属性而不是 Go 函数：FromGo 不支持 func 值，
		// 夹带函数会让整个 Promise 结算失败（FromGo: unsupported Go type）。
		return map[string]any{
			"status":  res.Status,
			"headers": res.Headers,
			"ok":      res.Status >= 200 && res.Status < 300,
			"text":    string(res.Body),
		}, nil
	}), nil
}
