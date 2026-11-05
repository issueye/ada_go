/**
 * Island Counter（official/ui-hello-tsx 的 P1-7 最小试点）。
 *
 * 同一份 TSX：
 *   - 服务端用 ada-render.renderToJSON 输出 VNode，Go 校验/转义成首屏 HTML；
 *   - 浏览器加载 static/client.js，用同一份 defineComponent hydrate；
 *   - 点击只更新本岛，不整页刷新。
 *
 * 数据只来自 props；事件只在客户端绑定，服务端 VNode 不携带函数。
 */
import { h, defineComponent } from "ada:render";

export default defineComponent({
  props: { start: { default: 0 } },
  data() {
    return { n: this.start };
  },
  methods: {
    inc() {
      this.n += 1;
    },
  },
  render(h) {
    return (
      <section class="ext-hello" data-island-counter>
        <h2>Island counter</h2>
        <p class="muted">
          这个页面由同一份 TSX 在服务端 SSR，并在浏览器 hydrate；点击按钮只更新本岛。
        </p>
        <p>
          count: <strong>{this.n}</strong>
        </p>
        <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" onClick={this.inc}>
          +1
        </button>
      </section>
    );
  },
});
