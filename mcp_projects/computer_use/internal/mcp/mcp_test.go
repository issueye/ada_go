//go:build windows

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEveryToolIsDispatched catches a tool that is listed but has no case in
// callTool, which an agent would see as "unknown tool".
func TestEveryToolIsDispatched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"content":[]}`))
	}))
	defer srv.Close()
	s := New(srv.URL, "", "test")

	// Required arguments, so that a tool fails for its own reasons rather
	// than for a missing argument.
	need := map[string]map[string]any{
		"click":          {"x": 1, "y": 1},
		"move":           {"x": 1, "y": 1},
		"drag":           {"x": 1, "y": 1, "x2": 2, "y2": 2},
		"scroll":         {"x": 1, "y": 1},
		"type":           {"text": "hi"},
		"key":            {"keys": "enter"},
		"wait":           {"ms": 1},
		"run_script":     {"source": "1"},
		"run_macro":      {"name": "m"},
		"macro_script":   {"name": "m"},
		"delete_macro":   {"name": "m"},
		"batch":          {"actions": []any{map[string]any{"type": "wait", "ms": 1}}},
		"mask":           {"dim": 0.4},
		"clipboard":      {"op": "get"},
		"focus_window":   {"title": "x"},
		"outline_window": {"title": "x"},
	}
	seen := map[string]bool{}
	for _, raw := range toolList() {
		name, _ := raw["name"].(string)
		if name == "" {
			t.Fatal("a tool has no name")
		}
		if seen[name] {
			t.Fatalf("tool %q is listed twice", name)
		}
		seen[name] = true
		args := need[name]
		if args == nil {
			args = map[string]any{}
		}
		encoded, _ := json.Marshal(args)
		if _, err := s.callTool(context.Background(), name, encoded); err != nil && strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("tool %q is listed but not dispatched: %v", name, err)
		}
	}
	t.Logf("%d tools", len(seen))
}

// TestHandshake checks initialize and tools/list over the wire.
func TestHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
`)
	out := &strings.Builder{}
	s := New(srv.URL, "", "test")
	if err := s.Serve(in, out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("answers = %d", len(lines))
	}
	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil {
		t.Fatal(err)
	}
	if init.Result.ProtocolVersion != "2024-11-05" || init.Result.ServerInfo.Name != "computer-use" {
		t.Fatalf("initialize = %+v", init.Result)
	}
	var list struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Result.Tools) < 20 {
		t.Fatalf("tools = %d", len(list.Result.Tools))
	}
}
