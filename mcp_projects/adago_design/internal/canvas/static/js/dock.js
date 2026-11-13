/* adago-design 画布 · 左侧双浮窗收起、项目树交互（单击定位/双击聚焦/更多菜单）。 */
"use strict";

import { S, $ } from "./state.js";
import {
  switchProject, SCOPE_ALL, focusNode, enterSolo, exitSolo, arrangeByProject,
  reloadProjects, renderProjectTree,
} from "./projects.js";
import {
  openProjectModal, toggleTreeMenu, closeTreeMenu, createProjectFromModal,
  closeProjectModal,
} from "./panels.js";
import { pollNodes } from "./sync.js";
import { closeCanvasMenus } from "./view.js";
// ---- 左侧双浮窗：项目 / 设计体系各自独立收起 ----
// 收起只隐藏 body、保留标题条，因此不需要额外的「展开把手」，也不会出现收起来拉不回来的问题。
function setPanelCollapsed(which, collapsed) {
  const panel = $(which === "project" ? "project-panel" : "system-panel");
  const btn = $(which === "project" ? "proj-collapse" : "sys-collapse");
  if (!panel) return;
  panel.classList.toggle("is-collapsed", !!collapsed);
  if (btn) {
    btn.setAttribute("aria-expanded", collapsed ? "false" : "true");
    btn.title = (collapsed ? "展开" : "收起") + (which === "project" ? "项目" : "设计体系") + "面板";
    btn.setAttribute("aria-label", btn.title);
  }
  try {
    localStorage.setItem(which === "project" ? "adago.projCollapsed" : "adago.sysCollapsed", collapsed ? "1" : "0");
  } catch (_) { /* 隐私模式忽略 */ }
}

function togglePanel(which) {
  const panel = $(which === "project" ? "project-panel" : "system-panel");
  if (!panel) return;
  setPanelCollapsed(which, !panel.classList.contains("is-collapsed"));
}

$("proj-collapse").addEventListener("click", () => togglePanel("project"));
$("sys-collapse").addEventListener("click", () => togglePanel("system"));
$("solo-back").addEventListener("click", () => exitSolo());
// ---- 项目树交互 ----
$("tree-all").addEventListener("click", () => switchProject(SCOPE_ALL));
$("tree-new").addEventListener("click", openProjectModal);
$("tree-more").addEventListener("click", (e) => { e.stopPropagation(); toggleTreeMenu(); });

$("tree-menu").addEventListener("click", async (e) => {
  const btn = e.target.closest("[data-menu]");
  if (!btn) return;
  closeTreeMenu();
  const act = btn.dataset.menu;
  if (act === "manage") openProjectModal();
  else if (act === "arrange") await arrangeByProject(false);
  else if (act === "refresh") { await reloadProjects(); await pollNodes(); }
});

$("tree-filter").addEventListener("input", (e) => {
  S.treeFilter = e.target.value;
  renderProjectTree();
});

// 单击定位 / 双击单独展示：单击延后 220ms，双击到来就取消，
// 否则两次 click 会先触发两次 switchProject，和双击的切作用域打架。
let treeChildClickTimer = 0;
$("tree-rows").addEventListener("click", (e) => {
  const child = e.target.closest(".tree-child");
  if (child) {
    const project = child.dataset.project, node = child.dataset.node;
    clearTimeout(treeChildClickTimer);
    treeChildClickTimer = setTimeout(() => focusNode(project, node), 220);
    return;
  }
  const row = e.target.closest(".tree-row");
  if (!row) return;
  if (e.target.closest('[data-act="toggle"]')) { toggleTreeRow(row.dataset.id); return; }
  switchProject(row.dataset.id);
});

$("tree-rows").addEventListener("dblclick", (e) => {
  const child = e.target.closest(".tree-child[data-node]");
  if (!child) return;
  clearTimeout(treeChildClickTimer);
  e.preventDefault();
  e.stopPropagation();
  enterSolo(child.dataset.node, child.dataset.project);
});

// 键盘：树行 Enter 切作用域、空格展开/收起；子项 Enter 定位（双击语义保留给鼠标）
$("tree-rows").addEventListener("keydown", (e) => {
  const child = e.target.closest(".tree-child[data-node]");
  if (child && (e.key === "Enter" || e.key === " ")) {
    e.preventDefault();
    focusNode(child.dataset.project, child.dataset.node);
    return;
  }
  const row = e.target.closest(".tree-row");
  if (!row) return;
  if (e.key === " " || e.key === "ArrowRight") {
    e.preventDefault();
    if (!S.expanded.has(row.dataset.id)) toggleTreeRow(row.dataset.id);
    else if (e.key === " ") toggleTreeRow(row.dataset.id);
    return;
  }
  if (e.key === "ArrowLeft") {
    e.preventDefault();
    if (S.expanded.has(row.dataset.id)) toggleTreeRow(row.dataset.id);
    return;
  }
  if (e.key === "Enter") {
    e.preventDefault();
    switchProject(row.dataset.id);
  }
});

// 点空白关掉「更多」菜单
document.addEventListener("click", (e) => {
  if (!e.target.closest(".tree__head")) closeTreeMenu();
  if (!e.target.closest(".tb-canvas")) closeCanvasMenus();
});

// ⇧N：新建项目（与设计稿工具的习惯一致）
window.addEventListener("keydown", (e) => {
  if (!e.shiftKey || (e.key !== "N" && e.key !== "n")) return;
  if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
  e.preventDefault();
  openProjectModal();
});

$("proj-create").addEventListener("click", createProjectFromModal);
$("proj-name").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); createProjectFromModal(); }
});
document.querySelectorAll("[data-proj-close]").forEach((el) => {
  el.addEventListener("click", closeProjectModal);
});

export { setPanelCollapsed, togglePanel };
