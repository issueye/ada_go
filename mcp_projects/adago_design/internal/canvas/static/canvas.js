/* adago-design 无限画布（vanilla JS，无构建步骤） */
"use strict";

// ---------------- 状态 ----------------
const S = {
  nodes: new Map(),      // id -> node
  els: new Map(),        // id -> element
  edges: new Map(),      // id -> edge
  tx: 0, ty: 0, scale: 1,
  selected: null,
  dragging: null,        // {id, dx, dy, moved}
  panning: null,         // {sx, sy, tx, ty}
  catalogTheme: "light",
  online: true,
};

const $ = (id) => document.getElementById(id);
const viewport = $("viewport"), world = $("world"), grid = $("grid-layer");
const edgeSvg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
edgeSvg.id = "edges";
edgeSvg.innerHTML = '<defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill="#7c5cff"/></marker></defs>';
world.appendChild(edgeSvg);

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
  el.className = "node" + (n.kind === "frame" ? " node--frame" : "");
  el.dataset.id = n.id;
  el.style.left = n.x + "px";
  el.style.top = n.y + "px";
  el.style.width = n.width + "px";
  el.style.height = n.height + "px";
  el.style.zIndex = n.z || 1;
  const sys = n.system ? `<span class="node__tag">${esc(n.system)}</span>` : "";
  if (n.kind === "frame") {
    el.innerHTML =
      `<div class="node__head node__head--frame"><span class="node__title">${esc(n.title || "分组")}</span>` +
      `<button class="node__del" title="删除">✕</button></div>`;
  } else {
    el.innerHTML =
      `<div class="node__head"><span class="node__title">${esc(n.title || n.id)}</span>${sys}` +
      `<button class="node__del" title="删除">✕</button></div>` +
      `<div class="node__body"><iframe sandbox="allow-same-origin" srcdoc="${esc(n.html)}" scrolling="auto"></iframe></div>`;
  }
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
    renderEdges();
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
    renderEdges();
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

// ---------------- 连线渲染（SVG，世界坐标系内） ----------------
function renderEdges() {
  while (edgeSvg.childNodes.length > 1) edgeSvg.removeChild(edgeSvg.lastChild);
  for (const e of S.edges.values()) {
    const a = S.nodes.get(e.from), b = S.nodes.get(e.to);
    if (!a || !b) continue;
    const ax = a.x + a.width / 2, ay = a.y + a.height / 2;
    const bx = b.x + b.width / 2, by = b.y + b.height / 2;
    const line = document.createElementNS("http://www.w3.org/2000/svg", "path");
    // 贝塞尔：从源底部中点到目标顶部中点，距离越远弧度越大
    const x1 = a.x + a.width / 2, y1 = a.y + a.height;
    const x2 = b.x + b.width / 2, y2 = b.y;
    const dy = Math.max(48, Math.abs(y2 - y1) / 2);
    line.setAttribute("d", `M ${x1} ${y1} C ${x1} ${y1 + dy}, ${x2} ${y2 - dy}, ${x2} ${y2}`);
    line.setAttribute("class", "edge-line");
    line.setAttribute("marker-end", "url(#arrow)");
    edgeSvg.appendChild(line);
    if (e.label) {
      const text = document.createElementNS("http://www.w3.org/2000/svg", "text");
      text.setAttribute("x", (ax + bx) / 2);
      text.setAttribute("y", (ay + by) / 2);
      text.setAttribute("class", "edge-label");
      text.textContent = e.label;
      edgeSvg.appendChild(text);
    }
    // 引用保存到 line 便于删除节点时清理（renderEdges 每次全量重画即可）
    line.dataset.edgeId = e.id;
  }
}

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
    // 连线同步
    try {
      const ed = await api("/api/edges");
      S.edges = new Map((ed.edges || []).map((e) => [e.id, e]));
    } catch (_) { /* 连线同步失败不影响节点 */ }
    renderEdges();
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
      ctx.fillStyle = getComputedStyle(document.documentElement).getPropertyValue("--bg") || "#0f1014";
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
    S.edges = new Map();
    renderEdges();
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

// ================= 元素检查模式（蓝湖式标注） =================
// 同源 iframe（sandbox=allow-same-origin）允许父页读取 contentDocument，
// 因此检查逻辑全部在父页实现：悬停高亮 → 点击选中 → 注入测量层 → 规格面板。
const INS = {
  on: false,
  node: null,        // 正在检查的节点 id
  target: null,      // 选中的元素
  hover: null,       // 悬停的元素
  catalog: null,     // 体系 Token 详情缓存 {systemId: detail}
};

const ACCENT = "#fe2c55";

