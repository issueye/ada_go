// token-usage 前端段（usage 页面）逻辑。
//
// 数据来自本插件 route GET /usage（经 ada.api 桥）：events.read 读取的每轮原始
// 计数在插件内聚合为 { workspace, thread }。页面不带 threadId 上下文，thread
// 一般为 null，「当前会话」视图如实渲染空态（与旧 SSR 页面在 /ext 路由上的行为
// 一致：page_usage 动作同样没有 threadId）。
//
// 仍是同一套口径：时间范围分段 → 工作区/当前会话切换；趋势图 → 三条占比计量条；
// 计量条沿用 usage.css 的 10% 分档类（tu-meter__fill--p*），不用内联样式。
import { ref, computed, inject, onMounted } from "/static/ada/ada-runtime.mjs";

function formatNumber(value) {
	const n = Number(value || 0);
	if (!isFinite(n)) return "0";
	const text = String(Math.round(n));
	let out = "";
	for (let i = 0; i < text.length; i += 1) {
		if (i > 0 && (text.length - i) % 3 === 0) out += ",";
		out += text.charAt(i);
	}
	return out;
}

function formatCost(micros) {
	const n = Number(micros || 0);
	if (!isFinite(n) || n <= 0) return "—";
	return "$" + (n / 1000000).toFixed(4);
}

function num(usage, key) {
	const n = Number(usage && usage[key]);
	return isFinite(n) ? n : 0;
}

function totalOf(usage) {
	return num(usage, "inputTokens") + num(usage, "outputTokens");
}

function cacheRate(usage) {
	const input = num(usage, "inputTokens");
	const cache = num(usage, "cacheTokens");
	if (input <= 0) return "—";
	return Math.round((cache * 100) / input) + "%";
}

function percent(part, whole) {
	if (whole <= 0 || part <= 0) return 0;
	return Math.round((part * 100) / whole);
}

function share(part, whole) {
	return percent(part, whole) + "%";
}

// 计量条按 10% 一档映射类名（usage.css 既有约定，不用内联 style）。
function meterClass(part, whole) {
	const p = percent(part, whole);
	const bucket = Math.min(100, Math.max(0, Math.round(p / 10) * 10));
	return "tu-meter__fill tu-meter__fill--p" + bucket;
}

function modelLabel(usage) {
	const provider = String((usage && usage.provider) || "");
	const model = String((usage && usage.model) || "");
	if (!provider && !model) return "";
	return provider + (model ? " / " + model : "");
}

export const setup = () => {
	const ada = inject("ada");

	const view = ref("workspace");
	const root = ref({});
	const toast = ref(null);
	let toastSeq = 0;
	let toastTimer = 0;

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
		try {
			const data = await ada.api("/usage");
			root.value = data && typeof data === "object" ? data : {};
		} catch (err) {
			// 页面读取失败保持空态：用量页没有可降级的本地缓存。
			root.value = {};
			showToast("读取用量失败：" + ((err && err.message) || String(err)), "error");
		}
	}

	const thread = computed(() => (root.value && root.value.thread) || null);
	const workspace = computed(() => (root.value && root.value.workspace) || null);
	const current = computed(() => (view.value === "thread" ? thread.value : workspace.value));
	const totals = computed(() => {
		const u = current.value;
		const input = num(u, "inputTokens");
		const output = num(u, "outputTokens");
		const cache = num(u, "cacheTokens");
		return { input, output, cache, total: input + output, whole: input + output + cache };
	});
	const modelText = computed(() => modelLabel(workspace.value));

	function summaryText() {
		const row = (label, u) =>
			[label, num(u, "inputTokens"), num(u, "outputTokens"), num(u, "cacheTokens"), num(u, "costMicros")].join(",");
		return [
			"scope,inputTokens,outputTokens,cacheTokens,costMicros",
			row("workspace", workspace.value),
			row("thread", thread.value),
		].join("\n");
	}

	function copySummary() {
		const text = summaryText();
		if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
			navigator.clipboard.writeText(text)
				.then(() => showToast("已复制用量汇总。"))
				.catch(() => showToast("复制失败，请手动导出 CSV。", "error"));
			return;
		}
		showToast("当前环境不支持一键复制，请导出 CSV。", "error");
	}

	function exportCSV() {
		const text = summaryText();
		const url = URL.createObjectURL(new Blob([text], { type: "text/csv;charset=utf-8" }));
		const a = document.createElement("a");
		a.href = url;
		a.download = "token-usage.csv";
		document.body.appendChild(a);
		a.click();
		document.body.removeChild(a);
		setTimeout(() => {
			URL.revokeObjectURL(url);
		}, 0);
		showToast("已导出 token-usage.csv。");
	}

	onMounted(load);

	return {
		view, toast, thread, current, totals, modelText,
		formatNumber, formatCost, num, totalOf, cacheRate, share, meterClass,
		copySummary, exportCSV,
	};
};
