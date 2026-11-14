/*
 * router.mjs 的单测（node:test，零 npm 依赖）。
 * 运行：node --test internal/gateway/web/static/ada/test/router.test.mjs
 *
 * 全程不设 globalThis.window（末尾的滚动用例除外且用后即删），
 * 历史实现与 popstate 注册均注入假对象，验证 SSR 安全与守卫回滚。
 */
import { test } from "node:test";
import assert from "node:assert/strict";

import { createRouter } from "../router.mjs";

/* ---------- 测试基建 ---------- */

// 组件用普通函数即可，router 不关心其实现
function ViewA() {}
function ViewB() {}
function ViewThread() {}

// 假 history：只记录调用；不会自动触发 popstate（由测试手动驱动回调）
function makeFakeHistory() {
  const calls = [];
  return {
    calls: calls,
    pushState: function (state, title, url) {
      calls.push({ kind: "pushState", state: state, title: title, url: url });
    },
    replaceState: function (state, title, url) {
      calls.push({ kind: "replaceState", state: state, title: title, url: url });
    },
    back: function () {
      calls.push({ kind: "back" });
    },
    go: function (n) {
      calls.push({ kind: "go", n: n });
    },
  };
}

// 组装：router + 假 history + popstate 回调数组
function setup(routes) {
  const fake = makeFakeHistory();
  const pops = [];
  const router = createRouter({
    routes: routes,
    history: fake,
    onPop: function (cb) {
      pops.push(cb);
    },
  });
  return { router: router, fake: fake, pops: pops };
}

const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

/* ---------- 匹配 ---------- */

test("静态段优先于参数段：先注册先匹配", () => {
  const { router } = setup([
    { path: "/thread/new", name: "new", component: ViewA },
    { path: "/thread/:id", name: "detail", component: ViewThread },
  ]);

  const hitStatic = router.resolve("/thread/new");
  assert.equal(hitStatic.route.name, "new");
  assert.deepEqual(hitStatic.params, {});

  const hitParam = router.resolve("/thread/abc");
  assert.equal(hitParam.route.name, "detail");
  assert.deepEqual(hitParam.params, { id: "abc" });

  // addRoute 追加到末尾，不抢占已注册的 :id
  router.addRoute({ path: "/thread/newer", name: "newer", component: ViewB });
  assert.equal(router.resolve("/thread/newer").route.name, "detail");

  assert.equal(router.resolve("/nope"), null);
});

test("query 解析：URLSearchParams、重复 key 取最后一个、自动解码", async () => {
  const { router } = setup([{ path: "/list", name: "list", component: ViewA }]);

  await router.push("/list?a=1&b=2&b=3&name=%E4%B8%AD");
  assert.equal(router.current.value.path, "/list");
  assert.deepEqual(router.current.value.query, { a: "1", b: "3", name: "中" });

  await router.push("/list");
  assert.deepEqual(router.current.value.query, {});
});

test("404：matched/route 为 null，params 为空对象，query 仍解析", async () => {
  const { router } = setup([]);

  // 初始占位为 "/" 且无路由，即是 404 快照
  assert.equal(router.current.value.path, "/");
  assert.equal(router.current.value.matched, null);

  await router.push("/missing?x=1");
  const snap = router.current.value;
  assert.equal(snap.matched, null);
  assert.equal(snap.route, null);
  assert.deepEqual(snap.params, {});
  assert.deepEqual(snap.query, { x: "1" });
  assert.equal(snap.path, "/missing");
});

/* ---------- push/replace/back 与 history 调用 ---------- */

test("push/replace 更新 current 并按序调用 fakeHistory", async () => {
  const { router, fake } = setup([{ path: "/a", name: "a", component: ViewA }]);

  const ok = await router.push("/a");
  assert.equal(ok, true);
  assert.equal(fake.calls.length, 1);
  assert.equal(fake.calls[0].kind, "pushState");
  assert.equal(fake.calls[0].url, "/a");
  assert.deepEqual(fake.calls[0].state, { url: "/a" });
  assert.equal(router.current.value.path, "/a");
  assert.equal(router.current.value.matched, ViewA);
  assert.equal(router.current.value.route.name, "a");

  await router.replace("/a?x=9");
  assert.equal(fake.calls.length, 2);
  assert.equal(fake.calls[1].kind, "replaceState");
  assert.equal(fake.calls[1].url, "/a?x=9");
  assert.equal(router.current.value.path, "/a");
  assert.deepEqual(router.current.value.query, { x: "9" });

  await router.back();
  assert.equal(fake.calls[2].kind, "back");

  router.go(-2);
  assert.equal(fake.calls[3].kind, "go");
  assert.equal(fake.calls[3].n, -2);
});

