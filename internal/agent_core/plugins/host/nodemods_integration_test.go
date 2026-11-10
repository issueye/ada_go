package host

// 本文件是「宿主自建 Node 内置模块」的行为验收（nodemods/src/*.js）。
//
// 每个用例都是一次真实装配：插件 import "node:*" → 链接期命中模块表 →
// 模块体求值（含 import.meta.binding 注入）→ 调工具 → 结算。因此一次跑通同时钉住
// 三件事：模块表注册、binding 注入、模块实现本身与 Node 的形状。
//
// 与 nodemods 包的自检分工：那边（nodemods_test.go）只做 moejs.Compile，钉「声明已
// 实现的模块一定能编译」；这里钉「真跑起来看到什么」。两边都不许用永真断言冒充覆盖：
// 明确不支持的成员（brotli、流式 zlib、网络面 os）断言的是「不存在 / 调用即抛」。

import (
	"context"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/MoeForge/moejs"
	"github.com/adag/ada-go/internal/agent_core/plugins/host/nodemods"
	"github.com/adag/ada-go/internal/common/pluginapi"
)

// runBuiltin 装配一个只注册一个探针工具的插件并调用它，返回工具输出的第一段文本。
func runBuiltin(t *testing.T, src string, hc Context) string {
	t.Helper()
	caps := testCaps(t, pluginapi.TrustThirdParty)
	p, err := Load(context.Background(), testScript(t, caps, src), caps, hc,
		Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	contrib := p.Contribution()
	if contrib == nil || len(contrib.Tools) == 0 {
		t.Fatalf("插件没有注册工具，无法探针")
	}
	res, err := p.CallTool(context.Background(), contrib.Tools[0].Handler, json.RawMessage(`{}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("工具没有返回内容")
	}
	return res.Content[0].Text
}

// probeJSON 把探针输出解析成不确定类型的对象（数字一律 float64，同 encoding/json）。
func probeJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("工具输出不是 JSON 对象: %v\n输出=%s", err, text)
	}
	return out
}

// want 断言一个键的期望值（字符串/布尔/数字都走这里）。
func want(t *testing.T, got map[string]any, key string, expect any) {
	t.Helper()
	have, ok := got[key]
	if !ok {
		t.Fatalf("输出缺少 %q：%v", key, got)
	}
	if fmt.Sprint(have) != fmt.Sprint(expect) {
		t.Fatalf("%s = %v（%T），期望 %v（%T）", key, have, have, expect, expect)
	}
}

// wantStrings 断言一个字符串数组键。
func wantStrings(t *testing.T, got map[string]any, key string, expect ...string) {
	t.Helper()
	raw, ok := got[key]
	if !ok {
		t.Fatalf("输出缺少 %q：%v", key, got)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s = %v（%T），期望数组", key, raw, raw)
	}
	if len(list) != len(expect) {
		t.Fatalf("%s = %v，期望 %v", key, list, expect)
	}
	for i := range expect {
		if fmt.Sprint(list[i]) != expect[i] {
			t.Fatalf("%s[%d] = %v，期望 %q（整体 %v）", key, i, list[i], expect[i], list)
		}
	}
}

// 本文件里的探针工具统一用一个名字：装配只关心模块行为，工具名没有区分度。
const builtinProbeTool = `agent.registerTool({ name: "probe", description: "内置模块探针",
    parameters: { type: "object" }, access: "read",`

// jsTool 生成只有一个探针工具的插件源码：body 是 execute 的函数体（可 async）。
func jsTool(imports, body string) string {
	return imports + `
export default function (agent) {
  ` + builtinProbeTool + `
    execute: async () => {
      const out = {};
` + body + `
      return { content: [{ type: "text", text: JSON.stringify(out) }] };
    } });
}
`
}

// jsHex 是测试里常用的「字节 → hex」助手（不依赖 Buffer 的形状）。
const jsHex = `function hex(b) { let s = ""; for (let i = 0; i < b.length; i++) s += (b[i] < 16 ? "0" : "") + b[i].toString(16); return s; }
`

// ---------------------------------------------------------------- path

// TestNodeBuiltinPath 钉 path 的字符串语义、虚拟根基准与两个平台视图（12 §6.12.2 path 行）。
func TestNodeBuiltinPath(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import path, { posix, win32 } from "node:path";`, `
      const p = path.parse("/a/b.txt");
      out.join = path.join("/a", "b", "..", "c");
      out.normalize = path.normalize("/a//b/../c");
      out.resolve = path.resolve("a", "b");
      out.dirname = path.dirname("/a/b.txt");
      out.basename = path.basename("/a/b.txt", ".txt");
      out.extname = path.extname("a.tar.gz");
      out.relative = path.relative("/a/b", "/a/c/d");
      out.isAbsolute = path.isAbsolute("/x");
      out.parse = [p.root, p.dir, p.base, p.ext, p.name];
      out.format = path.format({ dir: "/a", name: "b", ext: ".txt" });
      out.sep = path.sep;
      out.delimiter = path.delimiter;
      out.posixJoin = posix.join("/a", "b");
      out.posixSep = posix.sep;
      out.winJoin = win32.join("C:\\a", "b");
      out.winSep = win32.sep;
      out.winDir = win32.dirname("C:\\a\\b.txt");
      out.winExt = win32.extname("a.tar.gz");
      out.errCode = (() => { try { path.join(1, "a"); return "no-throw" } catch (e) { return e.code } })();
`), Context{}))

	wantStrings(t, got, "parse", "/", "/a", "b.txt", ".txt", "b")
	for key, expect := range map[string]string{
		"join": "/a/c", "normalize": "/a/c", "resolve": "/a/b", "dirname": "/a",
		"basename": "b", "extname": ".gz", "relative": "../c/d",
		"format": "/a/b.txt", "sep": "/", "delimiter": ":",
		"posixJoin": "/a/b", "posixSep": "/",
		"winJoin": `C:\a\b`, "winSep": `\`, "winDir": `C:\a`, "winExt": ".gz",
		"errCode": "ERR_INVALID_ARG_TYPE",
	} {
		want(t, got, key, expect)
	}
	want(t, got, "isAbsolute", true)
}

// TestNodeBuiltinPathNoHostCwd 钉「resolve 的基准是虚拟根 /，不是宿主 cwd」（12 §6.12.2）。
func TestNodeBuiltinPathNoHostCwd(t *testing.T) {
	// 工作区宿主路径绝不出现在脚本结果里：resolve() 只给 "/"。
	got := probeJSON(t, runBuiltin(t, jsTool(`import path from "node:path";`, `
      out.emptyResolve = path.resolve();
      out.onlyRel = path.resolve("a/b");
      out.relOfEmpty = path.relative("", "/a");
`), Context{}))
	want(t, got, "emptyResolve", "/")
	want(t, got, "onlyRel", "/a/b")
	want(t, got, "relOfEmpty", "a")
}

// -------------------------------------------------------------- events

// TestNodeBuiltinEvents 钉 EventEmitter 的核心语义（12 §6.12.2 events 行）。
func TestNodeBuiltinEvents(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import { EventEmitter, once } from "node:events";`, `
      const ee = new EventEmitter();
      const seen = [];
      const onX = (v) => seen.push("on:" + v);
      ee.on("x", onX);
      ee.prependListener("x", (v) => seen.push("pre:" + v));
      ee.once("x", (v) => seen.push("once:" + v));
      const had = ee.emit("x", 1);
      ee.emit("x", 2);
      out.seen = seen;
      out.had = had;
      out.emptyEmit = ee.emit("nope");
      out.countBeforeOff = ee.listenerCount("x");
      out.listeners = ee.listeners("x").length;
      ee.off("x", onX);
      out.countAfterOff = ee.listenerCount("x");
      out.names = ee.eventNames();
      ee.removeAllListeners("x");
      out.countAfterRemoveAll = ee.listenerCount("x");
      let errorThrow = "no-throw";
      try { ee.emit("error", new Error("boom")); } catch (e) { errorThrow = e.message; }
      out.errorThrow = errorThrow;
      out.badListener = (() => { try { ee.on("y", 1); return "no-throw" } catch (e) { return e.code } })();

      // once(emitter, name) 的 Promise 形状：参数数组作为 resolve 值。
      const pending = once(ee, "y");
      setTimeout(() => ee.emit("y", "a", "b"), 0);
      out.onceArgs = await pending;
`), Context{}))

	wantStrings(t, got, "seen", "pre:1", "on:1", "once:1", "pre:2", "on:2")
	want(t, got, "had", true)
	want(t, got, "emptyEmit", false)
	want(t, got, "countBeforeOff", 2)
	want(t, got, "listeners", 2)
	want(t, got, "countAfterOff", 1)
	want(t, got, "countAfterRemoveAll", 0)
	wantStrings(t, got, "onceArgs", "a", "b")
	want(t, got, "errorThrow", "boom")
	want(t, got, "badListener", "ERR_INVALID_ARG_TYPE")
	wantStrings(t, got, "names", "x")
}

