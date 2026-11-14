<template>
  <div class="mc-page">
    <div class="mc-head">
      <div class="mc-title-row">
        <h1 class="mc-title">MCP 服务端</h1>
        <span class="mc-tip mc-tip--below" data-tip="管理接入本工作区的 MCP server。工作区 .adago/mcp.json 可编辑，用户级 ~/.adago/mcp.json 只读；同名 server 由工作区覆盖。保存或启停后需重载插件，工具列表才会更新。" aria-label="管理接入本工作区的 MCP server。工作区 .adago/mcp.json 可编辑，用户级 ~/.adago/mcp.json 只读；同名 server 由工作区覆盖。保存或启停后需重载插件，工具列表才会更新。" tabindex="0" role="img">!</span>
      </div>
      <div class="mc-actions">
        <button class="mc-btn" type="button" @click="refresh">刷新状态</button>
        <button class="mc-btn" type="button" :disabled="reloading" @click="reloadPlugins">{{ reloading ? '重载中…' : '重载插件' }}</button>
        <button class="mc-btn mc-btn--primary" type="button" @click="openModal">编辑配置</button>
      </div>
    </div>

    <div class="mc-summary">
      <span>共 {{ counts.all }} 个 server</span>
      <div class="mc-seg" role="group" aria-label="按状态筛选">
        <button v-for="item in filters" :key="item.value" type="button" :class="'mc-seg__item' + (filter === item.value ? ' is-active' : '')" :aria-pressed="filter === item.value ? 'true' : 'false'" @click="filter = item.value">{{ item.label }} {{ item.count }}</button>
      </div>
      <span class="mc-summary__time">最后刷新 {{ lastRefresh || '—' }}</span>
    </div>

    <div v-if="error" class="mc-load-error">
      <span>读取 server 状态失败：{{ error }}</span>
      <button class="mc-link" type="button" @click="load">重试</button>
    </div>

    <p v-if="loading && !state" class="mc-loading">正在读取 server 状态…</p><section v-else-if="filteredServers.length > 0" class="mc-list" aria-label="MCP server 列表">
      <section v-for="server in filteredServers" :key="server.id" class="mc-card" :data-status="server.status || 'degraded'">
        <div class="mc-card__head">
          <div class="mc-card__title">
            <span class="mc-dot" :data-status="server.status || 'degraded'"></span>
            <code class="mc-card__name">{{ server.id }}</code>
            <span class="mc-chip">{{ server.transport || 'http' }}</span>
            <span :class="'mc-chip' + (server.status === 'ready' ? ' mc-chip--ok' : server.status === 'degraded' ? ' mc-chip--danger' : '')">{{ statusLabel(server.status) }}</span>
          </div>
          <div class="mc-card__right">
            <span v-if="toolCount(server) > 0" class="mc-card__count">{{ server.tools || toolCount(server) }} 个工具</span>
            <button class="mc-switch" type="button" role="switch" :aria-checked="server.enabled !== false ? 'true' : 'false'" :aria-label="(server.enabled !== false ? '停用 ' : '启用 ') + server.id" @click="toggleServer(server, server.enabled === false)"><span class="mc-switch__knob"></span></button>
          </div>
        </div>
        <div class="mc-card__cmd">
          <code>{{ server.url || '' }}</code>
          <button class="mc-link" type="button" @click="copyText(server.url || '', '命令')">复制</button>
        </div>
        <div v-if="server.error" class="mc-error">
          <span>启动失败：{{ server.error }}</span>
          <button class="mc-link mc-link--danger" type="button" @click="probe()">重新探测</button>
        </div>
        <div class="mc-card__meta">
          来源 <code>{{ server.source || configPath }}</code>
          <span v-if="server.toolPrefix"> · 前缀 <code>{{ server.toolPrefix }}</code></span>
        </div>
        <div v-if="toolCount(server) > 0" class="mc-tools">
          <div class="mc-tools__head">
            <span class="mc-tools__title">工具 <span class="mc-count">{{ toolCount(server) }}</span></span>
            <button class="mc-link" type="button" @click="toggleTools(server)">{{ isExpanded(server) ? '收起' : '展开全部 ' + toolCount(server) + ' 个' }}</button>
          </div>
          <div class="mc-tools__grid mc-tools__grid--head">
            <span>工具名</span><span>访问</span><span>备注</span>
          </div>
          <div v-for="(item, ti) in visibleTools(server)" :key="server.id + ':' + item.name" class="mc-tools__grid">
            <code class="mc-tools__name">{{ item.name }}</code>
            <span>
              <span :class="'mc-chip ' + (item.access === 'read' ? 'mc-chip--ok' : 'mc-chip--write')">{{ accessLabel(item.access) }}</span>
            </span>
            <span class="mc-tools__note">{{ item.schemaSimplified ? 'Schema 已简化' : (item.mcpName && item.mcpName !== item.name ? 'MCP ' + item.mcpName : '—') }}</span>
          </div>
        </div>
      </section>
    </section><div v-else-if="filter !== 'all'" class="mc-empty">
      <p class="mc-empty__title">这个状态下没有 server</p>
      <button class="mc-btn" type="button" @click="filter = 'all'">查看全部</button>
    </div><div v-else class="mc-empty">
      <p class="mc-empty__title">还没有 MCP server</p>
      <p class="mc-empty__desc">在 {{ configPath }} 里添加 server，保存后重载插件即可接入。</p>
      <button class="mc-btn" type="button" @click="openModal">编辑配置</button>
    </div>

    <div v-if="modalOpen" class="mc-modal" role="dialog" aria-modal="true" aria-label="编辑配置">
      <div class="mc-modal__backdrop" @click="closeModal"></div>
      <div class="mc-modal__panel">
        <div class="mc-modal__head">
          <div class="mc-modal__title">
            编辑配置
            <code class="mc-modal__tag">{{ configPath }}</code>
          </div>
          <button class="mc-modal__close" type="button" aria-label="关闭" @click="closeModal">×</button>
        </div>
        <div class="mc-modal__body">
          <p class="mc-modal__note">用户级 {{ userConfigPath }} 只读；同名 server 由工作区配置覆盖。</p>
          <div class="mc-editor">
            <pre class="mc-editor__hl" data-mcp-hl aria-hidden="true"><code><span v-for="(token, hi) in tokens" :key="hi" :class="token.cls">{{ token.text }}</span>{{ '\n' }}</code></pre>
            <textarea class="mc-editor__input" v-model="configText" rows="13" aria-label="MCP 配置文件内容" @scroll="syncScroll($event)"></textarea>
          </div>
          <div class="mc-editor__status" :data-tone="validation.ok ? 'ok' : 'error'" role="status" aria-live="polite">
            <span class="mc-editor__state">
              <span class="mc-status-dot"></span>
              {{ validation.ok ? 'JSON 有效 · ' + validation.servers + ' 个 server' : 'JSON 无效：' + validation.message }}
            </span>
            <span>共 {{ validation.lines }} 行</span>
          </div>
        </div>
        <div class="mc-modal__foot">
          <p class="mc-modal__hint">密钥用 <code>${secret:mcp_token}</code> 引用，不要写明文</p>
          <div class="mc-modal__actions">
            <button class="mc-btn" type="button" @click="loadConfig">重新读取</button>
            <button class="mc-btn" type="button" @click="format">格式化</button>
            <button class="mc-btn mc-btn--primary" type="button" :disabled="!validation.ok || saving" @click="save">{{ saving ? '保存中…' : '保存' }}</button>
          </div>
        </div>
      </div>
    </div>

    <div v-if="toast" class="mc-toast" :data-tone="toast.tone" role="status" aria-live="polite">{{ toast.message }}</div>
  </div>
</template>
<script src="./McpServers.js"></script>
