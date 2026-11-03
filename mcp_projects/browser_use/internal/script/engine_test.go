package script

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"browseruse/internal/browser"
	"browseruse/internal/config"
)

type fakeExec struct {
	calls []string
}

func (f *fakeExec) Call(ctx context.Context, client, name string, args json.RawMessage) (any, error) {
	f.calls = append(f.calls, name)
	switch name {
	case "status":
		return map[string]any{
			"active_tab": map[string]any{"id": "t1", "url": "https://example.com/", "title": "Example"},
		}, nil
	case "tabs":
		return map[string]any{"tabs": []map[string]any{{"id": "t1", "url": "https://example.com/", "title": "Example"}}}, nil
	case "control":
		return map[string]any{"control": map[string]any{"state": "idle"}}, nil
	default:
		return nil, &browser.Error{Code: browser.CodeNotFound, Message: "no " + name}
	}
}

func scriptConfig() config.ScriptConfig {
	cfg := config.Default().Script
	cfg.KeepSources = false
	cfg.ReleaseOnFinish = false
	return cfg
}

func TestRunReturnsValue(t *testing.T) {
	f := &fakeExec{}
	res, err := Run(context.Background(), f, scriptConfig(), Request{
		Source: `console.log("hi", 1); return { sum: 20 + 22, title: await browser.active().then(p => p.title()) };`,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.Result.(map[string]any)
	if !ok || m["sum"].(float64) != 42 || m["title"] != "Example" {
		t.Fatalf("result = %#v", res.Result)
	}
	if len(res.Console) != 1 || res.Console[0].Text != "hi 1" {
		t.Fatalf("console = %#v", res.Console)
	}
	if len(res.Actions) != 2 || res.Actions[0].Tool != "status" || res.Actions[1].Tool != "tabs" {
		t.Fatalf("actions = %#v", res.Actions)
	}
}

func TestRunTypeScript(t *testing.T) {
	f := &fakeExec{}
	res, err := Run(context.Background(), f, scriptConfig(), Request{
		Language: "ts",
		Source:   `const n: number = 7; return { n, ok: true };`,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Result.(map[string]any)
	if m["n"].(float64) != 7 || m["ok"] != true {
		t.Fatalf("result = %#v", res.Result)
	}
}

func TestRunSyntaxError(t *testing.T) {
	_, err := Run(context.Background(), &fakeExec{}, scriptConfig(), Request{Source: `return (`})
	var be *browser.Error
	if !errors.As(err, &be) || be.Code != browser.CodeScriptSyntax {
		t.Fatalf("err = %v", err)
	}
}

func TestRunTimeout(t *testing.T) {
	start := time.Now()
	_, err := Run(context.Background(), &fakeExec{}, scriptConfig(), Request{
		Source:  `for (;;) {}`,
		Timeout: 150 * time.Millisecond,
	})
	var be *browser.Error
	if !errors.As(err, &be) || be.Code != browser.CodeScriptTimeout {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout took too long")
	}
}

func TestRunActionLimit(t *testing.T) {
	cfg := scriptConfig()
	cfg.MaxActions = 2
	_, err := Run(context.Background(), &fakeExec{}, cfg, Request{
		Source: `for (let i = 0; i < 5; i++) { await browser.status(); } return 1;`,
	})
	var be *browser.Error
	if !errors.As(err, &be) || be.Code != browser.CodeScriptActions {
		t.Fatalf("err = %v", err)
	}
}

func TestRunNestedScriptRejected(t *testing.T) {
	_, err := Run(context.Background(), &fakeExec{}, scriptConfig(), Request{
		Source: `await browser.call("script", { source: "return 1" });`,
	})
	var be *browser.Error
	if !errors.As(err, &be) || be.Code != browser.CodeScriptUnsupported {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(be.Message, "scripts cannot call script") {
		t.Fatalf("message = %q", be.Message)
	}
}
