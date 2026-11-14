package design

import (
	"fmt"
	"html"
	"strings"
)

// componentCatalog 是全体体系共享的组件清单（HTML 结构一致，皮肤在 StyleSheet）。
var componentCatalog = map[string]Component{
	"button": {ID: "button", Name: "按钮", Category: "general",
		Description: "触发一个即时操作；主操作唯一，次操作用文字/描边。",
		Variants:    []string{"primary", "secondary", "danger", "text", "sizes"},
		Usage:       []string{"一个视图内 primary 按钮最多一个", "破坏性操作用 danger 变体并二次确认"}},
	"input": {ID: "input", Name: "输入框", Category: "form",
		Description: "单行文本输入，含标签、占位与错误态。",
		Variants:    []string{"default", "error", "disabled"},
		Usage:       []string{"标签置于输入框上方", "错误信息紧跟输入框并用状态色"}},
	"select": {ID: "select", Name: "选择器", Category: "form",
		Description: "从有限选项中选择一项，收起态与输入框一致。",
		Variants:    []string{"default", "disabled"},
		Usage:       []string{"选项 5 个以上用选择器而非单选组", "占位文案用「请选择」类中性描述"}},
	"switch": {ID: "switch", Name: "开关", Category: "form",
		Description: "两项即时切换（开/关），变更立即生效。",
		Variants:    []string{"on", "off"},
		Usage:       []string{"即时生效的操作才用开关，需提交的用复选框", "开关左侧放标题右侧放控件，保持行对齐"}},
	"checkbox": {ID: "checkbox", Name: "复选框", Category: "form",
		Description: "多选或协议勾选；选中态用主色填充。",
		Variants:    []string{"checked", "unchecked"},
		Usage:       []string{"同组复选框纵向排列间距 12", "列表全选可用半选态"}},
	"radio": {ID: "radio", Name: "单选框", Category: "form",
		Description: "互斥选择，选项数量 2-5 时使用。",
		Variants:    []string{"selected", "unselected"},
		Usage:       []string{"选项横向排列时保持 24 间距", "默认选中第一项或最常用项"}},
	"slider": {ID: "slider", Name: "滑块", Category: "form",
		Description: "在连续区间内取值，支持当前值气泡。",
		Variants:    []string{"default"},
		Usage:       []string{"展示当前值于滑块右侧", "拖动过程中实时更新数值"}},
	"card": {ID: "card", Name: "卡片", Category: "general",
		Description: "聚合标题、正文与操作的内容容器。",
		Variants:    []string{"with-actions", "plain"},
		Usage:       []string{"卡片内边距 16（紧凑 12）", "操作按钮放卡片底部右对齐"}},
	"tag": {ID: "tag", Name: "标签", Category: "data",
		Description: "标记状态或分类的小色块，分主色/成功/警告/危险/信息。",
		Variants:    []string{"primary", "success", "warning", "danger", "info"},
		Usage:       []string{"同屏标签不超过 3 种颜色", "文字颜色与浅底同源保证对比度"}},
	"badge": {ID: "badge", Name: "徽标", Category: "data",
		Description: "出现在图标/头像右上角的数字或圆点提醒。",
		Variants:    []string{"count", "dot"},
		Usage:       []string{"计数超过 99 显示 99+", "无数字提醒用圆点"}},
	"avatar": {ID: "avatar", Name: "头像", Category: "data",
		Description: "用户/实体标识，圆形，支持文字与图片回退。",
		Variants:    []string{"image-text", "sizes"},
		Usage:       []string{"无头像时用背景色 + 首字", "尺寸阶梯 24/32/40/56"}},
	"alert": {ID: "alert", Name: "通告", Category: "feedback",
		Description: "页面级提示（info/success/warning/danger），浅底 + 状态色文字。",
		Variants:    []string{"info", "success", "warning", "danger"},
		Usage:       []string{"置于页面/卡片顶部", "不要连续堆叠多条 alert"}},
	"progress": {ID: "progress", Name: "进度条", Category: "feedback",
		Description: "线性进度指示，支持百分比文案。",
		Variants:    []string{"default", "success", "danger"},
		Usage:       []string{"进度文案靠右对齐", "接近满值时切换 success 色"}},
	"navbar": {ID: "navbar", Name: "顶部导航栏", Category: "navigation",
		Description: "移动端页面顶栏：返回 + 标题 + 右侧动作。",
		Variants:    []string{"default"},
		Usage:       []string{"标题居中，左右动作等宽保证居中", "滚动后可加底部分隔或毛玻璃"}},
	"tabbar": {ID: "tabbar", Name: "底部标签栏", Category: "navigation",
		Description: "移动端底部导航，3-5 个入口，当前项高亮。",
		Variants:    []string{"default"},
		Usage:       []string{"当前项用主色，其余用次级文字色", "图标 + 11-12px 标签"}},
	"list": {ID: "list", Name: "列表", Category: "data",
		Description: "分组数据行：标题 + 说明 + 右侧值/箭头。",
		Variants:    []string{"grouped"},
		Usage:       []string{"行高 ≥44 保证触达", "分组间用背景色差或分组标题"}},
	"dialog": {ID: "dialog", Name: "对话框", Category: "feedback",
		Description: "模态弹窗：标题 + 内容 + 取消/确认双操作。",
		Variants:    []string{"confirm"},
		Usage:       []string{"确认按钮在右，取消在左", "内容避免滚动，超长改用独立页面"}},
	"toast": {ID: "toast", Name: "轻提示", Category: "feedback",
		Description: "操作反馈的短时浮层，居中顶部或底部。",
		Variants:    []string{"success", "error", "info"},
		Usage:       []string{"文案一句话，不承载操作", "停留 2-3 秒自动消失"}},
	"tabs": {ID: "tabs", Name: "标签页", Category: "navigation",
		Description: "同层内容的平级切换（2-5 个），当前项用主色下划线标记。",
		Variants:    []string{"active", "with-badge"},
		Usage:       []string{"超过 5 个改用下拉或抽屉", "下划线跟随当前项，勿与底部标签栏混用"}},
	"table": {ID: "table", Name: "表格", Category: "data",
		Description: "结构化数据的多列展示：表头 + 行分隔，数值列右对齐。",
		Variants:    []string{"bordered"},
		Usage:       []string{"列头左对齐、数值右对齐并用等宽数字", "移动端避免横向滚动，改用列表卡片"}},
	"pagination": {ID: "pagination", Name: "分页", Category: "navigation",
		Description: "长列表的分页控制：上一页 / 页码 / 省略号 / 下一页。",
		Variants:    []string{"default"},
		Usage:       []string{"页码超过 7 个时折叠省略号", "总数文案放分页器右侧"}},
}

