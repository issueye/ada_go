/* adago-design 画布 · 入口模块：弹层胶水、添加便签、启动引导（首屏加载、2s 轮询兜底、SSE 订阅）。
 *
 * index.html 只加载这一个模块；其余模块由这里的 import 图求值。
 * inspect.js 用副作用导入，保证它的 adago:frame-load 监听先于首个 iframe 创建。 */
"use strict";

import { S, $, tokenColor, tokenPx } from "./state.js";
import { focusables, closeAsk, submitAsk, uiConfirm, uiAsk, toast, isAskOpen, esc } from "./ui.js";
import { api } from "./net.js";
import { emit, EVT_THEME_CHANGE } from "./bus.js";
import { projectName, exitSolo, loadProjects } from "./projects.js";
import { isProjectModalOpen, closeProjectModal, closeTreeMenu } from "./panels.js";
import { removeNodeEl, addNodeEl, selectNode, showEmptyHint } from "./nodes.js";
import { scheduleEdges } from "./edges.js";
import {
  applyTransform, closeCanvasMenus, centerView, fitView, loadCanvasPresets,
} from "./view.js";
import { pollNodes } from "./sync.js";
import { setPanelCollapsed } from "./dock.js";
import { loadCatalog } from "./catalog.js";
import { taskLoop } from "./capture.js";
import { INS } from "../inspect.js";

// ---- 询问/确认弹层：点外关闭、Esc 关闭、Tab 陷阱、焦点归还 ----
document.querySelectorAll("[data-ask-close]").forEach((el) => {
  el.addEventListener("click", () => closeAsk(null));
});
$("ask-cancel").addEventListener("click", () => closeAsk(null));
$("ask-ok").addEventListener("click", submitAsk);
$("ask-input").addEventListener("keydown", (e) => {
  // Enter 提交、Shift+Enter 换行；输入法组合中不拦（IME 必拦，UED §19.4）
  if (e.key !== "Enter" || e.shiftKey || e.isComposing) return;
  e.preventDefault();
  submitAsk();
});
$("ask-modal").addEventListener("keydown", (e) => {
  if (e.key !== "Tab") return;
  const list = focusables($("ask-modal"));
  if (!list.length) return;
  const first = list[0], last = list[list.length - 1];
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault(); last.focus();
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault(); first.focus();
  }
});

// Esc：先关「更多」菜单，再关弹层，不再落到「清空选择 / 退出检查模式」上
window.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  if (isAskOpen()) { closeAsk(null); e.preventDefault(); e.stopPropagation(); return; }
  if (isProjectModalOpen()) { closeProjectModal(); e.preventDefault(); e.stopPropagation(); return; }
  const cvMenu = $("cv-menu"), ratioMenu = $("ratio-menu");
  if ((cvMenu && !cvMenu.classList.contains("hidden")) || (ratioMenu && !ratioMenu.classList.contains("hidden"))) {
    closeCanvasMenus();
    e.preventDefault();
    e.stopPropagation();
    return;
  }
  const menu = $("tree-menu");
  if (menu && !menu.classList.contains("hidden")) {
    closeTreeMenu();
    e.preventDefault();
    e.stopPropagation();
    return;
  }
  // 检查模式里还留着 A/B 配对时，Esc 先归检查模式（清 B → 清 A），别抢
  const inspecting = INS.on && (INS.anchor || INS.target);
  if (S.solo && !inspecting) {
    exitSolo();
    e.preventDefault();
    e.stopImmediatePropagation();
  }
}, true);

