/**
 * 环境变量管理页（official/env-manager）：/ext/env-manager/settings
 *
 * 首屏只渲染骨架；数据与操作由 static/env-manager.js 经本插件 route 完成：
 *   GET  /state   全局 + 工作区两层变量与合并视图
 *   POST /set     写入一个变量（scope = global | workspace）
 *   POST /remove  删除一个变量
 * 写入后需要重载插件（POST /agent-plugins/reload），新的子进程才会拿到新环境。
 */
export default function Page(_props: any) {
  return (
    <div class="em-page" data-em-page>
      <div class="em-head">
        <div class="em-head__text">
          <h1 class="em-title">环境变量</h1>
          <p class="em-sub">
            管理注入插件子进程的环境变量（<code>ctx.exec</code>、后台任务、stdio MCP）。全局 <code>~/.adago/env.json</code> 与工作区 <code>.adag/env.json</code> 两层，工作区同名覆盖全局；修改后重载插件生效。
          </p>
        </div>
        <div class="em-head__actions">
          <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-em-refresh>
            刷新
          </button>
          <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-em-reload>
            重载插件
          </button>
        </div>
      </div>

      <p class="em-status" data-em-status role="status" aria-live="polite"></p>

      <form class="em-add" data-em-add>
        <label class="em-field">
          <span class="em-field__label">范围</span>
          <select class="em-input" data-em-scope>
            <option value="workspace">工作区</option>
            <option value="global">全局</option>
          </select>
        </label>
        <label class="em-field em-field--grow">
          <span class="em-field__label">变量名</span>
          <input class="em-input" data-em-key name="key" placeholder="GITHUB_TOKEN" />
        </label>
        <label class="em-field em-field--grow">
          <span class="em-field__label">值</span>
          <input class="em-input" data-em-value name="value" placeholder="值（可为空）" />
        </label>
        <button class="ada-btn ada-btn--primary ada-btn--sm" type="submit">
          添加 / 更新
        </button>
      </form>

      <div class="em-table-wrap">
        <table class="em-table">
          <thead>
            <tr>
              <th class="em-col-scope">范围</th>
              <th class="em-col-key">变量名</th>
              <th>值</th>
              <th class="em-col-actions">操作</th>
            </tr>
          </thead>
          <tbody data-em-rows>
            <tr>
              <td class="em-empty" colspan="4">正在读取环境变量…</td>
            </tr>
          </tbody>
        </table>
      </div>

      <p class="em-hint">
        值只在当前页面展示（默认打码，可点「显示」）；日志与审计只记录变量名与路径。变量名需匹配 <code>^[A-Za-z_][A-Za-z0-9_]*$</code>，<code>ADAG_</code> 前缀保留。
      </p>
    </div>
  );
}
