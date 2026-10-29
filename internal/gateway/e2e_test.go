package gateway_test

// Gateway 端到端验收：真实构建 adag 二进制，启动 `adag gateway --fake`，
// 走完「健康检查 → 首页 → 建会话 → 发一轮消息 → 会话页出现回显」的完整 HTTP 链路。
//
// 设计取舍：
//   - 构建一次即可（TestMain 负责，sync.Once 兜底），避免每个用例重复 go build；
//   - 使用 --addr 127.0.0.1:0 让内核分配端口，避免与本机运行中的 Gateway 冲突；
//   - 只通过 stdout 首行 JSON 发现 endpoint/token，与手工验证方式保持一致；
//   - 进程清理封装在平台相关文件里（Windows 用 taskkill /T，Unix 用进程组），
//     避免测试结束后留下 Core 子进程。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	buildBin  string
	buildErr  error
	buildDir  string
)

func TestMain(m *testing.M) {
	// TestMain 里显式解析 flag，testing.Short() 才能在用例内可靠工作。
	flag.Parse()
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

// adagBinary 返回测试用的 adag 可执行文件路径；只构建一次。
// 没有 go 命令或构建失败时跳过（CI 可能只跑预编译产物）。
func adagBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		if _, err := exec.LookPath("go"); err != nil {
			buildErr = fmt.Errorf("找不到 go 命令: %w", err)
			return
		}
		repo, err := findRepoRoot()
		if err != nil {
			buildErr = err
			return
		}
		buildDir, buildErr = os.MkdirTemp("", "adag-e2e-bin-")
		if buildErr != nil {
			return
		}
		name := "adag"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		out := filepath.Join(buildDir, name)
		cmd := exec.Command("go", "build", "-o", out, "./cmd/adag")
		cmd.Dir = repo
		var logs bytes.Buffer
		cmd.Stdout = &logs
		cmd.Stderr = &logs
		if err := cmd.Run(); err != nil {
			buildErr = fmt.Errorf("go build ./cmd/adag 失败: %w\n%s", err, logs.String())
			return
		}
		buildBin = out
	})
	if buildErr != nil {
		t.Skipf("跳过 Gateway E2E：%v", buildErr)
	}
	return buildBin
}

// findRepoRoot 从测试工作目录向上找 go.mod，并确认 cmd/adag 存在。
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "cmd", "adag", "main.go")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("找不到仓库根（go.mod + cmd/adag）")
		}
		dir = parent
	}
}

// syncBuffer 是并发安全的 stderr 收集器：进程写入与测试读取可能同时发生。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// gatewayHandshake 是 gateway 写往 stdout 的首行 JSON（与手工验证格式一致）。
type gatewayHandshake struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
	PID      int    `json:"pid"`
}

type gatewayProc struct {
	cmd         *exec.Cmd
	stdout      io.ReadCloser
	stderr      *syncBuffer
	started     time.Time
	cleanupOnce sync.Once
}

// startGateway 拉起 gateway 子进程；cleanup 在测试结束时统一回收。
func startGateway(t *testing.T, bin, repo, workspace, dataDir string) *gatewayProc {
	t.Helper()
	cmd := exec.Command(bin,
		"gateway",
		"--workspace", workspace,
		"--data-dir", dataDir,
		"--fake",
		"--addr", "127.0.0.1:0",
	)
	// 在仓库根运行，让 defaultProductPath 能找到 products/headless.agent.json。
	cmd.Dir = repo
	// 去掉令牌类环境变量，确保本次 Gateway 自动生成 token 并回显在握手行里。
	cmd.Env = envWithout("ADAG_GATEWAY_TOKEN", "ADAG_CORE_TOKEN")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("创建 gateway stdout 管道失败: %v", err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	configureProcessGroup(cmd)

	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 gateway 失败: %v", err)
	}
	gp := &gatewayProc{cmd: cmd, stdout: stdout, stderr: stderr, started: started}
	t.Cleanup(func() { gp.stop(t) })
	return gp
}

