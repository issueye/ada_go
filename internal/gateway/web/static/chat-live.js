/*
 * 会话页的实时增强（从 ada-go M2 的 app.js 迁移，保持无框架）。
 *
 * 脚本缺席时页面完全退回 meta refresh 轮询 + 服务端表单；脚本可用时消费
 * /events/{threadId} 的 SSE 增量，终态后 reload 让服务端渲染最终历史与交互托盘。
 * 外壳交互（提示条、确认弹窗、侧栏）在 app.js；主题在 theme.js。
 */
(function () {
  "use strict";

  var ACTIVE = { running: true, waiting_input: true };
  var TERMINAL = {
    completed: true,
    cancelled: true,
    error: true,
    interrupted: true,
    budget_exceeded: true
  };
  var MAX_SILENT_RETRIES = 3;

  var root = document.querySelector("[data-thread-id]");
  if (!root || typeof window.EventSource !== "function") {
    return;
  }

  var threadId = root.getAttribute("data-thread-id") || "";
  var runStatus = (root.getAttribute("data-run-status") || "").toLowerCase();
  if (!threadId || !ACTIVE[runStatus]) {
    return;
  }

  // 脚本正常工作时移除无 JS 的 meta refresh，避免服务端轮询与 SSE 双重刷新。
  var refreshMeta = document.querySelector('meta[http-equiv="refresh"]');
  if (refreshMeta && refreshMeta.parentNode) {
    refreshMeta.parentNode.removeChild(refreshMeta);
  }

  var stream = document.getElementById("live-stream");
  var statusBox = document.getElementById("live-status");
  var blocks = Object.create(null);
  var failures = 0;
  var reloading = false;
  var source = null;

  function showStatus(text, level) {
    if (!statusBox) return;
    statusBox.textContent = text;
    statusBox.setAttribute("data-level", level || "info");
    statusBox.hidden = false;
  }

  function clearStatus() {
    if (!statusBox) return;
    statusBox.hidden = true;
    statusBox.textContent = "";
    statusBox.removeAttribute("data-level");
  }

  function reloadOnce() {
    if (reloading) return;
    reloading = true;
    try {
      if (source) source.close();
    } catch (e) {
      /* 忽略关闭失败 */
    }
    window.location.reload();
  }

  function nearBottom() {
    var scroller = document.getElementById("chat-scroll");
    if (scroller) {
      return scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 140;
    }
    var doc = document.documentElement;
    return window.innerHeight + window.pageYOffset >= doc.scrollHeight - 140;
  }

  function ensureBlock(messageId) {
    var key = messageId || "live";
    if (blocks[key]) return blocks[key];
    if (!stream) return null;

    var article = document.createElement("article");
    article.className = "ada-msg ada-msg--assistant live-msg";
    article.setAttribute("data-message-id", key);

    var avatar = document.createElement("span");
    avatar.className = "ada-avatar";
    avatar.setAttribute("aria-hidden", "true");
    avatar.textContent = "A";

    var body = document.createElement("div");
    body.className = "ada-msg__body";

    var thinking = document.createElement("div");
    thinking.className = "live-thinking";
    thinking.hidden = true;

    var text = document.createElement("div");
    text.className = "part-text live-text";

    body.appendChild(thinking);
    body.appendChild(text);
    article.appendChild(avatar);
    article.appendChild(body);
    stream.appendChild(article);
    stream.hidden = false;

    blocks[key] = { article: article, thinking: thinking, text: text };
    return blocks[key];
  }

  function handleDelta(raw) {
    var data;
    try {
      data = JSON.parse(raw);
    } catch (e) {
      return;
    }
    if (!data) return;

    var entry = ensureBlock(data.messageId);
    if (!entry) return;

    var kind = data.partKind || (data.thinking ? "thinking" : "text");
    if (kind === "thinking") {
      var thought = data.thinking || "";
      if (!thought) return;
      if (!entry.thinking.textContent) entry.thinking.textContent = "思考：";
      entry.thinking.textContent += thought;
      entry.thinking.hidden = false;
    } else {
      var chunk = data.text || "";
      if (!chunk) return;
      entry.text.textContent += chunk;
    }

    if (nearBottom()) {
      entry.article.scrollIntoView({ block: "nearest" });
    }
  }

  function handleRunStatus(raw) {
    var data;
    try {
      data = JSON.parse(raw);
    } catch (e) {
      return;
    }
    var status = ((data && data.status) || "").toLowerCase();
    var terminal = (
      (data && (data.terminal || data.terminalResult)) ||
      ""
    ).toLowerCase();

    if (terminal || status === "terminal" || TERMINAL[status]) {
      reloadOnce();
      return;
    }
    if (status === "waiting_input") {
      reloadOnce();
      return;
    }
    if (status === "running" || status === "finishing") {
      showStatus(status === "finishing" ? "正在收尾…" : "正在生成…", "info");
    }
  }

  function handleError(raw) {
    var data = {};
    try {
      data = JSON.parse(raw) || {};
    } catch (e) {
      data = {};
    }
    var message = data.message || data.detail || "出现可恢复错误。";
    var diag = data.diag || data.name || "";
    showStatus(diag ? diag + "：" + message : message, "error");
  }

  try {
    source = new EventSource("/events/" + encodeURIComponent(threadId));
  } catch (e) {
    showStatus("无法建立实时连接，正在重试…", "warn");
    return;
  }

  source.addEventListener("open", function () {
    failures = 0;
    clearStatus();
  });

  source.addEventListener("evt.message.delta", function (event) {
    handleDelta(event.data);
  });

  source.addEventListener("evt.run.status", function (event) {
    handleRunStatus(event.data);
  });

  source.addEventListener("evt.interaction.requested", function () {
    reloadOnce();
  });

  source.addEventListener("evt.error", function (event) {
    handleError(event.data);
  });

  source.addEventListener("resync", function () {
    reloadOnce();
  });

  source.addEventListener("error", function () {
    failures += 1;
    if (failures <= MAX_SILENT_RETRIES) {
      showStatus("连接断开，正在重试…", "warn");
    } else {
      showStatus("连接断开，已多次重试失败。请刷新页面。", "error");
    }
  });
})();
