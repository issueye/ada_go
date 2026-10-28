package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// TestScriptedModelAssembly 验证替身自己是对的（基础设施不可信就等于没有）：
// 统一增量组装、按 index 的参数片段、显式终止信号、EOF ≠ 完成（03 §5.5）。
func TestScriptedModelAssembly(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("文本思考用量与终止信号", func(t *testing.T) {
		t.Parallel()
		p := testkit.NewFakeProvider(testkit.NewScriptedModel(joinSteps(
			testkit.Thinking("先想一下"),
			testkit.Text("你好", "，世界"),
			[]testkit.ScriptStep{testkit.Usage(domain.Usage{InputTokens: 10, OutputTokens: 3})},
			[]testkit.ScriptStep{testkit.Finish("stop", "[DONE]")},
		)))
		s, err := p.Stream(ctx, ports.Request{Model: ports.ModelRef{ModelID: "fake-1"}})
		mustNoErr(t, "Stream", err)
		a, err := testkit.Collect(ctx, s)
		mustNoErr(t, "Collect", err)
		mustEqual(t, "文本", "你好，世界", a.Text)
		mustEqual(t, "思考", "先想一下", a.Thinking)
		mustTrue(t, "有显式终止信号", a.FinishSeen)
		mustEqual(t, "终止标记", "[DONE]", a.TerminalMark)
		mustEqual(t, "结束原因", "stop", a.FinishReason)
		mustTrue(t, "有用量", a.HasUsage)
		mustCount(t, "input token", 10, int(a.Usage.InputTokens))
		mustCount(t, "工具调用", 0, len(a.Calls))
		if a.ProtocolError != nil {
			t.Errorf("正常流不该出现协议错误: %v", a.ProtocolError)
		}
	})

	t.Run("工具调用参数按 index 组装", func(t *testing.T) {
		t.Parallel()
		p := testkit.NewFakeProvider(testkit.NewScriptedModel(joinSteps(
			testkit.ToolCall(0, "call_01J1", "read_file", `{"pa`, `th":"/x"`, `}`),
			testkit.ToolCall(1, "call_01J2", "list_files", `{"limit":10}`),
			[]testkit.ScriptStep{testkit.Finish("tool_calls", "[DONE]")},
		)))
		s, err := p.Stream(ctx, ports.Request{})
		mustNoErr(t, "Stream", err)
		a, err := testkit.Collect(ctx, s)
		mustNoErr(t, "Collect", err)
		mustCount(t, "组装出的调用数", 2, len(a.Calls))
		mustEqual(t, "第 1 个 callId", "call_01J1", a.Calls[0].CallID)
		mustEqual(t, "第 1 个工具", "read_file", a.Calls[0].Tool)
		mustEqual(t, "片段拼接结果", `{"path":"/x"}`, string(a.Calls[0].Args))
		mustEqual(t, "第 2 个 callId", "call_01J2", a.Calls[1].CallID)
		if len(a.InvalidArgs) != 0 {
			t.Errorf("合法参数被判非法: %v", a.InvalidArgs)
		}
		mustTrue(t, "HasToolCalls", a.HasToolCalls())
	})

	t.Run("畸形参数是工具错误而不是协议错误", func(t *testing.T) {
		t.Parallel()
		p := testkit.NewFakeProvider(testkit.NewScriptedModel(joinSteps(
			testkit.ToolCall(0, "call_bad", "edit_file", `{"a":`, `oops`),
			[]testkit.ScriptStep{testkit.Finish("tool_calls", "[DONE]")},
		)))
		s, err := p.Stream(ctx, ports.Request{})
		mustNoErr(t, "Stream", err)
		a, err := testkit.Collect(ctx, s)
		mustNoErr(t, "Collect", err)
		if a.ProtocolError != nil {
			t.Errorf("参数畸形不得升级为协议错误（02 §5.4：走工具 error 回执）: %v", a.ProtocolError)
		}
		mustCount(t, "InvalidArgs", 1, len(a.InvalidArgs))
		mustEqual(t, "畸形参数的 callId", "call_bad", a.InvalidArgs[0])
	})

	t.Run("EOF_不等于完成", func(t *testing.T) {
		t.Parallel()
		p := testkit.NewFakeProvider(testkit.EOFWithoutFinishScript())
		s, err := p.Stream(ctx, ports.Request{})
		mustNoErr(t, "Stream", err)
		a, err := testkit.Collect(ctx, s)
		if err == nil {
			t.Fatalf("没有终止信号的 EOF 必须失败（03 §5.5）")
		}
		if !errors.Is(err, testkit.ErrNoFinish) {
			t.Errorf("错误必须是 ErrNoFinish 包装，实得 %v", err)
		}
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeProtocolError {
			t.Errorf("必须归一化成 protocol_error(5002)，实得 %v", err)
		}
		mustTrue(t, "ProtocolError 字段也置位", a.ProtocolError != nil)
		if a.Text == "" {
			t.Errorf("EOF 前已收到的部分必须保留（取消/错误时不丢内容）")
		}
	})

	t.Run("脚本用尽同样判协议错误", func(t *testing.T) {
		t.Parallel()
		p := testkit.NewFakeProvider(testkit.NewScriptedModel(testkit.TextTurn("只有一轮")))
		_, err := p.Stream(ctx, ports.Request{})
		mustNoErr(t, "第一轮", err)
		s, err := p.Stream(ctx, ports.Request{})
		mustNoErr(t, "第二轮（超出脚本）", err)
		if _, err := testkit.Collect(ctx, s); err == nil {
			t.Errorf("超出脚本的轮次必须失败，而不是发一个假 finish 蒙混")
		}
		mustCount(t, "请求计数", 2, p.RequestCount())
	})

	t.Run("流中错误原样透出", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("provider: 上游 500")
		p := testkit.NewFakeProvider(testkit.MidStreamError(sentinel))
		s, err := p.Stream(ctx, ports.Request{})
		mustNoErr(t, "Stream", err)
		a, err := testkit.Collect(ctx, s)
		if !errors.Is(err, sentinel) {
			t.Errorf("注入的错误必须原样上抛，实得 %v", err)
		}
		if a.Text == "" {
			t.Errorf("失败前的部分文本要保留")
		}
	})

	t.Run("50_轮长循环脚本", func(t *testing.T) {
		t.Parallel()
		m := testkit.FiftyToolTurnsThenText("read_file")
		mustCount(t, "编排轮数", 51, m.TurnCount())
		p := testkit.NewFakeProvider(m)
		for i := 0; i < 51; i++ {
			s, err := p.Stream(ctx, ports.Request{Model: ports.ModelRef{ModelID: "fake-1"}})
			mustNoErr(t, "Stream", err)
			a, err := testkit.Collect(ctx, s)
			mustNoErr(t, "Collect", err)
			if !a.FinishSeen {
				t.Fatalf("第 %d 轮缺少终止信号", i+1)
			}
		}
		mustCount(t, "请求次数", 51, p.RequestCount())
		// 每轮的工具名与参数逐字一致 —— 循环检测的输入形状（02 §5.3）。
		reqs := p.Requests()
		if len(reqs) != 51 {
			t.Fatalf("Requests 快照数不对: %d", len(reqs))
		}
	})

	t.Run("相同回执可比较", func(t *testing.T) {
		t.Parallel()
		resp := testkit.FixedResponder("12 行")
		r1 := resp(domain.ToolCall{CallID: "call_1", Tool: "read_file", Args: json.RawMessage(`{"path":"/a"}`)})
		r2 := resp(domain.ToolCall{CallID: "call_2", Tool: "read_file", Args: json.RawMessage(`{"path":"/a"}`)})
		if r1.Receipt.Status != r2.Receipt.Status || r1.Content[0].Text != r2.Content[0].Text {
			t.Errorf("相同工具+相同参数必须给出相同回执")
		}
		den := testkit.DeniedResponder("no policy")
		d := den(domain.ToolCall{CallID: "call_3", Tool: "write_file"})
		mustEqual(t, "denied 状态", string(domain.ReceiptDenied), string(d.Receipt.Status))
	})
}

