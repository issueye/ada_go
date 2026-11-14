// composer-telemetry 前端段（composer.footer）逻辑。
//
// 数据面（与旧 composer-footer.tsx 相同的两条来源）：
//   - 运行状态 / 上下文占用：基座通用投影 GET /threads/{id}/state（thread.get）；
//   - token / 缓存：本插件 route GET /usage?threadId=…（events.read 聚合，经 ada.api）。
// 运行中每 2s 轮询一次，进入终态即停；没有会话上下文（非会话页）时只渲染
// 占位空根（telemetry.css 把 .ada-telemetry__pending 置为 display:none）。
//
// 降级：旧实现的 SVG 环形进度（viewBox circle dashoffset）在模板白名单（无 svg）
// 下不可用，改为文本百分比 + div 计量条（scoped CSS，仅引用设计令牌）。
import { ref, computed, inject, onMounted, onUnmounted } from "/static/ada/ada-runtime.mjs";

const POLL_INTERVAL_MS = 2000;
const RUN_LABELS = { running: "运行中", waiting_input: "等待答复", finishing: "收尾中" };

function num(value) {
	const n = Number(value);
	return isFinite(n) ? n : 0;
}

// compactTokenCount 与基座展示口径一致：≥10 取整，否则保留 1 位小数并去掉 ".0"。
function compactTokenCount(n) {
	if (!isFinite(n) || n < 0) n = 0;
	if (n >= 1000000) {
		const v = n / 1000000;
		return (v >= 10 ? String(Math.round(v)) : trimZero(v.toFixed(1))) + "M";
	}
	if (n >= 1000) {
		const v = n / 1000;
		return (v >= 10 ? String(Math.round(v)) : trimZero(v.toFixed(1))) + "K";
	}
	return String(Math.round(n));
}

function trimZero(text) {
	return text.endsWith(".0") ? text.slice(0, -2) : text;
}

function percentOf(part, total) {
	if (!isFinite(total) || total <= 0) return 0;
	return Math.max(0, Math.min(100, Math.round((part * 100) / total)));
}

// contextView 与基座 buildContextView 同源的口径（含「供应商未声明窗口按 1M 估算」兜底）。
function contextView(ctx) {
	if (!ctx || typeof ctx !== "object") return null;
	const assumed = num(ctx.window) <= 0;
	const windowTokens = assumed ? 1048576 : num(ctx.window);
	const tokens = num(ctx.tokens);
	const percent = assumed ? percentOf(tokens, windowTokens) : Math.max(0, Math.min(100, num(ctx.percent)));
	let messages = num(ctx.messages);
	if (messages === 0 && num(ctx.system) === 0 && num(ctx.tools) === 0) messages = tokens;
	const messagePercent = percentOf(messages, windowTokens);
	let hint = "";
	if (assumed) hint = "模型未声明窗口，占比按 1M 估算。";
	else if (messagePercent >= 80) hint = "对话消息已到压缩水位，下一轮会先压缩历史。";
	else if (percent >= 80) hint = "接近窗口上限，建议压缩历史或新开会话。";
	return {
		percent,
		high: percent >= 80,
		tokens: compactTokenCount(tokens),
		windowLabel: compactTokenCount(windowTokens),
		assumed,
		system: compactTokenCount(num(ctx.system)),
		tools: compactTokenCount(num(ctx.tools)),
		messages: compactTokenCount(messages),
		hint,
	};
}

