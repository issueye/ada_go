package conformance

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/domain"
)

// codingPlugins 是 08 §8.6 与 development-plan §1.1 冻结的官方插件清单
// （13 项：9 个后端 + 4 个前端）。
// 只出现在测试代码里：基座源码不认得它们（1.5.5 的硬边界），选择权在产品声明。
var codingPlugins = []string{
	"provider-openai-chat",
	"tools-fs",
	"tools-shell",
	"tools-git",
	"tools-plan",
	"tools-ask",
	"tools-skill",
	"policy-default",
	"persona-coding",
	"ui-todo",
	"ui-ask-user",
	"provider-admin",
	"token-usage",
}

// TestProductDeclarations 断言产品声明这一侧的契约（08 §8.6、M0.3/M0.4 交付物）：
//
//   - products/coding.agent.json 用 coreapp.LoadProduct 加载通过，13 项插件、
//     能力位与 policies 逐字段对表；
//   - products/skeleton.agent.json 的 plugins 是空数组（不是缺失字段），
//     coreapp.Assemble 得到空基座且 EmptyStateNotice() 非空 ——
//     这是 M0 退出标准「空基座桩可启动」的测试化表达（INV-11）；
//   - 声明里的负面取值（缺 plugins、重复启用、子智能体、非法 locale）必须被拒。
func TestProductDeclarations(t *testing.T) {
	t.Parallel()

	t.Run("coding 产品声明", func(t *testing.T) {
		t.Parallel()
		p, err := coreapp.LoadProduct(repoPath(t, "products", "coding.agent.json"))
		mustNoErr(t, "LoadProduct(coding)", err)
		mustEqual(t, "产品 id", "coding", p.ID)
		mustEqual(t, "产品名", "ada-go Coding", p.Name)
		mustCount(t, "coding 的官方插件数", 13, len(p.Plugins))
		for i, want := range codingPlugins {
			if p.Plugins[i] != want {
				t.Errorf("第 %d 项插件: want=%s got=%s", i+1, want, p.Plugins[i])
			}
		}
		if !p.Capabilities.Skills || !p.Capabilities.Images || p.Capabilities.Subagents {
			t.Errorf("能力位漂移: %+v（skills=true images=true subagents=false）", p.Capabilities)
		}
		mustEqual(t, "identity.name", "ada-go", p.Identity.Name)
		mustEqual(t, "identity.locale", "zh-CN", p.Identity.Locale)

		// policies 与 domain 的契约默认值必须咬合（08 §8.6 的消费者是引擎与工具包装）。
		mustCount(t, "toolTimeoutMs", 120000, p.Policies.ToolTimeoutMS)
		if d := time.Duration(p.Policies.ToolTimeoutMS) * time.Millisecond; d != domain.DefaultToolTimeout {
			t.Errorf("policies.toolTimeoutMs=%dms 与 domain.DefaultToolTimeout=%s 不一致",
				p.Policies.ToolTimeoutMS, d)
		}
		mustCount(t, "turnTimeoutMs（0=不限制）", 0, p.Policies.TurnTimeoutMS)
		mustCount(t, "maxTurnTokens（0=不限制）", 0, p.Policies.MaxTurnTokens)
		mustCount(t, "loopRepeatLimit", domain.LoopRepeatLimit, p.Policies.LoopRepeatLimit)
		mustCount(t, "loopAbortLimit", domain.LoopAbortLimit, p.Policies.LoopAbortLimit)
		mustCount(t, "parallelTools", 1, p.Policies.ParallelTools)
	})

	t.Run("skeleton 产品声明与空基座", func(t *testing.T) {
		t.Parallel()
		p, err := coreapp.LoadProduct(repoPath(t, "products", "skeleton.agent.json"))
		mustNoErr(t, "LoadProduct(skeleton)", err)
		if p.Plugins == nil {
			t.Fatalf("skeleton 的 plugins 必须是空数组而不是 nil（coreapp 用这个区分「无插件」与「忘写」）")
		}
		mustCount(t, "skeleton 的插件数", 0, len(p.Plugins))

		rt, err := coreapp.Assemble(p)
		mustNoErr(t, "Assemble(skeleton)", err)
		mustTrue(t, "空基座判定", rt.IsEmptyBase())
		mustCount(t, "空基座工具数", 0, rt.ToolCount)
		mustCount(t, "空基座 provider 数", 0, rt.ProviderCount)
		mustCount(t, "空基座页面数", 0, rt.PageCount)
		mustCount(t, "空基座插槽数", 0, rt.SlotCount)
		if rt.PolicyPluginActive {
			t.Errorf("空基座不该有策略插件")
		}
		notice := rt.EmptyStateNotice()
		if len(notice) == 0 {
			t.Fatalf("空基座必须给出空态提示（10 §11.3「未配置供应商/无工具」）")
		}
		joined := strings.Join(notice, "|")
		for _, want := range []string{"未配置供应商", "无工具", "无策略插件"} {
			if !strings.Contains(joined, want) {
				t.Errorf("空态提示缺少 %q: %v", want, notice)
			}
		}
		// INV-11 的目录侧证据：空基座的目录长度为 0，基座不补位。
		cat := testkit.NewEmptyCatalog()
		if got := len(cat.List(t.Context())); got != 0 {
			t.Errorf("空目录长度 %d", got)
		}
		if _, ok := cat.Lookup(t.Context(), "read_file"); ok {
			t.Errorf("空基座不该查到 read_file")
		}
		t.Logf("空基座空态提示: %v", notice)
	})

	t.Run("coding 在 M0 也只能装出空目录", func(t *testing.T) {
		t.Parallel()
		// M0 没有插件宿主，Assemble 的贡献计数必然为 0；这条断言的作用是
		// 「基座不注册任何工具」——M1 之后仍然必须为 0 除非插件真的贡献了。
		p, err := coreapp.LoadProduct(repoPath(t, "products", "coding.agent.json"))
		mustNoErr(t, "LoadProduct(coding)", err)
		rt, err := coreapp.Assemble(p)
		mustNoErr(t, "Assemble(coding)", err)
		mustCount(t, "M0 无宿主时的工具数", 0, rt.ToolCount)
		mustTrue(t, "coding 允许技能", rt.AllowsSkills())
		mustTrue(t, "图片双门：模型支持时放行", rt.AllowsImages(true))
		if rt.AllowsImages(false) {
			t.Errorf("模型能力位说 false 就必须拒绝，不能悄悄丢图（03 §5.5）")
		}
	})

	t.Run("策略门禁：无策略插件即失败关闭", func(t *testing.T) {
		t.Parallel()
		p, err := coreapp.LoadProduct(repoPath(t, "products", "coding.agent.json"))
		mustNoErr(t, "LoadProduct", err)

		bare := &coreapp.Runtime{Product: p, AccessesInUse: []string{"read", "write", "exec"}}
		err = bare.Check()
		mustErrContains(t, "缺策略插件", err, "role=tool-policy")
		mustErrContains(t, "缺策略插件的理由", err, "denied: no policy")

		withPolicy := &coreapp.Runtime{Product: p, AccessesInUse: []string{"read", "write"}, PolicyPluginActive: true}
		mustNoErr(t, "有策略插件", withPolicy.Check())

		readonly := &coreapp.Runtime{Product: p, AccessesInUse: []string{"read"}}
		mustNoErr(t, "只读工具不需要策略插件", readonly.Check())

		// 替身目录侧的同一门禁：禁用策略插件后 write 工具仍在目录里，
		// 但基座只能拒绝它（INV-11 + 02 §5.4）。
		cat := testkit.NewStaticCatalog(
			testkit.Descriptor("read_file", "some-fs", domain.AccessRead),
			testkit.Descriptor("write_file", "some-fs", domain.AccessWrite),
		)
		if len(cat.List(t.Context())) != 2 {
			t.Fatalf("静态目录应有 2 项")
		}
		mustCount(t, "按插件 id 禁用后移除的条数", 2, cat.Disable("some-fs"))
		if len(cat.List(t.Context())) != 0 {
			t.Errorf("禁用贡献插件后目录必须清空（基座不补位）")
		}
	})

	t.Run("产品声明负面用例", func(t *testing.T) {
		t.Parallel()
		cases := []struct{ name, raw, want string }{
			{"缺 plugins 字段", `{"id":"probe","name":"X","capabilities":{"skills":false,"images":false,"subagents":false},
			  "identity":{"name":"ada-go","locale":"zh-CN"},
			  "policies":{"toolTimeoutMs":120000,"turnTimeoutMs":0,"maxTurnTokens":0,"loopRepeatLimit":3,"loopAbortLimit":5,"parallelTools":1}}`,
				"缺少 plugins 数组"},
			{"重复启用", `{"id":"probe","name":"X","plugins":["dup-plugin","dup-plugin"],"capabilities":{},
			  "identity":{"name":"ada-go","locale":"zh-CN"},
			  "policies":{"toolTimeoutMs":120000,"loopRepeatLimit":3,"loopAbortLimit":5,"parallelTools":1}}`,
				"重复启用插件"},
			{"启用子智能体", `{"id":"probe","name":"X","plugins":[],"capabilities":{"subagents":true},
			  "identity":{"name":"ada-go","locale":"zh-CN"},
			  "policies":{"toolTimeoutMs":120000,"loopRepeatLimit":3,"loopAbortLimit":5,"parallelTools":1}}`,
				"v1 不装配"},
			{"locale 非法", `{"id":"probe","name":"X","plugins":[],"capabilities":{},
			  "identity":{"name":"ada-go","locale":"中文"},
			  "policies":{"toolTimeoutMs":120000,"loopRepeatLimit":3,"loopAbortLimit":5,"parallelTools":1}}`,
				"BCP47"},
			{"循环阈值倒挂", `{"id":"probe","name":"X","plugins":[],"capabilities":{},
			  "identity":{"name":"ada-go","locale":"zh-CN"},
			  "policies":{"toolTimeoutMs":120000,"loopRepeatLimit":5,"loopAbortLimit":3,"parallelTools":1}}`,
				"loopRepeatLimit"},
			{"工具超时非正", `{"id":"probe","name":"X","plugins":[],"capabilities":{},
			  "identity":{"name":"ada-go","locale":"zh-CN"},
			  "policies":{"toolTimeoutMs":0,"loopRepeatLimit":3,"loopAbortLimit":5,"parallelTools":1}}`,
				"toolTimeoutMs"},
		}
		for _, c := range cases {
			c := c
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				_, err := coreapp.ParseProduct([]byte(c.raw))
				mustErrContains(t, c.name, err, c.want)
			})
		}
	})

	t.Run("产品目录可被发现", func(t *testing.T) {
		t.Parallel()
		found, err := coreapp.DiscoverProducts(repoPath(t, "products"))
		mustNoErr(t, "DiscoverProducts", err)
		if len(found) < 2 {
			t.Fatalf("products 目录至少要有 coding 与 skeleton 两份声明，实得 %d: %v", len(found), found)
		}
		have := map[string]bool{}
		for _, f := range found {
			if !strings.HasSuffix(f, ".agent.json") {
				t.Errorf("发现非声明文件 %s", f)
			}
			have[filepath.Base(f)] = true
		}
		for _, want := range []string{"coding.agent.json", "skeleton.agent.json"} {
			if !have[want] {
				t.Errorf("缺少 %s（设计 8.6 的两份基准声明）", want)
			}
		}
	})
}
