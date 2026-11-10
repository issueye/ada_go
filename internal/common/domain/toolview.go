package domain

// 工具可见性：同一份描述符快照，按调用方投影出不同的视图。
//
// 规则只有这一处（02 §5.4、05 §6.3、08 §8.4）：模型工具声明、find_tool 的目录投影、
// tool.list 与诊断都必须问这里，不能各自再写一遍过滤条件——否则「模型看得到」与
// 「exec_tool 能执行」会漂移，而这类漂移只会在模型试着调用一个它以为存在、实际
// 被过滤掉的工具时暴露。
//
// 这里刻意只是纯函数：engine 不能 import toolkit（依赖方向），所以规则的落点必须
// 在 domain。
type ToolVisibility int

const (
	// ToolVisibleInjected 是注入式视图：随模型请求下发的工具声明。
	// 包含系统入口 find_tool/exec_tool（它们本身就是注入式工具）。
	ToolVisibleInjected ToolVisibility = iota
	// ToolVisibleDiscovery 是发现式视图：不注入模型清单，经 find_tool 发现后
	// 由 exec_tool 执行。
	ToolVisibleDiscovery
	// ToolVisibleSystem 只含宿主为内置发现桥预留的入口（find_tool/exec_tool）。
	ToolVisibleSystem
	// ToolVisibleAll 是全量视图：tool.list、诊断与统计使用。
	ToolVisibleAll
)

// String 返回稳定名字（诊断与测试断言用）。
func (v ToolVisibility) String() string {
	switch v {
	case ToolVisibleInjected:
		return "injected"
	case ToolVisibleDiscovery:
		return "discovery"
	case ToolVisibleSystem:
		return "system"
	case ToolVisibleAll:
		return "all"
	default:
		return "unknown"
	}
}

// Visible 报告某个描述符是否属于该视图。
//
// 注意 discovery 与 injected 是互补的两个真子集：工具怎么用由它自己的声明决定，
// 视图不猜测、不改写描述符。
func (v ToolVisibility) Visible(d Descriptor) bool {
	switch v {
	case ToolVisibleInjected:
		return !d.Discovery
	case ToolVisibleDiscovery:
		return d.Discovery
	case ToolVisibleSystem:
		return d.System
	case ToolVisibleAll:
		return true
	default:
		return false
	}
}

// VisibleTools 按视图过滤一份目录快照，保持入参顺序。
func VisibleTools(list []Descriptor, v ToolVisibility) []Descriptor {
	out := make([]Descriptor, 0, len(list))
	for _, d := range list {
		if v.Visible(d) {
			out = append(out, d)
		}
	}
	return out
}

// Discoverable 报告该描述符能否经 exec_tool 执行。
//
// 这是「只有 find_tool 列出的工具才能这样执行」的机器化表达（05 §6.3）：
// 发现式工具可以，注入式工具不行——后者模型本来就直呼其名，再开一条 exec_tool
// 旁路只会让审批与循环检测多一套入口。
func (d Descriptor) Discoverable() bool { return d.Discovery }
