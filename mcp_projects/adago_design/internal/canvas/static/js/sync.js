/* adago-design 画布 · 同步：节点轮询（列表已剥 HTML，按 html_rev 复用正文）、项目低频刷新。 */
"use strict";

import { S } from "./state.js";
import { api } from "./net.js";
import { addNodeEl, updateNodeEl, removeNodeEl } from "./nodes.js";
import { scheduleEdges } from "./edges.js";
import { scheduleLabels, renderProjectRows, isProjectModalOpen } from "./panels.js";
import { renderProjectTree } from "./projects.js";
import { syncCanvasFromScope } from "./view.js";
// ---------------- 同步轮询 ----------------
// 节点每 2s 增量合并；项目/画布版式低频刷新（约 30s）：
// Agent 侧 project_create / project_canvas 不会触发 UI 事件，只能靠轮询兜底。
let pollTicks = 0;
let projectsSig = "";

// projectsSig 由 sync 独占持有：其他模块只能通过 setter 更新，避免跨模块写外部绑定。
export function setProjectsSig(sig) { projectsSig = sig; }

function projectsSignature(list) {
  return JSON.stringify((list || []).map((p) => [
    p.id, p.name, p.note, p.node_count, p.frame_count, p.updated_at,
    p.canvas ? p.canvas.width : 0, p.canvas ? p.canvas.height : 0, p.canvas ? p.canvas.ratio : "",
  ]));
}

// 列表接口默认剥掉 html 大字段（每张预览文档 10KB+）：轮询只传元数据；
// 新节点 / 正文变更的节点再按需 GET /api/nodes/{id} 取正文，
// 首屏与切项目用 ?include=html 一次拿全。html_rev 相同 ⇒ 正文没变，
// 直接复用本地缓存 —— 挪个位置不再触发一次 10KB 拉取。
async function pollNodes(opts) {
  if (S.drag) return;
  const full = !!(opts && opts.full);
  const q0 = S.project ? "?project=" + encodeURIComponent(S.project) : "";
  const q = q0 + (full ? (q0 ? "&" : "?") + "include=html" : "");
  try {
    const [data, ed] = await Promise.all([
      api("/api/nodes" + q),
      api("/api/edges" + q0).catch(() => ({ edges: [] })),
    ]);
    const list = data.nodes || [];
    const need = [];
    for (const n of list) {
      const local = S.nodes.get(n.id);
      if (n.html === undefined && local && local.html !== undefined &&
          local.html_rev === n.html_rev) {
        n.html = local.html; // 正文没变，复用本地缓存
      } else if (n.html === undefined && n.kind !== "frame") {
        need.push(n); // frame 没有正文；其余等下面按需取
      }
    }
    if (need.length) {
      await Promise.all(need.map(async (n) => {
        try { n.html = (await api("/api/nodes/" + n.id)).node.html || ""; }
        catch (_) { n.html = ""; }
      }));
    }
    const seen = new Set();
    for (const n of list) {
      seen.add(n.id);
      const local = S.nodes.get(n.id);
      if (!local) {
        S.nodes.set(n.id, n);
        addNodeEl(n);
      } else if (local.updated_at !== n.updated_at || local.html !== n.html) {
        S.nodes.set(n.id, n);
        updateNodeEl(n, local);
      }
    }
    for (const id of [...S.nodes.keys()]) if (!seen.has(id)) removeNodeEl(id);
    S.edges.clear();
    for (const e of ed.edges || []) S.edges.set(e.id, e);
    scheduleEdges("*");
    scheduleLabels();
    if (++pollTicks % 15 === 0) await refreshProjectsQuietly();
  } catch (_) { /* 状态条已处理 */ }
}

// 项目 / 画布版式的低频刷新：只有内容真变了才重建树，避免每轮刷新打断滚动与展开状态。
async function refreshProjectsQuietly() {
  try {
    const data = await api("/api/projects");
    const next = data.projects || [];
    const sig = projectsSignature(next);
    if (sig === projectsSig) return;
    projectsSig = sig;
    S.projects = next;
    syncCanvasFromScope();
    renderProjectTree();
    if (isProjectModalOpen()) renderProjectRows();
  } catch (_) { /* 网络异常时保持现状 */ }
}

export { pollNodes, refreshProjectsQuietly, projectsSignature };
