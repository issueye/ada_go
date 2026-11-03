// Package mcp exposes browser-use as a Model Context Protocol server over
// stdio or Streamable HTTP.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"browseruse/internal/browser"
	"browseruse/internal/cli"
	"browseruse/internal/control"
	"browseruse/internal/version"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

// Service is the shared app connection and tool-call state of both transports.
type Service struct {
	ensureMu sync.Mutex
	client   *cli.Client

	callsMu sync.Mutex
	calls   map[string]context.CancelFunc
}

// NewService returns an empty service.
func NewService() *Service {
	return &Service{calls: map[string]context.CancelFunc{}}
}

func (s *Service) ensure(ctx context.Context) (*cli.Client, error) {
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

func (s *Service) addCall(id string, cancel context.CancelFunc) {
	s.callsMu.Lock()
	s.calls[id] = cancel
	s.callsMu.Unlock()
}

func (s *Service) removeCall(id string) {
	s.callsMu.Lock()
	delete(s.calls, id)
	s.callsMu.Unlock()
}

// Cancel cancels the call with the JSON-RPC id.
func (s *Service) Cancel(id string) {
	s.callsMu.Lock()
	cancel := s.calls[id]
	s.callsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) cancelAll() {
	s.callsMu.Lock()
	for _, cancel := range s.calls {
		cancel()
	}
	s.callsMu.Unlock()
}

// Release releases this MCP client's leases.
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

func (s *Service) Call(ctx context.Context, name string, args json.RawMessage) (any, error) {
	client, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	if name == "screenshot" {
		var m map[string]any
		if json.Unmarshal(args, &m) == nil {
			m["inline"] = true
			if b, err := json.Marshal(m); err == nil {
				args = b
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
	return dataResult(env.Data), nil
}

func screenshotResult(raw json.RawMessage) (map[string]any, error) {
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
	contentList := []content{{Type: "text", Text: text}}
	if shot.Data != "" {
		contentList = append(contentList, content{Type: "image", Data: shot.Data, MIMEType: shot.MIMEType})
	}
	meta := map[string]any{
		"path": shot.Path, "width": shot.Width, "height": shot.Height,
		"mime_type": shot.MIMEType, "annotated": shot.Annotated,
	}
	return map[string]any{
		"content":           contentList,
		"structuredContent": meta,
		"isError":           false,
	}, nil
}

func dataResult(raw json.RawMessage) map[string]any {
	text := string(raw)
	if len(raw) == 0 || text == "null" {
		text = "ok"
	}
	res := map[string]any{
		"content": []content{{Type: "text", Text: text}},
		"isError": false,
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil && obj != nil {
		res["structuredContent"] = obj
	}
	return res
}

func textResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []content{{Type: "text", Text: text}},
		"isError": isError,
	}
}

func errorResult(e *browser.Error) map[string]any {
	var b strings.Builder
	b.WriteString(e.Code)
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.Hint != "" {
		b.WriteString("\nhint: ")
		b.WriteString(e.Hint)
	}
	return textResult(b.String(), true)
}

type server struct {
	svc  *Service
	mu   sync.Mutex
	enc  *json.Encoder
	emit func(response)
}

// Run starts either stdio (default) or HTTP MCP.
func Run(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	httpMode := fs.Bool("http", false, "serve Streamable HTTP instead of stdio")
	port := fs.Int("port", 8766, "HTTP port")
	_ = fs.Parse(args)
	if *httpMode || (fs.NArg() > 0 && fs.Arg(0) == "http") {
		return runHTTP(*port)
	}
	return runStdio()
}

func runStdio() int {
	s := &server{
		svc: NewService(),
		enc: json.NewEncoder(os.Stdout),
	}
	dec := json.NewDecoder(bufio.NewReaderSize(os.Stdin, 1<<20))
	dec.UseNumber()
	var wg sync.WaitGroup
	for {
		var req request
		if err := dec.Decode(&req); err != nil {
			break
		}
		if req.JSONRPC == "" {
			req.JSONRPC = "2.0"
		}
		wg.Add(1)
		go func(r request) {
			defer wg.Done()
			s.handle(r)
		}(req)
	}
	s.svc.cancelAll()
	wg.Wait()
	s.svc.Release()
	return 0
}

func (s *server) handle(req request) {
	notification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		if !notification {
			s.reply(req.ID, initializeResult(req.Params))
		}
	case "notifications/initialized", "initialized":
		return
	case "ping":
		if !notification {
			s.reply(req.ID, map[string]any{})
		}
	case "tools/list":
		if !notification {
			s.reply(req.ID, map[string]any{"tools": toolList()})
		}
	case "tools/call":
		if !notification {
			s.call(req)
		}
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		s.svc.Cancel(idKey(p.RequestID))
	case "exit":
		return
	default:
		if !notification {
			s.fail(req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

func initializeResult(params json.RawMessage) map[string]any {
	proto := "2024-11-05"
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
		proto = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": proto,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "browser-use", "version": version.Version},
		"instructions":    "Local native browser for agents. Use snapshot before click, script for multi-step flows, and network to inspect fetch/XHR JSON APIs.",
	}
}

func toolList() []map[string]any {
	out := make([]map[string]any, 0, len(control.Tools))
	for _, t := range control.Tools {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.Schema,
		})
	}
	return out
}

func (s *server) call(req request) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		s.fail(req.ID, -32602, "tools/call needs name and arguments")
		return
	}
	if !knownTool(p.Name) {
		s.fail(req.ID, -32602, "unknown tool: "+p.Name)
		return
	}
	args := p.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	key := idKey(req.ID)
	s.svc.addCall(key, cancel)
	defer func() {
		cancel()
		s.svc.removeCall(key)
	}()
	res, err := s.svc.Call(ctx, p.Name, args)
	if err != nil {
		if be, ok := err.(*browser.Error); ok {
			s.reply(req.ID, errorResult(be))
		} else {
			s.reply(req.ID, textResult(err.Error(), true))
		}
		return
	}
	s.reply(req.ID, res)
}

func knownTool(name string) bool {
	for _, t := range control.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func (s *server) reply(id json.RawMessage, result any) {
	resp := response{JSONRPC: "2.0", ID: id, Result: result}
	if s.emit != nil {
		s.emit(resp)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.enc.Encode(resp)
}

func (s *server) fail(id json.RawMessage, code int, message string) {
	resp := response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
	if s.emit != nil {
		s.emit(resp)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.enc.Encode(resp)
}

func idKey(raw json.RawMessage) string {
	return strings.TrimSpace(string(raw))
}

// runHTTP serves the Streamable HTTP transport on 127.0.0.1.
func runHTTP(port int) int {
	svc := NewService()
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				http.Error(w, "use POST for JSON-RPC or Accept: text/event-stream", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprint(w, ": browser-use mcp\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			<-r.Context().Done()
		case http.MethodDelete:
			svc.Release()
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
			var req request
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad JSON-RPC: "+err.Error(), http.StatusBadRequest)
				return
			}
			if req.JSONRPC == "" {
				req.JSONRPC = "2.0"
			}
			notification := len(req.ID) == 0
			if notification {
				s := &server{svc: svc, emit: func(response) {}}
				s.handle(req)
				w.WriteHeader(http.StatusAccepted)
				return
			}
			ch := make(chan response, 1)
			s := &server{svc: svc, emit: func(resp response) {
				select {
				case ch <- resp:
				default:
				}
			}}
			s.handle(req)
			select {
			case resp := <-ch:
				w.Header().Set("Content-Type", "application/json")
				if req.Method == "initialize" {
					w.Header().Set("Mcp-Session-Id", fmt.Sprintf("browser-use-%d", os.Getpid()))
				}
				_ = json.NewEncoder(w).Encode(resp)
			case <-time.After(10 * time.Minute):
				http.Error(w, "MCP response timeout", http.StatusGatewayTimeout)
			}
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"app":"browser-use","transport":"mcp-http"}`))
	})

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser-use mcp http:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "browser-use mcp http: http://%s/mcp\n", ln.Addr())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "browser-use mcp http:", err)
		return 1
	}
	return 0
}
