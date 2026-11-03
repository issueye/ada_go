//go:build windows

// Package mcp serves the Model Context Protocol over stdin and stdout, in
// front of the program's HTTP API: an MCP client (Claude Desktop, an IDE, an
// agent harness) lists tools and calls them, and the mask runs in the app.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Server is an MCP server for one HTTP endpoint.
type Server struct {
	base    string
	token   string
	version string
	in      io.Reader
	out     io.Writer
	client  *http.Client
}

// New returns a server that asks the app at base for everything it does.
func New(base, token, version string, in io.Reader, out io.Writer) *Server {
	return &Server{
		base: strings.TrimSuffix(base, "/"), token: token, version: version,
		in: in, out: out,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads and answers messages until the input ends.
func (s *Server) Serve() error {
	sc := bufio.NewScanner(s.in)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			s.write(response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}})
			continue
		}
		if len(req.ID) == 0 {
			// A notification: nothing to answer.
			continue
		}
		result, rerr := s.handle(req)
		resp := response{JSONRPC: "2.0", ID: req.ID}
		if rerr != nil {
			resp.Error = rerr
		} else {
			resp.Result = result
		}
		s.write(resp)
	}
	return sc.Err()
}

func (s *Server) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.out.Write(append(b, '\n'))
}

func (s *Server) handle(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		version := p.ProtocolVersion
		if version == "" {
			version = "2024-11-05"
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "computer-use",
				"version": s.version,
			},
			"instructions": "Controls a Windows desktop and draws a mask over it: screenshots return physical-pixel coordinates; actions take them. Call screen_info first, then screenshot with a grid, then click/type.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolList()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
		}
		res, err := s.callTool(p.Name, p.Arguments)
		if err != nil {
			return map[string]any{
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
				"isError": true,
			}, nil
		}
		return res, nil
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