/* ---------- popstate 同步 ---------- */

test("popstate 注入回调同步 current（含 query）；无 state 的裸事件被忽略", async () => {
  const { router, pops } = setup([
    { path: "/a", name: "a", component: ViewA },
    { path: "/b", name: "b", component: ViewB },
  ]);
  assert.equal(pops.length, 1); // 创建时即注册

  await router.push("/a");
  await pops[0]({ state: { url: "/b?k=1" } });
  assert.equal(router.current.value.path, "/b");
  assert.equal(router.current.value.route.name, "b");
  assert.deepEqual(router.current.value.query, { k: "1" });

  // 无 window 时取不到 location，回调直接返回，current 不变
  await pops[0]({});
  assert.equal(router.current.value.path, "/b");
});

/* ---------- beforeLeave 守卫 ---------- */

test("beforeLeave 同步 false 阻止 push：不写 history、current 不变", async () => {
  const { router, fake } = setup([
    { path: "/locked", name: "locked", component: ViewA, beforeLeave: () => false },
    { path: "/free", name: "free", component: ViewB },
  ]);

  await router.push("/locked"); // 离开初始空路由，无守卫
  assert.equal(router.current.value.path, "/locked");
  assert.equal(fake.calls.length, 1);

  const ok = await router.push("/free");
  assert.equal(ok, false);
  assert.equal(fake.calls.length, 1); // 没有新的 pushState
  assert.equal(router.current.value.path, "/locked");
  assert.equal(router.current.value.route.name, "locked");
});

test("beforeLeave 异步 Promise：放行与拦截，守卫收到目标快照 to", async () => {
  let allow = false;
  let capturedTo = null;
  const { router, fake } = setup([
    {
      path: "/gate",
      name: "gate",
      component: ViewA,
      beforeLeave: async function (to) {
        capturedTo = to;
        await tick();
        return allow;
      },
    },
  ]);

  await router.push("/gate");
  assert.equal(fake.calls.length, 1);

  await router.push("/target");
  assert.equal(fake.calls.length, 1); // Promise<false> 拦截
  assert.equal(router.current.value.path, "/gate");
  assert.equal(capturedTo.path, "/target"); // 守卫参数是目标快照

  allow = true;
  const ok = await router.push("/target");
  assert.equal(ok, true);
  assert.equal(fake.calls[1].url, "/target");
  assert.equal(router.current.value.path, "/target");
});

test("popstate 时 beforeLeave 阻止：用 pushState 回滚旧地址，current 不动", async () => {
  let allow = false;
  const { router, fake, pops } = setup([
    { path: "/vault", name: "vault", component: ViewA, beforeLeave: () => allow },
  ]);

  await router.push("/vault");
  assert.equal(fake.calls.length, 1);

  await pops[0]({ state: { url: "/elsewhere" } });
  // 守卫拦截：浏览器已改 URL，router 用 pushState 把旧地址压回
  assert.equal(fake.calls.length, 2);
  assert.equal(fake.calls[1].kind, "pushState");
  assert.equal(fake.calls[1].url, "/vault");
  assert.equal(router.current.value.path, "/vault");
  assert.equal(router.current.value.route.name, "vault");

  allow = true;
  await pops[0]({ state: { url: "/elsewhere" } });
  assert.equal(router.current.value.path, "/elsewhere");
  assert.equal(fake.calls.length, 2); // 放行不写 history（URL 由浏览器管理）
});

/* ---------- 动态路由 ---------- */

