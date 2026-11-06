/* adago-design 元素检查模式
 *
 * 两层职责分离：
 *   1. 卡片内部：在各自 iframe 里画元素高亮与四边标注（坐标是该 iframe 的视口坐标）
 *   2. 卡片之间：在父页面 #rel-layer 里按「世界坐标」画 A↔B 的间距/对齐/尺寸差
 *
 * 为什么必须两层：iframe 内的事件与坐标系彼此隔离，跨卡片的连线只能由父页面画；
 * 世界坐标用「节点 x/y + 元素文档坐标」直接相加得到，与画布缩放/平移完全解耦。
 */
"use strict";

const INS = {
  on: false,
  anchor: null,   // {nodeId, el}
  target: null,
  hover: null,    // {nodeId, el}
  tempTarget: null, // hover 对比时的临时对象（不写入选择）
  raf: 0,
  needFull: false,
  catalog: {},    // systemId -> 体系详情（Token 合规标记用）
};

const ACC_A = "#165dff";  // anchor：标注蓝（对齐蓝湖式标注）
const ACC_B = "#fe2c55";  // target / hover：红
const LAYER_STYLE_ID = "dsx-style";

// ---------------- 开关 ----------------
function toggleInspect() {
  INS.on = !INS.on;
  const btn = $("btn-inspect");
  if (btn) btn.classList.toggle("tb-active", INS.on);
  document.body.classList.toggle("mode-inspect", INS.on);
  for (const [, el] of S.els) {
    const iframe = el.querySelector("iframe");
    if (!iframe) continue;
    if (INS.on) attachInspect(iframe, el.dataset.id);
    else detachInspect(iframe);
  }
  if (!INS.on) {
    INS.anchor = null; INS.target = null; INS.hover = null; INS.tempTarget = null;
    clearRelLayer();
    for (const [, el] of S.els) {
      const iframe = el.querySelector("iframe");
      if (iframe) clearLayerRoles(iframe.contentDocument, ["hover", "sel"]);
    }
    if (S.selected) selectNode(S.selected);
    toast("检查模式已关闭");
  } else {
    toast("检查模式：悬停即看规格与间距；单击定 A，移入另一个元素即对比（再单击固定 B，Esc 退出）");
  }
}

// ---------------- 挂载 / 卸载 ----------------
function attachInspect(iframe, nodeId) {
  const doc = iframe.contentDocument;
  if (!doc || !doc.body) return;              // load 事件会再次调用
  if (iframe.__dsx) return;                   // 已挂载
  const state = { doc: doc };
  state.move = (e) => {
    if (INS.on) setHover(nodeId, e.target);
  };
  state.leave = () => {
    if (INS.hover && INS.hover.nodeId === nodeId) { INS.hover = null; scheduleDraw(false); }
  };
  state.pick = (e) => {
    if (!INS.on) return;
    e.preventDefault();
    e.stopPropagation();
    pick(nodeId, e.target, e.shiftKey);
  };
  state.scroll = () => scheduleDraw(true);
  doc.addEventListener("pointermove", state.move, true);
  doc.addEventListener("pointerleave", state.leave, true);
  doc.addEventListener("pointerdown", state.pick, true);
  doc.addEventListener("scroll", state.scroll, true);
  iframe.__dsx = state;
}

function detachInspect(iframe) {
  const state = iframe.__dsx;
  if (!state) return;
  const doc = state.doc || iframe.contentDocument;
  if (doc) {
    doc.removeEventListener("pointermove", state.move, true);
    doc.removeEventListener("pointerleave", state.leave, true);
    doc.removeEventListener("pointerdown", state.pick, true);
    doc.removeEventListener("scroll", state.scroll, true);
    clearLayerRoles(doc, ["hover", "sel"]);
  }
  iframe.__dsx = null;
}

// 卡片重载（srcdoc 变化）后重新挂载：canvas.js 统一派发该事件
window.addEventListener("adago:frame-load", (ev) => {
  const { id, iframe } = ev.detail || {};
  if (!INS.on || !iframe) return;
  if (iframe.__dsx) detachInspect(iframe);
  attachInspect(iframe, id);
  // 该节点上的选择已随文档失效
  if (INS.anchor && INS.anchor.nodeId === id) INS.anchor = null;
  if (INS.target && INS.target.nodeId === id) INS.target = null;
  scheduleDraw(true);
});

