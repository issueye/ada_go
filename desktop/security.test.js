/*
 * 桌面壳安全基线（P3-4 Electron 主世界复查的静态门禁）。
 * 不启动 Electron；只钉住 main.cjs 的 webPreferences 与导航拦截。
 *
 * 运行：node --test desktop/security.test.js
 */
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const source = fs.readFileSync(path.join(__dirname, 'main.cjs'), 'utf8');

test('Electron 窗口保持隔离与同源安全默认值', () => {
  assert.match(source, /contextIsolation:\s*true/);
  assert.match(source, /nodeIntegration:\s*false/);
  assert.match(source, /webSecurity:\s*true/);
  assert.match(source, /allowRunningInsecureContent:\s*false/);
  assert.doesNotMatch(source, /webSecurity:\s*false/);
  assert.doesNotMatch(source, /allowRunningInsecureContent:\s*true/);
});

test('外部导航仍由主进程拦截并交给系统浏览器', () => {
  assert.match(source, /setWindowOpenHandler/);
  assert.match(source, /will-navigate/);
  assert.match(source, /shell\.openExternal/);
});
