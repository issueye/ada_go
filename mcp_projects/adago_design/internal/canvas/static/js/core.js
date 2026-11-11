/* adago-design 画布 · 核心：状态 S、根元素、令牌读取、询问/确认弹层（uiAsk/uiConfirm）、toast、api、esc。
 * 经典脚本按 index.html 顺序加载，全局作用域共享（与 inspect.js 同一契约）；
 * 各文件顶层语句的执行顺序与拆分前完全一致，请勿调换加载顺序。 */
"use strict";
// ---------------- 状态 ----------------
const S = {
  nodes: new Map(),      // id -> node
  els: new Map(),        // id -> element
  edges: new Map(),      // id -> edge
  tx: 0, ty: 0, scale: 1,
  selected: null,
  drag: null,            // {id, el, sx, sy, ox, oy, nx, ny, raf, moved, pid}
  panning: null,         // {sx, sy, tx, ty, pid}
  catalogTheme: "light",
  online: true,
  project: "",          // 画布作用域："" = 全部，否则为项目 ID
  projects: [],         // 项目列表（含每个项目的节点数）
  canvas: null,         // 当前画布版式（项目属性）：{width,height,ratio} 或 null（自由画布）
  presets: [],          // 画布比例预设（GET /api/canvas/presets）
  minSide: 320,         // 画布宽高下限（服务端下发，用于前端快速校验）
  maxSide: 8192,        // 画布宽高上限
  expanded: new Set(),  // 项目树里展开的项目
  treeNodes: new Map(), // projectID -> [{id,title}] 懒加载的子项
  treeFilter: "",       // 项目树搜索词
  solo: null,           // 单画布聚焦：只显示这一张设计稿（双击项目树子项 / 双击卡片）
};

// 节点标题栏高度：唯一真源是 tokens.css 的 --ui-node-head-h，
// 这里启动时读一次（检查模式换算世界坐标要用）。取不到再退回 32。
const HEAD_H = tokenPx("--ui-node-head-h", 32);

const $ = (id) => document.getElementById(id);
const viewport = $("viewport"), world = $("world"), grid = $("grid-layer");
const edgeSvg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
edgeSvg.id = "edges";
edgeSvg.innerHTML = '<defs><marker id="arrow" class="edge-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z"/></marker></defs>';
world.appendChild(edgeSvg);

// 关系标注层（世界坐标系，跨卡片绘制；检查模式使用）
const relLayer = document.createElementNS("http://www.w3.org/2000/svg", "svg");
relLayer.id = "rel-layer";
world.appendChild(relLayer);

// 画布纸张：项目版式边界（放在连线层之前，保证连线与节点都画在纸面上）
const paperEl = document.createElement("div");
paperEl.id = "canvas-paper";
paperEl.className = "hidden";
paperEl.innerHTML = '<span class="cv-label"><b class="cv-size"></b><span class="cv-ratio"></span></span>';
world.insertBefore(paperEl, edgeSvg);

// ---------------- 设计令牌读取 ----------------
// 布局/颜色常量的唯一真源是 tokens.css。JS 只读，不复制数值：
// 以前 leftDockWidth() 返回 292、fitView() 写 296/320/148，改样式就要同步改 JS。
function tokenPx(name, fallback) {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  const n = parseFloat(v);
  return Number.isFinite(n) ? n : fallback;
}

