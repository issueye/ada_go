// mcp-client 前端段（servers 页面）逻辑。
//
// 数据面仍是本插件的 route（经 ada.api 桥）：
//   GET  /servers        状态列表 + 配置路径
//   GET  /config         配置原文（只在打开弹窗时读取）
//   POST /config         保存配置（text 字段）
//   POST /servers/toggle 启停 server
// 「重载插件 / 重新探测」走基座端点 POST /agent-plugins/reload（CSRF 双提交）。
// 配置原文只在用户点开弹窗时读取；地址与命令行在服务端已脱敏。
//
// 与旧 page.tsx 的差异：确认/提示不变；编辑器高亮层的换行由插值节点输出
// （模板静态文本会被折叠，插值不会）。
import { ref, reactive, computed, inject, onMounted } from "/static/ada/ada-runtime.mjs";

const CONFIG_PATH = ".adago/mcp.json";
const USER_CONFIG_PATH = "~/.adago/mcp.json";
const MAX_HIGHLIGHT = 64 * 1024;

function statusLabel(status) {
	if (status === "ready") return "已连接";
	if (status === "disabled") return "已停用";
	return "连接失败";
}

function accessLabel(access) {
	return access === "read" ? "只读" : "写入";
}

function errorText(err) {
	const message = err && err.message ? String(err.message) : String(err);
	const cut = message.indexOf("失败: ");
	return (cut >= 0 ? message.slice(cut + "失败: ".length) : message) || "操作失败";
}

function clockText() {
	const d = new Date();
	const pad = (n) => (n < 10 ? "0" + String(n) : String(n));
	return pad(d.getHours()) + ":" + pad(d.getMinutes());
}

// describeJSONError 把 JSON.parse 的错误换成中文行/列，方便定位。
// V8 有两种文案：`... at position 15 (line 1 column 16)` 与
// `Unexpected token '}', ..."servers":[}" is not valid JSON`（带代码帧、无位置），
// 后者从代码帧反查出错字符所在位置。
function describeJSONError(err, text) {
	const message = err && err.message ? String(err.message) : String(err);
	let pos = -1;

	const marker = "position ";
	const at = message.indexOf(marker);
	if (at >= 0) {
		let i = at + marker.length;
		let digits = "";
		while (i < message.length && message.charAt(i) >= "0" && message.charAt(i) <= "9") {
			digits += message.charAt(i);
			i++;
		}
		if (digits) pos = Number(digits);
	}
	if (pos < 0) {
		// 代码帧形如 ..."servers":[}" ，末尾那个字符就是 V8 报的 token。
		const frame = message.indexOf('..."');
		if (frame >= 0) {
			const rest = message.slice(frame + 4);
			const end = rest.indexOf('"');
			if (end > 0) {
				const snippet = '"' + rest.slice(0, end);
				const found = text.lastIndexOf(snippet);
				if (found >= 0) pos = found + snippet.length - 1;
			}
		}
	}
	if (pos < 0) return message;
	const before = text.slice(0, pos);
	const line = before.split("\n").length;
	const col = pos - before.lastIndexOf("\n");
	return "第 " + line + " 行第 " + col + " 列 · " + message;
}

// parseConfig 同时做语法与结构校验，返回编辑器状态条所需的一切。
function parseConfig(text) {
	const lines = text ? text.split("\n").length : 0;
	if (!text || !text.trim()) {
		return { ok: false, message: "配置为空", servers: 0, lines: lines };
	}
	try {
		const value = JSON.parse(text);
		if (!value || typeof value !== "object" || !Array.isArray(value.servers)) {
			return { ok: false, message: '需要 {"version":1,"servers":[…]} 结构', servers: 0, lines: lines };
		}
		return { ok: true, message: "JSON 有效", servers: value.servers.length, lines: lines };
	} catch (err) {
		return { ok: false, message: describeJSONError(err, text), servers: 0, lines: lines };
	}
}

function prettyJSON(text) {
	const parsed = parseConfig(text);
	if (!parsed.ok) return { ok: false, text: text, message: parsed.message };
	try {
		return { ok: true, text: JSON.stringify(JSON.parse(text), null, 2) + "\n", message: "" };
	} catch (err) {
		return { ok: false, text: text, message: describeJSONError(err, text) };
	}
}

