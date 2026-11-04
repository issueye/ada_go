package cronstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustSchedule(t *testing.T, expr string) *Schedule {
	t.Helper()
	s, err := ParseCron(expr)
	if err != nil {
		t.Fatalf("ParseCron(%q) = %v", expr, err)
	}
	return s
}

func TestParseCronErrors(t *testing.T) {
	bad := []string{
		"", "0 9 * *", "0 9 * * * *", // 段数
		"60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *", "* * * 13 *", "* * * * 8", // 越界
		"*/0 * * * *", "a * * * *", "1- * * * *", "5-1 * * * *", "*,, * * * *", // 形状
	}
	for _, expr := range bad {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q) 应报错", expr)
		}
	}
}

func TestNextBasics(t *testing.T) {
	// 用 UTC 构造确定性时间；解析按时间自身时区取字段，语义一致。
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("构造时间 %s: %v", s, err)
		}
		return v
	}
	cases := []struct {
		expr  string
		after string
		want  string
	}{
		{"0 9 * * *", "2026-11-03T10:00:00Z", "2026-11-04T09:00:00Z"},    // 次日
		{"0 9 * * *", "2026-11-03T08:30:00Z", "2026-11-03T09:00:00Z"},    // 当天
		{"*/15 * * * *", "2026-11-03T08:47:00Z", "2026-11-03T09:00:00Z"}, // 步进跨小时
		{"30 9 1 * *", "2026-11-03T08:00:00Z", "2026-12-01T09:30:00Z"},   // 每月 1 号
		{"0 0 * * 1", "2026-11-03T00:00:00Z", "2026-11-09T00:00:00Z"},    // 每周一（11-03 是周二）
		{"0 12 * * 7", "2026-11-03T00:00:00Z", "2026-11-08T12:00:00Z"},   // 7=周日
		{"0 9 * * *", "2026-11-03T09:00:00Z", "2026-11-04T09:00:00Z"},    // 不含当前分钟
	}
	for _, tc := range cases {
		got := mustSchedule(t, tc.expr).Next(at(tc.after))
		if got.Format(time.RFC3339) != tc.want {
			t.Errorf("Next(%q, after %s) = %s，期望 %s", tc.expr, tc.after, got.Format(time.RFC3339), tc.want)
		}
	}
}

func TestNextVixieDomDowRule(t *testing.T) {
	// dom 与 dow 都受限：取「或」（Vixie）。11-03（周二）之后，
	// dom=5（周四 11-05）先到，OR 规则应命中它而不是等到周五。
	s := mustSchedule(t, "0 9 5 * 5")
	got := s.Next(time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC))
	if want := "2026-11-05T09:00:00Z"; got.Format(time.RFC3339) != want {
		t.Errorf("OR 规则 Next = %s，期望 %s", got.Format(time.RFC3339), want)
	}
	// 仅 dom 受限：必须是 5 号（周二不算）。
	s = mustSchedule(t, "0 9 5 * *")
	got = s.Next(time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC))
	if want := "2026-11-05T09:00:00Z"; got.Format(time.RFC3339) != want {
		t.Errorf("dom 规则 Next = %s，期望 %s", got.Format(time.RFC3339), want)
	}
	// 仅 dow 受限：必须是周五。
	s = mustSchedule(t, "0 9 * * 5")
	got = s.Next(time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC))
	if want := "2026-11-06T09:00:00Z"; got.Format(time.RFC3339) != want {
		t.Errorf("dow 规则 Next = %s，期望 %s", got.Format(time.RFC3339), want)
	}
}

func TestNextImpossible(t *testing.T) {
	// 2 月 30 日不存在：4 年内搜不到应返回零值而不是死循环。
	s := mustSchedule(t, "0 0 30 2 *")
	if got := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !got.IsZero() {
		t.Errorf("不存在的日期应返回零值，得到 %s", got)
	}
}

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
	if err := SaveFile(path, []Task{task}); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	loaded, warns, err := LoadFile(path)
	if err != nil || len(warns) != 0 {
		t.Fatalf("LoadFile: %v, warns=%v", err, warns)
	}
	if len(loaded) != 1 || loaded[0].ID != id ||
		loaded[0].Action.Text != "总结昨天的工作" || loaded[0].Workspace != "/tmp/report-ws" {
		t.Fatalf("往返不一致: %+v", loaded)
	}
}

