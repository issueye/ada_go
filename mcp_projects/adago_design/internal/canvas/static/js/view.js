/* adago-design 画布 · 视图变换：平移/缩放/适配/居中、画布控制菜单、比例预设、可视区计算。 */
"use strict";

import { S, $, world, grid, paperEl, UI } from "./state.js";
import { icon } from "./icons-bridge.js";
import { esc, toast, setTip } from "./ui.js";
import { api } from "./net.js";
import { isAllScope, totalNodes, projectName, renderProjectTree, switchProject } from "./projects.js";
import { renderProjectRows, isProjectModalOpen, scheduleLabels, updateLabelScale, closeTreeMenu } from "./panels.js";
import { scheduleEdges } from "./edges.js";
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
  const gutter = leftDockWidth();
  const availW = innerWidth - (soloNode ? gutter + UI.inspectorW : gutter + UI.gutter * 2 + 48);
  const availH = innerHeight - (soloNode ? 150 : UI.topSafe + UI.gutter * 3);
  const cap = soloNode ? 1.6 : 1.2;
  const scale = Math.min(cap, Math.max(0.08, Math.min(availW / bw, availH / bh)));
  S.scale = scale;
  // 居中到「浮窗右侧、工具条下方、检查器左侧」的可见区
  const v = visibleRect();
  S.tx = v.x + v.w / 2 - (minX + maxX) / 2 * scale;
  S.ty = v.y + v.h / 2 - (minY + maxY) / 2 * scale;
  applyTransform();
  scheduleEdges("*");
}

// ---------------- 画布版式（选择 / 比例 / 居中） ----------------
// 画布 = 项目作用域 + 一份版式（宽高/比例）。版式是项目的属性，随项目走：
//   选择画布 = 切项目作用域；修改比例 = PUT /api/projects/{id}/canvas；
//   居中展示 = 把画布矩形（无版式时用全部节点包围盒）对齐到可视区中心。
// 纸张只作版式参考，不裁剪、不拦截事件：无限画布仍可把草稿摆到边界外。

function currentProject() {
  return S.project ? S.projects.find((p) => p.id === S.project) || null : null;
}

function canvasCaption(c) {
  if (!c || !c.width) return "自由画布";
  return c.width + "×" + c.height + " · " + (c.ratio || "custom");
}

// 从项目列表同步当前画布版式，并刷新纸张与工具条。
function syncCanvasFromScope() {
  const p = currentProject();
  S.canvas = p && p.canvas ? p.canvas : null;
  renderCanvas();
}

function renderCanvas() {
  if (S.canvas && S.canvas.width > 0) {
    paperEl.classList.remove("hidden");
    paperEl.style.width = S.canvas.width + "px";
    paperEl.style.height = S.canvas.height + "px";
    paperEl.querySelector(".cv-size").textContent = S.canvas.width + " × " + S.canvas.height;
    paperEl.querySelector(".cv-ratio").textContent = S.canvas.ratio || "custom";
  } else {
    paperEl.classList.add("hidden");
  }
  // 工具条只显示图标：当前画布 / 比例都写进 tooltip（data-tip）
  const p = currentProject();
  const scopeTip = isAllScope()
    ? "画布：全部项目 · 点击选择画布"
    : "画布：" + (p ? p.name : "未选择") + " · " +
      (S.canvas ? S.canvas.width + "×" + S.canvas.height + "（" + (S.canvas.ratio || "custom") + "）" : "自由画布") +
      " · 点击选择";
  setTip("cv-select", scopeTip);

  const ratioTip = !S.project
    ? "先选择一个画布（项目），再设置比例"
    : S.canvas
      ? "画布比例：" + (S.canvas.ratio || "自定义") + "（" + S.canvas.width + "×" + S.canvas.height + "） · 点击修改"
      : "画布比例：自由画布 · 点击修改";
  setTip("cv-ratio", ratioTip);
  const ratioBtn = $("cv-ratio");
  if (ratioBtn) {
    ratioBtn.classList.toggle("is-disabled", !S.project);
    ratioBtn.setAttribute("aria-disabled", S.project ? "false" : "true");
  }
}

