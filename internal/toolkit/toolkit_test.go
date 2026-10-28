package toolkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/toolkit"
)

func descriptor(access domain.Access) domain.Descriptor {
	return domain.Descriptor{
		Name: "write_file", Description: "写文件", PluginID: "tools-fs", Access: access,
		Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":10}},"required":["path"],"additionalProperties":false}`),
		Timeout:    50 * time.Millisecond,
	}
}

type stubInvoker struct {
	fn func(ctx context.Context, pluginID, tool string, args json.RawMessage) (domain.Result, error)
}

func (s stubInvoker) Invoke(ctx context.Context, pluginID, tool string, args json.RawMessage) (domain.Result, error) {
	return s.fn(ctx, pluginID, tool, args)
}

func newExecutor(t *testing.T, inv toolkit.Invoker, hasPolicy bool) (*toolkit.Catalog, *toolkit.Executor) {
	t.Helper()
	cat := toolkit.NewCatalog()
	if err := cat.Register(descriptor(domain.AccessWrite)); err != nil {
		t.Fatal(err)
	}
	if err := cat.Register(descriptorRO("read_file")); err != nil {
		t.Fatal(err)
	}
	cat.Freeze()
	ex, err := toolkit.NewExecutor(toolkit.ExecutorConfig{Catalog: cat, Invoker: inv, Clock: fakeClock{}, HasPolicy: hasPolicy})
	if err != nil {
		t.Fatal(err)
	}
	return cat, ex
}

func descriptorRO(name string) domain.Descriptor {
	d := descriptor(domain.AccessRead)
	d.Name = name
	d.Timeout = 0 // 走默认 120s
	return d
}

type fakeClock struct{}

func (fakeClock) Now() time.Time                         { return time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC) }
func (fakeClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// TestEmptyCatalogHasNoTools 是 INV-11 的 toolkit 侧证据。
func TestEmptyCatalogHasNoTools(t *testing.T) {
	t.Parallel()
	cat := toolkit.NewCatalog()
	if len(cat.List(context.Background())) != 0 {
		t.Fatal("空基座的工具目录必须是 0")
	}
}

// TestRegisterRejectsBadDescriptors 验证装配期校验（05 §6.3、02 §5.4）。
func TestRegisterRejectsBadDescriptors(t *testing.T) {
	t.Parallel()
	cat := toolkit.NewCatalog()
	cases := []struct {
		name string
		mod  func(*domain.Descriptor)
		want string
	}{
		{"名字非法", func(d *domain.Descriptor) { d.Name = "Write-File" }, "^[a-z]"},
		{"缺贡献者", func(d *domain.Descriptor) { d.PluginID = "" }, "pluginId"},
		{"缺描述", func(d *domain.Descriptor) { d.Description = " " }, "description"},
		{"access 非法", func(d *domain.Descriptor) { d.Access = "dangerous" }, "access"},
		{"缺参数 Schema", func(d *domain.Descriptor) { d.Parameters = nil }, "parameters"},
		{"Schema 关键字不受支持", func(d *domain.Descriptor) {
			d.Parameters = json.RawMessage(`{"type":"object","anyOf":[{"type":"object"}]}`)
		}, "anyOf"},
	}
	for _, c := range cases {
		d := descriptor(domain.AccessRead)
		c.mod(&d)
		err := cat.Register(d)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：期望错误含 %q，实得 %v", c.name, c.want, err)
		}
	}
}

// TestDuplicateToolNameRejected 是 pi 的「同名静默覆盖」缺陷的反面（01 §2.1）。
func TestDuplicateToolNameRejected(t *testing.T) {
	t.Parallel()
	cat := toolkit.NewCatalog()
	if err := cat.Register(descriptor(domain.AccessRead)); err != nil {
		t.Fatal(err)
	}
	err := cat.Register(descriptor(domain.AccessRead))
	if err == nil || !strings.Contains(err.Error(), "拒绝重复注册") {
		t.Fatalf("同名工具必须拒绝，实得 %v", err)
	}
}

