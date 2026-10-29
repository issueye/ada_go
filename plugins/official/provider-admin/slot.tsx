/**
 * 模型设置入口（official/provider-admin 的前端段）：composer.left 插槽。
 *
 * 基座的模板不再写死 `/ext/provider-admin/providers`：插件把自己的页面
 * 入口贡献到模型选择器旁边，基座只知道「这里有个插件插槽」。禁用插件时
 * 入口与页面一起消失，符合 1.5.5「基座零插件 id」。
 */
import { Icon } from "ssr:components";

export default function Slot() {
  return (
    <a
      class="ada-btn ada-btn--ghost ada-btn--sm ada-model-manage"
      href="/ext/provider-admin/providers"
      title="管理供应商与模型"
      aria-label="管理供应商与模型"
    >
      <Icon name="i-plug" size={13} />
      <span class="ada-btn__label">模型设置</span>
    </a>
  );
}
