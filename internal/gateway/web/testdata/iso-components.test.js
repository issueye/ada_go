/*
 * P2-1 同构组件探针：ada-ui-iso.js 的 render(h) 定义在浏览器 mount 后
 * 应产出与设计一致的 class/DOM；同一份定义在服务端由 host.RenderIsland 执行
 * （Go 侧 TestRenderIslandWithIsoComponents 覆盖）。
 *
 * 运行：node --test internal/gateway/web/testdata/iso-components.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { createDocument } = require('./dom-harness.js');

global.document = createDocument();
globalThis.AdaRender = require('../static/ada-render.js');
require('../static/ada-ui-iso.js');

const UI = globalThis.AdaUIIso;

test('ada-ui-iso：Card/Badge/Button 渲染原生标签与既有 class', () => {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);

  const app = globalThis.AdaRender.createApp({
    render(h) {
      return h(UI.components.Card, { title: '同构卡片', subtitle: 'P2-1' }, [
        h(UI.components.Badge, { tone: 'brand', label: '3/5' }),
        h(UI.components.Button, { variant: 'primary' }, '保存'),
      ]);
    },
  });
  app.mount(root);

  const card = root.querySelector('.card');
  assert.ok(card, '应输出 section.card');
  assert.equal(card.querySelector('.section-title').textContent, '同构卡片');
  assert.equal(card.querySelector('.badge').textContent, '3/5');
  assert.ok(card.querySelector('.badge').getAttribute('class').includes('badge-brand'));
  const button = card.querySelector('button.ada-btn');
  assert.ok(button, '应输出 button.ada-btn');
  assert.ok(button.getAttribute('class').includes('ada-btn--primary'));
  assert.equal(button.textContent, '保存');
});

test('ada-ui-iso：Input 只在显式 id 时生成 label for', () => {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = globalThis.AdaRender.createApp({
    render(h) {
      return h(UI.components.Input, {
        id: 'demo-name',
        label: '名称',
        modelValue: 'Ada',
        hint: '用于显示',
      });
    },
  });
  app.mount(root);

  const label = root.querySelector('label.ada-field__label');
  const input = root.querySelector('input.ada-input');
  assert.equal(label.getAttribute('for'), 'demo-name');
  assert.equal(input.getAttribute('id'), 'demo-name');
  assert.equal(input.value, 'Ada');
  assert.equal(root.querySelector('.ada-field__hint').textContent, '用于显示');
});

test('ada-ui-iso：Textarea 复用 Input 的 render，默认多行', () => {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = globalThis.AdaRender.createApp({
    render(h) {
      return h(UI.components.Textarea, {
        id: 'demo-note',
        modelValue: 'hello',
        rows: 5,
      });
    },
  });
  app.mount(root);

  const area = root.querySelector('textarea.ada-input');
  assert.ok(area, 'Textarea 应输出 textarea.ada-input');
  assert.equal(area.getAttribute('rows'), '5');
  assert.equal(area.value, 'hello');
});

