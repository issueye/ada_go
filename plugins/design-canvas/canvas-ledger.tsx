/**
 * design-canvas/canvas-ledger —— 设计画布台账块。
 *
 * 约束（display profile，写错即降级）：
 *   - 只做纯计算：数据来自 props.block.data，已通过 schemas/canvas-ledger.json 校验；
 *   - 只有展示标签，禁止 img/svg/form/input/button/iframe 与 on*、style、原始 HTML；
 *   - 比例一律换算成固定档位 class（adag-chart__bar--l1..l10），不用内联 style；
 *   - 不依赖插件自己的 CSS：安装型插件的 pages/styles 不会被装配，只能复用宿主
 *     在 app-local.css 里提供的 adag-chart__* 令牌类。
 */

function toCount(value) {
  const n = Number(value);
  return isFinite(n) && n > 0 ? Math.round(n) : 0;
}

function levelOf(value, max) {
  if (max <= 0) return 1;
  const level = Math.round((toCount(value) / max) * 10);
  if (level < 1) return 1;
  if (level > 10) return 10;
  return level;
}

function barClass(value, max) {
  return "adag-chart__bar adag-chart__bar--l" + levelOf(value, max);
}

// 注意：本地辅助函数必须**当函数调用**（GroupRow({...})），不能写成 JSX 标签
// <GroupRow />：宿主的 TSX 编译器只认 import 自 ssr:components 的宿主组件，
// 其它 JSX 标签会报「组件函数缺少 __ada_component 名」。ui-charts 也遵循这个写法。
function GroupRow(props) {
  const row = props.row || {};
  return (
    <li class="adag-chart__row">
      <span class="adag-chart__line">
        <span class="adag-chart__name">{row.label}</span>
        {row.note ? <span class="adag-chart__meta">{row.note}</span> : null}
      </span>
      <span class="adag-chart__delta">{toCount(row.value)}</span>
      <span class="adag-chart__track">
        <span class={barClass(row.value, props.max)} />
      </span>
    </li>
  );
}

function NodeTable(props) {
  const nodes = props.nodes || [];
  if (!nodes.length) return null;
  return (
    <details>
      <summary>节点清单（{nodes.length}）</summary>
      <table>
        <thead>
          <tr>
            <th scope="col">标题</th>
            <th scope="col">体系</th>
            <th scope="col">画板</th>
            <th scope="col">尺寸</th>
          </tr>
        </thead>
        <tbody>
          {nodes.map((node) => (
            <tr>
              <td>{node.title}</td>
              <td>{node.system || "—"}</td>
              <td>{node.artboard || "—"}</td>
              <td>{node.size || "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </details>
  );
}

export default function CanvasLedgerBlock(props) {
  const data = (props && props.block && props.block.data) || {};
  const groups = Array.isArray(data.groups) ? data.groups : [];
  const nodes = Array.isArray(data.nodes) ? data.nodes : [];
  const total = toCount(data.total);

  // 空台账是合法状态（刚清空画布 / 还没生成预览）：返回 null 让宿主整块跳过，
  // 不要画一张"0 个节点"的空卡。
  if (total === 0 && groups.length === 0 && nodes.length === 0) return null;

  let max = 0;
  for (const group of groups) {
    const value = toCount(group && group.value);
    if (value > max) max = value;
  }

  return (
    <section class="adag-chart">
      <div class="adag-chart__head">
        <span class="adag-chart__title">{data.title}</span>
        <span class="adag-chart__subtitle">
          {total} 个节点{data.subtitle ? " · " + data.subtitle : ""}
        </span>
      </div>
      <ul class="adag-chart__rows">
        {groups.map((row) => GroupRow({ row: row, max: max }))}
      </ul>
      {NodeTable({ nodes: nodes })}
      {data.canvasUrl ? (
        <p class="adag-chart__meta">
          <a href={data.canvasUrl}>{data.canvasUrl}</a>
        </p>
      ) : null}
    </section>
  );
}
