/**
 * 任务卡片（official/ui-task-plan）：把任务计划渲染成 thread.tray 里的插件卡。
 *
 * DOM/class 参考 a_da 的 plugins/task-plan/slot.tsx（MIT，上游固定 commit）：
 * details/summary 的收起-展开无 JS 也能用；样式在 static/task-plan.css。
 *
 * 数据来源（7.5 的投影约定）：props.data.backend[<本插件 id>][<绑定名>] 是
 * 只读动作的预取结果。清单里的绑定 slot_thread_tray 指向 tools-plan 的 plan 动作，
 * 返回 { plan }。
 *
 * M4→tools-plan 接线点：Core 侧动作注册（Targets.Actions）与 action.invoke 的
 * 真实实现接通之前，动作预取会失败，模板回退到下面的示例数据——本轮刻意允许
 * （内置示例 + 明确注释）；接通后删除 SAMPLE_PLAN，未取到计划时返回 null（不画空面板）。
 */
import { Icon, Todo } from "ssr:components";

const SAMPLE_PLAN = {
  done: 2,
  total: 3,
  steps: [
    { step: "阅读 SSR 前端设计", status: "done" },
    { step: "实现 TSX → VNode → HTML 管线", status: "done" },
    { step: "把动作桥接到 tools-plan", status: "pending" }
  ],
  notes: "示例数据：接通 tools-plan 只读动作后删除。"
};

export default function Slot(props: any) {
  const data = props && props.data ? props.data : null;
  const backend = data && data.backend ? data.backend : null;
  const mine = backend && backend["ui-task-plan"] ? backend["ui-task-plan"] : null;
  const result = mine && mine.slot_thread_tray ? mine.slot_thread_tray : null;
  const plan = result && result.plan ? result.plan : null;
  const active = plan || SAMPLE_PLAN;
  if (!active || !active.total) return null;

  const done = active.done || 0;
  const total = active.total || 0;

  return (
    <details class="tp-card" data-state="collapsed" id="task-plan-card">
      <summary class="tp-card__summary" title="点击展开任务规划步骤">
        <span class="tp-card__head-left">
          <Icon name="i-list" size={12} />
          <span class="tp-card__title">任务规划</span>
          <span class="tp-card__count">{done + "/" + total}</span>
        </span>
      </summary>
      <div class="tp-card__body">
        <Todo steps={active.steps} notes={active.notes} />
      </div>
    </details>
  );
}
