/*
 * P2-5b SSE VNode patch 客户端探针：事件源帧 → applyVNodePatches →
 * 响应式列表 → keyed DOM patch。
 *
 * 运行：node --test internal/gateway/web/testdata/ada-patch-stream.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { createDocument } = require('./dom-harness.js');

global.document = createDocument();
globalThis.AdaRender = require('../static/ada-render.js');
require('../static/ada-patch-stream.js');

const Ada = globalThis.AdaRender;
const Stream = globalThis.AdaPatchStream;

function fakeSource() {
  return {
    handlers: Object.create(null),
    closed: false,
    addEventListener(name, fn) {
      this.handlers[name] = fn;
    },
    emit(name, payload) {
      if (this.handlers[name]) this.handlers[name]({ data: JSON.stringify(payload) });
    },
    close() {
      this.closed = true;
    },
  };
}

test('AdaPatchStream：SSE patch 帧驱动 keyed DOM 更新', async () => {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const source = fakeSource();
  const stream = Stream.create({
    element: root,
    source,
    hydrate: false,
    class: 'rows',
  });

  source.emit('patch', {
    patches: [
      { key: 'a', mode: 'upsert', vnode: Ada.h('div', { class: 'row a' }, 'A') },
      { key: 'b', mode: 'upsert', vnode: Ada.h('div', { class: 'row b' }, 'B') },
    ],
  });
  await Ada.nextTick();

  const aNode = root.querySelector('.a');
  const bNode = root.querySelector('.b');
  assert.equal(aNode.textContent, 'A');
  assert.equal(bNode.textContent, 'B');

  source.emit('patch', {
    patches: [{ key: 'b', mode: 'upsert', vnode: Ada.h('div', { class: 'row b' }, 'B2') }],
  });
  await Ada.nextTick();

  assert.equal(root.querySelector('.a'), aNode, '未变化行应复用');
  assert.equal(root.querySelector('.b'), bNode, '更新行应就地 patch');
  assert.equal(bNode.textContent, 'B2');

  source.emit('patch', { patches: [{ key: 'a', mode: 'remove' }] });
  await Ada.nextTick();
  assert.equal(root.querySelector('.a'), null);
  assert.equal(root.querySelector('.b'), bNode);

  stream.close();
  assert.equal(source.closed, true);
});
