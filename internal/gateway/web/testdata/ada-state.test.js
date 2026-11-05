/*
 * P2-2 状态管理探针：provide/inject、watch、组件卸载自动停止、defineStore
 * 快照/恢复。运行：
 *   node --test internal/gateway/web/testdata/ada-state.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { createDocument } = require('./dom-harness.js');

global.document = createDocument();

const Ada = require('../static/ada-render.js');

function mount(component) {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = Ada.createApp({ components: { Comp: component }, render: (h) => h(component) });
  app.mount(root);
  return { app, root };
}

test('provide/inject：祖先提供、后代读取，app 级 provide 兜底', () => {
  const Child = {
    setup() {
      return { theme: Ada.inject('theme', 'default') };
    },
    render(h) {
      return h('span', { class: 'theme' }, this.theme);
    },
  };
  const Parent = {
    setup() {
      Ada.provide('theme', 'dark');
    },
    render(h) {
      return h('div', null, [h(Child)]);
    },
  };

  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = Ada.createApp({ components: { Parent, Child }, render: (h) => h(Parent) });
  app.provide('appOnly', 'base');
  app.mount(root);
  assert.equal(root.querySelector('.theme').textContent, 'dark');
});

test('watch：浅比较、旧值、stop 后不再触发', async () => {
  const n = Ada.ref(0);
  const calls = [];
  const stop = Ada.watch(n, (value, previous) => calls.push([value, previous]));

  n.value = 1;
  await Ada.nextTick();
  n.value = 2;
  await Ada.nextTick();
  assert.deepEqual(calls, [[1, 0], [2, 1]]);

  stop();
  n.value = 3;
  await Ada.nextTick();
  assert.deepEqual(calls, [[1, 0], [2, 1]]);
});

test('watch：组件卸载时自动停止', async () => {
  const n = Ada.ref(0);
  let calls = 0;
  const Comp = {
    setup() {
      Ada.watch(n, () => { calls += 1; });
    },
    render(h) {
      return h('div', null, 'watch');
    },
  };
  const { app } = mount(Comp);
  n.value = 1;
  await Ada.nextTick();
  assert.equal(calls, 1);

  app.unmount();
  n.value = 2;
  await Ada.nextTick();
  assert.equal(calls, 1, '卸载后 watch 不应继续触发');
});

test('defineStore：单例、getter、action、快照与恢复', () => {
  const useCounter = Ada.defineStore('counter', {
    state() {
      return { n: 1 };
    },
    getters: {
      double(state) {
        return state.n * 2;
      },
    },
    actions: {
      inc() {
        this.n += 1;
      },
    },
  });

  const store = useCounter();
  assert.equal(store.n, 1);
  assert.equal(store.double, 2);
  store.inc();
  assert.equal(store.n, 2);
  assert.equal(store.double, 4);
  assert.equal(useCounter(), store, '同一 store 定义应返回同一实例');

  assert.deepEqual(store.$snapshot(), { n: 2 });
  store.$hydrate({ n: 10 });
  assert.equal(store.n, 10);
  assert.deepEqual(store.$snapshot(), { n: 10 });
});

test('store 快照：hydrateStores 可在 store 创建前恢复', () => {
  const use = Ada.defineStore('hydrated-store', {
    state() {
      return { n: 1 };
    },
  });

  Ada.hydrateStores({ 'hydrated-store': { n: 9 } });
  const store = use();
  assert.equal(store.n, 9, '首次创建时应应用待处理快照');

  assert.deepEqual(Ada.collectStoreSnapshots()['hydrated-store'], { n: 9 });
});
