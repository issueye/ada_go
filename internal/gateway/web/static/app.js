/*
 * 应用外壳交互层（无构建步骤，模板里只挂 data-* 属性）
 *
 * 覆盖三件事：
 *  1. 提示条：可关闭，并在一段时间后自动淡出；
 *  2. 确认弹窗：`form[data-confirm]` / `a[data-confirm]` 统一走**组件库**的纯 DOM 弹窗
 *     （`ui::ConfirmDialog`；不用原生 `<dialog>`，更不用 `window.confirm`），
 *     不再散落内联 onsubmit 脚本；
 *  3. 移动端侧栏抽屉。
 *
 * 复制按钮（data-copy）与组件行为在 components.js，这里不重复绑定——
 * 两份 document 级委托会让每次点击写两次剪贴板。
 * 主题切换在 theme.js（必须阻塞加载以避免闪白）。
 */
(function () {
  'use strict';

  /* ── 1. 提示条 ───────────────────────────────────────────────────────── */
  function dismiss(target) {
    if (!target) return;
    // 160ms = --ada-duration-fast（app-local.css :root）；JS 里读令牌不值当，字面量跟令牌同步。
    // flash 现在是底部居中 toast（left:50% + translateX(-50%)），退出时也必须保留
    // -50% 水平偏移，否则淡出瞬间会跳到左边。
    target.style.transition = 'opacity 160ms cubic-bezier(0.4, 0, 0.2, 1), transform 160ms cubic-bezier(0.4, 0, 0.2, 1)';
    target.style.opacity = '0';
    target.style.transform = 'translate(-50%, 8px)';
    window.setTimeout(function () {
      target.remove();
      syncFlashClass();
    }, 180);
  }

  // flash 在场标记保留给需要避让的浮层；toast 在底部，不再遮右上角。
  function syncFlashClass() {
    document.body.classList.toggle('ada-flash-open', !!document.querySelector('.ada-flash'));
  }

  // 与各插件页 toast 同一节奏：普通 2.4s、错误 4.2s。
  function toastDelay(kind) {
    return kind === 'error' ? 4200 : 2400;
  }

  function autoDismissFlashes() {
    var flashes = document.querySelectorAll('[data-flash]');
    syncFlashClass();
    for (var i = 0; i < flashes.length; i += 1) {
      (function (flash) {
        var delay = toastDelay(flash.getAttribute('data-flash'));
        window.setTimeout(function () {
          if (flash.isConnected) dismiss(flash);
        }, delay);
      })(flashes[i]);
    }
  }

  /* 非阻塞提示：替代 window.alert。alert 会冻结整页脚本，且和页面内
     提示条是两种观感；这里复用服务端 flash 的 DOM 与样式。 */
  function notify(message, kind) {
    var host = document.querySelector('.ada-main') || document.body;
    var box = document.createElement('div');
    var tone = kind === 'error' ? 'alert-danger' : 'alert-info';
    box.className = 'ada-flash alert ' + tone;
    box.setAttribute('data-flash', kind === 'error' ? 'error' : 'info');
    box.setAttribute('role', kind === 'error' ? 'alert' : 'status');

    var text = document.createElement('p');
    text.className = 'alert-message';
    text.textContent = message;

    var close = document.createElement('button');
    close.type = 'button';
    close.className = 'ada-flash__close';
    close.setAttribute('data-dismiss', '');
    close.setAttribute('aria-label', '关闭提示');
    close.setAttribute('title', '关闭');
    close.textContent = '×';

    box.appendChild(text);
    box.appendChild(close);
    host.insertBefore(box, host.firstChild);
    syncFlashClass();
    window.setTimeout(function () {
      if (box.isConnected) dismiss(box);
    }, toastDelay(kind));
  }

  // 全局通知唯一入口（19-ued §19.5.3）：chat-live 等模块复用它出提示，
  // 不再各造一套 toast；AdaUI.toast 已随通知收敛移除。
  window.AdaApp = { notify: notify };

  /* ── 2. 共享确认弹窗 ─────────────────────────────────────────────────
     弹窗本体是组件库的 `ui::ConfirmDialog`（`data-modal="confirm"`）；开合、焦点圈、
     Esc、遮罩与滚动锁定由 `components.js` 的 `[data-modal]` 控制器统一负责。
     这里只做三件事：记下待确认动作、写入文案、确认后执行。
     🔴 **不要退回 window.confirm**：原生对话框不可样式化、会阻塞整页脚本，而且同一个
     "删除"在聊天页与二级页会变成两种观感（历史 bug）。组件缺失＝模板漏渲染，直接报错。 */
  var dialog = document.querySelector('[data-confirm-dialog]');
  var pendingConfirm = null;

  function openConfirm(message, onConfirm) {
    if (!dialog || !window.AdaComponents || typeof window.AdaComponents.openModal !== 'function') {
      console.error('[ssr-h] 页面缺少 [data-confirm-dialog] 组件，确认动作已取消');
      return;
    }
    var messageNode = dialog.querySelector('[data-confirm-message]');
    if (messageNode) messageNode.textContent = message;
    pendingConfirm = onConfirm;
    window.AdaComponents.openModal('confirm');
  }

  function closeConfirm() {
    pendingConfirm = null;
    if (window.AdaComponents) window.AdaComponents.closeModal('confirm');
  }

  // 公共确认 API：插件页/组件需要「先确认再执行」时用它（复用 ui::ConfirmDialog）。
  // 不要用 window.confirm：原生对话框不可样式化、会阻塞脚本，且与聊天页观感不一致。
  window.adaConfirm = openConfirm;

  function wireDialog() {
    if (!dialog) return;
    dialog.addEventListener('click', function (event) {
      var target = event.target;
      if (!target || !target.closest) return;
      // 取消/遮罩：关闭交给 data-modal-close，这里只清掉待确认动作。
      if (target.closest('[data-confirm-cancel]')) {
        pendingConfirm = null;
        return;
      }
      if (target.closest('[data-confirm-ok]')) {
        var action = pendingConfirm;
        closeConfirm();
        if (action) action();
      }
    });
    // Esc / 点遮罩关闭时控制器会派发关闭事件，用来清掉悬着的动作。
    document.addEventListener('ada:modal-close', function (event) {
      if (event.detail && event.detail.id === 'confirm') pendingConfirm = null;
    });
  }

  document.addEventListener(
    'submit',
    function (event) {
      var form = event.target;
      if (!form || !form.matches || !form.matches('form[data-confirm]')) return;
      if (form.dataset.confirmed === '1') {
        delete form.dataset.confirmed;
        return;
      }
      event.preventDefault();
      var message = form.getAttribute('data-confirm') || '确认执行这个操作？';
      openConfirm(message, function () {
        form.dataset.confirmed = '1';
        form.requestSubmit();
      });
    },
    true
  );

  document.addEventListener('click', function (event) {
    var target = event.target;
    if (!target || !target.closest) return;

    var dismissButton = target.closest('[data-dismiss]');
    if (dismissButton) {
      dismiss(dismissButton.closest('[data-flash]'));
      return;
    }

    var confirmedLink = target.closest('a[data-confirm]');
    if (confirmedLink) {
      event.preventDefault();
      openConfirm(confirmedLink.getAttribute('data-confirm'), function () {
        window.location.href = confirmedLink.href;
      });
      return;
    }

    // 外部链接（Markdown 正文等）交给系统浏览器/新标签：不能把应用页面导航走。
    var anchor = target.closest('a[href]');
    if (!anchor || anchor.hasAttribute('download')) return;
    if (event.defaultPrevented || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    var href = anchor.getAttribute('href') || '';
    if (!href || href.charAt(0) === '#' || href.charAt(0) === '/') return;
    var url;
    try {
      url = new URL(href, window.location.href);
    } catch (_err) {
      return;
    }
    if (url.protocol !== 'http:' && url.protocol !== 'https:' && url.protocol !== 'mailto:') return;
    if (url.origin === window.location.origin) return;
    event.preventDefault();
    window.open(url.href, '_blank', 'noopener,noreferrer');
  });

  /* 输入框旁的模型选择器：改选即切换（线程级；没有会话上下文时工作区级）。
     选择器在发送表单内部，不能嵌套第二个 form，所以用 fetch + CSRF。 */
  function switchModel(picker, providerId, modelId, level) {
    var thread = document.querySelector("[data-thread-id]");
    var csrf = document.querySelector('meta[name="csrf-token"]');
    var body = new URLSearchParams();
    body.set("providerId", providerId);
    body.set("modelId", modelId);
    if (level) body.set("thinkingLevel", level);
    if (thread && thread.getAttribute("data-thread-id")) {
      body.set("threadId", thread.getAttribute("data-thread-id"));
    }
    picker.setAttribute("aria-busy", "true");
    fetch("/model/select", {
      method: "POST",
      credentials: "same-origin",
      redirect: "manual",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        "X-CSRF-Token": csrf ? csrf.getAttribute("content") : ""
      },
      body: body.toString()
    }).then(function (res) {
      if (res.type === "opaqueredirect" || res.ok) {
        // 服务端已持久化到线程/工作区；不再整页 reload，避免刷新期间
        // 丢掉输入草稿，也避免用户误以为「刷新后模型又被重置」。
        var input = picker.querySelector("[data-select-input]");
        var value = providerId + "|" + modelId;
        if (input) input.value = value;
        var rows = picker.querySelectorAll("[data-value]");
        for (var i = 0; i < rows.length; i += 1) {
          rows[i].setAttribute("data-current-level", rows[i].getAttribute("data-value") === value ? (level || "") : "");
        }
        picker.removeAttribute("aria-busy");
        notify("模型已切换");
        return null;
      }
      return res.text().then(function (text) {
        throw new Error(text || ("HTTP " + res.status));
      });
    }).catch(function (err) {
      picker.removeAttribute("aria-busy");
      notify("切换模型失败：" + (err && err.message ? err.message : err), "error");
    });
  }

  var cascade = null;
  var cascadePicker = null;
  var cascadeRow = null;

  function ensureCascade() {
    if (cascade) return cascade;
    cascade = document.createElement("div");
    cascade.className = "ada-model-cascade";
    cascade.hidden = true;
    cascade.setAttribute("role", "menu");
    document.body.appendChild(cascade);
    cascade.addEventListener("click", function (event) {
      var button = event.target.closest && event.target.closest("[data-cascade-level]");
      if (!button || !cascadeRow || !cascadePicker) return;
      event.preventDefault();
      var value = cascadeRow.getAttribute("data-value") || "";
      var cut = value.indexOf("|");
      if (cut <= 0) return;
      var level = button.getAttribute("data-cascade-level") || "";
      var picker = cascadePicker;
      hideCascade(null);
      switchModel(picker, value.slice(0, cut), value.slice(cut + 1), level);
    });
    cascade.addEventListener("keydown", function (event) {
      var items = Array.prototype.slice.call(cascade.querySelectorAll("[data-cascade-level]"));
      if (!items.length) return;
      var index = items.indexOf(document.activeElement);
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        var step = event.key === "ArrowDown" ? 1 : -1;
        var next = index < 0 ? 0 : index + step;
        if (next < 0) next = items.length - 1;
        if (next >= items.length) next = 0;
        items[next].focus();
        return;
      }
      if (event.key === "ArrowLeft" || event.key === "Escape") {
        event.preventDefault();
        hideCascade(true);
      }
    });
    document.addEventListener(
      "click",
      function (event) {
        if (!cascade || cascade.hidden) return;
        if (event.target.closest && (event.target.closest(".ada-model-cascade") || event.target.closest("[data-model-picker]"))) {
          return;
        }
        hideCascade(null);
      },
      true
    );
    window.addEventListener("resize", function () { hideCascade(null); });
    window.addEventListener("scroll", function () { hideCascade(null); }, true);
    return cascade;
  }

  function hideCascade(focusRow) {
    var row = cascadeRow;
    if (cascade) cascade.hidden = true;
    cascadePicker = null;
    cascadeRow = null;
    if (focusRow && row) row.focus();
  }

  function showCascade(picker, row) {
    if (cascadeRow === row && cascade && !cascade.hidden) return;
    var levels = (row.getAttribute("data-levels") || "").split("|").filter(Boolean);
    if (!levels.length) {
      hideCascade(null);
      return;
    }
    ensureCascade();
    cascade.textContent = "";
    var title = document.createElement("p");
    title.className = "ada-model-cascade__title";
    title.textContent = "推理等级";
    cascade.appendChild(title);
    var current = row.getAttribute("data-current-level") || "";
    levels.forEach(function (level) {
      var button = document.createElement("button");
      button.type = "button";
      button.className = "ada-select__option ada-model-cascade__option" + (level === current ? " is-selected" : "");
      button.setAttribute("role", "menuitem");
      button.setAttribute("data-cascade-level", level);
      button.textContent = level;
      cascade.appendChild(button);
    });
    cascade.hidden = false;
    cascade.style.visibility = "hidden";
    var rect = row.getBoundingClientRect();
    var width = cascade.offsetWidth;
    var height = cascade.offsetHeight;
    var left = rect.right + 4;
    if (left + width > window.innerWidth - 8) left = rect.left - width - 4;
    if (left < 8) left = 8;
    var top = rect.top;
    if (top + height > window.innerHeight - 8) top = Math.max(8, window.innerHeight - height - 8);
    cascade.style.left = left + "px";
    cascade.style.top = top + "px";
    cascade.style.visibility = "";
    cascadePicker = picker;
    cascadeRow = row;
  }

  /* 模型菜单的级联：有档位的模型在右侧展开推理等级，不把档位平铺到工具行。 */
  function bindModelCascades() {
    var pickers = document.querySelectorAll("[data-model-picker]");
    for (var i = 0; i < pickers.length; i += 1) {
      (function (picker) {
        if (picker.getAttribute("data-cascade-bound") === "1") return;
        picker.setAttribute("data-cascade-bound", "1");

        function rowOf(target) {
          return target && target.closest ? target.closest(".ada-model-picker__option") : null;
        }
        function hasLevels(row) {
          return !!(row && row.getAttribute("data-levels"));
        }

        // 点击有档位的模型：不直接选中，展开右侧子菜单。
        picker.addEventListener(
          "click",
          function (event) {
            var row = rowOf(event.target);
            if (!hasLevels(row)) return;
            event.preventDefault();
            event.stopImmediatePropagation();
            showCascade(picker, row);
          },
          true
        );
        picker.addEventListener("mouseover", function (event) {
          var row = rowOf(event.target);
          if (!row) return;
          if (hasLevels(row)) showCascade(picker, row);
          else hideCascade(null);
        });
        picker.addEventListener("focusin", function (event) {
          var row = rowOf(event.target);
          if (hasLevels(row)) showCascade(picker, row);
        });
        picker.addEventListener(
          "keydown",
          function (event) {
            var row = rowOf(document.activeElement);
            if (row && hasLevels(row) && (event.key === "ArrowRight" || event.key === "Enter")) {
              event.preventDefault();
              event.stopImmediatePropagation();
              showCascade(picker, row);
              var first = cascade ? cascade.querySelector("[data-cascade-level]") : null;
              if (first) first.focus();
              return;
            }
            if (event.key === "Escape" && cascade && !cascade.hidden) {
              event.preventDefault();
              event.stopImmediatePropagation();
              hideCascade(true);
            }
          },
          true
        );
        var menu = picker.querySelector("[data-select-menu]");
        if (menu && window.MutationObserver) {
          new MutationObserver(function () {
            if (menu.hidden) hideCascade(null);
          }).observe(menu, { attributes: true, attributeFilter: ["hidden"] });
        }
      })(pickers[i]);
    }
  }

  document.addEventListener("ada:select", function (event) {
    var picker = event.target;
    if (!picker || !picker.matches || !picker.matches("[data-model-picker]")) return;
    var value = String((event.detail && event.detail.value) || "");
    var cut = value.indexOf("|");
    if (cut <= 0) return;
    var providerId = value.slice(0, cut);
    var modelId = value.slice(cut + 1);

    // ada-select 默认把整个 option 文本写进 label；这里换成简短名称。
    var labelNode = picker.querySelector("[data-select-label]");
    var chosen = null;
    var options = picker.querySelectorAll("[data-select-menu] [data-value]");
    for (var i = 0; i < options.length; i++) {
      if (options[i].getAttribute("data-value") === value) chosen = options[i];
    }
    if (labelNode && chosen) {
      var shortLabel = chosen.getAttribute("data-label");
      if (shortLabel) labelNode.textContent = shortLabel;
    }
    switchModel(picker, providerId, modelId, "");
  });

  /* ── 3. 侧栏：≤760px 是抽屉，>760px 是 56px 窄轨 ───────────────────────
     两套形态共用一个按钮（data-sidebar-toggle），但状态不同：
       · 窄屏：.ada-sidebar.is-open + 遮罩，点遮罩/Esc 关闭；
       · 宽屏：.ada-shell.is-sidebar-collapsed（CSS 里收成窄轨，不是隐藏），
         偏好存 localStorage。窄轨的目的是"收起不等于失去导航"，
         所以别再改回 display:none —— 那会让折叠 = 自断一臂。 */
  var COLLAPSE_KEY = 'a_da_sidebar_collapsed';
  var narrowQuery = window.matchMedia('(max-width: 760px)');

  function sidebar() {
    return document.getElementById('chat-sidebar');
  }

  function backdrop() {
    return document.getElementById('chat-backdrop');
  }

  function shell() {
    return document.querySelector('.ada-shell');
  }

  function readCollapsed() {
    try {
      return window.localStorage.getItem(COLLAPSE_KEY) === '1';
    } catch (error) {
      return false;
    }
  }

  function writeCollapsed(collapsed) {
    try {
      window.localStorage.setItem(COLLAPSE_KEY, collapsed ? '1' : '0');
    } catch (error) {
      /* 隐私模式下不可用：忽略即可 */
    }
  }

  function isCollapsed() {
    var wrap = shell();
    return !!(wrap && wrap.classList.contains('is-sidebar-collapsed'));
  }

  /* 按钮的无障碍状态要跟真实形态一致，否则读屏用户听到的是"打开侧栏"却按不开 */
  function syncToggle() {
    var button = document.querySelector('[data-sidebar-toggle]');
    if (!button) return;
    var narrow = narrowQuery.matches;
    var open = narrow
      ? !!(sidebar() && sidebar().classList.contains('is-open'))
      : !isCollapsed();
    var label = narrow
      ? (open ? '关闭侧栏' : '打开侧栏')
      : (open ? '收起侧栏' : '展开侧栏');
    button.setAttribute('aria-expanded', open ? 'true' : 'false');
    button.setAttribute('aria-label', label);
    button.setAttribute('title', label);
  }

  function setSidebar(open) {
    var side = sidebar();
    var veil = backdrop();
    if (!side || !veil) return;
    var wasOpen = side.classList.contains('is-open');
    side.classList.toggle('is-open', open);
    veil.classList.toggle('is-open', open);
    // 抽屉开着时把主区置 inert：Tab 不再穿透遮罩落到被盖住的正文上
    // （19-ued §19.5.1；inert 覆盖 Electron/现代浏览器）。
    var main = document.getElementById('main');
    if (main) {
      if (open) main.setAttribute('inert', '');
      else main.removeAttribute('inert');
    }
    if (open) {
      var search = side.querySelector('.ada-search__input');
      if (search) search.focus();
    } else if (wasOpen) {
      // 焦点归还触发按钮（19-ued §19.5.1 弹层四要素）。
      var toggle = document.querySelector('[data-sidebar-toggle]');
      if (toggle) toggle.focus();
    }
    syncToggle();
  }

  function setCollapsed(collapsed) {
    var wrap = shell();
    if (!wrap) return;
    wrap.classList.toggle('is-sidebar-collapsed', collapsed);
    writeCollapsed(collapsed);
    syncToggle();
  }

  /* 跨断点时清理另一套形态的状态，避免"抽屉开着 + 折叠着"同时成立。
     进入窄屏时也强制关掉抽屉：首屏/bfcache 不应残留 is-open 遮住正文。 */
  function onBreakpointChange() {
    var side = sidebar();
    var veil = backdrop();
    if (narrowQuery.matches) {
      var wrap = shell();
      if (wrap) wrap.classList.remove('is-sidebar-collapsed');
      if (side) side.classList.remove('is-open');
      if (veil) veil.classList.remove('is-open');
    } else {
      if (side) side.classList.remove('is-open');
      if (veil) veil.classList.remove('is-open');
      setCollapsed(readCollapsed());
    }
    syncToggle();
  }

  /* ── 侧栏宽度：右缘拖拽（宽屏展开态） ───────────────────────────────
     宽度写进 --ada-sidebar-width，首帧由 sidebar-boot.js 预置以免闪动；
     拖拽只改 CSS 变量，松手才落 localStorage。窄屏抽屉与折叠窄轨不参与。 */
  var SIDEBAR_WIDTH_KEY = 'a_da_sidebar_width';
  var SIDEBAR_WIDTH_MIN = 200;
  var SIDEBAR_WIDTH_MAX = 420;
  var SIDEBAR_WIDTH_DEFAULT = 264;

  function clampSidebarWidth(width) {
    var max = Math.min(SIDEBAR_WIDTH_MAX, Math.max(SIDEBAR_WIDTH_MIN, window.innerWidth - 360));
    return Math.round(Math.min(Math.max(width, SIDEBAR_WIDTH_MIN), max));
  }

  function readSidebarWidth() {
    try {
      var stored = Number(window.localStorage.getItem(SIDEBAR_WIDTH_KEY));
      if (isFinite(stored) && stored >= SIDEBAR_WIDTH_MIN && stored <= SIDEBAR_WIDTH_MAX) return stored;
    } catch (error) {
      /* 存储不可用：用默认宽度 */
    }
    return SIDEBAR_WIDTH_DEFAULT;
  }

  function applySidebarWidth(width, persist) {
    var value = clampSidebarWidth(width);
    document.documentElement.style.setProperty('--ada-sidebar-width', value + 'px');
    var handle = document.querySelector('[data-sidebar-resizer]');
    if (handle) handle.setAttribute('aria-valuenow', String(value));
    if (persist) {
      try {
        window.localStorage.setItem(SIDEBAR_WIDTH_KEY, String(value));
      } catch (error) {
        /* 忽略 */
      }
    }
    return value;
  }

  function bindSidebarResizer() {
    var handle = document.querySelector('[data-sidebar-resizer]');
    if (!handle) return;
    applySidebarWidth(readSidebarWidth(), false);

    var drag = null;
    function onMove(event) {
      if (!drag || event.pointerId !== drag.id) return;
      applySidebarWidth(drag.startWidth + (event.clientX - drag.startX), false);
    }
    function onUp(event) {
      if (!drag || event.pointerId !== drag.id) return;
      applySidebarWidth(drag.startWidth + (event.clientX - drag.startX), true);
      drag = null;
      handle.classList.remove('is-dragging');
      document.body.classList.remove('ada-sidebar-resizing');
      try {
        handle.releasePointerCapture(event.pointerId);
      } catch (error) {
        /* 没有捕获也无需处理 */
      }
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
    }

    handle.addEventListener('pointerdown', function (event) {
      if (event.button !== 0 || narrowQuery.matches || isCollapsed()) return;
      event.preventDefault();
      try {
        handle.focus({ preventScroll: true });
      } catch (error) {
        handle.focus();
      }
      drag = {
        id: event.pointerId,
        startX: event.clientX,
        startWidth: sidebar() ? sidebar().getBoundingClientRect().width : SIDEBAR_WIDTH_DEFAULT
      };
      handle.classList.add('is-dragging');
      document.body.classList.add('ada-sidebar-resizing');
      try {
        handle.setPointerCapture(event.pointerId);
      } catch (error) {
        /* 指针捕获失败时 window 监听仍能收到移动 */
      }
      window.addEventListener('pointermove', onMove);
      window.addEventListener('pointerup', onUp);
      window.addEventListener('pointercancel', onUp);
    });

    // 键盘可达：左右 16px、Home/End 到边界、双击恢复默认。
    handle.addEventListener('keydown', function (event) {
      if (narrowQuery.matches || isCollapsed()) return;
      var current = sidebar() ? sidebar().getBoundingClientRect().width : SIDEBAR_WIDTH_DEFAULT;
      var next = null;
      if (event.key === 'ArrowLeft') next = current - 16;
      else if (event.key === 'ArrowRight') next = current + 16;
      else if (event.key === 'Home') next = SIDEBAR_WIDTH_MIN;
      else if (event.key === 'End') next = SIDEBAR_WIDTH_MAX;
      if (next === null) return;
      event.preventDefault();
      applySidebarWidth(next, true);
    });
    handle.addEventListener('dblclick', function () {
      if (narrowQuery.matches || isCollapsed()) return;
      applySidebarWidth(SIDEBAR_WIDTH_DEFAULT, true);
    });

    window.addEventListener('resize', function () {
      applySidebarWidth(readSidebarWidth(), false);
    });
  }

  document.addEventListener('click', function (event) {
    var target = event.target;
    if (!target || !target.closest) return;
    if (target.closest('[data-sidebar-toggle]')) {
      if (narrowQuery.matches) {
        setSidebar(!(sidebar() && sidebar().classList.contains('is-open')));
      } else {
        setCollapsed(!isCollapsed());
      }
      return;
    }
    if (target.closest('#chat-backdrop')) {
      setSidebar(false);
    }
  });

  if (typeof narrowQuery.addEventListener === 'function') {
    narrowQuery.addEventListener('change', onBreakpointChange);
  } else if (typeof narrowQuery.addListener === 'function') {
    narrowQuery.addListener(onBreakpointChange);
  }

  document.addEventListener('keydown', function (event) {
    if (event.key !== 'Escape') return;
    // 弹窗开着时 Esc 归弹窗控制器管，别顺手把侧栏也收起来。
    if (window.AdaComponents && window.AdaComponents.modalOpen && window.AdaComponents.modalOpen()) {
      return;
    }
    var side = sidebar();
    if (side && side.classList.contains('is-open')) {
      setSidebar(false);
    }
  });

  // 打开会话先落到最新一条：不置底的话实时输出在视口外，用户看到的是
  // “发完消息什么也没发生”。同步赋值，不加平滑（见 .ada-scroll 的注释）。
  function scrollChatToBottom() {
    var scroller = document.getElementById('chat-scroll');
    if (!scroller) return;
    scroller.scrollTop = scroller.scrollHeight;
  }

  /* 「回到最新」：模板里默认 hidden，用户向上滚出底部才显示。
     它挂在 app.js（每页都加载）而不是 chat-live.js（只在运行中加载），
     这样浏览历史消息时也能一键回到底部。 */
  function bindScrollBottom() {
    var scroller = document.getElementById('chat-scroll');
    var button = document.getElementById('scroll-bottom');
    if (!scroller || !button) return;

    function sync() {
      button.hidden = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight <= 140;
    }

    scroller.addEventListener('scroll', sync, { passive: true });
    button.addEventListener('click', function () {
      // 直接置底而不是 smooth：流式输出期间内容高度持续变化，平滑动画
      // 可能停在半途；后台标签页也会暂停 rAF，按钮看起来“没反应”。
      scroller.scrollTop = scroller.scrollHeight;
      sync();
    });
    sync();
  }

  /* ── 分步表单（ask_user 一次问多个）：上一题 / 下一题 / 提交 ─────────────
     没有 JS 时所有题目一起展开、直接提交也能用（字段名 q0、q1…，基座按序拼成
     answers）；这里只做翻阅与按钮显隐，答案仍由表单本身提交。 */
  function bindAskWizards(root) {
    var forms = (root || document).querySelectorAll('[data-ask-form]');
    for (var i = 0; i < forms.length; i += 1) {
      var form = forms[i];
      if (form.getAttribute('data-ask-bound') === '1') continue;
      form.setAttribute('data-ask-bound', '1');
      setupAskWizard(form);
    }
  }

  function setupAskWizard(form) {
    var steps = form.querySelectorAll('[data-ask-step]');
    if (!steps.length) return;
    var prev = form.querySelector('[data-ask-prev]');
    var next = form.querySelector('[data-ask-next]');
    var submit = form.querySelector('[data-ask-submit]');
    var counter = form.querySelector('[data-ask-current]');
    var index = 0;

    // 未到达步骤的字段一律 disabled（19-ued §19.5.5）：文本框里按 Enter 或
    // 任何路径触发的提前提交，都不能把 select/confirm 的默认值（第一项/「是」）
    // 替用户作答交上去。无 JS 时没有这段代码，全部步骤照常展开提交。
    function setStepEnabled(step, enabled) {
      var fields = step.querySelectorAll('input, select, textarea');
      for (var i = 0; i < fields.length; i += 1) {
        fields[i].disabled = !enabled;
      }
    }

    function render() {
      var last = steps.length - 1;
      for (var i = 0; i < steps.length; i += 1) {
        steps[i].hidden = i !== index;
        setStepEnabled(steps[i], i === index);
      }
      if (prev) prev.disabled = index === 0;
      if (next) next.hidden = index === last;
      if (submit) submit.hidden = index !== last;
      if (counter) counter.textContent = String(index + 1);
      var field = steps[index].querySelector('input, select, [data-select-trigger]');
      if (field && field.focus) {
        try {
          field.focus({ preventScroll: true });
        } catch (err) {
          field.focus();
        }
      }
    }

    // 非末步的提交一律转成「下一题」；取消（decision=cancel）随时放行。
    // 走到末步后，Enter 经表单默认提交按钮（提交）正常交出。
    form.addEventListener('submit', function (event) {
      var submitter = event.submitter;
      if (submitter && submitter.name === 'decision' && submitter.value === 'cancel') return;
      if (index >= steps.length - 1) {
        // 提交前把所有步骤的字段重新启用：disabled 的控件不随表单提交，
        // 只带末题会让服务端按 q0、q1… 拼出的 answers 缺项，插件会以
        // 「回答数量与问题不一致」拒绝（ask_user 多题的固定报错）。
        for (var s = 0; s < steps.length; s += 1) setStepEnabled(steps[s], true);
        return;
      }
      event.preventDefault();
      if (next) next.click();
    });

    if (next) {
      next.addEventListener('click', function () {
        if (index >= steps.length - 1) return;
        index += 1;
        render();
      });
    }
    if (prev) {
      prev.addEventListener('click', function () {
        if (index <= 0) return;
        index -= 1;
        render();
      });
    }
    render();
  }

  /* ── 会话搜索：本地过滤已渲染的会话行 ─────────────────────────────────
     侧栏的输入框、Ctrl/Cmd+K 与窄轨上的搜索按钮都指向同一份过滤逻辑。
     不做服务端搜索：线程列表本来就在 DOM 里，过滤是微秒级；重新请求
     反而会让侧栏闪一下。清空后恢复全部行与分组。 */
  function bindThreadSearch() {
    var input = document.getElementById('chat-thread-search');
    if (!input) return;
    var count = document.querySelector('.ada-search__count');

    function apply() {
      var needle = input.value.trim().toLowerCase();
      // 每次重新取：其它工作区的会话是展开时懒加载进来的，绑定时的快照会漏。
      var rows = document.querySelectorAll('#thread-list [data-thread-row]');
      var visible = 0;
      for (var i = 0; i < rows.length; i += 1) {
        var title = rows[i].querySelector('.ada-thread__title');
        var text = title ? title.textContent.toLowerCase() : '';
        var hit = !needle || text.indexOf(needle) !== -1;
        rows[i].hidden = !hit;
        if (hit) visible += 1;
      }

      // 有搜索词时，没有命中行的分组（含尚未懒加载的其它工作区）一起收起；
      // 清空后恢复全部分组，否则空分组会被永久隐藏。
      var groups = document.querySelectorAll('#thread-list .ada-ws');
      var anyGroup = false;
      for (var g = 0; g < groups.length; g += 1) {
        var hasRow = !!groups[g].querySelector('[data-thread-row]:not([hidden])');
        groups[g].hidden = needle ? !hasRow : false;
        if (hasRow) anyGroup = true;
      }
      var section = document.querySelector('[data-ws-section]');
      if (section) section.hidden = needle ? !anyGroup : false;

      if (count) {
        count.hidden = !needle;
        if (needle) count.textContent = visible ? visible + ' / ' + rows.length : '未找到';
      }
    }

    function focusSearch() {
      if (narrowQuery.matches) setSidebar(true);
      input.focus();
      input.select();
    }

    input.addEventListener('input', apply);
    input.addEventListener('search', apply);
    input.addEventListener('keydown', function (event) {
      if (event.key !== 'Escape') return;
      input.value = '';
      apply();
      input.blur();
    });

    document.addEventListener('keydown', function (event) {
      if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== 'k') return;
      // 弹窗打开时不抢焦点（19-ued §19.5.2）：把焦点甩到 modal 背后的侧栏
      // 等于把用户困在弹窗里。让路后不 preventDefault，交给浏览器默认行为。
      if (window.AdaComponents && typeof AdaComponents.modalOpen === 'function' && AdaComponents.modalOpen()) return;
      event.preventDefault();
      focusSearch();
    });

    var railSearch = document.querySelector('[data-rail-search]');
    if (railSearch) railSearch.addEventListener('click', focusSearch);
  }

  /* ── 待发送消息 outbox ───────────────────────────────────────────────
     运行中发送的消息不再立刻走 turn.steer，而是先落本地队列，浮在输入框
     上方；用户可编辑、立即发送或删除。刷新后队列仍在（localStorage）。
     当前轮次正常完成时自动发送队首，其余继续等待下一轮。 */
  var outboxForm = null;
  var outboxHost = null;
  var outboxThreadID = "";
  var outboxItems = [];
  var outboxSending = false;
  var OUTBOX_LIMIT = 20;

  function outboxKey(threadID) {
    return 'a_da_outbox:' + threadID;
  }

  function readOutbox(threadID) {
    try {
      var raw = window.localStorage.getItem(outboxKey(threadID));
      var parsed = raw ? JSON.parse(raw) : [];
      if (!Array.isArray(parsed)) return [];
      return parsed.filter(function (item) {
        return item && typeof item.text === 'string' && item.text.trim() !== '';
      }).slice(0, OUTBOX_LIMIT);
    } catch (error) {
      return [];
    }
  }

  function writeOutbox() {
    if (!outboxThreadID) return;
    try {
      window.localStorage.setItem(outboxKey(outboxThreadID), JSON.stringify(outboxItems));
    } catch (error) {
      /* 存储不可用：队列本次页面内仍然生效 */
    }
  }

  function outboxIcon(name) {
    var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'ada-icon');
    svg.setAttribute('width', '14');
    svg.setAttribute('height', '14');
    svg.setAttribute('aria-hidden', 'true');
    var use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', '#i-' + name);
    svg.appendChild(use);
    return svg;
  }

  function outboxActionButton(action, iconName, label, tone) {
    var button = document.createElement('button');
    button.type = 'button';
    button.className = 'ada-outbox__action' + (tone ? ' ada-outbox__action--' + tone : '');
    button.setAttribute('data-outbox-action', action);
    button.setAttribute('aria-label', label);
    button.setAttribute('title', label);
    button.appendChild(outboxIcon(iconName));
    return button;
  }

  function renderOutbox() {
    if (!outboxHost) return;
    outboxHost.textContent = '';
    outboxHost.hidden = outboxItems.length === 0;

    var head = document.createElement('div');
    head.className = 'ada-outbox__head';
    var title = document.createElement('span');
    title.textContent = '待发送 · ' + outboxItems.length;
    head.appendChild(title);
    outboxHost.appendChild(head);

    outboxItems.forEach(function (item) {
      var row = document.createElement('div');
      row.className = 'ada-outbox__item';
      row.setAttribute('data-outbox-id', item.id);

      var text = document.createElement('p');
      text.className = 'ada-outbox__text';
      text.textContent = item.text;
      text.setAttribute('title', item.text);
      row.appendChild(text);

      var actions = document.createElement('div');
      actions.className = 'ada-outbox__actions';
      actions.appendChild(outboxActionButton('edit', 'edit', '编辑'));
      actions.appendChild(outboxActionButton('send', 'send', '立即发送', 'primary'));
      actions.appendChild(outboxActionButton('delete', 'trash', '删除', 'danger'));
      row.appendChild(actions);
      outboxHost.appendChild(row);
    });
  }

  function currentRunStatus() {
    var root = document.getElementById('transcript');
    return root ? String(root.getAttribute('data-run-status') || '').toLowerCase() : '';
  }

  function runIsActive() {
    var status = currentRunStatus();
    return status === 'running' || status === 'waiting_input' || status === 'finishing';
  }

  function enqueueOutbox(text) {
    var value = String(text || '').trim();
    if (!value) return false;
    if (outboxItems.length >= OUTBOX_LIMIT) {
      notify('待发送消息最多 ' + OUTBOX_LIMIT + ' 条，请先处理已有消息。', 'error');
      return false;
    }
    outboxItems.push({
      id: Date.now().toString(36) + Math.random().toString(36).slice(2, 8),
      text: value
    });
    writeOutbox();
    renderOutbox();
    return true;
  }

  function removeOutboxItem(id) {
    var index = -1;
    for (var i = 0; i < outboxItems.length; i += 1) {
      if (outboxItems[i].id === id) {
        index = i;
        break;
      }
    }
    if (index < 0) return null;
    var item = outboxItems.splice(index, 1)[0];
    writeOutbox();
    renderOutbox();
    return item;
  }

  function editOutboxItem(id) {
    if (outboxSending) return;
    var item = removeOutboxItem(id);
    if (!item || !outboxForm) return;
    var input = outboxForm.querySelector('textarea[name="text"]');
    if (!input) return;
    input.value = item.text + (input.value ? '\n' + input.value : '');
    input.dispatchEvent(new Event('input', { bubbles: true }));
    try {
      input.focus({ preventScroll: true });
      input.setSelectionRange(item.text.length, item.text.length);
    } catch (error) {
      /* 焦点/光标失败不影响编辑 */
    }
  }

  function deleteOutboxItem(id) {
    if (outboxSending) return;
    if (removeOutboxItem(id)) notify('已删除待发送消息');
  }

  function sendOutboxItem(id, auto) {
    if (outboxSending || !outboxForm) return;
    if (currentRunStatus() === 'waiting_input' || currentRunStatus() === 'finishing') {
      notify('当前轮次正在等待答复或收尾，请稍后再发送。', 'error');
      return;
    }
    var item = null;
    for (var i = 0; i < outboxItems.length; i += 1) {
      if (outboxItems[i].id === id) {
        item = outboxItems[i];
        break;
      }
    }
    if (!item) return;

    var csrf = document.querySelector('meta[name="csrf-token"]');
    var body = new URLSearchParams();
    body.set('text', item.text);
    // 队列项 id 同时作为幂等键：网络失败后重试同一条不会起两轮。
    body.set('idempotencyKey', item.id);
    outboxSending = true;
    outboxHost.setAttribute('aria-busy', 'true');

    fetch(outboxForm.getAttribute('action') || window.location.pathname + '/send', {
      method: 'POST',
      credentials: 'same-origin',
      redirect: 'manual',
      headers: {
        'Content-Type': 'application/x-www-form-urlencoded',
        'X-CSRF-Token': csrf ? csrf.getAttribute('content') : ''
      },
      body: body.toString()
    }).then(function (res) {
      // 网关成功时是 303；manual 重定向在 Chromium 里是 opaqueredirect。
      if (res.type === 'opaqueredirect' || res.status === 303) {
        removeOutboxItem(item.id);
        window.location.href = '/threads/' + encodeURIComponent(outboxThreadID);
        return null;
      }
      throw new Error('HTTP ' + res.status);
    }).catch(function (err) {
      outboxSending = false;
      outboxHost.removeAttribute('aria-busy');
      if (auto) {
        window.location.reload();
        return;
      }
      notify('发送待发送消息失败：' + (err && err.message ? err.message : err), 'error');
    });
  }

  function handleOutboxClick(event) {
    var button = event.target.closest && event.target.closest('[data-outbox-action]');
    if (!button || !outboxHost) return;
    var row = button.closest('[data-outbox-id]');
    if (!row) return;
    var id = row.getAttribute('data-outbox-id');
    var action = button.getAttribute('data-outbox-action');
    if (action === 'edit') editOutboxItem(id);
    else if (action === 'delete') deleteOutboxItem(id);
    else if (action === 'send') sendOutboxItem(id, false);
  }

  function handleOutboxSubmit(event) {
    if (event.defaultPrevented || !runIsActive()) return;
    var form = event.target;
    if (form !== outboxForm) return;
    var input = form.querySelector('textarea[name="text"]');
    if (!input) return;
    var text = String(input.value || '').trim();
    if (!text) return; // 纯图片消息沿用原生发送

    // 图片暂不进 outbox：File 无法跨刷新保存，交给原生 turn.steer 立即送达。
    var images = form.querySelector('input[name="images"]');
    if (images && images.files && images.files.length > 0) return;

    event.preventDefault();
    event.stopImmediatePropagation();
    if (!enqueueOutbox(text)) return;
    input.value = '';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    try {
      input.focus({ preventScroll: true });
    } catch (error) {
      /* 忽略 */
    }
    notify('已加入待发送队列');
  }

  function initOutbox() {
    outboxForm = document.querySelector('form.ada-composer[data-thread-id]');
    outboxHost = document.querySelector('[data-outbox]');
    if (!outboxForm || !outboxHost) return;
    outboxThreadID = outboxForm.getAttribute('data-thread-id') || '';
    if (!outboxThreadID) return;
    outboxItems = readOutbox(outboxThreadID);
    renderOutbox();
    outboxForm.addEventListener('submit', handleOutboxSubmit, true);
    outboxHost.addEventListener('click', handleOutboxClick);
  }

  // chat-live.js 在 completed 终态、整页 reload 之前调用：有队列就先发队首，
  // 并让出 reload（发送成功后由 sendOutboxItem 自己导航）。
  window.AdaOutbox = {
    onRunEnd: function (reason) {
      if (String(reason || '') !== 'completed') return false;
      if (!outboxItems.length || !outboxForm) return false;
      if (outboxSending) return true;
      sendOutboxItem(outboxItems[0].id, true);
      return true;
    }
  };

  /* ── 会话右键菜单：复制 ID / 归档 ───────────────────────────────────── */
  var threadMenu = null;
  var threadMenuRow = null;
  var threadMenuBound = false;

  function closeThreadMenu(restoreFocus) {
    if (!threadMenu || threadMenu.hidden) return;
    threadMenu.hidden = true;
    var row = threadMenuRow;
    threadMenuRow = null;
    if (restoreFocus && row) {
      var link = row.querySelector('.ada-thread__link');
      if (link) link.focus({ preventScroll: true });
    }
  }

  function copyText(text) {
    if (window.AdaComponents && typeof window.AdaComponents.copyText === 'function') {
      return window.AdaComponents.copyText(text);
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text);
    }
    return Promise.reject(new Error('当前环境不支持剪贴板'));
  }

  function copyThreadID(row) {
    var id = row.getAttribute('data-thread-row') || '';
    if (!id) return;
    copyText(id).then(function () {
      notify('会话 ID 已复制');
    }).catch(function () {
      notify('复制失败，请手动选择会话 ID。', 'error');
    });
  }

  function archiveThread(row) {
    var id = row.getAttribute('data-thread-row') || '';
    if (!id) return;
    var titleNode = row.querySelector('.ada-thread__title');
    var title = titleNode ? titleNode.textContent : '这个会话';
    var form = document.createElement('form');
    form.method = 'post';
    form.action = '/threads/' + encodeURIComponent(id) + '/archive';
    form.hidden = true;

    var csrf = document.querySelector('meta[name="csrf-token"]');
    if (csrf) {
      var token = document.createElement('input');
      token.type = 'hidden';
      token.name = 'csrf_token';
      token.value = csrf.getAttribute('content') || '';
      form.appendChild(token);
    }
    var next = document.createElement('input');
    next.type = 'hidden';
    next.name = 'next';
    next.value = window.location.pathname + window.location.search;
    form.appendChild(next);
    document.body.appendChild(form);

    var submit = function () {
      form.submit();
    };
    if (typeof window.adaConfirm === 'function' && document.querySelector('[data-confirm-dialog]')) {
      window.adaConfirm('归档会话「' + title + '」？归档后不再显示在侧栏。', submit);
    } else {
      submit();
    }
    // 用户取消确认时表单不会提交；留一个兜底清理，避免隐藏表单常驻 DOM。
    window.setTimeout(function () {
      if (form.isConnected) form.remove();
    }, 30000);
  }

  function ensureThreadMenu() {
    if (threadMenu) return threadMenu;
    threadMenu = document.createElement('div');
    threadMenu.className = 'ada-context-menu';
    threadMenu.setAttribute('role', 'menu');
    threadMenu.setAttribute('aria-label', '会话操作');
    threadMenu.hidden = true;
    threadMenu.innerHTML =
      '<button class="ada-row-menu__item" type="button" role="menuitem" data-thread-menu-action="copy">复制会话 ID</button>' +
      '<button class="ada-row-menu__item" type="button" role="menuitem" data-thread-menu-action="archive">归档会话</button>';
    document.body.appendChild(threadMenu);

    threadMenu.addEventListener('click', function (event) {
      var button = event.target.closest && event.target.closest('[data-thread-menu-action]');
      if (!button || button.disabled) return;
      var action = button.getAttribute('data-thread-menu-action');
      var row = threadMenuRow;
      if (!row) return;
      if (action === 'copy') {
        copyThreadID(row);
        closeThreadMenu(false);
        return;
      }
      if (action === 'archive') {
        closeThreadMenu(false);
        archiveThread(row);
      }
    });
    return threadMenu;
  }

  function openThreadMenu(row, x, y) {
    var menu = ensureThreadMenu();
    threadMenuRow = row;
    var running = row.getAttribute('data-thread-running') === '1';
    var archive = menu.querySelector('[data-thread-menu-action="archive"]');
    if (archive) {
      archive.disabled = running;
      archive.title = running ? '运行中的会话不能归档' : '';
    }

    menu.hidden = false;
    menu.style.left = '0px';
    menu.style.top = '0px';
    var width = menu.offsetWidth;
    var height = menu.offsetHeight;
    var left = Math.max(8, Math.min(x, window.innerWidth - width - 8));
    var top = Math.max(8, Math.min(y, window.innerHeight - height - 8));
    menu.style.left = left + 'px';
    menu.style.top = top + 'px';

    var first = menu.querySelector('button:not(:disabled)');
    if (first) first.focus({ preventScroll: true });
  }

  function bindThreadContextMenu() {
    if (threadMenuBound) return;
    threadMenuBound = true;

    document.addEventListener('contextmenu', function (event) {
      var row = event.target.closest && event.target.closest('[data-thread-row]');
      var list = document.getElementById('thread-list');
      if (!row || !list || !list.contains(row)) {
        closeThreadMenu(false);
        return;
      }
      event.preventDefault();
      openThreadMenu(row, event.clientX, event.clientY);
    });

    document.addEventListener('pointerdown', function (event) {
      if (!threadMenu || threadMenu.hidden) return;
      if (!threadMenu.contains(event.target)) closeThreadMenu(false);
    }, true);

    document.addEventListener('keydown', function (event) {
      if (event.key !== 'Escape' || !threadMenu || threadMenu.hidden) return;
      event.preventDefault();
      closeThreadMenu(true);
    });

    window.addEventListener('resize', function () { closeThreadMenu(false); });
    window.addEventListener('scroll', function () { closeThreadMenu(false); }, true);
  }

  /* 选择/登记工作区：POST /api/workspace/select 记住 cookie，成功后调用方刷新页面。 */
  function selectWorkspace(path) {
    var csrf = document.querySelector('meta[name="csrf-token"]');
    var body = new URLSearchParams();
    body.set("path", path);
    return fetch("/api/workspace/select", {
      method: "POST",
      credentials: "same-origin",
      redirect: "manual",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        "X-CSRF-Token": csrf ? csrf.getAttribute("content") : ""
      },
      body: body.toString()
    }).then(function (res) {
      if (res.type === "opaqueredirect" || res.ok) return true;
      return res.text().then(function (text) {
        throw new Error(text || ("HTTP " + res.status));
      });
    });
  }

  /* 侧栏工作区树：工作区头部「+」打开目录选择器；其它工作区组展开时懒加载会话。
     会话片段由服务端渲染（同一套转义与行模板），客户端只做节点替换。 */
  function bindWorkspaceTree() {
    var opener = document.querySelector('[data-open-workspace]');
    if (opener && window.AdaFilePicker) {
      opener.addEventListener('click', function (event) {
        event.preventDefault();
        window.AdaFilePicker.open({
          current: opener.getAttribute('data-current-workspace') || '',
          onSelect: function (workspace) {
            if (!workspace || !workspace.path) return;
            selectWorkspace(workspace.path).then(function () {
              window.location.reload();
            }).catch(function (err) {
              notify("打开工作区失败：" + (err && err.message ? err.message : err), "error");
            });
          }
        });
      });
    }

    var groups = document.querySelectorAll('#thread-list .ada-ws');
    for (var i = 0; i < groups.length; i += 1) {
      (function (group) {
        var details = group.querySelector('.ada-ws__details');
        if (!details || details.open) return; // 当前工作区已由服务端渲染
        var path = group.getAttribute('data-ws-path');
        var body = group.querySelector('[data-ws-threads]');
        if (!path || !body) return;
        var loaded = false;
        details.addEventListener('toggle', function () {
          if (!details.open || loaded) return;
          loaded = true;
          body.innerHTML = '<p class="ada-ws__empty">正在加载会话…</p>';
          fetch('/partials/workspace-threads?path=' + encodeURIComponent(path), {
            headers: { Accept: 'text/html' },
            credentials: 'same-origin'
          }).then(function (res) {
            if (!res.ok) throw new Error('HTTP ' + res.status);
            return res.text();
          }).then(function (html) {
            body.innerHTML = html;
            var count = group.querySelector('[data-ws-count]');
            if (count) {
              count.textContent = String(body.querySelectorAll('[data-thread-row]').length);
              count.hidden = false;
            }
          }).catch(function (err) {
            loaded = false; // 允许重新展开重试
            body.innerHTML = '<p class="ada-ws__empty">读取会话失败，收起后重新展开重试</p>';
            console.warn('workspace threads', err);
          });
        });
      })(groups[i]);
    }
  }

  /* ── 新建任务：工作区选择 + 居中输入 ─────────────────────────────────
     有 JS 时把服务端渲染的原生 select 换成 FilePicker 弹窗；没有 JS 时
     原生 select 照常提交。hidden 字段只在增强成功后才有 name，避免两个
     同名字段同时提交。 */
  function bindNewTask() {
    var form = document.querySelector('form[data-new-task]');
    if (!form) return;
    var controls = form.querySelector('[data-workspace-controls]');
    var nativeSelect = form.querySelector('[data-workspace-native]');
    var hidden = form.querySelector('[data-workspace-input]');
    var label = form.querySelector('[data-workspace-label]');
    var enhanced = !!(window.AdaFilePicker && controls && hidden);

    function choose(workspace) {
      if (!workspace || !hidden) return;
      // 选中即落 cookie：模型选择等操作会整页 reload，未提交的工作区选择
      // 不能因此丢回旧工作区（否则模型列表会突然换一套供应商）。
      selectWorkspace(workspace.path).then(function () {
        window.location.reload();
      }).catch(function (err) {
        notify("切换工作区失败：" + (err && err.message ? err.message : err), "error");
      });
    }

    if (enhanced) {
      controls.hidden = false;
      if (nativeSelect) {
        nativeSelect.hidden = true;
        nativeSelect.disabled = true;
      }
      hidden.setAttribute('name', 'workspace');
      controls.addEventListener('click', function (event) {
        var opener = event.target.closest && event.target.closest('[data-file-picker-open]');
        if (!opener) return;
        event.preventDefault();
        window.AdaFilePicker.open({
          current: hidden.value,
          onSelect: choose
        });
      });
    } else if (controls) {
      controls.hidden = true;
    }

    var input = form.querySelector('textarea');
    if (input) {
      try {
        input.focus({ preventScroll: true });
      } catch (err) {
        input.focus();
      }
    }
  }

  function bindNewTaskShortcut() {
    document.addEventListener('keydown', function (event) {
      if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== 'n') return;
      // 输入中不拦截（19-ued §19.5.2）：整页跳转会丢掉正在写的消息。
      // 浏览器保留键（Ctrl+N/T/W）在普通浏览器上本就不可靠拦截，
      // 这里的提示只在 Electron 壳内稳定生效。
      var el = document.activeElement;
      if (el && (el.tagName === 'TEXTAREA' || el.isContentEditable ||
        (el.tagName === 'INPUT' && el.type !== 'checkbox' && el.type !== 'radio' &&
          el.type !== 'submit' && el.type !== 'button'))) return;
      event.preventDefault();
      window.location.href = '/';
    });
  }

  /* ── 表单提交态：防重复提交 + 让读屏知道「已提交、正在处理」 ──────────
     二级页的启用/停用、重载插件、新建目录都是整页 POST：网慢时按钮毫无变化，
     用户会以为没点上再点一次（重复启停、重复建目录）。这里在**冒泡阶段**给表单
     打 data-submitting，晚于上方 data-confirm 的捕获处理——确认弹窗拦下的提交
     （defaultPrevented）不进入提交态，等用户点「确认」后 requestSubmit 再来一次。
     不把按钮设成 disabled：disabled 的 submitter 不参与表单数据，
     `name="decision"` 这类按钮的值会丢；靠 CSS 的 pointer-events 挡住二次点击。 */
  var SUBMITTING = 'data-submitting';

  function markSubmitting(form, submitter) {
    if (!form || form.hasAttribute(SUBMITTING)) return false;
    form.setAttribute(SUBMITTING, '1');
    form.setAttribute('aria-busy', 'true');
    var button = submitter || form.querySelector('[type="submit"]');
    if (button && button.setAttribute) {
      button.setAttribute('aria-disabled', 'true');
      button.setAttribute('data-submit-pending', '1');
    }
    return true;
  }

  function clearSubmitting() {
    var forms = document.querySelectorAll('form[' + SUBMITTING + ']');
    for (var i = 0; i < forms.length; i += 1) {
      forms[i].removeAttribute(SUBMITTING);
      forms[i].removeAttribute('aria-busy');
      var buttons = forms[i].querySelectorAll('[data-submit-pending]');
      for (var j = 0; j < buttons.length; j += 1) {
        buttons[j].removeAttribute('aria-disabled');
        buttons[j].removeAttribute('data-submit-pending');
      }
    }
  }

  document.addEventListener('submit', function (event) {
    // 被确认弹窗拦下的提交：等用户答复，不能先锁死表单。
    if (event.defaultPrevented) return;
    var form = event.target;
    if (!form || form.tagName !== 'FORM') return;
    if (form.hasAttribute(SUBMITTING)) {
      event.preventDefault();
      return;
    }
    markSubmitting(form, event.submitter);
  });

  // 浏览器前进/后退恢复（bfcache）时，页面还带着上一轮的提交态，必须复位，
  // 否则表单会永久不可提交。
  window.addEventListener('pageshow', function (event) {
    if (event.persisted) clearSubmitting();
  });

  function boot() {
    wireDialog();
    autoDismissFlashes();
    scrollChatToBottom();
    bindScrollBottom();
    bindAskWizards(document);
    bindThreadSearch();
    bindThreadContextMenu();
    bindWorkspaceTree();
    bindSidebarResizer();
    bindNewTask();
    initOutbox();
    bindModelCascades();
    bindNewTaskShortcut();
    // 恢复上次的折叠偏好（宽屏）并让按钮文案与真实状态一致
    onBreakpointChange();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();


