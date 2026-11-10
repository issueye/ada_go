package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGlobalStoreSaveLoadAndMigrate(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "providers.json")
	gs, err := NewGlobalStore(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{ID: "p1", Name: "P1", Protocol: "protocol", BaseURL: "https://example.test", APIKey: "sk", Models: []Model{{ID: "m1"}}}
	if err := gs.Save([]Config{cfg}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config", "provider-config.json")); err != nil {
		t.Fatalf("global.config 未落盘: %v", err)
	}
	cfgs, exists, err := gs.Load()
	if err != nil || !exists || len(cfgs) != 1 || cfgs[0].APIKey != "sk" {
		t.Fatalf("Load = %+v/%v/%v", cfgs, exists, err)
	}

	// legacy 文件存在、global.config 缺失：读取时迁移。
	dir2 := t.TempDir()
	legacy2 := filepath.Join(dir2, "providers.json")
	if err := NewStore(legacy2).Save([]Config{cfg}); err != nil {
		t.Fatal(err)
	}
	gs2, err := NewGlobalStore(legacy2)
	if err != nil {
		t.Fatal(err)
	}
	cfgs, exists, err = gs2.Load()
	if err != nil || !exists || len(cfgs) != 1 {
		t.Fatalf("legacy Load = %+v/%v/%v", cfgs, exists, err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "config", "provider-config.json")); err != nil {
		t.Fatalf("legacy 未迁移到 global.config: %v", err)
	}
}
