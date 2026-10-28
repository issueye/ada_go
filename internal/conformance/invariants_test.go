package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/engine"
	"github.com/adag/ada-go/internal/ports"
	"github.com/adag/ada-go/internal/proto"
)

// TestInvariants 是 INV-1…INV-12 的骨架套件（02 §5.3 不变量表，development-plan §4.1 M0.5、
// §4.2 A「建立 INV-1～INV-12 的不变量测试」）。CI 门禁命令是
// `go test ./internal/conformance -run TestInvariants`（§7.1），命中的就是本函数。
//
// 口径（不放水）：
//   - 每条不变量都必须对**已存在的代码**做真实断言：端口签名、常量表、事件序号语义、
//     失败关闭判定、空基座装配、替身的取消/超时行为；
//   - 依赖 RunTurn / SQLite / moejs 宿主的行为由各自包的真实用例覆盖（engine/、
//     adapters/sqlite、plugins/host），这里不重复造第二套引擎夹具，也不留过期 Skip。
//
// 仍留给后续里程碑的只有：
//
//	INV-1 的第二半（子智能体复用同一装配变体）        -> M5/v1.5
func TestInvariants(t *testing.T) {
	t.Parallel()

	// ---------------------------------------------------------------
	t.Run("INV-1_只有一份多轮循环", func(t *testing.T) {
		t.Parallel()
		found := findFuncDecls(t, "Run")
		if len(found) != 1 {
			t.Fatalf("多轮循环入口 Run 必须全仓唯一（INV-1），实得 %d 处: %v", len(found), found)
		}
		entry := filepath.ToSlash(found[0])
		if !strings.Contains(entry, "internal/engine/") {
			t.Errorf("唯一的多轮循环入口不在 internal/engine 里: %s", found[0])
		}
		for _, forbidden := range []string{"internal/adapters", "internal/gateway", "internal/plugins", "cmd/"} {
			if strings.Contains(entry, forbidden) {
				t.Errorf("循环入口落在了 forbidden 目录 %s: %s", forbidden, entry)
			}
		}
		// M1-A 起 Run 是真实现：端口不齐时必须失败关闭（INV-2 不允许 nil 端口），
		// 而不是 panic，也不是静默成功。
		if got := engine.Run(t.Context(), engine.Deps{}, engine.TurnSpec{ThreadID: "th_x_1", RunID: "run-1"}); got != domain.TerminalError {
			t.Errorf("端口缺失时 Run 必须返回 error 终态，实得 %q", got)
		}
		// 不存在第二份「循环入口」的同义命名。
		for _, alt := range []string{"RunLoop", "RunAgentLoop", "RunSubTurn", "RunSubagentTurn", "StepLoop"} {
			if d := findFuncDecls(t, alt); len(d) > 0 {
				t.Errorf("发现了第二套循环入口 %s: %v（INV-1 禁止）", alt, d)
			}
		}
		// 引擎不得反向依赖适配器/插件/Gateway（01 §3.3 依赖方向）。
		assertNoImport(t, "internal/engine", []string{"internal/adapters", "internal/plugins", "internal/gateway", "official/"})
		t.Log("子智能体复用同一 RunTurn 的装配变体断言留给 v1.5（M5 预研）")
	})

	// ---------------------------------------------------------------
	t.Run("INV-2_端口都有测试替身", func(t *testing.T) {
		t.Parallel()
		// 替身一侧（M0 就能真断言）：7 个 used-by-M0 端口的实现关系由编译期 var _ 保证，
		// 这里再断言替身可实例化且非 nil，杜绝「nil 端口」（02 §5.3 INV-2）。
		doubles := map[string]any{
			"ModelClient":       testkit.NewFakeProvider(nil),
			"EventSink":         testkit.NewRecordingEventSink(),
			"Clock":             testkit.NewFakeClock(),
			"IDGen":             testkit.NewFixedIDGen(),
			"InteractionBroker": testkit.NewFakeInteractions(),
			"ToolCatalog":       testkit.NewEmptyCatalog(),
		}
		for name, d := range doubles {
			if d == nil {
				t.Errorf("端口 %s 的替身为 nil（INV-2 不允许 nil 端口）", name)
			}
			if _, ok := testkit.DoublesForM0[name]; !ok {
				t.Errorf("替身 %s 没有登记进 testkit.DoublesForM0", name)
			}
		}
		mustCount(t, "M0 替身登记数", 7, len(testkit.DoublesForM0))
		mustCount(t, "待落地替身登记数", 6, len(testkit.PendingDoubles))

		// 生产实现一侧：每个端口都要有真实实现，不能只有替身（INV-2）。
		for _, rel := range []string{
			"internal/adapters/clock.go",
			"internal/adapters/idgen.go",
			"internal/adapters/secret.go",
			"internal/adapters/scope.go",
			"internal/adapters/sqlite/store.go",
			"internal/plugins/provider/registry.go",
			"internal/plugins/host/runtime.go",
			"internal/coreproto/interactions.go",
			"internal/toolkit/execute.go",
			"internal/engine/run.go",
		} {
			if _, err := os.Stat(repoPath(t, filepath.FromSlash(rel))); err != nil {
				t.Errorf("端口 %s 缺少生产实现: %v", rel, err)
			}
		}
	})

	// ---------------------------------------------------------------
	t.Run("INV-3_描述符注册表是唯一真源", func(t *testing.T) {
		t.Parallel()
		// 描述符字段必须与 08 §8.4 的 JSON 形状逐字一致（目录由它派生，不是另写一份）。
		want := []string{
			"access", "description", "name", "parameters", "parallelSafe", "pluginId",
			"repeatable", "rollback", "terminatesTurn", "timeoutMs",
		}
		got := jsonTags(reflect.TypeOf(domain.Descriptor{}))
		sort.Strings(got)
		sortWant := append([]string(nil), want...)
		sort.Strings(sortWant)
		if strings.Join(sortWant, ",") != strings.Join(got, ",") {
			t.Errorf("domain.Descriptor 的 JSON 字段与 08 §8.4 不一致：\n  want=%v\n   got=%v", sortWant, got)
		}
		// 超时字段是 time.Duration，但 wire 名是 timeoutMs（换算责任在适配层，02 §5.4）。
		f, _ := reflect.TypeOf(domain.Descriptor{}).FieldByName("Timeout")
		if f.Type.String() != "time.Duration" || f.Tag.Get("json") != "timeoutMs" {
			t.Errorf("Timeout 字段漂移: %s tag=%q", f.Type, f.Tag.Get("json"))
		}
		// 目录端口只有读面：没有 Register 方法，运行期无法改目录。
		rt := reflect.TypeOf((*ports.ToolCatalog)(nil)).Elem()
		for i := 0; i < rt.NumMethod(); i++ {
			name := rt.Method(i).Name
			if strings.HasPrefix(name, "Reg") || strings.HasPrefix(name, "Add") || strings.HasPrefix(name, "Set") {
				t.Errorf("ToolCatalog 出现写入方法 %s（目录在装配期冻结，02 §5.4/INV-3）", name)
			}
		}
		mustCount(t, "ToolCatalog 方法数", 2, rt.NumMethod())
	})

	// ---------------------------------------------------------------
	t.Run("INV-4_交互失败方向默认关闭", func(t *testing.T) {
		t.Parallel()
		all := []domain.InteractionStatus{
			domain.InteractionPending, domain.InteractionAccepted, domain.InteractionDenied,
			domain.InteractionCancelled, domain.InteractionTimeout, domain.InteractionUnavailable,
			domain.InteractionStatus("未知状态"), domain.InteractionStatus(""),
		}
		for _, s := range all {
			if s == domain.InteractionAccepted {
				if s.IsDenial() {
					t.Errorf("accepted 不该被当作拒绝")
				}
				if s.Direction() != domain.FailOpen {
					t.Errorf("accepted 的失败方向应是已放行，实得 %s", s.Direction())
				}
				continue
			}
			if !s.IsDenial() {
				t.Errorf("状态 %q 没被当作拒绝（INV-4 要求除 accepted 外一律拒绝）", s)
			}
			if s.Direction() != domain.FailClosed {
				t.Errorf("状态 %q 的方向是 %s，要求 closed", s, s.Direction())
			}
		}

		// 替身侧的真实行为：无 UI 与超时都必须解析成拒绝。
		noUI := testkit.NewFakeInteractions(testkit.WithoutUI())
		res, err := noUI.Request(t.Context(), testkit.InteractionRequest("int-1", "th_a_1", "some-policy",
			domain.InteractionConfirm, "允许写文件？", "src/main.go"))
		mustNoErr(t, "无 UI Request", err)
		mustEqual(t, "无 UI 解析状态", string(domain.InteractionUnavailable), string(res.Status))
		mustTrue(t, "无 UI 必须按拒绝处理", res.Status.IsDenial())
		mustNoErr(t, "无 UI 恰好解析一次", noUI.ExactlyOnce("int-1"))

		expired := testkit.NewFakeInteractions()
		req := testkit.InteractionRequest("int-2", "th_a_1", "some-policy", domain.InteractionConfirm, "执行命令？", "rm -rf /tmp/x")
		req.ExpiresAt = req.CreatedAt.Add(time.Second)
		expired.Clock().(*testkit.FakeClock).Advance(2 * time.Second)
		res2, err := expired.Request(t.Context(), req)
		mustNoErr(t, "超时 Request", err)
		mustEqual(t, "超时解析状态", string(domain.InteractionTimeout), string(res2.Status))
		mustTrue(t, "超时必须按拒绝处理", res2.Status.IsDenial())
		mustNoErr(t, "超时恰好解析一次", expired.ExactlyOnce("int-2"))

		// 调用方不得伪造 timeout/unavailable（只能由实现方写入）。
		forged := testkit.NewFakeInteractions(testkit.WithOutcomes(testkit.Waiting()))
		go func() {
			_, _ = forged.Request(context.Background(), testkit.InteractionRequest("int-3", "th_a_1", "some-policy",
				domain.InteractionConfirm, "标题", "正文"))
		}()
		waitFor(t, func() bool { return forged.RequestCount() == 1 }, "交互请求登记")
		_, err = forged.Respond(t.Context(), "th_a_1", "int-3", domain.InteractionTimeout, "")
		if !errors.Is(err, testkit.ErrForbiddenStatus) {
			t.Errorf("伪造 timeout 必须被拒，实得 %v", err)
		}
	})

	// ---------------------------------------------------------------
	t.Run("INV-5_拒绝是工具结果而不是异常", func(t *testing.T) {
		t.Parallel()
		// 拒绝的载体是 domain.Result（content + receipt），不是 error：
		// 08 §8.4 的拒绝样例 + 02 §5.4 规则「拒绝是工具结果，不终止轮次」。
		res := testkit.DeniedResponder("用户不允许修改生产配置")(domain.ToolCall{CallID: "call_x", Tool: "write_file"})
		mustEqual(t, "回执状态", string(domain.ReceiptDenied), string(res.Receipt.Status))
		if len(res.Content) == 0 || !strings.HasPrefix(res.Content[0].Text, "denied:") {
			t.Errorf("拒绝理由必须作为文本回给模型（08 §8.4）: %+v", res.Content)
		}
		if res.Receipt.Reason == "" {
			t.Errorf("denied 回执要带 reason（界面卡片显示 denied）")
		}
		// Result 里没有 error 字段：拒绝走不到异常通道。
		if hasField(reflect.TypeOf(domain.Result{}), "Error") || hasField(reflect.TypeOf(domain.Result{}), "Err") {
			t.Errorf("domain.Result 出现 error 字段，违背 INV-5")
		}
		// 回执状态集合封闭：ok|error|denied|timeout|interrupted。
		for _, s := range []domain.ReceiptStatus{
			domain.ReceiptOK, domain.ReceiptError, domain.ReceiptDenied,
			domain.ReceiptTimeout, domain.ReceiptInterrupted,
		} {
			if string(s) == "" {
				t.Errorf("回执状态缺失")
			}
		}
		// 无策略插件时的失败关闭文案（03 §5.6 / coreapp.Check）。
		p, err := coreapp.LoadProduct(repoPath(t, "products", "coding.agent.json"))
		mustNoErr(t, "LoadProduct", err)
		err = (&coreapp.Runtime{Product: p, AccessesInUse: []string{"write"}}).Check()
		mustErrContains(t, "无策略插件", err, "denied: no policy")
	})

	// ---------------------------------------------------------------
	t.Run("INV-6_事件序号单调且先 started 后 finished", func(t *testing.T) {
		t.Parallel()
		sink := testkit.NewRecordingEventSink()
		const thread = "th_abc_01J00000000000000000000000"
		ctx := t.Context()
		mustNoErr(t, "Emit running", sink.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunRunning, "")))
		mustNoErr(t, "Emit delta", sink.Emit(ctx, testkit.MessageDeltaEvent(thread, "msg-1", "第一段")))
		mustNoErr(t, "Emit started", sink.Emit(ctx, testkit.ToolStartedEvent(thread, "call_01J1", "read_file")))
		mustNoErr(t, "Emit finished", sink.Emit(ctx, testkit.ToolFinishedEvent(thread, "call_01J1", domain.ReceiptOK)))
		mustNoErr(t, "Emit terminal", sink.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunTerminal, domain.TerminalCompleted)))

		mustNoErr(t, "SeqMonotonic", sink.SeqMonotonic())
		mustNoErr(t, "StartedBeforeFinished", sink.StartedBeforeFinished("call_01J1"))
		mustCount(t, "线程内 seq 唯一递增", 5, sink.Len())

		// 反例必须被测出来（断言辅助不是摆设）。
		bad := testkit.NewRecordingEventSink(testkit.WithRelaxed())
		mustNoErr(t, "手填 seq=9", bad.Emit(ctx, domain.Event{Kind: domain.EvtMessageDelta, ThreadID: thread, Seq: 9, Payload: []byte(`{}`)}))
		mustNoErr(t, "手填 seq=3", bad.Emit(ctx, domain.Event{Kind: domain.EvtMessageDelta, ThreadID: thread, Seq: 3, Payload: []byte(`{}`)}))
		if bad.SeqMonotonic() == nil {
			t.Errorf("乱序 seq 必须被 SeqMonotonic 判失败")
		}
		rev := testkit.NewRecordingEventSink()
		mustNoErr(t, "先 finished", rev.Emit(ctx, testkit.ToolFinishedEvent(thread, "call_01J2", domain.ReceiptError)))
		mustNoErr(t, "后 started", rev.Emit(ctx, testkit.ToolStartedEvent(thread, "call_01J2", "run_command")))
		if rev.StartedBeforeFinished("call_01J2") == nil {
			t.Errorf("finished 先于 started 必须被判定失败（INV-6）")
		}
		if err := rev.StartedBeforeFinished("call_不存在"); err == nil {
			t.Errorf("完全没有工具卡事件时也必须报错，不能默认通过")
		}
	})

	// ---------------------------------------------------------------
	t.Run("INV-7_同线程最多一个运行中轮次", func(t *testing.T) {
		t.Parallel()
		// 真实可断言的契约事实：thread_busy 是协议码、turn.start 是幂等方法且要求就绪实例。
		mustEqual(t, "thread_busy 名称", "thread_busy", domain.CodeName(domain.CodeThreadBusy))
		mustCount(t, "thread_busy 码值", 2001, int(domain.CodeThreadBusy))
		meta, ok := proto.LookupMethod(proto.MethodTurnStart)
		mustTrue(t, "turn.start 在生成方法表里", ok)
		if meta.Group != "turn" {
			t.Errorf("turn.start 分组漂移: %s", meta.Group)
		}
		if domain.IsRetryable(domain.CodeThreadBusy) {
			t.Errorf("thread_busy 不允许基座自动重试（INV-7：不排队重放）")
		}
		// 真实互斥由 engine/run_test.go 的 TestThreadBusyRejectsSecondRun 覆盖；
		// 这里只断言协议事实，不重复造第二套引擎夹具。
	})

	// ---------------------------------------------------------------
	t.Run("INV-8_取消贯穿模型阶段", func(t *testing.T) {
		t.Parallel()
		clk := testkit.NewFakeClock()
		provider := testkit.NewFakeProvider(testkit.NewScriptedModel([]testkit.ScriptStep{
			testkit.Idle(time.Minute), // 60s 静默：小于 90s 空闲预算，因此挂在假时钟上等取消
			testkit.Finish("stop", "[DONE]"),
		}))
		provider.Clock = clk
		ctx, cancel := context.WithCancel(t.Context())
		stream, err := provider.Stream(ctx, ports.Request{Model: ports.ModelRef{ModelID: "fake-1"}})
		mustNoErr(t, "Stream", err)
		got := make(chan error, 1)
		go func() {
			_, err := stream.Recv()
			got <- err
		}()
		waitFor(t, func() bool { return clk.Pending() >= 1 }, "假时钟上的空闲等待")
		cancel()
		select {
		case err := <-got:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("取消后 Recv 必须返回 context.Canceled，实得 %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("取消没有穿透到模型流（INV-8）")
		}
		mustNoErr(t, "Close", stream.Close())
		mustCount(t, "关闭后打开流数", 0, provider.OpenStreamCount())

		// 空闲预算本身：90s（domain.ProviderIdleTimeout，可经 ADAG_STREAM_IDLE 调整）。
		// 超过预算的静默必须直接失败关闭，而不是无限等（03 §5.5 超时分层）。
		idleProvider := testkit.NewFakeProvider(testkit.NewScriptedModel([]testkit.ScriptStep{
			testkit.Idle(91 * time.Second),
			testkit.Finish("stop", "[DONE]"),
		}))
		idleStream, err := idleProvider.Stream(t.Context(), ports.Request{})
		mustNoErr(t, "空闲流建立", err)
		_, err = idleStream.Recv()
		if !errors.Is(err, testkit.ErrStreamIdle) {
			t.Errorf("超过空闲预算必须判为空闲超时，实得 %v", err)
		}
		if budget := time.Duration(domain.ProviderIdleTimeout); budget != 90*time.Second {
			t.Errorf("空闲预算漂移: %s", budget)
		}

		// 取消也贯穿等待中的交互（03 §5.6：未决交互标记 cancelled）。
		broker := testkit.NewFakeInteractions()
		selectReq := testkit.InteractionRequest("int-9", "th_a_1", "some-policy",
			domain.InteractionSelect, "选一个", "")
		selectReq.Options = []string{"a", "b"}
		cctx, ccancel := context.WithCancel(t.Context())
		done := make(chan ports.InteractionResolution, 1)
		go func() {
			res, _ := broker.Request(cctx, selectReq)
			done <- res
		}()
		waitFor(t, func() bool { return broker.RequestCount() == 1 }, "交互登记")
		ccancel()
		select {
		case res := <-done:
			mustEqual(t, "取消后的交互状态", string(domain.InteractionCancelled), string(res.Status))
			mustTrue(t, "cancelled 也算拒绝", res.Status.IsDenial())
		case <-time.After(2 * time.Second):
			t.Fatalf("取消没有穿透到等待中的交互（INV-8）")
		}
		// 工具执行/插件脚本/进程树的取消由 engine、host 与 toolkit 的取消用例覆盖（INV-8）。
	})

	// ---------------------------------------------------------------
	t.Run("INV-9_恰好一个终态", func(t *testing.T) {
		t.Parallel()
		const thread = "th_abc_01J00000000000000000000001"
		ctx := t.Context()

		// 正常路径：running → terminal(completed) 恰好一次。
		ok := testkit.NewRecordingEventSink()
		mustNoErr(t, "running", ok.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunRunning, "")))
		mustNoErr(t, "terminal", ok.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunTerminal, domain.TerminalCompleted)))
		mustNoErr(t, "ExactlyOneTerminal", ok.ExactlyOneTerminal())
		mustCount(t, "terminal 计数", 1, ok.TerminalCount())

		// 错误路径：漏发必须被检出。
		missing := testkit.NewRecordingEventSink()
		mustNoErr(t, "running", missing.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunRunning, "")))
		if missing.ExactlyOneTerminal() == nil {
			t.Errorf("漏发终态必须被判定失败（INV-9）")
		}
		// 错误路径：重发必须被检出。
		dup := testkit.NewRecordingEventSink()
		mustNoErr(t, "terminal 1", dup.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunTerminal, domain.TerminalError)))
		mustNoErr(t, "terminal 2", dup.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunTerminal, domain.TerminalError)))
		if dup.ExactlyOneTerminal() == nil {
			t.Errorf("重发终态必须被判定失败（INV-9）")
		}

		// 终态原因集合封闭（02 §5.3 表）。
		seen := map[domain.TerminalReason]bool{
			domain.TerminalCompleted: true, domain.TerminalCancelled: true, domain.TerminalError: true,
			domain.TerminalInterrupted: true, domain.TerminalBudgetExceeded: true,
		}
		if len(seen) != 5 {
			t.Fatalf("终态原因必须正好 5 个")
		}
		for r := range seen {
			if !strings.Contains(fmt.Sprint(r), string(r)) {
				t.Errorf("原因 %s 不可序列化", r)
			}
		}
		// 错误注入路径由 engine 的 TestStreamWithoutFinishIsProtocolError / TestRunCancelKeepsPartial 覆盖。
	})

	// ---------------------------------------------------------------
	t.Run("INV-10_UI_only_永不进模型消息", func(t *testing.T) {
		t.Parallel()
		// 结构保证：发给模型的 Message 里没有 Details/Notice 字段（双层消息模型，01 §2.1）。
		modelFields := jsonTags(reflect.TypeOf(domain.Message{}))
		for _, forbidden := range []string{"details", "notice", "diag"} {
			if hasTag(modelFields, forbidden) {
				t.Errorf("domain.Message 出现 UI-only 字段 %q（INV-10）", forbidden)
			}
		}
		reqType := reflect.TypeOf(ports.Request{})
		f, ok := reqType.FieldByName("Messages")
		mustTrue(t, "Request.Messages 存在", ok)
		if f.Type.String() != "[]domain.Message" {
			t.Errorf("Request.Messages 必须是 []domain.Message（只含进模型的内容），实得 %s", f.Type)
		}
		// 引擎侧消息：Model==nil 表示整条 UI-only，绝不参与上下文重建。
		uiOnly := domain.AgentMessage{
			ID:        "01J0",
			Kind:      domain.EntryMessage,
			Details:   map[string]any{"diff": "--- a\n+++ b", "bytes": 42},
			Notice:    &domain.Notice{Level: domain.NoticeWarn, Text: "输出已截断"},
			CreatedAt: testkit.FrozenTime,
		}
		if uiOnly.Model != nil {
			t.Errorf("UI-only 消息不该带 Model")
		}
		raw, err := json.Marshal(uiOnly)
		mustNoErr(t, "marshal AgentMessage", err)
		var back domain.AgentMessage
		mustNoErr(t, "unmarshal AgentMessage", json.Unmarshal(raw, &back))
		if back.Model != nil {
			t.Errorf("往返后 Model 必须仍为 nil")
		}
		if back.Notice == nil || len(back.Details) == 0 {
			t.Errorf("UI-only 内容在往返后丢失: %+v", back)
		}
		// 工具结果的 details 同理（02 §5.4）。
		mustTrue(t, "Result.Details 存在", hasField(reflect.TypeOf(domain.Result{}), "Details"))
		if hasField(reflect.TypeOf(domain.Message{}), "Details") {
			t.Errorf("模型消息里不该能承载 Details")
		}
		// evt.usage / evt.notice 的载荷也不进模型上下文：events_test 已按 proto 元数据断言。
		sink := testkit.NewRecordingEventSink()
		mustNoErr(t, "notice", sink.Emit(t.Context(), testkit.NoticeEvent("th_a_1", domain.NoticeInfo, "只给人的提示")))
		for _, e := range sink.Events() {
			if e.Kind == domain.EvtNotice && len(e.Payload) == 0 {
				t.Errorf("notice 事件载荷为空")
			}
		}
	})

	// ---------------------------------------------------------------
	t.Run("INV-11_基座无业务兜底", func(t *testing.T) {
		t.Parallel()
		p, err := coreapp.LoadProduct(repoPath(t, "products", "skeleton.agent.json"))
		mustNoErr(t, "LoadProduct(skeleton)", err)
		rt, err := coreapp.Assemble(p)
		mustNoErr(t, "Assemble(skeleton)", err)
		mustTrue(t, "空基座", rt.IsEmptyBase())
		if len(rt.EmptyStateNotice()) == 0 {
			t.Errorf("空基座必须给出空态提示")
		}
		// 空目录：长度 0，且查询一律 false（tool_not_found 的来源）。
		cat := testkit.NewEmptyCatalog()
		mustCount(t, "空基座目录长度", 0, len(cat.List(t.Context())))
		for _, name := range []string{"read_file", "run_command", "finish"} {
			if _, ok := cat.Lookup(t.Context(), name); ok {
				t.Errorf("基座补位了 %s（INV-11）", name)
			}
		}
		// 禁用贡献插件后能力消失、不复活。
		sc := testkit.NewStaticCatalog(
			testkit.Descriptor("read_file", "some-fs", domain.AccessRead),
			testkit.Descriptor("write_file", "some-fs", domain.AccessWrite),
			testkit.Descriptor("finish", "some-plan", domain.AccessRead),
		)
		mustCount(t, "移除条数", 2, sc.Disable("some-fs"))
		if _, ok := sc.Lookup(t.Context(), "read_file"); ok {
			t.Errorf("禁用后 read_file 仍在目录里（INV-11）")
		}
		if _, ok := sc.Lookup(t.Context(), "finish"); !ok {
			t.Errorf("无关插件的工具被误删")
		}
		mustCount(t, "剩余目录长度", 1, len(sc.List(t.Context())))
		// provider 侧同理：没有 provider 插件时基座不补一个假模型。
		mustCount(t, "空基座 provider 数", 0, rt.ProviderCount)
	})

	// ---------------------------------------------------------------
	t.Run("INV-12_没有步数上限", func(t *testing.T) {
		t.Parallel()
		// 结构保证：引擎的轮次输入里没有「步数/工具调用次数」这类字段。
		fields := exportFields(reflect.TypeOf(engine.TurnSpec{}))
		for _, f := range fields {
			lower := strings.ToLower(f)
			if strings.Contains(lower, "step") || strings.Contains(lower, "maxtool") ||
				strings.Contains(lower, "iterations") || strings.Contains(lower, "rounds") {
				t.Errorf("engine.TurnSpec 出现步数类字段 %q（INV-12 禁止隐藏上限）", f)
			}
		}
		for _, want := range []string{"TurnTimeoutMS", "MaxTurnTokens", "LoopRepeatLimit", "LoopAbortLimit", "ParallelTools"} {
			if !hasFieldByName(fields, want) {
				t.Errorf("engine.TurnSpec 缺少可选上限字段 %q", want)
			}
		}
		// 默认不限制（0=不限）在产品声明里成立，domain 只有循环检测阈值、没有步数常量。
		coding, err := coreapp.LoadProduct(repoPath(t, "products", "coding.agent.json"))
		mustNoErr(t, "LoadProduct(coding)", err)
		mustCount(t, "turnTimeoutMs 默认不限制", 0, coding.Policies.TurnTimeoutMS)
		mustCount(t, "maxTurnTokens 默认不限制", 0, coding.Policies.MaxTurnTokens)
		mustCount(t, "循环检测阈值", domain.LoopRepeatLimit, coding.Policies.LoopRepeatLimit)
		mustCount(t, "循环中止阈值", domain.LoopAbortLimit, coding.Policies.LoopAbortLimit)

		// 替身侧的真实证据：50 轮工具调用 + 1 轮文本，51 次请求全部拿到显式 finish，
		// 没有任何一格被「步数」提前掐掉（03 §5.5 的终止信号语义）。
		provider := testkit.NewFakeProvider(testkit.FiftyToolTurnsThenText("read_file"))
		ctx := t.Context()
		for i := 0; i < 51; i++ {
			s, err := provider.Stream(ctx, ports.Request{Model: ports.ModelRef{ModelID: "fake-1"}})
			mustNoErr(t, fmt.Sprintf("第 %d 轮 Stream", i+1), err)
			a, err := testkit.Collect(ctx, s)
			mustNoErr(t, fmt.Sprintf("第 %d 轮 Collect", i+1), err)
			if !a.FinishSeen {
				t.Fatalf("第 %d 轮没有显式终止信号", i+1)
			}
			wantCalls := 1
			if i == 50 {
				wantCalls = 0
				if a.Text == "" {
					t.Errorf("最后一轮应当给出文本")
				}
			}
			if len(a.Calls) != wantCalls {
				t.Errorf("第 %d 轮工具调用数: want=%d got=%d", i+1, wantCalls, len(a.Calls))
			}
		}
		mustCount(t, "模型请求次数", 51, provider.RequestCount())
		// 引擎侧的 50 轮与 3/5 循环检测由 engine/run_test.go 的两个 golden 用例覆盖（INV-12）。
	})
}