// highlightTokens 是给「透明 textarea + 背后高亮层」用的分词器。
// 手写扫描输出纯数据（由模板生成 span），永远不会把配置内容当 HTML 注入。
function highlightTokens(text) {
	const out = [];
	if (text.length > MAX_HIGHLIGHT) return [{ text: text, cls: "" }];
	let plain = "";
	const flush = () => {
		if (plain) {
			out.push({ text: plain, cls: "" });
			plain = "";
		}
	};
	const isDigit = (c) => c >= "0" && c <= "9";
	const isWord = (c) => isDigit(c) || (c >= "a" && c <= "z") || (c >= "A" && c <= "Z") || c === "_";
	const matchAt = (word, at) => {
		for (let k = 0; k < word.length; k++) {
			if (text.charAt(at + k) !== word.charAt(k)) return false;
		}
		return true;
	};
	let i = 0;
	while (i < text.length) {
		const ch = text.charAt(i);
		if (ch === '"') {
			let j = i + 1;
			while (j < text.length) {
				const c = text.charAt(j);
				if (c === "\\") {
					j += 2;
					continue;
				}
				if (c === '"') {
					j++;
					break;
				}
				j++;
			}
			let k = j;
			while (k < text.length && (text.charAt(k) === " " || text.charAt(k) === "\t" || text.charAt(k) === "\n" || text.charAt(k) === "\r")) k++;
			flush();
			out.push({ text: text.slice(i, j), cls: text.charAt(k) === ":" ? "mc-tok-key" : "mc-tok-str" });
			i = j;
			continue;
		}
		if (isDigit(ch) || (ch === "-" && isDigit(text.charAt(i + 1)))) {
			let j = i + (ch === "-" ? 1 : 0);
			while (j < text.length) {
				const c = text.charAt(j);
				if (isDigit(c) || c === "." || c === "e" || c === "E" || c === "+" || c === "-") j++;
				else break;
			}
			const token = text.slice(i, j);
			if (token.length && !isWord(text.charAt(j))) {
				flush();
				out.push({ text: token, cls: "mc-tok-num" });
				i = j;
				continue;
			}
		}
		let literal = "";
		if (ch === "t" && matchAt("true", i)) literal = "true";
		else if (ch === "f" && matchAt("false", i)) literal = "false";
		else if (ch === "n" && matchAt("null", i)) literal = "null";
		if (literal && !isWord(text.charAt(i + literal.length))) {
			flush();
			out.push({ text: literal, cls: "mc-tok-lit" });
			i += literal.length;
			continue;
		}
		plain += ch;
		i++;
	}
	flush();
	return out;
}

