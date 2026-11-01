/*
 * 浏览器渲染路径与 /markdown 无 JS 路径的一致性：
 * 同一份 /markdown.ast VNode 夹具，经 ada-render.js（浏览器运行时）挂载后的 DOM
 * 必须与 Go 侧 RenderHTML 产出的 golden HTML 结构一致。
 *
 * 运行：node --test internal/gateway/web/testdata/ada-block-consistency.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { createDocument } = require('./dom-harness.js');
global.document = createDocument();

const Ada = require('../static/ada-render.js');
const UI = require('../static/ada-ui.js');

const ast = JSON.parse(
  fs.readFileSync(path.join(__dirname, 'markdown-block', 'usage-card.ast.json'), 'utf8')
);
const golden = fs
  .readFileSync(
    path.join(__dirname, '..', '..', 'frontend', 'testdata', 'block', 'usage-card.html'),
    'utf8'
  )
  .trim();

// snap 把 DOM 归一化成可比较结构：标签小写、属性按键排序、忽略空白差异。
function snap(node) {
  if (node.nodeType === 3) return { text: node.textContent };
  const attrs = (node.attributes || [])
    .map((attr) => [attr.name, attr.value])
    .sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
  return {
    tag: node.tagName.toLowerCase(),
    attrs,
    children: node.childNodes.map(snap),
  };
}

test('块 VNode：浏览器渲染与 /markdown golden 结构一致', () => {
  assert.equal(ast.nodes.length, 1, 'AST 应只有一个块节点');

  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = UI.createApp({ render: () => ast.nodes[0] });
  app.mount(root);

  const parsed = global.document.createElement('div');
  parsed.innerHTML = golden;

  assert.deepEqual(snap(root.firstChild), snap(parsed.firstChild));

  const el = root.firstChild;
  assert.equal(el.getAttribute('data-adag-renderer'), 'token-usage/usage-card');
  assert.equal(el.querySelector('.tu-card__total').textContent, '1,801');
  assert.equal(el.querySelectorAll('.tu-stat').length, 4);
});
