package frontend

// frontend.client 的资源存在性校验（docs/20 P1-1）：清单合法不等于包里有文件；
// newUnit 是装配期最后一道检查，缺 client 资源必须报错而不是等到浏览器 404。

import (
	"strings"
	"testing"

	pluginapi "github.com/adag/ada-go/internal/common/pluginapi"
)

func TestNewUnitRequiresClientAsset(t *testing.T) {
	m := &pluginapi.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: pluginapi.APIVersionMajor,
		Trust: pluginapi.TrustBuiltin,
		Frontend: &pluginapi.Frontend{
			Client: "client.js",
			Slots: []pluginapi.SlotContribution{{
				Slot: "chat.corner", Template: "slot.tsx",
			}},
		},
	}
	script := pluginapi.Script{
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
