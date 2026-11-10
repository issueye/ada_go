package app_test

// M3/M5 官方插件的行为验收：tools-git / tools-ask / tools-skill / persona-coding，
// 以及 coding 产品（skills=true，13 插件）的装配门控。全部走真实装配。

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/plugins/official"
)

// codingProduct 装配 coding 产品（skills=true，13 插件；前端插件由 Gateway 侧装配）。
func codingProduct(t *testing.T) *app.Assembly {
	return codingProductIn(t, tempDir(t))
}

// codingProductIn 用指定工作区装配 coding 产品（工作区可放 .adago/skills 夹具）。
func codingProductIn(t *testing.T, ws string) *app.Assembly {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "products", "coding.agent.json"))
	if err != nil {
		t.Fatalf("读取 coding 产品声明: %v", err)
	}
	p, err := coreapp.ParseProduct(raw)
	if err != nil {
		t.Fatalf("ParseProduct: %v", err)
	}
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	frontend, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	frontendIDs := map[string]bool{}
	for _, sc := range frontend {
		if sc.Manifest != nil {
			frontendIDs[sc.Manifest.ID] = true
		}
	}
	bridge, err := host.NewHTTPBridge(host.HTTPBridgeConfig{})
	if err != nil {
		t.Fatalf("NewHTTPBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })
	asm, err := app.Assemble(context.Background(), p, scripts, app.Deps{
		Context:     host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}, HTTP: bridge},
		FrontendIDs: frontendIDs, Settings: map[string]map[string]any{},
	})
	if err != nil {
		t.Fatalf("app.Assemble(coding): %v", err)
	}
	t.Cleanup(func() { _ = asm.Close(context.Background()) })
	return asm
}

// TestCodingProductAssembles 钉 coding 产品的装配：17 个后端实例 + 8 个前端声明
// （provider-admin/token-usage/composer-telemetry 同时有后端与页面/插槽，
// plugin-helper 只有后端（对话创建）），tools-skill 贡献 skill_search，
// persona-coding 的提示词片段已结算。
func TestCodingProductAssembles(t *testing.T) {
	asm := codingProduct(t)

	if len(asm.Plugins) != 16 {
		t.Fatalf("后端插件数 = %d，期望 16（provider-admin/env-manager/cron-manager/token-usage/composer-telemetry 的后端也装配；前端插件走 Gateway）: %v",
			len(asm.Plugins), pluginIDsOf(asm))
	}
	wantFrontend := []string{"tools-plan", "tools-ask", "provider-admin", "token-usage", "ui-charts", "env-manager", "cron-manager", "composer-telemetry"}
	if len(asm.FrontendDecls) != len(wantFrontend) {
		t.Fatalf("前端声明 = %v，期望 %v", asm.FrontendDecls, wantFrontend)
	}
	for i, id := range wantFrontend {
		if asm.FrontendDecls[i] != id {
			t.Fatalf("前端声明[%d] = %q，期望 %q（全部：%v）", i, asm.FrontendDecls[i], id, asm.FrontendDecls)
		}
	}
	if _, ok := asm.Tools.Lookup(context.Background(), "skill_search"); !ok {
		t.Fatalf("coding 产品（skills=true）必须有 tools-skill 贡献的 skill_search，现有 %v", toolNames(asm))
	}
	for _, name := range []string{"git_status", "git_diff", "git_log", "ask_user", "chart_guide"} {
		if _, ok := asm.Tools.Lookup(context.Background(), name); !ok {
			t.Fatalf("缺少工具 %s，现有 %v", name, toolNames(asm))
		}
	}
	// persona-coding 的提示词贡献已结算（内容、优先级）。
	var persona *host.PromptDef
	for _, inst := range asm.Plugins {
		if inst.ID() != "persona-coding" {
			continue
		}
		c := inst.Contribution()
		if len(c.Prompts) != 1 {
			t.Fatalf("persona-coding 应贡献 1 段提示词: %+v", c.Prompts)
		}
		persona = &c.Prompts[0]
	}
	if persona == nil {
		t.Fatal("coding 产品里没有 persona-coding 实例")
	}
	if persona.Priority != 10 || !strings.Contains(persona.Content, "先读后改") || !strings.Contains(persona.Content, "AGENTS.md") {
		t.Fatalf("persona 片段形状不对: %+v", persona)
	}
	// persona-coding 随包技能进入贡献（origin=plugin）。
	if inst, ok := asm.Plugin("persona-coding"); ok {
		skills := inst.Contribution().Skills
		if len(skills) != 1 || skills[0].Name != "clean-code" || skills[0].Origin != "plugin" {
			t.Fatalf("persona-coding 的随包技能 = %+v", skills)
		}
	}
	// ui-charts 的随包技能进入贡献（origin=plugin）。
	if inst, ok := asm.Plugin("ui-charts"); ok {
		skills := inst.Contribution().Skills
		if len(skills) != 1 || skills[0].Name != "charts-render" || skills[0].Origin != "plugin" {
			t.Fatalf("ui-charts 的随包技能 = %+v", skills)
		}
	}
}

