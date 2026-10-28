//go:build unix && !linux && !android

package sqlite

import (
	"fmt"
	"os"
	"runtime"
)

// probeLocalMedia 是除 Linux 之外 Unix 系（darwin/freebsd/openbsd/netbsd/dragonfly/solaris/aix…）
// 的介质判定。
//
// 诚实声明（M1-B 已知缺口，不假装支持）：这些平台需要 getfsstat(2)/statfs 才能拿到
// 挂载点的文件系统类型，而 Go 标准库 syscall 在各家 BSD 上的字段名不一致，本包目前
// 只实现了「能读到 /proc/mounts（Linux 兼容层，例如 FreeBSD 的 linprocfs）就走挂载表」
// 这一条路径。其余情况返回 MediaUnknown，调用方按失败方向拒绝 Core 启动——
// macOS 发布前必须补 syscall.Getfsstat 分支（M5 三平台加固项）。
func probeLocalMedia(probe string, info MediaInfo) (MediaInfo, error) {
	if raw, err := os.Open("/proc/mounts"); err == nil {
		defer raw.Close()
		mounts := parseMountTable(raw)
		if m, ok := mountForPath(mounts, probe); ok {
			info.MountPoint = m.MountPoint
			info.Device = m.Device
			info.FileSystem = m.FSType
			info.Verdict = classifyFileSystemName(m.FSType)
			info.Reason = fmt.Sprintf("挂载表 %s 的 fstype=%s（设备 %s）", m.MountPoint, m.FSType, m.Device)
			return info, nil
		}
	}
	info.Verdict = MediaUnknown
	info.FileSystem = ""
	info.Reason = fmt.Sprintf("平台 %s 未实现挂载点文件系统类型判定（只支持读取 /proc/mounts 的兼容层）", runtime.GOOS)
	return info, nil
}