// ---------------- 选中 ----------------
function setHover(nodeId, el) {
  const doc = el && el.ownerDocument;
  if (!el || !doc || el === doc.documentElement || el === doc.body) {
    if (INS.hover || INS.tempTarget) {
      INS.hover = null;
      if (!INS.target) INS.tempTarget = null;
      scheduleDraw(!!INS.anchor);
    }
    return;
  }
  if (INS.hover && INS.hover.el === el) return;
  INS.hover = { nodeId: nodeId, el: el };
  // 点选 + hover 对比：有锚点且尚未固定 B 时，hover 到谁就和谁比（不需要第二次点击）
  if (INS.anchor && INS.anchor.el !== el && !INS.target) {
    INS.tempTarget = { nodeId: nodeId, el: el };
  } else if (INS.target) {
    INS.tempTarget = null; // 已固定配对，hover 只更新信息，不改对比对象
  }
  scheduleDraw(true); // 面板也要跟着 hover 走（hover 即显示信息）
}

function pick(nodeId, el, shiftKey) {
  if (!el || el === el.ownerDocument.documentElement || el === el.ownerDocument.body) return;
  INS.tempTarget = null;
  const item = { nodeId: nodeId, el: el };

  if (!INS.anchor || (INS.anchor.nodeId === nodeId && INS.anchor.el === el)) {
    INS.anchor = item; INS.target = null;
  } else if (shiftKey || INS.target) {
    // Shift 强制替换 B；已有 A↔B 时再选 → 锚点前移（连续对比）
    if (INS.target) INS.anchor = INS.target;
    INS.target = item;
  } else {
    INS.target = item;
  }
  scheduleDraw(true);
}

function clearPair() {
  INS.anchor = null; INS.target = null; INS.tempTarget = null;
  scheduleDraw(true);
}

// ---------------- 坐标系 ----------------
// 元素 → 世界坐标：节点 x/y + 元素在 iframe 视口内的坐标。
// getBoundingClientRect 已经是「相对 iframe 视口」，滚动已在其中体现，不要再加 scrollX/Y。
function worldRect(nodeId, el) {
  const n = S.nodes.get(nodeId);
  const host = S.els.get(nodeId);
  if (!n || !host || !el) return null;
  const iframe = host.querySelector("iframe");
  if (!iframe) return null;
  const r = el.getBoundingClientRect();
  return { x: n.x + r.left, y: n.y + HEAD_H + r.top, w: r.width, h: r.height };
}

function localRect(el) {
  const r = el.getBoundingClientRect();
  return { x: r.left, y: r.top, w: r.width, h: r.height };
}

// ---------------- 绘制调度 ----------------
function scheduleDraw(full) {
  if (full) INS.needFull = true;
  if (INS.raf) return;
  INS.raf = requestAnimationFrame(() => {
    INS.raf = 0;
    const fullNow = INS.needFull;
    INS.needFull = false;
    renderHover();
    if (fullNow) { renderSelection(); renderRelations(); renderPanel(); }
  });
}

