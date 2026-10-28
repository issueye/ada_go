package conformance

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/ports"
)

// 编译期断言：used-by-M0 的每个端口都有测试替身（INV-2，02 §5.3）。
// 这三行与 testkit/doubles.go 里的同一组断言重复，是有意的：
//   - testkit 侧保证「替身本身不破契约」，`go build ./...` 就会失败；
//   - 这里保证「契约测试确实盯着这几个端口」，删掉替身时本文件立刻编译失败。
var (
	_ ports.ModelClient       = (*testkit.FakeProvider)(nil)
	_ ports.EventSink         = (*testkit.RecordingEventSink)(nil)
	_ ports.Clock             = (*testkit.FakeClock)(nil)
	_ ports.IDGen             = (*testkit.FixedIDGen)(nil)
	_ ports.InteractionBroker = (*testkit.FakeInteractions)(nil)
	_ ports.ToolCatalog       = (*testkit.EmptyCatalog)(nil)
	_ ports.ModelClient       = (*testkit.Retry)(nil)
	_ ports.Stream            = (*testkit.FakeStream)(nil)
)

// portInterface 是 12 个端口的 reflect 视图（顺序照 ports/doc.go 的清单）。
var portInterface = map[string]any{
	"Clock":             (*ports.Clock)(nil),
	"IDGen":             (*ports.IDGen)(nil),
	"EventSink":         (*ports.EventSink)(nil),
	"SessionStore":      (*ports.SessionStore)(nil),
	"ModelClient":       (*ports.ModelClient)(nil),
	"ToolCatalog":       (*ports.ToolCatalog)(nil),
	"InteractionBroker": (*ports.InteractionBroker)(nil),
	"HookRunner":        (*ports.HookRunner)(nil),
	"Scope":             (*ports.Scope)(nil),
	"ScriptHost":        (*ports.ScriptHost)(nil),
	"StateStore":        (*ports.StateStore)(nil),
	"SecretBroker":      (*ports.SecretBroker)(nil),
}

