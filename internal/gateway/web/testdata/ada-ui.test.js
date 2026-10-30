/*
 * ada-ui.js 的 Node 测试：组件注册、模板渲染、v-model、事件与关闭语义。
 * 使用 testdata/dom-harness.js 的最小 DOM/HTML 解析器；真实浏览器冒烟见
 * tools/render-demo/ui.html。
 *
 * 运行：node --test internal/gateway/web/testdata/ada-ui.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { createDocument } = require('./dom-harness.js');
global.document = createDocument();

const Ada = require('../static/ada-render.js');
const UI = require('../static/ada-ui.js');

function mount(options) {
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = UI.createApp(options);
  app.mount(root);
  return { app, root };
}

function text(node) {
  return node ? node.textContent : '';
}

function q(root, selector) {
  return root.querySelector(selector);
}

function qa(root, selector) {
  return root.querySelectorAll(selector);
}

test('AdaUI：注册全部组件，并保留调用方覆盖', () => {
  const names = [
    'Button', 'IconButton', 'Input', 'Textarea', 'Select', 'Dropdown', 'Tabs', 'Icon',
    'Card', 'Slider', 'Progress', 'Table', 'Modal', 'Message', 'Notice', 'Badge',
    'Empty', 'Spinner', 'Switch', 'Checkbox', 'Radio', 'Markdown'
  ];
  for (const name of names) assert.ok(UI.components[name], '缺少组件 ' + name);
  assert.equal(UI.components.TextField, UI.components.Input);
  assert.equal(UI.components.Alert, UI.components.Message);

  const Custom = { render: (h) => h('b', null, 'custom') };
  const app = Ada.createApp({ components: { Button: Custom }, render: (h) => h('div') });
  UI.install(app);
  const installed = app.component('Button');
  assert.equal(installed.render, Custom.render);
  assert.notEqual(installed, UI.components.Button);
  assert.ok(app.component('Input'));
});

test('Button：变体、尺寸、点击与禁用', async () => {
  let clicks = 0;
  const { app, root } = mount({
    data() { return { disabled: false }; },
    methods: { onClick() { clicks += 1; } },
    template: '<ui-button variant="primary" size="sm" :disabled="disabled" @click="onClick">保存</ui-button>'
  });
  const button = q(root, 'button');
  assert.equal(button.getAttribute('class'), 'ada-btn ada-btn--primary ada-btn--sm');
  assert.equal(text(button), '保存');

  button.click();
  await Ada.nextTick();
  assert.equal(clicks, 1);

  app._instance.data.disabled = true;
  await Ada.nextTick();
  assert.equal(button.disabled, true);
  button.click();
  await Ada.nextTick();
  assert.equal(clicks, 1);
});

test('Markdown：渲染后端 VNode 数据，未就绪时显示纯文本', () => {
  const { root } = mount({
    data() {
      return {
        nodes: [
          {
            t: 'p',
            p: {},
            c: [
              { t: '#text', v: 'hello ' },
              { t: 'strong', c: [{ t: '#text', v: 'world' }] }
            ]
          },
          {
            t: 'pre',
            c: [{ t: 'code', p: { class: 'language-go' }, c: [{ t: '#text', v: 'fmt.Println(1)' }] }]
          }
        ]
      };
    },
    template: '<div><ui-markdown :nodes="nodes"></ui-markdown><ui-markdown text="pending"></ui-markdown></div>'
  });
  assert.equal(text(q(root, 'p')), 'hello world');
  assert.equal(text(q(root, 'strong')), 'world');
  assert.ok(q(root, 'pre code.language-go'));
  assert.equal(text(q(root, '.markdown-pending')), 'pending');
});

test('Button / Badge：label 在空 slot 时作为 fallback 渲染', () => {
  const { root } = mount({
    template: '<div><ui-button label="保存"></ui-button><ui-badge tone="success" label="OK"></ui-badge></div>'
  });
  assert.equal(text(q(root, 'button')), '保存');
  assert.equal(text(q(root, '.badge')), 'OK');
});

test('Button：href 渲染链接并阻止禁用导航', async () => {
  const { root } = mount({
    template: '<ui-button href="/next" icon="i-plus" label="下一步" disabled></ui-button>'
  });
  const link = q(root, 'a');
  assert.equal(link.getAttribute('class').includes('ada-btn--secondary'), true);
  assert.equal(link.getAttribute('href'), null);
  assert.equal(link.getAttribute('aria-disabled'), 'true');
  assert.equal(link.getAttribute('tabindex'), '-1');
});

test('Input：v-model、label 与 hint', async () => {
  const { app, root } = mount({
    data() { return { name: 'Ada' }; },
    template: '<div><ui-input v-model="name" label="名称" hint="显示在侧栏" placeholder="输入名称"></ui-input><p class="echo">{{ name }}</p></div>'
  });
  const input = q(root, 'input');
  assert.equal(input.value, 'Ada');
  assert.equal(text(q(root, 'label')), '名称');
  assert.equal(text(q(root, '.ada-field__hint')), '显示在侧栏');
  assert.equal(input.getAttribute('placeholder'), '输入名称');

  input.value = 'Bob';
  input.dispatchEvent({ type: 'input' });
  await Ada.nextTick();
  assert.equal(app._instance.data.name, 'Bob');
  assert.equal(text(q(root, '.echo')), 'Bob');
});

test('Input：error 优先于 hint 并设置 aria-invalid', () => {
  const { root } = mount({
    data() { return { value: '' }; },
    template: '<ui-input v-model="value" label="名称" hint="提示" error="必填"></ui-input>'
  });
  const input = q(root, 'input');
  assert.equal(input.getAttribute('aria-invalid'), 'true');
  assert.equal(text(q(root, '.ada-field__hint--error')), '必填');
  assert.equal(q(root, '.ada-field__hint:not(.ada-field__hint--error)'), null);
});

test('Select：选项、当前值、ada:select 与 native 降级', async () => {
  const { app, root } = mount({
    data() {
      return {
        model: 'b',
        options: [{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }]
      };
    },
    template: '<ui-select v-model="model" :options="options" label="模型"></ui-select>'
  });
  const picker = q(root, 'ada-select');
  assert.ok(picker);
  assert.equal(text(q(root, '.ada-select__label')), 'B');
  assert.equal(q(root, '[data-select-input]').value, 'b');
  assert.ok(q(root, '[data-value="b"]').getAttribute('class').split(/\s+/).includes('is-selected'));

  picker.dispatchEvent({ type: 'ada:select', detail: { value: 'a' }, bubbles: true });
  await Ada.nextTick();
  assert.equal(app._instance.data.model, 'a');
  assert.equal(text(q(root, '.ada-select__label')), 'A');

  const native = mount({
    data() { return { model: 'b', options: [{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }] }; },
    template: '<ui-select v-model="model" :options="options" native></ui-select>'
  });
  assert.ok(q(native.root, 'select.ada-input'));
});

test('Tabs：切换、键盘与具名 slot', async () => {
  const { app, root } = mount({
    data() {
      return {
        active: 'one',
        tabs: [{ key: 'one', label: 'One', count: 1 }, { key: 'two', label: 'Two' }]
      };
    },
    template:
      '<ui-tabs v-model="active" :tabs="tabs">' +
      '<template #one><p class="one">ONE</p></template>' +
      '<template #two><p class="two">TWO</p></template>' +
      '</ui-tabs>'
  });
  const tabs = qa(root, '[role="tab"]');
  const panels = qa(root, '.ada-tabs__panel');
  assert.equal(tabs.length, 2);
  assert.equal(tabs[0].getAttribute('aria-selected'), 'true');
  assert.equal(text(q(root, '.one')), 'ONE');
  assert.equal(panels[0].hidden, false);
  assert.equal(panels[1].hidden, true);
  assert.equal(text(q(root, '.ada-tabs__count')), '1');

  tabs[1].click();
  await Ada.nextTick();
  assert.equal(app._instance.data.active, 'two');
  assert.equal(tabs[1].getAttribute('aria-selected'), 'true');
  assert.equal(panels[0].hidden, true);
  assert.equal(panels[1].hidden, false);

  const tablist = q(root, '[role="tablist"]');
  tablist.dispatchEvent({ type: 'keydown', key: 'ArrowLeft' });
  await Ada.nextTick();
  assert.equal(app._instance.data.active, 'one');
  assert.equal(global.document.activeElement.id, tabs[0].getAttribute('id'));
});

test('Card：标题、默认/具名 slot', () => {
  const { root } = mount({
    template:
      '<ui-card title="标题" subtitle="副标题">' +
      '<p class="body">BODY</p>' +
      '<template #header><button class="head-action">操作</button></template>' +
      '<template #footer><span class="foot">FOOT</span></template>' +
      '</ui-card>'
  });
  assert.ok(q(root, 'section.card'));
  assert.equal(text(q(root, '.section-title')), '标题');
  assert.equal(text(q(root, '.section-sub')), '副标题');
  assert.equal(text(q(root, '.body')), 'BODY');
  assert.equal(text(q(root, '.head-action')), '操作');
  assert.equal(text(q(root, '.foot')), 'FOOT');
});

test('Table：列渲染、空态与 loading', () => {
  const { root } = mount({
    data() {
      return {
        columns: [
          { key: 'name', label: '名称' },
          { key: 'age', label: '年龄', render: (row) => row.age + ' 岁' }
        ],
        rows: [{ id: 'a', name: 'Ada', age: 36 }]
      };
    },
    template: '<ui-table :columns="columns" :rows="rows" row-key="id"></ui-table>'
  });
  assert.ok(q(root, 'table.table'));
  assert.equal(qa(root, 'tbody tr').length, 1);
  const cells = qa(root, 'tbody td');
  assert.equal(text(cells[0]), 'Ada');
  assert.equal(text(cells[1]), '36 岁');

  const empty = mount({
    data() { return { columns: [{ key: 'name', label: '名称' }], rows: [] }; },
    template: '<ui-table :columns="columns" :rows="rows" empty="没有数据"></ui-table>'
  });
  assert.equal(text(q(empty.root, '.ada-table__empty')), '没有数据');

  const loading = mount({
    data() { return { columns: [{ key: 'name', label: '名称' }], rows: [] }; },
    template: '<ui-table :columns="columns" :rows="rows" loading></ui-table>'
  });
  assert.ok(q(loading.root, '.ada-table__empty .ada-run-status__spin'));
});

test('Slider / Progress：v-model、百分比与 max', async () => {
  const { app, root } = mount({
    data() { return { value: 40 }; },
    template:
      '<div><ui-slider v-model="value" label="进度"></ui-slider>' +
      '<ui-progress :value="value" label="进度" show-value></ui-progress></div>'
  });
  const range = q(root, 'input[type="range"]');
  const progress = q(root, 'progress');
  assert.equal(String(range.value), '40');
  assert.equal(String(progress.value), '40');
  assert.equal(progress.getAttribute('max'), '100');
  assert.equal(text(q(root, '.ada-progress__value')), '40%');

  range.value = '75';
  range.dispatchEvent({ type: 'input' });
  await Ada.nextTick();
  assert.equal(app._instance.data.value, 75);
  assert.equal(text(q(root, '.ada-progress__value')), '75%');
});

test('Switch / Checkbox / Radio：v-model 与 change', async () => {
  const { app, root } = mount({
    data() { return { on: false, agree: false, choice: 'a' }; },
    template:
      '<div>' +
      '<ui-switch v-model="on" label="开关"></ui-switch>' +
      '<ui-checkbox v-model="agree" label="同意"></ui-checkbox>' +
      '<ui-radio v-model="choice" value="a" name="choice" label="A"></ui-radio>' +
      '<ui-radio v-model="choice" value="b" name="choice" label="B"></ui-radio>' +
      '</div>'
  });
  const checkboxes = qa(root, 'input[type="checkbox"]');
  assert.equal(checkboxes.length, 2);
  checkboxes[0].checked = true;
  checkboxes[0].dispatchEvent({ type: 'change' });
  await Ada.nextTick();
  assert.equal(app._instance.data.on, true);

  checkboxes[1].checked = true;
  checkboxes[1].dispatchEvent({ type: 'change' });
  await Ada.nextTick();
  assert.equal(app._instance.data.agree, true);

  const radios = qa(root, 'input[type="radio"]');
  radios[1].checked = true;
  radios[1].dispatchEvent({ type: 'change' });
  await Ada.nextTick();
  assert.equal(app._instance.data.choice, 'b');
  assert.equal(radios[0].checked, false);
  assert.equal(radios[1].checked, true);
});

test('Message / Notice / Badge / Empty / Spinner：class 与文本', async () => {
  const { root } = mount({
    template:
      '<div>' +
      '<ui-message tone="warn" title="注意" closable>内容</ui-message>' +
      '<ui-notice tone="danger">失败</ui-notice>' +
      '<ui-badge tone="success" label="OK"></ui-badge>' +
      '<ui-empty icon="i-chat" title="空" description="没有内容"></ui-empty>' +
      '<ui-spinner :size="12"></ui-spinner>' +
      '</div>'
  });
  const alert = q(root, '.alert');
  assert.ok(alert.getAttribute('class').includes('alert-warn'));
  assert.equal(text(q(root, '.alert-title')), '注意');
  assert.equal(text(q(root, '.alert-message')), '注意内容');
  assert.ok(q(root, '.alert-dismiss'));
  assert.equal(q(root, '.ada-notice').getAttribute('class'), 'ada-notice ada-notice--danger');
  assert.equal(q(root, '.badge').getAttribute('class'), 'badge badge-success');
  assert.equal(text(q(root, '.ada-empty__title')), '空');
  assert.ok(q(root, 'svg.ada-run-status__spin'));

  q(root, '.alert-dismiss').click();
  await Ada.nextTick();
  assert.equal(alert.hidden, true);
});

test('Modal：modelValue 控制 hidden 与 document 关闭事件', async () => {
  const { app, root } = mount({
    data() { return { open: true }; },
    template: '<ui-modal v-model="open" id="m1" title="标题">正文</ui-modal>'
  });
  const modal = q(root, 'ada-modal');
  assert.ok(modal);
  assert.equal(modal.hasAttribute('hidden'), false);
  assert.equal(text(q(root, '.ada-modal__title')), '标题');
  assert.equal(text(q(root, '.ada-modal__body')), '正文');

  global.document.dispatchEvent({ type: 'ada:modal-close', detail: { id: 'm1' } });
  await Ada.nextTick();
  assert.equal(app._instance.data.open, false);
  assert.equal(modal.hasAttribute('hidden'), true);
});

test('Dropdown：菜单项、分隔线、select 与关闭', async () => {
  let picked = null;
  const { root } = mount({
    data() {
      return {
        items: [
          { label: 'A', value: 'a' },
          { label: 'B', value: 'b', danger: true },
          { separator: true },
          { label: 'C', value: 'c', href: '/c' }
        ]
      };
    },
    methods: { onPick(item) { picked = item.value; } },
    template: '<ui-dropdown label="操作" :items="items" @select="onPick"><span class="custom">CUSTOM</span></ui-dropdown>'
  });
  const details = q(root, 'details.ada-dropdown');
  const items = qa(root, '.ada-row-menu__item');
  assert.ok(details);
  assert.equal(items.length, 3);
  assert.ok(q(root, '.ada-dropdown__separator'));
  assert.equal(text(q(root, '.custom')), 'CUSTOM');

  items[1].click();
  await Ada.nextTick();
  assert.equal(picked, 'b');
  assert.equal(details.open, false);
});

test('toast：创建、内容与手动关闭', () => {
  const handle = UI.toast('已保存', { tone: 'success', duration: 0 });
  const stack = global.document.querySelector('.ada-toast-stack');
  assert.ok(stack);
  const element = q(stack, '.ada-toast');
  assert.ok(element.getAttribute('class').includes('alert-success'));
  assert.equal(text(element).includes('已保存'), true);
  handle.close();
  assert.equal(element.getAttribute('class').includes('ada-toast--closing'), true);
});
