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

import { S, $, tokenColor, tokenPx, relLayer } from "./js/state.js";
import { toast, esc, isAskOpen } from "./js/ui.js";
import { api } from "./js/net.js";
import { icon } from "./js/icons-bridge.js";
import { on, EVT_FRAME_LOAD, EVT_THEME_CHANGE } from "./js/bus.js";
import { selectNode } from "./js/nodes.js";
import { isProjectModalOpen } from "./js/panels.js";
import {
  relationAxisAligned as relation, alignedPairs, rectToQuad, quadAABB, quadClosest,
  quadClip, relationQuads,
} from "./js/geometry.js";

export const INS = {
  on: false,
  anchor: null,   // {nodeId, el}
  target: null,
  hover: null,    // {nodeId, el}
  tempTarget: null, // hover 对比时的临时对象（不写入选择）
  raf: 0,
  needFull: false,
  catalog: {},    // systemId -> 体系详情（Token 合规标记用）
};

/* 标注配色：A/B 是「数据编码色」（同语法高亮的定位），不是品牌色，
 * 因此与八套体系的肤色无关；取值只从 tokens.css 的令牌读，本文件不写色值。
 * 取不到时留空（不可见）而不是回退到某个写死颜色——宁可看不见也不要与主题不符。 */
function insA() { return tokenColor("--ui-ins-a", ""); }
function insB() { return tokenColor("--ui-ins-b", ""); }
function onIns() { return tokenColor("--ui-on-ins", ""); }
function insInk() { return tokenColor("--ui-ink", ""); }
const LAYER_STYLE_ID = "dsx-style";

