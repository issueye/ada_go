//go:build windows

// Package mcp serves the Model Context Protocol over stdin and stdout, in
// front of the program's HTTP API: an MCP client (Claude Desktop, an IDE, an
// agent harness) lists tools and calls them, and the mask runs in the app.
package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server is an MCP server for one HTTP endpoint.
type Server struct {
	base    string
	token   string
	version string
	client  *http.Client
}

// New returns a server that asks the app at base for everything it does.
func New(base, token, version string) *Server {
	return &Server{
		base: strings.TrimSuffix(base, "/"), token: token, version: version,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

const instructions = "Controls a Windows desktop and draws a mask over it: screenshots return physical-pixel coordinates; actions take them. Call screen_info first, then screenshot with a grid, then click/type."

// Serve runs the MCP server over the given streams until the input ends.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	return server.NewStdioServer(newMCPServer(s)).Listen(context.Background(), in, out)
}

// newMCPServer registers every tool of toolList with the mcp-go server.
func newMCPServer(s *Server) *server.MCPServer {
	srv := server.NewMCPServer("computer-use", s.version,
		server.WithInstructions(instructions),
		server.WithRecovery(),
	)
	for _, raw := range toolList() {
		name, _ := raw["name"].(string)
		desc, _ := raw["description"].(string)
		if name == "" {
			continue
		}
		schema, err := json.Marshal(raw["inputSchema"])
		if err != nil {
			continue
		}
		toolName := name
		srv.AddTool(mcpgo.NewToolWithRawSchema(toolName, desc, schema),
			func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				args, err := json.Marshal(req.Params.Arguments)
				if err != nil || len(args) == 0 || string(args) == "null" {
					args = []byte(`{}`)
				}
				res, err := s.callTool(ctx, toolName, args)
				if err != nil {
					// 工具失败走 isError 内容，让模型看到并自纠。
					return mcpgo.NewToolResultError(err.Error()), nil
				}
				return res, nil
			})
	}
	return srv
}

// callTool runs a tool through the HTTP API and shapes the MCP result.
func (s *Server) callTool(ctx context.Context, name string, args json.RawMessage) (*mcpgo.CallToolResult, error) {
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
		if err := s.doJSON(ctx, "GET", "/v1/screenshot?"+q.Encode(), nil, &out); err != nil {
			return nil, err
		}
		meta := strings.TrimSpace(string(out.Meta))
		if meta == "null" || meta == "" {
			meta = "{}"
		}
		return mcpgo.NewToolResultImage(
			"The image is "+mediaWord(out.MediaType)+". Coordinates are physical pixels of the desktop:\n"+meta,
			out.Image, out.MediaType,
		), nil

	case "mask_preview":
		png, err := s.doBytes(ctx, "GET", "/v1/mask.png", nil)
		if err != nil {
			return nil, err
		}
		return mcpgo.NewToolResultImage(
			"The mask as the user sees it on the first display.",
			base64.StdEncoding.EncodeToString(png), "image/png",
		), nil

	case "screen_info":
		raw, err := s.doBytes(ctx, "GET", "/v1/state", nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "list_windows":
		raw, err := s.doBytes(ctx, "GET", "/v1/windows", nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "clipboard":
		switch getStr("op") {
		case "set", "write":
			body := map[string]any{"text": getStr("text")}
			raw, err := s.doBytes(ctx, "POST", "/v1/clipboard", body)
			if err != nil {
				return nil, err
			}
			return textResult(raw), nil
		default:
			raw, err := s.doBytes(ctx, "GET", "/v1/clipboard", nil)
			if err != nil {
				return nil, err
			}
			return textResult(raw), nil
		}

	case "pause", "resume":
		raw, err := s.doBytes(ctx, "POST", "/v1/"+name, nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "mask":
		raw, err := s.doBytes(ctx, "POST", "/v1/mask", a)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "clear_mask":
		raw, err := s.doBytes(ctx, "POST", "/v1/mask/clear", nil)
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
		raw, err := s.doBytes(ctx, "POST", "/v1/actions", action)
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
		raw, err := s.doBytes(ctx, "POST", "/v1/actions", map[string]any{"actions": acts})
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "run_script":
		raw, err := s.doBytes(ctx, "POST", "/v1/script", a)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "record_start":
		body := map[string]any{}
		if v := getStr("name"); v != "" {
			body["name"] = v
		}
		raw, err := s.doBytes(ctx, "POST", "/v1/record/start", body)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "record_stop":
		raw, err := s.doBytes(ctx, "POST", "/v1/record/stop", nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "list_macros":
		raw, err := s.doBytes(ctx, "GET", "/v1/macros", nil)
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
		raw, err := s.doBytes(ctx, "POST", "/v1/macros/"+url.PathEscape(macroName)+"/run", body)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "macro_script":
		macroName := getStr("name")
		if macroName == "" {
			return nil, fmt.Errorf("macro_script needs a name")
		}
		raw, err := s.doBytes(ctx, "GET", "/v1/macros/"+url.PathEscape(macroName)+"/script", nil)
		if err != nil {
			return nil, err
		}
		return textResult(raw), nil

	case "delete_macro":
		macroName := getStr("name")
		if macroName == "" {
			return nil, fmt.Errorf("delete_macro needs a name")
		}
		raw, err := s.doBytes(ctx, "DELETE", "/v1/macros/"+url.PathEscape(macroName), nil)
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
		raw, err := s.doBytes(ctx, "POST", "/v1/actions", action)
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

func textResult(raw []byte) *mcpgo.CallToolResult {
	return mcpgo.NewToolResultText(string(raw))
}

func (s *Server) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, rdr)
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

func (s *Server) doBytes(ctx context.Context, method, path string, body any) ([]byte, error) {
	resp, err := s.request(ctx, method, path, body)
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

func (s *Server) doJSON(ctx context.Context, method, path string, body, out any) error {
	raw, err := s.doBytes(ctx, method, path, body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unexpected answer from the app: %w", err)
	}
	return nil
}
