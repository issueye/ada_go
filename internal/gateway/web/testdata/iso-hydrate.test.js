/*
 * P0 同构 hydrate 探针（docs/20）：
 * 同一份 render(h) 组件先经 renderToJSON 得到服务端 VNode，再把该 VNode 序列化成
 * 服务端 HTML；浏览器用同一份组件 hydrate 后必须复用节点，并且事件在客户端生效。
 *
 * 运行：node --test internal/gateway/web/testdata/iso-hydrate.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { createDocument } = require('./dom-harness.js');

global.document = createDocument();

const Ada = require('../static/ada-render.js');

const Card = {
  props: { label: { default: '' } },
  data() {
    return { n: 1 };
  },
  methods: {
    inc() {
      this.n += 1;
    },
  },
  render(h) {
    return h('div', { class: 'iso' }, [
      h('span', null, this.label + ':' + this.n),
      h('button', { type: 'button', onClick: this.inc }, 'add'),
    ]);
  },
};

// serialize 只服务这个探针：把简单 VNode 转成服务端等价 HTML。
// 生产环境的 HTML 由 Go 的 RenderHTML 负责，这里不实现第二套转义器。
function serialize(node, depth) {
  if ((depth || 0) > 16) throw new Error('VNode 过深');
  if (node.t === '#text') return String(node.v);
  const attrs = Object.keys(node.p || {})
    .sort()
    .map((key) => ' ' + key + '="' + String(node.p[key]).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;') + '"')
    .join('');
  const children = (node.c || []).map((child) => serialize(child, (depth || 0) + 1)).join('');
  return '<' + node.t + attrs + '>' + children + '</' + node.t + '>';
}

test('同构组件：renderToJSON → 服务端 HTML → hydrate 复用节点并绑定事件', async () => {
  const vnode = Ada.renderToJSON(Ada.h(Card, { label: 'a' }));
  assert.equal(vnode.t, 'div');
  assert.equal(vnode.p.onClick, undefined, '事件函数不应进入服务端 VNode');

  const host = global.document.createElement('div');
  global.document.body.appendChild(host);
  host.innerHTML = serialize(vnode);

  const serverRoot = host.firstChild;
  assert.equal(serverRoot.querySelector('span').textContent, 'a:1');

  const app = Ada.createApp({
    components: { Card },
    render(h) {
      return h(Card, { label: 'a' });
    },
  });
  app.mount(host, { hydrate: true });

  assert.equal(host.firstChild, serverRoot, 'hydrate 不应重建根节点');
  assert.equal(serverRoot.querySelector('span').textContent, 'a:1');

  serverRoot.querySelector('button').dispatchEvent({ type: 'click' });
  await Ada.nextTick();

  assert.equal(serverRoot.querySelector('span').textContent, 'a:2');
});

test('rootElement：直接 hydrate 页面里已经存在的岛屿根元素', async () => {
  const vnode = Ada.renderToJSON(Ada.h(Card, { label: 'b' }));
  const host = global.document.createElement('div');
  global.document.body.appendChild(host);
  host.innerHTML = serialize(vnode);

  const root = host.firstChild;
  root.setAttribute('data-ada-plugin-island', 'demo:slot:test');

  const app = Ada.createApp({
    components: { Card },
    render(h) {
      return h(Card, { label: 'b' });
    },
  });
  app.mount(root, { hydrate: true, rootElement: true });

  assert.equal(host.firstChild, root, 'rootElement hydrate 不应替换根元素');
  assert.equal(root.querySelector('span').textContent, 'b:1');

  root.querySelector('button').dispatchEvent({ type: 'click' });
  await Ada.nextTick();

  assert.equal(root.querySelector('span').textContent, 'b:2');
  assert.equal(host.childNodes.length, 1, '不应在根元素外新增节点');
});