function toggleInspect() {
  INS.on = !INS.on;
  $("btn-inspect").classList.toggle("tb-active", INS.on);
  for (const [, el] of S.els) {
    const iframe = el.querySelector("iframe");
    if (!iframe) continue;
    if (INS.on) attachInspect(iframe);
    else detachInspect(iframe);
  }
  if (!INS.on) {
    INS.node = null; INS.target = null;
    if (S.selected) { selectNode(S.selected); } // 恢复节点信息面板
  }
  toast(INS.on ? "检查模式已开启：在预览上悬停/点选元素" : "检查模式已关闭");
}

// 在 iframe 文档上挂载检查监听
function attachInspect(iframe) {
  const doc = iframe.contentDocument;
  if (!doc || !doc.body) {
    // 文档未就绪：load 后再挂
    iframe.addEventListener("load", () => attachInspect(iframe), { once: true });
    return;
  }
  if (iframe.__dsx) return; // 已挂载
  const state = { move: null, leave: null, click: null, scroll: null, layer: null };
  iframe.__dsx = state;
  const nodeId = iframe.closest(".node").dataset.id;

  state.move = (e) => {
    if (INS.node && INS.node !== nodeId && INS.target) return; // 已选中别的节点则不跟随悬停
    setHover(iframe, nodeId, e.target);
  };
  state.leave = () => {
    if (INS.hover === iframe) clearHover();
  };
  state.click = (e) => {
    e.preventDefault(); e.stopPropagation();
    INS.node = nodeId;
    INS.target = e.target;
    drawInspect(iframe);
    showSpecPanel(iframe, nodeId, e.target);
  };
  state.scroll = () => { if (INS.node === nodeId && INS.target) drawInspect(iframe); };
  doc.addEventListener("mousemove", state.move, true);
  doc.addEventListener("mouseleave", state.leave, true);
  doc.addEventListener("click", state.click, true);
  doc.addEventListener("scroll", state.scroll, true);
  iframe.addEventListener("load", () => { if (INS.on) { detachInspect(iframe); attachInspect(iframe); } });
}

function detachInspect(iframe) {
  const state = iframe.__dsx;
  if (!state) return;
  const doc = iframe.contentDocument;
  if (doc) {
    if (state.move) doc.removeEventListener("mousemove", state.move, true);
    if (state.leave) doc.removeEventListener("mouseleave", state.leave, true);
    if (state.click) doc.removeEventListener("click", state.click, true);
    if (state.scroll) doc.removeEventListener("scroll", state.scroll, true);
  }
  iframe.__dsx = null;
  iframe.dataset.inspectAttached = "";
}

function ensureLayer(iframe) {
  const doc = iframe.contentDocument;
  let layer = doc.querySelector(".dsx-layer");
  if (!layer) {
    layer = doc.createElement("div");
    layer.className = "dsx-layer";
    const style = doc.createElement("style");
    style.textContent = `
      .dsx-layer{position:fixed;inset:0;pointer-events:none;z-index:2147483000}
      .dsx-mask{position:fixed;background:rgba(22,24,35,.35)}
      .dsx-hl{position:fixed;outline:2px solid ${ACCENT};outline-offset:-1px;background:rgba(254,44,85,.06)}
      .dsx-line{position:fixed;background:${ACCENT}}
      .dsx-tick{position:fixed;background:${ACCENT}}
      .dsx-label{position:fixed;background:${ACCENT};color:#fff;font:600 10px/1 "Segoe UI",system-ui,sans-serif;
        padding:2px 5px;border-radius:3px;white-space:nowrap;z-index:2147483001}
    `;
    doc.head.appendChild(style);
    doc.body.appendChild(layer);
  }
  return layer;
}

function clearHover() {
  for (const [, el] of S.els) {
    const iframe = el.querySelector("iframe");
    if (iframe && iframe.__dsx) {
      const layer = ensureLayer(iframe);
      layer.innerHTML = "";
    }
  }
  INS.hover = null;
  if (INS.on && !INS.target && INS.node) { /* 保留选中态 */ }
}

// 悬停跟随高亮
function setHover(iframe, nodeId, target) {
  if (INS.node && INS.node !== nodeId) return;
  if (!target || target === iframe.contentDocument.documentElement || target === iframe.contentDocument.body) {
    if (!INS.target) { ensureLayer(iframe).innerHTML = ""; }
    return;
  }
  INS.hover = iframe;
  drawInspect(iframe, target, false);
}