// TestPortsAreImplemented 是 INV-2 在 M0 的可执行部分（development-plan §4.1 M0.5、
// 01 §5.2「12 个端口，一个不多一个不少」）：
//
//  1. 反射检查：12 个端口都是非空接口且方法数 > 0（防止把端口写成 struct 或空接口）；
//  2. 替身检查：M0 需要的端口必须已经在本包提供（编译期断言 + 运行期表比对）；
//  3. 端口数量必须正好是 12（多一个少一个都要先改设计文档，01 §5.2）；
//  4. 打印端口清单与「生产实现落在哪个里程碑」，作为 CI 日志里的人工核对物。
//
// 生产实现里程碑抄自 internal/ports 各文件的注释：
//
//	Clock/IDGen/EventSink/InteractionBroker/HookRunner/ToolCatalog -> M1-A 起（EventSink/InteractionBroker 的
//	                                                                  Gateway 侧在 M2）
//	SessionStore/StateStore/SecretBroker                           -> M1-B（adapters/sqlite、adapters/secret）
//	ScriptHost                                                     -> M1-C（plugins 的 moejs 宿主）
//	ModelClient                                                    -> M1-E（provider 插件桥 + 基座重试包装）
//	ToolCatalog                                                    -> M1-A/M3（装配期结算 + toolkit 注册表）
//	Scope                                                            -> M1-F/M5（adapters/scope，realpath + 平台规则）
func TestPortsAreImplemented(t *testing.T) {
	t.Parallel()

	t.Run("端口数量正好 12", func(t *testing.T) {
		t.Parallel()
		mustCount(t, "ports 端口数量", 12, len(portInterface))
		mustCount(t, "testkit 替身表覆盖的端口名", 12, len(testkit.ProductionMilestone))
		for name := range portInterface {
			if _, ok := testkit.ProductionMilestone[name]; !ok {
				t.Errorf("端口 %s 没有登记生产实现里程碑（INV-2 要求可追）", name)
			}
		}
		for name := range testkit.ProductionMilestone {
			if _, ok := portInterface[name]; !ok {
				t.Errorf("testkit 登记了未知端口 %q（ports 包里没有它）", name)
			}
		}
	})

	t.Run("每个端口都是非空接口", func(t *testing.T) {
		names := make([]string, 0, len(portInterface))
		for name := range portInterface {
			names = append(names, name)
		}
		sort.Strings(names)

		// 报告在父测试里顺序拼装：并行子测试共写一个 strings.Builder 是数据竞争
		// （-race 会直接抓到），而这里的断言本身是纯反射、耗时可忽略。
		var report strings.Builder
		fmt.Fprintf(&report, "端口清单（12 个，01 §5.2）：\n")
		for _, name := range names {
			rt := reflect.TypeOf(portInterface[name]).Elem()
			if rt.Kind() != reflect.Interface {
				t.Fatalf("ports.%s 不是接口类型，而是 %s", name, rt.Kind())
			}
			if rt.NumMethod() == 0 {
				t.Fatalf("ports.%s 是空接口：端口必须有语义（INV-2 要求有生产实现与替身）", name)
			}
			fmt.Fprintf(&report, "  %-18s 方法数=%-2d 替身=%-18s 生产=%s\n",
				name, rt.NumMethod(), testkit.DoublesForM0[name], testkit.ProductionMilestone[name])
		}
		t.Log(report.String())
	})

	t.Run("方法签名与替身一致", func(t *testing.T) {
		t.Parallel()
		// 替身类型必须实现同名端口的全部方法（编译期已保证；这里再断言一次
		// 「方法集合非空且可枚举」，防止有人把替身写成空壳 + panic）。
		pairs := []struct {
			port   any
			double any
			name   string
		}{
			{(*ports.ModelClient)(nil), &testkit.FakeProvider{}, "ModelClient"},
			{(*ports.EventSink)(nil), &testkit.RecordingEventSink{}, "EventSink"},
			{(*ports.Clock)(nil), &testkit.FakeClock{}, "Clock"},
			{(*ports.IDGen)(nil), &testkit.FixedIDGen{}, "IDGen"},
			{(*ports.InteractionBroker)(nil), &testkit.FakeInteractions{}, "InteractionBroker"},
			{(*ports.ToolCatalog)(nil), &testkit.EmptyCatalog{}, "ToolCatalog"},
		}
		for _, p := range pairs {
			p := p
			t.Run(p.name, func(t *testing.T) {
				t.Parallel()
				portType := reflect.TypeOf(p.port).Elem()
				doubleType := reflect.TypeOf(p.double)
				for i := 0; i < portType.NumMethod(); i++ {
					m := portType.Method(i)
					if _, ok := doubleType.MethodByName(m.Name); !ok {
						t.Errorf("%s (%s) 缺方法 %s", doubleType, portType, m.Name)
					}
				}
			})
		}
	})

	t.Run("M1 才落地的替身必须显式登记", func(t *testing.T) {
		t.Parallel()
		// INV-2 的诚实版本：本包只提供 M0 用到的替身，其余 6 个端口
		// 在 testkit.PendingDoubles 里登记里程碑；未登记的端口视为缺口。
		mustCount(t, "待落地替身数", 6, len(testkit.PendingDoubles))
		for name := range portInterface {
			_, has := testkit.DoublesForM0[name]
			_, pending := testkit.PendingDoubles[name]
			if !has && !pending {
				t.Errorf("端口 %s 既无替身也未登记落地里程碑", name)
			}
			if has && pending {
				t.Errorf("端口 %s 同时出现在替身表与待落地表里", name)
			}
		}
		for name, ms := range testkit.PendingDoubles {
			if !strings.HasPrefix(ms, "M1") {
				t.Errorf("端口 %s 的替身登记为 %q，M0 只允许 M1-*", name, ms)
			}
		}
	})
}
