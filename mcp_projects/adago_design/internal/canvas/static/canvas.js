/* adago-design 无限画布（vanilla JS，无构建步骤） */
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
  expanded: new Set(),  // 项目树里展开的项目
  treeNodes: new Map(), // projectID -> [{id,title}] 懒加载的子项
  treeFilter: "",       // 项目树搜索词
  solo: null,           // 单画布聚焦：只显示这一张设计稿（双击项目树子项 / 双击卡片）
};

const HEAD_H = 32;       // .node__head 高度（检查模式换算世界坐标要用）

const $ = (id) => document.getElementById(id);
const viewport = $("viewport"), world = $("world"), grid = $("grid-layer");
const edgeSvg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
edgeSvg.id = "edges";
edgeSvg.innerHTML = '<defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill="#7c5cff"/></marker></defs>';
world.appendChild(edgeSvg);

// 关系标注层（世界坐标系，跨卡片绘制；检查模式使用）
const relLayer = document.createElementNS("http://www.w3.org/2000/svg", "svg");
relLayer.id = "rel-layer";
world.appendChild(relLayer);

// ---------------- 工具 ----------------
function toast(msg, isErr) {
  const t = $("toast");
  t.textContent = msg;
  t.classList.toggle("err", !!isErr);
  t.classList.remove("hidden");
  clearTimeout(toast._h);
  toast._h = setTimeout(() => t.classList.add("hidden"), 2600);
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
  c.querySelector(".conn__text").textContent = ok ? "已连接" : "连接断开";
}

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g,
    (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// ---------------- 视图变换 ----------------
function applyTransform() {
  world.style.transform = `translate(${S.tx}px,${S.ty}px) scale(${S.scale})`;
  grid.style.transform = `translate(${S.tx}px,${S.ty}px) scale(${S.scale})`;
  $("zoom-label").textContent = Math.round(S.scale * 100) + "%";
  updateLabelScale();
}

function zoomAt(cx, cy, factor) {
  const ns = Math.min(4, Math.max(0.08, S.scale * factor));
  if (ns === S.scale) return;
  const k = ns / S.scale;
  S.tx = cx - (cx - S.tx) * k;
  S.ty = cy - (cy - S.ty) * k;
  S.scale = ns;
  applyTransform();
  scheduleEdges("*");
}

function fitView() {
  const soloNode = S.solo ? S.nodes.get(S.solo) : null;
  const pool = soloNode ? [soloNode] : [...S.nodes.values()];
  if (!pool.length) { S.tx = innerWidth / 2 - 200; S.ty = innerHeight / 2 - 150; S.scale = 1; applyTransform(); return; }
  let minX = 1e9, minY = 1e9, maxX = -1e9, maxY = -1e9;
  for (const n of pool) {
    minX = Math.min(minX, n.x); minY = Math.min(minY, n.y);
    maxX = Math.max(maxX, n.x + n.width); maxY = Math.max(maxY, n.y + n.height);
  }
  const pad = soloNode ? 48 : 80;
  // 全部视图：行首标签（约 152px 宽）在内容左侧，把它算进边界（聚焦时无标签）
  const labelPad = !soloNode && isAllScope() && S.nodes.size > 0 ? 170 : 0;
  minX -= labelPad;
  const bw = maxX - minX + pad * 2, bh = maxY - minY + pad * 2;
  const gutter = document.body.classList.contains("sb-collapsed") ? 0 : 296;
  const availW = innerWidth - (soloNode ? gutter + 64 : 320);
  const availH = innerHeight - (soloNode ? 150 : 120);
  const cap = soloNode ? 1.6 : 1.2;
  const scale = Math.min(cap, Math.max(0.08, Math.min(availW / bw, availH / bh)));
  S.scale = scale;
  if (soloNode) {
    // 聚焦视图按「侧栏右侧 + 工具条下方」的可见区居中
    S.tx = (gutter + innerWidth) / 2 - (minX + maxX) / 2 * scale;
    S.ty = (66 + innerHeight) / 2 - (minY + maxY) / 2 * scale;
  } else {
    S.tx = (innerWidth + 296) / 2 - (minX + maxX) / 2 * scale - 148;
    S.ty = innerHeight / 2 - (minY + maxY) / 2 * scale;
  }
  applyTransform();
  scheduleEdges("*");
}

// ---------------- 节点渲染 ----------------
// 位置用 transform 而不是 left/top：位移交给合成器，避免每帧 layout
// 与 iframe 表面的重新光栅化。
function applyNodeGeom(el, n) {
  el.style.transform = `translate3d(${n.x}px,${n.y}px,0)`;
  el.style.width = n.width + "px";
  el.style.height = n.height + "px";
  el.style.zIndex = n.z || 1;
}

function nodeEl(n) {
  const isFrame = n.kind === "frame";
  const el = document.createElement("div");
  el.className = "node" + (isFrame ? " node--frame" : "");
  el.dataset.id = n.id;
  applyNodeGeom(el, n);

  const head = document.createElement("div");
  head.className = "node__head" + (isFrame ? " node__head--frame" : "");
  const proj = isAllScope() && n.project_id
    ? `<span class="node__tag node__tag--proj" title="所属项目">${esc(projectName(n.project_id))}</span>` : "";
  const sys = n.system ? `<span class="node__tag">${esc(n.system)}</span>` : "";
  const ab = n.artboard ? `<span class="node__tag node__tag--ab" title="画板（尺寸规范）">${esc(n.artboard)}</span>` : "";
  head.innerHTML = `<span class="node__title">${esc(n.title || n.id)}</span>${proj}${ab}${sys}` +
    `<button class="node__del" title="删除节点">${icon("close", 14)}</button>`;
  head.addEventListener("pointerdown", (e) => startNodeDrag(e, n.id));
  el.appendChild(head);

  if (!isFrame) {
    const body = document.createElement("div");
    body.className = "node__body";
    const iframe = document.createElement("iframe");
    iframe.setAttribute("sandbox", "allow-same-origin");
    iframe.setAttribute("scrolling", "auto");
    // 先挂 load 监听再给 srcdoc，保证首次渲染就能测高 / 挂检查器
    iframe.addEventListener("load", () => onFrameLoad(n.id, iframe));
    iframe.srcdoc = n.html;
    body.appendChild(iframe);
    el.appendChild(body);
  }

  el.querySelector(".node__del").addEventListener("click", (e) => { e.stopPropagation(); removeNode(n.id); });
  el.addEventListener("pointerdown", () => selectNode(n.id));
  el.addEventListener("dblclick", (e) => {
    if (e.target.closest(".node__del")) return;
    e.stopPropagation();
    enterSolo(n.id, n.project_id);
  });
  return el;
}

function onFrameLoad(id, iframe) {
  autoFitHeight(id, iframe);
  window.dispatchEvent(new CustomEvent("adago:frame-load", { detail: { id: id, iframe: iframe } }));
}

// 内容高度自适应：if defined auto_height 且是组件预览，实测 .ds-page 高度回写。
// 量 .ds-page 而不是 scrollHeight：文档本身有 min-height:100vh，
// scrollHeight 会被视口撑满，量不到真实内容高度。
let autoFitBusy = new Set();
async function autoFitHeight(id, iframe) {
  if (S.drag || autoFitBusy.has(id)) return;
  const n = S.nodes.get(id);
  if (!n || !n.auto_height || n.kind !== "component") return;
  const doc = iframe.contentDocument;
  const page = doc && doc.querySelector(".ds-page");
  if (!page) return;
  const h = Math.ceil(page.getBoundingClientRect().height) + 2;
  if (!h || h < 120 || Math.abs(h - n.height) <= 4) return;
  autoFitBusy.add(id);
  try {
    const { node } = await api("/api/nodes/" + id, {
      method: "PUT", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ height: h }),
    });
    S.nodes.set(id, node);   // 同步 updated_at，避免下一轮轮询误判为远端修改而重载 iframe
    const el = S.els.get(id);
    if (el) applyNodeGeom(el, node);
    scheduleEdges("*");
  } catch (_) { /* 测高失败不影响主流程 */ }
  finally { autoFitBusy.delete(id); }
}

