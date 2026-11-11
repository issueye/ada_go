/* adago-design 画布 · 项目作用域：全部/单项目切换、项目树渲染、单画布聚焦（solo）、按项目重排。
 * 经典脚本按 index.html 顺序加载，全局作用域共享（与 inspect.js 同一契约）；
 * 各文件顶层语句的执行顺序与拆分前完全一致，请勿调换加载顺序。 */
"use strict";
// ---------------- 项目（画布作用域） ----------------
// S.project === "" 表示「全部」：一次看所有项目的稿子，按项目分行并在行首打标签。
const SCOPE_ALL = "";

function projectName(id) {
  const p = S.projects.find((x) => x.id === id);
  return p ? p.name : "（未知项目）";
}

function isAllScope() { return S.project === SCOPE_ALL; }

function totalNodes() {
  return S.projects.reduce((sum, p) => sum + (p.node_count || 0), 0);
}

// ---- 项目树（参照设计稿工具的分组列表） ----
function renderProjectTree() {
  const rows = $("tree-rows");
  if (!rows) return;
  const total = $("tree-total");
  if (total) total.textContent = String(totalNodes());
  const allBtn = $("tree-all");
  if (allBtn) allBtn.classList.toggle("is-active", isAllScope());

  const q = (S.treeFilter || "").trim().toLowerCase();
  const list = q ? S.projects.filter((p) => p.name.toLowerCase().includes(q)) : S.projects;
  if (!list.length) {
    rows.innerHTML = S.projects.length
      ? '<div class="tree-empty">没有匹配的项目</div>'
      : '<div class="tree-empty">还没有项目。<br>点上方「新建项目」创建 —— 设计稿必须归属项目。</div>';
    return;
  }
  rows.innerHTML = list.map((p) => {
    const active = p.id === S.project;
    const open = S.expanded.has(p.id);
    return `<div class="tree-row${active ? " is-active" : ""}${open ? " is-open" : ""}"
        data-id="${esc(p.id)}" role="treeitem" tabindex="0"
        aria-selected="${active}" aria-expanded="${open}"
        aria-label="${esc(p.name)}，${p.node_count} 个设计稿">
      <button class="tree-row__caret" data-act="toggle" tabindex="-1"
              title="${open ? "收起" : "展开"}" aria-label="${open ? "收起" : "展开"}项目">
        <svg class="ds-icon ds-icon--12" aria-hidden="true"><use href="#ds-ic-chevron-right"/></svg>
      </button>
      <span class="tree-row__icon">${icon(open ? "folder-open" : "folder", 14)}</span>
      <span class="tree-row__name" title="${esc(p.name)}">${esc(p.name)}</span>
      <span class="tree-row__count">${p.node_count}</span>
    </div>` + (open ? renderTreeChildren(p) : "");
  }).join("");
}

function renderTreeChildren(p) {
  const nodes = S.treeNodes.get(p.id);
  if (!nodes) return '<div class="tree-children"><div class="tree-child">加载中…</div></div>';
  if (!nodes.length) return '<div class="tree-children"><div class="tree-child">暂无设计稿</div></div>';
  return '<div class="tree-children" role="group">' + nodes.slice(0, 40).map((n) =>
    `<div class="tree-child${n.id === S.solo ? " is-solo" : ""}" data-node="${esc(n.id)}" data-project="${esc(p.id)}" tabindex="0"` +
      ` title="${esc(n.title || n.id)} · 单击定位 / 双击单独展示">` +
      `${icon("note", 12)}<span>${esc(n.title || n.id)}</span></div>`
  ).join("") + "</div>";
}

async function toggleTreeRow(id) {
  if (S.expanded.has(id)) { S.expanded.delete(id); renderProjectTree(); return; }
  S.expanded.add(id);
  if (!S.treeNodes.has(id)) {
    renderProjectTree(); // 先出「加载中…」
    try {
      const data = await api("/api/nodes?project=" + encodeURIComponent(id));
      S.treeNodes.set(id, (data.nodes || []).map((n) => ({ id: n.id, title: n.title })));
    } catch (_) {
      S.treeNodes.set(id, []);
    }
  }
  renderProjectTree();
}

async function focusNode(projectID, nodeID) {
  if (S.project !== projectID) await switchProject(projectID);
  const n = S.nodes.get(nodeID);
  if (!n) return;
  selectNode(nodeID);
  const scale = Math.min(1, Math.max(0.12, S.scale));
  S.scale = scale;
  S.tx = innerWidth / 2 - (n.x + n.width / 2) * scale;
  S.ty = innerHeight / 2 - (n.y + n.height / 2) * scale;
  applyTransform();
  scheduleEdges("*");
  scheduleLabels();
}

// ---------------- 单画布聚焦 ----------------
// 双击项目树里的设计稿（或双击画布卡片）→ 只展示这一张稿。
// 关键：不改数据、不改作用域，只隐藏其它节点的 DOM 并重新适配视口，
// 所以退出后拖动过的坐标、连线、检查模式状态都原样恢复。
function applySoloVisibility() {
  for (const [id, el] of S.els) el.classList.toggle("solo-hidden", !!S.solo && id !== S.solo);
  edgeSvg.style.display = S.solo ? "none" : "";
  // 关系层不能跟着隐藏：检查模式里 A↔B 的间距/对齐标注（含同一张卡内）
  // 就画在 #rel-layer；隐藏它会出现「选了两块却量不出距离」。
  relLayer.style.display = "";
  document.body.classList.toggle("mode-solo", !!S.solo);
  // 只切 class，不重建树 DOM（聚焦可能正由树上的 dblclick 触发）
  document.querySelectorAll(".tree-child[data-node]").forEach((el) => {
    el.classList.toggle("is-solo", !!S.solo && el.dataset.node === S.solo);
  });
  scheduleLabels();
}

