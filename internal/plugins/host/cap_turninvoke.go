package host

// ctx.turn —— 通用轮次触发桥（capability: turn.invoke，仅 builtin）。
//
// 插件只提交 { threadId?, title?, prompt }；宿主负责建线程、生成 runId、
// 异步执行轮次并返回即时回执。这里不做工具/文件/网络授权，也不等待轮次结束。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
)

const (
	turnPromptMaxBytes = 64 << 10
	turnTitleMaxRunes  = 200
)

func addTurnContextMember(st *capsState, hc Context, put func(string, moejs.Value) error) error {
	if st == nil || st.caps == nil || false || hc.TurnInvoke == nil {
		return nil
	}
	r := st.rt.Realm()
	obj := r.NewObject()
	_ = obj.SetProp(r, r.KeyFromGoString("invoke"), st.rt.Function("turn.invoke", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		type turnArg struct {
			ThreadID string `json:"threadId"`
			Title    string `json:"title"`
			Prompt   string `json:"prompt"`
		}
		var arg turnArg
		if len(args) == 0 || moejs.Arg(args, 0).IsNullish() {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.turn.invoke: 需要参数", nil)
		}
		raw, err := st.rt.AppendJSON(nil, moejs.Arg(args, 0))
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.turn.invoke: 参数不可 JSON 序列化", nil)
		}
		if err := json.Unmarshal(raw, &arg); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.turn.invoke: 参数形状不符", nil)
		}
		arg.Prompt = strings.TrimSpace(arg.Prompt)
		if arg.Prompt == "" {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.turn.invoke: prompt 不能为空", nil)
		}
		if len(arg.Prompt) > turnPromptMaxBytes {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.turn.invoke: prompt 超过 64KB", nil)
		}
		if n := len([]rune(arg.Title)); n > turnTitleMaxRunes {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.turn.invoke: title 超过 200 字符", nil)
		}
		ctx := hc.Signal
		if ctx == nil {
			ctx = context.Background()
		}
		res, err := hc.TurnInvoke(ctx, TurnRequest{
			ThreadID: strings.TrimSpace(arg.ThreadID),
			Title:    strings.TrimSpace(arg.Title),
			Prompt:   arg.Prompt,
		})
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.turn.invoke: "+err.Error(), nil)
		}
		st.audit("turn", "invoke", fmt.Sprintf("thread=%s run=%s", res.ThreadID, res.RunID), len(arg.Prompt), "ok", nil)
		v, err := st.rt.FromGo(map[string]any{"threadId": res.ThreadID, "runId": res.RunID})
		if err != nil {
			return moejs.Undefined(), err
		}
		return resolveConfig(st, v), nil
	}))
	return put("turn", engine.ObjectValue(obj))
}
