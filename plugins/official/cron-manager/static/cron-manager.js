/*
 * 定时任务管理页（cron-manager）的浏览器端。
 *
 * 数据与操作全部走本插件 route（window.adaAPI 自动带 CSRF）：
 *   GET  /state            任务列表（含下次运行预览）
 *   POST /create|update    新建 / 保存修改
 *   POST /remove|toggle    删除 / 启停
 *   POST /trigger          立即运行（cron 二进制下个 tick 消费）
 *   GET  /runs?id=         运行历史（最新在前）
 *
 * 调度输入支持预设（每 N 分钟 / 每天 / 每周）与自定义 cron；预设最终都展开成
 * 5 段 cron 表达式，存储格式统一。编辑复用同一个表单。
 */
(function () {
  "use strict";

  var root = document.querySelector("[data-cm-page]");
  if (!root) return;

  var rowsNode = root.querySelector("[data-cm-rows]");
  var statusNode = root.querySelector("[data-cm-status]");
  var refreshBtn = root.querySelector("[data-cm-refresh]");
  var form = root.querySelector("[data-cm-form]");
  var nameInput = root.querySelector("[data-cm-name]");
  var presetSelect = root.querySelector("[data-cm-preset]");
  var cronInput = root.querySelector("[data-cm-cron]");
  var cronField = root.querySelector("[data-cm-cron-field]");
  var cronPreview = root.querySelector("[data-cm-cron-preview]");
  var dailyField = root.querySelector("[data-cm-daily-field]");
  var dailyTime = root.querySelector("[data-cm-daily-time]");
  var weeklyFields = root.querySelectorAll("[data-cm-weekly-field]");
  var weeklyDay = root.querySelector("[data-cm-weekly-day]");
  var weeklyTime = root.querySelector("[data-cm-weekly-time]");
  var workspaceSelect = root.querySelector("[data-cm-workspace]");
  var textInput = root.querySelector("[data-cm-text]");
  var submitBtn = root.querySelector("[data-cm-submit]");
  var cancelBtn = root.querySelector("[data-cm-cancel]");
  var editingHint = root.querySelector("[data-cm-editing]");
  var runsSection = root.querySelector("[data-cm-runs]");
  var runsName = root.querySelector("[data-cm-runs-name]");
  var runsRows = root.querySelector("[data-cm-runs-rows]");
  var runsClose = root.querySelector("[data-cm-runs-close]");

  var state = null;
  var busy = false;
  var editingId = null;
  // 工作区列表来自网关 GET /api/workspaces；空字符串代表默认工作区。
  var workspaceList = [];
  var workspaceDefault = "";
  var DEFAULT_WORKSPACE_LABEL = "综合工作区（默认）";

  function status(message, tone) {
    if (!statusNode) return;
    statusNode.textContent = message || "";
    if (tone) statusNode.setAttribute("data-tone", tone);
    else statusNode.removeAttribute("data-tone");
  }

  function errorText(err) {
    var message = err && err.message ? String(err.message) : String(err);
    var cut = message.indexOf("失败: ");
    if (cut >= 0) message = message.slice(cut + "失败: ".length);
    return message || "操作失败";
  }

  function api(path, options) {
    if (typeof window.adaAPI !== "function") {
      throw new Error("页面缺少 adaAPI，无法调用插件接口");
    }
    return window.adaAPI("cron-manager", path, options || {});
  }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function formatTime(value) {
    if (!value) return "—";
    var d = new Date(value);
    if (isNaN(d.getTime())) return String(value);
    return d.toLocaleString();
  }

  function duration(ms) {
    var n = Number(ms) || 0;
    if (n <= 0) return "—";
    if (n < 1000) return n + " ms";
    return (n / 1000).toFixed(1) + " s";
  }

  var RUN_LABELS = {
    error: "失败",
    busy: "线程忙",
    started: "已启动",
    missed: "错过",
  };

  function runLabel(status) {
    return RUN_LABELS[status] || String(status || "—");
  }

  function lastRunText(task) {
    var runs = (task && task.runs) || [];
    if (!runs.length) return "—";
    var last = runs[runs.length - 1];
    return runLabel(last.status);
  }

  function lastRunTone(task) {
    var runs = (task && task.runs) || [];
    if (!runs.length) return "";
    var s = runs[runs.length - 1].status;
    if (s === "started") return "ok";
    if (s === "busy" || s === "missed") return "warn";
    return "bad";
  }

  // ---- 下拉组件（ada-select）----
  // 页面上的下拉都是宿主的 <ada-select> 组件（触发器 + listbox + 隐藏 input），
  // 不再用原生下拉控件。下面几个 helper 统一读写组件状态；即使组件尚未升级，
  // 读隐藏 input 也能拿到值。

  var optionSeq = 0;

  function selectValue(node) {
    if (!node || !node.querySelector) return "";
    var input = node.querySelector("[data-select-input]");
    return input ? String(input.value || "") : "";
  }

  function selectOptions(node) {
    if (!node || !node.querySelectorAll) return [];
    return Array.prototype.slice.call(node.querySelectorAll("[data-select-menu] [data-value]"));
  }

  function hasSelectValue(node, value) {
    var target = String(value === undefined || value === null ? "" : value);
    var options = selectOptions(node);
    for (var i = 0; i < options.length; i++) {
      if (options[i].getAttribute("data-value") === target) return true;
    }
    return false;
  }

  function appendSelectOption(node, value, text) {
    if (!node) return null;
    var menu = node.querySelector("[data-select-menu]");
    if (!menu) return null;
    var option = document.createElement("button");
    option.type = "button";
    option.className = "ada-select__option";
    option.id = "cm-select-opt-" + String(++optionSeq);
    option.setAttribute("data-value", value);
    option.setAttribute("role", "option");
    option.setAttribute("aria-selected", "false");
    option.textContent = text;
    option.title = text;
    menu.appendChild(option);
    return option;
  }

  function setSelectValue(node, value) {
    if (!node) return;
    var target = String(value === undefined || value === null ? "" : value);
    var selected = null;
    var options = selectOptions(node);
    for (var i = 0; i < options.length; i++) {
      var on = options[i].getAttribute("data-value") === target;
      options[i].classList.toggle("is-selected", on);
      options[i].setAttribute("aria-selected", on ? "true" : "false");
      if (on) selected = options[i];
    }
    var input = node.querySelector("[data-select-input]");
    if (input) input.value = target;
    var label = node.querySelector("[data-select-label]");
    if (label) label.textContent = selected ? String(selected.textContent || "").trim() : target;
  }

  // ---- 工作区 ----

  function workspaceLabel(path) {
    var raw = String(path || "").replace(/[\\/]+$/, "");
    if (!raw) return DEFAULT_WORKSPACE_LABEL;
    var parts = raw.split(/[\\/]/);
    return parts[parts.length - 1] || raw;
  }

  function setWorkspaceValue(value) {
    if (!workspaceSelect) return;
    var target = String(value || "");
    if (target && !hasSelectValue(workspaceSelect, target)) {
      // 任务指向的工作区已不在列表里：保留原值可编辑/改回默认，而不是悄悄改掉。
      appendSelectOption(workspaceSelect, target, workspaceLabel(target) + " — " + target + "（不在当前列表）");
    }
    setSelectValue(workspaceSelect, target);
  }

  function renderWorkspaceOptions(selected) {
    if (!workspaceSelect) return;
    var menu = workspaceSelect.querySelector("[data-select-menu]");
    if (!menu) return;
    menu.textContent = "";
    appendSelectOption(workspaceSelect, "", DEFAULT_WORKSPACE_LABEL);
    var seen = {};
    for (var i = 0; i < workspaceList.length; i++) {
      var ws = workspaceList[i] || {};
      var path = String(ws.path || "");
      if (!path || seen[path]) continue;
      seen[path] = true;
      appendSelectOption(
        workspaceSelect,
        path,
        (ws.name ? String(ws.name) : workspaceLabel(path)) + " — " + path
      );
    }
    setWorkspaceValue(selected || "");
  }

  async function loadWorkspaces() {
    if (!workspaceSelect) return;
    try {
      var response = await fetch("/api/workspaces", {
        headers: { Accept: "application/json" },
        credentials: "same-origin",
      });
      if (!response.ok) throw new Error("HTTP " + response.status);
      var data = await response.json();
      workspaceDefault = data && data.default ? String(data.default) : "";
      workspaceList = (data && data.workspaces) || [];
      renderWorkspaceOptions(selectValue(workspaceSelect));
    } catch (err) {
      // 读不到列表不阻塞页面：保留「默认」一项，任务仍会在综合工作区运行。
      console.warn("cron-manager: 读取工作区列表失败", err);
    }
  }

  // ---- 调度预设 → cron 表达式 ----

  function presetValue() {
    var kind = String(selectValue(presetSelect) || "custom");
    if (kind === "daily") {
      var t = dailyTime && dailyTime.value ? dailyTime.value : "09:00";
      var parts = t.split(":");
      return (Number(parts[0]) || 0) + " " + (Number(parts[1]) || 0) + " * * *";
    }
    if (kind === "weekly") {
      var wt = weeklyTime && weeklyTime.value ? weeklyTime.value : "09:00";
      var wp = wt.split(":");
      return (Number(wp[0]) || 0) + " " + (Number(wp[1]) || 0) + " * * " + (selectValue(weeklyDay) || "1");
    }
    if (kind === "custom") {
      return cronInput ? String(cronInput.value || "").trim() : "";
    }
    return kind; // 直接是 cron 的预设（每 5 分钟等）
  }

  function renderPresetFields() {
    var kind = String(selectValue(presetSelect) || "custom");
    if (dailyField) dailyField.classList.toggle("cm-hidden", kind !== "daily");
    if (weeklyFields && weeklyFields.length) {
      for (var i = 0; i < weeklyFields.length; i++) weeklyFields[i].classList.toggle("cm-hidden", kind !== "weekly");
    }
    if (cronField) cronField.classList.toggle("cm-hidden", kind !== "custom");
    if (cronPreview) {
      var expr = presetValue();
      cronPreview.textContent = kind === "custom" ? "" : "= " + (expr || "（无效预设）");
    }
  }

  function cronForSave() {
    var kind = String(selectValue(presetSelect) || "custom");
    if (kind === "custom") {
      var raw = cronInput ? String(cronInput.value || "").trim() : "";
      if (!raw) throw new Error("需要 cron 表达式");
      return raw;
    }
    var expr = presetValue();
    if (!expr || expr.split(/\s+/).length !== 5) throw new Error("预设生成的 cron 表达式无效");
    return expr;
  }

  function syncCronPreviewFromTask(cron) {
    // 编辑回填：尽量把表达式映射回预设；映射不上就落到「自定义」。
    var matched = hasSelectValue(presetSelect, cron);
    if (matched) {
      setSelectValue(presetSelect, cron);
    } else {
      var m = /^(\d{1,2}) (\d{1,2}) \* \* (\d)$/.exec(cron || "");
      if (m) {
        setSelectValue(presetSelect, "weekly");
        setSelectValue(weeklyDay, m[3]);
        if (weeklyTime) weeklyTime.value = (m[1].length < 2 ? "0" + m[1] : m[1]) + ":" + (m[2].length < 2 ? "0" + m[2] : m[2]);
        matched = true;
      } else {
        m = /^(\d{1,2}) (\d{1,2}) \* \* \*$/.exec(cron || "");
        if (m) {
          setSelectValue(presetSelect, "daily");
          if (dailyTime) dailyTime.value = (m[1].length < 2 ? "0" + m[1] : m[1]) + ":" + (m[2].length < 2 ? "0" + m[2] : m[2]);
          matched = true;
        }
      }
    }
    if (!matched) {
      setSelectValue(presetSelect, "custom");
      if (cronInput) cronInput.value = cron || "";
    }
    renderPresetFields();
  }

  // ---- 任务列表渲染 ----

  function actionButtons(task) {
    var td = el("td", "cm-col-actions");
    var trigger = el("button", "ada-btn ada-btn--secondary ada-btn--sm", "立即运行");
    trigger.type = "button";
    trigger.setAttribute("data-cm-trigger", task.id);
    td.appendChild(trigger);
    var edit = el("button", "ada-btn ada-btn--ghost ada-btn--sm", "编辑");
    edit.type = "button";
    edit.setAttribute("data-cm-edit", task.id);
    td.appendChild(edit);
    var runs = el("button", "ada-btn ada-btn--ghost ada-btn--sm", "历史");
    runs.type = "button";
    runs.setAttribute("data-cm-history", task.id);
    td.appendChild(runs);
    var remove = el("button", "ada-btn ada-btn--danger ada-btn--sm", "删除");
    remove.type = "button";
    remove.setAttribute("data-cm-remove", task.id);
    td.appendChild(remove);
    return td;
  }

  function renderTasks(tasks, warnings) {
    if (!rowsNode) return;
    rowsNode.textContent = "";
    if (warnings && warnings.length) {
      status("部分任务被跳过：" + warnings.join("；"), "warn");
    }
    if (!tasks.length) {
      var tr = document.createElement("tr");
      var td = el("td", "cm-empty", "还没有定时任务。用上面的表单创建第一个 AI 任务。");
      td.setAttribute("colspan", "7");
      tr.appendChild(td);
      rowsNode.appendChild(tr);
      return;
    }
    tasks.forEach(function (task) {
      var tr = document.createElement("tr");

      var enabledTd = el("td", "cm-col-enabled");
      var toggle = el("button", "cm-toggle", task.enabled ? "启用" : "停用");
      toggle.type = "button";
      toggle.setAttribute("data-cm-toggle", task.id);
      toggle.setAttribute("data-enabled", task.enabled ? "1" : "0");
      toggle.title = task.enabled ? "点击停用" : "点击启用";
      enabledTd.appendChild(toggle);
      tr.appendChild(enabledTd);

      var nameTd = el("td", "cm-col-name");
      nameTd.appendChild(el("span", "cm-name", task.name || ""));
      if (task.triggerRequested) nameTd.appendChild(el("span", "cm-chip", "待运行"));
      tr.appendChild(nameTd);

      var workspacePath = String(task.workspace || "");
      var workspaceTd = el("td", "cm-col-workspace", workspacePath ? workspaceLabel(workspacePath) : "综合");
      workspaceTd.title = workspacePath || (workspaceDefault ? "综合工作区（默认：" + workspaceDefault + "）" : DEFAULT_WORKSPACE_LABEL);
      tr.appendChild(workspaceTd);

      tr.appendChild(el("td", "cm-col-cron", task.cron || ""));
      tr.appendChild(el("td", "cm-col-time", task.nextRunAt ? formatTime(task.nextRunAt) : "—"));

      var lastTd = el("td", "cm-col-time");
      lastTd.appendChild(el("span", "cm-run cm-run--" + (lastRunTone(task) || "none"), lastRunText(task)));
      tr.appendChild(lastTd);

      tr.appendChild(actionButtons(task));
      rowsNode.appendChild(tr);
    });
  }

  // loadState 是只读刷新：不受 busy 守卫（否则写操作完成后的内部刷新会被
  // 吞掉，表格停在旧数据）；写操作各自的 busy 守卫防的是重复提交。
  async function loadState(silent) {
    try {
      var data = await api("/state");
      state = data || {};
      renderTasks(state.tasks || [], state.warnings);
      if (!silent) status(state.tasks && state.tasks.length ? "已加载 " + state.tasks.length + " 个任务。" : "暂无任务。");
    } catch (err) {
      status("读取失败：" + errorText(err), "bad");
    }
  }

  // ---- 运行历史 ----

  var runsTaskId = null;
  var triggerPollToken = 0;

  function taskByID(id) {
    var found = null;
    ((state && state.tasks) || []).forEach(function (t) {
      if (t.id === id) found = t;
    });
    return found;
  }

  function runCountOf(id) {
    var task = taskByID(id);
    return task && task.runs ? task.runs.length : 0;
  }

  async function loadRuns(id) {
    if (!runsRows) return;
    runsRows.textContent = "";
    try {
      var data = await api("/runs?id=" + encodeURIComponent(id) + "&limit=20");
      var runs = (data && data.runs) || [];
      if (!runs.length) {
        var tr = document.createElement("tr");
        var td = el("td", "cm-empty", "还没有运行记录。任务到期或点「立即运行」后这里会出现历史。");
        td.setAttribute("colspan", "4");
        tr.appendChild(td);
        runsRows.appendChild(tr);
        return;
      }
      runs.forEach(function (run) {
        var tr = document.createElement("tr");
        tr.appendChild(el("td", "cm-col-time", formatTime(run.at)));
        var st = el("td", "cm-col-status");
        st.appendChild(el("span", "cm-run cm-run--" + (run.status === "started" ? "ok" : run.status === "busy" || run.status === "missed" ? "warn" : run.status ? "bad" : "none"), runLabel(run.status)));
        tr.appendChild(st);
        tr.appendChild(el("td", "cm-col-size", duration(run.durationMs)));
        var detail = run.detail || "";
        if (run.threadId) detail = (detail ? detail + " · " : "") + "线程 " + run.threadId;
        tr.appendChild(el("td", "cm-value", detail || "—"));
        runsRows.appendChild(tr);
      });
    } catch (err) {
      status("读取历史失败：" + errorText(err), "bad");
    }
  }

  async function showRuns(id) {
    var task = taskByID(id);
    if (!runsSection) return;
    runsTaskId = id;
    runsSection.classList.remove("cm-hidden");
    if (runsName) runsName.textContent = task ? task.name : id;
    await loadRuns(id);
  }

  function hideRuns() {
    runsTaskId = null;
    if (runsSection) runsSection.classList.add("cm-hidden");
  }

  // 「立即运行」是异步的（cron 二进制下个 tick 消费），触发后轮询表格与历史，
  // 让用户看到「已启动/线程忙/失败」而不是一片安静。
  function waitForTriggerResult(id, before) {
    var token = ++triggerPollToken;
    var deadline = Date.now() + 15000;
    function tick() {
      if (token !== triggerPollToken) return;
      loadState(true).then(function () {
        if (runsTaskId === id) loadRuns(id);
        var task = taskByID(id);
        var runs = (task && task.runs) || [];
        if (runs.length > before) {
          var last = runs[runs.length - 1];
          var tone = last.status === "started" ? "ok" : last.status === "busy" || last.status === "missed" ? "warn" : "bad";
          status("立即运行结果：" + runLabel(last.status) + (last.detail ? "（" + last.detail + "）" : ""), tone);
          return;
        }
        if (Date.now() > deadline) {
          status("已触发；结果还没写入，稍后点「历史」查看。若一直为空，请确认 cron 调度器已启动。", "warn");
          return;
        }
        setTimeout(tick, 2000);
      });
    }
    setTimeout(tick, 1500);
  }

  // ---- 表单 ----

  function fillForm(task) {
    editingId = task.id;
    if (nameInput) nameInput.value = task.name || "";
    syncCronPreviewFromTask(task.cron || "");
    setWorkspaceValue(task.workspace || "");
    var action = task.action || {};
    if (textInput) textInput.value = action.text || "";
    if (submitBtn) submitBtn.textContent = "保存修改";
    if (cancelBtn) cancelBtn.classList.remove("cm-hidden");
    if (editingHint) editingHint.classList.remove("cm-hidden");
    if (nameInput) nameInput.focus();
  }

  function resetForm() {
    editingId = null;
    if (form) form.reset();
    setSelectValue(presetSelect, "*/5 * * * *");
    if (cronInput) cronInput.value = "";
    renderPresetFields();
    setWorkspaceValue("");
    if (submitBtn) submitBtn.textContent = "创建任务";
    if (cancelBtn) cancelBtn.classList.add("cm-hidden");
    if (editingHint) editingHint.classList.add("cm-hidden");
  }

  async function submitForm(event) {
    event.preventDefault();
    if (busy) return;
    var name = nameInput ? String(nameInput.value || "").trim() : "";
    if (!name) {
      status("请填写任务名。", "warn");
      return;
    }
    var cron;
    try {
      cron = cronForSave();
    } catch (err) {
      status(errorText(err), "warn");
      return;
    }
    var text = textInput ? String(textInput.value || "").trim() : "";
    if (!text) {
      status("AI 任务需要提示词。", "warn");
      return;
    }
    var workspace = selectValue(workspaceSelect);
    var payload = { name: name, cron: cron, workspace: workspace, action: { type: "agent", text: text }, enabled: true };
    busy = true;
    try {
      if (editingId) {
        await api("/update", { method: "POST", body: { id: editingId, task: payload } });
        status("已保存修改。cron 二进制最迟约 5 秒内生效。", "ok");
      } else {
        await api("/create", { method: "POST", body: payload });
        status("已创建任务。cron 二进制最迟约 5 秒内生效。", "ok");
      }
      resetForm();
      await loadState(true);
    } catch (err) {
      status((editingId ? "保存失败：" : "创建失败：") + errorText(err), "bad");
    } finally {
      busy = false;
    }
  }

  // ---- 行操作 ----

  async function onToggle(id) {
    if (busy) return;
    busy = true;
    try {
      var enabled = null;
      ((state && state.tasks) || []).forEach(function (t) {
        if (t.id === id) enabled = !t.enabled;
      });
      await api("/toggle", { method: "POST", body: { id: id, enabled: enabled } });
      await loadState(true);
      status(enabled ? "已启用。" : "已停用。", "ok");
    } catch (err) {
      status("操作失败：" + errorText(err), "bad");
    } finally {
      busy = false;
    }
  }

  async function onTrigger(id) {
    if (busy) return;
    busy = true;
    try {
      var before = runCountOf(id);
      await api("/trigger", { method: "POST", body: { id: id } });
      status("已标记立即运行，cron 二进制最迟约 5 秒内执行；正在等待结果…", "ok");
      await loadState(true);
      waitForTriggerResult(id, before);
    } catch (err) {
      status("操作失败：" + errorText(err), "bad");
    } finally {
      busy = false;
    }
  }

  function onRemove(id) {
    if (busy || typeof window.adaConfirm !== "function") return;
    var name = "";
    ((state && state.tasks) || []).forEach(function (t) {
      if (t.id === id) name = t.name;
    });
    window.adaConfirm("删除定时任务「" + (name || id) + "」？运行历史会一并删除。", async function () {
      busy = true;
      try {
        await api("/remove", { method: "POST", body: { id: id } });
        if (editingId === id) resetForm();
        await loadState(true);
        status("已删除。", "ok");
      } catch (err) {
        status("删除失败：" + errorText(err), "bad");
      } finally {
        busy = false;
      }
    });
  }

  function onEdit(id) {
    ((state && state.tasks) || []).forEach(function (t) {
      if (t.id === id) fillForm(t);
    });
  }

  // ---- 事件绑定 ----

  if (refreshBtn) refreshBtn.addEventListener("click", function () { loadWorkspaces(); loadState(false); });
  if (presetSelect) presetSelect.addEventListener("ada:select", renderPresetFields);
  if (dailyTime) dailyTime.addEventListener("change", renderPresetFields);
  if (weeklyDay) weeklyDay.addEventListener("ada:select", renderPresetFields);
  if (weeklyTime) weeklyTime.addEventListener("change", renderPresetFields);
  if (form) form.addEventListener("submit", submitForm);
  if (cancelBtn) cancelBtn.addEventListener("click", resetForm);
  if (runsClose) runsClose.addEventListener("click", hideRuns);
  if (rowsNode) {
    rowsNode.addEventListener("click", function (event) {
      var target = event.target;
      if (!target || !target.getAttribute) return;
      var id = target.getAttribute("data-cm-toggle");
      if (id) return onToggle(id);
      id = target.getAttribute("data-cm-trigger");
      if (id) return onTrigger(id);
      id = target.getAttribute("data-cm-remove");
      if (id) return onRemove(id);
      id = target.getAttribute("data-cm-edit");
      if (id) return onEdit(id);
      id = target.getAttribute("data-cm-history");
      if (id) return showRuns(id);
    });
  }

  renderPresetFields();
  loadWorkspaces();
  loadState(false);
})();