function addNodeEl(n) {
  const el = nodeEl(n);
  if (S.solo && S.solo !== n.id) el.classList.add("solo-hidden");
  S.els.set(n.id, el);
  world.appendChild(el);
  hideEmptyHint();
  scheduleLabels();
}

function updateNodeEl(n) {
  const el = S.els.get(n.id);
  if (!el) { addNodeEl(n); return; }
  const prev = S.nodes.get(n.id);
  applyNodeGeom(el, n);
  const title = el.querySelector(".node__title");
  if (title.textContent !== (n.title || n.id)) title.textContent = n.title || n.id;
  const iframe = el.querySelector("iframe");
  // 只在正文真的变了时重载 iframe（位置/高度变化不该重跑文档）
  if (iframe && n.html && prev && prev.html !== n.html) {
    iframe.srcdoc = n.html;
  }
}

function removeNodeEl(id) {
  if (S.solo === id) exitSolo(false);
  const el = S.els.get(id);
  if (el) el.remove();
  S.els.delete(id);
  S.nodes.delete(id);
  scheduleLabels();
  if (S.selected === id) { S.selected = null; $("inspector").classList.add("hidden"); }
  if (S.nodes.size === 0) showEmptyHint();
}

function hideEmptyHint() { const h = document.querySelector(".empty-hint"); if (h) h.remove(); }

function showHint(html) {
  hideEmptyHint();
  const d = document.createElement("div");
  d.className = "empty-hint";
  d.innerHTML = html;
  document.body.appendChild(d);
}

function showEmptyHint() {
  if (!S.projects.length) {
    showHint("<b>还没有项目</b><br>设计稿必须归属项目：点左侧「新建项目」创建（⇧N）<br>" +
      "Agent 侧对应 project_create / project_list 工具");
    return;
  }
  if (isAllScope()) {
    showHint("<b>项目里还没有设计稿</b><br>先在左侧选中一个项目，再从「设计体系」添加预览<br>" +
      "或让 Agent 用 design_preview / design_screen 生成");
    return;
  }
  showHint("<b>「" + esc(projectName(S.project)) + "」还没有设计稿</b><br>" +
    "从左侧设计体系添加预览，或让 Agent 用 design_preview 生成");
}

function selectNode(id) {
  if (S.selected === id) return;
  if (S.selected) { const p = S.els.get(S.selected); if (p) p.classList.remove("selected"); }
  S.selected = id;
  const el = S.els.get(id);
  if (!el) return;
  el.classList.add("selected");
  const n = S.nodes.get(id);
  const rows = [
    ["ID", n.id], ["项目", projectName(n.project_id)], ["标题", n.title || "-"],
    ["体系", n.system || "-"], ["类型", n.kind],
    ["画板", n.artboard || "-"],
    ["尺寸", n.width + " × " + n.height], ["坐标", Math.round(n.x) + ", " + Math.round(n.y)],
    ["更新", (n.updated_at || "").replace("T", " ").slice(0, 19)],
  ];
  $("insp-body").innerHTML =
    `<div class="insp-head">${icon("inspect", 14)} 节点信息</div>` +
    rows.map(([k, v]) => `<div class="kv"><span class="chip">${esc(k)}</span><span>${esc(v)}</span></div>`).join("") +
    `<div class="sec"><div class="sec__title">标签</div><div class="kv">${(n.tags || []).map((t) => `<span class="chip">${esc(t)}</span>`).join("") || "-"}</div></div>`;
  $("inspector").classList.remove("hidden");
}

async function removeNode(id) {
  try {
    await api("/api/nodes/" + id, { method: "DELETE" });
    removeNodeEl(id);
    S.edges.forEach((e, eid) => { if (e.from === id || e.to === id) S.edges.delete(eid); });
    scheduleEdges("*");
    reloadProjects().catch(() => { /* 同上方 */ });
  } catch (e) { toast("删除失败: " + e.message, true); }
}

// ---------------- 节点拖动（指针捕获 + rAF 合帧 + transform） ----------------
function startNodeDrag(e, id) {
  if (e.button !== 0) return;
  if (e.target.closest(".node__del")) return;                    // 删除按钮不触发拖动
  if (document.body.classList.contains("mode-inspect")) return; // 检查模式下拖拽让位给元素拾取
  selectNode(id);
  e.preventDefault();
  e.stopPropagation();
  const n = S.nodes.get(id);
  if (!n) return;
  const el = S.els.get(id);
  S.drag = {
    id: id, el: el, pid: e.pointerId,
    sx: e.clientX, sy: e.clientY, ox: n.x, oy: n.y, nx: n.x, ny: n.y,
    raf: 0, moved: false,
  };
  try { el.setPointerCapture(e.pointerId); } catch (_) { /* 老浏览器忽略 */ }
  el.classList.add("dragging");
  document.body.classList.add("is-dragging");
}

function onDragMove(e) {
  const d = S.drag;
  if (!d) return;
  d.nx = d.ox + (e.clientX - d.sx) / S.scale;
  d.ny = d.oy + (e.clientY - d.sy) / S.scale;
  if (Math.abs(d.nx - d.ox) > 1 || Math.abs(d.ny - d.oy) > 1) d.moved = true;
  if (d.raf) return;
  d.raf = requestAnimationFrame(() => {
    d.raf = 0;
    if (!S.drag) return;
    d.el.style.transform = `translate3d(${d.nx}px,${d.ny}px,0)`;
    scheduleEdges(d.id);
    scheduleLabels();
  });
}

function onDragEnd(e) {
  const d = S.drag;
  if (!d) return;
  if (d.raf) { cancelAnimationFrame(d.raf); d.raf = 0; }
  S.drag = null;
  d.el.classList.remove("dragging");
  document.body.classList.remove("is-dragging");
  try { d.el.releasePointerCapture(d.pid); } catch (_) { /* ignore */ }
  if (!d.moved) return;

  const n = S.nodes.get(d.id);
  if (!n) return;
  n.x = d.nx; n.y = d.ny;
  savePosition(d.id, d.nx, d.ny, d.ox, d.oy, d.el);
}

