// cron-manager 前端段（cron 页面）逻辑 —— 旧 scripts 型裸 DOM（static/cron-manager.js
// + data-cm-* 属性协议）收编进组件契约。
//
// 数据与操作全部走本插件 route（经 ada.api 桥，CSRF 由宿主封装）：
//   GET  /state            任务列表（含下次运行预览）+ 警告 + 调度器状态
//   POST /create|update    新建 / 保存修改
//   POST /remove|toggle    删除 / 启停
//   POST /trigger          立即运行（插件直接调用 turn.invoke）
//   GET  /runs?id=         运行历史（最新在前）
// 工作区列表来自网关 GET /api/workspaces（基座通用投影，GET 无需 CSRF）。
//
// 与旧实现的差异：ada-select 自定义下拉换回原生 select（v-model）；其余观感
// （toast、警告条、分段筛选、运行历史卡）保持一致。
import { ref, reactive, computed, inject, onMounted, onUnmounted } from "/static/ada/ada-runtime.mjs";

const DEFAULT_WORKSPACE_LABEL = "综合工作区（默认）";

const PRESETS = [
	{ value: "*/5 * * * *", label: "每 5 分钟" },
	{ value: "*/15 * * * *", label: "每 15 分钟" },
	{ value: "0 * * * *", label: "每小时" },
	{ value: "daily", label: "每天（指定时刻）" },
	{ value: "weekly", label: "每周（指定星期与时刻）" },
	{ value: "custom", label: "自定义 cron 表达式" },
];

const RUN_LABELS = { error: "失败", busy: "线程忙", started: "已启动", missed: "错过" };

function runLabel(status) {
	return RUN_LABELS[status] || String(status || "—");
}

function errorText(err) {
	const message = err && err.message ? String(err.message) : String(err);
	const cut = message.indexOf("失败: ");
	if (cut >= 0) return message.slice(cut + "失败: ".length);
	return message || "操作失败";
}

function workspaceLabel(path) {
	const raw = String(path || "").replace(/[\\/]+$/, "");
	if (!raw) return DEFAULT_WORKSPACE_LABEL;
	const parts = raw.split(/[\\/]/);
	return parts[parts.length - 1] || raw;
}

function duration(ms) {
	const n = Number(ms) || 0;
	if (n <= 0) return "—";
	if (n < 1000) return n + " ms";
	return (n / 1000).toFixed(1) + " s";
}

