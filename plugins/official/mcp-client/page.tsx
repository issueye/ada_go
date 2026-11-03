/**
 * MCP 服务端管理页（official/mcp-client）：/ext/mcp-client/servers
 *
 * 首屏只渲染骨架；数据与操作由 static/mcp-client.js 经本插件 route 完成：
 *   GET  /servers        server 状态 + 工具摘要
 *   GET  /config         工作区 .adago/mcp.json 原文
 *   POST /config         保存配置
 *   POST /servers/toggle 启用/停用单个 server
 * 保存后需要重载插件（POST /agent-plugins/reload），工具列表才会更新。
 *
 * 配置编辑器是「透明 textarea + 背后的高亮层」：pre/code 只做展示（aria-hidden），
 * 真实文本与光标在 textarea 上；高亮与格式化由 mcp-client.js 完成。
 */
export default function Page(_props: any) {
  return (
    <div class="mcp-page" data-mcp-page>
      <div class="mcp-head">
        <div class="mcp-head__text">
          <h1 class="mcp-title">MCP 服务端</h1>
          <p class="mcp-sub">
            管理接入本工作区的 MCP server。配置来自 <code>.adago/mcp.json</code>（用户级 <code>~/.adago/mcp.json</code> 只读）；保存或启停后需要重载插件，工具才会更新。
          </p>
        </div>
        <div class="mcp-head__actions">
          <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-mcp-refresh>
            刷新状态
          </button>
          <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-mcp-reload>
            重载插件
          </button>
          <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" data-mcp-edit>
            编辑配置
          </button>
        </div>
      </div>

      <p class="mcp-status" data-mcp-status role="status" aria-live="polite"></p>

      <section class="mcp-servers" data-mcp-servers aria-label="MCP server 列表">
        <p class="mcp-empty">正在读取 server 状态…</p>
      </section>

      <section class="mcp-config is-hidden" data-mcp-config aria-label="工作区配置">
        <div class="mcp-config__head">
          <div class="mcp-config__text">
            <h2 class="mcp-config__title">工作区配置</h2>
            <p class="mcp-config__path" data-mcp-config-path>.adago/mcp.json</p>
          </div>
          <div class="mcp-config__actions">
            <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-mcp-config-reload>
              重新读取
            </button>
            <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-mcp-format>
              格式化
            </button>
            <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" data-mcp-save>
              保存
            </button>
          </div>
        </div>
        <div class="mcp-editor-wrap" data-mcp-editor-wrap>
          <pre class="mcp-editor-highlight" data-mcp-highlight aria-hidden="true"><code data-mcp-highlight-code>{" "}</code></pre>
          <textarea
            class="mcp-editor"
            data-mcp-editor
            rows="18"
            aria-label="MCP 配置文件内容"
          ></textarea>
        </div>
        <p class="mcp-config__status" data-mcp-config-status role="status" aria-live="polite"></p>
        <p class="mcp-config__hint">
          保存会直接覆盖工作区配置。密钥请用 <code>{"${secret:mcp_token}"}</code> 引用，不要把明文写进配置。
        </p>
      </section>
    </div>
  );
}