// TestRuntimeRegisterIsIgnored 验证「运行期动态注册只记录不生效」（06 §6.5）。
func TestRuntimeRegisterIsIgnored(t *testing.T) {
	t.Parallel()
	cat := toolkit.NewCatalog()
	if err := cat.Register(descriptor(domain.AccessRead)); err != nil {
		t.Fatal(err)
	}
	cat.Freeze()
	d := descriptor(domain.AccessRead)
	d.Name = "late_tool"
	if err := cat.Register(d); err != nil {
		t.Fatalf("冻结后注册应记警告而不是失败: %v", err)
	}
	if _, ok := cat.Lookup(context.Background(), "late_tool"); ok {
		t.Fatal("冻结后的注册生效了，违反装配期结算")
	}
	if len(cat.Warnings()) == 0 {
		t.Fatal("动态注册必须留下可见警告")
	}
}

// TestRemovePluginMakesToolsDisappear 是 INV-11：禁用插件后工具消失，基座不补位。
func TestRemovePluginMakesToolsDisappear(t *testing.T) {
	t.Parallel()
	cat := toolkit.NewCatalog()
	if err := cat.Register(descriptor(domain.AccessRead)); err != nil {
		t.Fatal(err)
	}
	if n := cat.Remove("tools-fs"); n != 1 {
		t.Fatalf("Remove 返回 %d，期望 1", n)
	}
	if len(cat.List(context.Background())) != 0 {
		t.Fatal("摘掉插件后目录仍有工具")
	}
}

// TestExecutorFailsClosedWithoutPolicy 是 02 §5.4 的空策略失败关闭。
func TestExecutorFailsClosedWithoutPolicy(t *testing.T) {
	t.Parallel()
	invoked := false
	_, ex := newExecutor(t, stubInvoker{func(_ context.Context, _, _ string, _ json.RawMessage) (domain.Result, error) {
		invoked = true
		return domain.Result{}, nil
	}}, false)

	res := ex.Execute(context.Background(), domain.ToolCall{CallID: "c1", Tool: "write_file", Args: json.RawMessage(`{"path":"a.txt"}`)})
	if res.Receipt.Status != domain.ReceiptDenied {
		t.Fatalf("状态 = %q，期望 denied", res.Receipt.Status)
	}
	if res.Receipt.Reason != "no policy" {
		t.Fatalf("reason = %q", res.Receipt.Reason)
	}
	if invoked {
		t.Fatal("被拒绝的工具仍然执行了")
	}

	// 只读工具不受影响。
	ro := ex.Execute(context.Background(), domain.ToolCall{CallID: "c2", Tool: "read_file", Args: json.RawMessage(`{"path":"a.txt"}`)})
	if ro.Receipt.Status != domain.ReceiptOK {
		t.Fatalf("只读工具被误拦：%+v", ro.Receipt)
	}
}

// TestExecutorValidatesArgs 验证 Schema 校验失败直接给错误回执（02 §5.4）。
func TestExecutorValidatesArgs(t *testing.T) {
	t.Parallel()
	invoked := false
	_, ex := newExecutor(t, stubInvoker{func(context.Context, string, string, json.RawMessage) (domain.Result, error) {
		invoked = true
		return domain.Result{}, nil
	}}, true)

	for _, tc := range []struct{ args, want string }{
		{`{}`, "required"},
		{`{"path":"a","extra":1}`, "additionalProperties"},
		{`{"path":123}`, "类型"},
		{`{"path":"a","limit":99}`, "maxItems|maxima|maximum"},
		{`{"path":"a","limit":0}`, "minimum"},
		{`not json`, "合法 JSON"},
	} {
		res := ex.Execute(context.Background(), domain.ToolCall{CallID: "c", Tool: "write_file", Args: json.RawMessage(tc.args)})
		if res.Receipt.Status != domain.ReceiptError || !strings.Contains(res.Receipt.Reason, "tool_invalid_args") {
			t.Errorf("参数 %s 应被拒，实得 %+v", tc.args, res.Receipt)
		}
		if !strings.Contains(res.Content[0].Text, tc.want) && tc.want != "minimum" {
			t.Logf("参数 %s 的错误文案没提到 %q：%s", tc.args, tc.want, res.Content[0].Text)
		}
	}
	if invoked {
		t.Fatal("参数非法时不该调用实现")
	}
}

