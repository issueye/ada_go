/*
 * ada 自研前端路由器（零依赖单文件，History API + 订阅式渲染）。
 *
 * 用法（宿主脚本）：
 *   import { createRouter } from "./ada/router.mjs";
 *
 *   const router = createRouter({
 *     routes: [
 *       { path: "/thread/:id", name: "thread", component: ThreadView, meta: { title: "线程" } },
 *     ],
 *   });
 *
 *   router.subscribe((snap) => {
 *     if (!snap.matched) {
 *       renderNotFound(snap); // 404：宿主自己渲染错误视图，router 不渲染任何 DOM
 *       return;
 *     }
 *     outlet.replaceChildren(snap.matched(snap)); // matched 即该路由的 component
 *   });
 *   router.push("/thread/abc?a=1");
 *
 * current.value 快照形状（每次导航整体替换并通知订阅者）：
 *   {
 *     path:    "/thread/abc",   // pathname，不含 query；结尾 "/" 已归一化，保留 URL 编码形式
 *     params:  { id: "abc" },   // 段级路径参数（已解码）；未匹配时为 {}
 *     query:   { a: "1" },      // URLSearchParams 解析；重复 key 取最后一个
 *     matched: ThreadView|null, // 命中路由的 component；404 时为 null
 *     route:   {...}|null,      // 命中的路由记录（含 name/meta/beforeLeave）
 *   }
 *
 * 可注入项（测试 / SSR 用）：
 *   createRouter({ routes, history, onPop })
 *   - history: 需要 pushState/replaceState/back（go 可选），缺省 window.history；
 *   - onPop(cb): 注册 popstate 回调，缺省 window.addEventListener("popstate", cb)。
 *
 * 其它约定：
 *   - push/replace 返回 Promise<boolean>，false 表示被 beforeLeave 拦截；
 *   - beforeLeave(to) 定义在“即将离开”的路由上，返回 false（或 Promise<false>）拦截；
 *     拦截 push/replace 时 URL 还没动；拦截 popstate 时浏览器已改 URL，
 *     用 pushState 把旧地址压回（回滚）；
 *   - 模块顶层不触碰 window：无 window 环境可安全 import，注入 history 即可工作；
 *   - 匹配按注册顺序（先注册先匹配），addRoute 追加到末尾，不会抢占已有路由。
 */

/* ---------- 纯函数：URL 解析与匹配 ---------- */

// 规范化：转字符串、去 hash、补前导 "/"；空值按 "/" 处理
function normalizeUrl(path) {
  let url = typeof path === "string" && path ? path : "/";
  const hash = url.indexOf("#");
  if (hash !== -1) url = url.slice(0, hash);
  if (url.charAt(0) !== "/") url = "/" + url;
  return url;
}

// 拆 pathname 与 search（search 保留前导 "?"，可为 ""）
function splitUrl(url) {
  const q = url.indexOf("?");
  if (q === -1) return { pathname: url, search: "" };
  return { pathname: url.slice(0, q), search: url.slice(q) };
}

// 去结尾 "/"（根路径除外），使 "/a/" 与 "/a" 等价
function stripTrailingSlash(pathname) {
  if (pathname.length > 1 && pathname.charAt(pathname.length - 1) === "/") {
    return pathname.slice(0, -1);
  }
  return pathname;
}

// query 字符串 → 普通对象（走 URLSearchParams；重复 key 取最后一个）
function parseQuery(search) {
  const out = {};
  if (!search) return out;
  const sp = new URLSearchParams(search.charAt(0) === "?" ? search.slice(1) : search);
  for (const entry of sp.entries()) out[entry[0]] = entry[1];
  return out;
}

// 参数段解码；非法编码退回原文（与 api.js 同款兜底）
function decodeSegment(raw) {
  try {
    return decodeURIComponent(raw);
  } catch (err) {
    return raw;
  }
}

/* ---------- 路由器 ---------- */

