package web_test

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/web"
)

// 侧栏直接列出全部工作区：当前工作区展开并渲染会话，其它工作区折叠等待懒加载。
func TestSidebarListsWorkspacesWithLazyGroups(t *testing.T) {
	wsA, wsB := t.TempDir(), t.TempDir()
	core := newFakeCore().on("thread.list", `{"threads":[
		{"id":"th_a","title":"A 的会话","updatedAt":"2026-11-04T00:00:00Z","archived":false}
	],"nextCursor":"","hasMore":false}`)
	srv, _ := newServerWithWorkspaces(t, core, []string{wsA, wsB})

	body := serve(t, srv, "GET", "/", nil).Body.String()
	for _, want := range []string{
		"其它工作区",
		`ada-thread-list--split`,
		`data-ws-active`, // 当前工作区固定块
		`data-ws-others`, // 其它工作区独立滚动块
		`data-open-workspace`,
		`data-ws-path="` + wsB + `"`,
		`data-ws-index="1"`,
		"A 的会话",
		`data-ws-threads`,
		`展开以加载会话…`, // 非当前工作区折叠时的占位
		`/?ws=0`,   // 当前工作区组头的「在此新建」
	} {
		wantContains(t, body, want)
	}
	// 其它工作区的会话不在首屏渲染（展开时懒加载），只留占位。
	wantNotContains(t, body, "B 的会话")
}

// /partials/workspace-threads 只查指定工作区的 Core，返回带 ?ws=<index> 的会话行。
func TestWorkspaceThreadsPartial(t *testing.T) {
	wsA, wsB := t.TempDir(), t.TempDir()
	core := newFakeCore().on("thread.list", `{"threads":[
		{"id":"th_b","title":"B 的会话","updatedAt":"2026-11-04T00:00:00Z","archived":false}
	],"nextCursor":"","hasMore":false}`)
	srv, sup := newServerWithWorkspaces(t, core, []string{wsA, wsB})

	rec := serve(t, srv, "GET", "/partials/workspace-threads?path="+url.QueryEscape(wsB), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
	for _, want := range []string{
		"B 的会话",
		`href="/threads/th_b?ws=1"`,
		`action="/threads/th_b/delete?ws=1"`,
	} {
		wantContains(t, rec.Body.String(), want)
	}
	if got := sup.lastWorkspace(); got != wsB {
		t.Fatalf("懒加载应只查询目标工作区，实际 %q", got)
	}

	missing := serve(t, srv, "GET", "/partials/workspace-threads?path="+url.QueryEscape(filepath.Join(wsA, "missing")), nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("未知工作区 = %d，期望 404", missing.Code)
	}
}

// 从侧栏点进别的工作区的会话：?ws=<index> 决定本次请求的 Core，并写回 cookie。
func TestThreadLinkSwitchesWorkspaceByQuery(t *testing.T) {
	wsA, wsB := t.TempDir(), t.TempDir()
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesAscending).
		on("thread.list", emptyThreadList)
	srv, sup := newServerWithWorkspaces(t, core, []string{wsA, wsB})

	rec := serve(t, srv, "GET", "/threads/th_1?ws=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := sup.lastWorkspace(); got != wsB {
		t.Fatalf("应使用 ?ws=1 指定的工作区，实际 %q", got)
	}
	var chosen *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.WorkspaceCookieName {
			chosen = c
		}
	}
	if chosen == nil || chosen.Value != "1" {
		t.Fatalf("应从 query 写回工作区 cookie，得到 %+v", chosen)
	}
}
