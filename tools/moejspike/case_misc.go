package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"hash"
	"runtime"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	"github.com/MoeForge/moejs/webapi"
)

func init() {
	register("misc", "取消/克隆/编码与宿主可装面：AbortSignal、structuredClone、console/process/crypto", runMisc)
}

func runMisc(r *recorder) {
	t, err := makeTree("misc", 0)
	if err != nil {
		r.add(vNa, "夹具", "建工作区", err.Error(), "misc 用例未跑")
		return
	}
	base := cfg{useNode: true, install: nodeFS(t, "/workspace", false), web: &webapi.Options{}}

	// 1) AbortSignal 全家桶
	o := runJS(js(`const out = {};
const ac = new AbortController();
ac.abort(new Error("自定义 reason"));
out.reason = String(ac.signal.reason.message);
out.aborted = ac.signal.aborted;
out.throwIfAborted = (() => { try { ac.signal.throwIfAborted(); return "no-throw" } catch (e) { return e.name + ":" + e.message } })();
out.timeoutCtor = typeof AbortSignal.timeout;
out.anyCtor = typeof AbortSignal.any;
const tim = AbortSignal.timeout(20);
out.timeoutFired = await new Promise((res) => { tim.addEventListener("abort", () => res(String(tim.reason && tim.reason.name))); setTimeout(() => res("never"), 300) });
const a = new AbortController(), b = new AbortController();
const comp = AbortSignal.any([a.signal, b.signal]);
b.abort(new Error("第二个"));
out.anyFollows = String(comp.aborted) + "/" + String(comp.reason.message);
out.domException = typeof DOMException;
out.eventTarget = typeof EventTarget;
__report(JSON.stringify(out));`), base)
	r.add(ifElse(strings.Contains(o.report, "自定义 reason") && strings.Contains(o.report, `"anyCtor":"function"`), vMatch, vDev), "6.11/取消",
		"6.11 取消：AbortController/AbortSignal 的 reason、throwIfAborted、AbortSignal.timeout、AbortSignal.any、abort 事件",
		o.actual(),
		"webapi 包给的是完整 WhatWG 形状（含 timeout/any/reason/throwIfAborted），设计这一行现在就能兑现，前提是宿主装上 webapi")

	// 2) 监听器清理与组合信号泄漏
	o = runJS(js(`const ac = new AbortController();
let ran = 0;
const hs = [];
for (let i = 0; i < 10; i++) { const f = () => { ran++ }; hs.push(f); ac.signal.addEventListener("abort", f) }
ac.signal.dispatchEvent(new Event("abort"));
for (const f of hs) ac.signal.removeEventListener("abort", f)
ac.signal.dispatchEvent(new Event("abort"));
const a = new AbortController();
const comp = AbortSignal.any([a.signal]);
__report(JSON.stringify({afterRemove: ran, listenerCount: typeof ac.signal.onabort}));`), base)
	r.add(ifElse(strings.Contains(o.report, `"afterRemove":10`), vMatch, vDev), "6.11/监听器可清理",
		"6.11 取消：监听器可清理（防泄漏）",
		o.actual(),
		"addEventListener/removeEventListener 成对可用；AbortSignal.any 的组合信号是否随依赖释放要在长驻 Runtime 里测引用计数，本探针只证明了显式清理可用")

	// 3) structuredClone 与 transfer
	o = runJS(js(`const out = {};
const ta = new Uint8Array([1, 2, 3]);
const clone = structuredClone({ta, m: new Map([[1, "a"]]), d: new Date(0)});
out.deep = clone.ta[2] === 3 && clone.m.get(1) === "a" && clone.ta !== ta;
const buf = new ArrayBuffer(8);
const view = new Uint8Array(buf);
try { structuredClone({buf}, {transfer: [buf]}); out.transferred = true } catch (e) { out.transferred = "throw:" + e.name }
out.detachedLength = (() => { try { return view.length } catch (e) { return "throw:" + e.name } })();
const cyc = {a: 1}; cyc.self = cyc;
out.cycle = (() => { try { return structuredClone(cyc).self.a } catch (e) { return "throw:" + e.name } })();
out.proxy = (() => { try { structuredClone(new Proxy({}, {})); return "no-throw" } catch (e) { return e.name } })();
__report(JSON.stringify(out));`), base)
	r.add(ifElse(strings.Contains(o.report, `"deep":true`), vMatch, vDev), "6.11/结构化克隆",
		"6.11 二进制与编码：structuredClone 与 transfer；循环引用",
		o.actual(),
		"引擎自带 structuredClone（不依赖 webapi），Map/Date/TypedArray 与循环引用都对；transfer 语义见本条输出")

	// 4) Buffer 全局与 node:buffer
	o = runJS(js(`import buffer from "node:buffer";
const out = {};
out.isUint8 = Buffer.from("abc") instanceof Uint8Array;
out.allocUnsafeZeroed = Array.from(Buffer.allocUnsafe(8));
out.hex = Buffer.from("616263", "hex").toString("utf8");
out.latin1 = Buffer.from([0xe9]).toString("latin1");
out.kMaxLength = buffer.kMaxLength;
out.byteLengthCJK = buffer.Buffer.byteLength("中文");
out.globalSame = buffer.Buffer === Buffer;
out.atob = typeof buffer.atob;
out.noBigIntReads = typeof Buffer.alloc(8).readBigInt64LE;
__report(JSON.stringify(out));`), base)
	r.add(classify(o, ""), "6.12.2/buffer",
		"6.12.2：Buffer.from/alloc/allocUnsafe/concat/isBuffer/byteLength；Buffer 是全局；allocUnsafe 仍清零",
		o.actual(),
		"Buffer 全局由 node.Options.Buffer=true 给出；allocUnsafe 确实清零（上游无池）；kMaxLength 是 2GiB-1；readBigInt64LE 等大整数读写没有（TODO.md 列了）")

	// 5) TextDecoder / atob / btoa
	o = runJS(js(`const out = {};
out.fatal = (() => { try { return new TextDecoder("utf-8", {fatal: true}).decode(new Uint8Array([0xff])) } catch (e) { return "throw:" + e.name } })();
out.ignoreBOM = new TextDecoder("utf-8", {ignoreBOM: true}).decode(new Uint8Array([0xef, 0xbb, 0xbf, 0x41]));
out.atob = typeof atob;
out.btoaThrows = (() => { try { return btoa("中") } catch (e) { return "throw:" + e.name } })();
__report(JSON.stringify(out));`), base)
	r.add(classify(o, ""), "6.11/编码与 atob-btoa",
		"6.11：TextEncoder/TextDecoder（UTF-8、fatal、ignoreBOM）；6.12.2：atob/btoa 是全局",
		o.actual(),
		"TextEncoder/TextDecoder 是引擎内建（不需要 webapi）；atob/btoa 由引擎提供并按 WHATWG 校验（Node 的校验更宽松，非 ASCII 输入的错误类型要写进偏差表）")

	// 6) 宿主可装面：console 接 writer
	logs := []string{}
	o = runJS(js(`console.log("一条", {a: 1});
console.error("错误", new Error("boom"));
__report(JSON.stringify({typeofConsole: typeof console}));`), cfg{
		useNode: true, install: nodeFS(t, "/workspace", false),
		globals: func(h *hostContext) { installConsole(h, &logs) },
	})
	r.add(ifElse(len(logs) == 2, vHost, vDev), "6.12.2/console 接结构化日志",
		"6.12.2：console 的 log/info/warn/error/debug 映射到结构化日志，不写进程 stdout；长参数按 util.inspect 截断",
		o.actual()+"\n         宿主收到="+fmt.Sprint(logs),
		"上游没有 console，也没有进程 stdout/stderr 通道（设计的 Stdout/Stderr writer 在 moejs 里不存在）：宿主用 SetGlobal(map[string]any{NativeFunc}) 就能装，参数经 ToGo/AppendJSON 落到 slog；截断与脱敏也在这一层做。结论：console 100% 宿主工作量，无需上游")

	// 7) 宿主可装面：process
	o = runJS(js(`const out = {};
out.argv = process.argv;
out.envKeys = Object.keys(process.env);
out.cwd = process.cwd();
out.version = process.version;
out.versions = process.versions;
out.pid = typeof process.pid;
out.hrtime = typeof process.hrtime;
out.nextTick = await new Promise((res) => process.nextTick(() => res("ran")));
out.exit = (() => { try { process.exit(0); return "没有抛" } catch (e) { return "throw:" + e.code } })();
out.kill = typeof process.kill;
out.dlopen = typeof process.dlopen;
out.stdout = (() => { try { return process.stdout.write("x") } catch (e) { return "throw:" + e.code } })();
__report(JSON.stringify(out));`), cfg{
		useNode: true, install: nodeFS(t, "/workspace", false),
		globals: func(h *hostContext) { installProcess(h) },
	})
	r.add(ifElse(strings.Contains(o.report, `"exit":"throw:ERR_ACCESS_DENIED"`) && strings.Contains(o.report, `"cwd":"/workspace"`), vHost, vDev), "6.12.2/process",
		"6.12.2：argv/env(白名单)/cwd/platform/arch/version/versions/pid/hrtime/nextTick/stdout/stderr；cwd 是虚拟根；exit 抛异常；没有 kill/binding/dlopen",
		o.actual(),
		"上游完全没有 process：设计里这一整行（含 env 白名单、exit 抛异常、版本报兼容基线）都是宿主工作量；机制是 SetGlobal，env 用只读对象、按清单白名单填，未列出的名字根本不存在")

	// 8) 宿主可装面：node:crypto 最小集
	o = runJS(js(`import { createHash, randomBytes, timingSafeEqual } from "node:crypto";
const out = {};
out.sha256 = createHash("sha256").update("abc").digest("hex");
out.len = randomBytes(16).length;
out.equal = timingSafeEqual(Buffer.from("a"), Buffer.from("a"));
out.badAlgo = (() => { try { return createHash("md5").digest("hex").length } catch (e) { return "throw:" + e.code } })();
out.globalCrypto = typeof globalThis.crypto;
__report(JSON.stringify(out));`), cfg{
		useNode: true, install: nodeFS(t, "/workspace", false),
		globals: func(h *hostContext) { installCrypto(h) },
		virtual: map[string]string{
			"node:crypto": "const b = globalThis.__crypto;\nexport const createHash = b.createHash;\nexport const randomBytes = b.randomBytes;\nexport const timingSafeEqual = b.timingSafeEqual;\nexport default b;\n",
		},
	})
	r.add(ifElse(strings.Contains(o.report, "ba7816bf8f01cfea"), vHost, vDev), "6.12.2/crypto",
		"6.12.2：createHash(md5/sha1/sha2/sha3)/createHmac/pbkdf2/hkdf/randomBytes/randomInt/randomUUID/timingSafeEqual；crypto.subtle 的 digest/HMAC/PBKDF2/HKDF",
		o.actual(),
		"上游没有任何 crypto：宿主用 Go 标准库 + virtual module 就装出了 createHash/randomBytes/timingSafeEqual（sha256(\"abc\") 已知向量对上）。WebCrypto 的 subtle 要么宿主再做一层，要么先不装")

	// 9) zlib：完全没有
	o = runJS(js(`import { gzip } from "node:zlib";
__report(JSON.stringify({type: typeof gzip}));`), base)
	r.add(ifElse(o.linkErr != nil, vGap, vMatch), "6.12.2/zlib",
		"6.12.2：gzip/gunzip/deflate/inflate/deflateRaw/inflateRaw 及同步形式；CompressionStream/DecompressionStream；解压产物夹到 16MB",
		o.actual(),
		"node:zlib 不存在，webapi 也没有 CompressionStream/DecompressionStream；宿主可以用 Go compress/* 装同步与异步形式，16MB 上限由宿主自己夹——E-6 的又一处宿主工作量")
}

