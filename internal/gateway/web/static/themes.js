/*
 * 主题扩展：调色板维 + 明暗模式维（阻塞式加载，必须紧跟 theme.js 之后）
 *
 * theme.js（上游镜像，不改）只管明暗：localStorage['a_da_theme'] 或跟随系统，
 * 切 html.dark / html.light。本文件在它之后叠加第二维：
 *   - 调色板：localStorage['a_da_theme_palette'] → html[data-theme="<id>"]；
 *     不设属性 = 默认 Black Swan（tokens.css 原样生效）。
 *   - 模式：localStorage['a_da_theme_mode']（light / dark / system），system
 *     跟随系统并监听变化；与旧键 a_da_theme 保持同步（theme.js 的启动逻辑
 *     仍消费旧键），首次读到旧键时迁移成 mode。
 *
 * 面板交互走事件委托（与 theme.js 的 data-theme-toggle 同一模式）：
 *   [data-theme-panel-toggle] 开关面板、[data-theme-mode] 三态、
 *   [data-theme-pick] 选色板。侧栏 rail 的快切按钮仍是 data-theme-toggle，
 *   由 theme.js 切类写旧键，本文件的委托注册在其后，读取切类结果回写 mode。
 *
 * 规则见 docs/19-ued.md §19.2.2 / §19.5.1（弹层四要素）。
 */
(function () {
  'use strict';

  var root = document.documentElement;
  var LEGACY_KEY = 'a_da_theme';
  var MODE_KEY = 'a_da_theme_mode';
  var PALETTE_KEY = 'a_da_theme_palette';
  var MODES = ['light', 'dark', 'system'];
  var PALETTES = ['black-swan', 'moss', 'ocean', 'dusk'];

  function read(key) {
    try {
      return localStorage.getItem(key);
    } catch (_) {
      return null;
    }
  }
  function write(key, value) {
    try {
      localStorage.setItem(key, value);
    } catch (_) {
      /* 忽略持久化失败：本次会话内主题仍然生效 */
    }
  }
  function remove(key) {
    try {
      localStorage.removeItem(key);
    } catch (_) {
      /* 同上 */
    }
  }

  function prefersDark() {
    return !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
  }

  // 旧键迁移：theme.js 时代的 a_da_theme（light/dark）→ mode。
  var legacy = read(LEGACY_KEY);
  if ((legacy === 'light' || legacy === 'dark') && MODES.indexOf(read(MODE_KEY)) < 0) {
    write(MODE_KEY, legacy);
  }

  var mode = read(MODE_KEY);
  if (MODES.indexOf(mode) < 0) mode = 'system';

  var palette = read(PALETTE_KEY);
  if (PALETTES.indexOf(palette) < 0) palette = 'black-swan';

  var panel = null;

  function applyClasses(isDark) {
    root.classList.toggle('dark', isDark);
    root.classList.toggle('light', !isDark);
  }

  function syncUI() {
    var groups = document.querySelectorAll('[data-theme-mode]');
    for (var i = 0; i < groups.length; i += 1) {
      var btn = groups[i];
      var active = btn.getAttribute('data-theme-mode') === mode;
      btn.classList.toggle('is-selected', active);
      btn.setAttribute('aria-checked', active ? 'true' : 'false');
    }
    var picks = document.querySelectorAll('[data-theme-pick]');
    for (var j = 0; j < picks.length; j += 1) {
      var pick = picks[j];
      var picked = pick.getAttribute('data-theme-pick') === palette;
      pick.classList.toggle('is-selected', picked);
      pick.setAttribute('aria-checked', picked ? 'true' : 'false');
    }
    var trigger = document.querySelector('[data-theme-panel-toggle]');
    if (trigger) {
      var open = panel && panel.classList.contains('is-open');
      trigger.setAttribute('aria-expanded', open ? 'true' : 'false');
    }
  }

  // 应用模式：system 时移除旧键（theme.js 下次启动自然跟随系统），
  // 固定模式时把旧键一并写上，两条启动路径（theme.js / 本文件）结论一致。
  function applyMode() {
    if (mode === 'system') {
      remove(LEGACY_KEY);
      applyClasses(prefersDark());
    } else {
      write(LEGACY_KEY, mode);
      applyClasses(mode === 'dark');
    }
    syncUI();
  }

  function applyPalette() {
    if (palette === 'black-swan') {
      root.removeAttribute('data-theme');
    } else {
      root.setAttribute('data-theme', palette);
    }
  }

  // system 模式下跟随系统翻转（旧浏览器走 addListener）。
  if (window.matchMedia) {
    var mq = window.matchMedia('(prefers-color-scheme: dark)');
    var onSchemeChange = function () {
      if (mode === 'system') {
        applyClasses(mq.matches);
        syncUI();
      }
    };
    if (mq.addEventListener) mq.addEventListener('change', onSchemeChange);
    else if (mq.addListener) mq.addListener(onSchemeChange);
  }

  function panelOpen() {
    return !!(panel && panel.classList.contains('is-open'));
  }

  function setPanel(open) {
    if (!panel) return;
    panel.classList.toggle('is-open', open);
    if (open) {
      panel.removeAttribute('hidden');
    } else {
      panel.setAttribute('hidden', '');
    }
    syncUI();
    if (open) {
      var current = panel.querySelector('.is-selected') || panel.querySelector('button');
      if (current) current.focus();
    }
  }

  function modalOpen() {
    return !!(window.AdaComponents && typeof AdaComponents.modalOpen === 'function' && AdaComponents.modalOpen());
  }

  applyPalette();
  applyMode();

  document.addEventListener('click', function (event) {
    var target = event.target;
    if (!target || !target.closest) return;

    var modeBtn = target.closest('[data-theme-mode]');
    if (modeBtn) {
      var next = modeBtn.getAttribute('data-theme-mode');
      if (MODES.indexOf(next) >= 0) {
        mode = next;
        write(MODE_KEY, mode);
        applyMode();
      }
      return;
    }

    var pick = target.closest('[data-theme-pick]');
    if (pick) {
      var wanted = pick.getAttribute('data-theme-pick');
      if (PALETTES.indexOf(wanted) >= 0) {
        palette = wanted;
        write(PALETTE_KEY, palette);
        applyPalette();
        syncUI();
      }
      return;
    }

    if (target.closest('[data-theme-panel-toggle]')) {
      setPanel(!panelOpen());
      return;
    }

    // rail 快切（theme.js 的委托先跑，类已切好）：回写 mode 保持两键一致。
    if (target.closest('[data-theme-toggle]')) {
      mode = root.classList.contains('dark') ? 'dark' : 'light';
      write(MODE_KEY, mode);
      syncUI();
      /* 不 return：继续走到下面的点外关闭，收起已打开的面板 */
    }

    if (panelOpen() && !target.closest('[data-theme-panel]')) {
      setPanel(false);
    }
  });

  document.addEventListener('keydown', function (event) {
    if (!event || event.key !== 'Escape') return;
    // modal 优先：模态打开时让路（19-ued §19.5.1）。
    if (modalOpen()) return;
    if (panelOpen()) {
      if (event.preventDefault) event.preventDefault();
      setPanel(false);
      var trigger = document.querySelector('[data-theme-panel-toggle]');
      if (trigger) trigger.focus();
    }
  });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () {
      panel = document.querySelector('[data-theme-panel]');
      syncUI();
    });
  } else {
    panel = document.querySelector('[data-theme-panel]');
    syncUI();
  }
})();