// TestSkillsExposeReadablePaths 钉 6.7 的新契约：装配期只暴露 name/description/path，
// 正文不进基座；模型按 path 用 read_file 直接读取。
func TestSkillsExposeReadablePaths(t *testing.T) {
	ws := tempDir(t)
	dir := filepath.Join(ws, ".adago", "skills", "deploy-notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: deploy-notes\ndescription: 部署注意事项\n---\n部署步骤正文"), 0o600); err != nil {
		t.Fatal(err)
	}

	asm := codingProductIn(t, ws)
	byName := map[string]domain.SkillMeta{}
	for _, m := range asm.Skills() {
		byName[m.Name] = m
	}

	// 插件随包技能：path 指向插件包内的 SKILL.md，文件可读。
	clean, ok := byName["clean-code"]
	if !ok {
		t.Fatalf("缺少 persona-coding 的随包技能 clean-code: %+v", asm.Skills())
	}
	if !filepath.IsAbs(clean.Path) {
		t.Fatalf("clean-code path 不是绝对路径: %q", clean.Path)
	}
	body, err := os.ReadFile(clean.Path)
	if err != nil {
		t.Fatalf("按 path 读取 clean-code 失败（%s）: %v", clean.Path, err)
	}
	if !strings.Contains(string(body), "先读后改") {
		t.Fatalf("clean-code 正文不对: %.200s", body)
	}

	// 工作区技能：path 指向工作区 .adago/skills 下的文件。
	deploy, ok := byName["deploy-notes"]
	if !ok || !filepath.IsAbs(deploy.Path) {
		t.Fatalf("deploy-notes 路径不对: %+v", deploy)
	}
	body, err = os.ReadFile(deploy.Path)
	if err != nil || !strings.Contains(string(body), "部署步骤正文") {
		t.Fatalf("按 path 读取 deploy-notes = %q / %v", body, err)
	}

	// ui-charts 的随包技能 charts-render：path 指向插件包内的 SKILL.md，文件可读。
	charts, ok := byName["charts-render"]
	if !ok {
		t.Fatalf("缺少 ui-charts 的随包技能 charts-render: %+v", asm.Skills())
	}
	if !filepath.IsAbs(charts.Path) {
		t.Fatalf("charts-render path 不是绝对路径: %q", charts.Path)
	}
	body, err = os.ReadFile(charts.Path)
	if err != nil {
		t.Fatalf("按 path 读取 charts-render 失败（%s）: %v", charts.Path, err)
	}
	if !strings.Contains(string(body), "ui-charts/bar") || !strings.Contains(string(body), "adag-ui") {
		t.Fatalf("charts-render 正文不对: %.400s", body)
	}
}