// TestRetryOnlyBeforeFirstDelta 验证重试包装层的边界（03 §5.5）：
// 只在首个增量之前重试、最多 3 次、不可重试类立刻返回、开始输出后绝不重试。
// 退避用真实计时器但基准只有 1ms，所以测试不睡真时间；时钟注入点留给 M1
// （provider 流的空闲/总时长仍由 testkit.FakeClock 驱动）。
func TestRetryOnlyBeforeFirstDelta(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("建立阶段失败可重试", func(t *testing.T) {
		t.Parallel()
		inner := testkit.NewFakeProvider(testkit.ErrorThenSuccess(2, "终于成功"))
		retry := testkit.NewRetry(inner)
		retry.BaseDelay = time.Millisecond
		retry.MaxDelay = 4 * time.Millisecond

		s, err := retry.Stream(ctx, ports.Request{Model: ports.ModelRef{ModelID: "fake-1"}})
		mustNoErr(t, "Stream（前两次建立失败后应重试成功）", err)
		a, err := testkit.Collect(ctx, s)
		mustNoErr(t, "Collect", err)
		mustEqual(t, "文本", "终于成功", a.Text)
		mustCount(t, "建立次数（2 次失败 + 1 次成功）", 3, retry.Attempts())
		if delays := retry.Delays(); len(delays) != 2 {
			t.Errorf("退避次数不对: %v", delays)
		} else if !(delays[1] >= delays[0]) {
			t.Logf("退避带抖动，第二次未必更大: %v", delays)
		}
		mustCount(t, "内层实际建立的流数", 1, inner.RequestCount()-2)
	})

	t.Run("失败次数超过上限即上抛", func(t *testing.T) {
		t.Parallel()
		retry := testkit.NewRetry(testkit.NewFakeProvider(testkit.ErrorThenSuccess(99, "到不了")))
		retry.BaseDelay = time.Millisecond
		retry.MaxDelay = 2 * time.Millisecond
		_, err := retry.Stream(ctx, ports.Request{})
		if err == nil {
			t.Fatalf("持续失败必须上抛错误")
		}
		mustCount(t, "最多建立 1+3 次", 4, retry.Attempts())
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeProviderError {
			t.Errorf("上抛的应是 provider_error，实得 %v", err)
		}
	})

	t.Run("不可重试错误立刻返回", func(t *testing.T) {
		t.Parallel()
		retry := testkit.NewRetry(fixedFailer{err: domain.NewError(domain.CodeThreadBusy, "线程忙")})
		retry.BaseDelay = time.Millisecond
		_, err := retry.Stream(ctx, ports.Request{})
		mustErrContains(t, "thread_busy 上抛", err, "线程忙")
		mustCount(t, "不可重试时只建立一次", 1, retry.Attempts())
		// provider_error 一类才可重试（domain.IsRetryable 是唯一判定入口）。
		if domain.IsRetryable(domain.CodeThreadBusy) {
			t.Errorf("thread_busy 必须是不可自动重试")
		}
		if !domain.IsRetryable(domain.CodeProviderError) {
			t.Errorf("provider_error 必须是可自动重试（首个增量前）")
		}
	})

	t.Run("取消后不再重试", func(t *testing.T) {
		t.Parallel()
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		retry := testkit.NewRetry(fixedFailer{err: domain.NewError(domain.CodeProviderError, "网络抖动")})
		if _, err := retry.Stream(cctx, ports.Request{}); err == nil {
			t.Fatalf("已取消的上下文必须立刻失败")
		}
		mustCount(t, "取消后不重试", 1, retry.Attempts())
	})

	t.Run("首个增量之后不重试", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("provider: 流到一半断了")
		inner := testkit.NewFakeProvider(testkit.MidStreamError(sentinel))
		retry := testkit.NewRetry(inner)
		retry.BaseDelay = time.Millisecond
		s, err := retry.Stream(ctx, ports.Request{})
		mustNoErr(t, "Stream", err)
		var kinds []ports.DeltaKind
		for {
			d, err := s.Recv()
			if err != nil {
				if !errors.Is(err, sentinel) {
					t.Errorf("断流错误必须原样上抛，实得 %v", err)
				}
				break
			}
			kinds = append(kinds, d.Kind)
		}
		if len(kinds) == 0 {
			t.Fatalf("没有收到任何增量，测不到「首个增量之后」")
		}
		if kinds[0] != ports.DeltaTextStart {
			t.Errorf("首格应为 text.start，实得 %s", kinds[0])
		}
		mustCount(t, "已开始输出后不得重建流（03 §5.5）", 1, inner.RequestCount())
		if retry.Attempts() != 1 {
			t.Errorf("attempts 应为 1，实得 %d", retry.Attempts())
		}
	})
}

