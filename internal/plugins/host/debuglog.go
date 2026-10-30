package host

// 出网调试日志（adag --debug）：把每次 provider / ctx.fetch 请求与响应摘要写到
// 指定 writer。默认关闭；写出前统一脱敏，避免调试输出泄漏 API Key。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	debugRequestBodyLimit  = 32 << 10
	debugResponseBodyLimit = 8 << 10
)

// DebugLog 是并发安全的出网调试输出。多个请求交错执行时按整块写出，
// 不会把两个请求的行混在一起。
type DebugLog struct {
	mu  sync.Mutex
	w   io.Writer
	seq atomic.Int64
}

// NewDebugLog 包装 writer；w 为空时返回 nil（所有方法对 nil 安全）。
func NewDebugLog(w io.Writer) *DebugLog {
	if w == nil {
		return nil
	}
	return &DebugLog{w: w}
}

// NextID 取下一个请求序号；nil 时返回 0。
func (d *DebugLog) NextID() int64 {
	if d == nil {
		return 0
	}
	return d.seq.Add(1)
}

// Request 输出一次出网请求（方法、URL、脱敏后的头与请求体）。
func (d *DebugLog) Request(id int64, method, rawURL string, header http.Header, body []byte) {
	if d == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[adag:debug] >>> #%d %s %s\n", id, method, rawURL)
	keys := make([]string, 0, len(header))
	for k := range header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range header.Values(k) {
			fmt.Fprintf(&b, "[adag:debug]     %s: %s\n", k, redactDebugHeader(k, v))
		}
	}
	if len(body) > 0 {
		fmt.Fprintf(&b, "[adag:debug]     body:\n%s", debugIndent(debugBodyText(body, debugRequestBodyLimit)))
	}
	d.write(b.String())
}

// Response 输出响应状态、耗时与（非流式）响应体摘要。
func (d *DebugLog) Response(id int64, status string, header http.Header, elapsed time.Duration, body []byte) {
	if d == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[adag:debug] <<< #%d %s (%s)", id, status, elapsed.Round(time.Millisecond))
	if ct := header.Get("Content-Type"); ct != "" {
		fmt.Fprintf(&b, " content-type: %s", ct)
	}
	b.WriteString("\n")
	if len(body) > 0 && !strings.HasPrefix(header.Get("Content-Type"), "text/event-stream") {
		fmt.Fprintf(&b, "[adag:debug]     body:\n%s", debugIndent(debugBodyText(body, debugResponseBodyLimit)))
	}
	d.write(b.String())
}

// Error 输出请求失败（网络错误、超时、取消等）。
func (d *DebugLog) Error(id int64, err error, elapsed time.Duration) {
	if d == nil || err == nil {
		return
	}
	d.write(fmt.Sprintf("[adag:debug] !!! #%d failed after %s: %s\n",
		id, elapsed.Round(time.Millisecond), redactDebugText(err.Error())))
}

func (d *DebugLog) write(s string) {
	if d == nil || s == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, _ = io.WriteString(d.w, s)
}

// debugSensitiveHeaders 是整值脱敏的头；其余头原样输出。
var debugSensitiveHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"x-api-key":           true,
	"api-key":             true,
	"cookie":              true,
	"set-cookie":          true,
}

var (
	debugBearerRe = regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{4,}`)
	debugKeyRe    = regexp.MustCompile(`\b(sk|pk|rk)-[A-Za-z0-9_-]{6,}`)
	debugJSONRe   = regexp.MustCompile(`(?i)("(?:api[_-]?key|x-api-key|authorization|token|secret|access[_-]?key|password)"\s*:\s*)"[^"]*"`)
)

func redactDebugHeader(name, value string) string {
	if debugSensitiveHeaders[strings.ToLower(strings.TrimSpace(name))] {
		return "***"
	}
	return redactDebugText(value)
}

// redactDebugText 兜底处理请求体/错误文本里出现的密钥形态。
func redactDebugText(s string) string {
	s = debugBearerRe.ReplaceAllString(s, "${1}***")
	s = debugKeyRe.ReplaceAllString(s, "$1-***")
	s = debugJSONRe.ReplaceAllString(s, `${1}"***"`)
	return s
}

// debugBodyText 截断（超限时），尽量美化成缩进 JSON，再脱敏。
func debugBodyText(raw []byte, limit int) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	if len(text) > limit {
		return redactDebugText(truncateUTF8(text, limit)) + "\n…(truncated)"
	}
	var v any
	if err := json.Unmarshal([]byte(text), &v); err == nil {
		if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
			text = string(pretty)
		}
	}
	if len(text) > limit {
		text = truncateUTF8(text, limit) + "\n…(truncated)"
	}
	return redactDebugText(text)
}

func debugIndent(body string) string {
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString("[adag:debug]     | ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// truncateUTF8 在字节上限内截断，且不切坏多字节字符。
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}
