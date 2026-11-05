/*
 * ada-ui-iso.js · 可服务端执行的组件子集（docs/20 P2-1）
 * ---------------------------------------------------------------------------
 * 与 ada-ui.js 的区别只有一条：所有组件都用 render(h) 实现，不用字符串模板，
 * 因此同一份定义可以在 moejs（无 DOM）与浏览器里执行。
 *
 * 只使用 VNode 白名单里的原生标签与属性；不依赖 $C 宿主组件。插件通过
 * `ada:ui` 导入这些组件即可得到 SSR + hydrate 的同构 DOM。
 *
 * 依赖加载顺序：ada-render.js → ada-ui-iso.js。
 */
(function (root, factory) {
  var AdaRender = root.AdaRender;
  root.AdaUIIso = factory(AdaRender);
})(typeof globalThis !== 'undefined' ? globalThis : this, function (AdaRender) {
  'use strict';

  if (!AdaRender || typeof AdaRender.defineComponent !== 'function') {
    throw new Error('[ada-ui-iso] 请先加载 ada-render.js');
  }

  var defineComponent = AdaRender.defineComponent;
  var components = {};
  var BOOL = { default: false, type: Boolean };
  var BOOL_TRUE = { default: true, type: Boolean };

  function register(name, definition) {
    definition.name = definition.name || name;
    var normalized = defineComponent(definition);
    components[name] = normalized;
    return normalized;
  }

  function slot(self, name) {
    var fn = self && self.$slots ? self.$slots[name] : null;
    var out = typeof fn === 'function' ? fn() : null;
    return out && out.length ? out : null;
  }

  function slotOrLabel(self) {
    var nodes = slot(self, 'default');
    if (nodes) return nodes;
    return self && self.label ? [self.label] : null;
  }

  /* 图标：服务端保留 $C Icon（Go 渲染内联 SVG）；浏览器用 IconAdapter 输出
     同一套内联 path。插件不能直接写 svg（VNode 白名单禁止），必须走这个桥。 */
  var ICON_PATHS = {
    'i-list': [
      ['path', { d: 'm3 7 2 2 4-4' }],
      ['path', { d: 'm3 17 2 2 4-4' }],
      ['path', { d: 'M13 6h8M13 12h8M13 18h8' }],
    ],
    'i-copy': [
      ['rect', { x: 9, y: 9, width: 12, height: 12, rx: 2 }],
      ['path', { d: 'M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1' }],
    ],
    'i-chevron-up': [['path', { d: 'm6 15 6-6 6 6' }]],
    'i-check-circle': [
      ['circle', { cx: 12, cy: 12, r: 9 }],
      ['path', { d: 'm8.5 12.5 2.5 2.5 4.5-5' }],
    ],
    'i-arrow-right': [
      ['path', { d: 'M5 12h14' }],
      ['path', { d: 'm12 5 7 7-7 7' }],
    ],
    'i-circle': [['circle', { cx: 12, cy: 12, r: 9 }]],
  };

  function iconSize(value) {
    var size = Number(value || 14);
    if (!isFinite(size)) size = 14;
    size = Math.round(size);
    if (size < 8) size = 8;
    if (size > 32) size = 32;
    return size;
  }

  function iconVNode(h, name, size) {
    var children = [];
    var shapes = ICON_PATHS[name] || [];
    for (var i = 0; i < shapes.length; i += 1) {
      children.push(h(shapes[i][0], shapes[i][1]));
    }
    var px = iconSize(size);
    return h('svg', {
      class: 'ada-icon',
      width: px,
      height: px,
      viewBox: '0 0 24 24',
      fill: 'none',
      stroke: 'currentColor',
      'stroke-width': '1.75',
      'stroke-linecap': 'round',
      'stroke-linejoin': 'round',
      'aria-hidden': 'true',
    }, children);
  }

  var IconAdapter = defineComponent({
    name: 'IconAdapter',
    props: { name: { default: '' }, size: { default: 14 } },
    render: function (h) {
      return iconVNode(h, this.name, this.size);
    },
  });
  components.IconAdapter = IconAdapter;

  var host = AdaRender.host;
  var Icon = register('Icon', {
    props: { name: { default: '' }, size: { default: 14 } },
    render: function () {
      // 服务端：保持 $C，由 Go 的 renderIcon 输出白名单内联 SVG；
      // 浏览器：app 注册的 IconAdapter 负责同样的 DOM。
      return host('Icon', { name: this.name, size: iconSize(this.size) });
    },
  });

  var TODO_STATUS = {
    done: { className: 'is-done', icon: 'i-check-circle' },
    in_progress: { className: 'is-running', icon: 'i-arrow-right' },
    pending: { className: 'is-pending', icon: 'i-circle' },
  };

  function todoStepTitle(step) {
    var keys = ['step', 'text', 'title'];
    for (var i = 0; i < keys.length; i += 1) {
      var value = step[keys[i]];
      if (typeof value === 'string' && value.trim()) return value;
    }
    return '';
  }

  function todoStatus(value) {
    var text = typeof value === 'string' ? value.toLowerCase().trim() : '';
    if (text === 'done' || text === 'completed') return TODO_STATUS.done;
    if (text === 'in_progress' || text === 'in-progress' || text === 'active' || text === 'running') {
      return TODO_STATUS.in_progress;
    }
    return TODO_STATUS.pending;
  }

  var TodoList = register('TodoList', {
    props: {
      steps: { default: function () { return []; } },
      notes: { default: '' },
    },
    render: function (h) {
      var steps = Array.isArray(this.steps) ? this.steps : [];
      var children = [];
      var notes = typeof this.notes === 'string' ? this.notes : '';
      if (notes.trim()) {
        children.push(h('p', { class: 'ada-todo__notes' }, [h('span', null, '备注：'), notes]));
      }
      children.push(h('ol', { class: 'ada-todo__list' }, steps.map(function (step) {
        var item = step && typeof step === 'object' ? step : {};
        var status = todoStatus(item.status);
        return h('li', { class: 'ada-todo__step ' + status.className }, [
          h(Icon, { name: status.icon, size: 12 }),
          h('span', null, todoStepTitle(item)),
        ]);
      })));
      return h('div', { class: 'ada-todo__list-wrap' }, children);
    },
  });

  var NOTICE_ICON = {
    neutral: 'i-circle',
    info: 'i-circle',
    brand: 'i-circle',
    success: 'i-check-circle',
    warn: 'i-circle',
    danger: 'i-circle',
  };

  var Notice = register('Notice', {
    props: {
      tone: { default: 'neutral' },
      icon: { default: '' },
    },
    render: function (h) {
      var tone = this.tone || 'neutral';
      var iconName = this.icon || NOTICE_ICON[tone] || 'i-circle';
      return h('div', { class: ['ada-notice', 'ada-notice--' + tone] }, [
        h('div', { class: 'ada-notice__row' }, [
          h(Icon, { name: iconName, size: 14 }),
          h('span', null, slot(this, 'default') || ''),
        ]),
      ]);
    },
  });

  var Badge = register('Badge', {
    props: {
      tone: { default: 'neutral' },
      mono: BOOL,
      label: { default: '' },
      title: { default: '' },
    },
    render: function (h) {
      return h('span', {
        class: ['badge', 'badge-' + (this.tone || 'neutral'), this.mono ? 'badge-mono' : ''],
        title: this.title || null,
      }, slotOrLabel(this));
    },
  });

  var Card = register('Card', {
    props: {
      title: { default: '' },
      subtitle: { default: '' },
      padding: BOOL_TRUE,
      class: { default: '' },
    },
    render: function (h) {
      var header = slot(this, 'header');
      var footer = slot(this, 'footer');
      var body = slot(this, 'default');
      var children = [];
      if (this.title || this.subtitle || header) {
        var headChildren = [
          h('div', null, [
            this.title ? h('h2', { class: 'section-title' }, this.title) : null,
            this.subtitle ? h('p', { class: 'section-sub' }, this.subtitle) : null,
          ]),
        ];
        if (header) headChildren.push(h('div', { class: 'flex items-center gap-2' }, header));
        children.push(h('div', { class: 'section-head border-b border-line px-4 py-3' }, headChildren));
      }
      children.push(h('div', { class: this.padding ? 'p-4' : '' }, body));
      if (footer) children.push(h('div', { class: 'border-t border-line px-4 py-3' }, footer));
      return h('section', { class: ['card', this.class] }, children);
    },
  });

  var Button = register('Button', {
    props: {
      variant: { default: 'secondary' },
      size: { default: 'md' },
      type: { default: 'button' },
      href: { default: '' },
      disabled: BOOL,
      loading: BOOL,
      block: BOOL,
      label: { default: '' },
    },
    computed: {
      classes: function () {
        return [
          'ada-btn',
          'ada-btn--' + (this.variant || 'secondary'),
          'ada-btn--' + (this.size || 'md'),
          this.block ? 'ada-btn--block' : '',
        ];
      },
    },
    methods: {
      onClick: function (event) {
        if (this.disabled || this.loading) {
          if (event) {
            event.preventDefault();
            event.stopPropagation();
          }
          return;
        }
        this.$emit('click', event);
      },
    },
    render: function (h) {
      var body = slotOrLabel(this);
      var children = body ? [h('span', { class: 'ada-btn__label' }, body)] : [];
      if (this.href) {
        return h('a', {
          class: this.classes,
          href: this.disabled || this.loading ? null : this.href,
          'aria-disabled': this.disabled || this.loading ? 'true' : null,
          tabindex: this.disabled || this.loading ? -1 : null,
          onClick: this.onClick,
        }, children);
      }
      return h('button', {
        type: this.type || 'button',
        class: this.classes,
        disabled: !!(this.disabled || this.loading),
        onClick: this.onClick,
      }, children);
    },
  });

  var Table = register('Table', {
    props: {
      columns: { default: function () { return []; } },
      rows: { default: function () { return []; } },
      rowKey: { default: '' },
      empty: { default: '暂无数据' },
      loading: BOOL,
      striped: BOOL,
      hover: BOOL_TRUE,
      caption: { default: '' },
    },
    methods: {
      valueAt: function (row, column) {
        if (typeof column.value === 'function') return column.value(row);
        if (column.key) return row ? row[column.key] : '';
        return '';
      },
      cellContent: function (h, row, column, index) {
        if (typeof column.render === 'function') return column.render(row, index, h);
        var value = this.valueAt(row, column);
        return value === null || value === undefined ? '' : value;
      },
      keyFor: function (row, index) {
        var key;
        if (typeof this.rowKey === 'function') key = this.rowKey(row, index);
        else if (this.rowKey) key = row ? row[this.rowKey] : undefined;
        else key = row ? row.id : undefined;
        return key === undefined || key === null ? index : key;
      },
    },
    render: function (h) {
      var columns = this.columns || [];
      var rows = this.rows || [];
      var self = this;
      var head = h('thead', null, h('tr', null, columns.map(function (column) {
        return h('th', { scope: 'col', class: column.headerClass || column.class || '' }, column.label || '');
      })));
      var body;
      if (this.loading) {
        body = h('tbody', null, h('tr', null, h('td', {
          colspan: columns.length || 1,
          class: 'ada-table__empty',
        }, '加载中…')));
      } else if (!rows.length) {
        body = h('tbody', null, h('tr', null, h('td', {
          colspan: columns.length || 1,
          class: 'ada-table__empty',
        }, this.empty)));
      } else {
        body = h('tbody', null, rows.map(function (row, index) {
          return h('tr', { key: self.keyFor(row, index) }, columns.map(function (column) {
            return h('td', { class: column.class || '' }, self.cellContent(h, row, column, index));
          }));
        }));
      }
      var table = h('table', {
        class: ['table', this.striped ? 'ada-table--striped' : '', this.hover ? 'ada-table--hover' : ''],
      }, [
        this.caption ? h('caption', null, this.caption) : null,
        head,
        body,
      ]);
      return h('div', { class: 'ada-table-wrap' }, table);
    },
  });
  components.DataTable = Table;

  /* Select：服务端保留 $C Select（Go renderSelect 输出 ada-select DOM）；
     浏览器用 SelectAdapter 输出同一结构，并复用 ada-select.js 的交互。 */
  function normalizeOptions(options) {
    var out = [];
    var list = Array.isArray(options) ? options : [];
    for (var i = 0; i < list.length; i += 1) {
      var item = list[i];
      if (item && typeof item === 'object') {
        out.push({ value: String(item.value === undefined ? '' : item.value), label: String(item.label === undefined ? item.value : item.label) });
      } else {
        out.push({ value: String(item), label: String(item) });
      }
    }
    return out;
  }

  var SelectAdapter = defineComponent({
    name: 'SelectAdapter',
    props: {
      options: { default: function () { return []; } },
      value: { default: '' },
      modelValue: { default: '' },
      name: { default: '' },
      class: { default: '' },
      placeholder: { default: '' },
      disabled: BOOL,
      'aria-label': { default: '' },
    },
    computed: {
      normalized: function () { return normalizeOptions(this.options); },
      currentValue: function () {
        var value = this.modelValue !== '' && this.modelValue !== null && this.modelValue !== undefined
          ? this.modelValue
          : this.value;
        return value === null || value === undefined ? '' : String(value);
      },
    },
    methods: {
      onSelect: function (event) {
        var value = event && event.detail ? event.detail.value : '';
        this.$emit('update:modelValue', value);
        this.$emit('change', value, event);
      },
    },
    render: function (h) {
      var options = this.normalized;
      var value = this.currentValue;
      if (value === '' && options.length) value = options[0].value;
      var selectedLabel = this.placeholder || '';
      var triggerClass = this.class ? 'ada-select__trigger ' + this.class : 'ada-select__trigger';
      var dataProps = {};
      var attrs = this.$attrs || {};
      for (var key in attrs) {
        if (Object.prototype.hasOwnProperty.call(attrs, key) && key.indexOf('data-') === 0) {
          dataProps[key] = attrs[key];
        }
      }
      var items = [];
      for (var i = 0; i < options.length; i += 1) {
        var option = options[i];
        var selected = String(option.value) === value;
        if (selected) selectedLabel = option.label || option.value;
        items.push(h('button', {
          class: selected ? 'ada-select__option is-selected' : 'ada-select__option',
          type: 'button',
          'data-value': option.value,
          'aria-selected': selected ? 'true' : 'false',
        }, option.label || option.value));
      }
      if (selectedLabel === '') selectedLabel = value;
      return h('ada-select', Object.assign({
        class: 'ada-select',
        'onAda:select': this.onSelect,
      }, dataProps), [
        h('button', {
          class: triggerClass,
          type: 'button',
          'data-select-trigger': '',
          'aria-expanded': 'false',
          'aria-label': this['aria-label'] || null,
          disabled: !!this.disabled,
        }, [
          h('span', { class: 'ada-select__label', 'data-select-label': '' }, selectedLabel),
          h('span', { class: 'ada-select__caret', 'aria-hidden': 'true' }, '▾'),
        ]),
        h('div', { class: 'ada-select__menu', 'data-select-menu': '', hidden: true }, items),
        h('input', {
          type: 'hidden',
          'data-select-input': '',
          name: this.name || null,
          value: value,
          disabled: !!this.disabled,
        }),
      ]);
    },
  });
  components.SelectAdapter = SelectAdapter;

  var Select = register('Select', {
    props: {
      options: { default: function () { return []; } },
      value: { default: '' },
      modelValue: { default: '' },
      name: { default: '' },
      class: { default: '' },
      placeholder: { default: '' },
      disabled: BOOL,
      'aria-label': { default: '' },
    },
    render: function () {
      var p = {};
      for (var key in this.$props) {
        if (Object.prototype.hasOwnProperty.call(this.$props, key)) p[key] = this.$props[key];
      }
      if (p.modelValue !== '' && p.modelValue !== null && p.modelValue !== undefined) {
        p.value = p.modelValue;
      }
      var attrs = this.$attrs || {};
      for (var attr in attrs) {
        if (!Object.prototype.hasOwnProperty.call(attrs, attr)) continue;
        if (attr.indexOf('data-') === 0 || attr === '$on') p[attr] = attrs[attr];
      }
      return host('Select', p);
    },
  });

  var Tabs = register('Tabs', {
    props: {
      tabs: { default: function () { return []; } },
      modelValue: { default: null },
      id: { default: '' },
    },
    data: function () {
      var initial = this.modelValue;
      if (initial === null || initial === undefined) {
        initial = this.tabs && this.tabs[0] ? this.tabs[0].key : null;
      }
      return { active: initial };
    },
    computed: {
      rootId: function () {
        // 不生成自增 id：SSR 与客户端各自计数会错位。多 Tabs 请显式传 id。
        return this.id || 'ada-tabs';
      },
    },
    methods: {
      safe: function (key) {
        return String(key).replace(/[^A-Za-z0-9_-]/g, '-');
      },
      tabId: function (key) {
        return this.rootId + '-tab-' + this.safe(key);
      },
      panelId: function (key) {
        return this.rootId + '-panel-' + this.safe(key);
      },
      select: function (key) {
        var tabs = this.tabs || [];
        for (var i = 0; i < tabs.length; i += 1) {
          if (tabs[i].key === key) {
            if (tabs[i].disabled) return;
            this.active = key;
            this.$emit('update:modelValue', key);
            this.$emit('change', key);
            return;
          }
        }
      },
      onKeydown: function (event) {
        var keys = ['ArrowRight', 'ArrowLeft', 'Home', 'End'];
        if (keys.indexOf(event.key) === -1) return;
        var tabs = this.tabs || [];
        if (!tabs.length) return;
        event.preventDefault();
        var current = -1;
        for (var i = 0; i < tabs.length; i += 1) {
          if (tabs[i].key === this.active) current = i;
        }
        if (current < 0) current = 0;
        var next;
        if (event.key === 'Home') next = 0;
        else if (event.key === 'End') next = tabs.length - 1;
        else if (event.key === 'ArrowRight') next = (current + 1) % tabs.length;
        else next = (current - 1 + tabs.length) % tabs.length;
        var target = tabs[next];
        if (!target || target.disabled) return;
        this.select(target.key);
        if (typeof document !== 'undefined') {
          var button = document.getElementById(this.tabId(target.key));
          if (button) button.focus();
        }
      },
    },
    updated: function () {
      var tabs = this.tabs || [];
      var hasModel = this.modelValue !== null && this.modelValue !== undefined;
      var modelInTabs = false;
      for (var i = 0; i < tabs.length; i += 1) {
        if (tabs[i].key === this.modelValue) modelInTabs = true;
      }
      if (hasModel && modelInTabs && this.modelValue !== this.active) this.active = this.modelValue;
      if (tabs.length) {
        var found = false;
        for (var j = 0; j < tabs.length; j += 1) {
          if (tabs[j].key === this.active) found = true;
        }
        if (!found) this.active = tabs[0].key;
      }
    },
    render: function (h) {
      var self = this;
      var tabs = this.tabs || [];
      var list = [];
      for (var i = 0; i < tabs.length; i += 1) {
        (function (tab) {
          var selected = self.active === tab.key;
          var children = [];
          if (tab.icon) children.push(h(Icon, { name: tab.icon, size: 14 }));
          children.push(h('span', null, tab.label || tab.key));
          if (tab.count !== undefined && tab.count !== null) {
            children.push(h('span', { class: 'ada-tabs__count' }, tab.count));
          }
          list.push(h('button', {
            id: self.tabId(tab.key),
            class: 'ada-tabs__tab',
            type: 'button',
            role: 'tab',
            'aria-selected': selected ? 'true' : 'false',
            tabindex: selected ? 0 : -1,
            'aria-controls': self.panelId(tab.key),
            disabled: !!tab.disabled,
            onClick: function () { self.select(tab.key); },
          }, children));
        })(tabs[i]);
      }
      var panels = [];
      for (var j = 0; j < tabs.length; j += 1) {
        (function (tab) {
          panels.push(h('div', {
            id: self.panelId(tab.key),
            class: 'ada-tabs__panel',
            role: 'tabpanel',
            'aria-labelledby': self.tabId(tab.key),
            hidden: self.active !== tab.key,
          }, slot(self, tab.key) || []));
        })(tabs[j]);
      }
      return h('div', { class: 'ada-tabs' }, [
        h('div', { class: 'ada-tabs__list', role: 'tablist', onKeydown: this.onKeydown }, list),
        panels,
      ]);
    },
  });

  var Input = register('Input', {
    props: {
      modelValue: { default: '' },
      type: { default: 'text' },
      name: { default: '' },
      label: { default: '' },
      hint: { default: '' },
      error: { default: '' },
      placeholder: { default: '' },
      disabled: BOOL,
      readonly: BOOL,
      required: BOOL,
      multiline: BOOL,
      rows: { default: 3 },
      maxlength: { default: null },
      minlength: { default: null },
      id: { default: '' },
    },
    computed: {
      fieldId: function () {
        // 同构组件不生成自增 id：服务端/客户端各自计数会错位。需要 label for
        // 或 aria-describedby 时必须显式传 id。
        return this.id || '';
      },
      describedBy: function () {
        return (this.error || this.hint) && this.fieldId ? this.fieldId + '-hint' : null;
      },
    },
    methods: {
      onInput: function (event) {
        var value = event.target.value;
        if (this.type === 'number' && value !== '') value = Number(value);
        this.$emit('update:modelValue', value);
        this.$emit('input', value);
      },
    },
    render: function (h) {
      var id = this.fieldId || null;
      var attrs = {
        id: id,
        class: 'ada-input',
        name: this.name || null,
        placeholder: this.placeholder || null,
        disabled: !!this.disabled,
        readonly: !!this.readonly,
        required: !!this.required,
        value: this.modelValue,
        'aria-invalid': this.error ? 'true' : null,
        'aria-describedby': this.describedBy,
        onInput: this.onInput,
      };
      if (this.multiline) {
        attrs.rows = this.rows;
        attrs.maxlength = this.maxlength;
        attrs.minlength = this.minlength;
      } else {
        attrs.type = this.type || 'text';
        attrs.maxlength = this.maxlength;
        attrs.minlength = this.minlength;
      }
      var field = this.multiline ? h('textarea', attrs, null) : h('input', attrs, null);
      var children = [];
      if (this.label) children.push(h('label', { class: 'ada-field__label', for: id }, this.label));
      children.push(field);
      if (this.error) {
        children.push(h('p', { class: 'ada-field__hint ada-field__hint--error', id: id ? id + '-hint' : null }, this.error));
      } else if (this.hint) {
        children.push(h('p', { class: 'ada-field__hint', id: id ? id + '-hint' : null }, this.hint));
      }
      return h('div', { class: 'ada-field' }, children);
    },
  });

  var Empty = register('Empty', {
    props: {
      title: { default: '' },
      description: { default: '' },
    },
    render: function (h) {
      var action = slot(this, 'action');
      var children = [];
      if (this.title) children.push(h('h2', { class: 'ada-empty__title' }, this.title));
      if (this.description) children.push(h('p', { class: 'ada-empty__desc' }, this.description));
      if (action) children.push(h('div', { class: 'ada-empty__action' }, action));
      return h('div', { class: 'ada-empty' }, children);
    },
  });

  // TextField 与 ada-ui 同名别名；Textarea 是 Input 的多行默认值。
  var Textarea = register('Textarea', {
    props: Object.assign({}, Input.props, {
      multiline: BOOL_TRUE,
      rows: { default: 4 },
    }),
    computed: Input.computed,
    methods: Input.methods,
    render: Input.render,
  });
  components.TextField = Input;

  return {
    version: '0.1.0',
    components: components,
    TextField: Input,
    Textarea: Textarea,
  };
});
