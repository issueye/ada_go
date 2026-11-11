// tools-ask 前端段（thread.tray 的提问卡片）逻辑。
//
// 待答交互来自线程级 SSE（与旧 SSR 的 data.interactions 投影同一来源——Core 的
// 交互登记）：evt.interaction.requested 进队、evt.interaction.resolved 出队、
// evt.run.status 终态清空。连接建立前已登记的交互不会被 SSE 补发（Last-Event-ID
// 只在同一条连接的断线重连里生效），所以「页面刷新时仍有待答项」的场景托盘为空——
// 这是 SPA 化的已知降级，见迁移报告。
//
// 提交仍走基座交互表单端点 POST /threads/{id}/interaction/{requestId}
// （decision + kind + value；CSRF 双提交），审批/提问的安全语义完全由基座负责。
import { ref, reactive, inject, onMounted, onUnmounted } from "/static/ada/ada-runtime.mjs";

export const setup = () => {
	const ada = inject("ada");
	const bootstrap = inject("bootstrap");
	const csrf = bootstrap && bootstrap.csrf ? String(bootstrap.csrf) : "";
	const threadId = ada.thread ? String(ada.thread.id || "") : "";
	const items = ref([]);
	const answers = reactive({});
	const picked = reactive({});
	const failed = ref("");
	let es = null;

	function addInteraction(data) {
		if (!data || !data.interactionId) return;
		if (String(data.threadId || "") !== threadId) return;
		const id = String(data.interactionId);
		for (const item of items.value) {
			if (item.id === id) return;
		}
		items.value.push({
			id,
			kind: String(data.kind || "input"),
			title: String(data.title || ""),
			body: String(data.body || ""),
			options: Array.isArray(data.options) ? data.options.map((o) => String(o)) : [],
		});
	}

	function removeInteraction(id) {
		const next = items.value.filter((it) => it.id !== id);
		if (next.length !== items.value.length) items.value = next;
		delete answers[id];
		delete picked[id];
	}

	async function submit(item, decision) {
		const params = new URLSearchParams();
		params.set("decision", decision);
		params.set("kind", item.kind);
		if (decision === "accept") {
			if (item.kind === "select") {
				const value = picked[item.id] || "";
				if (item.options.indexOf(value) < 0) {
					failed.value = "请先选择一个候选项。";
					return;
				}
				params.set("value", value);
			} else if (item.kind === "input") {
				const value = String(answers[item.id] || "").trim();
				if (!value) {
					failed.value = "请先输入回答。";
					return;
				}
				params.set("value", value);
			}
		}
		failed.value = "";
		try {
			const res = await fetch(
				"/threads/" + encodeURIComponent(threadId) + "/interaction/" + encodeURIComponent(item.id),
				{
					method: "POST",
					credentials: "same-origin",
					headers: {
						"X-CSRF-Token": csrf,
						"Content-Type": "application/x-www-form-urlencoded",
					},
					body: params.toString(),
				},
			);
			if (!res.ok) {
				failed.value = "提交失败（HTTP " + res.status + "），请重试。";
				return;
			}
			removeInteraction(item.id);
		} catch {
			failed.value = "提交失败，请重试。";
		}
	}

	onMounted(() => {
		if (!threadId) return;
		es = new EventSource("/events/" + encodeURIComponent(threadId));
		es.addEventListener("evt.interaction.requested", (e) => {
			try { addInteraction(JSON.parse(e.data)); } catch { /* 忽略坏帧 */ }
		});
		es.addEventListener("evt.interaction.resolved", (e) => {
			try {
				const d = JSON.parse(e.data);
				if (d && d.interactionId) removeInteraction(String(d.interactionId));
			} catch { /* 忽略坏帧 */ }
		});
		es.addEventListener("evt.run.status", (e) => {
			try {
				const d = JSON.parse(e.data);
				if (d && d.status === "terminal") items.value = [];
			} catch { /* 忽略坏帧 */ }
		});
	});
	onUnmounted(() => {
		if (es !== null) es.close();
	});

	return { items, answers, picked, failed, submit };
};
