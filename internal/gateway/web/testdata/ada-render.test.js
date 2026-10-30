/*
 * ada-render.js 的 Node 测试：用最小 DOM 夹具验证 VNode、响应式、patch、
 * 组件、slots、hydration 与 SSR 线格式。模板编译器依赖真实浏览器，放在
 * tools/render-demo/index.html 的冒烟用例里。
 *
 * 运行：node --test internal/gateway/web/testdata/ada-render.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { createDocument } = require('./dom-harness.js');

global.document = createDocument();
const Ada = require('../static/ada-render.js');
const {
  h, host, text, createApp, reactive, ref, computed, watchEffect, nextTick,
  renderToJSON, evaluate
} = Ada;

function domText(node) {
  if (!node) return '';
  if (node.nodeType === 3) return node.nodeValue;
  return node.childNodes.map(domText).join('');
}

function byClass(root, className) {
  return root.querySelector('.' + className);
}

function container() {
  return global.document.createElement('div');
}

/* ── VNode ──────────────────────────────────────────────────────────── */

test('h: 规范化子节点、文本与 key', () => {
  const vnode = h('div', { class: 'a', key: 'k1' }, 'x', null, false, ['y', 1]);
  assert.equal(vnode.t, 'div');
  assert.equal(vnode.p.class, 'a');
  assert.equal(vnode.p.key, undefined);
  assert.equal(vnode.k, 'k1');
  assert.deepEqual(vnode.c, [
    { t: '#text', v: 'x' },
    { t: '#text', v: 'y' },
    { t: '#text', v: '1' }
  ]);
});

test('h: 第二个参数可以是子节点', () => {
  const vnode = h('p', 'hello');
  assert.deepEqual(vnode.c, [{ t: '#text', v: 'hello' }]);
});

test('h: onClick 转成事件绑定，不进入 props', () => {
  const vnode = h('button', { onClick: () => {} }, 'go');
  assert.equal(vnode.p.onClick, undefined);
  assert.equal(vnode._e.length, 1);
  assert.equal(vnode._e[0].name, 'click');
});

/* ── 表达式解释器 ───────────────────────────────────────────────────── */

test('evaluate: 路径、运算、三元与对象字面量', () => {
  assert.equal(evaluate('a + b * 2', { a: 1, b: 3 }), 7);
  assert.equal(evaluate('user.name.toUpperCase()', { user: { name: 'ada' } }), 'ADA');
  assert.equal(evaluate('items[1].id', { items: [{ id: 'a' }, { id: 'b' }] }), 'b');
  assert.equal(evaluate('ok ? "yes" : "no"', { ok: false }), 'no');
  assert.deepEqual(evaluate('{ active: ok, n: count }', { ok: true, count: 2 }), { active: true, n: 2 });
  assert.equal(evaluate('missing ?? "fallback"', {}), 'fallback');
  assert.equal(evaluate('Math.max(2, 5)', {}), 5);
});

test('evaluate: 拒绝 constructor 等逃逸路径', () => {
  assert.throws(
    () => evaluate('x.constructor.constructor("return 1")()', { x: {} }),
    /禁止访问属性 constructor/
  );
  assert.throws(
    () => evaluate('x.__proto__', { x: {} }),
    /禁止访问属性 __proto__/
  );
});

/* ── 响应式 ─────────────────────────────────────────────────────────── */

test('reactive/computed/watchEffect 在微任务里合并更新', async () => {
  const state = reactive({ count: 0, nested: { n: 1 } });
  const double = computed(() => state.count * 2);
  const seen = [];
  watchEffect(() => seen.push(double.value + state.nested.n));
  assert.deepEqual(seen, [1]);

  state.count = 2;
  state.count = 3;
  await nextTick();
  assert.deepEqual(seen, [1, 7]);

  state.nested.n = 2;
  await nextTick();
  assert.deepEqual(seen, [1, 7, 8]);
});

