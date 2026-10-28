package testkit

import "github.com/adag/ada-go/internal/ports"

// 本文件是 INV-2（02 §5.3「每个端口都有生产实现与测试替身，不允许 nil 端口」）
// 在测试替身一侧的编译期保证：一旦替身不再满足端口签名，`go build ./...` 直接失败，
// 不必等到 M1 运行期。右侧标注生产实现落地的里程碑（抄自 internal/ports 各文件注释）。
//
// used-by-M0 的端口（本包提供替身）：
//
//	ports.ModelClient        -> FakeProvider / Retry      生产：adapters + provider 插件桥（M1-E）
//	ports.Stream             -> FakeStream                生产：同上（M1-E）
//	ports.EventSink          -> RecordingEventSink         生产：coreproto 事件发射器（M1-A/M2）
//	ports.Clock              -> FakeClock                  生产：adapters/clock（M1-A）
//	ports.IDGen              -> FixedIDGen                 生产：adapters/idgen（M1-A）
//	ports.InteractionBroker  -> FakeInteractions           生产：coreproto + Gateway 托盘 + interactions 表（M1-A/M2）
//	ports.ToolCatalog        -> EmptyCatalog/StaticCatalog 生产：plugins 装配结算 + toolkit 注册表（M1-A/M3）
//
// M1 才需要替身的端口（本包刻意不预置，避免「替身先于契约实现」的假覆盖）：
//
//	ports.SessionStore   -> M1-B（内存 store，保持 append-only 与 seq 语义）
//	ports.StateStore     -> M1-B（内存 KV）
//	ports.SecretBroker   -> M1-B（内存密钥环 + 脱敏断言）
//	ports.Scope          -> M1-F（内存 scope，合成 symlink 环与越界样本；样本已由 TempWorkspace 提供）
//	ports.HookRunner     -> M1-A（可编程钩子桩，注入超时/异常/各动作）
//	ports.ScriptHost     -> M1-C（脚本桩，按剧本返回 JSON/错误/超时）
//
// internal/conformance/ports_test.go 会对 12 个端口做反射检查并把这张表打印出来。
var (
	_ ports.ModelClient       = (*FakeProvider)(nil)
	_ ports.ModelClient       = (*Retry)(nil)
	_ ports.Stream            = (*FakeStream)(nil)
	_ ports.EventSink         = (*RecordingEventSink)(nil)
	_ ports.Clock             = (*FakeClock)(nil)
	_ ports.IDGen             = (*FixedIDGen)(nil)
	_ ports.InteractionBroker = (*FakeInteractions)(nil)
	_ ports.ToolCatalog       = (*EmptyCatalog)(nil)
	_ ports.ToolCatalog       = (*StaticCatalog)(nil)
)

// 端口清单是测试断言与报告共用的真源（顺序照 ports/doc.go）。
var (
	// PortNames 是 12 个端口的稳定名称表。
	PortNames = []string{
		"Clock", "IDGen", "EventSink", "SessionStore", "ModelClient", "ToolCatalog",
		"InteractionBroker", "HookRunner", "Scope", "ScriptHost", "StateStore", "SecretBroker",
	}

	// DoublesForM0 列出 M0 已由本包提供替身的端口。
	DoublesForM0 = map[string]string{
		"ModelClient":       "FakeProvider",
		"Stream":            "FakeStream",
		"EventSink":         "RecordingEventSink",
		"Clock":             "FakeClock",
		"IDGen":             "FixedIDGen",
		"InteractionBroker": "FakeInteractions",
		"ToolCatalog":       "EmptyCatalog",
	}

	// PendingDoubles 列出替身留到哪个里程碑落地（与上面的注释一致）。
	PendingDoubles = map[string]string{
		"SessionStore": "M1-B",
		"StateStore":   "M1-B",
		"SecretBroker": "M1-B",
		"Scope":        "M1-F",
		"HookRunner":   "M1-A",
		"ScriptHost":   "M1-C",
	}

	// ProductionMilestone 是每个端口生产实现的落地里程碑（抄自 ports 包注释）。
	ProductionMilestone = map[string]string{
		"Clock":             "M1-A",
		"IDGen":             "M1-A",
		"EventSink":         "M1-A/M2",
		"SessionStore":      "M1-B",
		"ModelClient":       "M1-E",
		"ToolCatalog":       "M1-A/M3",
		"InteractionBroker": "M1-A/M2",
		"HookRunner":        "M1-A/M3",
		"Scope":             "M1-F/M5",
		"ScriptHost":        "M1-C",
		"StateStore":        "M1-B",
		"SecretBroker":      "M1-B",
	}
)
