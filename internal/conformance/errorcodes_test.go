package conformance

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/diag"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/proto"
)

// retryableDeviations 记录 domain.IsRetryable 与 proto.ErrorCodes.Retryable 的
// **已审阅分歧**，是唯一允许的不一致，语义差别如下：
//
//   - domain.IsRetryable = 「基座可以自动重试」（03 §5.5：只有 429/5xx/网络一类，
//     且 provider 只在首个增量之前；另有 08 §8.1 断线重连、04 §5.7 SQLITE_BUSY 退避）；
//   - proto.ErrorCodes.Retryable = 「Gateway/调用方可以在稍后重发这次 RPC」，
//     包含「此刻不能做、稍后可能可以」的瞬时态。
//
// 于是 4 个码属于「调用方可重试、基座不自动重试」：
//
//	1004 workspace_locked  另一个 Core 持锁：重发无意义，要先解决持有者（04 §5.7）
//	2001 thread_busy       同线程已有运行轮：INV-7，绝不自动排队重放
//	3003 plugin_timeout    插件超时已重建 Runtime：自动重试会放大故障（06 §6.4）
//	3004 plugin_crashed    Runtime 标脏重建：同上，交由用户/策略决定
//
// 这张表是防漂移网的一部分：任何一侧新增/移除 retryable，或把 domain 判成 true 而
// proto 是 false（那是真正的语义矛盾），测试都会失败并打印差异。
// 反向包含关系（domain ⊆ proto）永远是硬约束，不在可豁免集合里。
var retryableDeviations = map[int]string{
	1004: "workspace_locked：调用方可稍后重发，基座不自动重试",
	2001: "thread_busy：INV-7 禁止并发轮次，绝不自动重放",
	3003: "plugin_timeout：Runtime 已重建，自动重试放大故障",
	3004: "plugin_crashed：同上，交由用户/策略决定",
}

