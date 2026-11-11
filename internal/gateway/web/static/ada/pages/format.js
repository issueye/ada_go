// format.js —— 基座页面共用的展示辅助（与 Go 侧 relativeTime / buildRun 同口径）。

// relativeTime 把 epoch 毫秒转成「刚刚 / N 分钟前…」；0 或未来时间返回 ""。
export function relativeTime(atMS) {
	if (!atMS || atMS <= 0) return "";
	const now = Date.now();
	if (atMS >= now) return "刚刚";
	const secs = Math.floor((now - atMS) / 1000);
	if (secs < 60) return "刚刚";
	if (secs < 3600) return `${Math.floor(secs / 60)} 分钟前`;
	if (secs < 86400) return `${Math.floor(secs / 3600)} 小时前`;
	if (secs < 604800) return `${Math.floor(secs / 86400)} 天前`;
	if (secs < 2592000) return `${Math.floor(secs / 604800)} 周前`;
	if (secs < 31536000) return `${Math.floor(secs / 2592000)} 个月前`;
	return `${Math.floor(secs / 31536000)} 年前`;
}

// parseTimeMS 把 Core 的 RFC3339 时间转成 epoch 毫秒；失败返回 0。
export function parseTimeMS(raw) {
	if (!raw) return 0;
	const t = Date.parse(String(raw));
	return Number.isNaN(t) ? 0 : t;
}

// threadPath 生成会话页路径（与 Go threadPath 同形）。
export function threadPath(id) {
	return "/threads/" + encodeURIComponent(String(id ?? ""));
}

// runView 把 runStateDTO 翻译成界面文案（HEAD buildRun 的 JS 侧等价物）。
export function runView(run) {
	const r = run ?? {};
	const status = String(r.status ?? "");
	const terminal = String(r.terminal ?? "");
	const v = {
		status,
		terminal,
		startedAt: r.startedAt ?? "",
		active: false,
		label: status,
		errorLabel: runErrorLabel(terminal),
	};
	switch (status) {
		case "running":
			v.label = "运行中";
			v.active = true;
			break;
		case "waiting_input":
			v.label = "等待答复";
			break;
		case "finishing":
			v.label = "收尾中";
			v.active = true;
			break;
		case "terminal":
		case "":
			v.label = terminalLabel(terminal);
			break;
	}
	return v;
}

function terminalLabel(terminal) {
	switch (terminal) {
		case "completed": return "完成";
		case "cancelled": return "已停止";
		case "error": return "失败";
		case "interrupted": return "已中断";
		case "budget_exceeded": return "超出预算";
		default: return "空闲";
	}
}

// runErrorLabel 把终态错误映射成给用户看的下一步（HEAD runErrorLabel 文案照抄）。
export function runErrorLabel(terminal) {
	switch (terminal) {
		case "error":
			return "本轮执行失败；请检查供应商配置后重试。";
		case "interrupted":
			return "本轮被中断（网关重启或进程退出），可以重新发送。";
		case "budget_exceeded":
			return "本轮超出配置的预算；调整预算或缩短任务后重试。";
	}
	return "";
}

// toolIcon 给工具折叠行挑图标（HEAD toolIcon 的关键字归类照抄）。
export function toolIcon(name) {
	const n = String(name ?? "").toLowerCase();
	if (n.includes("git")) return "i-git-branch";
	if (n.includes("skill")) return "i-sparkles";
	if (n.includes("plan") || n.includes("todo") || n.includes("task")) return "i-list";
	if (n.includes("ask") || n.includes("question")) return "i-bot";
	if (n.includes("search") || n.includes("grep") || n.includes("find")) return "i-search";
	if (n.includes("list") || n.includes("glob") || n.includes("dir")) return "i-folder";
	if (n.includes("write") || n.includes("edit") || n.includes("patch")) return "i-edit";
	if (n.includes("read") || n.includes("file") || n.includes("cat")) return "i-file";
	if (n.includes("run") || n.includes("shell") || n.includes("exec") || n.includes("command") || n.includes("bash") || n.includes("terminal")) return "i-terminal";
	if (n.includes("fetch") || n.includes("http") || n.includes("web")) return "i-cloud";
	return "i-wrench";
}

// statusTone 把工具状态映射成 ada-badge 色调（HEAD statusTone 口径）。
export function statusTone(status) {
	switch (status) {
		case "ok": return "success";
		case "error": return "danger";
		case "denied": return "warn";
		case "timeout": return "warn";
		case "interrupted": return "warn";
		default: return "info";
	}
}

// greetingText 按小时给新建任务页一句问候（HEAD greetingText 文案照抄）。
export function greetingText(now) {
	const hour = (now ?? new Date()).getHours();
	if (hour >= 5 && hour < 11) return "上午好，开始一个新任务吧";
	if (hour >= 11 && hour < 13) return "中午好，开始一个新任务吧";
	if (hour >= 13 && hour < 18) return "下午好，开始一个新任务吧";
	return "晚上好，今天辛苦啦";
}