$("btn-theme").addEventListener("click", () => {
  const html = document.documentElement;
  const next = html.dataset.theme === "dark" ? "light" : "dark";
  html.dataset.theme = next;
  try { localStorage.setItem("adago.theme", next); } catch (_) { /* 隐私模式忽略 */ }
  // 标注层/截图都从令牌取色，换主题后要让检查模式重挂一次（inspect.js 监听）
  emit(EVT_THEME_CHANGE, { theme: next });
  toast(next === "dark" ? "已切换到墨纸（深色）" : "已切换到纸面（浅色）");
});
$("btn-clear").addEventListener("click", async () => {
  if (!S.project) { toast("还没有项目", true); return; }
  const ok = await uiConfirm({
    title: "清空当前画布",
    note: "将清空项目「" + projectName(S.project) + "」下的全部设计稿与连线。" +
      "Agent 侧可用 canvas_restore 找回最近一次清空的内容。",
    okText: "清空",
    danger: true,
  });
  if (!ok) return;
  try {
    await api("/api/nodes/clear", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ project: S.project }),
    });
    for (const id of [...S.nodes.keys()]) removeNodeEl(id);
    S.edges.clear();
    scheduleEdges("*");
    toast("画布已清空");
  } catch (e) { toast("清空失败: " + e.message, true); }
});
$("btn-add-note").addEventListener("click", async () => {
  if (!S.project) { toast("先在左侧项目树里选一个项目（或先新建），再添加便签", true); return; }
  const text = await uiAsk({
    title: "添加便签",
    note: "便签是画布上的自由注释卡，用来记目标、待办或验收口径。",
    placeholder: "备注内容…",
    rows: 4,
    okText: "添加",
  });
  if (text == null) return;
  if (!text.trim()) { toast("便签内容为空", true); return; }
  // 便签是独立 srcdoc 文档，拿不到父页面变量：创建时把当前令牌值内联进去
  const noteBg = tokenColor("--ui-surface", "transparent");
  const noteInk = tokenColor("--ui-ink", "currentColor");
  const noteLine = tokenColor("--ui-line-strong", "currentColor");
  const noteFont = tokenColor("--ui-font", "sans-serif");
  // 尺寸/字号同样从令牌读：srcdoc 里不能用 var()，只能内联计算结果
  const notePad = tokenPx("--ui-space-8", 16);
  const noteText = tokenPx("--ui-text-md", 13);
  const noteLh = tokenPx("--ui-lh-relaxed", 1.7);
  const noteMinH = tokenPx("--ui-note-min-h", 120);
  const noteRuleW = tokenPx("--ui-focus-w", 2);
  const html = `<!DOCTYPE html><html lang="zh-CN" data-note="1"><head><meta charset="utf-8"><style>
    *{box-sizing:border-box}
    body{margin:0;font-family:${noteFont};
      background:${noteBg};color:${noteInk};padding:${notePad}px;font-size:${noteText}px;
      line-height:${noteLh};min-height:${noteMinH}px;white-space:pre-wrap;
      border-left:${noteRuleW}px solid ${noteLine}}
  </style></head>
    <body>${esc(text)}</body></html>`;
  try {
    const data = await api("/api/nodes", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ project: S.project, title: "便签", kind: "note", html, width: 220, height: 140 }),
    });
    S.nodes.set(data.node.id, data.node);
    addNodeEl(data.node);
    selectNode(data.node.id);
  } catch (e) { toast("添加失败: " + e.message, true); }
});

// ---------------- 启动 ----------------
applyTransform();
try {
  // 两块浮窗的收起状态分开记，互不牵连
  setPanelCollapsed("project", localStorage.getItem("adago.projCollapsed") === "1");
  setPanelCollapsed("system", localStorage.getItem("adago.sysCollapsed") === "1");
} catch (_) { /* 忽略 */ }
loadCanvasPresets();
loadProjects().then(() => {
  if (!S.projects.length) showEmptyHint();
  pollNodes({ full: true }).then(() => {
    if (S.nodes.size === 0) showEmptyHint();
    if (S.canvas) centerView(true);
    else fitView();
  });
});
setInterval(pollNodes, 2000);
// 服务端 SSE 推送：任何变更 150ms 合帧后立刻拉一次（列表已剥 HTML，代价很小）；
// 2s 轮询保留作降级兜底（SSE 断线 EventSource 会自动重连）。
try {
  const es = new EventSource("/api/events");
  let sseTimer = 0;
  es.addEventListener("change", () => {
    clearTimeout(sseTimer);
    sseTimer = setTimeout(() => { pollNodes(); }, 150);
  });
} catch (_) { /* SSE 不可用时仍走 2s 轮询兜底 */ }
loadCatalog();
taskLoop();

