package toolkit_test

// ToolManager 的契约：owner 归属、按 owner 摘除、可见性视图、快照稳定性与执行路由。

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/toolkit"
)

// recordingInvoker 记录路由到的归属（pluginID/tool）。
type recordingInvoker struct {
	calls []string
	err   error
}

func (r *recordingInvoker) Invoke(_ context.Context, pluginID, tool string, _ json.RawMessage) (domain.Result, error) {
	r.calls = append(r.calls, pluginID+"/"+tool)
	if r.err != nil {
		return domain.Result{}, r.err
	}
	return domain.Result{
		Content: []domain.Part{{Type: domain.PartText, Text: "ok " + tool}},
		Receipt: domain.Receipt{Status: domain.ReceiptOK},
	}, nil
}

func mgrDescriptor(name, pluginID string, discovery bool) domain.Descriptor {
	return domain.Descriptor{
		Name: name, Description: "探针 " + name, Parameters: json.RawMessage(`{"type":"object"}`),
		Access: domain.AccessRead, PluginID: pluginID, Discovery: discovery,
	}
}

func newManager(t *testing.T) (*toolkit.ToolManager, *recordingInvoker) {
	t.Helper()
	inv := &recordingInvoker{}
	m, err := toolkit.NewToolManager(toolkit.NewCatalog(), inv)
	if err != nil {
		t.Fatalf("NewToolManager: %v", err)
	}
	return m, inv
}

// owner 由宿主盖章：缺 owner 或与声明不一致都必须拒绝，否则禁用 A 会摘掉 B 的工具。
func TestManagerStampsOwner(t *testing.T) {
	t.Parallel()
	m, _ := newManager(t)

	if err := m.RegisterOwned("", mgrDescriptor("a", "", false)); err == nil {
		t.Fatal("缺 owner 应被拒绝")
	}
	if err := m.RegisterOwned("tools-fs", mgrDescriptor("a", "tools-shell", false)); err == nil {
		t.Fatal("owner 与声明的 pluginId 不一致应被拒绝")
	}
	if err := m.RegisterOwned("tools-fs", mgrDescriptor("read_file", "", false)); err != nil {
		t.Fatalf("合法注册: %v", err)
	}
	d, ok := m.Lookup(context.Background(), "read_file")
	if !ok || d.PluginID != "tools-fs" {
		t.Fatalf("owner 没有被盖章: %+v / ok=%v", d, ok)
	}
}

// 可见性视图只由描述符自己的声明决定，规则在 domain（不在这里二次判断）。
func TestManagerViews(t *testing.T) {
	t.Parallel()
	m, _ := newManager(t)
	if err := m.RegisterOwned("tools-fs", mgrDescriptor("read_file", "", false)); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterOwned("cron-manager", mgrDescriptor("cron_list", "", true)); err != nil {
		t.Fatal(err)
	}
	find := mgrDescriptor(domain.ToolFindName, "tool-discovery", false)
	find.System = true
	if err := m.RegisterOwned("tool-discovery", find); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	cases := []struct {
		view domain.ToolVisibility
		want []string
	}{
		{domain.ToolVisibleInjected, []string{domain.ToolFindName, "read_file"}},
		{domain.ToolVisibleDiscovery, []string{"cron_list"}},
		{domain.ToolVisibleSystem, []string{domain.ToolFindName}},
		{domain.ToolVisibleAll, []string{"cron_list", domain.ToolFindName, "read_file"}},
	}
	for _, c := range cases {
		got := m.Names(ctx, c.view)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Fatalf("视图 %s = %v，期望 %v", c.view, got, c.want)
		}
	}
}

