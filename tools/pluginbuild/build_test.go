package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEntry(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
		t.Fatalf("写入 %s: %v", name, err)
	}
}

func TestBuildShimsHostModules(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "client.tsx", `import { h, defineComponent } from "ada:render";
import { Button } from "ada:ui";

export default defineComponent({
  render() {
    return <Button>保存</Button>;
  },
});
`)

	code, out, err := Build(Options{Dir: dir, ID: "demo"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	text := string(code)
	for _, want := range []string{
		"generated from client.tsx",
		"globalThis.AdaRender",
		"globalThis.AdaUI",
		"defineComponent",
		"export",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("产物缺少 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "eval(") || strings.Contains(text, "new Function") {
		t.Fatalf("产物包含 CSP 禁止内容:\n%s", text)
	}
	if strings.Contains(text, `"ada:render"`) || strings.Contains(text, `"ada:ui"`) {
		t.Fatalf("产物仍含 bare import:\n%s", text)
	}
	if out != filepath.Join(dir, "static", "client.js") {
		t.Fatalf("输出路径 = %q", out)
	}
	if len(code) > maxBundleBytes {
		t.Fatalf("产物超过上限: %d", len(code))
	}
}

func TestRunWriteAndCheck(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "client.tsx", `import { h } from "ada:render";
export default function App() { return <div class="x">ok</div>; }
`)
	if err := Run(Options{Dir: dir, ID: "demo"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "static", "client.js")); err != nil {
		t.Fatalf("产物未写入: %v", err)
	}
	if err := Run(Options{Dir: dir, ID: "demo", Check: true}); err != nil {
		t.Fatalf("同步时 --check 不应失败: %v", err)
	}

	writeEntry(t, dir, "client.tsx", `import { h } from "ada:render";
export default function App() { return <div class="x">changed</div>; }
`)
	if err := Run(Options{Dir: dir, ID: "demo", Check: true}); err == nil || !strings.Contains(err.Error(), "不同步") {
		t.Fatalf("源码变化后 --check 应失败，err = %v", err)
	}
}

func TestBuildRejectsEval(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "client.tsx", `const f = new Function("return 1");
export default f;
`)
	if _, _, err := Build(Options{Dir: dir, ID: "demo"}); err == nil || !strings.Contains(err.Error(), "eval/new Function") {
		t.Fatalf("应拒绝 new Function，err = %v", err)
	}
}