// ---------------- iframe 内标注层 ----------------
function ensureLayer(doc) {
  if (!doc || !doc.body) return null;
  let layer = doc.querySelector(".dsx-layer");
  if (layer) return layer;
  if (!doc.getElementById(LAYER_STYLE_ID)) {
    const style = doc.createElement("style");
    style.id = LAYER_STYLE_ID;
    style.textContent = `
      .dsx-layer{position:fixed;inset:0;pointer-events:none;z-index:2147483000}
      .dsx-box{position:fixed;border:1.5px solid;box-sizing:border-box}
      .dsx-box--hover{border-color:${ACC_B};border-style:dashed;background:rgba(254,44,85,.04)}
      .dsx-box--a{border-color:${ACC_A};background:rgba(22,93,255,.06)}
      .dsx-box--b{border-color:${ACC_B};background:rgba(254,44,85,.06)}
      .dsx-mask{position:fixed;background:rgba(22,24,35,.32)}
      .dsx-line{position:fixed;background:${ACC_B}}
      .dsx-tick{position:fixed;background:${ACC_B}}
      .dsx-label{position:fixed;background:${ACC_B};color:#fff;font:600 10px/1 "Inter","Noto Sans SC",system-ui,sans-serif;
        padding:2px 5px;border-radius:3px;white-space:nowrap;z-index:2147483001}
      .dsx-badge{position:fixed;width:16px;height:16px;border-radius:4px;color:#fff;
        font:700 10px/16px "Inter",system-ui,sans-serif;text-align:center;z-index:2147483001}
      .dsx-name{position:fixed;max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;
        background:#fe2c55;color:#fff;font:600 10px/1.5 "Inter","Noto Sans SC",system-ui,sans-serif;
        padding:1px 6px;border-radius:3px;z-index:2147483002;pointer-events:none}
    `;
    doc.head.appendChild(style);
  }
  layer = doc.createElement("div");
  layer.className = "dsx-layer";
  doc.body.appendChild(layer);
  return layer;
}

function clearLayerRoles(doc, roles) {
  if (!doc) return;
  const layer = doc.querySelector(".dsx-layer");
  if (!layer) return;
  for (const role of roles) {
    layer.querySelectorAll('[data-role="' + role + '"]').forEach((n) => n.remove());
  }
}

function box(doc, layer, rect, cls, role) {
  const d = doc.createElement("div");
  d.className = "dsx-box " + cls;
  d.dataset.role = role;
  d.style.cssText = `left:${rect.x}px;top:${rect.y}px;width:${rect.w}px;height:${rect.h}px`;
  layer.appendChild(d);
  return d;
}

function badge(doc, layer, rect, text, color, role) {
  const b = doc.createElement("div");
  b.className = "dsx-badge";
  b.dataset.role = role;
  b.textContent = text;
  b.style.cssText = `left:${rect.x - 8}px;top:${rect.y - 8}px;background:${color}`;
  layer.appendChild(b);
}

// 悬停：只动「上一帧悬停的那一张卡」的 hover 分区，绝不清其它帧、绝不动已选中的标注。
// 逐帧遍历所有 iframe 做 querySelectorAll 会让检查模式自己变卡，这里按变化增量处理。
let lastHover = { nodeId: null, el: null };

function nameChip(doc, layer, rect, text, color, role) {
  const el = doc.createElement("div");
  el.className = "dsx-name";
  el.dataset.role = role;
  el.textContent = text;
  el.style.cssText = `left:${Math.round(rect.x)}px;top:${Math.round(rect.y >= 20 ? rect.y - 19 : rect.y + 2)}px;background:${color}`;
  layer.appendChild(el);
  return el;
}

function elementLabel(el, prefix) {
  const r = el.getBoundingClientRect();
  const text = (prefix ? prefix + " · " : "") + tagOf(el) + " · " + Math.round(r.width) + "×" + Math.round(r.height);
  return text.length > 46 ? text.slice(0, 45) + "…" : text;
}

// 悬停时的「最近兄弟间距」：与参考图的 35px 标注同义，最多两条，避免噪音
// rect 是 localRect 的 {x,y,w,h}，不是 DOMRect —— 字段别混用，
// 否则比较全变成 undefined<undefined，间距标注会静默消失。
function hoverGaps(doc, el, rect) {
  const parent = el.parentElement;
  if (!parent || parent === doc.body) return [];
  const L = rect.x, T = rect.y, R = rect.x + rect.w, B = rect.y + rect.h;
  const out = [];
  for (const sib of parent.children) {
    if (sib === el || sib.classList.contains("dsx-layer")) continue;
    const s = sib.getBoundingClientRect(); // 兄弟节点直接读 DOMRect
    if (s.width === 0 && s.height === 0) continue;
    const vOverlap = T < s.bottom && s.top < B;
    const hOverlap = L < s.right && s.left < R;
    if (vOverlap && s.right <= L) out.push({ dir: "h", a: s.right, b: L, c: T + rect.h / 2, gap: L - s.right });
    else if (vOverlap && s.left >= R) out.push({ dir: "h", a: R, b: s.left, c: T + rect.h / 2, gap: s.left - R });
    else if (hOverlap && s.bottom <= T) out.push({ dir: "v", a: s.bottom, b: T, c: L + rect.w / 2, gap: T - s.bottom });
    else if (hOverlap && s.top >= B) out.push({ dir: "v", a: B, b: s.top, c: L + rect.w / 2, gap: B - s.top });
  }
  out.sort((p, q) => p.gap - q.gap);
  return out.slice(0, 2);
}

