package design

// 图标路径数据：24×24 网格，线性描边风格（描边宽度与端点样式由 .ds-icon 决定）。
// 约定：优先用 <path>；圆形/圆角矩形允许用 <circle>/<rect>。
// 新增图标请同时补 Tags（含被它替换掉的历史字符符号，便于检索）。
func init() {
	// ---------------- arrow：方向与层级 ----------------
	RegisterIcon(Icon{Name: "chevron-left", Group: IconGroupArrow, Tags: []string{"‹", "<", "back", "left"},
		Body: `<path d="M15 5l-7 7 7 7"/>`})
	RegisterIcon(Icon{Name: "chevron-right", Group: IconGroupArrow, Tags: []string{"›", ">", "right", "more"},
		Body: `<path d="M9 5l7 7-7 7"/>`})
	RegisterIcon(Icon{Name: "chevron-down", Group: IconGroupArrow, Tags: []string{"▾", "v", "down", "expand", "select"},
		Body: `<path d="M5 9l7 7 7-7"/>`})
	RegisterIcon(Icon{Name: "chevron-up", Group: IconGroupArrow, Tags: []string{"^", "up", "collapse"},
		Body: `<path d="M5 15l7-7 7 7"/>`})
	RegisterIcon(Icon{Name: "arrow-left", Group: IconGroupArrow, Tags: []string{"←", "return", "prev"},
		Body: `<path d="M19 12H5"/><path d="M11 6l-6 6 6 6"/>`})
	RegisterIcon(Icon{Name: "arrow-up-right", Group: IconGroupArrow, Tags: []string{"↗", "external", "share", "open"},
		Body: `<path d="M7 17L17 7"/><path d="M9 7h8v8"/>`})

	// ---------------- action：动作与工具 ----------------
	RegisterIcon(Icon{Name: "plus", Group: IconGroupAction, Tags: []string{"+", "＋", "add", "new"},
		Body: `<path d="M12 5v14M5 12h14"/>`})
	RegisterIcon(Icon{Name: "minus", Group: IconGroupAction, Tags: []string{"−", "-", "sub", "zoom-out"},
		Body: `<path d="M5 12h14"/>`})
	RegisterIcon(Icon{Name: "close", Group: IconGroupAction, Tags: []string{"✕", "×", "x", "cancel", "delete", "dismiss"},
		Body: `<path d="M6 6l12 12M18 6L6 18"/>`})
	RegisterIcon(Icon{Name: "search", Group: IconGroupAction, Tags: []string{"🔍", "find", "query"},
		Body: `<circle cx="11" cy="11" r="6"/><path d="M20 20l-3.6-3.6"/>`})
	RegisterIcon(Icon{Name: "inspect", Group: IconGroupAction, Tags: []string{"🔍", "measure", "spec", "ruler", "检查"},
		Body: `<path d="M4 9V5.5A1.5 1.5 0 015.5 4H9"/><path d="M15 4h3.5A1.5 1.5 0 0120 5.5V9"/>` +
			`<path d="M20 15v3.5a1.5 1.5 0 01-1.5 1.5H15"/><path d="M9 20H5.5A1.5 1.5 0 014 18.5V15"/>` +
			`<path d="M9.5 9.5h5v5h-5z"/>`})
	RegisterIcon(Icon{Name: "theme", Group: IconGroupAction, Tags: []string{"🌗", "🌓", "dark", "light", "contrast"},
		Body: `<circle cx="12" cy="12" r="8"/><path d="M12 4a8 8 0 010 16z" fill="currentColor" stroke="none"/>`})
	RegisterIcon(Icon{Name: "more", Group: IconGroupAction, Tags: []string{"⋯", "...", "ellipsis", "menu"},
		Body: `<circle cx="6" cy="12" r="1.3"/><circle cx="12" cy="12" r="1.3"/><circle cx="18" cy="12" r="1.3"/>`})
	RegisterIcon(Icon{Name: "trash", Group: IconGroupAction, Tags: []string{"delete", "remove"},
		Body: `<path d="M4 7h16"/><path d="M9.5 7V5h5v2"/><path d="M6.5 7l1 13h9l1-13"/>`})
	RegisterIcon(Icon{Name: "note", Group: IconGroupAction, Tags: []string{"sticky", "memo", "便签"},
		Body: `<path d="M5 5h14v9l-5 5H5z"/><path d="M19 14h-5v5"/>`})
	RegisterIcon(Icon{Name: "fit", Group: IconGroupAction, Tags: []string{"maximize", "fullscreen", "zoom-fit"},
		Body: `<path d="M4 9V4h5"/><path d="M20 9V4h-5"/><path d="M4 15v5h5"/><path d="M20 15v5h-5"/>`})
	RegisterIcon(Icon{Name: "ratio", Group: IconGroupAction, Tags: []string{"比例", "aspect", "frame", "画布比例"},
		Body: `<rect x="3" y="5" width="18" height="14" rx="2"/><rect x="6.5" y="8.5" width="11" height="7" rx="1"/>`})
	RegisterIcon(Icon{Name: "center", Group: IconGroupAction, Tags: []string{"居中", "align-center", "focus", "target"},
		Body: `<circle cx="12" cy="12" r="6"/><path d="M12 3v3M12 18v3M3 12h3M18 12h3"/>`})
	RegisterIcon(Icon{Name: "link", Group: IconGroupAction, Tags: []string{"connect", "chain", "关联"},
		Body: `<path d="M10.5 13.5a3.5 3.5 0 005 0l2.5-2.5a3.5 3.5 0 00-5-5l-1 1"/>` +
			`<path d="M13.5 10.5a3.5 3.5 0 00-5 0L6 13a3.5 3.5 0 005 5l1-1"/>`})
	RegisterIcon(Icon{Name: "undo", Group: IconGroupAction, Tags: []string{"revert", "restore", "back"},
		Body: `<path d="M4 10h9a5 5 0 010 10H8"/><path d="M8 6l-4 4 4 4"/>`})
	RegisterIcon(Icon{Name: "settings", Group: IconGroupAction, Tags: []string{"config", "preference", "sliders"},
		Body: `<path d="M4 8h9"/><path d="M17 8h3"/><path d="M4 16h3"/><path d="M11 16h9"/>` +
			`<circle cx="15" cy="8" r="2"/><circle cx="9" cy="16" r="2"/>`})

	// ---------------- status：状态与反馈 ----------------
	RegisterIcon(Icon{Name: "check", Group: IconGroupStatus, Tags: []string{"✓", "✔", "ok", "done", "success"},
		Body: `<path d="M4 12.5l5 5L20 6.5"/>`})
	RegisterIcon(Icon{Name: "check-circle", Group: IconGroupStatus, Tags: []string{"✓", "success", "passed"},
		Body: `<circle cx="12" cy="12" r="8.5"/><path d="M8 12.2l2.8 2.8L16 9.5"/>`})
	RegisterIcon(Icon{Name: "info", Group: IconGroupStatus, Tags: []string{"i", "notice", "hint"},
		Body: `<circle cx="12" cy="12" r="8.5"/><path d="M12 11v6"/><path d="M12 7.4v.9"/>`})
	RegisterIcon(Icon{Name: "warning", Group: IconGroupStatus, Tags: []string{"⚠", "!", "alert", "caution"},
		Body: `<path d="M12 4l8.5 15h-17z"/><path d="M12 10v4"/><path d="M12 16.6v.9"/>`})
	RegisterIcon(Icon{Name: "danger", Group: IconGroupStatus, Tags: []string{"✕", "error", "fail", "close-circle"},
		Body: `<circle cx="12" cy="12" r="8.5"/><path d="M9.2 9.2l5.6 5.6"/><path d="M14.8 9.2l-5.6 5.6"/>`})
	RegisterIcon(Icon{Name: "approx", Group: IconGroupStatus, Tags: []string{"≈", "near", "token", "近似"},
		Body: `<path d="M4 9.5c2-2 4-2 6 0s4 2 6 0"/><path d="M4 15c2-2 4-2 6 0s4 2 6 0"/>`})
	RegisterIcon(Icon{Name: "spinner", Group: IconGroupStatus, Tags: []string{"loading", "wait", "progress"},
		Body: `<path d="M12 4a8 8 0 018 8" opacity=".9"/><path d="M20 12a8 8 0 01-8 8"/>` +
			`<path d="M12 20a8 8 0 01-8-8"/><path d="M4 12a8 8 0 018-8"/>`})

	// ---------------- nav：导航入口 ----------------
	RegisterIcon(Icon{Name: "home", Group: IconGroupNav, Tags: []string{"◉", "index", "首页", "main"},
		Body: `<path d="M4 11l8-7 8 7"/><path d="M6.5 9.5V20h11V9.5"/>`})
	RegisterIcon(Icon{Name: "compass", Group: IconGroupNav, Tags: []string{"◎", "discover", "explore", "发现"},
		Body: `<circle cx="12" cy="12" r="8.5"/><path d="M15.2 8.8l-1.7 4.7-4.7 1.7 1.7-4.7z"/>`})
	RegisterIcon(Icon{Name: "message", Group: IconGroupNav, Tags: []string{"▢", "💬", "comment", "chat", "消息", "comment-bubble"},
		Body: `<path d="M20 11.8c0 3.3-3.6 6-8 6-1 0-1.9-.14-2.8-.4L4 19l1.2-3.3A5.9 5.9 0 014 11.8C4 8.5 7.6 5.8 12 5.8s8 2.7 8 6z"/>`})
	RegisterIcon(Icon{Name: "user", Group: IconGroupNav, Tags: []string{"○", "profile", "account", "我的", "avatar"},
		Body: `<circle cx="12" cy="9" r="3.5"/><path d="M5 20c1.2-3.4 3.9-5 7-5s5.8 1.6 7 5"/>`})
	RegisterIcon(Icon{Name: "bell", Group: IconGroupNav, Tags: []string{"notice", "notification", "remind"},
		Body: `<path d="M8 9.5a4 4 0 018 0c0 3.6 1.4 5.2 1.4 5.2H6.6S8 13.1 8 9.5z"/>` +
			`<path d="M10.4 17.6a1.7 1.7 0 003.2 0"/>`})

	// ---------------- media：内容互动 ----------------
	RegisterIcon(Icon{Name: "heart", Group: IconGroupMedia, Tags: []string{"♥", "like", "favorite", "赞"},
		Body: `<path d="M12 19.6S4.6 15.1 4.6 10.3A4.1 4.1 0 0112 7.7a4.1 4.1 0 017.4 2.6c0 4.8-7.4 9.3-7.4 9.3z"/>`})
	RegisterIcon(Icon{Name: "share", Group: IconGroupMedia, Tags: []string{"↗", "send", "forward", "分享"},
		Body: `<path d="M13.5 5H19v5.5"/><path d="M19 5l-7 7"/>` +
			`<path d="M17.5 13.5V18a1.5 1.5 0 01-1.5 1.5H5.5A1.5 1.5 0 014 18V7.5A1.5 1.5 0 015.5 6H10"/>`})
	RegisterIcon(Icon{Name: "image", Group: IconGroupMedia, Tags: []string{"photo", "picture", "media"},
		Body: `<rect x="4" y="5" width="16" height="14" rx="2.5"/><path d="M5 15.5l4-3.5 3.2 2.8 2.8-2.3 4 3.5"/>` +
			`<circle cx="9.2" cy="9.6" r="1.2"/>`})
	RegisterIcon(Icon{Name: "play", Group: IconGroupMedia, Tags: []string{"video", "start"},
		Body: `<circle cx="12" cy="12" r="8.5"/><path d="M10 8.8l5 3.2-5 3.2z"/>`})

	// ---------------- data：列表与数据 ----------------
	RegisterIcon(Icon{Name: "list", Group: IconGroupData, Tags: []string{"rows", "table", "列表"},
		Body: `<path d="M4 7h16"/><path d="M4 12h16"/><path d="M4 17h16"/>`})
	RegisterIcon(Icon{Name: "folder", Group: IconGroupData, Tags: []string{"分组", "目录", "project", "group"},
		Body: `<path d="M4 7.5A1.5 1.5 0 015.5 6h3.2c.4 0 .8.2 1 .5L11 8h7.5A1.5 1.5 0 0120 9.5v8A1.5 1.5 0 0118.5 19h-13A1.5 1.5 0 014 17.5z"/>`})
	RegisterIcon(Icon{Name: "folder-open", Group: IconGroupData, Tags: []string{"展开", "分组展开"},
		Body: `<path d="M4 8V6.5A1.5 1.5 0 015.5 5h3.2c.4 0 .8.2 1 .5L11 7h6.5A1.5 1.5 0 0119 8.5V10"/>` +
			`<path d="M4 10h15.2a1 1 0 01.96 1.28l-1.6 5.4A1.5 1.5 0 0117.1 18H5.5A1.5 1.5 0 014 16.5z"/>`})
	RegisterIcon(Icon{Name: "grid", Group: IconGroupData, Tags: []string{"layout", "collections", "网格"},
		Body: `<rect x="4" y="4" width="7" height="7" rx="1.5"/><rect x="13" y="4" width="7" height="7" rx="1.5"/>` +
			`<rect x="4" y="13" width="7" height="7" rx="1.5"/><rect x="13" y="13" width="7" height="7" rx="1.5"/>`})
	RegisterIcon(Icon{Name: "refresh", Group: IconGroupData, Tags: []string{"reload", "sync", "重试"},
		Body: `<path d="M20 12a8 8 0 10-2.6 5.9"/><path d="M20 6.5V12h-5.5"/>`})
}