export const setup = () => {
	const ada = inject("ada");
	const bootstrap = inject("bootstrap");
	const csrf = bootstrap && bootstrap.csrf ? String(bootstrap.csrf) : "";

	const state = ref(null);
	const loading = ref(true);
	const error = ref("");
	const toast = ref(null);
	let toastSeq = 0;
	let toastTimer = 0;
	const filter = ref("all");
	const lastRefresh = ref("");
	const reloading = ref(false);
	const expanded = reactive({});
	const modalOpen = ref(false);
	const configText = ref("");
	const configLoaded = ref(false);
	const saving = ref(false);

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
			const data = await ada.api("/servers");
			state.value = data || null;
			loading.value = false;
			lastRefresh.value = clockText();
		} catch (err) {
			loading.value = false;
			error.value = errorText(err);
			showToast("读取 server 状态失败：" + error.value, "error");
		}
	}

	function refresh() {
		load();
		showToast("已刷新 server 状态。");
	}

	const servers = computed(() => (state.value && Array.isArray(state.value.servers) ? state.value.servers : []));
	const counts = computed(() => {
		const all = servers.value;
		return {
			all: all.length,
			ready: all.filter((s) => s.status === "ready").length,
			degraded: all.filter((s) => s.status === "degraded").length,
			disabled: all.filter((s) => s.status === "disabled").length,
		};
	});
	const filters = computed(() => {
		const out = [
			{ value: "all", label: "全部", count: counts.value.all },
			{ value: "ready", label: "已连接", count: counts.value.ready },
		];
		// 有异常才出现这一档，避免常态下多一个永远为 0 的分段。
		if (counts.value.degraded) out.push({ value: "degraded", label: "异常", count: counts.value.degraded });
		out.push({ value: "disabled", label: "已停用", count: counts.value.disabled });
		return out;
	});
	const filteredServers = computed(() => {
		if (filter.value === "all") return servers.value;
		return servers.value.filter((s) => s.status === filter.value);
	});
	const configPath = computed(() => (state.value && state.value.configPath) || CONFIG_PATH);
	const userConfigPath = computed(() => (state.value && state.value.userConfigPath) || USER_CONFIG_PATH);
	const validation = computed(() => parseConfig(configText.value));
	const tokens = computed(() => highlightTokens(configText.value));

	function toolCount(server) {
		return Array.isArray(server.items) ? server.items.length : 0;
	}

	function toggleTools(server) {
		if (expanded[server.id]) delete expanded[server.id];
		else expanded[server.id] = true;
	}

	function isExpanded(server) {
		return expanded[server.id] === true;
	}

	function visibleTools(server) {
		const items = Array.isArray(server.items) ? server.items : [];
		return isExpanded(server) ? items : items.slice(0, 4);
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

	function toggleServer(server, wanted) {
		if (reloading.value) return;
		ada.api("/servers/toggle", { method: "POST", body: { id: server.id, enabled: wanted } })
			.then(() => {
				showToast("已保存：" + server.id + (wanted ? " 已启用" : " 已停用") + "，重载插件后生效。");
				load();
			})
			.catch((err) => {
				showToast("启停失败：" + errorText(err), "error");
				load();
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
				showToast("插件已重载，正在刷新状态…");
				load();
			})
			.catch((err) => {
				reloading.value = false;
				showToast("重载失败：" + errorText(err), "error");
			});
	}

	// probe 就是重新装配：会重新 initialize + tools/list，等于对降级 server 重探测。
	function probe() {
		showToast("正在重新探测…");
		reloadPlugins();
	}

	function openModal() {
		modalOpen.value = true;
		if (!configLoaded.value) loadConfig();
	}

	function closeModal() {
		modalOpen.value = false;
	}

	function loadConfig() {
		ada.api("/config")
			.then((config) => {
				const text = (config && config.text) || "";
				const pretty = text.trim() ? prettyJSON(text) : { ok: true, text: text };
				configText.value = pretty.ok ? pretty.text : text;
				configLoaded.value = true;
			})
			.catch((err) => {
				showToast("读取配置失败：" + errorText(err), "error");
			});
	}

	// 高亮层与 textarea 的滚动位置必须一致，否则长配置会错行。
	function syncScroll(event) {
		const input = event && event.target ? event.target : null;
		if (!input || !input.parentNode) return;
		const pre = input.parentNode.querySelector("[data-mcp-hl]");
		if (!pre) return;
		pre.scrollTop = input.scrollTop;
		pre.scrollLeft = input.scrollLeft;
	}

	function format() {
		const pretty = prettyJSON(configText.value);
		if (!pretty.ok) {
			showToast("格式化失败：" + pretty.message, "error");
			return;
		}
		if (pretty.text === configText.value) {
			showToast("已经是格式化后的 JSON。");
			return;
		}
		configText.value = pretty.text;
		showToast("已格式化（保存后写入文件）。");
	}

	function save() {
		if (saving.value || !validation.value.ok) return;
		saving.value = true;
		ada.api("/config", { method: "POST", body: { text: configText.value } })
			.then((result) => {
				saving.value = false;
				const warnings = result && Array.isArray(result.warnings) ? result.warnings : [];
				let message = "已保存（" + ((result && result.servers) || 0) + " 个 server），点「重载插件」后生效。";
				if (warnings.length) message += " 警告：" + warnings.join("；");
				showToast(message, warnings.length ? "error" : "info");
				load();
			})
			.catch((err) => {
				saving.value = false;
				showToast("保存失败：" + errorText(err), "error");
			});
	}

	onMounted(load);

	return {
		state, loading, error, toast, filter, lastRefresh, reloading,
		modalOpen, configText, saving,
		counts, filters, filteredServers, configPath, userConfigPath, validation, tokens,
		load, refresh, statusLabel, accessLabel, toolCount, toggleTools, isExpanded, visibleTools,
		copyText, toggleServer, reloadPlugins, probe, openModal, closeModal, loadConfig,
		syncScroll, format, save,
	};
};