// ----------------------------------------------------------------- util

// TestNodeBuiltinUtil 钉 format / inspect / promisify / 深比较 / types（12 §6.12.2 util 行）。
func TestNodeBuiltinUtil(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import util from "node:util";`, `
      out.format = util.format("%s=%d", "a", 5);
      out.formatExtra = util.format("%s", "a", "b", 3);
      out.escaped = util.format("100%%");
      out.escapedWithArg = util.format("%d%%", 5);
      out.inspect = util.inspect({ a: 1, b: [1, "x"] });
      out.inspectCircular = util.inspect((() => { const o = { a: 1 }; o.self = o; return o })());
      out.deep = util.isDeepStrictEqual({ a: [1, { b: 2 }] }, { a: [1, { b: 2 }] });
      out.notDeep = util.isDeepStrictEqual({ a: 1 }, { a: 2 });
      out.negZero = util.isDeepStrictEqual(-0, 0);
      const doubled = await util.promisify((a, cb) => cb(null, a * 2))(21);
      out.promisified = doubled;
      out.callbackified = await new Promise((res) => {
        util.callbackify(async () => 7)((err, value) => res(err ? "err" : value));
      });
      out.isPromise = util.types.isPromise(Promise.resolve(1));
      out.isDate = util.types.isDate(new Date(0));
      out.textEncoder = util.TextEncoder ? Array.from(new util.TextEncoder().encode("A中")).join(",") : "absent";
      out.badEncoding = (() => { try { new util.TextDecoder("koi8-r"); return "no-throw" } catch (e) { return e.code } })();
`), Context{}))

	want(t, got, "format", "a=5")
	want(t, got, "formatExtra", "a b 3")
	// 与 Node 一致：没有额外参数时占位符不参与替换（format("100%%") 原样返回）。
	want(t, got, "escaped", "100%%")
	want(t, got, "escapedWithArg", "5%")
	want(t, got, "deep", true)
	want(t, got, "notDeep", false)
	want(t, got, "negZero", false)
	want(t, got, "promisified", 42)
	want(t, got, "callbackified", 7)
	want(t, got, "isPromise", true)
	want(t, got, "isDate", true)
	want(t, got, "textEncoder", "65,228,184,173")
	want(t, got, "badEncoding", "ERR_ENCODING_NOT_SUPPORTED")
	for _, key := range []string{"inspect", "inspectCircular"} {
		if s, _ := got[key].(string); !strings.HasPrefix(s, "{") {
			t.Fatalf("%s = %q，期望对象字面量", key, got[key])
		}
	}
	if s, _ := got["inspectCircular"].(string); !strings.Contains(s, "[Circular]") {
		t.Fatalf("循环引用应标 [Circular]，实际 %q", s)
	}
}

// ---------------------------------------------------------------- crypto

// TestNodeBuiltinCrypto 钉已知向量与 KDF（cz-02-vectors 是验收表，12 §6.12.2 crypto 行）。
func TestNodeBuiltinCrypto(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import crypto from "node:crypto";`+jsHex, `
      out.sha256abc = crypto.createHash("sha256").update("abc").digest("hex");
      out.sha1abc = crypto.createHash("sha1").update("abc").digest("hex");
      out.md5abc = crypto.createHash("md5").update("abc").digest("hex");
      out.sha3abc = crypto.createHash("sha3-256").update("abc").digest("hex");
      out.hmac = crypto.createHmac("sha256", "key").update("abc").digest("hex");
      out.hmacBase64 = crypto.createHmac("sha256", "key").update("abc").digest("base64");
      out.pbkdf2 = hex(crypto.pbkdf2Sync("password", "salt", 1, 16, "sha256"));
      out.hkdf = hex(crypto.hkdfSync("sha256", "ikm", "salt", "info", 16));
      out.hexInput = hex(crypto.createHash("sha256").update("616263", "hex").digest());
      out.eq = crypto.timingSafeEqual(new Uint8Array([97]), new Uint8Array([97]));
      out.neq = crypto.timingSafeEqual(new Uint8Array([97]), new Uint8Array([98]));
      out.rbl = crypto.randomBytes(16).length;
      out.uuid = crypto.randomUUID();
      out.intInRange = (() => { const n = crypto.randomInt(10, 20); return n >= 10 && n < 20 })();
      out.getRandomValues = Array.from(crypto.getRandomValues(new Uint8Array(4))).length;
      out.hashes = crypto.getHashes();
      out.badAlgo = (() => { try { crypto.createHash("no-such-algo"); return "no-throw" } catch (e) { return e.code } })();
      out.digestTwice = (() => {
        const h = crypto.createHash("sha256").update("abc");
        h.digest("hex");
        try { h.digest("hex"); return "no-throw" } catch (e) { return e.code }
      })();
      out.timingLen = (() => { try { crypto.timingSafeEqual(new Uint8Array([1]), new Uint8Array([1, 2])); return "no-throw" } catch (e) { return e.code } })();
      out.subtleHex = hex(new Uint8Array(await crypto.subtle.digest("SHA-256", "abc")));
      out.subtleBadAlgo = await crypto.subtle.digest("MD5", "abc").then(() => "no-throw", (e) => e.code);
`), Context{}))

	// 已知向量（cz-02 的验收表）。
	want(t, got, "sha256abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	want(t, got, "sha1abc", "a9993e364706816aba3e25717850c26c9cd0d89d")
	want(t, got, "md5abc", "900150983cd24fb0d6963f7d28e17f72")
	want(t, got, "sha3abc", "3a985da74fe225b2045c172d6bd390bd855f086e3e9d525b46bfe24511431532")
	want(t, got, "hmac", "9c196e32dc0175f86f4b1cb89289d6619de6bee699e4c378e68309ed97a1a6ab")
	want(t, got, "hmacBase64", "nBluMtwBdfhvSxy4konWYZ3mvuaZ5MN45oMJ7Zehpqs=")
	want(t, got, "hexInput", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	want(t, got, "eq", true)
	want(t, got, "neq", false)
	want(t, got, "rbl", 16)
	want(t, got, "subtleHex", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	want(t, got, "getRandomValues", 4)
	want(t, got, "badAlgo", "ERR_CRYPTO_INVALID_DIGEST")
	want(t, got, "digestTwice", "ERR_CRYPTO_HASH_FINALIZED")
	want(t, got, "timingLen", "ERR_CRYPTO_TIMING_SAFE_EQUAL_LENGTH")
	want(t, got, "subtleBadAlgo", "ERR_CRYPTO_NOT_SUPPORTED")

	if s, _ := got["uuid"].(string); len(s) != 36 || s[14] != '4' {
		t.Fatalf("randomUUID = %q，期望 36 位且第 15 个字符是 '4'", s)
	}
	wantStrings(t, got, "hashes", "md5", "sha1", "sha256", "sha512", "sha3-256")

	// KDF：期望值用同一份 Go 标准库算，验的是 JS↔Go 的字节边界而不是算法本身。
	wantPBKDF2, err := pbkdf2.Key(func() hash.Hash { return sha256.New() }, "password", []byte("salt"), 1, 16)
	if err != nil {
		t.Fatalf("pbkdf2: %v", err)
	}
	want(t, got, "pbkdf2", hex.EncodeToString(wantPBKDF2))
	wantHKDF, err := hkdf.Key(func() hash.Hash { return sha256.New() }, []byte("ikm"), []byte("salt"), "info", 16)
	if err != nil {
		t.Fatalf("hkdf: %v", err)
	}
	want(t, got, "hkdf", hex.EncodeToString(wantHKDF))
}

// ---------------------------------------------------------------- zlib

// TestNodeBuiltinZlib 钉 cz-02 的压缩往返、错误码与解压炸弹上限（12 §6.12.2 zlib 行）。
func TestNodeBuiltinZlib(t *testing.T) {
	// 上限调到 1MB：几毫秒内就能验证「炸弹被拒」，不必造 16MB 载荷。
	nodemods.SetMaxDecompressBytes(1 << 20)
	defer nodemods.SetMaxDecompressBytes(nodemods.MaxDecompressBytes)

	got := probeJSON(t, runBuiltin(t, jsTool(`import zlib from "node:zlib";`, `
      // 二进制过桥后是 Uint8Array（也可能被宿主包成 Buffer），统一按字节读。
      const text = (b) => {
        const bytes = Array.from(b);
        let s = "";
        for (let i = 0; i < bytes.length; i += 4096) s += String.fromCharCode.apply(null, bytes.slice(i, i + 4096));
        return s;
      };
      out.gzip = text(zlib.gunzipSync(zlib.gzipSync("round-trip")));
      out.deflate = text(zlib.inflateSync(zlib.deflateSync("round-trip")));
      out.raw = text(zlib.inflateRawSync(zlib.deflateRawSync("round-trip")));
      // 异步形式先 await 再传：Node 的异步 API 不给回调时返回 Promise，不能嵌套直传。
      out.asyncGzip = text(await zlib.gunzip(await zlib.gzip("round-trip")));
      out.resultIsBytes = ArrayBuffer.isView(zlib.gzipSync("x"));
      out.corrupt = (() => { try { zlib.gunzipSync(new Uint8Array([1, 2, 3])); return "no-throw" } catch (e) { return e.code } })();
      out.bomb = (() => {
        const big = zlib.gzipSync(new Uint8Array(1200 * 1024));
        try { zlib.gunzipSync(big); return "no-throw" } catch (e) { return e.code }
      })();
      out.brotli = typeof zlib.brotliCompressSync;
      out.createGzip = (() => { try { zlib.createGzip(); return "no-throw" } catch (e) { return e.code } })();
      out.hasConstants = typeof zlib.constants.Z_BEST_COMPRESSION;
      out.badInput = (() => { try { zlib.gzipSync(42); return "no-throw" } catch (e) { return e.code } })();
`), Context{}))

	want(t, got, "gzip", "round-trip")
	want(t, got, "deflate", "round-trip")
	want(t, got, "raw", "round-trip")
	want(t, got, "asyncGzip", "round-trip")
	want(t, got, "resultIsBytes", true)
	want(t, got, "corrupt", "ERR_ZLIB_DECOMPRESS_FAILED")
	want(t, got, "bomb", "ERR_BUFFER_TOO_LARGE")
	want(t, got, "brotli", "undefined")
	want(t, got, "createGzip", "ERR_NOT_SUPPORTED")
	want(t, got, "hasConstants", "number")
	want(t, got, "badInput", "ERR_INVALID_ARG_TYPE")
}

// ----------------------------------------------------------------- url

// TestNodeBuiltinURL 钉 WHATWG URL 常用面与虚拟 FS 的 file URL 映射（12 §6.12.2 url 行）。
func TestNodeBuiltinURL(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import url from "node:url";`, `
      const u = new url.URL("https://example.com:8443/a/b?x=1&y=2#frag");
      out.protocol = u.protocol;
      out.hostname = u.hostname;
      out.host = u.host;
      out.port = u.port;
      out.pathname = u.pathname;
      out.search = u.search;
      out.hash = u.hash;
      out.origin = u.origin;
      out.href = u.href;
      out.json = JSON.stringify(u);
      const sp = new url.URLSearchParams("a=1&b=2&a=3");
      out.allA = sp.getAll("a");
      out.hasB = sp.has("b");
      out.joined = sp.toString();
      u.searchParams.set("z", "9");
      out.afterSet = u.search;
      out.rel = new url.URL("../c", "https://example.com/a/b/").href;
      const parsed = url.parse("https://example.com/a?x=1", true);
      out.parsedQuery = parsed.query;
      out.parsedHost = parsed.host;
      out.formatted = url.format({ protocol: "https:", host: "example.com", pathname: "/a" });
      out.filePath = url.fileURLToPath("file:///workspace/a%20b.txt");
      out.fileURL = url.pathToFileURL("/workspace/a b.txt").href;
      out.badURL = (() => { try { new url.URL("not a url"); return "no-throw" } catch (e) { return e.code } })();
      out.badScheme = (() => { try { url.fileURLToPath("https://x/a"); return "no-throw" } catch (e) { return e.code } })();
      out.domain = url.domainToASCII("Example.COM");
`), Context{}))

	for key, expect := range map[string]string{
		"protocol": "https:", "hostname": "example.com", "host": "example.com:8443",
		"port": "8443", "pathname": "/a/b", "search": "?x=1&y=2", "hash": "#frag",
		"origin":    "https://example.com:8443",
		"href":      "https://example.com:8443/a/b?x=1&y=2#frag",
		"rel":       "https://example.com/a/c",
		"joined":    "a=1&b=2&a=3",
		"formatted": "https://example.com/a",
		"filePath":  "/workspace/a b.txt",
		"fileURL":   "file:///workspace/a%20b.txt",
		"badURL":    "ERR_INVALID_URL",
		"badScheme": "ERR_INVALID_URL_SCHEME",
		"domain":    "example.com",
		"afterSet":  "?x=1&y=2&z=9",
	} {
		want(t, got, key, expect)
	}
	want(t, got, "hasB", true)
	wantStrings(t, got, "allA", "1", "3")
	if q, ok := got["parsedQuery"].(map[string]any); !ok || fmt.Sprint(q["x"]) != "1" {
		t.Fatalf("url.parse(...,true).query = %v", got["parsedQuery"])
	}
	if s, _ := got["parsedHost"].(string); s != "example.com" {
		t.Fatalf("url.parse().host = %q", s)
	}
}

// ---------------------------------------------------------- querystring

// TestNodeBuiltinQuerystring 钉 parse/stringify 的重复键、null 原型与转义。
func TestNodeBuiltinQuerystring(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import qs from "node:querystring";`, `
      const parsed = qs.parse("a=1&b=2&a=3");
      out.a = parsed.a;
      out.b = parsed.b;
      out.nullProto = Object.getPrototypeOf(parsed) === null;
      out.eqName = qs.parse("x").x;
      out.stringify = qs.stringify({ a: [1, 2], b: "x y" });
      out.skipUndefined = qs.stringify({ a: undefined, b: 1 });
      out.escape = qs.escape("a b&c");
      out.unescape = qs.unescape("a%20b");
      out.plusIsLiteral = qs.unescape("a+b");
      out.maxKeys = qs.parse("a=1&b=2&c=3", "&", "=", { maxKeys: 2 });
      out.encodeAlias = qs.encode === qs.escape;
`), Context{}))

	wantStrings(t, got, "a", "1", "3")
	want(t, got, "b", "2")
	want(t, got, "stringify", "a=1&a=2&b=x%20y")
	want(t, got, "skipUndefined", "b=1")
	want(t, got, "escape", "a%20b%26c")
	want(t, got, "unescape", "a b")
	want(t, got, "plusIsLiteral", "a+b")
	want(t, got, "encodeAlias", true)
	// null 原型对象过 JSON 会变成 null，因此用布尔比较而不是读原型本身。
	want(t, got, "nullProto", true)
	if m, ok := got["maxKeys"].(map[string]any); !ok || len(m) != 2 {
		t.Fatalf("maxKeys=2 应只解析两个键，实际 %v", got["maxKeys"])
	}
}