// ---- 下拉菜单 ----
function renderCanvasMenu() {
  const box = $("cv-menu");
  if (!box) return;
  const rows = S.projects
    .map((p) => {
      const active = p.id === S.project;
      const meta = canvasCaption(p.canvas) + " · " + (p.node_count || 0) + " 稿";
      return `<button data-act="scope" data-id="${esc(p.id)}" class="${active ? "is-active" : ""}">
        ${icon("folder", 14)}<span class="tb-menu__name">${esc(p.name)}</span><span class="tb-menu__meta">${esc(meta)}</span></button>`;
    })
    .join("");
  box.innerHTML =
    `<div class="tb-menu__head">画布（项目作用域）</div>` +
    `<button data-act="scope" data-id="" class="${isAllScope() ? "is-active" : ""}">` +
    `${icon("grid", 14)}<span class="tb-menu__name">全部项目</span><span class="tb-menu__meta">${totalNodes()} 个稿</span></button>` +
    (rows || '<div class="tb-menu__empty">还没有项目。设计稿必须归属项目，先新建一个。</div>') +
    `<div class="tb-menu__sep"></div>` +
    `<button data-act="new">${icon("plus", 14)}<span>新建项目…</span></button>` +
    `<button data-act="manage">${icon("settings", 14)}<span>项目管理…</span></button>`;
}

function renderRatioMenu() {
  const box = $("ratio-menu");
  if (!box) return;
  const cur = S.canvas ? (S.canvas.ratio || "custom") : "free";
  const rows = S.presets
    .map((p) => {
      const active = cur === p.id;
      return `<button data-ratio="${esc(p.id)}" class="${active ? "is-active" : ""}">
        ${icon("fit", 14)}<span>${esc(p.name)}</span><span class="tb-menu__meta">${p.width}×${p.height}</span></button>`;
    })
    .join("");
  box.innerHTML =
    `<div class="tb-menu__head">画布比例</div>` +
    `<button data-ratio="free" class="${cur === "free" ? "is-active" : ""}">` +
    `${icon("grid", 14)}<span>自由画布</span><span class="tb-menu__meta">无边界</span></button>` +
    rows +
    `<div class="tb-menu__sep"></div>` +
    `<button data-ratio="custom" class="${cur === "custom" ? "is-active" : ""}">${icon("settings", 14)}<span>自定义宽高…</span></button>`;
}

function closeCanvasMenus() {
  for (const id of ["cv-menu", "ratio-menu"]) {
    const el = $(id);
    if (el) el.classList.add("hidden");
  }
  for (const id of ["cv-select", "cv-ratio"]) {
    const trigger = $(id);
    if (trigger) trigger.setAttribute("aria-expanded", "false");
  }
}

function toggleCanvasMenu(id) {
  const menu = $(id);
  if (!menu) return;
  const willOpen = menu.classList.contains("hidden");
  closeCanvasMenus();
  closeTreeMenu();
  if (!willOpen) return;
  if (id === "cv-menu") renderCanvasMenu();
  else renderRatioMenu();
  menu.classList.remove("hidden");
  const trigger = $(id === "cv-menu" ? "cv-select" : "cv-ratio");
  if (trigger) trigger.setAttribute("aria-expanded", "true");
}

async function loadCanvasPresets() {
  try {
    const data = await api("/api/canvas/presets");
    if (Array.isArray(data.presets) && data.presets.length) S.presets = data.presets;
    if (data.min_side) S.minSide = data.min_side;
    if (data.max_side) S.maxSide = data.max_side;
  } catch (_) {
    // 接口不可用时退回内置最小集，保证菜单仍可用（服务端仍会做最终校验）
    S.presets = [
      { id: "16:9", name: "宽屏 16:9", width: 1920, height: 1080 },
      { id: "4:3", name: "标准 4:3", width: 1440, height: 1080 },
      { id: "1:1", name: "方形 1:1", width: 1440, height: 1440 },
      { id: "9:16", name: "竖屏 9:16", width: 1080, height: 1920 },
    ];
  }
  renderRatioMenu();
}

