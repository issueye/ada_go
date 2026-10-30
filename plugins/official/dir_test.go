package official_test

// 磁盘加载：桌面打包把 plugins/official 放进 Electron resources，运行时就地加载。

import (
	"os"
	"testing"

	"github.com/adag/ada-go/plugins/official"
)

func TestScriptsFromRepoDir(t *testing.T) {
	root, err := os.Getwd() // plugins/official
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	scripts, err := official.ScriptsFromDir(root)
	if err != nil {
		t.Fatalf("ScriptsFromDir: %v", err)
	}
	backend := map[string]bool{}
	for _, s := range scripts {
		if s.Manifest != nil {
			backend[s.Manifest.ID] = true
		}
	}
	for _, want := range []string{"tools-plan", "tools-fs", "provider-openai-chat", "policy-default"} {
		if !backend[want] {
			t.Fatalf("后端插件缺少 %q（现有：%v）", want, backend)
		}
	}
	if backend["token-usage"] {
		t.Fatal("纯前端插件不应出现在后端脚本里")
	}

	frontend, err := official.FrontendScriptsFromDir(root)
	if err != nil {
		t.Fatalf("FrontendScriptsFromDir: %v", err)
	}
	fe := map[string]bool{}
	for _, s := range frontend {
		if s.Manifest != nil {
			fe[s.Manifest.ID] = true
		}
	}
	for _, want := range []string{"tools-plan", "provider-admin", "token-usage", "ui-hello-tsx"} {
		if !fe[want] {
			t.Fatalf("前端插件缺少 %q（现有：%v）", want, fe)
		}
	}
}
