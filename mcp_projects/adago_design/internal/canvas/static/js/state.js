/* adago-design 画布 · 共享状态与根元素（ES module）
 *
 * 唯一的「无依赖」模块：其他模块都从这里取 S / DOM 根 / 令牌读取，禁止反向依赖。
 * 模块在文档解析完成后执行，因此这里的 $() 与 DOM 构建是安全的。 */
"use strict";

// ---------------- 状态 ----------------
export const S = {
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

export const $ = (id) => document.getElementById(id);
export const viewport = $("viewport"), world = $("world"), grid = $("grid-layer");

export const edgeSvg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
edgeSvg.id = "edges";
edgeSvg.innerHTML = '<defs><marker id="arrow" class="edge-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z"/></marker></defs>';
world.appendChild(edgeSvg);

// 关系标注层（世界坐标系，跨卡片绘制；检查模式使用）
export const relLayer = document.createElementNS("http://www.w3.org/2000/svg", "svg");
relLayer.id = "rel-layer";
world.appendChild(relLayer);

// 画布纸张：项目版式边界（放在连线层之前，保证连线与节点都画在纸面上）
export const paperEl = document.createElement("div");
paperEl.id = "canvas-paper";
paperEl.className = "hidden";
paperEl.innerHTML = '<span class="cv-label"><b class="cv-size"></b><span class="cv-ratio"></span></span>';
world.insertBefore(paperEl, edgeSvg);

// ---------------- 设计令牌读取 ----------------
// 布局/颜色常量的唯一真源是 tokens.css。JS 只读，不复制数值。
export function tokenPx(name, fallback) {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  const n = parseFloat(v);
  return Number.isFinite(n) ? n : fallback;
}

export function tokenColor(name, fallback) {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

// 节点标题栏高度：唯一真源是 tokens.css 的 --ui-node-head-h。
export const HEAD_H = tokenPx("--ui-node-head-h", 32);

// 画布界面 tokens 的 z-index / 布局常量（样式在 <head> 里，模块执行即可读）
export const UI = {
  headH: HEAD_H,
  gutter: tokenPx("--ui-gutter", 12),
  dockW: tokenPx("--ui-dock-w", 280),
  inspectorW: tokenPx("--ui-inspector-w", 288),
  toolbarH: tokenPx("--ui-toolbar-h", 44),
  topSafe: tokenPx("--ui-top-safe", 56),
  soloBarH: tokenPx("--ui-solo-bar-h", 44),
};
