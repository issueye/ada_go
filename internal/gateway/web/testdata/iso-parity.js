/*
 * iso-parity.js · SSR golden 与客户端 mount 的结构比较工具（docs/20 P0-3）
 *
 * 只做测试支撑：把 DOM 归一化成 { tag, attrs, children }，属性按键排序，
 * 布尔属性归一成「存在 = 空串」。比较失败时给出第一处差异的路径，
 * 避免只报一句 deepEqual。
 */
'use strict';

var BOOLEAN_ATTRS = [
  'required', 'disabled', 'readonly', 'checked', 'open', 'hidden',
  'selected', 'multiple', 'autofocus', 'novalidate', 'formnovalidate', 'inert',
];

function normalizeAttr(name, value, element) {
  if (BOOLEAN_ATTRS.indexOf(String(name).toLowerCase()) !== -1) return '';
  return String(value);
}

function snapshot(node) {
  if (!node) return null;
  if (node.nodeType === 3) return { text: String(node.textContent) };
  if (node.nodeType !== 1) return null;

  var attrs = [];
  var raw = node.attributes || [];
  for (var i = 0; i < raw.length; i += 1) {
    attrs.push([raw[i].name, normalizeAttr(raw[i].name, raw[i].value, node)]);
  }
  // DOM 属性可能只写在 property 上（例如 disabled=true 不反射成 attribute）；
  // 对布尔属性统一补成「存在」。
  for (var b = 0; b < BOOLEAN_ATTRS.length; b += 1) {
    var name = BOOLEAN_ATTRS[b];
    var exists = false;
    for (var a = 0; a < attrs.length; a += 1) {
      if (attrs[a][0] === name) { exists = true; break; }
    }
    if (!exists && node[name] === true) attrs.push([name, '']);
  }
  attrs.sort(function (left, right) {
    return left[0] < right[0] ? -1 : left[0] > right[0] ? 1 : 0;
  });

  var children = [];
  var nodes = node.childNodes || [];
  for (var c = 0; c < nodes.length; c += 1) {
    var child = snapshot(nodes[c]);
    if (child) children.push(child);
  }
  return { tag: String(node.tagName).toLowerCase(), attrs: attrs, children: children };
}

function parseHTML(doc, html) {
  var host = doc.createElement('div');
  host.innerHTML = html;
  return host.firstChild;
}

function describe(value) {
  return JSON.stringify(value);
}

function diffNode(actual, expected, path) {
  if (!actual || !expected) {
    return path + ': 节点缺失（actual=' + describe(actual) + ' expected=' + describe(expected) + '）';
  }
  var actualIsText = actual.text !== undefined;
  var expectedIsText = expected.text !== undefined;
  if (actualIsText || expectedIsText) {
    if (!actualIsText || !expectedIsText) return path + ': 文本/元素类型不一致';
    if (actual.text !== expected.text) {
      return path + ': 文本不一致（actual=' + describe(actual.text) + ' expected=' + describe(expected.text) + '）';
    }
    return '';
  }
  if (actual.tag !== expected.tag) {
    return path + ': 标签不一致（actual=' + actual.tag + ' expected=' + expected.tag + '）';
  }
  if (describe(actual.attrs) !== describe(expected.attrs)) {
    return path + ': 属性不一致（actual=' + describe(actual.attrs) + ' expected=' + describe(expected.attrs) + '）';
  }
  if (actual.children.length !== expected.children.length) {
    return path + ': 子节点数量不一致（actual=' + actual.children.length + ' expected=' + expected.children.length + '）';
  }
  for (var i = 0; i < actual.children.length; i += 1) {
    var problem = diffNode(actual.children[i], expected.children[i], path + '/' + i);
    if (problem) return problem;
  }
  return '';
}

function assertEquivalent(actual, expected, label) {
  var problem = diffNode(actual, expected, label || 'root');
  if (problem) throw new Error('[iso-parity] ' + problem);
}

module.exports = {
  snapshot: snapshot,
  parseHTML: parseHTML,
  diffNode: diffNode,
  assertEquivalent: assertEquivalent,
};
