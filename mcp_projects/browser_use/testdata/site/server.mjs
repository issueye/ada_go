import http from "node:http";

const page = (title, body) => `<!doctype html>
<html><head><meta charset="utf-8"><title>${title}</title></head>
<body style="font-family: sans-serif; max-width: 640px; margin: 40px auto">
${body}
</body></html>`;

const home = page("Fixture Home", `
  <h1>Fixture Home</h1>
  <p>This is a local page for browser-use tests.</p>
  <p><a id="form-link" href="/form">Open the form</a></p>
  <p><a id="slow-link" href="/slow">A slow page</a></p>
  <p><a id="popup-link" href="/popup" target="_blank">Open a popup</a></p>
  <p><a id="download-link" href="/download">Download a file</a></p>
`);

const form = page("Fixture Form", `
  <h1>Fixture Form</h1>
  <form action="/done" method="get">
    <p><label for="q">Search</label><br>
      <input id="q" name="q" type="text" placeholder="Type a word"></p>
    <p><label for="color">Color</label><br>
      <select id="color" name="color">
        <option value="red">Red</option>
        <option value="green">Green</option>
        <option value="blue">Blue</option>
      </select></p>
    <p><button id="submit" type="submit">Submit</button></p>
  </form>
`);

const done = (q) => page("Fixture Done", `
  <h1>Done</h1>
  <p id="result">You typed: <strong>${escapeHtml(q || "")}</strong></p>
  <p><a href="/">Back home</a></p>
`);

const popup = page("Fixture Popup", `
  <h1>Popup</h1>
  <p id="popup">This page was opened in a new tab.</p>
`);

const slow = page("Fixture Slow", `
  <h1>Slow</h1>
  <div id="later">Waiting…</div>
  <script>
    setTimeout(() => {
      const el = document.getElementById("later");
      el.textContent = "Ready now";
      el.setAttribute("data-ready", "1");
    }, 600);
  </script>
`);

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// /long: 3200px of sections plus a fixed top bar — exercises full-page capture
// (fixed/sticky must appear in the first tile only).
const long = `<!doctype html>
<html><head><meta charset="utf-8"><title>Fixture Long</title></head>
<body style="font-family: sans-serif; margin: 0">
  <div id="topbar" style="position:fixed; top:0; left:0; right:0; height:48px; background:#1e293b; color:#fff; padding:12px 20px; box-sizing:border-box">Fixed top bar</div>
  <div style="height:60px"></div>
  <section id="s1" style="height:800px; background:#fee2e2; padding:24px"><h1>Section one</h1></section>
  <section id="s2" style="height:800px; background:#dcfce7; padding:24px"><h1>Section two</h1></section>
  <section id="s3" style="height:800px; background:#dbeafe; padding:24px"><h1>Section three</h1></section>
  <section id="s4" style="height:800px; background:#fef9c3; padding:24px"><h1>Section four</h1></section>
</body></html>`;

