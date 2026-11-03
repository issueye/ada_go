// cronexpr.go 是 5 段标准 cron 表达式解析（定时任务模块）。
//
// 语法：分 时 日 月 周（minute hour dom month dow），支持 `*`、列表 `,`、
// 范围 `a-b`、步进 `*/n` 与 `a-b/n`（`a/n` 视作 `a-max/n`）；周字段 0 与 7 都是周日。
// 语义沿用 Vixie cron：dom 与 dow 都受限（非 `*`）时取「或」，否则取「且」。
// 时间按表达式的本地时间解释；`Next` 逐分钟前进，上限 4 年（覆盖闰年）。
//
// 该实现与独立二进制 mcp_projects/cron 的 internal/cronexpr 保持同一份语义：
// 两侧注释互指，改动必须同步（契约见 internal/cronstore/cronstore.go 顶部）。
package cronstore

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// maxLookaheadMinutes 是 Next 的搜索上限：4 年的分钟数（覆盖一个闰日周期）。
const maxLookaheadMinutes = 366*4*24*60 + 1440

// fieldBits 是一个字段的命中位图（下标即字段值，容量覆盖 0..31 与周字段 0..7）。
type fieldBits [64]bool

// Schedule 是一颗解析后的 cron 表达式：每个字段一组命中位图。
type Schedule struct {
	minutes       fieldBits
	hours         fieldBits
	doms          fieldBits // 下标 1..31
	months        fieldBits // 下标 1..12
	dows          fieldBits // 0=周日 .. 6=周六（7 已折回 0）
	domRestricted bool
	dowRestricted bool
}

// ParseCron 解析 5 段 cron 表达式；错误消息可直接展示给用户。
func ParseCron(expr string) (*Schedule, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron 表达式需要 5 段（分 时 日 月 周），收到 %d 段", len(fields))
	}
	s := &Schedule{}
	var err error
	if s.minutes, err = parseField(fields[0], 0, 59, "分"); err != nil {
		return nil, err
	}
	if s.hours, err = parseField(fields[1], 0, 23, "时"); err != nil {
		return nil, err
	}
	if s.doms, err = parseField(fields[2], 1, 31, "日"); err != nil {
		return nil, err
	}
	if s.months, err = parseField(fields[3], 1, 12, "月"); err != nil {
		return nil, err
	}
	dows, err := parseField(fields[4], 0, 7, "周")
	if err != nil {
		return nil, err
	}
	// 周字段 7 视作 0（周日）。
	if dows[7] {
		dows[0] = true
	}
	s.dows = dows
	s.domRestricted = fields[2] != "*"
	s.dowRestricted = fields[4] != "*"
	return s, nil
}

// Next 返回 after 之后（不含 after 所在分钟）的下一个触发时刻；找不到返回零值。
func (s *Schedule) Next(after time.Time) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < maxLookaheadMinutes; i++ {
		if s.matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// matches 报告 t 是否命中本调度（按 t 自身时区的字段值判断）。
func (s *Schedule) matches(t time.Time) bool {
	if !s.minutes[t.Minute()] || !s.hours[t.Hour()] || !s.months[int(t.Month())] {
		return false
	}
	domHit := s.doms[t.Day()]
	dowHit := s.dows[int(t.Weekday())]
	switch {
	case s.domRestricted && s.dowRestricted:
		return domHit || dowHit // Vixie 规则：两者都受限取「或」
	case s.domRestricted:
		return domHit
	case s.dowRestricted:
		return dowHit
	default:
		return true
	}
}

// parseField 解析单个字段：逗号分隔的项，每项是 `*`、`a`、`a-b`，可带 `/n` 步进。
func parseField(field string, min, max int, label string) (fieldBits, error) {
	var out fieldBits
	if strings.TrimSpace(field) == "" {
		return out, fmt.Errorf("cron 的%s字段为空", label)
	}
	for _, item := range strings.Split(field, ",") {
		if item == "" {
			return out, fmt.Errorf("cron 的%s字段有空的列表项: %q", label, field)
		}
		base, step := item, 1
		if i := strings.IndexByte(item, '/'); i >= 0 {
			base = item[:i]
			n, err := strconv.Atoi(item[i+1:])
			if err != nil || n < 1 {
				return out, fmt.Errorf("cron 的%s字段步进 %q 非法（需为正整数）", label, item[i+1:])
			}
			step = n
		}
		lo, hi := min, max
		explicitStep := strings.ContainsRune(item, '/')
		switch {
		case base == "*":
		case strings.ContainsRune(base, '-'):
			parts := strings.SplitN(base, "-", 2)
			a, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			b, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err1 != nil || err2 != nil || a < min || b > max || a > b {
				return out, fmt.Errorf("cron 的%s字段范围 %q 非法（应在 %d-%d 内且起点≤终点）", label, base, min, max)
			}
			lo, hi = a, b
		default:
			a, err := strconv.Atoi(strings.TrimSpace(base))
			if err != nil || a < min || a > max {
				return out, fmt.Errorf("cron 的%s字段值 %q 非法（应在 %d-%d 内）", label, base, min, max)
			}
			if !explicitStep {
				out[a] = true
				continue
			}
			// `a/n` 视作 a-max/n（宽松语义，与常见 cron 实现一致）。
			lo = a
		}
		for v := lo; v <= hi; v += step {
			out[v] = true
		}
	}
	return out, nil
}
