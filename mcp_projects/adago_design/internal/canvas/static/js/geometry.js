/* adago-design 画布 · 纯几何（ES module，无 DOM 依赖，可被 node --test 直接导入）
 *
 * 轴对齐关系计算的唯一实现；旋转元素走四边形（凸多边形）版本。
 * 设计原则：轴对齐路径与旧实现逐位一致（不改变既有数值），旋转路径才启用多边形运算。 */
"use strict";

// a、b 均为 {x, y, w, h}（轴对齐）。字段语义与旧 inspect.relation 逐位保持一致。
export function relationAxisAligned(a, b) {
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

// 对齐对（±1px 容差）。dir: "v" 画竖线，"h" 画横线。
export function alignedPairs(rel) {
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

// ---------------- 四边形 ----------------

// 左上 → 右上 → 右下 → 左下（屏幕坐标，y 向下）
export function rectToQuad(r) {
  return [
    { x: r.x, y: r.y },
    { x: r.x + r.w, y: r.y },
    { x: r.x + r.w, y: r.y + r.h },
    { x: r.x, y: r.y + r.h },
  ];
}

export function quadAABB(q) {
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const p of q) {
    if (p.x < x0) x0 = p.x;
    if (p.y < y0) y0 = p.y;
    if (p.x > x1) x1 = p.x;
    if (p.y > y1) y1 = p.y;
  }
  return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
}

export function quadCenter(q) {
  let x = 0, y = 0;
  for (const p of q) { x += p.x; y += p.y; }
  return { x: x / q.length, y: y / q.length };
}

// 边是否都水平/垂直（含 90° 旋转后的矩形）。
export function quadIsAxisAligned(q, tol) {
  const t = tol == null ? 0.5 : tol;
  for (let i = 0; i < q.length; i++) {
    const a = q[i], b = q[(i + 1) % q.length];
    const dx = Math.abs(b.x - a.x), dy = Math.abs(b.y - a.y);
    if (dx > t && dy > t) return false;
  }
  return true;
}

// 有符号面积（屏幕坐标下 TL→TR→BR→BL 为正值）
export function quadArea(q) {
  let s = 0;
  for (let i = 0; i < q.length; i++) {
    const a = q[i], b = q[(i + 1) % q.length];
    s += a.x * b.y - b.x * a.y;
  }
  return s / 2;
}

// ---------------- 点 / 线段 ----------------

function cross(ax, ay, bx, by) { return ax * by - ay * bx; }

function orient(a, b, c) {
  return cross(b.x - a.x, b.y - a.y, c.x - a.x, c.y - a.y);
}

function onSegment(a, b, p) {
  return Math.min(a.x, b.x) - 1e-9 <= p.x && p.x <= Math.max(a.x, b.x) + 1e-9 &&
    Math.min(a.y, b.y) - 1e-9 <= p.y && p.y <= Math.max(a.y, b.y) + 1e-9;
}

function segmentsIntersect(a, b, c, d) {
  const o1 = orient(a, b, c), o2 = orient(a, b, d);
  const o3 = orient(c, d, a), o4 = orient(c, d, b);
  if (((o1 > 0 && o2 < 0) || (o1 < 0 && o2 > 0)) &&
      ((o3 > 0 && o4 < 0) || (o3 < 0 && o4 > 0))) return true;
  if (Math.abs(o1) < 1e-9 && onSegment(a, b, c)) return true;
  if (Math.abs(o2) < 1e-9 && onSegment(a, b, d)) return true;
  if (Math.abs(o3) < 1e-9 && onSegment(c, d, a)) return true;
  if (Math.abs(o4) < 1e-9 && onSegment(c, d, b)) return true;
  return false;
}

function closestOnSegment(p, a, b) {
  const abx = b.x - a.x, aby = b.y - a.y;
  const den = abx * abx + aby * aby;
  if (den < 1e-12) return { x: a.x, y: a.y };
  let t = ((p.x - a.x) * abx + (p.y - a.y) * aby) / den;
  t = Math.max(0, Math.min(1, t));
  return { x: a.x + abx * t, y: a.y + aby * t };
}

function dist2(a, b) {
  const dx = a.x - b.x, dy = a.y - b.y;
  return dx * dx + dy * dy;
}

// 两条线段最近点对（相交时两点重合于交点）。
function segmentClosest(a, b, c, d) {
  if (segmentsIntersect(a, b, c, d)) {
    // 交点：用参数方程求解（非平行时唯一）
    const r = { x: b.x - a.x, y: b.y - a.y };
    const s = { x: d.x - c.x, y: d.y - c.y };
    const den = cross(r.x, r.y, s.x, s.y);
    let p = null;
    if (Math.abs(den) > 1e-12) {
      const t = cross(c.x - a.x, c.y - a.y, s.x, s.y) / den;
      p = { x: a.x + r.x * t, y: a.y + r.y * t };
    } else {
      p = closestOnSegment(a, c, d);
    }
    return { p: p, q: p, dist: 0 };
  }
  const pairs = [
    { p: a, q: closestOnSegment(a, c, d) },
    { p: b, q: closestOnSegment(b, c, d) },
    { p: c, q: closestOnSegment(c, a, b) },
    { p: d, q: closestOnSegment(d, a, b) },
  ];
  let best = pairs[0];
  for (const cand of pairs) if (dist2(cand.p, cand.q) < dist2(best.p, best.q)) best = cand;
  return { p: best.p, q: best.q, dist: Math.sqrt(dist2(best.p, best.q)) };
}

// 两凸四边形最近点与距离；相交返回 0。
export function quadClosest(a, b) {
  let best = null;
  for (let i = 0; i < a.length; i++) {
    const a1 = a[i], a2 = a[(i + 1) % a.length];
    for (let j = 0; j < b.length; j++) {
      const b1 = b[j], b2 = b[(j + 1) % b.length];
      const c = segmentClosest(a1, a2, b1, b2);
      if (!best || c.dist < best.dist) best = c;
      if (best.dist === 0) return best;
    }
  }
  return best || { p: quadCenter(a), q: quadCenter(b), dist: 0 };
}

export function quadDistance(a, b) {
  return quadClosest(a, b).dist;
}

// ---------------- 多边形包含 / 相交 ----------------

function pointInConvex(p, q) {
  const sign = Math.sign(quadArea(q)) || 1;
  for (let i = 0; i < q.length; i++) {
    if (orient(q[i], q[(i + 1) % q.length], p) * sign < -1e-9) return false;
  }
  return true;
}

export function quadContains(a, b) {
  return b.every((p) => pointInConvex(p, a));
}

// Sutherland–Hodgman：用凸多边形 clip 裁剪 subject。
export function quadClip(subject, clip) {
  // 退化多边形（零面积）没有内部，交集恒为空。
  if (Math.abs(quadArea(subject)) < 1e-9 || Math.abs(quadArea(clip)) < 1e-9) return [];
  const sign = Math.sign(quadArea(clip)) || 1;
  let out = subject;
  for (let i = 0; i < clip.length && out.length; i++) {
    const a = clip[i], b = clip[(i + 1) % clip.length];
    const input = out;
    out = [];
    for (let j = 0; j < input.length; j++) {
      const cur = input[j], prev = input[(j + input.length - 1) % input.length];
      const curIn = orient(a, b, cur) * sign >= -1e-9;
      const prevIn = orient(a, b, prev) * sign >= -1e-9;
      if (curIn) {
        if (!prevIn) out.push(lineIntersect(prev, cur, a, b));
        out.push(cur);
      } else if (prevIn) {
        out.push(lineIntersect(prev, cur, a, b));
      }
    }
  }
  return out;
}

function lineIntersect(p1, p2, p3, p4) {
  const r = { x: p2.x - p1.x, y: p2.y - p1.y };
  const s = { x: p4.x - p3.x, y: p4.y - p3.y };
  const den = cross(r.x, r.y, s.x, s.y);
  if (Math.abs(den) < 1e-12) return { x: p1.x, y: p1.y };
  const t = cross(p3.x - p1.x, p3.y - p1.y, s.x, s.y) / den;
  return { x: p1.x + r.x * t, y: p1.y + r.y * t };
}

export function quadIntersection(a, b) {
  return quadClip(b, a);
}

export function quadIntersectionArea(a, b) {
  return Math.abs(quadArea(quadIntersection(a, b)));
}

// ---------------- 关系（四边形） ----------------

// 供旋转/混合场景使用：数值均来自真实边界，不把 AABB 当形状。
export function relationQuads(qa, qb, sizeA, sizeB) {
  const ca = quadCenter(qa), cb = quadCenter(qb);
  const closest = quadClosest(qa, qb);
  const intersects = closest.dist === 0;
  return {
    distance: closest.dist,
    closest: closest,
    overlapArea: quadIntersectionArea(qa, qb),
    containsA: quadContains(qa, qb),
    containsB: quadContains(qb, qa),
    intersects: intersects,
    dw: (sizeB && sizeB.w != null ? sizeB.w : 0) - (sizeA && sizeA.w != null ? sizeA.w : 0),
    dh: (sizeB && sizeB.h != null ? sizeB.h : 0) - (sizeA && sizeA.h != null ? sizeA.h : 0),
    dx: cb.x - ca.x,
    dy: cb.y - ca.y,
  };
}
