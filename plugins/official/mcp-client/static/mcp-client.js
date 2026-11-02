/*
 * MCP 服务端管理页（mcp-client）的浏览器端。
 *
 * 数据与操作全部走本插件 route（window.adaAPI 自动带 CSRF）：
 *   GET  /servers        server 状态 + 工具摘要
 *   GET  /config         工作区 .adag/mcp.json 原文
 *   POST /config         保存配置
 *   POST /servers/toggle 启用/停用单个 server
 * 保存后需要重载插件（POST /agent-plugins/reload），工具列表才会更新。
 *
 * 配置编辑器 = 透明 textarea + 背后的高亮层（pre/code）：
 *   - 文本/光标/选择都在 textarea 上；高亮层只负责显示彩色 JSON，严格同步滚动；
 *   - 高亮用 DOM 节点拼装（textContent），不拼 innerHTML，配置里的内容不会被当成 HTML；
 *   - 超过 MAX_HIGHLIGHT 的配置降级成纯文本，避免每次输入重建上千个节点。
 */
(function () {
  "use strict";

  var root = document.querySelector("[data-mcp-page]");
  if (!root) return;

  var serversNode = root.querySelector("[data-mcp-servers]");
  var statusNode = root.querySelector("[data-mcp-status]");
  var configNode = root.querySelector("[data-mcp-config]");
  var configStatusNode = root.querySelector("[data-mcp-config-status]");
  var pathNode = root.querySelector("[data-mcp-config-path]");
  var editor = root.querySelector("[data-mcp-editor]");
  var editorWrap = root.querySelector("[data-mcp-editor-wrap]");
  var highlightCode = root.querySelector("[data-mcp-highlight-code]");
  var refreshBtn = root.querySelector("[data-mcp-refresh]");
  var reloadBtn = root.querySelector("[data-mcp-reload]");
  var editBtn = root.querySelector("[data-mcp-edit]");
  var configReloadBtn = root.querySelector("[data-mcp-config-reload]");
  var formatBtn = root.querySelector("[data-mcp-format]");
  var saveBtn = root.querySelector("[data-mcp-save]");

  // 超过这个长度的配置不做语法高亮（大文件优先保证输入流畅）。
  var MAX_HIGHLIGHT = 64 * 1024;

  var configLoaded = false;
  var saving = false;
  var validationTimer = null;

  function status(message, tone) {
    if (!statusNode) return;
    statusNode.textContent = message || "";
    if (tone) statusNode.setAttribute("data-tone", tone);
    else statusNode.removeAttribute("data-tone");
  }

  function configStatus(message, tone) {
    if (!configStatusNode) return;
    configStatusNode.textContent = message || "";
    if (tone) configStatusNode.setAttribute("data-tone", tone);
    else configStatusNode.removeAttribute("data-tone");
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
    return window.adaAPI("mcp-client", path, options || {});
  }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function chip(text, tone) {
    return el("span", "mcp-chip" + (tone ? " mcp-chip--" + tone : ""), text);
  }

  function statusLabel(value) {
    if (value === "ready") return "已连接";
    if (value === "disabled") return "已停用";
    return "不可用";
  }

  // parseJSON 只做解析，不抛异常：编辑器要区分「语法错误」与「结构错误」。
  function parseJSON(text) {
    try {
      return { ok: true, value: JSON.parse(text) };
    } catch (err) {
      return { ok: false, message: describeJSONError(err, text) };
    }
  }

  // describeJSONError 把 JSON.parse 的位置换成中文的 行/列，便于用户定位。
  // V8 有时同时给 position 与 (line L column C)，只翻译一次，避免重复。
  function describeJSONError(err, text) {
    var message = err && err.message ? String(err.message) : String(err);
    var full = /at position\s+(\d+)\s*\(line\s+(\d+)\s+column\s+(\d+)\)/i.exec(message);
    if (full) {
      return message.replace(full[0], "（第 " + full[2] + " 行第 " + full[3] + " 列）");
    }
    var at = /position\s+(\d+)/i.exec(message);
    if (!at) return message;
    var pos = parseInt(at[1], 10);
    if (!isFinite(pos) || pos < 0) return message;
    var before = text.slice(0, pos);
    var line = before.split("\n").length;
    var col = pos - before.lastIndexOf("\n");
    return message.replace(/at position\s+\d+/i, "（第 " + line + " 行第 " + col + " 列）");
  }

  // prettyJSON 返回 {ok, text}；美化用 2 空格缩进并保留末尾换行。
  function prettyJSON(text) {
    var parsed = parseJSON(text);
    if (!parsed.ok) return parsed;
    return { ok: true, text: JSON.stringify(parsed.value, null, 2) + "\n" };
  }

  // ---- 语法高亮 ----------------------------------------------------------

  var TOKEN_RE = /"(?:\\.|[^"\\])*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|\b(?:true|false|null)\b/g;

  function refreshHighlight() {
    if (!editor || !highlightCode) return;
    var text = editor.value;
    if (text.length > MAX_HIGHLIGHT) {
      // 太大：退化成纯文本（仍然可见、可编辑）。
      highlightCode.textContent = text || " ";
      ready(true);
      return;
    }
    var fragment = document.createDocumentFragment();
    var index = 0;
    TOKEN_RE.lastIndex = 0;
    var match;
    while ((match = TOKEN_RE.exec(text)) !== null) {
      if (match.index > index) {
        fragment.appendChild(document.createTextNode(text.slice(index, match.index)));
      }
      var token = match[0];
      var span = document.createElement("span");
      if (token.charCodeAt(0) === 34) {
        // 字符串：后面紧跟冒号的当 key。
        span.className = /^\s*:/.test(text.slice(TOKEN_RE.lastIndex)) ? "mcp-tok-key" : "mcp-tok-str";
      } else if (token === "true" || token === "false" || token === "null") {
        span.className = "mcp-tok-lit";
      } else {
        span.className = "mcp-tok-num";
      }
      span.textContent = token;
      fragment.appendChild(span);
      index = TOKEN_RE.lastIndex;
    }
    if (index < text.length) {
      fragment.appendChild(document.createTextNode(text.slice(index)));
    }
    // pre 末尾补一个换行，和 textarea 的最后一个空行对齐。
    fragment.appendChild(document.createTextNode("\n"));
    highlightCode.textContent = "";
    highlightCode.appendChild(fragment);
    ready(true);
  }

  function ready(value) {
    if (!editorWrap) return;
    if (value) editorWrap.setAttribute("data-mcp-highlight-ready", "true");
    else editorWrap.removeAttribute("data-mcp-highlight-ready");
  }

  // autoGrow 让 textarea 随内容增高：没有内部滚动条，高亮层与正文的换行位置
  // 就永远一致（滚动条宽度会让两者换行点错开）。
  function autoGrow() {
    if (!editor) return;
    editor.style.height = "auto";
    editor.style.height = editor.scrollHeight + "px";
  }

  // ---- 校验 --------------------------------------------------------------

  function scheduleValidation() {
    if (validationTimer) clearTimeout(validationTimer);
    validationTimer = setTimeout(function () {
      validationTimer = null;
      updateValidation();
    }, 220);
  }

  function updateValidation() {
    if (!editor) return;
    var text = editor.value;
    if (!text.trim()) {
      configStatus("配置为空", "warn");
      return;
    }
    var parsed = parseJSON(text);
    if (parsed.ok) {
      configStatus("JSON 有效", "success");
    } else {
      configStatus("JSON 无效：" + parsed.message, "error");
    }
  }

  // ---- server 列表 -------------------------------------------------------

  function serverCard(server) {
    var card = el("section", "mcp-server");
    card.setAttribute("data-status", String(server.status || "degraded"));

    var head = el("div", "mcp-server__head");
    var main = el("div", "mcp-server__main");

    var title = el("div", "mcp-server__title");
    title.appendChild(el("span", "mcp-server__id", server.id || "?"));
    title.appendChild(chip(server.transport || "http"));
    if (server.source) title.appendChild(chip(server.source, "muted"));
    var badge = el("span", "mcp-badge", statusLabel(server.status));
    badge.setAttribute("data-status", String(server.status || "degraded"));
    title.appendChild(badge);
    main.appendChild(title);

    main.appendChild(el("code", "mcp-server__endpoint", server.url || ""));
    if (server.error) main.appendChild(el("p", "mcp-server__error", server.error));
    head.appendChild(main);

    var actions = el("div", "mcp-server__actions");
    var label = el("label", "mcp-toggle");
    var toggle = document.createElement("input");
    toggle.type = "checkbox";
    toggle.checked = server.enabled !== false;
    toggle.setAttribute("data-mcp-toggle", server.id || "");
    toggle.setAttribute("aria-label", "启用 " + (server.id || ""));
    label.appendChild(toggle);
    label.appendChild(document.createTextNode(" 启用"));
    actions.appendChild(label);
    head.appendChild(actions);
    card.appendChild(head);

    var items = server.items || [];
    if (items.length) {
      var details = document.createElement("details");
      details.className = "mcp-server__tools";
      var summary = document.createElement("summary");
      summary.textContent = "工具（" + items.length + "）";
      details.appendChild(summary);
      var list = el("ul", "mcp-tools");
      items.forEach(function (item) {
        var li = document.createElement("li");
        li.appendChild(el("code", "mcp-tool__name", item.name || ""));
        li.appendChild(chip(item.access || "write", item.access === "read" ? "ok" : "muted"));
        if (item.schemaSimplified) li.appendChild(chip("Schema 已简化", "warn"));
        if (item.mcpName && item.mcpName !== item.name) {
          li.appendChild(el("span", "mcp-tool__origin", "MCP: " + item.mcpName));
        }
        list.appendChild(li);
      });
      details.appendChild(list);
      card.appendChild(details);
    } else if (server.enabled === false) {
      card.appendChild(el("p", "mcp-server__note", "已停用：重载插件后不再连接这个 server。"));
    } else if (server.status === "ready") {
      card.appendChild(el("p", "mcp-server__note", "该 server 没有提供工具。"));
    }
    return card;
  }

  function renderServers(state) {
    if (!serversNode) return;
    serversNode.textContent = "";
    if (state && state.configPath && pathNode) pathNode.textContent = state.configPath;
    var servers = (state && state.servers) || [];
    if (!servers.length) {
      var empty = el("div", "mcp-empty");
      empty.appendChild(el("p", "mcp-empty__title", "还没有 MCP server"));
      empty.appendChild(
        el("p", "mcp-empty__hint", "点右上角「编辑配置」，按 .adag/mcp.json 的格式添加 server，保存后重载插件。")
      );
      serversNode.appendChild(empty);
      return;
    }
    servers.forEach(function (server) {
      serversNode.appendChild(serverCard(server));
    });
  }

  function loadServers() {
    return api("/servers")
      .then(function (state) {
        renderServers(state);
        var count = ((state && state.servers) || []).length;
        status(count ? "共 " + count + " 个 server" : "");
      })
      .catch(function (err) {
        status("读取 server 状态失败：" + errorText(err), "error");
      });
  }

  // ---- 配置 --------------------------------------------------------------

  function loadConfig() {
    return api("/config")
      .then(function (config) {
        configLoaded = true;
        if (pathNode && config && config.path) pathNode.textContent = config.path;
        var text = (config && config.text) || "";
        if (editor) {
          // 打开即美化：合法 JSON 统一按 2 空格显示（保存后才写回文件）。
          var pretty = text.trim() ? prettyJSON(text) : { ok: true, text: text };
          editor.value = pretty.ok ? pretty.text : text;
          refreshHighlight();
          autoGrow();
          updateValidation();
        }
      })
      .catch(function (err) {
        status("读取配置失败：" + errorText(err), "error");
      });
  }

  function reloadPlugins() {
    var headers = { Accept: "text/html" };
    var token = csrfToken();
    if (token) headers["X-CSRF-Token"] = token;
    return fetch("/agent-plugins/reload", {
      method: "POST",
      credentials: "same-origin",
      headers: headers,
    }).then(function (response) {
      if (!response.ok) throw new Error("插件重载失败（HTTP " + response.status + "）");
      return true;
    });
  }

  function csrfToken() {
    var meta = document.querySelector('meta[name="csrf-token"]');
    return meta && meta.getAttribute("content") ? meta.getAttribute("content") : "";
  }

  // ---- 事件 --------------------------------------------------------------

  if (refreshBtn) {
    refreshBtn.addEventListener("click", function () {
      status("正在刷新…");
      loadServers().then(function () {
        status("已刷新", "success");
      });
    });
  }

  if (reloadBtn) {
    reloadBtn.addEventListener("click", function () {
      if (saving) return;
      saving = true;
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
          saving = false;
          reloadBtn.disabled = false;
          status("重载失败：" + errorText(err), "error");
        });
    });
  }

  if (editBtn) {
    editBtn.addEventListener("click", function () {
      if (!configNode) return;
      var opening = configNode.classList.contains("is-hidden");
      configNode.classList.toggle("is-hidden", !opening);
      editBtn.textContent = opening ? "收起配置" : "编辑配置";
      if (opening && !configLoaded) loadConfig();
    });
  }

  if (configReloadBtn) {
    configReloadBtn.addEventListener("click", function () {
      status("正在读取配置…");
      loadConfig().then(function () {
        status("已读取配置", "success");
      });
    });
  }

  if (formatBtn) {
    formatBtn.addEventListener("click", function () {
      if (!editor) return;
      var pretty = prettyJSON(editor.value);
      if (!pretty.ok) {
        configStatus("格式化失败：" + pretty.message, "error");
        return;
      }
      if (pretty.text === editor.value) {
        configStatus("已经是格式化后的 JSON", "success");
        return;
      }
      editor.value = pretty.text;
      refreshHighlight();
      autoGrow();
      updateValidation();
      configStatus("已格式化（保存后写入文件）", "success");
    });
  }

  if (saveBtn) {
    saveBtn.addEventListener("click", function () {
      if (saving || !editor) return;
      saving = true;
      saveBtn.disabled = true;
      status("正在保存…");
      api("/config", { method: "POST", body: { text: editor.value } })
        .then(function (result) {
          var warnings = (result && result.warnings) || [];
          var message = "已保存（" + ((result && result.servers) || 0) + " 个 server），点「重载插件」后生效";
          if (warnings.length) message += "；警告：" + warnings.join("；");
          status(message, warnings.length ? "warn" : "success");
          updateValidation();
        })
        .catch(function (err) {
          status("保存失败：" + errorText(err), "error");
        })
        .then(function () {
          saving = false;
          saveBtn.disabled = false;
        });
    });
  }

  if (editor) {
    editor.addEventListener("input", function () {
      refreshHighlight();
      autoGrow();
      scheduleValidation();
    });
    // 窗口宽度变化会改变换行位置，高度要跟着重算。
    window.addEventListener("resize", autoGrow);
    // 初次渲染（配置为空时不显示高亮层，但 textarea 保持可输入）。
    refreshHighlight();
  }

  if (serversNode) {
    serversNode.addEventListener("change", function (event) {
      var input = event.target;
      if (!input || !input.getAttribute) return;
      var id = input.getAttribute("data-mcp-toggle");
      if (!id) return;
      var wanted = !!input.checked;
      input.disabled = true;
      api("/servers/toggle", { method: "POST", body: { id: id, enabled: wanted } })
        .then(function () {
          status("已保存：" + id + (wanted ? " 已启用" : " 已停用") + "，重载插件后生效", "success");
        })
        .catch(function (err) {
          input.checked = !wanted;
          status("启停失败：" + errorText(err), "error");
        })
        .then(function () {
          input.disabled = false;
        });
    });
  }

  status("正在读取 server 状态…");
  loadServers();
})();