// callTool runs a tool through the HTTP API and shapes the MCP result.
func (s *Server) callTool(name string, args json.RawMessage) (map[string]any, error) {
	a := map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	getStr := func(key string) string {
		if v, ok := a[key].(string); ok {
			return v
		}
		return ""
	}
	getInt := func(key string) int {
		switch v := a[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
		return 0
	}
	getBool := func(key string, def bool) bool {
		if v, ok := a[key].(bool); ok {
			return v
		}
		return def
	}

	switch name {
	case "screenshot":
		q := url.Values{}
		if v := getStr("display"); v != "" {
			q.Set("display", v)
		}
		if v := getInt("max_width"); v > 0 {
			q.Set("max_width", fmt.Sprint(v))
		}
		if v := getInt("grid"); v > 0 {
			q.Set("grid", fmt.Sprint(v))
		}
		if v := getStr("format"); v != "" {
			q.Set("format", v)
		}
		if v := getStr("region"); v != "" && v != "all" {
			// "x,y,w,h"
			var x, y, w, h int
			if _, err := fmt.Sscanf(v, "%d,%d,%d,%d", &x, &y, &w, &h); err == nil {
				q.Set("x", fmt.Sprint(x))
				q.Set("y", fmt.Sprint(y))
				q.Set("w", fmt.Sprint(w))
				q.Set("h", fmt.Sprint(h))
			}
		}
		for _, k := range []string{"x", "y", "w", "h", "scale", "quality"} {
			if v := getInt(k); v != 0 {
				q.Set(k, fmt.Sprint(v))
			}
		}
		if getBool("cursor", false) {
			q.Set("cursor", "1")
		}
		var out struct {
			MediaType string          `json:"media_type"`
			Image     string          `json:"image_base64"`
			Meta      json.RawMessage `json:"meta"`
			Error     string          `json:"error"`
		}
		if err := s.doJSON("GET", "/v1/screenshot?"+q.Encode(), nil, &out); err != nil {
			return nil, err
		}
		meta := strings.TrimSpace(string(out.Meta))
		if meta == "null" || meta == "" {
			meta = "{}"
		}
		return map[string]any{
			"content": []map[string]any{
				{"type": "image", "data": out.Image, "mimeType": out.MediaType},
				{"type": "text", "text": "The image is " + mediaWord(out.MediaType) + ". Coordinates are physical pixels of the desktop:\n" + meta},
			},
		}, nil

	case "mask_preview":
		png, err := s.doBytes("GET", "/v1/mask.png", nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"content": []map[string]any{
				{"type": "image", "data": base64.StdEncoding.EncodeToString(png), "mimeType": "image/png"},
				{"type": "text", "text": "The mask as the user sees it on the first display."},
			},
		}, nil

	case "screen_info":
		raw, err := s.doBytes("GET", "/v1/state", nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "list_windows":
		raw, err := s.doBytes("GET", "/v1/windows", nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "clipboard":
		switch getStr("op") {
		case "set", "write":
			body := map[string]any{"text": getStr("text")}
			raw, err := s.doBytes("POST", "/v1/clipboard", body)
			if err != nil {
				return nil, err
			}
			return textResult(raw), nil
		default:
			raw, err := s.doBytes("GET", "/v1/clipboard", nil)
			if err != nil {
				return nil, err
			}
			return textResult(raw), nil
		}

	case "pause", "resume":
		raw, err := s.doBytes("POST", "/v1/"+name, nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "mask":
		raw, err := s.doBytes("POST", "/v1/mask", a)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "clear_mask":
		raw, err := s.doBytes("POST", "/v1/mask/clear", nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "outline_window":
		action := map[string]any{"type": "outline"}
		for _, k := range []string{"handle", "title", "append"} {
			if v, ok := a[k]; ok {
				action[k] = v
			}
		}
		raw, err := s.doBytes("POST", "/v1/actions", action)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "batch":
		acts, _ := a["actions"].([]any)
		for _, one := range acts {
			m, ok := one.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("each action must be an object")
			}
			if _, ok := m["type"].(string); !ok {
				return nil, fmt.Errorf("each action needs a type")
			}
		}
		raw, err := s.doBytes("POST", "/v1/actions", map[string]any{"actions": acts})
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "run_script":
		raw, err := s.doBytes("POST", "/v1/script", a)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "record_start":
		body := map[string]any{}
		if v := getStr("name"); v != "" {
			body["name"] = v
		}
		raw, err := s.doBytes("POST", "/v1/record/start", body)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "record_stop":
		raw, err := s.doBytes("POST", "/v1/record/stop", nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "list_macros":
		raw, err := s.doBytes("GET", "/v1/macros", nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "run_macro":
		macroName := getStr("name")
		if macroName == "" {
			return nil, fmt.Errorf("run_macro needs a name")
		}
		body := map[string]any{}
		if v, ok := a["speed"].(float64); ok && v > 0 {
			body["speed"] = v
		}
		raw, err := s.doBytes("POST", "/v1/macros/"+url.PathEscape(macroName)+"/run", body)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "macro_script":
		macroName := getStr("name")
		if macroName == "" {
			return nil, fmt.Errorf("macro_script needs a name")
		}
		raw, err := s.doBytes("GET", "/v1/macros/"+url.PathEscape(macroName)+"/script", nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "delete_macro":
		macroName := getStr("name")
		if macroName == "" {
			return nil, fmt.Errorf("delete_macro needs a name")
		}
		raw, err := s.doBytes("DELETE", "/v1/macros/"+url.PathEscape(macroName), nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "click", "move", "drag", "scroll", "type", "key", "focus_window", "wait":
		// The tools are named as an agent reads them; the actions are named
		// as the API takes them.
		kind := name
		if kind == "focus_window" {
			kind = "focus"
		}
		action := map[string]any{"type": kind}
		for k, v := range a {
			action[k] = v
		}
		raw, err := s.doBytes("POST", "/v1/actions", action)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

func mediaWord(mt string) string {
	if mt == "image/jpeg" {
		return "a JPEG"
	}
	return "a PNG"
}

func textResult(raw []byte) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(raw)}},
	}
}

func (s *Server) request(method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	return s.client.Do(req)
}

func (s *Server) doBytes(method, path string, body any) ([]byte, error) {
	resp, err := s.request(method, path, body)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the computer-use app at %s: %w", s.base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("%s", e.Error)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

func (s *Server) doJSON(method, path string, body, out any) error {
	raw, err := s.doBytes(method, path, body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unexpected answer from the app: %w", err)
	}
	return nil
}
