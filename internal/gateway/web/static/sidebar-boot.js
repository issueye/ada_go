/*
 * 侧栏折叠的“首屏预置”。
 *
 * app.js 在 DOMContentLoaded 后才恢复 localStorage 里的折叠偏好；如果等到那时
 * 才给 .ada-shell 加 is-sidebar-collapsed，用户会先看到 264px 的展开侧栏，
 * 再看到 0.2s 的宽度过渡——每次导航都闪一下。
 *
 * 这个脚本紧跟在 .ada-shell 开标签之后同步执行：在侧栏 DOM 解析/首帧之前
 * 就加上类名，宽度直接以 56px 计算，不产生过渡。CSP 只允许同源脚本，
 * 所以用独立文件而不是内联。
 */
(function () {
  "use strict";
  // 告诉服务端「这个浏览器有 JS」：有 JS 时运行中的会话页走 SSE 增量，不再带
  // <meta refresh>。那个标签一旦被解析就会排队刷新，JS 之后删掉也取消不了，
  // 整个运行期会变成每 2 秒整页重载一次。没有 JS 的客户端没有这个 cookie，
  // 服务端继续发 meta refresh 轮询。
  try {
    if (document.cookie.indexOf("adag_js=1") === -1) {
      document.cookie = "adag_js=1; path=/; max-age=31536000; samesite=lax";
    }
  } catch (err) {
    /* cookie 不可用：退回服务端轮询 */
  }
  try {
    if (!window.matchMedia("(min-width: 761px)").matches) return;
    if (window.localStorage.getItem("a_da_sidebar_collapsed") !== "1") return;
    var shell = document.querySelector(".ada-shell");
    if (shell) shell.classList.add("is-sidebar-collapsed");
  } catch (err) {
    /* 隐私模式/存储不可用：保持默认展开即可 */
  }
})();
