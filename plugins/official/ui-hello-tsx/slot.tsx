/**
 * Sidebar 岛屿（official/ui-hello-tsx 的 P2-3 插槽试点）。
 *
 * 与 counter 共用 static/client.js；bundle 通过 `islands` 映射按 entry 选择组件，
 * 因此同一插件可以同时有页面岛与插槽岛。
 */
import { h, defineComponent } from "ada:render";
import { Badge } from "ada:ui";

export default defineComponent({
  render(h) {
    return (
      <div class="ext-hello-slot" title="P2-3 插槽岛屿">
        <Badge tone="brand" label="island" />
      </div>
    );
  },
});
