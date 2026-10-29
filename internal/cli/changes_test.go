package cli_test

// M5 检查点账目与回滚验收：真实装配（官方插件 × moejs × SQLite）+
// 带回滚的写工具 → 持久化检查点 → change.list → change.revert 四份清单。
// 回滚语义以 02 §5.4 为准：恢复/删除/跳过/失效，不谎报成功。

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/adapters/sqlite"
	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/plugins/official"
)

// ckptFixture 是测试夹具：builtin 信任、fs.write 能力、rollback=true 的写工具。
// 走 ctx.fs.writeText——与官方文件工具同一条写入路径（检查点在覆盖前登记）。
const ckptFixtureManifest = `{
  "id": "test-ckpt",
  "name": "Checkpoint Fixture",
  "version": "0.1.0",
  "apiVersion": 1,
  "description": "测试夹具：带回滚的写工具",
  "trust": "builtin",
  "backend": { "entry": "index.ts", "capabilities": ["fs.read", "fs.write"] }
}`

const ckptFixtureSource = `export default function (agent) {
  agent.registerTool({
    name: "ckpt_write",
    description: "写入文件（rollback=true，测试检查点）",
    access: "write",
    rollback: true,
    parameters: {
      type: "object",
      properties: { path: { type: "string" }, content: { type: "string" } },
      required: ["path", "content"],
    },
    async execute(args, ctx) {
      await ctx.fs.writeText(args.path, args.content);
      return { content: [{ type: "text", text: "written " + args.path }] };
    },
  });
}
`