function renderSoloBar() {
  const bar = $("solo-bar");
  if (!bar) return;
  const n = S.solo && S.nodes.get(S.solo);
  if (!n) { bar.classList.add("hidden"); return; }
  $("solo-title").textContent = n.title || n.id;
  $("solo-project").textContent = projectName(n.project_id);
  bar.classList.remove("hidden");
}

async function enterSolo(nodeID, projectID) {
  // 单项目视图里双击其它项目下的稿：先切作用域；「全部」视图本来就都在画布上
  if (projectID && S.project && S.project !== projectID) await switchProject(projectID);
  if (!S.nodes.has(nodeID)) await pollNodes();
  const n = S.nodes.get(nodeID);
  if (!n) { toast("这张设计稿已不在当前画布", true); return; }
  // 跨卡片的 A↔B 关系在只剩一张稿时没有意义，先清掉，避免残影画到 (0,0)；
  // 检查器面板也一并收起，否则会留着上一次的对比数据（hover 时检查器会自己再打开）
  if (typeof clearPair === "function") clearPair();
  if (S.selected) { const el = S.els.get(S.selected); if (el) el.classList.remove("selected"); S.selected = null; }
  $("inspector").classList.add("hidden");
  S.solo = nodeID;
  applySoloVisibility();
  renderSoloBar();
  fitView();
  toast("已单独展示「" + (n.title || n.id) + "」· Esc 返回画布");
}

function exitSolo(refit) {
  if (!S.solo) return;
  S.solo = null;
  applySoloVisibility();
  renderSoloBar();
  scheduleEdges("*");
  if (refit !== false) fitView();
}

// 切换作用域 = 换一块画布：先清掉当前渲染的节点，再按新作用域重新拉取
function resetCanvas() {
  if (S.solo) exitSolo(false);
  for (const id of [...S.nodes.keys()]) removeNodeEl(id);
  S.edges = new Map();
  scheduleEdges("*");
  scheduleLabels();
  S.selected = null;
  $("inspector").classList.add("hidden");
}

async function loadProjects(preferred) {
  try {
    const data = await api("/api/projects");
    S.projects = data.projects || [];
    S.treeNodes.clear();
    S.expanded.clear();
  } catch (e) {
    S.projects = [];
    toast("项目列表加载失败: " + e.message, true);
  }
  projectsSig = projectsSignature(S.projects);
  const remembered = preferred !== undefined
    ? preferred
    : (localStorage.getItem("adago.scope") || "all");
  if (remembered && remembered !== "all" && S.projects.some((p) => p.id === remembered)) {
    S.project = remembered;
  } else {
    S.project = SCOPE_ALL;
  }
  localStorage.setItem("adago.scope", S.project || "all");
  syncCanvasFromScope();
  renderProjectTree();
  return S.project;
}

async function switchProject(id) {
  const scope = id || SCOPE_ALL;
  if (scope === S.project) { closeTreeMenu(); closeCanvasMenus(); return; }
  S.project = scope;
  localStorage.setItem("adago.scope", scope || "all");
  closeTreeMenu();
  closeCanvasMenus();
  resetCanvas();
  syncCanvasFromScope();
  renderProjectTree();
  await pollNodes({ full: true }); // 切作用域 = 全新节点集，一次 include=html 拿全
  if (isAllScope()) await ensureGroupLayout();
  if (S.nodes.size === 0) showEmptyHint();
  else hideEmptyHint();
  // 有版式就居中版式（而不是把所有节点塞进视野）；自由画布仍按内容适配
  if (S.canvas) centerView(true);
  else fitView();
}

// 「全部」视图里，各项目原本都从 (80,80) 起排，卡片会完全重叠。
// 检测到项目包围盒相交就按项目分行重排一次（一次性写坐标，之后仍可自由拖动）。
async function ensureGroupLayout() {
  const boxes = new Map();
  for (const n of S.nodes.values()) {
    const pid = n.project_id || "";
    const b = boxes.get(pid) || { x0: Infinity, y0: Infinity, x1: -Infinity, y1: -Infinity };
    b.x0 = Math.min(b.x0, n.x);
    b.y0 = Math.min(b.y0, n.y);
    b.x1 = Math.max(b.x1, n.x + n.width);
    b.y1 = Math.max(b.y1, n.y + n.height);
    boxes.set(pid, b);
  }
  const arr = [...boxes.values()];
  let overlap = false;
  for (let i = 0; i < arr.length && !overlap; i++) {
    for (let j = i + 1; j < arr.length; j++) {
      const a = arr[i], b = arr[j];
      if (a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1) { overlap = true; break; }
    }
  }
  if (!overlap) return;
  try {
    await api("/api/arrange", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ group_by: "project" }),
    });
    await pollNodes();
  } catch (_) { /* 排列失败不阻塞浏览 */ }
}

async function arrangeByProject(silent) {
  try {
    const data = await api("/api/arrange", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ group_by: "project" }),
    });
    await pollNodes();
    fitView();
    if (!silent) toast("已按项目重新排列（移动 " + (data.moved || 0) + " 个节点）");
  } catch (e) {
    if (!silent) toast("排列失败: " + e.message, true);
  }
}

async function reloadProjects() {
  const data = await api("/api/projects");
  S.projects = data.projects || [];
  projectsSig = projectsSignature(S.projects);
  syncCanvasFromScope();
  renderProjectTree();
  if (isProjectModalOpen()) renderProjectRows();
}
