// Overview.js —— 概览页逻辑：dashboard.json + 路由跳转。
import { ref, computed, onMounted, onUnmounted, inject } from "/static/ada/ada-runtime.mjs";
import { shellState } from "./state.js";

export const setup = () => {
	const fetch = inject("fetch");
	const router = inject("router");
	const bootstrap = inject("bootstrap");
	const data = ref(null);
	const failed = ref(null);

	shellState.title = "概览";
	shellState.topbar.clear();

	async function load() {
		try {
			data.value = await fetch("/partials/dashboard.json");
			failed.value = null;
		} catch (err) {
			failed.value = err?.message ?? String(err);
		}
	}
	onMounted(load);
	onUnmounted(() => shellState.topbar.clear());

	const recentSlice = computed(() => (Array.isArray(data.value?.recent) ? data.value.recent.slice(0, 5) : []));
	const recentShown = computed(() => (Array.isArray(data.value?.recent) ? data.value.recent.length : 0));
	const gatewayLabel = computed(() => {
		const raw = String(bootstrap?.gateway ?? "");
		return raw.replace(/^[a-z]+:\/\//i, "").replace(/\/$/, "") + " · Headless";
	});

	// 页内导航走 router（整页刷新会丢 SPA 状态）；a 的 href 保留给中键/新标签。
	const go = (path) => () => { router.navigate(path); };
	return {
		data,
		recentSlice,
		failed,
		recentShown,
		gatewayLabel,
		load,
		goThreads: go("/threads"),
		goExtensions: go("/extensions"),
		threadURL: (id) => "/threads/" + encodeURIComponent(id),
		openThread: (id) => { router.navigate("/threads/" + encodeURIComponent(id)); },
	};
};
