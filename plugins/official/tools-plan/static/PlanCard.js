// tools-plan 前端段（chat.corner 的 Todo 卡片）逻辑：SFC 的 <script src> 指向的 ESM。
//
// 数据：读动作桥 ada.action("slot_chat_corner", { threadId })（manifest read:true
// → 宿主走 GET）；会话内 SSE（evt.item.updated / evt.run.status / resync）触发
// 重取：计划由 update_plan 工具调用产生，工具结果落账即 item.updated。旧 SSR
// 预取没有这个实时性（整页渲染时取一次），这里顺带补齐。
//
// 没有计划（total=0）或没有会话上下文时返回 null——不画空卡，也绝不回退到示例数据。
import { ref, computed, inject, onMounted, onUnmounted } from "/static/ada/ada-runtime.mjs";

const STATUS_CLASS = { done: "is-done", in_progress: "is-running", pending: "is-pending" };
const STATUS_MARK = { done: "✓", in_progress: "→", pending: "•" };
const STATUS_ICON = { done: "i-check-circle", in_progress: "i-arrow-right", pending: "i-circle" };

// 状态归一化与后端 normalizeStatus 同口径：用户手改的 TODO 文件坏值按 pending 处理。
function normalizeStatus(value) {
	const s = String(value || "").toLowerCase().trim();
	if (s === "done" || s === "completed") return "done";
	if (s === "in_progress" || s === "in-progress" || s === "active" || s === "running") return "in_progress";
	return "pending";
}

export const setup = () => {
	const ada = inject("ada");
	const threadId = ada.thread ? String(ada.thread.id || "") : "";
	const plan = ref(null);
	let es = null;

	async function load() {
		if (!threadId) return;
		try {
			// 读动作绑定（read:true）由宿主约束为 GET 并原样透出 JSON；
			// POST 是写动作的 303 PRG 通道。
			const body = await ada.action("slot_chat_corner", { threadId });
			plan.value = body && body.plan ? body.plan : null;
		} catch {
			// 读取失败保持上一份数据；没有数据时等于不画卡（与旧宿主语义一致）。
		}
	}

	const done = computed(() => (plan.value ? Number(plan.value.done) || 0 : 0));
	const total = computed(() => (plan.value ? Number(plan.value.total) || 0 : 0));
	const steps = computed(() => (plan.value && Array.isArray(plan.value.steps) ? plan.value.steps : []));
	const visible = computed(() => total.value > 0);
	const complete = computed(() => total.value > 0 && done.value >= total.value);

	function stepStatus(step) {
		return normalizeStatus(step && step.status);
	}
	function stepClass(step) {
		return "ada-todo__step " + STATUS_CLASS[stepStatus(step)];
	}
	function stepMark(step) {
		return STATUS_MARK[stepStatus(step)];
	}
	function stepIcon(step) {
		return STATUS_ICON[stepStatus(step)];
	}

	onMounted(() => {
		if (!threadId) return;
		load();
		es = new EventSource("/events/" + encodeURIComponent(threadId));
		es.addEventListener("evt.item.updated", load);
		es.addEventListener("evt.run.status", load);
		es.addEventListener("resync", load);
	});
	onUnmounted(() => {
		if (es !== null) es.close();
	});

	return { visible, complete, done, total, steps, stepClass, stepMark, stepIcon };
};
