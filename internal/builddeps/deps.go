// Package builddeps 锁定 ada-go 的第三方依赖基线（设计 13.2、开发计划 M0.1）。
//
// M0 阶段还没有业务代码引用这些库，直接 `go mod tidy` 会把它们从 go.mod 里删掉，
// 因此用空导入固定版本。每个库在对应里程碑被真实包引用后，导入从这个文件移走；
// 全部搬完后（M5）本文件删除。升级任一依赖必须单独提 PR 并跑全量契约测试。
//
// go.work 只是本地联调覆盖（`.` 与 `../moejs`），发布依赖以本模块的
// go.mod/go.sum 为准；更新这里请用 `GOWORK=off go mod tidy`。
package builddeps

import (
	// 配置解析（附录 B 的 config.toml）。
	_ "github.com/BurntSushi/toml"

	// 插件与 TSX 运行时（M1-C 宿主、M4 Render Runtime）。固定 v0.1.0-alpha.11。
	_ "github.com/MoeForge/moejs"
	_ "github.com/MoeForge/moejs/node" // Node 兼容原生库：能力位 → 安装配置
	_ "github.com/MoeForge/moejs/webapi"

	// Gateway ↔ Core WebSocket（context 友好）。
	_ "github.com/coder/websocket"

	// 官方/第三方 TS、TSX 编译（构建期与安装期）。
	_ "github.com/evanw/esbuild/pkg/api"

	// Gateway HTTP 路由与中间件。
	_ "github.com/gin-gonic/gin"

	// 数据目录单实例锁。
	_ "github.com/gofrs/flock"

	// Markdown 输出兜底清洗。
	_ "github.com/microcosm-cc/bluemonday"

	// 线程、entry、run 的可排序 id。
	_ "github.com/oklog/ulid/v2"

	// Markdown 渲染（关闭 unsafe）。
	_ "github.com/yuin/goldmark"

	// 纯 Go SQLite 驱动，保持无 cgo 交叉编译。
	_ "modernc.org/sqlite"

	// errgroup / semaphore：预取与并发工具。
	_ "golang.org/x/sync/errgroup"
	_ "golang.org/x/sync/semaphore"
)