// ---------------- 开关 ----------------
function toggleInspect() {
  INS.on = !INS.on;
  const btn = $("btn-inspect");
  if (btn) {
    btn.classList.toggle("tb-active", INS.on);
    btn.setAttribute("aria-pressed", INS.on ? "true" : "false");
  }
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

// 卡片重载（srcdoc 变化）后重新挂载：nodes.js 统一派发该事件
on(EVT_FRAME_LOAD, (ev) => {
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
// 元素 → 世界坐标：节点 x/y + iframe 在卡片内的实测偏移 + 元素在 iframe 视口内的坐标。
// getBoundingClientRect 已经是「相对 iframe 视口」，滚动已在其中体现，不要再加 scrollX/Y。

// 元素到 iframe 视口的线性变换（不含平移）。网站预览里旋转通常来自元素自身或祖先。
function accumulatedLinear(el) {
  let a = 1, b = 0, c = 0, d = 1, any = false;
  for (let n = el; n && n.nodeType === 1; n = n.parentElement) {
    const t = getComputedStyle(n).transform;
    if (!t || t === "none") continue;
    let m;
    try { m = new DOMMatrixReadOnly(t); } catch (_) { return null; }
    // 父变换左乘：M = A(ancestor) × M
    const na = m.a * a + m.c * b;
    const nb = m.b * a + m.d * b;
    const nc = m.a * c + m.c * d;
    const nd = m.b * c + m.d * d;
    a = na; b = nb; c = nc; d = nd;
    any = true;
  }
  return any ? { a: a, b: b, c: c, d: d } : null;
}

function inViewport(el, doc) {
  const r = el.getBoundingClientRect();
  const vw = doc.documentElement.clientWidth, vh = doc.documentElement.clientHeight;
  return r.width > 0 && r.height > 0 && r.right > 0 && r.bottom > 0 && r.left < vw && r.top < vh;
}

// 量一个元素：AABB（用于面板数值/可视区判断）、未变换尺寸、真实四边形。
// 纯 rotate/scale 由「AABB 中心 + 累积线性变换」重建四边形（旋转/缩放下中心不变）；
// 存在 skew 或祖先裁剪导致预测 AABB 与实测不符时退回 AABB 并标记 approximate。
function measureLocal(el) {
  const r = el.getBoundingClientRect();
  const aabb = { x: r.left, y: r.top, w: r.width, h: r.height };
  const base = {
    aabb: aabb, w: el.offsetWidth || r.width, h: el.offsetHeight || r.height,
    quad: rectToQuad(aabb), rotated: false, approximate: false, theta: 0,
    visible: inViewport(el, el.ownerDocument),
  };
  if (!el.isConnected || (r.width === 0 && r.height === 0)) return base;
  const lin = accumulatedLinear(el);
  if (!lin) return base;
  const theta = Math.atan2(lin.b, lin.a);
  const skew = Math.abs(lin.a * lin.c + lin.b * lin.d) > 0.01;
  const uniform = Math.abs(Math.hypot(lin.a, lin.b) - Math.hypot(lin.c, lin.d)) < 0.02;
  if (skew || !uniform || Math.abs(theta) < 0.0005) return base;

  const cx = aabb.x + aabb.w / 2, cy = aabb.y + aabb.h / 2;
  const w2 = base.w / 2, h2 = base.h / 2;
  const q = [[-w2, -h2], [w2, -h2], [w2, h2], [-w2, h2]].map(([x, y]) => ({
    x: cx + lin.a * x + lin.c * y,
    y: cy + lin.b * x + lin.d * y,
  }));
  const predicted = quadAABB(q);
  if (Math.abs(predicted.w - aabb.w) > 1 || Math.abs(predicted.h - aabb.h) > 1) {
    return Object.assign({}, base, { rotated: true, approximate: true, theta: theta });
  }
  return Object.assign({}, base, { quad: q, rotated: true, theta: theta });
}

// iframe 在卡片内的偏移（含边框），除以画布缩放还原成世界坐标单位。
function frameOrigin(nodeId) {
  const n = S.nodes.get(nodeId);
  const host = S.els.get(nodeId);
  const iframe = host && host.querySelector("iframe");
  if (!n || !host || !iframe) return null;
  const hr = host.getBoundingClientRect();
  const ir = iframe.getBoundingClientRect();
  const k = S.scale || 1;
  return { x: n.x + (ir.left - hr.left) / k, y: n.y + (ir.top - hr.top) / k };
}

function worldMeasure(nodeId, el) {
  if (!el || !el.isConnected) return null;
  const m = measureLocal(el);
  const o = frameOrigin(nodeId);
  if (!o) return null;
  const shift = (p) => ({ x: p.x + o.x, y: p.y + o.y });
  return Object.assign({}, m, {
    quad: m.quad.map(shift),
    aabb: { x: m.aabb.x + o.x, y: m.aabb.y + o.y, w: m.aabb.w, h: m.aabb.h },
  });
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
    // 标注层样式所需的颜色一律从父页面令牌读（iframe 取不到父页面变量），
    // 因此换主题后重新挂载即可得到新配色（toggleInspect 里会清掉旧 layer 样式）。
    const A = insA(), B = insB(), onTint = onIns();
    // iframe 是独立文档，取不到父页面变量：几何与字号同样从令牌读出后内联。
    // z-index 保留大整数：标注层必须压过预览页自身的一切堆叠（含第三方组件库）。
    const bw = tokenPx("--ui-inspect-border-w", 1.5);
    const tx = tokenPx("--ui-text-xs", 11);
    const padY = tokenPx("--ui-space-1", 2);
    const padX = tokenPx("--ui-space-2", 4);
    const padX2 = tokenPx("--ui-space-3", 6);
    const rad = tokenPx("--ui-radius-sm", 4);
    const badge = tokenPx("--ui-space-8", 16);
    const labelW = tokenPx("--ui-inspect-label-max-w", 260);
    const lhBase = tokenPx("--ui-lh-base", 1.5);
    const font = tokenColor("--ui-font", "sans-serif");
    style.textContent = `
      .dsx-layer{position:fixed;inset:0;pointer-events:none;z-index:2147483000}
      .dsx-box{position:fixed;border:${bw}px solid;box-sizing:border-box}
      .dsx-box--hover{border-color:${B};border-style:dashed;
        background:color-mix(in srgb,${B} 5%,transparent)}
      .dsx-box--a{border-color:${A};background:color-mix(in srgb,${A} 7%,transparent)}
      .dsx-box--b{border-color:${B};background:color-mix(in srgb,${B} 7%,transparent)}
      .dsx-mask{position:fixed;background:color-mix(in srgb,${insInk()} 32%,transparent)}
      .dsx-line{position:fixed;background:${B}}
      .dsx-tick{position:fixed;background:${B}}
      .dsx-label{position:fixed;background:${B};color:${onTint};
        font:600 ${tx}px/1 ${font};
        padding:${padY}px ${padX}px;border-radius:${rad}px;white-space:nowrap;
        z-index:2147483001}
      .dsx-badge{position:fixed;width:${badge}px;height:${badge}px;
        border-radius:${rad}px;color:${onTint};
        font:700 ${tx}px/${badge}px ${font};text-align:center;
        z-index:2147483001}
      .dsx-name{position:fixed;max-width:${labelW}px;overflow:hidden;text-overflow:ellipsis;
        white-space:nowrap;background:${B};color:${onTint};
        font:600 ${tx}px/${lhBase} ${font};
        padding:${padY}px ${padX2}px;border-radius:${rad}px;
        z-index:2147483002;pointer-events:none}
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

// m 是 measureLocal 的返回值：轴对齐用 AABB；旋转时用「AABB 中心 + rotate(θ)」复现四边形
// （宽高取未变换尺寸，否则旋转后的 AABB 会把框放大 √2 倍）。
function box(doc, layer, m, cls, role) {
  const d = doc.createElement("div");
  d.className = "dsx-box " + cls;
  d.dataset.role = role;
  if (m.rotated) {
    const cx = m.aabb.x + m.aabb.w / 2, cy = m.aabb.y + m.aabb.h / 2;
    d.style.cssText = `left:${cx - m.w / 2}px;top:${cy - m.h / 2}px;` +
      `width:${m.w}px;height:${m.h}px;transform:rotate(${m.theta}rad);transform-origin:50% 50%`;
  } else {
    d.style.cssText = `left:${m.aabb.x}px;top:${m.aabb.y}px;` +
      `width:${m.aabb.w}px;height:${m.aabb.h}px`;
  }
  layer.appendChild(d);
  return d;
}

function badge(doc, layer, m, text, color, role) {
  const b = doc.createElement("div");
  b.className = "dsx-badge";
  b.dataset.role = role;
  b.textContent = text;
  b.style.cssText = `left:${m.aabb.x - 8}px;top:${m.aabb.y - 8}px;background:${color}`;
  layer.appendChild(b);
}

// 悬停：只动「上一帧悬停的那一张卡」的 hover 分区，绝不清其它帧、绝不动已选中的标注。
// 逐帧遍历所有 iframe 做 querySelectorAll 会让检查模式自己变卡，这里按变化增量处理。
let lastHover = { nodeId: null, el: null };

function nameChip(doc, layer, m, text, color, role) {
  const el = doc.createElement("div");
  el.className = "dsx-name";
  el.dataset.role = role;
  el.textContent = text;
  const x = m.aabb.x, y = m.aabb.y;
  el.style.cssText = `left:${Math.round(x)}px;top:${Math.round(y >= 20 ? y - 19 : y + 2)}px;background:${color}`;
  layer.appendChild(el);
  return el;
}

function elementLabel(el, prefix, m) {
  const meas = m || measureLocal(el);
  const text = (prefix ? prefix + " · " : "") + tagOf(el) + " · " +
    Math.round(meas.w) + "×" + Math.round(meas.h);
  return text.length > 46 ? text.slice(0, 45) + "…" : text;
}

// 悬停时的「最近兄弟间距」：与参考图的 35px 标注同义，最多两条，避免噪音。
// 双方轴对齐时给出方向性间距（横/竖）；任一方旋转时改用真实边界距离（dir="d"）。
function hoverGaps(doc, el, m) {
  const parent = el.parentElement;
  if (!parent || parent === doc.body) return [];
  const out = [];
  for (const sib of parent.children) {
    if (sib === el || sib.classList.contains("dsx-layer")) continue;
    if (sib.getClientRects().length === 0) continue; // 被滚动容器/overflow 完全裁掉
    const sm = measureLocal(sib);
    if (sm.aabb.w === 0 && sm.aabb.h === 0) continue;
    if (!m.rotated && !sm.rotated) {
      const L = m.aabb.x, T = m.aabb.y, R = L + m.aabb.w, B = T + m.aabb.h;
      const s = sm.aabb, sr = s.x + s.w, sb = s.y + s.h;
      const vOverlap = T < sb && s.y < B;
      const hOverlap = L < sr && s.x < R;
      if (vOverlap && sr <= L) out.push({ dir: "h", a: sr, b: L, c: T + m.aabb.h / 2, gap: L - sr });
      else if (vOverlap && s.x >= R) out.push({ dir: "h", a: R, b: s.x, c: T + m.aabb.h / 2, gap: s.x - R });
      else if (hOverlap && sb <= T) out.push({ dir: "v", a: sb, b: T, c: L + m.aabb.w / 2, gap: T - sb });
      else if (hOverlap && s.y >= B) out.push({ dir: "v", a: B, b: s.y, c: L + m.aabb.w / 2, gap: s.y - B });
    } else {
      const c = quadClosest(m.quad, sm.quad);
      if (c.dist < 1) continue;
      out.push({ dir: "d", p: c.p, q: c.q, gap: c.dist });
    }
  }
  out.sort((p, q) => p.gap - q.gap);
  return out.slice(0, 2);
}

function drawHoverGaps(doc, layer, el, m) {
  for (const g of hoverGaps(doc, el, m)) {
    if (g.gap < 1) continue;
    const line = doc.createElement("div");
    line.className = "dsx-line";
    line.dataset.role = "hover";
    if (g.dir === "h") {
      line.style.cssText = `left:${g.a}px;top:${g.c}px;width:${g.gap}px;height:1px`;
    } else if (g.dir === "v") {
      line.style.cssText = `left:${g.c}px;top:${g.a}px;width:1px;height:${g.gap}px`;
    } else {
      const ang = Math.atan2(g.q.y - g.p.y, g.q.x - g.p.x);
      line.style.cssText = `left:${g.p.x}px;top:${g.p.y}px;width:${g.gap}px;height:1px;` +
        `transform-origin:0 50%;transform:rotate(${ang}rad)`;
    }
    layer.appendChild(line);
    const label = doc.createElement("div");
    label.className = "dsx-label";
    label.dataset.role = "hover";
    if (g.dir === "h") {
      const mid = (g.a + g.b) / 2;
      label.style.cssText = `left:${Math.round(mid - 13)}px;top:${Math.round(g.c - 16)}px`;
    } else if (g.dir === "v") {
      const mid = (g.a + g.b) / 2;
      label.style.cssText = `left:${Math.round(g.c + 5)}px;top:${Math.round(mid - 7)}px`;
    } else {
      label.style.cssText = `left:${Math.round((g.p.x + g.q.x) / 2)}px;` +
        `top:${Math.round((g.p.y + g.q.y) / 2 - 8)}px`;
    }
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
  const m = measureLocal(el);
  if (!m.visible) return; // 已滚出可视区/被裁剪：不画标注，避免半截框
  box(doc, layer, m, "dsx-box--hover", "hover");
  nameChip(doc, layer, m, elementLabel(el, "", m), insB(), "hover");
  // 有锚点且正在对比时不再画「就近间距」，避免和 A↔B 的标注打架
  if (!INS.anchor) drawHoverGaps(doc, layer, el, m);
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
    [INS.anchor, "dsx-box--a", "A", insA()],
    [second, "dsx-box--b", INS.target ? "B" : "", insB()],
  ]) {
    if (!item || !item.el || !item.el.isConnected) continue;
    const host = S.els.get(item.nodeId);
    if (!host) continue;
    const iframe = host.querySelector("iframe");
    const doc = iframe && iframe.contentDocument;
    const layer = ensureLayer(doc);
    if (!layer) continue;
    const m = measureLocal(item.el);
    if (!m.visible) continue;
    box(doc, layer, m, cls, "sel");
    if (text) badge(doc, layer, m, text, color, "sel");
    nameChip(doc, layer, m, elementLabel(item.el, text || "B", m), color, "sel");
  }
}

// 关系计算（relation / alignedPairs）是纯函数，已移入 js/geometry.js 以便 node --test 直接测试。

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
    fill: onIns(), "font-weight": 600,
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

// 用多边形画轮廓：轴对齐时它退化成矩形，旋转时能跟上真实边界。
function svgOutline(parent, m, color, tag) {
  parent.appendChild(svg("polygon", {
    points: m.quad.map((p) => `${p.x},${p.y}`).join(" "), fill: "none",
    stroke: color, "stroke-width": 1, "stroke-dasharray": "4 3",
  }));
  if (tag) {
    const a = m.aabb;
    const b = svg("rect", { x: a.x - 7, y: a.y - 7, width: 14, height: 14, rx: 4, fill: color });
    const t = svg("text", {
      x: a.x, y: a.y - 1, "text-anchor": "middle", "dominant-baseline": "middle",
      "font-size": 11, "font-weight": 700, fill: onIns(),
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
  const A = worldMeasure(INS.anchor.nodeId, INS.anchor.el);
  const B = worldMeasure(second.nodeId, second.el);
  if (!A || !B) return;

  if (A.visible) svgOutline(relLayer, A, insA(), "A");
  if (B.visible) svgOutline(relLayer, B, insB(), "B");

  // 间距/对齐标注的胶囊底色：用正文墨色，白字/纸字对比都比彩色可读且不抢 A/B 的编码语义
  const ink = insInk();

  // 任一方旋转（或近似）时走四边形：真实边界距离 / 交集面积，不编造 ±1px 对齐。
  if (A.rotated || B.rotated) {
    if (!A.visible || !B.visible) {
      const mx = (A.aabb.x + B.aabb.x) / 2, my = (A.aabb.y + B.aabb.y) / 2;
      svgPill(relLayer, mx, my - 12, "一侧不在可视区", ink);
      return;
    }
    const qrel = relationQuads(A.quad, B.quad, A, B);
    if (qrel.intersects) {
      const inter = quadClip(B.quad, A.quad);
      if (inter.length >= 3) {
        relLayer.appendChild(svg("polygon", {
          points: inter.map((p) => `${p.x},${p.y}`).join(" "),
          fill: insB(), "fill-opacity": 0.12, stroke: insB(), "stroke-width": 1,
        }));
      }
      const label = qrel.containsA ? "A 包含 B"
        : qrel.containsB ? "B 包含 A"
          : "重叠 " + Math.round(qrel.overlapArea) + "px²";
      svgPill(relLayer, (A.aabb.x + A.aabb.w / 2 + B.aabb.x + B.aabb.w / 2) / 2,
        Math.min(A.aabb.y, B.aabb.y) - 12, label, insB());
    } else {
      const c = qrel.closest;
      svgLine(relLayer, c.p.x, c.p.y, c.q.x, c.q.y, ink, false);
      svgPill(relLayer, (c.p.x + c.q.x) / 2, (c.p.y + c.q.y) / 2 - 12,
        Math.round(c.dist) + "px", ink);
    }
    return;
  }

  const a = A.aabb, b = B.aabb;
  const rel = relation(a, b);

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
        fill: insB(), "fill-opacity": 0.12, stroke: insB(), "stroke-width": 1,
      }));
      const label = rel.contains ? "A 包含 B" : "重叠 " + Math.round(ox) + "×" + Math.round(oy);
      svgPill(relLayer, inter.x + inter.w / 2, inter.y - 12, label, insB());
    }
  }

  // 对齐参考线（最多两条，避免视觉噪音）
  const guides = alignedPairs(rel).slice(0, 2);
  for (const g of guides) {
    if (g.dir === "v") {
      const x = g.key === "left" ? a.x : g.key === "right" ? a.x + a.w : a.x + a.w / 2;
      const y1 = Math.min(a.y, b.y) - 6, y2 = Math.max(a.y + a.h, b.y + b.h) + 6;
      svgLine(relLayer, x, y1, x, y2, insA(), true);
      svgPill(relLayer, x, y1 - 8, g.label, insA());
    } else {
      const y = g.key === "top" ? a.y : g.key === "bottom" ? a.y + a.h : a.y + a.h / 2;
      const x1 = Math.min(a.x, b.x) - 6, x2 = Math.max(a.x + a.w, b.x + b.w) + 6;
      svgLine(relLayer, x1, y, x2, y, insA(), true);
      svgPill(relLayer, x2 + textWidth(g.label) / 2 + 8, y, g.label, insA());
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
  const m = measureLocal(el);
  const rect = m.aabb;
  const ab = artboardOf(item);
  const con = (v, kind) => tokenMark(v, kind, detail);
  const fmt = (v) => Math.max(0, Math.round(v));

  const pad = [cs.paddingTop, cs.paddingRight, cs.paddingBottom, cs.paddingLeft].map((v) => Math.round(parseFloat(v)));
  const brs = [cs.borderTopLeftRadius, cs.borderTopRightRadius, cs.borderBottomRightRadius, cs.borderBottomLeftRadius]
    .map((v) => Math.round(parseFloat(v)));
  const fs = Math.round(parseFloat(cs.fontSize));

  const rows = [];
  rows.push(["尺寸", `${Math.round(m.w)} × ${Math.round(m.h)} px` +
    (m.rotated ? `（旋转 ${Math.round((m.theta * 180) / Math.PI)}°${m.approximate ? "，近似" : ""}）` : "")]);
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
  const A = worldMeasure(anchorItem.nodeId, anchorItem.el);
  const B = worldMeasure(targetItem.nodeId, targetItem.el);
  if (!A || !B) return;
  const detail = await ensureCatalog(systemOf(anchorItem.nodeId));
  if (gen !== panelGen) return;
  const con = (v) => tokenMark(v, "spacing", detail);
  const row = (k, v) => `<div class="kv spec-row"><span class="chip">${esc(k)}</span><span class="spec-val">${v}</span></div>`;

  const head =
    `<div class="insp-head">${icon("link", 14)} 元素关联</div>` +
    `<div class="pair-row pair-row--a"><span class="pair-badge pair-badge--a">A</span>` +
    `<span class="spec-tag spec-tag--tight">${esc(tagOf(anchorItem.el))}</span>` +
    `<span class="pair-size">${Math.round(A.w)}×${Math.round(A.h)}</span></div>` +
    `<div class="pair-row pair-row--b"><span class="pair-badge pair-badge--b">B</span>` +
    `<span class="spec-tag spec-tag--tight">${esc(tagOf(targetItem.el))}</span>` +
    `<span class="pair-size">${Math.round(B.w)}×${Math.round(B.h)}</span></div>`;

  const rotated = A.rotated || B.rotated;
  if (!rotated) {
    const rel = relation(A.aabb, B.aabb);
    const gapX = Math.round(Math.abs(rel.gapX));
    const gapY = Math.round(Math.abs(rel.gapY));
    const guides = alignedPairs(rel).map((g) => g.label);
    const gapCell = (v, separated) => separated
      ? `${v}px ${con(v)}`
      : `<span class="muted">重叠/相接</span>`;
    body.innerHTML =
      head +
      `<div class="sec"><div class="sec__title">间距</div>` +
      row("水平", gapCell(gapX, rel.separatedX)) +
      row("垂直", gapCell(gapY, rel.separatedY)) +
      `</div>` +
      `<div class="sec"><div class="sec__title">关系</div>` +
      row("对齐", guides.length ? guides.join(" / ") : "无对齐") +
      row("尺寸差", `W ${rel.dw >= 0 ? "+" : ""}${Math.round(rel.dw)} / H ${rel.dh >= 0 ? "+" : ""}${Math.round(rel.dh)}`) +
      row("位移", `Δx ${Math.round(rel.dx)} / Δy ${Math.round(rel.dy)}`) +
      (rel.contains ? row("包含", "A 完全包含 B") : "") +
      `</div>` +
      `<div class="insp-hint">${INS.target ? "已固定 B · 单击第三个元素则锚点前移 · Esc 清除" : "移入其它元素继续对比 · 单击固定 B · Esc 清除"}</div>`;
    return;
  }

  // 旋转/混合：只报真实几何量，不把 AABB 当形状、不判定 ±1px 对齐。
  const qrel = relationQuads(A.quad, B.quad, A, B);
  const visNote = (!A.visible || !B.visible)
    ? row("可见性", '<span class="muted">有一侧已滚出可视区或被裁剪</span>') : "";
  body.innerHTML =
    head +
    `<div class="sec"><div class="sec__title">边界（旋转元素）</div>` +
    row("边界距离", qrel.intersects ? `<span class="muted">相交</span>` : `${Math.round(qrel.distance)}px`) +
    row("重叠面积", qrel.intersects ? `${Math.round(qrel.overlapArea)}px²` : "0") +
    (qrel.containsA ? row("包含", "A 完全包含 B") : qrel.containsB ? row("包含", "B 完全包含 A") : "") +
    visNote +
    `</div>` +
    `<div class="sec"><div class="sec__title">关系</div>` +
    row("对齐", '<span class="muted">旋转元素不判定对齐</span>') +
    row("尺寸差", `W ${qrel.dw >= 0 ? "+" : ""}${Math.round(qrel.dw)} / H ${qrel.dh >= 0 ? "+" : ""}${Math.round(qrel.dh)}`) +
    row("位移", `Δx ${Math.round(qrel.dx)} / Δy ${Math.round(qrel.dy)}`) +
    `</div>` +
    `<div class="insp-hint">${INS.target ? "已固定 B · Esc 清除" : "单击固定 B · Esc 清除"}</div>`;
}

// ---------------- 事件 ----------------
window.addEventListener("keydown", (e) => {
  // 「询问/确认」弹层打开时 Esc 归弹层（main.js 的捕获监听已处理），检查模式不抢
  if (!INS.on) return;
  if (isAskOpen()) return;
  if (isProjectModalOpen()) return;
  if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
  if (e.key === "Escape") {
    if (INS.target) { INS.target = null; scheduleDraw(true); e.preventDefault(); e.stopPropagation(); }
    else if (INS.anchor) { INS.anchor = null; scheduleDraw(true); e.preventDefault(); e.stopPropagation(); }
    else { toggleInspect(); e.preventDefault(); e.stopPropagation(); }
  } else if (e.key === "c" || e.key === "C") {
    clearPair();
  }
}, true);

// 换主题后标注层样式要从新令牌重建：iframe 里存的是旧颜色的 <style>，
// 只切父页面变量不会生效。这里清掉旧 layer 样式并在检查模式下重挂。
function refreshInspectTheme() {
  for (const [, el] of S.els) {
    const iframe = el.querySelector("iframe");
    const doc = iframe && iframe.contentDocument;
    if (!doc) continue;
    const style = doc.getElementById(LAYER_STYLE_ID);
    if (style) style.remove();
    const layer = doc.querySelector(".dsx-layer");
    if (layer) layer.remove();
    if (INS.on) attachInspect(iframe, el.dataset.id);
    iframe.__dsx = INS.on ? iframe.__dsx : null;
  }
  if (INS.on) scheduleDraw(true);
}

document.addEventListener("DOMContentLoaded", () => {
  const btn = $("btn-inspect");
  if (btn) btn.addEventListener("click", toggleInspect);
});

// main.js 的主题切换会派发该事件
on(EVT_THEME_CHANGE, refreshInspectTheme);

export { toggleInspect, clearPair, refreshInspectTheme };