async function savePosition(id, x, y, ox, oy, el) {
  try {
    const { node } = await api("/api/nodes/" + id, {
      method: "PUT", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ x: x, y: y }),
    });
    // 用服务端回包覆盖本地：拿到新的 updated_at，避免下轮轮询误判为远端修改
    S.nodes.set(id, node);
    if (el) applyNodeGeom(el, node);
    scheduleEdges(id);
  } catch (e) {
    const n = S.nodes.get(id);
    if (n) { n.x = ox; n.y = oy; }
    if (el) el.style.transform = `translate3d(${ox}px,${oy}px,0)`;
    scheduleEdges(id);
    toast("位置保存失败: " + e.message, true);
  }
}

// ---------------- 平移 ----------------
viewport.addEventListener("pointerdown", (e) => {
  if (e.target !== viewport && e.target !== grid && e.target !== world) return;
  if (e.button !== 0) return;
  S.panning = { sx: e.clientX, sy: e.clientY, tx: S.tx, ty: S.ty, pid: e.pointerId };
  viewport.classList.add("panning");
  try { viewport.setPointerCapture(e.pointerId); } catch (_) { /* ignore */ }
  if (S.selected) {
    const el = S.els.get(S.selected);
    if (el) el.classList.remove("selected");
    S.selected = null;
    $("inspector").classList.add("hidden");
  }
});

window.addEventListener("pointermove", (e) => {
  if (S.drag) { onDragMove(e); return; }
  if (S.panning) {
    S.tx = S.panning.tx + (e.clientX - S.panning.sx);
    S.ty = S.panning.ty + (e.clientY - S.panning.sy);
    applyTransform();
  }
}, { passive: true });

window.addEventListener("pointerup", (e) => {
  if (S.drag) { onDragEnd(e); return; }
  if (S.panning) {
    try { viewport.releasePointerCapture(S.panning.pid); } catch (_) { /* ignore */ }
    viewport.classList.remove("panning");
    S.panning = null;
    scheduleEdges("*");
  }
});

window.addEventListener("pointercancel", () => {
  if (S.drag) onDragEnd({ pointerId: S.drag.pid, clientX: 0, clientY: 0 });
  if (S.panning) { viewport.classList.remove("panning"); S.panning = null; }
});

viewport.addEventListener("wheel", (e) => {
  e.preventDefault();
  const factor = e.deltaY < 0 ? 1.12 : 1 / 1.12;
  zoomAt(e.clientX, e.clientY, factor);
}, { passive: false });

viewport.addEventListener("dblclick", (e) => {
  if (!S.solo) return;
  // 卡片自身的 dblclick 会 stopPropagation；落到这里的就是空白网格
  if (e.target.closest && e.target.closest(".node")) return;
  exitSolo();
});

window.addEventListener("keydown", (e) => {
  if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
  if (e.key === "Delete" || e.key === "Backspace") {
    if (document.body.classList.contains("mode-inspect")) return;
    if (S.selected) { e.preventDefault(); removeNode(S.selected); }
  }
  else if (e.key === "f" || e.key === "F") fitView();
  else if (e.key === "Escape") {
    if (S.selected) { const el = S.els.get(S.selected); if (el) el.classList.remove("selected"); S.selected = null; $("inspector").classList.add("hidden"); }
  }
  else if (e.key === "=" || e.key === "+") zoomAt(innerWidth / 2, innerHeight / 2, 1.15);
  else if (e.key === "-") zoomAt(innerWidth / 2, innerHeight / 2, 1 / 1.15);
});

// ---------------- 连线（元素池化 + 脏边增量 + rAF 合帧） ----------------
const edgeEls = new Map();     // edgeId -> {path, label}
const edgeDirty = new Set();
let edgeRaf = 0;

function scheduleEdges(id) {
  edgeDirty.add(id || "*");
  if (edgeRaf) return;
  edgeRaf = requestAnimationFrame(flushEdges);
}

function clearEdgeEls() {
  for (const o of edgeEls.values()) {
    o.path.remove();
    if (o.label) o.label.remove();
  }
  edgeEls.clear();
}

function flushEdges() {
  edgeRaf = 0;
  const dirty = new Set(edgeDirty);
  edgeDirty.clear();
  const all = dirty.has("*");

  if (!S.edges.size) { if (edgeEls.size) clearEdgeEls(); return; }

  const live = new Set();
  for (const e of S.edges.values()) {
    live.add(e.id);
    if (!all && !dirty.has(e.from) && !dirty.has(e.to)) continue;
    upsertEdge(e);
  }
  for (const [id, o] of edgeEls) {
    if (!live.has(id)) {
      o.path.remove();
      if (o.label) o.label.remove();
      edgeEls.delete(id);
    }
  }
}

// 贝塞尔：源底部中点 → 目标顶部中点，距离越远弧度越大
function edgePath(a, b) {
  const x1 = a.x + a.width / 2, y1 = a.y + a.height;
  const x2 = b.x + b.width / 2, y2 = b.y;
  const dy = Math.max(48, Math.abs(y2 - y1) / 2);
  return `M ${x1} ${y1} C ${x1} ${y1 + dy}, ${x2} ${y2 - dy}, ${x2} ${y2}`;
}

function upsertEdge(e) {
  const a = S.nodes.get(e.from), b = S.nodes.get(e.to);
  if (!a || !b) return;
  let o = edgeEls.get(e.id);
  if (!o) {
    const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
    path.setAttribute("class", "edge-line");
    path.setAttribute("marker-end", "url(#arrow)");
    path.dataset.edgeId = e.id;
    edgeSvg.appendChild(path);
    o = { path: path, label: null };
    edgeEls.set(e.id, o);
  }
  o.path.setAttribute("d", edgePath(a, b));
  const ax = a.x + a.width / 2, ay = a.y + a.height / 2;
  const bx = b.x + b.width / 2, by = b.y + b.height / 2;
  if (e.label) {
    if (!o.label) {
      o.label = document.createElementNS("http://www.w3.org/2000/svg", "text");
      o.label.setAttribute("class", "edge-label");
      edgeSvg.appendChild(o.label);
    }
    o.label.setAttribute("x", (ax + bx) / 2);
    o.label.setAttribute("y", (ay + by) / 2);
    if (o.label.textContent !== e.label) o.label.textContent = e.label;
  } else if (o.label) {
    o.label.remove();
    o.label = null;
  }
}

// ---------------- 同步轮询 ----------------
async function pollNodes() {
  if (S.drag) return;
  const q = S.project ? "?project=" + encodeURIComponent(S.project) : "";
  try {
    const [data, ed] = await Promise.all([
      api("/api/nodes" + q),
      api("/api/edges" + q).catch(() => ({ edges: [] })),
    ]);
    const seen = new Set();
    for (const n of data.nodes || []) {
      seen.add(n.id);
      const local = S.nodes.get(n.id);
      if (!local) {
        S.nodes.set(n.id, n);
        addNodeEl(n);
      } else if (local.updated_at !== n.updated_at) {
        S.nodes.set(n.id, n);
        updateNodeEl(n);
      }
    }
    for (const id of [...S.nodes.keys()]) if (!seen.has(id)) removeNodeEl(id);
    S.edges = new Map((ed.edges || []).map((e) => [e.id, e]));
    scheduleEdges("*");
    scheduleLabels();
  } catch (_) { /* 状态条已处理 */ }
}

