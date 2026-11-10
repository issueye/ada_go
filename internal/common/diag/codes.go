// Package diag 是诊断码词汇表的唯一位置（设计 8.5）。
//
// 页面与日志只显示「码 + 人话 + 下一步」，不显示堆栈。格式固定为
// ADG-<AREA>-<NNN>，区域枚举与 spec/proto.json 里每个错误码的
// diagnosticArea 必须逐字一致，adag verify 会交叉核对。
package diag

import (
	"fmt"
	"strconv"
	"strings"
)

// Area 是诊断码的区域段。
type Area string

// 区域集合是封闭的：新增区域视为一次契约变更，需要先改设计文档。
const (
	AreaWS     Area = "WS"     // 工作区与实例生命周期
	AreaCore   Area = "CORE"   // Core 进程
	AreaThread Area = "THREAD" // 线程与轮次
	AreaTool   Area = "TOOL"   // 工具目录与执行
	AreaPlugin Area = "PLUGIN" // 插件宿主与装配
	AreaAuth   Area = "AUTH"   // 鉴权、CSRF、Origin
	AreaModel  Area = "MODEL"  // provider 与模型流
	AreaStore  Area = "STORE"  // SQLite 与备份
	AreaUI     Area = "UI"     // 界面与渲染
	AreaConfig Area = "CFG"    // 配置与产品声明
)

// Areas 返回全部合法区域，供门禁与模糊测试枚举。
func Areas() []Area {
	return []Area{
		AreaWS, AreaCore, AreaThread, AreaTool, AreaPlugin,
		AreaAuth, AreaModel, AreaStore, AreaUI, AreaConfig,
	}
}

// Valid 报告 s 是否是已定义区域。
func (a Area) Valid() bool {
	for _, x := range Areas() {
		if a == x {
			return true
		}
	}
	return false
}

// Code 是一个具体诊断码。
type Code struct {
	Area Area
	Num  int
}

// String 渲染 ADG-<AREA>-<NNN>（序号固定三位，不足补零）。
func (c Code) String() string {
	return fmt.Sprintf("ADG-%s-%03d", string(c.Area), c.Num)
}

// Parse 反解一个诊断码字符串。
func Parse(s string) (Code, error) {
	const prefix = "ADG-"
	if !strings.HasPrefix(s, prefix) {
		return Code{}, fmt.Errorf("diag: 缺少前缀 %q: %q", prefix, s)
	}
	rest := strings.TrimPrefix(s, prefix)
	area, num, ok := strings.Cut(rest, "-")
	if !ok {
		return Code{}, fmt.Errorf("diag: 缺少序号分隔符: %q", s)
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 0 || n > 999 {
		return Code{}, fmt.Errorf("diag: 序号非法: %q", s)
	}
	c := Code{Area: Area(area), Num: n}
	if !c.Area.Valid() {
		return Code{}, fmt.Errorf("diag: 未知区域: %q", s)
	}
	return c, nil
}

// Entry 把一个应用错误码（spec/proto.json 的 code/name）绑定到诊断码与人话下一步。
type Entry struct {
	AppCode  int    // spec/proto.json errorCodes[].code
	Name     string // spec/proto.json errorCodes[].name
	Diag     Code   // 面向日志与界面的诊断码
	NextStep string // 页面上的「下一步」文案
}

// 应用错误码 → 诊断码的唯一映射表。
//
// 数值分配规则：区域段固定，序号段按 spec/proto.json 的 8.5 表顺序递增，
// 已分配的号不复用；新增错误码追加到对应区域末尾。
var table = []Entry{
	{1001, "workspace_missing", Code{AreaWS, 1}, "选择存在的工作区目录，或先在磁盘上创建它。"},
	{1002, "core_unavailable", Code{AreaCore, 1}, "等待内核自动重启，或在设置里手动重启该工作区。"},
	{1003, "core_busy", Code{AreaCore, 2}, "实例正在启动或回收，稍后重试。"},
	{1004, "workspace_locked", Code{AreaWS, 2}, "同一工作区只允许一个内核：结束占用者，或换一份数据目录（adag doctor 能报出持有者 pid）。"},
	{2001, "thread_busy", Code{AreaThread, 1}, "该线程已有运行中的轮次；先等待或取消。"},
	{2002, "turn_aborted", Code{AreaThread, 2}, "轮次已按你的请求取消，已产生的内容保留在会话里。"},
	{2003, "budget_exceeded", Code{AreaThread, 3}, "达到产品显式配置的墙钟/token 上限；需要更长轮次就调大上限（默认不限制）。"},
	{2004, "context_overflow", Code{AreaThread, 4}, "压缩后仍超出上下文窗口；开新线程或让模型换用更短的回顾。"},
	{2005, "loop_detected", Code{AreaThread, 5}, "模型连续重复同样的工具调用且未改道；补充信息或明确要求换方法。"},
	{3001, "tool_not_found", Code{AreaTool, 1}, "贡献该工具的插件已被禁用；在插件页重新启用或重载。"},
	{3002, "tool_invalid_args", Code{AreaTool, 2}, "参数不符合 Schema；按工具描述重新给出参数。"},
	{3003, "plugin_timeout", Code{AreaPlugin, 1}, "插件调用超时并已重建运行时；重试一次，持续超时请查看插件日志。"},
	{3004, "plugin_crashed", Code{AreaPlugin, 2}, "插件运行时失败并已隔离；其余功能不受影响，可手动重载该插件。"},
	{3005, "plugin_denied", Code{AreaPlugin, 3}, "能力或信任级别不允许该调用；检查清单里的 capabilities 与 trust。"},
	{4001, "auth_required", Code{AreaAuth, 1}, "登录已过期；重新打开 /__auth 获取会话。"},
	{4002, "csrf_invalid", Code{AreaAuth, 2}, "表单 token 不匹配；刷新页面后重新提交。"},
	{4003, "origin_denied", Code{AreaAuth, 3}, "Origin/Host 校验失败；从配置的地址访问本站。"},
	{5001, "provider_error", Code{AreaModel, 1}, "模型服务返回错误；查看供应商配置与网络，必要时切换模型。"},
	{5002, "protocol_error", Code{AreaModel, 2}, "适配器流解析失败；确认端点与官方 Chat/Message 协议一致。"},
	{5003, "storage_busy", Code{AreaStore, 1}, "数据库写锁等待超时；减少并发写入或检查磁盘与网络盘。"},
	{5004, "storage_corrupt", Code{AreaStore, 2}, "WAL 或数据库损坏；运行 adag doctor，必要时用最近的备份恢复。"},
}

// entries 建立 name/appCode 两个索引（只读，初始化一次）。
var (
	byName    = make(map[string]Entry, len(table))
	byAppCode = make(map[int]Entry, len(table))
)

func init() {
	for _, e := range table {
		byName[e.Name] = e
		byAppCode[e.AppCode] = e
	}
}

// Table 返回全部诊断条目（供 adag verify 与文档一致性检查）。
func Table() []Entry {
	out := make([]Entry, len(table))
	copy(out, table)
	return out
}

// For 按应用错误码取诊断条目。
func For(appCode int) (Entry, bool) {
	e, ok := byAppCode[appCode]
	return e, ok
}

// ForName 按应用错误码名称取诊断条目。
func ForName(name string) (Entry, bool) {
	e, ok := byName[name]
	return e, ok
}
