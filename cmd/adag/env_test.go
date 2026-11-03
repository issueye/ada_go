package main

// adag env 命令的离线验收：写入/列出/删除、工作区覆盖全局、非法键拒绝与 JSON 输出。
// 只碰 env.json 文件，不需要 Core。

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnvCommandRoundtrip(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("ADAGO_HOME", home)

	var out bytes.Buffer
	runEnv := func(args ...string) string {
		t.Helper()
		out.Reset()
		if err := run(append([]string{"env"}, args...), &out, io.Discard); err != nil {
			t.Fatalf("adag env %s: %v", strings.Join(args, " "), err)
		}
		return out.String()
	}

	runEnv("set", "GLOBAL_A=1", "GLOBAL_B=two words")
	globalPath := filepath.Join(home, envFileForTest)
	raw, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("全局 env.json 未写入: %v", err)
	}
	if !strings.Contains(string(raw), `"GLOBAL_A": "1"`) {
		t.Fatalf("全局文件内容: %s", raw)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(globalPath); err != nil {
			t.Fatal(err)
		} else if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("全局 env.json 权限 = %o，期望 600", perm)
		}
	}

	if got := runEnv("list"); got != "GLOBAL_A=1\nGLOBAL_B=two words\n" {
		t.Fatalf("list 输出 = %q", got)
	}

	// 工作区覆盖全局；只写工作区文件。
	runEnv("set", "--workspace", ws, "GLOBAL_A=ws", "WORKSPACE_X=9")
	merged := runEnv("list", "--workspace", ws)
	for _, want := range []string{"GLOBAL_A=ws", "GLOBAL_B=two words", "WORKSPACE_X=9"} {
		if !strings.Contains(merged, want) {
			t.Fatalf("合并列表缺少 %q:\n%s", want, merged)
		}
	}

	// 删除工作区变量不影响全局。
	if got := runEnv("unset", "--workspace", ws, "WORKSPACE_X"); !strings.Contains(got, "已移除工作区环境变量 WORKSPACE_X") {
		t.Fatalf("unset 输出 = %q", got)
	}
	if got := runEnv("list", "--workspace", ws); strings.Contains(got, "WORKSPACE_X") {
		t.Fatalf("删除后仍列出 WORKSPACE_X:\n%s", got)
	}
	if got := runEnv("list"); !strings.Contains(got, "GLOBAL_A=1") {
		t.Fatalf("全局变量被工作区删除影响: %q", got)
	}
	if got := runEnv("unset", "MISSING"); !strings.Contains(got, "未设置：MISSING") {
		t.Fatalf("删除不存在的变量: %q", got)
	}

	// 值里的 = 原样保留。
	runEnv("set", "WITH_EQ=a=b c")
	if got := runEnv("list"); !strings.Contains(got, "WITH_EQ=a=b c") {
		t.Fatalf("值里的 = 被截断: %q", got)
	}

	// JSON 输出可解析。
	var decoded struct {
		Vars map[string]string `json:"vars"`
	}
	out.Reset()
	if err := run([]string{"env", "list", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON 输出不可解析: %v\n%s", err, out.String())
	}
	if decoded.Vars["GLOBAL_A"] != "1" || decoded.Vars["WITH_EQ"] != "a=b c" {
		t.Fatalf("JSON 内容: %v", decoded.Vars)
	}

	// 非法键被拒绝且不落盘。
	before, _ := os.ReadFile(globalPath)
	out.Reset()
	if err := run([]string{"env", "set", "BAD-KEY=x"}, &out, io.Discard); err == nil {
		t.Fatal("非法键应报错")
	}
	after, _ := os.ReadFile(globalPath)
	if string(before) != string(after) {
		t.Fatal("非法键不应改写文件")
	}

	// 缺参数给出可读用法。
	if err := run([]string{"env", "set"}, io.Discard, io.Discard); err == nil {
		t.Fatal("缺少 KEY=VALUE 应报错")
	}
	if err := run([]string{"env"}, io.Discard, io.Discard); err == nil {
		t.Fatal("缺少子命令应报错")
	}
}

const envFileForTest = "env.json"
