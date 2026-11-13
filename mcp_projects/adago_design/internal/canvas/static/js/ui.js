/* adago-design 画布 · 交互反馈：询问/确认弹层、toast、tooltip、转义（ES module）
 *
 * 原生 prompt()/confirm() 由浏览器绘制，字体、圆角、语气全部脱离体系；
 * 这里统一走体系弹层，并补齐弹层四要素：点外关闭、Esc 关闭、Tab 陷阱、关闭后焦点归还。 */
"use strict";

import { $ } from "./state.js";

let askState = null;

export function focusables(root) {
  return [...root.querySelectorAll(
    'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')]
    .filter((el) => !el.disabled && el.offsetParent !== null);
}

export function closeAsk(result) {
  const box = $("ask-modal");
  if (!box || box.classList.contains("hidden")) return;
  box.classList.add("hidden");
  const st = askState;
  askState = null;
  $("ask-input").value = "";
  $("ask-error").textContent = "";
  if (st && st.restore && typeof st.restore.focus === "function") st.restore.focus();
  if (st && st.resolve) st.resolve(result);
}

// uiAsk({title, note, defaultValue, placeholder, okText, multiline}) → Promise<string|null>
export function uiAsk(opt) {
  const o = opt || {};
  return new Promise((resolve) => {
    const box = $("ask-modal");
    const restore = document.activeElement;
    $("ask-title").textContent = o.title || "请输入";
    $("ask-note").textContent = o.note || "";
    $("ask-note").classList.toggle("hidden", !o.note);
    $("ask-error").textContent = "";
    const wrap = $("ask-field-wrap");
    wrap.classList.toggle("hidden", o.field === false);
    const input = $("ask-input");
    input.value = o.defaultValue || "";
    input.placeholder = o.placeholder || "";
    input.rows = o.rows || 3;
    // 单行询问（如重命名）不该占三行高：默认 min-height 只给多行输入
    input.classList.toggle("ask-input--single", (o.rows || 3) <= 1);
    $("ask-cancel").textContent = o.cancelText || "取消";
    $("ask-ok").textContent = o.okText || "确定";
    $("ask-ok").className = "btn btn--primary";
    askState = { resolve, restore, mode: "ask", opt: o };
    box.classList.remove("hidden");
    box.dataset.kind = "ask";
    setTimeout(() => { if (o.field !== false) { input.focus(); input.select(); } else $("ask-ok").focus(); }, 0);
  });
}

// uiConfirm({title, note, okText, danger}) → Promise<boolean>
export function uiConfirm(opt) {
  const o = opt || {};
  return new Promise((resolve) => {
    const box = $("ask-modal");
    const restore = document.activeElement;
    $("ask-title").textContent = o.title || "请确认";
    $("ask-note").textContent = o.note || "";
    $("ask-note").classList.toggle("hidden", !o.note);
    $("ask-error").textContent = "";
    $("ask-field-wrap").classList.add("hidden");
    $("ask-cancel").textContent = o.cancelText || "取消";
    $("ask-ok").textContent = o.okText || "确定";
    $("ask-ok").className = "btn " + (o.danger ? "btn--danger" : "btn--primary");
    askState = { resolve, restore, mode: "confirm", opt: o };
    box.classList.remove("hidden");
    box.dataset.kind = "confirm";
    setTimeout(() => $("ask-ok").focus(), 0);
  });
}

export function submitAsk() {
  const st = askState;
  if (!st) return;
  if (st.mode === "confirm") { closeAsk(true); return; }
  const val = $("ask-input").value;
  if (st.opt && typeof st.opt.validate === "function") {
    const err = st.opt.validate(val);
    if (err) {
      $("ask-error").textContent = err;
      $("ask-input").focus();
      return;
    }
  }
  closeAsk(val);
}

export function isAskOpen() {
  const box = $("ask-modal");
  return !!box && !box.classList.contains("hidden");
}

// ---------------- 工具 ----------------
export function toast(msg, isErr) {
  const t = $("toast");
  t.textContent = msg;
  t.classList.toggle("err", !!isErr);
  t.classList.toggle("hidden", false);
  // 语义二分：普通反馈 role=status（polite），错误 role=alert（immediate）
  t.setAttribute("role", isErr ? "alert" : "status");
  t.setAttribute("aria-live", isErr ? "assertive" : "polite");
  // 重放入场动画：连续相同文案也要有反馈（移除类再强制重排）
  t.classList.remove("toast-in");
  void t.offsetWidth;
  t.classList.add("toast-in");
  clearTimeout(toast._h);
  // 时长对齐 UED §19.5.3：普通 2.4s / 错误 4.2s
  toast._h = setTimeout(() => t.classList.add("hidden"), isErr ? 4200 : 2400);
}

// 更新图标按钮的 tooltip 与无障碍名称。
export function setTip(id, text) {
  const el = $(id);
  if (!el) return;
  el.dataset.tip = text;
  el.setAttribute("aria-label", text);
}

export function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g,
    (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
