// state.js —— shell 级共享状态（基座页面 ↔ ShellApp 布局的轻量通道）。
//
// 页面 setup 里写 title/topbar.actions，ShellApp 的 topbar 读取渲染；
// 侧栏开合由 topbar 的菜单按钮与遮罩共同驱动。actions 的 onClick
// 由页面自己提供，离开页面时（onUnmounted）自行清理。
//
// 侧栏两种形态（与 HEAD app.js §3 同一套语义；SPA 重建时整段丢失过一次）：
//   · 宽屏（>760px）：.ada-shell.is-sidebar-collapsed 收成 56px 窄轨，
//     偏好存 localStorage['a_da_sidebar_collapsed']；
//   · 窄屏（≤760px）：.ada-sidebar.is-open + 遮罩的抽屉。
// 断点必须与 ui/pages.css 的 @media (max-width: 760px) 保持同值，否则
// 「点菜单按钮没有效果」——按钮改了状态，但没有任何 CSS 规则响应它。

import { reactive } from "/static/ada/ada-runtime.mjs";

// 抽屉断点：与 pages.css 的侧栏抽屉媒体查询逐字一致。
const NARROW_QUERY = "(max-width: 760px)";
const COLLAPSE_KEY = "a_da_sidebar_collapsed";

function narrowNow() {
	if (typeof window === "undefined" || typeof window.matchMedia !== "function") return false;
	try {
		return window.matchMedia(NARROW_QUERY).matches;
	} catch {
		return false;
	}
}

function readCollapsed() {
	try {
		// 窄屏没有折叠窄轨（抽屉态强制完整侧栏），启动时不能残留折叠状态。
		if (narrowNow()) return false;
		return window.localStorage.getItem(COLLAPSE_KEY) === "1";
	} catch {
		return false;
	}
}

function writeCollapsed(collapsed) {
	try {
		window.localStorage.setItem(COLLAPSE_KEY, collapsed ? "1" : "0");
	} catch {
		/* 隐私模式不可用：本次会话内仍生效 */
	}
}

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
	// 侧栏抽屉（窄屏）：is-open 类由 Sidebar.vue 绑定；遮罩由 ShellApp 绑定。
	sidebarOpen: false,
	// 侧栏折叠（宽屏）：is-sidebar-collapsed 类由 ShellApp 绑定。
	collapsed: readCollapsed(),
	// isNarrow 是断点的响应式镜像：菜单按钮的文案/行为要跟着它切换，
	// 而 matchMedia 本身不参与响应式，所以用一个字段 + change 监听镜像。
	isNarrow: narrowNow(),
	// 菜单按钮的唯一入口（HEAD app.js 的 data-sidebar-toggle）：
	// 窄屏切抽屉，宽屏切折叠窄轨。
	toggleAppearance() {
		if (this.isNarrow) {
			this.sidebarOpen = !this.sidebarOpen;
			return;
		}
		this.setCollapsed(!this.collapsed);
	},
	setCollapsed(collapsed) {
		this.collapsed = Boolean(collapsed);
		writeCollapsed(this.collapsed);
	},
	closeSidebar() {
		this.sidebarOpen = false;
	},
	// 跨断点清理另一套形态：宽屏回到存储的折叠偏好，窄屏清掉折叠与抽屉。
	applyBreakpoint() {
		this.isNarrow = narrowNow();
		this.sidebarOpen = false;
		if (this.isNarrow) {
			this.collapsed = false;
			return;
		}
		this.collapsed = readCollapsed();
	},
	narrow() {
		return this.isNarrow;
	},
});
