package mcp

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// Run starts either stdio (default) or Streamable HTTP MCP.
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

func runHTTP(port int) int {
	svc := NewService()
	mux := newHTTPMux(newServer(svc))

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
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
	svc.Release()
	return 0
}
