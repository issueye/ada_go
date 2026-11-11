// env-manager 前端段（settings 页面）逻辑。
//
// 数据与操作全部经本插件 route（经 ada.api 桥，CSRF 由宿主封装）：
//   GET  /state   全局 + 工作区两层变量、合并视图、服务程序目录
//   POST /set     写入一个变量（scope = global | workspace）
//   POST /remove  删除一个变量
// 写入后需要重载插件（POST /agent-plugins/reload，基座端点），新的子进程才会拿到新环境。
//
// 与旧 env-page.tsx 的差异：确认框走 ada.confirm()；一次性反馈仍是底部 toast。
import { ref, reactive, computed, inject, onMounted } from "/static/ada/ada-runtime.mjs";

const KEY_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;

const SCOPES = [
	{ value: "workspace", label: "工作区" },
	{ value: "global", label: "全局" },
];

function scopeLabel(scope) {
	return scope === "global" ? "全局" : "工作区";
}

function varsOf(layer) {
	return layer && layer.vars && typeof layer.vars === "object" ? layer.vars : {};
}

function formatBytes(size) {
	const bytes = Number(size);
	if (!isFinite(bytes) || bytes <= 0) return "—";
	if (bytes >= 1024 * 1024) return Math.round((bytes / (1024 * 1024)) * 10) / 10 + " MB";
	if (bytes >= 1024) return Math.round(bytes / 1024) + " KB";
	return bytes + " B";
}

function formatTime(value) {
	const text = String(value || "");
	if (!text) return "—";
	const d = new Date(text);
	if (isNaN(d.getTime())) return text;
	const pad = (n) => (n < 10 ? "0" + n : String(n));
	return (
		d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) +
		" " + pad(d.getHours()) + ":" + pad(d.getMinutes())
	);
}

function maskValue(value) {
	const text = String(value === undefined || value === null ? "" : value);
	if (!text) return "";
	const tail = text.length > 4 ? text.slice(-4) : "";
	return "••••••••••" + tail;
}

