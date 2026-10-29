/**
 * 提问卡片（official/tools-ask 的前端段）：thread.tray 插槽。
 *
 * 数据来自 props.data.interactions（线程待答交互的只读投影）；提交仍走基座
 * 交互路由 /threads/{id}/interaction/{requestId}，因此审批/提问的安全语义
 * 完全由基座负责——插件只决定怎么展示。
 *
 * 没有待答项时返回 null：插槽贡献为空，基座的默认托盘会接手。
 */
export default function Slot(props: any) {
  const data = props && props.data ? props.data : null;
  const interactions = data && Array.isArray(data.interactions) ? data.interactions : [];
  const thread = data && data.thread ? data.thread : null;
  const csrf = props && props.app && props.app.csrf ? props.app.csrf : "";
  if (!interactions.length || !thread || !thread.id) return null;
  // 多题分步表单（kind=form）由基座托盘渲染：它带「上一题/下一题/提交」的翻页，
  // 这里不重复实现；同一线程同时只有一个待答项，让位给基座即可。
  for (let i = 0; i < interactions.length; i += 1) {
    if (interactions[i] && interactions[i].kind === "form") return null;
  }

  return (
    <div class="aq-list">
      {interactions.map(function (item: any) {
        const options = Array.isArray(item.options) ? item.options : [];
        const action = "/threads/" + thread.id + "/interaction/" + item.id;
        return (
          <form class="aq-card" method="post" action={action}>
            {csrf ? <input type="hidden" name="csrf_token" value={csrf} /> : null}
            <input type="hidden" name="kind" value={item.kind} />
            <div class="aq-card__head">
              <span class="aq-card__title">{item.title || "待答复"}</span>
              <span class="aq-card__meta">等待回答</span>
            </div>
            {item.body ? <p class="aq-card__prompt">{item.body}</p> : null}

            {item.kind === "select" && options.length > 0 ? (
              <div class="aq-card__options">
                {options.map(function (option: any) {
                  const value = typeof option === "string" ? option : option.value;
                  const label = typeof option === "string" ? option : (option.label || option.value);
                  return (
                    <label class="aq-card__option">
                      <input type="radio" name="value" value={value} />
                      <span class="aq-card__option-label">{label}</span>
                    </label>
                  );
                })}
              </div>
            ) : null}

            {item.kind === "input" ? (
              <input class="ada-input aq-card__text" type="text" name="value" placeholder="输入你的回答" />
            ) : null}

            <div class="aq-card__actions">
              {item.kind === "confirm" ? (
                <span class="aq-card__buttons">
                  <button class="ada-btn ada-btn--secondary ada-btn--sm" type="submit" name="decision" value="deny">拒绝</button>
                  <button class="ada-btn ada-btn--primary ada-btn--sm" type="submit" name="decision" value="accept">接受</button>
                </span>
              ) : (
                <span class="aq-card__buttons">
                  <button class="ada-btn ada-btn--primary ada-btn--sm" type="submit" name="decision" value="accept">提交</button>
                  <button class="ada-btn ada-btn--secondary ada-btn--sm" type="submit" name="decision" value="deny">拒绝</button>
                </span>
              )}
              <button class="ada-btn ada-btn--ghost ada-btn--sm" type="submit" name="decision" value="cancel">取消</button>
            </div>
          </form>
        );
      })}
    </div>
  );
}
