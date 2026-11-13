// Package design 内置多套设计体系的 Token、组件与预览生成。
//
// 每个 System 由一份 Token（颜色/字型/间距/圆角/阴影/动效）与共享的组件
// HTML 结构组成；组件渲染时按体系注入对应 StyleSheet，实现「一份结构、
// 多份皮肤」。Token 值基于各体系公开设计规范整理，为近似实现。
package design

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Color 是一个语义色 Token。
type Color struct {
	Value string `json:"value"` // 十六进制或 rgba() 字符串
	Usage string `json:"usage,omitempty"`
}

// TypeSpec 描述字型阶梯的一档。
type TypeSpec struct {
	Size          int     `json:"size"`                    // px
	Weight        int     `json:"weight"`                  // 100-900
	LineHeight    float64 `json:"lineHeight"`              // 倍数
	Usage         string  `json:"usage,omitempty"`         // 用途说明
	LetterSpacing float64 `json:"letterSpacing,omitempty"` // px，可为负
}

// TypeScale 是一套字型体系。
type TypeScale struct {
	Family string              `json:"family"` // CSS font-family
	Levels map[string]TypeSpec `json:"levels"` // display/title/body/caption...
}

// Tokens 是一套主题下的全部设计 Token。
type Tokens struct {
	Colors  map[string]Color  `json:"colors"`
	Type    TypeScale         `json:"type"`
	Spacing map[string]int    `json:"spacing"` // 语义 -> px
	Radius  map[string]int    `json:"radius"`  // 语义 -> px
	Shadows map[string]string `json:"shadows"` // 语义 -> CSS box-shadow
	Motion  map[string]string `json:"motion"`  // 语义 -> CSS transition 值
	Grid    int               `json:"grid"`    // 栅格基数 px（4/8）
}

// StyleNotes 是给人/Agent 读的风格要点。
type StyleNotes struct {
	Summary   string   `json:"summary"`             // 一句话风格
	Keywords  []string `json:"keywords,omitempty"`  // 风格关键词
	Density   string   `json:"density,omitempty"`   // 信息密度倾向，如 "舒适"
	Elevation string   `json:"elevation,omitempty"` // 阴影/层级策略
}

// System 是一个完整设计体系。
type System struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Vendor      string     `json:"vendor"`
	Family      string     `json:"family"` // mobile | web | cross
	Description string     `json:"description"`
	Style       StyleNotes `json:"style"`
	Themes      []string   `json:"themes"` // light, dark
	Light       Tokens     `json:"light"`
	Dark        *Tokens    `json:"dark,omitempty"`
	Guideline   []string   `json:"guideline,omitempty"`
}

// Component 描述一个可渲染组件。
type Component struct {
	ID          string   `json:"id"`       // 如 "button"
	Name        string   `json:"name"`     // 如 "按钮"
	Category    string   `json:"category"` // general|form|feedback|navigation|data
	Description string   `json:"description"`
	Variants    []string `json:"variants,omitempty"` // 可演示的变体
	Usage       []string `json:"usage,omitempty"`    // 用法守则
}

// ThemeKey 主题标识。
type ThemeKey string

// 支持的主题。
const (
	ThemeLight ThemeKey = "light"
	ThemeDark  ThemeKey = "dark"
)

// registry 是全部已注册体系的集合。
//
// 内置体系在 init 注册，自定义体系可在运行期通过 design_derive /
// design_register 增删；MCP 工具与画布 HTTP 目录接口会并发读它，所以
// 所有访问都必须经 registryMu（读 RLock、写 Lock）。
var (
	registryMu sync.RWMutex
	registry   = map[string]*System{}
)

// Register 注册一个设计体系（init 中调用）。
func Register(s *System) {
	if s.ID == "" {
		panic("design: system id is empty")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[s.ID]; dup {
		panic(fmt.Sprintf("design: duplicate system id %q", s.ID))
	}
	registry[s.ID] = s
}

// Systems 返回全部体系（按 ID 排序）。
func Systems() []*System {
	registryMu.RLock()
	out := make([]*System, 0, len(registry))
	for _, s := range registry {
		out = append(out, s)
	}
	registryMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GetSystem 按 ID 查找体系。
func GetSystem(id string) (*System, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	s, ok := registry[id]
	return s, ok
}

// unregisterSystem 从注册表移除一个体系（删除路径与测试清理共用）。
// 是否允许删除（如内置体系保护）由调用方校验。
func unregisterSystem(id string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, id)
}

// Components 返回某体系的组件清单（按 ID 排序）。目录是全局共享的，
// sys 参数仅为将来按体系裁剪目录预留。
func Components(_ *System) []Component {
	ids := make([]string, 0, len(componentCatalog))
	for id := range componentCatalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Component, 0, len(ids))
	for _, id := range ids {
		out = append(out, componentCatalog[id])
	}
	return out
}

// GetComponent 查找组件定义。
func GetComponent(id string) (Component, bool) {
	c, ok := componentCatalog[id]
	return c, ok
}

// TokensFor 返回指定主题的 Token（dark 不存在时回退 light）。
func (s *System) TokensFor(theme ThemeKey) (Tokens, ThemeKey) {
	if theme == ThemeDark && s.Dark != nil {
		return *s.Dark, ThemeDark
	}
	return s.Light, ThemeLight
}

// HasTheme 判断体系是否声明了某主题。
func (s *System) HasTheme(theme ThemeKey) bool {
	if theme == ThemeLight {
		return true
	}
	return s.Dark != nil
}

// ColorValue 取语义色，缺省回退：dark 主题缺 -> light -> text-primary -> #333。
func (t Tokens) ColorValue(name string) string {
	if c, ok := t.Colors[name]; ok {
		return c.Value
	}
	if c, ok := t.Colors["text-primary"]; ok {
		return c.Value
	}
	return "#333333"
}

// SanitizeID 归一化体系/组件 ID（去空白、转小写）。
func SanitizeID(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