function drawHoverGaps(doc, layer, el, rect) {
  for (const g of hoverGaps(doc, el, rect)) {
    if (g.gap < 1) continue;
    const line = doc.createElement("div");
    line.className = "dsx-line";
    line.dataset.role = "hover";
    if (g.dir === "h") line.style.cssText = `left:${g.a}px;top:${g.c}px;width:${g.gap}px;height:1px`;
    else line.style.cssText = `left:${g.c}px;top:${g.a}px;width:1px;height:${g.gap}px`;
    layer.appendChild(line);
    const label = doc.createElement("div");
    label.className = "dsx-label";
    label.dataset.role = "hover";
    const mid = (g.a + g.b) / 2;
    if (g.dir === "h") label.style.cssText = `left:${Math.round(mid - 13)}px;top:${Math.round(g.c - 16)}px`;
    else label.style.cssText = `left:${Math.round(g.c + 5)}px;top:${Math.round(mid - 7)}px`;
    label.textContent = Math.round(g.gap) + "px";
    layer.appendChild(label);
  }
}

function renderHover() {
  const cur = INS.hover;
  const changed = lastHover.nodeId !== (cur ? cur.nodeId : null) || lastHover.el !== (cur ? cur.el : null);
  if (lastHover.nodeId && changed) clearHoverIn(lastHover.nodeId);
  lastHover = cur ? { nodeId: cur.nodeId, el: cur.el } : { nodeId: null, el: null };
  if (!cur || !changed) return;
  const el = cur.el;
  if (!el || !el.isConnected) return;
  if (INS.anchor && INS.anchor.el === el) return;
  const second = INS.target || INS.tempTarget;
  if (second && second.el === el) return;
  const host = S.els.get(cur.nodeId);
  const iframe = host && host.querySelector("iframe");
  if (!iframe) return;
  const doc = iframe.contentDocument;
  const layer = ensureLayer(doc);
  if (!layer) return;
  const rect = localRect(el);
  box(doc, layer, rect, "dsx-box--hover", "hover");
  nameChip(doc, layer, rect, elementLabel(el), ACC_B, "hover");
  // 有锚点且正在对比时不再画「就近间距」，避免和 A↔B 的标注打架
  if (!INS.anchor) drawHoverGaps(doc, layer, el, rect);
}

function clearHoverIn(nodeId) {
  const host = S.els.get(nodeId);
  const iframe = host && host.querySelector("iframe");
  if (iframe) clearLayerRoles(iframe.contentDocument, ["hover"]);
}

// 选中：A / B 各一框 + 角标（Alt 临时对照用 B 的样式但不打角标）
function renderSelection() {
  for (const [, host] of S.els) {
    const iframe = host.querySelector("iframe");
    if (!iframe) continue;
    clearLayerRoles(iframe.contentDocument, ["sel"]);
  }
  const second = INS.target || INS.tempTarget;
  for (const [item, cls, text, color] of [
    [INS.anchor, "dsx-box--a", "A", ACC_A],
    [second, "dsx-box--b", INS.target ? "B" : "", ACC_B],
  ]) {
    if (!item || !item.el || !item.el.isConnected) continue;
    const host = S.els.get(item.nodeId);
    if (!host) continue;
    const iframe = host.querySelector("iframe");
    const doc = iframe && iframe.contentDocument;
    const layer = ensureLayer(doc);
    if (!layer) continue;
    const rect = localRect(item.el);
    box(doc, layer, rect, cls, "sel");
    if (text) badge(doc, layer, rect, text, color, "sel");
    nameChip(doc, layer, rect, elementLabel(item.el, text || "B"), color, "sel");
  }
}

