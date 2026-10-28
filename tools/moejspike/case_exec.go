package main

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/MoeForge/moejs/webapi"
)

func init() {
	register("exec", "child_process：上游缺失面 + 宿主窄适配可行性（E-5）", runExec)
}

// execSample 给出一组跨平台的 execFile 取样命令（都不经过 shell）。
func execSample(kind string) (string, []string, bool) {
	if runtime.GOOS == "windows" {
		switch kind {
		case "ok":
			return "cmd.exe", []string{"/c", "echo", "spike-out"}, true
		case "fail":
			return "cmd.exe", []string{"/c", "exit", "7"}, true
		case "slow":
			return "cmd.exe", []string{"/c", "ping", "-n", "6", "127.0.0.1"}, true
		case "big":
			return "cmd.exe", []string{"/c", "echo", strings.Repeat("A", 4096)}, true
		case "env":
			return "cmd.exe", []string{"/c", "set"}, true
		}
		return "", nil, false
	}
	switch kind {
	case "ok":
		return "/bin/echo", []string{"spike-out"}, true
	case "fail":
		return "/usr/bin/env", []string{"false"}, true
	case "slow":
		return "/bin/sleep", []string{"6"}, true
	case "big":
		return "/bin/echo", []string{strings.Repeat("A", 4096)}, true
	case "env":
		return "/usr/bin/env", nil, true
	}
	return "", nil, false
}

// allowProgram 是宿主 Run.Allow 的最小形态：只认程序名白名单。
var allowProgram = map[string]bool{"cmd.exe": true, "/bin/echo": true, "/bin/sleep": true, "/usr/bin/env": true}

const childShim = `const b = globalThis.__childProcess;
export const execFile = b.execFile;
export const execFileSync = b.execFileSync;
export default b;
`

const execProbe = `import child, { execFile, execFileSync } from "node:child_process";
const out = {};
out.members = Object.keys(child);
out.noSpawn = typeof child.spawn;
out.noExec = typeof child.exec;

const run = (fn) => { try { return {ok: fn()} } catch (e) { return {code: e.code, name: e.name, message: e.message} } };

out.sync = run(() => execFileSync(__cmdName("ok"), __cmdArgs("ok"), {cwd: "/workspace"}));
out.syncFail = run(() => execFileSync(__cmdName("fail"), __cmdArgs("fail"), {}));
out.shellTrue = run(() => execFileSync(__cmdName("ok"), __cmdArgs("ok"), {shell: true}));
out.badProgram = run(() => execFileSync("/etc/passwd", [], {}));
out.bigBuffer = run(() => execFileSync(__cmdName("big"), __cmdArgs("big"), {maxBuffer: 1024}));
out.cwdEscape = run(() => execFileSync(__cmdName("ok"), __cmdArgs("ok"), {cwd: "/etc"}));
out.envOnly = run(() => execFileSync(__cmdName("env"), __cmdArgs("env"), {env: {KEEP: "1"}}));

out.timeout = await execFile(__cmdName("slow"), __cmdArgs("slow"), {timeout: 400})
  .then((v) => ({unexpected: v.code}), (e) => ({code: e.code, killed: e.killed}));
out.async = await execFile(__cmdName("ok"), __cmdArgs("ok"), {});

const ac = new AbortController();
const p = execFile(__cmdName("slow"), __cmdArgs("slow"), {signal: ac.signal, timeout: 5000});
ac.abort(new Error("轮次取消"));
out.cancel = await p.then((v) => ({unexpected: v.code}), (e) => ({name: e.name, code: e.code, message: e.message}));
out.afterKill = __children();
__report(JSON.stringify(out));
`

