/**
 * 通用图表 block：一个模板服务 ui-charts/{bar,column,compare,stacked,heatmap,spark}。
 *
 * data 是调用方给的静态数据（见 schemas/chart.json）；这里只做展示与换算。
 * 比例一律换算成固定档位 class，不使用内联 style 或 SVG（CSP + block profile）。
 */

function toNumber(value: any): number {
  const n = Number(value);
  return isFinite(n) ? n : 0;
}

function formatNumber(value: any): string {
  const n = toNumber(value);
  const text = String(Math.round(n * 100) / 100);
  const dot = text.indexOf(".");
  const intPart = dot >= 0 ? text.slice(0, dot) : text;
  const decPart = dot >= 0 ? text.slice(dot) : "";
  let out = "";
  for (let i = 0; i < intPart.length; i += 1) {
    if (i > 0 && (intPart.length - i) % 3 === 0) out += ",";
    out += intPart.charAt(i);
  }
  return out + decPart;
}

function clampLevel(level: number, min: number, max: number): number {
  if (!isFinite(level)) return min;
  if (level < min) return min;
  if (level > max) return max;
  return level;
}

function levelOf(value: number, max: number, buckets: number): number {
  if (max <= 0) return 1;
  return clampLevel(Math.round((value / max) * buckets), 1, buckets);
}

function scaleMax(items: any[]): number {
  let max = 0;
  for (const item of items) {
    const a = toNumber(item.value);
    const b = toNumber(item.value2);
    if (a > max) max = a;
    if (b > max) max = b;
  }
  return max > 0 ? max : 1;
}

function Head(data: any) {
  return (
    <div class="adag-chart__head">
      <span class="adag-chart__title">{data.title}</span>
      {data.subtitle ? <span class="adag-chart__subtitle">{data.subtitle}</span> : null}
    </div>
  );
}

function Legend(props: any) {
  return (
    <div class="adag-chart__legend">
      <span><span class="adag-chart__dot"></span>{props.a}</span>
      <span><span class="adag-chart__dot adag-chart__dot--b"></span>{props.b}</span>
    </div>
  );
}