// ---------------- 项目（画布作用域） ----------------
// S.project === "" 表示「全部」：一次看所有项目的稿子，按项目分行并在行首打标签。
const SCOPE_ALL = "";

function projectName(id) {
  const p = S.projects.find((x) => x.id === id);
  return p ? p.name : "（未知项目）";
}

function isAllScope() { return S.project === SCOPE_ALL; }

function totalNodes() {
  return S.projects.reduce((sum, p) => sum + (p.node_count || 0), 0);
}

// ---- 项目树（参照设计稿工具的分组列表） ----
function renderProjectTree() {
  const rows = $("tree-rows");
  if (!rows) return;
  const total = $("tree-total");
  if (total) total.textContent = String(totalNodes());
  const allBtn = $("tree-all");
  if (allBtn) allBtn.classList.toggle("is-active", isAllScope());

  const q = (S.treeFilter || "").trim().toLowerCase();
  const list = q ? S.projects.filter((p) => p.name.toLowerCase().includes(q)) : S.projects;
  if (!list.length) {
    rows.innerHTML = S.projects.length
      ? '<div class="tree-empty">没有匹配的项目</div>'
      : '<div class="tree-empty">还没有项目。<br>点上方「新建项目」创建 —— 设计稿必须归属项目。</div>';
    return;
  }
  rows.innerHTML = list.map((p) => {
    const active = p.id === S.project;
    const open = S.expanded.has(p.id);
    return `<div class="tree-row${active ? " is-active" : ""}${open ? " is-open" : ""}" data-id="${esc(p.id)}">
      <button class="tree-row__caret" data-act="toggle" title="${open ? "收起" : "展开"}">
        <svg class="ds-icon ds-icon--12" aria-hidden="true"><use href="#ds-ic-chevron-right"/></svg>
      </button>
      <span class="tree-row__icon">${icon(open ? "folder-open" : "folder", 14)}</span>
      <span class="tree-row__name" title="${esc(p.name)}">${esc(p.name)}</span>
      <span class="tree-row__count">${p.node_count}</span>
    </div>` + (open ? renderTreeChildren(p) : "");
  }).join("");
}

function renderTreeChildren(p) {
  const nodes = S.treeNodes.get(p.id);
  if (!nodes) return '<div class="tree-children"><div class="tree-child">加载中…</div></div>';
  if (!nodes.length) return '<div class="tree-children"><div class="tree-child">暂无设计稿</div></div>';
  return '<div class="tree-children">' + nodes.slice(0, 40).map((n) =>
    `<div class="tree-child${n.id === S.solo ? " is-solo" : ""}" data-node="${esc(n.id)}" data-project="${esc(p.id)}" title="${esc(n.title || n.id)} · 单击定位 / 双击单独展示">` +
      `${icon("note", 12)}<span>${esc(n.title || n.id)}</span></div>`
  ).join("") + "</div>";
}

async function toggleTreeRow(id) {
  if (S.expanded.has(id)) { S.expanded.delete(id); renderProjectTree(); return; }
  S.expanded.add(id);
  if (!S.treeNodes.has(id)) {
    renderProjectTree(); // 先出「加载中…」
    try {
      const data = await api("/api/nodes?project=" + encodeURIComponent(id));
      S.treeNodes.set(id, (data.nodes || []).map((n) => ({ id: n.id, title: n.title })));
    } catch (_) {
      S.treeNodes.set(id, []);
    }
  }
  renderProjectTree();
}

async function focusNode(projectID, nodeID) {
  if (S.project !== projectID) await switchProject(projectID);
  const n = S.nodes.get(nodeID);
  if (!n) return;
  selectNode(nodeID);
  const scale = Math.min(1, Math.max(0.12, S.scale));
  S.scale = scale;
  S.tx = innerWidth / 2 - (n.x + n.width / 2) * scale;
  S.ty = innerHeight / 2 - (n.y + n.height / 2) * scale;
  applyTransform();
  scheduleEdges("*");
  scheduleLabels();
}

// ---------------- 单画布聚焦 ----------------
// 双击项目树里的设计稿（或双击画布卡片）→ 只展示这一张稿。
// 关键：不改数据、不改作用域，只隐藏其它节点的 DOM 并重新适配视口，
// 所以退出后拖动过的坐标、连线、检查模式状态都原样恢复。
function applySoloVisibility() {
  for (const [id, el] of S.els) el.classList.toggle("solo-hidden", !!S.solo && id !== S.solo);
  edgeSvg.style.display = S.solo ? "none" : "";
  relLayer.style.display = S.solo ? "none" : "";
  document.body.classList.toggle("mode-solo", !!S.solo);
  // 只切 class，不重建树 DOM（聚焦可能正由树上的 dblclick 触发）
  document.querySelectorAll(".tree-child[data-node]").forEach((el) => {
    el.classList.toggle("is-solo", !!S.solo && el.dataset.node === S.solo);
  });
  scheduleLabels();
}

function renderSoloBar() {
  const bar = $("solo-bar");
  if (!bar) return;
  const n = S.solo && S.nodes.get(S.solo);
  if (!n) { bar.classList.add("hidden"); return; }
  $("solo-title").textContent = n.title || n.id;
  $("solo-project").textContent = projectName(n.project_id);
  bar.classList.remove("hidden");
}

async function enterSolo(nodeID, projectID) {
  // 单项目视图里双击其它项目下的稿：先切作用域；「全部」视图本来就都在画布上
  if (projectID && S.project && S.project !== projectID) await switchProject(projectID);
  if (!S.nodes.has(nodeID)) await pollNodes();
  const n = S.nodes.get(nodeID);
  if (!n) { toast("这张设计稿已不在当前画布", true); return; }
  // 跨卡片的 A↔B 关系在只剩一张稿时没有意义，先清掉，避免残影画到 (0,0)；
  // 检查器面板也一并收起，否则会留着上一次的对比数据（hover 时检查器会自己再打开）
  if (typeof clearPair === "function") clearPair();
  if (S.selected) { const el = S.els.get(S.selected); if (el) el.classList.remove("selected"); S.selected = null; }
  $("inspector").classList.add("hidden");
  S.solo = nodeID;
  applySoloVisibility();
  renderSoloBar();
  fitView();
  toast("已单独展示「" + (n.title || n.id) + "」· Esc 返回画布");
}

function exitSolo(refit) {
  if (!S.solo) return;
  S.solo = null;
  applySoloVisibility();
  renderSoloBar();
  scheduleEdges("*");
  if (refit !== false) fitView();
}

// 切换作用域 = 换一块画布：先清掉当前渲染的节点，再按新作用域重新拉取
function resetCanvas() {
  if (S.solo) exitSolo(false);
  for (const id of [...S.nodes.keys()]) removeNodeEl(id);
  S.edges = new Map();
  scheduleEdges("*");
  scheduleLabels();
  S.selected = null;
  $("inspector").classList.add("hidden");
}

