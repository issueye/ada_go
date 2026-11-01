/*
 * 动态块里的 $C 组件（TodoList）：浏览器运行时渲染出的 DOM 必须与 Go 侧
 * RenderHTML 产出的 golden 结构一致（README/SPEC 承诺块内可用 Icon 与 TodoList）。
 *
 * 与 ada-block-consistency.test.js 的区别：那份夹具不含组件节点，这份专门覆盖
 * $C → 浏览器组件注册这条路径（TodoList 曾因客户端未注册而整条消息挂载失败）。
 *
 * 运行：node --test internal/gateway/web/testdata/ada-block-components.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { createDocument } = require('./dom-harness.js');
global.document = createDocument();

const UI = require('../static/ada-ui.js');

const ast = JSON.parse(
  fs.readFileSync(path.join(__dirname, 'markdown-block', 'todo-list.ast.json'), 'utf8')
);
const golden = fs
  .readFileSync(
    path.join(__dirname, '..', '..', 'frontend', 'testdata', 'block', 'todo-list.html'),
    'utf8'
  )
  .trim();

// snap 把 DOM 归一化成可比较结构：标签小写、属性按键排序、忽略空白差异。
// svg 只比外壳：Go 侧内联 path，浏览器侧用 <use href="#i-…"> 引用 sprite，
// 图形内部不要求逐节点一致（CSS 与尺寸/描边属性一致即可）。
function snap(node) {
  if (node.nodeType === 3) return { text: node.textContent };
  const attrs = (node.attributes || [])
    .map((attr) => [attr.name, attr.value])
    .sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
  const tag = node.tagName.toLowerCase();
  return {
    tag,
    attrs,
    children: tag === 'svg' ? [] : node.childNodes.map(snap),
  };
}

test('块 $C TodoList：浏览器渲染与 /markdown golden 结构一致', () => {
  assert.equal(ast.nodes.length, 1, 'AST 应只有一个块节点');

  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  // 先确认组件确实在应用注册表里：未注册时 mount 会直接抛错。
  const app = UI.createApp({ render: () => ast.nodes[0] });
  app.mount(root);

  const parsed = global.document.createElement('div');
  parsed.innerHTML = golden;

  assert.deepEqual(snap(root.firstChild), snap(parsed.firstChild));

  const el = root.firstChild;
  assert.equal(el.getAttribute('data-adag-renderer'), 'demo/todo');
  assert.equal(el.querySelectorAll('.ada-todo__step').length, 3);
  assert.equal(el.querySelectorAll('.ada-icon').length, 3);
  assert.equal(el.querySelector('.ada-todo__step.is-done').textContent, '写文档');
  assert.equal(el.querySelector('.ada-todo__step.is-running').textContent, '跑测试');
  assert.equal(el.querySelector('.ada-todo__step.is-pending').textContent, '发布');
});

test('TodoList 空清单：不画面板（Go 侧不输出内容，浏览器侧留空容器）', () => {
  const empty = {
    t: 'div',
    p: { class: 'adag-ui-block', 'data-adag-renderer': 'demo/todo' },
    c: [{ t: '$C', n: 'TodoList', p: { notes: '不应出现', steps: [] } }],
  };
  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  UI.createApp({ render: () => empty }).mount(root);

  assert.equal(root.querySelectorAll('.ada-todo__list').length, 0);
  assert.equal(root.querySelectorAll('.ada-todo__notes').length, 0);
  assert.equal(root.querySelectorAll('.ada-todo__list-wrap').length, 1);
});