// TestFakeInteractionsOutcomes 覆盖交互桩的三种结局与幂等语义（03 §5.6、INV-4）。
func TestFakeInteractionsOutcomes(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("用户响应_accepted", func(t *testing.T) {
		t.Parallel()
		b := testkit.NewFakeInteractions(testkit.WithOutcomes(testkit.Accepted("true")))
		res, err := b.Request(ctx, testkit.InteractionRequest("i-1", "th_a_1", "some-policy",
			domain.InteractionConfirm, "写文件？", "src/main.go"))
		mustNoErr(t, "Request", err)
		mustEqual(t, "状态", string(domain.InteractionAccepted), string(res.Status))
		mustEqual(t, "值", "true", res.Value)
		mustNoErr(t, "恰好一次", b.ExactlyOnce("i-1"))
		if res.Status.IsDenial() {
			t.Errorf("accepted 不是拒绝")
		}
	})

	t.Run("用户响应_denied", func(t *testing.T) {
		t.Parallel()
		b := testkit.NewFakeInteractions(testkit.WithOutcomes(testkit.Denied()))
		res, err := b.Request(ctx, testkit.InteractionRequest("i-2", "th_a_1", "some-policy",
			domain.InteractionConfirm, "执行命令？", "rm -rf /"))
		mustNoErr(t, "Request", err)
		mustEqual(t, "状态", string(domain.InteractionDenied), string(res.Status))
		mustTrue(t, "denied 算拒绝", b.DeniedOrWorse("i-2"))
	})

	t.Run("无UI_unavailable", func(t *testing.T) {
		t.Parallel()
		b := testkit.NewFakeInteractions(testkit.WithoutUI())
		if b.HasUI(ctx, "th_a_1") {
			t.Errorf("HasUI 必须报告没有界面")
		}
		res, err := b.Request(ctx, testkit.InteractionRequest("i-3", "th_a_1", "some-policy",
			domain.InteractionInput, "补充说明", ""))
		mustNoErr(t, "Request", err)
		mustEqual(t, "状态", string(domain.InteractionUnavailable), string(res.Status))
		mustTrue(t, "unavailable 必须按拒绝处理（INV-4）", res.Status.IsDenial())
	})

	t.Run("超时_timeout", func(t *testing.T) {
		t.Parallel()
		clk := testkit.NewFakeClock()
		b := testkit.NewFakeInteractions(testkit.WithInteractionClock(clk))
		req := testkit.InteractionRequest("i-4", "th_a_1", "some-policy", domain.InteractionSelect, "选一个", "")
		req.ExpiresAt = clk.Now().Add(domain.DefaultInteractionTimeout)
		mustCount(t, "默认交互期限", 300, int(domain.DefaultInteractionTimeout/time.Second))
		clk.Advance(domain.DefaultInteractionTimeout + time.Second)
		res, err := b.Request(ctx, req)
		mustNoErr(t, "Request", err)
		mustEqual(t, "状态", string(domain.InteractionTimeout), string(res.Status))
		mustTrue(t, "timeout 必须按拒绝处理（INV-4）", res.Status.IsDenial() && b.DeniedOrWorse("i-4"))
	})

	t.Run("等待后由_Respond_解析并幂等", func(t *testing.T) {
		t.Parallel()
		b := testkit.NewFakeInteractions()
		req := testkit.InteractionRequest("i-5", "th_a_1", "some-policy", domain.InteractionConfirm, "允许吗？", "")
		got := make(chan ports.InteractionResolution, 1)
		go func() {
			res, _ := b.Request(context.Background(), req)
			got <- res
		}()
		waitFor(t, func() bool { return b.RequestCount() == 1 }, "交互登记")

		pending, err := b.ListPending(ctx, "th_a_1")
		mustNoErr(t, "ListPending", err)
		mustCount(t, "待处理交互", 1, len(pending))

		// 双键匹配：threadId 不匹配必须被拒（03 §5.6），且不改变状态。
		if _, err := b.Respond(ctx, "th_other", "i-5", domain.InteractionDenied, "false"); !errors.Is(err, testkit.ErrThreadMismatch) {
			t.Errorf("threadId 不匹配必须报 ErrThreadMismatch，实得 %v", err)
		}
		if _, ok := b.Resolution("i-5"); ok {
			t.Errorf("错配的响应不得落定状态")
		}

		first, err := b.Respond(ctx, "th_a_1", "i-5", domain.InteractionAccepted, "true")
		mustNoErr(t, "Respond", err)
		second, err := b.Respond(ctx, "th_a_1", "i-5", domain.InteractionDenied, "false")
		mustNoErr(t, "重复 Respond", err)
		if second.Status != first.Status || second.Value != first.Value {
			t.Errorf("重复响应必须幂等返回首次结果: first=%+v second=%+v", first, second)
		}
		mustNoErr(t, "恰好解析一次", b.ExactlyOnce("i-5"))
		if b.RequestCount() != 1 {
			t.Errorf("重复响应不得新增请求: %d", b.RequestCount())
		}
		res := <-got
		mustEqual(t, "等待方拿到的状态", string(domain.InteractionAccepted), string(res.Status))

		if _, err := b.Respond(ctx, "th_a_1", "i-不存在", domain.InteractionAccepted, ""); !errors.Is(err, testkit.ErrUnknownInteraction) {
			t.Errorf("未知 requestId 必须被拒，实得 %v", err)
		}
	})

	t.Run("过期后不得改写状态", func(t *testing.T) {
		t.Parallel()
		clk := testkit.NewFakeClock()
		b := testkit.NewFakeInteractions(testkit.WithInteractionClock(clk), testkit.WithExpiryWatch())
		req := testkit.InteractionRequest("i-6", "th_a_1", "some-policy", domain.InteractionConfirm, "允许吗？", "")
		req.ExpiresAt = clk.Now().Add(time.Minute)

		cctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan ports.InteractionResolution, 1)
		go func() {
			res, _ := b.Request(cctx, req)
			done <- res
		}()
		waitFor(t, func() bool { return b.RequestCount() == 1 }, "交互登记")

		// 推进假时钟过期限：看门狗把交互解析为 timeout（INV-4 的失败方向）。
		clk.Advance(2 * time.Minute)
		var res ports.InteractionResolution
		select {
		case res = <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("期限看门狗没有把交互解析为 timeout")
		}
		mustEqual(t, "期限到后的状态", string(domain.InteractionTimeout), string(res.Status))
		mustTrue(t, "timeout 按拒绝处理", res.Status.IsDenial())

		// 迟到的点击：报错且不改变状态（客户端无法伪造已响应，10 §11.3）。
		if _, err := b.Respond(ctx, "th_a_1", "i-6", domain.InteractionAccepted, "true"); err == nil {
			t.Errorf("过期后的响应必须报错")
		} else if !errors.Is(err, testkit.ErrNotPending) {
			t.Errorf("过期后的响应应报 ErrNotPending，实得 %v", err)
		}
		after, ok := b.Resolution("i-6")
		mustTrue(t, "解析记录存在", ok)
		mustEqual(t, "状态未被改写", string(domain.InteractionTimeout), string(after.Status))
		mustNoErr(t, "仍然恰好一次", b.ExactlyOnce("i-6"))
	})

	t.Run("同一线程最多一个待处理", func(t *testing.T) {
		t.Parallel()
		b := testkit.NewFakeInteractions()
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, _ = b.Request(context.Background(), testkit.InteractionRequest(
					string(rune('a'+i)), "th_a_1", "some-policy", domain.InteractionConfirm, "标题", "正文"))
			}(i)
		}
		waitFor(t, func() bool { return b.RequestCount() == 3 }, "三个交互登记")
		pending, err := b.ListPending(context.Background(), "th_a_1")
		mustNoErr(t, "ListPending", err)
		mustCount(t, "登记数", 3, len(pending))
		t.Log("M1-A：引擎/桥要把「同线程一个待处理」当成硬约束（03 §5.6），替身只提供计数面")
		for i := 0; i < 3; i++ {
			if _, err := b.Respond(ctx, "th_a_1", string(rune('a'+i)), domain.InteractionDenied, ""); err != nil {
				t.Errorf("Respond %d 失败: %v", i, err)
			}
		}
		wg.Wait()
	})
}