// TestToolsGit 钉 tools-git：真实临时仓库上的 status/log、非仓库的错误文本、cwd 越界。
func TestToolsGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git，跳过")
	}
	ws := tempDir(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = ws
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "first commit")

	hc := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
	asm := assemble(t, testProduct(t, "tools-git"), hc, nil)

	if text, res := callTool(t, asm, hc, "git_status", map[string]any{}); !strings.Contains(text, "first") {
		// --branch 输出含分支头；至少要拿到 exit 0 的状态文本。
		if res.Details["code"].(float64) != 0 {
			t.Fatalf("git_status = %q / %#v", text, res.Details)
		}
	}
	if text, res := callTool(t, asm, hc, "git_log", map[string]any{"maxCount": 5}); !strings.Contains(text, "first commit") || res.Details["code"].(float64) != 0 {
		t.Fatalf("git_log = %q / %#v", text, res.Details)
	}
	if text, res := callTool(t, asm, hc, "git_diff", map[string]any{}); res.Details["code"].(float64) != 0 {
		t.Fatalf("git_diff 空仓库差异应 exit 0: %q / %#v", text, res.Details)
	}

	// 不是 git 仓库的目录：git 退出码非零，文本带回错误（不抛脚本异常）。
	plain := tempDir(t)
	hc2 := host.Context{Workspace: plain, Log: func(string, string, map[string]any) {}}
	asm2 := assemble(t, testProduct(t, "tools-git"), hc2, nil)
	text, res := callTool(t, asm2, hc2, "git_status", map[string]any{})
	if res.Details["code"].(float64) == 0 || !strings.Contains(text, "not a git repository") {
		t.Fatalf("非仓库应回 git 错误文本: %q / %#v", text, res.Details)
	}

	// cwd 越界必须被拒（插件先判一次）。
	if _, err := callToolErr(t, asm, hc, "git_status", map[string]any{"cwd": "/etc"}); err == nil {
		t.Fatal("工作区外的 cwd 必须失败")
	}
}