func TestLoadSkipsInvalidTasks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	raw := fmt.Sprintf(`{"version":1,"tasks":[
		{"id":"crn_aaaa","name":"旧 HTTP 任务","enabled":true,"cron":"0 9 * * *","action":{"type":"http","method":"GET","url":"https://x.com"}},
		{"id":"crn_bbbb","name":"好任务","enabled":true,"cron":"0 9 * * *","action":{"type":"agent","text":"早报"}}]}`)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("写测试文件: %v", err)
	}
	loaded, warns, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(warns) != 1 || len(loaded) != 1 || loaded[0].Name != "好任务" {
		t.Fatalf("应跳过旧 HTTP 任务保留 AI 任务: tasks=%+v warns=%v", loaded, warns)
	}
	if !strings.Contains(warns[0], "HTTP 动作已移除") {
		t.Fatalf("跳过原因应说明 HTTP 动作已移除: %v", warns)
	}
}

func TestLoadCorruptFileBacksUp(t *testing.T) {
	// 与 cron 二进制同一容错：损坏文件备份为 *.corrupt-* 后按空表启动，
	// 不让管理页/调度器因坏文件拒启，原文件保住即可排查。
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("{不是 JSON"), 0o600); err != nil {
		t.Fatalf("写坏文件: %v", err)
	}
	tasks, warns, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile 应按空表启动而不是报错: %v", err)
	}
	if len(tasks) != 0 || len(warns) != 1 {
		t.Fatalf("期望空表 + 一条警告: tasks=%d warns=%v", len(tasks), warns)
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("损坏文件应被备份，glob=%v", matches)
	}
}

