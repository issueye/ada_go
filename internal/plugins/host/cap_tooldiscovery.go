package host

// ctx.catalog / ctx.tool —— 发现式工具系统插件的宿主桥（capability: tool.discovery，仅 builtin）。
//
// 这条路径把 find_tool/exec_tool 从引擎搬到插件后仍然保持单份审批链：
//   - ctx.catalog.list 只返回 tool.Discovery=true 的目录快照（只读投影）；
//   - ctx.tool.invoke 把内层调用交回宿主，宿主按内层描述符重跑 beforeCall、
//     policy 失败关闭与 Runner.Execute；脚本不能直接调用 Executor。
//
// 不提供注册面、不暴露任意工具句柄；能力位仅 builtin，且只应授予 tool-discovery。

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
)

func addToolDiscoveryContextMembers(st *capsState, hc Context, put func(string, moejs.Value) error) error {
	if st == nil || st.caps == nil || !st.caps.Has(plugins.CapToolDiscovery) || st.caps.Trust != plugins.TrustBuiltin {
		return nil
	}
	if hc.Catalog == nil || hc.InvokeTool == nil {
		return nil
	}
	r := st.rt.Realm()

	catalogObj := r.NewObject()
	_ = catalogObj.SetProp(r, r.KeyFromGoString("list"), st.rt.Function("catalog.list", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		query := ""
		if len(args) > 0 && !moejs.Arg(args, 0).IsNullish() {
			if obj := moejs.Arg(args, 0).AsObject(); obj != nil {
				if v, ok := obj.GetOwnDataValue(r.KeyFromGoString("query")); ok {
					query = strings.ToLower(strings.TrimSpace(v.String()))
				}
			}
		}
		ctx := hc.Signal
		if ctx == nil {
			ctx = context.Background()
		}
		list, err := hc.Catalog(ctx)
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.catalog.list: "+err.Error(), nil)
		}
		// 可见性规则只有一处（domain.ToolVisibleDiscovery）：这里不做二次判断，
		// 否则「find_tool 列出的」与「exec_tool 能执行的」会各自漂移。
		discoverable := domain.VisibleTools(list, domain.ToolVisibleDiscovery)
		out := make([]map[string]any, 0, len(discoverable))
		for _, d := range discoverable {
			if query != "" && !strings.Contains(strings.ToLower(d.Name), query) && !strings.Contains(strings.ToLower(d.Description), query) {
				continue
			}
			var params any
			if len(d.Parameters) > 0 {
				if err := json.Unmarshal(d.Parameters, &params); err != nil {
					return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.catalog.list: 参数 Schema 损坏", nil)
				}
			}
			out = append(out, map[string]any{
				"name": d.Name, "description": d.Description, "access": string(d.Access), "parameters": params,
			})
		}
		st.audit("catalog", "list", query, len(out), "ok", nil)
		v, err := st.rt.FromGo(out)
		if err != nil {
			return moejs.Undefined(), err
		}
		return resolveConfig(st, v), nil
	}))

	toolObj := r.NewObject()
	_ = toolObj.SetProp(r, r.KeyFromGoString("invoke"), st.rt.Function("tool.invoke", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		var req struct {
			Tool string          `json:"tool"`
			Args json.RawMessage `json:"args"`
		}
		raw, err := st.rt.AppendJSON(nil, moejs.Arg(args, 0))
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.tool.invoke: 参数不可 JSON 序列化", nil)
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.tool.invoke: 参数形状不符", nil)
		}
		tool := strings.TrimSpace(req.Tool)
		if tool == "" {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.tool.invoke: 缺少 tool", nil)
		}
		if tool == domain.ToolFindName || tool == domain.ToolExecName {
			return moejs.Undefined(), throwCoded(rr, codeAccessDenied, "ctx.tool.invoke: 不能执行发现式入口本身", nil)
		}
		ctx := hc.Signal
		if ctx == nil {
			ctx = context.Background()
		}
		res, err := hc.InvokeTool(ctx, CallMeta{ThreadID: hc.ThreadID, RunID: hc.RunID, CallID: hc.CallID},
			domain.ToolCall{CallID: hc.CallID, Tool: tool, Args: req.Args})
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.tool.invoke: "+err.Error(), nil)
		}
		st.audit("tool", "invoke", tool, 0, string(res.Receipt.Status), nil)
		return resolveConfig(st, resultObject(st, res)), nil
	}))

	var firstErr error
	if err := put("catalog", engine.ObjectValue(catalogObj)); err != nil {
		firstErr = err
	}
	if err := put("tool", engine.ObjectValue(toolObj)); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// resultObject 把 domain.Result 转成脚本可读对象；content 仍是 text/image 部件。
func resultObject(st *capsState, res domain.Result) moejs.Value {
	content := make([]map[string]any, 0, len(res.Content))
	for _, p := range res.Content {
		item := map[string]any{"type": string(p.Type), "text": p.Text}
		if p.Mime != "" {
			item["mime"] = p.Mime
		}
		if p.Data != "" {
			item["data"] = p.Data
		}
		content = append(content, item)
	}
	v, err := st.rt.FromGo(map[string]any{
		"content": content,
		"details": res.Details,
		"status":  string(res.Receipt.Status),
		"reason":  res.Receipt.Reason,
	})
	if err != nil {
		return moejs.Undefined()
	}
	return v
}