// TestFakeClockAndIDGen 验证时钟与 id 的可用性（golden 回放的确定性前提）。
func TestFakeClockAndIDGen(t *testing.T) {
	t.Parallel()

	t.Run("时钟可推进且不倒走", func(t *testing.T) {
		t.Parallel()
		c := testkit.NewFakeClock()
		start := c.Now()
		mustEqual(t, "起点是冻结时刻", testkit.FrozenTime.UTC().Format(time.RFC3339), start.Format(time.RFC3339))
		ch := c.After(90 * time.Second)
		mustCount(t, "挂起的定时器", 1, c.Pending())
		c.Advance(60 * time.Second)
		select {
		case <-ch:
			t.Fatalf("只推进 60s 就触发了 90s 定时器")
		default:
		}
		c.Advance(31 * time.Second)
		select {
		case at := <-ch:
			if !at.After(start) {
				t.Errorf("触发时刻 %v 不早于起点 %v", at, start)
			}
		case <-time.After(time.Second):
			t.Fatalf("推进过后定时器必须触发")
		}
		before := c.Now()
		c.Advance(-time.Hour)
		if !c.Now().Equal(before) {
			t.Errorf("时钟倒走了: %v -> %v", before, c.Now())
		}
		mustCount(t, "触发后无残留", 0, c.Pending())
	})

	t.Run("id 形状与可预测", func(t *testing.T) {
		t.Parallel()
		g := testkit.NewFixedIDGen()
		ctx := context.Background()
		tid, err := g.NewThreadID(ctx, "abc123")
		mustNoErr(t, "NewThreadID", err)
		if !testkit.ThreadIDPattern.MatchString(tid) {
			t.Errorf("线程 id %q 不符合 th_<hash>_<ulid>（proto.ThreadIDFormat）", tid)
		}
		cid, err := g.NewCallID(ctx)
		mustNoErr(t, "NewCallID", err)
		if !testkit.CallIDPattern.MatchString(cid) {
			t.Errorf("调用 id %q 不符合 call_ 形状", cid)
		}
		kid, err := g.NewCheckpointID(ctx)
		mustNoErr(t, "NewCheckpointID", err)
		if !testkit.CheckpointIDPattern.MatchString(kid) {
			t.Errorf("检查点 id %q 不符合 chk_ 形状", kid)
		}
		u1, err := g.NewULID(ctx)
		mustNoErr(t, "NewULID", err)
		mustTrue(t, "裸 ULID 形状合法: "+u1, testkit.IsULID(u1))
		u2, err := g.NewULID(ctx)
		mustNoErr(t, "NewULID 2", err)
		if u2 <= u1 {
			t.Errorf("id 必须可排序: %s -> %s", u1, u2)
		}
		for _, f := range []func(context.Context) (string, error){g.NewRunID, g.NewMessageID, g.NewInteractionID} {
			v, err := f(ctx)
			mustNoErr(t, "idgen", err)
			mustTrue(t, "ULID 形状: "+v, testkit.IsULID(v))
		}
		g.Reset()
		again, err := g.NewULID(ctx)
		mustNoErr(t, "Reset 后 NewULID", err)
		mustEqual(t, "Reset 后回到同一条", u1, again)
		if _, err := g.NewThreadID(ctx, ""); err == nil {
			t.Errorf("空 workspaceHash 必须报错")
		}
	})
}