// 禁用插件按 owner 整体摘除，别的插件不受影响；版本号随之推进。
func TestManagerRemoveOwner(t *testing.T) {
	t.Parallel()
	m, _ := newManager(t)
	for _, n := range []string{"read_file", "write_file"} {
		if err := m.RegisterOwned("tools-fs", mgrDescriptor(n, "", false)); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.RegisterOwned("tools-git", mgrDescriptor("git_status", "", false)); err != nil {
		t.Fatal(err)
	}
	before := m.Version()

	if n := m.RemoveOwner("tools-fs"); n != 2 {
		t.Fatalf("摘除条数 = %d，期望 2", n)
	}
	if m.Version() == before {
		t.Fatal("摘除后版本号应推进")
	}
	for _, name := range []string{"read_file", "write_file"} {
		if _, ok := m.Lookup(context.Background(), name); ok {
			t.Fatalf("%s 应已摘除", name)
		}
	}
	if _, ok := m.Lookup(context.Background(), "git_status"); !ok {
		t.Fatal("别的插件的工具不应被摘掉")
	}
	if n := m.RemoveOwner("no-such-plugin"); n != 0 {
		t.Fatalf("摘除不存在的 owner 应返回 0，实得 %d", n)
	}
}

// 快照是同代不可变副本：取完之后再注册不影响已取到的那一份。
func TestManagerSnapshotStable(t *testing.T) {
	t.Parallel()
	m, _ := newManager(t)
	if err := m.RegisterOwned("tools-fs", mgrDescriptor("read_file", "", false)); err != nil {
		t.Fatal(err)
	}
	snap := m.Snapshot(context.Background())
	version := snap.Version
	if d, ok := snap.Lookup("read_file"); !ok || d.PluginID != "tools-fs" {
		t.Fatalf("快照索引缺失: %+v", snap.Index)
	}

	if err := m.RegisterOwned("cron-manager", mgrDescriptor("cron_list", "", true)); err != nil {
		t.Fatal(err)
	}
	if len(snap.Tools) != 1 {
		t.Fatalf("已取到的快照被后续注册改写: %v", snap.Tools)
	}
	if _, ok := snap.Lookup("cron_list"); ok {
		t.Fatal("新注册的工具不应出现在旧快照里")
	}
	if m.Snapshot(context.Background()).Version == version {
		t.Fatal("新快照的版本号应推进")
	}
}

// 执行路由的归属以目录为准：上层传错 pluginID 时不得投给另一个插件。
func TestManagerInvokeUsesCatalogOwner(t *testing.T) {
	t.Parallel()
	m, inv := newManager(t)
	if err := m.RegisterOwned("tools-fs", mgrDescriptor("read_file", "", false)); err != nil {
		t.Fatal(err)
	}

	res, err := m.Invoke(context.Background(), "tools-fs", "read_file", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.Content[0].Text != "ok read_file" {
		t.Fatalf("结果 = %+v", res)
	}
	if fmt.Sprint(inv.calls) != "[tools-fs/read_file]" {
		t.Fatalf("路由 = %v", inv.calls)
	}

	if _, err := m.Invoke(context.Background(), "tools-git", "read_file", json.RawMessage(`{}`)); err == nil {
		t.Fatal("归属不一致应被拒绝")
	}
	if _, err := m.Invoke(context.Background(), "", "ghost", json.RawMessage(`{}`)); err == nil {
		t.Fatal("未知工具应被拒绝")
	}
	if len(inv.calls) != 1 {
		t.Fatalf("被拒绝的调用不得进入插件路由: %v", inv.calls)
	}
}

// 冻结后注册不生效：Catalog 只记警告，Manager 也不推进版本（快照没有真的换）。
func TestManagerFrozenRegisterIsIgnored(t *testing.T) {
	t.Parallel()
	m, _ := newManager(t)
	if err := m.RegisterOwned("tools-fs", mgrDescriptor("read_file", "", false)); err != nil {
		t.Fatal(err)
	}
	m.Catalog().Freeze()
	before := m.Version()

	if err := m.RegisterOwned("tools-git", mgrDescriptor("git_status", "", false)); err != nil {
		t.Fatalf("冻结后注册只记警告，不返回错误: %v", err)
	}
	if _, ok := m.Lookup(context.Background(), "git_status"); ok {
		t.Fatal("冻结后的注册不应生效")
	}
	if m.Version() != before {
		t.Fatal("冻结后的注册不应推进版本号")
	}
	if len(m.Catalog().Warnings()) == 0 {
		t.Fatal("冻结后注册必须留警告（不静默）")
	}
}
