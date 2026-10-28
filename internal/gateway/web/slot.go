// Package web 是 Gateway 的 SSR 宿主。M0 阶段它只携带一件契约：插槽宿主表。
//
// 插槽集合是稳定的兼容承诺（设计 7.2）：12 个名字与位置由 internal/plugins 声明，
// 这里声明「谁消费它们」——每个插槽都必须有一个明确的宿主位置与空贡献回退，
// adag verify 双向核对，任何一边多出或少掉都算漂移。
package web

import (
	"fmt"
	"html/template"
	"strconv"

	"github.com/adag/ada-go/internal/plugins"
)

// SlotHost 描述一个插槽在页面外壳中的宿主位置与回退行为。
type SlotHost struct {
	// Slot 是契约名，必须与 plugins.Slots() 逐字一致。
	Slot string
	// Template 是承载它的模板片段名（M2 落地 web/templates/）。
	Template string
	// Region 是人话位置（照 7.2 表格）。
	Region string
	// BaseOwned 报告外壳是否属于基座：插件只能填内容，不能替换托盘与安全操作（1.5.6）。
	BaseOwned bool
	// Fallback 是空贡献时的界面表现（7.3：贡献为空时回退）。
	Fallback string
}

// slotHosts 是 12 个插槽的宿主声明。
var slotHosts = []SlotHost{
	{"sidebar.top", "partials", "侧栏品牌区下方", false, "留空，不占位"},
	{"sidebar.bottom", "partials", "侧栏导航之后", false, "留空，不占位"},
	{"topbar.right", "partials", "顶栏右侧操作区", false, "只剩基座自有的模式与停止控件"},
	{"chat.corner", "thread", "会话区右上角悬浮", false, "不渲染浮层，避免顶动对话流"},
	{"composer.left", "thread", "输入框工具行左侧", false, "工具行只剩基座控件"},
	{"composer.right", "thread", "输入框工具行右侧", false, "工具行只剩基座控件"},
	{"composer.footer", "thread", "输入框下方辅助区", false, "显示状态与遥测，不显示插件项"},
	{"composer.above", "thread", "输入框上方悬浮区（sticky）", false, "不渲染浮层，与「回到最新」互不遮挡"},
	{"thread.tray", "thread", "交互托盘内的插件位", true, "托盘外壳与安全操作仍由基座渲染"},
	{"transcript.after", "thread", "对话流末尾", false, "留空"},
	{"extensions.list", "thread", "插件列表页（M2 暂由会话外壳承载空槽，M4 迁到扩展页）", false, "只显示基座的插件状态表"},
	{"dashboard.widgets", "thread", "概览页卡片区（M2 暂由会话外壳承载空槽，M4 迁到概览页）", false, "显示空状态，不放假统计"},
}

// RenderSlot 渲染一个插槽宿主容器（7.2、7.3）。
//
// 无论插件是否贡献内容都输出稳定的 `.ada-slot` 外壳与 `data-slot`，这样：
//   - SSR 期就能落下 12 个可被 CSS/测试定位的宿主点；
//   - 空贡献时容器内回退到 fallback，主应用照常工作。
//
// body 与 fallback 都要求是基座生成的 template.HTML：插件/消息文本在进入这里
// 之前必须已经由 html/template 默认转义，绝不把 string 直接拼进输出。
func RenderSlot(name string, body template.HTML, contributors int, fallback template.HTML) template.HTML {
	content := body
	if content == "" {
		content = fallback
	}
	if contributors < 0 {
		contributors = 0
	}
	return template.HTML(
		`<div class="ada-slot" data-slot="` + template.HTMLEscapeString(name) +
			`" data-contributors="` + strconv.Itoa(contributors) + `">` +
			string(content) + `</div>`,
	)
}

// SlotHosts 返回全部宿主声明（只读副本）。
func SlotHosts() []SlotHost { return append([]SlotHost(nil), slotHosts...) }

// HostFor 按插槽名找宿主。
func HostFor(slot string) (SlotHost, bool) {
	for _, h := range slotHosts {
		if h.Slot == slot {
			return h, true
		}
	}
	return SlotHost{}, false
}

// CheckSlotCoverage 双向核对契约注册表与宿主表：名字集合必须完全一致，
// 且基座所有权标记不能互相矛盾（这是 adag verify 的一项检查）。
func CheckSlotCoverage() error {
	contract := plugins.Slots()
	if len(contract) != len(slotHosts) {
		return fmt.Errorf("web: 插槽数量不一致：契约 %d 个，宿主 %d 个", len(contract), len(slotHosts))
	}
	for _, c := range contract {
		h, ok := HostFor(c.Name)
		if !ok {
			return fmt.Errorf("web: 契约插槽 %q 没有宿主", c.Name)
		}
		if h.BaseOwned != c.BaseOwned {
			return fmt.Errorf("web: 插槽 %q 的基座所有权标记不一致（契约 %v，宿主 %v）", c.Name, c.BaseOwned, h.BaseOwned)
		}
	}
	for _, h := range slotHosts {
		if !plugins.IsSlotName(h.Slot) {
			return fmt.Errorf("web: 宿主声明了未知插槽 %q", h.Slot)
		}
	}
	return nil
}
