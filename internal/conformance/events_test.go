package conformance

import (
	"sort"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/proto"
)

// domainEventKinds 是 internal/domain/event.go 里的 12 个 evt.* 常量。
// domain 包没有导出集合函数（M0 契约缺口，见汇报），所以这里逐条列出：
// 新增常量不写进来就会在「集合一致」子测试里失败（缺一侧）。
func domainEventKinds() []string {
	return []string{
		string(domain.EvtReady),
		string(domain.EvtStateSnapshot),
		string(domain.EvtStatePatch),
		string(domain.EvtMessageDelta),
		string(domain.EvtItemUpdated),
		string(domain.EvtRunStatus),
		string(domain.EvtInteractionRequested),
		string(domain.EvtInteractionResolved),
		string(domain.EvtNotice),
		string(domain.EvtUsage),
		string(domain.EvtPluginChanged),
		string(domain.EvtError),
	}
}

// TestEventNamesAlignWithProto 断言事件名的两处真源一致（08 §8.2 与生成的 proto）：
//
//   - domain/event.go 的 12 个常量 ⇔ proto.AllEventNames() 的 12 个常量，集合相同；
//   - 每个名字都能在 proto.LookupEvent 里查到，且 scope 与 testkit 记录器的
//     序号分配规则一致（线程级用 Seq、工作区级用 WorkspaceSeq，08 §8.1）；
//   - 事件名一律 evt. 前缀（08 §8.1）；
//   - entersModelContext=false 的事件正是 UI-only 的那几个（INV-10 的第二处证据）。
func TestEventNamesAlignWithProto(t *testing.T) {
	t.Parallel()

	dom := domainEventKinds()
	gen := proto.AllEventNames()

	mustCount(t, "domain 事件常量数", 12, len(dom))
	mustCount(t, "proto 事件名数", 12, len(gen))

	set := func(in []string) map[string]bool {
		m := make(map[string]bool, len(in))
		for _, s := range in {
			if m[s] {
				t.Errorf("事件名重复: %s", s)
			}
			m[s] = true
		}
		return m
	}
	domSet, genSet := set(dom), set(gen)

	t.Run("集合一致", func(t *testing.T) {
		t.Parallel()
		var diff []string
		for name := range domSet {
			if !genSet[name] {
				diff = append(diff, "  只在 domain: "+name)
			}
		}
		for name := range genSet {
			if !domSet[name] {
				diff = append(diff, "  只在 proto: "+name)
			}
		}
		if len(diff) > 0 {
			sort.Strings(diff)
			t.Errorf("事件名漂移（domain/event.go ↔ proto.Events）：\n%s", strings.Join(diff, "\n"))
		}
	})

	t.Run("前缀与查找表", func(t *testing.T) {
		t.Parallel()
		for _, name := range dom {
			if !strings.HasPrefix(name, proto.EventIDPrefix) {
				t.Errorf("事件名 %q 缺少 %q 前缀（08 §8.1）", name, proto.EventIDPrefix)
			}
			if _, ok := proto.LookupEvent(name); !ok {
				t.Errorf("事件名 %q 不在生成的 proto.Events 里", name)
			}
		}
	})

	t.Run("scope 与序号语义", func(t *testing.T) {
		t.Parallel()
		sink := testkit.NewRecordingEventSink()
		var workspaceEvents, threadEvents []string
		for _, name := range sortedStrings(dom) {
			meta, ok := proto.LookupEvent(name)
			mustTrue(t, "LookupEvent("+name+")", ok)
			switch meta.Scope {
			case "workspace":
				workspaceEvents = append(workspaceEvents, name)
				// 工作区级事件必须用 WorkspaceSeq 独立计数（08 §8.1）。
				mustNoErr(t, "Emit "+name, sink.Emit(t.Context(),
					domain.Event{Kind: domain.EventKind(name), Payload: []byte(`{}`)}))
			case "thread":
				threadEvents = append(threadEvents, name)
				mustNoErr(t, "Emit "+name, sink.Emit(t.Context(), domain.Event{
					Kind: domain.EventKind(name), ThreadID: "th_abc_01J00000000000000000000000",
					Payload: []byte(`{}`)}))
			default:
				t.Errorf("事件 %q 的 scope %q 既不是 thread 也不是 workspace", name, meta.Scope)
			}
		}
		if len(workspaceEvents) == 0 || len(threadEvents) == 0 {
			t.Errorf("scope 分类异常: workspace=%v thread=%v", workspaceEvents, threadEvents)
		}
		// testkit 的替身必须按 proto 的 scope 分配序号：这里回读断言。
		var sawWS, sawSeq bool
		for _, e := range sink.Events() {
			if e.WorkspaceSeq > 0 {
				sawWS = true
			}
			if e.Seq > 0 {
				sawSeq = true
			}
		}
		mustTrue(t, "工作区级事件应拿到 WorkspaceSeq", sawWS)
		mustTrue(t, "线程级事件应拿到 Seq", sawSeq)
		mustNoErr(t, "SeqMonotonic", sink.SeqMonotonic())
		t.Logf("workspace 级事件: %v", workspaceEvents)
	})

	t.Run("UI-only 事件不进模型上下文", func(t *testing.T) {
		t.Parallel()
		// INV-10 的第二处证据：协议元数据里 entersModelContext=false 必须覆盖
		// notice / run.status / interaction.* / usage / plugin.changed / error / ready。
		for _, name := range []string{
			string(domain.EvtNotice), string(domain.EvtRunStatus),
			string(domain.EvtInteractionRequested), string(domain.EvtInteractionResolved),
			string(domain.EvtUsage), string(domain.EvtPluginChanged), string(domain.EvtError),
			string(domain.EvtReady),
		} {
			meta, ok := proto.LookupEvent(name)
			mustTrue(t, "LookupEvent("+name+")", ok)
			if meta.EntersModelContext {
				t.Errorf("事件 %q 被标成进模型上下文（INV-10 要求不进）", name)
			}
		}
	})

	t.Run("replayable 覆盖断序恢复", func(t *testing.T) {
		t.Parallel()
		// 08 §8.1「所有事件可重放，至少保留到对应 run 结束」与 §8.3 的补发窗口：
		// 快照本身是重同步入口，不要求可补发；其余线程级事件必须可重放。
		meta, ok := proto.LookupEvent(string(domain.EvtStateSnapshot))
		mustTrue(t, "LookupEvent(evt.state.snapshot)", ok)
		if meta.Replayable {
			t.Logf("evt.state.snapshot 标记为可补发（协议里它是重同步入口，不是补发对象）")
		}
		for _, name := range []string{
			string(domain.EvtStatePatch), string(domain.EvtMessageDelta),
			string(domain.EvtItemUpdated), string(domain.EvtRunStatus),
			string(domain.EvtInteractionRequested), string(domain.EvtInteractionResolved),
			string(domain.EvtNotice),
		} {
			m, ok := proto.LookupEvent(name)
			mustTrue(t, "LookupEvent("+name+")", ok)
			if !m.Replayable {
				t.Errorf("事件 %q 不可重放，违反 08 §8.1「所有事件可重放」", name)
			}
		}
	})
}
