/*
 * 环境变量管理页（env-manager）的浏览器端。
 *
 * 数据与操作全部走本插件 route（window.adaAPI 自动带 CSRF）：
 *   GET  /state   全局 + 工作区两层变量与合并视图
 *   POST /set     写入一个变量（scope = global | workspace）
 *   POST /remove  删除一个变量
 * 写入后需要重载插件（POST /agent-plugins/reload），新的子进程才会拿到新环境。
 *
 * 值默认打码，点「显示」才展开；编辑是行内输入框，回车保存、Esc 取消。
 */
(function () {
  "use strict";

  var root = document.querySelector("[data-em-page]");
  if (!root) return;

  var rowsNode = root.querySelector("[data-em-rows]");
  var statusNode = root.querySelector("[data-em-status]");
  var form = root.querySelector("[data-em-add]");
  var scopeInput = root.querySelector("[data-em-scope]");
  var keyInput = root.querySelector("[data-em-key]");
  var valueInput = root.querySelector("[data-em-value]");
  var refreshBtn = root.querySelector("[data-em-refresh]");
  var reloadBtn = root.querySelector("[data-em-reload]");
  var binsPathsNode = root.querySelector("[data-em-bins-paths]");
  var binsListNode = root.querySelector("[data-em-bins-list]");

  var state = null;
  var revealed = Object.create(null);
  var busy = false;

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
    return window.adaAPI("env-manager", path, options || {});
  }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function scopeLabel(scope) {
    return scope === "global" ? "全局" : "工作区";
  }

  // 下拉是宿主 <ada-select>（触发器 + listbox + 隐藏 input）；值统一从隐藏 input 读，
  // 组件尚未升级时也能拿到初始值。
  function selectValue(node) {
    if (!node || !node.querySelector) return "";
    var input = node.querySelector("[data-select-input]");
    return input ? String(input.value || "") : "";
  }

  function rowKey(scope, key) {
    return scope + ":" + key;
  }

  function formatBytes(bytes) {
    var n = Number(bytes) || 0;
    if (n <= 0) return "—";
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
    return (n / 1024 / 1024).toFixed(1) + " MB";
  }

  function formatTime(value) {
    if (!value) return "—";
    var d = new Date(value);
    if (isNaN(d.getTime())) return String(value);
    return d.toLocaleString();
  }

  // 服务程序：展示宿主前置到 PATH 的两个目录与扫描到的可执行文件。
  function renderBins(bins) {
    var paths = (bins && bins.paths) || [];
    var programs = (bins && bins.programs) || [];
    if (binsPathsNode) {
      binsPathsNode.textContent = "";
      paths.forEach(function (item) {
        var row = el("div", "em-bin-path");
        row.appendChild(el("span", "em-chip em-chip--" + item.scope, scopeLabel(item.scope)));
        row.appendChild(el("code", "em-bin-path__code", item.path || ""));
        var state = el("span", "em-bin-path__state", item.exists ? "已创建" : "未创建");
        if (!item.exists) state.className += " is-missing";
        row.appendChild(state);
        binsPathsNode.appendChild(row);
      });
    }
    if (!binsListNode) return;
    binsListNode.textContent = "";
    if (!programs.length) {
      var tr = document.createElement("tr");
      var td = el("td", "em-empty", "还没有服务程序。把可执行文件放进上面的目录即可。");
      td.setAttribute("colspan", "4");
      tr.appendChild(td);
      binsListNode.appendChild(tr);
      return;
    }
    programs.forEach(function (item) {
      var tr = document.createElement("tr");
      var scopeTd = el("td", "em-col-scope");
      scopeTd.appendChild(el("span", "em-chip em-chip--" + item.scope, scopeLabel(item.scope)));
      tr.appendChild(scopeTd);
      var nameTd = el("td");
      nameTd.appendChild(el("code", "em-key", item.name || ""));
      tr.appendChild(nameTd);
      tr.appendChild(el("td", "em-value", formatBytes(item.size)));
      tr.appendChild(el("td", "em-value", formatTime(item.modified)));
      binsListNode.appendChild(tr);
    });
  }

  function maskValue(value) {
    var text = String(value === undefined || value === null ? "" : value);
    if (text === "") return "（空）";
    return "•".repeat(Math.min(10, Math.max(4, text.length)));
  }

  function varsOf(layer) {
    return layer && layer.vars ? layer.vars : {};
  }

  function render() {
    if (!rowsNode) return;
    rowsNode.textContent = "";
    if (!state) return;
    var workspace = varsOf(state.workspace);
    var global = varsOf(state.global);
    var total = 0;

    Object.keys(workspace).sort().forEach(function (key) {
      total++;
      rowsNode.appendChild(buildRow("workspace", key, workspace[key], Object.prototype.hasOwnProperty.call(global, key)));
    });
    Object.keys(global).sort().forEach(function (key) {
      total++;
      rowsNode.appendChild(buildRow("global", key, global[key], false));
    });

    if (total === 0) {
      var tr = document.createElement("tr");
      var td = el("td", "em-empty", "还没有环境变量。用上方表单添加，或执行 adag env set KEY=VALUE。");
      td.setAttribute("colspan", "4");
      tr.appendChild(td);
      rowsNode.appendChild(tr);
    }
    if (state.warnings && state.warnings.length) {
      status(state.warnings.join("；"), "warn");
    }
    renderBins(state.bins);
  }

  function buildRow(scope, key, value, overridesGlobal) {
    var tr = document.createElement("tr");

    var scopeTd = el("td", "em-col-scope");
    scopeTd.appendChild(el("span", "em-chip em-chip--" + scope, scopeLabel(scope)));
    tr.appendChild(scopeTd);

    var keyTd = el("td", "em-col-key");
    keyTd.appendChild(el("code", "em-key", key));
    if (overridesGlobal) keyTd.appendChild(el("span", "em-chip em-chip--warn", "覆盖全局"));
    tr.appendChild(keyTd);

    var valueTd = el("td", "em-value");
    var open = !!revealed[rowKey(scope, key)];
    var text = String(value === undefined || value === null ? "" : value);
    valueTd.textContent = open ? (text === "" ? "（空）" : text) : maskValue(text);
    tr.appendChild(valueTd);

    var actionsTd = el("td", "em-col-actions");
    var showBtn = el("button", "ada-btn ada-btn--ghost ada-btn--sm", open ? "隐藏" : "显示");
    showBtn.type = "button";
    showBtn.addEventListener("click", function () {
      var rk = rowKey(scope, key);
      if (revealed[rk]) delete revealed[rk];
      else revealed[rk] = true;
      render();
    });
    var editBtn = el("button", "ada-btn ada-btn--ghost ada-btn--sm", "编辑");
    editBtn.type = "button";
    editBtn.addEventListener("click", function () {
      beginEdit(tr, scope, key, text);
    });
    var delBtn = el("button", "ada-btn ada-btn--danger ada-btn--sm", "删除");
    delBtn.type = "button";
    delBtn.addEventListener("click", function () {
      confirmAction("删除" + scopeLabel(scope) + "变量 " + key + "？", function () {
        api("/remove", { method: "POST", body: { scope: scope, key: key } })
          .then(function (result) {
            delete revealed[rowKey(scope, key)];
            return loadState().then(function () {
              status(result && result.removed ? "已删除 " + key : key + " 不存在", "success");
            });
          })
          .catch(function (err) {
            status("删除失败：" + errorText(err), "error");
          });
      });
    });
    actionsTd.appendChild(showBtn);
    actionsTd.appendChild(editBtn);
    actionsTd.appendChild(delBtn);
    tr.appendChild(actionsTd);
    return tr;
  }

  function beginEdit(tr, scope, key, value) {
    var cells = tr.children;
    var valueTd = cells[2];
    var actionsTd = cells[3];
    valueTd.textContent = "";
    var input = el("input", "em-input em-input--inline");
    input.value = String(value === undefined || value === null ? "" : value);
    valueTd.appendChild(input);
    actionsTd.textContent = "";

    function save() {
      if (busy) return;
      busy = true;
      api("/set", { method: "POST", body: { scope: scope, key: key, value: input.value } })
        .then(function () {
          return loadState().then(function () {
            status("已保存 " + key + "（" + scopeLabel(scope) + "），重载插件后生效", "success");
          });
        })
        .catch(function (err) {
          status("保存失败：" + errorText(err), "error");
        })
        .then(function () {
          busy = false;
        });
    }

    var saveBtn = el("button", "ada-btn ada-btn--primary ada-btn--sm", "保存");
    saveBtn.type = "button";
    saveBtn.addEventListener("click", save);
    var cancelBtn = el("button", "ada-btn ada-btn--secondary ada-btn--sm", "取消");
    cancelBtn.type = "button";
    cancelBtn.addEventListener("click", function () {
      render();
    });
    actionsTd.appendChild(saveBtn);
    actionsTd.appendChild(cancelBtn);
    input.addEventListener("keydown", function (event) {
      if (event.key === "Enter") save();
      if (event.key === "Escape") render();
    });
    input.focus();
  }

  // 使用基座的共享确认弹窗（ui::ConfirmDialog）；不要退回 window.confirm。
  function confirmAction(message, onConfirm) {
    if (typeof window.adaConfirm !== "function") {
      status("页面缺少确认弹窗组件，操作已取消", "error");
      return;
    }
    window.adaConfirm(message, onConfirm);
  }

  function loadState() {
    return api("/state").then(function (next) {
      state = next;
      render();
    });
  }

  function reloadPlugins() {
    var meta = document.querySelector('meta[name="csrf-token"]');
    var headers = { Accept: "text/html" };
    if (meta && meta.getAttribute("content")) headers["X-CSRF-Token"] = meta.getAttribute("content");
    return fetch("/agent-plugins/reload", {
      method: "POST",
      credentials: "same-origin",
      headers: headers,
    }).then(function (response) {
      if (!response.ok) throw new Error("插件重载失败（HTTP " + response.status + "）");
      return true;
    });
  }

  if (form) {
    form.addEventListener("submit", function (event) {
      event.preventDefault();
      if (busy) return;
      var scope = selectValue(scopeInput) || "workspace";
      var key = keyInput ? String(keyInput.value || "").trim() : "";
      var value = valueInput ? String(valueInput.value || "") : "";
      if (!key) {
        status("变量名不能为空", "error");
        if (keyInput) keyInput.focus();
        return;
      }
      busy = true;
      api("/set", { method: "POST", body: { scope: scope, key: key, value: value } })
        .then(function () {
          return loadState().then(function () {
            if (keyInput) keyInput.value = "";
            if (valueInput) valueInput.value = "";
            status("已保存 " + key + "（" + scopeLabel(scope) + "），重载插件后生效", "success");
          });
        })
        .catch(function (err) {
          status("保存失败：" + errorText(err), "error");
        })
        .then(function () {
          busy = false;
        });
    });
  }

  if (refreshBtn) {
    refreshBtn.addEventListener("click", function () {
      status("正在刷新…");
      loadState()
        .then(function () {
          status("已刷新", "success");
        })
        .catch(function (err) {
          status("读取失败：" + errorText(err), "error");
        });
    });
  }

  if (reloadBtn) {
    reloadBtn.addEventListener("click", function () {
      if (busy) return;
      busy = true;
      reloadBtn.disabled = true;
      status("正在重载插件…");
      reloadPlugins()
        .then(function () {
          status("插件已重载，正在刷新页面…", "success");
          window.setTimeout(function () {
            window.location.reload();
          }, 400);
        })
        .catch(function (err) {
          busy = false;
          reloadBtn.disabled = false;
          status("重载失败：" + errorText(err), "error");
        });
    });
  }

  status("正在读取环境变量…");
  loadState()
    .then(function () {
      // 初始读取完成后清掉占位文案；有警告时保留 render 写入的 warning。
      if (!(state && state.warnings && state.warnings.length)) status("");
    })
    .catch(function (err) {
      status("读取失败：" + errorText(err), "error");
    });
})();



