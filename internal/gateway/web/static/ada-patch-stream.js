/*
 * ada-patch-stream.js · SSE VNode patch 客户端适配（docs/20 P2-5b）
 * ---------------------------------------------------------------------------
 * 插件 route 的 SSE 帧可以发送：
 *   event: patch
 *   data: {"patches":[{"key":"row:1","mode":"upsert","vnode":{...}}]}
 * 这里把帧应用到响应式列表，由 ada-render 的 keyed patch 复用 DOM。
 *
 * 用法：
 *   var stream = AdaPatchStream.create({
 *     element: document.getElementById('rows'),
 *     url: '/ext/my-plugin/api/rows',
 *     initial: [{ key: 'row:1', vnode: <VNode> }],
 *   });
 *   stream.close();
 */
(function (root, factory) {
  root.AdaPatchStream = factory(root.AdaRender);
})(typeof globalThis !== 'undefined' ? globalThis : this, function (AdaRender) {
  'use strict';

  if (!AdaRender || typeof AdaRender.createApp !== 'function') {
    throw new Error('[ada-patch-stream] 请先加载 ada-render.js');
  }

  function create(options) {
    options = options || {};
    if (!options.element) throw new Error('[ada-patch-stream] 缺少 element');
    if (!options.url && !options.source) throw new Error('[ada-patch-stream] 缺少 url/source');

    var initial = Array.isArray(options.initial) ? options.initial.slice() : [];
    var app = (options.createApp || AdaRender.createApp)({
      data: function () {
        return { items: initial };
      },
      render: function (h) {
        return h('div', { class: options.class || 'ada-patch-list' }, this.items.map(function (item) {
          return AdaRender.keyed(item.vnode, item.key);
        }));
      },
    });
    var proxy = app.mount(options.element, {
      hydrate: options.hydrate !== false,
      rootElement: !!options.rootElement,
    });

    var source = options.source || new EventSource(options.url);
    var closed = false;
    function onPatch(event) {
      if (closed) return;
      var data;
      try {
        data = JSON.parse(event && event.data ? event.data : '{}');
      } catch (err) {
        if (options.onError) options.onError(err);
        return;
      }
      var patches = Array.isArray(data) ? data : (data && data.patches);
      if (!Array.isArray(patches)) return;
      proxy.items = AdaRender.applyVNodePatches(proxy.items, patches);
    }
    source.addEventListener(options.eventName || 'patch', onPatch);

    return {
      app: app,
      proxy: proxy,
      source: source,
      close: function () {
        if (closed) return;
        closed = true;
        if (source && typeof source.close === 'function') source.close();
      },
    };
  }

  return { create: create };
});
