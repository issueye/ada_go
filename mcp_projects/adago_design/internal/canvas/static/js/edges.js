/* adago-design 画布 · 连线：元素池化 + 脏边增量 + rAF 合帧渲染。 */
"use strict";

import { S, edgeSvg } from "./state.js";
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

export { scheduleEdges, clearEdgeEls, flushEdges };
