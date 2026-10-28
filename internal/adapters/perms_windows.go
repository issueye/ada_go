//go:build windows

package adapters

// needsUnixPermCheck 在 Windows 上跳过 Unix 权限位判定：Chmod 只影响只读位，
// 真正的隔离靠 NTFS ACL 与「数据目录属于当前用户」这一部署前提（09 §9.4）。
func needsUnixPermCheck() bool { return false }