// TestToolsAsk 钉 ask_user 的三形态与两条失败关闭。
func TestToolsAsk(t *testing.T) {
	asm := assemble(t, testProduct(t, "tools-ask"),
		host.Context{Workspace: tempDir(t), Log: func(string, string, map[string]any) {}}, nil)

	run := func(t *testing.T, ui *host.Context, args map[string]any) (string, error) {
		t.Helper()
		inst := pluginOwning(t, asm, "ask_user")
		raw, _ := json.Marshal(args)
		hc := host.Context{Workspace: tempDir(t), Log: func(string, string, map[string]any) {}}
		if ui != nil {
			hc.UI = ui.UI
		}
		ref := handlerOf(t, inst, "ask_user")
		res, err := inst.CallTool(context.Background(), ref, raw, hc)
		if err != nil {
			return "", err
		}
		return res.Content[0].Text, nil
	}
	yesUI := &host.Context{UI: func(_ context.Context, req host.UIRequest) (host.UIResolution, error) {
		return host.UIResolution{Status: domain.InteractionAccepted, Value: "true"}, nil
	}}
	noUI := &host.Context{UI: func(_ context.Context, req host.UIRequest) (host.UIResolution, error) {
		return host.UIResolution{Status: domain.InteractionDenied}, nil
	}}
	selectUI := &host.Context{UI: func(_ context.Context, req host.UIRequest) (host.UIResolution, error) {
		if req.Kind != domain.InteractionSelect || len(req.Options) != 3 {
			t.Fatalf("select 请求形状不对: %+v", req)
		}
		return host.UIResolution{Status: domain.InteractionAccepted, Value: req.Options[1]}, nil
	}}

	// confirm 同意/拒绝。
	if text, err := run(t, yesUI, map[string]any{"question": "继续？", "kind": "confirm"}); err != nil || !strings.Contains(text, "yes") {
		t.Fatalf("confirm 同意 = %q / %v", text, err)
	}
	if _, err := run(t, noUI, map[string]any{"question": "继续？", "kind": "confirm"}); err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("confirm 拒绝应按 denied 报错: %v", err)
	}
	// select：候选项校验与回显。
	if text, err := run(t, selectUI, map[string]any{"question": "选一个", "kind": "select", "options": []string{"a", "b", "c"}}); err != nil || !strings.Contains(text, "b") {
		t.Fatalf("select = %q / %v", text, err)
	}
	if _, err := run(t, yesUI, map[string]any{"question": "选一个", "kind": "select", "options": []string{"只一个"}}); err == nil {
		t.Fatal("options 少于 2 个必须报错")
	}
	// input。
	inputUI := &host.Context{UI: func(_ context.Context, req host.UIRequest) (host.UIResolution, error) {
		return host.UIResolution{Status: domain.InteractionAccepted, Value: "hello"}, nil
	}}
	if text, err := run(t, inputUI, map[string]any{"question": "说点什么", "kind": "input"}); err != nil || !strings.Contains(text, "hello") {
		t.Fatalf("input = %q / %v", text, err)
	}
	// 完全没有 UI：失败关闭。
	if _, err := run(t, nil, map[string]any{"question": "继续？", "kind": "confirm"}); err == nil || !strings.Contains(err.Error(), "失败关闭") {
		t.Fatalf("无 UI 必须失败关闭: %v", err)
	}
	// 非法 kind。
	if _, err := run(t, yesUI, map[string]any{"question": "继续？", "kind": "phone"}); err == nil {
		t.Fatal("非法 kind 必须报错")
	}
	// ask 不是终止型工具：回答作为工具结果喂回模型后，轮次继续（02 §5.3 第 6g 步）。
	// 标成 terminatesTurn 会在用户作答后立刻收尾，界面看起来就是「对话被终止」。
	for _, inst := range asm.Plugins {
		if inst.ID() != "tools-ask" {
			continue
		}
		for _, td := range inst.Contribution().Tools {
			if td.Name == "ask_user" && td.TerminatesTurn {
				t.Fatal("ask_user 不能标记 terminatesTurn：回答后还要继续本轮")
			}
		}
	}
}

// TestToolsSkillSearch 钉 skill_search：命中、空结果、宿主没给技能表时失败关闭。
func TestToolsSkillSearch(t *testing.T) {
	ws := tempDir(t)
	hc := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
	hc.Skills = func() []domain.SkillMeta {
		return []domain.SkillMeta{
			{Name: "clean-code", Description: "改动纪律", Origin: host.SkillOriginPlugin, PluginID: "persona-coding"},
			{Name: "deploy-notes", Description: "部署注意事项", Origin: host.SkillOriginWorkspace},
		}
	}
	asm := assemble(t, testProduct(t, "tools-skill"), hc, nil)

	if text, res := callTool(t, asm, hc, "skill_search", map[string]any{"query": "deploy"}); !strings.Contains(text, "deploy-notes") || strings.Contains(text, "clean-code") {
		t.Fatalf("skill_search(deploy) = %q / %#v", text, res.Details)
	}
	if text, _ := callTool(t, asm, hc, "skill_search", map[string]any{"query": "CLEAN"}); !strings.Contains(text, "clean-code") {
		t.Fatalf("匹配应大小写不敏感: %q", text)
	}
	if text, _ := callTool(t, asm, hc, "skill_search", map[string]any{"query": "nothing"}); !strings.Contains(text, "没有匹配") {
		t.Fatalf("空结果文案 = %q", text)
	}
	// 宿主没给技能表：失败关闭。
	hc2 := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
	asm2 := assemble(t, testProduct(t, "tools-skill"), hc2, nil)
	inst := pluginOwning(t, asm2, "skill_search")
	_, err := inst.CallTool(context.Background(), handlerOf(t, inst, "skill_search"),
		json.RawMessage(`{"query":"x"}`), hc2)
	if err == nil || !strings.Contains(err.Error(), "ctx.skills") {
		t.Fatalf("没有技能表应失败关闭: %v", err)
	}
}

