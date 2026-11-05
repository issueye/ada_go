/*
 * P2-4b-2b 组件化 provider 页：真实 bundle + 假 adaAPI。
 * 验证 /state 取数、列表/页签、模型表与 /select 写回都走组件状态。
 *
 * 运行：node --test internal/gateway/web/testdata/provider-next.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { createDocument } = require('./dom-harness.js');

global.document = createDocument();
globalThis.AdaRender = require('../static/ada-render.js');
require('../static/ada-ui-iso.js');

const calls = [];
let confirmMessage = '';
const provider = {
  id: 'p1',
  displayName: '演示供应商',
  protocol: 'openai',
  baseUrl: 'https://example.test/v1',
  hasApiKey: true,
  apiKeyHint: '1234',
  models: [
    { id: 'm1', displayName: '模型一', contextWindow: 128000, supportsTools: true },
    { id: 'm2', displayName: '模型二', contextWindow: 64000, supportsTools: false },
  ],
};
const providers = [provider];
const current = { providerId: 'p1', modelId: 'm1' };

global.window = {
  adaConfirm(message, onConfirm) {
    confirmMessage = message;
    onConfirm();
  },
  adaAPI(plugin, route, options) {
    calls.push({ plugin, route, options });
    if (route === '/state') {
      return Promise.resolve({ providers, current });
    }
    if (route === '/select') {
      current.providerId = options.body.providerId;
      current.modelId = options.body.modelId;
      return Promise.resolve({ ok: true });
    }
    if (route === '/save') {
      const body = options.body;
      provider.displayName = body.name;
      provider.protocol = body.protocol;
      provider.baseUrl = body.baseUrl;
      provider.disabled = !!body.disabled;
      provider.models = body.models;
      if (body.apiKey) {
        provider.hasApiKey = true;
        provider.apiKeyHint = 'new';
      }
      return Promise.resolve({ ok: true });
    }
    if (route === '/remove') {
      providers.length = 0;
      return Promise.resolve({ ok: true });
    }
    if (route === '/fetch-models') {
      return Promise.resolve([
        { id: 'm3', displayName: '模型三', contextWindow: 32000, supportsTools: true },
      ]);
    }
    return Promise.reject(new Error('unexpected route ' + route));
  },
};

async function importBundle() {
  const file = path.resolve(__dirname, '..', '..', '..', '..', 'plugins', 'official', 'provider-admin', 'static', 'client.js');
  // 仓库根 package.json 不是 type:module，直接 import .js 会被当成 CJS；
  // 用 data: URL 让 Node 按 ESM 求值，bundle 本身是自包含的。
  const source = fs.readFileSync(file, 'utf8');
  return import('data:text/javascript;base64,' + Buffer.from(source, 'utf8').toString('base64'));
}

test('providers-next：/state 取数、模型表与设为当前', async () => {
  const bundle = await importBundle();
  const def = bundle.islands && bundle.islands['page:providers-next'];
  assert.ok(def, 'bundle 应导出 providers-next 岛');

  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = globalThis.AdaRender.createApp({ components: {}, render: (h) => h(def, {}) });
  app.mount(root);

  // mounted 的 /state Promise 结算。
  for (let i = 0; i < 20 && root.querySelectorAll('.pm-item').length === 0; i += 1) {
    await new Promise((resolve) => setTimeout(resolve, 10));
    await globalThis.AdaRender.nextTick();
  }

  assert.equal(root.querySelectorAll('.pm-item').length, 1);
  assert.equal(root.querySelector('.pm-item__name').textContent, '演示供应商');
  assert.ok(root.textContent.includes('密钥已保存'));

  const modelsTab = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '模型列表');
  assert.ok(modelsTab);
  modelsTab.dispatchEvent({ type: 'click' });
  await globalThis.AdaRender.nextTick();

  assert.equal(root.querySelectorAll('tbody tr').length, 2);
  const current = root.textContent.includes('当前');
  assert.equal(current, true);

  const switchButton = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '设为当前');
  assert.ok(switchButton, '非当前模型应提供设为当前按钮');
  switchButton.dispatchEvent({ type: 'click' });

  for (let i = 0; i < 20; i += 1) {
    if (calls.some((c) => c.route === '/select')) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const select = calls.find((c) => c.route === '/select');
  assert.ok(select, '应调用 /select');
  assert.deepEqual(select.options.body, { providerId: 'p1', modelId: 'm2' });

  const disable = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '停用');
  assert.ok(disable, '详情头应提供停用按钮');
  disable.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (calls.some((c) => c.route === '/save')) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const save = calls.find((c) => c.route === '/save');
  assert.ok(save, '应调用 /save');
  assert.equal(save.options.body.id, 'p1');
  assert.equal(save.options.body.disabled, true);
  assert.equal(save.options.body.models.length, 2);
  assert.equal(save.options.body.apiKey, undefined, 'apiKey 留空表示保留原值');

  const fetchButton = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '拉取模型');
  assert.ok(fetchButton, '详情头应提供拉取模型按钮');
  fetchButton.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (root.textContent.includes('拉取到 1 个模型')) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
    await globalThis.AdaRender.nextTick();
  }
  assert.ok(root.textContent.includes('拉取到 1 个模型'));

  const addButton = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '添加未配置模型');
  assert.ok(addButton, '拉取后应提供添加按钮');
  addButton.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (calls.filter((c) => c.route === '/save').length >= 2) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const saves = calls.filter((c) => c.route === '/save');
  assert.ok(saves.length >= 2, '添加模型应再次调用 /save');
  assert.equal(saves[saves.length - 1].options.body.models.length, 3);

  const infoTab = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '供应商信息');
  assert.ok(infoTab);
  infoTab.dispatchEvent({ type: 'click' });
  await globalThis.AdaRender.nextTick();
  const edit = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '编辑');
  assert.ok(edit, '信息页应提供编辑按钮');
  edit.dispatchEvent({ type: 'click' });
  await globalThis.AdaRender.nextTick();

  const name = root.querySelector('#pm-name');
  const protocol = root.querySelector('#pm-protocol');
  const baseUrl = root.querySelector('#pm-base-url');
  const apiKey = root.querySelector('#pm-api-key');
  assert.ok(name && protocol && baseUrl && apiKey, '编辑表单应渲染四个输入');
  name.value = '新名称';
  name.dispatchEvent({ type: 'input' });
  protocol.value = 'openai-chat';
  protocol.dispatchEvent({ type: 'input' });
  baseUrl.value = 'https://new.example/v1';
  baseUrl.dispatchEvent({ type: 'input' });
  apiKey.value = 'sk-new';
  apiKey.dispatchEvent({ type: 'input' });
  await globalThis.AdaRender.nextTick();

  const saveButton = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '保存');
  assert.ok(saveButton, '编辑表单应提供保存按钮');
  saveButton.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (calls.filter((c) => c.route === '/save').length >= 3) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const savesAfterEdit = calls.filter((c) => c.route === '/save');
  assert.ok(savesAfterEdit.length >= 3, '保存表单应调用 /save');
  const editSave = savesAfterEdit[savesAfterEdit.length - 1].options.body;
  assert.equal(editSave.name, '新名称');
  assert.equal(editSave.protocol, 'openai-chat');
  assert.equal(editSave.baseUrl, 'https://new.example/v1');
  assert.equal(editSave.apiKey, 'sk-new');
  assert.equal(editSave.models.length, 3, '保存表单应保留现有模型');

  const remove = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '删除');
  assert.ok(remove, '详情头应提供删除按钮');
  remove.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (calls.some((c) => c.route === '/remove')) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const removeCall = calls.find((c) => c.route === '/remove');
  assert.ok(removeCall, '应调用 /remove');
  assert.deepEqual(removeCall.options.body, { id: 'p1' });
  assert.ok(confirmMessage.includes('删除供应商'), '删除前应走 adaConfirm');
});