export const setup = () => {
	const ada = inject("ada");
	const bootstrap = inject("bootstrap");
	const csrf = bootstrap && bootstrap.csrf ? String(bootstrap.csrf) : "";

	const loading = ref(true);
	const error = ref("");
	const state = ref(null);
	const toast = ref(null);
	let toastSeq = 0;
	let toastTimer = 0;
	const filter = ref("all");
	const form = reactive({ scope: "workspace", key: "", value: "" });
	const revealed = reactive({});
	const busy = ref(false);
	const reloading = ref(false);

	function showToast(message, tone) {
		toastSeq += 1;
		const seq = toastSeq;
		toast.value = { id: seq, message, tone: tone || "info" };
		if (toastTimer) clearTimeout(toastTimer);
		toastTimer = setTimeout(() => {
			if (toast.value && toast.value.id === seq) toast.value = null;
		}, tone === "error" ? 4200 : 2400);
	}

	async function load() {
		loading.value = true;
		error.value = "";
		try {
			const data = await ada.api("/state");
			state.value = data || null;
			loading.value = false;
		} catch (err) {
			loading.value = false;
			error.value = (err && err.message) || String(err);
		}
	}

	const workspaceVars = computed(() => varsOf(state.value && state.value.workspace));
	const globalVars = computed(() => varsOf(state.value && state.value.global));

	// 工作区优先展示；同名全局项标记为「被覆盖」，避免看成一式两份。
	const rows = computed(() => {
		const workspace = workspaceVars.value;
		const global = globalVars.value;
		const out = [];
		Object.keys(workspace).sort().forEach((key) => {
			out.push({
				id: "workspace:" + key,
				scope: "workspace",
				key,
				value: workspace[key],
				overridden: Object.prototype.hasOwnProperty.call(global, key),
			});
		});
		Object.keys(global).sort().forEach((key) => {
			out.push({
				id: "global:" + key,
				scope: "global",
				key,
				value: global[key],
				overridden: false,
			});
		});
		return out;
	});

	const filteredRows = computed(() => {
		if (filter.value === "all") return rows.value;
		return rows.value.filter((row) => row.scope === filter.value);
	});

	const counts = computed(() => ({
		all: rows.value.length,
		workspace: rows.value.filter((row) => row.scope === "workspace").length,
		global: rows.value.filter((row) => row.scope === "global").length,
	}));

	const warnings = computed(() => {
		const list = state.value && Array.isArray(state.value.warnings) ? state.value.warnings : [];
		return list;
	});

	const binPaths = computed(() => {
		const bins = state.value && state.value.bins;
		return bins && Array.isArray(bins.paths) ? bins.paths : [];
	});

	const binPrograms = computed(() => {
		const bins = state.value && state.value.bins;
		return bins && Array.isArray(bins.programs) ? bins.programs : [];
	});

	const keyError = computed(() => {
		const key = String(form.key || "").trim();
		if (!key) return "";
		if (!KEY_PATTERN.test(key)) return "变量名需匹配 ^[A-Za-z_][A-Za-z0-9_]*$";
		if (key.indexOf("ADAG_") === 0) return "ADAG_ 前缀由宿主保留";
		return "";
	});

	function toggleReveal(row) {
		if (revealed[row.id]) delete revealed[row.id];
		else revealed[row.id] = true;
	}

	function displayValue(row) {
		if (!String(row.value === undefined || row.value === null ? "" : row.value)) return "";
		return revealed[row.id] ? String(row.value) : maskValue(row.value);
	}

	function copyText(text, label) {
		const value = String(text === undefined || text === null ? "" : text);
		if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
			navigator.clipboard.writeText(value)
				.then(() => showToast("已复制" + label + "。"))
				.catch(() => showToast("复制失败，请手动选择复制。", "error"));
			return;
		}
		showToast("当前环境不支持一键复制。", "error");
	}

	function submit() {
		const key = String(form.key || "").trim();
		if (!key) {
			showToast("变量名不能为空。", "error");
			return;
		}
		if (keyError.value) {
			showToast(keyError.value, "error");
			return;
		}
		busy.value = true;
		ada.api("/set", { method: "POST", body: { scope: form.scope, key, value: String(form.value || "") } })
			.then(() => {
				busy.value = false;
				showToast("已写入 " + scopeLabel(form.scope) + " " + key + "，重载插件后生效。");
				form.key = "";
				form.value = "";
				load();
			})
			.catch((err) => {
				busy.value = false;
				showToast("写入失败：" + ((err && err.message) || String(err)), "error");
			});
	}

	function removeVar(row) {
		ada.confirm("删除 " + scopeLabel(row.scope) + " 变量「" + row.key + "」？").then((ok) => {
			if (!ok) return;
			ada.api("/remove", { method: "POST", body: { scope: row.scope, key: row.key } })
				.then(() => {
					showToast("已删除 " + row.key + "。");
					load();
				})
				.catch((err) => {
					showToast("删除失败：" + ((err && err.message) || String(err)), "error");
				});
		});
	}

	function reloadPlugins() {
		if (reloading.value) return;
		reloading.value = true;
		fetch("/agent-plugins/reload", {
			method: "POST",
			credentials: "same-origin",
			headers: { "X-CSRF-Token": csrf },
		})
			.then((response) => {
				if (!response.ok) throw new Error("HTTP " + response.status);
				reloading.value = false;
				showToast("插件已重载，新子进程会拿到新环境。");
			})
			.catch((err) => {
				reloading.value = false;
				showToast("重载失败：" + ((err && err.message) || String(err)), "error");
			});
	}

	onMounted(load);

	return {
		loading, error, toast, filter, form, busy, reloading,
		counts, filteredRows, warnings, binPaths, binPrograms, keyError,
		scopes: SCOPES,
		load, scopeLabel, formatBytes, formatTime, displayValue, toggleReveal, copyText, submit, removeVar, reloadPlugins,
	};
};
