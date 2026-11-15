package web

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// handleStatic 提供内嵌的静态资源（a_da token/CSS/JS 副本与 app-local.css）。
// 只服务 embed FS 中真实存在的普通文件：目录、空路径与任何非规范路径
// （. / .. / 重复斜杠 / 反斜杠）一律 404，因此不存在目录穿越面。
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name, ok := staticAssetName(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(staticFS, "static/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", staticContentType(name))
	// 带 ?v= 指纹的资源可以长缓存；例外是 SPA 运行时的内部 ESM 模块图：
	// import "./sfc.mjs" 这类相对导入不带 query，必须 no-cache，否则重建网关后
	// 浏览器继续执行旧模块（2026-11-11 SPA 重建期间实际踩到过）。
	if r.URL.Query().Get("v") != "" || !isSpaModuleAsset(name) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if isSpaModuleAsset(name) {
		// SPA 运行时模块必须每次协商校验（不管有没有 ?v）：模块图里的相对
		// import 不带版本号，而浏览器已缓存的 max-age=3600 响应在 TTL 内不会
		// 重新验证——重建网关后会继续执行旧模块（2026-11-11 走查踩到）。
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// staticAssetName 把 /static/ 之后的路径规范化；只接受规范相对路径。
func staticAssetName(urlPath string) (string, bool) {
	name := strings.TrimPrefix(urlPath, staticPrefix)
	if name == "" || strings.ContainsAny(name, `\`) {
		return "", false
	}
	// 先按 path.Clean 归一：任何 . / .. / 重复或末尾斜杠都会让结果与原名不同。
	if strings.TrimPrefix(path.Clean("/"+name), "/") != name {
		return "", false
	}
	if !fs.ValidPath(name) {
		return "", false
	}
	return name, true
}

// staticContentType 按扩展名给出稳定 Content-Type，不依赖系统 mime.types，
// 保证 /static/app.js 始终是 application/javascript。
func staticContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".vue":
		return "text/plain; charset=utf-8"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}

// isSpaModuleAsset 判断名字是否属于 SPA 运行时的内部 ESM 模块图
// （static/ada/**/*.js|*.mjs）：这些文件由模块依赖图互相 import，URL 不带 ?v。
func isSpaModuleAsset(name string) bool {
	if !strings.HasPrefix(name, "ada/") {
		return false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".mjs", ".vue":
		// .vue 也是运行时装载的文本组件（sfc.mjs fetch 后编译），不带 ?v 时
		// 同样要协商校验，否则重建后浏览器继续用旧组件。
		return true
	default:
		return false
	}
}