// ---------------- 关系计算（纯函数，便于测试） ----------------
function relation(a, b) {
  const gapX = b.x >= a.x + a.w ? b.x - (a.x + a.w) : (a.x >= b.x + b.w ? -(a.x - (b.x + b.w)) : 0);
  const gapY = b.y >= a.y + a.h ? b.y - (a.y + a.h) : (a.y >= b.y + b.h ? -(a.y - (b.y + b.h)) : 0);
  const dx = b.x - a.x, dy = b.y - a.y;
  return {
    gapX: gapX, gapY: gapY,
    separatedX: gapX !== 0,
    separatedY: gapY !== 0,
    overlapX: Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x),
    overlapY: Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y),
    dw: b.w - a.w, dh: b.h - a.h,
    dx: dx, dy: dy,
    align: {
      left: a.x - b.x, right: (a.x + a.w) - (b.x + b.w),
      top: a.y - b.y, bottom: (a.y + a.h) - (b.y + b.h),
      cx: (a.x + a.w / 2) - (b.x + b.w / 2), cy: (a.y + a.h / 2) - (b.y + b.h / 2),
    },
    contains: a.x <= b.x && a.y <= b.y && a.x + a.w >= b.x + b.w && a.y + a.h >= b.y + b.h,
  };
}

function alignedPairs(rel) {
  const names = [
    ["left", "左对齐", "v"], ["right", "右对齐", "v"],
    ["cx", "水平居中", "v"], ["top", "顶对齐", "h"],
    ["bottom", "底对齐", "h"], ["cy", "垂直居中", "h"],
  ];
  const out = [];
  for (const [key, label, dir] of names) {
    if (Math.abs(rel.align[key]) <= 1) out.push({ key: key, label: label, dir: dir });
  }
  return out;
}

// ---------------- 父页面关系层（世界坐标） ----------------
const SVGNS = "http://www.w3.org/2000/svg";

function clearRelLayer() {
  if (relLayer) relLayer.innerHTML = "";
}

function textWidth(s) {
  let w = 0;
  for (const ch of String(s)) w += /[\u3000-\u9fff\uff00-\uffef]/.test(ch) ? 11 : 6.4;
  return w;
}

function svg(tag, attrs) {
  const el = document.createElementNS(SVGNS, tag);
  for (const k in attrs) el.setAttribute(k, attrs[k]);
  return el;
}

function svgPill(parent, x, y, text, color) {
  const w = textWidth(text) + 12, h = 18;
  const rect = svg("rect", {
    x: x - w / 2, y: y - h / 2, width: w, height: h, rx: 4,
    fill: color, "fill-opacity": 0.94,
  });
  const label = svg("text", {
    x: x, y: y + 1, "text-anchor": "middle", "dominant-baseline": "middle",
    "font-size": 11, "font-family": 'Inter, "Noto Sans SC", system-ui, sans-serif',
    fill: "#fff", "font-weight": 600,
  });
  label.textContent = text;
  parent.appendChild(rect);
  parent.appendChild(label);
}

function svgLine(parent, x1, y1, x2, y2, color, dashed) {
  const l = svg("line", {
    x1: x1, y1: y1, x2: x2, y2: y2, stroke: color, "stroke-width": 1,
    "stroke-dasharray": dashed ? "3 3" : "none",
  });
  parent.appendChild(l);
  return l;
}

function svgOutline(parent, r, color, tag) {
  parent.appendChild(svg("rect", {
    x: r.x, y: r.y, width: r.w, height: r.h, fill: "none",
    stroke: color, "stroke-width": 1, "stroke-dasharray": "4 3",
  }));
  if (tag) {
    const b = svg("rect", { x: r.x - 7, y: r.y - 7, width: 14, height: 14, rx: 3, fill: color });
    const t = svg("text", {
      x: r.x, y: r.y - 1, "text-anchor": "middle", "dominant-baseline": "middle",
      "font-size": 9, "font-weight": 700, fill: "#fff",
      "font-family": "Inter, system-ui, sans-serif",
    });
    t.textContent = tag;
    parent.appendChild(b);
    parent.appendChild(t);
  }
}

