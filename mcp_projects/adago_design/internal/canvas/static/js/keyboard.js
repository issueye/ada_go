/* adago-design 画布 · 全局快捷键与临时平移模式（空格键）。
 *
 * 空格按住 = 临时进入平移模式：光标变抓手，任意位置（含卡片、iframe、浮窗外的画布区域）
 * 按左键拖动都是平移画布，滚轮也可缩放；松开空格恢复原状。
 * 这是设计稿工具（Figma/即时设计/蓝湖）的通用手势，与「空白处拖动」「滚轮缩放」并存。 */
"use strict";

import { S, $, setSpacePan } from "./state.js";
import { api } from "./net.js";
import { isAskOpen } from "./ui.js";
import { removeNode } from "./nodes.js";
import { fitView, centerView, zoomAt } from "./view.js";

// 空格键：按住 = 平移模式。以下情形不抢（空格保持它原本的语义）：
//   · 输入框/文本域/可编辑区里打字；
//   · 焦点在左侧浮窗、工具条、弹层、检查器、空态卡上（按钮的「空格激活」、
//     树行的「空格展开/收起」都归控件自己）。空格平移只对「画布区域」生效 ——
//     否则在树行上按空格会顺带进入搬动画布模式，按钮也没法用键盘触发。
function typingInText(e) {
  const t = e.target;
  return t instanceof HTMLInputElement || t instanceof HTMLTextAreaElement ||
    (t && t.isContentEditable);
}

// 画布界面外壳（非画布区域）：空格在这里归控件，不切平移模式。
const CHROME_ROOTS = ["left-dock", "toolbar", "inspector", "solo-bar"];
const CHROME_CLASSES = ["modal", "empty-hint"];

function inCanvasChrome(t) {
  if (!t || !t.closest) return false;
  for (const id of CHROME_ROOTS) {
    const root = $(id);
    if (root && root.contains(t)) return true;
  }
  for (const cls of CHROME_CLASSES) {
    if (t.closest("." + cls)) return true;
  }
  return false;
}

window.addEventListener("keydown", (e) => {
  if (e.code === "Space" || e.key === " ") {
    if (typingInText(e) || inCanvasChrome(e.target) || isAskOpen()) return;
    if (e.repeat) { e.preventDefault(); return; }  // 长按不连发
    e.preventDefault();                            // 阻止页面滚动 / 空格误触按钮
    setSpacePan(true);
    return;
  }
  if (typingInText(e)) return;
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

// 松开空格退出平移模式；窗口失焦（切走/弹窗）也要退出，否则回来时还卡在抓手手势里。
window.addEventListener("keyup", (e) => {
  if (e.code === "Space" || e.key === " ") setSpacePan(false);
});
window.addEventListener("blur", () => setSpacePan(false));
