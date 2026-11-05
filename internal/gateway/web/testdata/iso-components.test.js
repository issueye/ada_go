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

test('ada-ui-iso：Icon 通过 IconAdapter 输出与 Go 一致的内联 SVG', () => {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = globalThis.AdaRender.createApp({
    components: { Icon: UI.components.IconAdapter },
    render(h) {
      return h(UI.components.Icon, { name: 'i-list', size: 12 });
    },
  });
  app.mount(root);

  const svg = root.querySelector('svg.ada-icon');
  assert.ok(svg, '应输出 svg.ada-icon');
  assert.equal(svg.getAttribute('width'), '12');
  assert.equal(svg.getAttribute('height'), '12');
  assert.equal(svg.getAttribute('stroke-width'), '1.75');
  assert.equal(svg.querySelectorAll('path').length, 3);
});

test('ada-ui-iso：TodoList/Notice 通过 Icon 桥输出原生结构', () => {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = globalThis.AdaRender.createApp({
    components: { Icon: UI.components.IconAdapter },
    render(h) {
      return h('div', null, [
        h(UI.components.Notice, { tone: 'success' }, '已完成'),
        h(UI.components.TodoList, {
          notes: '备注',
          steps: [
            { step: '第一步', status: 'done' },
            { step: '第二步', status: 'in_progress' },
            { step: '第三步', status: 'pending' },
          ],
        }),
      ]);
    },
  });
  app.mount(root);

  const notice = root.querySelector('.ada-notice--success');
  assert.ok(notice, 'Notice 应输出 tone class');
  assert.equal(notice.textContent, '已完成');
  assert.ok(notice.querySelector('svg.ada-icon'));

  const wrap = root.querySelector('.ada-todo__list-wrap');
  assert.ok(wrap, 'TodoList 应输出 wrapper');
  assert.equal(root.querySelector('.ada-todo__notes').textContent, '备注：备注');
  assert.equal(root.querySelectorAll('.ada-todo__step').length, 3);
  assert.ok(root.querySelector('.ada-todo__step.is-done'));
  assert.ok(root.querySelector('.ada-todo__step.is-running'));
  assert.ok(root.querySelector('.ada-todo__step.is-pending'));
  assert.equal(root.querySelectorAll('.ada-todo__step svg.ada-icon').length, 3);
});

test('ada-ui-iso：Table 输出原生 table 与列定义', () => {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = globalThis.AdaRender.createApp({
    render(h) {
      return h(UI.components.Table, {
        caption: '模型列表',
        rowKey: 'id',
        columns: [
          { key: 'name', label: '名称' },
          { key: 'size', label: '大小', class: 'is-size' },
        ],
        rows: [
          { id: 'a', name: '甲', size: '1K' },
          { id: 'b', name: '乙', size: '2K' },
        ],
      });
    },
  });
  app.mount(root);

  assert.ok(root.querySelector('.ada-table-wrap table.table'));
  assert.equal(root.querySelector('caption').textContent, '模型列表');
  assert.equal(root.querySelectorAll('thead th').length, 2);
  assert.equal(root.querySelectorAll('tbody tr').length, 2);
  assert.equal(root.querySelector('tbody tr td').textContent, '甲');
  assert.ok(root.querySelector('td.is-size'));
});

test('ada-ui-iso：Select 通过 SelectAdapter 输出 ada-select 结构', () => {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = globalThis.AdaRender.createApp({
    components: { Select: UI.components.SelectAdapter },
    render(h) {
      return h(UI.components.Select, {
        options: ['a', { value: 'b', label: '乙' }],
        value: 'b',
        name: 'preset',
        class: 'cm-input',
        'aria-label': '选择预设',
      });
    },
  });
  app.mount(root);

  const picker = root.querySelector('ada-select.ada-select');
  assert.ok(picker, '应输出 ada-select');
  const trigger = picker.querySelector('button.ada-select__trigger.cm-input');
  assert.ok(trigger, '触发器应带自定义 class');
  assert.equal(trigger.getAttribute('aria-label'), '选择预设');
  assert.equal(picker.querySelector('[data-select-label]').textContent, '乙');
  assert.ok(picker.querySelector('.ada-select__option.is-selected[data-value="b"]'));
  const hidden = picker.querySelector('input[type="hidden"][data-select-input]');
  assert.equal(hidden.getAttribute('name'), 'preset');
  assert.equal(hidden.getAttribute('value'), 'b');
});

