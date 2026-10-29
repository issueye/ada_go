/*
 * 插件 HTTP API 的浏览器端小工具（registerHTTPAPI）。
 *
 * 用法（插件自带脚本或页面内联脚本）：
 *   const data = await window.adaAPI("provider-admin", "/providers", { method: "GET" });
 *   await window.adaAPI("provider-admin", "/providers/save", { method: "POST", body: {...} });
 *
 * - 路径相对于 /ext/<pluginId>/api；
 * - POST 自动带 X-CSRF-Token（来自页面 <meta name="csrf-token">）；
 * - 非 2xx 抛 Error（带 status 与 data），错误形状与基座 JSON API 一致。
 */
(function () {
  "use strict";

  function csrfToken() {
    var meta = document.querySelector('meta[name="csrf-token"]');
    return meta && meta.getAttribute("content") ? meta.getAttribute("content") : "";
  }

  window.adaAPI = async function (pluginId, path, options) {
    if (!pluginId) throw new Error("adaAPI: 缺少 pluginId");
    options = options || {};
    var method = String(options.method || "GET").toUpperCase();
    var target = String(path || "/");
    if (target.charAt(0) !== "/") target = "/" + target;

    var headers = Object.assign({ Accept: "application/json" }, options.headers || {});
    var body;
    if (options.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(options.body);
    }
    if (method !== "GET") {
      var token = csrfToken();
      if (token) headers["X-CSRF-Token"] = token;
    }

    var response = await fetch(
      "/ext/" + encodeURIComponent(pluginId) + "/api" + target,
      { method: method, headers: headers, body: body, credentials: "same-origin" }
    );
    var text = await response.text();
    var data = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch (err) {
        data = text;
      }
    }
    if (!response.ok) {
      var message =
        (data && data.error && data.error.message) ||
        (typeof data === "string" ? data : "") ||
        response.statusText ||
        ("HTTP " + response.status);
      var failure = new Error(message);
      failure.status = response.status;
      failure.data = data;
      throw failure;
    }
    return data;
  };
})();
