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
global.window = {
  adaAPI(plugin, route, options) {
    calls.push({ plugin, route, options });
    if (route === '/state') {
      return Promise.resolve({
        providers: [{
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
        }],
        current: { providerId: 'p1', modelId: 'm1' },
      });
    }
    if (route === '/select') return Promise.resolve({ ok: true });
    if (route === '/save') return Promise.resolve({ ok: true });
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
});