// esc 转义 HTML 文本。
func esc(s string) string { return html.EscapeString(s) }

// renderComponent 生成演示片段（图标占位符在此统一展开为 <svg>）。
func renderComponent(compID string) (string, error) {
	frag, err := componentTemplate(compID)
	if err != nil {
		return "", err
	}
	return ExpandIcons(frag), nil
}

// componentTemplate 返回带 {{icon:...}} 占位符的原始片段。
func componentTemplate(compID string) (string, error) {
	if _, valid := componentCatalog[compID]; !valid {
		return "", fmt.Errorf("unknown component %q", compID)
	}
	switch compID {
	case "button":
		return `
<div class="ds-demo-row">
  <button class="ds-btn ds-btn--primary">主操作</button>
  <button class="ds-btn ds-btn--secondary">次操作</button>
  <button class="ds-btn ds-btn--danger">删除</button>
  <button class="ds-btn ds-btn--text">文字按钮</button>
</div>
<div class="ds-demo-row">
  <button class="ds-btn ds-btn--primary ds-btn--small">小按钮</button>
  <button class="ds-btn ds-btn--primary">中按钮</button>
  <button class="ds-btn ds-btn--primary ds-btn--large">大按钮</button>
</div>`, nil
	case "input":
		return `
<div class="ds-field">
  <label class="ds-label">账号</label>
  <input class="ds-input" placeholder="请输入账号" />
  <div class="ds-help">支持手机号 / 邮箱</div>
</div>
<div class="ds-field">
  <label class="ds-label">手机号</label>
  <input class="ds-input ds-input--error" value="13800138" />
  <div class="ds-help ds-help--error">手机号位数不正确</div>
</div>
<div class="ds-field">
  <label class="ds-label">邀请码</label>
  <input class="ds-input" placeholder="不可修改" disabled />
</div>`, nil
	case "select":
		return `
<div class="ds-field">
  <label class="ds-label">所属部门</label>
  <div class="ds-select"><span>研发中心</span><span class="ds-select__arrow">{{icon:chevron-down:16}}</span></div>
</div>
<div class="ds-field">
  <label class="ds-label">城市</label>
  <div class="ds-select ds-select--disabled"><span>请选择</span><span class="ds-select__arrow">{{icon:chevron-down:16}}</span></div>
</div>`, nil
	case "switch":
		return `
<div class="ds-row-between"><div><div class="ds-list__title">消息通知</div><div class="ds-list__desc">接收商品与物流提醒</div></div>
  <span class="ds-switch ds-switch--on"><span class="ds-switch__thumb"></span></span></div>
<div class="ds-row-between"><div><div class="ds-list__title">省流量模式</div><div class="ds-list__desc">仅 Wi-Fi 下加载图片</div></div>
  <span class="ds-switch"><span class="ds-switch__thumb"></span></span></div>`, nil
	case "checkbox":
		return `
<div class="ds-check-group">
  <label class="ds-checkbox ds-checkbox--checked"><span class="ds-checkbox__box">{{icon:check:12}}</span> 全部消息</label>
  <label class="ds-checkbox"><span class="ds-checkbox__box"></span> 仅重要事项</label>
  <label class="ds-checkbox"><span class="ds-checkbox__box"></span> 营销活动</label>
</div>`, nil
	case "radio":
		return `
<div class="ds-check-group">
  <label class="ds-radio ds-radio--checked"><span class="ds-radio__dot"></span> 标准配送</label>
  <label class="ds-radio"><span class="ds-radio__dot"></span> 次日达</label>
  <label class="ds-radio"><span class="ds-radio__dot"></span> 自提点自取</label>
</div>`, nil
	case "slider":
		return `
<div class="ds-field">
  <div class="ds-row-between"><span class="ds-label">音量</span><span class="ds-slider__value">68%</span></div>
  <div class="ds-slider"><div class="ds-slider__track"><div class="ds-slider__fill" style="width:68%"></div></div><div class="ds-slider__thumb" style="left:68%"></div></div>
</div>`, nil
	case "card":
		return `
<div class="ds-card">
  <div class="ds-card__title">会员权益</div>
  <div class="ds-card__body">开通后可享专属折扣、优先客服与每月免邮额度。权益明细以购买页展示为准。</div>
  <div class="ds-card__actions"><button class="ds-btn ds-btn--text">查看明细</button><button class="ds-btn ds-btn--primary">立即开通</button></div>
</div>
<div class="ds-card">
  <div class="ds-card__title">无操作卡片</div>
  <div class="ds-card__body">仅承载内容的朴素卡片。</div>
</div>`, nil
	case "tag":
		return `
<div class="ds-demo-row">
  <span class="ds-tag ds-tag--primary">进行中</span>
  <span class="ds-tag ds-tag--success">已通过</span>
  <span class="ds-tag ds-tag--warning">待审核</span>
  <span class="ds-tag ds-tag--danger">已驳回</span>
  <span class="ds-tag ds-tag--info">草稿</span>
</div>`, nil
	case "badge":
		return `
<div class="ds-demo-row ds-demo-row--loose">
  <span class="ds-badge-wrap"><span class="ds-avatar ds-avatar--40 ds-avatar--tint">Y</span><span class="ds-badge ds-badge--count">8</span></span>
  <span class="ds-badge-wrap"><span class="ds-avatar ds-avatar--40 ds-avatar--tint">A</span><span class="ds-badge ds-badge--count ds-badge--99">99+</span></span>
  <span class="ds-badge-wrap"><span class="ds-avatar ds-avatar--40 ds-avatar--tint">M</span><span class="ds-badge ds-badge--dot"></span></span>
</div>`, nil
	case "avatar":
		return `
<div class="ds-demo-row ds-demo-row--loose">
  <span class="ds-avatar ds-avatar--24 ds-avatar--tint">小</span>
  <span class="ds-avatar ds-avatar--32 ds-avatar--tint">米</span>
  <span class="ds-avatar ds-avatar--40 ds-avatar--tint">朵</span>
  <span class="ds-avatar ds-avatar--56 ds-avatar--tint">朵</span>
</div>`, nil
	case "alert":
		return `
<div class="ds-alert ds-alert--info"><span class="ds-alert__icon">{{icon:info:12}}</span><span>这是一条信息通告：今晚 22:00 系统维护。</span></div>
<div class="ds-alert ds-alert--success"><span class="ds-alert__icon">{{icon:check:12}}</span><span>保存成功，配置已生效。</span></div>
<div class="ds-alert ds-alert--warning"><span class="ds-alert__icon">{{icon:warning:12}}</span><span>存储空间不足 10%，请及时清理。</span></div>
<div class="ds-alert ds-alert--danger"><span class="ds-alert__icon">{{icon:danger:12}}</span><span>支付失败，请更换支付方式后重试。</span></div>`, nil
	case "progress":
		return `
<div class="ds-field">
  <div class="ds-row-between"><span class="ds-label">上传中</span><span class="ds-progress__label">72%</span></div>
  <div class="ds-progress"><div class="ds-progress__fill" style="width:72%"></div></div>
</div>
<div class="ds-field">
  <div class="ds-row-between"><span class="ds-label">剩余空间</span><span class="ds-progress__label ds-progress__label--danger">5%</span></div>
  <div class="ds-progress"><div class="ds-progress__fill ds-progress__fill--danger" style="width:5%"></div></div>
</div>`, nil
	case "tabs":
		return `
<div class="ds-tabs">
  <div class="ds-tabs__item ds-tabs__item--active">全部</div>
  <div class="ds-tabs__item">待发货<span class="ds-tabs__badge">12</span></div>
  <div class="ds-tabs__item">已收货</div>
  <div class="ds-tabs__item">售后</div>
</div>
<div class="ds-tabs">
  <div class="ds-tabs__item">商品</div>
  <div class="ds-tabs__item ds-tabs__item--active">评价 2.1k</div>
  <div class="ds-tabs__item">详情</div>
  <div class="ds-tabs__item">推荐</div>
</div>`, nil
	case "table":
		return `
<div class="ds-table-wrap">
  <table class="ds-table">
    <thead><tr><th>订单号</th><th>商品</th><th class="ds-table__num">金额</th><th>状态</th></tr></thead>
    <tbody>
      <tr><td>#202601</td><td>无线键盘</td><td class="ds-table__num">¥199.00</td><td><span class="ds-tag ds-tag--success">已支付</span></td></tr>
      <tr><td>#202602</td><td>机械键盘</td><td class="ds-table__num">¥599.00</td><td><span class="ds-tag ds-tag--warning">待发货</span></td></tr>
      <tr><td>#202603</td><td>键帽套装</td><td class="ds-table__num">¥89.00</td><td><span class="ds-tag ds-tag--danger">已退款</span></td></tr>
    </tbody>
  </table>
</div>`, nil
	case "pagination":
		return `
<div class="ds-row-between">
  <div class="ds-pagination">
    <button class="ds-page-btn" aria-label="上一页">{{icon:chevron-left:14}}</button>
    <button class="ds-page-btn ds-page-btn--active">1</button>
    <button class="ds-page-btn">2</button>
    <button class="ds-page-btn">3</button>
    <span class="ds-pagination__ellipsis">…</span>
    <button class="ds-page-btn">9</button>
    <button class="ds-page-btn" aria-label="下一页">{{icon:chevron-right:14}}</button>
  </div>
  <span class="ds-pagination__total">共 86 条</span>
</div>`, nil
	case "navbar":
		return `
<div class="ds-navbar">
  <span class="ds-navbar__side ds-navbar__back">{{icon:chevron-left:16}} 返回</span>
  <span class="ds-navbar__title">订单详情</span>
  <span class="ds-navbar__side ds-navbar__right">{{icon:more:16}}</span>
</div>`, nil
	case "tabbar":
		return `
<div class="ds-tabbar">
  <div class="ds-tabbar__item ds-tabbar__item--active"><span class="ds-tabbar__icon">{{icon:home:20}}</span><span>首页</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">{{icon:compass:20}}</span><span>发现</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">{{icon:plus:20}}</span><span>发布</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">{{icon:message:20}}</span><span>消息</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">{{icon:user:20}}</span><span>我的</span></div>
</div>`, nil
	case "list":
		return `
<div class="ds-list">
  <div class="ds-list__item"><div class="ds-list__main"><div class="ds-list__title">账户与安全</div></div><span class="ds-list__value">已保护</span><span class="ds-list__arrow">{{icon:chevron-right:16}}</span></div>
  <div class="ds-list__item"><div class="ds-list__main"><div class="ds-list__title">通用设置</div></div><span class="ds-list__arrow">{{icon:chevron-right:16}}</span></div>
  <div class="ds-list__item"><div class="ds-list__main"><div class="ds-list__title">深色模式</div><div class="ds-list__desc">跟随系统</div></div><span class="ds-list__value">自动</span><span class="ds-list__arrow">{{icon:chevron-right:16}}</span></div>
</div>`, nil
	case "dialog":
		return `
<div class="ds-dialog-demo">
  <div class="ds-dialog">
    <div class="ds-dialog__title">删除订单？</div>
    <div class="ds-dialog__body">删除后不可恢复，相关优惠券将一并退回账户。</div>
    <div class="ds-dialog__actions">
      <button class="ds-btn ds-btn--secondary">取消</button>
      <button class="ds-btn ds-btn--danger">删除</button>
    </div>
  </div>
</div>`, nil
	case "toast":
		return `
<div class="ds-demo-row ds-demo-row--wrap">
  <div class="ds-toast ds-toast--success"><span class="ds-toast__icon">{{icon:check:14}}</span>已提交</div>
  <div class="ds-toast ds-toast--error"><span class="ds-toast__icon">{{icon:close:14}}</span>网络异常，请重试</div>
  <div class="ds-toast ds-toast--info">已复制到剪贴板</div>
</div>`, nil
	}
	return "", fmt.Errorf("component %q has no template", compID)
}

