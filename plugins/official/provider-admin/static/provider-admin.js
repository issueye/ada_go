/*
 * 供应商管理（provider-admin）的浏览器增强。
 *
 * 首屏由 SSR 用只读投影渲染；这里只做两件事：
 *   - 页面加载后用 GET /ext/provider-admin/api/state 同步一次真实状态；
 *   - 拦截“设为当前”表单，改走 POST /ext/provider-admin/api/select。
 * 没有 JS 时表单回落到基座 POST /model/select，校验与结果一致。
 */
(function () {
  "use strict";

  function statusEl(root) {
    return root.querySelector("[data-pa-status]");
  }

  function say(root, message, tone) {
    var el = statusEl(root);
    if (!el) return;
    el.textContent = message || "";
    if (tone) {
      el.setAttribute("data-tone", tone);
    } else {
      el.removeAttribute("data-tone");
    }
  }

  function labelOf(current) {
    if (!current || !current.providerId) return "未设置";
    return current.providerId + " / " + current.modelId;
  }

  function applyCurrent(root, current) {
    var label = root.querySelector("[data-pa-current-label]");
    if (label) label.textContent = labelOf(current);
    var rows = root.querySelectorAll("[data-pa-model]");
    for (var i = 0; i < rows.length; i++) {
      var row = rows[i];
      var match =
        current &&
        row.getAttribute("data-provider-id") === current.providerId &&
        row.getAttribute("data-model-id") === current.modelId;
      row.setAttribute("data-current", match ? "true" : "false");
    }
    var providers = root.querySelectorAll("[data-pa-provider]");
    for (var j = 0; j < providers.length; j++) {
      var provider = providers[j];
      var active = current && provider.getAttribute("data-provider-id") === current.providerId;
      provider.setAttribute("data-current", active ? "true" : "false");
    }
  }

  function busy(form, on) {
    form.setAttribute("aria-busy", on ? "true" : "false");
    var button = form.querySelector("button");
    if (button) button.disabled = !!on;
  }

  function refresh(root) {
    if (!window.adaAPI) return;
    window
      .adaAPI("provider-admin", "/state")
      .then(function (state) {
        if (!state) return;
        applyCurrent(root, state.current);
        if (root.getAttribute("data-empty") === "true" && state.providers && state.providers.length) {
          say(root, "检测到可用供应商，刷新页面后显示完整列表。", "");
        }
      })
      .catch(function (err) {
        say(root, "读取供应商状态失败：" + (err && err.message ? err.message : err), "error");
      });
  }

  function select(root, form) {
    var providerId = form.getAttribute("data-provider-id") || "";
    var modelId = form.getAttribute("data-model-id") || "";
    busy(form, true);
    say(root, "正在切换…", "");
    window
      .adaAPI("provider-admin", "/select", {
        method: "POST",
        body: { providerId: providerId, modelId: modelId },
      })
      .then(function (current) {
        applyCurrent(root, current);
        say(root, "已切换到 " + labelOf(current), "success");
      })
      .catch(function (err) {
        say(root, "切换失败：" + (err && err.message ? err.message : err), "error");
      })
      .then(function () {
        busy(form, false);
      });
  }

  function start() {
    var root = document.querySelector("[data-provider-admin]");
    if (!root) return;
    refresh(root);
    root.addEventListener("submit", function (event) {
      var form = event.target;
      if (!form || !form.hasAttribute || !form.hasAttribute("data-pa-select")) return;
      event.preventDefault();
      select(root, form);
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
