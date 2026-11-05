/* adago-design 无限画布（vanilla JS，无构建步骤） */
"use strict";

// ---------------- 状态 ----------------
const S = {
  nodes: new Map(),      // id -> node
  els: new Map(),        // id -> element
  tx: 0, ty: 0, scale: 1,
  selected: null,
  dragging: null,        // {id, dx, dy, moved}
  panning: null,         // {sx, sy, tx, ty}
  catalogTheme: "light",
  online: true,
};

const $ = (id) => document.getElementById(id);
const viewport = $("viewport"), world = $("world"), grid = $("grid-layer");

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
  c.textContent = ok ? "● 已连接" : "● 连接断开";
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
}

function zoomAt(cx, cy, factor) {
  const ns = Math.min(4, Math.max(0.08, S.scale * factor));
  if (ns === S.scale) return;
  const k = ns / S.scale;
  S.tx = cx - (cx - S.tx) * k;
  S.ty = cy - (cy - S.ty) * k;
  S.scale = ns;
  applyTransform();
}

function fitView() {
  if (S.nodes.size === 0) { S.tx = innerWidth / 2 - 200; S.ty = innerHeight / 2 - 150; S.scale = 1; applyTransform(); return; }
  let minX = 1e9, minY = 1e9, maxX = -1e9, maxY = -1e9;
  for (const n of S.nodes.values()) {
    minX = Math.min(minX, n.x); minY = Math.min(minY, n.y);
    maxX = Math.max(maxX, n.x + n.width); maxY = Math.max(maxY, n.y + n.height);
  }
  const pad = 80;
  const bw = maxX - minX + pad * 2, bh = maxY - minY + pad * 2;
  const scale = Math.min(1.2, Math.max(0.08, Math.min((innerWidth - 320) / bw, (innerHeight - 120) / bh)));
  S.scale = scale;
  S.tx = (innerWidth + 296) / 2 - (minX + maxX) / 2 * scale - 148;
  S.ty = innerHeight / 2 - (minY + maxY) / 2 * scale;
  applyTransform();
}

// ---------------- 节点渲染 ----------------
function nodeEl(n) {
  const el = document.createElement("div");
  el.className = "node";
  el.dataset.id = n.id;
  el.style.left = n.x + "px";
  el.style.top = n.y + "px";
  el.style.width = n.width + "px";
  el.style.height = n.height + "px";
  el.style.zIndex = n.z || 1;
  const sys = n.system ? `<span class="node__tag">${esc(n.system)}</span>` : "";
  el.innerHTML =
    `<div class="node__head"><span class="node__title">${esc(n.title || n.id)}</span>${sys}` +
    `<button class="node__del" title="删除">✕</button></div>` +
    `<div class="node__body"><iframe sandbox="allow-same-origin" srcdoc="${esc(n.html)}" scrolling="auto"></iframe></div>`;
  el.querySelector(".node__del").addEventListener("click", (e) => { e.stopPropagation(); removeNode(n.id); });
  el.querySelector(".node__head").addEventListener("mousedown", (e) => startNodeDrag(e, n.id));
  el.addEventListener("mousedown", () => selectNode(n.id));
  return el;
}

function addNodeEl(n) {
  const el = nodeEl(n);
  S.els.set(n.id, el);
  world.appendChild(el);
  hideEmptyHint();
}

function updateNodeEl(n) {
  const el = S.els.get(n.id);
  if (!el) { addNodeEl(n); return; }
  el.style.left = n.x + "px";
  el.style.top = n.y + "px";
  el.style.width = n.width + "px";
  el.style.height = n.height + "px";
  el.style.zIndex = n.z || 1;
  const title = el.querySelector(".node__title");
  if (title.textContent !== (n.title || n.id)) title.textContent = n.title || n.id;
  const iframe = el.querySelector("iframe");
  if (iframe.dataset.rev !== n.updated_at) {
    iframe.dataset.rev = n.updated_at;
    iframe.srcdoc = n.html;
  }
}

function removeNodeEl(id) {
  const el = S.els.get(id);
  if (el) el.remove();
  S.els.delete(id);
  S.nodes.delete(id);
  if (S.selected === id) { S.selected = null; $("inspector").classList.add("hidden"); }
  if (S.nodes.size === 0) showEmptyHint();
}

