package web

import "testing"

// 工具折叠行的图标按工具名归类：同类工具画同一个图标，认不出来的退回扳手。
func TestToolIconMapping(t *testing.T) {
	cases := map[string]string{
		"list_files":   "i-folder",
		"read_file":    "i-file",
		"write_file":   "i-edit",
		"edit_file":    "i-edit",
		"search_files": "i-search",
		"git_diff":     "i-git-branch",
		"git_status":   "i-git-branch",
		"run_command":  "i-terminal",
		"update_plan":  "i-list",
		"read_plan":    "i-list",
		"ask_user":     "i-bot",
		"skill_search": "i-sparkles", // 先归 skill，再归 search
		"web_fetch":    "i-cloud",
		"totally_new":  "i-wrench",
		"":             "i-wrench",
	}
	for name, want := range cases {
		if got := toolIcon(name); got != want {
			t.Errorf("toolIcon(%q) = %q，want %q", name, got, want)
		}
	}
}