// /drag: HTML5 DnD source + drop target plus a mousemove-driven list reorder —
// exercises both drag event sequences.
const drag = `<!doctype html>
<html><head><meta charset="utf-8"><title>Fixture Drag</title></head>
<body style="font-family: sans-serif; max-width: 640px; margin: 40px auto">
  <h1>Drag lab</h1>
  <div id="card" draggable="true" style="width:220px; padding:16px; border:2px solid #2563eb; background:#dbeafe">Draggable card</div>
  <div id="bin" style="width:220px; padding:24px; margin-top:24px; border:2px dashed #64748b; background:#f1f5f9">Drop bin</div>
  <p id="dnd-result">dnd: none</p>
  <ol id="list" style="margin-top:32px">
    <li id="item-a" style="padding:8px; background:#fee2e2">Item A</li>
    <li id="item-b" style="padding:8px; background:#dcfce7">Item B</li>
  </ol>
  <p id="ptr-result">ptr: none</p>
  <script>
    const bin = document.getElementById("bin");
    const card = document.getElementById("card");
    card.addEventListener("dragstart", (e) => e.dataTransfer.setData("text/plain", "card"));
    bin.addEventListener("dragover", (e) => e.preventDefault());
    bin.addEventListener("drop", (e) => {
      e.preventDefault();
      document.getElementById("dnd-result").textContent = "dnd: dropped " + e.dataTransfer.getData("text/plain");
    });
    // mousemove-driven reorder: press A, move over B, release.
    const a = document.getElementById("item-a");
    const b = document.getElementById("item-b");
    let dragging = false;
    a.addEventListener("mousedown", () => { dragging = true; });
    window.addEventListener("mousemove", () => {
      if (dragging) {
        const list = document.getElementById("list");
        if (list.lastElementChild !== a) { list.insertBefore(b, a); }
      }
    });
    window.addEventListener("mouseup", () => {
      if (dragging) {
        dragging = false;
        const list = document.getElementById("list");
        document.getElementById("ptr-result").textContent = "ptr: order=" + [...list.children].map((li) => li.id).join(",");
      }
    });
  </script>
</body></html>`;

const server = http.createServer((req, res) => {
  const url = new URL(req.url, "http://127.0.0.1");
  res.setHeader("Content-Type", "text/html; charset=utf-8");
  if (url.pathname === "/") return res.end(home);
  if (url.pathname === "/form") return res.end(form);
  if (url.pathname === "/done") return res.end(done(url.searchParams.get("q")));
  if (url.pathname === "/frames") {
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    return res.end(`<!doctype html><meta charset="utf-8"><title>Frames</title>
      <h1>Frames</h1>
      <iframe id="same" src="/form" width="500" height="220"></iframe>
      <iframe id="cross" src="http://127.0.0.1:8767/form" width="500" height="220"></iframe>`);
  }
  if (url.pathname === "/auto") {
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    return res.end(`<!doctype html><meta charset="utf-8"><title>Auto fetch</title>
      <h1>Auto fetch</h1>
      <script>
        fetch("/api/schedule?auto=1").then(r => r.json()).then(j => {
          document.title = "Auto " + j.summary.hours;
        });
      </script>`);
  }
  if (url.pathname === "/api/schedule") {
    res.setHeader("Content-Type", "application/json; charset=utf-8");
    return res.end(JSON.stringify({
      range: { start: "2026-11-01", end: "2026-11-30" },
      summary: { hours: 99.5, shifts: 17.6, attendance: 19, duty: 19 },
      days: [
        { date: "2026-11-01", weekday: "周日", shift: "午班 11:30-18:00" },
        { date: "2026-11-02", weekday: "周一", shift: "休息" },
      ],
    }));
  }
  if (url.pathname === "/slow") return res.end(slow);
  if (url.pathname === "/popup") return res.end(popup);
  if (url.pathname === "/long") {
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    return res.end(long);
  }
  if (url.pathname === "/drag") {
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    return res.end(drag);
  }
  if (url.pathname === "/nested") {
    // /frames itself embeds an iframe: nested relay lists f1 and f1.1 here.
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    return res.end(`<!doctype html><meta charset="utf-8"><title>Nested</title>
      <h1>Nested frames</h1>
      <iframe id="inner" src="/frames" width="600" height="300"></iframe>`);
  }
  if (url.pathname === "/download") {
    res.setHeader("Content-Type", "text/plain; charset=utf-8");
    res.setHeader("Content-Disposition", "attachment; filename=\"hello.txt\"");
    return res.end("hello from browser-use\n");
  }
  res.statusCode = 404;
  res.end(page("Not Found", "<h1>Not found</h1>"));
});

const port = Number(process.argv[2] || 8765);
server.listen(port, "127.0.0.1", () => {
  console.log(`fixture listening on http://127.0.0.1:${port}`);
});
