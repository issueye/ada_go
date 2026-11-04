package mcp

import (
	"crypto/subtle"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// Run starts either stdio (default) or Streamable HTTP MCP.
func Run(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	httpMode := fs.Bool("http", false, "serve Streamable HTTP instead of stdio")
	port := fs.Int("port", 8766, "HTTP port")
	token := fs.String("token", os.Getenv("BROWSER_USE_MCP_TOKEN"), "require this bearer token for /mcp (default: no auth)")
	_ = fs.Parse(args)
	if *httpMode || (fs.NArg() > 0 && fs.Arg(0) == "http") {
		return runHTTP(*port, strings.TrimSpace(*token))
	}
	return runStdio()
}

func runStdio() int {
	svc := NewService()
	if err := server.ServeStdio(newServer(svc)); err != nil {
		fmt.Fprintln(os.Stderr, "browser-use mcp:", err)
		return 1
	}
	svc.Release()
	return 0
}

// newHTTPMux mounts the stateless Streamable HTTP transport and a health probe.
func newHTTPMux(s *server.MCPServer) http.Handler {
	streamable := server.NewStreamableHTTPServer(s,
		server.WithEndpointPath("/mcp"),
		server.WithStateLess(true),
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamable)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"app":"browser-use","transport":"mcp-http"}`))
	})
	return mux
}

// withToken protects /mcp when a token is configured. /healthz stays open so
// process supervisors can probe without a secret.
func withToken(h http.Handler, token string) http.Handler {
	if token == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			h.ServeHTTP(w, r)
			return
		}
		got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="browser-use-mcp"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"ok":false,"error":"missing or wrong token"}`))
			return
		}
		h.ServeHTTP(w, r)
	})
}

func runHTTP(port int, token string) int {
	svc := NewService()
	mux := withToken(newHTTPMux(newServer(svc)), token)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser-use mcp http:", err)
		return 1
	}
	if token != "" {
		fmt.Fprintf(os.Stderr, "browser-use mcp http: http://%s/mcp (bearer token required)\n", ln.Addr())
	} else {
		fmt.Fprintf(os.Stderr, "browser-use mcp http: http://%s/mcp (no auth; loopback only)\n", ln.Addr())
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "browser-use mcp http:", err)
		return 1
	}
	svc.Release()
	return 0
}
