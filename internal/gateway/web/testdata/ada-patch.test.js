/*
 * P2-5 keyed VNode patch 探针：服务端增量信封应用后，引擎必须按 key 复用
 * 已有 DOM，而不是整段重建。
 *
 * 运行：node --test internal/gateway/web/testdata/ada-patch.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { createDocument } = require('./dom-harness.js');

global.document = createDocument();

const Ada = require('../static/ada-render.js');

test('applyVNodePatches：upsert/remove 不修改入参', () => {
  const a = Ada.h('div', { class: 'a' }, 'A');
  const b = Ada.h('div', { class: 'b' }, 'B');
  const input = [{ key: 'a', vnode: a }];
  const out = Ada.applyVNodePatches(input, [
    { key: 'a', mode: 'upsert', vnode: a },
    { key: 'b', mode: 'upsert', vnode: b },
  ]);
  assert.equal(input.length, 1, '入参不应被修改');
  assert.equal(out.length, 2);
  assert.equal(out[1].key, 'b');
});

test('keyed patch：按 key 复用 DOM，文本就地更新', async () => {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);

  const List = {
    data() {
      return {
        items: [
          { key: 'a', vnode: Ada.h('div', { class: 'row a' }, 'A') },
          { key: 'b', vnode: Ada.h('div', { class: 'row b' }, 'B') },
        ],
      };
    },
    render(h) {
      return h('div', { class: 'list' }, this.items.map((item) => Ada.keyed(item.vnode, item.key)));
    },
  };

  const app = Ada.createApp(List);
  const proxy = app.mount(root);

  const list = root.querySelector('.list');
  const aNode = list.querySelector('.a');
  const bNode = list.querySelector('.b');
  assert.equal(aNode.textContent, 'A');
  assert.equal(bNode.textContent, 'B');

  proxy.items = Ada.applyVNodePatches(proxy.items, [
    { key: 'b', mode: 'upsert', vnode: Ada.h('div', { class: 'row b' }, 'B2') },
    { key: 'c', mode: 'upsert', vnode: Ada.h('div', { class: 'row c' }, 'C') },
  ]);
  await Ada.nextTick();

  assert.equal(root.querySelector('.a'), aNode, '未变化行应复用原节点');
  assert.equal(root.querySelector('.b'), bNode, '更新行应就地 patch');
  assert.equal(bNode.textContent, 'B2');
  assert.ok(root.querySelector('.c'));
  assert.equal(root.querySelectorAll('.row').length, 3);

  proxy.items = Ada.applyVNodePatches(proxy.items, [{ key: 'a', mode: 'remove' }]);
  await Ada.nextTick();

  assert.equal(root.querySelector('.a'), null, 'remove 应删除节点');
  assert.equal(root.querySelector('.b'), bNode, '删除其它行不应重建 b');
  assert.equal(root.querySelectorAll('.row').length, 2);
});
