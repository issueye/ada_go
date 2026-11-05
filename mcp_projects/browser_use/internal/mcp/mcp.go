// Package mcp exposes browser-use as a Model Context Protocol server.
//
// Protocol plumbing (initialize, tools/list, tools/call, cancellation, stdio and
// Streamable HTTP) comes from github.com/mark3labs/mcp-go. This package only
// bridges the app's /v1/call API to the tool declarations in internal/control:
//
//   - tools are registered with their existing JSON Schema (control.Tools);
//   - a tool call becomes one /v1/call request (screenshot asks for inline data);
//   - /v1/call envelopes become MCP text/structured/image content, and tool
//     errors stay tool errors (isError) instead of protocol errors.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"browseruse/internal/browser"
	"browseruse/internal/cli"
)

// appClient is the control-plane dependency of Service (*cli.Client satisfies
// it; tests inject a fake).
type appClient interface {
	Health(ctx context.Context) error
	SetClientID(id string)
	ClientID() string
	Call(ctx context.Context, name string, args any) (json.RawMessage, error)
}

// Service is the shared app connection of both transports.
type Service struct {
	ensureMu sync.Mutex
	client   appClient
}

// NewService returns an empty service.
func NewService() *Service { return &Service{} }

// ensure returns a healthy app client, starting/reusing the app as needed.
func (s *Service) ensure(ctx context.Context) (appClient, error) {
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	if s.client != nil {
		check, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.client.Health(check)
		cancel()
		if err == nil {
			return s.client, nil
		}
	}
	start, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, err := cli.Ensure(start)
	if err != nil {
		return nil, err
	}
	c.SetClientID(fmt.Sprintf("mcp-%d", os.Getpid()))
	s.client = c
	return c, nil
}

// Release releases this MCP client's leases from the app.
func (s *Service) Release() {
	s.ensureMu.Lock()
	c := s.client
	s.ensureMu.Unlock()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _ = c.Call(ctx, "control", map[string]any{"action": "release", "client": c.ClientID()})
	cancel()
}

// Call invokes one app tool and maps the /v1/call envelope to an MCP result.
func (s *Service) Call(ctx context.Context, name string, args json.RawMessage) (*mcpgo.CallToolResult, error) {
	client, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	if name == "screenshot" {
		var m map[string]any
		if json.Unmarshal(args, &m) == nil && m != nil {
			// 默认 inline（MCP 需要 image 内容），但调用方显式给了 inline 就不再
			// 覆盖：之前无条件覆盖，inline:false 被静默改回 true。
			if _, ok := m["inline"]; !ok {
				m["inline"] = true
				if b, err := json.Marshal(m); err == nil {
					args = b
				}
			}
		}
	}
	raw, err := client.Call(ctx, name, args)
	if err != nil {
		return nil, err
	}
	var env struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *browser.Error  `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("bad app response: %w", err)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, env.Error
		}
		return nil, fmt.Errorf("tool failed")
	}
	if name == "screenshot" {
		return screenshotResult(env.Data)
	}
	return dataResult(name, env.Data), nil
}

func screenshotResult(raw json.RawMessage) (*mcpgo.CallToolResult, error) {
	var shot browser.ScreenshotResult
	if err := json.Unmarshal(raw, &shot); err != nil {
		return nil, err
	}
	if shot.MIMEType == "" {
		shot.MIMEType = "image/png"
	}
	text := fmt.Sprintf("screenshot %dx%d %s", shot.Width, shot.Height, shot.Path)
	if shot.Annotated {
		text += fmt.Sprintf(" (%d refs)", len(shot.Nodes))
	}
	meta := map[string]any{
		"path": shot.Path, "width": shot.Width, "height": shot.Height,
		"mime_type": shot.MIMEType, "annotated": shot.Annotated,
	}
	var res *mcpgo.CallToolResult
	if shot.Data == "" {
		res = mcpgo.NewToolResultText(text)
	} else {
		res = mcpgo.NewToolResultImage(text, shot.Data, shot.MIMEType)
	}
	res.StructuredContent = meta
	return res, nil
}

func dataResult(name string, raw json.RawMessage) *mcpgo.CallToolResult {
	text := formatToolText(name, raw)
	if len(raw) == 0 || string(raw) == "null" || text == "" {
		text = "ok"
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil && obj != nil {
		return mcpgo.NewToolResultStructured(obj, text)
	}
	return mcpgo.NewToolResultText(text)
}

// errorResult keeps tool failures visible to the model (isError=true) while
// preserving the app's stable error code and hint.
func errorResult(err error) *mcpgo.CallToolResult {
	var be *browser.Error
	if errors.As(err, &be) {
		var b strings.Builder
		b.WriteString(be.Code)
		if be.Message != "" {
			b.WriteString(": ")
			b.WriteString(be.Message)
		}
		if be.Hint != "" {
			b.WriteString("\nhint: ")
			b.WriteString(be.Hint)
		}
		if be.Details != nil {
			if raw, err := json.Marshal(be.Details); err == nil {
				b.WriteString("\ndetails: ")
				b.WriteString(clipText(string(raw), 2000))
			}
		}
		return mcpgo.NewToolResultError(b.String())
	}
	return mcpgo.NewToolResultError(err.Error())
}
