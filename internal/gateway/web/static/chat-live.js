/*
 * 会话页的实时增强（从 ada-go M2 的 app.js 迁移，保持无框架）。
 *
 * 脚本缺席时页面完全退回 meta refresh 轮询 + 服务端表单；脚本可用时消费
 * /events/{threadId} 的 SSE 增量：文本/思考直接追加，工具卡按 item.updated
 * 就地换行，终态后 reload 让服务端渲染最终历史与交互托盘。
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

  // 跟随输出：页面打开时已经置底（app.js 的 scrollChatToBottom），默认就在
  // 跟随状态；用户主动往上滚就停住，滚回底部再恢复。用绝对置底而不是
  // scrollIntoView，增量再大也不会被甩在视口外。
  var following = true;

  function followOutput(article) {
    if (!following) return;
    var scroller = document.getElementById("chat-scroll");
    if (scroller) {
      scroller.scrollTop = scroller.scrollHeight;
      return;
    }
    if (article) article.scrollIntoView({ block: "nearest" });
  }

  (function watchFollow() {
    var scroller = document.getElementById("chat-scroll");
    if (scroller) {
      scroller.addEventListener(
        "scroll",
        function () {
          following = nearBottom();
        },
        { passive: true }
      );
    }
  })();

  // 工具卡与消息行只有服务端渲染版本。运行中收到 item.updated 时只换对话行，
  // 不整页重载——整页重载会把正在追加的实时块和滚动位置一起冲掉，那正是
  // 「输出时界面持续刷新」的来源。500ms 节流：一轮最多十来次，肉眼是插入
  // 而不是闪烁。
  var refreshTimer = null;
  var refreshing = false;
  var refreshAgain = false;
  // 已经落库的消息 id：它们的实时块要在服务端行换上之后扔掉，
  // 否则同一段文字会在原位和列表末尾各出现一次（就是「顺序乱」的来源）。
  var persisted = Object.create(null);

  function scheduleTranscriptRefresh() {
    if (refreshTimer) return;
    refreshTimer = window.setTimeout(function () {
      refreshTimer = null;
      refreshTranscript();
    }, 500);
  }

  // 消息落库：先立刻换行，成功后再扔掉对应的实时块（换失败就留着，不丢内容）。
  function flushPersisted() {
    if (refreshTimer) {
      window.clearTimeout(refreshTimer);
      refreshTimer = null;
    }
    refreshTranscript();
  }

  function dropLiveBlock(messageId) {
    var entry = blocks[messageId];
    if (!entry) return;
    if (entry.article.parentNode) entry.article.parentNode.removeChild(entry.article);
    delete blocks[messageId];
    if (stream && !stream.children.length) stream.hidden = true;
  }

  function dropPersistedBlocks() {
    Object.keys(persisted).forEach(function (id) {
      dropLiveBlock(id);
      delete persisted[id];
    });
  }

  function itemKey(node) {
    return (node && node.getAttribute && node.getAttribute("data-item-key")) || "";
  }

  // 行内容是否一致。比较前去掉两类「只在浏览器里才有的状态」：
  //   - open / data-state：用户展开过的折叠行，服务端那份永远是 closed；
  //   - data-ada-bound="1"：components.js 给已绑定的折叠行打的标记。
  // 不剔掉的话每一行都判成「变了」，整份对话流会被重画一次（就是那下闪烁）。
  function sameRow(existing, fresh) {
    if (existing.outerHTML === fresh.outerHTML) return true;
    var strip = function (html) {
      return html
        .replace(/ open=""/g, "")
        .replace(/ data-state="open"/g, ' data-state="closed"')
        .replace(/ data-ada-bound="1"/g, "");
    };
    return strip(existing.outerHTML) === strip(fresh.outerHTML);
  }

  // 只换真正变化的行：内容没变的行复用原节点（<details> 展开状态、已渲染的
  // markdown 都不重建），否则整份对话流重画一次就是肉眼可见的一下闪烁。
  function reconcileTranscript(current, fresh, live) {
    var pool = {};
    Array.prototype.slice.call(current.children).forEach(function (node) {
      if (node === live) return;
      var key = itemKey(node);
      if (key) pool[key] = node;
    });

    var frag = document.createDocumentFragment();
    Array.prototype.slice.call(fresh.children).forEach(function (node) {
      if (node.id === "live-stream") return;
      var key = itemKey(node);
      var existing = key ? pool[key] : null;
      if (existing && sameRow(existing, node)) {
        frag.appendChild(existing); // 移动复用，不重建
        return;
      }
      // 思考在流式期间实时块里是展开的；落库后按服务端状态收起（默认折叠）。
      frag.appendChild(document.importNode(node, true));
    });

    // 复用/新建的行都已经挪进 frag，剩下的就是过期行。
    Array.prototype.slice.call(current.children).forEach(function (node) {
      if (node !== live) current.removeChild(node);
    });
    current.insertBefore(frag, live || null);
  }

  function refreshTranscript() {
    if (refreshing) {
      refreshAgain = true;
      return;
    }
    var target = document.getElementById("transcript");
    if (!target || !window.fetch || !window.DOMParser) return;
    refreshing = true;
    var stick = following;
    window
      .fetch(window.location.href, {
        credentials: "same-origin",
        headers: { Accept: "text/html" }
      })
      .then(function (res) {
        return res.ok ? res.text() : "";
      })
      .then(function (html) {
        if (!html) return;
        var parsed = new DOMParser().parseFromString(html, "text/html");
        var fresh = parsed.getElementById("transcript");
        var current = document.getElementById("transcript");
        if (!fresh || !current) return;
        var live = document.getElementById("live-stream");
        reconcileTranscript(current, fresh, live);
        // 服务端行已经在列表里了，实时块里的同一段文字可以退场。
        dropPersistedBlocks();
        if (stick) {
          var scroller = document.getElementById("chat-scroll");
          if (scroller) scroller.scrollTop = scroller.scrollHeight;
        }
      })
      .catch(function () {
        /* 取不到就等下一次 item.updated，不打断流 */
      })
      .then(function () {
        refreshing = false;
        if (refreshAgain) {
          refreshAgain = false;
          scheduleTranscriptRefresh();
        }
      });
  }

  // 实时行尽量长得跟落库后的服务端行一样：头像用同一个 bot 图标、思考用同一套
  // 图标 + 标签，换行时才不会「换个样子」。
  function icon(name, size) {
    var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "ada-icon");
    svg.setAttribute("width", String(size));
    svg.setAttribute("height", String(size));
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
    avatar.appendChild(icon("bot", 14));

    var body = document.createElement("div");
    body.className = "ada-msg__body";

    var thinking = document.createElement("div");
    thinking.className = "live-thinking";
    thinking.hidden = true;
    var thinkingHead = el("div", "live-thinking__head");
    thinkingHead.appendChild(icon("bulb", 12));
    thinkingHead.appendChild(el("span", "", "思考"));
    var thinkingText = el("div", "live-thinking__text");
    thinking.appendChild(thinkingHead);
    thinking.appendChild(thinkingText);

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
      entry.thinkingText.textContent += thought;
      entry.thinking.hidden = false;
    } else {
      var chunk = data.text || "";
      if (!chunk) return;
      entry.text.textContent += chunk;
    }

    followOutput(entry.article);
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

  source.addEventListener("evt.item.updated", function (event) {
    var data = {};
    try {
      data = JSON.parse(event.data) || {};
    } catch (e) {
      data = {};
    }
    var key = String(data.key || "");
    var status = String(data.status || "");
    if (key.indexOf("msg:") === 0 && (status === "end" || status === "finished")) {
      persisted[key.slice(4)] = true;
      flushPersisted();
      return;
    }
    scheduleTranscriptRefresh();
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
