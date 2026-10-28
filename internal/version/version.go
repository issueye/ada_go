// Package version 汇总 ada-go 的版本与协议常量（握手版本见 01 §4.2/§3.1，
// 插件 apiVersion 见 05 §6.2，system.hello 见 08 §8.2）。
//
// 依赖方向：只 import domain；gateway/core 装配与握手用 ProtocolVersion 校验，
// 插件清单 apiVersion 用 APIVersionMajor（05 §6.2：主版本不匹配直接拒绝加载，
// 次版本兼容、宿主忽略未知字段）。ports 不依赖本包。
package version

import (
	"fmt"

	"github.com/adag/ada-go/internal/domain"
)

// ProtocolVersion 是 Gateway↔Core 的 wire 协议版本（system.hello 握手比对，08 §8.1/§8.2）。
// 不兼容变更必须 +1，并同步 spec/proto.json。
const ProtocolVersion = 1

// APIVersionMajor/Minor 是插件清单 apiVersion 兼容窗口（05 §6.2、design.md 开放问题：
// 冻结为 1，M0 契约冻结后变更走 ProtocolVersion 同步流程）。
const (
	APIVersionMajor = 1
	APIVersionMinor = 0
)

// Product 是产品名（工作名，发布前统一，development-plan 决策清单）；
// 界面品牌用 identity.name，这里是稳定标识符。
const Product = "ada-go"

// Binary 是单二进制命令名（子命令 gateway/core/plugin/doctor/verify/export/backup，01 §3.1）。
const Binary = "adag"

// 以下变量由 CI 用 ldflags 注入；默认 "dev" 用于本地构建。
//
//	CGO_ENABLED=0 go build -trimpath \
//	  -ldflags "-X github.com/adag/ada-go/internal/version.Revision=$(git rev-parse --short HEAD) \
//	            -X github.com/adag/ada-go/internal/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
//	  ./cmd/adag
//
// 发布产物一律 GOWORK=off 构建（development-plan §7.1）；Revision/BuildTime
// 是 /healthz 与 system.hello 的版本字段真源（01 §4.2、08 §8.2）。
var (
	// Revision 是 git commit（短哈希）。
	Revision = "dev"
	// BuildTime 是 UTC 构建时刻（RFC3339）。
	BuildTime = "dev"
)

// String 渲染人类可读的一行版本（doctor/healthz/日志共用）。
func String() string {
	return fmt.Sprintf("%s %s (proto=%d, api=%d.%d, rev=%s, built=%s)",
		Product, Binary, ProtocolVersion, APIVersionMajor, APIVersionMinor, Revision, BuildTime)
}

// CheckCompatible 是握手期的协议版本校验（system.hello：版本不匹配即拒）。
// 不匹配返回 domain 错误（CodeProtocolError），调用方据此拒绝建立会话；
// Core 版本不匹配时 Gateway 不重试、直接显示原因（01 §4.2 启动失败结构化错误）。
func CheckCompatible(protocol int) error {
	if protocol != ProtocolVersion {
		return domain.NewError(domain.CodeProtocolError,
			fmt.Sprintf("协议版本不匹配：对端 %d，本端 %d（%s）", protocol, ProtocolVersion, String())).
			WithDiag(domain.NewDiagnostic(domain.DiagCore, 1))
	}
	return nil
}