// ckptProduct 写一份测试产品声明：headless 的插件集合 + test-ckpt 夹具
// （产品声明就是「启用这些插件」的承诺，夹具插件必须显式声明才会被装配）。
func ckptProduct(t *testing.T) string {
	t.Helper()
	raw := `{
  "id": "ckpt-test",
  "name": "Checkpoint Fixture Product",
  "plugins": ["provider-openai-chat", "tools-fs", "tools-shell", "tools-plan", "policy-default", "test-ckpt"],
  "capabilities": { "skills": false, "images": true, "subagents": false },
  "identity": { "name": "ada-go", "locale": "zh-CN" },
  "policies": { "toolTimeoutMs": 120000, "turnTimeoutMs": 0, "maxTurnTokens": 0, "loopRepeatLimit": 3, "loopAbortLimit": 5, "parallelTools": 1 }
}`
	path := filepath.Join(t.TempDir(), "ckpt.agent.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ckptSession 打开一个带夹具插件的会话；policy-default 用 auto 模式（写工具免询问）。
// steps 是逐轮的模型脚本（每一轮一次 ckpt_write 或文本收尾）。
func ckptSession(t *testing.T, workspace, dataDir string, steps ...[]testkit.ScriptStep) *cli.Session {
	t.Helper()
	base, err := official.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := plugins.LoadManifest([]byte(ckptFixtureManifest))
	if err != nil {
		t.Fatal(err)
	}
	fixture := host.Script{
		Manifest:  manifest,
		EntryName: "plugin://test-ckpt/index.ts",
		Source:    ckptFixtureSource,
	}
	model := testkit.NewFakeProvider(testkit.NewScriptedModel(steps...))
	cfg := cli.Config{
		Workspace:   workspace,
		DataDir:     dataDir,
		Product:     ckptProduct(t),
		Scripts:     append(base, fixture),
		Settings:    map[string]map[string]any{"policy-default": {"mode": "auto"}},
		ModelClient: model,
		In:          strings.NewReader(""),
		Out:         io.Discard,
	}
	sess, err := cli.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	return sess
}

// listAudit 供测试断言审计行（只读连接）。
func listAudit(t *testing.T, dataDir string, limit int) []sqlite.AuditRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := sqlite.OpenReadOnly(ctx, sqlite.Config{DataDir: dataDir})
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer r.Close()
	rows, err := r.ListAudit(ctx, limit, time.Time{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	return rows
}

func auditKinds(rows []sqlite.AuditRecord) map[string]int {
	out := map[string]int{}
	for _, r := range rows {
		out[r.Kind]++
	}
	return out
}

// TestCheckpointRevertRestoresFile 是主链路：写文件 → checkpoint → 二次修改 →
// revert 恢复首次内容；storage.restore 落审计；回滚后 change.list 不再列出。
func TestCheckpointRevertRestoresFile(t *testing.T) {
	ws := t.TempDir()
	data := t.TempDir()
	aTxt := filepath.Join(ws, "a.txt")
	if err := os.WriteFile(aTxt, []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := ckptSession(t, ws, data,
		testkit.ToolTurn("call_1", "ckpt_write", `{"path":"/workspace/a.txt","content":"v2\n"}`),
		testkit.TextTurn("done"),
	)
	ctx := context.Background()
	if term, err := sess.Run(ctx, "写 a.txt"); err != nil || term != "completed" {
		t.Fatalf("Run = %s / %v", term, err)
	}
	if got, err := os.ReadFile(aTxt); err != nil || string(got) != "v2\n" {
		t.Fatalf("第一次写后 a.txt = %q / %v", got, err)
	}
	threadID := sess.CurrentThread()

	page, err := sess.Changes(threadID, 50, "")
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if len(page.ChangeSets) != 1 {
		t.Fatalf("change.list = %d 条，期望 1：%+v", len(page.ChangeSets), page.ChangeSets)
	}
	cs := page.ChangeSets[0]
	if cs.ID != "chk_call_1" || cs.Tool != "ckpt_write" || cs.ThreadID != threadID {
		t.Fatalf("ChangeSet 字段不对: %+v", cs)
	}
	if len(cs.Paths) != 1 || cs.Paths[0] != "a.txt" {
		t.Fatalf("ChangeSet.paths = %v，期望 [a.txt]", cs.Paths)
	}
	if cs.ItemKey != "tool:call_1" {
		t.Fatalf("ChangeSet.itemKey = %q，期望 tool:call_1", cs.ItemKey)
	}

	res, err := sess.Revert(ctx, cli.RevertRequest{ThreadID: threadID, Scope: "item", CheckpointID: "chk_call_1"})
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if len(res.Restored) != 1 || res.Restored[0] != "a.txt" ||
		len(res.Deleted) != 0 || len(res.Invalidated) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("Revert 四份清单不对: %+v", res)
	}
	if got, err := os.ReadFile(aTxt); err != nil || string(got) != "v1\n" {
		t.Fatalf("回滚后 a.txt = %q / %v，期望 v1", got, err)
	}
	// 回滚前的当前内容要有 revert-<ts> 备份（防二次回滚丢数据）。
	matches, _ := filepath.Glob(filepath.Join(data, "changes", "chk_call_1", "revert-*", "*.bak"))
	if len(matches) == 0 {
		t.Fatalf("没有 revert-<ts> 备份目录")
	}
	// 审计：storage.restore 落库。
	kinds := auditKinds(listAudit(t, data, 100))
	if kinds["storage.restore"] != 1 {
		t.Fatalf("audit kinds = %v，期望恰好 1 条 storage.restore", kinds)
	}
	// 回滚后 change.list 不再列出，重复回滚被拒绝。
	page, err = sess.Changes(threadID, 50, "")
	if err != nil || len(page.ChangeSets) != 0 {
		t.Fatalf("回滚后 change.list = %+v / %v，期望空", page.ChangeSets, err)
	}
	if _, err := sess.Revert(ctx, cli.RevertRequest{ThreadID: threadID, Scope: "item", CheckpointID: "chk_call_1"}); err == nil {
		t.Fatal("重复回滚应当被拒绝")
	}
}

// TestCheckpointRevertNewFileDeletes：工具新建的文件回滚 = 删除（deleted）。
func TestCheckpointRevertNewFileDeletes(t *testing.T) {
	ws := t.TempDir()
	data := t.TempDir()
	newFile := filepath.Join(ws, "created.txt")
	sess := ckptSession(t, ws, data,
		testkit.ToolTurn("call_n1", "ckpt_write", `{"path":"/workspace/created.txt","content":"new"}`),
		testkit.TextTurn("done"),
	)
	ctx := context.Background()
	if term, err := sess.Run(ctx, "建文件"); err != nil || term != "completed" {
		t.Fatalf("Run = %s / %v", term, err)
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("工具应当已创建文件: %v", err)
	}
	res, err := sess.Revert(ctx, cli.RevertRequest{ThreadID: sess.CurrentThread(), Scope: "item", CheckpointID: "chk_call_n1"})
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "created.txt" || len(res.Restored) != 0 {
		t.Fatalf("四份清单不对: %+v", res)
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Fatalf("回滚后文件应当被删除，stat = %v", err)
	}
}

// TestCheckpointRevertMissingBackupInvalidated：备份文件丢失 → invalidated（不谎报）。
func TestCheckpointRevertMissingBackupInvalidated(t *testing.T) {
	ws := t.TempDir()
	data := t.TempDir()
	aTxt := filepath.Join(ws, "a.txt")
	if err := os.WriteFile(aTxt, []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := ckptSession(t, ws, data,
		testkit.ToolTurn("call_m1", "ckpt_write", `{"path":"/workspace/a.txt","content":"v2\n"}`),
		testkit.TextTurn("done"),
	)
	ctx := context.Background()
	if term, err := sess.Run(ctx, "写"); err != nil || term != "completed" {
		t.Fatalf("Run = %s / %v", term, err)
	}
	// 把备份内容清掉，模拟数据目录损坏/被清理。
	backup, _ := filepath.Glob(filepath.Join(data, "changes", "chk_call_m1", "files", "*.bak"))
	if len(backup) != 1 {
		t.Fatalf("期望恰好 1 份备份，拿到 %v", backup)
	}
	if err := os.WriteFile(backup[0], nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// 完全删除备份文件（读不到 → invalidated）。
	if err := os.Remove(backup[0]); err != nil {
		t.Fatal(err)
	}
	res, err := sess.Revert(ctx, cli.RevertRequest{ThreadID: sess.CurrentThread(), Scope: "item", CheckpointID: "chk_call_m1"})
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if len(res.Invalidated) != 1 || res.Invalidated[0] != "a.txt" || len(res.Restored) != 0 {
		t.Fatalf("四份清单不对: %+v", res)
	}
	if got, _ := os.ReadFile(aTxt); string(got) != "v2\n" {
		t.Fatalf("失效的路径不应被改动，a.txt = %q", got)
	}
}

// TestAuditPluginSetEnabledAndReload：plugin.setEnabled / plugin.reload 写审计（09 §10）。
func TestAuditPluginSetEnabledAndReload(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t, "")
	sess, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close(ctx)
	if _, err := sess.SetPluginEnabled(ctx, "tools-git", false); err != nil {
		t.Fatalf("SetPluginEnabled: %v", err)
	}
	if _, err := sess.ReloadPlugins(ctx, ""); err != nil {
		t.Fatalf("ReloadPlugins: %v", err)
	}
	kinds := auditKinds(listAudit(t, cfg.DataDir, 100))
	if kinds["plugin.changed"] != 1 {
		t.Fatalf("audit kinds = %v，期望 1 条 plugin.changed", kinds)
	}
	if kinds["plugin.reload"] != 1 {
		t.Fatalf("audit kinds = %v，期望 1 条 plugin.reload", kinds)
	}
}

// TestChangeListFilterAndPaging：线程过滤 + limit/cursor 分页（最新在前）。
func TestChangeListFilterAndPaging(t *testing.T) {
	ws := t.TempDir()
	data := t.TempDir()
	sess := ckptSession(t, ws, data,
		testkit.ToolTurn("call_p1", "ckpt_write", `{"path":"/workspace/p1.txt","content":"1"}`),
		testkit.TextTurn("done"),
		testkit.ToolTurn("call_p2", "ckpt_write", `{"path":"/workspace/p2.txt","content":"2"}`),
		testkit.TextTurn("done"),
		testkit.ToolTurn("call_p3", "ckpt_write", `{"path":"/workspace/p3.txt","content":"3"}`),
		testkit.TextTurn("done"),
	)
	ctx := context.Background()
	for _, prompt := range []string{"p1", "p2", "p3"} {
		if term, err := sess.Run(ctx, prompt); err != nil || term != "completed" {
			t.Fatalf("Run(%s) = %s / %v", prompt, term, err)
		}
	}
	threadID := sess.CurrentThread()

	page, err := sess.Changes(threadID, 2, "")
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if len(page.ChangeSets) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("第一页 = %+v", page)
	}
	if page.ChangeSets[0].ID != "chk_call_p3" || page.ChangeSets[1].ID != "chk_call_p2" {
		t.Fatalf("分页应当最新在前: %v", page.ChangeSets)
	}
	page2, err := sess.Changes(threadID, 2, page.NextCursor)
	if err != nil {
		t.Fatalf("Changes(2): %v", err)
	}
	if len(page2.ChangeSets) != 1 || page2.HasMore || page2.ChangeSets[0].ID != "chk_call_p1" {
		t.Fatalf("第二页 = %+v", page2)
	}
	// 全工作区口径（threadId 缺省）同样能看到 3 条。
	all, err := sess.Changes("", 50, "")
	if err != nil || len(all.ChangeSets) != 3 {
		t.Fatalf("全工作区 change.list = %d / %v", len(all.ChangeSets), err)
	}
	// 回滚最新一笔（item），另外两笔仍在。
	if _, err := sess.Revert(ctx, cli.RevertRequest{ThreadID: threadID, Scope: "item", CheckpointID: "chk_call_p3"}); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	page3, _ := sess.Changes(threadID, 50, "")
	if len(page3.ChangeSets) != 2 {
		t.Fatalf("回滚后剩 %d 条，期望 2", len(page3.ChangeSets))
	}
	// scope=all：回滚线程内全部（含新建文件的删除）。
	res, err := sess.Revert(ctx, cli.RevertRequest{ThreadID: threadID, Scope: "all"})
	if err != nil {
		t.Fatalf("Revert(all): %v", err)
	}
	if len(res.Deleted) != 2 { // p1/p2 都是新建文件
		t.Fatalf("all 回滚 deleted = %v，期望 2 条", res.Deleted)
	}
	// scope=file：路径过滤（chk_call_p3 已回滚，不可再用；file 找不到时报错）。
	if _, err := sess.Revert(ctx, cli.RevertRequest{ThreadID: threadID, Scope: "file", Path: "/workspace/p1.txt"}); err == nil {
		t.Fatal("全部回滚后 file 回滚应当报错（没有可回滚的检查点）")
	}
}
