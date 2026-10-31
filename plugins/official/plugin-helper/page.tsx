/**
 * 插件助手（official/plugin-helper）：插件安装向导 + 健康检查入口页。
 *
 * 表单只负责收集 id/名称/描述/工具名；落盘与路径校验都在后端 route 完成。
 * 创建成功后由 plugin-helper.js 自动重载插件列表并跳到新插件详情页；
 * 诊断（check_plugin）走后端 route/工具，不在本页重复实现。
 */
export default function Page(_props: any) {
  return (
    <div class="pc-page" data-plugin-helper>
      <div class="pc-head">
        <div class="pc-head__text">
          <h1 class="pc-title">插件助手</h1>
          <p class="pc-sub">在全局插件目录 <code>~/.adago/plugins</code> 下生成一个可直接加载的第三方插件骨架，跨工作区共享。</p>
        </div>
        <a class="ada-btn ada-btn--secondary ada-btn--sm" href="/extensions">返回插件列表</a>
      </div>

      <p class="pc-status" data-pc-status role="status" aria-live="polite"></p>

      <div class="pc-layout">
        <form class="card pc-form" data-pc-form>
          <label class="pc-field">
            <span class="pc-field__label">插件 ID</span>
            <input class="input" name="id" required maxlength="64" placeholder="my-plugin" />
            <span class="pc-field__hint">3-64 位小写字母、数字或连字符，创建后作为目录名与插件 id。</span>
          </label>

          <label class="pc-field">
            <span class="pc-field__label">插件名称</span>
            <input class="input" name="name" maxlength="80" placeholder="我的插件" />
          </label>

          <label class="pc-field">
            <span class="pc-field__label">描述</span>
            <textarea class="input pc-textarea" name="description" maxlength="500" rows="3" placeholder="这个插件提供什么能力？"></textarea>
          </label>

          <label class="pc-field">
            <span class="pc-field__label">示例工具名</span>
            <input class="input" name="toolName" maxlength="64" placeholder="my_plugin_hello" />
            <span class="pc-field__hint">留空时使用 <code>&lt;id&gt;_hello</code>。</span>
          </label>

          <div class="pc-actions">
            <button class="ada-btn ada-btn--primary ada-btn--sm" type="submit" data-pc-submit>创建插件</button>
            <span class="pc-actions__hint">创建后会写入 3 个文件，并自动重载插件列表。</span>
          </div>
        </form>

        <div class="card pc-preview" role="complementary" aria-label="创建预览">
          <h2 class="pc-preview__title">将创建</h2>
          <code class="pc-preview__path" data-pc-path>~/.adago/plugins/&lt;id&gt;</code>
          <ul class="pc-preview__files">
            <li><code>plugin.json</code><span>清单：third-party、index.ts 入口</span></li>
            <li><code>index.ts</code><span>注册一个只读回显工具</span></li>
            <li><code>README.md</code><span>使用与开发约束</span></li>
          </ul>
          <p class="pc-preview__note">新插件默认未启用；创建后可在插件详情页点“启用”。也可以直接在对话里说「创建一个 XXX 插件」，AI 会生成代码并调用 <code>create_plugin</code> 工具；需要文件/网络等能力时，编辑清单里的 <code>backend.capabilities</code>。</p>
        </div>
      </div>
    </div>
  );
}
