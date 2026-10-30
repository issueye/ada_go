/*
 * FilePicker · 选择工作区的弹窗组件
 * ---------------------------------------------------------------------------
 * 只依赖网关的只读目录 API：GET /api/fs/list?path=...
 *
 * 安全边界由服务端负责：只能浏览已配置工作区的子树，路径还会 EvalSymlinks；
 * 这个组件只负责把 roots / entries 渲染出来，并把选中的工作区根回传给调用方。
 *
 * 用法：
 *   window.AdaFilePicker.open({
 *     current: "/workspace/ada_go",
 *     onSelect: function (workspace) { ... }
 *   });
 */
(function () {
  'use strict';

  var modal = null;
  var body = null;
  var onSelect = null;
  var state = emptyState();

  function emptyState() {
    return {
      path: '',
      parent: '',
      roots: [],
      workspaces: [],
      entries: [],
      selectable: false,
      truncated: false,
      omitted: 0,
      loading: false,
      error: ''
    };
  }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function icon(name, size) {
    var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'ada-icon');
    svg.setAttribute('width', String(size));
    svg.setAttribute('height', String(size));
    svg.setAttribute('aria-hidden', 'true');
    var use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', '#i-' + name);
    svg.appendChild(use);
    return svg;
  }

  function samePath(a, b) {
    return String(a || '').toLowerCase() === String(b || '').toLowerCase();
  }

  function baseName(path) {
    var parts = String(path || '').split(/[\\/]/).filter(Boolean);
    return parts.length ? parts[parts.length - 1] : path;
  }

  function ensureModal() {
    if (modal) return;
    modal = document.createElement('ada-modal');
    modal.className = 'ada-modal ada-file-picker';
    modal.setAttribute('data-modal', 'file-picker');
    modal.setAttribute('hidden', '');

    var backdrop = el('div', 'ada-modal__backdrop');
    backdrop.setAttribute('data-modal-close', '');

    var panel = el('div', 'ada-modal__panel ada-modal__panel--wide ada-file-picker__panel');
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-modal', 'true');
    panel.setAttribute('aria-labelledby', 'ada-file-picker-title');

    var head = el('div', 'ada-file-picker__head');
    var title = el('h2', 'ada-modal__title', '选择工作区');
    title.id = 'ada-file-picker-title';
    var close = el('button', 'wp-picker__close', '×');
    close.type = 'button';
    close.setAttribute('data-modal-close', '');
    close.setAttribute('aria-label', '关闭');
    close.setAttribute('title', '关闭');
    head.appendChild(title);
    head.appendChild(close);

    body = el('div', 'wp-picker');
    body.id = 'ada-file-picker-body';
    panel.appendChild(head);
    panel.appendChild(body);
    modal.appendChild(backdrop);
    modal.appendChild(panel);
    document.body.appendChild(modal);
    modal.addEventListener('click', onClick);
  }

  function openModal() {
    if (typeof modal.open === 'function') {
      modal.open();
    } else {
      modal.removeAttribute('hidden');
      modal.classList.add('is-open');
      document.body.classList.add('ada-modal-open');
    }
  }

  function closeModal() {
    if (typeof modal.close === 'function') {
      modal.close();
    } else {
      modal.setAttribute('hidden', '');
      modal.classList.remove('is-open');
      document.body.classList.remove('ada-modal-open');
    }
  }

  function load(path) {
    state.loading = true;
    state.error = '';
    render();
    var url = '/api/fs/list';
    if (path) url += '?path=' + encodeURIComponent(path);
    fetch(url, { credentials: 'same-origin', headers: { Accept: 'application/json' } })
      .then(function (response) {
        return response
          .json()
          .catch(function () {
            return {};
          })
          .then(function (data) {
            if (!response.ok) {
              throw new Error(data.error || '读取目录失败（HTTP ' + response.status + '）');
            }
            return data;
          });
      })
      .then(function (data) {
        state.loading = false;
        state.path = data.path || path || '';
        state.parent = data.parent || '';
        state.roots = Array.isArray(data.roots) ? data.roots : [];
        state.workspaces = Array.isArray(data.workspaces) ? data.workspaces : [];
        state.entries = Array.isArray(data.entries) ? data.entries : [];
        state.selectable = !!data.selectable;
        state.truncated = !!data.truncated;
        state.omitted = Number(data.omitted || 0);
        render();
      })
      .catch(function (err) {
        state.loading = false;
        state.error = err && err.message ? err.message : String(err);
        render();
      });
  }

  function renderChips(label, items) {
    var section = el('div', 'wp-picker__section');
    section.appendChild(el('p', 'wp-picker__section-label', label));
    var wrap = el('div', 'wp-picker__roots');
    items.forEach(function (root) {
      var button = el('button', 'wp-picker__root' + (samePath(root.path, state.path) ? ' is-active' : ''), root.label);
      button.type = 'button';
      button.title = root.path;
      button.setAttribute('data-fp-root', root.path);
      wrap.appendChild(button);
    });
    section.appendChild(wrap);
    return section;
  }

  function renderRoots() {
    var fragment = document.createDocumentFragment();
    if (state.roots.length) fragment.appendChild(renderChips('磁盘', state.roots));
    if (state.workspaces.length) fragment.appendChild(renderChips('工作区', state.workspaces));
    return fragment;
  }

  function renderPathbar() {
    var bar = el('div', 'wp-picker__pathbar');
    var up = el('button', 'wp-picker__up', '↑');
    up.type = 'button';
    up.title = '上一级';
    up.setAttribute('aria-label', '上一级');
    up.disabled = !state.parent;
    up.setAttribute('data-fp-up', '1');
    var path = el('div', 'wp-picker__path', state.path || '先选择磁盘或工作区');
    path.title = state.path;
    bar.appendChild(up);
    bar.appendChild(path);
    return bar;
  }

  function renderList() {
    var list = el('div', 'wp-picker__list');
    if (state.loading) {
      list.appendChild(el('p', 'wp-picker__loading', '正在读取目录…'));
      return list;
    }
    if (state.error) {
      list.appendChild(el('p', 'wp-picker__error', state.error));
      var retry = el('button', 'ada-btn ada-btn--secondary ada-btn--sm', '重试');
      retry.type = 'button';
      retry.setAttribute('data-fp-retry', '1');
      list.appendChild(retry);
      return list;
    }
    if (!state.entries.length) {
      list.appendChild(el('p', 'wp-picker__empty', state.path ? '这个目录下没有子目录' : '先选择一个磁盘或工作区'));
      return list;
    }
    state.entries.forEach(function (entry) {
      var row = el('button', 'wp-picker__entry');
      row.type = 'button';
      row.title = entry.path;
      row.setAttribute('data-fp-entry', entry.path);
      row.appendChild(icon('folder', 14));
      row.appendChild(el('span', 'wp-picker__entry-name', entry.name));
      row.appendChild(el('span', 'wp-picker__entry-hint', '进入'));
      list.appendChild(row);
    });
    return list;
  }

  function renderFoot() {
    var foot = el('div', 'wp-picker__foot');
    var current = el('div', 'wp-picker__current', '当前：');
    var strong = el('strong', '', state.path || '未选择');
    current.appendChild(strong);
    var actions = el('div', 'wp-picker__actions');

    var cancel = el('button', 'ada-btn ada-btn--ghost ada-btn--sm', '取消');
    cancel.type = 'button';
    cancel.setAttribute('data-modal-close', '');

    var choose = el('button', 'ada-btn ada-btn--primary ada-btn--sm', '使用此工作区');
    choose.type = 'button';
    choose.disabled = !state.selectable;
    choose.setAttribute('data-fp-select', '1');
    if (!state.selectable) choose.title = '请先选择一个目录';

    actions.appendChild(cancel);
    actions.appendChild(choose);
    foot.appendChild(current);
    foot.appendChild(actions);
    return foot;
  }

  function render() {
    if (!body) return;
    body.textContent = '';
    body.appendChild(renderRoots());
    body.appendChild(renderPathbar());
    if (state.truncated) {
      body.appendChild(el('p', 'wp-picker__notice', '目录项过多，已省略 ' + state.omitted + ' 项。'));
    }
    body.appendChild(renderList());
    body.appendChild(renderFoot());
  }

  function onClick(event) {
    var target = event.target;
    if (!target || !target.closest) return;
    var root = target.closest('[data-fp-root]');
    if (root) {
      load(root.getAttribute('data-fp-root'));
      return;
    }
    var entry = target.closest('[data-fp-entry]');
    if (entry) {
      load(entry.getAttribute('data-fp-entry'));
      return;
    }
    if (target.closest('[data-fp-up]')) {
      if (state.parent) load(state.parent);
      return;
    }
    if (target.closest('[data-fp-retry]')) {
      load(state.path);
      return;
    }
    if (target.closest('[data-fp-select]')) {
      if (!state.selectable || !onSelect) return;
      var name = baseName(state.path);
      var known = state.workspaces.concat(state.roots);
      for (var i = 0; i < known.length; i += 1) {
        if (samePath(known[i].path, state.path)) {
          name = known[i].label;
          break;
        }
      }
      onSelect({ path: state.path, name: name });
      closeModal();
    }
  }

  function open(options) {
    ensureModal();
    onSelect = options && typeof options.onSelect === 'function' ? options.onSelect : null;
    state = emptyState();
    // Windows 习惯：先选盘符，再进入目录；工作区作为快捷入口单独列出。
    openModal();
    load('');
  }

  window.AdaFilePicker = { open: open };
})();
