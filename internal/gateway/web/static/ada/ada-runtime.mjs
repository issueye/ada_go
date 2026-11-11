// ada-runtime —— 插件前端代码的唯一稳定 import 入口。
//
// 约定（proposals/dev-plan-spa-frontend.md §0.4）：
//   import { ref, computed, inject } from "/static/ada/ada-runtime.mjs";
// 本文件的导出面是对插件作者的公开契约；内部模块路径不承诺稳定。
// 每次调整导出面需同步 proposals/dev-plan 与插件手册。

export {
	// 响应式
	ref, reactive, computed, effect, effectScope, watch, watchEffect, nextTick,
	isRef, unref, isReactive, stop, activeEffectScope,
} from "./reactivity.mjs";

export {
	// 组件模型
	defineComponent, getCurrentInstance, provide, inject,
	onMounted, onUnmounted, onErrorCaptured,
	// 渲染
	h, Fragment, defineAsyncComponent,
} from "./runtime.mjs";

export {
	// SFC 装载（宿主内部主要用它；插件一般不直接用）
	loadSFC, parseSFC, compileScopedCss, clearSfcCache, SfcError,
} from "./sfc.mjs";

export { createRouter } from "./router.mjs";

// 错误类型（插件可 instanceof 判断）
export { RenderError } from "./runtime.mjs";
export { TemplateError } from "./compiler.mjs";
