//go:build windows

package script

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"computeruse/internal/engine"
	"computeruse/internal/macro"
)

// newRunner returns a runner over an engine that injects no input, so that
// the tests never move the pointer.
func newRunner(t *testing.T) (*Runner, *engine.Engine) {
	t.Helper()
	eng, err := engine.New(engine.Config{AllowInput: false})
	if err != nil {
		t.Fatal(err)
	}
	store := macro.NewStore(filepath.Join(t.TempDir(), "macros.json"))
	eng.SetMacroStore(store)
	t.Cleanup(eng.Close)
	return New(eng, "test"), eng
}

func TestScriptBasics(t *testing.T) {
	r, _ := newRunner(t)
	res, err := r.Run(Request{
		Source: `
			cu.log("hello", 42);
			console.log("from console");
			const s = cu.state();
			const c = cu.cursor();
			cu.wait(5);
			({ displays: s.displays.length, x: c.x, y: c.y, arg: args.who });
		`,
		Args: map[string]any{"who": "tester"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("script failed: %s", res.Error)
	}
	m, ok := res.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is %T", res.Result)
	}
	if m["arg"] != "tester" {
		t.Errorf("args.who = %v", m["arg"])
	}
	if n := toInt(m["displays"]); n < 1 {
		t.Errorf("displays = %v", m["displays"])
	}
	if len(res.Output) != 2 || !strings.Contains(res.Output[0], "hello 42") {
		t.Errorf("output = %v", res.Output)
	}
	if res.DurationMS < 0 {
		t.Errorf("duration = %d", res.DurationMS)
	}
}

func TestScriptReportsGoErrors(t *testing.T) {
	r, _ := newRunner(t)
	res, err := r.Run(Request{Source: `
		let message = "";
		try { cu.click(10, 10); } catch (e) { message = String(e.message || e); }
		message;
	`})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("script failed: %s", res.Error)
	}
	msg, _ := res.Result.(string)
	if !strings.Contains(msg, "read-only") {
		t.Fatalf("click error = %q, want the read-only message", msg)
	}
}

func TestScriptSyntaxError(t *testing.T) {
	r, _ := newRunner(t)
	res, err := r.Run(Request{Source: "const = ;"})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !strings.Contains(res.Error, "SyntaxError") {
		t.Fatalf("result = %+v", res)
	}
}

func TestScriptTimeout(t *testing.T) {
	r, _ := newRunner(t)
	start := time.Now()
	res, err := r.Run(Request{Source: `for(;;){}`, TimeoutMS: 200})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("the script was not stopped")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if !strings.Contains(res.Error, "longer than") {
		t.Fatalf("error = %q", res.Error)
	}
}

func TestScriptRecording(t *testing.T) {
	r, eng := newRunner(t)
	res, err := r.Run(Request{Source: `cu.wait(5); cu.wait(5); "done"`})
	if err != nil || !res.OK {
		t.Fatalf("script: %+v %v", res, err)
	}
	// A run can record the actions of the script.
	res, err = r.Run(Request{Source: `cu.wait(5); cu.click(1, 1); "x"`, Record: "probe"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Macro != "probe" {
		t.Fatalf("macro = %q (error %q)", res.Macro, res.Error)
	}
	m, ok := eng.Macros().Get("probe")
	if !ok {
		t.Fatal("the macro was not saved")
	}
	if len(m.Steps) != 1 || m.Steps[0].Type != "wait" {
		t.Fatalf("steps = %+v", m.Steps)
	}
	// Recording from inside the script works too.
	res, err = r.Run(Request{Source: `
		cu.recordStart("inner");
		cu.wait(3);
		const m = cu.recordStop();
		const list = cu.macroList();
		({name: m.name, steps: m.steps, count: list.length});
	`})
	if err != nil || !res.OK {
		t.Fatalf("script: %+v %v", res, err)
	}
	inner, _ := res.Result.(map[string]any)
	if inner["name"] != "inner" || toInt(inner["steps"]) != 1 {
		t.Fatalf("inner = %+v", inner)
	}
	if _, ok := eng.Macros().Get("inner"); !ok {
		t.Fatal("the inner recording was not saved")
	}
}

func TestScriptReplaysMacro(t *testing.T) {
	r, eng := newRunner(t)
	// A macro of waits replays without input.
	m := &macro.Macro{Name: "waits", Steps: []macro.Step{{Type: "wait", Ms: 10}, {Type: "wait", Ms: 10}}}
	if err := eng.Macros().Save(m); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(Request{Source: `cu.macroRun("waits", {speed: 4}); "replayed"`})
	if err != nil || !res.OK {
		t.Fatalf("script: %+v %v", res, err)
	}
	src, ok := eng.Macros().Get("waits")
	if !ok {
		t.Fatal("the macro went missing")
	}
	js := macro.Script(src)
	if !strings.Contains(js, `cu.wait(10)`) {
		t.Fatalf("script = %q", js)
	}
}

func TestMacroStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "macros.json")
	s := macro.NewStore(path)
	if err := s.Save(&macro.Macro{Name: "a", Steps: []macro.Step{{Type: "wait", Ms: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(&macro.Macro{Name: "b", Steps: []macro.Step{{Type: "key", Keys: "enter"}}}); err != nil {
		t.Fatal(err)
	}
	if got := len(s.List()); got != 2 {
		t.Fatalf("list = %d", got)
	}
	// The file is read back by another store.
	s2 := macro.NewStore(path)
	if _, ok := s2.Get("a"); !ok {
		t.Fatal("a was not saved")
	}
	if !strings.Contains(macro.Script(mustGet(t, s2, "b")), `cu.key("enter")`) {
		t.Fatal("the script form is wrong")
	}
	if !s2.Delete("a") {
		t.Fatal("a was not deleted")
	}
	if _, ok := s2.Get("a"); ok {
		t.Fatal("a is still there")
	}
	if err := s2.Save(&macro.Macro{Name: "bad name!", Steps: nil}); err == nil {
		t.Fatal("a bad name was accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func mustGet(t *testing.T, s *macro.Store, name string) *macro.Macro {
	t.Helper()
	m, ok := s.Get(name)
	if !ok {
		t.Fatalf("no macro %q", name)
	}
	return m
}