// 绘制测量层：遮罩 + 高亮 + 画板四边距离 + 最近兄弟间距（蓝湖式）
function drawInspect(iframe, target, pinned) {
  const doc = iframe.contentDocument;
  const layer = ensureLayer(iframe);
  let el = target || INS.target;
  if (!el || !el.isConnected) return;
  if (!pinned) {
    if (INS.target && INS.node === iframe.closest(".node").dataset.id) {
      el = INS.target; // 选中态优先于悬停
    }
  }
  layer.innerHTML = "";
  const rect = el.getBoundingClientRect();
  const bodyRect = doc.body.getBoundingClientRect();
  // 四周遮罩（蓝湖式聚焦）
  const masks = [
    [0, 0, "100%", Math.max(0, rect.top)],
    [0, rect.bottom, "100%", Math.max(0, bodyRect.height - rect.bottom)],
    [0, rect.top, Math.max(0, rect.left), rect.height],
    [rect.right, rect.top, Math.max(0, bodyRect.width - rect.right), rect.height],
  ];
  for (const [x, y, w, h] of masks) {
    if (w <= 0 || h <= 0) continue;
    const m = doc.createElement("div");
    m.className = "dsx-mask";
    m.style.cssText = `left:${x}px;top:${y}px;width:${w}px;height:${h}px`;
    layer.appendChild(m);
  }
  const hl = doc.createElement("div");
  hl.className = "dsx-hl";
  hl.style.cssText = `left:${rect.left}px;top:${rect.top}px;width:${rect.width}px;height:${rect.height}px`;
  layer.appendChild(hl);

  // 画板四边距离（垂直/水平两条测量线）
  const dists = [];
  if (rect.top - bodyRect.top >= 0) dists.push(["v", rect.left + rect.width / 2, bodyRect.top, rect.top]);
  if (bodyRect.bottom - rect.bottom >= 0) dists.push(["v", rect.left + rect.width / 2, rect.bottom, bodyRect.bottom]);
  if (rect.left - bodyRect.left >= 0) dists.push(["h", bodyRect.left, rect.top + rect.height / 2, rect.left]);
  if (bodyRect.right - rect.right >= 0) dists.push(["h", rect.right, rect.top + rect.height / 2, bodyRect.right]);
  // 最近兄弟间距
  const parent = el.parentElement;
  if (parent && parent !== doc.body) {
    const pRect = parent.getBoundingClientRect();
    for (const sib of parent.children) {
      if (sib === el || sib.classList.contains("dsx-layer")) continue;
      const s = sib.getBoundingClientRect();
      if (s.width === 0 && s.height === 0) continue;
      const vOverlap = rect.top < s.bottom && s.top < rect.bottom;
      const hOverlap = rect.left < s.right && s.left < rect.right;
      if (vOverlap && s.right <= rect.left) dists.push(["h", s.right, rect.top + rect.height / 2, rect.left]);
      if (vOverlap && s.left >= rect.right) dists.push(["h", rect.right, rect.top + rect.height / 2, s.left]);
      if (hOverlap && s.bottom <= rect.top) dists.push(["v", Math.max(rect.left, s.left) + Math.min(rect.width, s.width) / 2, s.bottom, rect.top]);
      if (hOverlap && s.top >= rect.bottom) dists.push(["v", Math.max(rect.left, s.left) + Math.min(rect.width, s.width) / 2, rect.bottom, s.top]);
    }
    void pRect;
  }
  for (const [dir, x1, y1, y2orx2] of dists) {
    const isV = dir === "v";
    const from = isV ? y1 : x1;
    const to = isV ? y2orx2 : y2orx2;
    const len = Math.abs(to - from);
    if (len < 1) continue;
    const line = doc.createElement("div");
    line.className = "dsx-line";
    if (isV) line.style.cssText = `left:${x1}px;top:${Math.min(from, to)}px;width:1px;height:${len}px`;
    else line.style.cssText = `left:${Math.min(from, to)}px;top:${y1}px;width:${len}px;height:1px`;
    layer.appendChild(line);
    // 端点刻度
    for (const p of [from, to]) {
      const tick = doc.createElement("div");
      tick.className = "dsx-tick";
      if (isV) tick.style.cssText = `left:${x1 - 3}px;top:${p - 3}px;width:7px;height:7px`;
      else tick.style.cssText = `left:${p - 3}px;top:${y1 - 3}px;width:7px;height:7px`;
      layer.appendChild(tick);
    }
    const label = doc.createElement("div");
    label.className = "dsx-label";
    if (isV) label.style.cssText = `left:${x1 + 6}px;top:${(from + to) / 2 - 7}px`;
    else label.style.cssText = `left:${(from + to) / 2 - 14}px;top:${y1 - 18}px`;
    label.textContent = Math.round(len) + "px";
    layer.appendChild(label);
  }
}