func TestUpdateBacksUpBeforeDroppingInvalid(t *testing.T) {
	// Load 跳过的非法任务会在下一次 Update 落盘时从文件里消失；
	// 落盘前必须先把原文件备份下来，不能无声清除。
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	lock := filepath.Join(dir, LockName)
	raw := `{"version":1,"tasks":[
		{"id":"crn_bad1","name":"坏任务","enabled":true,"cron":"0 9 * * *","action":{"type":"http"}},
		{"id":"crn_good","name":"好任务","enabled":true,"cron":"0 9 * * *","action":{"type":"agent","text":"早报"}}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("写测试文件: %v", err)
	}
	err := Update(path, lock, 5*time.Second, func(tasks *[]Task) error {
		for j := range *tasks {
			if (*tasks)[j].ID == "crn_good" {
				AppendRun(&(*tasks)[j], Run{At: time.Now().UTC().Format(time.RFC3339), Status: RunStarted})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	matches, _ := filepath.Glob(path + ".backup-*")
	if len(matches) != 1 {
		t.Fatalf("落盘前应留一份备份，glob=%v", matches)
	}
	bak, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(bak), "crn_bad1") {
		t.Fatalf("备份应保留被跳过的任务: %s", bak)
	}
	loaded, warns, err := LoadFile(path)
	if err != nil || len(loaded) != 1 || len(warns) != 0 {
		t.Fatalf("落盘后坏任务应消失: tasks=%+v warns=%v err=%v", loaded, warns, err)
	}
	// 坏任务已不在文件里：再次 Update 不应再产生新备份。
	if err := Update(path, lock, 5*time.Second, func(tasks *[]Task) error { return nil }); err != nil {
		t.Fatalf("第二次 Update: %v", err)
	}
	matches, _ = filepath.Glob(path + ".backup-*")
	if len(matches) != 1 {
		t.Fatalf("无跳过时不应新增备份，glob=%v", matches)
	}
}

func TestValidateTask(t *testing.T) {
	base := Task{ID: "crn_x", Name: "n", Enabled: true, Cron: "0 9 * * *"}
	cases := []struct {
		label  string
		mutate func(*Task)
		wantOK bool
	}{
		{"旧 HTTP 动作被拒绝", func(t *Task) { t.Action = Action{Type: ActionHTTP} }, false},
		{"合法 agent", func(t *Task) { t.Action = Action{Type: ActionAgent, Text: "总结昨天"} }, true},
		{"agent 空 text", func(t *Task) { t.Action = Action{Type: ActionAgent, Text: "  "} }, false},
		{"提示词超长", func(t *Task) { t.Action = Action{Type: ActionAgent, Text: strings.Repeat("x", MaxTextBytes+1)} }, false},
		{"未知类型", func(t *Task) { t.Action = Action{Type: "shell"} }, false},
		{"指定工作区", func(t *Task) {
			t.Workspace = "/tmp/ws"
			t.Action = Action{Type: ActionAgent, Text: "t"}
		}, true},
		{"工作区含控制字符", func(t *Task) {
			t.Workspace = "/tmp/\x00ws"
			t.Action = Action{Type: ActionAgent, Text: "t"}
		}, false},
		{"工作区超长", func(t *Task) {
			t.Workspace = strings.Repeat("a", MaxWorkspaceBytes+1)
			t.Action = Action{Type: ActionAgent, Text: "t"}
		}, false},
		{"空名", func(t *Task) { t.Name = " " }, false},
		{"任务名含控制字符", func(t *Task) { t.Name = "早报\n第二轮" }, false},
		{"空 id", func(t *Task) { t.ID = "" }, false},
		{"坏 cron", func(t *Task) { t.Cron = "0 9 * *" }, false},
	}
	for _, tc := range cases {
		task := base
		tc.mutate(&task)
		err := ValidateTask(&task)
		if tc.wantOK != (err == nil) {
			t.Errorf("%s: err=%v，期望 ok=%v", tc.label, err, tc.wantOK)
		}
	}
}

func TestUpdateConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	lock := filepath.Join(dir, LockName)
	id, _ := NewID()
	if err := SaveFile(path, []Task{{ID: id, Name: "n", Enabled: true, Cron: "* * * * *", Action: Action{Type: ActionAgent, Text: "t"}}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 两个 goroutine 各追加一条 run，锁应保证两条都在。
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := Update(path, lock, 5*time.Second, func(tasks *[]Task) error {
				for j := range *tasks {
					if (*tasks)[j].ID == id {
						AppendRun(&(*tasks)[j], Run{At: time.Now().UTC().Format(time.RFC3339), Status: RunStarted})
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
	loaded, _, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(loaded) != 1 || len(loaded[0].Runs) != 2 {
		t.Fatalf("并发追加丢失: %+v", loaded)
	}
}

func TestAppendRunCaps(t *testing.T) {
	task := Task{ID: "crn_x", Name: "n", Cron: "* * * * *", Action: Action{Type: ActionAgent, Text: "t"}}
	for i := 0; i < MaxRunsPerTask+10; i++ {
		AppendRun(&task, Run{At: fmt.Sprintf("2026-01-01T00:%02d:00Z", i%60), Status: RunStarted})
	}
	if len(task.Runs) != MaxRunsPerTask {
		t.Fatalf("runs = %d，期望上限 %d", len(task.Runs), MaxRunsPerTask)
	}
	// 保留的是最新的。
	if task.Runs[len(task.Runs)-1].At == fmt.Sprintf("2026-01-01T00:%02d:00Z", 0) {
		t.Errorf("最旧的一条应被丢弃")
	}
}

func TestSaveFileRejectsBad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	tooMany := make([]Task, MaxTasks+1)
	for i := range tooMany {
		tooMany[i] = Task{ID: fmt.Sprintf("crn_%03d", i), Name: "n", Enabled: true, Cron: "* * * * *", Action: Action{Type: ActionAgent, Text: "t"}}
	}
	if err := SaveFile(path, tooMany); err == nil {
		t.Errorf("超过任务上限应报错")
	}
	// JSON 形状回归：信封必须带 version 与 tasks 字段名。
	if err := SaveFile(path, nil); err != nil {
		t.Fatalf("SaveFile(nil): %v", err)
	}
	raw, _ := os.ReadFile(path)
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("信封不是对象: %v", err)
	}
	if _, ok := env["version"]; !ok {
		t.Errorf("信封缺 version 字段")
	}
	if _, ok := env["tasks"]; !ok {
		t.Errorf("信封缺 tasks 字段")
	}
}
