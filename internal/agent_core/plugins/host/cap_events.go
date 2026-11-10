package host

// ctx.events —— 只读事件/用量投影（capability: events.read）。
//
// 数据源由宿主注入（Context.Events / Context.Usage）；本包只负责：
//   - 能力门禁：没声明 events.read 时 ctx.events 不存在；
//   - 线程边界：query 永久绑定当前调用线程；usage 在调用方有线程上下文时
//     也拒绝查询其它线程（无线程上下文时按工作区聚合，供工作区级页面使用）；
//   - JSON 形状：{seq,type,threadId,at,data} / 原始用量行数组，返回已结算 Promise。
//
// 它不提供写事件、订阅或跨工作区查询；事件缓冲的缺口由宿主回调返回错误，
// 插件应降级为轮询 thread.get/usage（09 §9.5）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
)

func addEventsContextMember(st *capsState, hc Context, put func(string, moejs.Value) error) error {
	if st == nil || st.caps == nil {
		return nil
	}
	if hc.Events == nil && hc.Usage == nil {
		return nil
	}
	r := st.rt.Realm()
	obj := r.NewObject()
	if hc.Events != nil {
		_ = obj.SetProp(r, r.KeyFromGoString("query"), st.rt.Function("events.query", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			if strings.TrimSpace(hc.ThreadID) == "" {
				return moejs.Undefined(), throwCoded(rr, codeAccessDenied, "ctx.events.query: 需要线程上下文", nil)
			}
			type queryArg struct {
				ThreadID string   `json:"threadId"`
				FromSeq  int64    `json:"fromSeq"`
				Types    []string `json:"types"`
				Limit    int      `json:"limit"`
			}
			var arg queryArg
			if len(args) > 0 && !moejs.Arg(args, 0).IsNullish() {
				raw, err := st.rt.AppendJSON(nil, moejs.Arg(args, 0))
				if err != nil {
					return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.events.query: 参数不可 JSON 序列化", nil)
				}
				if err := json.Unmarshal(raw, &arg); err != nil {
					return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.events.query: 参数形状不符", nil)
				}
			}
			if arg.ThreadID != "" && arg.ThreadID != hc.ThreadID {
				return moejs.Undefined(), throwCoded(rr, codeAccessDenied, "ctx.events.query: 不能查询其它线程", nil)
			}
			q := EventQuery{ThreadID: hc.ThreadID, FromSeq: arg.FromSeq, Types: arg.Types, Limit: arg.Limit}
			ctx := hc.Signal
			if ctx == nil {
				ctx = context.Background()
			}
			views, err := hc.Events(ctx, q)
			if err != nil {
				return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.events.query: "+err.Error(), nil)
			}
			if q.Limit <= 0 {
				q.Limit = 200
			}
			if len(views) > q.Limit {
				views = views[:q.Limit]
			}
			out := make([]map[string]any, 0, len(views))
			for _, v := range views {
				var data any
				if len(v.Data) > 0 {
					if err := json.Unmarshal(v.Data, &data); err != nil {
						return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.events.query: 事件载荷损坏", nil)
					}
				}
				out = append(out, map[string]any{
					"seq": v.Seq, "type": v.Type, "threadId": v.ThreadID,
					"at": v.At.UTC().Format(time.RFC3339Nano), "data": data,
				})
			}
			st.audit("events", "query", fmt.Sprintf("limit=%d", len(out)), len(out), "ok", nil)
			v, err := st.rt.FromGo(out)
			if err != nil {
				return moejs.Undefined(), err
			}
			return resolveConfig(st, v), nil
		}))
	}
	if hc.Usage != nil {
		_ = obj.SetProp(r, r.KeyFromGoString("usage"), st.rt.Function("events.usage", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			type usageArg struct {
				ThreadID string `json:"threadId"`
				Limit    int    `json:"limit"`
			}
			var arg usageArg
			if len(args) > 0 && !moejs.Arg(args, 0).IsNullish() {
				raw, err := st.rt.AppendJSON(nil, moejs.Arg(args, 0))
				if err != nil {
					return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.events.usage: 参数不可 JSON 序列化", nil)
				}
				if err := json.Unmarshal(raw, &arg); err != nil {
					return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.events.usage: 参数形状不符", nil)
				}
			}
			if arg.ThreadID != "" && hc.ThreadID != "" && arg.ThreadID != hc.ThreadID {
				return moejs.Undefined(), throwCoded(rr, codeAccessDenied, "ctx.events.usage: 不能查询其它线程", nil)
			}
			if arg.ThreadID == "" {
				arg.ThreadID = hc.ThreadID
			}
			if arg.Limit <= 0 || arg.Limit > 500 {
				arg.Limit = 500
			}
			ctx := hc.Signal
			if ctx == nil {
				ctx = context.Background()
			}
			rows, err := hc.Usage(ctx, UsageQuery{ThreadID: arg.ThreadID, Limit: arg.Limit})
			if err != nil {
				return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.events.usage: "+err.Error(), nil)
			}
			if len(rows) > arg.Limit {
				rows = rows[:arg.Limit]
			}
			out := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				out = append(out, map[string]any{
					"threadId": row.ThreadID, "provider": row.Provider, "model": row.Model,
					"inputTokens": row.InputTokens, "outputTokens": row.OutputTokens,
					"cacheTokens": row.CacheTokens, "costMicros": row.CostMicros,
					"at": row.At.UTC().Format(time.RFC3339Nano),
				})
			}
			st.audit("events", "usage", fmt.Sprintf("limit=%d", len(out)), len(out), "ok", nil)
			v, err := st.rt.FromGo(out)
			if err != nil {
				return moejs.Undefined(), err
			}
			return resolveConfig(st, v), nil
		}))
	}
	return put("events", engine.ObjectValue(obj))
}
