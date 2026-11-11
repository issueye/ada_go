// provider-admin 前端段（providers 页面）逻辑：供应商/模型的增删改查、模型拉取
// 与当前模型切换。数据与操作全部经本插件 route（经 ada.api 桥，CSRF 由宿主封装）：
//   GET  /state        供应商目录 + 当前模型（API Key 不经过浏览器）
//   POST /save         整条替换供应商（apiKey 留空表示保留原值）
//   POST /remove       删除供应商
//   POST /fetch-models 从端点拉取模型清单
//   POST /select       切换当前模型
// 结构与旧 providers-next.tsx 一致；差异仅两处：ada:ui 的 Button/Badge/Input/Empty
// 组件改写为等价的原生标签 + ada-btn/ada-badge 类名；确认框改走 ada.confirm()。
import { ref, reactive, computed, inject, onMounted } from "/static/ada/ada-runtime.mjs";

// 协议取值必须与各适配器 ProviderDef.protocol 完全一致：
// provider-openai-chat → openai_chat、provider-openai-responses → openai_responses、
// provider-anthropic-messages → anthropic。写错不会报错，只会存下一个没有任何适配器认领的值。
const PROTOCOL_OPTIONS = [
	{ value: "openai_chat", label: "OpenAI Chat" },
	{ value: "openai_responses", label: "OpenAI Responses" },
	{ value: "anthropic", label: "Anthropic" },
];

const INPUT_MODES = [
	{ key: "text", label: "文本" },
	{ key: "image", label: "图片" },
	{ key: "audio", label: "音频" },
	{ key: "video", label: "视频" },
];

function protocolLabel(protocol) {
	for (const item of PROTOCOL_OPTIONS) {
		if (item.value === protocol) return item.label;
	}
	return protocol || "未设置";
}

function formatContext(value) {
	const size = Number(value);
	if (!value || !isFinite(size) || size <= 0) return "—";
	if (size >= 1000000) return Math.round(size / 100000) / 10 + "M";
	if (size >= 1000) return Math.round(size / 100) / 10 + "k";
	return String(size);
}

function headerCount(map) {
	if (!map || typeof map !== "object") return 0;
	return Object.keys(map).length;
}

function parseHeaders(text) {
	const out = {};
	String(text || "").split(/\r?\n/).forEach((line) => {
		const index = line.indexOf(":");
		if (index <= 0) return;
		const key = line.slice(0, index).trim();
		const value = line.slice(index + 1).trim();
		if (key) out[key] = value;
	});
	return out;
}

