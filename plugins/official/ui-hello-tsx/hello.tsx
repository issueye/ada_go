/**
 * Hello 页面（official/ui-hello-tsx）：M4 前端插件的最小示例（frontend.page）。
 *
 * 访问 /ext/ui-hello-tsx/hello：TSX 在 Gateway 的渲染 Runtime 里执行成 VNode，
 * 再由 Go 渲染器转义成 HTML——无 JS、无 hydration（7.1、7.3）。
 * 组件只从 ssr:components 导入；数据只来自 props（7.5 的投影），没有副作用。
 */
export default function Page(props: any) {
  const app = props && props.app ? props.app : null;
  const plugin = props && props.plugin ? props.plugin : null;
  const product = app && app.product ? app.product : "ada-go";
  const route = app && app.route ? app.route : "/ext";

  return (
    <section class="ext-hello">
      <h2>Hello from TSX</h2>
      <p class="muted">
        这个页面由插件 TSX 在服务端渲染成 VNode，再由基座校验并转义成 HTML；没有客户端运行时。
      </p>
      <ul>
        <li>产品：{product}</li>
        {plugin ? <li>插件：{plugin.id}（{plugin.name}）版本 {plugin.version}</li> : null}
        <li>路由：{route}</li>
      </ul>
    </section>
  );
}