function renderRelations() {
  clearRelLayer();
  const second = INS.target || INS.tempTarget;
  if (!relLayer || !INS.anchor || !second) return;
  const a = worldRect(INS.anchor.nodeId, INS.anchor.el);
  const b = worldRect(second.nodeId, second.el);
  if (!a || !b) return;

  svgOutline(relLayer, a, ACC_A, "A");
  svgOutline(relLayer, b, ACC_B, "B");

  const rel = relation(a, b);
  const ink = "#1d2129";

  // 水平间距：A/B 在水平方向分离且垂直投影有交集
  if (rel.separatedX) {
    const top = Math.max(a.y, b.y), bottom = Math.min(a.y + a.h, b.y + b.h);
    if (bottom > top) {
      const y = (top + bottom) / 2;
      const x1 = rel.gapX > 0 ? a.x + a.w : a.x;
      const x2 = rel.gapX > 0 ? b.x : b.x + b.w;
      svgLine(relLayer, x1, y, x2, y, ink, false);
      svgLine(relLayer, x1, y - 4, x1, y + 4, ink, false);
      svgLine(relLayer, x2, y - 4, x2, y + 4, ink, false);
      svgPill(relLayer, (x1 + x2) / 2, y - 12, Math.round(Math.abs(rel.gapX)) + "px", ink);
    }
  }

  // 垂直间距
  if (rel.separatedY) {
    const left = Math.max(a.x, b.x), right = Math.min(a.x + a.w, b.x + b.w);
    if (right > left) {
      const x = (left + right) / 2;
      const y1 = rel.gapY > 0 ? a.y + a.h : a.y;
      const y2 = rel.gapY > 0 ? b.y : b.y + b.h;
      svgLine(relLayer, x, y1, x, y2, ink, false);
      svgLine(relLayer, x - 4, y1, x + 4, y1, ink, false);
      svgLine(relLayer, x - 4, y2, x + 4, y2, ink, false);
      svgPill(relLayer, x + textWidth(Math.abs(Math.round(rel.gapY)) + "px") / 2 + 10, (y1 + y2) / 2,
        Math.round(Math.abs(rel.gapY)) + "px", ink);
    }
  }

  // 重叠 / 包含
  if (!rel.separatedX && !rel.separatedY) {
    const ox = rel.overlapX, oy = rel.overlapY;
    if (ox > 0 && oy > 0) {
      const inter = {
        x: Math.max(a.x, b.x), y: Math.max(a.y, b.y), w: ox, h: oy,
      };
      relLayer.appendChild(svg("rect", {
        x: inter.x, y: inter.y, width: inter.w, height: inter.h,
        fill: ACC_B, "fill-opacity": 0.12, stroke: ACC_B, "stroke-width": 1,
      }));
      const label = rel.contains ? "A 包含 B" : "重叠 " + Math.round(ox) + "×" + Math.round(oy);
      svgPill(relLayer, inter.x + inter.w / 2, inter.y - 12, label, ACC_B);
    }
  }

  // 对齐参考线（最多两条，避免视觉噪音）
  const guides = alignedPairs(rel).slice(0, 2);
  for (const g of guides) {
    if (g.dir === "v") {
      const x = g.key === "left" ? a.x : g.key === "right" ? a.x + a.w : a.x + a.w / 2;
      const y1 = Math.min(a.y, b.y) - 6, y2 = Math.max(a.y + a.h, b.y + b.h) + 6;
      svgLine(relLayer, x, y1, x, y2, ACC_A, true);
      svgPill(relLayer, x, y1 - 8, g.label, ACC_A);
    } else {
      const y = g.key === "top" ? a.y : g.key === "bottom" ? a.y + a.h : a.y + a.h / 2;
      const x1 = Math.min(a.x, b.x) - 6, x2 = Math.max(a.x + a.w, b.x + b.w) + 6;
      svgLine(relLayer, x1, y, x2, y, ACC_A, true);
      svgPill(relLayer, x2 + textWidth(g.label) / 2 + 8, y, g.label, ACC_A);
    }
  }
}

// ---------------- 面板 ----------------
function tagOf(el) {
  const cls = el.className && typeof el.className === "string" ? el.className.trim().split(/\s+/).join(".") : "";
  return el.tagName.toLowerCase() + (cls ? "." + cls : "");
}