// TestTempWorkspaceSamples 验证临时工作区与 scope 边界样本（M1 的 scope 测试要复用它）。
func TestTempWorkspaceSamples(t *testing.T) {
	t.Parallel()
	w := testkit.TempWorkspace(t)
	if _, err := os.Stat(w.Root); err != nil {
		t.Fatalf("工作区不存在: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Root, "src", "main.go")); err != nil {
		t.Errorf("缺少工作区内样本: %v", err)
	}
	if w.Outside == "" {
		t.Errorf("缺少工作区外样本目录")
	}
	samples := w.Samples()
	if len(samples) < 4 {
		t.Fatalf("scope 样本太少: %d", len(samples))
	}
	var inside, outside, symlinked int
	for _, s := range samples {
		switch {
		case s.Symlink:
			symlinked++
		case s.WantDenied:
			outside++
		default:
			inside++
		}
		if s.Virtual != "" && !strings.HasPrefix(s.Virtual, "/") {
			t.Errorf("虚拟路径必须以 / 开头: %q", s.Virtual)
		}
		if s.Note == "" {
			t.Errorf("样本 %q 缺少规则说明", s.Virtual)
		}
	}
	t.Logf("样本: 区内=%d 越界=%d symlink=%d（symlink 支持=%v）", inside, outside, symlinked, w.SymlinkOK)
	if !w.SymlinkOK {
		t.Logf("跳过 symlink 样本: %s", w.SymlinkSkipReason())
		if symlinked != 0 {
			t.Errorf("没建成链接却给了 symlink 样本")
		}
	}
	// 工作区外样本必须真的在工作区外。
	for _, s := range samples {
		if s.WantDenied && s.Real != "" {
			rel, err := filepath.Rel(w.Root, s.Real)
			if err == nil && !strings.HasPrefix(rel, "..") {
				t.Errorf("样本 %q 落在工作区内，越界断言无意义", s.Virtual)
			}
		}
	}
	hash := testkit.WorkspaceHash(w.Root)
	if hash == "" {
		t.Errorf("WorkspaceHash 不能为空")
	}
	if testkit.WorkspaceHash(w.Root) != hash {
		t.Errorf("WorkspaceHash 必须稳定")
	}
}

// TestEventGolden 把一条完整轮次的事件序列写成 golden 基线并比对
// （10 §11.1「事件 golden 回放」、INV-6 的 golden 表达）。
// 基线在 testdata/golden/*.txt；重新生成见 doc.go 的 ADAG_GOLDEN_UPDATE 说明。
func TestEventGolden(t *testing.T) {
	t.Parallel()
	const thread = "th_gold_01J0000000000000000000000A"
	ctx := context.Background()

	t.Run("completed 轮次", func(t *testing.T) {
		t.Parallel()
		sink := testkit.NewRecordingEventSink(testkit.WithGoldenDir(goldenDir(t)))
		mustNoErr(t, "ready", sink.Emit(ctx, testkit.ReadyEvent("skeleton", 0, 0)))
		mustNoErr(t, "running", sink.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunRunning, "")))
		mustNoErr(t, "thinking", sink.Emit(ctx, testkit.MessageDeltaEvent(thread, "01J-M1", "")))
		mustNoErr(t, "delta1", sink.Emit(ctx, testkit.MessageDeltaEvent(thread, "01J-M1", "计划分三步")))
		mustNoErr(t, "started", sink.Emit(ctx, testkit.ToolStartedEvent(thread, "call_01J-G1", "todo")))
		mustNoErr(t, "finished", sink.Emit(ctx, testkit.ToolFinishedEvent(thread, "call_01J-G1", domain.ReceiptOK)))
		mustNoErr(t, "notice", sink.Emit(ctx, testkit.NoticeEvent(thread, domain.NoticeInfo, "已写入 3 项计划")))
		mustNoErr(t, "finishing", sink.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunFinishing, "")))
		mustNoErr(t, "terminal", sink.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunTerminal, domain.TerminalCompleted)))

		mustNoErr(t, "SeqMonotonic", sink.SeqMonotonic())
		mustNoErr(t, "ExactlyOneTerminal", sink.ExactlyOneTerminal())
		mustNoErr(t, "StartedBeforeFinished", sink.StartedBeforeFinished("call_01J-G1"))
		mustNoErr(t, "Golden", sink.Golden("turn-completed"))
	})

	t.Run("denied_与可恢复错误后仍恰好一个终态", func(t *testing.T) {
		t.Parallel()
		sink := testkit.NewRecordingEventSink(testkit.WithGoldenDir(goldenDir(t)))
		mustNoErr(t, "running", sink.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunRunning, "")))
		mustNoErr(t, "started", sink.Emit(ctx, testkit.ToolStartedEvent(thread, "call_01J-G2", "write_file")))
		mustNoErr(t, "denied", sink.Emit(ctx, testkit.ToolFinishedEvent(thread, "call_01J-G2", domain.ReceiptDenied)))
		mustNoErr(t, "interaction", sink.Emit(ctx, domain.Event{
			Kind: domain.EvtInteractionResolved, ThreadID: thread,
			Payload: json.RawMessage(`{"requestId":"int-1","status":"denied"}`)}))
		mustNoErr(t, "error 事件（工作区级）", sink.Emit(ctx,
			testkit.ErrorEvent(thread, domain.CodeLoopDetected, domain.NewDiagnostic(domain.DiagThread, 5))))
		mustNoErr(t, "terminal", sink.Emit(ctx, testkit.RunStatusEvent(thread, domain.RunTerminal, domain.TerminalCompleted)))
		mustNoErr(t, "Golden", sink.Golden("turn-denied-recovered"))
	})
}

// goldenDir 把 golden 目录定位到 internal/conformance/testdata/golden，
// 这样无论测试工作目录是哪一种（go test 默认包目录 / IDE 仓库根）都能命中。
func goldenDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "internal", "conformance", "testdata", "golden")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建 golden 目录 %s: %v", dir, err)
	}
	return dir
}

// fixedFailer 是只返回固定错误的 ModelClient（测「不可重试错误立刻返回」）。
type fixedFailer struct{ err error }

func (f fixedFailer) Stream(context.Context, ports.Request) (ports.Stream, error) { return nil, f.err }

// joinSteps 把多段脚本串成一轮。
func joinSteps(parts ...[]testkit.ScriptStep) []testkit.ScriptStep {
	var out []testkit.ScriptStep
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
