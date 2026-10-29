/*
 * 供应商管理（provider-admin）的浏览器端：Master-Detail 的模型设置。
 *
 * 所有数据与写操作都走本插件后端 route（/state、/save、/remove、
 * /fetch-models、/select）。API Key 只由后端保存，页面只看到尾号。
 */
(function () {
  "use strict";

  var state = {
    providers: [],
    current: { providerId: "", modelId: "" },
    selectedId: "",
    editingIndex: -1,
    busy: false,
  };
  var root = null;

  // ── 小工具 ─────────────────────────────────────────────────────────────

  function icon(name, size) {
    var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "pm-ico");
    svg.setAttribute("width", String(size || 14));
    svg.setAttribute("height", String(size || 14));
    svg.setAttribute("aria-hidden", "true");
    var use = document.createElementNS("http://www.w3.org/2000/svg", "use");
    use.setAttribute("href", "#i-" + name);
    svg.appendChild(use);
    return svg;
  }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function btn(className, label, iconName, onClick) {
    var b = document.createElement("button");
    b.type = "button";
    b.className = className;
    if (iconName) b.appendChild(icon(iconName, 14));
    if (label) {
      var span = el("span", "", label);
      b.appendChild(span);
    }
    if (onClick) b.addEventListener("click", onClick);
    return b;
  }

  function status(message, tone) {
    var node = root && root.querySelector("[data-pm-status]");
    if (!node) return;
    node.textContent = message || "";
    if (tone) node.setAttribute("data-tone", tone);
    else node.removeAttribute("data-tone");
  }

  function fail(err) {
    status((err && err.message ? err.message : String(err)) || "操作失败", "error");
  }

  function api(path, body) {
    if (!window.adaAPI) throw new Error("页面缺少 adaAPI");
    if (body === undefined) return window.adaAPI("provider-admin", path);
    return window.adaAPI("provider-admin", path, { method: "POST", body: body });
  }

  // 使用基座的确认弹窗（ada-modal），不再弹原生 window.confirm。
  function confirmAction(message, onConfirm) {
    var dialog = document.querySelector("[data-confirm-dialog]");
    if (!dialog || !window.AdaComponents || typeof window.AdaComponents.openModal !== "function") {
      status("缺少确认弹窗组件，操作已取消。", "error");
      return;
    }
    var messageNode = dialog.querySelector("[data-confirm-message]");
    if (messageNode) messageNode.textContent = message;
    var ok = dialog.querySelector("[data-confirm-ok]");
    if (!ok) {
      status("确认弹窗缺少确认按钮，操作已取消。", "error");
      return;
    }
    function cleanup() {
      ok.removeEventListener("click", onOk);
      dialog.removeEventListener("ada:modal-close", cleanup);
    }
    function onOk() {
      cleanup();
      onConfirm();
    }
    ok.addEventListener("click", onOk);
    dialog.addEventListener("ada:modal-close", cleanup);
    window.AdaComponents.openModal("confirm");
  }

  // 用 ada-select 渲染一个下拉；options 是 [value, label] 数组。
  function adaSelect(options, currentValue, onChange) {
    var wrap = document.createElement("ada-select");
    wrap.className = "ada-select";

    var trigger = document.createElement("button");
    trigger.type = "button";
    trigger.className = "ada-select__trigger";
    trigger.setAttribute("data-select-trigger", "");
    trigger.setAttribute("aria-expanded", "false");
    var label = el("span", "ada-select__label");
    var caret = el("span", "ada-select__caret");
    caret.appendChild(icon("chevron-down", 12));
    trigger.appendChild(label);
    trigger.appendChild(caret);
    wrap.appendChild(trigger);

    var menu = el("div", "ada-select__menu");
    menu.setAttribute("data-select-menu", "");
    menu.hidden = true;
    options.forEach(function (opt) {
      var selected = opt[0] === currentValue;
      var item = document.createElement("button");
      item.type = "button";
      item.className = "ada-select__option" + (selected ? " is-selected" : "");
      item.setAttribute("data-value", opt[0]);
      item.setAttribute("aria-selected", selected ? "true" : "false");
      item.textContent = opt[1];
      if (selected) label.textContent = opt[1];
      menu.appendChild(item);
    });
    wrap.appendChild(menu);

    var hidden = document.createElement("input");
    hidden.type = "hidden";
    hidden.setAttribute("data-select-input", "");
    hidden.value = currentValue;
    wrap.appendChild(hidden);
    if (!label.textContent) label.textContent = "请选择";

    wrap.addEventListener("ada:select", function (event) {
      var value = event.detail && event.detail.value;
      if (!value) return;
      onChange(value);
    });
    return wrap;
  }

  function selected() {
    for (var i = 0; i < state.providers.length; i++) {
      if (state.providers[i].id === state.selectedId) return state.providers[i];
    }
    return null;
  }

  function replaceProvider(info) {
    for (var i = 0; i < state.providers.length; i++) {
      if (state.providers[i].id === info.id) {
        state.providers[i] = info;
        return;
      }
    }
    state.providers.push(info);
  }

  // ── 数据加载与保存 ─────────────────────────────────────────────────────

  function load() {
    if (!window.adaAPI) {
      status("页面脚本未加载完成，请刷新后重试。", "error");
      return;
    }
    api("/state")
      .then(function (data) {
        state.providers = Array.isArray(data && data.providers) ? data.providers : [];
        state.current = (data && data.current) || { providerId: "", modelId: "" };
        var still = false;
        for (var i = 0; i < state.providers.length; i++) {
          if (state.providers[i].id === state.selectedId) still = true;
        }
        if (!still) state.selectedId = state.providers.length ? state.providers[0].id : "";
        state.editingIndex = -1;
        render();
      })
      .catch(fail);
  }

  function saveProvider(patch, okMessage) {
    var p = selected();
    if (!p) return Promise.resolve();
    var body = {
      id: p.id,
      name: p.displayName || p.id,
      protocol: p.protocol || "openai_chat",
      baseUrl: p.baseUrl || "",
      disabled: !!p.disabled,
      models: (p.models || []).map(function (m) {
        return {
          id: m.id,
          displayName: m.displayName || "",
          contextWindow: Number(m.contextWindow || 0),
          maxOutput: Number(m.maxOutput || 0),
          input: Array.isArray(m.input) ? m.input : ["text"],
          supportsTools: !!m.supportsTools,
          supportsThinking: !!m.supportsThinking,
          disabled: !!m.disabled,
        };
      }),
    };
    for (var key in patch) {
      if (Object.prototype.hasOwnProperty.call(patch, key)) body[key] = patch[key];
    }
    state.busy = true;
    return api("/save", body)
      .then(function (info) {
        replaceProvider(info);
        if (okMessage) status(okMessage, "success");
        render();
      })
      .catch(fail)
      .then(function () { state.busy = false; });
  }

  function createProvider() {
    state.busy = true;
    api("/save", {
      name: "新供应商",
      protocol: "openai_chat",
      baseUrl: "",
      models: [],
    })
      .then(function (info) {
        state.providers.push(info);
        state.selectedId = info.id;
        status("已添加供应商，填写端点与密钥后即可使用。", "success");
        render();
      })
      .catch(fail)
      .then(function () { state.busy = false; });
  }

  function removeProvider() {
    var p = selected();
    if (!p) return;
    confirmAction("删除供应商「" + (p.displayName || p.id) + "」？其模型配置会一起删除。", function () {
      api("/remove", { id: p.id })
        .then(function () {
          state.providers = state.providers.filter(function (x) { return x.id !== p.id; });
          state.selectedId = state.providers.length ? state.providers[0].id : "";
          status("供应商已删除", "success");
          render();
        })
        .catch(fail);
    });
  }

  function fetchModels() {
    var p = selected();
    if (!p) return;
    status("正在从供应商拉取模型列表…", "");
    api("/fetch-models", { id: p.id })
      .then(function (models) {
        var existing = {};
        (p.models || []).forEach(function (m) { existing[m.id] = true; });
        var merged = (p.models || []).slice();
        (models || []).forEach(function (m) {
          if (!existing[m.id]) {
            merged.push({
              id: m.id,
              displayName: m.displayName || "",
              contextWindow: m.contextWindow || 0,
              maxOutput: m.maxOutput || 0,
              input: m.input || ["text"],
              supportsTools: m.supportsTools !== false,
              supportsThinking: !!m.supportsThinking,
              disabled: false,
            });
            existing[m.id] = true;
          }
        });
        return saveProvider({ models: merged }, "已拉取 " + (models || []).length + " 个模型");
      })
      .catch(fail);
  }

  function selectModel(modelId) {
    var p = selected();
    if (!p) return;
    api("/select", { providerId: p.id, modelId: modelId })
      .then(function (cur) {
        state.current = cur || { providerId: p.id, modelId: modelId };
        status("当前模型已切换", "success");
        render();
      })
      .catch(fail);
  }

  // ── 列表 ───────────────────────────────────────────────────────────────

  function render() {
    renderList();
    renderDetail();
  }

  function renderList() {
    var list = root.querySelector("[data-pm-list]");
    if (!list) return;
    list.textContent = "";
    if (!state.providers.length) {
      list.appendChild(el("p", "pm-empty", "还没有供应商，点击右上角「添加供应商」。"));
      return;
    }
    list.appendChild(el("p", "pm-group", "供应商"));
    state.providers.forEach(function (p) {
      var item = el("button", "pm-item" + (p.id === state.selectedId ? " is-active" : ""));
      item.type = "button";
      var unavailable = p.disabled || p.available === false;
      item.appendChild(el("span", "pm-dot" + (unavailable ? " is-off" : "")));
      if (p.available === false) item.title = "适配器插件未启用";
      item.appendChild(el("span", "pm-item__name", p.displayName || p.id));
      if (state.current.providerId === p.id) {
        item.appendChild(el("span", "pm-item__badge", "当前"));
      }
      item.addEventListener("click", function () {
        state.selectedId = p.id;
        state.editingIndex = -1;
        render();
      });
      list.appendChild(item);
    });
  }

  // ── 详情 ───────────────────────────────────────────────────────────────

  function field(labelText, control) {
    var wrap = el("label", "pm-field");
    wrap.appendChild(el("span", "pm-field__label", labelText));
    wrap.appendChild(control);
    return wrap;
  }

  function input(className, value, placeholder) {
    var node = document.createElement("input");
    node.className = className;
    node.type = "text";
    node.value = value || "";
    if (placeholder) node.placeholder = placeholder;
    return node;
  }

  function renderDetail() {
    var box = root.querySelector("[data-pm-detail]");
    if (!box) return;
    box.textContent = "";
    var p = selected();
    if (!p) {
      var ph = el("div", "pm-placeholder");
      ph.appendChild(el("span", "pm-placeholder__title", "选择一个供应商"));
      ph.appendChild(el("span", "pm-placeholder__hint", "从左侧选择，或添加一个新的供应商。"));
      box.appendChild(ph);
      return;
    }

    // 头部：名称 + 当前标记 + 启用开关 + 更多
    var head = el("div", "pm-detail__head");
    var titleWrap = el("div", "pm-detail__title");
    var name = input("pm-name", p.displayName || p.id, "供应商名称");
    name.addEventListener("change", function () { saveProvider({ name: name.value.trim() || p.id }); });
    titleWrap.appendChild(name);
    if (state.current.providerId === p.id) {
      titleWrap.appendChild(el("span", "pm-badge", "当前使用"));
    }
    head.appendChild(titleWrap);

    var actions = el("div", "pm-detail__actions");
    var switchLabel = el("label", "pm-switch");
    switchLabel.title = p.disabled ? "启用供应商" : "停用供应商";
    var toggle = document.createElement("input");
    toggle.type = "checkbox";
    toggle.checked = !p.disabled;
    toggle.addEventListener("change", function () {
      saveProvider({ disabled: !toggle.checked }, toggle.checked ? "供应商已启用" : "供应商已停用");
    });
    switchLabel.appendChild(toggle);
    switchLabel.appendChild(el("span", "pm-switch__track"));
    actions.appendChild(switchLabel);
    actions.appendChild(btn("pm-icon-btn", "", "more", removeProvider));
    head.appendChild(actions);
    box.appendChild(head);

    if (p.available === false) {
      box.appendChild(el("div", "pm-notice", "供应商适配器插件未启用：请到「插件」页启用对应的 provider 插件后使用。"));
    }

    // 连接信息
    var base = input("pm-input", p.baseUrl || "", "https://api.example.com/v1");
    base.addEventListener("change", function () { saveProvider({ baseUrl: base.value.trim() }); });
    box.appendChild(field("Base URL", base));

    var protocol = adaSelect(
      [
        ["openai_chat", "Chat Completions (/chat/completions)"],
        ["openai_responses", "OpenAI Responses"],
        ["anthropic", "Anthropic Messages"],
      ],
      p.protocol || "openai_chat",
      function (value) { saveProvider({ protocol: value }, "API 格式已更新"); }
    );
    box.appendChild(field("API 格式", protocol));

    // API Key：留空保持原值
    var keyRow = el("div", "pm-key");
    var keyInput = document.createElement("input");
    keyInput.className = "pm-input pm-key__input";
    keyInput.type = "password";
    keyInput.value = "";
    keyInput.autocomplete = "new-password";
    keyInput.placeholder = p.hasApiKey
      ? "已配置 " + (p.apiKeyHint || "") + "，留空保持不变"
      : "填写 API Key";
    var reveal = btn("pm-icon-btn", "", "search", function () {
      keyInput.type = keyInput.type === "password" ? "text" : "password";
    });
    reveal.title = "显示/隐藏密钥";
    var clear = btn("pm-icon-btn", "", "trash", function () {
      if (!p.hasApiKey && !keyInput.value) return;
      confirmAction("清空这个供应商的 API Key？", function () {
        keyInput.value = "";
        saveProvider({ apiKey: "", clearApiKey: true }, "API Key 已清空");
      });
    });
    clear.title = "清空密钥";
    keyRow.appendChild(keyInput);
    keyRow.appendChild(reveal);
    keyRow.appendChild(clear);
    keyInput.addEventListener("change", function () {
      if (keyInput.value.trim()) saveProvider({ apiKey: keyInput.value.trim() }, "API Key 已保存");
    });
    box.appendChild(field("API Key", keyRow));

    // 模型列表
    var modelsHead = el("div", "pm-models__head");
    modelsHead.appendChild(el("span", "pm-models__title", "模型列表"));
    var tools = el("div", "pm-models__actions");
    tools.appendChild(btn("pm-icon-btn", "", "refresh", fetchModels)).title = "从供应商 /models 拉取";
    tools.appendChild(btn("ada-btn ada-btn--secondary ada-btn--sm", "添加模型", "plus", addModel));
    modelsHead.appendChild(tools);
    box.appendChild(modelsHead);

    var models = el("div", "pm-models");
    (p.models || []).forEach(function (m, index) {
      models.appendChild(renderModelRow(m, index));
    });
    if (!(p.models || []).length) {
      models.appendChild(el("p", "pm-empty", "还没有模型。可以手动添加，或点刷新从 /models 拉取。"));
    }
    box.appendChild(models);
  }

  function modelTags(m) {
    var tags = [];
    var ctx = Number(m.contextWindow || 0);
    if (ctx > 0) {
      tags.push(ctx >= 1000000 ? Math.round(ctx / 1000000) + "M" : Math.round(ctx / 1000) + "K");
    }
    var inputs = Array.isArray(m.input) ? m.input : [];
    if (inputs.indexOf("image") >= 0) tags.push("视觉");
    if (m.supportsTools) tags.push("工具");
    if (m.supportsThinking) tags.push("思考");
    return tags;
  }

  function renderModelRow(m, index) {
    var isCurrent = state.current.providerId === (selected() || {}).id && state.current.modelId === m.id;
    var row = el("div", "pm-model" + (m.disabled ? " is-disabled" : "") + (isCurrent ? " is-current" : ""));
    var info = el("div", "pm-model__info");
    info.appendChild(el("span", "pm-model__id", m.id));
    var tagWrap = el("span", "pm-model__tags");
    modelTags(m).forEach(function (t) { tagWrap.appendChild(el("span", "pm-tag", t)); });
    info.appendChild(tagWrap);
    row.appendChild(info);

    var actions = el("div", "pm-model__actions");
    var provider = selected() || {};
    if (isCurrent) {
      actions.appendChild(el("span", "pm-badge", "当前"));
    } else if (!m.disabled && !provider.disabled && provider.available !== false) {
      actions.appendChild(btn("pm-text-btn", "使用", "", function () { selectModel(m.id); }));
    }
    actions.appendChild(btn("pm-icon-btn", "", "edit", function () {
      openModelModal(m, index);
    }));
    actions.appendChild(btn("pm-icon-btn", "", "trash", function () {
      confirmAction("删除模型「" + m.id + "」？", function () { removeModel(index); });
    }));
    var sw = el("label", "pm-switch pm-switch--sm");
    var cb = document.createElement("input");
    cb.type = "checkbox";
    cb.checked = !m.disabled;
    cb.addEventListener("change", function () { toggleModel(index, cb.checked); });
    sw.appendChild(cb);
    sw.appendChild(el("span", "pm-switch__track"));
    actions.appendChild(sw);
    row.appendChild(actions);
    return row;
  }

  function buildModelModal() {
    if (document.getElementById("pm-model-modal")) return;
    var modal = document.createElement("ada-modal");
    modal.id = "pm-model-modal";
    modal.className = "ada-modal";
    modal.setAttribute("data-modal", "pm-model");
    modal.hidden = true;
    modal.innerHTML =
      '<div class="ada-modal__backdrop" data-modal-close></div>' +
      '<div class="ada-modal__panel" role="dialog" aria-modal="true" aria-labelledby="pm-model-title">' +
      '<h2 class="ada-modal__title" id="pm-model-title" data-pm-model-title>编辑模型</h2>' +
      '<form class="pm-model-form" data-pm-model-form>' +
      '<label class="pm-field"><span class="pm-field__label">模型 ID</span><input class="pm-input" name="id" data-modal-autofocus autocomplete="off"></label>' +
      '<label class="pm-field"><span class="pm-field__label">显示名称（可选）</span><input class="pm-input" name="displayName" autocomplete="off"></label>' +
      '<div class="pm-model-form__row">' +
      '<label class="pm-field"><span class="pm-field__label">上下文窗口</span><input class="pm-input" name="contextWindow" inputmode="numeric" autocomplete="off"></label>' +
      '<label class="pm-field"><span class="pm-field__label">最大输出</span><input class="pm-input" name="maxOutput" inputmode="numeric" autocomplete="off"></label>' +
      "</div>" +
      '<div class="pm-model-form__flags">' +
      '<label class="pm-check"><input type="checkbox" name="supportsTools"><span>工具</span></label>' +
      '<label class="pm-check"><input type="checkbox" name="supportsThinking"><span>思考</span></label>' +
      '<label class="pm-check"><input type="checkbox" name="image"><span>视觉</span></label>' +
      "</div>" +
      '<div class="ada-modal__actions">' +
      '<button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-modal-close>取消</button>' +
      '<button class="ada-btn ada-btn--primary ada-btn--sm" type="submit">保存</button>' +
      "</div></form></div>";
    document.body.appendChild(modal);

    var form = modal.querySelector("[data-pm-model-form]");
    form.addEventListener("submit", function (event) {
      event.preventDefault();
      var model = {
        id: String(form.elements.id.value || "").trim(),
        displayName: String(form.elements.displayName.value || "").trim(),
        contextWindow: Number(form.elements.contextWindow.value || 0),
        maxOutput: Number(form.elements.maxOutput.value || 0),
        input: form.elements.image.checked ? ["text", "image"] : ["text"],
        supportsTools: form.elements.supportsTools.checked,
        supportsThinking: form.elements.supportsThinking.checked,
        disabled: false,
      };
      if (!model.id) {
        status("模型 id 不能为空", "error");
        form.elements.id.focus();
        return;
      }
      var index = state.editingIndex;
      if (index >= 0) {
        var existing = allModels()[index];
        if (existing) model.disabled = !!existing.disabled;
      }
      saveModel(model, index);
    });
  }

  function openModelModal(model, index) {
    buildModelModal();
    if (!window.AdaComponents) {
      status("缺少弹窗组件，无法编辑模型。", "error");
      return;
    }
    var modal = document.getElementById("pm-model-modal");
    if (!modal) return;
    state.editingIndex = index;
    var form = modal.querySelector("[data-pm-model-form]");
    var title = modal.querySelector("[data-pm-model-title]");
    if (title) title.textContent = index >= 0 ? "编辑模型" : "添加模型";
    var m = model || {
      id: "", displayName: "", contextWindow: 128000, maxOutput: 0,
      input: ["text"], supportsTools: true, supportsThinking: false, disabled: false,
    };
    form.elements.id.value = m.id || "";
    form.elements.displayName.value = m.displayName || "";
    form.elements.contextWindow.value = m.contextWindow ? String(m.contextWindow) : "";
    form.elements.maxOutput.value = m.maxOutput ? String(m.maxOutput) : "";
    form.elements.supportsTools.checked = !!m.supportsTools;
    form.elements.supportsThinking.checked = !!m.supportsThinking;
    form.elements.image.checked = Array.isArray(m.input) && m.input.indexOf("image") >= 0;
    window.AdaComponents.openModal("pm-model");
  }

  function allModels() {
    var p = selected();
    return (p && p.models ? p.models.slice() : []);
  }

  function saveModel(model, index) {
    var models = allModels();
    if (index >= 0) models[index] = model;
    else models.push(model);
    state.editingIndex = -1;
    if (window.AdaComponents) window.AdaComponents.closeModal("pm-model");
    saveProvider({ models: models }, index >= 0 ? "模型已保存" : "模型已添加");
  }

  function removeModel(index) {
    var models = allModels();
    models.splice(index, 1);
    saveProvider({ models: models }, "模型已删除");
  }

  function toggleModel(index, enabled) {
    var models = allModels();
    if (!models[index]) return;
    models[index].disabled = !enabled;
    saveProvider({ models: models }, enabled ? "模型已启用" : "模型已停用");
  }

  function addModel() {
    openModelModal(null, -1);
  }

  // ── 启动 ───────────────────────────────────────────────────────────────

  function start() {
    root = document.querySelector("[data-provider-admin]");
    if (!root) return;
    buildModelModal();
    var refresh = root.querySelector("[data-pm-refresh]");
    if (refresh) refresh.addEventListener("click", load);
    var add = root.querySelector("[data-pm-add]");
    if (add) add.addEventListener("click", createProvider);
    load();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
