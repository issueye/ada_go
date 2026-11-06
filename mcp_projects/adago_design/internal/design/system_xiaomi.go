package design

// 小米 MIUI / HyperOS —— 基于小米公开视觉规范整理（MiSans、大圆角、暖白轻阴影）。
func init() {
	base := TypeScale{
		Family: `"MiSans", "HarmonyOS Sans SC", Inter, "Noto Sans SC", system-ui, "PingFang SC", sans-serif`,
		Levels: map[string]TypeSpec{
			"display": {Size: 36, Weight: 700, LineHeight: 1.17, Usage: "数字/大标题"},
			"title-1": {Size: 26, Weight: 600, LineHeight: 1.31, Usage: "页面标题"},
			"title-2": {Size: 21, Weight: 600, LineHeight: 1.38, Usage: "模块标题"},
			"title-3": {Size: 17, Weight: 600, LineHeight: 1.41, Usage: "卡片标题"},
			"body":    {Size: 16, Weight: 400, LineHeight: 1.5, Usage: "正文"},
			"body-sm": {Size: 14, Weight: 400, LineHeight: 1.43, Usage: "紧凑正文"},
			"caption": {Size: 12, Weight: 400, LineHeight: 1.33, Usage: "辅助说明"},
		},
	}
	light := Tokens{
		Grid: 8,
		Colors: map[string]Color{
			"primary":        {"#FF6900", "小米橙/主操作"},
			"primary-hover":  {"#FF8226", "主色悬浮"},
			"primary-active": {"#E05E00", "主色按下"},
			"primary-bg":     {"#FFF1E6", "主色浅底"},
			"accent-blue":    {"#3482FF", "信息/链接蓝（HyperOS）"},
			"success":        {"#23B34A", "成功"},
			"success-bg":     {"#E8F8EC", "成功浅底"},
			"warning":        {"#FF9F0A", "警告"},
			"warning-bg":     {"#FFF6E6", "警告浅底"},
			"danger":         {"#F53F3F", "危险"},
			"danger-bg":      {"#FFECE8", "危险浅底"},
			"text-1":         {"#111111", "一级文字"},
			"text-2":         {"#666666", "二级文字"},
			"text-3":         {"#999999", "三级文字/占位"},
			"text-disabled":  {"#CCCCCC", "禁用"},
			"border":         {"#EEEEEE", "描边/分隔"},
			"fill":           {"#F5F5F5", "填充底"},
			"bg":             {"#F7F7F7", "页面背景（暖白）"},
			"surface":        {"#FFFFFF", "卡片表面"},
		},
		Type:    base,
		Spacing: map[string]int{"xxs": 4, "xs": 8, "sm": 12, "md": 16, "lg": 20, "xl": 24, "xxl": 32},
		Radius: map[string]int{
			"small": 10, "medium": 14, "large": 18, "xlarge": 24, "pill": 100,
		},
		Shadows: map[string]string{
			"card":     "0 4px 20px rgba(0,0,0,0.06)",
			"floating": "0 10px 30px rgba(0,0,0,0.12)",
			"none":     "none",
		},
		Motion: map[string]string{
			"standard": "250ms cubic-bezier(0.4,0,0.2,1)",
			"fast":     "150ms cubic-bezier(0.4,0,0.2,1)",
			"spring":   "400ms cubic-bezier(0.34,1.4,0.64,1)",
		},
	}
	Register(&System{
		ID:          "xiaomi",
		Name:        "小米 MIUI / HyperOS",
		Vendor:      "小米",
		Family:      "mobile",
		Description: "小米移动端设计语言：MiSans 字族、小米橙主色、24px 级大圆角、暖白背景 + 极轻阴影的「轻」质感。",
		Style: StyleNotes{
			Summary:   "轻、快、圆润：暖白底大圆角卡片，轻阴影分层，橙色只做点睛，蓝绿灰做功能色。",
			Keywords:  []string{"MiSans", "小米橙", "大圆角", "轻阴影", "暖白"},
			Density:   "舒适",
			Elevation: "轻阴影（6% 黑）为主，配合浅灰描边",
		},
		Themes: []string{string(ThemeLight), string(ThemeDark)},
		Light:  light,
		Dark: &Tokens{
			Grid: 8,
			Colors: map[string]Color{
				"primary":        {"#FF8226", "小米橙（暗色提亮）"},
				"primary-hover":  {"#FF9C52", "主色悬浮"},
				"primary-active": {"#E05E00", "主色按下"},
				"primary-bg":     {"#3A2413", "主色浅底"},
				"accent-blue":    {"#5C9DFF", "信息/链接蓝"},
				"success":        {"#3DCB63", "成功"},
				"success-bg":     {"#122B18", "成功浅底"},
				"warning":        {"#FFB84D", "警告"},
				"warning-bg":     {"#3A2A0F", "警告浅底"},
				"danger":         {"#FF6B6B", "危险"},
				"danger-bg":      {"#3A1515", "危险浅底"},
				"text-1":         {"#F2F2F2", "一级文字"},
				"text-2":         {"#B3B3B3", "二级文字"},
				"text-3":         {"#808080", "三级文字/占位"},
				"text-disabled":  {"#4D4D4D", "禁用"},
				"border":         {"#2A2A2A", "描边/分隔"},
				"fill":           {"#1E1E1E", "填充底"},
				"bg":             {"#111111", "页面背景"},
				"surface":        {"#1C1C1C", "卡片表面"},
			},
			Type:    base,
			Spacing: map[string]int{"xxs": 4, "xs": 8, "sm": 12, "md": 16, "lg": 20, "xl": 24, "xxl": 32},
			Radius: map[string]int{
				"small": 10, "medium": 14, "large": 18, "xlarge": 24, "pill": 100,
			},
			Shadows: map[string]string{
				"card":     "0 4px 20px rgba(0,0,0,0.4)",
				"floating": "0 10px 30px rgba(0,0,0,0.55)",
				"none":     "none",
			},
			Motion: map[string]string{
				"standard": "250ms cubic-bezier(0.4,0,0.2,1)",
				"fast":     "150ms cubic-bezier(0.4,0,0.2,1)",
				"spring":   "400ms cubic-bezier(0.34,1.4,0.64,1)",
			},
		},
		Guideline: []string{
			"MiSans 是体系身份的一部分，Web 预览退级到 PingFang/雅黑需保持字重节奏（600 标题 / 400 正文）。",
			"卡片圆角 18-24：越大的表面用越大的圆角；小控件（开关、标签）用 10-14。",
			"小米橙是点睛色：主按钮、关键数据、进度；不要给整个界面刷橙。",
			"信息/链接用 accent-blue，与品牌橙严格分工。",
			"阴影克制（6% 黑），主要靠 surface 与 bg 的明度差分层。",
			"移动端正文 16px，紧凑场景 14px，最小 12px。",
		},
	})
}
