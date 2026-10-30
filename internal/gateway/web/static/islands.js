/*
 * islands.js · 把 SSR 页面里的标记区域接到 AdaUI
 * ---------------------------------------------------------------------------
 * 服务端仍然输出完整 HTML；这里只接管 `[data-ada-island]`，没有 JS 时回退保持原样。
 * 首个岛是概览页：统计卡 + 最近会话，每 5 秒从 /partials/dashboard.json 刷新。
 *
 * 依赖加载顺序：ada-render.js → ada-ui.js → islands.js。
 */
(function () {
  'use strict';

  if (typeof window === 'undefined' || !window.AdaRender || !window.AdaUI) return;

  var AdaRender = window.AdaRender;
  var AdaUI = window.AdaUI;

  function parseState(element) {
    try {
      var raw = element.getAttribute('data-ada-state') || '{}';
      var value = JSON.parse(raw);
      return value && typeof value === 'object' ? value : {};
    } catch (error) {
      return {};
    }
  }

  function mountOverview(element) {
    if (element.getAttribute('data-ada-mounted') === '1') return;
    element.setAttribute('data-ada-mounted', '1');
    var initial = parseState(element);

    var app = AdaUI.createApp({
      data: function () {
        return {
          stats: Array.isArray(initial.stats) ? initial.stats : [],
          recent: Array.isArray(initial.recent) ? initial.recent : [],
          error: '',
          loading: false,
          timer: null
        };
      },
      computed: {
        columns: function () {
          var h = AdaRender.h;
          return [
            { key: 'updatedLabel', label: '更新时间', class: 'text-xs text-muted' },
            {
              key: 'title',
              label: '会话',
              render: function (row) {
                return h('a', {
                  class: 'link text-xs',
                  href: '/threads/' + encodeURIComponent(row.id || '')
                }, row.title || '新会话');
              }
            },
            { key: 'workspace', label: '工作区', class: 'text-xs text-muted' }
          ];
        }
      },
      methods: {
        refresh: function () {
          if (this.loading) return;
          if (typeof document !== 'undefined' && document.hidden) return;
          if (typeof window.fetch !== 'function') return;
          var self = this;
          this.loading = true;
          window.fetch('/partials/dashboard.json', {
            credentials: 'same-origin',
            headers: { Accept: 'application/json' }
          })
            .then(function (response) {
              if (!response.ok) {
                return response.json().then(function (body) {
                  throw new Error(body && body.error ? body.error : 'HTTP ' + response.status);
                });
              }
              return response.json();
            })
            .then(function (data) {
              if (!data || typeof data !== 'object') return;
              self.stats = Array.isArray(data.stats) ? data.stats : [];
              self.recent = Array.isArray(data.recent) ? data.recent : [];
              self.error = '';
            })
            .catch(function (error) {
              self.error = error && error.message ? error.message : '无法刷新概览';
            })
            .then(function () {
              self.loading = false;
            });
        }
      },
      mounted: function () {
        this.refresh();
        var self = this;
        this.timer = window.setInterval(function () { self.refresh(); }, 5000);
      },
      unmounted: function () {
        if (this.timer) {
          window.clearInterval(this.timer);
          this.timer = null;
        }
      },
      template:
        '<div>' +
        '<ui-message v-if="error" tone="error" title="概览刷新失败">{{ error }}</ui-message>' +
        '<div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">' +
        '<ui-card v-for="stat in stats" :key="stat.label">' +
        '<ui-badge :tone="stat.tone || \'neutral\'" :label="stat.label"></ui-badge>' +
        '<p class="mt-3 text-2xl font-semibold tracking-tight text-ink">{{ stat.value }}</p>' +
        '<p class="mt-1 text-xs leading-4 text-muted">{{ stat.hint }}</p>' +
        '</ui-card>' +
        '</div>' +
        '<ui-card class="mt-4" title="最近会话" subtitle="点击进入会话详情并发送消息">' +
        '<template #header><ui-spinner v-if="loading" :size="14" label="刷新中"></ui-spinner>' +
        '<ui-button href="/" variant="ghost" size="sm" label="全部会话"></ui-button></template>' +
        '<ui-table v-if="recent.length" :columns="columns" :rows="recent" row-key="id"></ui-table>' +
        '<ui-empty v-else icon="i-chat" title="还没有会话" description="去「对话」页新建一个。"></ui-empty>' +
        '</ui-card>' +
        '</div>'
    });

    app.mount(element);
  }

  function mountExtensions(element) {
    if (element.getAttribute('data-ada-mounted') === '1') return;
    element.setAttribute('data-ada-mounted', '1');
    var initial = parseState(element);

    var app = AdaUI.createApp({
      data: function () {
        return {
          plugins: Array.isArray(initial.plugins) ? initial.plugins : [],
          csrf: typeof initial.csrf === 'string' ? initial.csrf : ''
        };
      },
      computed: {
        columns: function () {
          var h = AdaRender.h;
          var self = this;
          return [
            {
              label: '插件',
              render: function (row) {
                var nodes = [
                  h('a', {
                    class: 'font-medium text-ink',
                    href: '/extensions/' + encodeURIComponent(row.id || '')
                  }, row.name || row.id),
                  h('p', { class: 'mt-1 font-mono text-xs text-faint' }, row.id)
                ];
                if (row.error) nodes.push(h('p', { class: 'mt-1 text-xs text-danger-ink' }, row.error));
                return h('div', null, nodes);
              }
            },
            {
              label: '版本',
              render: function (row) {
                if (!row.version) return h('span', { class: 'text-xs text-faint' }, '—');
                return h(AdaUI.components.Badge, { tone: 'neutral', mono: true, label: 'v' + row.version });
              }
            },
            {
              label: '状态',
              render: function (row) {
                return h(AdaUI.components.Badge, {
                  tone: row.statusTone || 'neutral',
                  label: row.statusLabel || row.status || '—'
                });
              }
            },
            {
              label: '能力',
              render: function (row) {
                var parts = [];
                if (row.tools) parts.push(h('span', { class: 'mr-3' }, row.tools + ' 工具'));
                if (row.pages) parts.push(h('span', { class: 'mr-3' }, row.pages + ' 页面'));
                if (row.slots) parts.push(h('span', { class: 'mr-3' }, row.slots + ' 插槽'));
                if (row.hasDashboard) parts.push(h(AdaUI.components.Badge, { tone: 'neutral', label: 'dashboard' }));
                if (!parts.length) parts.push(h('span', { class: 'text-faint' }, '—'));
                return h('div', { class: 'text-xs text-muted' }, parts);
              }
            },
            {
              label: '操作',
              render: function (row) { return self.actions(h, row); }
            }
          ];
        }
      },
      methods: {
        actions: function (h, row) {
          var parts = [];
          if (row.backend && row.status !== 'unused') {
            var enable = !row.enabled;
            parts.push(h('form', {
              method: 'post',
              action: '/extensions/' + encodeURIComponent(row.id || '') + (enable ? '/enable' : '/disable')
            },
              h('input', { type: 'hidden', name: 'csrf_token', value: this.csrf || '' }),
              h(AdaUI.components.Button, {
                type: 'submit',
                size: 'sm',
                variant: enable ? 'primary' : 'secondary'
              }, enable ? '启用' : '停用')
            ));
          }
          parts.push(h('a', {
            class: 'link text-xs',
            href: '/extensions/' + encodeURIComponent(row.id || '')
          }, '详情'));
          return h('div', { class: 'flex items-center gap-2' }, parts);
        }
      },
      template:
        '<div>' +
        '<ui-card v-if="plugins.length" class="overflow-hidden" :padding="false">' +
        '<ui-table :columns="columns" :rows="plugins" row-key="id"></ui-table>' +
        '</ui-card>' +
        '<ui-empty v-else icon="i-plug" title="没有插件" ' +
        'description="插件可能尚未装配，或网关暂时不可用。查看服务端日志确认。"></ui-empty>' +
        '</div>'
    });

    app.mount(element);
  }

  function boot() {
    var overview = document.querySelectorAll('[data-ada-island="overview"]');
    for (var i = 0; i < overview.length; i += 1) mountOverview(overview[i]);
    var extensions = document.querySelectorAll('[data-ada-island="extensions"]');
    for (var j = 0; j < extensions.length; j += 1) mountExtensions(extensions[j]);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }

  window.AdaIslands = { mountOverview: mountOverview, mountExtensions: mountExtensions };
})();
