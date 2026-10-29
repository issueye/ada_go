// Package verify 实现 adag verify 的开发门禁（设计 10 verify、开发计划 §7.1、M0 退出标准）。
//
// 每一条检查都必须能在桩代码上跑起来，并且「失败即阻断」：协议漂移、未接线的钩子点位、
// 没有消费者的能力位与插槽、生成物不同步、基座边界越界，都在这里拦。
//
// 命令形态：
//
//	adag verify              # 全部检查
//	adag verify dispatch     # 单跑一项
//	adag verify boundary
//	adag verify nodecompat
//	adag verify ui
package verify

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Check 是一项检查。
type Check struct {
	// Name 是命令行可选的名字。
	Name string
	// Summary 说明它拦什么。
	Summary string
	// Milestone 说明该检查从哪个里程碑开始生效。
	Milestone string
	// Run 返回失败原因；nil 表示通过。
	Run func(repoRoot string) error
	// Skippable 表示在依赖资产尚未导入时可以明确跳过（不伪装成通过）。
	Skippable func(repoRoot string) (skip bool, reason string)
}

// Result 是一项检查的结果。
type Result struct {
	Check  Check
	Status Status
	Err    error
}

// Status 是检查结论。
type Status string

const (
	Pass    Status = "PASS"
	Fail    Status = "FAIL"
	Skipped Status = "SKIP"
)

// Registry 返回全部检查，按名字排序，保证输出稳定。
func Registry() []Check {
	out := []Check{
		{Name: "generated", Summary: "spec/proto.json 与生成物同步；协议常量、错误码与 diag 词汇表一致", Milestone: "M0", Run: checkGenerated},
		{Name: "dispatch", Summary: "Core dispatch 覆盖 35 个协议方法，且没有协议外方法", Milestone: "M0", Run: checkDispatch},
		{Name: "hooks", Summary: "15 个钩子点位都有引擎调用方，成对点位齐备", Milestone: "M0", Run: checkHooks},
		{Name: "slots", Summary: "12 个插槽都有宿主位置与空贡献回退，基座所有权一致", Milestone: "M0", Run: checkSlots},
		{Name: "capabilities", Summary: "每个能力位都有安装消费者与信任约束", Milestone: "M0", Run: checkCapabilities},
		{Name: "manifest", Summary: "清单 Schema 字段与 Go 加载结构逐字一致，且每字段有消费者", Milestone: "M0", Run: checkManifestFields},
		{Name: "vnode", Summary: "VNode Schema 的白名单与限制与 Go 校验器一致", Milestone: "M0", Run: checkVNode},
		{Name: "products", Summary: "产品声明逐字段有消费者；skeleton 空基座可启动", Milestone: "M0", Run: checkProducts},
		{Name: "docs", Summary: "文档链接可解析；核心数字（35/15/12/1）与生成结果一致", Milestone: "M0", Run: checkDocs},
		{Name: "nodecompat", Summary: "Node 模块矩阵、能力位、错误码与偏差表自洽", Milestone: "M0", Run: checkNodeCompat},
		{Name: "ui", Summary: "a_da token/CSS/DOM 基线一致；无硬编码色与奇数尺寸", Milestone: "M2", Run: checkUI, Skippable: uiBaselinePending},
		{Name: "boundary", Summary: "基座边界：不 import official、源码无官方插件 id、无平行 Go 注册 API", Milestone: "M0", Run: checkBoundary},
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names 返回全部检查名（供 CLI 提示与 CI 矩阵）。
func Names() []string {
	out := make([]string, 0, len(Registry()))
	for _, c := range Registry() {
		out = append(out, c.Name)
	}
	return out
}

// Run 执行选中的检查。pattern 为空表示全部。
func Run(w io.Writer, repoRoot string, patterns []string) ([]Result, error) {
	checks := selectChecks(patterns)
	if len(checks) == 0 {
		return nil, fmt.Errorf("verify: 没有匹配的检查（可用：%s）", strings.Join(Names(), ", "))
	}
	results := make([]Result, 0, len(checks))
	for _, c := range checks {
		res := Result{Check: c}
		if c.Skippable != nil {
			if skip, reason := c.Skippable(repoRoot); skip {
				res.Status = Skipped
				results = append(results, res)
				fmt.Fprintf(w, "SKIP  %-13s %s（%s）\n", c.Name, c.Summary, reason)
				continue
			}
		}
		if err := c.Run(repoRoot); err != nil {
			res.Status = Fail
			res.Err = err
			fmt.Fprintf(w, "FAIL  %-13s %s\n        %v\n", c.Name, c.Summary, err)
		} else {
			res.Status = Pass
			fmt.Fprintf(w, "PASS  %-13s %s\n", c.Name, c.Summary)
		}
		results = append(results, res)
	}
	return results, nil
}

// selectChecks 处理 all/空/具体名字。
func selectChecks(patterns []string) []Check {
	all := Registry()
	if len(patterns) == 0 {
		return all
	}
	want := map[string]bool{}
	for _, p := range patterns {
		if p == "all" {
			return all
		}
		want[p] = true
	}
	var out []Check
	for _, c := range all {
		if want[c.Name] {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Summarize 汇总结果；有失败时返回聚合错误，供 CLI 设退出码。
func Summarize(w io.Writer, results []Result) error {
	var failed, skipped []string
	for _, r := range results {
		switch r.Status {
		case Fail:
			failed = append(failed, r.Check.Name)
		case Skipped:
			skipped = append(skipped, r.Check.Name)
		}
	}
	fmt.Fprintf(w, "\n合计 %d 项：通过 %d，失败 %d，跳过 %d\n",
		len(results), len(results)-len(failed)-len(skipped), len(failed), len(skipped))
	if len(skipped) > 0 {
		fmt.Fprintf(w, "跳过（尚未到生效里程碑）：%s\n", strings.Join(skipped, ", "))
	}
	if len(failed) > 0 {
		return fmt.Errorf("verify 失败：%s", strings.Join(failed, ", "))
	}
	return nil
}
