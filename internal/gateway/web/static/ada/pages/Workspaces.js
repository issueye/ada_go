// Workspaces.js —— 工作区页：目录浏览（/api/fs/list）+ 最近工作区 + 新建目录。
import { ref, reactive, computed, onMounted, onUnmounted, inject } from "/static/ada/ada-runtime.mjs";
import { shellState } from "./state.js";

export const setup = () => {
	const fetch = inject("fetch");
	const router = inject("router");
	const bootstrap = inject("bootstrap");

	shellState.title = "工作区";
	shellState.topbar.clear();

	const current = ref("");
	const parentPath = ref("");
	const roots = reactive([]);
	const recent = reactive([]);
	const entries = reactive([]);
	const truncated = ref(false);
	const omitted = ref(0);
	const failed = ref(null);
	const busy = ref(false);
	const newDirName = ref("");

	const rootCount = computed(() => roots.length);
	const bootstrapWorkspace = String(bootstrap.workspace ?? "");
	const selectable = computed(() =>
		(bootstrap.workspaces ?? []).some((w) => w.path === current.value));

	async function loadWorkspaces() {
		try {
			const data = await fetch("/api/workspaces");
			const list = data.workspaces ?? [];
			recent.splice(0, recent.length, ...list.map((w) => ({
				path: w.path,
				name: w.name,
				// HEAD 的「N 会话」来自逐工作区 thread.list（会各自拉起 Core）；
				// SPA 侧降级为不预取计数，标注「会话数未加载」同款语义。
				meta: w.path === bootstrapWorkspace ? "当前 · 点击切换" : "会话数未加载 · 点击切换",
			})));
		} catch (err) {
			failed.value = err?.message ?? String(err);
		}
	}

	// 点击「最近工作区」：HEAD 是表单 POST /workspaces/select（切换 + 回首页）。
	async function switchTo(path) {
		busy.value = true;
		try {
			await fetch("/workspaces/select", {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body: new URLSearchParams({ path }).toString(),
			});
			await loadWorkspaces();
			router.navigate("/");
		} catch (err) {
			failed.value = err?.message ?? String(err);
		} finally {
			busy.value = false;
		}
	}

	async function browse(path) {
		try {
			const q = path ? "?path=" + encodeURIComponent(path) : "";
			const data = await fetch("/api/fs/list" + q);
			current.value = data.path ?? "";
			parentPath.value = data.parent ?? "";
			roots.splice(0, roots.length, ...(data.roots ?? []));
			entries.splice(0, entries.length, ...(data.entries ?? []));
			truncated.value = data.truncated === true;
			omitted.value = data.omitted ?? 0;
			failed.value = null;
		} catch (err) {
			failed.value = err?.message ?? String(err);
		}
	}

	// 「使用此目录」：HEAD 走 /workspaces/select（只接受已配置工作区）。
	// 这里同一语义：selectable 已保证 current 在配置列表里；303 由 fetch 吸收。
	async function useCurrent() {
		busy.value = true;
		try {
			await fetch("/workspaces/select", {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body: new URLSearchParams({ path: current.value }).toString(),
			});
			await Promise.all([loadWorkspaces(), browse(current.value)]);
			router.navigate("/");
		} catch (err) {
			failed.value = err?.message ?? String(err);
		} finally {
			busy.value = false;
		}
	}

	// 新建目录限制在当前工作区子树内（服务端 withinRoot 校验）；成功后刷新列表。
	async function mkdir() {
		const name = newDirName.value.trim();
		if (!name || !current.value) return;
		busy.value = true;
		try {
			await fetch("/workspaces/mkdir", {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body: new URLSearchParams({ parent: current.value, name }).toString(),
			});
			newDirName.value = "";
			await browse(current.value);
		} catch (err) {
			failed.value = err?.message ?? String(err);
		} finally {
			busy.value = false;
		}
	}

	onMounted(() => {
		void loadWorkspaces();
		void browse("");
	});
	onUnmounted(() => shellState.topbar.clear());

	return {
		current, parentPath, roots, recent, entries, truncated, omitted, failed, busy, newDirName,
		rootCount, selectable, bootstrapWorkspace,
		loadWorkspaces, browse, useCurrent, mkdir, switchTo,
	};
};
