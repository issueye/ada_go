/**
 * composer-telemetry 的浏览器 bundle 入口。
 *
 * 一个插件只有一个 client bundle；`islands` 按服务端注入的 data-ada-entry
 * 选择组件，default 作为没有 entry 时的兜底。这里只有一个插槽岛：
 * composer.footer（清单声明 clientOnly，服务端只出空壳）。
 */
import ComposerFooter from "./composer-footer.tsx";

export const islands = {
  "slot:composer.footer": ComposerFooter,
};

export default ComposerFooter;