export const setup = () => {
	const ada = inject("ada");

	const state = ref(null);
	const busy = ref(false);
	const editingId = ref("");
	const filter = ref("all");
	const refreshed = ref("");
	const toast = ref(null);
	let toastSeq = 0;
	let toastTimer = 0;

	// 工作区列表来自网关 GET /api/workspaces；空字符串代表默认工作区。
	const workspaceList = ref([]);
	const workspaceDefault = ref("");

	const form = reactive({
		name: "",
		preset: "*/5 * * * *",
		dailyTime: "09:00",
		weeklyDay: "1",
		weeklyTime: "09:00",
		cron: "",
		workspace: "",
		text: "",
	});

	const runsOpen = ref(false);
	const runsName = ref("");
	const runs = ref([]);
	let runsTaskId = null;
	let triggerPollToken = 0;

	function showToast(message, tone) {
		toastSeq += 1;
		const seq = toastSeq;
		toast.value = { id: seq, message, tone: tone || "info" };
		if (toastTimer) clearTimeout(toastTimer);
		toastTimer = setTimeout(() => {
			if (toast.value && toast.value.id === seq) toast.value = null;
		}, tone === "error" ? 4200 : 2400);
	}

	// loadState 是只读刷新：不受 busy 守卫（否则写操作完成后的内部刷新会被
	// 吞掉，表格停在旧数据）；写操作各自的 busy 守卫防的是重复提交。
	async function loadState(silent) {
		try {
			const data = await ada.api("/state");
			state.value = data || {};
			refreshed.value = clockText();
			if (!silent) showToast(state.value.tasks && state.value.tasks.length ? "已加载 " + state.value.tasks.length + " 个任务。" : "暂无任务。");
		} catch (err) {
			showToast("读取失败：" + errorText(err), "error");
		}
	}

	async function loadWorkspaces() {
		try {
			const res = await fetch("/api/workspaces", {
				headers: { Accept: "application/json" },
				credentials: "same-origin",
			});
			if (!res.ok) throw new Error("HTTP " + res.status);
			const data = await res.json();
			workspaceDefault.value = data && data.default ? String(data.default) : "";
			workspaceList.value = (data && data.workspaces) || [];
		} catch {
			// 读不到列表不阻塞页面：保留「默认」一项，任务仍会在综合工作区运行。
		}
	}

	const tasks = computed(() => (state.value && Array.isArray(state.value.tasks) ? state.value.tasks : []));
	const warnings = computed(() => (state.value && Array.isArray(state.value.warnings) ? state.value.warnings : []));
	const counts = computed(() => {
		let all = 0;
		let active = 0;
		let paused = 0;
		tasks.value.forEach((t) => {
			all += 1;
			if (t.enabled) active += 1;
			else paused += 1;
		});
		return { all, active, paused };
	});
	const visibleTasks = computed(() => {
		if (filter.value === "active") return tasks.value.filter((t) => !!t.enabled);
		if (filter.value === "paused") return tasks.value.filter((t) => !t.enabled);
		return tasks.value;
	});
	const workspaceOptions = computed(() => {
		const out = [];
		const seen = {};
		for (const ws of workspaceList.value) {
			const path = String((ws && ws.path) || "");
			if (!path || seen[path]) continue;
			seen[path] = true;
			out.push({ value: path, label: (ws.name ? String(ws.name) : workspaceLabel(path)) + " — " + path });
		}
		// 任务指向的工作区已不在列表里：保留原值可编辑/改回默认，而不是悄悄改掉。
		const current = String(form.workspace || "");
		if (current && !seen[current]) {
			out.push({ value: current, label: workspaceLabel(current) + " — " + current + "（不在当前列表）" });
		}
		return out;
	});

	// ---- 调度预设 → cron 表达式 ----

	function presetValue() {
		const kind = String(form.preset || "custom");
		if (kind === "daily") {
			const t = form.dailyTime || "09:00";
			const parts = t.split(":");
			return (Number(parts[0]) || 0) + " " + (Number(parts[1]) || 0) + " * * *";
		}
		if (kind === "weekly") {
			const wt = form.weeklyTime || "09:00";
			const wp = wt.split(":");
			return (Number(wp[0]) || 0) + " " + (Number(wp[1]) || 0) + " * * " + (form.weeklyDay || "1");
		}
		if (kind === "custom") {
			return String(form.cron || "").trim();
		}
		return kind; // 直接是 cron 的预设（每 5 分钟等）
	}

	const cronPreview = computed(() => {
		if (form.preset === "custom") return "";
		const expr = presetValue();
		return "= " + (expr || "（无效预设）");
	});

	function cronForSave() {
		const kind = String(form.preset || "custom");
		if (kind === "custom") {
			const raw = String(form.cron || "").trim();
			if (!raw) throw new Error("需要 cron 表达式");
			return raw;
		}
		const expr = presetValue();
		if (!expr || expr.split(/\s+/).length !== 5) throw new Error("预设生成的 cron 表达式无效");
		return expr;
	}

	function padTime(text) {
		return text.length < 2 ? "0" + text : text;
	}

	// 编辑回填：尽量把表达式映射回预设；映射不上就落到「自定义」。
	function syncFormFromTask(cron) {
		const matchedPreset = PRESETS.some((p) => p.value === cron);
		if (matchedPreset) {
			form.preset = cron;
			return;
		}
		let m = /^(\d{1,2}) (\d{1,2}) \* \* (\d)$/.exec(cron || "");
		if (m) {
			form.preset = "weekly";
			form.weeklyDay = m[3];
			form.weeklyTime = padTime(m[1]) + ":" + padTime(m[2]);
			return;
		}
		m = /^(\d{1,2}) (\d{1,2}) \* \* \*$/.exec(cron || "");
		if (m) {
			form.preset = "daily";
			form.dailyTime = padTime(m[1]) + ":" + padTime(m[2]);
			return;
		}
		form.preset = "custom";
		form.cron = cron || "";
	}

	function resetForm() {
		editingId.value = "";
		form.name = "";
		form.preset = "*/5 * * * *";
		form.dailyTime = "09:00";
		form.weeklyDay = "1";
		form.weeklyTime = "09:00";
		form.cron = "";
		form.workspace = "";
		form.text = "";
	}

	async function submitForm() {
		if (busy.value) return;
		const name = String(form.name || "").trim();
		if (!name) {
			showToast("请填写任务名。", "error");
			return;
		}
		let cron;
		try {
			cron = cronForSave();
		} catch (err) {
			showToast(errorText(err), "error");
			return;
		}
		const text = String(form.text || "").trim();
		if (!text) {
			showToast("AI 任务需要提示词。", "error");
			return;
		}
		const payload = { name: name, cron: cron, workspace: form.workspace, action: { type: "agent", text: text }, enabled: true };
		busy.value = true;
		try {
			if (editingId.value) {
				await ada.api("/update", { method: "POST", body: { id: editingId.value, task: payload } });
				showToast("已保存修改，下一次 tick（≤30 秒）生效。");
			} else {
				await ada.api("/create", { method: "POST", body: payload });
				showToast("已创建任务，下一次 tick（≤30 秒）生效。");
			}
			resetForm();
			await loadState(true);
		} catch (err) {
			showToast((editingId.value ? "保存失败：" : "创建失败：") + errorText(err), "error");
		} finally {
			busy.value = false;
		}
	}

	// ---- 行操作 ----

	async function onToggle(id) {
		if (busy.value) return;
		busy.value = true;
		try {
			let enabled = null;
			tasks.value.forEach((t) => {
				if (t.id === id) enabled = !t.enabled;
			});
			await ada.api("/toggle", { method: "POST", body: { id: id, enabled: enabled } });
			await loadState(true);
			showToast(enabled ? "已启用。" : "已停用。");
		} catch (err) {
			showToast("操作失败：" + errorText(err), "error");
		} finally {
			busy.value = false;
		}
	}

	async function onTrigger(id) {
		if (busy.value) return;
		busy.value = true;
		try {
			const before = runCountOf(id);
			await ada.api("/trigger", { method: "POST", body: { id: id } });
			showToast("已标记立即运行，正在等待结果…");
			await loadState(true);
			waitForTriggerResult(id, before);
		} catch (err) {
			showToast("操作失败：" + errorText(err), "error");
		} finally {
			busy.value = false;
		}
	}

	function onRemove(id) {
		if (busy.value) return;
		let name = "";
		tasks.value.forEach((t) => {
			if (t.id === id) name = t.name;
		});
		ada.confirm("删除定时任务「" + (name || id) + "」？运行历史会一并删除。").then((ok) => {
			if (!ok) return;
			busy.value = true;
			ada.api("/remove", { method: "POST", body: { id: id } })
				.then(async () => {
					if (editingId.value === id) resetForm();
					if (runsTaskId === id) hideRuns();
					await loadState(true);
					showToast("已删除。");
				})
				.catch((err) => {
					showToast("删除失败：" + errorText(err), "error");
				})
				.finally(() => {
					busy.value = false;
				});
		});
	}

	function onEdit(id) {
		tasks.value.forEach((t) => {
			if (t.id !== id) return;
			editingId.value = t.id;
			form.name = t.name || "";
			syncFormFromTask(t.cron || "");
			form.workspace = t.workspace || "";
			form.text = (t.action && t.action.text) || "";
		});
	}

	function taskByID(id) {
		let found = null;
		tasks.value.forEach((t) => {
			if (t.id === id) found = t;
		});
		return found;
	}

	function runCountOf(id) {
		const task = taskByID(id);
		return task && Array.isArray(task.runs) ? task.runs.length : 0;
	}

	// ---- 运行历史 ----

	async function loadRuns(id) {
		try {
			const data = await ada.api("/runs?id=" + encodeURIComponent(id) + "&limit=20");
			runs.value = (data && data.runs) || [];
		} catch (err) {
			showToast("读取历史失败：" + errorText(err), "error");
		}
	}

	async function showRuns(id) {
		const task = taskByID(id);
		runsTaskId = id;
		runsOpen.value = true;
		runsName.value = task ? task.name : id;
		await loadRuns(id);
	}

	function hideRuns() {
		runsTaskId = null;
		runsOpen.value = false;
		runs.value = [];
	}

	function runDetail(run) {
		let detail = run && run.detail ? String(run.detail) : "";
		if (run && run.threadId) detail = (detail ? detail + " · " : "") + "线程 " + run.threadId;
		return detail || "—";
	}

	function runTone(status) {
		if (status === "started") return "ok";
		if (status === "busy" || status === "missed") return "warn";
		return status ? "bad" : "none";
	}

	function lastRunStatus(task) {
		const runsOf = (task && task.runs) || [];
		const last = runsOf.length ? runsOf[runsOf.length - 1] : null;
		return last ? last.status : "";
	}

	function lastRunTone(task) {
		const runsOf = (task && task.runs) || [];
		if (!runsOf.length) return "none";
		return runTone(lastRunStatus(task));
	}

	function refresh() {
		loadWorkspaces();
		loadState(false);
	}

	// 「立即运行」是异步的（插件经调度层投递后轮次在后台执行），触发后轮询表格与历史，
	// 让用户看到「已启动/线程忙/失败」而不是一片安静。
	function waitForTriggerResult(id, before) {
		const token = ++triggerPollToken;
		const deadline = Date.now() + 15000;
		const tick = async () => {
			if (token !== triggerPollToken) return;
			await loadState(true);
			if (runsTaskId === id) await loadRuns(id);
			const task = taskByID(id);
			const runsOf = (task && task.runs) || [];
			if (runsOf.length > before) {
				const last = runsOf[runsOf.length - 1];
				const tone = runTone(last.status);
				showToast("立即运行结果：" + runLabel(last.status) + (last.detail ? "（" + last.detail + "）" : ""), tone === "bad" ? "error" : "info");
				return;
			}
			if (Date.now() > deadline) {
				showToast("已触发；结果还没写入，稍后点「历史」查看。", "error");
				return;
			}
			setTimeout(tick, 2000);
		};
		setTimeout(tick, 1500);
	}

	function formatTime(value) {
		if (!value) return "—";
		const d = new Date(value);
		if (isNaN(d.getTime())) return String(value);
		const pad = (n) => (n < 10 ? "0" + String(n) : String(n));
		return pad(d.getMonth() + 1) + "-" + pad(d.getDate()) + " " + pad(d.getHours()) + ":" + pad(d.getMinutes());
	}

	function clockText() {
		const d = new Date();
		const pad = (n) => (n < 10 ? "0" + String(n) : String(n));
		return pad(d.getHours()) + ":" + pad(d.getMinutes()) + ":" + pad(d.getSeconds());
	}

	onMounted(() => {
		loadWorkspaces();
		loadState(true);
	});
	onUnmounted(() => {
		triggerPollToken += 1; // 组件卸载后轮询停止
		if (toastTimer) clearTimeout(toastTimer);
	});

	return {
		state, busy, editingId, filter, refreshed, toast,
		counts, visibleTasks, warnings, workspaceOptions, workspaceDefault,
		form, presets: PRESETS, cronPreview,
		runsOpen, runsName, runs,
		loadState, refresh, resetForm, submitForm,
		onToggle, onTrigger, onRemove, onEdit, showRuns, hideRuns,
		workspaceLabel, runLabel, runTone, lastRunTone, lastRunStatus, runDetail, duration, formatTime,
	};
};
