package design

import (
	"fmt"
	"sort"
	"strings"
)

// ScreenTemplate 是内置整页屏幕模板。
type ScreenTemplate struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Components  []string `json:"components"` // 用到的组件（提示 Agent 后续如何拆分）
}

// screenTemplates 是全部内置屏幕模板。
var screenTemplates = map[string]ScreenTemplate{
	"login": {ID: "login", Name: "登录页", Description: "居中卡片：品牌块 + 账号/密码 + 主操作 + 辅助链接。",
		Components: []string{"input", "checkbox", "button"}},
	"settings": {ID: "settings", Name: "设置页", Description: "导航栏 + 开关分组 + 列表分组 + 底部标签栏。",
		Components: []string{"navbar", "list", "switch", "tabbar"}},
	"feed": {ID: "feed", Name: "内容流", Description: "导航栏 + 两条媒体卡片（图/文/操作行） + 底部标签栏。",
		Components: []string{"navbar", "card", "tabbar"}},
	"profile": {ID: "profile", Name: "个人主页", Description: "头像头部 + 数据统计 + 列表 + 底部标签栏。",
		Components: []string{"avatar", "list", "tabbar"}},
}

// ScreenTemplates 返回全部模板（按 ID 排序）。
func ScreenTemplates() []ScreenTemplate {
	ids := make([]string, 0, len(screenTemplates))
	for id := range screenTemplates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]ScreenTemplate, 0, len(ids))
	for _, id := range ids {
		out = append(out, screenTemplates[id])
	}
	return out
}

// GetScreenTemplate 查找模板。
func GetScreenTemplate(id string) (ScreenTemplate, bool) {
	t, ok := screenTemplates[SanitizeID(id)]
	return t, ok
}

// ScreenInput 是 ComposeScreen 的入参。
type ScreenInput struct {
	SystemID string
	Template string
	Theme    ThemeKey
	Title    string
	AppName  string // 品牌块/顶栏应用名，默认「我的应用」
}

// ComposeScreen 按模板把组件拼装成一张整页预览（完整 HTML 文档）。
func ComposeScreen(in ScreenInput) (*ComposeResult, error) {
	sys, ok := GetSystem(SanitizeID(in.SystemID))
	if !ok {
		return nil, fmt.Errorf("unknown system %q", in.SystemID)
	}
	tpl, ok := GetScreenTemplate(in.Template)
	if !ok {
		return nil, fmt.Errorf("unknown template %q（可用: login|settings|feed|profile）", in.Template)
	}
	appName := in.AppName
	if appName == "" {
		appName = "我的应用"
	}
	var body strings.Builder
	switch tpl.ID {
	case "login":
		body.WriteString(loginBody(appName))
	case "settings":
		body.WriteString(settingsBody(appName))
	case "feed":
		body.WriteString(feedBody(appName))
	case "profile":
		body.WriteString(profileBody(appName))
	}
	_, effTheme := sys.TokensFor(in.Theme)
	css := StyleSheet(sys, in.Theme)
	title := in.Title
	if title == "" {
		title = sys.Name + " · " + tpl.Name
	}
	width := 420
	if sys.Family == "mobile" {
		width = 390
	}
	doc := buildDoc(css, body.String(), title, sys.ID, string(effTheme))
	height := map[string]int{"login": 560, "settings": 780, "feed": 900, "profile": 780}[tpl.ID]
	return &ComposeResult{
		HTML: doc, SystemID: sys.ID, Theme: effTheme,
		Width: width, Height: height, CSSBytes: len(css),
	}, nil
}

// wrapScreen 把屏幕内容包进 .ds-screen 容器。
func wrapScreen(inner string) string {
	return `<section class="ds-screen">` + inner + `</section>`
}

func loginBody(appName string) string {
	return wrapScreen(`
<section class="ds-screen-login">
  <div class="ds-login-card">
    <div class="ds-login-brand">` + esc(string([]rune(appName)[0])) + `</div>
    <div><div class="ds-login-title">欢迎回来</div><div class="ds-login-sub">登录 ` + esc(appName) + ` 以继续</div></div>
    <div class="ds-field"><label class="ds-label">账号</label><input class="ds-input" placeholder="手机号 / 邮箱" /></div>
    <div class="ds-field"><label class="ds-label">密码</label><input class="ds-input" type="password" placeholder="请输入密码" /></div>
    <div class="ds-row-between"><label class="ds-checkbox ds-checkbox--checked"><span class="ds-checkbox__box">✓</span>记住我</label><span class="ds-login-link">忘记密码？</span></div>
    <button class="ds-btn ds-btn--primary ds-btn--large ds-login-submit">登录</button>
    <div class="ds-login-alt">还没有账号？<span class="ds-login-link">立即注册</span></div>
  </div>
</section>`)
}

