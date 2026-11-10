package coreapp_test

// 产品级 pluginIslands 开关：默认产品开启，空基座显式关闭；gateway 用它
// 与 ADAG_DISABLE_ISLANDS 组合成 Bridge.DisableIslands。

import (
	"testing"

	"github.com/adag/ada-go/internal/agent_core/coreapp"
)

func TestProductPluginIslandsCapability(t *testing.T) {
	coding, err := coreapp.LoadProduct("../../../products/coding.agent.json")
	if err != nil {
		t.Fatalf("加载 coding 产品: %v", err)
	}
	if !coding.Capabilities.PluginIslands {
		t.Fatal("coding 产品应开启 pluginIslands")
	}

	skeleton, err := coreapp.LoadProduct("../../../products/skeleton.agent.json")
	if err != nil {
		t.Fatalf("加载 skeleton 产品: %v", err)
	}
	if skeleton.Capabilities.PluginIslands {
		t.Fatal("skeleton 空基座应关闭 pluginIslands")
	}
}