// --------------------------------------------------------------- assert

// TestNodeBuiltinAssert 钉断言函数、AssertionError 字段与 rejects。
func TestNodeBuiltinAssert(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import assert from "node:assert";`, `
      try { assert(false, "自定义消息"); } catch (e) { out.falsy = [e.name, e.code, e.message]; }
      try { assert.ok(0); } catch (e) { out.okCode = e.code; }
      try { assert.strictEqual(1, "1"); } catch (e) { out.strict = e.code; }
      try { assert.deepStrictEqual({ a: [1, { b: 2 }] }, { a: [1, { b: 2 }] }); out.deep = "ok"; }
      catch (e) { out.deep = "threw"; }
      try { assert.deepStrictEqual({ a: 1 }, { a: 2 }); out.deepDiff = "no-throw" }
      catch (e) { out.deepDiff = e.code; }
      try { assert.throws(() => { throw new TypeError("boom"); }, TypeError); out.throws = "ok"; }
      catch (e) { out.throws = "threw"; }
      try { assert.throws(() => {}); out.noThrow = "no-throw"; }
      catch (e) { out.noThrow = e.code; }
      try { assert.match("abc", /b/); out.match = "ok"; } catch (e) { out.match = e.code; }
      try { assert.ifError(new Error("x")); out.ifError = "no-throw"; } catch (e) { out.ifError = e.code; }
      out.strictAlias = assert.strict.strictEqual === assert.strictEqual;
      out.rejects = await assert.rejects(async () => { throw new Error("r") }, Error).then(() => "ok", (e) => e.code);
      out.doesNotReject = await assert.doesNotReject(async () => {}).then(() => "ok", (e) => e.code);
`), Context{}))

	wantStrings(t, got, "falsy", "AssertionError", "ERR_ASSERTION", "自定义消息")
	want(t, got, "okCode", "ERR_ASSERTION")
	want(t, got, "strict", "ERR_ASSERTION")
	want(t, got, "deep", "ok")
	want(t, got, "deepDiff", "ERR_ASSERTION")
	want(t, got, "throws", "ok")
	want(t, got, "noThrow", "ERR_ASSERTION")
	want(t, got, "match", "ok")
	want(t, got, "ifError", "ERR_ASSERTION")
	want(t, got, "strictAlias", true)
	want(t, got, "rejects", "ok")
	want(t, got, "doesNotReject", "ok")
}

// -------------------------------------------------------- string_decoder

// TestNodeBuiltinStringDecoder 钉跨 chunk 的多字节 UTF-8 重组（StringDecoder 存在的理由）。
func TestNodeBuiltinStringDecoder(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import { StringDecoder } from "node:string_decoder";`, `
      // "你" = E4 BD A0，切成 2 + 1 两次写。
      const d = new StringDecoder("utf8");
      out.first = d.write(new Uint8Array([0xE4, 0xBD]));
      out.second = d.write(new Uint8Array([0xA0]));
      out.whole = out.first + out.second;
      out.ascii = new StringDecoder("utf8").write(new Uint8Array([104, 105]));
      out.encoding = d.encoding;
      out.end = (() => {
        const e = new StringDecoder("utf8");
        e.write(new Uint8Array([0xE4]));
        return e.end();
      })();
      out.bad = (() => { try { new StringDecoder("no-such-encoding"); return "no-throw" } catch (e) { return e.code } })();
      out.badArg = (() => { try { d.write(42); return "no-throw" } catch (e) { return e.code } })();
`), Context{}))

	want(t, got, "first", "")
	want(t, got, "whole", "你")
	want(t, got, "ascii", "hi")
	want(t, got, "encoding", "utf8")
	want(t, got, "bad", "ERR_UNKNOWN_ENCODING")
	want(t, got, "badArg", "ERR_INVALID_ARG_TYPE")
	if s, _ := got["end"].(string); s != "\uFFFD" {
		t.Fatalf("end() 应把截断序列补成 U+FFFD，实际 %q", s)
	}
}

