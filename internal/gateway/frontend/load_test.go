package frontend_test

// 第三方插件只经 LoadBlockScript 进入前端桥：只保留 frontend.blocks，
// 清单里的页面/插槽/样式/脚本不会越过边界。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	pluginapi "github.com/adag/ada-go/internal/common/pluginapi"
	"github.com/adag/ada-go/internal/gateway/frontend"
)

func writeBlockPlugin(t *testing.T, manifest string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "demo-block")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string]string{
		"plugin.json":       manifest,
		"schemas/card.json": `{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`,
		"blocks/card.tsx":   `export default function (props) { return <section class="x">{props.block.data.title}</section>; }`,
		"blocks/broken.tsx": ``,
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

const thirdPartyBlockManifest = `{
  "id": "demo-block",
  "name": "Demo Block",
  "version": "0.1.0",
  "apiVersion": 1,
  "trust": "third-party",
  "frontend": {
    "capabilities": [],
    "slots": [{"slot": "chat.corner", "template": "slot.tsx"}],
    "pages": [{"slug": "main", "title": "Main", "template": "page.tsx"}],
    "styles": ["main.css"],
    "blocks": [{"name": "card", "schema": "schemas/card.json", "template": "blocks/card.tsx"}]
  }
}`

func TestLoadBlockScriptStripsNonBlockFrontend(t *testing.T) {
	dir := writeBlockPlugin(t, thirdPartyBlockManifest)
	script, err := frontend.LoadBlockScript(dir)
	if err != nil {
		t.Fatalf("LoadBlockScript: %v", err)
	}
	if got := script.Manifest.Frontend.Blocks; len(got) != 1 || got[0].Name != "card" {
		t.Fatalf("blocks = %+v", got)
	}
	f := script.Manifest.Frontend
	if len(f.Slots) != 0 || len(f.Pages) != 0 || len(f.Styles) != 0 || f.Assets != "" {
		t.Fatalf("非 block 前端贡献未剥离: %+v", f)
	}
	if got := script.Schemas["schemas/card.json"]; len(got) == 0 {
		t.Fatal("缺少数据 Schema")
	}
	if got := script.Bundle["blocks/card.tsx"]; got == "" {
		t.Fatal("缺少 block 模板")
	}

	bridge, err := frontend.NewBridge(frontend.BridgeOptions{Scripts: []pluginapi.Script{script}, MarkdownBlocks: true})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	raw, html, err := renderBlock(bridge, request(t, "/thread/th_1"), "demo-block/card", "b1", json.RawMessage(`{"title":"hi"}`))
	if err != nil {
		t.Fatalf("RenderBlock: %v", err)
	}
	if len(raw) == 0 || html == "" {
		t.Fatalf("渲染结果为空: %s / %q", raw, html)
	}
}

func TestLoadBlockScriptRejectsInvalid(t *testing.T) {
	if _, err := frontend.LoadBlockScript(writeBlockPlugin(t, `{
	  "id":"demo-block","name":"Demo","version":"0.1.0","apiVersion":1,"trust":"third-party",
	  "frontend":{"capabilities":[]}
	}`)); !errors.Is(err, frontend.ErrNoBlocks) {
		t.Fatalf("无 blocks 应返回 ErrNoBlocks: %v", err)
	}

	if _, err := frontend.LoadBlockScript(writeBlockPlugin(t, `{
	  "id":"demo-block","name":"Demo","version":"0.1.0","apiVersion":1,"trust":"third-party",
	  "frontend":{"blocks":[{"name":"card","schema":"schemas/missing.json","template":"blocks/card.tsx"}]}
	}`)); err == nil {
		t.Fatal("缺 Schema 应报错")
	}

	if _, err := frontend.LoadBlockScript(writeBlockPlugin(t, `{
	  "id":"demo-block","name":"Demo","version":"0.1.0","apiVersion":1,"trust":"third-party",
	  "frontend":{"blocks":[{"name":"card","schema":"schemas/card.json","template":"blocks/missing.tsx"}]}
	}`)); err == nil {
		t.Fatal("缺模板应报错")
	}
}
