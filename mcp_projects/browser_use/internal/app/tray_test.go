package app

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPConfigSnippet(t *testing.T) {
	raw := mcpConfigSnippet()
	var cfg struct {
		ID        string   `json:"id"`
		Transport string   `json:"transport"`
		Command   string   `json:"command"`
		Args      []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("snippet 不是合法 JSON: %v\n%s", err, raw)
	}
	if cfg.ID != "browser_use" || cfg.Transport != "stdio" || cfg.Command == "" || !filepath.IsAbs(cfg.Command) {
		t.Fatalf("配置不符: %+v", cfg)
	}
	if len(cfg.Args) != 1 || cfg.Args[0] != "mcp" {
		t.Fatalf("args 不符: %+v", cfg.Args)
	}
	if !strings.Contains(raw, "\"id\": \"browser_use\"") {
		t.Fatalf("snippet 应可读缩进: %s", raw)
	}
}

func TestMCPHTTPConfigSnippet(t *testing.T) {
	raw := mcpHTTPConfigSnippet()
	var cfg struct {
		ID        string `json:"id"`
		Transport string `json:"transport"`
		URL       string `json:"url"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("HTTP snippet 不是合法 JSON: %v\n%s", err, raw)
	}
	if cfg.ID != "browser_use" || cfg.Transport != "http" || cfg.URL != "http://127.0.0.1:8766/mcp" {
		t.Fatalf("HTTP 配置不符: %+v", cfg)
	}
	if !strings.Contains(raw, "\"transport\": \"http\"") {
		t.Fatalf("HTTP snippet 应可读缩进: %s", raw)
	}
}
