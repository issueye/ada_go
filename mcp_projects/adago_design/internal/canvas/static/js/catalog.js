/* adago-design 画布 · 侧栏设计体系目录：清单、详情、添加预览卡。
 * 经典脚本按 index.html 顺序加载，全局作用域共享（与 inspect.js 同一契约）；
 * 各文件顶层语句的执行顺序与拆分前完全一致，请勿调换加载顺序。 */
"use strict";
// ---------------- 侧栏：设计体系目录 ----------------
async function loadCatalog() {
  try {
    const data = await api("/api/catalog/systems");
    const list = $("sb-list");
    list.innerHTML = "";
    for (const sys of data.systems || []) {
      const card = document.createElement("div");
      card.className = "sys-card";
      card.innerHTML =
        `<div class="sys-card__name">${esc(sys.name)}</div>` +
        `<div class="sys-card__vendor">${esc(sys.vendor)} · ${esc(sys.family)}</div>` +
        `<div class="sys-card__chips">${(sys.keywords || []).map((k) => `<span class="chip">${esc(k)}</span>`).join("")}` +
        (sys.default_artboard ? `<span class="chip chip--ab">${icon("fit", 12)} ${esc(sys.default_artboard)}</span>` : "") +
        `</div>`;
      card.addEventListener("click", () => showSystemDetail(sys.id));
      list.appendChild(card);
    }
  } catch (e) { toast("目录加载失败: " + e.message, true); }
}

async function showSystemDetail(id) {
  try {
    const sys = await api("/api/catalog/systems/" + encodeURIComponent(id));
    $("sb-list").classList.add("hidden");
    const box = $("sb-detail");
    box.classList.remove("hidden");
    const body = $("sb-detail-body");
    const colors = Object.entries((sys.light && sys.light.colors) || {}).slice(0, 24);
    const levels = Object.entries((sys.light && sys.light.type && sys.light.type.levels) || {});
    const spacing = Object.entries((sys.light && sys.light.spacing) || {});
    const radius = Object.entries((sys.light && sys.light.radius) || {});
    const comps = (sys.components || []);
    body.innerHTML = `
      <div class="sys-detail__title">${esc(sys.name)}</div>
      <div class="sys-detail__vendor">${esc(sys.vendor)}</div>
      <div class="sys-detail__summary">${esc(sys.style && sys.style.summary || sys.description || "")}</div>
      <div class="kv sys-detail__meta">
        <span class="chip">${icon("fit", 12)} ${esc(sys.default_artboard || "-")}</span>
        <span class="chip" title="字体授权构成">${icon("settings", 12)} ${esc(sys.font_license || "-")}</span>
      </div>
      <div class="theme-seg sys-detail__seg">
        <button data-th="light" class="${S.catalogTheme === "light" ? "active" : ""}">浅色</button>
        <button data-th="dark" class="${S.catalogTheme === "dark" ? "active" : ""}">深色</button>
      </div>
      <div class="sec"><div class="sec__title">颜色</div><div class="swatches">
        ${colors.map(([k, v]) => {
          const val = esc(v.value || v);
          // 色值本身是数据：只作为色点注入。名字与色值用正文墨色落在纸上，
          // 而不是压在色块上 —— 白色系色块上压白字必然不可读（旧实现的白字在 bg 上 1:1）。
          return `<div class="swatch-row"><i class="swatch-dot" style="background:${val}"></i>` +
            `<span class="swatch-name" title="${esc(k)}">${esc(k)}</span>` +
            `<span class="swatch-val">${val}</span></div>`;
        }).join("")}
      </div></div>
      <div class="sec"><div class="sec__title">字型</div>
        ${levels.map(([k, v]) => {
          const trueSize = v.size || v.Size || 14;
          const weight = v.weight || v.Weight || 400;
          // 样例按 22px 封顶（浮窗只有 280 宽），显示的真实字号不受影响
          return `<div class="type-row"><span class="type-row__sample" style="font-size:${Math.min(trueSize, 22)}px;font-weight:${weight}">${esc(k)}</span><small>${trueSize}px</small></div>`;
        }).join("")}
      </div></div>
      <div class="sec"><div class="sec__title">间距 / 圆角</div><div class="kv">
        ${spacing.map(([k, v]) => `<span class="chip">${esc(k)} ${v}</span>`).join("")}
        ${radius.map(([k, v]) => `<span class="chip">R ${esc(k)} ${v}</span>`).join("")}
      </div></div>
      <div class="sec"><div class="sec__title">组件（点击添加预览）</div><div class="comp-list">
        ${comps.map((c) => `<div class="comp-item"><span class="comp-item__name">${esc(c.name)}</span><span class="comp-item__id">${esc(c.id)}</span><button data-comp="${esc(c.id)}">${icon("plus", 14)}</button></div>`).join("")}
      </div></div>
      <div class="sec"><div class="sec__title">整页预览</div>
        <button id="sb-compose-all" class="sb-wide">${icon("grid", 14)} 添加全部核心组件（一张卡）</button>
      </div>
      <div class="sec"><div class="sec__title">使用守则</div><ul class="guideline">
        ${(sys.guideline || []).map((g) => `<li>${esc(g)}</li>`).join("")}
      </ul></div>`;
    body.querySelectorAll(".theme-seg button").forEach((b) => b.addEventListener("click", () => {
      S.catalogTheme = b.dataset.th;
      showSystemDetail(id);
    }));
    body.querySelectorAll("[data-comp]").forEach((b) => b.addEventListener("click", () => addPreview(id, [b.dataset.comp])));
    const all = body.querySelector("#sb-compose-all");
    if (all) all.addEventListener("click", () => addPreview(id, comps.slice(0, 8).map((c) => c.id), true));
  } catch (e) {
    toast("体系详情加载失败: " + e.message, true);
    backToList();
  }
}

function backToList() {
  $("sb-detail").classList.add("hidden");
  $("sb-list").classList.remove("hidden");
}

async function addPreview(systemID, components, wholePage) {
  if (!S.project) { toast("先在左侧项目树里选一个项目（或先新建），再添加预览", true); return; }
  try {
    toast("生成预览中…");
    const data = await api("/api/catalog/preview", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        project: S.project, system: systemID, components, theme: S.catalogTheme,
        title: wholePage ? systemID + " 组件总览" : "",
      }),
    });
    if (data.node) { S.nodes.set(data.node.id, data.node); addNodeEl(data.node); selectNode(data.node.id); }
    reloadProjects().catch(() => { /* 计数刷新失败不影响主流程 */ });
    toast("已添加到画布");
  } catch (e) { toast("添加失败: " + e.message, true); }
}
