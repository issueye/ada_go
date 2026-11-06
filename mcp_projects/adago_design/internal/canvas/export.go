package canvas

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// ExportBoard 把画布节点导出为一张独立 HTML 板（节点按坐标绝对定位，
// 内容用沙箱 iframe 还原）。返回完整 HTML 文档字符串。
func ExportBoard(nodes []Node, title string) string {
	if title == "" {
		title = "adago-design 画布导出"
	}
	if len(nodes) == 0 {
		return boardShell(title, "<div class='empty'>画布为空。</div>", 800, 400)
	}
	var b strings.Builder
	minX, minY, maxX, maxY := bounds(nodes)
	pad := 40.0
	for _, n := range nodes {
		meta := n.System + " · " + n.Kind
		if n.Artboard != "" {
			meta += " · " + n.Artboard
		}
		b.WriteString(fmt.Sprintf(
			`<div class="frame" style="left:%dpx;top:%dpx;width:%dpx;height:%dpx">`+
				`<div class="cap"><span class="cap-title">%s</span><span class="cap-meta">%s</span></div>`+
				`<iframe sandbox="allow-same-origin" srcdoc="%s" scrolling="auto"></iframe></div>`+"\n",
			int(n.X-minX+pad), int(n.Y-minY+pad), n.Width, n.Height,
			html.EscapeString(n.Title), html.EscapeString(meta), html.EscapeString(n.HTML)))
	}
	w := int(maxX - minX + pad*2)
	h := int(maxY - minY + pad*2 + 56)
	return boardShell(title, b.String(), w, h)
}

func bounds(nodes []Node) (minX, minY, maxX, maxY float64) {
	minX, minY = 1e18, 1e18
	for _, n := range nodes {
		if n.X < minX {
			minX = n.X
		}
		if n.Y < minY {
			minY = n.Y
		}
		if r := n.X + float64(n.Width); r > maxX {
			maxX = r
		}
		if b := n.Y + float64(n.Height); b > maxY {
			maxY = b
		}
	}
	return
}

func boardShell(title, body string, w, h int) string {
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + html.EscapeString(title) + `</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{background:#0f1014;color:#e8e9ee;font-family:"Segoe UI",system-ui,"PingFang SC","Microsoft YaHei",sans-serif;padding:20px}
header{display:flex;align-items:baseline;gap:12px;margin-bottom:16px}
h1{font-size:18px;font-weight:700}
meta-info{color:#8a8fa0}
.meta{color:#8a8fa0;font-size:12px}
.board{position:relative;background-image:radial-gradient(#2a2b36 1px,transparent 1px);background-size:28px 28px;border:1px solid #2a2b36;border-radius:12px;min-width:` +
		fmt.Sprint(w) + `px;min-height:` + fmt.Sprint(h) + `px}
.frame{position:absolute;background:#16171d;border:1px solid #2a2b36;border-radius:10px;overflow:hidden;display:flex;flex-direction:column;box-shadow:0 8px 30px rgba(0,0,0,.45)}
.cap{display:flex;align-items:center;gap:8px;height:26px;padding:0 10px;border-bottom:1px solid #2a2b36;font-size:11px;flex:none}
.cap-title{font-weight:600}
.cap-meta{margin-left:auto;color:#8a8fa0}
.frame iframe{width:100%;flex:1;border:none;background:#fff}
.empty{color:#8a8fa0;padding:40px;text-align:center}
</style>
</head>
<body>
<header><h1>` + html.EscapeString(title) + `</h1><span class="meta">adago-design · ` + time.Now().Format("2006-01-02 15:04") + `</span></header>
<div class="board">
` + body + `</div>
</body>
</html>
`
}
