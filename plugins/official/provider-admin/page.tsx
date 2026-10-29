/**
 * 供应商管理（official/provider-admin）：供应商/模型目录 + 切换当前模型。
 *
 * 页面由 SSR 用只读投影渲染首屏；交互经本插件自己的 route：
 *   GET  /ext/provider-admin/api/state   读取最新目录与当前模型
 *   POST /ext/provider-admin/api/select  切换当前模型
 * 无 JS 时表单回落到基座 POST /model/select（同一套校验）。
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
      <div class="pa-page" data-provider-admin data-empty="true">
        <div class="pa-head">
          <h1 class="pa-title">供应商</h1>
        </div>
        <div class="pa-notice" role="status">
          没有读到供应商。请确认 provider 插件已在插件页启用，并在配置里填写端点与密钥。
        </div>
        <p class="pa-status" data-pa-status role="status" aria-live="polite"></p>
      </div>
    );
  }

  return (
    <div class="pa-page" data-provider-admin data-empty="false">
      <div class="pa-head">
        <div>
          <h1 class="pa-title">供应商</h1>
          <p class="pa-sub">
            当前模型：<strong data-pa-current-label>{currentLabel}</strong>
          </p>
        </div>
      </div>
      <p class="pa-status" data-pa-status role="status" aria-live="polite"></p>

      <div class="pa-grid">
        {providers.map(function (p: any) {
          const models = Array.isArray(p.models) ? p.models : [];
          const providerCurrent = current && current.providerId === p.id;
          return (
            <section
              class="card pa-provider"
              data-pa-provider
              data-provider-id={p.id}
              data-current={providerCurrent ? "true" : "false"}
            >
              <div class="pa-provider__head">
                <span class="pa-provider__name">{p.name || p.id}</span>
                <span class="badge badge-success pa-provider__badge" data-pa-current>
                  当前供应商
                </span>
                <span class="pa-provider__id">{p.id}</span>
              </div>
              {p.baseUrl ? <p class="pa-provider__base">{p.baseUrl}</p> : null}

              <div class="pa-models">
                {models.map(function (m: any) {
                  const isCurrent =
                    current && current.providerId === p.id && current.modelId === m.id;
                  return (
                    <div
                      class="pa-model"
                      data-pa-model
                      data-provider-id={p.id}
                      data-model-id={m.id}
                      data-current={isCurrent ? "true" : "false"}
                    >
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
                      <span class="badge badge-success pa-model__badge" data-pa-current>
                        当前
                      </span>
                      <form
                        class="pa-model__form"
                        method="post"
                        action="/model/select"
                        data-pa-select
                        data-provider-id={p.id}
                        data-model-id={m.id}
                      >
                        {csrf ? <input type="hidden" name="csrf_token" value={csrf} /> : null}
                        <input type="hidden" name="providerId" value={p.id} />
                        <input type="hidden" name="modelId" value={m.id} />
                        <button class="ada-btn ada-btn--secondary ada-btn--sm" type="submit">
                          设为当前
                        </button>
                      </form>
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
