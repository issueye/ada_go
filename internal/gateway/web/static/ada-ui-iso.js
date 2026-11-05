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