async function loadProjects(preferred) {
  try {
    const data = await api("/api/projects");
    S.projects = data.projects || [];
    S.treeNodes.clear();
    S.expanded.clear();
  } catch (e) {
    S.projects = [];
    toast("项目列表加载失败: " + e.message, true);
  }
  const remembered = preferred !== undefined
    ? preferred
    : (localStorage.getItem("adago.scope") || "all");
  if (remembered && remembered !== "all" && S.projects.some((p) => p.id === remembered)) {
    S.project = remembered;
  } else {
    S.project = SCOPE_ALL;
  }
  localStorage.setItem("adago.scope", S.project || "all");
  renderProjectTree();
  return S.project;
}

async function switchProject(id) {
  const scope = id || SCOPE_ALL;
  if (scope === S.project) { closeTreeMenu(); return; }
  S.project = scope;
  localStorage.setItem("adago.scope", scope || "all");
  closeTreeMenu();
  resetCanvas();
  renderProjectTree();
  await pollNodes();
  if (isAllScope()) await ensureGroupLayout();
  if (S.nodes.size === 0) showEmptyHint();
  else hideEmptyHint();
  fitView();
}

// 「全部」视图里，各项目原本都从 (80,80) 起排，卡片会完全重叠。
// 检测到项目包围盒相交就按项目分行重排一次（一次性写坐标，之后仍可自由拖动）。
async function ensureGroupLayout() {
  const boxes = new Map();
  for (const n of S.nodes.values()) {
    const pid = n.project_id || "";
    const b = boxes.get(pid) || { x0: Infinity, y0: Infinity, x1: -Infinity, y1: -Infinity };
    b.x0 = Math.min(b.x0, n.x);
    b.y0 = Math.min(b.y0, n.y);
    b.x1 = Math.max(b.x1, n.x + n.width);
    b.y1 = Math.max(b.y1, n.y + n.height);
    boxes.set(pid, b);
  }
  const arr = [...boxes.values()];
  let overlap = false;
  for (let i = 0; i < arr.length && !overlap; i++) {
    for (let j = i + 1; j < arr.length; j++) {
      const a = arr[i], b = arr[j];
      if (a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1) { overlap = true; break; }
    }
  }
  if (!overlap) return;
  try {
    await api("/api/arrange", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ group_by: "project" }),
    });
    await pollNodes();
  } catch (_) { /* 排列失败不阻塞浏览 */ }
}

async function arrangeByProject(silent) {
  try {
    const data = await api("/api/arrange", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ group_by: "project" }),
    });
    await pollNodes();
    fitView();
    if (!silent) toast("已按项目重新排列（移动 " + (data.moved || 0) + " 个节点）");
  } catch (e) {
    if (!silent) toast("排列失败: " + e.message, true);
  }
}

async function reloadProjects() {
  const data = await api("/api/projects");
  S.projects = data.projects || [];
  renderProjectTree();
  if (isProjectModalOpen()) renderProjectRows();
}

// ---- 画布上的分组标签（世界坐标，跟着节点走） ----
const labelLayer = document.createElement("div");
labelLayer.id = "group-labels";
world.appendChild(labelLayer);

let labelRaf = 0;
function scheduleLabels() {
  if (labelRaf) return;
  labelRaf = requestAnimationFrame(() => { labelRaf = 0; renderGroupLabels(); });
}

function renderGroupLabels() {
  labelLayer.innerHTML = "";
  if (!isAllScope() || S.solo) return;
  const groups = new Map();
  for (const n of S.nodes.values()) {
    const pid = n.project_id || "";
    const g = groups.get(pid);
    if (!g) groups.set(pid, { minX: n.x, minY: n.y, count: 1 });
    else {
      g.minX = Math.min(g.minX, n.x);
      g.minY = Math.min(g.minY, n.y);
      g.count++;
    }
  }
  for (const [pid, g] of groups) {
    const el = document.createElement("button");
    el.className = "group-label";
    el.dataset.id = pid;
    // 世界坐标存下来，缩放时只重算 transform，不重建节点
    el.dataset.wx = String(Math.round(Math.max(8, g.minX - 152)));
    el.dataset.wy = String(Math.round(g.minY));
    el.innerHTML = `<span>${esc(projectName(pid))}</span><span class="group-label__count">${g.count}</span>`;
    el.title = "只看「" + projectName(pid) + "」";
    el.addEventListener("click", () => switchProject(pid));
    labelLayer.appendChild(el);
    applyLabelTransform(el);
  }
}

// 标签反向缩放：画布缩到 26% 时标签若跟着缩会看不清，
// 这里按 1/scale 抵消并封顶 2.4 倍，保证任何缩放下都能读。
function labelScale() {
  return Math.min(2.4, Math.max(1, 1 / S.scale));
}

function applyLabelTransform(el) {
  const k = labelScale();
  el.style.transform = `translate3d(${el.dataset.wx}px,${el.dataset.wy}px,0) scale(${k})`;
  el.style.transformOrigin = "top left";
}

function updateLabelScale() {
  if (!labelLayer) return;
  const k = labelScale();
  if (updateLabelScale._k === k) return;
  updateLabelScale._k = k;
  labelLayer.querySelectorAll(".group-label").forEach(applyLabelTransform);
}

// ---- 项目管理弹层 ----
function isProjectModalOpen() {
  const m = $("proj-modal");
  return !!m && !m.classList.contains("hidden");
}

function openProjectModal() {
  closeTreeMenu();
  if (!isProjectModalOpen()) $("proj-modal").classList.remove("hidden");
  renderProjectRows();
  const input = $("proj-name");
  if (input) setTimeout(() => input.focus(), 0);
}

function closeProjectModal() {
  const m = $("proj-modal");
  if (m) m.classList.add("hidden");
}

function fmtTime(iso) {
  if (!iso) return "-";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "-";
  const p2 = (n) => String(n).padStart(2, "0");
  return `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}`;
}

function renderProjectRows() {
  const box = $("proj-rows");
  if (!box) return;
  const count = $("proj-count");
  if (count) count.textContent = String(S.projects.length);
  if (!S.projects.length) {
    box.innerHTML = '<div class="proj-empty">还没有项目。先在上面创建 —— 设计稿必须归属项目，不能直接新增。</div>';
    return;
  }
  box.innerHTML = S.projects
    .map((p) => {
      const cur = p.id === S.project;
      const frames = p.frame_count ? ` · ${p.frame_count} 个分组框` : "";
      return `<div class="proj-row${cur ? " is-current" : ""}" data-id="${esc(p.id)}">
        <div class="proj-row__main">
          <div class="proj-row__name">${esc(p.name)}${cur ? '<span class="proj-row__cur">当前</span>' : ""}</div>
          <div class="proj-row__meta">${p.node_count} 个设计稿${frames} · 更新 ${fmtTime(p.updated_at)} · ${esc(p.id)}</div>
        </div>
        <div class="proj-row__acts">
          ${cur ? "" : '<button data-act="switch">切换</button>'}
          <button data-act="rename">重命名</button>
          <button data-act="remove" class="danger">删除</button>
        </div>
      </div>`;
    })
    .join("");
  box.querySelectorAll(".proj-row").forEach((row) => {
    const id = row.dataset.id;
    row.querySelectorAll("[data-act]").forEach((btn) => {
      btn.addEventListener("click", () => onProjectAction(btn.dataset.act, id));
    });
  });
}