// RenderComponent 对外渲染入口。
func RenderComponent(compID string) (string, error) {
	return renderComponent(compID)
}

// ComposeInput 是 Compose 的入参。
type ComposeInput struct {
	SystemID   string
	Theme      ThemeKey
	Components []string // 组件 ID 顺序即展示顺序
	Title      string
	Artboard   string // 画板 ID；空 = 按体系 Family 取默认画板
}

// ComposeResult 是生成的预览文档。
type ComposeResult struct {
	HTML     string // 完整 HTML 文档
	SystemID string
	Theme    ThemeKey
	Artboard string   // 实际生效的画板 ID
	Width    int      // 渲染宽度（取自画板，不再写死 420/390）
	Height   int      // 初始渲染高度；客户端渲染后按内容自适应修正
	Missing  []string // 无效组件 ID
	CSSBytes int      // StyleSheet 字节数（提示信息量）
}

// Compose 把若干组件拼成一张完整预览 HTML 文档。
func Compose(in ComposeInput) (*ComposeResult, error) {
	sys, ok := GetSystem(SanitizeID(in.SystemID))
	if !ok {
		return nil, fmt.Errorf("unknown system %q", in.SystemID)
	}
	ab, ok := ResolveArtboard(in.Artboard, sys.Family)
	if !ok {
		return nil, fmt.Errorf("unknown artboard %q（可用 design_artboard 查看）", in.Artboard)
	}
	var missing []string
	var body strings.Builder
	for _, c := range in.Components {
		id := SanitizeID(c)
		if _, valid := componentCatalog[id]; !valid {
			missing = append(missing, c)
			continue
		}
		frag, err := renderComponent(id)
		if err != nil {
			missing = append(missing, c)
			continue
		}
		body.WriteString(`<section class="ds-block"><div class="ds-block__head"><span class="ds-block__name">` +
			esc(componentCatalog[id].Name) + `</span><span class="ds-block__id">` + esc(id) +
			`</span></div>` + frag + `</section>`)
	}
	if body.Len() == 0 {
		return nil, fmt.Errorf("no valid components in %v", in.Components)
	}
	_, effTheme := sys.TokensFor(in.Theme)
	css := StyleSheet(sys, effTheme)
	title := in.Title
	if title == "" {
		title = sys.Name + " 组件预览"
	}
	// 初始高度只是首帧估值，客户端加载后按 .ds-page 实测高度回写（auto_height）
	height := 200 + len(in.Components)*140
	if height < 320 {
		height = 320
	}
	if height > 1800 {
		height = 1800
	}
	doc := buildDoc(css, body.String(), title, sys.ID, string(effTheme), ab)
	return &ComposeResult{
		HTML: doc, SystemID: sys.ID, Theme: effTheme, Artboard: ab.ID,
		Width: ab.Width, Height: height, Missing: missing, CSSBytes: len(css),
	}, nil
}

