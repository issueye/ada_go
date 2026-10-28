package conformance

import (
	"encoding/json"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
)

// manifestCase 是一个清单夹具用例。
type manifestCase struct {
	dir  string
	want string // 期望错误信息里必须出现的「缺失能力/点位/插槽」名字
	why  string // 对应的设计条款
}

// TestManifestFixtures 把 docs/13-appendix.md 附录 A.1（task-plan 前端清单）与
// A.2（tools-plan 后端清单）落成 testdata/plugins/*/plugin.json，
// 用真实加载器 plugins.LoadManifest 跑正反两面（05 §6.2、06 §6.4/§6.6/§6.9、12 §6.14）。
//
// 负面用例的硬要求来自 12 §6.14：装配期失败必须「报错直接指向缺失能力」，
// 所以每个用例都断言错误信息里出现那个缺失的名字本身，而不是笼统的「清单无效」。
//
// backend.entry 的校验是路径形状校验（绝对路径、反斜杠、盘符、. 与 .. 段一律拒绝），
// 带扩展名的 index.ts 与默认入口都必须能正常加载。
func TestManifestFixtures(t *testing.T) {
	t.Parallel()

	// --- 正面：附录 A.1 / A.2 ---
	t.Run("附录A.1_task-plan", func(t *testing.T) {
		t.Parallel()
		m := loadFixture(t, "task-plan")
		mustEqual(t, "id", "task-plan", m.ID)
		mustEqual(t, "trust", "third-party", string(m.Trust))
		if m.Backend != nil {
			t.Errorf("A.1 是纯前端插件，不该有 backend 段")
		}
		if m.Frontend == nil {
			t.Fatalf("A.1 缺少 frontend 段")
		}
		if len(m.Frontend.Slots) != 1 {
			t.Fatalf("A.1 应有 1 个插槽贡献，实得 %d", len(m.Frontend.Slots))
		}
		slot := m.Frontend.Slots[0]
		mustEqual(t, "插槽名", "chat.corner", slot.Slot)
		mustEqual(t, "模板", "slot.tsx", slot.Template)
		if !plugins.IsSlotName(slot.Slot) {
			t.Errorf("chat.corner 不在 12 个插槽白名单里")
		}
		if !hasString(plugins.FrontendCapabilityNames(), "backend.read") {
			t.Errorf("backend.read 不在前端能力白名单里")
		}
		a, ok := m.Frontend.Actions["plan"]
		if !ok {
			t.Fatalf("A.1 缺少 plan 动作绑定")
		}
		if !a.Read {
			t.Errorf("plan 动作必须是 read（只读预取，7.5）")
		}
		mustEqual(t, "动作目标插件", "tools-plan", a.Backend)
		// 只读动作必须有 backend.read 能力（validateFrontend 的规则，这里复核一遍）。
		if !hasString(capNames(m.Frontend.Capabilities), "backend.read") {
			t.Errorf("A.1 声明了只读动作但没有 backend.read 能力")
		}
		mustTrue(t, "第三方信任级别不给 ui.ask", !m.Trust.AllowsUIAsk())
	})

	t.Run("附录A.2_tools-plan", func(t *testing.T) {
		t.Parallel()
		m := loadFixture(t, "tools-plan")
		mustEqual(t, "id", "tools-plan", m.ID)
		mustEqual(t, "trust", "builtin", string(m.Trust))
		if m.Frontend != nil {
			t.Errorf("A.2 是纯后端插件，不该有 frontend 段")
		}
		if m.Backend == nil {
			t.Fatalf("A.2 缺少 backend 段")
		}
		if m.Backend.Entry != "index.ts" {
			t.Errorf("entry 漂移: %q（附录 A.2 声明 index.ts，加载器不得改写它）", m.Backend.Entry)
		}
		if !hasString(capNames(m.Backend.Capabilities), "state") {
			t.Errorf("A.2 应声明 state 能力")
		}
		mustTrue(t, "state 能力有安装消费点", hasInstallSite("state"))
		if len(m.Backend.Hooks) != 0 {
			t.Logf("A.2 声明了钩子 %v（附录里未声明，装配期按 0 处理）", m.Backend.Hooks)
		}
		// 目录名必须等于 id（6.2）；LoadManifest 不校验目录，这里用 Validate 补一次。
		mustNoErr(t, "Validate(dirName)", m.Validate("tools-plan"))
		if err := m.Validate("other-dir"); err == nil {
			t.Errorf("id 与目录名不一致时 Validate 必须失败")
		}
	})

	t.Run("backend_entry_的形状规则", func(t *testing.T) {
		t.Parallel()
		// 6.2：entry 必须留在插件目录内；带扩展名与子目录的合法入口都要能加载，
		// 越界写法必须被拒——这是插件逃逸目录的入口（09 §9.1 供应链）。
		合法 := []string{"index.ts", "dist/index.js", "src/entry.ts"}
		for _, entry := range 合法 {
			raw := []byte(`{"id":"gap-probe","name":"P","version":"0.1.0","apiVersion":1,` +
				`"trust":"builtin","backend":{"entry":` + jsonStr(entry) + `,"capabilities":["state"]}}`)
			m, err := plugins.LoadManifest(raw)
			mustNoErr(t, "entry="+entry, err)
			if m != nil && m.Backend != nil && m.Backend.Entry != entry {
				t.Errorf("entry 漂移: %q != %q", m.Backend.Entry, entry)
			}
		}
		省略入口 := []byte(`{"id":"gap-probe","name":"P","version":"0.1.0","apiVersion":1,` +
			`"trust":"builtin","backend":{"capabilities":["state"]}}`)
		m, err := plugins.LoadManifest(省略入口)
		mustNoErr(t, "省略 entry 走默认值", err)
		mustEqual(t, "默认入口", plugins.DefaultBackendEntry, m.Backend.Entry)

		越界 := []string{"/etc/passwd", "..\\evil.ts", "../evil.ts", "C:/temp/index.ts", "./index.ts", "a//b.ts"}
		for _, entry := range 越界 {
			raw := []byte(`{"id":"gap-probe","name":"P","version":"0.1.0","apiVersion":1,` +
				`"trust":"builtin","backend":{"entry":` + jsonStr(entry) + `,"capabilities":["state"]}}`)
			_, err := plugins.LoadManifest(raw)
			if err == nil {
				t.Fatalf("entry=%q 必须被拒绝", entry)
			}
			mustErrContains(t, "越界入口/"+entry, err, "backend.entry")
		}
	})

	t.Run("两份夹具共用同一入口", func(t *testing.T) {
		t.Parallel()
		// 官方与第三方只差 trust 与随包发布，没有第二条注册路径（05 §6.2、1.5.5）。
		builtin := loadFixture(t, "tools-plan")
		third := loadFixture(t, "task-plan")
		if builtin.APIVersion != third.APIVersion {
			t.Errorf("apiVersion 应当一致: %d vs %d", builtin.APIVersion, third.APIVersion)
		}
		mustEqual(t, "宿主 API 主版本", "1", jsonInt(t, plugins.APIVersionMajor))
	})

	// --- 负面：每个都必须「报错直接指向缺失能力」 ---
	cases := []manifestCase{
		{"bad-unknown-slot", "chat.middle", "7.2：12 个插槽之外的名字必须拒绝，并报出该插槽名"},
		{"bad-unknown-capability", "fs.reads", "6.2：能力白名单封闭，未知能力位必须报出该名字"},
		{"bad-thirdparty-exec", "exec", "6.9：exec 仅 builtin/verified；第三方声明即拒绝并报出该能力"},
		{"bad-thirdparty-uiask", "ui.ask", "5.6：第三方不能弹窗（钓鱼），必须报出该能力与所需信任级别"},
		{"bad-reserved-env", "ADAG_", "6.13：ADAG_ 前缀由宿主保留，声明即拒绝并报出保留前缀"},
		{"bad-pool-two", "pool=2", "6.4：v1 固定每插件 1 个 Runtime，pool>1 声明即装配失败"},
		{"bad-unknown-hook", "tool.beforeExec", "6.6：15 个点位之外的名字必须拒绝，并报出该点位"},
		{"bad-api-version", "apiVersion=2", "6.2：主版本不匹配直接拒绝加载，并报出声明值与支持范围"},
	}
	for _, c := range cases {
		c := c
		t.Run("负面_"+c.dir, func(t *testing.T) {
			t.Parallel()
			raw := readFixture(t, "plugins", c.dir, "plugin.json")
			m, err := plugins.LoadManifest(raw)
			if err == nil {
				t.Fatalf("%s 必须被拒绝（%s），实得合法清单 %+v", c.dir, c.why, m)
			}
			// 错误信息必须点名缺失的东西（12 §6.14）。
			mustErrContains(t, c.dir, err, c.want)
			// 必须指向具体插件 id，方便多插件装配时定位。
			mustErrContains(t, c.dir+" 的 id 定位", err, c.dir)
			t.Logf("%s → %v", c.dir, err)
		})
	}

	t.Run("负面清单都是合法 JSON", func(t *testing.T) {
		t.Parallel()
		for _, c := range cases {
			c := c
			t.Run(c.dir, func(t *testing.T) {
				t.Parallel()
				var probe map[string]any
				raw := readFixture(t, "plugins", c.dir, "plugin.json")
				if err := json.Unmarshal(raw, &probe); err != nil {
					t.Fatalf("%s 夹具不是合法 JSON: %v", c.dir, err)
				}
				if probe["id"] != c.dir {
					t.Errorf("%s 夹具的 id 与目录名不一致: %v", c.dir, probe["id"])
				}
			})
		}
	})

	t.Run("能力白名单与消费点对齐", func(t *testing.T) {
		t.Parallel()
		// 清单校验依赖的两张表必须自洽：能力位都有安装消费者，点位与插槽数量固定。
		mustNoErr(t, "CheckCapabilitySites", plugins.CheckCapabilitySites())
		mustCount(t, "插槽数量", 12, len(plugins.Slots()))
		mustCount(t, "钩子点位数量", 15, len(plugins.HookPoints()))
		mustCount(t, "后端能力位数量", 8, len(plugins.BackendCapabilities()))
		mustCount(t, "前端能力位数量", 2, len(plugins.FrontendCapabilities()))
		mustCount(t, "角色集合", 1, len(plugins.Roles()))
	})
}

// loadFixture 直接走 plugins.LoadManifest：夹具必须原样加载，不做任何绕行。
func loadFixture(t *testing.T, dir string) *plugins.Manifest {
	t.Helper()
	raw := readFixture(t, "plugins", dir, "plugin.json")
	m, err := plugins.LoadManifest(raw)
	mustNoErr(t, "LoadManifest("+dir+")", err)
	if m == nil {
		t.Fatalf("LoadManifest(%s) 返回 nil", dir)
	}
	return m
}

func capNames(in []plugins.Capability) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		out = append(out, string(c))
	}
	return out
}

func hasString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func hasInstallSite(cap string) bool {
	_, ok := plugins.InstallSiteFor(plugins.Capability(cap))
	return ok
}

// jsonInt 把整数渲染成字符串（断言常量值用，避免 fmt.Sprint 的歧义）。
func jsonInt(t *testing.T, v int) string {
	t.Helper()
	raw, err := json.Marshal(v)
	mustNoErr(t, "marshal int", err)
	return string(raw)
}

// jsonStr 把一个字符串编码成 JSON 字面量，用于拼装清单原文。
func jsonStr(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
