// Thread.js —— 会话页逻辑（HEAD chat.html.tmpl + chat-live.js + app.js 会话部分
// 的 SPA 移植）：transcript JSON + SSE 增量流 + composer（图片/草稿/模型/待发送
// 队列）+ 交互托盘（基座外壳）+ 顶部动作（刷新/停止）。
//
// 数据面：/partials/transcript/{id}（全量行，支持 ?before 游标翻页）、
// /threads/{id}/state（run/context/interactions）、/partials/model.json、
// /events/{id}（evt.message.delta 增量、evt.item.updated 落库、evt.run.status）。
import { ref, reactive, computed, watch, onMounted, onUnmounted, nextTick, inject } from "/static/ada/ada-runtime.mjs";
import { shellState } from "./state.js";
import { runView, runErrorLabel, toolIcon as toolIconName, statusTone as toolTone, threadPath, greetingText } from "./format.js";

const DRAFT_PREFIX = "a_da_draft:";
const DRAFT_DELAY = 250;
const OUTBOX_PREFIX = "a_da_outbox:";
const OUTBOX_LIMIT = 20;
const REFRESH_INTERVAL = 800;
const MARKDOWN_DELAY = 120;
const MARKDOWN_RETRY = 600;

function storage() {
	try { return window.localStorage; } catch { return null; }
}