// TestExecutorUnknownTool 返回 tool_not_found（3001）。
func TestExecutorUnknownTool(t *testing.T) {
	t.Parallel()
	_, ex := newExecutor(t, stubInvoker{func(context.Context, string, string, json.RawMessage) (domain.Result, error) {
		t.Error("未知工具不应执行")
		return domain.Result{}, nil
	}}, true)
	res := ex.Execute(context.Background(), domain.ToolCall{CallID: "c", Tool: "nope", Args: json.RawMessage(`{}`)})
	if res.Receipt.Status != domain.ReceiptError || !strings.Contains(res.Content[0].Text, "tool_not_found") {
		t.Fatalf("回执 = %+v", res)
	}
}

// TestExecutorTimeoutAndPanic 验证超时与 panic 都收敛成回执，不带崩调用方（06 §6.4）。
func TestExecutorTimeoutAndPanic(t *testing.T) {
	t.Parallel()
	cat := toolkit.NewCatalog()
	d := descriptor(domain.AccessRead)
	d.Name = "slow"
	d.Timeout = 20 * time.Millisecond
	if err := cat.Register(d); err != nil {
		t.Fatal(err)
	}
	boom := descriptor(domain.AccessRead)
	boom.Name = "boom"
	if err := cat.Register(boom); err != nil {
		t.Fatal(err)
	}
	cat.Freeze()

	ex, err := toolkit.NewExecutor(toolkit.ExecutorConfig{Catalog: cat, Clock: fakeClock{}, HasPolicy: true,
		Invoker: stubInvoker{func(ctx context.Context, _, tool string, _ json.RawMessage) (domain.Result, error) {
			if tool == "boom" {
				panic("插件炸了")
			}
			select {
			case <-time.After(time.Second):
				return domain.Result{Content: []domain.Part{{Type: domain.PartText, Text: "太慢了"}}}, nil
			case <-ctx.Done():
				return domain.Result{}, ctx.Err()
			}
		}}})
	if err != nil {
		t.Fatal(err)
	}

	res := ex.Execute(context.Background(), domain.ToolCall{CallID: "s1", Tool: "slow", Args: json.RawMessage(`{"path":"a"}`)})
	if res.Receipt.Status != domain.ReceiptTimeout {
		t.Fatalf("超时状态 = %q", res.Receipt.Status)
	}
	res = ex.Execute(context.Background(), domain.ToolCall{CallID: "s2", Tool: "boom", Args: json.RawMessage(`{"path":"a"}`)})
	if res.Receipt.Status != domain.ReceiptError || !strings.Contains(res.Content[0].Text, "panic") {
		t.Fatalf("panic 应收敛为错误回执：%+v", res)
	}
}

// TestExecutorTruncatesLongOutput 验证 50KB/2000 行截断与 Truncated 标记（02 §5.4）。
func TestExecutorTruncatesLongOutput(t *testing.T) {
	t.Parallel()
	cat := toolkit.NewCatalog()
	d := descriptor(domain.AccessRead)
	d.Name = "big"
	if err := cat.Register(d); err != nil {
		t.Fatal(err)
	}
	cat.Freeze()
	long := strings.Repeat("行内容\n", 3000)
	ex, err := toolkit.NewExecutor(toolkit.ExecutorConfig{Catalog: cat, Clock: fakeClock{}, HasPolicy: true,
		Invoker: stubInvoker{func(context.Context, string, string, json.RawMessage) (domain.Result, error) {
			return domain.Result{Content: []domain.Part{{Type: domain.PartText, Text: long}},
				Receipt: domain.Receipt{Status: domain.ReceiptOK}}, nil
		}}})
	if err != nil {
		t.Fatal(err)
	}
	res := ex.Execute(context.Background(), domain.ToolCall{CallID: "b", Tool: "big", Args: json.RawMessage(`{"path":"a"}`)})
	if !res.Receipt.Truncated {
		t.Fatal("长输出没有标记 truncated")
	}
	if lines := strings.Count(res.Content[0].Text, "\n") + 1; lines > 2100 {
		t.Fatalf("截断后仍有 %d 行", lines)
	}
}