async function onProjectAction(act, id) {
  const target = S.projects.find((p) => p.id === id);
  if (!target) return;
  if (act === "switch") {
    await switchProject(id);
    renderProjectRows();
    closeProjectModal();
    return;
  }
  if (act === "rename") {
    const name = prompt("重命名项目：", target.name);
    if (name == null) return;
    if (!name.trim()) { toast("项目名不能为空", true); return; }
    try {
      await api("/api/projects/" + encodeURIComponent(id), {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name.trim() }),
      });
      await reloadProjects();
      renderProjectRows();
      toast("已重命名为：" + name.trim());
    } catch (e) { toast("重命名失败: " + e.message, true); }
    return;
  }
  if (act === "remove") {
    if (!confirm("删除项目「" + target.name + "」？\n只有空项目可以删除，项目下的设计稿需先清空。")) return;
    try {
      await api("/api/projects/" + encodeURIComponent(id), { method: "DELETE" });
      if (S.project === id) {
        // 当前项目被删：回落到「全部」
        S.project = SCOPE_ALL;
        await loadProjects("all");
        resetCanvas();
        await pollNodes();
        showEmptyHint();
        fitView();
      } else {
        await reloadProjects();
      }
      renderProjectRows();
      toast("已删除项目：" + target.name);
    } catch (e) {
      toast("删除失败: " + e.message + "（项目下还有设计稿时需先清空）", true);
    }
  }
}

async function createProjectFromModal() {
  const nameInput = $("proj-name");
  const noteInput = $("proj-note");
  const name = (nameInput.value || "").trim();
  if (!name) { toast("请输入项目名称", true); nameInput.focus(); return; }
  try {
    const data = await api("/api/projects", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, note: (noteInput.value || "").trim() }),
    });
    nameInput.value = "";
    noteInput.value = "";
    await loadProjects(data.project.id);
    resetCanvas();
    await pollNodes();
    showEmptyHint();
    fitView();
    renderProjectRows();
    toast("已创建项目：" + data.project.name + "，已切换到该项目");
  } catch (e) { toast("创建失败: " + e.message, true); }
}

// ---- 项目树的「更多」菜单 ----
function openTreeMenu() {
  const menu = $("tree-menu");
  if (!menu) return;
  menu.classList.remove("hidden");
}
function closeTreeMenu() {
  const menu = $("tree-menu");
  if (menu) menu.classList.add("hidden");
}
function toggleTreeMenu() {
  const menu = $("tree-menu");
  if (!menu) return;
  menu.classList.toggle("hidden");
}

// ---------------- 侧栏：设计体系目录 ----------------
async function loadCatalog() {
  try {
    const data = await api("/api/catalog/systems");
    const list = $("sb-list");
    list.innerHTML = "";
    for (const sys of data.systems || []) {
      const card = document.createElement("div");
      card.className = "sys-card";
      card.innerHTML =
        `<div class="sys-card__name">${esc(sys.name)}</div>` +
        `<div class="sys-card__vendor">${esc(sys.vendor)} · ${esc(sys.family)}</div>` +
        `<div class="sys-card__chips">${(sys.keywords || []).map((k) => `<span class="chip">${esc(k)}</span>`).join("")}` +
        (sys.default_artboard ? `<span class="chip chip--ab">${icon("fit", 12)} ${esc(sys.default_artboard)}</span>` : "") +
        `</div>`;
      card.addEventListener("click", () => showSystemDetail(sys.id));
      list.appendChild(card);
    }
  } catch (e) { toast("目录加载失败: " + e.message, true); }
}

async function showSystemDetail(id) {
  try {
    const sys = await api("/api/catalog/systems/" + encodeURIComponent(id));
    $("sb-list").classList.add("hidden");
    const box = $("sb-detail");
    box.classList.remove("hidden");
    const body = $("sb-detail-body");
    const colors = Object.entries((sys.light && sys.light.colors) || {}).slice(0, 24);
    const levels = Object.entries((sys.light && sys.light.type && sys.light.type.levels) || {});
    const spacing = Object.entries((sys.light && sys.light.spacing) || {});
    const radius = Object.entries((sys.light && sys.light.radius) || {});
    const comps = (sys.components || []);
    body.innerHTML = `
      <div style="font-size:15px;font-weight:700">${esc(sys.name)}</div>
      <div style="font-size:11px;color:var(--fg2);margin:2px 0 6px">${esc(sys.vendor)}</div>
      <div style="font-size:12px;color:var(--fg2);line-height:1.6">${esc(sys.style && sys.style.summary || sys.description || "")}</div>
      <div class="kv" style="margin-top:8px">
        <span class="chip">${icon("fit", 12)} ${esc(sys.default_artboard || "-")}</span>
        <span class="chip" title="字体授权构成">${icon("settings", 12)} ${esc(sys.font_license || "-")}</span>
      </div>
      <div class="theme-seg" style="margin-top:10px">
        <button data-th="light" class="${S.catalogTheme === "light" ? "active" : ""}">浅色</button>
        <button data-th="dark" class="${S.catalogTheme === "dark" ? "active" : ""}">深色</button>
      </div>
      <div class="sec"><div class="sec__title">颜色</div><div class="swatches">
        ${colors.map(([k, v]) => `<div class="swatch" style="background:${esc(v.value || v)}" title="${esc(k)} = ${esc(v.value || v)}"><span>${esc(k.slice(0, 6))}</span></div>`).join("")}
      </div></div>
      <div class="sec"><div class="sec__title">字型</div>
        ${levels.map(([k, v]) => `<div class="type-row"><span style="font-size:${Math.min(v.size || v.Size || 14, 22)}px;font-weight:${v.weight || v.Weight || 400}">${esc(k)}</span><small>${v.size || v.Size || "?"}px</small></div>`).join("")}
      </div></div>
      <div class="sec"><div class="sec__title">间距 / 圆角</div><div class="kv">
        ${spacing.map(([k, v]) => `<span class="chip">${esc(k)} ${v}</span>`).join("")}
        ${radius.map(([k, v]) => `<span class="chip">R ${esc(k)} ${v}</span>`).join("")}
      </div></div>
      <div class="sec"><div class="sec__title">组件（点击添加预览）</div><div class="comp-list">
        ${comps.map((c) => `<div class="comp-item"><span class="comp-item__name">${esc(c.name)}</span><span class="comp-item__id">${esc(c.id)}</span><button data-comp="${esc(c.id)}">${icon("plus", 14)}</button></div>`).join("")}
      </div></div>
      <div class="sec"><div class="sec__title">整页预览</div>
        <button id="sb-compose-all" class="sb-wide">${icon("grid", 14)} 添加全部核心组件（一张卡）</button>
      </div>
      <div class="sec"><div class="sec__title">使用守则</div><ul class="guideline">
        ${(sys.guideline || []).map((g) => `<li>${esc(g)}</li>`).join("")}
      </ul></div>`;
    body.querySelectorAll(".theme-seg button").forEach((b) => b.addEventListener("click", () => {
      S.catalogTheme = b.dataset.th;
      showSystemDetail(id);
    }));
    body.querySelectorAll("[data-comp]").forEach((b) => b.addEventListener("click", () => addPreview(id, [b.dataset.comp])));
    const all = body.querySelector("#sb-compose-all");
    if (all) all.addEventListener("click", () => addPreview(id, comps.slice(0, 8).map((c) => c.id), true));
  } catch (e) {
    toast("体系详情加载失败: " + e.message, true);
    backToList();
  }
}

