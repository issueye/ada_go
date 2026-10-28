// Package coreapp 是通用装配器：接收「产品声明 + 插件集合」产出目录与引擎配置。
//
// 它不知道任何官方插件的存在（不 import official/，源码里不出现插件 id），
// 官方插件与第三方插件都以同一形态进来（设计 5.1、1.5.5）。
// app/ 组合根是唯一同时知道产品声明与嵌入官方插件包的包（M1/M3 落地）。
package coreapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Product 是产品声明（products/*.agent.json）的加载形态。
//
// 每个字段的消费者见设计 8.6 的消费者表；adag verify 逐字段确认消费者存在。
type Product struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Description  string       `json:"description,omitempty"`
	Plugins      []string     `json:"plugins"`
	Capabilities Capabilities `json:"capabilities"`
	Identity     Identity     `json:"identity"`
	Policies     Policies     `json:"policies"`

	// sourcePath 记录声明文件位置，用于错误信息。
	sourcePath string
}

// Capabilities 是产品能力位（不含人格，人格来自 persona-* 插件）。
type Capabilities struct {
	Skills    bool `json:"skills"`
	Images    bool `json:"images"`
	Subagents bool `json:"subagents"`
}

// Identity 是界面品牌与语言；刻意不含人格字段（8.6）。
type Identity struct {
	Name   string `json:"name"`
	Locale string `json:"locale"`
}

// Policies 是引擎与工具的运行参数；刻意不含审批规则，审批属于策略插件自己的设置（8.6）。
type Policies struct {
	ToolTimeoutMS   int `json:"toolTimeoutMs"`
	TurnTimeoutMS   int `json:"turnTimeoutMs"`
	MaxTurnTokens   int `json:"maxTurnTokens"`
	LoopRepeatLimit int `json:"loopRepeatLimit"`
	LoopAbortLimit  int `json:"loopAbortLimit"`
	ParallelTools   int `json:"parallelTools"`
}

// LoadProduct 读取并校验一份产品声明。
func LoadProduct(path string) (*Product, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("coreapp: 读取产品声明失败 %s: %w", path, err)
	}
	var p Product
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("coreapp: 解析产品声明失败 %s: %w", path, err)
	}
	p.sourcePath = path
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// ParseProduct 从内存原文解析（测试与嵌入声明使用）。
func ParseProduct(raw []byte) (*Product, error) {
	var p Product
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("coreapp: 解析产品声明失败: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// SourcePath 返回声明文件路径（未从文件加载时为空）。
func (p *Product) SourcePath() string { return p.sourcePath }

// Validate 检查声明自洽性与「字段必有消费者」前提下的取值范围（8.6、5.3）。
func (p *Product) Validate() error {
	where := p.sourcePath
	if where == "" {
		where = "inline"
	}
	if !rePluginID.MatchString(p.ID) {
		return fmt.Errorf("coreapp: %s 的产品 id %q 非法", where, p.ID)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("coreapp: %s 缺少 name", where)
	}
	if p.Plugins == nil {
		// 空基座用 plugins: [] 表达，不能缺字段，否则分不清「无插件」与「忘写」。
		return fmt.Errorf("coreapp: %s 缺少 plugins 数组（空基座写 []）", where)
	}
	seen := make(map[string]bool, len(p.Plugins))
	for _, id := range p.Plugins {
		if !rePluginID.MatchString(id) {
			return fmt.Errorf("coreapp: %s 的插件条目 %q 非法", where, id)
		}
		if seen[id] {
			return fmt.Errorf("coreapp: %s 重复启用插件 %q", where, id)
		}
		seen[id] = true
	}
	if strings.TrimSpace(p.Identity.Name) == "" {
		return fmt.Errorf("coreapp: %s 缺少 identity.name（界面品牌消费者）", where)
	}
	if !isLocale(p.Identity.Locale) {
		return fmt.Errorf("coreapp: %s 的 identity.locale %q 不是 BCP47 形状", where, p.Identity.Locale)
	}
	if p.Policies.ToolTimeoutMS <= 0 {
		return fmt.Errorf("coreapp: %s 的 policies.toolTimeoutMs 必须 > 0（默认 120000）", where)
	}
	if p.Policies.TurnTimeoutMS < 0 || p.Policies.MaxTurnTokens < 0 {
		return fmt.Errorf("coreapp: %s 的墙钟/token 上限不能为负；0 表示不限制（5.3）", where)
	}
	if p.Policies.LoopRepeatLimit <= 0 || p.Policies.LoopAbortLimit <= p.Policies.LoopRepeatLimit {
		return fmt.Errorf("coreapp: %s 需要 0 < loopRepeatLimit < loopAbortLimit（先给错误回执，再按 error 结束）", where)
	}
	if p.Policies.ParallelTools < 1 {
		return fmt.Errorf("coreapp: %s 的 parallelTools 至少为 1；默认顺序执行（5.4）", where)
	}
	if p.Capabilities.Subagents {
		return fmt.Errorf("coreapp: %s 启用了子智能体，但 v1 不装配它（保持 v1.5）", where)
	}
	return nil
}

// rePluginID 与插件 id 规则一致（6.2）。
var rePluginID = mustCompile(`^[a-z][a-z0-9-]{1,63}$`)

// Runtime 是一次装配的结果：Core 的只读目录快照（设计 0 摘要第 2 条「贡献在装配期结算」）。
type Runtime struct {
	Product *Product
	// 贡献计数：M0 只验证空基座路径，M1/M3 由真实装配填充。
	ToolCount     int
	ProviderCount int
	PromptCount   int
	ActionCount   int
	RouteCount    int
	SlotCount     int
	PageCount     int
	// PolicyPluginActive 报告当前装配里是否存在 role=tool-policy 插件。
	PolicyPluginActive bool
	// AccessesInUse 列出目录里工具用到的外部效果类别，用于策略门禁。
	AccessesInUse []string
	// Warnings 是装配期记录的非致命问题。
	Warnings []string
}

// Assemble 把产品声明与插件贡献结算成运行期目录（6.5 的两阶段在 M3 落地）。
//
// M0 只保证：空声明 → 空目录，且失败关闭判定正确（没有策略插件时写/执行/网络不可用）。
func Assemble(p *Product) (*Runtime, error) {
	if p == nil {
		return nil, fmt.Errorf("coreapp: Assemble 需要非空产品声明")
	}
	rt := &Runtime{Product: p}
	// M0：没有插件宿主，因此目录必然为空。这条断言本身就是 INV-11 的证据：
	// 基座不补任何工具、provider 或页面。
	if err := rt.Check(); err != nil {
		return nil, err
	}
	return rt, nil
}

// Check 跑装配期门禁（1.5.5、5.4）：
// 存在需要外部效果的工具时，必须至少有一个 role=tool-policy 插件，否则失败关闭。
func (rt *Runtime) Check() error {
	needsPolicy := false
	for _, a := range rt.AccessesInUse {
		if a != "read" {
			needsPolicy = true
			break
		}
	}
	if needsPolicy && !rt.PolicyPluginActive {
		return fmt.Errorf("coreapp: 目录里有 write/exec/network 工具但缺少 role=tool-policy 插件；" +
			"基座对这类工具返回 denied: no policy（5.4）")
	}
	return nil
}

// AllowsImages 实现图片输入的双门（5.5：模型能力位 + 产品能力位，任一说 false 就拒绝）。
func (rt *Runtime) AllowsImages(modelSupportsImages bool) bool {
	return rt.Product.Capabilities.Images && modelSupportsImages
}

// AllowsSkills 报告是否装配技能（8.6 的消费者）。
func (rt *Runtime) AllowsSkills() bool { return rt.Product.Capabilities.Skills }

// EmptyStateNotice 给出空基座应该显示的提示文案（1.5.5 空基座验收、1.4 成功标准）。
func (rt *Runtime) EmptyStateNotice() []string {
	var out []string
	if rt.ProviderCount == 0 {
		out = append(out, "未配置供应商：没有可用的模型适配器插件，无法开始对话。")
	}
	if rt.ToolCount == 0 {
		out = append(out, "无工具：当前装配没有贡献任何工具，模型只能直接回答。")
	}
	if !rt.PolicyPluginActive {
		out = append(out, "无策略插件：写、执行与网络类工具即使出现也会被基座拒绝（失败关闭）。")
	}
	return out
}

// IsEmptyBase 报告这是不是「零插件空基座」形态。
func (rt *Runtime) IsEmptyBase() bool {
	return len(rt.Product.Plugins) == 0 && rt.ToolCount == 0 && rt.ProviderCount == 0
}

// DiscoverProducts 列出 products 目录下的声明文件名（adag verify 与 doctor 用）。
func DiscoverProducts(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("coreapp: 扫描产品目录失败 %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".agent.json") {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out, nil
}
