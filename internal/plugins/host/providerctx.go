package host

// provider 的脚本侧上下文：ctx.emit 与 ctx.http（03 §5.5）。
//
// 这两个成员只在 callProvider 上安装。emit 把归一化增量交给宿主桥；
// http 把 SSE/JSON 请求交给 host.HTTPBridge —— TLS、代理、重定向、超时、
// allowlist 与响应大小都在桥里执行，插件只做「SSE 事件 → 增量」的协议映射。
//
// 一个关键约束：ctx.http.sse 的 onEvent 回调必须回到 owner goroutine 上执行，
// 而 SSE 读取在另一个 goroutine 上。这里的 deliverEvent 用「投递任务 + 回执通道」
// 实现，且任务本身不等待 Promise：异步 onEvent 通过 then 回调回投，不会卡死 loop。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
)

// emitFunction 构造 ctx.emit(delta)：delta 必须是 JSON 对象，原样交给宿主桥。
func (p *Instance) emitFunction(rt *mojsRuntime, c *call) moejs.Value {
	return rt.Function("emit", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		v := moejs.Arg(args, 0)
		if !v.IsObject() {
			return moejs.Undefined(), rt.Realm().TypeError("ctx.emit(delta): delta 必须是对象（03 §5.5）")
		}
		raw, err := rt.AppendJSON(nil, v)
		if err != nil {
			return moejs.Undefined(), rt.Realm().TypeError("ctx.emit(delta): delta 不可 JSON 序列化: %v", err)
		}
		if err := c.onDelta(raw); err != nil {
			return moejs.Undefined(), err
		}
		return moejs.Undefined(), nil
	})
}

// httpObject 构造 ctx.http：json 与 sse 两个宿主长调用。
func (p *Instance) httpObject(rt *mojsRuntime, c *call, hc Context) moejs.Value {
	r := rt.Realm()
	httpObj := r.NewObject()
	_ = httpObj.SetProp(r, r.KeyFromGoString("json"), rt.Function("http.json", 2,
		func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			req, err := parseHTTPInit(rt, args)
			if err != nil {
				return moejs.Undefined(), err
			}
			return p.hostAsync(c, "http.json", func(ctx context.Context) (any, error) {
				res, err := hc.HTTP.JSON(ctx, req.method, req.url, req.headers, req.body, req.maxBytes)
				if err != nil {
					return nil, err
				}
				var body any
				if len(res.Body) > 0 {
					if jerr := jsonUnmarshal(res.Body, &body); jerr != nil {
						body = string(res.Body)
					}
				}
				return map[string]any{
					"status":  res.Status,
					"headers": flattenHeaders(res.Headers),
					"body":    body,
				}, nil
			}), nil
		}))
	_ = httpObj.SetProp(r, r.KeyFromGoString("sse"), rt.Function("http.sse", 3,
		func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			req, err := parseHTTPInit(rt, args)
			if err != nil {
				return moejs.Undefined(), err
			}
			cb := moejs.Arg(args, 2)
			if !engine.IsCallable(cb) {
				return moejs.Undefined(), r.TypeError("ctx.http.sse(url, init, onEvent): onEvent 必须是函数（03 §5.5）")
			}
			p.keepRoot(cb)
			return p.hostAsyncTimeout(c, "http.sse", p.budgets.ProviderTotal, func(ctx context.Context) (any, error) {
				res, err := hc.HTTP.SSE(ctx, req.method, req.url, req.headers, req.body,
					func(event, data string) error {
						return p.deliverEvent(ctx, c, cb, event, data)
					})
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"status":  res.Status,
					"headers": flattenHeaders(res.Headers),
				}, nil
			}), nil
		}))
	return engine.ObjectValue(httpObj)
}

// deliverEvent 把一条 SSE 事件投递到 loop 上调用 onEvent，并等待它（或它返回的 Promise）结束。
//
// 这里等待的是「loop 上回调的回执」，调用方所在的 goroutine 不是 loop，
// 所以不会阻塞事件循环；onEvent 返回 Promise 时用 then 回调回投，保持事件顺序。
func (p *Instance) deliverEvent(ctx context.Context, c *call, cb moejs.Value, event, data string) error {
	if p.loop == nil {
		return errors.New("host: provider 运行时尚未装配")
	}
	done := make(chan error, 1)
	if !p.loop.RunOnLoop(func(rt *mojsRuntime) {
		arg, err := rt.FromGo(map[string]string{"event": event, "data": data})
		if err != nil {
			done <- err
			return
		}
		v, err := rt.CallFunction(cb, moejs.Undefined(), arg)
		if err != nil {
			done <- p.exportError(err)
			return
		}
		r := rt.Realm()
		then, terr := r.GetMethod(v, r.KeyFromGoString("then"))
		if terr != nil || !engine.IsCallable(then) {
			done <- nil
			return
		}
		okFn := rt.Function("__ada_sse_event_ok", 1, func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
			done <- nil
			return moejs.Undefined(), nil
		})
		errFn := rt.Function("__ada_sse_event_err", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			done <- p.valueError(moejs.Arg(args, 0), c)
			return moejs.Undefined(), nil
		})
		if _, err := rt.CallFunction(then, v, okFn, errFn); err != nil {
			done <- p.exportError(err)
		}
	}) {
		return errors.New("host: provider 运行时已结束，SSE 事件被丢弃")
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// httpInit 是脚本侧 HttpInit 的 Go 形状（03 §5.5）。
type httpInit struct {
	url      string
	method   string
	headers  map[string]string
	body     string
	maxBytes int
}

// parseHTTPInit 读取 (url, init) 参数；只做形状校验，allowlist 与期限留给桥。
func parseHTTPInit(rt *mojsRuntime, args []moejs.Value) (httpInit, error) {
	out := httpInit{url: strings.TrimSpace(moejs.Arg(args, 0).String()), method: "GET"}
	if out.url == "" {
		return out, rt.Realm().TypeError("ctx.http: url 不能为空（03 §5.5）")
	}
	init := moejs.Arg(args, 1)
	if !init.IsObject() {
		return out, nil
	}
	if v, err := rt.Get(init, "method"); err == nil && !v.IsNullish() {
		out.method = strings.ToUpper(strings.TrimSpace(v.String()))
	}
	if v, err := rt.Get(init, "headers"); err == nil && v.IsObject() {
		if g, gerr := rt.ToGo(v); gerr == nil {
			if m, ok := g.(map[string]any); ok {
				out.headers = map[string]string{}
				for k, val := range m {
					out.headers[k] = fmt.Sprint(val)
				}
			}
		}
	}
	if v, err := rt.Get(init, "body"); err == nil && !v.IsNullish() {
		out.body = v.String()
	}
	if v, err := rt.Get(init, "maxBytes"); err == nil {
		if n, ok := jsInt(v); ok && n > 0 {
			out.maxBytes = int(n)
		}
	}
	return out, nil
}

// flattenHeaders 把 http.Header 折叠成 Record<string,string>（多值取第一个）。
func flattenHeaders(h map[string][]string) map[string]string {
	if len(h) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(h))
	for k, vals := range h {
		if len(vals) > 0 {
			out[k] = vals[0]
		}
	}
	return out
}

// jsonUnmarshal 是 encoding/json 的薄封装，便于本文件保持导入集中。
func jsonUnmarshal(raw []byte, out any) error {
	return json.Unmarshal(raw, out)
}
