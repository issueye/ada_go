// provider-admin 前端段（composer.left 的模型设置入口）逻辑。
// 纯静态链接：指向本插件页面 /ext/provider-admin/providers；禁用插件时
// 入口与页面一起从注册表消失。旧实现里有一个 svg 图标（ssr:components Icon），
// 模板白名单不含 svg，降级为纯文字按钮。
export const setup = () => ({});