function applyProjectPatch(p) {
  if (!p || !p.id) return;
  const i = S.projects.findIndex((x) => x.id === p.id);
  if (i >= 0) S.projects[i] = Object.assign({}, S.projects[i], p);
  else S.projects.push(p);
}

async function setCanvasConfig(payload, okText) {
  if (!S.project) {
    toast("画布是项目的属性：先在「全部项目」下拉里选一个项目", true);
    return false;
  }
  try {
    const data = await api("/api/projects/" + encodeURIComponent(S.project) + "/canvas", {
      method: "PUT", headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    applyProjectPatch(data.project);
    syncCanvasFromScope();
    renderProjectTree();
    if (isProjectModalOpen()) renderProjectRows();
    centerView(true);
    toast(okText || "画布已更新");
    return true;
  } catch (e) {
    toast("画布设置失败: " + e.message, true);
    return false;
  }
}

// ---- 居中 ----
// 目标矩形：单画布聚焦时是该节点；否则有版式用画布矩形，没有则用全部节点包围盒。
function canvasBounds() {
  const solo = S.solo && S.nodes.get(S.solo);
  if (solo) return { x: solo.x, y: solo.y, w: solo.width, h: solo.height, kind: "node" };
  if (S.canvas && S.canvas.width > 0) {
    return { x: 0, y: 0, w: S.canvas.width, h: S.canvas.height, kind: "canvas" };
  }
  const nodes = [...S.nodes.values()].filter((n) => n.kind !== "frame");
  if (!nodes.length) return null;
  let minX = 1e9, minY = 1e9, maxX = -1e9, maxY = -1e9;
  for (const n of nodes) {
    minX = Math.min(minX, n.x);
    minY = Math.min(minY, n.y);
    maxX = Math.max(maxX, n.x + n.width);
    maxY = Math.max(maxY, n.y + n.height);
  }
  return { x: minX, y: minY, w: maxX - minX, h: maxY - minY, kind: "nodes" };
}

// 可视区：扣掉左侧浮窗、右侧检查器、顶部工具条（聚焦时还有一条返回条）。
// 数值全部来自 tokens.css 的布局令牌，这里不再复制 292/288/64。
function leftDockWidth() {
  const dock = $("left-dock");
  if (!dock) return 0;
  const open = [...dock.querySelectorAll(".dock-panel")].some((p) => !p.classList.contains("is-collapsed"));
  return open ? UI.dockW + UI.gutter : 0;
}

function visibleRect() {
  const left = leftDockWidth();
  const inspEl = $("inspector");
  const right = inspEl && !inspEl.classList.contains("hidden") ? UI.inspectorW : 0;
  const top = UI.topSafe + UI.gutter + (S.solo ? UI.soloBarH + UI.gutter : 0);
  return {
    x: left, y: top,
    w: Math.max(160, innerWidth - left - right),
    h: Math.max(160, innerHeight - top - 12),
  };
}

// centerView(fit)：fit=true 时缩放到刚好放下，否则保持当前缩放（放不下才缩）。
function centerView(fit) {
  const b = canvasBounds();
  if (!b) {
    toast("画布上还没有内容");
    return;
  }
  const v = visibleRect();
  const pad = b.kind === "canvas" ? 72 : 48;
  const availW = Math.max(40, v.w - pad * 2);
  const availH = Math.max(40, v.h - pad * 2);
  let scale = S.scale;
  if (fit || b.w * scale > availW || b.h * scale > availH) {
    scale = Math.min(1, availW / b.w, availH / b.h);
  }
  scale = Math.min(4, Math.max(0.06, scale));
  S.scale = scale;
  S.tx = v.x + v.w / 2 - (b.x + b.w / 2) * scale;
  S.ty = v.y + v.h / 2 - (b.y + b.h / 2) * scale;
  applyTransform();
  scheduleEdges("*");
  scheduleLabels();
}

export {
  applyTransform, zoomAt, fitView, currentProject, canvasCaption, syncCanvasFromScope,
  renderCanvas, renderCanvasMenu, renderRatioMenu, closeCanvasMenus, toggleCanvasMenu,
  loadCanvasPresets, applyProjectPatch, setCanvasConfig, canvasBounds, leftDockWidth,
  visibleRect, centerView,
};
