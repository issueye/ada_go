/* adago-design 画布 · 全局快捷键：Del/F/0/Esc/缩放/Ctrl+Z·Y（撤销重做走服务端历史）。
 * 经典脚本按 index.html 顺序加载，全局作用域共享（与 inspect.js 同一契约）；
 * 各文件顶层语句的执行顺序与拆分前完全一致，请勿调换加载顺序。 */
"use strict";
window.addEventListener("keydown", (e) => {
  if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
  if (e.key === "Delete" || e.key === "Backspace") {
    if (document.body.classList.contains("mode-inspect")) return;
    if (S.selected) { e.preventDefault(); removeNode(S.selected); }
  }
  else if (e.key === "f" || e.key === "F") fitView();
  else if (e.key === "0") centerView(true);
  else if (e.key === "Escape") {
    if (S.selected) { const el = S.els.get(S.selected); if (el) el.classList.remove("selected"); S.selected = null; $("inspector").classList.add("hidden"); }
  }
  else if (e.key === "=" || e.key === "+") zoomAt(innerWidth / 2, innerHeight / 2, 1.15);
  else if (e.key === "-") zoomAt(innerWidth / 2, innerHeight / 2, 1 / 1.15);
  else if ((e.ctrlKey || e.metaKey) && !e.altKey) {
    // 撤销/重做走服务端历史（SSE 推送会让所有打开的画布页同步刷新）；
    // 焦点在输入框时已提前 return，不与文本编辑的原生撤销冲突
    if (e.key === "z" || e.key === "Z") {
      e.preventDefault();
      if (e.shiftKey) api("/api/redo", { method: "POST" }).catch(() => {});
      else api("/api/undo", { method: "POST" }).catch(() => {});
    }
    else if (e.key === "y" || e.key === "Y") {
      e.preventDefault();
      api("/api/redo", { method: "POST" }).catch(() => {});
    }
  }
});
