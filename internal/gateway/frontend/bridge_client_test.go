package frontend

// frontend.client 的资源存在性校验（docs/20 P1-1）：清单合法不等于包里有文件；
// newUnit 是装配期最后一道检查，缺 client 资源必须报错而不是等到浏览器 404。

import (
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
)

func TestNewUnitRequiresClientAsset(t *testing.T) {
	m := &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: plugins.TrustBuiltin,
		Frontend: &plugins.Frontend{
			Client: "client.js",
			Slots: []plugins.SlotContribution{{
				Slot: "chat.corner", Template: "slot.tsx",
			}},
		},
	}
	script := host.Script{
		Manifest:     m,
		Bundle:       map[string]string{"slot.tsx": "export default function () { return null; }"},
		StaticAssets: map[string][]byte{},
	}
	if _, err := newUnit(script); err == nil || !strings.Contains(err.Error(), "client") {
		t.Fatalf("缺 client 资源应装配失败，err = %v", err)
	}

	script.StaticAssets["client.js"] = []byte("export default {}")
	if _, err := newUnit(script); err != nil {
		t.Fatalf("client 资源存在时不应失败: %v", err)
	}
}
