package host

// 本文件钉「宿主自建模块」的四条线：path / events / util / crypto（12 §6.11.1 E-1 的 pure compute 面）。
//
// 每个用例都是一次真实装配：插件 import "node:*" → 链接期过矩阵 → 模块体求值 →
// 调工具 → 结算。因此它们同时验证三件事：模块表注册、import.meta.binding、
// 以及模块实现本身与 Node 的对齐程度。

import (
	"context"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"runtime"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
	hostnode "github.com/adag/ada-go/internal/plugins/host/node"
	"github.com/adag/ada-go/internal/plugins/host/nodemods"
)

// runOnce 装配一个只注册一个工具 test-plugin 并调用它，返回工具输出文本。
func runOnce(t *testing.T, src string, caps *CapabilitySet, input string) string {
	t.Helper()
	if caps == nil {
		caps = testCaps(t, plugins.TrustThirdParty)
	}
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{},
		Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(input), Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("工具没有返回内容")
	}
	return res.Content[0].Text
}

// TestNodePath 钉 path 的字符串语义（12 §6.12.2 path 行）。
func TestNodePath(t *testing.T) {
	src := `
import path from "node:path";
export default function (agent) {
  agent.registerTool({ name: "path_it", description: "path", parameters: { type: "object" }, access: "read",
    execute() {
      const p = path.parse("/a/b.txt");
      return { content: [{ type: "text", text: JSON.stringify({
        join: path.join("/a", "b", "..", "c"),
        resolve: path.resolve("a", "b"),
        dirname: path.dirname("/a/b.txt"),
        basename: path.basename("/a/b.txt", ".txt"),
        extname: path.extname("a.tar.gz"),
        relative: path.relative("/a/b", "/a/c/d"),
        isAbsolute: path.isAbsolute("/x"),
        parse: [p.root, p.dir, p.base, p.ext, p.name],
        format: path.format({ dir: "/a", name: "b", ext: ".txt" }),
        sep: path.sep,
      }) }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		Join, Resolve, Dirname, Basename, Extname, Relative string
		IsAbsolute                                          bool
		Parse                                               []string
		Format, Sep                                         string
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	want := map[string]string{
		"join": "/a/c", "resolve": "/a/b", "dirname": "/a", "basename": "b",
		"extname": ".gz", "relative": "../c/d", "format": "/a/b.txt", "sep": "/",
	}
	for k, v := range want {
		var have string
		switch k {
		case "join":
			have = out.Join
		case "resolve":
			have = out.Resolve
		case "dirname":
			have = out.Dirname
		case "basename":
			have = out.Basename
		case "extname":
			have = out.Extname
		case "relative":
			have = out.Relative
		case "format":
			have = out.Format
		case "sep":
			have = out.Sep
		}
		if have != v {
			t.Fatalf("path.%s = %q，期望 %q", k, have, v)
		}
	}
	if !out.IsAbsolute {
		t.Fatal(`path.isAbsolute("/x") 应为 true`)
	}
	if len(out.Parse) != 5 || out.Parse[0] != "/" || out.Parse[2] != "b.txt" || out.Parse[3] != ".txt" || out.Parse[4] != "b" {
		t.Fatalf("path.parse 字段 = %v", out.Parse)
	}
}

// TestNodeEvents 钉 EventEmitter 的核心语义（含「error 无监听即抛」）。
func TestNodeEvents(t *testing.T) {
	src := `
import { EventEmitter } from "node:events";
export default function (agent) {
  agent.registerTool({ name: "events_it", description: "events", parameters: { type: "object" }, access: "read",
    execute() {
      const ee = new EventEmitter();
      const seen = [];
      const onX = (v) => seen.push(String(v));
      ee.on("x", onX);
      const had = ee.emit("x", 1);
      ee.once("x", (v) => seen.push("once:" + String(v)));
      ee.emit("x", 2);
      ee.emit("x", 3);
      const none = ee.emit("nope");
      const count = ee.listenerCount("x");
      ee.off("x", onX);
      const afterOff = ee.listenerCount("x");
      let errorThrow = "";
      try { ee.emit("error", new Error("boom")); } catch (e) { errorThrow = e.message; }
      return { content: [{ type: "text", text: JSON.stringify({ seen, had, none, count, afterOff, errorThrow }) }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		Seen       []string
		Had, None  bool
		Count      int
		AfterOff   int
		ErrorThrow string
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	wantSeen := []string{"1", "2", "once:2", "3"}
	if len(out.Seen) != len(wantSeen) {
		t.Fatalf("seen = %v，期望 %v", out.Seen, wantSeen)
	}
	for i := range wantSeen {
		if out.Seen[i] != wantSeen[i] {
			t.Fatalf("seen = %v，期望 %v", out.Seen, wantSeen)
		}
	}
	if !out.Had || out.None {
		t.Fatalf("emit 返回值 = had:%v none:%v，期望 true/false", out.Had, out.None)
	}
	if out.AfterOff != 0 || out.Count == 0 {
		t.Fatalf("off 后监听数 = %d（off 前 %d）", out.AfterOff, out.Count)
	}
	if out.ErrorThrow != "boom" {
		t.Fatalf("error 无监听应抛出原错误，实际 %q", out.ErrorThrow)
	}
}

// TestNodeUtil 钉 format / inspect / 深比较 / promisify（12 §6.12.2 util 行）。
func TestNodeUtil(t *testing.T) {
	src := `
import util from "node:util";
export default function (agent) {
  agent.registerTool({ name: "util_it", description: "util", parameters: { type: "object" }, access: "read",
    execute: async () => {
      const fn = (a, cb) => cb(null, a * 2);
      const p = util.promisify(fn);
      const doubled = await p(21);
      return { content: [{ type: "text", text: JSON.stringify({
        format: util.format("%s=%d", "a", 5),
        inspect: util.inspect({ a: 1, b: [1, 2] }),
        deep: util.isDeepStrictEqual({ a: [1, { b: 2 }] }, { a: [1, { b: 2 }] }),
        notDeep: util.isDeepStrictEqual({ a: 1 }, { a: 2 }),
        doubled,
        typeName: util.types.isPromise(Promise.resolve(1)),
      }) }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		Format, Inspect string
		Deep, NotDeep   bool
		Doubled         int
		TypeName        bool
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	if out.Format != "a=5" {
		t.Fatalf("util.format = %q", out.Format)
	}
	if out.Inspect == "" || out.Inspect[0] != '{' {
		t.Fatalf("util.inspect = %q，期望对象字面量", out.Inspect)
	}
	if !out.Deep || out.NotDeep {
		t.Fatalf("isDeepStrictEqual = deep:%v notDeep:%v", out.Deep, out.NotDeep)
	}
	if out.Doubled != 42 {
		t.Fatalf("promisify 结果 = %d", out.Doubled)
	}
	if !out.TypeName {
		t.Fatal("util.types.isPromise(Promise.resolve(1)) 应为 true")
	}
}

// TestNodeCrypto 钉已知向量与 KDF（cz-02-vectors 是验收表）。
func TestNodeCrypto(t *testing.T) {
	src := `
import crypto from "node:crypto";
export default function (agent) {
  agent.registerTool({ name: "crypto_it", description: "crypto", parameters: { type: "object" }, access: "read",
    execute: async () => {
      const sha256abc = crypto.createHash("sha256").update("abc").digest("hex");
      const sha1abc = crypto.createHash("sha1").update("abc").digest("hex");
      const md5abc = crypto.createHash("md5").update("abc").digest("hex");
      const hmac = crypto.createHmac("sha256", "key").update("abc").digest("hex");
      const pbkdf2 = crypto.createHash("sha256") && Array.from(crypto.pbkdf2Sync("password", "salt", 1, 16, "sha256"),
        (b) => b.toString(16).padStart(2, "0")).join("");
      const eq = crypto.timingSafeEqual(new Uint8Array([97]), new Uint8Array([97]));
      const rb = crypto.randomBytes(16).length;
      const uuid = crypto.randomUUID();
      let bad = "";
      try { crypto.createHash("no-such-algo"); } catch (e) { bad = e.code || e.name; }
      const digest = await crypto.subtle.digest("SHA-256", "abc");
      const subtleHex = Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("");
      return { content: [{ type: "text", text: JSON.stringify({
        sha256abc, sha1abc, md5abc, hmac, pbkdf2, eq, rb, uuidLen: uuid.length, bad, subtleHex,
      }) }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		Sha256abc, Sha1abc, Md5abc, Hmac, Pbkdf2, Bad, SubtleHex, UUID string
		Eq                                                             bool
		Rb                                                             int
		UUIDLen                                                        int `json:"uuidLen"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	// 已知向量（cz-02 的验收表）。
	if out.Sha256abc != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha256(abc) = %q", out.Sha256abc)
	}
	if out.Sha1abc != "a9993e364706816aba3e25717850c26c9cd0d89d" {
		t.Fatalf("sha1(abc) = %q", out.Sha1abc)
	}
	if out.Md5abc != "900150983cd24fb0d6963f7d28e17f72" {
		t.Fatalf("md5(abc) = %q", out.Md5abc)
	}
	if out.Hmac != "9c196e32dc0175f86f4b1cb89289d6619de6bee699e4c378e68309ed97a1a6ab" {
		t.Fatalf("hmac-sha256(key,abc) = %q", out.Hmac)
	}
	// KDF：期望值用同一份 Go 标准库算，验证的是 JS↔Go 的字节边界而不是算法本身。
	wantPbkdf2, err := pbkdf2.Key(func() hash.Hash { return sha256.New() }, "password", []byte("salt"), 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	if out.Pbkdf2 != hex.EncodeToString(wantPbkdf2) {
		t.Fatalf("pbkdf2 = %q，期望 %q", out.Pbkdf2, hex.EncodeToString(wantPbkdf2))
	}
	if !out.Eq {
		t.Fatal("timingSafeEqual([97],[97]) 应为 true")
	}
	if out.Rb != 16 {
		t.Fatalf("randomBytes(16).length = %d", out.Rb)
	}
	if out.UUIDLen != 36 {
		t.Fatalf("randomUUID 长度 = %d，期望 36", out.UUIDLen)
	}
	if out.Bad == "" {
		t.Fatal("未知算法必须抛错")
	}
	if out.SubtleHex != out.Sha256abc {
		t.Fatalf("subtle.digest(SHA-256) = %q，期望与 createHash 一致", out.SubtleHex)
	}
}

// TestNodeCryptoHKDF 单独钉 hkdf（输入是二进制串，容易在边界上错）。
func TestNodeCryptoHKDF(t *testing.T) {
	src := `
import crypto from "node:crypto";
export default function (agent) {
  agent.registerTool({ name: "hkdf_it", description: "hkdf", parameters: { type: "object" }, access: "read",
    execute() {
      const info = "info";
      const out = crypto.hkdfSync("sha256", "ikm", "salt", info, 16);
      return { content: [{ type: "text",
        text: Array.from(new Uint8Array(out), (b) => b.toString(16).padStart(2, "0")).join("") }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	want, err := hkdf.Key(func() hash.Hash { return sha256.New() }, []byte("ikm"), []byte("salt"), "info", 16)
	if err != nil {
		t.Fatal(err)
	}
	if got != hex.EncodeToString(want) {
		t.Fatalf("hkdf = %q，期望 %q", got, hex.EncodeToString(want))
	}
}

// TestNodemodsMatrixAgreement 钉「矩阵说宿主实现 × nodemods 真的实现」两者一致。
func TestNodemodsMatrixAgreement(t *testing.T) {
	caps := testCaps(t, plugins.TrustThirdParty)
	policy := NewModulePolicy(caps)
	for _, name := range []string{"path", "events", "util", "crypto"} {
		if policy.Provider(name) != providerHost {
			t.Fatalf("模块 %s 应由宿主实现，实际 %q（缺失原因：%s）", name, policy.Provider(name), policy.MissingFor(name))
		}
		if err := policy.Allow("node:" + name); err != nil {
			t.Fatalf("node:%s 应放行: %v", name, err)
		}
	}
}

// TestNodePathPlatformViews 钉 path/posix 与 path/win32 两个视图入口（矩阵里的独立行）。
func TestNodePathPlatformViews(t *testing.T) {
	src := `
import posix from "node:path/posix";
import win32 from "node:path/win32";
export default function (agent) {
  agent.registerTool({ name: "views", description: "path views", parameters: { type: "object" }, access: "read",
    execute() {
      return { content: [{ type: "text", text: JSON.stringify({
        posixSep: posix.sep,
        posixJoin: posix.join("/a", "b"),
        winSep: win32.sep,
        winJoin: win32.join("C:\\a", "b"),
        winDir: win32.dirname("C:\\a\\b.txt"),
        winExt: win32.extname("a.tar.gz"),
      }) }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		PosixSep, PosixJoin, WinSep, WinJoin, WinDir, WinExt string
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	if out.PosixSep != "/" || out.PosixJoin != "/a/b" {
		t.Fatalf("posix 视图 = %q / %q", out.PosixSep, out.PosixJoin)
	}
	if out.WinSep != "\\" || out.WinJoin != "C:\\a\\b" || out.WinDir != "C:\\a" || out.WinExt != ".gz" {
		t.Fatalf("win32 视图 = sep:%q join:%q dir:%q ext:%q", out.WinSep, out.WinJoin, out.WinDir, out.WinExt)
	}
}

// runOnceCtx 是带宿主上下文的版本（console 用例要收日志）。
func runOnceCtx(t *testing.T, src string, hc Context) string {
	t.Helper()
	caps := testCaps(t, plugins.TrustThirdParty)
	p, err := Load(context.Background(), testScript(t, caps, src), caps, hc,
		Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("工具没有返回内容")
	}
	return res.Content[0].Text
}

// TestNodeConsole 钉 console：模块与全局都写宿主日志，不写进程 stdout。
func TestNodeConsole(t *testing.T) {
	var lines []string
	src := `
import console from "node:console";
export default function (agent) {
  agent.registerTool({ name: "log_it", description: "log", parameters: { type: "object" }, access: "read",
    execute() {
      console.log("via-module", 42);
      console.warn("warned");
      globalThis.console.error("via-global");
      return { content: [{ type: "text", text: "ok" }] };
    } });
}
`
	runOnceCtx(t, src, Context{Log: func(level, msg string, fields map[string]any) {
		lines = append(lines, level+"|"+msg)
	}})
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"info|via-module 42", "warn|warned", "error|via-global"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("日志里缺少 %q，实际：\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "stdout") {
		t.Fatalf("console 不应写进程 stdout：%v", lines)
	}
}

// TestNodeOS 钉 os 的受限形状（12 §6.12.2：hostname/homedir 为空、没有网络面）。
func TestNodeOS(t *testing.T) {
	src := `
import os from "node:os";
export default function (agent) {
  agent.registerTool({ name: "os_it", description: "os", parameters: { type: "object" }, access: "read",
    execute() {
      let netErr = "";
      try { os.networkInterfaces(); } catch (e) { netErr = e.code; }
      return { content: [{ type: "text", text: JSON.stringify({
        platform: os.platform(), arch: os.arch(), type: os.type(),
        eol: os.EOL, tmpdir: os.tmpdir(), hostname: os.hostname(), homedir: os.homedir(),
        cpus: os.cpus().length, endianness: os.endianness(), netErr,
      }) }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		Platform, Arch, Type, EOL, Tmpdir, Hostname, Homedir, Endianness, NetErr string
		Cpus                                                                     int
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	if out.Platform != processPlatform() || out.Arch != runtime.GOARCH {
		t.Fatalf("platform/arch = %q/%q", out.Platform, out.Arch)
	}
	if out.Tmpdir != "/tmp" || out.Hostname != "" || out.Homedir != "" {
		t.Fatalf("tmpdir/hostname/homedir = %q/%q/%q", out.Tmpdir, out.Hostname, out.Homedir)
	}
	if out.Cpus < 1 {
		t.Fatalf("cpus().length = %d", out.Cpus)
	}
	if out.NetErr != "ERR_NOT_SUPPORTED" {
		t.Fatalf("networkInterfaces 应抛 ERR_NOT_SUPPORTED，实际 %q", out.NetErr)
	}
}

// TestNodeModule 钉 createRequire 的诚实失败面（E-3 未闭合）。
func TestNodeModule(t *testing.T) {
	src := `
import mod, { createRequire, isBuiltin } from "node:module";
export default function (agent) {
  agent.registerTool({ name: "mod_it", description: "module", parameters: { type: "object" }, access: "read",
    execute() {
      const require = createRequire("plugin://test/index.js");
      let esmErr = "", missingErr = "";
      try { require("node:path"); } catch (e) { esmErr = e.code; }
      try { require("left-pad"); } catch (e) { missingErr = e.code; }
      return { content: [{ type: "text", text: JSON.stringify({
        hasPath: mod.builtinModules.indexOf("path") >= 0,
        hasCrypto: mod.builtinModules.indexOf("crypto") >= 0,
        isBuiltin: isBuiltin("node:fs"),
        esmErr, missingErr,
      }) }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		HasPath, HasCrypto, IsBuiltin bool
		EsmErr, MissingErr            string
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	if !out.HasPath || !out.HasCrypto || !out.IsBuiltin {
		t.Fatalf("builtinModules/isBuiltin 不对: %+v", out)
	}
	if out.EsmErr != "ERR_REQUIRE_ESM" {
		t.Fatalf("require 已安装内置模块应报 ERR_REQUIRE_ESM，实际 %q", out.EsmErr)
	}
	if out.MissingErr != "ERR_MODULE_NOT_FOUND" {
		t.Fatalf("require 未知模块应报 ERR_MODULE_NOT_FOUND，实际 %q", out.MissingErr)
	}
}

// TestNodeTimers 钉 timers 与 setImmediate 的 0ms 实现。
func TestNodeTimers(t *testing.T) {
	src := `
import timers from "node:timers";
export default function (agent) {
  agent.registerTool({ name: "timers_it", description: "timers", parameters: { type: "object" }, access: "read",
    execute() {
      return new Promise((resolve) => {
        const order = [];
        const cancelled = timers.setTimeout(() => order.push("cancelled"), 10);
        timers.clearTimeout(cancelled);
        timers.setImmediate(() => order.push("immediate"));
        timers.setTimeout(() => {
          order.push("timeout");
          resolve({ content: [{ type: "text", text: JSON.stringify(order) }] });
        }, 10);
      });
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var order []string
	if err := json.Unmarshal([]byte(got), &order); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	if len(order) != 2 || order[0] != "immediate" || order[1] != "timeout" {
		t.Fatalf("定时器顺序 = %v，期望 [immediate timeout]", order)
	}
}

// TestNodeURL 钉 WHATWG URL 的常用面与虚拟 FS 的 file URL 映射。
func TestNodeURL(t *testing.T) {
	src := `
import url from "node:url";
export default function (agent) {
  agent.registerTool({ name: "url_it", description: "url", parameters: { type: "object" }, access: "read",
    execute() {
      const u = new url.URL("https://example.com:8443/a/b?x=1&y=2#frag");
      const sp = new url.URLSearchParams("a=1&b=2&a=3");
      const rel = new url.URL("../c", "https://example.com/a/b/");
      const parsed = url.parse("https://example.com/a?x=1", true);
      return { content: [{ type: "text", text: JSON.stringify({
        protocol: u.protocol, hostname: u.hostname, port: u.port, pathname: u.pathname,
        search: u.search, hash: u.hash, origin: u.origin,
        all: sp.getAll("a"), joined: sp.toString(),
        rel: rel.href,
        query: parsed.query,
        filePath: url.fileURLToPath("file:///workspace/a%20b.txt"),
        fileURL: url.pathToFileURL("/workspace/a b.txt").href,
      }) }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		Protocol, Hostname, Port, Pathname, Search, Hash, Origin string
		All                                                      []string
		Joined, Rel                                              string
		Query                                                    map[string]string
		FilePath, FileURL                                        string
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	want := map[string]string{
		"protocol": "https:", "hostname": "example.com", "port": "8443",
		"pathname": "/a/b", "search": "?x=1&y=2", "hash": "#frag",
		"origin": "https://example.com:8443", "rel": "https://example.com/a/c",
		"filePath": "/workspace/a b.txt", "fileURL": "file:///workspace/a%20b.txt",
	}
	have := map[string]string{
		"protocol": out.Protocol, "hostname": out.Hostname, "port": out.Port,
		"pathname": out.Pathname, "search": out.Search, "hash": out.Hash,
		"origin": out.Origin, "rel": out.Rel, "filePath": out.FilePath, "fileURL": out.FileURL,
	}
	for k, v := range want {
		if have[k] != v {
			t.Fatalf("url %s = %q，期望 %q", k, have[k], v)
		}
	}
	if len(out.All) != 2 || out.All[0] != "1" || out.All[1] != "3" {
		t.Fatalf("searchParams.getAll(a) = %v", out.All)
	}
	if out.Query["x"] != "1" {
		t.Fatalf("url.parse(...,true).query = %v", out.Query)
	}
}

// TestNodeQuerystring 钉 parse/stringify 的数组与转义。
func TestNodeQuerystring(t *testing.T) {
	src := `
import qs from "node:querystring";
export default function (agent) {
  agent.registerTool({ name: "qs_it", description: "qs", parameters: { type: "object" }, access: "read",
    execute() {
      const parsed = qs.parse("a=1&b=2&a=3");
      return { content: [{ type: "text", text: JSON.stringify({
        a: parsed.a, b: parsed.b, proto: Object.getPrototypeOf(parsed),
        out: qs.stringify({ a: [1, 2], b: "x y" }),
        esc: qs.escape("a b&c"),
        unesc: qs.unescape("a%20b"),
      }) }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		A          []string
		B, Out     string
		Esc, Unesc string
		Proto      any
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	// a 是重复键数组；这里只验证 stringify 与转义（数组经 JSON 会变成字符串形式）。
	if len(out.A) != 2 || out.A[0] != "1" || out.A[1] != "3" || out.B != "2" {
		t.Fatalf("querystring.parse 重复键 = %v / %q", out.A, out.B)
	}
	if out.Out != "a=1&a=2&b=x%20y" {
		t.Fatalf("querystring.stringify = %q", out.Out)
	}
	if out.Esc != "a%20b%26c" || out.Unesc != "a b" {
		t.Fatalf("escape/unescape = %q / %q", out.Esc, out.Unesc)
	}
	if out.Proto != nil {
		t.Fatalf("parse 的结果应是 null 原型对象，实际原型 = %v", out.Proto)
	}
}

// TestNodeAssert 钉断言函数与 AssertionError 的 code。
func TestNodeAssert(t *testing.T) {
	src := `
import assert from "node:assert";
export default function (agent) {
  agent.registerTool({ name: "assert_it", description: "assert", parameters: { type: "object" }, access: "read",
    execute() {
      const out = {};
      try { assert(false, "自定义消息"); } catch (e) { out.falsy = [e.name, e.code, e.message]; }
      try { assert.strictEqual(1, "1"); } catch (e) { out.strict = e.code; }
      try { assert.deepStrictEqual({ a: [1, { b: 2 }] }, { a: [1, { b: 2 }] }); out.deep = "ok"; }
      catch (e) { out.deep = "threw"; }
      try { assert.deepStrictEqual({ a: 1 }, { a: 2 }); out.deepDiff = "no-throw"; }
      catch (e) { out.deepDiff = e.code; }
      try { assert.throws(() => { throw new TypeError("boom"); }, TypeError); out.throws = "ok"; }
      catch (e) { out.throws = "threw"; }
      try { assert.throws(() => {}); out.noThrow = "no-throw"; }
      catch (e) { out.noThrow = e.code; }
      return { content: [{ type: "text", text: JSON.stringify(out) }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		Falsy            []string
		Strict, Deep     string
		DeepDiff, Throws string
		NoThrow          string
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	if len(out.Falsy) != 3 || out.Falsy[0] != "AssertionError" || out.Falsy[1] != "ERR_ASSERTION" {
		t.Fatalf("assert(false) = %v", out.Falsy)
	}
	if out.Falsy[2] != "自定义消息" {
		t.Fatalf("assert(false) 的自定义消息 = %q", out.Falsy[2])
	}
	if out.Strict != "ERR_ASSERTION" || out.Deep != "ok" || out.DeepDiff != "ERR_ASSERTION" {
		t.Fatalf("strict/deep = %q/%q/%q", out.Strict, out.Deep, out.DeepDiff)
	}
	if out.Throws != "ok" || out.NoThrow != "ERR_ASSERTION" {
		t.Fatalf("throws = %q / %q", out.Throws, out.NoThrow)
	}
}

// TestNodeStringDecoder 钉跨 chunk 的多字节 UTF-8 重组（StringDecoder 存在的理由）。
func TestNodeStringDecoder(t *testing.T) {
	src := `
import { StringDecoder } from "node:string_decoder";
export default function (agent) {
  agent.registerTool({ name: "sd_it", description: "string_decoder", parameters: { type: "object" }, access: "read",
    execute() {
      const d = new StringDecoder("utf8");
      // "你" = E4 BD A0，切成 2 + 1 两次写
      const first = d.write(new Uint8Array([0xE4, 0xBD]));
      const second = d.write(new Uint8Array([0xA0]));
      const ascii = new StringDecoder("utf8").write(new Uint8Array([104, 105]));
      let bad = "";
      try { new StringDecoder("no-such-encoding"); } catch (e) { bad = e.code; }
      return { content: [{ type: "text", text: JSON.stringify({ first, second, ascii, bad, whole: first + second }) }] };    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		First, Second, Ascii, Bad, Whole string
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}

	if out.Whole != "你" {
		t.Fatalf("跨 chunk 重组 = %q，期望 你", out.Whole)
	}
	if out.First != "" {
		t.Fatalf("不完整序列不应提前输出，实际 %q", out.First)
	}
	if out.Ascii != "hi" {
		t.Fatalf("ascii 解码 = %q", out.Ascii)
	}
	if out.Bad != "ERR_UNKNOWN_ENCODING" {
		t.Fatalf("未知编码的错误码 = %q", out.Bad)
	}
}

// TestNodeStreamWeb 钉 WHATWG 流的最小可用面（读、写、管道、转换）。
func TestNodeStreamWeb(t *testing.T) {
	src := `
import { ReadableStream, WritableStream, TransformStream } from "node:stream/web";
export default function (agent) {
  agent.registerTool({ name: "stream_it", description: "stream/web", parameters: { type: "object" }, access: "read",
    execute: async () => {
      const rs = new ReadableStream({
        start(c) { c.enqueue("a"); c.enqueue("b"); c.close(); },
      });
      const reader = rs.getReader();
      const got = [];
      for (;;) { const { value, done } = await reader.read(); if (done) break; got.push(value); }

      const sink = [];
      const ws = new WritableStream({ write(chunk) { sink.push(chunk); } });
      const rs2 = new ReadableStream({ start(c) { c.enqueue(1); c.enqueue(2); c.close(); } });
      await rs2.pipeTo(ws);

      const upper = new TransformStream({
        transform(chunk, controller) { controller.enqueue(String(chunk).toUpperCase()); },
      });
      const piped = new ReadableStream({ start(c) { c.enqueue("x"); c.close(); } });
      const outReader = piped.pipeThrough(upper).getReader();
      const upperOut = [];
      for (;;) { const { value, done } = await outReader.read(); if (done) break; upperOut.push(value); }

      return { content: [{ type: "text", text: JSON.stringify({ got, sink, upperOut }) }] };
    } });
}
`
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		Got, UpperOut []string
		Sink          []any
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	if len(out.Got) != 2 || out.Got[0] != "a" || out.Got[1] != "b" {
		t.Fatalf("reader 读到 %v", out.Got)
	}
	if len(out.Sink) != 2 || fmt.Sprint(out.Sink[0]) != "1" || fmt.Sprint(out.Sink[1]) != "2" {
		t.Fatalf("pipeTo 未把数据写进 sink：%v", out.Sink)
	}
	if len(out.UpperOut) != 1 || out.UpperOut[0] != "X" {
		t.Fatalf("transform 输出 %v", out.UpperOut)
	}
}

// TestDeniedModuleIsHonest 钉「矩阵说不安装的模块」与「完全未知的模块」两种拒绝方向：
// 都不许出现「import 成功但成员是 undefined」。
func TestDeniedModuleIsHonest(t *testing.T) {
	caps := testCaps(t, plugins.TrustThirdParty)

	policy := NewModulePolicy(caps)
	if msg := policy.MissingFor("worker_threads"); !strings.Contains(msg, "不安装") {
		t.Fatalf("worker_threads 应报「不安装」，实际 %q", msg)
	}
	if msg := policy.MissingFor("no-such-module"); msg != "" {
		t.Fatalf("未知模块不该有安装矩阵条目，实际 %q", msg)
	}

	for _, spec := range []string{"node:worker_threads", "left-pad"} {
		src := "import x from \"" + spec + "\";\nexport default function (agent) {}\n"
		_, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{})
		if err == nil {
			t.Fatalf("import %q 必须在装配期失败", spec)
		}
		if !strings.Contains(err.Error(), strings.TrimPrefix(spec, "node:")) {
			t.Fatalf("错误应点名模块：%v", err)
		}
	}
}

// TestEveryHostModuleInstalled 钉矩阵与实现的完整性：矩阵里标「宿主实现」的模块，
// ModulePolicy 必须都报告已安装（少一个就说明 hostImplemented 清单漂了）。
func TestEveryHostModuleInstalled(t *testing.T) {
	// 授予全部后端能力：否则 child_process 这类「有能力才装」的模块会被误判为没实现。
	caps := testCaps(t, plugins.TrustBuiltin, plugins.BackendCapabilities()...)
	policy := NewModulePolicy(caps)
	for _, m := range hostnode.Modules() {
		if hostnode.ProvidedByOf(m) != hostnode.ProvidedHost {
			continue
		}
		if got := policy.Provider(m.Name); got == "" {
			t.Errorf("矩阵要求宿主实现 %q，但安装矩阵说没装（原因：%s）", m.Name, policy.MissingFor(m.Name))
		}
	}
}

// TestNodeZlib 钉 cz-02 的压缩往返与解压炸弹上限。
func TestNodeZlib(t *testing.T) {
	src := `
import zlib from "node:zlib";
export default function (agent) {
  agent.registerTool({ name: "zlib_it", description: "zlib", parameters: { type: "object" }, access: "read",
    execute: async () => {
      const asText = (b) => {
        const bytes = typeof Buffer !== "undefined" && Buffer.isBuffer(b) ? Array.from(b) : Array.from(b);
        return String.fromCharCode.apply(null, bytes);
      };
      const gz = zlib.gzipSync("round-trip");
      const back = asText(zlib.gunzipSync(gz));
      const dfl = asText(zlib.inflateSync(zlib.deflateSync("round-trip")));
      const raw = asText(zlib.inflateRawSync(zlib.deflateRawSync("round-trip")));
      const asyncOut = await zlib.gzip("abc").then((b) => b.length > 0);
      let corrupt = "";
      try { zlib.gunzipSync(new Uint8Array([1, 2, 3])); } catch (e) { corrupt = e.code; }
      // 解压炸弹：1.2MB 全零压缩后很小，解压必须被上限拒绝（测试把上限调到 1MB）。
      const bomb = zlib.gzipSync(new Uint8Array(1200 * 1024));
      let bombCode = "";
      try { zlib.gunzipSync(bomb); } catch (e) { bombCode = e.code; }
      return { content: [{ type: "text", text: JSON.stringify({
        back, dfl, raw, asyncOut, corrupt, bombCode,
        brotli: typeof zlib.brotliCompressSync,
        gzIsBuffer: typeof Buffer !== "undefined" && Buffer.isBuffer(gz),
      }) }] };
    } });
}
`
	nodemods.SetMaxDecompressBytes(1 << 20)
	defer nodemods.SetMaxDecompressBytes(nodemods.MaxDecompressBytes)
	got := runOnce(t, src, nil, `{}`)
	var out struct {
		Back, Dfl, Raw, Corrupt, BombCode, Brotli string
		AsyncOut, GzIsBuffer                      bool
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是 JSON: %v (%s)", err, got)
	}
	if out.Back != "round-trip" || out.Dfl != "round-trip" || out.Raw != "round-trip" {
		t.Fatalf("往返 = %q / %q / %q", out.Back, out.Dfl, out.Raw)
	}
	if !out.AsyncOut {
		t.Fatal("async gzip 应返回非空结果")
	}
	if out.Corrupt != "ERR_ZLIB_DECOMPRESS_FAILED" {
		t.Fatalf("损坏输入的 code = %q", out.Corrupt)
	}
	if out.BombCode != "ERR_BUFFER_TOO_LARGE" {
		t.Fatalf("解压炸弹的 code = %q，期望 ERR_BUFFER_TOO_LARGE", out.BombCode)
	}
	if out.Brotli != "undefined" {
		t.Fatalf("brotli 不应存在，typeof = %q", out.Brotli)
	}
	if !out.GzIsBuffer {
		t.Log("提示：本 Runtime 没有全局 Buffer，gzipSync 返回 Uint8Array")
	}
}