function BarChart(data: any, items: any[], unit: string): any {
  const max = scaleMax(items);
  return (
    <div class="adag-chart">
      {Head(data)}
      <ol class="adag-chart__rows">
        {items.map(function (item: any, index: number) {
          const value = toNumber(item.value);
          const meta = [formatNumber(value) + unit, item.note].filter(Boolean).join(" · ");
          return (
            <li class="adag-chart__row">
              <div class="adag-chart__line">
                <span class="adag-chart__rank">{index + 1}</span>
                <span class="adag-chart__name">{item.label}</span>
                <span class="adag-chart__meta">{meta}</span>
              </div>
              <div class="adag-chart__track">
                <span class={"adag-chart__bar adag-chart__bar--l" + levelOf(value, max, 10)}></span>
              </div>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

function ColumnChart(data: any, items: any[], unit: string): any {
  const max = scaleMax(items);
  return (
    <div class="adag-chart">
      {Head(data)}
      <ol class="adag-chart__cols">
        {items.map(function (item: any) {
          const value = toNumber(item.value);
          return (
            <li class="adag-chart__col">
              <span class="adag-chart__col-value">{formatNumber(value) + unit}</span>
              <span class={"adag-chart__colbar adag-chart__colbar--h" + levelOf(value, max, 10)}></span>
              <span class="adag-chart__col-label">{item.label}</span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

function CompareChart(data: any, items: any[], unit: string): any {
  const max = scaleMax(items);
  return (
    <div class="adag-chart">
      {Head(data)}
      <ol class="adag-chart__rows">
        {items.map(function (item: any, index: number) {
          const a = toNumber(item.value);
          const b = toNumber(item.value2);
          return (
            <li class="adag-chart__row">
              <div class="adag-chart__line">
                <span class="adag-chart__rank">{index + 1}</span>
                <span class="adag-chart__name">{item.label}</span>
                <span class="adag-chart__meta">
                  {formatNumber(a) + unit + " / " + formatNumber(b) + unit}
                </span>
              </div>
              <div class="adag-chart__pair">
                <div class="adag-chart__track">
                  <span class={"adag-chart__bar adag-chart__bar--l" + levelOf(a, max, 10)}></span>
                </div>
                <div class="adag-chart__track">
                  <span class={"adag-chart__bar adag-chart__bar--alt adag-chart__bar--l" + levelOf(b, max, 10)}></span>
                </div>
              </div>
            </li>
          );
        })}
      </ol>
      {Legend({ a: data.legendA || "数值 A", b: data.legendB || "数值 B" })}
    </div>
  );
}

function StackedChart(data: any, items: any[], unit: string): any {
  let total = 0;
  for (const item of items) total += toNumber(item.value);
  if (total <= 0) total = 1;
  const palette = ["a", "b", "c", "d"];
  return (
    <div class="adag-chart">
      {Head(data)}
      <div class="adag-chart__stack">
        {items.map(function (item: any, index: number) {
          const value = toNumber(item.value);
          const level = clampLevel(Math.round((value / total) * 10), 1, 10);
          return (
            <span class={"adag-chart__seg adag-chart__seg--" + palette[index % 4] + " adag-chart__seg--l" + level}></span>
          );
        })}
      </div>
      <div class="adag-chart__legend">
        {items.map(function (item: any, index: number) {
          const value = toNumber(item.value);
          const pct = Math.round((value / total) * 100);
          return (
            <span>
              <span class={"adag-chart__dot adag-chart__dot--" + palette[index % 4]}></span>
              {item.label + " " + pct + "%" + (unit ? "（" + formatNumber(value) + unit + "）" : "")}
            </span>
          );
        })}
      </div>
    </div>
  );
}

function HeatmapChart(data: any, items: any[], unit: string): any {
  let max = 0;
  for (const item of items) {
    const value = toNumber(item.value);
    if (value > max) max = value;
  }
  return (
    <div class="adag-chart">
      {Head(data)}
      <div class="adag-heat">
        {items.map(function (item: any) {
          const value = toNumber(item.value);
          const given = item.level;
          const level = given === undefined || given === null
            ? levelOf(value, max > 0 ? max : 1, 5)
            : clampLevel(Math.round(toNumber(given)), 1, 5);
          return (
            <span
              class={"adag-heat__cell adag-heat__cell--l" + level}
              title={item.label + " " + formatNumber(value) + unit + (item.note ? " · " + item.note : "")}
            ></span>
          );
        })}
      </div>
    </div>
  );
}

const SPARK_CHARS = ["▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"];

function SparkChart(data: any, values: number[], unit: string): any {
  let min = Infinity;
  let max = -Infinity;
  for (const value of values) {
    if (value < min) min = value;
    if (value > max) max = value;
  }
  if (!isFinite(min) || !isFinite(max)) return null;
  const span = max - min || 1;
  let text = "";
  for (const value of values) {
    const idx = clampLevel(Math.round(((value - min) / span) * (SPARK_CHARS.length - 1)), 0, SPARK_CHARS.length - 1);
    text += SPARK_CHARS[idx];
  }
  return (
    <div class="adag-chart">
      {Head(data)}
      <p class="adag-spark">{text}</p>
      <div class="adag-chart__legend">
        <span>{"最低 " + formatNumber(min) + unit}</span>
        <span>{"最高 " + formatNumber(max) + unit}</span>
      </div>
    </div>
  );
}

export default function ChartBlock(props: any) {
  const block = props && props.block ? props.block : {};
  const data = block.data || {};
  const kind = String(block.renderer || "").split("/")[1] || "bar";
  const unit = data.unit ? String(data.unit) : "";
  const items = Array.isArray(data.items) ? data.items.slice(0, 12) : [];

  if (kind === "spark") {
    const values = Array.isArray(data.values) ? data.values.slice(0, 48).map(function (v: any) {
      return toNumber(v);
    }) : [];
    if (!values.length) return null;
    return SparkChart(data, values, unit);
  }
  if (!items.length) return null;
  if (kind === "column") return ColumnChart(data, items, unit);
  if (kind === "compare") return CompareChart(data, items, unit);
  if (kind === "stacked") return StackedChart(data, items, unit);
  if (kind === "heatmap") return HeatmapChart(data, items, unit);
  return BarChart(data, items, unit);
}
