/**
 * 供应商管理（official/provider-admin）：Master-Detail 的模型设置页。
 *
 * 首屏只渲染骨架；数据与操作全部经本插件 route（GET /state、POST /save、
 * /remove、/fetch-models、/select）。API Key 由后端保存，页面只显示
 * “已配置 + 尾号”，输入框留空表示保持原值。
 */
export default function Page(_props: any) {
  return (
    <div class="pm-page" data-provider-admin>
      <div class="pm-head">
        <div class="pm-head__text">
          <h1 class="pm-title">模型设置</h1>
          <p class="pm-sub">管理自定义模型供应商，配置后可在聊天时选择使用。</p>
        </div>
        <div class="pm-head__actions">
          <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-pm-refresh title="重新读取供应商列表">
            刷新
          </button>
          <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" data-pm-add>
            添加供应商
          </button>
        </div>
      </div>

      <p class="pm-status" data-pm-status role="status" aria-live="polite"></p>

      <div class="pm-layout">
        <div class="pm-list" data-pm-list aria-label="供应商列表">
          <p class="pm-empty">正在读取供应商…</p>
        </div>
        <section class="pm-detail" data-pm-detail aria-label="供应商详情">
          <div class="pm-placeholder">
            <span class="pm-placeholder__title">选择一个供应商</span>
            <span class="pm-placeholder__hint">从左侧选择，或添加一个新的供应商。</span>
          </div>
        </section>
      </div>
    </div>
  );
}