test('ref 在 reactive 对象里自动解包', async () => {
  const count = ref(0);
  const state = reactive({ count });
  const seen = [];
  watchEffect(() => seen.push(state.count));
  state.count = 5;
  await nextTick();
  assert.deepEqual(seen, [0, 5]);
  count.value = 6;
  await nextTick();
  assert.deepEqual(seen, [0, 5, 6]);
});

/* ── 挂载、更新、事件 ───────────────────────────────────────────────── */

test('mount：渲染状态、点击更新并复用 DOM', async () => {
  const app = createApp({
    data() { return { count: 0 }; },
    render(h) {
      return h('div', { class: 'box' },
        h('span', { class: 'count' }, String(this.count)),
        h('button', { class: 'inc', type: 'button', onClick: () => { this.count += 1; } }, '+'));
    }
  });
  const root = container();
  app.mount(root);
  assert.equal(domText(root), '0+');

  const span = byClass(root, 'count');
  const button = byClass(root, 'inc');
  button.dispatchEvent({ type: 'click' });
  await nextTick();

  assert.equal(domText(root), '1+');
  assert.equal(byClass(root, 'count'), span);
  assert.equal(byClass(root, 'inc'), button);
});

test('patch：keyed 列表重排复用节点', async () => {
  const items = reactive([
    { id: 'a', label: 'A' },
    { id: 'b', label: 'B' },
    { id: 'c', label: 'C' }
  ]);
  const app = createApp({
    data() { return { items }; },
    render(h) {
      return h('ul', null, this.items.map((item) => h('li', { key: item.id }, item.label)));
    }
  });
  const root = container();
  app.mount(root);
  const ul = root.firstChild;
  const [a, b, c] = ul.childNodes;
  assert.equal(domText(ul), 'ABC');

  items.reverse();
  await nextTick();
  assert.equal(domText(ul), 'CBA');
  assert.equal(ul.childNodes[0], c);
  assert.equal(ul.childNodes[1], b);
  assert.equal(ul.childNodes[2], a);
});

/* ── 组件、props、emit、slots ───────────────────────────────────────── */

test('component：props 更新、$emit 与默认 slot', async () => {
  const Child = {
    props: ['label'],
    render(h) {
      return h('button', {
        class: 'child',
        type: 'button',
        onClick: () => this.$emit('pick', this.label)
      }, this.$slots.default());
    }
  };
  const picked = [];
  const app = createApp({
    components: { Child },
    data() { return { label: 'A' }; },
    render(h) {
      return h('section', null,
        h(Child, { label: this.label, onPick: (value) => picked.push(value) },
          h('em', null, 'slot')));
    }
  });
  const root = container();
  app.mount(root);
  assert.equal(domText(root), 'slot');

  const button = byClass(root, 'child');
  button.dispatchEvent({ type: 'click' });
  assert.deepEqual(picked, ['A']);

  app._instance.data.label = 'B';
  await nextTick();
  assert.equal(domText(root), 'slot');
  button.dispatchEvent({ type: 'click' });
  assert.deepEqual(picked, ['A', 'B']);
});

test('component：空 default slot 回退到模板 fallback', () => {
  const Box = {
    template: '<span><slot>fallback</slot></span>'
  };
  const empty = createApp({ components: { Box }, template: '<div><box></box></div>' });
  const emptyRoot = container();
  empty.mount(emptyRoot);
  assert.equal(domText(emptyRoot), 'fallback');

  const filled = createApp({ components: { Box }, template: '<div><box>body</box></div>' });
  const filledRoot = container();
  filled.mount(filledRoot);
  assert.equal(domText(filledRoot), 'body');
});

test('component：setup 返回渲染函数与 ref 自动解包', () => {
  const Counter = {
    setup() {
      const count = ref(2);
      return (h) => h('strong', null, String(count.value));
    }
  };
  const app = createApp({ components: { Counter }, render: (h) => h(Counter) });
  const root = container();
  app.mount(root);
  assert.equal(domText(root), '2');
});

