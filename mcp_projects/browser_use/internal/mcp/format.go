package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"browseruse/internal/browser"
	"browseruse/internal/script"
)

// formatToolText renders the model-facing text for a tool result. It is
// intentionally compact: structuredContent keeps the full object for UIs, while
// the text content should not repeat the whole JSON envelope (snapshot text was
// otherwise sent twice, and every result paid for field names).
func formatToolText(name string, raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "ok"
	}
	switch name {
	case "snapshot":
		var v browser.Snapshot
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		var b strings.Builder
		if v.URL != "" {
			b.WriteString("URL: " + v.URL + "\n")
		}
		if v.Title != "" {
			b.WriteString("Title: " + v.Title + "\n")
		}
		if v.Truncated {
			b.WriteString("(snapshot truncated by max_chars; narrow with filter/scope)\n")
		}
		b.WriteString(v.Snapshot)
		return strings.TrimRight(b.String(), "\n")

	case "read":
		var v browser.ReadResult
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		var b strings.Builder
		if v.URL != "" {
			b.WriteString("URL: " + v.URL + "\n")
		}
		if v.Truncated {
			b.WriteString("(truncated at max_chars)\n")
		}
		b.WriteString("\n")
		b.WriteString(v.Content)
		return b.String()

	case "navigate":
		var v struct {
			Navigated bool            `json:"navigated"`
			Tab       browser.TabInfo `json:"tab"`
		}
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		if v.Navigated {
			return "navigated: " + tabText(v.Tab)
		}
		return "navigation stopped: " + tabText(v.Tab)

	case "tabs":
		var v struct {
			Tabs   []browser.TabInfo `json:"tabs"`
			Tab    *browser.TabInfo  `json:"tab"`
			Active string            `json:"active"`
			Closed string            `json:"closed"`
		}
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		if v.Closed != "" {
			return "closed [" + v.Closed + "]"
		}
		if v.Tab != nil {
			return "tab: " + tabText(*v.Tab)
		}
		if v.Active != "" {
			return "active: [" + v.Active + "]"
		}
		if v.Tabs != nil {
			lines := make([]string, 0, len(v.Tabs))
			for _, t := range v.Tabs {
				lines = append(lines, "- "+tabText(t))
			}
			return strings.Join(lines, "\n")
		}

	case "click":
		var v browser.Receipt
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		var b strings.Builder
		b.WriteString("clicked " + nodeLabel(v.Clicked))
		if v.Revealed {
			b.WriteString(" (revealed a hover-only wrapper)")
		}
		if v.Navigated {
			b.WriteString("; page navigated")
		}
		b.WriteString("\n" + tailURL(v.URL, v.Title))
		return b.String()

	case "type":
		var v browser.Receipt
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		var b strings.Builder
		b.WriteString("typed " + nodeLabel(v.Typed))
		if v.Submitted {
			b.WriteString("; submitted")
		}
		if v.Navigated {
			b.WriteString("; page navigated")
		}
		b.WriteString("\n" + tailURL(v.URL, v.Title))
		return b.String()

	case "press":
		var v browser.Receipt
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		return "pressed " + compactAny(v.Pressed) + "\n" + tailURL(v.URL, "")

	case "select":
		var v browser.Receipt
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		return "selected: " + strings.Join(v.Selected, ", ") + "\n" + tailURL(v.URL, "")

	case "hover":
		var v browser.Receipt
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		return "hovered " + nodeLabel(v.Hovered) + "\n" + tailURL(v.URL, "")

	case "scroll":
		var v struct {
			Scroll map[string]any `json:"scroll"`
		}
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		return "scroll: " + compactAny(v.Scroll)

	case "wait":
		var v browser.WaitResult
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		if !v.Matched {
			return fmt.Sprintf("not matched after %dms", v.ElapsedMS)
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("matched after %dms", v.ElapsedMS))
		if v.Path != "" {
			b.WriteString("; download: " + v.Path)
		}
		if v.URL != "" {
			b.WriteString("\n" + tailURL(v.URL, v.Title))
		}
		return b.String()

	case "eval":
		var v browser.EvalResult
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		return "eval result:\n" + compactAny(v.Result)

	case "script":
		var v script.Result
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("script %s finished in %dms\nresult:\n", v.ScriptID, v.DurationMS))
		b.WriteString(compactAny(v.Result))
		if len(v.Console) > 0 {
			b.WriteString("\nconsole:")
			for i, line := range v.Console {
				if i >= 50 {
					b.WriteString("\n  …")
					break
				}
				b.WriteString("\n  [" + line.Level + "] " + line.Text)
			}
		}
		if len(v.Actions) > 0 {
			b.WriteString(fmt.Sprintf("\nactions: %d", len(v.Actions)))
			for i, a := range v.Actions {
				if i >= 40 {
					b.WriteString("\n  …")
					break
				}
				status := "ok"
				if !a.OK {
					status = "failed"
					if a.Code != "" {
						status += " " + a.Code
					}
				}
				b.WriteString(fmt.Sprintf("\n  - %s %s (%dms)", a.Tool, status, a.MS))
			}
		}
		return b.String()

	case "network":
		var v browser.NetworkResult
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		if v.Cleared > 0 {
			return fmt.Sprintf("cleared %d request(s)", v.Cleared)
		}
		var lines []string
		if v.Entry != nil {
			lines = append(lines, networkLine(*v.Entry, true))
		}
		for _, e := range v.Entries {
			lines = append(lines, networkLine(e, false))
		}
		lines = append(lines, fmt.Sprintf("count=%d dropped=%d bytes=%d", v.Count, v.Dropped, v.TotalBytes))
		return strings.Join(lines, "\n")

	case "console":
		var v browser.ConsoleResult
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		if v.Cleared > 0 {
			return fmt.Sprintf("cleared %d console line(s)", v.Cleared)
		}
		var lines []string
		if v.Entry != nil {
			lines = append(lines, consoleLine(*v.Entry))
		}
		for _, e := range v.Entries {
			lines = append(lines, consoleLine(e))
		}
		if len(lines) == 0 {
			if v.Matched {
				return "no matching console output"
			}
			return fmt.Sprintf("no matching console output after %dms", v.ElapsedMS)
		}
		lines = append(lines, fmt.Sprintf("count=%d dropped=%d bytes=%d", v.Count, v.Dropped, v.TotalBytes))
		return strings.Join(lines, "\n")

	case "downloads":
		var v struct {
			Downloads []browser.Download `json:"downloads"`
			Dir       string             `json:"dir"`
		}
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		var lines []string
		for _, d := range v.Downloads {
			line := fmt.Sprintf("[%s] %s", d.Tab, d.Path)
			if d.URL != "" {
				line += " <- " + d.URL
			}
			if d.Err != "" {
				line += " (failed: " + d.Err + ")"
			}
			lines = append(lines, line)
		}
		if v.Dir != "" {
			lines = append(lines, "dir: "+v.Dir)
		}
		if len(lines) == 0 {
			return "no downloads"
		}
		return strings.Join(lines, "\n")

	case "dialogs":
		var v browser.DialogResult
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		if v.Cleared > 0 {
			return fmt.Sprintf("cleared %d dialog record(s)", v.Cleared)
		}
		var lines []string
		for _, d := range v.Dialogs {
			line := "[" + d.Type + "] " + d.Message
			if d.Answer != nil {
				line += fmt.Sprintf(" -> %v", d.Answer)
			}
			lines = append(lines, line)
		}
		if len(lines) == 0 {
			lines = append(lines, "no dialogs")
		}
		lines = append(lines, "policy: "+compactAny(v.Policy))
		return strings.Join(lines, "\n")

	case "files":
		var v browser.UploadResult
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		return fmt.Sprintf("uploaded %d file(s) to [%s]: %s", v.Uploaded.Count, v.Uploaded.Ref, strings.Join(v.Uploaded.Names, ", "))

	case "frames":
		var v browser.FramesResult
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		if v.Active != "" {
			return "active frame: " + v.Active
		}
		var lines []string
		for _, f := range v.Frames {
			lines = append(lines, fmt.Sprintf("[%s] %s %s", f.ID, f.Title, f.URL))
		}
		if len(lines) == 0 {
			return "no direct iframes"
		}
		return strings.Join(lines, "\n")

	case "cookies":
		var v browser.CookiesResult
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		var lines []string
		for _, c := range v.Cookies {
			lines = append(lines, fmt.Sprintf("%s (domain=%s path=%s http_only=%v)", c.Name, c.Domain, c.Path, c.HTTPOnly))
		}
		if len(lines) == 0 {
			lines = append(lines, "no cookies")
		}
		lines = append(lines, fmt.Sprintf("count=%d changed=%d imported=%d", v.Count, v.Changed, v.Imported))
		return strings.Join(lines, "\n")

	case "control":
		var v struct {
			Control browser.ControlState `json:"control"`
		}
		if json.Unmarshal(raw, &v) != nil {
			break
		}
		return fmt.Sprintf("control=%s tab=%s masked=%v client=%s", v.Control.State, v.Control.TabID, v.Control.Masked, v.Control.Client)
	}
	return compactJSON(raw)
}