function backToList() {
  $("sb-detail").classList.add("hidden");
  $("sb-list").classList.remove("hidden");
}

async function addPreview(systemID, components, wholePage) {
  if (!S.project) { toast("先在左侧项目树里选一个项目（或先新建），再添加预览", true); return; }
  try {
    toast("生成预览中…");
    const data = await api("/api/catalog/preview", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        project: S.project, system: systemID, components, theme: S.catalogTheme,
        title: wholePage ? systemID + " 组件总览" : "",
      }),
    });
    if (data.node) { S.nodes.set(data.node.id, data.node); addNodeEl(data.node); selectNode(data.node.id); }
    reloadProjects().catch(() => { /* 计数刷新失败不影响主流程 */ });
    toast("已添加到画布");
  } catch (e) { toast("添加失败: " + e.message, true); }
}

// ---------------- 截图任务（foreignObject 光栅化） ----------------
async function rasterize(html, width, height, scale) {
  const iframe = document.createElement("iframe");
  iframe.style.cssText = `position:fixed;left:-10000px;top:0;width:${width}px;height:${height}px;border:0;visibility:hidden`;
  iframe.setAttribute("sandbox", "allow-same-origin");
  iframe.srcdoc = html;
  document.body.appendChild(iframe);
  try {
    await new Promise((res) => iframe.addEventListener("load", res, { once: true }));
    // 布局就绪：rAF 优先，后台标签页被节流时用定时器兜底
    await Promise.race([
      new Promise((res) => requestAnimationFrame(() => requestAnimationFrame(res))),
      new Promise((res) => setTimeout(res, 300)),
    ]);
    const doc = iframe.contentDocument;
    const realH = Math.max(height, doc.body ? doc.body.scrollHeight : height);
    const xhtml = new XMLSerializer().serializeToString(doc.documentElement);
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${realH}">` +
      `<foreignObject width="100%" height="100%">${xhtml}</foreignObject></svg>`;
    const img = new Image();
    await new Promise((res, rej) => {
      img.onload = res;
      img.onerror = () => rej(new Error("SVG 光栅化失败"));
      img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg);
    });
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(width * scale));
    canvas.height = Math.max(1, Math.round(realH * scale));
    const ctx = canvas.getContext("2d");
    // 底色取文档 body 的实际背景色，避免深色主题在内容未铺满处露白
    let bg = "#ffffff";
    try {
      const c = doc.defaultView.getComputedStyle(doc.body).backgroundColor;
      if (c && c !== "rgba(0, 0, 0, 0)") bg = c;
    } catch (_) { /* 保持白底 */ }
    ctx.fillStyle = bg;
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
    return canvas.toDataURL("image/png").split(",")[1];
  } finally { iframe.remove(); }
}

async function runCaptureTask(task) {
  try {
    let png;
    if (task.node_id) {
      const n = S.nodes.get(task.node_id);
      if (!n) throw new Error("节点不在画布上: " + task.node_id);
      png = await rasterize(n.html, n.width, n.height, task.scale || 1);
    } else {
      if (S.nodes.size === 0) throw new Error("画布为空，没有可截图的内容");
      let minX = 1e9, minY = 1e9, maxX = -1e9, maxY = -1e9;
      for (const n of S.nodes.values()) {
        if (n.kind === "frame") continue; // 分组框是背板，不参与截图边界
        minX = Math.min(minX, n.x); minY = Math.min(minY, n.y);
        maxX = Math.max(maxX, n.x + n.width); maxY = Math.max(maxY, n.y + n.height);
      }
      const pad = 24, scale = task.scale || 1;
      const canvas = document.createElement("canvas");
      canvas.width = Math.round((maxX - minX + pad * 2) * scale);
      canvas.height = Math.round((maxY - minY + pad * 2) * scale);
      const ctx = canvas.getContext("2d");
      const pageBg = getComputedStyle(document.documentElement).getPropertyValue("--bg").trim();
      ctx.fillStyle = pageBg || "#0f1014";
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      // 先画连线（与画布一致：连线在节点下层）
      const toX = (v) => (v - minX + pad) * scale, toY = (v) => (v - minY + pad) * scale;
      ctx.strokeStyle = "#7c5cff";
      ctx.fillStyle = "#e8e9ee";
      ctx.lineWidth = 2 * scale;
      ctx.font = `${Math.round(11 * scale)}px sans-serif`;
      for (const e of S.edges.values()) {
        const a = S.nodes.get(e.from), b = S.nodes.get(e.to);
        if (!a || !b || a.kind === "frame" || b.kind === "frame") continue;
        const x1 = toX(a.x + a.width / 2), y1 = toY(a.y + a.height);
        const x2 = toX(b.x + b.width / 2), y2 = toY(b.y);
        const dy = Math.max(48 * scale, Math.abs(y2 - y1) / 2);
        ctx.beginPath();
        ctx.moveTo(x1, y1);
        ctx.bezierCurveTo(x1, y1 + dy, x2, y2 - dy, x2, y2);
        ctx.stroke();
        if (e.label) {
          const mx = (x1 + x2) / 2, my = (y1 + y2) / 2;
          const w = ctx.measureText(e.label).width + 8 * scale;
          ctx.fillStyle = "#16171d";
          ctx.fillRect(mx - w / 2, my - 10 * scale, w, 16 * scale);
          ctx.fillStyle = "#e8e9ee";
          ctx.fillText(e.label, mx - w / 2 + 4 * scale, my + 3 * scale);
          ctx.strokeStyle = "#7c5cff";
          ctx.fillStyle = "#e8e9ee";
        }
      }
      // 再叠节点（跳过分组框）
      for (const n of S.nodes.values()) {
        if (n.kind === "frame") continue;
        const part = await rasterize(n.html, n.width, n.height, scale);
        const img = new Image();
        await new Promise((res, rej) => { img.onload = res; img.onerror = rej; img.src = "data:image/png;base64," + part; });
        ctx.drawImage(img, Math.round((n.x - minX + pad) * scale), Math.round((n.y - minY + pad) * scale));
      }
      png = canvas.toDataURL("image/png").split(",")[1];
    }
    await api(`/api/tasks/${task.id}/result`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ png_base64: png }),
    });
  } catch (e) {
    try {
      await api(`/api/tasks/${task.id}/result`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ png_base64: "", error: e.message }),
      });
    } catch (_) { /* ignore */ }
  }
}

