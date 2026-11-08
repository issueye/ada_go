/*
 * composer-telemetry 的 slot 岛屿探针：把生成的 static/client.js 装进最小 DOM
 * 夹具，验证 clientOnly 挂载后的 DOM 形态与轮询开关。
 *
 * 刻意不依赖真实定时器：window.setInterval / clearInterval 换成假实现，
 * 由测试手动触发 tick，避免测试进程被 2s 轮询钉住。
 *
 * 结果断言只看 DOM 与假定时器，不读实例内部字段：app.mount 返回的是外层包装
 * 组件的 proxy，岛自己的响应式数据不在它上面。
 *
 * 运行：node --test plugins/official/composer-telemetry/static/telemetry.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs');

const repoRoot = path.join(__dirname, '..', '..', '..', '..');
const harnessPath = path.join(repoRoot, 'internal', 'gateway', 'web', 'testdata', 'dom-harness.js');
const { createDocument } = require(harnessPath);

global.document = createDocument();
const Ada = require(path.join(repoRoot, 'internal', 'gateway', 'web', 'static', 'ada-render.js'));
globalThis.AdaRender = Ada;

function loadIslands() {
  // bundle 是 ESM（export islands / default）；包一层把导出转成对象。
  const body = fs
    .readFileSync(path.join(__dirname, 'client.js'), 'utf8')
    .replace(/^\/\/ generated.*$/m, '')
    .replace(/export\s*\{\s*client_default as default,\s*islands\s*\};?/, '');
  return new Function('globalThis', body + '\nreturn { islands: islands };')(globalThis);
}

// fakeTimers 记录 setInterval/clearInterval，不真的排队。
function installFakeTimers() {
  const intervals = [];
  const state = { cleared: [] };
  globalThis.window = {
    setInterval: function (fn) {
      const handle = intervals.length + 1;
      intervals.push({ handle, fn });
      return handle;
    },
    clearInterval: function (handle) {
      state.cleared.push(handle);
      const index = intervals.findIndex((entry) => entry.handle === handle);
      if (index >= 0) intervals.splice(index, 1);
    },
  };
  return { intervals, state };
}

function respond(payload) {
  return Promise.resolve({
    ok: true,
    json: function () {
      return Promise.resolve(payload);
    },
  });
}

async function waitReady(host) {
  for (let i = 0; i < 50; i += 1) {
    if (host.querySelector('.ada-aux')) return true;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  return false;
}

function mountIsland(def, threadId) {
  const host = global.document.createElement('div');
  host.setAttribute('class', 'ada-island ada-island--client');
  global.document.body.appendChild(host);
  const app = Ada.createApp({
    render: function (h) {
      // 服务端桥注入的 props 形状：app（身份投影）+ data.thread（会话引用）。
      return h(def, { app: { workspaceName: 'ada_go' }, data: { thread: { id: threadId } } });
    },
  });
  app.mount(host);
  return { host, app };
}

test('clientOnly 挂载：先占位、取数后渲染整套辅助区，运行中开始轮询', async () => {
  const def = loadIslands().islands['slot:composer.footer'];
  assert.ok(def, 'bundle 必须导出 slot:composer.footer 的岛');

  const timers = installFakeTimers();
  const calls = [];
  globalThis.window.fetch = function (url) {
    calls.push(url);
    return respond({
      run: { status: 'running', startedAt: '2026-10-28T10:00:00Z' },
      context: { tokens: 215000, messages: 152000, system: 1500, tools: 7700, window: 1048576, percent: 21 },
      usage: { thread: { inputTokens: 1000, cacheTokens: 990, outputTokens: 200, costMicros: 1200 } },
    });
  };

  const { host, app } = mountIsland(def, 'th_1');

  // 首帧占位根：取数之前不画内容、不占版面（CSS 里 display:none）。
  assert.equal(host.childNodes.length, 1);
  assert.equal(host.firstChild.className, 'ada-telemetry__pending');
  assert.equal(calls.length, 1, '挂载即拉一次');
  assert.equal(calls[0], '/threads/th_1/state');

  assert.equal(await waitReady(host), true, '首帧取数应完成');

  const text = host.textContent;
  assert.ok(text.includes('运行中'), '运行中应显示：' + text);
  assert.ok(text.includes('工作区：ada_go'), '工作区应显示：' + text);
  assert.ok(text.includes('缓存命中 99%'), '缓存命中率 = cache/input：' + text);
  assert.ok(text.includes('21%'), '上下文环百分比：' + text);
  assert.equal(host.querySelectorAll('.ada-token-ring').length, 1);
  assert.equal(host.querySelectorAll('.ada-context__row').length, 3, '气泡三段分项');

  // 运行中：注册了一个轮询，手动 tick 会再拉一次。
  assert.equal(timers.intervals.length, 1, '运行中应注册轮询');
  timers.intervals[0].fn();
  assert.equal(calls.length, 2, 'tick 应再拉一次');

  // 卸载即停（岛自己的 unmounted 钩子）。
  assert.deepEqual(timers.state.cleared, [], '卸载前不该清过定时器');
  app.unmount();
  assert.deepEqual(timers.state.cleared, [1], '卸载应清掉轮询');
  assert.equal(timers.intervals.length, 0);
});

test('终态不轮询：一次取数后保持静止', async () => {
  const def = loadIslands().islands['slot:composer.footer'];

  const timers = installFakeTimers();
  const calls = [];
  globalThis.window.fetch = function (url) {
    calls.push(url);
    return respond({
      run: { status: 'terminal', terminal: 'completed' },
      context: { tokens: 0, window: 1048576, percent: 0 },
      usage: { thread: { inputTokens: 0, cacheTokens: 0 } },
    });
  };

  const { host } = mountIsland(def, 'th_2');
  assert.equal(await waitReady(host), true);
  assert.equal(timers.intervals.length, 0, '终态不应注册轮询');
  assert.equal(calls.length, 1);

  // 无用量时缓存命中显示破折号，与迁走前基座的「—」一致。
  assert.ok(host.textContent.includes('缓存命中 —'), host.textContent);
  // 无运行时状态就不出现「运行中」品牌项。
  assert.equal(host.querySelectorAll('.ada-aux__item--brand').length, 0);
});

test('取数失败保持空占位，不抛错也不重复注册定时器', async () => {
  const def = loadIslands().islands['slot:composer.footer'];

  const timers = installFakeTimers();
  globalThis.window.fetch = function () {
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) });
  };

  const { host } = mountIsland(def, 'th_3');
  for (let i = 0; i < 30; i += 1) await new Promise((resolve) => setTimeout(resolve, 10));

  // 失败路径不画内容：仍然是占位根，但会按周期重试（这里断言只注册了一次）。
  assert.equal(host.childNodes.length, 1);
  assert.equal(host.firstChild.className, 'ada-telemetry__pending');
  assert.equal(timers.intervals.length, 1, '失败后应继续轮询一次而不是叠加');
});
