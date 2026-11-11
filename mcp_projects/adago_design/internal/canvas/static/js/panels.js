/* adago-design 画布 · 分组标签（世界坐标）与项目管理弹层：新建/重命名/删除/切换。
 * 经典脚本按 index.html 顺序加载，全局作用域共享（与 inspect.js 同一契约）；
 * 各文件顶层语句的执行顺序与拆分前完全一致，请勿调换加载顺序。 */
"use strict";
// ---- 画布上的分组标签（世界坐标，跟着节点走） ----
const labelLayer = document.createElement("div");
labelLayer.id = "group-labels";
world.appendChild(labelLayer);

let labelRaf = 0;
function scheduleLabels() {
  if (labelRaf) return;
  labelRaf = requestAnimationFrame(() => { labelRaf = 0; renderGroupLabels(); });
}

function renderGroupLabels() {
  labelLayer.innerHTML = "";
  if (!isAllScope() || S.solo) return;
  const groups = new Map();
  for (const n of S.nodes.values()) {
    const pid = n.project_id || "";
    const g = groups.get(pid);
    if (!g) groups.set(pid, { minX: n.x, minY: n.y, count: 1 });
    else {
      g.minX = Math.min(g.minX, n.x);
      g.minY = Math.min(g.minY, n.y);
      g.count++;
    }
  }
  for (const [pid, g] of groups) {
    const el = document.createElement("button");
    el.className = "group-label";
    el.dataset.id = pid;
    // 世界坐标存下来，缩放时只重算 transform，不重建节点
    el.dataset.wx = String(Math.round(Math.max(8, g.minX - 152)));
    el.dataset.wy = String(Math.round(g.minY));
    el.innerHTML = `<span>${esc(projectName(pid))}</span><span class="group-label__count">${g.count}</span>`;
    el.title = "只看「" + projectName(pid) + "」";
    el.addEventListener("click", () => switchProject(pid));
    labelLayer.appendChild(el);
    applyLabelTransform(el);
  }
}

// 标签反向缩放：画布缩到 26% 时标签若跟着缩会看不清，
// 这里按 1/scale 抵消并封顶 2.4 倍，保证任何缩放下都能读。
function labelScale() {
  return Math.min(2.4, Math.max(1, 1 / S.scale));
}

function applyLabelTransform(el) {
  const k = labelScale();
  el.style.transform = `translate3d(${el.dataset.wx}px,${el.dataset.wy}px,0) scale(${k})`;
  el.style.transformOrigin = "top left";
}

function updateLabelScale() {
  if (!labelLayer) return;
  const k = labelScale();
  if (updateLabelScale._k === k) return;
  updateLabelScale._k = k;
  labelLayer.querySelectorAll(".group-label").forEach(applyLabelTransform);
}

// ---- 项目管理弹层 ----
function isProjectModalOpen() {
  const m = $("proj-modal");
  return !!m && !m.classList.contains("hidden");
}

function openProjectModal() {
  closeTreeMenu();
  closeCanvasMenus();
  if (!isProjectModalOpen()) $("proj-modal").classList.remove("hidden");
  renderProjectRows();
  const input = $("proj-name");
  if (input) setTimeout(() => input.focus(), 0);
}

function closeProjectModal() {
  const m = $("proj-modal");
  if (m) m.classList.add("hidden");
}

function fmtTime(iso) {
  if (!iso) return "-";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "-";
  const p2 = (n) => String(n).padStart(2, "0");
  return `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}`;
}

