/*
 * 插件助手（plugin-helper）的浏览器端：安装向导。
 *
 * 表单提交到本插件 route POST /create；后端负责校验 id、检查目录是否已存在、
 * 写入 plugin.json / index.ts / README.md。成功后调用基座的插件重载接口，
 * 再跳到新插件详情页。
 */
(function () {
  "use strict";

  var form = document.querySelector("[data-pc-form]");
  if (!form) return;

  var statusNode = document.querySelector("[data-pc-status]");
  var submit = form.querySelector("[data-pc-submit]");
  var idInput = form.querySelector('input[name="id"]');
  var pathNode = document.querySelector("[data-pc-path]");
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
    return message || "创建失败";
  }

  function setBusy(value) {
    busy = value;
    if (submit) {
      submit.disabled = value;
      submit.textContent = value ? "创建中…" : "创建插件";
    }
  }

  function updatePath() {
    if (!pathNode || !idInput) return;
    var id = String(idInput.value || "").trim().toLowerCase();
    pathNode.textContent = "~/.adago/plugins/" + (id || "<id>");
  }

  function reloadPlugins() {
    var meta = document.querySelector('meta[name="csrf-token"]');
    var headers = { Accept: "text/html" };
    if (meta && meta.getAttribute("content")) {
      headers["X-CSRF-Token"] = meta.getAttribute("content");
    }
    return window
      .fetch("/agent-plugins/reload", {
        method: "POST",
        credentials: "same-origin",
        headers: headers,
      })
      .then(function (response) {
        if (!response.ok) throw new Error("插件重载失败（HTTP " + response.status + "）");
        return true;
      });
  }

  if (idInput) idInput.addEventListener("input", updatePath);
  updatePath();

  form.addEventListener("submit", function (event) {
    event.preventDefault();
    if (busy) return;
    if (typeof window.adaAPI !== "function") {
      status("页面缺少 adaAPI，无法创建插件。", "error");
      return;
    }
    if (typeof form.reportValidity === "function" && !form.reportValidity()) return;

    var data = new FormData(form);
    var id = String(data.get("id") || "").trim().toLowerCase();
    var body = {
      id: id,
      name: String(data.get("name") || "").trim(),
      description: String(data.get("description") || "").trim(),
      toolName: String(data.get("toolName") || "").trim(),
    };
    setBusy(true);
    status("正在创建插件…");

    window
      .adaAPI("plugin-helper", "/create", { method: "POST", body: body })
      .then(function (result) {
        status("已创建 " + result.path + "，正在重载插件…", "success");
        return reloadPlugins()
          .catch(function () {
            status("插件已创建，但自动重载失败；请到插件页手动重载。", "warn");
          })
          .then(function () {
            window.setTimeout(function () {
              window.location.href = "/extensions/" + encodeURIComponent(result.id);
            }, 300);
          });
      })
      .catch(function (err) {
        setBusy(false);
        status(errorText(err), "error");
      });
  });
})();