export const setup = () => {
	const ada = inject("ada");

	const loading = ref(true);
	const error = ref("");
	const toast = ref(null);
	let toastSeq = 0;
	let toastTimer = 0;
	const providers = ref([]);
	const current = ref({ providerId: "", modelId: "" });
	const selectedId = ref("");
	const tab = ref("info");
	const search = ref("");
	const filter = ref("all");

	// 供应商表单弹窗（新增 / 编辑同一个弹窗）
	const providerModalOpen = ref(false);
	const providerModalMode = ref("add");
	const providerForm = reactive({ id: "", name: "", protocol: "", baseUrl: "", proxy: "", headersText: "", apiKey: "" });

	// 拉取模型弹窗
	const fetchOpen = ref(false);
	const fetching = ref(false);
	const fetchedModels = ref([]);
	const selectedFetch = reactive({});
	const fetchError = ref("");
	const fetchQuery = ref("");

	// 模型弹窗
	const modelModalOpen = ref(false);
	const modelModalMode = ref("add");
	const editingModelId = ref("");
	const levelDraft = ref("");
	const levelInputOpen = ref(false);
	const modelForm = reactive({
		id: "",
		displayName: "",
		contextWindow: "",
		maxOutput: "",
		supportsTools: false,
		disabled: false,
		thinkingLevels: [],
		inputModes: [],
	});

	// 轻提示：底部居中浮层，自动消失。tone=error 时用危险色、停留更久。
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
			providers.value = data && Array.isArray(data.providers) ? data.providers : [];
			current.value = (data && data.current) || { providerId: "", modelId: "" };
			let exists = false;
			for (const p of providers.value) {
				if (p.id === selectedId.value) exists = true;
			}
			if (!exists) selectedId.value = providers.value.length > 0 ? providers.value[0].id : "";
			loading.value = false;
		} catch (err) {
			loading.value = false;
			error.value = (err && err.message) || String(err);
		}
	}

	const selected = computed(() => {
		for (const p of providers.value) {
			if (p.id === selectedId.value) return p;
		}
		return providers.value[0] || null;
	});
	const enabledCount = computed(() => {
		let count = 0;
		providers.value.forEach((p) => {
			if (!p.disabled) count += 1;
		});
		return count;
	});
	const filtered = computed(() => {
		const query = String(search.value || "").trim().toLowerCase();
		const mode = filter.value;
		return providers.value.filter((p) => {
			if (mode === "enabled" && p.disabled) return false;
			if (mode === "disabled" && !p.disabled) return false;
			if (!query) return true;
			const hay = [p.displayName || "", p.id || "", p.protocol || "", p.baseUrl || ""].join(" ").toLowerCase();
			return hay.indexOf(query) >= 0;
		});
	});
	const selectedIndex = computed(() => {
		for (let i = 0; i < providers.value.length; i += 1) {
			if (selected.value && providers.value[i].id === selected.value.id) return i;
		}
		return 0;
	});
	const selectedModels = computed(() => {
		const provider = selected.value;
		return provider && Array.isArray(provider.models) ? provider.models : [];
	});
	const modelStats = computed(() => {
		let enabled = 0;
		let tools = 0;
		let thinking = 0;
		selectedModels.value.forEach((m) => {
			if (!m) return;
			if (!m.disabled) enabled += 1;
			if (m.supportsTools) tools += 1;
			if (Array.isArray(m.thinkingLevels) && m.thinkingLevels.length) thinking += 1;
		});
		return { total: selectedModels.value.length, enabled, tools, thinking };
	});
	const currentModelId = computed(() => {
		const provider = selected.value;
		if (!provider) return "";
		if (current.value.providerId !== provider.id) return "";
		return current.value.modelId || "";
	});
	const existingModelIds = computed(() => {
		const map = {};
		selectedModels.value.forEach((m) => {
			if (m && m.id) map[m.id] = true;
		});
		return map;
	});
	const fetchedVisible = computed(() => {
		const query = String(fetchQuery.value || "").trim().toLowerCase();
		if (!query) return fetchedModels.value;
		return fetchedModels.value.filter((m) => {
			const hay = [m && m.id, m && m.displayName].join(" ").toLowerCase();
			return hay.indexOf(query) >= 0;
		});
	});
	const selectedFetchCount = computed(() => {
		let count = 0;
		Object.keys(selectedFetch).forEach((key) => {
			if (selectedFetch[key]) count += 1;
		});
		return count;
	});
	const allFetchSelected = computed(() => {
		let selectable = 0;
		let pickedCount = 0;
		fetchedVisible.value.forEach((m) => {
			if (!m || !m.id || existingModelIds.value[m.id]) return;
			selectable += 1;
			if (selectedFetch[m.id]) pickedCount += 1;
		});
		return { selectable, picked: pickedCount, all: selectable > 0 && pickedCount === selectable };
	});
	const protocolOptions = computed(() => {
		const list = PROTOCOL_OPTIONS.slice();
		const value = String(providerForm.protocol || "");
		if (value) {
			let found = false;
			list.forEach((item) => {
				if (item.value === value) found = true;
			});
			if (!found) list.push({ value, label: value });
		}
		return list;
	});

	function select(id) {
		selectedId.value = id;
		tab.value = "info";
	}

	function copyText(text, label) {
		const value = String(text || "");
		if (!value) return;
		if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
			navigator.clipboard.writeText(value)
				.then(() => showToast("已复制" + label + "。"))
				.catch(() => showToast("复制失败，请手动选择复制。", "error"));
			return;
		}
		showToast("当前环境不支持一键复制。", "error");
	}

	function avatarTone(index) {
		return " is-tone-" + (index % 3);
	}
	function initialOf(provider) {
		const name = String((provider && (provider.displayName || provider.id)) || "?").trim();
		return name.charAt(0).toUpperCase() || "?";
	}
	function modelCount(provider) {
		return Array.isArray(provider.models) ? provider.models.length : 0;
	}

	async function selectModel(modelId) {
		const s = selected.value;
		if (!s) return;
		try {
			await ada.api("/select", { method: "POST", body: { providerId: s.id, modelId } });
			current.value = { providerId: s.id, modelId };
			showToast("已切换当前模型。");
		} catch (err) {
			showToast("切换失败：" + ((err && err.message) || String(err)), "error");
		}
	}

	async function toggleProvider() {
		const s = selected.value;
		if (!s) return;
		const nextDisabled = !s.disabled;
		// /save 是整条替换：未编辑字段原样带回，apiKey 留空表示保留原值。
		try {
			await ada.api("/save", {
				method: "POST",
				body: {
					id: s.id,
					name: s.displayName || s.id,
					protocol: s.protocol || "",
					baseUrl: s.baseUrl || "",
					proxy: s.proxy || "",
					headers: s.headers || {},
					disabled: nextDisabled,
					models: s.models || [],
				},
			});
			showToast(nextDisabled ? "供应商已停用。" : "供应商已启用。");
			load();
		} catch (err) {
			showToast("更新失败：" + ((err && err.message) || String(err)), "error");
		}
	}

	function removeProvider() {
		const s = selected.value;
		if (!s) return;
		ada.confirm("删除供应商「" + (s.displayName || s.id) + "」？其模型配置会一起删除。").then((ok) => {
			if (!ok) return;
			ada.api("/remove", { method: "POST", body: { id: s.id } })
				.then(() => {
					showToast("供应商已删除。");
					selectedId.value = "";
					load();
				})
				.catch((err) => {
					showToast("删除失败：" + ((err && err.message) || String(err)), "error");
				});
		});
	}

	// ── 供应商表单弹窗 ────────────────────────────────────────────────
	function openAddProvider() {
		providerModalMode.value = "add";
		providerForm.id = "";
		providerForm.name = "";
		providerForm.protocol = PROTOCOL_OPTIONS[0].value;
		providerForm.baseUrl = "";
		providerForm.proxy = "";
		providerForm.headersText = "";
		providerForm.apiKey = "";
		providerModalOpen.value = true;
	}

	function openEditProvider() {
		const s = selected.value;
		if (!s) return;
		const headers = s.headers || {};
		const lines = [];
		Object.keys(headers).forEach((key) => {
			lines.push(key + ": " + headers[key]);
		});
		providerModalMode.value = "edit";
		providerForm.id = s.id;
		providerForm.name = s.displayName || s.id;
		providerForm.protocol = s.protocol || PROTOCOL_OPTIONS[0].value;
		providerForm.baseUrl = s.baseUrl || "";
		providerForm.proxy = s.proxy || "";
		providerForm.headersText = lines.join("\n");
		providerForm.apiKey = "";
		providerModalOpen.value = true;
	}

	function closeProviderModal() {
		providerModalOpen.value = false;
	}

	function saveProviderForm() {
		const editing = providerModalMode.value === "edit";
		const s = selected.value;
		const name = String(providerForm.name || "").trim();
		if (!name) {
			showToast("供应商名称不能为空。", "error");
			return;
		}
		const body = {
			id: editing && s ? s.id : providerForm.id,
			name,
			protocol: String(providerForm.protocol || "").trim(),
			baseUrl: String(providerForm.baseUrl || "").trim(),
			proxy: String(providerForm.proxy || "").trim(),
			headers: parseHeaders(providerForm.headersText),
			disabled: editing && s ? s.disabled === true : false,
			models: editing && s ? s.models || [] : [],
		};
		const key = String(providerForm.apiKey || "").trim();
		if (key) body.apiKey = key;
		ada.api("/save", { method: "POST", body })
			.then(() => {
				showToast(editing ? "供应商已保存。" : "供应商已新增。");
				providerModalOpen.value = false;
				load();
			})
			.catch((err) => {
				showToast((editing ? "保存失败：" : "新增失败：") + ((err && err.message) || String(err)), "error");
			});
	}

	// ── 拉取模型弹窗 ──────────────────────────────────────────────────
	function openFetchModels() {
		const s = selected.value;
		if (!s) return;
		fetchOpen.value = true;
		fetching.value = true;
		fetchError.value = "";
		fetchQuery.value = "";
		fetchedModels.value = [];
		Object.keys(selectedFetch).forEach((k) => delete selectedFetch[k]);
		ada.api("/fetch-models", { method: "POST", body: { id: s.id } })
			.then((models) => {
				fetching.value = false;
				fetchedModels.value = Array.isArray(models) ? models : [];
			})
			.catch((err) => {
				fetching.value = false;
				fetchError.value = "拉取失败：" + ((err && err.message) || String(err));
			});
	}

	function closeFetch() {
		fetchOpen.value = false;
		fetchedModels.value = [];
		Object.keys(selectedFetch).forEach((k) => delete selectedFetch[k]);
		fetchError.value = "";
		fetchQuery.value = "";
	}

	function toggleFetchModel(modelId, checked) {
		selectedFetch[modelId] = checked === true;
	}

	function toggleSelectAll(checked) {
		fetchedVisible.value.forEach((m) => {
			if (!m || !m.id || existingModelIds.value[m.id]) return;
			selectedFetch[m.id] = checked === true;
		});
	}

	// 只加选中且尚未添加的模型，保留已有模型配置不动。
	function addSelectedModels() {
		const s = selected.value;
		if (!s) return;
		const added = fetchedModels.value.filter(
			(m) => m && m.id && !existingModelIds.value[m.id] && selectedFetch[m.id],
		);
		if (!added.length) {
			showToast("没有勾选可添加的模型。", "error");
			return;
		}
		closeFetch();
		persistModels((s.models || []).concat(added), "已添加 " + added.length + " 个模型。");
	}

	// ── 模型弹窗 ──────────────────────────────────────────────────────
	function openEditModel(row) {
		if (!row) return;
		modelModalMode.value = "edit";
		editingModelId.value = row.id;
		levelInputOpen.value = false;
		levelDraft.value = "";
		modelForm.id = row.id;
		modelForm.displayName = row.displayName || "";
		modelForm.contextWindow = row.contextWindow === undefined || row.contextWindow === null ? "" : String(row.contextWindow);
		modelForm.maxOutput = row.maxOutput === undefined || row.maxOutput === null ? "" : String(row.maxOutput);
		modelForm.supportsTools = row.supportsTools === true;
		modelForm.disabled = row.disabled === true;
		modelForm.thinkingLevels = Array.isArray(row.thinkingLevels) ? row.thinkingLevels.slice() : [];
		modelForm.inputModes = Array.isArray(row.input) ? row.input.slice() : [];
		modelModalOpen.value = true;
	}

	function openAddModel() {
		modelModalMode.value = "add";
		editingModelId.value = "";
		levelInputOpen.value = false;
		levelDraft.value = "";
		modelForm.id = "";
		modelForm.displayName = "";
		modelForm.contextWindow = "";
		modelForm.maxOutput = "";
		modelForm.supportsTools = false;
		modelForm.disabled = false;
		modelForm.thinkingLevels = [];
		modelForm.inputModes = ["text"];
		modelModalOpen.value = true;
	}

	function closeModelModal() {
		modelModalOpen.value = false;
		editingModelId.value = "";
		levelInputOpen.value = false;
		levelDraft.value = "";
	}

	function openLevelInput() {
		levelInputOpen.value = true;
		levelDraft.value = "";
	}

	function addLevel(preset) {
		const value = String(preset === undefined ? levelDraft.value : preset).trim();
		if (value && modelForm.thinkingLevels.indexOf(value) < 0) {
			modelForm.thinkingLevels = modelForm.thinkingLevels.concat([value]);
		}
		levelDraft.value = "";
		levelInputOpen.value = false;
	}

	function removeLevel(index) {
		const next = modelForm.thinkingLevels.slice();
		next.splice(index, 1);
		modelForm.thinkingLevels = next;
	}

	function onLevelKeydown(event) {
		if (!event) return;
		if (event.key === "Enter") {
			event.preventDefault();
			addLevel();
		} else if (event.key === "Escape") {
			event.preventDefault();
			levelDraft.value = "";
			levelInputOpen.value = false;
		}
	}

	function toggleInputMode(mode, checked) {
		const list = modelForm.inputModes.slice();
		const index = list.indexOf(mode);
		if (checked && index < 0) list.push(mode);
		if (!checked && index >= 0) list.splice(index, 1);
		modelForm.inputModes = list;
	}

	function modelFormPatch() {
		const patch = {
			displayName: String(modelForm.displayName || "").trim(),
			supportsTools: modelForm.supportsTools === true,
			disabled: modelForm.disabled === true,
		};
		const context = String(modelForm.contextWindow || "").trim();
		if (context) patch.contextWindow = Number(context);
		const output = String(modelForm.maxOutput || "").trim();
		if (output) patch.maxOutput = Number(output);
		const levels = modelForm.thinkingLevels.slice();
		if (levels.length) patch.thinkingLevels = levels;
		const inputs = modelForm.inputModes.slice();
		if (inputs.length) patch.input = inputs;
		return patch;
	}

	function persistModels(models, message) {
		const s = selected.value;
		if (!s) return;
		ada.api("/save", {
			method: "POST",
			body: {
				id: s.id,
				name: s.displayName || s.id,
				protocol: s.protocol || "",
				baseUrl: s.baseUrl || "",
				proxy: s.proxy || "",
				headers: s.headers || {},
				disabled: s.disabled === true,
				models,
			},
		})
			.then(() => {
				showToast(message);
				editingModelId.value = "";
				modelModalOpen.value = false;
				load();
			})
			.catch((err) => {
				showToast("保存模型失败：" + ((err && err.message) || String(err)), "error");
			});
	}

	function saveModel() {
		const s = selected.value;
		if (!s) return;
		if (modelModalMode.value === "add") {
			const id = String(modelForm.id || "").trim();
			if (!id) {
				showToast("模型 ID 不能为空。", "error");
				return;
			}
			const dup = (s.models || []).some((m) => m && m.id === id);
			if (dup) {
				showToast("模型 ID 已存在。", "error");
				return;
			}
			const next = Object.assign({ id }, modelFormPatch());
			if (!next.displayName) next.displayName = id;
			persistModels((s.models || []).concat(next), "模型已添加。");
			return;
		}
		const target = editingModelId.value;
		if (!target) return;
		const models = (s.models || []).map((m) => {
			if (!m || m.id !== target) return m;
			const merged = Object.assign({}, m, modelFormPatch());
			if (!modelForm.thinkingLevels.length) delete merged.thinkingLevels;
			if (!modelForm.inputModes.length) delete merged.input;
			return merged;
		});
		persistModels(models, "模型已保存。");
	}

	function removeModelById(modelId) {
		const s = selected.value;
		if (!s || !modelId) return;
		ada.confirm("删除模型「" + modelId + "」？").then((ok) => {
			if (!ok) return;
			persistModels((s.models || []).filter((m) => m && m.id !== modelId), "模型已删除。");
		});
	}

	function removeModel() {
		if (!editingModelId.value) return;
		removeModelById(editingModelId.value);
	}

	onMounted(load);

	return {
		loading, error, toast,
		providers, selected, selectedIndex, selectedModels, modelStats, currentModelId,
		enabledCount, filtered, existingModelIds,
		search, filter, tab,
		providerModalOpen, providerModalMode, providerForm, protocolOptions,
		fetchOpen, fetching, fetchedModels, fetchedVisible, selectedFetch, selectedFetchCount, allFetchSelected, fetchError, fetchQuery,
		modelModalOpen, modelModalMode, modelForm, levelDraft, levelInputOpen,
		inputModes: INPUT_MODES,
		load, select, selectModel, toggleProvider, removeProvider,
		copyText, avatarTone, initialOf, modelCount, protocolLabel, formatContext, headerCount,
		openAddProvider, openEditProvider, closeProviderModal, saveProviderForm,
		openFetchModels, closeFetch, toggleFetchModel, toggleSelectAll, addSelectedModels,
		openEditModel, openAddModel, closeModelModal, openLevelInput, addLevel, removeLevel, onLevelKeydown,
		toggleInputMode, saveModel, removeModel, removeModelById,
	};
};