function renderProjectRows() {
  const box = $("proj-rows");
  if (!box) return;
  const count = $("proj-count");
  if (count) count.textContent = String(S.projects.length);
  if (!S.projects.length) {
    box.innerHTML = '<div class="proj-empty">还没有项目。先在上面创建 —— 设计稿必须归属项目，不能直接新增。</div>';
    return;
  }
  box.innerHTML = S.projects
    .map((p) => {
      const cur = p.id === S.project;
      const frames = p.frame_count ? ` · ${p.frame_count} 个分组框` : "";
      const canvasMeta = p.canvas ? ` · 画布 ${p.canvas.width}×${p.canvas.height}` : "";
      return `<div class="proj-row${cur ? " is-current" : ""}" data-id="${esc(p.id)}">
        <div class="proj-row__main">
          <div class="proj-row__name">${esc(p.name)}${cur ? '<span class="proj-row__cur">当前</span>' : ""}</div>
          <div class="proj-row__meta">${p.node_count} 个设计稿${frames}${canvasMeta} · 更新 ${fmtTime(p.updated_at)} · ${esc(p.id)}</div>
        </div>
        <div class="proj-row__acts">
          ${cur ? "" : '<button data-act="switch">切换</button>'}
          <button data-act="rename">重命名</button>
          <button data-act="remove" class="danger">删除</button>
        </div>
      </div>`;
    })
    .join("");
  box.querySelectorAll(".proj-row").forEach((row) => {
    const id = row.dataset.id;
    row.querySelectorAll("[data-act]").forEach((btn) => {
      btn.addEventListener("click", () => onProjectAction(btn.dataset.act, id));
    });
  });
}

async function onProjectAction(act, id) {
  const target = S.projects.find((p) => p.id === id);
  if (!target) return;
  if (act === "switch") {
    await switchProject(id);
    renderProjectRows();
    closeProjectModal();
    return;
  }
  if (act === "rename") {
    const name = await uiAsk({
      title: "重命名项目",
      note: "名称用于项目树、分组标签与导出文件名。",
      defaultValue: target.name,
      placeholder: "项目名称（最多 40 字）",
      rows: 1,
      okText: "保存",
      validate: (v) => (v.trim() ? null : "项目名不能为空"),
    });
    if (name == null) return;
    const trimmed = name.trim();
    if (!trimmed || trimmed === target.name) return;
    try {
      await api("/api/projects/" + encodeURIComponent(id), {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: trimmed }),
      });
      await reloadProjects();
      renderProjectRows();
      toast("已重命名为：" + trimmed);
    } catch (e) { toast("重命名失败: " + e.message, true); }
    return;
  }
  if (act === "remove") {
    const ok = await uiConfirm({
      title: "删除项目",
      note: "「" + target.name + "」下还有 " + (target.node_count || 0) +
        " 个设计稿。只有空项目可以删除，需要先清空该项目。",
      okText: "删除项目",
      danger: true,
    });
    if (!ok) return;
    try {
      await api("/api/projects/" + encodeURIComponent(id), { method: "DELETE" });
      if (S.project === id) {
        // 当前项目被删：回落到「全部」
        S.project = SCOPE_ALL;
        await loadProjects("all");
        resetCanvas();
        await pollNodes();
        showEmptyHint();
        fitView();
      } else {
        await reloadProjects();
      }
      renderProjectRows();
      toast("已删除项目：" + target.name);
    } catch (e) {
      toast("删除失败: " + e.message + "（项目下还有设计稿时需先清空）", true);
    }
  }
}

async function createProjectFromModal() {
  const nameInput = $("proj-name");
  const noteInput = $("proj-note");
  const name = (nameInput.value || "").trim();
  if (!name) { toast("请输入项目名称", true); nameInput.focus(); return; }
  try {
    const data = await api("/api/projects", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, note: (noteInput.value || "").trim() }),
    });
    nameInput.value = "";
    noteInput.value = "";
    await loadProjects(data.project.id);
    resetCanvas();
    await pollNodes();
    showEmptyHint();
    fitView();
    renderProjectRows();
    toast("已创建项目：" + data.project.name + "，已切换到该项目");
  } catch (e) { toast("创建失败: " + e.message, true); }
}

// ---- 项目树的「更多」菜单 ----
function openTreeMenu() {
  const menu = $("tree-menu");
  if (!menu) return;
  menu.classList.remove("hidden");
}
function closeTreeMenu() {
  const menu = $("tree-menu");
  if (menu) menu.classList.add("hidden");
}
function toggleTreeMenu() {
  const menu = $("tree-menu");
  if (!menu) return;
  menu.classList.toggle("hidden");
}