// 规格面板：结构化样式 + Token 合规标记
async function showSpecPanel(iframe, nodeId, el) {
  const n = S.nodes.get(nodeId);
  if (!n) return;
  selectNode(nodeId);
  const detail = await ensureCatalog(n.system);
  const cs = iframe.contentDocument.defaultView.getComputedStyle(el);
  const con = (val, kind) => tokenMark(val, kind, detail);
  const fmtDist = (v) => Math.max(0, Math.round(v));
  const rect = el.getBoundingClientRect();
  const bodyRect = iframe.contentDocument.body.getBoundingClientRect();
  const tag = el.tagName.toLowerCase() + (el.className && typeof el.className === "string" && el.className.trim() ? "." + el.className.trim().split(/\s+/).join(".") : "");
  const rows = [];
  rows.push(["尺寸", `${Math.round(rect.width)} × ${Math.round(rect.height)} px`]);
  rows.push(["到画板", `上${fmtDist(rect.top - bodyRect.top)} / 左${fmtDist(rect.left - bodyRect.left)} / 下${fmtDist(bodyRect.bottom - rect.bottom)} / 右${fmtDist(bodyRect.right - rect.right)}`]);
  const pad = [cs.paddingTop, cs.paddingRight, cs.paddingBottom, cs.paddingLeft].map((v) => Math.round(parseFloat(v)));
  rows.push(["内边距", `${pad[0]} ${pad[1]} ${pad[2]} ${pad[3]} ${con(pad[0], "spacing")}${con(pad[1], "spacing")}`]);
  const bt = cs.borderTopWidth;
  rows.push(["边框", bt !== "0px" ? `${bt} ${cs.borderTopStyle} ${cs.borderTopColor}` : "无"]);
  const brs = [cs.borderTopLeftRadius, cs.borderTopRightRadius, cs.borderBottomRightRadius, cs.borderBottomLeftRadius].map((v) => Math.round(parseFloat(v)));
  rows.push(["圆角", brs[0] === brs[1] && brs[1] === brs[2] && brs[2] === brs[3] ? `${brs[0]}px ${con(brs[0], "radius")}` : `${brs.join(" / ")} ${con(brs[0], "radius")}`]);
  rows.push(["阴影", cs.boxShadow !== "none" ? cs.boxShadow.slice(0, 60) + (cs.boxShadow.length > 60 ? "…" : "") + con(cs.boxShadow, "shadow") : "无"]);
  rows.push(["背景", cs.backgroundColor]);
  const fs = Math.round(parseFloat(cs.fontSize));
  rows.push(["字体", `${fs}px / ${cs.fontWeight} ${con(fs, "type")} ${cs.color}`]);

  $("insp-body").innerHTML =
    `<div style="font-size:11px;color:var(--fg2);margin-bottom:8px">🔍 元素规格 · ${esc(n.title || n.id)}（Esc 退出选中）</div>` +
    `<div class="spec-tag">${esc(tag)}</div>` +
    rows.map(([k, v]) => `<div class="kv spec-row"><span class="chip">${esc(k)}</span><span class="spec-val">${v}</span></div>`).join("");
}

// Token 合规标记：✓ 落在阶梯 / ⚠ 偏差（给出最近规范值）
function tokenMark(val, kind, detail) {
  if (!detail || !detail.light) return "";
  const tok = detail.light;
  let vals = null;
  if (kind === "spacing") vals = Object.values(tok.spacing || {});
  else if (kind === "radius") vals = Object.values(tok.radius || {});
  else if (kind === "type") vals = Object.values(tok.type.levels || {}).map((l) => l.size);
  else return "";
  if (!vals || !vals.length) return "";
  let nearest = null, diff = 1e9;
  for (const v of vals) {
    const d = Math.abs(val - v);
    if (d < diff || (d === diff && nearest !== null && v < nearest)) { diff = d; nearest = v; }
  }
  if (diff === 0) return ` <span class="mark ok">✓ ${nearest}px</span>`;
  if (diff <= 2 && nearest > 0) return ` <span class="mark ok">✓≈${nearest}px</span>`;
  return ` <span class="mark warn">⚠ 规范 ${nearest}px</span>`;
}

async function ensureCatalog(systemID) {
  if (!systemID) return null;
  INS.catalog = INS.catalog || {};
  if (INS.catalog[systemID]) return INS.catalog[systemID];
  try {
    const d = await api("/api/catalog/systems/" + encodeURIComponent(systemID));
    INS.catalog[systemID] = d;
    return d;
  } catch (_) { return null; }
}

// 检查模式的 Esc 退出选中
window.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && INS.on && INS.target) {
    INS.target = null; INS.node = null;
    clearHover();
    for (const [, el] of S.els) {
      const iframe = el.querySelector("iframe");
      if (iframe && iframe.__dsx) ensureLayer(iframe).innerHTML = "";
    }
    if (S.selected) selectNode(S.selected);
    e.stopPropagation();
  }
}, true);

$("btn-inspect").addEventListener("click", toggleInspect);
