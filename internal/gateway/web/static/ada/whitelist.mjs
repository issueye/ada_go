// 白名单词汇表 —— 客户端编译期/打补丁期的唯一安全关卡数据源。
//
// 与 internal/gateway/frontend/vnode.go 的 AllowedTags/AllowedAttributes/AllowedInputTypes
// 及 spec/vnode.schema.json 逐字一致（adag verify 做比对）。词汇表是冻结契约：
// 修改这里必须同步 Go 侧与 schema，并过 verify。
//
// 与 SSR 时代的差异只有两点（见 proposals/spa-frontend-framework.md §5.7）：
//   1. 事件绑定（@event → on* DOM 监听）对插件 SFC 放开——插件代码本就是同源脚本；
//      模型内容（display profile）不经本文件，仍走服务端校验的 VNode JSON。
//   2. :style 仅接受对象/数组绑定，运行时走 CSSOM（style.setProperty），
//      静态 style="..." 字符串属性仍被拒绝（UED 纪律 + CSP style-src）。

export const TAGS = [
	"div", "span", "p", "br", "hr",
	"h1", "h2", "h3", "h4", "h5", "h6",
	"ul", "ol", "li", "strong", "em", "code", "pre", "small", "blockquote",
	"a", "main", "nav", "section",
	"form", "details", "summary", "label", "input", "button", "select", "option", "optgroup", "textarea",
	"table", "thead", "tbody", "tr", "th", "td", "caption",
];

// 组件名规则：插件 SFC 模板中只允许引用 "ui-" 前缀组件（宿主 UI kit，由 shell 注册）。
// 其余 kebab-case 标签一律按原生元素处理（未知元素无害但会在编译期产生告警）。
// display profile（模型内容）的组件词汇（TodoList/Icon 等）不在本文件 —— 那条路径
// 是独立的 VNode JSON 渲染器，词汇表仍以 spec/vnode.schema.json 为准。
export const COMPONENT_PREFIX = "ui-";

export const ATTRS = [
	"class", "id", "href", "title", "for", "name", "value", "type", "placeholder",
	"required", "disabled", "readonly", "checked", "open", "min", "max", "step",
	"hidden", "tabindex",
	"minlength", "maxlength", "cols", "rows", "action", "method", "start", "scope",
	"colspan", "rowspan", "role",
	"key",   // v-for 渲染引擎内部用
];

export const INPUT_TYPES = [
	"button", "submit", "reset", "text", "search", "email", "password",
	"number", "checkbox", "radio", "hidden",
];

export const DANGEROUS_URL_PREFIXES = ["javascript:", "vbscript:", "data:text/html"];

// URL 语义属性：值需要过 DANGEROUS_URL_PREFIXES 检查。
export const URL_ATTRS = ["href", "action"];

const tagSet = new Set(TAGS);
const attrSet = new Set(ATTRS);
const inputTypeSet = new Set(INPUT_TYPES);

export function isAllowedTag(tag) {
	return tagSet.has(tag);
}

export function isUiComponent(name) {
	return typeof name === "string" && name.startsWith(COMPONENT_PREFIX);
}

export function isAllowedAttr(name) {
	if (attrSet.has(name)) return true;
	if (name.startsWith("aria-")) return true;
	if (name.startsWith("data-")) return true;
	return false;
}

export function isAllowedInputType(v) {
	return inputTypeSet.has(v);
}

export function isDangerousURL(v) {
	if (typeof v !== "string") return false;
	const lower = v.toLowerCase().replace(/[\s\x00-\x1f]/g, "");
	return DANGEROUS_URL_PREFIXES.some((p) => lower.startsWith(p));
}

// 编译期/打补丁期共用的属性级校验。返回 null 表示通过，否则返回错误消息。
export function checkAttr(name, value) {
	if (name.startsWith("on") && !name.startsWith("on:")) {
		// 静态 on* 属性（onclick="..."）永远拒绝；事件绑定只经编译器 @event 路径。
		return `静态事件属性 "${name}" 被拒绝：请使用 @event 绑定`;
	}
	if (name === "style") {
		return `静态 style 属性被拒绝：请使用 :style 对象绑定`;
	}
	if (!isAllowedAttr(name)) {
		return `属性 "${name}" 不在白名单`;
	}
	if (name === "type" && value !== undefined && !isAllowedInputType(value)) {
		return `input type "${value}" 不在白名单`;
	}
	if (URL_ATTRS.includes(name) && isDangerousURL(value)) {
		return `URL 属性 "${name}" 含危险协议`;
	}
	return null;
}

// 内联 <script> 的 SFC 拒绝消息（sfc.mjs 与安装 preflight 共用文案口径）。
export const INLINE_SCRIPT_MSG =
	"SFC 不允许内联 <script>：逻辑请放在旁边的 .js ESM 文件并通过 <script src=\"./xxx.js\"> 引用";
