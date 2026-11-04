package cronexpr

import (
	"testing"
	"time"
)

func TestParseErrors(t *testing.T) {
	bad := []string{
		"", "0 9 * *", "0 9 * * * *", // 段数
		"60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *", "* * * 13 *", "* * * * 8", // 越界
		"*/0 * * * *", "a * * * *", "5-1 * * * *", "*,, * * * *", // 形状
	}
	for _, expr := range bad {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q) 应报错", expr)
		}
	}
}

func TestNextBasics(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("构造时间 %s: %v", s, err)
		}
		return v
	}
	cases := []struct{ expr, after, want string }{
		{"0 9 * * *", "2026-11-04T10:00:00Z", "2026-11-05T09:00:00Z"},
		{"0 9 * * *", "2026-11-04T08:30:00Z", "2026-11-04T09:00:00Z"},
		{"*/15 * * * *", "2026-11-04T08:47:00Z", "2026-11-04T09:00:00Z"},
		{"30 9 1 * *", "2026-11-04T08:00:00Z", "2026-12-01T09:30:00Z"},
		{"0 0 * * 1", "2026-11-04T00:00:00Z", "2026-11-09T00:00:00Z"},
		{"0 12 * * 7", "2026-11-04T00:00:00Z", "2026-11-08T12:00:00Z"},
		{"0 9 * * *", "2026-11-04T09:00:00Z", "2026-11-05T09:00:00Z"},
	}
	for _, tc := range cases {
		s, err := ParseCron(tc.expr)
		if err != nil {
			t.Fatalf("ParseCron(%q): %v", tc.expr, err)
		}
		if got := s.Next(at(tc.after)); got.Format(time.RFC3339) != tc.want {
			t.Errorf("Next(%q, after %s) = %s，期望 %s", tc.expr, tc.after, got.Format(time.RFC3339), tc.want)
		}
	}
}

func TestNextVixieRule(t *testing.T) {
	// dom 与 dow 都受限：取「或」。2026-11-04 是周三。
	s, _ := ParseCron("0 9 5 * 5")
	if got := s.Next(time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)); got.Format(time.RFC3339) != "2026-11-05T09:00:00Z" {
		t.Errorf("OR 规则 = %s", got)
	}
	s, _ = ParseCron("0 9 * * 5")
	if got := s.Next(time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)); got.Format(time.RFC3339) != "2026-11-06T09:00:00Z" {
		t.Errorf("dow 规则 = %s", got)
	}
}

func TestNextImpossible(t *testing.T) {
	s, _ := ParseCron("0 0 30 2 *")
	if got := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !got.IsZero() {
		t.Errorf("不存在的日期应返回零值，得到 %s", got)
	}
}
