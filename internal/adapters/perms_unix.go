//go:build !windows

package adapters

// needsUnixPermCheck 在 Unix 语义有效的平台上启用 0600 判定（09 §9.4）。
func needsUnixPermCheck() bool { return true }
