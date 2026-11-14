package control

import "encoding/json"

// ToolSpec is a tool definition shared by HTTP and MCP.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Phase       string          `json:"phase"`
	Schema      json.RawMessage `json:"inputSchema"`
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }

// Tools are the tools the M0 build exposes.
var Tools = []ToolSpec{
	{
		Name: "status", Phase: "M0",
		Description: "App version, control state, active tab, tab count and downloads directory.",
		Schema:      schema(`{"type":"object","properties":{"include_tabs":{"type":"boolean","default":false,"description":"include the full tab list"}},"additionalProperties":false}`),
	},
	{
		Name: "tabs", Phase: "M0",
		Description: "List, create, close or select tabs.",
		Schema: schema(`{"type":"object","properties":{
			"action":{"type":"string","enum":["list","new","close","select"]},
			"url":{"type":"string"},
			"tab":{"type":"string"},
			"activate":{"type":"boolean","default":true},
			"force":{"type":"boolean","default":false}
		},"required":["action"],"additionalProperties":false}`),
	},
	{
		Name: "navigate", Phase: "M0",
		Description: "Go to a URL, or go back, forward, reload or stop.",
		Schema: schema(`{"type":"object","properties":{
			"action":{"type":"string","enum":["goto","back","forward","reload","stop"],"default":"goto"},
			"url":{"type":"string"},
			"tab":{"type":"string"},
			"wait_until":{"type":"string","enum":["load","none"],"default":"load","description":"load waits for the page load event; none returns immediately. SPA readiness needs wait for=network or wait for=selector."},
			"timeout_ms":{"type":"integer","minimum":100,"maximum":120000,"default":30000}
		},"required":["action"],"additionalProperties":false}`),
	},
	{
		Name: "snapshot", Phase: "M0",
		Description: "Semantic snapshot of the page with stable refs such as e12. diff=true returns only nodes added, changed or removed since the previous snapshot.",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"scope":{"type":"string","enum":["full","viewport"],"default":"full"},
			"filter":{"type":"string"},
			"max_chars":{"type":"integer","minimum":1000,"maximum":50000,"default":12000},
			"diff":{"type":"boolean","default":false}
		},"additionalProperties":false}`),
	},
	{
		Name: "read", Phase: "M0.5",
		Description: "Readable text of the page or of a selected element. format=links lists anchors, format=tables renders <table> elements as markdown, format=markdown returns the text plus tables as markdown (innerText flattens tables).",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"selector":{"type":"string"},
			"format":{"type":"string","enum":["text","markdown","links","tables"],"default":"text"},
			"max_chars":{"type":"integer","minimum":1000,"maximum":50000,"default":12000}
		},"additionalProperties":false}`),
	},
	{
		Name: "script", Phase: "M1.5",
		Description: "Run a JS/TS async function body with the browser API. Use for multi-step flows, loops and extraction; return a small JSON value.",
		Schema: schema(`{"type":"object","properties":{
			"source":{"type":"string"},
			"language":{"type":"string","enum":["js","ts"],"default":"js"},
			"args":{"type":"object"},
			"timeout_ms":{"type":"integer","minimum":1000,"maximum":300000,"default":120000},
			"max_output_bytes":{"type":"integer","minimum":1024,"maximum":262144,"default":32768},
			"release_on_finish":{"type":"boolean","default":true},
			"tab":{"type":"string"}
		},"required":["source"],"additionalProperties":false}`),
	},
	{
		Name: "network", Phase: "M1",
		Description: "Inspect fetch/XHR requests captured from page scripts (URL, status, timing, request/response bodies). Use it to find JSON APIs behind a UI.",
		Schema: schema(`{"type":"object","properties":{
			"action":{"type":"string","enum":["list","get","clear","wait"],"default":"list"},
			"tab":{"type":"string"},
			"url_contains":{"type":"string"},
			"method":{"type":"string"},
			"status":{"type":"integer"},
			"since":{"type":"string"},
			"limit":{"type":"integer","minimum":1,"maximum":200,"default":50},
			"include_body":{"type":"boolean","default":false},
			"max_body":{"type":"integer","minimum":0},
			"id":{"type":"string"},
			"timeout_ms":{"type":"integer","minimum":100,"maximum":120000,"default":5000}
		},"additionalProperties":false}`),
	},
	{
		Name: "console", Phase: "M2",
		Description: "Read console output captured by the runtime: console.log/info/warn/error/debug plus uncaught errors and unhandled rejections. Use wait to block until a matching line appears.",
		Schema: schema(`{"type":"object","properties":{
			"action":{"type":"string","enum":["list","get","clear","wait"],"default":"list"},
			"tab":{"type":"string"},
			"level":{"type":"string","enum":["log","info","warn","error","debug"]},
			"text_contains":{"type":"string"},
			"source":{"type":"string","enum":["console","exception","rejection"]},
			"since":{"type":"string"},
			"limit":{"type":"integer","minimum":1,"maximum":200,"default":50},
			"id":{"type":"string"},
			"timeout_ms":{"type":"integer","minimum":100,"maximum":120000,"default":5000}
		},"additionalProperties":false}`),
	},
	{
		Name: "frames", Phase: "M2",
		Description: "List direct iframes (same- or cross-origin), select one for page actions, or return to the main frame.",
		Schema: schema(`{"type":"object","properties":{
			"action":{"type":"string","enum":["list","use","main","current"],"default":"list"},
			"tab":{"type":"string"},
			"frame":{"type":"string"}
		},"additionalProperties":false}`),
	},
	{
		Name: "files", Phase: "M2",
		Description: "Upload local files into a page file input (input[type=file]) by ref, selector or text.",
		Schema: schema(`{"type":"object","properties":{
			"action":{"type":"string","enum":["upload"],"default":"upload"},
			"tab":{"type":"string"},
			"ref":{"type":"string"},
			"selector":{"type":"string"},
			"text":{"type":"string"},
			"paths":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":10}
		},"required":["paths"],"additionalProperties":false}`),
	},
	{
		Name: "cookies", Phase: "M2",
		Description: "List, set, delete, clear or import session cookies (HTTPOnly included). Use it to reuse or repair a login. Values are sensitive.",
		Schema: schema(`{"type":"object","properties":{
			"action":{"type":"string","enum":["list","set","delete","clear","import","export"],"default":"list"},
			"url":{"type":"string"},
			"name":{"type":"string"},
			"value":{"type":"string"},
			"domain":{"type":"string"},
			"path":{"type":"string"},
			"secure":{"type":"boolean"},
			"http_only":{"type":"boolean"},
			"same_site":{"type":"string","enum":["lax","strict","none"]},
			"expires":{"type":"string"},
			"cookies":{"type":"array","items":{"type":"object"}}
		},"additionalProperties":false}`),
	},
	{
		Name: "screenshot", Phase: "M1",
		Description: "PNG/JPEG screenshot of the page, optionally clipped to a ref/selector and annotated with snapshot refs. full_page=true scrolls and stitches the whole page height (fixed/sticky elements stay in the first tile only). Saved under the app data directory.",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"annotate":{"type":"boolean","default":true},
			"inline":{"type":"boolean","default":false},
			"full_page":{"type":"boolean","default":false,"description":"stitch the whole scrollable page height; not combinable with ref/selector"},
			"max_width":{"type":"integer","minimum":320,"maximum":4096,"default":1600},
			"ref":{"type":"string","description":"clip to this snapshot ref"},
			"selector":{"type":"string","description":"clip to this CSS/text/role selector"},
			"format":{"type":"string","enum":["png","jpeg"],"default":"png"},
			"quality":{"type":"integer","minimum":1,"maximum":100,"default":82,"description":"JPEG quality"}
		},"additionalProperties":false}`),
	},
	{
		Name: "scroll", Phase: "M1",
		Description: "Scroll the page up/down/left/right or to an element.",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"direction":{"type":"string","enum":["up","down","left","right"],"default":"down"},
			"amount":{"type":"number","default":600},
			"ref":{"type":"string"},
			"selector":{"type":"string"}
		},"additionalProperties":false}`),
	},
	{
		Name: "hover", Phase: "M1",
		Description: "Move the pointer over an element by ref, selector or text.",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"ref":{"type":"string"},
			"selector":{"type":"string"},
			"text":{"type":"string"}
		},"additionalProperties":false}`),
	},
	{
		Name: "eval", Phase: "M1",
		Description: "Evaluate JavaScript in the page and return its JSON result (gated by agent.allow_eval).",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"expression":{"type":"string"},
			"timeout_ms":{"type":"integer","minimum":100,"maximum":120000,"default":30000}
		},"required":["expression"],"additionalProperties":false}`),
	},
	{
		Name: "downloads", Phase: "M1",
		Description: "List finished downloads.",
		Schema:      schema(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name: "dialogs", Phase: "M1",
		Description: "List alert/confirm/prompt calls the page made (they are intercepted and non-blocking), clear the list, or set the policy for the next confirm/prompt: accept (confirm=true, prompt returns text/default) or dismiss (confirm=false, prompt returns null).",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"action":{"type":"string","enum":["list","clear","accept","dismiss"],"default":"list"},
			"text":{"type":"string","description":"prompt answer for action=accept"}
		},"additionalProperties":false}`),
	},
	{
		Name: "click", Phase: "M0",
		Description: "Click an element by ref, selector, text or point.",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"ref":{"type":"string"},
			"selector":{"type":"string"},
			"text":{"type":"string"},
			"point":{"type":"object","properties":{"x":{"type":"number"},"y":{"type":"number"}},"required":["x","y"]},
			"button":{"type":"string","enum":["left","right","middle"],"default":"left"},
			"count":{"type":"integer","enum":[1,2],"default":1},
			"modifiers":{"type":"array","items":{"type":"string","enum":["Alt","Control","Meta","Shift"]}},
			"force":{"type":"boolean","default":false},
			"reveal":{"type":"boolean","default":true,"description":"reveal an opacity:0 hover-only wrapper for the click"},
			"wait_after":{"type":"string","enum":["auto","load","none"],"default":"auto"},
			"timeout_ms":{"type":"integer","minimum":100,"maximum":120000,"default":30000}
		},"additionalProperties":false}`),
	},
	{
		Name: "type", Phase: "M0",
		Description: "Type text into a field, optionally submitting with Enter.",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"ref":{"type":"string"},
			"selector":{"type":"string"},
			"text":{"type":"string"},
			"clear":{"type":"boolean","default":true},
			"submit":{"type":"boolean","default":false},
			"slowly":{"type":"boolean","default":false},
			"wait_after":{"type":"string","enum":["auto","load","none"],"default":"auto"},
			"timeout_ms":{"type":"integer","minimum":100,"maximum":120000,"default":30000}
		},"required":["text"],"additionalProperties":false}`),
	},
	{
		Name: "select", Phase: "M0.5",
		Description: "Choose one or more options of a <select> by value or visible label.",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"ref":{"type":"string"},
			"selector":{"type":"string"},
			"values":{"type":"array","items":{"type":"string"}},
			"timeout_ms":{"type":"integer","minimum":100,"maximum":120000,"default":30000}
		},"required":["values"],"additionalProperties":false}`),
	},
	{
		Name: "press", Phase: "M0",
		Description: "Press a key or a chord such as Control+A.",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"key":{"type":"string"},
			"ref":{"type":"string"},
			"selector":{"type":"string"},
			"timeout_ms":{"type":"integer","minimum":100,"maximum":120000,"default":30000}
		},"required":["key"],"additionalProperties":false}`),
	},
	{
		Name: "wait", Phase: "M0",
		Description: "Wait for load, URL, selector, text, download, time, idle, a new tab, a control change or a page dialog.",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"for":{"type":"string","enum":["load","url","selector","text","download","time","idle","tab","control","dialog"]},
			"value":{"type":"string"},
			"state":{"type":"string","enum":["visible","attached","hidden"],"default":"visible"},
			"timeout_ms":{"type":"integer","minimum":100,"maximum":120000,"default":30000}
		},"required":["for"],"additionalProperties":false}`),
	},
	{
		Name: "control", Phase: "M0",
		Description: "Release the agent's lease (and the mask), or report it.",
		Schema: schema(`{"type":"object","properties":{
			"action":{"type":"string","enum":["release","state"],"default":"state"},
			"tab":{"type":"string"}
		},"required":["action"],"additionalProperties":false}`),
	},
	{
		Name: "drag", Phase: "M3",
		Description: "Drag one element onto another. Both an HTML5 DnD sequence (when the source is draggable or a payload is given) and a pointer step sequence are dispatched, so custom mousemove-driven drag implementations work too.",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"from_ref":{"type":"string"},
			"from_selector":{"type":"string"},
			"from_text":{"type":"string"},
			"to_ref":{"type":"string"},
			"to_selector":{"type":"string"},
			"to_text":{"type":"string"},
			"html5":{"type":"boolean","default":false,"description":"force the HTML5 DnD sequence"},
			"payload":{"type":"string","description":"text set on the DataTransfer for HTML5 drops"},
			"steps":{"type":"integer","minimum":2,"maximum":16,"default":8,"description":"pointer move steps"},
			"wait_ms":{"type":"integer","minimum":100,"maximum":30000,"default":5000,"description":"actionability wait for both endpoints"}
		},"additionalProperties":false}`),
	},
	{
		Name: "storage", Phase: "M3",
		Description: "Inspect or modify the page origin's localStorage/sessionStorage: list keys, get/set one value, clear one key or the whole area. Values are capped at 64 KiB on set.",
		Schema: schema(`{"type":"object","properties":{
			"tab":{"type":"string"},
			"kind":{"type":"string","enum":["local","session"],"default":"local"},
			"action":{"type":"string","enum":["list","get","set","clear"],"default":"list"},
			"key":{"type":"string"},
			"value":{"type":"string","description":"action=set only, max 64 KiB"}
		},"additionalProperties":false}`),
	},
}
