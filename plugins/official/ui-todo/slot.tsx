/**
 * Todo 列表（official/ui-todo）：把 tools-plan 的当前计划渲染成 chat.corner 卡片。
 *
 * 数据只来自只读动作预取：props.data.backend["ui-todo"].slot_chat_corner.plan。
 * 没有计划（total=0）或动作失败时返回 null——不画空卡，也绝不回退到示例数据。
 */
import { Icon, Todo } from "ssr:components";

export default function Slot(props: any) {
  const data = props && props.data ? props.data : null;
  const backend = data && data.backend ? data.backend : null;
  const mine = backend && backend["ui-todo"] ? backend["ui-todo"] : null;
  const result = mine && mine.slot_chat_corner ? mine.slot_chat_corner : null;
  const plan = result && result.plan ? result.plan : null;
  if (!plan || !plan.total) return null;

  const done = plan.done || 0;
  const total = plan.total || 0;
  const complete = total > 0 && done >= total;

  return (
    <details class={"todo-card" + (complete ? " is-done" : "")} data-state="collapsed" id="todo-card">
      <summary class="todo-card__summary" title="点击展开 Todo 列表">
        <span class="todo-card__head">
          <Icon name="i-list" size={12} />
          <span class="todo-card__title">Todo 列表</span>
          <span class="todo-card__count">{done + "/" + total}</span>
        </span>
      </summary>
      <div class="todo-card__body">
        <Todo steps={plan.steps || []} notes={plan.notes} />
      </div>
    </details>
  );
}
