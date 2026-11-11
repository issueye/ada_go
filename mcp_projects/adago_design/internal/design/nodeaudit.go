// 节点级设计走查（迭代 2-③）。
//
// Audit(system) 走查的是「体系皮肤」（WCAG/阶梯/字体授权/图标一致），面对的是
// StyleSheet；Agent 真正常问的是「我刚摆的这张稿有什么问题」—— 这需要对
// 已生成节点的 HTML 做内容级检查。两者互补：体系走查管皮肤，节点走查管内容。
//
// 检查项（复用 checks.go / fonts.go / artboard.go 的既有判定）：
//  1. 外链资源 —— srcdoc + CSP 下会被阻断，命中即 fail（画面会留空白）
//  2. 图标与符号 —— emoji/字符图标，与体系走查同一硬规则，fail
//  3. 尺寸契约 —— 是否声明画板、节点宽与画板宽是否一致，偏离 warn
//  4. 字体授权 —— 节点内声明的 font-family 逐栈判定可商用性，非商用 warn
package design

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// NodeAuditInput 是节点级走查的输入（HTML 为节点正文，尺寸取节点当前值）。
type NodeAuditInput struct {
	HTML     string
	Width    int
	Height   int
	Artboard string // 节点声明的画板 ID（空 = 未声明）
}

// NodeAuditReport 是节点级走查结果（语义与 AuditResult 一致：pass/warn/fail）。
type NodeAuditReport struct {
	Score  int
	Passed int
	Warned int
	Failed int
	Checks []AuditCheck
}

// 外链资源引用：<img src> / <link href> / CSS url() 里的 http(s) 地址。
var externalRefRe = regexp.MustCompile(
	`(?i)(?:src|href)\s*=\s*["']https?://[^"']+|url\(\s*['"]?https?://[^)"']+`)
var fontFamilyRe = regexp.MustCompile(`(?i)font-family\s*:\s*([^;}"']*(?:["'][^"']+["'][^;}"]*)*)`)

// AuditNodeHTML 对一份节点 HTML 做内容级走查。
func AuditNodeHTML(in NodeAuditInput) *NodeAuditReport {
	res := &NodeAuditReport{Checks: []AuditCheck{}}
	add := func(status, name, detail string) {
		res.Checks = append(res.Checks, AuditCheck{Status: status, Name: name, Detail: detail})
		switch status {
		case "pass":
			res.Passed++
		case "warn":
			res.Warned++
		case "fail":
			res.Failed++
		}
	}

	// 1 外链资源：CSP 下不会加载，画面留空白且不可交付
	refs := externalRefRe.FindAllString(in.HTML, 8)
	if len(refs) > 0 {
		shown := refs
		if len(shown) > 5 {
			shown = shown[:5]
		}
		add("fail", "外链资源",
			fmt.Sprintf("发现 %d 处 http(s) 资源引用（画布 iframe 的 CSP 会阻断它们，正文将出现空白）: %s",
				len(refs), strings.Join(shown, " · ")))
	} else {
		add("pass", "外链资源", "未发现 http(s) 引用，全部资源内联")
	}

	// 2 图标与符号：emoji/字符图标与体系走查同一硬规则
	if symbols := symbolRe.FindAllString(in.HTML, 8); len(symbols) > 0 {
		add("fail", "图标与符号",
			fmt.Sprintf("发现 %d 处 emoji/字符符号，应改用 design_icons 的 SVG: %s",
				len(symbols), strings.Join(symbols, " ")))
	} else {
		add("pass", "图标与符号", "未发现 emoji 与字符图标")
	}

	// 3 尺寸契约：画板声明 + 宽度一致（两张卡片的尺寸可比性建立在同一画板上）
	if strings.TrimSpace(in.Artboard) == "" {
		add("warn", "尺寸规范", "节点未声明画板（artboard）—— 与其它卡片不可比，建议补 design_artboard")
	} else if ab, ok := GetArtboard(in.Artboard); !ok {
		add("warn", "尺寸规范", "声明的画板 "+in.Artboard+" 不存在，用 design_artboard 查看可用画板")
	} else if in.Width > 0 && in.Width != ab.Width {
		add("warn", "尺寸规范",
			fmt.Sprintf("节点宽 %d ≠ 画板 %s 宽 %d（差 %+d）；内容区应为 %d",
				in.Width, ab.ID, ab.Width, in.Width-ab.Width, ab.Spec().ContentWidth))
	} else {
		add("pass", "尺寸规范", fmt.Sprintf("画板 %s · 宽 %d 一致", in.Artboard, in.Width))
	}

	// 4 字体授权：节点内声明的 font-family 逐栈判定（未声明 = 继承体系默认栈）
	declared := map[string]bool{}
	for _, m := range fontFamilyRe.FindAllStringSubmatch(in.HTML, -1) {
		family := strings.TrimSpace(m[1])
		if family != "" {
			declared[family] = true
		}
	}
	if len(declared) == 0 {
		add("pass", "字体授权", "节点未自带 font-family，继承体系默认栈（已过体系走查）")
	} else {
		names := make([]string, 0, len(declared))
		for f := range declared {
			names = append(names, f)
		}
		sort.Strings(names)
		var bad []string
		for _, f := range names {
			if ok, risky := StackCommercial(f); !ok {
				bad = append(bad, fmt.Sprintf("%s（%s）", f, strings.Join(risky, "/")))
			}
		}
		if len(bad) > 0 {
			add("warn", "字体授权",
				"节点内声明的字族含不可商用/仅系统项，跨平台不可复现: "+strings.Join(bad, " · "))
		} else {
			add("pass", "字体授权", fmt.Sprintf("声明的 %d 个字族均可商用", len(names)))
		}
	}

	res.Score = 100 - res.Failed*25 - res.Warned*10
	if res.Score < 0 {
		res.Score = 0
	}
	return res
}