// installConsole 是宿主版 console：参数经 JSON 落到切片，模拟结构化日志 writer。
func installConsole(h *hostContext, sink *[]string) {
	make := func(level string) moejs.NativeFunc {
		return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			parts := make([]string, 0, len(args))
			for _, a := range args {
				b, err := h.rt.AppendJSON(nil, a)
				if err != nil {
					parts = append(parts, a.String())
					continue
				}
				parts = append(parts, string(b))
			}
			*sink = append(*sink, level+": "+strings.Join(parts, " "))
			return moejs.Undefined(), nil
		}
	}
	if err := h.rt.SetGlobal("console", map[string]any{
		"log": moejs.NativeFunc(make("log")), "info": moejs.NativeFunc(make("info")),
		"warn": moejs.NativeFunc(make("warn")), "error": moejs.NativeFunc(make("error")),
		"debug": moejs.NativeFunc(make("debug")),
	}); err != nil {
		h.out.setupErr = fmt.Errorf("SetGlobal console: %w", err)
	}
}

// installProcess 是宿主版 process：env 白名单、虚拟 cwd、exit 抛 ERR_ACCESS_DENIED。
func installProcess(h *hostContext) {
	nextTick := func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		fn := moejs.Arg(args, 0)
		if !engine.IsCallable(fn) {
			return moejs.Undefined(), r.TypeError("nextTick 需要函数")
		}
		rest := args
		if len(rest) > 1 {
			rest = rest[1:]
		} else {
			rest = nil
		}
		h.loop.RunOnLoop(func(rt *moejs.Runtime) {
			_, _ = rt.CallFunction(fn, moejs.Undefined(), rest...)
		})
		return moejs.Undefined(), nil
	}
	proc := map[string]any{
		"argv": []string{"adag-plugin", "spike"},
		"cwd": moejs.NativeFunc(func(r *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			return moejs.String("/workspace"), nil
		}),
		"env": map[string]any{"KEEP": "1"},
		"exit": moejs.NativeFunc(func(r *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			return moejs.Undefined(), nodeError(r, engine.KindError, "Error", "ERR_ACCESS_DENIED", "插件不允许结束进程", nil)
		}),
		"nextTick": moejs.NativeFunc(nextTick),
		"version":  "v20.0.0",
		"versions": map[string]any{"node": "20.0.0", "ada": "0.1.0", "moejs": "v0.1.0-alpha.11"},
		"platform": runtime.GOOS,
		"arch":     runtime.GOARCH,
		"pid":      1,
		"hrtime": moejs.NativeFunc(func(r *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			return r.FromGo([]int64{0, 0})
		}),
		"stdout": map[string]any{"write": moejs.NativeFunc(func(r *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) { return r.FromGo(false) })},
		"stderr": map[string]any{"write": moejs.NativeFunc(func(r *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) { return r.FromGo(false) })},
	}
	if err := h.rt.SetGlobal("process", proc); err != nil {
		h.out.setupErr = fmt.Errorf("SetGlobal process: %w", err)
	}
}

