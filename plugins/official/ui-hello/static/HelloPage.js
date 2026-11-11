import { ref, onMounted } from "/static/ada/ada-runtime.mjs";

export const setup = () => {
	const stack = ["自研响应式内核（ref/effect/scope）", "模板编译器（渲染计划，无 eval）", "SFC 运行时装载器", "CSP script-src 'self' 不变"];
	const mountedAt = ref("");
	onMounted(() => { mountedAt.value = new Date().toLocaleTimeString(); });
	return { stack, mountedAt };
};
