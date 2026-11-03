//go:build windows

package api

import "net/http"

// index is the page a human opens to watch the agent: the desktop, the
// overlay it draws, and the switch that holds it.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}

const indexHTML = `<!doctype html>
<html lang="zh-CN">
<meta charset="utf-8">
<title>Computer Use · 蒙板</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  :root { color-scheme: dark; }
  body { margin: 0; font: 13px/1.5 "Segoe UI", system-ui, sans-serif; background: #0b0f17; color: #e2e8f0; }
  header { display: flex; align-items: center; gap: 14px; padding: 14px 20px; border-bottom: 1px solid #1e2635; }
  header h1 { font-size: 15px; margin: 0; letter-spacing: .3px; font-weight: 650; }
  .pill { padding: 3px 10px; border-radius: 999px; font-size: 12px; border: 1px solid #2a3547; color: #93c5fd; }
  .pill.paused { color: #fca5a5; border-color: #7f1d1d; background: #1f0b0b; }
  .spacer { flex: 1; }
  button { background: #16202f; color: #e2e8f0; border: 1px solid #263349; border-radius: 8px; padding: 6px 12px; font: inherit; cursor: pointer; }
  button:hover { background: #1c2940; }
  main { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; padding: 16px 20px 30px; }
  @media (max-width: 1100px) { main { grid-template-columns: 1fr; } }
  section { background: #0e1420; border: 1px solid #1c2637; border-radius: 12px; padding: 12px; min-width: 0; }
  section h2 { font-size: 12px; text-transform: uppercase; letter-spacing: .8px; color: #7f8ea8; margin: 0 0 10px; }
  img { width: 100%; min-height: 160px; border-radius: 8px; display: block; background: #0a0e15; object-fit: contain; }
  dl { display: grid; grid-template-columns: 120px 1fr; gap: 4px 10px; margin: 0; }
  dt { color: #7f8ea8; }
  dd { margin: 0; font-family: Consolas, monospace; overflow-wrap: anywhere; }
  .hint { color: #7f8ea8; font-size: 12px; margin-top: 10px; }
  code { background: #16202f; padding: 1px 5px; border-radius: 4px; }
  textarea { width: 100%; box-sizing: border-box; min-height: 110px; resize: vertical; background: #0a0e15; color: #dbe6f5; border: 1px solid #1d2839; border-radius: 8px; padding: 10px; font: 12px/1.5 Consolas, monospace; }
  pre { background: #0a0e15; border: 1px solid #1d2839; border-radius: 8px; padding: 10px; margin: 10px 0 0; max-height: 220px; overflow: auto; font: 12px/1.5 Consolas, monospace; white-space: pre-wrap; }
  .row { display: flex; align-items: center; gap: 10px; margin-top: 10px; }
  ul.macros { list-style: none; margin: 0; padding: 0; }
  ul.macros li { display: flex; align-items: center; gap: 10px; padding: 5px 0; border-bottom: 1px solid #151d2a; }
  ul.macros li span.name { font-family: Consolas, monospace; flex: 1; }
  ul.macros li span.meta { color: #7f8ea8; font-size: 12px; }
</style>
<header>
  <h1>Computer Use · 蒙板</h1>
  <span id="status" class="pill">…</span>
  <span id="uptime" class="hint"></span>
  <span class="spacer"></span>
  <button id="pause">暂停 Pause</button>
  <button id="clear">清除标注 Clear</button>
</header>
<main>
  <section>
    <h2>屏幕 · 截图中不含蒙板</h2>
    <img id="shot" alt="screenshot">
  </section>
  <section>
    <h2>蒙板 · 用户所见</h2>
    <img id="maskp" alt="mask preview">
  </section>
  <section>
    <h2>状态</h2>
    <dl>
      <dt>虚拟屏幕</dt><dd id="virtual">-</dd>
      <dt>显示器</dt><dd id="displays">-</dd>
      <dt>指针</dt><dd id="cursor">-</dd>
      <dt>前台窗口</dt><dd id="fg">-</dd>
      <dt>标注区域</dt><dd id="regions">-</dd>
      <dt>暂停</dt><dd id="paused">-</dd>
    </dl>
    <p class="hint">坐标一律为物理像素：<code>screen = region + image / scale</code></p>
  </section>
  <section>
    <h2>脚本 / 宏</h2>
    <textarea id="src" spellcheck="false">// 在页面里直接跑：cu 就是全部动作
const c = cu.cursor();
cu.mask({
  regions: [{ rect: { x: c.x - 150, y: c.y - 80, w: 300, h: 160 }, label: "脚本标注 · from script", index: 1 }],
  markers: [{ at: c, label: "指针" }],
  hud: { detail: "脚本运行中" }
});
cu.wait(600);
cu.clearMask();
({ cursor: c, windows: cu.windows().length });</textarea>
    <div class="row"><button id="run">运行脚本 Run</button><span id="scriptmsg" class="hint"></span></div>
    <pre id="out">——</pre>
    <h2 style="margin-top:16px">宏 Macros</h2>
    <ul id="macros" class="macros"><li><span class="meta">还没有宏</span></li></ul>
  </section>
  <section>
    <h2>接口</h2>
    <dl>
      <dt>GET /v1/state</dt><dd>状态与显示器</dd>
      <dt>GET /v1/screenshot</dt><dd>截图（base64 + 坐标映射）</dd>
      <dt>GET /v1/screenshot.png</dt><dd>截图（二进制）</dd>
      <dt>POST /v1/actions</dt><dd>click / move / drag / scroll / type / key / focus</dd>
      <dt>POST /v1/mask</dt><dd>区域、坐标标签、聚光、网格</dd>
      <dt>POST /v1/pause</dt><dd>暂停注入</dd>
      <dt>POST /v1/script</dt><dd>运行 JS（cu.* 动作）</dd>
      <dt>GET /v1/macros</dt><dd>宏列表；<code>/run</code> 回放，<code>/script</code> 导出 JS</dd>
    </dl>
    <p class="hint">MCP：<code>computer-use mcp</code>，stdin/stdout 上的 JSON-RPC。</p>
  </section>
</main>
<script>
var token = new URLSearchParams(location.search).get("token") || "";
var q = token ? "?token=" + encodeURIComponent(token) : "";
function set(id, v) { document.getElementById(id).textContent = v; }
function bust(u) { return u + (u.indexOf("?") >= 0 ? "&" : "?") + "_t=" + Date.now(); }
function displayName(d) { return (d.Name || d.name) + ((d.Primary || d.primary) ? " (主屏)" : ""); }
async function refresh() {
  try {
    var r = await fetch("/v1/state" + q, { cache: "no-store" });
    var s = await r.json();
    var st = document.getElementById("status");
    st.textContent = s.paused ? "已暂停 PAUSED" : "运行中 RUNNING";
    st.className = "pill" + (s.paused ? " paused" : "");
    set("uptime", "v" + (s.version || ""));
    set("virtual", s.virtual ? ("x" + s.virtual.x + "  y" + s.virtual.y + "  " + s.virtual.w + "×" + s.virtual.h) : "-");
    set("displays", (s.displays || []).map(displayName).join("，") || "-");
    set("cursor", s.cursor ? (s.cursor.x + ", " + s.cursor.y) : "-");
    set("fg", s.foreground ? (s.foreground.title + "  #" + s.foreground.handle) : "-");
    set("regions", ((s.mask && s.mask.regions) ? s.mask.regions.length : 0) + " 个，暗度 " + (s.mask ? s.mask.dim : 0));
    set("paused", s.paused ? "是" : "否");
    document.getElementById("shot").src = bust("/v1/screenshot.png?format=jpeg&quality=72&max_width=1200&audit=0" + (token ? "&token=" + encodeURIComponent(token) : ""));
    document.getElementById("maskp").src = bust("/v1/mask.png" + q);
  } catch (e) {
    set("status", "连接失败");
    document.getElementById("status").className = "pill paused";
  }
}
document.getElementById("pause").onclick = async function () {
  var st = await (await fetch("/v1/state" + q)).json();
  await fetch((st.paused ? "/v1/resume" : "/v1/pause") + q, { method: "POST" });
  refresh();
};
document.getElementById("clear").onclick = async function () {
  await fetch("/v1/mask/clear" + q, { method: "POST" });
  refresh();
};
async function refreshMacros() {
  const list = document.getElementById("macros");
  try {
    const r = await fetch("/v1/macros" + q, { cache: "no-store" });
    const s = await r.json();
    const ms = s.macros || [];
    if (!ms.length) { list.innerHTML = "<li><span class=\"meta\">还没有宏 · no macros yet</span></li>"; return; }
    list.innerHTML = "";
    for (const m of ms) {
      const li = document.createElement("li");
      const name = document.createElement("span");
      name.className = "name";
      name.textContent = m.name;
      const meta = document.createElement("span");
      meta.className = "meta";
      meta.textContent = m.steps + " 步";
      const play = document.createElement("button");
      play.textContent = "▶ 回放";
      play.onclick = async () => {
        play.disabled = true;
        play.textContent = "回放中…";
        try {
          const res = await fetch("/v1/macros/" + encodeURIComponent(m.name) + "/run" + q, {
            method: "POST", headers: { "Content-Type": "application/json" }, body: "{}"
          });
          const body = await res.json();
          set("scriptmsg", body.ok ? ("已回放 " + body.steps + " 步") : ("失败：" + (body.error || "")));
        } catch (e) { set("scriptmsg", "回放失败"); }
        play.disabled = false;
        play.textContent = "▶ 回放";
      };
      li.append(name, meta, play);
      list.append(li);
    }
  } catch (e) {
    list.innerHTML = "<li><span class=\"meta\">读取失败 · cannot read macros</span></li>";
  }
}
document.getElementById("run").onclick = async () => {
  const src = document.getElementById("src").value;
  set("scriptmsg", "运行中…");
  try {
    const res = await fetch("/v1/script" + q, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ source: src, name: "dashboard.js" })
    });
    const body = await res.json();
    const lines = [];
    if (body.output) lines.push(...body.output);
    if (body.result !== undefined) lines.push("→ " + JSON.stringify(body.result));
    if (!body.ok) lines.push("错误 · error: " + body.error);
    lines.push("· " + body.duration_ms + " ms");
    document.getElementById("out").textContent = lines.join("\n");
    set("scriptmsg", body.ok ? "完成" : "失败");
    if (body.macro) refreshMacros();
  } catch (e) {
    document.getElementById("out").textContent = String(e);
    set("scriptmsg", "失败");
  }
};
refresh();
refreshMacros();
setInterval(refresh, 2000);
setInterval(refreshMacros, 5000);
</script>
</html>`
