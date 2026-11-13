// adago-design 画布 · 几何纯函数测试（node --test testdata/geometry.test.mjs）
// 由 internal/canvas/geometry_test.go 代理执行；node 不存在的环境下 Go 侧跳过。
import test from "node:test";
import assert from "node:assert/strict";

import {
  rectToQuad, quadAABB, quadArea, quadCenter, quadIsAxisAligned,
  quadDistance, quadClosest, quadContains, quadIntersection, quadIntersectionArea,
  relationAxisAligned, relationQuads,
} from "../static/js/geometry.js";

test("轴对齐关系与旧实现逐字段一致", () => {
  const rel = relationAxisAligned({ x: 0, y: 0, w: 100, h: 50 },
    { x: 120, y: 10, w: 50, h: 50 });
  assert.equal(rel.gapX, 20);
  assert.equal(rel.gapY, 0);
  assert.equal(rel.separatedX, true);
  assert.equal(rel.separatedY, false);
  assert.equal(rel.overlapX, -20);
  assert.equal(rel.overlapY, 40);
  assert.equal(rel.dw, -50);
  assert.equal(rel.dh, 0);
  assert.equal(rel.dx, 120);
  assert.equal(rel.dy, 10);
  assert.equal(rel.contains, false);
  assert.equal(rel.align.left, -120);
});

test("矩形 → 四边形 → AABB 可往返", () => {
  const q = rectToQuad({ x: 10, y: 20, w: 30, h: 40 });
  assert.deepEqual(quadAABB(q), { x: 10, y: 20, w: 30, h: 40 });
  assert.equal(quadArea(q), 1200);
  assert.deepEqual(quadCenter(q), { x: 25, y: 40 });
  assert.equal(quadIsAxisAligned(q), true);
});

test("45° 旋转：AABB 放大 √2，但仍能识别为旋转", () => {
  const c = { x: 50, y: 50 };
  // 宽高 10 的方块绕中心转 45° → AABB ≈ 14.14
  const w2 = 5, h2 = 5, th = Math.PI / 4;
  const cos = Math.cos(th), sin = Math.sin(th);
  const rq = [[-w2, -h2], [w2, -h2], [w2, h2], [-w2, h2]].map(([x, y]) => ({
    x: c.x + cos * x - sin * y,
    y: c.y + sin * x + cos * y,
  }));
  const rb = quadAABB(rq);
  assert.ok(Math.abs(rb.w - 10 * Math.SQRT2) < 1e-6);
  assert.equal(quadIsAxisAligned(rq), false);
});

test("90° 旋转仍是轴对齐矩形", () => {
  const q = [
    { x: 0, y: 0 }, { x: 0, y: 10 }, { x: 10, y: 10 }, { x: 10, y: 0 },
  ];
  assert.equal(quadIsAxisAligned(q), true);
  assert.equal(Math.abs(quadArea(q)), 100);
});

test("边界距离：分离 / 相邻 / 相交", () => {
  const a = rectToQuad({ x: 0, y: 0, w: 10, h: 10 });
  const far = rectToQuad({ x: 20, y: 0, w: 10, h: 10 });
  const touch = rectToQuad({ x: 10, y: 0, w: 10, h: 10 });
  const overlap = rectToQuad({ x: 5, y: 5, w: 10, h: 10 });

  assert.ok(Math.abs(quadDistance(a, far) - 10) < 1e-6);
  assert.ok(quadDistance(a, touch) < 1e-6);
  assert.equal(quadDistance(a, overlap), 0);

  const c = quadClosest(a, far);
  assert.ok(Math.abs(c.dist - 10) < 1e-6);
  assert.ok(Math.abs(c.p.x - c.q.x) < 1e-6 || Math.abs(c.p.y - c.q.y) < 1e-6);
});

test("包含与交集面积", () => {
  const outer = rectToQuad({ x: 0, y: 0, w: 100, h: 100 });
  const inner = rectToQuad({ x: 10, y: 10, w: 20, h: 20 });
  const part = rectToQuad({ x: 90, y: 90, w: 20, h: 20 });

  assert.equal(quadContains(outer, inner), true);
  assert.equal(quadContains(inner, outer), false);
  assert.equal(quadContains(outer, part), false);

  assert.equal(quadIntersectionArea(outer, inner), 400);
  assert.equal(quadIntersectionArea(outer, part), 100);
  assert.equal(quadIntersectionArea(inner, part), 0);
  assert.deepEqual(quadIntersection(inner, part), []);
});

test("relationQuads：相交/分离/包含的数值语义", () => {
  const a = rectToQuad({ x: 0, y: 0, w: 10, h: 10 });
  const b = rectToQuad({ x: 5, y: 5, w: 10, h: 10 });
  const r1 = relationQuads(a, b, { w: 10, h: 10 }, { w: 10, h: 10 });
  assert.equal(r1.intersects, true);
  assert.equal(r1.distance, 0);
  assert.equal(r1.overlapArea, 25);
  assert.equal(r1.dw, 0);
  assert.equal(r1.dx, 5);

  const far = rectToQuad({ x: 40, y: 0, w: 10, h: 10 });
  const r2 = relationQuads(a, far, { w: 10, h: 10 }, { w: 10, h: 10 });
  assert.equal(r2.intersects, false);
  assert.ok(Math.abs(r2.distance - 30) < 1e-6);
  assert.equal(r2.overlapArea, 0);

  const small = rectToQuad({ x: 2, y: 2, w: 4, h: 4 });
  const r3 = relationQuads(a, small, { w: 10, h: 10 }, { w: 4, h: 4 });
  assert.equal(r3.containsA, true);
  assert.equal(r3.containsB, false);
});

test("退化尺寸不抛异常", () => {
  const zero = rectToQuad({ x: 0, y: 0, w: 0, h: 0 });
  const other = rectToQuad({ x: 5, y: 5, w: 10, h: 10 });
  assert.doesNotThrow(() => quadDistance(zero, other));
  assert.equal(quadAABB(zero).w, 0);
  assert.equal(quadIntersectionArea(zero, other), 0);
});
