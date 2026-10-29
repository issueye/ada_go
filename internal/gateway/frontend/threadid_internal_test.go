package frontend

import (
	"net/http/httptest"
	"testing"
)

// 页面路由是 /threads/{id}（a_da 同形），旧路径 /thread/{id} 仍兼容；
// 槽位取数（Todo 计划 / 待答交互）依赖这里识别线程。
func TestThreadIDFromRequestSupportsThreadsPath(t *testing.T) {
	cases := map[string]string{
		"/threads/abc":                  "abc",
		"/threads/abc/send":             "abc",
		"/thread/abc":                   "abc",
		"/threads/abc?threadId=zzz":     "zzz",
		"/threads/":                     "",
		"/ext/provider-admin/providers": "",
	}
	for path, want := range cases {
		r := httptest.NewRequest("GET", path, nil)
		if got := threadIDFromRequest(r); got != want {
			t.Fatalf("threadIDFromRequest(%q) = %q, want %q", path, got, want)
		}
	}
}
