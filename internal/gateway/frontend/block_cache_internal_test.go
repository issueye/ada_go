package frontend

// 块渲染缓存的内部约定：键必须覆盖 renderer/block/插件版本/路由/data，
// 缓存值按副本进出，达到上限整体清空（宁可重渲染，不无限增长）。

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/adag/ada-go/internal/agent_core/plugins"
)

func TestBlockCacheStoreAndKey(t *testing.T) {
	b := &Bridge{blockCache: map[string]blockCacheEntry{}}
	entry := blockEntry{u: &unit{manifest: &plugins.Manifest{ID: "demo", Version: "0.1.0"}}}
	r := httptest.NewRequest("GET", "/threads/th_1", nil)
	data := json.RawMessage(`{"a":1}`)
	key := b.blockCacheKey(entry, r, "demo/card", "b1", data)

	if _, _, ok := b.cachedBlock(key); ok {
		t.Fatal("空缓存不应命中")
	}

	raw := json.RawMessage(`{"t":"div"}`)
	b.storeBlock(key, raw, `<div></div>`)
	gotRaw, gotHTML, ok := b.cachedBlock(key)
	if !ok || string(gotRaw) != string(raw) || gotHTML != `<div></div>` {
		t.Fatalf("缓存命中 = %v/%s/%q", ok, gotRaw, gotHTML)
	}

	// 返回值是副本：调用方改写不会污染缓存。
	gotRaw[0] = 'X'
	if again, _, _ := b.cachedBlock(key); string(again) != string(raw) {
		t.Fatalf("缓存被调用方改写: %s", again)
	}

	// 键区分 renderer / block id / 插件版本 / 路由 / data。
	bumped := blockEntry{u: &unit{manifest: &plugins.Manifest{ID: "demo", Version: "0.2.0"}}}
	for name, got := range map[string]string{
		"renderer": b.blockCacheKey(entry, r, "demo/other", "b1", data),
		"block":    b.blockCacheKey(entry, r, "demo/card", "b2", data),
		"version":  b.blockCacheKey(bumped, r, "demo/card", "b1", data),
		"route":    b.blockCacheKey(entry, httptest.NewRequest("GET", "/threads/th_2", nil), "demo/card", "b1", data),
		"data":     b.blockCacheKey(entry, r, "demo/card", "b1", json.RawMessage(`{"a":2}`)),
	} {
		if got == key {
			t.Fatalf("%s 未参与缓存键", name)
		}
	}
}

func TestBlockCacheLimitResets(t *testing.T) {
	b := &Bridge{blockCache: map[string]blockCacheEntry{}}
	raw := json.RawMessage(`{"t":"div"}`)
	for i := 0; i < blockCacheLimit; i++ {
		b.storeBlock(fmt.Sprintf("k%d", i), raw, `<div></div>`)
	}
	b.storeBlock("fresh", raw, `<div></div>`)

	if _, _, ok := b.cachedBlock("k0"); ok {
		t.Fatal("达到上限后应清空旧缓存，而不是继续增长")
	}
	if _, _, ok := b.cachedBlock("fresh"); !ok {
		t.Fatal("清空后新条目应写入")
	}
}
