// PluginDetail.js —— 插件详情页：清单/文件树/文件预览（Markdown 走 /markdown.ast，
// 源码走客户端高亮 ../highlight.js；HEAD 的服务端 chroma 已随 SSR 移除）。
import { ref, reactive, computed, watch, onMounted, onUnmounted, inject } from "/static/ada/ada-runtime.mjs";
import { shellState } from "./state.js";
import { normalizeLang } from "../highlight.js";

export const setup = (props) => {
	const fetch = inject("fetch");
	const router = inject("router");
	const registry = inject("registry");
	const confirm = inject("confirmService");
	const id = String(props.id ?? "");

	shellState.title = "插件详情";
	shellState.topbar.clear();

	const plugin = ref(null);
	const failed = ref(null);
	const busy = ref(false);
	const file = ref(null);
	const astNodes = ref(null);
	const readmeNodes = ref(null);
	const preview = ref(true);
	let astCacheKey = "";
	let lastFilePath = "";

	// 前端贡献（页面/插槽/导航/dashboard）来自注册表，与清单能力合并展示。
	const regInfo = computed(() => registry.store.plugins.find((p) => p.id === id) ?? null);
	const capPages = computed(() => regInfo.value?.pages?.length ?? 0);
	const capSlots = computed(() => regInfo.value?.slots?.length ?? 0);
	const capNav = computed(() => regInfo.value?.nav?.length ?? 0);
	const capDashboard = computed(() => (regInfo.value?.dashboard?.length ?? 0) > 0);
	const firstPage = computed(() => {
		const slug = regInfo.value?.pages?.[0]?.slug ?? "";
		return slug ? `/ext/${encodeURIComponent(id)}/${slug}` : "";
	});
	const ownership = computed(() => {
		const p = plugin.value;
		if (!p) return "—";
		if (p.frontend && p.backend) return "前端 + 后端";
		if (p.frontend) return "前端插件";
		if (p.backend) return "后端插件";
		return "—";
	});
	const enabledLabel = computed(() => {
		const p = plugin.value;
		if (!p) return "";
		if (p.enabled) return "已启用";
		return p.statusLabel || "未启用";
	});
	const fileCount = computed(() => (plugin.value?.files ?? []).filter((f) => !f.dir).length);
	// 源码高亮语言：按文件扩展名（HEAD 是服务端 lexers.Match(filename)）。
	// 未知扩展名 → 空串 → 纯文本（不高亮）。
	const fileLang = computed(() => {
		const name = String(file.value?.path ?? "");
		const ext = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1) : "";
		return normalizeLang(ext);
	});
	const footLabel = computed(() => {
		const f = file.value;
		if (!f) return "README · 渲染预览";
		if (f.markdown) return `${f.sizeLabel ?? ""} · ${preview.value ? "渲染预览" : "源码"}`;
		return `${f.sizeLabel ?? ""} · ${fileLang.value !== "" ? fileLang.value : "文本"}`;
	});

	async function astFor(text) {
		const key = String(text ?? "");
		if (key === astCacheKey && astNodes.value !== null) return astNodes.value;
		try {
			const data = await fetch("/markdown.ast", { method: "POST", body: { text: key } });
			astCacheKey = key;
			astNodes.value = Array.isArray(data.nodes) ? data.nodes : [];
		} catch {
			astCacheKey = key;
			astNodes.value = null;
		}
		return astNodes.value;
	}

	async function load() {
		try {
			const data = await fetch("/partials/extensions.json?detail=" + encodeURIComponent(id));
			const found = (data.plugins ?? []).find((p) => p.id === id) ?? null;
			plugin.value = found;
			failed.value = null;
			if (found?.readme) {
				readmeNodes.value = null;
				readmeNodes.value = await astFor(found.readme);
			}
			// 默认选中 README（HEAD 语义）；文件树里没有 README 就保持空。
			const readmeFile = (found?.files ?? []).find((f) => !f.dir && f.path.toLowerCase() === "readme.md");
			if (readmeFile) await openFile(readmeFile.path);
		} catch (err) {
			failed.value = err?.message ?? String(err);
		}
	}

	async function openFile(path) {
		if (lastFilePath === path && file.value !== null) return;
		lastFilePath = path;
		try {
			const view = await fetch(`/extensions/${encodeURIComponent(id)}/file?path=${encodeURIComponent(path)}`);
			file.value = view;
			preview.value = true;
			if (view.markdown && view.content) await astFor(view.content);
		} catch (err) {
			file.value = { path, error: err?.message ?? String(err) };
		}
	}

	// 文件树点击切换文件：markdown 文件按选中内容重取 AST（缓存键随之变化）。
	watch(file, (f) => {
		if (f && f.markdown && f.content) void astFor(f.content);
	});

	async function toggle() {
		const p = plugin.value;
		if (!p) return;
		const verb = p.enabled ? "停用" : "启用";
		const ok = await confirm(`${verb}插件「${p.name || p.id}」？`);
		if (!ok) return;
		busy.value = true;
		try {
			await fetch(`/extensions/${encodeURIComponent(p.id)}/${p.enabled ? "disable" : "enable"}`, {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body: new URLSearchParams({ next: "/extensions/" + encodeURIComponent(p.id) }).toString(),
			});
			await load();
		} catch (err) {
			failed.value = err?.message ?? String(err);
		} finally {
			busy.value = false;
		}
	}

	async function copyText(text) {
		const value = String(text ?? "");
		if (!value) return;
		try {
			await navigator.clipboard.writeText(value);
		} catch {
			/* 剪贴板不可用（无授权/非安全上下文）：静默降级，不阻断浏览 */
		}
	}
	function copyCurrent() {
		if (file.value?.content !== undefined) void copyText(file.value.content);
		else void copyText(plugin.value?.readme);
	}

	const goList = () => { router.navigate("/extensions"); };
	const openFirstPage = () => { if (firstPage.value) router.navigate(firstPage.value); };
	// 预览/源码切换（模板表达式里没有布尔字面量：编译器把 true/false 当标识符求值）。
	const showPreview = () => { preview.value = true; };
	const showSource = () => { preview.value = false; };

	onMounted(load);
	onUnmounted(() => shellState.topbar.clear());

	return {
		id, plugin, failed, busy, file, preview,
		astNodes, readmeNodes,
		capPages, capSlots, capNav, capDashboard, firstPage, ownership, enabledLabel, fileCount, footLabel,
		fileLang,
		load, openFile, toggle, copyText, copyCurrent, showPreview, showSource,
		goList, openFirstPage,
	};
};