// ------------------------------------------------------------ stream/web

// TestNodeBuiltinStreamWeb 钉 WHATWG 流的最小可用面（读、写、管道、转换）。
func TestNodeBuiltinStreamWeb(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import { ReadableStream, WritableStream, TransformStream, CountQueuingStrategy } from "node:stream/web";`, `
      const rs = new ReadableStream({ start(c) { c.enqueue("a"); c.enqueue("b"); c.close(); } });
      const reader = rs.getReader();
      out.locked = rs.locked;
      const got1 = [];
      for (;;) { const { value, done } = await reader.read(); if (done) break; got1.push(value); }
      out.read = got1;

      const sink = [];
      const ws = new WritableStream({ write(chunk) { sink.push(chunk); } });
      const rs2 = new ReadableStream({ start(c) { c.enqueue(1); c.enqueue(2); c.close(); } });
      await rs2.pipeTo(ws);
      out.sink = sink;

      const upper = new TransformStream({
        transform(chunk, controller) { controller.enqueue(String(chunk).toUpperCase()); },
      });
      const piped = new ReadableStream({ start(c) { c.enqueue("x"); c.close(); } });
      const outReader = piped.pipeThrough(upper).getReader();
      const upperOut = [];
      for (;;) { const { value, done } = await outReader.read(); if (done) break; upperOut.push(value); }
      out.transform = upperOut;

      out.asyncIterable = await (async () => {
        const acc = [];
        for await (const v of new ReadableStream({ start(c) { c.enqueue(1); c.enqueue(2); c.close(); } })) acc.push(v);
        return acc;
      })();
      out.strategy = new CountQueuingStrategy({ highWaterMark: 3 }).highWaterMark;
`), Context{}))

	wantStrings(t, got, "read", "a", "b")
	wantStrings(t, got, "transform", "X")
	wantStrings(t, got, "asyncIterable", "1", "2")
	want(t, got, "locked", true)
	want(t, got, "strategy", 3)
	sink, ok := got["sink"].([]any)
	if !ok || len(sink) != 2 || fmt.Sprint(sink[0]) != "1" || fmt.Sprint(sink[1]) != "2" {
		t.Fatalf("pipeTo 未把数据按序写进 sink：%v", got["sink"])
	}
}

// ------------------------------------------------------------- console

// TestNodeBuiltinConsole 钉 node:console 与全局 console 都写宿主结构化日志，不写进程 stdout。
func TestNodeBuiltinConsole(t *testing.T) {
	var lines []string
	got := probeJSON(t, runBuiltin(t, jsTool(`import console from "node:console";`, `
      console.log("via-module", 42);
      globalThis.console.warn("via-global");
      console.count("c");
      console.count("c");
      const t = console.time("t"); void t;
      console.timeEnd("t");
      out.moduleLog = typeof console.log;
      out.hasConsole = typeof console.Console;
`), Context{Log: func(level, msg string, _ map[string]any) {
		lines = append(lines, level+"|"+msg)
	}}))

	want(t, got, "moduleLog", "function")
	// 有意简化：没有 Console 类（12 §6.12.2 已声明）。
	want(t, got, "hasConsole", "undefined")
	joined := strings.Join(lines, "\n")
	for _, expect := range []string{"info|via-module 42", "warn|via-global", "info|c: 1", "info|c: 2", "info|t: "} {
		if !strings.Contains(joined, expect) {
			t.Fatalf("日志里缺少 %q，实际：\n%s", expect, joined)
		}
	}
}

// ------------------------------------------------------------------ os

// TestNodeBuiltinOS 钉 os 的受限形状（networkInterfaces/userInfo 调用即抛，hostname/homedir 为空）。
func TestNodeBuiltinOS(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import os from "node:os";`, `
      out.platform = os.platform();
      out.arch = os.arch();
      out.type = os.type();
      out.eol = os.EOL;
      out.tmpdir = os.tmpdir();
      out.hostname = os.hostname();
      out.homedir = os.homedir();
      out.cpus = os.cpus().length;
      out.model = os.cpus()[0].model;
      out.endianness = os.endianness();
      out.parallelism = os.availableParallelism();
      out.totalmemIsNumber = typeof os.totalmem();
      out.net = (() => { try { os.networkInterfaces(); return "no-throw" } catch (e) { return e.code } })();
      out.user = (() => { try { os.userInfo(); return "no-throw" } catch (e) { return e.code } })();
`), Context{}))

	want(t, got, "platform", processPlatform())
	want(t, got, "arch", goruntime.GOARCH)
	want(t, got, "type", osType())
	want(t, got, "tmpdir", "/tmp")
	want(t, got, "hostname", "")
	want(t, got, "homedir", "")
	want(t, got, "model", "ada-go virtual cpu")
	want(t, got, "net", "ERR_NOT_SUPPORTED")
	want(t, got, "user", "ERR_NOT_SUPPORTED")
	want(t, got, "totalmemIsNumber", "number")
	if n, _ := got["cpus"].(float64); n < 1 {
		t.Fatalf("cpus().length = %v", got["cpus"])
	}
	if e, _ := got["eol"].(string); e == "" {
		t.Fatalf("EOL 不应为空")
	}
}