func settingsBody(appName string) string {
	return wrapScreen(`
<div class="ds-navbar"><span class="ds-navbar__side ds-navbar__back">‹ 返回</span><span class="ds-navbar__title">设置</span><span class="ds-navbar__side ds-navbar__right"></span></div>
<div class="ds-settings-group">
  <div class="ds-row-between"><div><div class="ds-list__title">消息通知</div><div class="ds-list__desc">接收商品与物流提醒</div></div><span class="ds-switch ds-switch--on"><span class="ds-switch__thumb"></span></span></div>
  <div class="ds-row-between"><div><div class="ds-list__title">省流量模式</div><div class="ds-list__desc">仅 Wi-Fi 下加载图片</div></div><span class="ds-switch"><span class="ds-switch__thumb"></span></span></div>
  <div class="ds-row-between"><div><div class="ds-list__title">深色模式</div><div class="ds-list__desc">跟随系统</div></div><span class="ds-switch ds-switch--on"><span class="ds-switch__thumb"></span></span></div>
</div>
<div class="ds-list">
  <div class="ds-list__item"><div class="ds-list__main"><div class="ds-list__title">账户与安全</div></div><span class="ds-list__value">已保护</span><span class="ds-list__arrow">›</span></div>
  <div class="ds-list__item"><div class="ds-list__main"><div class="ds-list__title">隐私设置</div></div><span class="ds-list__arrow">›</span></div>
  <div class="ds-list__item"><div class="ds-list__main"><div class="ds-list__title">通用</div><div class="ds-list__desc">语言 · 字号 · 清理缓存</div></div><span class="ds-list__arrow">›</span></div>
  <div class="ds-list__item"><div class="ds-list__main"><div class="ds-list__title">关于 ` + esc(appName) + `</div></div><span class="ds-list__value">v1.0.0</span><span class="ds-list__arrow">›</span></div>
</div>
<div class="ds-tabbar">
  <div class="ds-tabbar__item ds-tabbar__item--active"><span class="ds-tabbar__icon">◉</span><span>首页</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">◎</span><span>发现</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">▢</span><span>消息</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">○</span><span>我的</span></div>
</div>`)
}

func feedBody(appName string) string {
	card := func(gradient, title, meta string) string {
		return `
<div class="ds-feed-card">
  <div class="ds-feed-media" style="background:linear-gradient(135deg,` + gradient + `)"></div>
  <div><div class="ds-feed-title">` + esc(title) + `</div><div class="ds-feed-meta">` + esc(meta) + `</div></div>
  <div class="ds-feed-actions"><span>♥ 2.1k</span><span>💬 486</span><span>↗ 分享</span></div>
</div>`
	}
	return wrapScreen(`
<div class="ds-navbar"><span class="ds-navbar__side ds-navbar__back"></span><span class="ds-navbar__title">` + esc(appName) + `</span><span class="ds-navbar__side ds-navbar__right">🔍</span></div>
` + card("var(--ds-color-primary,#409eff),var(--ds-color-info,#909399)", "晨跑五公里：把节奏还给自己", "运动 · 3 分钟前 · 阅读 1.2 万") +
		card("var(--ds-color-success,#67c23a),var(--ds-color-primary,#409eff)", "手冲入门：三段式注水实测", "咖啡 · 12 分钟前 · 阅读 8,642") + `
<div class="ds-tabbar">
  <div class="ds-tabbar__item ds-tabbar__item--active"><span class="ds-tabbar__icon">◉</span><span>首页</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">◎</span><span>发现</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">✚</span><span>发布</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">▢</span><span>消息</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">○</span><span>我的</span></div>
</div>`)
}

func profileBody(appName string) string {
	return wrapScreen(`
<div class="ds-profile-head">
  <span class="ds-avatar ds-avatar--56 ds-avatar--tint">朵</span>
  <div class="ds-profile-info"><div class="ds-profile-name">木小朵</div><div class="ds-profile-sign">` + esc(appName) + ` · 设计爱好者</div></div>
  <button class="ds-btn ds-btn--secondary">编辑</button>
</div>
<div class="ds-stats">
  <div class="ds-stat"><b>128</b><span>作品</span></div>
  <div class="ds-stat"><b>3.6k</b><span>粉丝</span></div>
  <div class="ds-stat"><b>842</b><span>关注</span></div>
  <div class="ds-stat"><b>9.8k</b><span>获赞</span></div>
</div>
<div class="ds-list">
  <div class="ds-list__item"><div class="ds-list__main"><div class="ds-list__title">我的作品</div></div><span class="ds-list__value">128</span><span class="ds-list__arrow">›</span></div>
  <div class="ds-list__item"><div class="ds-list__main"><div class="ds-list__title">收藏夹</div></div><span class="ds-list__value">12</span><span class="ds-list__arrow">›</span></div>
  <div class="ds-list__item"><div class="ds-list__main"><div class="ds-list__title">浏览历史</div></div><span class="ds-list__arrow">›</span></div>
</div>
<div class="ds-tabbar">
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">◉</span><span>首页</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">◎</span><span>发现</span></div>
  <div class="ds-tabbar__item"><span class="ds-tabbar__icon">▢</span><span>消息</span></div>
  <div class="ds-tabbar__item ds-tabbar__item--active"><span class="ds-tabbar__icon">○</span><span>我的</span></div>
</div>`)
}