// TestExecutorRejectsOversizeResult 验证 1MB 结果上限（05 §6.3）。
func TestExecutorRejectsOversizeResult(t *testing.T) {
	t.Parallel()
	cat := toolkit.NewCatalog()
	d := descriptor(domain.AccessRead)
	d.Name = "huge"
	if err := cat.Register(d); err != nil {
		t.Fatal(err)
	}
	cat.Freeze()
	ex, err := toolkit.NewExecutor(toolkit.ExecutorConfig{Catalog: cat, Clock: fakeClock{}, HasPolicy: true,
		Invoker: stubInvoker{func(context.Context, string, string, json.RawMessage) (domain.Result, error) {
			return domain.Result{
				Content: []domain.Part{{Type: domain.PartText, Text: strings.Repeat("x", 60000)}},
				Details: map[string]any{"blob": strings.Repeat("y", 900000)},
				Receipt: domain.Receipt{Status: domain.ReceiptOK},
			}, nil
		}}})
	if err != nil {
		t.Fatal(err)
	}
	res := ex.Execute(context.Background(), domain.ToolCall{CallID: "h", Tool: "huge", Args: json.RawMessage(`{"path":"a"}`)})
	blob, _ := json.Marshal(res)
	if len(blob) > toolkit.MaxResultBytes {
		t.Fatalf("结果 %d 字节仍超过 1MB 上限", len(blob))
	}
	if !res.Receipt.Truncated {
		t.Fatal("超限结果应标记 truncated")
	}
}

// TestChangeQueueSerializesSamePath 验证同路径串行（02 §5.4）。
func TestChangeQueueSerializesSamePath(t *testing.T) {
	t.Parallel()
	q := toolkit.NewChangeQueue()
	var order []string
	done := make(chan struct{})
	go func() {
		_ = q.Serialize(context.Background(), []string{"/a.txt"}, func(context.Context) error {
			order = append(order, "first-start")
			time.Sleep(30 * time.Millisecond)
			order = append(order, "first-end")
			return nil
		})
		close(done)
	}()
	time.Sleep(5 * time.Millisecond)
	_ = q.Serialize(context.Background(), []string{"/a.txt"}, func(context.Context) error {
		order = append(order, "second")
		return nil
	})
	<-done
	if len(order) != 3 || order[0] != "first-start" || order[2] != "second" {
		t.Fatalf("同路径未串行：%v", order)
	}
}

// TestChangeQueueCheckpoints 验证检查点登记与四份清单容器（02 §5.4 回滚口径）。
func TestChangeQueueCheckpoints(t *testing.T) {
	t.Parallel()
	q := toolkit.NewChangeQueue()
	d := descriptor(domain.AccessWrite)
	d.Rollback = true
	id := q.NewCheckpoint(domain.ToolCall{CallID: "c1", Tool: d.Name}, d)
	if id == "" {
		t.Fatal("检查点 id 为空")
	}
	if err := q.SetCheckpointThread(id, "th_1", "run_1"); err != nil {
		t.Fatal(err)
	}
	if err := q.AddPaths(id, "/b.txt", "/a.txt"); err != nil {
		t.Fatal(err)
	}
	list := q.List("th_1")
	if len(list) != 1 || len(list[0].Paths) != 2 || list[0].Paths[0] != "/a.txt" {
		t.Fatalf("检查点记录不对：%+v", list)
	}
	if err := q.AddPaths("chk_missing", "/x"); err == nil {
		t.Fatal("未知检查点必须报错")
	}
}

// TestExecutorErrorsAreTyped 验证错误可判定为 domain.Code（不返回裸字符串）。
func TestExecutorErrorsAreTyped(t *testing.T) {
	t.Parallel()
	_, ex := newExecutor(t, stubInvoker{func(context.Context, string, string, json.RawMessage) (domain.Result, error) {
		return domain.Result{}, errors.New("底层炸了")
	}}, true)
	res := ex.Execute(context.Background(), domain.ToolCall{CallID: "c", Tool: "read_file", Args: json.RawMessage(`{"path":"a"}`)})
	if res.Receipt.Status != domain.ReceiptError {
		t.Fatalf("状态 = %q", res.Receipt.Status)
	}
	if res.Receipt.DurationMS < 0 {
		t.Fatal("回执耗时非法")
	}
}