// -------------------------------------------------------------- module

// TestNodeBuiltinModule 钉 createRequire 的诚实失败面（E-3 未闭合）与内置清单。
func TestNodeBuiltinModule(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import mod, { createRequire, isBuiltin, builtinModules } from "node:module";`, `
      const require = createRequire("plugin://test/index.js");
      out.builtins = builtinModules;
      out.hasPath = builtinModules.indexOf("path") >= 0;
      out.hasCrypto = builtinModules.indexOf("crypto") >= 0;
      out.hasFS = builtinModules.indexOf("fs") >= 0;
      out.hasBuffer = builtinModules.indexOf("buffer") >= 0;
      out.hasProcess = builtinModules.indexOf("process") >= 0;
      out.noAgentHost = builtinModules.indexOf("agent:host") < 0;
      out.isBuiltinFS = isBuiltin("node:fs");
      out.isBuiltinBare = isBuiltin("left-pad");
      out.bareFS = isBuiltin("fs");
      out.esmErr = (() => { try { require("node:fs"); return "no-throw" } catch (e) { return e.code } })();
      out.missingErr = (() => { try { require("left-pad"); return "no-throw" } catch (e) { return e.code } })();
      out.resolve = (() => { try { return require.resolve("node:path") } catch (e) { return "throw:" + e.code } })();
      out.resolveMissing = (() => { try { return require.resolve("left-pad") } catch (e) { return e.code } })();
      out.cache = Object.keys(require.cache).length;
      out.defaultHasCreateRequire = typeof mod.createRequire;
      out.staticCreate = typeof mod.Module.createRequire;
`), Context{}))

	want(t, got, "hasPath", true)
	want(t, got, "hasCrypto", true)
	// 宿主实际能装出来的模块都要在清单里（fs/buffer/process 由上游 node 包与宿主桥提供）。
	want(t, got, "hasFS", true)
	want(t, got, "hasBuffer", true)
	want(t, got, "hasProcess", true)
	want(t, got, "noAgentHost", true)
	want(t, got, "isBuiltinFS", true)
	want(t, got, "isBuiltinBare", false)
	want(t, got, "bareFS", true)
	want(t, got, "esmErr", "ERR_REQUIRE_ESM")
	want(t, got, "missingErr", "ERR_MODULE_NOT_FOUND")
	want(t, got, "resolve", "node:path")
	want(t, got, "resolveMissing", "ERR_MODULE_NOT_FOUND")
	want(t, got, "cache", 0)
	want(t, got, "defaultHasCreateRequire", "function")
	want(t, got, "staticCreate", "function")
}

// -------------------------------------------------------------- timers

// TestNodeBuiltinTimers 钉 timers 模块的自对应：与全局是同一批函数对象（Node 的形状），
// clear 真的生效，且不存在的全局是「调用即抛 ERR_NOT_SUPPORTED」而不是 undefined。
func TestNodeBuiltinTimers(t *testing.T) {
	got := probeJSON(t, runBuiltin(t, jsTool(`import timers from "node:timers";`, `
      out.sameSetTimeout = timers.setTimeout === globalThis.setTimeout;
      out.sameClear = timers.clearTimeout === globalThis.clearTimeout;
      out.sameInterval = timers.setInterval === globalThis.setInterval;
      out.sameImmediate = timers.setImmediate === globalThis.setImmediate;
      out.order = await new Promise((resolve) => {
        const acc = [];
        const cancelled = timers.setTimeout(() => acc.push("cancelled"), 5);
        timers.clearTimeout(cancelled);
        timers.setImmediate(() => acc.push("immediate"));
        timers.setTimeout(() => { acc.push("timeout"); resolve(acc); }, 5);
      });
      out.badCallback = (() => { try { timers.setImmediate(42); return "no-throw" } catch (e) { return e.code } })();
`), Context{}))

	want(t, got, "sameSetTimeout", true)
	want(t, got, "sameClear", true)
	want(t, got, "sameInterval", true)
	// setImmediate 由引擎提供：模块导出与全局是同一个函数（Node 的形状）。
	want(t, got, "sameImmediate", true)
	want(t, got, "badCallback", "ERR_INVALID_ARG_TYPE")
	// immediate 先于 5ms 的 timeout，clear 掉的那个不出现。
	wantStrings(t, got, "order", "immediate", "timeout")
}

// ----------------------------------------------------- 模块表完整性

// TestNodeBuiltinModulesAllImportable 是「声称已实现」的完整性门禁：
// nodemods.Modules() 里的每一个名字都必须能在真实宿主装配里 import 并求值。
//
// 只做 moejs.Compile 的自检（nodemods_test.go）挡不住这一类回归：模块能编译，但
// 宿主没把它登记进模块表时，插件会在链接期失败。
func TestNodeBuiltinModulesAllImportable(t *testing.T) {
	// 每个模块至少要有这些导出（默认导出或具名导出都算），否则就是「装了个空壳」。
	mustHave := map[string]string{
		"path":           "join",
		"path/posix":     "sep",
		"path/win32":     "sep",
		"events":         "EventEmitter",
		"util":           "format",
		"crypto":         "createHash",
		"zlib":           "gzipSync",
		"console":        "log",
		"os":             "platform",
		"module":         "createRequire",
		"timers":         "setTimeout",
		"url":            "URL",
		"querystring":    "parse",
		"string_decoder": "StringDecoder",
		"assert":         "strictEqual",
		"stream/web":     "ReadableStream",
	}
	for _, name := range nodemods.Modules() {
		name := name
		t.Run(name, func(t *testing.T) {
			src := "import * as m from \"node:" + name + "\";\n" +
				"import d from \"node:" + name + "\";\n" +
				`export default function (agent) {
  ` + builtinProbeTool + `
    execute() {
      const keys = Object.keys(m).sort();
      const merged = Object.assign({}, m, d);
      return { content: [{ type: "text", text: JSON.stringify({ keys, has: Object.prototype.hasOwnProperty.call(merged, ` + "`" + mustHave[name] + "`" + `) }) }] };
    } });
}
`
			text := runBuiltin(t, src, Context{})
			var probe struct {
				Keys []string `json:"keys"`
				Has  bool     `json:"has"`
			}
			if err := json.Unmarshal([]byte(text), &probe); err != nil {
				t.Fatalf("输出不是 JSON: %v (%s)", err, text)
			}
			if len(probe.Keys) == 0 && !probe.Has {
				t.Fatalf("node:%s 求值后没有任何导出（模块体为空？）", name)
			}
			if !probe.Has {
				t.Fatalf("node:%s 缺少必需成员 %q（导出=%v）", name, mustHave[name], probe.Keys)
			}
		})
	}
}

// TestNodeBuiltinUnknownModuleIsHonest 钉「宿主表里没有的模块在链接期就失败」，
// 不允许出现「import 成功但成员是 undefined」的静默降级（12 §6.13 失败方向）。
func TestNodeBuiltinUnknownModuleIsHonest(t *testing.T) {
	for _, spec := range []string{"node:vm", "node:net", "node:worker_threads", "node:nope"} {
		spec := spec
		t.Run(spec, func(t *testing.T) {
			caps := testCaps(t, pluginapi.TrustThirdParty)
			src := "import x from \"" + spec + "\";\nexport default function (agent) { void x }\n"
			_, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{})
			if err == nil {
				t.Fatalf("import %q 必须在装配期失败", spec)
			}
			if !strings.Contains(err.Error(), strings.TrimPrefix(spec, "node:")) {
				t.Fatalf("错误应点名模块：%v", err)
			}
		})
	}
}

// TestNodeBuiltinModuleBindingPresence 钉「需要宿主 binding 的模块」只有 crypto 与 zlib：
// 这两个落到 Go 标准库（摘要/随机数、编解码）；其余模块是纯 JS，不该有 binding。
func TestNodeBuiltinModuleBindingPresence(t *testing.T) {
	rt := moejs.NewRuntime(moejs.Options{})
	for _, name := range nodemods.Modules() {
		base := strings.TrimPrefix(name, "node:")
		_, has := nodemods.Binding(rt, base)
		switch base {
		case "crypto", "zlib":
			if !has {
				t.Fatalf("%s 需要宿主 binding，但 nodemods.Binding 说没有", base)
			}
		default:
			if has {
				t.Fatalf("%s 不该有宿主 binding（纯 JS 模块）", base)
			}
		}
	}
}
