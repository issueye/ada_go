/* adago-design 画布 · 节点：渲染（iframe srcdoc）、选中、删除、拖动（pointer capture + rAF）、位置保存、高度自适应。
 * 经典脚本按 index.html 顺序加载，全局作用域共享（与 inspect.js 同一契约）；
 * 各文件顶层语句的执行顺序与拆分前完全一致，请勿调换加载顺序。 */
"use strict";
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

function updateNodeEl(n, prev) {
  const el = S.els.get(n.id);
  if (!el) { addNodeEl(n); return; }
  applyNodeGeom(el, n);
  const title = el.querySelector(".node__title");
  if (title.textContent !== (n.title || n.id)) title.textContent = n.title || n.id;
  const iframe = el.querySelector("iframe");
  // 只在正文真的变了时重载 iframe（位置/高度变化不该重跑文档）。
  // prev 必须传更新前的本地节点 —— 旧实现先 set 再取，prev===n，比较永远相等。
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

// 空态三件套：图标 + 标题（6–10 字）+ 说明句；可行动的空态带一个行动点。
// 容器 pointer-events:none（不挡画布），只有行动按钮单独打开事件。
function showHint(opts) {
  hideEmptyHint();
  const d = document.createElement("div");
  d.className = "empty-hint";
  d.innerHTML =
    `<span class="empty-hint__icon">${icon(opts.icon || "note", 32)}</span>` +
    `<b>${esc(opts.title)}</b>` +
    `<span>${opts.desc}</span>` +
    (opts.action ? `<button class="btn btn--primary empty-hint__act">${esc(opts.action)}</button>` : "");
  document.body.appendChild(d);
  const act = d.querySelector(".empty-hint__act");
  if (act && opts.onAction) act.addEventListener("click", opts.onAction);
}

function showEmptyHint() {
  if (!S.projects.length) {
    showHint({
      icon: "folder",
      title: "还没有项目",
      desc: "设计稿必须归属项目。新建一个项目后，左侧「设计体系」的预览就会落在它的画布上。<br>" +
        "Agent 侧对应 project_create / project_list 工具。",
      action: "新建项目",
      onAction: openProjectModal,
    });
    return;
  }
  if (isAllScope()) {
    showHint({
      icon: "grid",
      title: "项目里还没有设计稿",
      desc: "先在左侧选中一个项目，再从「设计体系」添加预览；<br>" +
        "也可以让 Agent 用 design_preview / design_screen 生成。",
      action: "选择一个项目",
      onAction: () => $("tree-rows").focus(),
    });
    return;
  }
  showHint({
    icon: "note",
    title: "「" + esc(projectName(S.project)) + "」还没有设计稿",
    desc: "从左侧设计体系添加预览，或让 Agent 用 design_preview 生成。",
    action: "查看设计体系",
    onAction: () => $("sys-collapse").getAttribute("aria-expanded") === "false"
      ? togglePanel("system") : $("sb-list").focus(),
  });
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
