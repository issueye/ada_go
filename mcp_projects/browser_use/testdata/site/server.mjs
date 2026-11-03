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
