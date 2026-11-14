// sidebar-width.js —— 侧栏宽度拖拽（宽屏展开态）的唯一实现。
//
// 单独成模块（而不是塞在 Sidebar.js 的 setup 里）是为了可测试：这里只碰
// 「句柄 + window 指针事件 + 一个 CSS 变量 + localStorage」，不依赖组件实例。
// 行为与 HEAD app.js §3 同源：
//   · 拖动只改 CSS 变量 --ada-sidebar-width（sidebar-boot.js 在首帧前预置同一
//     个变量，避免先画 264 再跳变），松手才落 localStorage；
//   · 键盘可达：←/→ 16px、Home/End 到边界、双击恢复默认；
//   · 窄屏抽屉与折叠窄轨不参与（由调用方通过 isDisabled 判定）。
//
// 环境通过参数注入（默认取全局 document/window），node 测试可直接给假对象。

export const SIDEBAR_WIDTH_KEY = "a_da_sidebar_width";
export const SIDEBAR_WIDTH_MIN = 200;
export const SIDEBAR_WIDTH_MAX = 420;
export const SIDEBAR_WIDTH_DEFAULT = 264;
// ui/pages.css 的窄屏档（≤1100px 侧栏是 236）：没有存过宽度时按这一档起步，
// 否则拖动条一绑就把断点算好的宽度覆盖成 264。
const COMPACT_BREAKPOINT = 1100;
const COMPACT_WIDTH = 236;
const KEYBOARD_STEP = 16;

export function clampSidebarWidth(width, viewportWidth) {
	const max = Math.min(SIDEBAR_WIDTH_MAX, Math.max(SIDEBAR_WIDTH_MIN, viewportWidth - 360));
	return Math.round(Math.min(Math.max(width, SIDEBAR_WIDTH_MIN), max));
}

export function readSidebarWidth(win) {
	const w = win ?? (typeof window === "undefined" ? null : window);
	if (w === null) return SIDEBAR_WIDTH_DEFAULT;
	try {
		const stored = Number(w.localStorage.getItem(SIDEBAR_WIDTH_KEY));
		if (Number.isFinite(stored) && stored >= SIDEBAR_WIDTH_MIN && stored <= SIDEBAR_WIDTH_MAX) return stored;
	} catch {
		/* 存储不可用：用默认宽度 */
	}
	return w.innerWidth <= COMPACT_BREAKPOINT ? COMPACT_WIDTH : SIDEBAR_WIDTH_DEFAULT;
}

export function applySidebarWidth(width, persist, env) {
	const { doc, win } = resolveEnv(env);
	const value = clampSidebarWidth(width, win.innerWidth);
	doc.documentElement.style.setProperty("--ada-sidebar-width", value + "px");
	const handle = doc.querySelector("[data-sidebar-resizer]");
	if (handle !== null && handle !== undefined) handle.setAttribute("aria-valuenow", String(value));
	if (persist) {
		try {
			win.localStorage.setItem(SIDEBAR_WIDTH_KEY, String(value));
		} catch {
			/* 隐私模式不可用：本次会话内宽度仍生效 */
		}
	}
	return value;
}

function resolveEnv(env) {
	if (env !== undefined && env !== null) return { doc: env.doc, win: env.win };
	return {
		doc: typeof document === "undefined" ? null : document,
		win: typeof window === "undefined" ? null : window,
	};
}

function sidebarBoxWidth(doc, win) {
	const side = doc.getElementById("chat-sidebar");
	if (side !== null && typeof side.getBoundingClientRect === "function") {
		const width = side.getBoundingClientRect().width;
		if (Number.isFinite(width) && width > 0) return width;
	}
	return readSidebarWidth(win);
}

// bindSidebarWidth(handle, { isDisabled, env }) → cleanup()
// handle 是模板里的 .ada-sidebar__resizer；绑定与解绑都由调用方在挂载/卸载时驱动。
export function bindSidebarWidth(handle, opts = {}) {
	if (handle === null || handle === undefined) return null;
	const env = resolveEnv(opts.env);
	const { doc, win } = env;
	const isDisabled = typeof opts.isDisabled === "function" ? opts.isDisabled : () => false;
	const body = doc.body ?? null;
	const setBodyClass = (name, on) => {
		if (body === null || body === undefined || body.classList === undefined) return;
		if (on) body.classList.add(name);
		else body.classList.remove(name);
	};
	const setHandleClass = (name, on) => {
		if (handle.classList === undefined) return;
		if (on) handle.classList.add(name);
		else handle.classList.remove(name);
	};

	applySidebarWidth(readSidebarWidth(win), false, env);

	let drag = null;
	function onMove(event) {
		if (drag === null || event.pointerId !== drag.id) return;
		applySidebarWidth(drag.startWidth + (event.clientX - drag.startX), false, env);
	}
	function onUp(event) {
		if (drag === null || event.pointerId !== drag.id) return;
		applySidebarWidth(drag.startWidth + (event.clientX - drag.startX), true, env);
		drag = null;
		setHandleClass("is-dragging", false);
		setBodyClass("ada-sidebar-resizing", false);
		try {
			handle.releasePointerCapture(event.pointerId);
		} catch {
			/* 没有捕获也无需处理 */
		}
		win.removeEventListener("pointermove", onMove);
		win.removeEventListener("pointerup", onUp);
		win.removeEventListener("pointercancel", onUp);
	}
	function onDown(event) {
		if (event.button !== 0 || isDisabled()) return;
		event.preventDefault();
		try {
			handle.focus({ preventScroll: true });
		} catch {
			try { handle.focus(); } catch { /* 句柄不可聚焦时拖动照旧 */ }
		}
		drag = { id: event.pointerId, startX: event.clientX, startWidth: sidebarBoxWidth(doc, win) };
		setHandleClass("is-dragging", true);
		setBodyClass("ada-sidebar-resizing", true);
		try {
			handle.setPointerCapture(event.pointerId);
		} catch {
			/* 指针捕获失败时 window 监听仍能收到移动 */
		}
		win.addEventListener("pointermove", onMove);
		win.addEventListener("pointerup", onUp);
		win.addEventListener("pointercancel", onUp);
	}
	function onKey(event) {
		if (isDisabled()) return;
		const current = sidebarBoxWidth(doc, win);
		let next = null;
		if (event.key === "ArrowLeft") next = current - KEYBOARD_STEP;
		else if (event.key === "ArrowRight") next = current + KEYBOARD_STEP;
		else if (event.key === "Home") next = SIDEBAR_WIDTH_MIN;
		else if (event.key === "End") next = SIDEBAR_WIDTH_MAX;
		if (next === null) return;
		event.preventDefault();
		applySidebarWidth(next, true, env);
	}
	function onDoubleClick() {
		if (isDisabled()) return;
		applySidebarWidth(SIDEBAR_WIDTH_DEFAULT, true, env);
	}
	function onViewportResize() {
		applySidebarWidth(readSidebarWidth(win), false, env);
	}

	handle.addEventListener("pointerdown", onDown);
	handle.addEventListener("keydown", onKey);
	handle.addEventListener("dblclick", onDoubleClick);
	win.addEventListener("resize", onViewportResize);

	return function cleanup() {
		handle.removeEventListener("pointerdown", onDown);
		handle.removeEventListener("keydown", onKey);
		handle.removeEventListener("dblclick", onDoubleClick);
		win.removeEventListener("resize", onViewportResize);
		win.removeEventListener("pointermove", onMove);
		win.removeEventListener("pointerup", onUp);
		win.removeEventListener("pointercancel", onUp);
	};
}