func runExec(r *recorder) {
	// 1) node:child_process 是否存在
	o := runJS(js(`import { execFile, spawn, fork, exec } from "node:child_process";
__report(JSON.stringify({execFile: typeof execFile, spawn: typeof spawn, fork: typeof fork, exec: typeof exec}));`), cfg{useNode: true})
	r.add(ifElse(o.linkErr != nil, vGap, vDev), "E-5/node:child_process",
		"6.12.2、6.13：只有 execFile/execFileSync（无 shell）；spawn/fork/exec 不安装",
		o.actual(),
		"alpha.11 根本没有 child_process：模块整体 LINK-FAIL。spawn/fork/exec 是「不存在」而不是「存在但能绕过」——没有绕过面，但 execFile 得 ada-go 自己造")

	// 2) 是否存在第二条命令面
	o = runJS(js(`const out = {};
for (const n of ["Worker", "child_process", "process"]) { out[n] = typeof globalThis[n] }
out.workerThreads = await import("node:worker_threads").then(() => "ok", (e) => String(e.message))
out.net = await import("node:net").then(() => "ok", (e) => String(e.message))
__report(JSON.stringify(out));`), cfg{useNode: true, web: &webapi.Options{}})
	r.add(ifElse(!strings.Contains(o.report, `"ok"`), vMatch, vDev), "6.13/命令面唯一性",
		"6.10、6.13：插件不能通过 Node 模块拿到第二条访问路径",
		o.actual(),
		"webapi 只有 fetch；node 包只有 fs/buffer；worker_threads 与 net 都链接失败。命令执行只剩宿主装的那一条路")

	// 3) 宿主自建受限模块：跑通 execFile/execFileSync 的全部 E-5 语义
	st := newChildState()
	tx, err := makeTree("exec", 0)
	if err != nil {
		r.add(vNa, "夹具-exec", "建工作区（cwd 映射需要）", err.Error(), "宿主版 execFile 探针未跑")
		return
	}
	o = runJS(execProbe, cfg{
		useNode: true,
		web:     &webapi.Options{},
		virtual: map[string]string{"node:child_process": childShim},
		globals: func(h *hostContext) { installChildProcess(h, st, tx.Root) },
	})
	report := o.js()
	checks := map[string]bool{
		"同步成功":              strings.Contains(report, `"sync":{"ok"`),
		"非零退出码":             strings.Contains(report, `"syncFail":{"code":7`),
		"shell 被拒":          strings.Contains(report, `"shellTrue":{"code":"ERR_ACCESS_DENIED"`),
		"程序名白名单":            strings.Contains(report, `"badProgram":{"code":"ERR_ACCESS_DENIED"`),
		"maxBuffer 超限":      strings.Contains(report, "ERR_CHILD_PROCESS_STDIO_MAXBUFFER"),
		"cwd 越界被拒":          strings.Contains(report, `"cwdEscape":{"code":"ERR_ACCESS_DENIED"`),
		"timeout→ETIMEDOUT": strings.Contains(report, "ETIMEDOUT"),
		"取消→AbortError":     strings.Contains(report, `"name":"AbortError"`),
		"取消后进程树为空":          strings.Contains(report, `"afterKill":0`),
	}
	var missed []string
	for k, v := range checks {
		if !v {
			missed = append(missed, k)
		}
	}
	verdict := vHost
	if len(missed) > 0 {
		report += "\n         未达成: " + strings.Join(missed, "、")
		verdict = vDev
	}
	r.add(verdict, "E-5/宿主窄适配-execFile",
		"6.13：execFile 参数数组、cwd、env、timeout、maxBuffer；取消杀整棵进程树；捕获上限 4MB、超时 120s、exec/execSync 因走 shell 被拒绝",
		o.actual(),
		"宿主用一个 virtual module + 两个 NativeFunc 就装出了 node:child_process："+fmt.Sprintf("无 shell（shell:true 直接 ERR_ACCESS_DENIED）、maxBuffer 夹住、timeout 走 context、取消经 signal→__kill 桥、进程树用 taskkill/kill。结论：E-5 不需要上游也不需要 fork；代价是 ada-go 自己实现并维护这一个模块"))
}