export const setup = (props) => {
	const fetch = inject("fetch");
	const router = inject("router");
	const bootstrap = inject("bootstrap");
	const confirm = inject("confirmService");
	const registry = inject("registry");
	const imageViewer = inject("imageViewer");

	// threadId 为空串时是新建任务页（/threads）；HEAD 的 ada-newtask 同位。
	const threadId = String(props.params?.id ?? "");
	const productName = String(bootstrap.productName ?? "ada-go");
	const brandName = String(bootstrap.brandName ?? "ada-go");
	const logoStyle = { backgroundImage: "url(/static/logo.png)" };
	const greeting = threadId === "" ? greetingText(new Date()) : "";

	// ---- 会话状态 ----
	const rows = ref([]);
	const hasOlder = ref(false);
	const olderCursor = ref("");
	const loadFailed = ref(null);
	const run = reactive({ status: "", terminal: "", label: "空闲", active: false, startedAt: "", endedAt: "", errorCode: 0, errorLabel: "", errorDetail: "", usage: null });
	const interactions = ref([]);
	const statusText = ref("");
	const statusLevel = ref("info");
	const statusRetry = ref(false);
	// 流式行是持续追加的对象：必须用 reactive（ref 是浅响应，嵌套变更不触发）。
	const live = reactive({ active: false, messageId: "", thinking: "", text: "", nodes: null, renderedText: "" });
	const liveActive = computed(() => live.active);
	const persisted = new Set();

	shellState.title = threadId === "" ? "新建任务" : "会话";
	shellState.topbar.clear();

	// ---- composer ----
	const draft = ref("");
	const files = ref([]);
	const thumbs = ref([]);
	const dragging = ref(false);
	const sending = ref(false);
	const workspaces = (bootstrap.workspaces ?? []).map((w) => ({ name: w.name, path: w.path }));
	const wsSelected = ref(String(bootstrap.workspace ?? ""));
	const wsLabel = computed(() => {
		const w = workspaces.find((x) => x.path === wsSelected.value);
		return w ? w.name : workspaceName(wsSelected.value);
	});
	const workspacePath = computed(() => wsSelected.value);
	const imageInputID = "ada-image-input-" + (threadId === "" ? "new" : threadId);
	const filePickerReady = ref(false);
	let fileInput = null;

	// ---- 模型选择器 ----
	const models = ref([]);
	const currentModelLabel = ref("选择模型");
	const modelOpen = ref(false);
	// 当前模型的推理档位（选模型时从 backend 读回；没有档位的模型为空）。
	const currentModelLevel = ref("");
	// modelChipLabel 是输入框右下角那枚模型 chip 的文案：「模型名」或
	// 「模型名 · 档位」。新版输入框把「模型 + 推理档位」合成一枚 chip，
	// 档位不再单开浮层以外的地方展示。
	const modelChipLabel = computed(() => {
		const name = String(currentModelLabel.value || "选择模型");
		const level = String(currentModelLevel.value || "");
		return level === "" ? name : name + " " + level;
	});

	// 推理等级子菜单：HEAD（app.js showCascade/hideCascade）是 body 级 fixed 浮层，
	// 定位到触发选项右侧（空间不足向左/向上翻转、视口内夹取）。这里沿用同一形态：
	// 浮层挂在 document.body，不受对话滚动容器裁剪；滚动/外部点击/Esc 关闭。
	let cascadeEl = null;
	let cascadeModel = null;
	let cascadeAnchor = null;
	let cascadeFocusFirst = false;

	// ---- outbox（待发送队列）----
	const outbox = ref([]);

	// ---- 编辑重发 ----
	const editText = ref("");
	let editRow = null;

	// ---- 交互托盘 ----
	const trayValues = reactive({});
	const wizardAnswers = reactive({});
	const wizardSteps = reactive({});

	function workspaceName(path) {
		const base = String(path ?? "").split(/[\\/]/).filter(Boolean).pop();
		return base || String(path ?? "");
	}

	// =========================================================================
	// transcript（全量行 + keyed 渲染）
	// =========================================================================

	async function loadTranscript(opts = {}) {
		try {
			const data = await fetch(`/partials/transcript/${encodeURIComponent(threadId)}`);
			rows.value = data.rows ?? [];
			run.status = data.runStatus ?? run.status;
			hasOlder.value = data.hasMore === true;
			olderCursor.value = data.nextCursor ?? "";
			loadFailed.value = null;
			await nextTick();
			if (opts.stick) scrollToBottom();
			dropLive();
		} catch (err) {
			loadFailed.value = err?.message ?? String(err);
		}
	}

	// 「加载更早消息」：游标翻页（8.2），旧行前插；行 key 稳定，内容不变。
	async function loadOlder() {
		if (!olderCursor.value) return;
		try {
			const q = `?before=${encodeURIComponent(olderCursor.value)}&limit=100`;
			const data = await fetch(`/partials/transcript/${encodeURIComponent(threadId)}${q}`);
			const older = data.rows ?? [];
			rows.value = [...older, ...rows.value];
			hasOlder.value = data.hasMore === true;
			olderCursor.value = data.nextCursor ?? "";
		} catch (err) {
			showStatus(`加载历史失败：${err?.message ?? err}`, "error");
		}
	}

	// =========================================================================
	// state（run / interactions）
	// =========================================================================

	async function loadState() {
		try {
			const data = await fetch(`/threads/${encodeURIComponent(threadId)}/state`);
			applyRun(data.run ?? {});
			interactions.value = data.interactions ?? [];
			syncTrayDefaults();
		} catch {
			/* 状态取不到时保留上次值；transcript 失败已有独立提示 */
		}
	}

	function applyRun(raw) {
		const v = runView(raw);
		run.status = v.status;
		run.terminal = v.terminal;
		run.label = v.label;
		run.active = v.active;
		run.startedAt = v.startedAt ?? "";
		// endedAt 是终态时刻：tok/s 在终态后要用它冻结成整轮平均速度。
		run.endedAt = String(raw?.endedAt ?? "");
		run.errorCode = raw?.errorCode ?? 0;
		run.errorLabel = v.errorLabel ?? "";
		const detail = String(raw?.errorDetail ?? "").trim().slice(0, 600);
		run.errorDetail = detail;
		// 本轮累计用量（thread.get 的 run.usage）：tok/s 与缓存命中率的数据源。
		run.usage = raw?.usage ?? null;
		if (run.errorLabel === "" && run.terminal === "error") run.errorLabel = "本轮执行失败；请检查供应商配置后重试。";
	}

	function syncTrayDefaults() {
		for (const x of interactions.value) {
			if (x.kind === "form") {
				if (!Array.isArray(wizardAnswers[x.id])) {
					wizardAnswers[x.id] = (x.questions ?? []).map((q) => (q.kind === "confirm" ? "yes" : q.kind === "select" && q.options.length > 0 ? q.options[0] : ""));
				}
				if (typeof wizardSteps[x.id] !== "number") wizardSteps[x.id] = 0;
			} else if (x.kind === "select" && x.options.length > 0 && trayValues[x.id] === undefined) {
				trayValues[x.id] = x.options[0];
			} else if (trayValues[x.id] === undefined) {
				trayValues[x.id] = "";
			}
		}
	}

	// =========================================================================
	// SSE（/events/{threadId}）：delta 增量进流式行，落库/终态全量重拉
	// =========================================================================

	let es = null;
	let esFailures = 0;
	let reconnectTimer = null;

	function connect() {
		if (threadId === "" || typeof window.EventSource !== "function") return;
		try {
			es = new EventSource(`/events/${encodeURIComponent(threadId)}`);
		} catch {
			showStatus("无法建立实时连接，正在重试…", "warn");
			return;
		}
		es.addEventListener("open", () => {
			esFailures = 0;
			clearStatus();
		});
		es.addEventListener("evt.message.delta", (event) => handleDelta(event.data));
		es.addEventListener("evt.item.updated", (event) => handleItemUpdated(event.data));
		es.addEventListener("evt.run.status", (event) => handleRunStatusEvent(event.data));
		es.addEventListener("evt.interaction.requested", () => { void loadState(); });
		es.addEventListener("evt.interaction.resolved", () => { void loadState(); });
		es.addEventListener("evt.error", (event) => handleErrorEvent(event.data));
		es.addEventListener("resync", () => {
			void loadTranscript({ stick: true });
			void loadState();
		});
		es.addEventListener("error", () => {
			esFailures += 1;
			if (esFailures <= 3) showStatus("连接断开，正在重试…", "warn");
			else {
				statusRetry.value = true;
				showStatus("连接断开，已多次重试失败。", "error");
			}
		});
	}

	function reconnect() {
		statusRetry.value = false;
		try { es?.close(); } catch { /* 忽略 */ }
		connect();
	}

	function handleDelta(raw) {
		let data;
		try { data = JSON.parse(raw); } catch { return; }
		if (!data) return;
		const mid = String(data.messageId ?? "");
		if (mid !== "" && persisted.has(mid)) return; // 已落库的消息由重拉接管
		if (!live.active || (mid !== "" && live.messageId !== "" && live.messageId !== mid)) {
			live.active = true;
			live.messageId = mid;
			live.thinking = "";
			live.text = "";
			live.nodes = null;
			live.renderedText = "";
		} else if (mid !== "" && live.messageId === "") {
			live.messageId = mid;
		}
		const kind = data.partKind ?? "text";
		if (kind === "thinking") {
			const thought = String(data.thinking ?? "");
			if (thought !== "") live.thinking += thought;
		} else {
			const chunk = String(data.text ?? "");
			if (chunk !== "") {
				live.text += chunk;
				scheduleMarkdown();
			}
		}
		followOutput();
	}

	function handleItemUpdated(raw) {
		let data = {};
		try { data = JSON.parse(raw) ?? {}; } catch { data = {}; }
		const key = String(data.key ?? "");
		const status = String(data.status ?? "");
		if (key.startsWith("msg:") && (status === "end" || status === "finished")) {
			// 消息落库：立即换行（全量重拉），成功后扔掉对应的实时块。
			persisted.add(key.slice(4));
			flushTranscriptRefresh();
			return;
		}
		scheduleTranscriptRefresh();
	}

	function handleRunStatusEvent(raw) {
		let data = {};
		try { data = JSON.parse(raw) ?? {}; } catch { data = {}; }
		const status = String(data.status ?? "").toLowerCase();
		const terminal = String(data.terminal ?? "").toLowerCase();
		applyRun({ ...data, status, terminal });
		if (terminal !== "" || status === "terminal" || ["completed", "cancelled", "error", "interrupted", "budget_exceeded"].includes(status)) {
			onRunEnd(terminal || status);
			return;
		}
		if (status === "waiting_input") {
			void loadTranscript({ stick: true });
			void loadState();
			return;
		}
		if (status === "running") showStatus("正在生成…", "info");
	}

	// 终态：HEAD reloadOnce → SPA 全量重拉 + 状态刷新；completed 先发队首
	// （outbox.onRunEnd 语义）。
	function onRunEnd(reason) {
		clearStatus();
		if (String(reason) === "completed" && outbox.value.length > 0) {
			void sendOutbox(outbox.value[0].id, true);
			return;
		}
		void loadTranscript({ stick: true });
		void loadState();
	}

	function handleErrorEvent(raw) {
		let data = {};
		try { data = JSON.parse(raw) ?? {}; } catch { data = {}; }
		const message = data.message ?? data.detail ?? "出现可恢复错误。";
		const diag = data.diag ?? data.name ?? "";
		showStatus(diag !== "" ? `${diag}：${message}` : message, "error");
	}

	function showStatus(text, level) {
		statusText.value = text;
		statusLevel.value = level ?? "info";
	}
	function clearStatus() {
		statusText.value = "";
		statusLevel.value = "info";
		statusRetry.value = false;
	}

	// ---- 流式 Markdown：增量是纯文本，节流交给 /markdown.ast 渲染 ----
	let mdTimer = null;
	let mdRetryTimer = null;
	let mdPending = false;

	function scheduleMarkdown() {
		if (mdTimer || mdRetryTimer || !live.active) return;
		mdTimer = window.setTimeout(() => {
			mdTimer = null;
			void renderMarkdownPart();
		}, MARKDOWN_DELAY);
	}

	async function renderMarkdownPart() {
		if (!live.active || mdPending) return;
		const source = live.text;
		if (source === "" || source === live.renderedText) return;
		mdPending = true;
		try {
			const data = await fetch("/markdown.ast", { method: "POST", body: { text: source } });
			if (!live.active) return;
			live.nodes = Array.isArray(data.nodes) ? data.nodes : null;
			live.renderedText = source;
			followOutput();
			if (live.text !== source) scheduleMarkdown();
		} catch {
			if (mdRetryTimer) return;
			mdRetryTimer = window.setTimeout(() => {
				mdRetryTimer = null;
				scheduleMarkdown();
			}, MARKDOWN_RETRY);
		} finally {
			mdPending = false;
		}
	}

	function dropLive() {
		// 只回收已落库的实时块（HEAD dropPersistedBlocks 语义）：流式中的文本
		// 保留继续追加，否则一次无关的行刷新会把正在生成的回答抹掉。
		if (!live.active) return;
		const mid = live.messageId;
		if (mid === "" || !persisted.has(mid)) return;
		live.active = false;
		live.messageId = "";
		live.thinking = "";
		live.text = "";
		live.nodes = null;
		live.renderedText = "";
	}

	// ---- transcript 重拉的合并窗口（HEAD scheduleTranscriptRefresh 同参）----
	let refreshTimer = null;
	let lastRefreshAt = 0;
	let refreshing = false;
	let refreshAgain = false;

	function scheduleTranscriptRefresh() {
		if (refreshTimer) return;
		const wait = Math.max(0, REFRESH_INTERVAL - (Date.now() - lastRefreshAt));
		refreshTimer = window.setTimeout(() => {
			refreshTimer = null;
			lastRefreshAt = Date.now();
			void refreshTranscript();
		}, wait);
	}

	function flushTranscriptRefresh() {
		if (refreshTimer) {
			window.clearTimeout(refreshTimer);
			refreshTimer = null;
		}
		lastRefreshAt = Date.now();
		void refreshTranscript();
	}

	async function refreshTranscript() {
		if (refreshing) {
			refreshAgain = true;
			return;
		}
		refreshing = true;
		const stick = following;
		try {
			const data = await fetch(`/partials/transcript/${encodeURIComponent(threadId)}`);
			rows.value = data.rows ?? [];
			run.status = data.runStatus ?? run.status;
			hasOlder.value = data.hasMore === true;
			olderCursor.value = data.nextCursor ?? "";
			await nextTick();
			dropLive();
			if (stick) scrollToBottom();
		} catch {
			/* 局部刷新失败不打断流：下一个事件仍会触发重试 */
		} finally {
			refreshing = false;
			if (refreshAgain) {
				refreshAgain = false;
				scheduleTranscriptRefresh();
			}
		}
	}

	// =========================================================================
	// 滚动：打开置底、跟随输出、回到最新按钮
	// =========================================================================

	let following = true;
	let userScrolledAt = 0;
	const showScrollBottom = ref(false);

	function scroller() {
		return document.getElementById("chat-scroll");
	}
	function nearBottom() {
		const el = scroller();
		if (!el) return true;
		return el.scrollTop + el.clientHeight >= el.scrollHeight - 140;
	}
	function scrollToBottom() {
		const el = scroller();
		if (el) el.scrollTop = el.scrollHeight;
	}
	function jumpToBottom() {
		following = true;
		scrollToBottom();
	}
	function followOutput() {
		if (!following) return;
		scrollToBottom();
	}
	function onScroll() {
		showScrollBottom.value = !nearBottom();
		if (nearBottom()) {
			following = true;
			return;
		}
		if (Date.now() - userScrolledAt < 800) following = false;
	}
	function noteUserScroll() {
		userScrolledAt = Date.now();
	}

	// =========================================================================
	// composer：发送 / 图片 / 草稿
	// =========================================================================

	function buildFormData(text, images) {
		const fd = new FormData();
		fd.set("text", text);
		for (const f of images) fd.append("images", f, f.name || "image");
		return fd;
	}

	async function send() {
		const text = draft.value.trim();
		const images = files.value;
		if (threadId === "") return sendNewTask(text, images);
		if (text === "" && images.length === 0) return;
		// 运行中且无图片：进待发送队列（HEAD handleOutboxSubmit 语义）。
		if (runIsActive() && images.length === 0 && text !== "") {
			if (!enqueueOutbox(text)) return;
			draft.value = "";
			saveDraft("");
			return;
		}
		if (sending.value) return;
		sending.value = true;
		try {
			const fd = buildFormData(text, images);
			fd.set("idempotencyKey", newIdempotencyKey());
			await fetch(`/threads/${encodeURIComponent(threadId)}/send`, { method: "POST", body: fd });
			draft.value = "";
			saveDraft("");
			files.value = [];
			thumbs.value = [];
			if (fileInput) fileInput.value = new DataTransfer().files;
			await Promise.all([loadTranscript({ stick: true }), loadState()]);
		} catch (err) {
			showStatus(`发送失败：${err?.message ?? err}`, "error");
		} finally {
			sending.value = false;
		}
	}

	// 新建任务：POST /threads → 303 到 /threads/{id}；fetch 跟随后用 response.url
	// 拿到新会话路径并交给路由（HEAD 是整页跳转，SPA 等价实现）。
	async function sendNewTask(text, images) {
		if (text === "" && images.length === 0) return;
		if (sending.value) return;
		sending.value = true;
		try {
			const fd = buildFormData(text, images);
			if (wsSelected.value) fd.set("workspace", wsSelected.value);
			const res = await window.fetch("/threads", {
				method: "POST",
				credentials: "same-origin",
				headers: { "X-CSRF-Token": bootstrap.csrf ?? "" },
				body: fd,
			});
			if (!res.ok && res.type !== "opaqueredirect") {
				throw new Error(`HTTP ${res.status}`);
			}
			const path = new URL(res.url, window.location.href).pathname;
			if (path.startsWith("/threads/")) {
				router.navigate(path);
			} else {
				router.navigate("/threads");
			}
		} catch (err) {
			showStatus(`创建任务失败：${err?.message ?? err}`, "error");
		} finally {
			sending.value = false;
		}
	}

	function newIdempotencyKey() {
		const b = new Uint8Array(16);
		window.crypto?.getRandomValues?.(b);
		return [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
	}

	function runIsActive() {
		return run.status === "running" || run.status === "waiting_input" || run.status === "finishing";
	}

	// ---- 图片附件（input file + 粘贴 + 拖拽；HEAD chat-images.js 语义）----

	function addFiles(fileList) {
		const incoming = [...(fileList ?? [])].filter((f) => f && f.type && f.type.startsWith("image/"));
		if (incoming.length === 0) return;
		const next = [...files.value, ...incoming].slice(0, 4);
		files.value = next;
		syncFileInput();
		renderThumbs();
	}

	function removeImage(index) {
		const next = files.value.filter((_, i) => i !== index);
		files.value = next;
		syncFileInput();
		renderThumbs();
	}

	function syncFileInput() {
		if (!fileInput) return;
		const dt = new DataTransfer();
		for (const f of files.value) dt.items.add(f);
		fileInput.files = dt.files;
	}

	function renderThumbs() {
		const initial = files.value.map((file, index) => ({
			key: `${index}:${file.name}:${file.size}`,
			name: file.name || "图片",
			style: { backgroundImage: "none" },
		}));
		thumbs.value = initial;
		// FileReader 读成 data URL（CSP img-src 只放行 self/data:，HEAD 同款）；
		// ref 是浅响应，缩略图整体替换而不是改元素属性。
		for (let i = 0; i < initial.length; i++) {
			const key = initial[i].key;
			const reader = new window.FileReader();
			reader.onload = () => {
				const url = String(reader.result || "");
				thumbs.value = thumbs.value.map((t) => (t.key === key ? { ...t, style: { backgroundImage: `url(${url})` } } : t));
			};
			reader.readAsDataURL(files.value[i]);
		}
	}

	function imageStyle(p) {
		return { backgroundImage: `url(data:${p.mime ?? "image/png"};base64,${p.data ?? ""})` };
	}

	// 消息图片灯箱（HEAD chat-images.js 的 data-image-viewer 行为）：
	// 同一消息行里的 image part 组成灯箱图集，点哪张从哪张开始（多图可左右切）。
	function imageHas(p) {
		return typeof p?.data === "string" && p.data !== "";
	}
	function rowImages(row) {
		const out = [];
		for (const p of row?.parts ?? []) {
			if (p?.type !== "image" || !imageHas(p)) continue;
			out.push({ src: `data:${p.mime ?? "image/png"};base64,${p.data}`, alt: "图片" });
		}
		return out;
	}
	function openImage(row, partIndex) {
		const images = rowImages(row);
		if (images.length === 0) return;
		let index = 0;
		const parts = row?.parts ?? [];
		for (let i = 0; i < partIndex && i < parts.length; i++) {
			if (imageHas(parts[i])) index += 1;
		}
		imageViewer.open(images, index);
	}

	function onPaste(event) {
		const clipboardFiles = event.clipboardData?.files;
		if (clipboardFiles && clipboardFiles.length > 0) {
			event.preventDefault();
			addFiles(clipboardFiles);
		}
	}
	let dragDepth = 0;
	function onDragEnter() {
		dragDepth += 1;
		dragging.value = true;
	}
	function onDragLeave() {
		dragDepth -= 1;
		if (dragDepth <= 0) {
			dragDepth = 0;
			dragging.value = false;
		}
	}
	function onDrop(event) {
		dragDepth = 0;
		dragging.value = false;
		const dropped = event.dataTransfer?.files;
		if (dropped && dropped.length > 0) addFiles(dropped);
	}
	function noop() { /* 修饰符占位：@dragover.prevent 需要合法表达式 */ }

	function openWorkspacePicker() {
		const picker = window.AdaFilePicker;
		if (!picker || typeof picker.open !== "function") return;
		picker.open({
			current: wsSelected.value,
			onSelect: (ws) => {
				if (!ws?.path) return;
				wsSelected.value = ws.path;
				void fetch("/api/workspace/select", {
					method: "POST",
					headers: { "Content-Type": "application/x-www-form-urlencoded" },
					body: new URLSearchParams({ path: ws.path }).toString(),
				}).then(() => loadModels());
			},
		});
	}

	// ---- 草稿（HEAD composer-draft.js 同键名：a_da_draft:<key>）----
	const draftKey = threadId === "" ? "new-task" : `thread:${threadId}`;
	let draftTimer = null;

	function saveDraft(value) {
		const s = storage();
		if (!s) return;
		try {
			if (value === "") s.removeItem(DRAFT_PREFIX + draftKey);
			else s.setItem(DRAFT_PREFIX + draftKey, value);
		} catch { /* 隐私模式/配额满：草稿降级 */ }
	}

	watch(draft, (value) => {
		if (draftTimer) window.clearTimeout(draftTimer);
		draftTimer = window.setTimeout(() => {
			draftTimer = null;
			saveDraft(value);
		}, DRAFT_DELAY);
	});

	// =========================================================================
	// outbox（HEAD app.js 待发送队列移植）
	// =========================================================================

	function readOutbox() {
		const s = storage();
		if (!s) return [];
		try {
			const parsed = JSON.parse(s.getItem(OUTBOX_PREFIX + threadId) ?? "[]");
			if (!Array.isArray(parsed)) return [];
			return parsed.filter((it) => it && typeof it.text === "string" && it.text.trim() !== "").slice(0, OUTBOX_LIMIT);
		} catch {
			return [];
		}
	}
	function writeOutbox() {
		const s = storage();
		if (!s) return;
		try { s.setItem(OUTBOX_PREFIX + threadId, JSON.stringify(outbox.value)); } catch { /* 忽略 */ }
	}
	function enqueueOutbox(text) {
		const value = String(text ?? "").trim();
		if (value === "") return false;
		if (outbox.value.length >= OUTBOX_LIMIT) {
			showStatus(`待发送消息最多 ${OUTBOX_LIMIT} 条，请先处理已有消息。`, "error");
			return false;
		}
		outbox.value = [...outbox.value, { id: Date.now().toString(36) + Math.random().toString(36).slice(2, 8), text: value }];
		writeOutbox();
		return true;
	}
	function removeOutboxItem(id) {
		const next = outbox.value.filter((it) => it.id !== id);
		const removed = next.length !== outbox.value.length;
		outbox.value = next;
		writeOutbox();
		return removed;
	}
	function editOutbox(id) {
		const item = outbox.value.find((it) => it.id === id);
		if (!item || !removeOutboxItem(id)) return;
		draft.value = item.text + (draft.value ? "\n" + draft.value : "");
		saveDraft(draft.value);
	}
	function deleteOutbox(id) {
		if (removeOutboxItem(id)) showStatus("已删除待发送消息", "info");
	}
	async function sendOutbox(id, auto) {
		if (sending.value) return;
		if (run.status === "waiting_input" || run.status === "finishing") {
			if (!auto) showStatus("当前轮次正在等待答复或收尾，请稍后再发送。", "error");
			return;
		}
		const item = outbox.value.find((it) => it.id === id);
		if (!item) return;
		sending.value = true;
		try {
			await fetch(`/threads/${encodeURIComponent(threadId)}/send`, {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body: new URLSearchParams({ text: item.text, idempotencyKey: item.id }).toString(),
			});
			removeOutboxItem(id);
			await Promise.all([loadTranscript({ stick: true }), loadState()]);
		} catch (err) {
			if (auto) void loadTranscript({ stick: true });
			else showStatus(`发送待发送消息失败：${err?.message ?? err}`, "error");
		} finally {
			sending.value = false;
		}
	}

	// =========================================================================
	// 编辑并重发（HEAD 行内表单 → fetch POST /threads/{id}/resend）
	// =========================================================================

	function startEdit(row) {
		editRow = row;
		editText.value = row.editText ?? "";
	}
	function cancelEdit() {
		editRow = null;
		editText.value = "";
	}
	async function resend(row) {
		if (!editRow || editRow !== row) return;
		const text = editText.value.trim();
		if (text === "") return;
		sending.value = true;
		try {
			await fetch(`/threads/${encodeURIComponent(threadId)}/resend`, {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body: new URLSearchParams({ entryId: row.key, idempotencyKey: row.editKey ?? newIdempotencyKey(), text }).toString(),
			});
			cancelEdit();
			await Promise.all([loadTranscript({ stick: true }), loadState()]);
		} catch (err) {
			showStatus(`重新发送失败：${err?.message ?? err}`, "error");
		} finally {
			sending.value = false;
		}
	}

	// =========================================================================
	// 交互托盘（基座外壳；respond 走 handleInteraction 的表单语义）
	// =========================================================================

	function wizardStep(x) {
		return wizardSteps[x.id] ?? 0;
	}
	function wizardPrev(x) {
		if (wizardStep(x) > 0) wizardSteps[x.id] = wizardStep(x) - 1;
	}
	function wizardNext(x) {
		const last = (x.questions?.length ?? 1) - 1;
		if (wizardStep(x) < last) wizardSteps[x.id] = wizardStep(x) + 1;
	}

	async function respond(x, decision) {
		const body = new URLSearchParams({ kind: x.kind, decision });
		if (decision === "accept") {
			if (x.kind === "form") {
				const answers = wizardAnswers[x.id] ?? [];
				answers.forEach((value, i) => body.set("q" + i, String(value ?? "")));
			} else {
				const value = String(trayValues[x.id] ?? "").trim();
				if (value !== "") body.set("value", value);
			}
		}
		try {
			await fetch(`/threads/${encodeURIComponent(threadId)}/interaction/${encodeURIComponent(x.id)}`, {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body: body.toString(),
			});
			await Promise.all([loadState(), loadTranscript({ stick: true })]);
		} catch (err) {
			showStatus(`提交答复失败：${err?.message ?? err}`, "error");
		}
	}

	// thread.tray 插槽数为 0 时渲染基座托盘（HEAD 条件一致）；
	// 注册表是响应式的，插件启停会自动重算。
	const trayInteractions = computed(() => (registry.slots("thread.tray").length > 0 ? [] : interactions.value));

	// 运行状态坞与错误卡（HEAD run-status 文案照抄）。
	const runDockVisible = computed(() => run.active || run.status === "waiting_input");
	const runStatusText = computed(() => {
		if (run.status === "waiting_input") return "等待你的答复";
		if (run.status === "finishing") return "正在收尾";
		return "深度求索中";
	});
	const runErrorTitle = computed(() => {
		if (run.terminal === "error") return "本轮失败";
		if (run.terminal === "interrupted") return "本轮已中断";
		return "本轮未完成";
	});

	// =========================================================================
	// 模型选择器（/partials/model.json + POST /model/select）
	// =========================================================================

	async function loadModels() {
		try {
			const q = threadId === "" ? "" : `?threadId=${encodeURIComponent(threadId)}`;
			const data = await fetch(`/partials/model.json${q}`);
			models.value = (data.models ?? []).map((m) => ({ ...m, thinkingLevels: m.thinkingLevels ?? [] }));
			currentModelLabel.value = data.currentLabel ?? "选择模型";
			currentModelLevel.value = String(data.currentLevel ?? "");
		} catch {
			models.value = [];
		}
	}

	function chooseModel(m, event) {
		if ((m.thinkingLevels ?? []).length > 0) {
			// 有档位的模型：不直接选中，展开右侧推理等级浮层（HEAD showCascade）。
			const anchor = event?.currentTarget ?? null;
			if (cascadeAnchor === anchor && cascadeEl !== null && cascadeEl.hidden === false) {
				hideCascade(true);
				return;
			}
			// event.detail === 0 表示键盘激活（Enter/Space 触发的合成 click）：
			// 这条路径把焦点直接交给浮层第一项，鼠标点开则保持焦点在选项上。
			cascadeFocusFirst = Number(event?.detail ?? 1) === 0;
			showCascade(m, anchor);
			return;
		}
		hideCascade(null);
		void selectModel(m, "");
	}

	// 浮层元素懒创建：只在需要时挂到 body（与 HEAD ensureCascade 同形）。
	function ensureCascade() {
		if (cascadeEl !== null) return cascadeEl;
		const el = document.createElement("div");
		el.className = "ada-model-cascade";
		el.hidden = true;
		el.setAttribute("role", "menu");
		document.body.appendChild(el);
		el.addEventListener("click", (event) => {
			const button = event.target?.closest?.("[data-cascade-level]") ?? null;
			if (button === null || cascadeModel === null) return;
			event.preventDefault();
			const level = button.getAttribute("data-cascade-level") ?? "";
			const model = cascadeModel;
			hideCascade(null);
			void selectModel(model, level);
		});
		el.addEventListener("keydown", (event) => {
			const items = Array.from(el.querySelectorAll("[data-cascade-level]"));
			if (items.length === 0) return;
			if (event.key === "ArrowDown" || event.key === "ArrowUp") {
				event.preventDefault();
				const index = items.indexOf(document.activeElement);
				const step = event.key === "ArrowDown" ? 1 : -1;
				let next = index < 0 ? 0 : index + step;
				if (next < 0) next = items.length - 1;
				if (next >= items.length) next = 0;
				items[next].focus();
				return;
			}
			if (event.key === "ArrowLeft" || event.key === "Escape") {
				event.preventDefault();
				hideCascade(true);
			}
		});
		cascadeEl = el;
		return el;
	}
	// hideCascade(focusAnchor)：focusAnchor=true 时把焦点还给触发选项（键盘路径）。
	function hideCascade(focusAnchor) {
		const anchor = cascadeAnchor;
		const el = cascadeEl;
		cascadeModel = null;
		cascadeAnchor = null;
		cascadeFocusFirst = false;
		if (el !== null) {
			el.hidden = true;
			el.textContent = "";
		}
		if (focusAnchor === true && anchor !== null && anchor.isConnected !== false && typeof anchor.focus === "function") {
			anchor.focus();
		}
	}

	// 浮层定位：默认贴选项右侧（HEAD 同：rect.right + 4）；右侧放不下向左翻，
	// 底边放不下上移，最后夹在视口 8px 内。
	function placeCascade(el, anchor) {
		if (anchor === null || typeof anchor.getBoundingClientRect !== "function") return;
		const rect = anchor.getBoundingClientRect();
		const win = typeof window === "undefined"
			? { innerWidth: 1280, innerHeight: 800 }
			: window;
		const width = el.offsetWidth || 0;
		const height = el.offsetHeight || 0;
		let left = rect.right + 4;
		if (left + width > win.innerWidth - 8) left = rect.left - width - 4;
		if (left < 8) left = 8;
		let top = rect.top;
		if (top + height > win.innerHeight - 8) top = Math.max(8, win.innerHeight - height - 8);
		el.style.setProperty("left", `${left}px`);
		el.style.setProperty("top", `${top}px`);
	}

	function showCascade(model, anchor) {
		const levels = (model.thinkingLevels ?? []).filter((lv) => String(lv ?? "") !== "");
		if (levels.length === 0) {
			hideCascade(null);
			return;
		}
		const el = ensureCascade();
		el.textContent = "";
		cascadeModel = model;
		cascadeAnchor = anchor;
		const title = document.createElement("p");
		title.className = "ada-model-cascade__title";
		title.textContent = "推理等级";
		el.appendChild(title);
		for (const level of levels) {
			const button = document.createElement("button");
			button.type = "button";
			button.className = "ada-select__option ada-model-cascade__option" + (level === model.thinkingLevel ? " is-selected" : "");
			button.setAttribute("role", "menuitem");
			button.setAttribute("data-cascade-level", level);
			button.textContent = level;
			el.appendChild(button);
		}
		el.hidden = false;
		placeCascade(el, anchor);
		// 键盘打开时（Enter/ArrowRight 路径）焦点直接落到第一项；
		// 鼠标点开时保持焦点在选项上，避免出现不该有的焦点圈。
		if (cascadeFocusFirst === true && typeof el.querySelector === "function") {
			const first = el.querySelector("[data-cascade-level]");
			if (first && typeof first.focus === "function") first.focus();
		}
		cascadeFocusFirst = false;
	}

	// 浮层打开期间的全局关闭条件（滚动/外部点击/Esc/视口变化）。
	// 监听挂在 document 上、捕获阶段：滚动事件不冒泡，只有 capture 收得到。
	function onCascadeScroll() {
		if (cascadeEl !== null && cascadeEl.hidden === false) hideCascade(null);
	}
	function onCascadeClick(event) {
		if (cascadeEl === null || cascadeEl.hidden !== false) return;
		const target = event?.target ?? null;
		const inCascade = typeof target?.closest === "function" && target.closest(".ada-model-cascade") !== null;
		const onPicker = typeof target?.closest === "function" && target.closest(".ada-model-picker") !== null;
		if (inCascade || onPicker) return;
		hideCascade(null);
	}
	function onCascadeKeydown(event) {
		if (cascadeEl !== null && cascadeEl.hidden === false && event.key === "Escape") {
			event.preventDefault();
			hideCascade(true);
		}
	}
	function onCascadeResize() {
		if (cascadeEl !== null && cascadeEl.hidden === false) hideCascade(null);
	}

	// 模型菜单收起时子菜单一并收起（HEAD 用 MutationObserver 盯 [data-select-menu]
	// 的 hidden；这里菜单开关是 reactive 状态，watch 更直接）。
	watch(modelOpen, (open) => {
		if (!open) hideCascade(null);
	});

	async function selectModel(m, level) {
		modelOpen.value = false;
		const body = new URLSearchParams({ providerId: m.providerId, modelId: m.modelId });
		if (level) body.set("thinkingLevel", level);
		if (threadId !== "") body.set("threadId", threadId);
		try {
			await fetch("/model/select", {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body: body.toString(),
			});
			const next = models.value.map((x) => {
				const current = x.value === m.value;
				return { ...x, current, thinkingLevel: current && level ? level : current ? x.thinkingLevel : "" };
			});
			models.value = next;
			currentModelLabel.value = m.shortLabel;
			currentModelLevel.value = String(level ?? "");
			showStatus("模型已切换", "info");
		} catch (err) {
			showStatus(`切换模型失败：${err?.message ?? err}`, "error");
		}
	}

	// =========================================================================
	// 顶部动作（HEAD threadTopbarActions：刷新 + 运行中停止）
	// =========================================================================

	function syncTopbar() {
		const actions = [{ key: "refresh", kind: "icon", icon: "i-refresh", label: "刷新", title: "刷新", onClick: () => refreshAll() }];
		if (run.active) {
			actions.push({ key: "stop", kind: "danger", label: "停止", icon: "i-stop", title: "停止", onClick: () => stopRun() });
		}
		shellState.topbar.set(actions);
	}

	async function refreshAll() {
		if (threadId === "") {
			await loadModels();
			return;
		}
		await Promise.all([loadTranscript({ stick: true }), loadState(), loadModels()]);
	}

	async function stopRun() {
		const ok = await confirm("确定中止当前轮次吗？");
		if (!ok) return;
		try {
			await fetch(`/threads/${encodeURIComponent(threadId)}/abort`, { method: "POST" });
			await Promise.all([loadTranscript({ stick: true }), loadState()]);
		} catch (err) {
			showStatus(`停止失败：${err?.message ?? err}`, "error");
		}
	}

	function goNewTask() {
		router.navigate("/threads");
	}

	// =========================================================================
	// 行渲染辅助
	// =========================================================================

	function toolPhase(tool) {
		if (tool?.status === "error") return "danger";
		if (tool?.status === "denied") return "warn";
		return "";
	}
	function noticeTone(level) {
		if (level === "error") return "danger";
		if (level === "warn") return "warn";
		return "brand";
	}
	function lineCount(text) {
		return String(text ?? "").split("\n").length;
	}
	async function copyText(text) {
		const value = String(text ?? "");
		if (value === "") return;
		try { await navigator.clipboard.writeText(value); } catch { /* 剪贴板不可用：静默 */ }
	}

	// =========================================================================
	// 生命周期
	// =========================================================================

	let elapsedTimer = null;
	const elapsedText = ref("— 秒");

	function tickElapsed() {
		if (run.startedAt === "" || !run.active) {
			elapsedText.value = "— 秒";
			return;
		}
		const started = Date.parse(run.startedAt);
		if (Number.isNaN(started)) {
			elapsedText.value = "— 秒";
			return;
		}
		elapsedText.value = `${Math.max(0, Math.floor((Date.now() - started) / 1000))} 秒`;
	}

	watch(() => run.active, () => syncTopbar());

	onMounted(() => {
		// 草稿还原（HEAD composer-draft：服务端预填时以页面为准）。
		const s = storage();
		if (s) {
			try {
				const saved = s.getItem(DRAFT_PREFIX + draftKey) ?? "";
				if (saved !== "" && draft.value === "") draft.value = saved;
			} catch { /* 忽略 */ }
		}
		// 隐藏的图片选择 input：白名单没有 input[type=file]，命令行创建
		//（label[for] 原生触发，脚本缺席时点不了但发送仍可用）。
		const card = threadId === ""
			? document.querySelector(".ada-newtask__card")
			: document.querySelector(".ada-composer__card");
		if (card !== null) {
			fileInput = document.createElement("input");
			fileInput.type = "file";
			fileInput.accept = "image/png,image/jpeg,image/webp,image/gif";
			fileInput.multiple = true;
			fileInput.className = "ada-composer__image-input";
			fileInput.id = imageInputID;
			fileInput.setAttribute("aria-hidden", "true");
			fileInput.tabIndex = -1;
			fileInput.addEventListener("change", () => addFiles(fileInput?.files));
			card.appendChild(fileInput);
		}
		filePickerReady.value = typeof window.AdaFilePicker?.open === "function";

		if (threadId === "") {
			void loadModels();
		} else {
			outbox.value = readOutbox();
			syncTopbar();
			void loadTranscript({ stick: true }).then(loadState);
			void loadModels();
			void loadThreadTitle();
			connect();
			const el = scroller();
			if (el) {
				el.addEventListener("wheel", noteUserScroll, { passive: true });
				el.addEventListener("touchmove", noteUserScroll, { passive: true });
				el.addEventListener("pointerdown", noteUserScroll, { passive: true });
			}
			scrollToBottom();
		}
		elapsedTimer = window.setInterval(tickElapsed, 1000);
		tickElapsed();
		// 推理等级浮层的全局关闭条件（捕获阶段：滚动事件不冒泡）。
		document.addEventListener("click", onCascadeClick, true);
		document.addEventListener("keydown", onCascadeKeydown, true);
		document.addEventListener("scroll", onCascadeScroll, true);
		window.addEventListener("resize", onCascadeResize);
	});

	// 顶栏标题：会话标题（threads.json 里找；找不到退回「会话」）。
	async function loadThreadTitle() {
		try {
			const data = await fetch("/partials/threads.json?limit=200");
			const found = (data.threads ?? []).find((t) => t.id === threadId);
			if (found?.title) shellState.title = found.title;
		} catch { /* 标题缺失只影响顶栏文案 */ }
	}

	onUnmounted(() => {
		try { es?.close(); } catch { /* 忽略 */ }
		if (reconnectTimer) window.clearTimeout(reconnectTimer);
		if (refreshTimer) window.clearTimeout(refreshTimer);
		if (mdTimer) window.clearTimeout(mdTimer);
		if (mdRetryTimer) window.clearTimeout(mdRetryTimer);
		if (draftTimer) window.clearTimeout(draftTimer);
		if (elapsedTimer) window.clearInterval(elapsedTimer);
		// 推理等级浮层：摘掉 body 级节点与全局监听（它不属于本组件子树，
		// 组件卸载不会自动回收）。
		document.removeEventListener("click", onCascadeClick, true);
		document.removeEventListener("keydown", onCascadeKeydown, true);
		document.removeEventListener("scroll", onCascadeScroll, true);
		window.removeEventListener("resize", onCascadeResize);
		if (cascadeEl !== null && cascadeEl.parentNode !== null) cascadeEl.parentNode.removeChild(cascadeEl);
		cascadeEl = null;
		cascadeModel = null;
		cascadeAnchor = null;
		// 卸载前同步落盘草稿（HEAD pagehide 语义）。
		saveDraft(draft.value);
		shellState.topbar.clear();
	});

	return {
		threadId, productName, brandName, logoStyle, greeting,
		rows, hasOlder, loadFailed, run, statusText, statusLevel, statusRetry, live, liveActive,
		draft, thumbs, dragging, sending, workspaces, wsSelected, wsLabel, workspacePath,
		imageInputID, filePickerReady, outbox,
		models, currentModelLabel, modelChipLabel, modelOpen, chooseModel,
		editText,
		trayInteractions, trayValues, wizardAnswers, wizardStep, wizardPrev, wizardNext, respond,
		send, sendNewTask, noop, onPaste, onDragEnter, onDragLeave, onDrop,
		addFiles, removeImage, imageStyle, imageHas, openImage, openWorkspacePicker, goNewTask,
		editOutbox, deleteOutbox, sendOutbox,
		cancelEdit, resend, startEdit,
		loadOlder, refreshAll, stopRun, reconnect,
		onScroll, showScrollBottom, jumpToBottom,
		runStatusText, runDockVisible, runErrorTitle, elapsedText,
		toolPhase, noticeTone, lineCount, copyText, toolIconName, toolTone,
	};
};