// Token 合规标记：✓ 落在阶梯 / ✓≈ 2px 内近似 / ⚠ 偏离并给出规范值
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
  if (INS.catalog[systemID]) return INS.catalog[systemID];
  try {
    const d = await api("/api/catalog/systems/" + encodeURIComponent(systemID));
    INS.catalog[systemID] = d;
    return d;
  } catch (_) { return null; }
}

function systemOf(nodeId) {
  const n = S.nodes.get(nodeId);
  return n && n.system ? n.system : "";
}

function artboardOf(item) {
  const host = S.els.get(item.nodeId);
  const iframe = host && host.querySelector("iframe");
  const doc = iframe && iframe.contentDocument;
  if (!doc) return { id: "", w: 0, h: 0 };
  return {
    id: (doc.documentElement.dataset || {}).artboard || "",
    w: doc.documentElement.clientWidth,
    h: doc.documentElement.clientHeight,
  };
}

let panelGen = 0;

async function renderPanel() {
  const body = $("insp-body");
  const insp = $("inspector");
  if (!body || !insp) return;

  const second = INS.target || INS.tempTarget;
  const hoverOnly = INS.hover && (!INS.anchor || INS.anchor.el === INS.hover.el);
  if (!INS.anchor && !hoverOnly) {
    if (S.selected) selectNode(S.selected);
    return;
  }
  insp.classList.remove("hidden");
  const gen = ++panelGen;

  if (INS.anchor && second) return renderPairPanel(body, INS.anchor, second, gen);
  if (INS.anchor) return renderSinglePanel(body, INS.anchor, gen);
  return renderSinglePanel(body, INS.hover, gen);
}

async function renderSinglePanel(body, item, gen) {
  const el = item && item.el;
  if (!el || !el.isConnected) return;
  const detail = await ensureCatalog(systemOf(item.nodeId));
  if (gen !== panelGen) return; // 期间又 hover 到别的元素，丢弃这次结果
  const doc = el.ownerDocument;
  const cs = doc.defaultView.getComputedStyle(el);
  const rect = localRect(el);
  const ab = artboardOf(item);
  const con = (v, kind) => tokenMark(v, kind, detail);
  const fmt = (v) => Math.max(0, Math.round(v));

  const pad = [cs.paddingTop, cs.paddingRight, cs.paddingBottom, cs.paddingLeft].map((v) => Math.round(parseFloat(v)));
  const brs = [cs.borderTopLeftRadius, cs.borderTopRightRadius, cs.borderBottomRightRadius, cs.borderBottomLeftRadius]
    .map((v) => Math.round(parseFloat(v)));
  const fs = Math.round(parseFloat(cs.fontSize));

  const rows = [];
  rows.push(["尺寸", `${Math.round(rect.w)} × ${Math.round(rect.h)} px`]);
  rows.push(["到画板", `上${fmt(rect.y)} / 左${fmt(rect.x)} / 下${fmt(ab.h - rect.y - rect.h)} / 右${fmt(ab.w - rect.x - rect.w)}`]);
  rows.push(["宽度利用率", ab.w ? Math.round((rect.w / ab.w) * 100) + "%（画板 " + ab.w + "px）" : "-"]);
  rows.push(["内边距", `${pad[0]} ${pad[1]} ${pad[2]} ${pad[3]} ${con(pad[0], "spacing")}${con(pad[1], "spacing")}`]);
  rows.push(["边框", cs.borderTopWidth !== "0px" ? `${cs.borderTopWidth} ${cs.borderTopStyle} ${cs.borderTopColor}` : "无"]);
  rows.push(["圆角", brs.every((v) => v === brs[0]) ? `${brs[0]}px ${con(brs[0], "radius")}` : `${brs.join(" / ")} ${con(brs[0], "radius")}`]);
  rows.push(["阴影", cs.boxShadow !== "none" ? cs.boxShadow.slice(0, 60) + (cs.boxShadow.length > 60 ? "…" : "") : "无"]);
  rows.push(["背景", cs.backgroundColor]);
  rows.push(["字体", `${fs}px / ${cs.fontWeight} ${con(fs, "type")} ${cs.color}`]);

  body.innerHTML =
    `<div class="insp-head">${icon("inspect", 14)} 元素规格 · ${esc(ab.id || "无画板")}</div>` +
    `<div class="spec-tag">${esc(tagOf(el))}</div>` +
    rows.map(([k, v]) => `<div class="kv spec-row"><span class="chip">${esc(k)}</span><span class="spec-val">${v}</span></div>`).join("") +
    `<div class="insp-hint">移入其它元素即与它对比 · 单击固定 A</div>`;
}

