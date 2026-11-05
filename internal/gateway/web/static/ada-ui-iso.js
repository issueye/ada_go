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
