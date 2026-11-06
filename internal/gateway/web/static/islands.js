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
        /* 与 templates/overview.html.tmpl 的 overview_partial 逐类对齐：
           ov-kpis / ov-recent / ov-thread；改一处必须改另一处。 */
        recentRows: function () {
          return (this.recent || []).slice(0, 5).map(function (row) {
            return {
              id: row.id || '',
              title: row.title || '新会话',
              workspace: row.workspace || '',
              running: !!row.running,
              updatedLabel: row.updatedLabel || '',
              href: '/threads/' + encodeURIComponent(row.id || '')
            };
          });
        },
        recentCount: function () { return (this.recent || []).length; }
      },
      methods: {
        refresh: function () {
          if (this.loading) return;
          if (typeof document !== 'undefined' && document.hidden) return;
          // 焦点或指针落在岛内时跳过本轮：整岛重渲染会打断选区与悬停。
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
              self.error = '';
              var next = {
                stats: Array.isArray(data.stats) ? data.stats : [],
                recent: Array.isArray(data.recent) ? data.recent : []
              };
              // 数据没变就不赋值：5s 一轮的整岛重渲染会打断选区/悬停。
              if (JSON.stringify(next) === JSON.stringify({ stats: self.stats, recent: self.recent })) return;
              self.stats = next.stats;
              self.recent = next.recent;
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
        '<div class="ov-island-root">' +
        '<ui-message v-if="error" tone="error" title="概览刷新失败">{{ error }}</ui-message>' +
        '<div class="ov-kpis">' +
        '<div v-for="stat in stats" :key="stat.label" class="ov-kpi" :data-tone="stat.tone">' +
        '<span class="ov-kpi__label">{{ stat.label }}</span>' +
        '<span :class="stat.numeric ? \'ov-kpi__value\' : \'ov-kpi__value ov-kpi__value--text\'">{{ stat.value }}</span>' +
        '<span class="ov-kpi__note">{{ stat.hint }}</span>' +
        '</div>' +
        '</div>' +
        '<section class="ov-recent" aria-label="最近会话">' +
        '<div class="ov-recent__head">' +
        '<div class="ov-recent__title"><span>最近会话</span><span class="ov-count">{{ recentCount }}</span></div>' +
        '<a class="ov-link" href="/">全部会话 →</a>' +
        '</div>' +
        '<div v-if="recentRows.length" class="ov-recent__list">' +
        '<a v-for="row in recentRows" :key="row.id" class="ov-thread" :href="row.href">' +
        '<span class="ov-thread__main">' +
        '<span class="ov-thread__title">{{ row.title }}' +
        '<span v-if="row.running" class="ov-chip ov-chip--warn">运行中</span></span>' +
        '<span class="ov-thread__path">{{ row.workspace }}</span>' +
        '</span>' +
        '<span class="ov-thread__time">{{ row.updatedLabel }}</span>' +
        '</a>' +
        '</div>' +
        '<div v-else class="ov-recent__empty">还没有会话。去「对话」页新建一个。</div>' +
        '<a class="ov-recent__more" href="/">查看全部会话 →</a>' +
        '</section>' +
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
          csrf: typeof initial.csrf === 'string' ? initial.csrf : '',
          query: '',
          filter: 'all'
        };
      },
      computed: {
        /* 分段计数与筛选全部在客户端算，SSR 与这里用同一套 ex-* 类。 */
        counts: function () {
          var enabled = 0;
          var list = this.plugins || [];
          list.forEach(function (p) { if (p.enabled) enabled += 1; });
          return { all: list.length, enabled: enabled, disabled: list.length - enabled };
        },
        visibleRows: function () {
          var q = String(this.query || '').trim().toLowerCase();
          var filter = this.filter;
          var csrf = this.csrf;
          return (this.plugins || []).filter(function (p) {
            if (filter === 'enabled' && !p.enabled) return false;
            if (filter === 'disabled' && p.enabled) return false;
            if (!q) return true;
            return String(p.name || '').toLowerCase().indexOf(q) >= 0 ||
              String(p.id || '').toLowerCase().indexOf(q) >= 0;
          }).map(function (p) {
            var chips = [];
            if (p.tools) chips.push({ label: p.tools + ' 工具', className: 'ex-chip' });
            if (p.pages) chips.push({ label: p.pages + ' 页面', className: 'ex-chip' });
            if (p.slots) chips.push({ label: p.slots + ' 插槽', className: 'ex-chip' });
            if (p.hasDashboard) chips.push({ label: 'dashboard', className: 'ex-chip ex-chip--ok' });
            if (!chips.length) chips.push({ label: '—', className: 'ex-row__dash' });
            var id = String(p.id || '');
            return {
              id: id,
              name: p.name || id,
              versionLabel: p.version ? 'v' + p.version : '—',
              statusLabel: p.statusLabel || p.status || '—',
              tone: p.statusTone || 'neutral',
              error: p.error || '',
              enabled: !!p.enabled,
              chips: chips,
              detailHref: '/extensions/' + encodeURIComponent(id),
              action: '/extensions/' + encodeURIComponent(id) + (p.enabled ? '/disable' : '/enable'),
              actionLabel: p.enabled ? '停用' : '启用',
              actionClass: p.enabled ? 'ex-action' : 'ex-action ex-action--primary',
              csrf: csrf
            };
          });
        }
      },
      methods: {
        onSearch: function (event) {
          this.query = event && event.target ? event.target.value : '';
        },
        setFilter: function (value) { this.filter = value; }
      },
      template:
        '<div class="ex-island-root">' +
        '<div class="ex-toolbar">' +
        '<label class="ex-search">' +
        '<input class="ex-search__input" type="text" placeholder="搜索插件名 / ID" ' +
        'aria-label="搜索插件" :value="query" @input="onSearch($event)">' +
        '<span class="ex-search__kbd">⌘K</span>' +
        '</label>' +
        '<div class="ex-seg" role="group" aria-label="按状态筛选">' +
        '<button type="button" class="ex-seg__item" :class="{\'is-active\': filter === \'all\'}" ' +
        ':aria-pressed="filter === \'all\' ? \'true\' : \'false\'" @click="setFilter(\'all\')">全部 {{ counts.all }}</button>' +
        '<button type="button" class="ex-seg__item" :class="{\'is-active\': filter === \'enabled\'}" ' +
        ':aria-pressed="filter === \'enabled\' ? \'true\' : \'false\'" @click="setFilter(\'enabled\')">已启用 {{ counts.enabled }}</button>' +
        '<button type="button" class="ex-seg__item" :class="{\'is-active\': filter === \'disabled\'}" ' +
        ':aria-pressed="filter === \'disabled\' ? \'true\' : \'false\'" @click="setFilter(\'disabled\')">未启用 {{ counts.disabled }}</button>' +
        '</div>' +
        '<span class="ex-toolbar__meta">{{ counts.all }} 个已安装</span>' +
        '</div>' +
        '<div v-if="visibleRows.length" class="ex-list">' +
        '<div class="ex-row ex-row--head">' +
        '<span>插件</span><span>版本</span><span>状态</span><span>能力</span><span>操作</span>' +
        '</div>' +
        '<div v-for="row in visibleRows" :key="row.id" class="ex-row" :data-enabled="row.enabled">' +
        '<div class="ex-row__main">' +
        '<a class="ex-row__name" :href="row.detailHref">{{ row.name }}</a>' +
        '<span class="ex-row__id">{{ row.id }}</span>' +
        '<span v-if="row.error" class="ex-row__error">{{ row.error }}</span>' +
        '</div>' +
        '<span class="ex-row__version">{{ row.versionLabel }}</span>' +
        '<span class="ex-row__status" :data-tone="row.tone"><span class="ex-dot"></span>{{ row.statusLabel }}</span>' +
        '<div class="ex-row__caps">' +
        '<span v-for="chip in row.chips" :key="chip.label" :class="chip.className">{{ chip.label }}</span>' +
        '</div>' +
        '<div class="ex-row__actions">' +
        '<form method="post" :action="row.action">' +
        '<input type="hidden" name="csrf_token" :value="row.csrf">' +
        '<input type="hidden" name="next" value="/extensions">' +
        '<button type="submit" :class="row.actionClass">{{ row.actionLabel }}</button>' +
        '</form>' +
        '<a class="ex-link" :href="row.detailHref">详情</a>' +
        '</div>' +
        '</div>' +
        '</div>' +
        '<div v-else class="ex-empty">' +
        '<p class="ex-empty__title">没有匹配的插件</p>' +
        '<p class="ex-empty__desc">换个关键词，或切回「全部」查看已安装的插件。</p>' +
        '</div>' +
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
    plugin.files = Array.isArray(plugin.files) ? plugin.files : [];
    plugin.readmeNodes = Array.isArray(plugin.readmeNodes) ? plugin.readmeNodes : [];
    // 一次性反馈用底部 toast；计时器留在闭包里，不进响应式 data。
    var toastTimer = null;

    var app = AdaUI.createApp({
      allowHTML: true,
      data: function () {
        return {
          plugin: plugin,
          file: initial.file && typeof initial.file === 'object' ? initial.file : null,
          loading: false,
          fileError: '',
          // Markdown 文件的「预览 / 源码」开关；初始值与无 JS 路径的 ?view=md 一致。
          preview: !!(initial.file && initial.file.showPreview),
          csrf: typeof initial.csrf === 'string' ? initial.csrf : '',
          fileSeq: 0,
          // 注意：data 里有 toast 字段，反馈方法必须叫 showToast，
          // 否则运行时 get 里 data 优先会遮蔽同名方法（点击无反应）。
          toast: { text: '', tone: 'ok', seq: 0 }
        };
      },
      computed: {
        /* 与 templates/plugin_detail.html.tmpl 的 pd-* 结构逐类对齐：
           服务端渲染的首屏和 hydrate 后的岛必须是同一套 DOM，挂载时不重排。 */
        statusText: function () {
          return this.plugin.enabled ? '已启用' : (this.plugin.statusLabel || this.plugin.status || '未启用');
        },
        statusValueClass: function () {
          return 'pd-field__value pd-field__value--' + (this.plugin.statusTone || 'neutral');
        },
        ownership: function () {
          if (this.plugin.frontend && this.plugin.backend) return '前端 + 后端';
          if (this.plugin.frontend) return '前端插件';
          if (this.plugin.backend) return '后端插件';
          return '—';
        },
        trustLabel: function () { return this.plugin.trust || '—'; },
        sourceLabel: function () { return this.plugin.source || '随包'; },
        pathLabel: function () { return this.plugin.path || '—'; },
        showDescription: function () {
          return !!this.plugin.description && this.plugin.description !== this.plugin.readme;
        },
        caps: function () {
          var p = this.plugin, out = [];
          if (p.tools) out.push({ label: p.tools + ' 工具', cls: 'pd-cap' });
          if (p.pages) out.push({ label: p.pages + ' 页面', cls: 'pd-cap' });
          if (p.slots) out.push({ label: p.slots + ' 插槽', cls: 'pd-cap' });
          if (p.hooks) out.push({ label: p.hooks + ' 钩子', cls: 'pd-cap' });
          if (p.skills) out.push({ label: p.skills + ' 技能', cls: 'pd-cap' });
          if (p.nav) out.push({ label: p.nav + ' 导航', cls: 'pd-cap' });
          if (p.hasDashboard) out.push({ label: 'dashboard', cls: 'pd-cap pd-cap--ok' });
          if (!out.length) out.push({ label: '—', cls: 'pd-cap pd-cap--empty' });
          return out;
        },
        fileBaseHref: function () {
          return '/extensions/' + encodeURIComponent(this.plugin.id) + '?file=' +
            encodeURIComponent(this.file ? this.file.path : '');
        },
        previewHref: function () { return this.fileBaseHref + '&view=md'; },
        sourceHref: function () { return this.fileBaseHref; },
        docFoot: function () {
          if (!this.file) return 'README · 渲染预览';
          var parts = [
            this.file.sizeLabel || '',
            this.file.markdown ? 'Markdown' : (this.file.language || '文本'),
            this.preview ? '渲染预览' : '源码'
          ];
          var out = [];
          parts.forEach(function (v) { if (v) out.push(v); });
          return out.join(' · ');
        }
      },
      methods: {
        fileHref: function (path) {
          return '/extensions/' + encodeURIComponent(this.plugin.id) + '?file=' + encodeURIComponent(path);
        },
        openFile: function (path) {
          var self = this;
          if (!path || typeof window.fetch !== 'function') return;
          self.fileSeq += 1;
          var seq = self.fileSeq;
          self.loading = true;
          self.fileError = '';
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
        setPreview: function (value) {
          if (!this.file || !this.file.markdown) return;
          this.preview = !!value;
        },
        showToast: function (text, tone) {
          var self = this;
          this.toast = { text: text, tone: tone || 'ok', seq: (this.toast.seq || 0) + 1 };
          if (toastTimer) window.clearTimeout(toastTimer);
          toastTimer = window.setTimeout(function () {
            self.toast = { text: '', tone: 'ok', seq: (self.toast.seq || 0) + 1 };
          }, tone === 'error' ? 4200 : 2400);
        },
        fallbackCopy: function (text, done) {
          var self = this;
          try {
            var ta = document.createElement('textarea');
            ta.value = text;
            ta.setAttribute('readonly', '');
            ta.style.position = 'fixed';
            ta.style.opacity = '0';
            document.body.appendChild(ta);
            ta.select();
            var copied = document.execCommand('copy');
            document.body.removeChild(ta);
            if (copied) { done(); return; }
          } catch (e) { /* 权限或环境不支持时给出手动兜底提示 */ }
          self.showToast('复制失败，请手动选中文本。', 'error');
        },
        copy: function (kind) {
          var self = this;
          var text = kind === 'path'
            ? (this.plugin.path || '')
            : (this.file && typeof this.file.content === 'string' ? this.file.content : '');
          if (!text) { this.showToast('没有可复制的内容。', 'error'); return; }
          var done = function () {
            self.showToast(kind === 'path' ? '已复制插件路径。' : '已复制文件内容。');
          };
          var fail = function () { self.fallbackCopy(text, done); };
          if (typeof navigator !== 'undefined' && navigator.clipboard && navigator.clipboard.writeText) {
            try {
              navigator.clipboard.writeText(text).then(done).catch(fail);
            } catch (e) { fail(); }
          } else { fail(); }
        }
      },
      template:
        '<div class="pd-root">' +
        '<section class="pd-meta" aria-label="插件信息" id="plugin-info">' +
        '<div class="pd-meta__row">' +
        '<div class="pd-field"><span class="pd-field__label">状态</span>' +
        '<span :class="statusValueClass">{{ statusText }}</span></div>' +
        '<div class="pd-field"><span class="pd-field__label">归属</span>' +
        '<span class="pd-field__value">{{ ownership }}</span></div>' +
        '<div class="pd-field"><span class="pd-field__label">信任级别</span>' +
        '<span class="pd-field__value">{{ trustLabel }}</span></div>' +
        '<div class="pd-field"><span class="pd-field__label">来源</span>' +
        '<span class="pd-field__value">{{ sourceLabel }}</span></div>' +
        '<div class="pd-field pd-field--grow"><span class="pd-field__label">能力</span>' +
        '<span class="pd-caps">' +
        '<span v-for="cap in caps" :key="cap.label" :class="cap.cls">{{ cap.label }}</span>' +
        '</span></div>' +
        '</div>' +
        '<div class="pd-meta__path">' +
        '<span class="pd-field__label">路径</span>' +
        '<code class="pd-path">{{ pathLabel }}</code>' +
        '<button type="button" class="pd-link" data-pd-copy="path" @click="copy(\'path\')">复制</button>' +
        '<span v-if="plugin.apiVersion" class="pd-meta__api">API v{{ plugin.apiVersion }}</span>' +
        '</div>' +
        '<p v-if="plugin.status === \'unused\'" class="pd-note">当前产品没有默认启用这个插件；可在插件列表或本页手动启用。</p>' +
        '<p v-if="showDescription" class="pd-note">{{ plugin.description }}</p>' +
        '<p v-if="plugin.error" class="pd-error">{{ plugin.error }}</p>' +
        '</section>' +

        '<div class="pd-main">' +
        '<aside class="pd-tree" aria-label="插件目录">' +
        '<div class="pd-tree__head">' +
        '<span class="pd-card__title">插件目录</span>' +
        '<span class="pd-count">{{ plugin.files.length }} 个文件</span>' +
        '</div>' +
        '<ul v-if="plugin.files.length" class="pd-tree__list">' +
        '<li v-for="f in plugin.files" :key="f.path" class="pd-tree__row" :class="f.depthClass">' +
        '<span v-if="f.dir" class="pd-tree__item is-dir" :title="f.path">' +
        '<span class="pd-tree__bar"></span><span class="pd-tree__bullet"></span>' +
        '<code class="pd-tree__name">{{ f.path }}</code></span>' +
        '<a v-else class="pd-tree__item" :class="{ \'is-active\': file && file.path === f.path }" ' +
        ':title="f.path" :href="fileHref(f.path)" :data-plugin-file-path="f.path" ' +
        '@click.prevent="openFile(f.path)">' +
        '<span class="pd-tree__bar"></span><span class="pd-tree__bullet"></span>' +
        '<code class="pd-tree__name">{{ f.path }}</code>' +
        '<span class="pd-tree__size">{{ f.sizeLabel }}</span></a>' +
        '</li>' +
        '</ul>' +
        '<p v-else class="pd-tree__empty">这个插件没有可展示的包内文件。</p>' +
        '<p class="pd-tree__hint">点击文件查看内容；Markdown 可切换渲染 / 源码。</p>' +
        '</aside>' +

        '<section class="pd-doc" aria-label="文件内容">' +
        '<div class="pd-doc__head">' +
        '<code class="pd-doc__name">{{ file ? file.path : \'README.md\' }}</code>' +
        '<span v-if="file" class="pd-doc__size">{{ file.sizeLabel }}' +
        '<span v-if="file.truncated"> · 已截断</span></span>' +
        '<div v-if="file && file.markdown" class="pd-seg" role="group" aria-label="视图切换">' +
        '<a class="pd-seg__item" :class="{ \'is-active\': preview }" rel="nofollow" ' +
        ':href="previewHref" title="切换到渲染预览" @click.prevent="setPreview(true)">预览</a>' +
        '<a class="pd-seg__item" :class="{ \'is-active\': !preview }" rel="nofollow" ' +
        ':href="sourceHref" title="切换到源码" @click.prevent="setPreview(false)">源码</a>' +
        '</div>' +
        '<button type="button" class="pd-link pd-doc__copy" data-pd-copy="file" ' +
        '@click="copy(\'file\')">复制</button>' +
        '</div>' +
        '<div class="pd-doc__body">' +
        '<p v-if="loading" class="pd-doc__empty">正在读取文件…</p>' +
        '<p v-else-if="fileError" class="pd-doc__empty">{{ fileError }}</p>' +
        '<div v-else-if="file && file.error" class="pd-doc__fallback">' +
        '<p class="pd-doc__error-line">{{ file.error }}</p>' +
        '<div v-if="plugin.readmeNodes.length || plugin.readme" class="pd-doc__fallback-body">' +
        '<span class="pd-doc__fallback-label">无法读取所选文件，以下为插件 README</span>' +
        '<div class="markdown-body ada-markdown pd-doc__preview">' +
        '<ui-markdown v-if="plugin.readmeNodes.length" :nodes="plugin.readmeNodes"></ui-markdown>' +
        '<ui-markdown v-else :text="plugin.readme"></ui-markdown>' +
        '</div></div></div>' +
        '<p v-else-if="file && file.binary" class="pd-doc__empty">这是二进制文件，暂不展示内容。</p>' +
        '<div v-else-if="file && preview" class="markdown-body ada-markdown pd-doc__preview" ' +
        'v-html="file.previewHtml"></div>' +
        '<pre v-else-if="file" class="pd-doc__code chroma" data-plugin-file-content>' +
        '<code v-html="file.html"></code></pre>' +
        '<div v-else-if="plugin.readmeNodes.length" class="markdown-body ada-markdown pd-doc__preview">' +
        '<ui-markdown :nodes="plugin.readmeNodes"></ui-markdown></div>' +
        '<div v-else-if="plugin.readme" class="markdown-body ada-markdown pd-doc__preview">' +
        '<ui-markdown :text="plugin.readme"></ui-markdown></div>' +
        '<p v-else class="pd-doc__empty">这个插件没有提供 README。</p>' +
        '</div>' +
        '<div class="pd-doc__foot"><span>{{ docFoot }}</span><span>只读</span></div>' +
        '</section>' +
        '</div>' +

        '<div class="pd-foot">' +
        '<div class="pd-foot__left">' +
        '<span class="pd-tip pd-tip--sm" ' +
        'data-tip="第三方插件的入口与能力以插件清单为准；停用后不再贡献工具与页面。" ' +
        'tabindex="0" role="img">i</span>' +
        '<span>第三方插件的入口与能力以插件清单为准；停用后不再贡献工具与页面。</span>' +
        '</div>' +
        '<span class="pd-foot__right">状态来自插件清单</span>' +
        '</div>' +
        '<div v-if="toast.text" :key="toast.seq" class="pd-toast" :data-tone="toast.tone">' +
        '{{ toast.text }}</div>' +
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
    // 服务地址来自当前页面的 host，不在模板里写死（本地端口会变）。
    var status = document.querySelectorAll('[data-ov-status]');
    for (var n = 0; n < status.length; n += 1) {
      status[n].textContent = '服务已连接 · ' + window.location.host;
    }
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