function hideEmptyHint() { const h = document.querySelector(".empty-hint"); if (h) h.remove(); }
function showEmptyHint() {
  if (document.querySelector(".empty-hint")) return;
  const d = document.createElement("div");
  d.className = "empty-hint";
  d.innerHTML = "<b>无限画布</b><br>滚轮缩放 · 拖拽平移 · 从左侧设计体系添加预览<br>Agent 会通过 MCP 工具把组件预览摆放到这里";
  document.body.appendChild(d);
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
    ["ID", n.id], ["标题", n.title || "-"], ["体系", n.system || "-"], ["类型", n.kind],
    ["尺寸", n.width + " × " + n.height], ["坐标", Math.round(n.x) + ", " + Math.round(n.y)],
    ["更新", (n.updated_at || "").replace("T", " ").slice(0, 19)],
  ];
  $("insp-body").innerHTML =
    rows.map(([k, v]) => `<div class="kv"><span class="chip">${esc(k)}</span><span>${esc(v)}</span></div>`).join("") +
    `<div class="sec"><div class="sec__title">标签</div><div class="kv">${(n.tags || []).map((t) => `<span class="chip">${esc(t)}</span>`).join("") || "-"}</div></div>`;
  $("inspector").classList.remove("hidden");
}

async function removeNode(id) {
  try {
    await api("/api/nodes/" + id, { method: "DELETE" });
    removeNodeEl(id);
  } catch (e) { toast("删除失败: " + e.message, true); }
}

// ---------------- 拖拽 / 平移 / 缩放 ----------------
function startNodeDrag(e, id) {
  if (e.button !== 0) return;
  e.preventDefault(); e.stopPropagation();
  const n = S.nodes.get(id);
  S.dragging = { id, dx: e.clientX / S.scale - n.x, dy: e.clientY / S.scale - n.y, moved: false };
  const el = S.els.get(id);
  el.classList.add("dragging");
  el.style.zIndex = 9999;
}

window.addEventListener("mousemove", (e) => {
  if (S.dragging) {
    const n = S.nodes.get(S.dragging.id);
    if (!n) return;
    const nx = e.clientX / S.scale - S.dragging.dx;
    const ny = e.clientY / S.scale - S.dragging.dy;
    if (Math.abs(nx - n.x) > 1 || Math.abs(ny - n.y) > 1) S.dragging.moved = true;
    n.x = nx; n.y = ny;
    const el = S.els.get(S.dragging.id);
    el.style.left = nx + "px"; el.style.top = ny + "px";
  } else if (S.panning) {
    S.tx = S.panning.tx + (e.clientX - S.panning.sx);
    S.ty = S.panning.ty + (e.clientY - S.panning.sy);
    applyTransform();
  }
});

window.addEventListener("mouseup", async () => {
  if (S.dragging) {
    const { id, moved } = S.dragging;
    const el = S.els.get(id);
    el.classList.remove("dragging");
    S.dragging = null;
    if (moved) {
      const n = S.nodes.get(id);
      try { await api("/api/nodes/" + id, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ x: n.x, y: n.y }) }); }
      catch (e) { toast("位置保存失败: " + e.message, true); }
    }
  }
  if (S.panning) { viewport.classList.remove("panning"); S.panning = null; }
});

viewport.addEventListener("mousedown", (e) => {
  if (e.target !== viewport && e.target !== grid && e.target !== world) return;
  if (e.button !== 0) return;
  S.panning = { sx: e.clientX, sy: e.clientY, tx: S.tx, ty: S.ty };
  viewport.classList.add("panning");
  if (S.selected) { const el = S.els.get(S.selected); if (el) el.classList.remove("selected"); S.selected = null; $("inspector").classList.add("hidden"); }
});

viewport.addEventListener("wheel", (e) => {
  e.preventDefault();
  const factor = e.deltaY < 0 ? 1.12 : 1 / 1.12;
  zoomAt(e.clientX, e.clientY, factor);
}, { passive: false });

window.addEventListener("keydown", (e) => {
  if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
  if (e.key === "Delete" || e.key === "Backspace") { if (S.selected) { e.preventDefault(); removeNode(S.selected); } }
  else if (e.key === "f" || e.key === "F") fitView();
  else if (e.key === "Escape") { if (S.selected) { const el = S.els.get(S.selected); if (el) el.classList.remove("selected"); S.selected = null; $("inspector").classList.add("hidden"); } }
  else if (e.key === "=" || e.key === "+") zoomAt(innerWidth / 2, innerHeight / 2, 1.15);
  else if (e.key === "-") zoomAt(innerWidth / 2, innerHeight / 2, 1 / 1.15);
});

