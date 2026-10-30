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
  if (!root) {
    return;
  }

  var threadId = root.getAttribute("data-thread-id") || "";
  if (!threadId) {
    return;
  }

  // Todo 卡片是用户可展开的 <details>：整页 reload（轮次收尾/手动刷新）和
  // 插槽局部刷新都会把它换回服务端的折叠态。展开状态跟运行状态无关，必须
  // 在下面「非运行中直接 return」之前先还原。
  var todoState = setupTodoState(threadId);

  if (typeof window.EventSource !== "function") {
    return;
  }

  var runStatus = (root.getAttribute("data-run-status") || "").toLowerCase();
  if (!ACTIVE[runStatus]) {
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
    if (!scroller) return;

    // 只有用户主动滚动（滚轮/触摸/拖滚动条）才停止跟随。思考块收起、行重排
    // 也会触发 scroll，但那是布局变化：把它当成用户意图的话，一次收缩就会
    // 把 following 关掉，后面再也不自动置底，只能手点「回到最新」。
    var userScrolledAt = 0;
    function noteUserScroll() {
      userScrolledAt = Date.now();
    }
    scroller.addEventListener("wheel", noteUserScroll, { passive: true });
    scroller.addEventListener("touchmove", noteUserScroll, { passive: true });
    scroller.addEventListener("pointerdown", noteUserScroll, { passive: true });
    scroller.addEventListener("mousedown", noteUserScroll, { passive: true });

    scroller.addEventListener(
      "scroll",
      function () {
        if (nearBottom()) {
          following = true;
          return;
        }
        // 布局引起的 scroll 不改变跟随状态；用户刚操作过滚动装置才停跟随。
        if (Date.now() - userScrolledAt < 800) following = false;
      },
      { passive: true }
    );
  })();

  // 工具卡与消息行只有服务端渲染版本。运行中收到 item.updated 时只换对话行，
  // 不整页重载——整页重载会把正在追加的实时块和滚动位置一起冲掉，那正是
  // 「输出时界面持续刷新」的来源。800ms 合并窗口：连续工具调用一次性落行，
  // 避免每来一个工具就插一行造成刷屏；消息收尾仍立即刷新。
  var refreshTimer = null;
  var lastRefreshAt = 0;
  var REFRESH_INTERVAL = 800;
  var refreshing = false;
  var refreshAgain = false;
  // 已经落库的消息 id：它们的实时块要在服务端行换上之后扔掉，
  // 否则同一段文字会在原位和列表末尾各出现一次（就是「顺序乱」的来源）。
  var persisted = Object.create(null);

  function scheduleTranscriptRefresh() {
    if (refreshTimer) return;
    var wait = Math.max(0, REFRESH_INTERVAL - (Date.now() - lastRefreshAt));
    refreshTimer = window.setTimeout(function () {
      refreshTimer = null;
      lastRefreshAt = Date.now();
      refreshTranscript();
    }, wait);
  }

  // 消息落库：先立刻换行，成功后再扔掉对应的实时块（换失败就留着，不丢内容）。
  function flushPersisted() {
    if (refreshTimer) {
      window.clearTimeout(refreshTimer);
      refreshTimer = null;
    }
    lastRefreshAt = Date.now();
    refreshTranscript();
  }

  // Todo 卡片的展开状态按线程记忆：服务端 slot 永远渲染折叠态，整页 reload
  // 与插槽局部替换都会把它复位。这里在首屏与每次局部刷新后还原，并把用户
  // 的展开/收起写回 localStorage。
  function setupTodoState(threadID) {
    var key = "a_da_todo_open:" + threadID;
    var open = false;
    try {
      open = window.localStorage.getItem(key) === "1";
    } catch (e) {
      open = false;
    }

    function apply(scope) {
      var card = scope && scope.querySelector ? scope.querySelector("#todo-card") : null;
      if (!card) return null;
      card.open = open;
      card.setAttribute("data-state", open ? "open" : "collapsed");
      return card;
    }

    // 用 JS 属性而不是 data-* 记绑定标记：插槽刷新比较 outerHTML 时不会因为
    // 绑定标记多出一个差异，导致没必要的整卡替换。
    function bind(card) {
      if (!card || card._todoBound) return;
      card._todoBound = true;
      card.addEventListener("toggle", function () {
        open = card.open;
        card.setAttribute("data-state", open ? "open" : "collapsed");
        try {
          window.localStorage.setItem(key, open ? "1" : "0");
        } catch (e) {
          /* 持久化失败只在本次页面内生效，不影响使用 */
        }
      });
    }

    function sync(scope) {
      bind(apply(scope));
    }

    sync(document);
    return {
      // 局部替换前先读一次实时状态，避免用户刚展开、toggle 还没派发时被旧值覆盖。
      read: function (card) {
        if (card) open = card.open;
      },
      apply: sync
    };
  }

  // 插件插槽（chat.corner 的 Todo 卡片等）由服务端渲染，原先只有整页 reload
  // 才会更新；运行中每次工具落库都拉一次单槽 HTML，卡片不必等收尾才出现。
  // 300ms 合并窗口：工具批次通常连续到达，合并后只发一次请求。
  var slotTimer = null;
  var slotLastAt = 0;
  var SLOT_REFRESH_INTERVAL = 300;
  var slotRefreshing = false;
  var slotAgain = false;

  function scheduleSlotRefresh() {
    if (slotTimer) return;
    var wait = Math.max(0, SLOT_REFRESH_INTERVAL - (Date.now() - slotLastAt));
    slotTimer = window.setTimeout(function () {
      slotTimer = null;
      slotLastAt = Date.now();
      refreshCornerSlot();
    }, wait);
  }

  function refreshCornerSlot() {
    var host = document.getElementById("chat-corner");
    if (!host || !window.fetch) return;
    if (slotRefreshing) {
      slotAgain = true;
      return;
    }
    slotRefreshing = true;
    window
      .fetch("/partials/slot/chat.corner?threadId=" + encodeURIComponent(threadId), {
        credentials: "same-origin",
        headers: { Accept: "text/html" }
      })
      .then(function (res) {
        return res.ok ? res.text() : "";
      })
      .then(function (html) {
        if (!html) return;
        var holder = document.createElement("div");
        holder.innerHTML = html;
        var next = holder.firstElementChild;
        var current = host.querySelector(".ada-slot");
        if (!next) return;
        // 内容没变就保留原节点：<details> 的展开状态、悬浮态都不会被重置。
        // sameRow 会忽略 open/data-state，用户展开卡片本身不算内容变化。
        if (current && sameRow(current, next)) return;
        if (current) {
          todoState.read(current.querySelector("#todo-card"));
          host.replaceChild(next, current);
        } else {
          host.appendChild(next);
        }
        todoState.apply(next);
      })
      .catch(function () {
        /* 取不到就等下一次工具事件，不打断流 */
      })
      .then(function () {
        slotRefreshing = false;
        if (slotAgain) {
          slotAgain = false;
          scheduleSlotRefresh();
        }
      });
  }

  function dropLiveBlock(messageId) {
    var entry = blocks[messageId];
    if (!entry) return;
    entry.dead = true;
    if (entry.renderTimer) {
      window.clearTimeout(entry.renderTimer);
      entry.renderTimer = null;
    }
    if (entry.retryTimer) {
      window.clearTimeout(entry.retryTimer);
      entry.retryTimer = null;
    }
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

  function transcriptIsland() {
    var island = document.getElementById("transcript-island");
    return island && island._adaTranscriptApp ? island._adaTranscriptApp : null;
  }

  // 有 AdaUI 岛时只取 JSON 行，按 key 做 VNode patch；没有岛（空会话/老浏览器）
  // 仍走原来的整页 HTML 对比路径。
  function refreshTranscript() {
    var app = transcriptIsland();
    if (app) refreshTranscriptJSON(app);
    else refreshTranscriptHTML();
  }

  function refreshTranscriptJSON(app) {
    if (refreshing) {
      refreshAgain = true;
      return;
    }
    if (!window.fetch) return;
    refreshing = true;
    var stick = following;
    window
      .fetch("/partials/transcript/" + encodeURIComponent(threadId), {
        credentials: "same-origin",
        headers: { Accept: "application/json" }
      })
      .then(function (res) {
        return res.ok ? res.json() : null;
      })
      .then(function (data) {
        if (!data) return;
        app.setRows(data.rows || [], data.runStatus);
        // 岛的行是 VNode patch：setRows 只把更新排进微任务，DOM 还是旧的。
        // 必须等这一拍落完再删实时块/量高度，否则滚到底用的是旧高度，
        // 新行（思考收起后的短行）随后插进来，视口就停在底部上方。
        var afterRender = function () {
          dropPersistedBlocks();
          if (stick) {
            var scroller = document.getElementById("chat-scroll");
            if (scroller) scroller.scrollTop = scroller.scrollHeight;
          }
        };
        if (window.AdaUI && typeof window.AdaUI.nextTick === "function") {
          return window.AdaUI.nextTick().then(afterRender);
        }
        afterRender();
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

  function refreshTranscriptHTML() {
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
    body.className = "ada-msg__body markdown-body ada-markdown";

    // 思考与落库后的 "thinking" 模板保持同一套结构：详情卡 + .ada-thinking。
    // 流式期间固定 open，内容随增量增长；落库后由服务端行接管（默认折叠）。
    var thinking = document.createElement("details");
    thinking.className = "ada-disclosure";
    thinking.setAttribute("data-kind", "thinking");
    thinking.setAttribute("data-state", "open");
    thinking.open = true;
    thinking.hidden = true;

    var thinkingHead = el("summary", "ada-disclosure__summary");
    var thinkingIcon = el("span", "ada-disclosure__icon");
    thinkingIcon.appendChild(icon("bulb", 14));
    var thinkingCaret = el("span", "ada-disclosure__caret");
    thinkingCaret.appendChild(icon("chevron", 12));
    thinkingHead.appendChild(thinkingIcon);
    thinkingHead.appendChild(thinkingCaret);
    thinkingHead.appendChild(el("span", "ada-disclosure__label", "思考"));

    var thinkingText = el("div", "ada-thinking");
    var thinkingRail = el("div", "ada-disclosure__rail");
    thinkingRail.appendChild(thinkingText);
    thinking.appendChild(thinkingHead);
    thinking.appendChild(thinkingRail);
    thinking.addEventListener("toggle", function () {
      thinking.setAttribute("data-state", thinking.open ? "open" : "closed");
    });

    var text = document.createElement("div");
    text.className = "part-text live-text";
    var markdownApp = null;
    if (window.AdaUI && window.AdaRender) {
      var host = document.createElement("div");
      host.className = "live-text-host";
      text.appendChild(host);
      var markdown = window.AdaUI.createApp({
        data: function () { return { nodes: [], text: "" }; },
        template: '<ui-markdown class="live-text" :nodes="nodes" :text="text"></ui-markdown>'
      });
      markdown.mount(host);
      markdownApp = markdown._instance ? markdown._instance.proxy : null;
    }

    body.appendChild(thinking);
    body.appendChild(text);
    article.appendChild(avatar);
    article.appendChild(body);
    stream.appendChild(article);
    stream.hidden = false;

    blocks[key] = {
      article: article,
      thinking: thinking,
      thinkingText: thinkingText,
      text: text,
      markdownApp: markdownApp,
      raw: "",
      renderedText: "",
      rendered: false,
      renderTimer: null,
      retryTimer: null,
      renderPending: false,
      dead: false
    };
    return blocks[key];
  }

  // ---- 实时 Markdown ----
  // 流式增量是纯文本；按节流把累积文本交给服务端渲染（同一个 goldmark +
  // bluemonday 管线），保证「正在生成」与落库后的排版一致，也不把未清洗的
  // HTML 交给前端。渲染失败时保留已有的纯文本/上一次结果，稍后重试。
  var MARKDOWN_RENDER_DELAY = 120;
  var MARKDOWN_RETRY_DELAY = 600;
  var csrfMeta = document.querySelector('meta[name="csrf-token"]');
  var csrfToken = csrfMeta ? csrfMeta.getAttribute("content") || "" : "";

  function scheduleMarkdown(entry) {
    // 没有 CSRF token 时退回纯文本：POST 会被安全中间件拒绝，重试没有意义。
    if (!window.fetch || !csrfToken || entry.dead) return;
    if (entry.renderTimer || entry.retryTimer) return;
    entry.renderTimer = window.setTimeout(function () {
      entry.renderTimer = null;
      renderMarkdownPart(entry);
    }, MARKDOWN_RENDER_DELAY);
  }

  function renderMarkdownPart(entry) {
    if (entry.dead || entry.renderPending) return;
    var source = entry.raw;
    if (!source || source === entry.renderedText) return;
    entry.renderPending = true;
    var asNodes = !!entry.markdownApp;
    window
      .fetch(asNodes ? "/markdown.ast" : "/markdown", {
        method: "POST",
        credentials: "same-origin",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": csrfToken
        },
        body: JSON.stringify({ text: source })
      })
      .then(function (res) {
        if (!res.ok) return null;
        return asNodes ? res.json() : res.text();
      })
      .then(function (data) {
        entry.renderPending = false;
        if (entry.dead) return;
        if (data === null) {
          retryMarkdown(entry);
          return;
        }
        if (asNodes) {
          entry.markdownApp.nodes = Array.isArray(data.nodes) ? data.nodes : [];
        } else {
          entry.text.innerHTML = data;
        }
        entry.renderedText = source;
        entry.rendered = true;
        followOutput(entry.article);
        // 请求期间又来了新内容：立刻排下一次，不要等下一个 delta。
        if (entry.raw !== source) scheduleMarkdown(entry);
      })
      .catch(function () {
        entry.renderPending = false;
        if (entry.dead) return;
        retryMarkdown(entry);
      });
  }

  function retryMarkdown(entry) {
    if (entry.dead || entry.retryTimer) return;
    entry.retryTimer = window.setTimeout(function () {
      entry.retryTimer = null;
      scheduleMarkdown(entry);
    }, MARKDOWN_RETRY_DELAY);
  }

  function handleDelta(raw) {
    var data;
    try {
      data = JSON.parse(raw);
    } catch (e) {
      return;
    }
    if (!data) return;

    // 已经落库的消息由刷新对话流接管；迟到的增量不能再造一个实时块。
    if (data.messageId && persisted[data.messageId]) return;

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
      entry.raw += chunk;
      // 有 Markdown 组件时先显示纯文本占位；AST 到达后由组件 patch。
      if (entry.markdownApp) {
        entry.markdownApp.text = entry.raw;
      } else if (!entry.rendered) {
        entry.text.textContent = entry.raw;
      }
      scheduleMarkdown(entry);
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
    // finishing 紧跟着 terminal：为了这一帧先插一条状态条再整页 reload，
    // 只会闪一下。收尾直接等 reload 完成，running 才需要持续提示。
    if (status === "running") {
      showStatus("正在生成…", "info");
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
    scheduleSlotRefresh();
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