export const setup = () => {
	const ada = inject("ada");
	const bootstrap = inject("bootstrap");
	const threadId = ada.thread ? String(ada.thread.id || "") : "";
	const workspaceName = String((bootstrap && (bootstrap.workspaceName || bootstrap.workspace)) || "");
	const ready = ref(false);
	const run = ref(null);
	const context = ref(null);
	const usage = ref(null);
	let timer = 0;
	let inflight = false;

	function stop() {
		if (timer) {
			clearInterval(timer);
			timer = 0;
		}
	}

	function schedule() {
		const status = run.value ? String(run.value.status || "") : "";
		if (status === "terminal") {
			stop();
			return;
		}
		if (!timer) timer = setInterval(() => refresh(), POLL_INTERVAL_MS);
	}

	async function refresh() {
		if (!threadId || inflight) return;
		inflight = true;
		try {
			const [state, usageBody] = await Promise.all([
				fetch("/threads/" + encodeURIComponent(threadId) + "/state", {
					credentials: "same-origin",
					headers: { Accept: "application/json" },
				}).then((res) => (res.ok ? res.json() : null)),
				ada.api("usage?threadId=" + encodeURIComponent(threadId)).catch(() => null),
			]);
			// 非 2xx 或空响应：保持上一次的值，下个周期再试（与旧实现一致）。
			if (state && typeof state === "object") {
				run.value = state.run || null;
				context.value = state.context || null;
				ready.value = true;
			}
			// 用量来自插件自己的 route；失败时保持上一次的值。
			if (usageBody && typeof usageBody === "object") usage.value = usageBody;
		} catch {
			// 失败不打断页面：保持上一次的值，并在下一个周期再试。
		} finally {
			inflight = false;
		}
		schedule();
	}

	const runLabel = computed(() => (run.value ? RUN_LABELS[String(run.value.status || "")] || "" : ""));
	const runActive = computed(() => {
		const s = run.value ? String(run.value.status || "") : "";
		return s === "running" || s === "finishing";
	});
	const contextInfo = computed(() => contextView(context.value));

	// speedLabel —— 生成速度（tok/s，2026-11-12 按新版输入框设计）：
	// 本轮累计输出 token ÷ 已运行秒数。数据来自 thread.get 的 run.usage
	// （Core 把 evt.usage 的累计快照收敛进 runInfo）；终态后冻结成整轮平均
	// 速度（用 endedAt - startedAt 而不是当前时刻，避免数字随时间漂）。
	const nowTick = ref(Date.now());
	let tickTimer = 0;
	const speedLabel = computed(() => {
		const r = run.value;
		if (!r) return "";
		const u = r.usage;
		const ended = Date.parse(String(r.endedAt || ""));
		const started = Date.parse(String(r.startedAt || ""));
		if (!u || Number.isNaN(started)) return "";
		const end = Number.isNaN(ended) ? nowTick.value : ended;
		const secs = (end - started) / 1000;
		if (secs < 1) return "";
		const out = num(u.outputTokens);
		if (out <= 0) return "";
		const perSec = out / secs;
		// 小数位按量级收：>10 取整，否则保留 1 位（与 token 数展示同一口径）。
		return (perSec >= 10 ? String(Math.round(perSec)) : trimZero(perSec.toFixed(1))) + " tok/s";
	});
	const speedTitle = computed(() => {
		const r = run.value;
		const u = r && r.usage ? r.usage : null;
		if (!u) return "生成速度";
		const out = num(u.outputTokens);
		const started = Date.parse(String(r.startedAt || ""));
		const ended = Date.parse(String(r.endedAt || ""));
		const end = Number.isNaN(ended) ? nowTick.value : ended;
		const secs = Number.isNaN(started) ? 0 : Math.max(0, Math.round((end - started) / 1000));
		return "本轮已生成 " + compactTokenCount(out) + " token，用时 " + secs + " 秒";
	});

	// cacheLabel —— 缓存命中率：优先读本轮 run.usage（实时），
	// 没有实时用量时退回插件 route 的线程聚合（历史会话也看得到）。
	const cacheLabel = computed(() => {
		const r = run.value;
		const u = r && r.usage ? r.usage : null;
		if (u) {
			const hit = percentOf(num(u.cacheTokens), num(u.inputTokens) + num(u.cacheTokens));
			return hit > 0 ? hit + "%" : "—";
		}
		const thread = usage.value && usage.value.thread ? usage.value.thread : null;
		if (!thread) return "";
		const input = num(thread.inputTokens);
		if (input <= 0) return "—";
		return percentOf(num(thread.cacheTokens), input) + "%";
	});

	onMounted(() => {
		// 先取一次：首屏空壳紧接着就有值；轮询只在运行中继续。
		if (threadId) refresh();
		// 运行中每秒推进 nowTick：tok/s 的分母是「已运行秒数」，
		// 只靠 2s 轮询会让数字顿挫。
		tickTimer = setInterval(() => { nowTick.value = Date.now(); }, 1000);
	});
	onUnmounted(() => {
		stop();
		if (tickTimer) {
			clearInterval(tickTimer);
			tickTimer = 0;
		}
	});

	return { ready, workspaceName, runLabel, runActive, contextInfo, cacheLabel, speedLabel, speedTitle };
};
