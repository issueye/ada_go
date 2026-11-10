//go:build !windows && !unix

package sqlite

import (
	"fmt"
	"runtime"
)

// probeLocalMedia 是既非 Windows 也非 Unix 的平台（js/wasm、plan9、wasip1…）的兜底：
// 没有可靠的文件系统类型信号，一律返回 MediaUnknown，由调用方拒绝启动（失败方向）。
func probeLocalMedia(_ string, info MediaInfo) (MediaInfo, error) {
	info.Verdict = MediaUnknown
	info.Reason = fmt.Sprintf("平台 %s 没有可用的存储介质判定实现", runtime.GOOS)
	return info, nil
}