export function createRouter({ routes, history, onPop } = {}) {
  // 路由表（可变）：匹配按数组顺序，先注册先匹配；拷贝一份防外部改动
  let routeTable = Array.isArray(routes) ? routes.slice() : [];

  // 未注入且无 window 时为 null，此时 push/replace/back 会抛错（SSR 只用 resolve）
  const historyImpl =
    history || (typeof window !== "undefined" && window.history ? window.history : null);

  // 已提交的完整 url（path+search），与 current.value 平行维护：
  // popstate 被守卫拦截时浏览器已改 URL，需要它做 pushState 回滚
  let committedUrl = "/";
  if (typeof window !== "undefined" && window.location) {
    committedUrl = window.location.pathname + (window.location.search || "");
  }

  let snapshot = buildSnapshot(committedUrl);
  const listeners = new Set();

  // current 是最小 reactive ref：value 被赋值即通知全部订阅者
  const current = {
    get value() {
      return snapshot;
    },
    set value(next) {
      snapshot = next;
      notify(next);
    },
  };

  function notify(value) {
    // 拷贝后遍历：允许回调里退订；单个订阅者异常不阻断导航
    for (const fn of Array.from(listeners)) {
      try {
        fn(value);
      } catch (err) {
        /* 订阅者错误静默 */
      }
    }
  }

  // 订阅导航变化（回调收到新快照），返回退订函数
  function subscribe(fn) {
    listeners.add(fn);
    return function () {
      listeners.delete(fn);
    };
  }

  // 目标解析：path（去尾斜杠）+ search + 完整 url（写 history 与回滚都用它）
  function parseTarget(url) {
    const parts = splitUrl(normalizeUrl(url));
    const path = stripTrailingSlash(parts.pathname);
    return { path: path, search: parts.search, url: path + parts.search };
  }

  // 段级匹配：静态段全等，":x" 段收参数；不做正则
  function matchRoute(pathname) {
    const segs = stripTrailingSlash(pathname).split("/");
    for (const route of routeTable) {
      const pattern = typeof route.path === "string" ? stripTrailingSlash(route.path) : "";
      const parts = pattern.split("/");
      if (parts.length !== segs.length) continue;
      const params = {};
      let hit = true;
      for (let i = 0; i < parts.length; i++) {
        const part = parts[i];
        if (part.charAt(0) === ":") {
          params[part.slice(1)] = decodeSegment(segs[i]);
        } else if (part !== segs[i]) {
          hit = false;
          break;
        }
      }
      if (hit) return { route: route, params: params };
    }
    return null;
  }

  // 由 url 构造快照；404 时 matched/route 为 null，但 query 仍解析
  function buildSnapshot(url) {
    const target = parseTarget(url);
    const matched = matchRoute(target.path);
    return {
      path: target.path,
      params: matched ? matched.params : {},
      query: parseQuery(target.search),
      matched: matched && matched.route.component ? matched.route.component : null,
      route: matched ? matched.route : null,
    };
  }

  // 契约入口：命中返回 { route, params }，未命中 null
  function resolve(path) {
    return matchRoute(parseTarget(path).path);
  }

  // 执行“即将离开”路由的 beforeLeave(to)；显式 false 或抛错都视为拦截
  async function runLeaveGuard(to) {
    const guard = snapshot.route && snapshot.route.beforeLeave;
    if (typeof guard !== "function") return true;
    try {
      const verdict = await guard(to);
      return verdict !== false;
    } catch (err) {
      return false; // 守卫出错宁可不动
    }
  }

  // 无 window 时静默跳过滚动恢复
  function restoreScroll() {
    try {
      if (typeof window !== "undefined") window.scrollTo(0, 0);
    } catch (err) {
      /* 忽略 */
    }
  }

  // push/replace 共用：先跑守卫，再写 URL，最后提交快照。
  // 守卫期间不排队（并发 push 的竞态由宿主自行避免）
  async function navigate(url, method) {
    const target = parseTarget(url);
    const to = buildSnapshot(target.url);
    if (!(await runLeaveGuard(to))) return false;
    // 先写 URL 再改 current：history 抛错时 current 保持原状
    historyImpl[method]({ url: target.url }, "", target.url);
    committedUrl = target.url;
    current.value = to; // 触发订阅者
    restoreScroll();
    return true;
  }

  function push(path) {
    return navigate(path, "pushState");
  }

  function replace(path) {
    return navigate(path, "replaceState");
  }

  // 交给 history：真实导航在 popstate 回调里同步（含守卫与回滚）
  function back() {
    historyImpl.back();
  }

  // go 需要宿主历史实现支持；fake history 可能没实现，做能力探测
  function go(delta) {
    if (historyImpl && typeof historyImpl.go === "function") historyImpl.go(delta);
  }

  // 从 popstate 事件取目标 url：优先用我们写入的 state.url；
  // 首次进入页面的历史项没有 state，退回 window.location
  function popUrl(event) {
    const state = event && event.state;
    if (state && typeof state.url === "string") return state.url;
    if (typeof window !== "undefined" && window.location) {
      return window.location.pathname + (window.location.search || "");
    }
    return null;
  }

  // popstate：URL 已被浏览器改掉，重新解析并同步 current；
  // 守卫拦截时用 pushState 把旧地址压回（回滚）
  async function handlePop(event) {
    const url = popUrl(event);
    if (url === null) return;
    const target = parseTarget(url);
    const to = buildSnapshot(target.url);
    if (!(await runLeaveGuard(to))) {
      try {
        historyImpl.pushState({ url: committedUrl }, "", committedUrl);
      } catch (err) {
        /* 回滚失败只能忽略 */
      }
      return;
    }
    committedUrl = target.url;
    current.value = to;
    restoreScroll();
  }

  // 插件动态注册页面路由；追加到末尾（先注册先匹配，不抢占已有路由）。
  // 若当前正处在 404，立即重解析，命中即通知
  function addRoute(route) {
    if (!route || typeof route.path !== "string") {
      throw new TypeError("addRoute: route.path 必须是字符串");
    }
    routeTable.push(route);
    if (snapshot.route === null) {
      const to = buildSnapshot(committedUrl);
      if (to.route !== null) current.value = to;
    }
  }

  // 按 name 或 path 注销；删到才返回 true。
  // 当前若落在被删路由上则重解析：可能变 404，也可能落到后注册的同路径路由
  function removeRoute(nameOrPath) {
    const next = routeTable.filter(function (route) {
      return !(route && (route.name === nameOrPath || route.path === nameOrPath));
    });
    if (next.length === routeTable.length) return false;
    routeTable = next;
    const to = buildSnapshot(committedUrl);
    if (to.route !== snapshot.route) current.value = to;
    return true;
  }

  /* ---------- outlet（可选便捷能力）---------- */
  // 契约推荐宿主用 subscribe 自行渲染；这里只提供最省事的默认实现：
  // 把 matched 组件挂进 el；404 时不触碰 el（错误视图由宿主渲染）。

  function installOutlet(el) {
    const render = function (snap) {
      renderOutlet(el, snap);
    };
    const off = subscribe(render);
    render(snapshot); // 先按当前快照渲染一次
    return off;
  }

  function renderOutlet(el, snap) {
    if (!el || !snap || !snap.matched) return; // 404：router 不渲染任何 DOM
    if (typeof el.replaceChildren === "function") {
      el.replaceChildren();
    } else {
      while (el.firstChild) el.removeChild(el.firstChild);
    }
    const node = mountComponent(snap.matched, snap);
    if (node && typeof node.nodeType === "number") el.appendChild(node);
  }

  // component 支持：渲染函数（返回 Node）或元素构造器（如自定义元素类）
  function mountComponent(component, ctx) {
    if (typeof component !== "function") return null;
    let node = null;
    try {
      node = component(ctx);
    } catch (err) {
      try {
        node = new component(); // 构造器不能直接调用，按类实例化
      } catch (inner) {
        node = null;
      }
    }
    return node && typeof node.nodeType === "number" ? node : null;
  }

  // popstate 注册：优先注入的 onPop，否则有 window 才挂监听（SSR/测试环境跳过）
  if (typeof onPop === "function") {
    onPop(handlePop);
  } else if (typeof window !== "undefined") {
    window.addEventListener("popstate", handlePop);
  }

  return {
    current: current,
    subscribe: subscribe,
    go: go,
    push: push,
    replace: replace,
    back: back,
    installOutlet: installOutlet,
    addRoute: addRoute,
    removeRoute: removeRoute,
    resolve: resolve,
  };
}
