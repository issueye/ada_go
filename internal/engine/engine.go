// Package engine 只有唯一一份多轮循环（设计 1.3「单一引擎」、INV-1）。
//
// M0 阶段本包提供两件东西：
//   - RunTurn 的签名与不变量声明（M1-A 落地实现）；
//   - 15 个钩子点位的调用方表（HookSites），供 adag verify 证明「设计里的每个点位都有真实调用方」。
//
// 引擎不知道 moejs、不知道 SQLite、不知道 Gin：它只依赖 ports（设计 5.1、3.3）。
// 因此本包不 import adapters/plugins/gateway；M1 也保持这条依赖方向。
package engine

import ()

// Site 描述一个钩子点位的调用方。
type Site struct {
	// Point 是点位名，必须与 plugins.HookPoints() 逐字一致。
	Point string
	// Stage 是 RunTurn 状态机里的调用时机。
	Stage string
	// Caller 是 M1 落地时的引擎函数名；verify 用它证明点位不是孤儿。
	Caller string
	// Blocking 报告该点位是否是安全点：失败方向为关闭，必须等待结果（tool.beforeCall）。
	Blocking bool
}

// hookSites 列出 15 个点位的引擎调用方（设计 5.3 步骤 5–8、6.6 表格）。
//
// M1-A 把这里的 Caller 变成真实函数调用；adag verify 在 M0 检查「点位 ↔ 调用方」双向覆盖，
// 在 M1 起额外检查每个 Caller 在引擎源码里真实存在。
var hookSites = []Site{
	{Point: "session.start", Stage: "Core 装配完成、会话加载后", Caller: "Engine.OnSessionStart"},
	{Point: "session.end", Stage: "Core 优雅退出前", Caller: "Engine.OnSessionEnd"},
	{Point: "thread.created", Stage: "thread.create 提交 entry 后", Caller: "Engine.OnThreadCreated"},
	{Point: "user.message", Stage: "用户消息 entry 追加后、进模型前", Caller: "Engine.OnUserMessage"},
	{Point: "turn.before", Stage: "轮次开始、终止条件初始化前（5.3 第 5 步）", Caller: "Engine.BeforeTurn"},
	{Point: "model.beforeRequest", Stage: "每次模型请求前（5.3 第 6c 步）", Caller: "Engine.BeforeModelRequest"},
	{Point: "model.afterResponse", Stage: "收到显式终止信号后（5.3 第 6e 步）", Caller: "Engine.AfterModelResponse"},
	{Point: "tool.beforeCall", Stage: "参数与 scope 校验后、执行前（5.3 第 6g 步）", Caller: "Engine.BeforeToolCall", Blocking: true},
	{Point: "tool.afterCall", Stage: "工具回执生成后（5.3 第 6g 步）", Caller: "Engine.AfterToolCall"},
	{Point: "compaction.before", Stage: "生成摘要前（5.3 第 6b 步）", Caller: "Engine.BeforeCompaction"},
	{Point: "compaction.after", Stage: "压缩 entry 写入后（5.3 第 6b 步）", Caller: "Engine.AfterCompaction"},
	{Point: "turn.after", Stage: "轮次结束、TurnFinished 前（5.3 第 7 步）", Caller: "Engine.AfterTurn"},
	{Point: "interaction.requested", Stage: "交互桥发出请求后（3.4 第 7 步）", Caller: "Engine.OnInteractionRequested"},
	{Point: "plugin.loaded", Stage: "插件贡献原子提交后（6.5）", Caller: "Engine.OnPluginLoaded"},
	{Point: "error", Stage: "轮次内可恢复错误（5.3 终止条件 error 分支）", Caller: "Engine.OnTurnError"},
}

// HookSites 返回全部点位调用方（只读副本）。
func HookSites() []Site { return append([]Site(nil), hookSites...) }

// SiteFor 按点位名找调用方。
func SiteFor(point string) (Site, bool) {
	for _, s := range hookSites {
		if s.Point == point {
			return s, true
		}
	}
	return Site{}, false
}
