// state.js —— shell 级共享状态（基座页面 ↔ ShellApp 布局的轻量通道）。
//
// 页面 setup 里写 title/topbar.actions，ShellApp 的 topbar 读取渲染；
// 侧栏抽屉开合由 topbar 的菜单按钮与遮罩共同驱动。actions 的 onClick
// 由页面自己提供，离开页面时（onUnmounted）自行清理。

import { reactive } from "/static/ada/ada-runtime.mjs";

// 窄屏断点与 HEAD app.js 同值：860px 以下侧栏是抽屉。
const NARROW_QUERY = "(max-width: 860px)";

export const shellState = reactive({
	// routePath 由 index.mjs 的 router 订阅同步；页面据此判断激活态。
	routePath: typeof window !== "undefined" ? window.location.pathname : "/",
	// topbar 标题：页面 setup 里按 HEAD 语义设置（概览/插件/工作区/会话标题）。
	title: "",
	// topbar 动作（会话页的刷新/停止等）：{key, icon?, label, title?, size?, onClick}。
	topbar: {
		actions: [],
		set(actions) {
			this.actions.splice(0, this.actions.length, ...actions);
		},
		clear() {
			this.actions.splice(0, this.actions.length);
		},
	},
	// 侧栏抽屉（窄屏）：is-open 类由 Sidebar.vue 绑定。
	sidebarOpen: false,
	toggleSidebar() {
		this.sidebarOpen = !this.sidebarOpen;
	},
	closeSidebar() {
		this.sidebarOpen = false;
	},
	narrow() {
		return typeof window !== "undefined" && typeof window.matchMedia === "function"
			? window.matchMedia(NARROW_QUERY).matches
			: false;
	},
});
