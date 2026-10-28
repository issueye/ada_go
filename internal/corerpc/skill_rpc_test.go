package corerpc_test

// skill.list / skill.read 的 RPC 形状验收（spec proto.json 的 skill 组）：
// 数据源是 Session 的装配技能表；模型工具 read_skill 与 skill.read 走同一实现。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// TestSkillRPC 钉 skill.list / skill.read 的形状与错误语义。
func TestSkillRPC(t *testing.T) {
	sess := newCodingSession(t)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")

	// skill.list：工作区技能 + 随包技能（clean-code），origin 正确。
	res := c.call(t, "skill.list", map[string]any{})
	var list struct {
		Skills []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
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
	}{}
	for _, s := range list.Skills {
		byName[s.Name] = struct {
			origin   string
			pluginID string
		}{s.Origin, s.PluginID}
	}
	if got, ok := byName["release-checklist"]; !ok || got.origin != "workspace" {
		t.Fatalf("skill.list 缺少工作区技能或 origin 不对: %+v", byName)
	}
	if got, ok := byName["clean-code"]; !ok || got.origin != "plugin" || got.pluginID != "persona-coding" {
		t.Fatalf("skill.list 缺少随包技能或归属不对: %+v", byName)
	}

	// skill.read：命中（content 是 frontmatter 之后的正文，截断标记为 true）。
	res = c.call(t, "skill.read", map[string]any{"name": "release-checklist"})
	var read struct {
		Skill struct {
			Name   string `json:"name"`
			Origin string `json:"origin"`
		} `json:"skill"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(res, &read); err != nil {
		t.Fatalf("skill.read 结果不是期望形状: %v\n%s", err, res)
	}
	if read.Skill.Name != "release-checklist" || read.Skill.Origin != "workspace" {
		t.Fatalf("skill.read 的 skill 摘要不对: %+v", read.Skill)
	}
	if strings.Contains(read.Content, "description:") || strings.Contains(read.Content, "name:") {
		t.Fatalf("content 不应包含 frontmatter: %.80s", read.Content)
	}
	if !strings.Contains(read.Content, "检查项") {
		t.Fatalf("content 应是正文: %.80s", read.Content)
	}
	if len(read.Content) > 16<<10+64 {
		t.Fatalf("content 应截断到 16KB 附近，得到 %d 字节", len(read.Content))
	}
	if !read.Truncated {
		t.Fatal("超长正文应标 truncated=true")
	}

	// 未知技能：invalid_params。
	resp := c.callError(t, "skill.read", map[string]any{"name": "no-such"})
	if !strings.Contains(string(resp.Error), "no-such") {
		t.Fatalf("错误应点名技能名: %s", resp.Error)
	}

	// 缺 name：invalid_params。
	c.callError(t, "skill.read", map[string]any{})
}