// TestChartGuide 钉 ui-charts 的 chart_guide 工具：全部/单形态/非法形态。
func TestChartGuide(t *testing.T) {
	hc := host.Context{Workspace: tempDir(t), Log: func(string, string, map[string]any) {}}
	asm := assemble(t, testProduct(t, "ui-charts"), hc, nil)

	// 全部 renderer：六种 id 都在，adag-ui 封套语法与公共字段齐全。
	text, res := callTool(t, asm, hc, "chart_guide", map[string]any{})
	for _, id := range []string{"ui-charts/bar", "ui-charts/column", "ui-charts/compare", "ui-charts/stacked", "ui-charts/heatmap", "ui-charts/spark"} {
		if !strings.Contains(text, id) {
			t.Fatalf("chart_guide 全部应包含 %s:\n%s", id, text)
		}
	}
	for _, want := range []string{"adag-ui", "fallback", "renderer", "title（必填）"} {
		if !strings.Contains(text, want) {
			t.Fatalf("chart_guide 全部缺少 %q:\n%s", want, text)
		}
	}
	if renderers, ok := res.Details["renderers"].([]any); !ok || len(renderers) != 6 {
		t.Fatalf("details.renderers = %#v", res.Details["renderers"])
	}

	// 单形态：只返回该 renderer，且 details.selected 正确。
	text, res = callTool(t, asm, hc, "chart_guide", map[string]any{"kind": "bar"})
	if !strings.Contains(text, "ui-charts/bar") || strings.Contains(text, "ui-charts/spark") {
		t.Fatalf("chart_guide(bar) 应只含 bar:\n%s", text)
	}
	if res.Details["selected"] != "ui-charts/bar" {
		t.Fatalf("details.selected = %#v", res.Details["selected"])
	}

	// 非法形态：失败关闭。
	inst := pluginOwning(t, asm, "chart_guide")
	_, err := inst.CallTool(context.Background(), handlerOf(t, inst, "chart_guide"),
		json.RawMessage(`{"kind":"line"}`), hc)
	if err == nil || !strings.Contains(err.Error(), "未知图表形态") {
		t.Fatalf("非法 kind 应报错: %v", err)
	}
}

// ---- helpers ----

func pluginIDsOf(asm *app.Assembly) []string {
	var out []string
	for _, inst := range asm.Plugins {
		out = append(out, inst.ID())
	}
	return out
}

func pluginOwning(t *testing.T, asm *app.Assembly, tool string) *host.Instance {
	t.Helper()
	for _, inst := range asm.Plugins {
		c := inst.Contribution()
		if c == nil {
			continue
		}
		for _, td := range c.Tools {
			if td.Name == tool {
				return inst
			}
		}
	}
	t.Fatalf("没有插件贡献工具 %q", tool)
	return nil
}

func handlerOf(t *testing.T, inst *host.Instance, tool string) host.HandlerRef {
	t.Helper()
	ref, ok := inst.ToolRef(tool)
	if !ok {
		t.Fatalf("插件 %s 没有工具 %s", inst.ID(), tool)
	}
	return ref
}

// callToolErr 调用一个期望失败的插件工具（与 callTool 同一路径，不 Fatal）。
func callToolErr(t *testing.T, asm *app.Assembly, hc host.Context, tool string, args any) (string, error) {
	t.Helper()
	inst := pluginOwning(t, asm, tool)
	raw, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	callCtx := hc
	callCtx.PluginID = inst.ID()
	if callCtx.Workspace == "" {
		callCtx.Workspace = "/workspace"
	}
	res, err := inst.CallTool(context.Background(), handlerOf(t, inst, tool), raw, callCtx)
	if err != nil {
		return "", err
	}
	if len(res.Content) == 0 {
		return "", nil
	}
	return res.Content[0].Text, nil
}
