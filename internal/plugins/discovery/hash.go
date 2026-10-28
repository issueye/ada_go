package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
)

// fileItem 是指纹计算的一个文件项（相对路径 + 内容）。
type fileItem struct {
	rel  string
	data []byte
}

// hashItems 把排序后的（相对路径, 内容）序列压进 sha256：路径与长度都进入摘要，
// 避免不同拼接方式碰撞。指纹取 16 个十六进制字符（与 Core 其它指纹同宽度）。
func hashItems(items []fileItem) (string, error) {
	h := sha256.New()
	for _, it := range items {
		if _, err := fmt.Fprintf(h, "%s\x00%d\x00", it.rel, len(it.data)); err != nil {
			return "", err
		}
		if _, err := h.Write(it.data); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// isAbsoluteZipName 报告 zip 条目名是否是绝对路径（/开头、盘符、UNC 或反斜杠）。
func isAbsoluteZipName(name string) bool {
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return true
	}
	if len(name) >= 2 && name[1] == ':' &&
		((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z')) {
		return true
	}
	if strings.HasPrefix(name, `\\`) {
		return true
	}
	return false
}

// hasEscapeSegment 报告路径里是否有 .. 段（zip 路径穿越的唯一入口）。
func hasEscapeSegment(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// cleanZipRel 归一 zip 条目的相对路径（去掉顶层前缀后）。
func cleanZipRel(rel string) string {
	return path.Clean(strings.TrimPrefix(rel, "./"))
}
