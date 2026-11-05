// Package app 装配画布服务、设计目录与 MCP 服务器。
package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"adagodesign/internal/canvas"
	"adagodesign/internal/config"
	"adagodesign/internal/design"
	"adagodesign/internal/openbrowser"
)

// Server 是运行中的画布服务句柄。
type Server struct {
	BaseURL  string
	listener net.Listener
	httpSrv  *http.Server
	store    *canvas.Store
	tasks    *canvas.TaskQueue
}

// Store 暴露节点仓库（MCP 工具使用）。
func (s *Server) Store() *canvas.Store { return s.store }

// Tasks 暴露截图任务队列（MCP 工具使用）。
func (s *Server) Tasks() *canvas.TaskQueue { return s.tasks }

// Start 启动画布 HTTP 服务；port=0 自动探测。
func Start(ctx context.Context, cfg *config.Config) (*Server, error) {
	store, err := canvas.OpenStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	tasks := canvas.NewTaskQueue()
	srv := &Server{store: store, tasks: tasks}

	canvasSrv := canvas.NewServer(store, tasks, "")
	canvasSrv.Catalog = Catalog{}
	handler := canvasSrv.Handler()
	var ln net.Listener
	for port := cfg.Port; port <= cfg.Port+20; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		if cfg.Port == 0 {
			// :0 由内核分配，无需重试
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("listen 127.0.0.1:%d..%d: %w", cfg.Port, cfg.Port+20, err)
	}
	srv.listener = ln
	srv.BaseURL = "http://127.0.0.1" + fmt.Sprintf(":%d", ln.Addr().(*net.TCPAddr).Port)
	if cfg.Port == 0 {
		srv.BaseURL = "http://127.0.0.1:" + fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	}

	httpSrv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	srv.httpSrv = httpSrv
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP 服务异常退出: %v", err)
		}
	}()
	log.Printf("画布已就绪: %s (data=%s)", srv.BaseURL, cfg.DataDir)
	if cfg.Open {
		openbrowser.Open(srv.BaseURL)
	}
	return srv, nil
}

// Stop 优雅关闭 HTTP 服务。
func (s *Server) Stop(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

// Catalog 实现 canvas.CatalogSource，桥接 design 包。
type Catalog struct{}

// SystemSummaries 返回全部体系摘要。
func (Catalog) SystemSummaries() any {
	out := make([]map[string]any, 0)
	for _, sys := range design.Systems() {
		out = append(out, map[string]any{
			"id":              sys.ID,
			"name":            sys.Name,
			"vendor":          sys.Vendor,
			"family":          sys.Family,
			"summary":         sys.Style.Summary,
			"keywords":        sys.Style.Keywords,
			"themes":          sys.Themes,
			"component_count": lenDesignComponents(),
		})
	}
	return out
}

// SystemDetail 返回体系完整数据。
func (Catalog) SystemDetail(id string) (any, bool) {
	sys, ok := design.GetSystem(id)
	if !ok {
		return nil, false
	}
	comps := design.Components(sys)
	compList := make([]map[string]any, 0, len(comps))
	for _, c := range comps {
		compList = append(compList, map[string]any{
			"id": c.ID, "name": c.Name, "category": c.Category,
			"description": c.Description, "variants": c.Variants, "usage": c.Usage,
		})
	}
	light, _ := sys.TokensFor(design.ThemeLight)
	detail := map[string]any{
		"id": sys.ID, "name": sys.Name, "vendor": sys.Vendor, "family": sys.Family,
		"description": sys.Description, "style": sys.Style, "themes": sys.Themes,
		"light": light, "guideline": sys.Guideline, "components": compList,
	}
	if sys.Dark != nil {
		detail["dark"] = *sys.Dark
	}
	return detail, true
}

// ComposePreview 生成预览文档。
func (Catalog) ComposePreview(systemID string, components []string, theme, title string) (string, int, int, error) {
	res, err := design.Compose(design.ComposeInput{
		SystemID:   systemID,
		Theme:      design.ThemeKey(themeOrDefault(theme)),
		Components: components,
		Title:      title,
	})
	if err != nil {
		return "", 0, 0, err
	}
	return res.HTML, res.Width, res.Height, nil
}

func themeOrDefault(t string) string {
	if t == "dark" {
		return "dark"
	}
	return "light"
}

func lenDesignComponents() int { return len(design.Components(nil)) }
