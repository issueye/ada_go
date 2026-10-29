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
    tab: "info",
    busy: false,
  };
  var root = null;

  // 拉取弹窗的独立状态：它是一次性的远端快照，不混进供应商配置里。
  var fetchState = {
    loading: false,
    error: "",
    models: [],
    existing: {},
    seq: 0,
  };

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

  // 宿主会把插件 route 的 reject 包成
  // 「host: 插件 X 的 route 被 reject：TypeError: host: providers.y 失败: <原因>」。
  // 前缀对用户没有意义，界面上只留最后那句原因。
  function errorText(err) {
    var message = err && err.message ? String(err.message) : String(err);
    var cut = message.indexOf("失败: ");
    if (cut >= 0) message = message.slice(cut + "失败: ".length);
    return message || "操作失败";
  }

  function fail(err) {
    status(errorText(err), "error");
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
        state.tab = "info"; // 新供应商先填端点与密钥，模型清单等拉取后再看
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

  // 从供应商拉取模型清单：结果先在弹窗里过一遍再落库，避免把几百个模型
  // 一次性塞进列表。
  function openFetchModal() {
    var p = selected();
    if (!p) return;
    buildFetchModal();
    if (!window.AdaComponents) {
      status("缺少弹窗组件，无法展示模型列表。", "error");
      return;
    }
    var existing = {};
    (p.models || []).forEach(function (m) { existing[m.id] = true; });
    fetchState.existing = existing;
    fetchState.models = [];
    fetchState.error = "";
    fetchState.loading = true;
    fetchState.seq += 1;
    var seq = fetchState.seq;

    var title = document.querySelector("[data-pm-fetch-title]");
    if (title) title.textContent = "从「" + (p.displayName || p.id) + "」获取模型";
    var hint = document.querySelector("[data-pm-fetch-hint]");
    if (hint) hint.textContent = "已配置的模型不可重复添加；取消勾选可跳过。";
    var search = document.querySelector("[data-remote-search]");
    if (search) search.value = "";
    var all = document.querySelector("[data-pm-fetch-all]");
    if (all) { all.checked = false; all.disabled = true; }
    renderFetchBody();
    window.AdaComponents.openModal("pm-fetch");

    api("/fetch-models", { id: p.id })
      .then(function (models) {
        if (seq !== fetchState.seq) return;
        fetchState.loading = false;
        fetchState.models = Array.isArray(models) ? models : [];
        renderFetchBody();
      })
      .catch(function (err) {
        if (seq !== fetchState.seq) return;
        fetchState.loading = false;
        fetchState.error = errorText(err);
        renderFetchBody();
      });
  }

  function fetchAllBox() {
    return document.querySelector("[data-pm-fetch-all]");
  }

  function fetchItems() {
    return document.querySelectorAll("[data-pm-fetch-item]");
  }

  function selectedFetchModels() {
    var byId = {};
    fetchState.models.forEach(function (m) { byId[m.id] = m; });
    var out = [];
    var items = fetchItems();
    for (var i = 0; i < items.length; i++) {
      if (items[i].checked && byId[items[i].value]) out.push(byId[items[i].value]);
    }
    return out;
  }

  // 勾选变化后同步「添加所选（n）」与全选框，避免每次点击都重绘整个列表。
  function syncFetchSelection() {
    var chosen = selectedFetchModels().length;
    var confirm = document.querySelector("[data-pm-fetch-confirm]");
    if (confirm) {
      confirm.disabled = chosen === 0;
      confirm.textContent = chosen ? "添加所选（" + chosen + "）" : "添加所选";
    }
    var all = fetchAllBox();
    if (!all) return;
    var items = fetchItems();
    var total = 0;
    var checked = 0;
    for (var i = 0; i < items.length; i++) {
      if (items[i].disabled) continue;
      total += 1;
      if (items[i].checked) checked += 1;
    }
    all.disabled = total === 0;
    all.checked = total > 0 && checked === total;
    all.indeterminate = checked > 0 && checked < total;
  }

  function renderFetchBody() {
    var list = document.querySelector("[data-pm-fetch-list]");
    var count = document.querySelector("[data-remote-count]");
    var all = fetchAllBox();
    if (!list) return;
    list.textContent = "";
    if (all) { all.checked = false; all.indeterminate = false; all.disabled = true; }
    if (count) count.textContent = "";

    if (fetchState.loading) {
      list.appendChild(el("p", "pm-fetch__state", "正在从供应商拉取模型列表…"));
      syncFetchSelection();
      return;
    }
    if (fetchState.error) {
      var box = el("div", "pm-fetch__state pm-fetch__state--error");
      box.appendChild(el("span", "", fetchState.error));
      var retry = btn("pm-text-btn", "重试", "", function () {
        var p = selected();
        if (!p) return;
        fetchState.loading = true;
        fetchState.error = "";
        fetchState.seq += 1;
        var seq = fetchState.seq;
        renderFetchBody();
        api("/fetch-models", { id: p.id })
          .then(function (models) {
            if (seq !== fetchState.seq) return;
            fetchState.loading = false;
            fetchState.models = Array.isArray(models) ? models : [];
            renderFetchBody();
          })
          .catch(function (err) {
            if (seq !== fetchState.seq) return;
            fetchState.loading = false;
            fetchState.error = errorText(err);
            renderFetchBody();
          });
      });
      box.appendChild(retry);
      list.appendChild(box);
      syncFetchSelection();
      return;
    }
    if (!fetchState.models.length) {
      list.appendChild(el("p", "pm-fetch__state", "供应商没有返回任何模型。"));
      syncFetchSelection();
      return;
    }

    if (count) count.textContent = fetchState.models.length + " 个模型";
    fetchState.models.forEach(function (m) {
      var added = !!fetchState.existing[m.id];
      var row = document.createElement("label");
      row.className = "ada-remote__row pm-fetch__row" + (added ? " is-added" : "");
      row.setAttribute("data-remote-row", "");
      row.setAttribute("data-name", m.id.toLowerCase());
      var cb = document.createElement("input");
      cb.type = "checkbox";
      cb.setAttribute("data-pm-fetch-item", "");
      cb.value = m.id;
      cb.checked = !added;
      cb.disabled = added;
      row.appendChild(cb);
      row.appendChild(el("span", "ada-remote__id", m.id));
      var tags = el("span", "pm-model__tags");
      modelTags(m).forEach(function (t) { tags.appendChild(el("span", "pm-tag", t)); });
      row.appendChild(tags);
      if (added) row.appendChild(el("span", "ada-remote__added", "已添加"));
      list.appendChild(row);
    });
    syncFetchSelection();
  }

  function commitFetch() {
    var p = selected();
    if (!p) return;
    var picked = selectedFetchModels();
    if (!picked.length) {
      window.AdaComponents.closeModal("pm-fetch");
      return;
    }
    var models = (p.models || []).slice();
    picked.forEach(function (m) {
      if (fetchState.existing[m.id]) return;
      models.push({
        id: m.id,
        displayName: m.displayName || "",
        contextWindow: m.contextWindow || 0,
        maxOutput: m.maxOutput || 0,
        input: Array.isArray(m.input) && m.input.length ? m.input : ["text"],
        supportsTools: m.supportsTools !== false,
        supportsThinking: !!m.supportsThinking,
        disabled: false,
      });
    });
    fetchState.seq += 1;
    window.AdaComponents.closeModal("pm-fetch");
    saveProvider({ models: models }, "已添加 " + picked.length + " 个模型");
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

    box.appendChild(renderDetailHead(p));

    if (p.available === false) {
      box.appendChild(el("div", "pm-notice", "供应商适配器插件未启用：请到「插件」页启用对应的 provider 插件后使用。"));
    }

    box.appendChild(renderTabs(p));

    var panel = el("div", "pm-panel");
    panel.setAttribute("role", "tabpanel");
    if (state.tab === "models") {
      panel.id = "pm-panel-models";
      panel.setAttribute("aria-labelledby", "pm-tab-models");
      renderModelsPanel(panel, p);
    } else {
      panel.id = "pm-panel-info";
      panel.setAttribute("aria-labelledby", "pm-tab-info");
      renderInfoPanel(panel, p);
    }
    box.appendChild(panel);
  }

  // 头部：名称 + 当前标记 + 启用开关 + 删除。它与页签无关，切换页签时保持可见。
  function renderDetailHead(p) {
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
    return head;
  }

  // 页签：沿用基座的 .ada-tabs 外观，但由本插件自己切换（详情区整块是 JS 渲染的，
  // 没有服务端 ?tab= 可回退）。
  function renderTabs(p) {
    var bar = el("div", "ada-tabs__list pm-tabs");
    bar.setAttribute("role", "tablist");
    bar.setAttribute("aria-label", "供应商配置");

    var items = [
      { key: "info", label: "供应商信息" },
      { key: "models", label: "模型列表", count: (p.models || []).length },
    ];

    function selectTab(key, focus) {
      if (state.tab === key) return;
      state.tab = key;
      render();
      if (focus) {
        var next = root.querySelector('[data-pm-tab="' + key + '"]');
        if (next) next.focus();
      }
    }

    items.forEach(function (item) {
      var active = state.tab === item.key;
      var tab = el("button", "ada-tabs__tab", item.label);
      tab.type = "button";
      tab.id = "pm-tab-" + item.key;
      tab.setAttribute("role", "tab");
      tab.setAttribute("data-pm-tab", item.key);
      tab.setAttribute("aria-selected", active ? "true" : "false");
      tab.setAttribute("aria-controls", "pm-panel-" + item.key);
      tab.setAttribute("tabindex", active ? "0" : "-1");
      if (item.count !== undefined) tab.appendChild(el("span", "ada-tabs__count", item.count));
      tab.addEventListener("click", function () { selectTab(item.key, false); });
      bar.appendChild(tab);
    });

    bar.addEventListener("keydown", function (event) {
      var index = -1;
      for (var i = 0; i < items.length; i++) {
        if (items[i].key === state.tab) index = i;
      }
      if (index < 0) return;
      var next = -1;
      if (event.key === "ArrowRight") next = (index + 1) % items.length;
      else if (event.key === "ArrowLeft") next = (index - 1 + items.length) % items.length;
      else if (event.key === "Home") next = 0;
      else if (event.key === "End") next = items.length - 1;
      else return;
      event.preventDefault();
      selectTab(items[next].key, true);
    });
    return bar;
  }

  // 页签一：连接与鉴权。
  function renderInfoPanel(panel, p) {
    var base = input("pm-input", p.baseUrl || "", "https://api.example.com/v1");
    base.addEventListener("change", function () { saveProvider({ baseUrl: base.value.trim() }); });
    panel.appendChild(field("Base URL", base));

    var protocol = adaSelect(
      [
        ["openai_chat", "Chat Completions (/chat/completions)"],
        ["openai_responses", "OpenAI Responses"],
        ["anthropic", "Anthropic Messages"],
      ],
      p.protocol || "openai_chat",
      function (value) { saveProvider({ protocol: value }, "API 格式已更新"); }
    );
    panel.appendChild(field("API 格式", protocol));

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
    panel.appendChild(field("API Key", keyRow));

    var models = p.models || [];
    var enabled = models.filter(function (m) { return !m.disabled; }).length;
    panel.appendChild(
      el("p", "pm-hint", "模型清单在「模型列表」页签维护：当前 " + models.length + " 个模型，其中 " + enabled + " 个已启用。")
    );
  }

  // 页签二：模型清单。
  function renderModelsPanel(panel, p) {
    var models = p.models || [];
    var enabled = models.filter(function (m) { return !m.disabled; }).length;

    var head = el("div", "pm-models__head");
    head.appendChild(el("span", "pm-models__hint", "共 " + models.length + " 个模型，其中 " + enabled + " 个已启用"));
    var tools = el("div", "pm-models__actions");
    tools.appendChild(btn("ada-btn ada-btn--secondary ada-btn--sm", "获取模型", "refresh", openFetchModal)).title =
      "从供应商 /models 拉取，弹窗里挑选要添加的模型";
    tools.appendChild(btn("ada-btn ada-btn--primary ada-btn--sm", "添加模型", "plus", addModel));
    head.appendChild(tools);
    panel.appendChild(head);

    var list = el("div", "pm-models");
    models.forEach(function (m, index) {
      list.appendChild(renderModelRow(m, index));
    });
    if (!models.length) {
      list.appendChild(el("p", "pm-empty", "还没有模型。可以手动添加，或点「获取模型」从供应商 /models 拉取。"));
    }
    panel.appendChild(list);
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

  // 拉取结果弹窗：远端可能有上百个模型，所以按「搜索框 + 自己滚的列表」摆，
  // 不把整份清单摊到页面上。
  function buildFetchModal() {
    if (document.getElementById("pm-fetch-modal")) return;
    var modal = document.createElement("ada-modal");
    modal.id = "pm-fetch-modal";
    modal.className = "ada-modal";
    modal.setAttribute("data-modal", "pm-fetch");
    modal.hidden = true;
    modal.innerHTML =
      '<div class="ada-modal__backdrop" data-modal-close></div>' +
      '<div class="ada-modal__panel ada-modal__panel--wide pm-fetch" role="dialog" aria-modal="true" aria-labelledby="pm-fetch-title">' +
      '<h2 class="ada-modal__title" id="pm-fetch-title" data-pm-fetch-title>从供应商获取模型</h2>' +
      '<p class="pm-fetch__hint" data-pm-fetch-hint></p>' +
      '<div class="ada-remote">' +
      '<div class="ada-remote__bar">' +
      '<input class="ada-remote__search" type="search" data-remote-search placeholder="筛选模型（按 ID 搜索）" autocomplete="off">' +
      '<span class="ada-remote__count" data-remote-count></span>' +
      "</div>" +
      '<div class="ada-remote__body" data-pm-fetch-list></div>' +
      "</div>" +
      '<label class="pm-check pm-fetch__all"><input type="checkbox" data-pm-fetch-all><span>全选</span></label>' +
      '<div class="ada-modal__actions">' +
      '<button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-modal-close>取消</button>' +
      '<button class="ada-btn ada-btn--primary ada-btn--sm" type="button" data-pm-fetch-confirm disabled>添加所选</button>' +
      "</div></div>";
    document.body.appendChild(modal);

    modal.addEventListener("change", function (event) {
      var target = event.target;
      if (!target) return;
      if (target.matches("[data-pm-fetch-all]")) {
        var items = fetchItems();
        for (var i = 0; i < items.length; i++) {
          if (!items[i].disabled) items[i].checked = target.checked;
        }
        syncFetchSelection();
        return;
      }
      if (target.matches("[data-pm-fetch-item]")) syncFetchSelection();
    });

    var confirm = modal.querySelector("[data-pm-fetch-confirm]");
    if (confirm) confirm.addEventListener("click", commitFetch);

    // 关闭（取消、Esc、点遮罩）后让在途请求作废。
    modal.addEventListener("ada:modal-close", function () {
      fetchState.seq += 1;
      fetchState.loading = false;
      fetchState.error = "";
      fetchState.models = [];
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
    buildFetchModal();
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
