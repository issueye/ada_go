/* adago-design 画布 · 截图：foreignObject 光栅化、任务循环（长轮询领取/回传）。
 * 经典脚本按 index.html 顺序加载，全局作用域共享（与 inspect.js 同一契约）；
 * 各文件顶层语句的执行顺序与拆分前完全一致，请勿调换加载顺序。 */
"use strict";
// ---------------- 截图任务（foreignObject 光栅化） ----------------
async function rasterize(html, width, height, scale) {
  const iframe = document.createElement("iframe");
  iframe.style.cssText = `position:fixed;left:-10000px;top:0;width:${width}px;height:${height}px;border:0;visibility:hidden`;
  iframe.setAttribute("sandbox", "allow-same-origin");
  iframe.srcdoc = html;
  document.body.appendChild(iframe);
  try {
    await new Promise((res) => iframe.addEventListener("load", res, { once: true }));
    // 布局就绪：rAF 优先，后台标签页被节流时用定时器兜底
    await Promise.race([
      new Promise((res) => requestAnimationFrame(() => requestAnimationFrame(res))),
      new Promise((res) => setTimeout(res, 300)),
    ]);
    const doc = iframe.contentDocument;
    const realH = Math.max(height, doc.body ? doc.body.scrollHeight : height);
    const xhtml = new XMLSerializer().serializeToString(doc.documentElement);
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${realH}">` +
      `<foreignObject width="100%" height="100%">${xhtml}</foreignObject></svg>`;
    const img = new Image();
    await new Promise((res, rej) => {
      img.onload = res;
      img.onerror = () => rej(new Error("SVG 光栅化失败"));
      img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg);
    });
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(width * scale));
    canvas.height = Math.max(1, Math.round(realH * scale));
    const ctx = canvas.getContext("2d");
    // 底色取文档 body 的实际背景色，避免深色预览在内容未铺满处露白
    let bg = tokenColor("--ui-elevated", "transparent");
    try {
      const c = doc.defaultView.getComputedStyle(doc.body).backgroundColor;
      if (c && c !== "rgba(0, 0, 0, 0)") bg = c;
    } catch (_) { /* 取不到就用节点面的令牌色 */ }
    ctx.fillStyle = bg;
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
    return canvas.toDataURL("image/png").split(",")[1];
  } finally { iframe.remove(); }
}

async function runCaptureTask(task) {
  try {
    let png;
    if (task.node_id) {
      const n = S.nodes.get(task.node_id);
      if (!n) throw new Error("节点不在画布上: " + task.node_id);
      png = await rasterize(n.html, n.width, n.height, task.scale || 1);
    } else {
      if (S.nodes.size === 0) throw new Error("画布为空，没有可截图的内容");
      let minX = 1e9, minY = 1e9, maxX = -1e9, maxY = -1e9;
      for (const n of S.nodes.values()) {
        if (n.kind === "frame") continue; // 分组框是背板，不参与截图边界
        minX = Math.min(minX, n.x); minY = Math.min(minY, n.y);
        maxX = Math.max(maxX, n.x + n.width); maxY = Math.max(maxY, n.y + n.height);
      }
      const pad = 24, scale = task.scale || 1;
      const canvas = document.createElement("canvas");
      canvas.width = Math.round((maxX - minX + pad * 2) * scale);
      canvas.height = Math.round((maxY - minY + pad * 2) * scale);
      const ctx = canvas.getContext("2d");
      // 画布底色与连线/标签色全部读令牌：浅色主题导出浅色，深色导出深色，不再混色。
      // 兜底用 transparent 而不是写死色值：令牌在 head 已加载，取不到说明样式没加载，
      // 此时宁可留空也不要输出一套与界面不符的颜色。
      const pageBg = tokenColor("--ui-canvas", "transparent");
      ctx.fillStyle = pageBg;
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      // 先画连线（与画布一致：连线在节点下层）
      const toX = (v) => (v - minX + pad) * scale, toY = (v) => (v - minY + pad) * scale;
      const edgeInk = tokenColor("--ui-ink", "transparent");
      const edgeStroke = tokenColor("--ui-brand", "transparent");
      const labelBg = tokenColor("--ui-elevated", "transparent");
      ctx.strokeStyle = edgeStroke;
      ctx.fillStyle = edgeInk;
      ctx.lineWidth = 2 * scale;
      ctx.font = `${Math.round(11 * scale)}px ${tokenColor("--ui-font", "sans-serif")}`;
      for (const e of S.edges.values()) {
        const a = S.nodes.get(e.from), b = S.nodes.get(e.to);
        if (!a || !b || a.kind === "frame" || b.kind === "frame") continue;
        const x1 = toX(a.x + a.width / 2), y1 = toY(a.y + a.height);
        const x2 = toX(b.x + b.width / 2), y2 = toY(b.y);
        const dy = Math.max(48 * scale, Math.abs(y2 - y1) / 2);
        ctx.beginPath();
        ctx.moveTo(x1, y1);
        ctx.bezierCurveTo(x1, y1 + dy, x2, y2 - dy, x2, y2);
        ctx.stroke();
        if (e.label) {
          const mx = (x1 + x2) / 2, my = (y1 + y2) / 2;
          const w = ctx.measureText(e.label).width + 8 * scale;
          ctx.fillStyle = labelBg;
          ctx.fillRect(mx - w / 2, my - 10 * scale, w, 16 * scale);
          ctx.fillStyle = edgeInk;
          ctx.fillText(e.label, mx - w / 2 + 4 * scale, my + 3 * scale);
          ctx.fillStyle = edgeInk;
        }
      }
      // 再叠节点（跳过分组框）
      for (const n of S.nodes.values()) {
        if (n.kind === "frame") continue;
        const part = await rasterize(n.html, n.width, n.height, scale);
        const img = new Image();
        await new Promise((res, rej) => { img.onload = res; img.onerror = rej; img.src = "data:image/png;base64," + part; });
        ctx.drawImage(img, Math.round((n.x - minX + pad) * scale), Math.round((n.y - minY + pad) * scale));
      }
      png = canvas.toDataURL("image/png").split(",")[1];
    }
    await api(`/api/tasks/${task.id}/result`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ png_base64: png }),
    });
  } catch (e) {
    try {
      await api(`/api/tasks/${task.id}/result`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ png_base64: "", error: e.message }),
      });
    } catch (_) { /* ignore */ }
  }
}

