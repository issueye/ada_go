/**
 * 供应商管理（official/provider-admin）：供应商与模型清单 + 切换当前模型。
 *
 * 数据来自 Core 的只读投影 props.data.providers（provider.list + model.list），
 * 不含 API Key；"设为当前"通过基座路由 POST /model/select（model.set）完成。
 *
 * 注意：插件侧组件运行时只实现了 Icon/TodoList，这里只用原生标签 + ada-* class。
 */
export default function Page(props: any) {
  const data = props && props.data ? props.data : null;
  const projection = data && data.providers ? data.providers : null;
  const providers = projection && Array.isArray(projection.providers) ? projection.providers : [];
  const current = projection && projection.current ? projection.current : null;
  const csrf = props && props.app && props.app.csrf ? props.app.csrf : "";
  const currentLabel =
    current && current.providerId ? current.providerId + " / " + current.modelId : "未设置";

  if (!providers.length) {
    return (
      <div class="pa-page">
        <div class="pa-head">
          <h1 class="pa-title">供应商</h1>
        </div>
        <div class="pa-notice" role="status">
          没有读到供应商。请确认 provider 插件已在插件页启用，并在配置里填写端点与密钥。
        </div>
      </div>
    );
  }

  return (
    <div class="pa-page">
      <div class="pa-head">
        <div>
          <h1 class="pa-title">供应商</h1>
          <p class="pa-sub">当前模型：<strong>{currentLabel}</strong></p>
        </div>
      </div>

      <div class="pa-grid">
        {providers.map(function (p: any) {
          const models = Array.isArray(p.models) ? p.models : [];
          return (
            <section class="card pa-provider">
              <div class="pa-provider__head">
                <span class="pa-provider__name">{p.name || p.id}</span>
                {p.active ? <span class="badge badge-success">当前供应商</span> : null}
                <span class="pa-provider__id">{p.id}</span>
              </div>
              {p.baseUrl ? <p class="pa-provider__base">{p.baseUrl}</p> : null}

              <div class="pa-models">
                {models.map(function (m: any) {
                  const isCurrent =
                    current && current.providerId === p.id && current.modelId === m.id;
                  return (
                    <div class="pa-model">
                      <div class="pa-model__info">
                        <span class="pa-model__id">{m.id}</span>
                        <span class="pa-model__meta">
                          {m.name && m.name !== m.id ? m.name + " · " : ""}
                          上下文 {m.contextWindow || "—"}
                          {m.maxOutput ? " · 输出 " + m.maxOutput : ""}
                          {m.supportsTools ? " · 工具" : ""}
                          {m.supportsThinking ? " · 思考" : ""}
                        </span>
                      </div>
                      {isCurrent ? (
                        <span class="badge badge-success">当前</span>
                      ) : (
                        <form class="pa-model__form" method="post" action="/model/select">
                          {csrf ? <input type="hidden" name="csrf_token" value={csrf} /> : null}
                          <input type="hidden" name="providerId" value={p.id} />
                          <input type="hidden" name="modelId" value={m.id} />
                          <button class="ada-btn ada-btn--secondary ada-btn--sm" type="submit">
                            设为当前
                          </button>
                        </form>
                      )}
                    </div>
                  );
                })}
                {models.length === 0 ? <p class="pa-empty">这个供应商没有可用模型</p> : null}
              </div>
            </section>
          );
        })}
      </div>
    </div>
  );
}

