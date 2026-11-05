/**
 * ui-hello-tsx 的浏览器 bundle 入口。
 *
 * 一个插件只有一个 client bundle；`islands` 按服务端注入的 data-ada-entry
 * 选择组件，default 作为没有 entry 时的兜底。
 */
import Counter from "./counter.tsx";
import Slot from "./slot.tsx";

export const islands = {
  "page:counter": Counter,
  "slot:sidebar.top": Slot,
};

export default Counter;