test("addRoute 后新路径可达：404 快照被重解析并通知订阅者", async () => {
  const { router } = setup([{ path: "/a", name: "a", component: ViewA }]);

  await router.push("/plugin/x");
  assert.equal(router.current.value.matched, null); // 此前 404

  const seen = [];
  router.subscribe((snap) => seen.push(snap));

  router.addRoute({ path: "/plugin/:name", name: "pl", component: ViewB });
  assert.equal(router.current.value.matched, ViewB);
  assert.equal(router.current.value.route.name, "pl");
  assert.deepEqual(router.current.value.params, { name: "x" });
  assert.equal(seen.length, 1); // 重解析触发了通知
});

test("removeRoute 后 current 重解析为 null 并通知；支持按 name 删除", async () => {
  const { router } = setup([
    { path: "/a", name: "a", component: ViewA },
    { path: "/b", name: "b", component: ViewB },
  ]);

  await router.push("/a");
  const seen = [];
  router.subscribe((snap) => seen.push(snap));

  assert.equal(router.removeRoute("/a"), true); // 按 path
  assert.equal(router.current.value.matched, null);
  assert.equal(router.current.value.route, null);
  assert.equal(router.resolve("/a"), null);
  assert.equal(seen.length, 1);

  await router.push("/b");
  assert.equal(router.current.value.route.name, "b");
  assert.equal(seen.length, 2); // push 本身也通知一次

  assert.equal(router.removeRoute("b"), true); // 按 name
  assert.equal(router.current.value.matched, null);
  assert.equal(seen.length, 3);

  assert.equal(router.removeRoute("/not-exists"), false); // 没删到东西
  assert.equal(seen.length, 3); // 不通知
});

/* ---------- 订阅 ---------- */

test("subscribe 收到新快照、退订生效、current.value 赋值也通知", async () => {
  const { router } = setup([{ path: "/a", name: "a", component: ViewA }]);

  const seen = [];
  const off = router.subscribe((snap) => seen.push(snap));

  await router.push("/a");
  assert.equal(seen.length, 1);
  assert.equal(seen[0], router.current.value); // 回调收到的就是新快照

  off();
  await router.push("/a?x=1");
  assert.equal(seen.length, 1); // 已退订

  router.current.value = { path: "/manual", params: {}, query: {}, matched: null, route: null };
  assert.equal(seen.length, 1); // 退订后不再通知
  assert.equal(router.current.value.path, "/manual"); // 赋值本身生效
});

/* ---------- 无 window 环境 ---------- */

test("无 window 环境：模块可导入、注入 history 可完整工作、参数自动解码", async () => {
  assert.equal(typeof globalThis.window, "undefined"); // 本测试进程从未设置 window

  const { router, pops } = setup([
    { path: "/thread/:id", name: "thread", component: ViewThread },
  ]);

  await router.push("/thread/a%20b?x=1");
  // path 保留 URL 原样（编码形式，与 window.location.pathname 一致），params 已解码
  assert.equal(router.current.value.path, "/thread/a%20b");
  assert.deepEqual(router.current.value.params, { id: "a b" });

  await pops[0]({ state: { url: "/thread/c" } });
  assert.deepEqual(router.current.value.params, { id: "c" });

  // 不注入 history 也不抛错：创建与 resolve 可用（SSR 场景）
  const bare = createRouter({ routes: [{ path: "/x", name: "x", component: ViewA }] });
  assert.deepEqual(bare.resolve("/x").params, {});
  assert.equal(bare.current.value.path, "/"); // 初始占位，无 match
});

test("导航后调用 window.scrollTo(0,0)；window 缺失时不炸（用完即删）", async () => {
  const scrolls = [];
  globalThis.window = { scrollTo: (x, y) => scrolls.push([x, y]) };
  try {
    const { router } = setup([{ path: "/a", name: "a", component: ViewA }]);
    await router.push("/a");
    assert.deepEqual(scrolls, [[0, 0]]);
  } finally {
    delete globalThis.window;
  }
  assert.equal(typeof globalThis.window, "undefined"); // 清理干净，不影响其它用例
});

test("初始快照：createRouter 后 current.value 即为命中路由（宿主首屏依赖）", () => {
	const R = { path: "/" };
	const router = createRouter({ routes: [{ path: "/", component: R }], history: fakeHistoryFor() });
	assert.ok(router.current.value !== null);
	assert.equal(router.current.value.path, "/");
	assert.equal(router.current.value.matched, R);
	function fakeHistoryFor() {
		return { pushState() {}, replaceState() {}, back() {} };
	}
});