// TestErrorCodeAlignment 是「同一份错误码表在三处出现」的防漂移网（08 §8.5）：
//
//	internal/domain/errors.go   —— 领域码枚举 + CodeName + IsRetryable
//	internal/proto/methods_gen.go —— 生成的 ErrorCodes（源是 spec/proto.json）
//	internal/diag/codes.go      —— 诊断码词汇表（ADG-<AREA>-<NNN> + 下一步文案）
//
// 逐条核对：码集合、名称、诊断区域、可重试性、诊断码形状与下一步文案。
// 任一处漂移都会失败并打印差异行（development-plan §7.1 每次合并必跑）。
func TestErrorCodeAlignment(t *testing.T) {
	t.Parallel()

	domCodes := domain.AllCodes()

	// --- 三方的码集合 ---
	domSet := map[int]string{}
	for _, c := range domCodes {
		if _, dup := domSet[int(c)]; dup {
			t.Errorf("domain.AllCodes() 里 %d 重复", int(c))
		}
		domSet[int(c)] = domain.CodeName(c)
	}
	protoSet := map[int]string{}
	protoMeta := map[int]proto.ErrorMeta{}
	for _, c := range proto.ErrorCodes {
		if _, dup := protoSet[c.Code]; dup {
			t.Errorf("proto.ErrorCodes 里 %d 重复", c.Code)
		}
		protoSet[c.Code] = c.Name
		protoMeta[c.Code] = c
	}
	diagSet := map[int]string{}
	diagEntry := map[int]diag.Entry{}
	for _, e := range diag.Table() {
		if _, dup := diagSet[e.AppCode]; dup {
			t.Errorf("diag.Table() 里 %d 重复", e.AppCode)
		}
		diagSet[e.AppCode] = e.Name
		diagEntry[e.AppCode] = e
	}

	t.Run("数量与集合一致", func(t *testing.T) {
		t.Parallel()
		mustCount(t, "domain 错误码数量", 21, len(domCodes))
		mustCount(t, "proto 错误码数量", 21, len(proto.ErrorCodes))
		mustCount(t, "diag 错误码数量", 21, len(diag.Table()))
		if diff := setDiff(domSet, protoSet); diff != "" {
			t.Errorf("domain ↔ proto 码集合/名称差异：\n%s", diff)
		}
		if diff := setDiff(domSet, diagSet); diff != "" {
			t.Errorf("domain ↔ diag 码集合/名称差异：\n%s", diff)
		}
	})

	t.Run("名称逐条一致", func(t *testing.T) {
		t.Parallel()
		var lines []string
		for _, c := range domCodes {
			code := int(c)
			if got := protoSet[code]; got != domain.CodeName(c) {
				lines = append(lines, fmt.Sprintf("  %d: domain=%q proto=%q", code, domain.CodeName(c), got))
			}
			if got := diagSet[code]; got != domain.CodeName(c) {
				lines = append(lines, fmt.Sprintf("  %d: domain=%q diag=%q", code, domain.CodeName(c), got))
			}
		}
		if len(lines) > 0 {
			t.Errorf("错误码名称漂移：\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("诊断区域逐条一致", func(t *testing.T) {
		t.Parallel()
		var lines []string
		for _, c := range domCodes {
			code := int(c)
			pm, ok := protoMeta[code]
			if !ok {
				continue
			}
			de, ok := diagEntry[code]
			if !ok {
				continue
			}
			if !diag.Area(pm.DiagnosticArea).Valid() {
				lines = append(lines, fmt.Sprintf("  %d(%s): proto 的区域 %q 不在 diag.Areas() 里",
					code, pm.Name, pm.DiagnosticArea))
			}
			if string(de.Diag.Area) != pm.DiagnosticArea {
				lines = append(lines, fmt.Sprintf("  %d(%s): proto=%q diag=%q",
					code, pm.Name, pm.DiagnosticArea, de.Diag.Area))
			}
			// domain 的区域枚举必须与 diag 的区域枚举同集合（domain.DiagArea ↔ diag.Area）。
			if !domainAreaValid(domain.DiagArea(pm.DiagnosticArea)) {
				lines = append(lines, fmt.Sprintf("  %d(%s): 区域 %q 在 domain.DiagArea 枚举里没有对应项",
					code, pm.Name, pm.DiagnosticArea))
			}
		}
		if len(lines) > 0 {
			t.Errorf("诊断区域漂移：\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("可重试性逐条一致或已登记分歧", func(t *testing.T) {
		t.Parallel()
		var lines []string
		seenDeviation := map[int]bool{}
		for _, c := range domCodes {
			code := int(c)
			domRet := domain.IsRetryable(c)
			pm, ok := protoMeta[code]
			if !ok {
				continue
			}
			switch {
			case domRet == pm.Retryable:
				continue
			case domRet && !pm.Retryable:
				// 硬矛盾：基座会自动重试，协议却说不可重试。
				lines = append(lines, fmt.Sprintf("  %d(%s): domain.IsRetryable=true 但 proto.Retryable=false（语义矛盾，不可豁免）",
					code, pm.Name))
			case !domRet && pm.Retryable:
				if why, listed := retryableDeviations[code]; !listed {
					lines = append(lines, fmt.Sprintf("  %d(%s): 新的未登记分歧 domain=false proto=true（要么改码表要么登记进 retryableDeviations 并写明语义）",
						code, pm.Name))
				} else {
					seenDeviation[code] = true
					t.Logf("已登记分歧 %d(%s)：%s", code, pm.Name, why)
				}
			}
		}
		for code := range retryableDeviations {
			if !seenDeviation[code] {
				lines = append(lines, fmt.Sprintf("  %d: retryableDeviations 里的条目已不再分歧（请删除该豁免）", code))
			}
		}
		if len(lines) > 0 {
			t.Errorf("retryable 漂移：\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("诊断码形状与下一步文案", func(t *testing.T) {
		t.Parallel()
		for _, e := range diag.Table() {
			s := e.Diag.String()
			back, err := diag.Parse(s)
			mustNoErr(t, fmt.Sprintf("diag.Parse(%s)", s), err)
			if back != e.Diag {
				t.Errorf("诊断码 %s 往返不一致: %+v", s, back)
			}
			if !strings.HasPrefix(s, "ADG-"+string(e.Diag.Area)+"-") {
				t.Errorf("诊断码 %s 与区域 %s 不匹配", s, e.Diag.Area)
			}
			if strings.TrimSpace(e.NextStep) == "" {
				t.Errorf("错误码 %d(%s) 缺少「下一步」文案（08 §8.5：页面只显示码+人话+下一步）", e.AppCode, e.Name)
			}
			if _, ok := proto.LookupError(e.AppCode); !ok {
				t.Errorf("diag 条目 %d 在 proto 里没有对应错误码", e.AppCode)
			}
		}
	})

	t.Run("domain 诊断码渲染与 diag 一致", func(t *testing.T) {
		t.Parallel()
		for _, code := range domCodes {
			entry, ok := diag.For(int(code))
			if !ok {
				t.Fatalf("domain 码 %d 在 diag 里没有条目", int(code))
			}
			e := domain.NewError(code, "契约测试").WithDiag(domain.NewDiagnostic(domain.DiagArea(entry.Diag.Area), entry.Diag.Num))
			want := fmt.Sprintf("ADG-%s-%03d", entry.Diag.Area, entry.Diag.Num)
			mustEqual(t, fmt.Sprintf("domain 码 %d 的诊断码", int(code)), want, e.Diag)
			if !strings.Contains(e.Error(), want) {
				t.Errorf("业务错误渲染里没有诊断码 %s: %q", want, e.Error())
			}
		}
	})

	t.Run("未知码兜底", func(t *testing.T) {
		t.Parallel()
		mustEqual(t, "未知码的名称", "unknown", domain.CodeName(domain.Code(9999)))
		if domain.IsRetryable(domain.Code(9999)) {
			t.Errorf("未知码不得被判为可重试")
		}
		if _, ok := proto.LookupError(9999); ok {
			t.Errorf("proto 不该认识 9999")
		}
		if _, ok := diag.For(9999); ok {
			t.Errorf("diag 不该认识 9999")
		}
	})

	// 把逐条比对结果打进日志，作为 CI 里的人工核对物。
	t.Run("对齐表", func(t *testing.T) {
		t.Parallel()
		codes := make([]int, 0, len(domSet))
		for c := range domSet {
			codes = append(codes, c)
		}
		sort.Ints(codes)
		var b strings.Builder
		b.WriteString("code\tname\tdomain.retry\tproto.retry\tdiag.area\tdiag.code\n")
		for _, c := range codes {
			pm := protoMeta[c]
			de := diagEntry[c]
			fmt.Fprintf(&b, "%d\t%s\t%v\t%v\t%s\t%s\n",
				c, domSet[c], domain.IsRetryable(domain.Code(c)), pm.Retryable, pm.DiagnosticArea, de.Diag.String())
		}
		t.Log("\n" + b.String())
	})

	// testkit 的错误夹具必须落在同一张表里（防替身自造码）。
	t.Run("替身事件夹具只用表内码", func(t *testing.T) {
		t.Parallel()
		evt := testkit.ErrorEvent("th_abc_01J00000000000000000000000",
			domain.CodeLoopDetected, domain.NewDiagnostic(domain.DiagThread, 5))
		if len(evt.Payload) == 0 {
			t.Fatalf("夹具载荷为空")
		}
		if !strings.Contains(string(evt.Payload), "loop_detected") {
			t.Errorf("夹具载荷没有携带表内名称: %s", string(evt.Payload))
		}
	})
}

// setDiff 返回 a 有 b 无 / b 有 a 无 / 名称不匹配的清单。
func setDiff(a, b map[int]string) string {
	var lines []string
	for k, v := range a {
		w, ok := b[k]
		if !ok {
			lines = append(lines, fmt.Sprintf("  只在第一侧: %d %s", k, v))
			continue
		}
		if w != v {
			lines = append(lines, fmt.Sprintf("  名称不一致: %d %s vs %s", k, v, w))
		}
	}
	for k, v := range b {
		if _, ok := a[k]; !ok {
			lines = append(lines, fmt.Sprintf("  只在第二侧: %d %s", k, v))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func domainAreaValid(a domain.DiagArea) bool {
	for _, x := range []domain.DiagArea{
		domain.DiagWS, domain.DiagCore, domain.DiagThread, domain.DiagTool, domain.DiagPlugin,
		domain.DiagAuth, domain.DiagModel, domain.DiagStore, domain.DiagUI, domain.DiagConfig,
	} {
		if x == a {
			return true
		}
	}
	return false
}
