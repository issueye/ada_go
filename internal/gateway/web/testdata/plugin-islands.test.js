/*
 * P1-5 客户端引导探针：
 * 扫描 [data-ada-plugin-island] → 动态 import data-ada-module → 读 props →
 * 从页面隐藏字段补 CSRF → mount(rootElement, { hydrate:true })。
 *
 * 运行：node --test internal/gateway/web/testdata/plugin-islands.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { createDocument } = require('./dom-harness.js');

global.document = createDocument();

const Ada = require('../static/ada-render.js');
const AdaUI = require('../static/ada-ui.js');

// 模拟浏览器全局：loader 只依赖 window.AdaRender / window.AdaUI。
global.window = {
  AdaRender: Ada,
  AdaUI,
  console,
};

require('../static/plugin-islands.js');

test('plugin-islands：动态加载模块、补 CSRF 并 hydrate 根元素', async () => {
  const moduleSource = `export default {
    props: { label: { default: "" } },
    data() { return { n: 1 }; },
    render(h) {
      const token = this.app && this.app.csrf ? this.app.csrf : "none";
      return h("div", { class: "loaded" }, this.label + ":" + token);
    }
  };`;
  const moduleURL = 'data:text/javascript,' + encodeURIComponent(moduleSource);

  const csrf = global.document.createElement('input');
  csrf.setAttribute('type', 'hidden');
  csrf.setAttribute('name', 'csrf_token');
  csrf.value = 'tok';
  global.document.body.appendChild(csrf);

  const root = global.document.createElement('div');
  root.setAttribute('class', 'loaded');
  root.setAttribute('data-ada-plugin-island', 'demo:slot:test');
  root.setAttribute('data-ada-plugin', 'demo');
  root.setAttribute('data-ada-module', moduleURL);
  root.setAttribute('data-ada-props', JSON.stringify({ app: { route: '/' }, label: 'x' }));
  root.textContent = 'x:tok';
  global.document.body.appendChild(root);

  global.window.AdaPluginIslands.boot();

  // 动态 import 是异步的；轮询到 mounted 或超时。
  for (let i = 0; i < 50 && root.getAttribute('data-ada-mounted') !== '1'; i += 1) {
    await new Promise((resolve) => setTimeout(resolve, 10));
  }

  assert.equal(root.getAttribute('data-ada-mounted'), '1', '岛屿应完成 hydrate');
  assert.equal(root.getAttribute('data-ada-error'), null, '不应进入错误态');
  assert.equal(root.textContent, 'x:tok', 'CSRF 应从页面隐藏字段补进 props');
});

test('plugin-islands：按 data-ada-entry 从 islands 映射选择组件', async () => {
  const moduleSource = `const Slot = {
    render(h) { return h("div", { class: "picked" }, "slot"); }
  };
  const Page = {
    render(h) { return h("div", { class: "default" }, "page"); }
  };
  export const islands = { "slot:sidebar.top": Slot };
  export default Page;`;
  const moduleURL = 'data:text/javascript,' + encodeURIComponent(moduleSource);

  const root = global.document.createElement('div');
  root.setAttribute('class', 'picked');
  root.setAttribute('data-ada-plugin-island', 'demo:slot:sidebar.top');
  root.setAttribute('data-ada-entry', 'slot:sidebar.top');
  root.setAttribute('data-ada-module', moduleURL);
  root.setAttribute('data-ada-props', '{}');
  root.textContent = 'slot';
  global.document.body.appendChild(root);

  global.window.AdaPluginIslands.boot();
  for (let i = 0; i < 50 && root.getAttribute('data-ada-mounted') !== '1'; i += 1) {
    await new Promise((resolve) => setTimeout(resolve, 10));
  }

  assert.equal(root.getAttribute('data-ada-mounted'), '1');
  assert.equal(root.getAttribute('data-ada-error'), null);
  assert.equal(root.textContent, 'slot', '应选择 islands 映射中的 slot，而不是 default page');
});
