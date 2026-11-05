package design

// 抖音设计体系 —— 基于抖音公开视觉规范整理（品牌红黑 + 高饱和撞色）。
func init() {
	Register(&System{
		ID:          "douyin",
		Name:        "抖音设计体系",
		Vendor:      "字节跳动 · 抖音",
		Family:      "mobile",
		Description: "抖音移动端视觉：品牌红 #FE2C55 与电光青 #25F4EE 的撞色、沉浸式墨黑 #161823、大圆角与强字重标题。",
		Style: StyleNotes{
			Summary:   "内容即界面：沉浸式黑底、红青撞色点缀、超大字重标题、胶囊大按钮，装饰服务于内容。",
			Keywords:  []string{"抖音红", "电光青", "墨黑", "撞色", "沉浸式"},
			Density:   "舒适",
			Elevation: "弱阴影 + 遮罩分层，沉浸式场景用整层黑色",
		},
		Themes: []string{string(ThemeLight), string(ThemeDark)},
		Light: Tokens{
			Grid: 8,
			Colors: map[string]Color{
				"primary":       {"#FE2C55", "品牌红/主操作"},
				"accent":        {"#25F4EE", "电光青/强调点缀"},
				"ink":           {"#161823", "墨黑（文本/沉浸底）"},
				"bg":            {"#FFFFFF", "页面背景"},
				"surface":       {"#F8F8F8", "浅灰表面"},
				"text-1":        {"#161823", "一级文字"},
				"text-2":        {"#161823BF", "二级文字（75%）"},
				"text-3":        {"#16182380", "三级文字（50%）"},
				"text-inverse":  {"#FFFFFF", "深底上的文字"},
				"border":        {"#1618231F", "描边/分隔"},
				"fill":          {"#F1F1F2", "输入/填充底"},
				"success":       {"#15C85D", "成功"},
				"warning":       {"#FF7D00", "警告"},
				"danger":        {"#FE2C55", "危险（与品牌红同源）"},
				"gradient-live": {"linear-gradient(135deg,#FE2C55,#25F4EE)", "直播/活动渐变"},
			},
			Type: TypeScale{
				Family: `"Douyin Sans", "DouyinSansBold", "PingFang SC", "HarmonyOS Sans SC", "Microsoft YaHei", sans-serif`,
				Levels: map[string]TypeSpec{
					"display": {Size: 32, Weight: 800, LineHeight: 1.19, Usage: "活动大字"},
					"title-1": {Size: 24, Weight: 700, LineHeight: 1.33, Usage: "页面大标题"},
					"title-2": {Size: 20, Weight: 700, LineHeight: 1.4, Usage: "模块标题"},
					"title-3": {Size: 17, Weight: 600, LineHeight: 1.41, Usage: "卡片标题"},
					"body":    {Size: 16, Weight: 400, LineHeight: 1.5, Usage: "正文（移动端偏大）"},
					"body-sm": {Size: 14, Weight: 400, LineHeight: 1.43, Usage: "紧凑正文"},
					"caption": {Size: 13, Weight: 400, LineHeight: 1.38, Usage: "辅助说明"},
					"tab":     {Size: 12, Weight: 500, LineHeight: 1.17, Usage: "底部标签"},
				},
			},
			Spacing: map[string]int{
				"xxs": 4, "xs": 8, "sm": 12, "md": 16, "lg": 20, "xl": 24, "xxl": 32,
			},
			Radius: map[string]int{
				"small": 8, "medium": 12, "large": 16, "xlarge": 24, "pill": 100,
			},
			Shadows: map[string]string{
				"card":     "0 2px 12px rgba(22,24,35,0.08)",
				"floating": "0 8px 28px rgba(22,24,35,0.16)",
				"none":     "none",
			},
			Motion: map[string]string{
				"standard": "300ms cubic-bezier(0.22,0.61,0.36,1)",
				"fast":     "180ms cubic-bezier(0.22,0.61,0.36,1)",
				"spring":   "420ms cubic-bezier(0.34,1.56,0.64,1)",
			},
		},
		Dark: &Tokens{
			Grid: 8,
			Colors: map[string]Color{
				"primary":       {"#FE2C55", "品牌红/主操作"},
				"accent":        {"#25F4EE", "电光青/强调点缀"},
				"ink":           {"#161823", "墨黑"},
				"bg":            {"#121212", "页面背景（沉浸黑）"},
				"surface":       {"#1C1C1E", "深灰表面"},
				"text-1":        {"#FFFFFF", "一级文字"},
				"text-2":        {"#FFFFFFBF", "二级文字"},
				"text-3":        {"#FFFFFF80", "三级文字"},
				"text-inverse":  {"#161823", "浅底上的文字"},
				"border":        {"#FFFFFF1F", "描边/分隔"},
				"fill":          {"#2A2A2E", "输入/填充底"},
				"success":       {"#15C85D", "成功"},
				"warning":       {"#FF9F0A", "警告"},
				"danger":        {"#FE2C55", "危险"},
				"gradient-live": {"linear-gradient(135deg,#FE2C55,#25F4EE)", "直播/活动渐变"},
			},
			Type: TypeScale{
				Family: `"Douyin Sans", "DouyinSansBold", "PingFang SC", "HarmonyOS Sans SC", "Microsoft YaHei", sans-serif`,
				Levels: map[string]TypeSpec{
					"display": {Size: 32, Weight: 800, LineHeight: 1.19, Usage: "活动大字"},
					"title-1": {Size: 24, Weight: 700, LineHeight: 1.33, Usage: "页面大标题"},
					"title-2": {Size: 20, Weight: 700, LineHeight: 1.4, Usage: "模块标题"},
					"title-3": {Size: 17, Weight: 600, LineHeight: 1.41, Usage: "卡片标题"},
					"body":    {Size: 16, Weight: 400, LineHeight: 1.5, Usage: "正文（移动端偏大）"},
					"body-sm": {Size: 14, Weight: 400, LineHeight: 1.43, Usage: "紧凑正文"},
					"caption": {Size: 13, Weight: 400, LineHeight: 1.38, Usage: "辅助说明"},
					"tab":     {Size: 12, Weight: 500, LineHeight: 1.17, Usage: "底部标签"},
				},
			},
			Spacing: map[string]int{
				"xxs": 4, "xs": 8, "sm": 12, "md": 16, "lg": 20, "xl": 24, "xxl": 32,
			},
			Radius: map[string]int{
				"small": 8, "medium": 12, "large": 16, "xlarge": 24, "pill": 100,
			},
			Shadows: map[string]string{
				"card":     "0 2px 12px rgba(0,0,0,0.4)",
				"floating": "0 8px 28px rgba(0,0,0,0.55)",
				"none":     "none",
			},
			Motion: map[string]string{
				"standard": "300ms cubic-bezier(0.22,0.61,0.36,1)",
				"fast":     "180ms cubic-bezier(0.22,0.61,0.36,1)",
				"spring":   "420ms cubic-bezier(0.34,1.56,0.64,1)",
			},
		},
		Guideline: []string{
			"品牌红只给关键动作与品牌元素，大面积使用会稀释点击欲。",
			"红青同框仅用于活动/直播等高能场景，常规界面只出现其一。",
			"沉浸式内容区用墨黑 #161823 整层铺底，白色文字 + 75%/50% 透明度分层。",
			"按钮大而圆：主操作用 pill 胶囊 + 600/700 字重，禁用细描边小按钮。",
			"标题靠字重（700/800）建立层级，正文保持 16px 保证移动端可读性。",
			"图标/插画可带 8% 透明度的红青双色错位描边（品牌故障感）。",
		},
	})
}