test('component：$C 字符串按 app 注册表解析', () => {
  const Icon = { render: (h) => h('svg', { class: 'icon' }) };
  const app = createApp({
    components: { Icon },
    render: (h) => h('div', null, { t: '$C', n: 'Icon', p: {}, c: [] })
  });
  const root = container();
  app.mount(root);
  assert.equal(root.firstChild.firstChild.tagName, 'SVG');
  assert.equal(root.firstChild.firstChild.getAttribute('class'), 'icon');
});

/* ── SSR 线格式 ─────────────────────────────────────────────────────── */

test('renderToJSON：展开组件、丢弃事件与内部字段', () => {
  const Card = {
    props: ['name'],
    render(h) {
      return h('div', { class: 'card', 'data-role': 'card' },
        h('span', null, this.name),
        h('button', { class: 'go', onClick: () => {} }, 'open'));
    }
  };
  const json = renderToJSON(Card, { name: 'Ada' });
  assert.deepEqual(json, {
    t: 'div',
    p: { class: 'card', 'data-role': 'card' },
    c: [
      { t: 'span', p: {}, c: [{ t: '#text', v: 'Ada' }] },
      { t: 'button', p: { class: 'go' }, c: [{ t: '#text', v: 'open' }] }
    ]
  });
});

test('renderToJSON：host 组件保持 $C 形态', () => {
  const json = renderToJSON({
    render: (h) => h('div', null, host('Icon', { name: 'i-list', size: 12 }))
  });
  assert.deepEqual(json, {
    t: 'div',
    p: {},
    c: [{ t: '$C', n: 'Icon', p: { name: 'i-list', size: 12 }, c: [] }]
  });
});

test('renderToJSON：注册过的字符串组件会展开', () => {
  const Card = { render: (h) => h('div', { class: 'card' }, 'x') };
  const json = renderToJSON(
    { t: '$C', n: 'Card', p: {}, c: [] },
    undefined,
    { components: { Card } }
  );
  assert.deepEqual(json, {
    t: 'div',
    p: { class: 'card' },
    c: [{ t: '#text', v: 'x' }]
  });
});

test('renderToJSON：v-html 内容拒绝进入 SSR 线格式', () => {
  const Widget = { render: (h) => h('div', { innerHTML: '<b>x</b>' }) };
  // innerHTML 不是 v-html；这里模拟引擎的 _html 字段。
  const vnode = h('div');
  vnode._html = '<b>x</b>';
  assert.throws(() => renderToJSON(vnode), /v-html/);
  assert.ok(Widget);
});

/* ── hydration ──────────────────────────────────────────────────────── */

test('hydrate：复用服务端节点并在更新时就地 patch', async () => {
  const app = createApp({
    data() { return { message: 'hello' }; },
    render(h) {
      return h('div', { class: 'root' }, h('span', { class: 'msg' }, this.message));
    }
  });
  const root = container();
  const serverDiv = global.document.createElement('div');
  serverDiv.setAttribute('class', 'root');
  const serverSpan = global.document.createElement('span');
  serverSpan.setAttribute('class', 'msg');
  serverSpan.appendChild(global.document.createTextNode('hello'));
  serverDiv.appendChild(serverSpan);
  root.appendChild(serverDiv);

  app.mount(root, { hydrate: true });
  assert.equal(root.firstChild, serverDiv);
  assert.equal(serverDiv.firstChild, serverSpan);
  assert.equal(domText(root), 'hello');

  app._instance.data.message = 'bye';
  await nextTick();
  assert.equal(domText(root), 'bye');
  assert.equal(serverDiv.firstChild, serverSpan);
});

test('unmount：停止更新并移除 DOM', async () => {
  const app = createApp({
    data() { return { n: 0 }; },
    render(h) { return h('div', null, String(this.n)); }
  });
  const root = container();
  app.mount(root);
  app.unmount();
  assert.equal(root.childNodes.length, 0);
  app._instance = null; // 防止测试尾部访问
});