func tabText(t browser.TabInfo) string {
	label := "[" + t.ID + "] " + t.Title
	if t.Title == "" {
		label = "[" + t.ID + "] " + t.URL
	}
	if t.Loading {
		label += " (loading)"
	}
	if t.Locked {
		label += " (agent lease)"
	}
	if t.URL != "" && t.URL != t.Title {
		label += " — " + t.URL
	}
	return label
}

func nodeLabel(n *browser.SnapshotNode) string {
	if n == nil {
		return "element"
	}
	role := n.Role
	if role == "" {
		role = "element"
	}
	label := role
	if n.Name != "" {
		label += fmt.Sprintf(" %q", n.Name)
	}
	if n.Ref != "" {
		label += " [" + n.Ref + "]"
	}
	return label
}

func tailURL(url, title string) string {
	if url == "" && title == "" {
		return ""
	}
	if title != "" {
		return "URL: " + url + "\nTitle: " + title
	}
	return "URL: " + url
}

func networkLine(e browser.NetworkEntry, withBody bool) string {
	line := fmt.Sprintf("[%s] %s %d %dms %s", e.ID, e.Method, e.Status, e.MS, e.URL)
	if e.Error != "" {
		line += " (error: " + e.Error + ")"
	}
	if withBody {
		if e.ReqBody != "" {
			line += "\n  request: " + clipText(e.ReqBody, 2000)
		}
		if e.ResBody != "" {
			line += "\n  response: " + clipText(e.ResBody, 4000)
		}
	}
	return line
}

func consoleLine(e browser.ConsoleEntry) string {
	line := fmt.Sprintf("[%s] %s %s", e.ID, e.Level, clipText(e.Text, 4000))
	if e.URL != "" && e.Line > 0 {
		line += fmt.Sprintf(" (%s:%d)", e.URL, e.Line)
	}
	return line
}

func compactAny(v any) string {
	if v == nil {
		return "null"
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err == nil {
		return buf.String()
	}
	return string(raw)
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