async function renderPairPanel(body, anchorItem, targetItem, gen) {
  const a = worldRect(anchorItem.nodeId, anchorItem.el);
  const b = worldRect(targetItem.nodeId, targetItem.el);
  if (!a || !b) return;
  const rel = relation(a, b);
  const detail = await ensureCatalog(systemOf(anchorItem.nodeId));
  if (gen !== panelGen) return;
  const con = (v) => tokenMark(v, "spacing", detail);

  const gapX = Math.round(Math.abs(rel.gapX));
  const gapY = Math.round(Math.abs(rel.gapY));
  const guides = alignedPairs(rel).map((g) => g.label);

  const row = (k, v) => `<div class="kv spec-row"><span class="chip">${esc(k)}</span><span class="spec-val">${v}</span></div>`;

  const gapCell = (v, separated, axis) => {
    if (!separated) return `<span class="muted">${axis === "x" ? "重叠/相接" : "重叠/相接"}</span>`;
    return `${v}px ${con(v)}`;
  };

  body.innerHTML =
    `<div class="insp-head">${icon("link", 14)} 元素关联</div>` +
    `<div class="pair-row pair-row--a"><span class="pair-badge" style="background:${ACC_A}">A</span>` +
    `<span class="spec-tag spec-tag--tight">${esc(tagOf(anchorItem.el))}</span>` +
    `<span class="pair-size">${Math.round(a.w)}×${Math.round(a.h)}</span></div>` +
    `<div class="pair-row pair-row--b"><span class="pair-badge" style="background:${ACC_B}">B</span>` +
    `<span class="spec-tag spec-tag--tight">${esc(tagOf(targetItem.el))}</span>` +
    `<span class="pair-size">${Math.round(b.w)}×${Math.round(b.h)}</span></div>` +
    `<div class="sec"><div class="sec__title">间距</div>` +
    row("水平", gapCell(gapX, rel.separatedX, "x")) +
    row("垂直", gapCell(gapY, rel.separatedY, "y")) +
    `</div>` +
    `<div class="sec"><div class="sec__title">关系</div>` +
    row("对齐", guides.length ? guides.join(" / ") : "无对齐") +
    row("尺寸差", `W ${rel.dw >= 0 ? "+" : ""}${Math.round(rel.dw)} / H ${rel.dh >= 0 ? "+" : ""}${Math.round(rel.dh)}`) +
    row("位移", `Δx ${Math.round(rel.dx)} / Δy ${Math.round(rel.dy)}`) +
    (rel.contains ? row("包含", "A 完全包含 B") : "") +
    `</div>` +
    `<div class="insp-hint">${INS.target ? "已固定 B · 单击第三个元素则锚点前移 · Esc 清除" : "移入其它元素继续对比 · 单击固定 B · Esc 清除"}</div>`;
}

// ---------------- 事件 ----------------
window.addEventListener("keydown", (e) => {
  // 项目弹层打开时 Esc 归弹层（canvas.js 的捕获监听已处理），检查模式不抢
  if (!INS.on || (typeof isProjectModalOpen === "function" && isProjectModalOpen())) return;
  if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
  if (e.key === "Escape") {
    if (INS.target) { INS.target = null; scheduleDraw(true); e.preventDefault(); e.stopPropagation(); }
    else if (INS.anchor) { INS.anchor = null; scheduleDraw(true); e.preventDefault(); e.stopPropagation(); }
    else { toggleInspect(); e.preventDefault(); e.stopPropagation(); }
  } else if (e.key === "c" || e.key === "C") {
    clearPair();
  }
}, true);

document.addEventListener("DOMContentLoaded", () => {
  const btn = $("btn-inspect");
  if (btn) btn.addEventListener("click", toggleInspect);
});
