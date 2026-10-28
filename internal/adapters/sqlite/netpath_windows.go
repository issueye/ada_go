//go:build windows

package sqlite

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Windows 侧的网络盘判定（04 §5.7「Windows 用卷类型」）：
//  1. UNC 路径（\\server\share）→ 网络；
//  2. 盘符映射：mpr!WNetGetConnectionW 能解析出远端名 → 映射网络驱动器（SMB/WebDAV/NFS 客户端）；
//  3. kernel32!GetVolumeInformationW 拿卷的文件系统名与卷标志：卷名表（NTFS/ReFS/exFAT/FAT…）
//     判本地，网络重定向器返回的卷类型（NFS、CIFS、WebDrive、RDPDR…）判网络；
//  4. 两个 API 都拿不到可信结论 → MediaUnknown（调用方拒绝启动）。
var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	mpr      = syscall.NewLazyDLL("mpr.dll")

	procGetVolumeInformation = kernel32.NewProc("GetVolumeInformationW")
	procWNetGetConnection    = mpr.NewProc("WNetGetConnectionW")
)

const (
	// errNotConnected 是 WNetGetConnectionW 的 ERROR_NOT_CONNECTED：该盘符不是网络映射。
	errNotConnected = syscall.Errno(2250)
	// errBadNetPath 是 ERROR_BAD_NETPATH：盘符是网络映射，但远端当前不可达。
	errBadNetPath = syscall.Errno(67)

	// 缓冲区尺寸（WCHAR 个数）。
	maxVolumeName = 128
	maxFSName     = 256
)

// probeLocalMedia 是 Windows 的介质判定实现。
func probeLocalMedia(probe string, info MediaInfo) (MediaInfo, error) {
	root := filepath.VolumeName(probe)
	if root == "" {
		info.Verdict = MediaUnknown
		info.Reason = fmt.Sprintf("无法从路径解析卷名：%s", probe)
		return info, nil
	}
	if strings.HasPrefix(root, `\\`) {
		info.Verdict = MediaNetwork
		info.Device = root
		info.Reason = "UNC 卷（SMB/DFS）"
		return info, nil
	}

	// 盘符 → 远端名：映射网络驱动器在这里就能定论。
	if remote, isNet := wnetGetConnection(root + `\`); isNet {
		info.Verdict = MediaNetwork
		info.Device = remote
		info.FileSystem = "redirector"
		info.MountPoint = root + `\`
		if remote == "" {
			info.Reason = fmt.Sprintf("盘符 %s: 指向网络共享（当前不可达）", root)
		} else {
			info.Reason = fmt.Sprintf("盘符 %s: 映射到网络共享 %s", root, remote)
		}
		return info, nil
	}

	fsName, flags, err := getVolumeInformation(root + `\`)
	if err != nil {
		info.Verdict = MediaUnknown
		info.Reason = fmt.Sprintf("GetVolumeInformationW(%s) 失败：%v", root, err)
		return info, nil
	}
	info.FileSystem = fsName
	info.MountPoint = root + `\`
	info.Verdict = classifyFileSystemName(fsName)
	switch info.Verdict {
	case MediaLocal:
		info.Reason = fmt.Sprintf("本地卷：%s（卷标志 0x%x）", fsName, flags)
	case MediaNetwork:
		info.Reason = fmt.Sprintf("网络卷类型：%s（卷标志 0x%x）", fsName, flags)
	default:
		info.Reason = fmt.Sprintf("未知卷文件系统类型 %q（卷标志 0x%x）", fsName, flags)
	}
	return info, nil
}

// wnetGetConnection 解析盘符对应的远端共享名。
//
// 返回值含义：
//   - (unc, true)：ERROR_SUCCESS，盘符映射到 unc；
//   - ("", true)：ERROR_BAD_NETPATH(67)，确实是网络映射但当前不可达（同样拒绝启动）；
//   - ("", false)：ERROR_NOT_CONNECTED(2250) 或 API 不可用/其它错误 → 交给卷类型判定。
func wnetGetConnection(localName string) (string, bool) {
	if procWNetGetConnection.Find() != nil {
		return "", false
	}
	buf := make([]uint16, maxVolumeName)
	var length uint32 = uint32(len(buf))
	p, err := syscall.UTF16PtrFromString(localName)
	if err != nil {
		return "", false
	}
	r1, _, _ := procWNetGetConnection.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&length)),
	)
	switch syscall.Errno(r1) {
	case 0:
		return syscall.UTF16ToString(buf), true
	case errBadNetPath:
		return "", true
	case errNotConnected:
		return "", false
	default:
		return "", false
	}
}

// getVolumeInformation 返回卷的文件系统名与卷标志。
func getVolumeInformation(root string) (fsName string, flags uint32, err error) {
	if procGetVolumeInformation.Find() != nil {
		return "", 0, fmt.Errorf("GetVolumeInformationW 不可用")
	}
	p, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return "", 0, err
	}
	var (
		volName    = make([]uint16, maxVolumeName)
		fsBuf      = make([]uint16, maxFSName)
		serial     uint32
		maxComp    uint32
		fileSystem uint32
	)
	r1, _, callErr := procGetVolumeInformation.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&volName[0])),
		uintptr(len(volName)),
		uintptr(unsafe.Pointer(&serial)),
		uintptr(unsafe.Pointer(&maxComp)),
		uintptr(unsafe.Pointer(&fileSystem)),
		uintptr(unsafe.Pointer(&fsBuf[0])),
		uintptr(len(fsBuf)),
	)
	if r1 == 0 {
		// LazyDLL 在成功时也可能带回非空的 Errno(0)；只有真正的非零错误码才算失败原因。
		if errno, ok := callErr.(syscall.Errno); ok && errno != 0 {
			return "", 0, fmt.Errorf("GetVolumeInformationW(%s): %w", root, errno)
		}
		return "", 0, fmt.Errorf("GetVolumeInformationW(%s): 调用失败", root)
	}
	return syscall.UTF16ToString(fsBuf), fileSystem, nil
}