function tokenColor(name, fallback) {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

// 画布界面 tokens 的 z-index / 布局常量（在 <head> 里已加载，启动即可读）
const UI = {
  headH: HEAD_H,
  gutter: tokenPx("--ui-gutter", 12),
  dockW: tokenPx("--ui-dock-w", 280),
  inspectorW: tokenPx("--ui-inspector-w", 288),
  toolbarH: tokenPx("--ui-toolbar-h", 44),
  topSafe: tokenPx("--ui-top-safe", 56),
  soloBarH: tokenPx("--ui-solo-bar-h", 44),
};

// ---------------- 询问 / 确认弹层 ----------------
// 原生 prompt()/confirm() 由浏览器绘制，字体、圆角、语气全部脱离体系；
// prompt 在部分环境被静默禁用 → 删项目、便签会直接不可用。这里统一走体系弹层，
// 并补齐弹层四要素：点外关闭、Esc 关闭、Tab 陷阱、关闭后焦点归还（UED §19.5.1）。
let askState = null;

function focusables(root) {
  return [...root.querySelectorAll(
    'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')]
    .filter((el) => !el.disabled && el.offsetParent !== null);
}

function closeAsk(result) {
  const box = $("ask-modal");
  if (!box || box.classList.contains("hidden")) return;
  box.classList.add("hidden");
  const st = askState;
  askState = null;
  $("ask-input").value = "";
  $("ask-error").textContent = "";
  if (st && st.restore && typeof st.restore.focus === "function") st.restore.focus();
  if (st && st.resolve) st.resolve(result);
}

// uiAsk({title, note, defaultValue, placeholder, okText, multiline}) → Promise<string|null>
function uiAsk(opt) {
  const o = opt || {};
  return new Promise((resolve) => {
    const box = $("ask-modal");
    const restore = document.activeElement;
    $("ask-title").textContent = o.title || "请输入";
    $("ask-note").textContent = o.note || "";
    $("ask-note").classList.toggle("hidden", !o.note);
    $("ask-error").textContent = "";
    const wrap = $("ask-field-wrap");
    wrap.classList.toggle("hidden", o.field === false);
    const input = $("ask-input");
    input.value = o.defaultValue || "";
    input.placeholder = o.placeholder || "";
    input.rows = o.rows || 3;
    // 单行询问（如重命名）不该占三行高：默认 min-height 只给多行输入
    input.classList.toggle("ask-input--single", (o.rows || 3) <= 1);
    $("ask-cancel").textContent = o.cancelText || "取消";
    $("ask-ok").textContent = o.okText || "确定";
    $("ask-ok").className = "btn btn--primary";
    askState = { resolve, restore, mode: "ask", opt: o };
    box.classList.remove("hidden");
    box.dataset.kind = "ask";
    setTimeout(() => { if (o.field !== false) { input.focus(); input.select(); } else $("ask-ok").focus(); }, 0);
  });
}

// uiConfirm({title, note, okText, danger}) → Promise<boolean>
function uiConfirm(opt) {
  const o = opt || {};
  return new Promise((resolve) => {
    const box = $("ask-modal");
    const restore = document.activeElement;
    $("ask-title").textContent = o.title || "请确认";
    $("ask-note").textContent = o.note || "";
    $("ask-note").classList.toggle("hidden", !o.note);
    $("ask-error").textContent = "";
    $("ask-field-wrap").classList.add("hidden");
    $("ask-cancel").textContent = o.cancelText || "取消";
    $("ask-ok").textContent = o.okText || "确定";
    $("ask-ok").className = "btn " + (o.danger ? "btn--danger" : "btn--primary");
    askState = { resolve, restore, mode: "confirm", opt: o };
    box.classList.remove("hidden");
    box.dataset.kind = "confirm";
    setTimeout(() => $("ask-ok").focus(), 0);
  });
}

function submitAsk() {
  const st = askState;
  if (!st) return;
  if (st.mode === "confirm") { closeAsk(true); return; }
  const val = $("ask-input").value;
  if (st.opt && typeof st.opt.validate === "function") {
    const err = st.opt.validate(val);
    if (err) {
      $("ask-error").textContent = err;
      $("ask-input").focus();
      return;
    }
  }
  closeAsk(val);
}

// ---------------- 工具 ----------------
function toast(msg, isErr) {
  const t = $("toast");
  t.textContent = msg;
  t.classList.toggle("err", !!isErr);
  t.classList.toggle("hidden", false);
  // 语义二分：普通反馈 role=status（polite），错误 role=alert（immediate）
  t.setAttribute("role", isErr ? "alert" : "status");
  t.setAttribute("aria-live", isErr ? "assertive" : "polite");
  // 重放入场动画：连续相同文案也要有反馈（移除类再强制重排）
  t.classList.remove("toast-in");
  void t.offsetWidth;
  t.classList.add("toast-in");
  clearTimeout(toast._h);
  // 时长对齐 UED §19.5.3：普通 2.4s / 错误 4.2s
  toast._h = setTimeout(() => t.classList.add("hidden"), isErr ? 4200 : 2400);
}

async function api(path, opts) {
  let res;
  try {
    res = await fetch(path, opts);
  } catch (e) {
    setOnline(false);
    throw e;
  }
  setOnline(true);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = data && data.error || { message: res.statusText };
    const e = new Error(err.message || res.statusText);
    e.code = err.code; e.hint = err.hint;
    throw e;
  }
  return data;
}

function setOnline(ok) {
  if (ok === S.online) return;
  S.online = ok;
  const c = $("conn");
  c.classList.toggle("conn--ok", ok);
  c.classList.toggle("conn--bad", !ok);
  setTip("conn", ok ? "已连接" : "连接断开");
}

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g,
    (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
