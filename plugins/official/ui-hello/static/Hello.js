// ui-hello 插件逻辑（SFC 的 <script src> 指向的 ESM；零构建，原生 import）。
import { ref } from "/static/ada/ada-runtime.mjs";

export const setup = () => {
	const n = ref(0);
	return {
		who: "ada",
		n,
		bump() { n.value += 1; },
	};
};
