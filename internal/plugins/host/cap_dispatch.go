package host

// ctx.dispatch —— 内部发送桥（capability: dispatch.send，仅 builtin）。
//
// 插件只提交「发给哪个工作区、发什么」；身份由宿主按能力集盖章，投递语义
// （建线程 / 空闲 start / 运行中 steer）由网关调度层决定。
//
// 与 ctx.turn.invoke 的区别：
//   - turn.invoke 在本 Core 内直接开一轮，只服务本工作区；
//   - dispatch.send 交给网关调度层，可跨工作区，且与界面发送共用同一份投递规则
//     （busy / waiting_input / finishing 的判定只有一处）。
//
// 令牌不进脚本：宿主用它调回环端点，插件既拿不到地址也拿不到凭据。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"

	"github.com/adag/ada-go/internal/plugins"
)

const (
	dispatchTextMaxBytes     = 64 << 10
	dispatchTitleMaxRunes    = 200
	dispatchWorkspaceMaxRune = 4096
	dispatchKeyMaxBytes      = 200
)

func addDispatchContextMember(st *capsState, hc Context, put func(string, moejs.Value) error) error {
	if st == nil || st.caps == nil || !st.caps.Has(plugins.CapDispatchSend) || st.caps.Trust != plugins.TrustBuiltin {
		return nil
	}
	if hc.Dispatch == nil {
		return nil
	}
	r := st.rt.Realm()
	obj := r.NewObject()
	_ = obj.SetProp(r, r.KeyFromGoString("send"), st.rt.Function("dispatch.send", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		var arg DispatchRequest
		if len(args) == 0 || moejs.Arg(args, 0).IsNullish() {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.dispatch.send: 需要参数", nil)
		}
		raw, err := st.rt.AppendJSON(nil, moejs.Arg(args, 0))
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.dispatch.send: 参数不可 JSON 序列化", nil)
		}
		if err := json.Unmarshal(raw, &arg); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.dispatch.send: 参数形状不符", nil)
		}
		arg.Text = strings.TrimSpace(arg.Text)
		if arg.Text == "" {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.dispatch.send: text 不能为空", nil)
		}
		if len(arg.Text) > dispatchTextMaxBytes {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.dispatch.send: text 超过 64KB", nil)
		}
		if n := len([]rune(arg.Title)); n > dispatchTitleMaxRunes {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.dispatch.send: title 超过 200 字符", nil)
		}
		if n := len([]rune(arg.Workspace)); n > dispatchWorkspaceMaxRune {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.dispatch.send: workspace 过长", nil)
		}
		if len(arg.IdempotencyKey) > dispatchKeyMaxBytes {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.dispatch.send: idempotencyKey 超过 200 字节", nil)
		}
		switch strings.TrimSpace(arg.Mode) {
		case "", "auto", "start", "steer":
		default:
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.dispatch.send: mode 只能是 auto|start|steer", nil)
		}

		ctx := hc.Signal
		if ctx == nil {
			ctx = context.Background()
		}
		res, err := hc.Dispatch(ctx, arg)
		if err != nil {
			// 业务失败（busy / waiting_input / 工作区不存在）同样是调用方要处理的结果：
			// 用固定错误码抛出，不把上游文案当异常文本。
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.dispatch.send: "+err.Error(), nil)
		}
		st.audit("dispatch", "send",
			fmt.Sprintf("ws=%s thread=%s mode=%s", res.Workspace, res.ThreadID, res.Mode),
			len(arg.Text), "ok", map[string]any{"taskId": arg.TaskID})
		v, err := st.rt.FromGo(map[string]any{
			"workspace": res.Workspace, "threadId": res.ThreadID, "runId": res.RunID, "mode": res.Mode,
		})
		if err != nil {
			return moejs.Undefined(), err
		}
		return resolveConfig(st, v), nil
	}))
	return put("dispatch", engine.ObjectValue(obj))
}
