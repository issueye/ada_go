package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("写 config.json: %v", err)
	}
}

// TestLoadFromOverrides:config.json 覆盖默认值;ui 节靠显式 json tag 生效
// (T1.8 补了 tag,不再依赖 encoding/json 的大小写宽容匹配);未覆盖的键
// 保持默认。
func TestLoadFromOverrides(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"agent":{"max_tabs":3},"ui":{"width":999,"height":555},"script":{"max_timeout_ms":60000}}`)
	cfg := loadFrom(dir, Default())
	if cfg.Agent.MaxTabs != 3 {
		t.Fatalf("agent.max_tabs = %d, 期望 3", cfg.Agent.MaxTabs)
	}
	if cfg.UI.Width != 999 || cfg.UI.Height != 555 {
		t.Fatalf("ui = %+v, 期望 999x555", cfg.UI)
	}
	if cfg.Script.MaxTimeoutMS != 60000 {
		t.Fatalf("script.max_timeout_ms = %d, 期望 60000", cfg.Script.MaxTimeoutMS)
	}
	if cfg.Script.TimeoutMS != 120000 {
		t.Fatalf("未覆盖的 script.timeout_ms 应保持默认 120000,得到 %d", cfg.Script.TimeoutMS)
	}
}

// TestLoadFromBrokenFile 钉住 T1.8:解析失败的 config.json 回退完整默认值
// 并打日志(旧实现 _ = json.Unmarshal 只留半份配置静默运行)。
func TestLoadFromBrokenFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"agent":{"max_tabs":3},,}`)
	cfg := loadFrom(dir, Default())
	if cfg.Agent.MaxTabs != 8 {
		t.Fatalf("坏文件应回退默认 max_tabs=8,得到 %d", cfg.Agent.MaxTabs)
	}
	if cfg.UI.Width != 1280 {
		t.Fatalf("坏文件应回退完整默认值,ui.width = %d", cfg.UI.Width)
	}
}

// TestLoadFromMissingFile:没有 config.json 时保持默认值。
func TestLoadFromMissingFile(t *testing.T) {
	cfg := loadFrom(t.TempDir(), Default())
	if cfg.Agent.MaxTabs != 8 || !cfg.Control.Mask {
		t.Fatalf("缺文件应保持默认: max_tabs=%d mask=%v", cfg.Agent.MaxTabs, cfg.Control.Mask)
	}
}

// TestNormalizeZeroValues:0 值回默认 —— config.json 写 0 不能悄悄关闭
// 标签页上限或脚本预算。
func TestNormalizeZeroValues(t *testing.T) {
	cfg := Default()
	cfg.Agent.MaxTabs = 0
	cfg.Script.MaxTimeoutMS = 0
	cfg.UI.Width = 0
	cfg.UI.Height = 0
	normalize(&cfg)
	if cfg.Agent.MaxTabs != 8 {
		t.Fatalf("agent.max_tabs = %d, 期望兜底 8", cfg.Agent.MaxTabs)
	}
	if cfg.Script.MaxTimeoutMS != 300000 {
		t.Fatalf("script.max_timeout_ms = %d, 期望兜底 300000", cfg.Script.MaxTimeoutMS)
	}
	if cfg.UI.Width != 1280 || cfg.UI.Height != 860 {
		t.Fatalf("ui = %+v, 期望兜底 1280x860", cfg.UI)
	}
}