// awaitHandshake 在超时内读取 stdout 首行并解析 endpoint/token。
func (g *gatewayProc) awaitHandshake(timeout time.Duration) (gatewayHandshake, error) {
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		r := bufio.NewReader(io.LimitReader(g.stdout, 64<<10))
		line, err := r.ReadBytes('\n')
		ch <- result{line: line, err: err}
	}()

	select {
	case res := <-ch:
		trimmed := bytes.TrimSpace(res.line)
		if len(trimmed) == 0 {
			if res.err != nil {
				return gatewayHandshake{}, fmt.Errorf("读取 gateway 握手行失败: %w", res.err)
			}
			return gatewayHandshake{}, errors.New("gateway 没有输出握手行")
		}
		var hs gatewayHandshake
		if err := json.Unmarshal(trimmed, &hs); err != nil {
			return gatewayHandshake{}, fmt.Errorf("gateway 握手行不是合法 JSON: %w（原文 %.300s）", err, trimmed)
		}
		if strings.TrimSpace(hs.Endpoint) == "" {
			return gatewayHandshake{}, fmt.Errorf("握手行缺少 endpoint: %.300s", trimmed)
		}
		if strings.TrimSpace(hs.Token) == "" {
			return gatewayHandshake{}, fmt.Errorf("握手行缺少 token（是否设置了 ADAG_GATEWAY_TOKEN？）: %.300s", trimmed)
		}
		return hs, nil
	case <-time.After(timeout):
		return gatewayHandshake{}, fmt.Errorf("等待 gateway 握手行超时（%s）；stderr: %s", timeout, g.stderr.String())
	}
}

// stop 杀掉 gateway 及其子进程，并等待父进程退出，避免端口/文件句柄泄漏。
func (g *gatewayProc) stop(t *testing.T) {
	g.cleanupOnce.Do(func() {
		if g.stdout != nil {
			_ = g.stdout.Close()
		}
		if g.cmd.Process == nil {
			return
		}
		killProcessTree(g.cmd)
		done := make(chan struct{})
		go func() {
			_ = g.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Logf("gateway 进程未在 10s 内退出；stderr: %s", g.stderr.String())
		}
	})
}

