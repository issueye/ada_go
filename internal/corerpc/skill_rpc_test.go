package corerpc_test

// skill.list 的 RPC 形状验收（spec proto.json 的 skill 组）：
// 数据源是 Session 的装配技能表；元数据带绝对 path，正文由模型用 read_file 读取。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/cli"
)

// codingProductPath 返回 coding 产品声明（capabilities.skills=true）。
func codingProductPath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "products", "coding.agent.json")
}

// newCodingSession 打开 coding 产品的会话并放一个超长工作区技能。
func newCodingSession(t *testing.T) *cli.Session {
	t.Helper()
	ws := t.TempDir()
	dir := filepath.Join(ws, ".adago", "skills", "release-checklist")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("检查项。\n", 4000) // 约 20KB，超过 16KB 截断阈值
	raw := "---\nname: release-checklist\ndescription: 发版前的检查清单\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, err := cli.Open(context.Background(), cli.Config{
		Workspace: ws,
		DataDir:   t.TempDir(),
		Product:   codingProductPath(t),
		Assembler: app.Assembler{},
		Fake:      true,
		In:        strings.NewReader(""),
		Out:       discard{},
	})
	if err != nil {
		t.Fatalf("cli.Open(coding): %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	return sess
}

// TestSkillRPC 钉 skill.list 的形状：元数据带绝对 path，正文由模型用 read_file 读取。
func TestSkillRPC(t *testing.T) {
	sess := newCodingSession(t)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")

	// skill.list：工作区技能 + 随包技能（clean-code），origin/path 正确。
	res := c.call(t, "skill.list", map[string]any{})
	var list struct {
		Skills []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Path        string `json:"path"`
			Origin      string `json:"origin"`
			PluginID    string `json:"pluginId,omitempty"`
		} `json:"skills"`
		Conflicts []string `json:"conflicts"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		t.Fatalf("skill.list 结果不是期望形状: %v\n%s", err, res)
	}
	byName := map[string]struct {
		origin   string
		pluginID string
		path     string
	}{}
	for _, s := range list.Skills {
		byName[s.Name] = struct {
			origin   string
			pluginID string
			path     string
		}{s.Origin, s.PluginID, s.Path}
	}
	if got, ok := byName["release-checklist"]; !ok || got.origin != "workspace" || !filepath.IsAbs(got.path) {
		t.Fatalf("skill.list 缺少工作区技能或 path 不对: %+v", byName)
	}
	if got, ok := byName["clean-code"]; !ok || got.origin != "plugin" || got.pluginID != "persona-coding" || !filepath.IsAbs(got.path) {
		t.Fatalf("skill.list 缺少随包技能或归属不对: %+v", byName)
	}
	// path 指向真实 SKILL.md，模型可直接 read_file。
	body, err := os.ReadFile(byName["release-checklist"].path)
	if err != nil || !strings.Contains(string(body), "检查项") {
		t.Fatalf("按 path 读取 release-checklist = %q / %v", body, err)
	}
}