// 任务循环：纯 await 链 + 长轮询，不用定时器串链，
// 避免后台标签页被浏览器节流后无法领取截图任务。
async function taskLoop() {
  for (;;) {
    try {
      const data = await api("/api/tasks");
      if (data.task) await runCaptureTask(data.task);
    } catch (_) {
      await new Promise((r) => setTimeout(r, 2000)); // 网络异常退避
    }
  }
}

// ---------------- 工具条动作 ----------------
$("btn-zoom-in").addEventListener("click", () => zoomAt(innerWidth / 2, innerHeight / 2, 1.2));
$("btn-zoom-out").addEventListener("click", () => zoomAt(innerWidth / 2, innerHeight / 2, 1 / 1.2));
$("btn-fit").addEventListener("click", fitView);
$("sb-back").addEventListener("click", backToList);
$("insp-close").addEventListener("click", () => $("inspector").classList.add("hidden"));
function setSidebarCollapsed(collapsed) {
  const sb = $("sidebar");
  sb.classList.toggle("collapsed", collapsed);
  // body 上的 class 驱动 #sb-expand 显隐：侧栏整体移出屏幕后，
  // 屏内必须仍有一个可点的展开入口，否则面板就再也拉不回来了。
  document.body.classList.toggle("sb-collapsed", collapsed);
  const bt = $("sb-toggle");
  if (bt) bt.innerHTML = icon(collapsed ? "chevron-right" : "chevron-left", 16);
  try { localStorage.setItem("adago.sbCollapsed", collapsed ? "1" : "0"); } catch (_) { /* 隐私模式忽略 */ }
}

$("sb-toggle").addEventListener("click", () => setSidebarCollapsed(!$("sidebar").classList.contains("collapsed")));
$("sb-expand").addEventListener("click", () => setSidebarCollapsed(false));
$("solo-back").addEventListener("click", () => exitSolo());
// ---- 项目树交互 ----
$("tree-all").addEventListener("click", () => switchProject(SCOPE_ALL));
$("tree-new").addEventListener("click", openProjectModal);
$("tree-more").addEventListener("click", (e) => { e.stopPropagation(); toggleTreeMenu(); });

$("tree-menu").addEventListener("click", async (e) => {
  const btn = e.target.closest("[data-menu]");
  if (!btn) return;
  closeTreeMenu();
  const act = btn.dataset.menu;
  if (act === "manage") openProjectModal();
  else if (act === "arrange") await arrangeByProject(false);
  else if (act === "refresh") { await reloadProjects(); await pollNodes(); }
});

$("tree-filter").addEventListener("input", (e) => {
  S.treeFilter = e.target.value;
  renderProjectTree();
});

// 单击定位 / 双击单独展示：单击延后 220ms，双击到来就取消，
// 否则两次 click 会先触发两次 switchProject，和双击的切作用域打架。
let treeChildClickTimer = 0;
$("tree-rows").addEventListener("click", (e) => {
  const child = e.target.closest(".tree-child");
  if (child) {
    const project = child.dataset.project, node = child.dataset.node;
    clearTimeout(treeChildClickTimer);
    treeChildClickTimer = setTimeout(() => focusNode(project, node), 220);
    return;
  }
  const row = e.target.closest(".tree-row");
  if (!row) return;
  if (e.target.closest('[data-act="toggle"]')) { toggleTreeRow(row.dataset.id); return; }
  switchProject(row.dataset.id);
});

$("tree-rows").addEventListener("dblclick", (e) => {
  const child = e.target.closest(".tree-child[data-node]");
  if (!child) return;
  clearTimeout(treeChildClickTimer);
  e.preventDefault();
  e.stopPropagation();
  enterSolo(child.dataset.node, child.dataset.project);
});

// 点空白关掉「更多」菜单
document.addEventListener("click", (e) => {
  if (!e.target.closest(".tree__head")) closeTreeMenu();
});

// ⇧N：新建项目（与设计稿工具的习惯一致）
window.addEventListener("keydown", (e) => {
  if (!e.shiftKey || (e.key !== "N" && e.key !== "n")) return;
  if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
  e.preventDefault();
  openProjectModal();
});

$("proj-create").addEventListener("click", createProjectFromModal);
$("proj-name").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); createProjectFromModal(); }
});
document.querySelectorAll("[data-proj-close]").forEach((el) => {
  el.addEventListener("click", closeProjectModal);
});
// Esc：先关「更多」菜单，再关弹层，不再落到「清空选择 / 退出检查模式」上
window.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  if (isProjectModalOpen()) { closeProjectModal(); e.preventDefault(); e.stopPropagation(); return; }
  const menu = $("tree-menu");
  if (menu && !menu.classList.contains("hidden")) {
    closeTreeMenu();
    e.preventDefault();
    e.stopPropagation();
    return;
  }
  // 检查模式里还留着 A/B 配对时，Esc 先归检查模式（清 B → 清 A），别抢
  const inspecting = typeof INS !== "undefined" && INS.on && (INS.anchor || INS.target);
  if (S.solo && !inspecting) {
    exitSolo();
    e.preventDefault();
    e.stopImmediatePropagation();
  }
}, true);

$("btn-theme").addEventListener("click", () => {
  const html = document.documentElement;
  html.dataset.theme = html.dataset.theme === "dark" ? "light" : "dark";
});
$("btn-clear").addEventListener("click", async () => {
  if (!S.project) { toast("还没有项目", true); return; }
  if (!confirm("清空项目「" + projectName(S.project) + "」下的全部设计稿？")) return;
  try {
    await api("/api/nodes/clear", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ project: S.project }),
    });
    for (const id of [...S.nodes.keys()]) removeNodeEl(id);
    S.edges = new Map();
    scheduleEdges("*");
    toast("画布已清空");
  } catch (e) { toast("清空失败: " + e.message, true); }
});
$("btn-add-note").addEventListener("click", async () => {
  if (!S.project) { toast("先在左侧项目树里选一个项目（或先新建），再添加便签", true); return; }
  const text = prompt("便签内容：", "备注…");
  if (text == null) return;
  const html = `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><style>
    *{box-sizing:border-box}body{margin:0;font-family:"Inter","Noto Sans SC",system-ui,sans-serif;background:#FFF7C2;color:#5b4a00;
    padding:14px;font-size:13px;line-height:1.6;min-height:120px;white-space:pre-wrap}</style></head>
    <body>${esc(text)}</body></html>`;
  try {
    const data = await api("/api/nodes", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ project: S.project, title: "便签", kind: "note", html, width: 220, height: 140 }),
    });
    S.nodes.set(data.node.id, data.node);
    addNodeEl(data.node);
    selectNode(data.node.id);
  } catch (e) { toast("添加失败: " + e.message, true); }
});

// ---------------- 启动 ----------------
applyTransform();
try {
  if (localStorage.getItem("adago.sbCollapsed") === "1") setSidebarCollapsed(true);
} catch (_) { /* 忽略 */ }
loadProjects().then(() => {
  if (!S.projects.length) showEmptyHint();
  pollNodes().then(() => {
    if (S.nodes.size === 0) showEmptyHint();
    fitView();
  });
});
setInterval(pollNodes, 2000);
loadCatalog();
taskLoop();
