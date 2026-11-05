/*
 * P0-3 一致性测试骨架（docs/20）：
 * 同一份 VNode fixture：
 *   - Go 的 RenderHTML 产出 testdata/iso/parity.html（SSR golden）；
 *   - 浏览器运行时 mount 后，归一化结构必须与 golden 一致。
 *
 * 运行：node --test internal/gateway/web/testdata/iso-parity.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { createDocument } = require('./dom-harness.js');
const { snapshot, parseHTML, assertEquivalent } = require('./iso-parity.js');

global.document = createDocument();

const AdaUI = require('../static/ada-ui.js');

const fixtureDir = path.join(__dirname, '..', '..', 'frontend', 'testdata', 'iso');
const vnode = JSON.parse(fs.readFileSync(path.join(fixtureDir, 'parity.vnode.json'), 'utf8'));
const golden = fs.readFileSync(path.join(fixtureDir, 'parity.html'), 'utf8').trim();

test('VNode fixture：客户端 mount 与 Go SSR golden 结构一致', () => {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);

  const app = AdaUI.createApp({ render: () => vnode });
  app.mount(root);

  const expected = parseHTML(global.document, golden);
  assert.ok(expected, 'golden HTML 应能解析出根节点');

  assertEquivalent(snapshot(root.firstChild), snapshot(expected), 'parity');
  assert.equal(root.firstChild.querySelectorAll('button').length, 2);
  assert.equal(root.firstChild.querySelector('.iso-chip').textContent, '2/3');
});
