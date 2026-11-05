/*
 * composer-draft.js 的回归测试：刷新后恢复、输入防抖、提交清草稿、
 * 页面卸载前同步落盘。
 *
 * 用 testdata/dom-harness.js 的最小 DOM；localStorage 用内存实现。
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { createDocument } = require('./dom-harness.js');

const SOURCE = fs.readFileSync(
  path.join(__dirname, '..', 'static', 'composer-draft.js'),
  'utf8'
);

function createStorage(seed) {
  const data = new Map(Object.entries(seed || {}));
  return {
    getItem(key) { return data.has(key) ? data.get(key) : null; },
    setItem(key, value) { data.set(key, String(value)); },
    removeItem(key) { data.delete(key); },
    get size() { return data.size; },
  };
}

function boot(markup, seed) {
  const document = createDocument();
  document.body.innerHTML = markup;
  const listeners = Object.create(null);
  const window = {
    localStorage: createStorage(seed),
    setTimeout,
    clearTimeout,
    addEventListener(name, fn) {
      (listeners[name] || (listeners[name] = [])).push(fn);
    },
    emit(name) {
      for (const fn of listeners[name] || []) fn({ type: name });
    },
  };
  vm.runInNewContext(SOURCE, { window, document, setTimeout, clearTimeout, console });
  return { document, window };
}

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const formWith = (key) =>
  `<form><textarea name="text" data-draft-key="${key}"></textarea></form>`;

test('加载时恢复草稿，并把光标放到末尾', () => {
  const { document } = boot(formWith('new-task'), { 'a_da_draft:new-task': '上次没发完的内容' });
  const input = document.querySelector('textarea');
  assert.equal(input.value, '上次没发完的内容');
});

test('输入防抖后写入 localStorage，清空输入删除草稿', async () => {
  const { document, window } = boot(formWith('new-task'));
  const input = document.querySelector('textarea');

  input.value = '第一版';
  input.dispatchEvent({ type: 'input' });
  await wait(300);
  assert.equal(window.localStorage.getItem('a_da_draft:new-task'), '第一版');

  input.value = '';
  input.dispatchEvent({ type: 'input' });
  await wait(300);
  assert.equal(window.localStorage.getItem('a_da_draft:new-task'), null);
});

test('页面卸载前同步落盘，不丢防抖窗口里的最后输入', () => {
  const { document, window } = boot(formWith('thread:th_1'));
  const input = document.querySelector('textarea');

  input.value = '正在输入就触发了刷新';
  input.dispatchEvent({ type: 'input' });
  window.emit('pagehide');
  assert.equal(window.localStorage.getItem('a_da_draft:thread:th_1'), '正在输入就触发了刷新');
});

test('提交后删除草稿，卸载 flush 不会把它写回来', () => {
  const { document, window } = boot(formWith('new-task'), { 'a_da_draft:new-task': '要发送的内容' });
  const form = document.querySelector('form');
  const input = document.querySelector('textarea');

  input.value = '要发送的内容';
  form.dispatchEvent({ type: 'submit' });
  assert.equal(window.localStorage.getItem('a_da_draft:new-task'), null);

  window.emit('pagehide');
  assert.equal(window.localStorage.getItem('a_da_draft:new-task'), null);
});

test('被确认弹窗拦下的提交不清草稿', () => {
  const { document, window } = boot(formWith('new-task'), { 'a_da_draft:new-task': '还没确认' });
  const form = document.querySelector('form');

  form.dispatchEvent({ type: 'submit', defaultPrevented: true });
  assert.equal(window.localStorage.getItem('a_da_draft:new-task'), '还没确认');
});

test('不同会话按各自的 key 隔离', () => {
  const markup = formWith('thread:th_1') + formWith('thread:th_2');
  const { document, window } = boot(markup, {
    'a_da_draft:thread:th_1': '会话一',
    'a_da_draft:thread:th_2': '会话二',
  });
  const inputs = document.querySelectorAll('textarea');
  assert.equal(inputs[0].value, '会话一');
  assert.equal(inputs[1].value, '会话二');
  assert.equal(window.localStorage.getItem('a_da_draft:thread:th_1'), '会话一');
});
