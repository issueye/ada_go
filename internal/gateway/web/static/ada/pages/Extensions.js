// Extensions.js —— 插件列表页：extensions.json + 注册表能力合并 + 启停/重载。
import { ref, reactive, computed, onMounted, onUnmounted, inject } from "/static/ada/ada-runtime.mjs";
import { shellState } from "./state.js";

export const setup = () => {
	const fetch = inject("fetch");
	const router = inject("router");
	const registry = inject("registry");
	const confirm = inject("confirmService");

	shellState.title = "插件";
	shellState.topbar.clear();

	const plugins = reactive([]);
	const count = ref(0);
	const failed = ref(null);
	const busy = ref(false);
	const reloading = ref(false);
	const needle = ref("");
	const filter = ref("all");

	async function load() {
		try {
			const data = await fetch("/partials/extensions.json");
			plugins.splice(0, plugins.length, ...(data.plugins ?? []));
			count.value = data.count ?? plugins.length;
			failed.value = null;
		} catch (err) {
			failed.value = err?.message ?? String(err);
		}
	}
	onMounted(load);
	onUnmounted(() => shellState.topbar.clear());

	// 前端贡献（页面/插槽/dashboard）来自 /frontend-registry：与清单能力合并成
	// HEAD 扩展行同一套「工具/页面/插槽/dashboard」徽标。
	const capOf = (id) => registry.store.plugins.find((p) => p.id === id) ?? null;
	const capPages = (p) => capOf(p.id)?.pages?.length ?? 0;
	const capSlots = (p) => capOf(p.id)?.slots?.length ?? 0;
	const capDashboard = (p) => (capOf(p.id)?.dashboard?.length ?? 0) > 0;

	const enabledCount = computed(() => plugins.filter((p) => p.enabled).length);
	const disabledCount = computed(() => plugins.length - enabledCount.value);
	const filtered = computed(() => {
		const q = needle.value.trim().toLowerCase();
		return plugins.filter((p) => {
			if (filter.value === "on" && !p.enabled) return false;
			if (filter.value === "off" && p.enabled) return false;
			if (!q) return true;
			return (p.name ?? "").toLowerCase().includes(q) || (p.id ?? "").toLowerCase().includes(q);
		});
	});

	// 启停是两个独立表单端点（303 由 fetch 跟随后吸收）；注册表经
	// evt.plugin.changed 自动重拉。
	async function toggle(p) {
		const verb = p.enabled ? "停用" : "启用";
		const ok = await confirm(`${verb}插件「${p.name || p.id}」？${verb}后不再贡献工具与页面，重载插件后生效。`);
		if (!ok) return;
		busy.value = true;
		try {
			await fetch(`/extensions/${encodeURIComponent(p.id)}/${p.enabled ? "disable" : "enable"}`, {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body: new URLSearchParams({ next: "/extensions" }).toString(),
			});
			await load();
		} catch (err) {
			failed.value = err?.message ?? String(err);
		} finally {
			busy.value = false;
		}
	}

	async function reloadPlugins() {
		reloading.value = true;
		try {
			await fetch("/agent-plugins/reload", {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body: new URLSearchParams({ next: "/extensions" }).toString(),
			});
			await load();
		} catch (err) {
			failed.value = err?.message ?? String(err);
		} finally {
			reloading.value = false;
		}
	}

	const detailURL = (id) => "/extensions/" + encodeURIComponent(id);
	const openDetail = (id) => { router.navigate("/extensions/" + encodeURIComponent(id)); };

	return {
		plugins, count, failed, busy, reloading, needle, filter,
		enabledCount, disabledCount, filtered,
		load, toggle, reloadPlugins,
		capPages, capSlots, capDashboard,
		detailURL, openDetail,
	};
};
