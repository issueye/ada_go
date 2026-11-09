package processmgr

// 被管进程的控制凭据：每次启动签发、范围受限、停止即回收。
//
// 这一层是 P6 才出现的：scheduler 需要经调度层投递消息，但它不该继承宿主凭据。
// 测试钉住三件事：令牌只在启动时注入、身份与范围由网关决定、停止后令牌立即失效。

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIssuer 记录签发/回收，返回固定地址与可预测的令牌。
type fakeIssuer struct {
	mu       sync.Mutex
	issued   []string
	revoked  []string
	url      string
	disabled bool
}

func (f *fakeIssuer) IssueForProcess(owner, name, workspace string) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disabled {
		return "", ""
	}
	key := owner + "/" + name + "/" + workspace
	f.issued = append(f.issued, key)
	url := f.url
	if url == "" {
		url = "http://127.0.0.1:1"
	}
	return url, "tok-" + key
}

func (f *fakeIssuer) RevokeProcess(owner, name, workspace string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, owner+"/"+name+"/"+workspace)
}

func (f *fakeIssuer) issuedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.issued...)
}

func (f *fakeIssuer) revokedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

// 启动时注入控制凭据；环境里其余内容保持不变。
func TestControlEnvInjectedOnStart(t *testing.T) {
	spawn := &fakeSpawner{}
	issuer := &fakeIssuer{}
	m := newTestManager(t, Options{Spawn: spawn.spawn, Control: issuer})

	ref, err := m.Register("cron-manager", ProcessSpec{
		Name: "scheduler", Runtime: RuntimeNode, Entry: "/plugin/dist/scheduler.js",
		StartMode: StartOnEnable,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := issuer.issuedKeys(); len(got) != 1 || got[0] != "cron-manager/scheduler/" {
		t.Fatalf("签发键 = %v，期望 [cron-manager/scheduler/]", got)
	}
	spec := spawn.specs[0]
	joined := strings.Join(spec.Env, "\n")
	if !strings.Contains(joined, ControlTokenEnv+"=tok-cron-manager/scheduler/") {
		t.Fatalf("启动环境缺少控制令牌: %s", joined)
	}
	if !strings.Contains(joined, ControlURLEnv+"=http://127.0.0.1:1") {
		t.Fatalf("启动环境缺少控制地址: %s", joined)
	}

	if err := m.Stop(ref); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := issuer.revokedKeys(); len(got) != 1 || got[0] != "cron-manager/scheduler/" {
		t.Fatalf("停止后应回收令牌，实得 %v", got)
	}
}

// 未注入 ControlIssuer（CLI 直连）时环境里不该出现控制凭据。
func TestControlEnvAbsentWithoutIssuer(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})
	ref, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh", StartMode: StartOnEnable})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	joined := strings.Join(spawn.specs[0].Env, "\n")
	if strings.Contains(joined, ControlTokenEnv) || strings.Contains(joined, ControlURLEnv) {
		t.Fatalf("没有 ControlIssuer 时不应注入控制凭据: %s", joined)
	}
	_ = m.Unregister(ref)
}

// workspace scope 的签发键带工作区，global 不带。
func TestControlKeyScope(t *testing.T) {
	dir := t.TempDir()
	spawn := &fakeSpawner{}
	issuer := &fakeIssuer{}
	m := newTestManager(t, Options{Spawn: spawn.spawn, Control: issuer})

	if _, err := m.Register("plug", ProcessSpec{
		Name: "ws", Scope: ScopeWorkspace, Workspace: dir, Entry: "/bin/sh", StartMode: StartOnEnable,
	}); err != nil {
		t.Fatalf("Register ws: %v", err)
	}
	if _, err := m.Register("plug", ProcessSpec{
		Name: "global", Entry: "/bin/sh", StartMode: StartOnEnable,
	}); err != nil {
		t.Fatalf("Register global: %v", err)
	}
	keys := issuer.issuedKeys()
	if len(keys) != 2 {
		t.Fatalf("签发次数 = %d，期望 2", len(keys))
	}
	if !strings.HasPrefix(keys[0], "plug/ws/") || !strings.Contains(keys[0], dir) {
		t.Fatalf("workspace 进程的签发键应带工作区，实得 %q", keys[0])
	}
	if keys[1] != "plug/global/" {
		t.Fatalf("global 进程的签发键不应带工作区，实得 %q", keys[1])
	}
}

// Shutdown 也要回收令牌：进程被杀后旧令牌不能残留可用。
func TestControlRevokedOnShutdown(t *testing.T) {
	spawn := &fakeSpawner{}
	issuer := &fakeIssuer{}
	m := newTestManager(t, Options{Spawn: spawn.spawn, Control: issuer})
	if _, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh", StartMode: StartOnEnable}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := issuer.revokedKeys(); len(got) == 0 || got[0] != "plug/s/" {
		t.Fatalf("Shutdown 后应回收令牌，实得 %v", got)
	}
}

// issuer 拒绝签发（没有控制面地址）时，进程仍能启动，只是没有凭据。
func TestControlIssuerEmptyMeansNoToken(t *testing.T) {
	spawn := &fakeSpawner{}
	issuer := &fakeIssuer{disabled: true}
	m := newTestManager(t, Options{Spawn: spawn.spawn, Control: issuer})
	if _, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh", StartMode: StartOnEnable}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	joined := strings.Join(spawn.specs[0].Env, "\n")
	if strings.Contains(joined, ControlTokenEnv) {
		t.Fatalf("issuer 返回空令牌时不应注入: %s", joined)
	}
}

// injectControlEnv 只追加，不覆盖已有的控制变量（避免两个来源打架）。
func TestInjectControlEnvAppends(t *testing.T) {
	base := []string{"PATH=/usr/bin", "TIMEOUT=5"}
	out := injectControlEnv(base, "http://127.0.0.1:9", "tok")
	if len(out) != len(base)+2 {
		t.Fatalf("环境长度 = %d，期望 %d", len(out), len(base)+2)
	}
	for i, kv := range base {
		if out[i] != kv {
			t.Fatalf("第 %d 项被改写: %q", i, out[i])
		}
	}
	if out[len(out)-1] != ControlTokenEnv+"=tok" {
		t.Fatalf("末项 = %q", out[len(out)-1])
	}
	// 空地址或空令牌一律不注入。
	if got := injectControlEnv(base, "", "tok"); len(got) != len(base) {
		t.Fatalf("空地址不应注入，实得 %v", got)
	}
	if got := injectControlEnv(base, "http://x", ""); len(got) != len(base) {
		t.Fatalf("空令牌不应注入，实得 %v", got)
	}
}