// envWithout 复制当前环境并删除指定变量（大小写不敏感）。
func envWithout(names ...string) []string {
	drop := make(map[string]struct{}, len(names))
	for _, n := range names {
		drop[strings.ToUpper(n)] = struct{}{}
	}
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if _, ok := drop[strings.ToUpper(key)]; ok {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func newTestClient() *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		// POST 的 303 要保留下来供断言，不自动跟随。
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// newRequest 构造请求；token 非空时带 Bearer，form 非空时带表单体。
func newRequest(t *testing.T, method, rawURL, token string, form url.Values) *http.Request {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		t.Fatalf("构造请求 %s %s 失败: %v", method, rawURL, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return req
}

// doRequest 执行请求并读完响应体（响应体已关闭，返回状态与文本）。
func doRequest(t *testing.T, client *http.Client, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		t.Fatalf("读取 %s %s 响应失败: %v", req.Method, req.URL, err)
	}
	return resp, string(body)
}

// TestGatewayE2E 是 M2 的垂直切片验收：真实进程、真实 HTTP、真实 Core 子进程。
func TestGatewayE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过 E2E：需要 go build 并拉起真实 Core 子进程")
	}

	bin := adagBinary(t)
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}

	// 工作区必须已存在（Core 对不存在的目录会拒绝启动）。
	workspace := filepath.Join(t.TempDir(), "workspace")
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("创建工作区失败: %v", err)
	}

	gw := startGateway(t, bin, repo, workspace, dataDir)
	hs, err := gw.awaitHandshake(5 * time.Second)
	if err != nil {
		t.Fatalf("%v\nstderr: %s", err, gw.stderr.String())
	}
	startupMS := time.Since(gw.started).Milliseconds()
	endpoint := strings.TrimRight(hs.Endpoint, "/")
	t.Logf("gateway 就绪：endpoint=%s pid=%d", endpoint, hs.PID)

	client := newTestClient()

	// 1. /healthz 免鉴权。
	resp, body := doRequest(t, client, newRequest(t, http.MethodGet, endpoint+"/healthz", "", nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz 状态 = %d，期望 200；正文: %.500s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("/healthz 正文缺少 ok=true: %.500s", body)
	}
	t.Logf("/healthz OK（免鉴权）")

	// 2. 首页：Bearer token，渲染会话列表（这一步会触发 Core spawn）。
	pageStart := time.Now()
	resp, body = doRequest(t, client, newRequest(t, http.MethodGet, endpoint+"/", hs.Token, nil))
	firstPageMS := time.Since(pageStart).Milliseconds()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("首页状态 = %d，期望 200；正文: %.800s\nstderr: %s", resp.StatusCode, body, gw.stderr.String())
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("首页 Content-Type = %q，期望 text/html", ct)
	}
	if !strings.Contains(body, `class="ada-brand"`) || !strings.Contains(body, "ada-go") {
		t.Fatalf("首页未包含品牌标识；正文片段: %.800s", body)
	}
	t.Logf("首页 OK（含品牌，%.0f ms）", float64(firstPageMS))

	// 3. 建会话：表单 title，期望 303 到 /thread/{id}。
	resp, body = doRequest(t, client, newRequest(t, http.MethodPost, endpoint+"/threads", hs.Token,
		url.Values{"title": {"e2e 会话"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /threads 状态 = %d，期望 303；正文: %.800s", resp.StatusCode, body)
	}
	location := resp.Header.Get("Location")
	if !strings.HasPrefix(location, "/threads/") || strings.TrimPrefix(location, "/threads/") == "" {
		t.Fatalf("POST /threads Location = %q，期望 /threads/{id}", location)
	}
	threadPath := location
	t.Logf("建会话 OK：%s", threadPath)

	// 4. 发一轮消息：text=hello e2e。
	resp, body = doRequest(t, client, newRequest(t, http.MethodPost, endpoint+threadPath+"/turn", hs.Token,
		url.Values{"text": {"hello e2e"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST %s/turn 状态 = %d，期望 303；正文: %.800s", threadPath, resp.StatusCode, body)
	}

	// 5. 轮询会话页，直到出现 fake 回显或 10s 超时。
	turnStart := time.Now()
	deadline := turnStart.Add(10 * time.Second)
	var lastStatus int
	var lastBody string
	for {
		resp, body = doRequest(t, client, newRequest(t, http.MethodGet, endpoint+threadPath, hs.Token, nil))
		lastStatus, lastBody = resp.StatusCode, body
		if resp.StatusCode == http.StatusOK && strings.Contains(body, "fake: hello e2e") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("10s 内未在会话页看到回显（最后状态 %d）；正文片段: %.1200s\nstderr: %s",
				lastStatus, lastBody, gw.stderr.String())
		}
		time.Sleep(250 * time.Millisecond)
	}
	turnCompleteMS := time.Since(turnStart).Milliseconds()

	// Core 是否真的被 spawn：Core 启动时会打开/创建 <data-dir>/state.db。
	// 页面能渲染出 fake 回显本身也已证明 Core 在工作；这里再加一条平台无关的物证。
	if _, err := os.Stat(filepath.Join(dataDir, "state.db")); err != nil {
		t.Errorf("未找到 Core 创建的 state.db（%v），可能 Core 并未真正 spawn", err)
	}

	t.Logf("性能基线（本机、fake Core；仅记录，不做阈值断言）：startup_ms=%d first_page_ms=%d turn_complete_ms=%d",
		startupMS, firstPageMS, turnCompleteMS)
}