// 任务循环：纯 await 链 + 长轮询，不用定时器串链，
// 避免后台标签页被浏览器节流后无法领取截图任务。
async function taskLoop() {
  for (;;) {
    try {
      const data = await api("/api/tasks");
      if (data.task) await runCaptureTask(data.task);
    } catch (_) {
      await new Promise((r) => setTimeout(r, 2000)); // 网络异常退避
    }
  }
}

// ---------------- 工具条动作 ----------------
$("btn-zoom-in").addEventListener("click", () => zoomAt(innerWidth / 2, innerHeight / 2, 1.2));
$("btn-zoom-out").addEventListener("click", () => zoomAt(innerWidth / 2, innerHeight / 2, 1 / 1.2));
$("btn-fit").addEventListener("click", fitView);

// ---- 画布：选择 / 比例 / 居中 ----
$("cv-select").addEventListener("click", (e) => {
  e.stopPropagation();
  toggleCanvasMenu("cv-menu");
});
$("cv-ratio").addEventListener("click", (e) => {
  e.stopPropagation();
  if (!S.project) { toast("画布是项目的属性：先在「全部项目」下拉里选一个项目", true); return; }
  toggleCanvasMenu("ratio-menu");
});
$("cv-center").addEventListener("click", () => centerView(true));

$("cv-menu").addEventListener("click", async (e) => {
  const btn = e.target.closest("[data-act]");
  if (!btn) return;
  const act = btn.dataset.act;
  closeCanvasMenus();
  if (act === "scope") await switchProject(btn.dataset.id || SCOPE_ALL);
  else if (act === "new" || act === "manage") openProjectModal();
});

$("ratio-menu").addEventListener("click", async (e) => {
  const btn = e.target.closest("[data-ratio]");
  if (!btn) return;
  const kind = btn.dataset.ratio;
  closeCanvasMenus();
  if (!S.project) { toast("先在「全部项目」下拉里选一个项目", true); return; }
  if (kind === "free") {
    await setCanvasConfig({ free: true }, "已恢复自由画布");
    return;
  }
  if (kind === "custom") {
    const cur = S.canvas ? S.canvas.width + "x" + S.canvas.height : "1440x900";
    // 用同一个 ask 弹层做校验：出错留在弹层里（Inline 错误），不关闭再弹第二条 toast
    const parseSize = (raw) => raw.trim().match(/^(\d+)\s*[x×*]\s*(\d+)$/i);
    const raw = await uiAsk({
      title: "自定义画布尺寸",
      note: "宽高需在 " + S.minSide + "–" + S.maxSide + " 之间，格式「宽 x 高」，如 1440x900。",
      defaultValue: cur,
      placeholder: cur,
      rows: 1,
      okText: "应用",
      validate: (v) => {
        const m = parseSize(v || "");
        if (!m) return "格式应为「宽 x 高」，如 1440x900";
        const w = Number(m[1]), h = Number(m[2]);
        if (w < S.minSide || h < S.minSide || w > S.maxSide || h > S.maxSide) {
          return "宽高需在 " + S.minSide + "–" + S.maxSide + " 之间";
        }
        return null;
      },
    });
    if (raw == null) return;
    if (!parseSize(raw)) return;
    const w = Number(parseSize(raw)[1]), h = Number(parseSize(raw)[2]);
    await setCanvasConfig({ width: w, height: h }, "画布尺寸已更新");
    return;
  }
  await setCanvasConfig({ ratio: kind }, "画布比例已更新");
});
$("sb-back").addEventListener("click", backToList);
$("insp-close").addEventListener("click", () => $("inspector").classList.add("hidden"));