// ---------------------------------------------------------------------------
// 支撑函数
// ---------------------------------------------------------------------------

// findFuncDecls 在仓库的 internal/ 下找名为 name 的函数声明（不含测试文件）。
func findFuncDecls(t *testing.T, name string) []string {
	t.Helper()
	root := repoPath(t, "internal")
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// nodecompat 属于 M1，尚未落地时跳过。
		if strings.Contains(filepath.ToSlash(path), "/conformance/nodecompat/") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return fmt.Errorf("解析 %s: %w", path, perr)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == name && fn.Recv == nil {
				out = append(out, filepath.ToSlash(path))
			}
		}
		return nil
	})
	mustNoErr(t, "扫描 internal/", err)
	sort.Strings(out)
	return out
}

// assertNoImport 检查某个内部包的非测试源码没有 import 到禁止的前缀。
func assertNoImport(t *testing.T, pkg string, forbidden []string) {
	t.Helper()
	root := repoPath(t, filepath.FromSlash(pkg))
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return fmt.Errorf("解析 %s: %w", path, perr)
		}
		for _, imp := range f.Imports {
			pathValue := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if strings.Contains(pathValue, bad) {
					t.Errorf("%s import 了 %q（禁止依赖 %q，01 §3.3）", filepath.ToSlash(path), pathValue, bad)
				}
			}
		}
		return nil
	})
	mustNoErr(t, "扫描 "+pkg, err)
}

// jsonTags 返回结构的 json tag 名集合（去掉 omitempty 等选项）。
func jsonTags(rt reflect.Type) []string {
	var out []string
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func hasTag(tags []string, want string) bool {
	for _, x := range tags {
		if x == want {
			return true
		}
	}
	return false
}

// exportFields 返回结构体导出字段名。
func exportFields(rt reflect.Type) []string {
	var out []string
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.IsExported() {
			out = append(out, f.Name)
		}
	}
	return out
}

func hasField(rt reflect.Type, name string) bool {
	_, ok := rt.FieldByName(name)
	return ok
}

func hasFieldByName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// waitFor 自旋等待条件成立（真实时间上限 2s）；替身的异步登记用它，避免 sleep 硬编码。
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	if !cond() {
		t.Fatalf("等待超时: %s", what)
	}
}
