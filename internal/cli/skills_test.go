package cli_test

// 技能的会话侧验收（6.7）：deps.Skills / deps.Prompts 注入、name/description/path
// 注入系统提示、read_file 按 path 读取正文，以及工作区覆盖插件技能的冲突语义。
// 全部走真实装配（官方插件 × moejs × SQLite × 引擎）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/ports"
	"github.com/adag/ada-go/plugins/official"
)

// codingProductPath 返回 coding 产品声明（capabilities.skills=true，10 插件）。
func codingProductPath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "products", "coding.agent.json")
}

// openWithProduct 按产品声明打开会话；model 为 nil 时用 fake 回显模型。
func openWithProduct(t *testing.T, productPath, ws string, model ports.ModelClient) *cli.Session {
	t.Helper()
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	var out strings.Builder
	sess, err := cli.Open(context.Background(), cli.Config{
		Workspace:   ws,
		DataDir:     t.TempDir(),
		Product:     productPath,
		Scripts:     scripts,
		Assembler:   app.Assembler{},
		ModelClient: model,
		In:          strings.NewReader(""),
		Out:         &out,
	})
	if err != nil {
		t.Fatalf("cli.Open(%s): %v", filepath.Base(productPath), err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	return sess
}

// writeWorkspaceSkill 在 <ws>/.adago/skills/<name>/SKILL.md 写一个技能夹具。
func writeWorkspaceSkill(t *testing.T, ws, name, description, body string) {
	t.Helper()
	dir := filepath.Join(ws, ".adago", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSessionCodingSkillsInjection 钉 coding 产品（skills=true）的注入语义：
//   - 系统提示 = 安全前言（最前）+ persona 片段 + 技能元数据 XML（含绝对 path）；
//   - 模型按 path 调用 read_file，工具结果里拿到正文；
//   - 技能表 = 插件贡献（clean-code）+ 工作区技能。
func TestSessionCodingSkillsInjection(t *testing.T) {
	ws := t.TempDir()
	writeWorkspaceSkill(t, ws, "release-checklist", "发版前的检查清单", "1. 跑全量测试\n2. 打 tag\n")
	skillPath := filepath.Join(ws, ".adago", "skills", "release-checklist", "SKILL.md")

	fake := testkit.NewFakeProvider(testkit.NewScriptedModel(
		testkit.ToolTurn("call_1", "read_file", fmt.Sprintf(`{"path":%q}`, skillPath)),
		testkit.TextTurn("已按清单执行"),
	))
	sess := openWithProduct(t, codingProductPath(t), ws, fake)
	ctx := context.Background()
	if term, err := sess.Run(ctx, "按发版清单走一遍"); err != nil || term != domain.TerminalCompleted {
		t.Fatalf("Run = %s / %v", term, err)
	}

	reqs := fake.Requests()
	if len(reqs) < 2 {
		t.Fatalf("应有两次模型请求（工具轮 + 收尾轮），得到 %d", len(reqs))
	}
	system := reqs[0].System
	// 安全前言在最前。
	if !strings.HasPrefix(system, "安全边界（不可移除") {
		t.Fatalf("系统提示必须以安全前言开头: %.80s", system)
	}
	// persona 片段注入（来自 persona-coding 的 registerPrompt）。
	if !strings.Contains(system, "先读后改") {
		t.Fatal("系统提示缺少 persona-coding 的提示词片段")
	}
	// .adago 归属写进不可移除的安全前言。
	if !strings.Contains(system, ".adago/") || !strings.Contains(system, "宿主与插件") {
		t.Fatalf("安全前言缺少 .adago 归属说明: %.400s", system)
	}
	if strings.Index(system, "安全边界") > strings.Index(system, "先读后改") {
		t.Fatal("安全前言必须在 persona 片段之前")
	}
	// 技能元数据注入（插件技能 + 工作区技能），且带绝对 path。
	for _, want := range []string{`<skill name="clean-code"`, `<skill name="release-checklist"`, `path="`} {
		if !strings.Contains(system, want) {
			t.Fatalf("系统提示缺少技能元数据 %s：%.400s", want, system)
		}
	}
	if !strings.Contains(system, "release-checklist") || !strings.Contains(system, skillPath) {
		t.Fatalf("系统提示缺少工作区技能的绝对路径 %q：%.400s", skillPath, system)
	}
	// 第二次请求的历史里有 read_file 的工具结果：正文是 frontmatter 之后的内容。
	var toolText string
	for _, m := range reqs[1].Messages {
		if m.Role != domain.RoleTool {
			continue
		}
		for _, p := range m.Parts {
			if p.Type == domain.PartText {
				toolText += p.Text
			}
		}
	}
	if !strings.Contains(toolText, "跑全量测试") || !strings.Contains(toolText, "description:") {
		t.Fatalf("read_file 应返回 SKILL.md 原文（含 frontmatter）: %q", toolText)
	}
}

// TestSessionSkillWorkspaceOverridesPlugin 钉冲突语义：同名技能工作区优先并记录冲突。
func TestSessionSkillWorkspaceOverridesPlugin(t *testing.T) {
	ws := t.TempDir()
	// 与 persona-coding 随包技能同名：工作区版本应胜出。
	writeWorkspaceSkill(t, ws, "clean-code", "工作区覆盖版的改动纪律", "工作区正文")

	sess := openWithProduct(t, codingProductPath(t), ws, nil)
	metas, conflicts := sess.SkillSummaries()
	var found *domain.SkillMeta
	for i := range metas {
		if metas[i].Name == "clean-code" {
			found = &metas[i]
		}
	}
	if found == nil {
		t.Fatalf("技能表缺少 clean-code: %+v", metas)
	}
	if found.Origin != host.SkillOriginWorkspace {
		t.Fatalf("同名技能应 origin=workspace: %+v", found)
	}
	if !strings.Contains(found.Description, "工作区覆盖版") {
		t.Fatalf("应取工作区的 description: %+v", found)
	}
	hasConflict := false
	for _, c := range conflicts {
		if c == "clean-code" {
			hasConflict = true
		}
	}
	if !hasConflict {
		t.Fatalf("冲突列表应包含 clean-code: %v", conflicts)
	}
	// path/正文也是工作区版本。
	body, err := os.ReadFile(found.Path)
	if err != nil || !strings.Contains(string(body), "工作区正文") {
		t.Fatalf("工作区技能 path 应可读且是工作区版本: %q / %v", body, err)
	}
}

// TestSessionHeadlessSkillReadable：skills=true 且 plugin-helper 带随包技能后，
// 系统提示注入 name/description/path，模型可按 path 读正文。
func TestSessionHeadlessSkillReadable(t *testing.T) {
	ws := t.TempDir()
	fake := testkit.NewFakeProvider(testkit.NewScriptedModel(testkit.TextTurn("好的")))
	sess := openWithProduct(t, productPath(t), ws, fake)
	ctx := context.Background()
	if term, err := sess.Run(ctx, "hello"); err != nil || term != domain.TerminalCompleted {
		t.Fatalf("Run = %s / %v", term, err)
	}
	reqs := fake.Requests()
	if len(reqs) == 0 {
		t.Fatal("没有模型请求")
	}
	if !strings.Contains(reqs[0].System, "可用技能") || !strings.Contains(reqs[0].System, "plugin-manual") {
		t.Fatalf("系统提示应注入 plugin-manual 技能简介: %.400s", reqs[0].System)
	}
	metas, _ := sess.SkillSummaries()
	var skill *domain.SkillMeta
	for i := range metas {
		if metas[i].Name == "plugin-manual" {
			skill = &metas[i]
		}
	}
	if skill == nil || !filepath.IsAbs(skill.Path) {
		t.Fatalf("plugin-manual 应带绝对 path: %+v", skill)
	}
	if !strings.Contains(reqs[0].System, skill.Path) {
		t.Fatalf("系统提示缺少 plugin-manual 的 path %q: %.400s", skill.Path, reqs[0].System)
	}
	body, err := os.ReadFile(skill.Path)
	if err != nil || !strings.Contains(string(body), "create_plugin") {
		t.Fatalf("按 path 读取 plugin-manual 正文 = %q / %v", body, err)
	}
	// headless 声明了 tools-skill：搜索入口来自该插件。
	if _, ok := sess.Assembly().ToolCatalog().Lookup(ctx, "skill_search"); !ok {
		t.Fatal("headless 声明 tools-skill 后应装配 skill_search")
	}
}
