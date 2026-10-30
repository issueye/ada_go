/*
 * ada-ui.js · ada-render 的组件层（单文件、零依赖）
 * ---------------------------------------------------------------------------
 * 目标不是另造一套设计语言：所有 class、DOM 结构与状态语义都复用现有
 * `ada-*` / `.card` / `.badge` / `.table` / `.alert` 基线，只补项目里
 * 还没有的控件样式（见 ada-ui.css）。
 *
 * 依赖加载顺序：
 *   ada-render.js → ada-select.js / ada-modal.js → ada-ui.js
 * `Select` / `Modal` 的行为由现有 Web Components 提供；没有它们时，
 * 组件仍能渲染静态结构，但下拉/弹窗交互不可用。
 *
 * 用法：
 *   const app = AdaUI.createApp({ data() { return {...} }, template: '...' });
 *   app.mount('#app');
 *   // 或 AdaUI.install(AdaRender.createApp({...}))
 */
(function (root, factory) {
  if (typeof module === 'object' && module.exports) {
    module.exports = factory(require('./ada-render.js'));
  } else {
    root.AdaUI = factory(root.AdaRender);
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function (AdaRender) {
  'use strict';

  if (!AdaRender || typeof AdaRender.createApp !== 'function') {
    throw new Error('[ada-ui] 请先加载 ada-render.js');
  }

  var defineComponent = AdaRender.defineComponent;
  var nextTick = AdaRender.nextTick;
  var h = AdaRender.h;

  var idCounter = 0;
  function nextId(prefix) {
    idCounter += 1;
    return prefix + '-' + idCounter;
  }

  function toneOf(tone) {
    return tone === 'error' ? 'danger' : (tone || 'info');
  }

  function iconForTone(tone) {
    var map = {
      info: 'i-bulb',
      success: 'i-check-circle',
      warn: 'i-alert',
      danger: 'i-x-circle',
      error: 'i-x-circle'
    };
    return map[tone] || 'i-bulb';
  }

  var components = {};

  // 布尔 prop：模板里裸写 `native` / `show-value` 时由引擎按 true 处理。
  var BOOL = { default: false, type: Boolean };
  var BOOL_TRUE = { default: true, type: Boolean };

  function register(name, definition) {
    definition.name = definition.name || name;
    var normalized = defineComponent(definition);
    components[name] = normalized;
    return normalized;
  }

  /* ═══════════════════════════════════════════════════════════════════
   * 基础显示
   * ═══════════════════════════════════════════════════════════════════ */

  var Icon = register('Icon', {
    props: {
      name: { default: '' },
      size: { default: 14 },
      title: { default: '' },
      label: { default: '' }
    },
    computed: {
      symbol: function () {
        var name = this.name || '';
        return name.charAt(0) === '#' ? name : '#' + name;
      }
    },
    template:
      '<svg class="ada-icon" :width="size" :height="size" viewBox="0 0 24 24" fill="none" ' +
      'stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" ' +
      ':aria-hidden="label || title ? null : \'true\'" :role="label ? \'img\' : null" :aria-label="label || null">' +
      '<title v-if="title">{{ title }}</title>' +
      '<use v-if="name" :href="symbol"></use>' +
      '</svg>'
  });

  var Spinner = register('Spinner', {
    props: {
      size: { default: 16 },
      label: { default: '' }
    },
    template:
      '<svg class="ada-run-status__spin" :width="size" :height="size" viewBox="0 0 24 24" fill="none" ' +
      ':aria-hidden="label ? null : \'true\'" :role="label ? \'img\' : null" :aria-label="label || null">' +
      '<circle cx="12" cy="12" r="9" stroke="currentColor" stroke-opacity=".25" stroke-width="2"></circle>' +
      '<path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" stroke-width="2" stroke-linecap="round"></path>' +
      '</svg>'
  });

  // Markdown 只接收后端给的 VNode AST（不是 HTML）。流式阶段还没有 AST 时
  // 先用 text 纯文本占位，避免空白；AST 到达后由 VNode patch 接管。
  var Markdown = register('Markdown', {
    props: {
      nodes: { default: function () { return []; } },
      text: { default: '' },
      class: { default: 'part-text' }
    },
    render: function (h) {
      var nodes = Array.isArray(this.nodes) ? this.nodes : [];
      if (nodes.length) return h('div', { class: this.class }, nodes);
      if (this.text) return h('div', { class: [this.class, 'markdown-pending'] }, this.text);
      return h('div', { class: this.class });
    }
  });

  var Badge = register('Badge', {
    props: {
      tone: { default: 'neutral' },
      mono: BOOL,
      label: { default: '' },
      title: { default: '' }
    },
    template:
      '<span class="badge" :class="[\'badge-\' + tone, mono ? \'badge-mono\' : \'\']" :title="title || null">' +
      '<slot>{{ label }}</slot></span>'
  });

  var Card = register('Card', {
    props: {
      title: { default: '' },
      subtitle: { default: '' },
      padding: BOOL_TRUE,
      class: { default: '' }
    },
    template:
      '<section class="card" :class="class">' +
      '<div v-if="title || subtitle || $slots.header" class="section-head border-b border-line px-4 py-3">' +
      '<div>' +
      '<h2 v-if="title" class="section-title">{{ title }}</h2>' +
      '<p v-if="subtitle" class="section-sub">{{ subtitle }}</p>' +
      '</div>' +
      '<div v-if="$slots.header" class="flex items-center gap-2"><slot name="header"></slot></div>' +
      '</div>' +
      '<div :class="padding ? \'p-4\' : \'\'"><slot></slot></div>' +
      '<div v-if="$slots.footer" class="border-t border-line px-4 py-3"><slot name="footer"></slot></div>' +
      '</section>'
  });

  var Empty = register('Empty', {
    props: {
      icon: { default: 'i-circle' },
      title: { default: '' },
      description: { default: '' }
    },
    template:
      '<div class="ada-empty">' +
      '<span v-if="icon" class="ada-empty__mark"><ui-icon :name="icon" :size="20"></ui-icon></span>' +
      '<h2 v-if="title" class="ada-empty__title">{{ title }}</h2>' +
      '<p v-if="description" class="ada-empty__desc">{{ description }}</p>' +
      '<div v-if="$slots.action" class="ada-empty__action"><slot name="action"></slot></div>' +
      '</div>'
  });

  /* ═══════════════════════════════════════════════════════════════════
   * 操作控件
   * ═══════════════════════════════════════════════════════════════════ */

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
      icon: { default: '' },
      iconRight: { default: '' }
    },
    computed: {
      classes: function () {
        return [
          'ada-btn',
          'ada-btn--' + this.variant,
          'ada-btn--' + this.size,
          this.block ? 'ada-btn--block' : ''
        ];
      },
      hasLabel: function () {
        return !!(this.label || (this.$slots.default && this.$slots.default().length));
      }
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
      }
    },
    template:
      '<a v-if="href" :href="disabled || loading ? null : href" :class="classes" ' +
      ':aria-disabled="disabled || loading ? \'true\' : null" :tabindex="disabled || loading ? -1 : null" @click="onClick">' +
      '<ui-spinner v-if="loading" :size="14"></ui-spinner>' +
      '<ui-icon v-else-if="icon" :name="icon" :size="14"></ui-icon>' +
      '<span v-if="hasLabel" class="ada-btn__label"><slot>{{ label }}</slot></span>' +
      '<ui-icon v-if="iconRight" :name="iconRight" :size="14"></ui-icon>' +
      '</a>' +
      '<button v-else :type="type" :class="classes" :disabled="disabled || loading" @click="onClick">' +
      '<ui-spinner v-if="loading" :size="14"></ui-spinner>' +
      '<ui-icon v-else-if="icon" :name="icon" :size="14"></ui-icon>' +
      '<span v-if="hasLabel" class="ada-btn__label"><slot>{{ label }}</slot></span>' +
      '<ui-icon v-if="iconRight" :name="iconRight" :size="14"></ui-icon>' +
      '</button>'
  });

  var IconButton = register('IconButton', {
    props: {
      icon: { default: '' },
      label: { default: '' },
      title: { default: '' },
      size: { default: 14 },
      type: { default: 'button' },
      disabled: BOOL
    },
    methods: {
      onClick: function (event) {
        if (this.disabled) {
          if (event) event.preventDefault();
          return;
        }
        this.$emit('click', event);
      }
    },
    template:
      '<button class="ada-icon-btn" :type="type" :disabled="disabled" ' +
      ':aria-label="label || title || null" :title="title || label || null" @click="onClick">' +
      '<ui-icon :name="icon" :size="size"></ui-icon></button>'
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
      autocomplete: { default: '' },
      multiline: BOOL,
      rows: { default: 3 },
      maxlength: { default: null },
      minlength: { default: null },
      id: { default: '' }
    },
    data: function () {
      return { uid: nextId('ada-input') };
    },
    computed: {
      fieldId: function () {
        return this.id || this.uid;
      },
      describedBy: function () {
        return this.error || this.hint ? this.fieldId + '-hint' : null;
      }
    },
    methods: {
      onInput: function (event) {
        var value = event.target.value;
        if (this.type === 'number' && value !== '') value = Number(value);
        this.$emit('update:modelValue', value);
        this.$emit('input', value);
      }
    },
    template:
      '<div class="ada-field">' +
      '<label v-if="label" class="ada-field__label" :for="fieldId">{{ label }}</label>' +
      '<textarea v-if="multiline" :id="fieldId" class="ada-input" :name="name" :placeholder="placeholder" ' +
      ':rows="rows" :maxlength="maxlength" :minlength="minlength" :autocomplete="autocomplete" ' +
      ':disabled="disabled" :readonly="readonly" :required="required" :value="modelValue" ' +
      ':aria-invalid="error ? \'true\' : null" :aria-describedby="describedBy" @input="onInput"></textarea>' +
      '<input v-else :id="fieldId" class="ada-input" :type="type" :name="name" :placeholder="placeholder" ' +
      ':maxlength="maxlength" :minlength="minlength" :autocomplete="autocomplete" ' +
      ':disabled="disabled" :readonly="readonly" :required="required" :value="modelValue" ' +
      ':aria-invalid="error ? \'true\' : null" :aria-describedby="describedBy" @input="onInput">' +
      '<p v-if="error" class="ada-field__hint ada-field__hint--error" :id="fieldId + \'-hint\'">{{ error }}</p>' +
      '<p v-else-if="hint" class="ada-field__hint" :id="fieldId + \'-hint\'">{{ hint }}</p>' +
      '</div>'
  });

  var Textarea = register('Textarea', {
    name: 'Textarea',
    props: Object.assign({}, Input.props, {
      multiline: BOOL_TRUE,
      rows: { default: 4 }
    }),
    data: Input.data,
    computed: Input.computed,
    methods: Input.methods,
    template: Input.template
  });

  var Select = register('Select', {
    props: {
      modelValue: { default: '' },
      options: { default: function () { return []; } },
      name: { default: '' },
      label: { default: '' },
      hint: { default: '' },
      error: { default: '' },
      placeholder: { default: '' },
      disabled: BOOL,
      native: BOOL,
      id: { default: '' }
    },
    data: function () {
      return { uid: nextId('ada-select') };
    },
    computed: {
      fieldId: function () {
        return this.id || this.uid;
      },
      describedBy: function () {
        return this.error || this.hint ? this.fieldId + '-hint' : null;
      },
      normalized: function () {
        var options = this.options || [];
        return options.map(function (option) {
          if (option && typeof option === 'object') return option;
          return { value: option, label: String(option) };
        });
      },
      hasValue: function () {
        return this.modelValue !== '' && this.modelValue !== null && this.modelValue !== undefined;
      },
      current: function () {
        var value = String(this.modelValue);
        for (var i = 0; i < this.normalized.length; i += 1) {
          if (String(this.normalized[i].value) === value) return this.normalized[i];
        }
        if (this.placeholder) return { value: '', label: this.placeholder };
        return this.normalized[0] || { value: '', label: '' };
      }
    },
    methods: {
      emitValue: function (value, event) {
        this.$emit('update:modelValue', value);
        this.$emit('change', value, event);
      },
      onSelect: function (event) {
        var raw = event && event.detail ? event.detail.value : '';
        for (var i = 0; i < this.normalized.length; i += 1) {
          if (String(this.normalized[i].value) === String(raw)) {
            this.emitValue(this.normalized[i].value, event);
            return;
          }
        }
        this.emitValue(raw, event);
      },
      onNativeChange: function (event) {
        var raw = event.target.value;
        for (var i = 0; i < this.normalized.length; i += 1) {
          if (String(this.normalized[i].value) === String(raw)) {
            this.emitValue(this.normalized[i].value, event);
            return;
          }
        }
        this.emitValue(raw, event);
      },
      sync: function () {
        var element = this.native ? this.$refs.nativeSelect : this.$refs.picker;
        if (!element) return;
        if (this.native) {
          var nativeValue = this.hasValue ? String(this.modelValue) : '';
          if (element.value !== nativeValue) element.value = nativeValue;
          return;
        }
        if (this.disabled || !this.hasValue) return;
        var value = String(this.modelValue);
        if (element.value !== value) element.value = value;
      }
    },
    mounted: function () { this.sync(); },
    updated: function () { this.sync(); },
    template:
      '<div class="ada-field">' +
      '<label v-if="label && native" class="ada-field__label" :for="fieldId">{{ label }}</label>' +
      '<span v-else-if="label" class="ada-field__label" :id="fieldId + \'-label\'">{{ label }}</span>' +
      '<select v-if="native" :id="fieldId" ref="nativeSelect" class="ada-input" :name="name" ' +
      ':disabled="disabled" :value="modelValue" :aria-invalid="error ? \'true\' : null" ' +
      ':aria-describedby="describedBy" @change="onNativeChange">' +
      '<option v-if="placeholder && !hasValue" value="">{{ placeholder }}</option>' +
      '<option v-for="option in normalized" :key="option.value" :value="option.value" :disabled="option.disabled">{{ option.label }}</option>' +
      '</select>' +
      '<ada-select v-else ref="picker" class="ada-select" :data-disabled="disabled ? \'true\' : null" @ada:select="onSelect">' +
      '<button class="ada-select__trigger" type="button" data-select-trigger aria-expanded="false" ' +
      ':disabled="disabled" :aria-labelledby="label ? fieldId + \'-label\' : null" ' +
      ':aria-describedby="describedBy" :aria-invalid="error ? \'true\' : null">' +
      '<span class="ada-select__label" data-select-label>{{ current.label }}</span>' +
      '<span class="ada-select__caret"><ui-icon name="i-chevron-down" :size="12"></ui-icon></span>' +
      '</button>' +
      '<div class="ada-select__menu" data-select-menu hidden>' +
      '<button v-for="option in normalized" :key="option.value" class="ada-select__option" ' +
      ':class="{ \'is-selected\': String(option.value) === String(modelValue) }" type="button" ' +
      ':data-value="option.value" :disabled="option.disabled">{{ option.label }}</button>' +
      '</div>' +
      '<input type="hidden" :name="name" data-select-input :value="modelValue">' +
      '</ada-select>' +
      '<p v-if="error" class="ada-field__hint ada-field__hint--error" :id="fieldId + \'-hint\'">{{ error }}</p>' +
      '<p v-else-if="hint" class="ada-field__hint" :id="fieldId + \'-hint\'">{{ hint }}</p>' +
      '</div>'
  });

  var Dropdown = register('Dropdown', {
    props: {
      label: { default: '' },
      icon: { default: '' },
      items: { default: function () { return []; } },
      variant: { default: 'secondary' },
      size: { default: 'md' },
      align: { default: 'right' },
      disabled: BOOL,
      closeOnSelect: BOOL_TRUE
    },
    methods: {
      pick: function (item, event) {
        if (!item) return;
        if (item.disabled) {
          if (event) event.preventDefault();
          return;
        }
        if (this.closeOnSelect && this.$refs.root) this.$refs.root.open = false;
        this.$emit('select', item, event);
        this.$emit('command', item.value, item, event);
      },
      close: function () {
        if (this.$refs.root) this.$refs.root.open = false;
      }
    },
    template:
      '<details class="ada-row-menu ada-dropdown" :class="\'ada-dropdown--\' + align" ' +
      ':data-disabled="disabled ? \'true\' : null" ref="root" @keydown.esc="close">' +
      '<summary class="ada-dropdown__summary" :aria-disabled="disabled ? \'true\' : null" ' +
      ':tabindex="disabled ? -1 : null">' +
      '<span :class="[\'ada-btn\', \'ada-btn--\' + variant, \'ada-btn--\' + size, \'ada-dropdown__trigger\']">' +
      '<ui-icon v-if="icon" :name="icon" :size="14"></ui-icon>' +
      '<span v-if="label" class="ada-btn__label">{{ label }}</span>' +
      '<ui-icon name="i-chevron-down" :size="12"></ui-icon>' +
      '</span>' +
      '</summary>' +
      '<div class="ada-row-menu__panel ada-dropdown__panel">' +
      '<template v-if="items && items.length">' +
      '<template v-for="(item, index) in items">' +
      '<div v-if="item.separator" class="ada-dropdown__separator" role="separator"></div>' +
      '<a v-else-if="item.href" class="ada-row-menu__item" ' +
      ':key="item.value !== undefined ? item.value : index" ' +
      ':class="{ \'ada-row-menu__item--danger\': item.danger }" :href="item.href" @click="pick(item, $event)">' +
      '<ui-icon v-if="item.icon" :name="item.icon" :size="14"></ui-icon><span>{{ item.label }}</span></a>' +
      '<button v-else class="ada-row-menu__item" :class="{ \'ada-row-menu__item--danger\': item.danger }" ' +
      ':key="item.value !== undefined ? item.value : index" ' +
      'type="button" :disabled="item.disabled" @click="pick(item, $event)">' +
      '<ui-icon v-if="item.icon" :name="item.icon" :size="14"></ui-icon><span>{{ item.label }}</span></button>' +
      '</template>' +
      '</template>' +
      '<slot></slot>' +
      '</div>' +
      '</details>'
  });

  /* ═══════════════════════════════════════════════════════════════════
   * 页签
   * ═══════════════════════════════════════════════════════════════════ */

  var Tabs = register('Tabs', {
    props: {
      tabs: { default: function () { return []; } },
      modelValue: { default: null },
      id: { default: '' }
    },
    data: function () {
      var initial = this.modelValue;
      if (initial === null || initial === undefined) {
        initial = this.tabs[0] ? this.tabs[0].key : null;
      }
      return { uid: nextId('ada-tabs'), active: initial };
    },
    computed: {
      rootId: function () {
        return this.id || this.uid;
      }
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
      }
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
    template:
      '<div class="ada-tabs">' +
      '<div class="ada-tabs__list" role="tablist" @keydown="onKeydown">' +
      '<button v-for="tab in tabs" :key="tab.key" :id="tabId(tab.key)" class="ada-tabs__tab" ' +
      'type="button" role="tab" :aria-selected="active === tab.key ? \'true\' : \'false\'" ' +
      ':tabindex="active === tab.key ? 0 : -1" :aria-controls="panelId(tab.key)" ' +
      ':disabled="tab.disabled" @click="select(tab.key)">' +
      '<ui-icon v-if="tab.icon" :name="tab.icon" :size="14"></ui-icon>' +
      '<span>{{ tab.label }}</span>' +
      '<span v-if="tab.count !== undefined && tab.count !== null" class="ada-tabs__count">{{ tab.count }}</span>' +
      '</button>' +
      '</div>' +
      '<div v-for="tab in tabs" :key="\'panel-\' + tab.key" :id="panelId(tab.key)" class="ada-tabs__panel" ' +
      'role="tabpanel" :aria-labelledby="tabId(tab.key)" :hidden="active !== tab.key">' +
      '<slot :name="tab.key"></slot>' +
      '</div>' +
      '</div>'
  });

  /* ═══════════════════════════════════════════════════════════════════
   * 范围 / 进度 / 开关 / 选择项
   * ═══════════════════════════════════════════════════════════════════ */

  var Slider = register('Slider', {
    props: {
      modelValue: { default: 0 },
      min: { default: 0 },
      max: { default: 100 },
      step: { default: 1 },
      disabled: BOOL,
      label: { default: '' },
      showValue: BOOL_TRUE,
      suffix: { default: '' },
      format: { default: null },
      id: { default: '' }
    },
    data: function () {
      return { uid: nextId('ada-slider') };
    },
    computed: {
      fieldId: function () {
        return this.id || this.uid;
      },
      display: function () {
        if (typeof this.format === 'function') return this.format(this.modelValue);
        return String(this.modelValue) + (this.suffix || '');
      }
    },
    methods: {
      onInput: function (event) {
        var value = Number(event.target.value);
        this.$emit('update:modelValue', value);
        this.$emit('input', value);
      }
    },
    template:
      '<div class="ada-field ada-slider-field">' +
      '<div v-if="label || showValue" class="ada-slider__head">' +
      '<label v-if="label" class="ada-field__label" :for="fieldId">{{ label }}</label>' +
      '<span v-if="showValue" class="ada-slider__value">{{ display }}</span>' +
      '</div>' +
      '<input :id="fieldId" class="ada-slider" type="range" :min="min" :max="max" :step="step" ' +
      ':value="modelValue" :disabled="disabled" :aria-label="label ? null : \'滑块\'" @input="onInput">' +
      '</div>'
  });

  var Progress = register('Progress', {
    props: {
      value: { default: 0 },
      max: { default: 100 },
      tone: { default: 'brand' },
      size: { default: 'md' },
      label: { default: '' },
      showValue: BOOL
    },
    computed: {
      limit: function () {
        var max = Number(this.max);
        return Number.isFinite(max) && max > 0 ? max : 0;
      },
      clamped: function () {
        var value = Number(this.value);
        if (!Number.isFinite(value)) value = 0;
        if (value < 0) value = 0;
        return this.limit && value > this.limit ? this.limit : value;
      },
      percent: function () {
        return this.limit ? Math.round((this.clamped / this.limit) * 100) : 0;
      },
      progressTone: function () {
        var tone = this.tone;
        return tone === 'success' || tone === 'warn' || tone === 'danger' ? tone : 'brand';
      }
    },
    template:
      '<div class="ada-progress-field">' +
      '<div v-if="label || showValue" class="ada-progress__head">' +
      '<span v-if="label" class="ada-field__label">{{ label }}</span>' +
      '<span v-if="showValue" class="ada-progress__value">{{ percent }}%</span>' +
      '</div>' +
      '<progress class="ada-progress" :class="[\'ada-progress--\' + progressTone, \'ada-progress--\' + size]" ' +
      ':value="clamped" :max="max" :aria-label="label || null" ' +
      ':aria-valuetext="showValue ? percent + \'%\' : null"></progress>' +
      '</div>'
  });

  var Switch = register('Switch', {
    props: {
      modelValue: { default: false },
      disabled: BOOL,
      label: { default: '' },
      id: { default: '' }
    },
    data: function () {
      return { uid: nextId('ada-switch') };
    },
    computed: {
      fieldId: function () {
        return this.id || this.uid;
      },
      hasLabel: function () {
        return !!(this.label || (this.$slots.default && this.$slots.default().length));
      }
    },
    methods: {
      onChange: function (event) {
        var value = !!event.target.checked;
        this.$emit('update:modelValue', value);
        this.$emit('change', value);
      }
    },
    template:
      '<label class="ada-switch" :class="{ \'is-disabled\': disabled }" :for="fieldId">' +
      '<input :id="fieldId" class="ada-switch__input" type="checkbox" role="switch" ' +
      ':checked="modelValue" :disabled="disabled" @change="onChange">' +
      '<span class="ada-switch__track" aria-hidden="true"><span class="ada-switch__thumb"></span></span>' +
      '<span v-if="hasLabel" class="ada-switch__label"><slot>{{ label }}</slot></span>' +
      '</label>'
  });

  var Checkbox = register('Checkbox', {
    props: {
      modelValue: { default: false },
      disabled: BOOL,
      label: { default: '' },
      id: { default: '' }
    },
    data: function () {
      return { uid: nextId('ada-checkbox') };
    },
    computed: {
      fieldId: function () {
        return this.id || this.uid;
      },
      hasLabel: function () {
        return !!(this.label || (this.$slots.default && this.$slots.default().length));
      }
    },
    methods: {
      onChange: function (event) {
        var value = !!event.target.checked;
        this.$emit('update:modelValue', value);
        this.$emit('change', value);
      }
    },
    template:
      '<label class="ada-choice" :class="{ \'is-disabled\': disabled }" :for="fieldId">' +
      '<input :id="fieldId" class="ada-choice__input" type="checkbox" :checked="modelValue" ' +
      ':disabled="disabled" @change="onChange">' +
      '<span v-if="hasLabel" class="ada-choice__label"><slot>{{ label }}</slot></span>' +
      '</label>'
  });

  var Radio = register('Radio', {
    props: {
      modelValue: { default: '' },
      value: { default: '' },
      name: { default: '' },
      disabled: BOOL,
      label: { default: '' },
      id: { default: '' }
    },
    data: function () {
      return { uid: nextId('ada-radio') };
    },
    computed: {
      fieldId: function () {
        return this.id || this.uid;
      },
      checked: function () {
        return this.modelValue === this.value;
      },
      hasLabel: function () {
        return !!(this.label || (this.$slots.default && this.$slots.default().length));
      }
    },
    methods: {
      onChange: function (event) {
        if (!event.target.checked) return;
        this.$emit('update:modelValue', this.value);
        this.$emit('change', this.value);
      }
    },
    template:
      '<label class="ada-choice" :class="{ \'is-disabled\': disabled }" :for="fieldId">' +
      '<input :id="fieldId" class="ada-choice__input" type="radio" :name="name" :value="value" ' +
      ':checked="checked" :disabled="disabled" @change="onChange">' +
      '<span v-if="hasLabel" class="ada-choice__label"><slot>{{ label }}</slot></span>' +
      '</label>'
  });

  /* ═══════════════════════════════════════════════════════════════════
   * 反馈
   * ═══════════════════════════════════════════════════════════════════ */

  var Message = register('Message', {
    props: {
      tone: { default: 'info' },
      title: { default: '' },
      closable: BOOL
    },
    data: function () {
      return { visible: true };
    },
    computed: {
      alertTone: function () { return toneOf(this.tone); },
      iconName: function () { return iconForTone(this.tone); },
      role: function () { return this.tone === 'error' ? 'alert' : 'status'; }
    },
    methods: {
      close: function () {
        this.visible = false;
        this.$emit('close');
      }
    },
    template:
      '<div v-show="visible" class="alert" :class="\'alert-\' + alertTone" :role="role">' +
      '<ui-icon :name="iconName" :size="14"></ui-icon>' +
      '<div class="alert-message">' +
      '<strong v-if="title" class="alert-title">{{ title }}</strong>' +
      '<div><slot></slot></div>' +
      '</div>' +
      '<button v-if="closable" class="alert-dismiss" type="button" aria-label="关闭" @click="close">×</button>' +
      '</div>'
  });

  var Notice = register('Notice', {
    props: {
      tone: { default: 'neutral' },
      icon: { default: '' }
    },
    computed: {
      iconName: function () { return this.icon || iconForTone(this.tone); }
    },
    template:
      '<div class="ada-notice" :class="\'ada-notice--\' + tone">' +
      '<div class="ada-notice__row"><ui-icon :name="iconName" :size="14"></ui-icon><span><slot></slot></span></div>' +
      '</div>'
  });

  var Modal = register('Modal', {
    props: {
      modelValue: { default: null },
      id: { default: function () { return nextId('ada-modal'); } },
      title: { default: '' },
      wide: BOOL,
      confirmText: { default: '确认' },
      cancelText: { default: '取消' },
      confirmVariant: { default: 'primary' },
      closeOnConfirm: BOOL_TRUE
    },
    data: function () {
      return { innerOpen: false };
    },
    computed: {
      modalId: function () {
        return this.id || 'ada-modal';
      },
      titleId: function () {
        return this.modalId + '-title';
      },
      isOpen: function () {
        return this.modelValue === null || this.modelValue === undefined
          ? this.innerOpen
          : !!this.modelValue;
      }
    },
    methods: {
      open: function () {
        if (this.modelValue === null || this.modelValue === undefined) this.innerOpen = true;
        this.$emit('update:modelValue', true);
        this.$emit('open');
      },
      close: function () {
        if (this.modelValue === null || this.modelValue === undefined) this.innerOpen = false;
        this.$emit('update:modelValue', false);
        this.$emit('close');
      },
      cancel: function () {
        this.$emit('cancel');
        this.close();
      },
      confirm: function () {
        this.$emit('confirm');
        if (this.closeOnConfirm) this.close();
      },
      onDocumentOpen: function (event) {
        if (!event || !event.detail || event.detail.id !== this.modalId) return;
        this.innerOpen = true;
        this.$emit('update:modelValue', true);
        this.$emit('open');
      },
      onDocumentClose: function (event) {
        if (!event || !event.detail || event.detail.id !== this.modalId) return;
        this.innerOpen = false;
        this.$emit('update:modelValue', false);
        this.$emit('close');
      },
      sync: function () {
        var element = this.$refs.modal;
        if (!element) return;
        if (typeof element.open === 'function') {
          if (this.isOpen && !element.isOpen) element.open();
          else if (!this.isOpen && element.isOpen) element.close();
          return;
        }
        if (this.isOpen) element.removeAttribute('hidden');
        else element.setAttribute('hidden', '');
        if (typeof customElements !== 'undefined' && customElements.whenDefined) {
          var self = this;
          customElements.whenDefined('ada-modal').then(function () { self.sync(); });
        }
      }
    },
    mounted: function () {
      if (typeof document !== 'undefined') {
        document.addEventListener('ada:modal-open', this.onDocumentOpen);
        document.addEventListener('ada:modal-close', this.onDocumentClose);
      }
      this.sync();
    },
    updated: function () { this.sync(); },
    unmounted: function () {
      if (typeof document !== 'undefined') {
        document.removeEventListener('ada:modal-open', this.onDocumentOpen);
        document.removeEventListener('ada:modal-close', this.onDocumentClose);
      }
    },
    template:
      '<ada-modal class="ada-modal" :data-modal="modalId" hidden ref="modal">' +
      '<div class="ada-modal__backdrop" data-modal-close></div>' +
      '<div class="ada-modal__panel" :class="{ \'ada-modal__panel--wide\': wide }" role="dialog" ' +
      'aria-modal="true" :aria-labelledby="title ? titleId : null">' +
      '<h2 v-if="title" class="ada-modal__title" :id="titleId">{{ title }}</h2>' +
      '<div class="ada-modal__body"><slot></slot></div>' +
      '<div class="ada-modal__actions">' +
      '<slot name="actions">' +
      '<ui-button v-if="cancelText" variant="secondary" size="sm" @click="cancel">{{ cancelText }}</ui-button>' +
      '<ui-button v-if="confirmText" :variant="confirmVariant" size="sm" @click="confirm">{{ confirmText }}</ui-button>' +
      '</slot>' +
      '</div>' +
      '</div>' +
      '</ada-modal>'
  });

  /* ═══════════════════════════════════════════════════════════════════
   * 表格
   * ═══════════════════════════════════════════════════════════════════ */

  var Table = register('Table', {
    props: {
      columns: { default: function () { return []; } },
      rows: { default: function () { return []; } },
      rowKey: { default: '' },
      empty: { default: '暂无数据' },
      loading: BOOL,
      striped: BOOL,
      hover: BOOL_TRUE,
      caption: { default: '' }
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
      }
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
          class: 'ada-table__empty'
        }, h(Spinner, { size: 14 }), ' 加载中…')));
      } else if (!rows.length) {
        body = h('tbody', null, h('tr', null, h('td', {
          colspan: columns.length || 1,
          class: 'ada-table__empty'
        }, this.empty)));
      } else {
        body = h('tbody', null, rows.map(function (row, index) {
          return h('tr', { key: self.keyFor(row, index) }, columns.map(function (column) {
            return h('td', { class: column.class || '' }, self.cellContent(h, row, column, index));
          }));
        }));
      }
      var table = h('table', {
        class: ['table', this.striped ? 'ada-table--striped' : '', this.hover ? 'ada-table--hover' : '']
      }, this.caption ? h('caption', null, this.caption) : null, head, body);
      return h('div', { class: 'ada-table-wrap' }, table);
    }
  });

  /* ═══════════════════════════════════════════════════════════════════
   * Toast（命令式，挂到 document.body）
   * ═══════════════════════════════════════════════════════════════════ */

  var toastStack = null;

  function ensureToastStack() {
    if (typeof document === 'undefined' || !document.body) return null;
    if (toastStack && toastStack.parentNode) return toastStack;
    toastStack = document.createElement('div');
    toastStack.className = 'ada-toast-stack';
    toastStack.setAttribute('aria-live', 'polite');
    document.body.appendChild(toastStack);
    return toastStack;
  }

  function svgIcon(name, size) {
    if (typeof document === 'undefined') return null;
    var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'ada-icon');
    svg.setAttribute('width', String(size));
    svg.setAttribute('height', String(size));
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', '1.75');
    svg.setAttribute('stroke-linecap', 'round');
    svg.setAttribute('stroke-linejoin', 'round');
    svg.setAttribute('aria-hidden', 'true');
    var use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', '#' + name);
    svg.appendChild(use);
    return svg;
  }

  function toast(message, options) {
    var opts = typeof options === 'string' ? { tone: options } : (options || {});
    var stack = ensureToastStack();
    if (!stack) return null;
    var tone = opts.tone || 'info';
    var element = document.createElement('div');
    element.className = 'ada-toast alert alert-' + toneOf(tone);
    element.setAttribute('role', tone === 'error' ? 'alert' : 'status');
    var icon = svgIcon(iconForTone(tone), 14);
    if (icon) element.appendChild(icon);
    var body = document.createElement('div');
    body.className = 'alert-message';
    if (opts.title) {
      var title = document.createElement('strong');
      title.className = 'alert-title';
      title.textContent = String(opts.title);
      body.appendChild(title);
    }
    var text = document.createElement('div');
    text.textContent = String(message === null || message === undefined ? '' : message);
    body.appendChild(text);
    element.appendChild(body);
    var timer = null;
    var closed = false;
    function remove() {
      if (closed) return;
      closed = true;
      if (timer) {
        clearTimeout(timer);
        timer = null;
      }
      element.className += ' ada-toast--closing';
      setTimeout(function () {
        if (element.parentNode) element.parentNode.removeChild(element);
      }, 140);
    }
    var close = document.createElement('button');
    close.className = 'alert-dismiss';
    close.setAttribute('type', 'button');
    close.setAttribute('aria-label', '关闭');
    close.textContent = '×';
    close.addEventListener('click', remove);
    element.appendChild(close);
    stack.appendChild(element);
    var duration = opts.duration === undefined
      ? (tone === 'error' ? 9000 : 6000)
      : Number(opts.duration);
    if (duration > 0) timer = setTimeout(remove, duration);
    return { element: element, close: remove };
  }

  ['info', 'success', 'warn', 'error'].forEach(function (tone) {
    toast[tone] = function (message, options) {
      var opts = options || {};
      opts.tone = tone;
      return toast(message, opts);
    };
  });

  /* ═══════════════════════════════════════════════════════════════════
   * 安装与导出
   * ═══════════════════════════════════════════════════════════════════ */

  // 别名：不复制实现，只让调用方可以用更常见的名字。
  components.TextField = Input;
  components.Alert = Message;
  components.Range = Slider;
  components.ProgressBar = Progress;
  components.DataTable = Table;
  components.DropdownButton = Dropdown;

  function install(app) {
    if (!app || typeof app.component !== 'function') {
      throw new Error('[ada-ui] install 需要一个 ada-render 的 app');
    }
    for (var name in components) {
      if (Object.prototype.hasOwnProperty.call(components, name) && !app.component(name)) {
        app.component(name, components[name]);
      }
    }
    // Dropdown 没有 components.js 时也要能点外面收起。
    bindDropdownDismiss();
    return app;
  }

  var dropdownDismissBound = false;
  function bindDropdownDismiss() {
    if (dropdownDismissBound || typeof document === 'undefined') return;
    dropdownDismissBound = true;
    document.addEventListener('click', function (event) {
      var current = event.target && event.target.closest ? event.target.closest('details.ada-dropdown') : null;
      var open = document.querySelectorAll('details.ada-dropdown[open]');
      for (var i = 0; i < open.length; i += 1) {
        if (open[i] !== current) open[i].open = false;
      }
    });
    document.addEventListener('keydown', function (event) {
      if (event.key !== 'Escape') return;
      var open = document.querySelectorAll('details.ada-dropdown[open]');
      for (var i = 0; i < open.length; i += 1) open[i].open = false;
    });
  }

  function createApp(options) {
    var app = AdaRender.createApp(options || {});
    install(app);
    return app;
  }

  return {
    version: '0.1.0',
    components: components,
    install: install,
    createApp: createApp,
    toast: toast,
    nextTick: nextTick
  };
});