// ---------------- 同步轮询 ----------------
async function pollNodes() {
  try {
    const data = await api("/api/nodes");
    const seen = new Set();
    for (const n of data.nodes || []) {
      seen.add(n.id);
      const local = S.nodes.get(n.id);
      if (!local) {
        S.nodes.set(n.id, n);
        addNodeEl(n);
      } else if (local.updated_at !== n.updated_at && (!S.dragging || S.dragging.id !== n.id)) {
        S.nodes.set(n.id, n);
        updateNodeEl(n);
      }
    }
    for (const id of [...S.nodes.keys()]) if (!seen.has(id)) removeNodeEl(id);
  } catch (_) { /* 状态条已处理 */ }
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
        `<div class="sys-card__chips">${(sys.keywords || []).map((k) => `<span class="chip">${esc(k)}</span>`).join("")}</div>`;
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
      <div class="theme-seg" style="margin-top:10px">
        <button data-th="light" class="${S.catalogTheme === "light" ? "active" : ""}">浅色</button>
        <button data-th="dark" class="${S.catalogTheme === "dark" ? "active" : ""}">深色</button>
      </div>
      <div class="sec"><div class="sec__title">颜色</div><div class="swatches">
        ${colors.map(([k, v]) => `<div class="swatch" style="background:${esc(v.value || v)}" title="${esc(k)} = ${esc(v.value || v)}"><span>${esc(k.slice(0, 6))}</span></div>`).join("")}
      </div></div>
      <div class="sec"><div class="sec__title">字型</div>
        ${levels.map(([k, v]) => `<div class="type-row"><span style="font-size:${Math.min(v.size || v.Size || 14, 22)}px;font-weight:${v.weight || v.Weight || 400}">${esc(k)}</span><small>${v.size || v.Size || "?"}px</small></div>`).join("")}
      </div>
      <div class="sec"><div class="sec__title">间距 / 圆角</div><div class="kv">
        ${spacing.map(([k, v]) => `<span class="chip">${esc(k)} ${v}</span>`).join("")}
        ${radius.map(([k, v]) => `<span class="chip">R ${esc(k)} ${v}</span>`).join("")}
      </div></div>
      <div class="sec"><div class="sec__title">组件（点击添加预览）</div><div class="comp-list">
        ${comps.map((c) => `<div class="comp-item"><span class="comp-item__name">${esc(c.name)}</span><span class="comp-item__id">${esc(c.id)}</span><button data-comp="${esc(c.id)}">＋</button></div>`).join("")}
      </div></div>
      <div class="sec"><div class="sec__title">整页预览</div>
        <button id="sb-compose-all" style="width:100%;padding:7px;border-radius:9px;border:1px solid var(--accent);background:transparent;color:var(--accent);cursor:pointer;font-size:12px">添加全部核心组件（一张卡）</button>
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
  try {
    toast("生成预览中…");
    const data = await api("/api/catalog/preview", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ system: systemID, components, theme: S.catalogTheme, title: wholePage ? systemID + " 组件总览" : "" }),
    });
    if (data.node) { S.nodes.set(data.node.id, data.node); addNodeEl(data.node); selectNode(data.node.id); }
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
    ctx.fillStyle = "#ffffff";
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
        minX = Math.min(minX, n.x); minY = Math.min(minY, n.y);
        maxX = Math.max(maxX, n.x + n.width); maxY = Math.max(maxY, n.y + n.height);
      }
      const pad = 24, scale = task.scale || 1;
      const canvas = document.createElement("canvas");
      canvas.width = Math.round((maxX - minX + pad * 2) * scale);
      canvas.height = Math.round((maxY - minY + pad * 2) * scale);
      const ctx = canvas.getContext("2d");
      ctx.fillStyle = getComputedStyle(document.documentElement).getPropertyValue("--bg") || "#0f1014";
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      for (const n of S.nodes.values()) {
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
$("sb-toggle").addEventListener("click", () => {
  const sb = $("sidebar");
  sb.classList.toggle("collapsed");
  $("sb-toggle").textContent = sb.classList.contains("collapsed") ? "›" : "‹";
});
$("btn-theme").addEventListener("click", () => {
  const html = document.documentElement;
  html.dataset.theme = html.dataset.theme === "dark" ? "light" : "dark";
});
$("btn-clear").addEventListener("click", async () => {
  if (!confirm("清空画布上的全部节点？")) return;
  try {
    await api("/api/nodes/clear", { method: "POST" });
    for (const id of [...S.nodes.keys()]) removeNodeEl(id);
    toast("画布已清空");
  } catch (e) { toast("清空失败: " + e.message, true); }
});
$("btn-add-note").addEventListener("click", async () => {
  const text = prompt("便签内容：", "备注…");
  if (text == null) return;
  const html = `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><style>
    *{box-sizing:border-box}body{margin:0;font-family:"Segoe UI",system-ui,sans-serif;background:#FFF7C2;color:#5b4a00;
    padding:14px;font-size:13px;line-height:1.6;min-height:120px;white-space:pre-wrap}</style></head>
    <body>${esc(text)}</body></html>`;
  try {
    const data = await api("/api/nodes", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title: "便签", kind: "note", html, width: 220, height: 140 }),
    });
    S.nodes.set(data.node.id, data.node);
    addNodeEl(data.node);
    selectNode(data.node.id);
  } catch (e) { toast("添加失败: " + e.message, true); }
});

// ---------------- 启动 ----------------
applyTransform();
showEmptyHint();
pollNodes().then(fitView);
setInterval(pollNodes, 2000);
loadCatalog();
taskLoop();