// buildDoc 拼装完整 HTML 文档：内联 StyleSheet + 画板变量 + 图标精灵。
// 全仓唯一文档出口，保证「一份结构 × 多份皮肤」在预览/导出/截图三处完全一致。
func buildDoc(css, body, title, sysID, theme string, ab Artboard) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\" data-system=\"" + esc(sysID) +
		"\" data-theme=\"" + esc(theme) + "\" data-artboard=\"" + esc(ab.ID) +
		"\" data-font-license=\"" + esc(StackLicenseLabel(fontFamilyOf(sysID))) + "\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>" + esc(title) + "</title>\n")
	b.WriteString("<style>\n" + css + "\n" + PageCSS(ab) + "</style>\n</head>\n")
	// 图标精灵必须内联：预览是 srcdoc 文档，外链相对路径无基准，截图会 404
	b.WriteString("<body class=\"ds-surface\">\n" + IconSprite() + "\n<main class=\"ds-page\">\n")
	b.WriteString(`<header class="ds-page__head"><span class="ds-page__title">` + esc(title) + `</span><span class="ds-page__meta">` + esc(sysID) + " · " + esc(theme) + `</span></header>` + "\n")
	b.WriteString(body)
	b.WriteString("\n</main>\n</body>\n</html>\n")
	return b.String()
}

// fontFamilyOf 取体系的字族串（供文档级授权标注）。
func fontFamilyOf(sysID string) string {
	if sys, ok := GetSystem(sysID); ok {
		return sys.Light.Type.Family
	}
	return DefaultStack("latin")
}

// StandaloneDoc 把任意 body 片段（HTML）包装为使用某体系皮肤的完整文档。
func StandaloneDoc(sys *System, theme ThemeKey, title, bodyHTML string) string {
	css := StyleSheet(sys, theme)
	ab := DefaultArtboardFor(sys.Family)
	return buildDoc(css, ExpandIcons(bodyHTML), title, sys.ID, string(theme), ab)
}
