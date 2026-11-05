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
const existingSaves = () => calls.filter(
  (c) => c.route === '/save' && c.options && c.options.body && c.options.body.id,
);
let confirmMessage = '';
const provider = {
  id: 'p1',
  displayName: '演示供应商',
  protocol: 'openai',
  baseUrl: 'https://example.test/v1',
  proxy: 'http://old.proxy:8080',
  headers: { 'X-Old': '1' },
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
      if (!body.id) {
        providers.push({
          id: 'p2',
          displayName: body.name,
          protocol: body.protocol,
          baseUrl: body.baseUrl,
          proxy: body.proxy,
          headers: body.headers,
          hasApiKey: !!body.apiKey,
          models: body.models || [],
        });
        return Promise.resolve({ ok: true });
      }
      provider.displayName = body.name;
      provider.protocol = body.protocol;
      provider.baseUrl = body.baseUrl;
      provider.proxy = body.proxy;
      provider.headers = body.headers;
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

test('providers-next：SSR props 直接渲染，不再请求 /state', async () => {
  calls.length = 0;
  const bundle = await importBundle();
  const def = bundle.islands && bundle.islands['page:providers'];
  assert.ok(def);

  const root = global.document.createElement('div');
  global.document.body.appendChild(root);
  const app = globalThis.AdaRender.createApp({
    components: {},
    render: (h) => h(def, {
      data: {
        backend: {
          'provider-admin': {
            state: {
              providers: [{
                id: 'ssr1',
                displayName: 'SSR 供应商',
                protocol: 'openai-chat',
                baseUrl: 'https://ssr.example/v1',
                hasApiKey: false,
                models: [],
              }],
              current: { providerId: 'ssr1', modelId: '' },
            },
          },
        },
      },
    }),
  });
  app.mount(root);
  await globalThis.AdaRender.nextTick();

  assert.equal(root.querySelector('.pm-item__name').textContent, 'SSR 供应商');
  assert.equal(root.textContent.includes('正在读取供应商'), false);
  assert.equal(calls.filter((c) => c.route === '/state').length, 0);
});

test('providers-next：客户端 /state 取数与写操作', async () => {
  const bundle = await importBundle();
  const def = bundle.islands && bundle.islands['page:providers'];
  assert.ok(def, 'bundle 应导出 providers 岛');

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

  const addProvider = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '添加供应商');
  assert.ok(addProvider, '页头应提供添加供应商');
  addProvider.dispatchEvent({ type: 'click' });
  await globalThis.AdaRender.nextTick();
  const newProviderName = root.querySelector('#pm-new-provider-name');
  const newProviderProtocol = root.querySelector('#pm-new-provider-protocol');
  const newProviderBase = root.querySelector('#pm-new-provider-base-url');
  const newProviderProxy = root.querySelector('#pm-new-provider-proxy');
  const newProviderHeaders = root.querySelector('#pm-new-provider-headers');
  const newProviderKey = root.querySelector('#pm-new-provider-api-key');
  assert.ok(newProviderName && newProviderProtocol && newProviderBase && newProviderProxy && newProviderHeaders && newProviderKey);
  newProviderName.value = '新供应商';
  newProviderName.dispatchEvent({ type: 'input' });
  newProviderProtocol.value = 'openai-chat';
  newProviderProtocol.dispatchEvent({ type: 'input' });
  newProviderBase.value = 'https://new-provider.example/v1';
  newProviderBase.dispatchEvent({ type: 'input' });
  newProviderProxy.value = '';
  newProviderProxy.dispatchEvent({ type: 'input' });
  newProviderHeaders.value = 'X-New: 1';
  newProviderHeaders.dispatchEvent({ type: 'input' });
  newProviderKey.value = 'sk-created';
  newProviderKey.dispatchEvent({ type: 'input' });
  await globalThis.AdaRender.nextTick();
  const createProvider = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '创建供应商');
  assert.ok(createProvider);
  createProvider.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (calls.some((c) => c.route === '/save' && !c.options.body.id)) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const createCall = calls.find((c) => c.route === '/save' && !c.options.body.id);
  assert.ok(createCall, '新增供应商应调用 /save 且不带 id');
  assert.equal(createCall.options.body.name, '新供应商');
  assert.equal(createCall.options.body.protocol, 'openai-chat');
  assert.equal(createCall.options.body.baseUrl, 'https://new-provider.example/v1');
  assert.equal(createCall.options.body.apiKey, 'sk-created');
  assert.deepEqual(createCall.options.body.headers, { 'X-New': '1' });

  for (let i = 0; i < 20; i += 1) {
    if (root.querySelectorAll('.pm-item').length >= 2) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
    await globalThis.AdaRender.nextTick();
  }

  const modelsTab = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '模型列表');
  assert.ok(modelsTab);
  modelsTab.dispatchEvent({ type: 'click' });
  await globalThis.AdaRender.nextTick();

  assert.equal(root.querySelectorAll('tbody tr').length, 2);
  const current = root.textContent.includes('当前');
  assert.equal(current, true);

  const modelEdit = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '编辑');
  assert.ok(modelEdit, '模型行应提供编辑按钮');
  modelEdit.dispatchEvent({ type: 'click' });
  await globalThis.AdaRender.nextTick();
  assert.ok(root.querySelector('.pm-modal'), '编辑模型应打开弹窗');
  const modelName = root.querySelector('#pm-model-name');
  const modelContext = root.querySelector('#pm-model-context');
  const levelAdd = root.querySelector('.pm-level__add');
  const imageMode = root.querySelector('#pm-mode-image');
  assert.ok(modelName && modelContext && levelAdd && imageMode, '模型编辑应渲染显示名/上下文/等级标签/模态复选框');  modelName.value = '模型一（改）';
  modelName.dispatchEvent({ type: 'input' });
  modelContext.value = '256000';
  modelContext.dispatchEvent({ type: 'input' });

  for (const level of ['low', 'high']) {
    root.querySelector('.pm-level__add').dispatchEvent({ type: 'click' });
    await globalThis.AdaRender.nextTick();
    const levelInput = root.querySelector('#pm-level-input');
    assert.ok(levelInput, '添加等级应出现输入框');
    levelInput.value = level;
    levelInput.dispatchEvent({ type: 'input' });
    levelInput.dispatchEvent({ type: 'keydown', key: 'Enter', preventDefault() {} });
    await globalThis.AdaRender.nextTick();
  }
  assert.equal(root.querySelectorAll('.pm-level__name').length, 2, '应出现两个等级标签');

  const pickedMode = root.querySelector('#pm-mode-image');
  assert.ok(pickedMode, '模态复选框应存在');
  pickedMode.checked = true;
  pickedMode.dispatchEvent({ type: 'change' });
  await globalThis.AdaRender.nextTick();
  const saveModel = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '保存模型');
  assert.ok(saveModel, '模型编辑应提供保存按钮');
  saveModel.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (existingSaves().length >= 1) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const modelSave = existingSaves().slice(-1)[0].options.body;
  const savedModel = modelSave.models.find((m) => m.id === 'm1');
  assert.equal(savedModel.displayName, '模型一（改）');
  assert.equal(savedModel.contextWindow, 256000);
  assert.deepEqual(savedModel.thinkingLevels, ['low', 'high']);
  assert.deepEqual(savedModel.input, ['image']);

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
    if (existingSaves().length >= 2) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const save = existingSaves().slice(-1)[0];
  assert.ok(save, '应调用 /save');
  assert.equal(save.options.body.id, 'p1');
  assert.equal(save.options.body.disabled, true);
  assert.equal(save.options.body.models.length, 2);
  assert.equal(save.options.body.apiKey, undefined, 'apiKey 留空表示保留原值');

  const fetchButton = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '拉取模型');
  assert.ok(fetchButton, '详情头应提供拉取模型按钮');
  fetchButton.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (root.querySelector('.pm-modal')) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
    await globalThis.AdaRender.nextTick();
  }
  const modal = root.querySelector('.pm-modal');
  assert.ok(modal, '拉取模型应打开弹窗');
  for (let i = 0; i < 20; i += 1) {
    if (modal.textContent.includes('m3')) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
    await globalThis.AdaRender.nextTick();
  }
  assert.ok(modal.textContent.includes('m3'), '弹窗应列出远端模型');

  const fetchedCheckbox = modal.querySelector('input[type="checkbox"]');
  assert.ok(fetchedCheckbox, '弹窗应提供复选框');
  fetchedCheckbox.checked = true;
  fetchedCheckbox.dispatchEvent({ type: 'change' });
  await globalThis.AdaRender.nextTick();
  const addSelected = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '添加所选（1）');
  assert.ok(addSelected, '勾选后应启用添加所选');
  addSelected.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (existingSaves().length >= 3) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const saves = existingSaves();
  assert.ok(saves.length >= 2, '添加模型应再次调用 /save');
  assert.ok(saves[saves.length - 1].options.body.models.some((m) => m.id === 'm3'));
  await globalThis.AdaRender.nextTick();
  assert.equal(root.querySelector('.pm-modal'), null, '添加后弹窗应关闭');

  const addModel = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '添加模型');
  assert.ok(addModel, '模型页应提供添加模型');
  addModel.dispatchEvent({ type: 'click' });
  await globalThis.AdaRender.nextTick();
  const newId = root.querySelector('#pm-model-id');
  const newName = root.querySelector('#pm-model-name');
  const newContext = root.querySelector('#pm-model-context');
  assert.ok(newId && newName && newContext);
  newId.value = 'm4';
  newId.dispatchEvent({ type: 'input' });
  newName.value = '模型四';
  newName.dispatchEvent({ type: 'input' });
  newContext.value = '16000';
  newContext.dispatchEvent({ type: 'input' });
  await globalThis.AdaRender.nextTick();
  const addConfirm = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '保存模型');
  assert.ok(addConfirm);
  addConfirm.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (existingSaves().length >= 4) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const afterAdd = existingSaves().slice(-1)[0].options.body;
  assert.ok(afterAdd.models.some((m) => m.id === 'm4'));
  assert.equal(afterAdd.models.find((m) => m.id === 'm4').contextWindow, 16000);

  for (let i = 0; i < 20; i += 1) {
    if (root.textContent.includes('m4')) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
    await globalThis.AdaRender.nextTick();
  }

  const row = [...root.querySelectorAll('tbody tr')].find((r) => r.textContent.includes('m4'));
  assert.ok(row, '新模型应出现在表格');
  const rowEdit = [...row.querySelectorAll('button')].find((b) => b.textContent.trim() === '编辑');
  assert.ok(rowEdit);
  rowEdit.dispatchEvent({ type: 'click' });
  await globalThis.AdaRender.nextTick();
  const removeModel = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '删除模型');
  assert.ok(removeModel);
  removeModel.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (existingSaves().length >= 5) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const afterRemove = existingSaves().slice(-1)[0].options.body;
  assert.ok(!afterRemove.models.some((m) => m.id === 'm4'));
  assert.equal(afterRemove.models.length, 3);

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
  const proxy = root.querySelector('#pm-proxy');
  const headers = root.querySelector('#pm-headers');
  const apiKey = root.querySelector('#pm-api-key');
  assert.ok(name && protocol && baseUrl && proxy && headers && apiKey, '编辑表单应渲染全部输入');
  name.value = '新名称';
  name.dispatchEvent({ type: 'input' });
  protocol.value = 'openai-chat';
  protocol.dispatchEvent({ type: 'input' });
  baseUrl.value = 'https://new.example/v1';
  baseUrl.dispatchEvent({ type: 'input' });
  proxy.value = 'http://127.0.0.1:7890';
  proxy.dispatchEvent({ type: 'input' });
  headers.value = 'X-A: 1\nX-B: two';
  headers.dispatchEvent({ type: 'input' });
  apiKey.value = 'sk-new';
  apiKey.dispatchEvent({ type: 'input' });
  await globalThis.AdaRender.nextTick();

  const saveButton = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === '保存');
  assert.ok(saveButton, '编辑表单应提供保存按钮');
  saveButton.dispatchEvent({ type: 'click' });
  for (let i = 0; i < 20; i += 1) {
    if (existingSaves().length >= 6) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  const savesAfterEdit = existingSaves();
  assert.ok(savesAfterEdit.length >= 3, '保存表单应调用 /save');
  const editSave = savesAfterEdit[savesAfterEdit.length - 1].options.body;
  assert.equal(editSave.name, '新名称');
  assert.equal(editSave.protocol, 'openai-chat');
  assert.equal(editSave.baseUrl, 'https://new.example/v1');
  assert.equal(editSave.proxy, 'http://127.0.0.1:7890');
  assert.deepEqual(editSave.headers, { 'X-A': '1', 'X-B': 'two' });
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
