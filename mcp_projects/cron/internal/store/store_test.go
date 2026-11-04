package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	task := Task{
		ID: id, Name: "每天早报", Enabled: true, Cron: "0 9 * * *",
		Workspace: "/tmp/report-ws",
		Action:    Action{Type: ActionAgent, Text: "总结昨天的工作"},
	}
	if err := Save(path, []Task{task}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, warns, err := Load(path)
	if err != nil || len(warns) != 0 {
		t.Fatalf("Load: %v warns=%v", err, warns)
	}
	if len(loaded) != 1 || loaded[0].ID != id ||
		loaded[0].Action.Text != "总结昨天的工作" || loaded[0].Workspace != "/tmp/report-ws" {
		t.Fatalf("往返不一致: %+v", loaded)
	}
}

func TestLoadCorruptFileBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("{不是 JSON"), 0o600); err != nil {
		t.Fatalf("写坏文件: %v", err)
	}
	tasks, warns, err := Load(path)
	if err != nil {
		t.Fatalf("Load 应按空表启动而不是报错: %v", err)
	}
	if len(tasks) != 0 || len(warns) != 1 {
		t.Fatalf("期望空表 + 一条警告: tasks=%d warns=%v", len(tasks), warns)
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("损坏文件应被备份，glob=%v", matches)
	}
}

func TestUpdateConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	lock := filepath.Join(dir, LockName)
	id, _ := NewID()
	if err := Save(path, []Task{{ID: id, Name: "n", Enabled: true, Cron: "* * * * *", Action: Action{Type: ActionAgent, Text: "t"}}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := Update(path, lock, 5*time.Second, func(all *[]Task) error {
				for j := range *all {
					if (*all)[j].ID == id {
						AppendRun(&(*all)[j], Run{At: time.Now().UTC().Format(time.RFC3339), Status: RunStarted})
					}
				}
				return nil
			})
			if err != nil {
				t.Errorf("Update: %v", err)
			}
		}()
	}
	wg.Wait()
	loaded, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 1 || len(loaded[0].Runs) != 2 {
		t.Fatalf("并发追加丢失: %+v", loaded)
	}
}

func TestValidate(t *testing.T) {
	base := Task{ID: "crn_x", Name: "n", Enabled: true, Cron: "0 9 * * *"}
	cases := []struct {
		label  string
		mutate func(*Task)
		wantOK bool
	}{
		{"旧 HTTP 动作被拒绝", func(t *Task) { t.Action = Action{Type: ActionHTTP} }, false},
		{"合法 agent", func(t *Task) { t.Action = Action{Type: ActionAgent, Text: "总结昨天"} }, true},
		{"agent 空 text", func(t *Task) { t.Action = Action{Type: ActionAgent, Text: " "} }, false},
		{"未知类型", func(t *Task) { t.Action = Action{Type: "shell"} }, false},
		{"指定工作区", func(t *Task) {
			t.Workspace = "/tmp/ws"
			t.Action = Action{Type: ActionAgent, Text: "t"}
		}, true},
		{"工作区超长", func(t *Task) {
			t.Workspace = strings.Repeat("a", MaxWorkspaceBytes+1)
			t.Action = Action{Type: ActionAgent, Text: "t"}
		}, false},
		{"坏 cron", func(t *Task) { t.Cron = "0 9 * *" }, false},
	}
	for _, tc := range cases {
		task := base
		tc.mutate(&task)
		err := Validate(&task)
		if tc.wantOK != (err == nil) {
			t.Errorf("%s: err=%v 期望 ok=%v", tc.label, err, tc.wantOK)
		}
	}
}

func TestEnvelopeShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := Save(path, nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	raw, _ := os.ReadFile(path)
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("信封不是对象: %v", err)
	}
	if _, ok := env["version"]; !ok {
		t.Fatalf("信封缺 version 字段")
	}
	if _, ok := env["tasks"]; !ok {
		t.Fatalf("信封缺 tasks 字段")
	}
}
