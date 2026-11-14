/* adago-design 画布 · 模块间事件总线（ES module）
 *
 * 仍然走 window CustomEvent：跨模块通知保持「先派发、后订阅也能收到下一次」的松耦合，
 * 但事件名收敛为常量，避免字符串散落各处。 */
"use strict";

export const EVT_FRAME_LOAD = "adago:frame-load";
export const EVT_THEME_CHANGE = "adago:theme-change";

export function emit(name, detail) {
  window.dispatchEvent(new CustomEvent(name, { detail: detail }));
}

export function on(name, fn) {
  window.addEventListener(name, fn);
}