// installCrypto 是宿主版 node:crypto 的最小集：createHash/randomBytes/timingSafeEqual。
func installCrypto(h *hostContext) {
	binding := map[string]any{
		"randomBytes": moejs.NativeFunc(func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			n, _ := intOf(moejs.Arg(args, 0))
			buf := make([]byte, n)
			if _, err := rand.Read(buf); err != nil {
				return moejs.Undefined(), err
			}
			return r.FromGo(buf)
		}),
		"timingSafeEqual": moejs.NativeFunc(func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			a, err1 := h.rt.ToGo(moejs.Arg(args, 0))
			b, err2 := h.rt.ToGo(moejs.Arg(args, 1))
			if err1 != nil || err2 != nil {
				return moejs.Undefined(), r.TypeError("timingSafeEqual 需要两个 Buffer")
			}
			ba, ok1 := a.([]byte)
			bb, ok2 := b.([]byte)
			if !ok1 || !ok2 {
				return r.FromGo(fmt.Sprint(a) == fmt.Sprint(b))
			}
			return r.FromGo(len(ba) == len(bb) && subtle.ConstantTimeCompare(ba, bb) == 1)
		}),
		"createHash": moejs.NativeFunc(func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			algo := moejs.Arg(args, 0).String()
			var hv hash.Hash
			switch algo {
			case "sha256":
				hv = sha256.New()
			default:
				return moejs.Undefined(), nodeError(r, engine.KindTypeError, "TypeError", "ERR_INVALID_ARG_VALUE", "spike 版只支持 sha256", nil)
			}
			obj, err := r.FromGo(map[string]any{})
			if err != nil {
				return moejs.Undefined(), err
			}
			o := obj.AsObject()
			upd := r.NewHostFunction(engine.FromGoString("update"), 1, func(rr *moejs.Realm, _ engine.Value, a []engine.Value) (engine.Value, error) {
				hv.Write([]byte(moejs.Arg(a, 0).String()))
				return obj, nil
			})
			dig := r.NewHostFunction(engine.FromGoString("digest"), 0, func(rr *moejs.Realm, _ engine.Value, _ []engine.Value) (engine.Value, error) {
				return moejs.String(hex.EncodeToString(hv.Sum(nil))), nil
			})
			_ = o.SetProp(r, r.KeyFromGoString("update"), engine.ObjectValue(upd))
			_ = o.SetProp(r, r.KeyFromGoString("digest"), engine.ObjectValue(dig))
			return obj, nil
		}),
	}
	if err := h.rt.SetGlobal("__crypto", binding); err != nil {
		h.out.setupErr = fmt.Errorf("SetGlobal __crypto: %w", err)
	}
}
