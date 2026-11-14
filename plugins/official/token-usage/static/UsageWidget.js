// token-usage 前端段（概览页 dashboard 卡片）逻辑。
//
// 数据来自本插件 route GET /usage（经 ada.api 桥）；没有数据（整卡无任何用量
// 记录）时渲染 null——概览页不留空卡（与旧 dashboard.tsx 语义一致）。
// 旧实现依赖 SSR 预取（page_usage 动作投影），投影已随 SSR 移除，这里改为
// 装载后经 route 取一次数。
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

export const setup = () => {
	const ada = inject("ada");
	const root = ref(null);

	async function load() {
		try {
			const data = await ada.api("/usage");
			root.value = data && typeof data === "object" ? data : null;
		} catch {
			// 读取失败不留空卡。
			root.value = null;
		}
	}

	const workspace = computed(() => (root.value && root.value.workspace) || null);
	const thread = computed(() => (root.value && root.value.thread) || null);
	const workspaceTotal = computed(() => num(workspace.value, "inputTokens") + num(workspace.value, "outputTokens"));
	const hasData = computed(() => workspace.value !== null || thread.value !== null);

	onMounted(load);

	return { workspace, thread, workspaceTotal, hasData, formatNumber, formatCost, num };
};
