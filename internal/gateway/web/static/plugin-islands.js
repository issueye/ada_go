/*
 * plugin-islands.js · 插件岛屿的浏览器引导（docs/20 P1-5）
 * ---------------------------------------------------------------------------
 * 服务端在 hydrate 贡献的根元素上输出：
 *   data-ada-plugin-island / data-ada-plugin / data-ada-module / data-ada-props
 * 这里只做三件事：
 *   1. 按 data-ada-module 动态 import 插件 bundle（同 URL 只加载一次）；
 *   2. 读 data-ada-props，并从页面隐藏字段补 CSRF（服务端没有把它写进 props）；
 *   3. 用同一份组件定义 mount(el, { hydrate:true, rootElement:true })。
 *
 * 单岛失败只标记该元素并保留 SSR DOM，不影响其他岛；没有 AdaRender 时直接退出。
 */
(function () {
  'use strict';

  if (typeof window === 'undefined' || !window.AdaRender) {
    return;
  }

  var AdaRender = window.AdaRender;
  var AdaUI = window.AdaUI || null;
  var AdaUIIso = window.AdaUIIso || null;
  var moduleCache = Object.create(null);

  function readProps(element) {
    try {
      var raw = element.getAttribute('data-ada-props') || '{}';
      var value = JSON.parse(raw);
      return value && typeof value === 'object' ? value : {};
    } catch (err) {
      return {};
    }
  }

  function readCSRF() {
    var input = document.querySelector('input[name="csrf_token"]');
    return input && typeof input.value === 'string' ? input.value : '';
  }

  function loadModule(url) {
    if (!moduleCache[url]) moduleCache[url] = import(url);
    return moduleCache[url];
  }

  function isComponentDef(value) {
    if (!value || typeof value !== 'object') return false;
    return typeof value.render === 'function' ||
      typeof value.setup === 'function' ||
      typeof value.template === 'string' ||
      (value.template && typeof value.template === 'object');
  }

  function hydrate(element) {
    if (!element || element.getAttribute('data-ada-mounted') === '1') return;
    var moduleURL = element.getAttribute('data-ada-module') || '';
    if (!moduleURL) return;

    var label = element.getAttribute('data-ada-plugin-island') || moduleURL;
    element.setAttribute('data-ada-mounted', '1');
    element.setAttribute('data-ada-loading', '1');

    loadModule(moduleURL).then(function (mod) {
      var entry = element.getAttribute('data-ada-entry') || '';
      var def = entry && mod && mod.islands ? mod.islands[entry] : null;
      if (!def) def = mod && (mod.default || mod.Island);
      if (!isComponentDef(def)) {
        throw new Error('bundle 没有 entry "' + entry + '" 对应的组件定义（需要 render/setup/template）');
      }
      var props = readProps(element);
      if (props.stores) {
        AdaRender.hydrateStores(props.stores);
        delete props.stores;
      }
      if (!props.app || typeof props.app !== 'object') props.app = {};
      if (!props.app.csrf) {
        var token = readCSRF();
        if (token) props.app.csrf = token;
      }

      var options = {
        render: function (h) {
          return h(def, props);
        },
      };
      var app = AdaUI && typeof AdaUI.createApp === 'function'
        ? AdaUI.createApp(options)
        : AdaRender.createApp(options);
      // 插件 bundle 里的 $C Icon 在浏览器要落到与 Go renderIcon 相同的内联 SVG；
      // 覆盖 app 级 Icon，不影响基座其它岛屿。
      if (AdaUIIso && AdaUIIso.components && AdaUIIso.components.IconAdapter) {
        app.component('Icon', AdaUIIso.components.IconAdapter);
      }
      app.mount(element, { hydrate: true, rootElement: true });
      element.removeAttribute('data-ada-loading');
    }).catch(function (err) {
      // 失败时保留 SSR DOM，并允许后续再次尝试（例如网络恢复后）。
      element.removeAttribute('data-ada-mounted');
      element.removeAttribute('data-ada-loading');
      element.setAttribute('data-ada-error', '1');
      if (window.console && typeof window.console.error === 'function') {
        window.console.error('[ada-islands] ' + label + ' hydrate 失败', err);
      }
    });
  }

  function boot() {
    var nodes = document.querySelectorAll('[data-ada-plugin-island]');
    for (var i = 0; i < nodes.length; i += 1) hydrate(nodes[i]);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }

  window.AdaPluginIslands = {
    hydrate: hydrate,
    boot: boot,
  };
})();
