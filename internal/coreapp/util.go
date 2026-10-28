package coreapp

import (
	"regexp"
	"strings"
)

// mustCompile 在包初始化时编译正则；失败即 panic（只在字面量上使用）。
func mustCompile(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}

// isLocale 做宽松的 BCP47 形状检查：字母段 + 可选区域/变体，用 "-" 分隔，总长 ≤ 35。
//
// 消费者是界面语言选择（8.6 identity.locale）；这里不追求完整 RFC 校验，
// 但拒绝空值、空格与非法字符，避免把无意义值传到前端。
func isLocale(s string) bool {
	if s == "" || len(s) > 35 {
		return false
	}
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if p == "" {
			return false
		}
		if i == 0 {
			if len(p) < 2 || len(p) > 8 {
				return false
			}
		}
		for _, r := range p {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !ok {
				return false
			}
		}
	}
	return true
}
