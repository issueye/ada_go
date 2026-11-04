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

  // :hover 的安全探测：个别老引擎在 matches(':hover') 上抛错。
  function hoverMatch(element) {
    try {
      return element.matches(':hover');
    } catch (err) {
      return false;
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
          // 用户焦点或指针落在岛内时跳过本轮：整岛重渲染会打断选区与悬停
          // （19-ued §19.5.4）。下一轮 5s 间隔会再试。
          if (element && (element.contains(document.activeElement) || hoverMatch(element))) return;
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
          var enable = !row.enabled;
          parts.push(h('form', {
            method: 'post',
            action: '/extensions/' + encodeURIComponent(row.id || '') + (enable ? '/enable' : '/disable')
          },
            h('input', { type: 'hidden', name: 'csrf_token', value: this.csrf || '' }),
            h('input', { type: 'hidden', name: 'next', value: '/extensions' }),
            h(AdaUI.components.Button, {
              type: 'submit',
              size: 'sm',
              variant: enable ? 'primary' : 'secondary'
            }, enable ? '启用' : '停用')
          ));
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

  function transcriptThinking(h, text) {
    return h('details', {
      class: 'ada-disclosure',
      'data-kind': 'thinking',
      'data-state': 'closed'
    },
      h('summary', { class: 'ada-disclosure__summary' },
        h('span', { class: 'ada-disclosure__icon' }, h(AdaUI.components.Icon, { name: 'i-bulb', size: 14 })),
        h('span', { class: 'ada-disclosure__caret' }, h(AdaUI.components.Icon, { name: 'i-chevron', size: 12 })),
        h('span', { class: 'ada-disclosure__label' }, '思考')),
      h('div', { class: 'ada-disclosure__rail' },
        h('div', { class: 'ada-thinking' }, text || '')));
  }

  function transcriptAssistant(h, row) {
    var parts = row.parts.map(function (part) {
      if (part.type === 'thinking') return transcriptThinking(h, part.text);
      if (part.type === 'image') return h('span', { class: 'ada-msg__images' }, '[图片]');
      return h(AdaUI.components.Markdown, { nodes: part.nodes || [] });
    });
    return h('div', {
      class: 'ada-msg ada-msg--assistant',
      'data-item-key': row.key
    },
      h('span', { class: 'ada-avatar', title: '助手' },
        h(AdaUI.components.Icon, { name: 'i-bot', size: 14 })),
      h('div', { class: 'ada-msg__body markdown-body ada-markdown' }, parts));
  }

  function transcriptRow(h, row) {
    if (!row || !row.key) return null;
    var wrap = h('div', {
      class: 'ada-transcript__item',
      key: row.key,
      'data-item-key': row.key
    });
    if (row.kind === 'assistant' && Array.isArray(row.parts)) {
      wrap.c = [transcriptAssistant(h, row)];
      return wrap;
    }
    if (row.html) {
      wrap._html = row.html;
      return wrap;
    }
    return null;
  }

  function mountTranscript(element) {
    if (element.getAttribute('data-ada-mounted') === '1') return;
    var fallback = element.querySelector('#transcript');
    if (!fallback) return;
    var initial = parseState(element);
    var threadId = typeof initial.threadId === 'string' && initial.threadId
      ? initial.threadId
      : (fallback.getAttribute('data-thread-id') || '');
    var runStatus = typeof initial.runStatus === 'string' && initial.runStatus
      ? initial.runStatus
      : (fallback.getAttribute('data-run-status') || '');
    var rows = Array.isArray(initial.rows) ? initial.rows : [];
    if (!rows.length) {
      // 没有内联状态时退回读取 SSR 行（老页面/状态损坏的兜底）。
      for (var i = 0; i < fallback.children.length; i += 1) {
        var node = fallback.children[i];
        if (node.id === 'live-stream') continue;
        if (node.hasAttribute && node.hasAttribute('data-slot')) continue;
        if (!node.matches || !node.matches('.ada-msg, .ada-disclosure, .ada-notice')) continue;
        rows.push({
          key: node.getAttribute('data-item-key') || ('row:' + i),
          html: node.outerHTML
        });
      }
    }
    if (!rows.length) return;
    element.setAttribute('data-ada-mounted', '1');

    // 「加载更早消息」是 SSR 输出的普通链接（?before= 游标翻历史）；岛屿重建
    // 行时把它捡回来放到顶部，否则水合一次链接就没了。
    var olderLink = fallback.querySelector('.ada-history-more');
    var olderHref = olderLink ? olderLink.getAttribute('href') : '';

    var app = AdaUI.createApp({
      data: function () {
        return { rows: rows, threadId: threadId, runStatus: runStatus, olderHref: olderHref };
      },
      methods: {
        setRows: function (next, status) {
          this.rows = Array.isArray(next) ? next : [];
          if (status) this.runStatus = status;
        }
      },
      render: function (h) {
        var nodes = this.rows.map(function (row) {
          return transcriptRow(h, row);
        }).filter(Boolean);
        if (this.olderHref) {
          nodes.unshift(h('a', {
            class: 'ada-history-more',
            href: this.olderHref,
            key: 'history-more'
          }, '加载更早消息'));
        }
        nodes.push(h('div', {
          id: 'live-stream',
          class: 'live-stream',
          key: 'live-stream',
          'aria-live': 'polite',
          'aria-label': '实时输出',
          hidden: true
        }));
        return h('div', {
          id: 'transcript',
          class: 'ada-transcript',
          'data-thread-id': this.threadId,
          'data-run-status': this.runStatus
        }, nodes);
      }
    });
    app.mount(element);
    element._adaTranscriptApp = app._instance ? app._instance.proxy : app;
  }

  function mountWorkspaces(element) {
    if (element.getAttribute('data-ada-mounted') === '1') return;
    var initial = parseState(element);
    // 状态缺失/损坏时保留服务端回退，不要用空选择器覆盖它。
    if (!initial.current) return;
    element.setAttribute('data-ada-mounted', '1');

    var app = AdaUI.createApp({
      data: function () {
        return {
          roots: Array.isArray(initial.roots) ? initial.roots : [],
          recent: Array.isArray(initial.recent) ? initial.recent : [],
          entries: Array.isArray(initial.entries) ? initial.entries : [],
          current: typeof initial.current === 'string' ? initial.current : '',
          parentHref: typeof initial.parentHref === 'string' ? initial.parentHref : '',
          truncated: !!initial.truncated,
          omitted: initial.omitted || 0,
          selectable: !!initial.selectable,
          csrf: typeof initial.csrf === 'string' ? initial.csrf : ''
        };
      },
      template:
        '<div>' +
        '<div v-if="current" class="ada-picker-current">' +
        '<div class="min-w-0"><p class="ada-eyebrow">当前浏览目录</p>' +
        '<p class="ada-picker-current__path" :title="current">{{ current }}</p></div>' +
        '<form v-if="selectable" method="post" action="/workspaces/select">' +
        '<input type="hidden" name="csrf_token" :value="csrf">' +
        '<input type="hidden" name="path" :value="current">' +
        '<ui-button type="submit" variant="primary" size="sm" label="使用此目录"></ui-button>' +
        '</form>' +
        '<ui-badge v-else tone="neutral" label="非配置工作区" title="Gateway 的工作区在启动时固定"></ui-badge>' +
        '</div>' +
        '<div class="ada-picker">' +
        '<aside class="card ada-picker__side">' +
        '<div class="ada-picker__section"><p class="ada-eyebrow">磁盘与常用位置</p>' +
        '<template v-if="roots.length">' +
        '<a v-for="root in roots" :key="root.path" class="ada-picker__root" ' +
        ':class="[\'ada-picker__root--\' + root.kind, root.path === current ? \'is-active\' : \'\']" ' +
        ':href="root.href" :title="root.path">' +
        '<ui-icon name="i-folder" :size="13"></ui-icon>' +
        '<span class="ada-picker__root-label">{{ root.label }}</span>' +
        '<span class="ada-picker__root-path">{{ root.path }}</span></a>' +
        '</template>' +
        '<p v-else class="ada-picker__empty">没有可用的根目录</p></div>' +
        '<div class="ada-picker__section"><p class="ada-eyebrow">最近工作区</p>' +
        '<template v-if="recent.length">' +
        '<form v-for="item in recent" :key="item.path" method="post" action="/workspaces/select" class="ada-picker__recent-form">' +
        '<input type="hidden" name="csrf_token" :value="csrf">' +
        '<input type="hidden" name="path" :value="item.path">' +
        '<button class="ada-picker__recent" :class="{ \'is-active\': item.path === current }" type="submit" :title="item.path">' +
        '<span class="ada-picker__recent-name">{{ item.name }}</span>' +
        '<span class="ada-picker__recent-meta">{{ item.known ? item.sessions + " 会话" : "会话数未加载" }}{{ item.path === current ? " · 当前" : "" }}</span>' +
        '</button></form>' +
        '</template>' +
        '<p v-else class="ada-picker__empty">还没有带会话的工作区</p></div>' +
        '</aside>' +
        '<section class="card ada-picker__main">' +
        '<div class="ada-picker__crumbs">' +
        '<ui-button v-if="parentHref" :href="parentHref" variant="ghost" size="sm" label="上一级"></ui-button>' +
        '<span class="ada-picker__path">{{ current }}</span></div>' +
        '<form method="post" action="/workspaces/mkdir" class="ada-picker__mkdir">' +
        '<input type="hidden" name="csrf_token" :value="csrf">' +
        '<input type="hidden" name="parent" :value="current">' +
        '<input class="input" name="name" placeholder="新建文件夹名称" required autocomplete="off">' +
        '<ui-button type="submit" variant="secondary" size="sm" label="新建目录"></ui-button></form>' +
        '<p v-if="truncated" class="ada-picker__notice">目录项过多，已省略 {{ omitted }} 项；可用上方路径直接进入更深的目录。</p>' +
        '<div v-if="entries.length" class="ada-picker__body"><ul class="ada-picker__list">' +
        '<li v-for="entry in entries" :key="entry.path">' +
        '<a class="ada-picker__entry" :href="entry.href" :title="entry.path">' +
        '<ui-icon name="i-folder" :size="14"></ui-icon><span>{{ entry.name }}</span></a></li>' +
        '</ul></div>' +
        '<div v-else class="empty-state"><p class="text-sm font-medium text-ink">这个目录下没有子目录</p>' +
        '<p class="mt-1 text-xs leading-5 text-faint">可以直接选择当前工作区，或在上面新建一个文件夹。</p></div>' +
        '</section></div></div>'
    });

    app.mount(element);
  }

  function mountPluginDetail(element) {
    if (element.getAttribute('data-ada-mounted') === '1') return;
    var initial = parseState(element);
    if (!initial.plugin || !initial.plugin.id) return;
    element.setAttribute('data-ada-mounted', '1');
    var plugin = initial.plugin;
    plugin.versionLabel = plugin.version ? 'v' + plugin.version : '—';
    plugin.apiVersionLabel = plugin.apiVersion ? String(plugin.apiVersion) : '—';
    plugin.trustLabel = plugin.trust || '—';
    plugin.sourceLabel = plugin.source || '随包';
    plugin.pathLabel = plugin.path || '—';
    plugin.showDescription = !!plugin.description && plugin.description !== plugin.readme;
    plugin.readmeNodes = Array.isArray(plugin.readmeNodes) ? plugin.readmeNodes : [];
    plugin.files = Array.isArray(plugin.files) ? plugin.files : [];

    var app = AdaUI.createApp({
      allowHTML: true,
      data: function () {
        return {
          plugin: plugin,
          file: initial.file && typeof initial.file === 'object' ? initial.file : null,
          loading: false,
          fileError: '',
          showInfo: false,
          // Markdown 文件的「预览 / 源码」开关；初始值与无 JS 路径的 ?view=md 一致。
          preview: !!(initial.file && initial.file.showPreview),
          csrf: typeof initial.csrf === 'string' ? initial.csrf : '',
          fileSeq: 0
        };
      },
      methods: {
        openFile: function (path) {
          var self = this;
          if (!path || typeof window.fetch !== 'function') return;
          self.fileSeq += 1;
          var seq = self.fileSeq;
          self.loading = true;
          self.fileError = '';
          self.showInfo = false;
          window
            .fetch('/extensions/' + encodeURIComponent(self.plugin.id) + '/file?path=' + encodeURIComponent(path), {
              credentials: 'same-origin',
              headers: { Accept: 'application/json' }
            })
            .then(function (response) {
              return response.json().then(function (body) {
                if (!response.ok) throw new Error(body && body.error ? body.error : 'HTTP ' + response.status);
                return body;
              });
            })
            .then(function (data) {
              if (seq !== self.fileSeq) return;
              self.file = data;
              // 换文件回到源码态：预览是按文件内容渲染的，沿用上一个文件的开关会让人误以为新文件也是富文本。
              self.preview = false;
              self.loading = false;
            })
            .catch(function (error) {
              if (seq !== self.fileSeq) return;
              self.loading = false;
              self.fileError = (error && error.message) || '读取文件失败';
            });
        },
        toggleInfo: function () {
          this.showInfo = !this.showInfo;
        },
        // Markdown 文件在源码与渲染预览之间切换（预览 HTML 由服务端渲染并清洗）。
        togglePreview: function () {
          if (!this.file || !this.file.markdown) return;
          this.preview = !this.preview;
        }
      },
      template:
        '<div class="ada-plugin-detail">' +
        '<aside class="ada-plugin-detail__side">' +
        '<section class="card ada-plugin-tree-card" aria-label="插件目录">' +
        '<div class="ada-plugin-tree-card__head">' +
        '<h2 class="ada-plugin-section__title">插件目录 <span class="ada-plugin-section__meta">{{ plugin.files.length }} 个文件</span></h2>' +
        '</div>' +
        '<ul v-if="plugin.files.length" class="ada-plugin-tree">' +
        '<li v-for="f in plugin.files" :key="f.path" class="ada-plugin-tree__row" :class="f.depthClass || \'ada-plugin-file--d0\'">' +
        '<span v-if="f.dir" class="ada-plugin-tree__item is-dir" :title="f.path">' +
        '<span class="ada-plugin-tree__icon"><ui-icon name="i-folder" :size="13"></ui-icon></span>' +
        '<code class="ada-plugin-tree__path">{{ f.path }}</code></span>' +
        '<button v-else type="button" class="ada-plugin-tree__item" ' +
        ':class="{ \'is-active\': file && file.path === f.path }" :title="f.path" @click="openFile(f.path)">' +
        '<span class="ada-plugin-tree__icon"><ui-icon name="i-file" :size="13"></ui-icon></span>' +
        '<code class="ada-plugin-tree__path">{{ f.path }}</code>' +
        '<span class="ada-plugin-tree__size">{{ f.sizeLabel }}</span>' +
        '</button></li>' +
        '</ul>' +
        '<p v-else class="ada-plugin-tree__empty">这个插件没有可展示的包内文件。</p>' +
        '<button type="button" class="ada-plugin-tree-card__info" @click="toggleInfo">' +
        '{{ showInfo ? \'返回文件\' : \'插件信息\' }}</button>' +
        '</section>' +
        '</aside>' +
        '<div class="ada-plugin-detail__main">' +

        '<section v-if="!showInfo" class="card ada-plugin-viewer" aria-label="文件内容">' +
        '<header v-if="file" class="ada-plugin-viewer__head">' +
        '<code class="ada-plugin-viewer__path">{{ file.path }}</code>' +
        '<div class="ada-plugin-viewer__actions">' +
        '<span class="ada-plugin-viewer__meta">{{ file.sizeLabel }}<span v-if="file.truncated"> · 已截断</span></span>' +
        '<button v-if="file.markdown" type="button" class="ada-plugin-viewer__toggle" @click="togglePreview" ' +
        ':title="preview ? \'切换到源码\' : \'切换到渲染预览\'">{{ preview ? \'源码\' : \'预览\' }}</button>' +
        '</div>' +
        '</header>' +
        '<p v-if="loading" class="ada-plugin-viewer__empty">正在读取文件…</p>' +
        '<p v-else-if="fileError" class="ada-plugin-viewer__empty">{{ fileError }}</p>' +
        '<p v-else-if="file && file.error" class="ada-plugin-viewer__empty">{{ file.error }}</p>' +
        '<p v-else-if="file && file.binary" class="ada-plugin-viewer__empty">这是二进制文件，暂不展示内容。</p>' +
        '<div v-else-if="file && preview" class="markdown-body ada-markdown ada-plugin-file-preview" v-html="file.previewHtml"></div>' +
        '<pre v-else-if="file" class="ada-plugin-viewer__code chroma"><code v-html="file.html"></code></pre>' +
        '<p v-else class="ada-plugin-viewer__empty">这个插件没有可展示的包内文件。</p>' +
        '</section>' +

        '<section v-else class="card p-4 ada-plugin-panel">' +
        '<div class="flex flex-wrap items-center gap-2">' +
        '<ui-badge v-if="plugin.frontend" tone="brand" label="前端插件"></ui-badge>' +
        '<ui-badge v-if="plugin.backend" tone="neutral" label="后端插件"></ui-badge>' +
        '<ui-badge :tone="plugin.statusTone || \'neutral\'" :label="plugin.statusLabel || plugin.status || \'—\'"></ui-badge>' +
        '<ui-badge v-if="plugin.status" tone="neutral" mono :label="plugin.status"></ui-badge>' +
        '</div>' +
        '<p v-if="plugin.status === \'unused\'" class="mt-2 text-xs text-muted">当前产品没有默认启用这个插件；可在插件列表或本页手动启用。</p>' +
        '<p v-if="plugin.showDescription" class="mt-2 text-sm leading-6 text-muted">{{ plugin.description }}</p>' +
        '<div class="ada-plugin-stats">' +
        '<div class="ada-plugin-stat"><span class="ada-plugin-stat__value">{{ plugin.pages }}</span><span class="ada-plugin-stat__label">页面</span></div>' +
        '<div class="ada-plugin-stat"><span class="ada-plugin-stat__value">{{ plugin.tools }}</span><span class="ada-plugin-stat__label">工具</span></div>' +
        '<div class="ada-plugin-stat"><span class="ada-plugin-stat__value">{{ plugin.slots }}</span><span class="ada-plugin-stat__label">插槽</span></div>' +
        '</div>' +
        '<dl class="ada-plugin-meta">' +
        '<div><dt>版本</dt><dd>{{ plugin.versionLabel }}</dd></div>' +
        '<div><dt>API 版本</dt><dd>{{ plugin.apiVersionLabel }}</dd></div>' +
        '<div><dt>信任级别</dt><dd>{{ plugin.trustLabel }}</dd></div>' +
        '<div><dt>来源</dt><dd>{{ plugin.sourceLabel }}</dd></div>' +
        '<div class="ada-plugin-meta__wide"><dt>插件路径</dt><dd class="ada-plugin-meta__mono">{{ plugin.pathLabel }}</dd></div>' +
        '<div v-if="plugin.hooks"><dt>钩子</dt><dd>{{ plugin.hooks }}</dd></div>' +
        '<div v-if="plugin.skills"><dt>技能</dt><dd>{{ plugin.skills }}</dd></div>' +
        '<div v-if="plugin.nav"><dt>导航</dt><dd>{{ plugin.nav }}</dd></div>' +
        '<div class="ada-plugin-meta__wide"><dt>插件 ID</dt><dd class="ada-plugin-meta__mono">{{ plugin.id }}</dd></div>' +
        '</dl>' +
        '<div class="ada-plugin-section">' +
        '<h2 class="ada-plugin-section__title">README</h2>' +
        '<div v-if="plugin.readmeNodes.length" class="markdown-body ada-markdown ada-plugin-readme">' +
        '<ui-markdown :nodes="plugin.readmeNodes"></ui-markdown>' +
        '</div>' +
        '<div v-else-if="plugin.readme" class="markdown-body ada-markdown ada-plugin-readme">' +
        '<ui-markdown :text="plugin.readme"></ui-markdown>' +
        '</div>' +
        '<p v-else class="text-sm text-muted">这个插件没有提供 README。</p>' +
        '</div>' +
        '<p v-if="plugin.error" class="mt-3 text-xs text-danger-ink">{{ plugin.error }}</p>' +
        '</section>' +

        '</div>' +
        '</div>'
    });

    app.mount(element);
  }

  function boot() {
    var overview = document.querySelectorAll('[data-ada-island="overview"]');
    for (var i = 0; i < overview.length; i += 1) mountOverview(overview[i]);
    var extensions = document.querySelectorAll('[data-ada-island="extensions"]');
    for (var j = 0; j < extensions.length; j += 1) mountExtensions(extensions[j]);
    var workspaces = document.querySelectorAll('[data-ada-island="workspaces"]');
    for (var k = 0; k < workspaces.length; k += 1) mountWorkspaces(workspaces[k]);
    var detail = document.querySelectorAll('[data-ada-island="plugin-detail"]');
    for (var l = 0; l < detail.length; l += 1) mountPluginDetail(detail[l]);
    var transcript = document.querySelectorAll('[data-ada-island="transcript"]');
    for (var m = 0; m < transcript.length; m += 1) mountTranscript(transcript[m]);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }

  window.AdaIslands = {
    mountOverview: mountOverview,
    mountExtensions: mountExtensions,
    mountWorkspaces: mountWorkspaces,
    mountPluginDetail: mountPluginDetail,
    mountTranscript: mountTranscript
  };
})();
