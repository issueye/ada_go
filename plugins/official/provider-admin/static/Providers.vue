<template>
  <section class="pm-page">
    <div class="pm-head">
      <div class="pm-head__text">
        <h1 class="pm-title">模型供应商<span class="pm-tip pm-tip--below" data-tip="管理自定义模型供应商，配置后可在聊天时选择使用。" aria-label="管理自定义模型供应商，配置后可在聊天时选择使用。" tabindex="0" role="img">!</span></h1>
      </div>
      <div class="pm-head__actions">
        <span class="pm-count">{{ providers.length }} 个供应商</span>
        <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="load">刷新</button>
        <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" @click="openAddProvider">添加供应商</button>
      </div>
    </div>

    <div class="pm-layout">
      <div class="pm-list" aria-label="供应商列表">
        <div class="pm-list__head">
          <div class="pm-list__title">
            <span>供应商</span>
            <span class="pm-list__num">{{ providers.length }}</span>
          </div>
          <label class="pm-search">
            <input type="search" v-model="search" placeholder="搜索名称或协议" aria-label="搜索供应商" />
          </label>
          <div class="pm-filters" role="group" aria-label="按状态筛选">
            <button type="button" :class="'pm-filter' + (filter === 'all' ? ' is-active' : '')" :aria-pressed="filter === 'all' ? 'true' : 'false'" @click="filter = 'all'">全部 {{ providers.length }}</button>
            <button type="button" :class="'pm-filter' + (filter === 'enabled' ? ' is-active' : '')" :aria-pressed="filter === 'enabled' ? 'true' : 'false'" @click="filter = 'enabled'">已启用 {{ enabledCount }}</button>
            <button type="button" :class="'pm-filter' + (filter === 'disabled' ? ' is-active' : '')" :aria-pressed="filter === 'disabled' ? 'true' : 'false'" @click="filter = 'disabled'">已停用 {{ providers.length - enabledCount }}</button>
          </div>
        </div>

        <div class="pm-list__scroll">
          <p v-if="error" class="pm-error" role="alert">{{ error }}</p><p v-else-if="loading" class="pm-empty">正在读取供应商…</p><button v-else-if="filtered.length > 0" v-for="p in filtered" :key="p.id" type="button" :class="'pm-item' + (selected && p.id === selected.id ? ' is-active' : '') + (p.disabled ? ' is-off' : '')" :aria-pressed="selected && p.id === selected.id ? 'true' : 'false'" @click="select(p.id)">
            <span :class="'pm-avatar' + avatarTone(selectedIndex) + ' pm-avatar--sm'">{{ initialOf(p) }}</span>
            <span class="pm-item__body">
              <span class="pm-item__name">{{ p.displayName || p.id }}</span>
              <span class="pm-item__meta">{{ protocolLabel(p.protocol) }}{{ modelCount(p) > 0 ? ' · ' + modelCount(p) + ' 个模型' : '' }}</span>
            </span>
            <span :class="'pm-dot' + (p.disabled ? ' is-off' : '')" aria-hidden="true"></span>
          </button><p v-else-if="providers.length > 0" class="pm-empty">没有匹配的供应商。</p><p v-else class="pm-empty">还没有供应商。</p>
        </div>

        <button class="pm-list__add" type="button" @click="openAddProvider">
          <span class="pm-list__plus" aria-hidden="true">+</span>
          添加供应商
        </button>
      </div>

      <section class="pm-detail" aria-label="供应商详情">
        <div v-if="selected" class="pm-detail__col">
          <div class="pm-detail__head">
            <span :class="'pm-avatar' + avatarTone(selectedIndex) + ' pm-avatar--md'">{{ initialOf(selected) }}</span>
            <div class="pm-detail__id">
              <div class="pm-detail__title-row">
                <h2 class="pm-detail__name">{{ selected.displayName || selected.id }}</h2>
                <span :class="'badge ' + (selected.disabled ? 'badge-neutral' : 'badge-success')">{{ selected.disabled ? '已停用' : '已启用' }}</span>
                <span :class="'badge ' + (selected.hasApiKey ? 'badge-neutral' : 'badge-warn')">{{ selected.hasApiKey ? '密钥已保存' : '未配置密钥' }}</span>
              </div>
              <p class="pm-detail__meta">{{ protocolLabel(selected.protocol) }}{{ modelStats.total > 0 ? ' · ' + modelStats.total + ' 个模型' : ' · 暂无模型' }}</p>
            </div>
            <div class="pm-detail__actions">
              <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="openEditProvider">编辑配置</button>
              <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" :disabled="fetching" @click="openFetchModels">{{ fetching ? '拉取中…' : '拉取模型' }}</button>
            </div>
          </div>

          <div class="ada-tabs__list pm-tabs" role="tablist">
            <button class="ada-tabs__tab" type="button" role="tab" :aria-selected="tab === 'info' ? 'true' : 'false'" :tabindex="tab === 'info' ? 0 : -1" @click="tab = 'info'">供应商信息</button>
            <button class="ada-tabs__tab" type="button" role="tab" :aria-selected="tab === 'models' ? 'true' : 'false'" :tabindex="tab === 'models' ? 0 : -1" @click="tab = 'models'">
              模型列表
              <span v-if="modelStats.total > 0" class="pm-tab__num">{{ modelStats.total }}</span>
            </button>
          </div>

          <div v-if="tab === 'info'" class="pm-panel">
            <p class="pm-section">基本信息</p>
            <div class="pm-defs">
              <div class="pm-def">
                <span class="pm-def__label">供应商 ID</span>
                <span class="pm-def__value"><code>{{ selected.id }}</code><button class="pm-copy" type="button" :aria-label="'复制供应商 ID'" :title="'复制供应商 ID'" @click="copyText(selected.id, '供应商 ID')">复制</button></span>
              </div>
              <div class="pm-def">
                <span class="pm-def__label">协议</span>
                <span class="pm-def__value"><code>{{ selected.protocol || '—' }}</code></span>
              </div>
              <div class="pm-def">
                <span class="pm-def__label">端点</span>
                <span class="pm-def__value"><code>{{ selected.baseUrl || '—' }}</code><button v-if="selected.baseUrl" class="pm-copy" type="button" aria-label="复制端点" title="复制端点" @click="copyText(selected.baseUrl, '端点')">复制</button></span>
              </div>
              <div class="pm-def">
                <span class="pm-def__label">代理</span>
                <span class="pm-def__value"><code>{{ selected.proxy || '—' }}</code></span>
              </div>
              <div class="pm-def">
                <span class="pm-def__label">请求头</span>
                <span class="pm-def__value">{{ headerCount(selected.headers) > 0 ? headerCount(selected.headers) + ' 条' : '—' }}</span>
              </div>
              <div class="pm-def">
                <span class="pm-def__label">密钥</span>
                <span class="pm-def__value">{{ selected.hasApiKey ? '尾号 ' + (selected.apiKeyHint || '••••') : '未配置' }}<span v-if="!selected.hasApiKey" class="pm-def__warn">需配置后才能拉取模型</span></span>
              </div>
            </div>

            <p class="pm-section">概览</p>
            <div class="pm-stats">
              <div class="pm-stat">
                <span class="pm-stat__value">{{ modelStats.total }}</span>
                <span class="pm-stat__label">全部模型</span>
              </div>
              <div class="pm-stat">
                <span class="pm-stat__value">{{ modelStats.enabled }}</span>
                <span class="pm-stat__label">可用模型</span>
              </div>
              <div class="pm-stat">
                <span class="pm-stat__value">{{ modelStats.tools }}</span>
                <span class="pm-stat__label">支持工具</span>
              </div>
            </div>

            <p class="pm-section">模型速览</p>
            <div v-if="selectedModels.length > 0" class="pm-chips">
              <span v-for="m in selectedModels.slice(0, 4)" :key="m.id" :class="'pm-chip' + (m.disabled ? ' is-off' : '')">
                <span class="pm-chip__name">{{ m.id }}</span>
                <span class="pm-chip__ctx">{{ formatContext(m.contextWindow) }}</span>
              </span>
              <button class="pm-chip pm-chip--more" type="button" @click="tab = 'models'">{{ selectedModels.length > 4 ? '还有 ' + (selectedModels.length - 4) + ' 个' : '查看全部' }}</button>
            </div><p v-else class="pm-hint">还没有模型，点右上角「拉取模型」从服务商同步，或到「模型列表」手动添加。</p>
          </div><div v-else class="pm-panel">
            <div class="pm-models__head">
              <p class="pm-section pm-section--flush">模型列表</p>
              <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="openAddModel">添加模型</button>
            </div>
            <div v-if="selectedModels.length > 0" class="pm-models">
              <div v-for="m in selectedModels" :key="m.id" :class="'pm-mrow' + (currentModelId === m.id ? ' is-current' : '') + (m.disabled ? ' is-disabled' : '')">
                <div class="pm-mrow__main">
                  <span class="pm-mrow__id">{{ m.id }}</span>
                  <span v-if="m.displayName && m.displayName !== m.id" class="pm-mrow__name">{{ m.displayName }}</span>
                  <span class="pm-mrow__tags">
                    <span class="pm-tag">{{ formatContext(m.contextWindow) }}</span>
                    <span v-for="mode in m.input" :key="mode" class="pm-tag">{{ mode }}</span>
                    <span v-if="m.supportsTools" class="pm-tag">工具</span>
                    <span v-if="m.thinkingLevels && m.thinkingLevels.length > 0" class="pm-tag pm-tag--brand" :title="'推理档位：' + m.thinkingLevels.join(' / ')">推理 {{ m.thinkingLevels.join('/') }}</span>
                    <span v-if="m.disabled" class="pm-tag pm-tag--off">已停用</span>
                  </span>
                </div>
                <div class="pm-mrow__actions">
                  <span v-if="currentModelId === m.id" class="badge badge-success">当前</span><button v-else class="ada-btn ada-btn--secondary ada-btn--sm" type="button" :disabled="m.disabled === true" @click="selectModel(m.id)">设为当前</button>
                  <button class="ada-btn ada-btn--ghost ada-btn--sm" type="button" @click="openEditModel(m)">编辑</button>
                  <button class="ada-btn ada-btn--danger ada-btn--sm" type="button" @click="removeModelById(m.id)">删除</button>
                </div>
              </div>
            </div><p v-else class="pm-empty">还没有模型 — 点「拉取模型」从服务商同步，或手动添加一个。</p>
          </div>

          <div class="pm-danger">
            <span class="pm-danger__label">
              危险操作
              <span class="pm-tip" data-tip="停用后将从聊天列表中隐藏，配置与密钥保留；删除会同时移除其下所有模型。" aria-label="停用后将从聊天列表中隐藏，配置与密钥保留；删除会同时移除其下所有模型。" tabindex="0" role="img">!</span>
            </span>
            <span class="pm-danger__actions">
              <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="toggleProvider">{{ selected.disabled ? '启用此供应商' : '停用此供应商' }}</button>
              <button class="ada-btn ada-btn--danger ada-btn--sm" type="button" @click="removeProvider">删除</button>
            </span>
          </div>
        </div><div v-else class="pm-placeholder">
          <span class="pm-placeholder__title">选择一个供应商</span>
          <span class="pm-placeholder__hint">从左侧选择，或点击「添加供应商」创建。</span>
        </div>
      </section>
    </div>

    <div v-if="providerModalOpen" class="pm-modal" role="dialog" aria-modal="true" aria-labelledby="pm-provider-title">
      <div class="pm-modal__backdrop" @click="closeProviderModal"></div>
      <div class="pm-modal__panel">
        <div class="pm-modal__head">
          <h2 id="pm-provider-title" class="pm-modal__title">
            {{ providerModalMode === 'edit' ? '编辑供应商' : '添加供应商' }}
            <span class="pm-tip pm-tip--below" data-tip="保存后会整条替换供应商配置；API Key 留空表示保持原值。" aria-label="保存后会整条替换供应商配置；API Key 留空表示保持原值。" tabindex="0" role="img">!</span>
          </h2>
          <button class="pm-modal__close" type="button" aria-label="关闭" @click="closeProviderModal">×</button>
        </div>
        <div class="pm-modal__body">
          <label class="pm-field">
            <span class="pm-field__label">名称</span>
            <input class="pm-input" type="text" v-model="providerForm.name" placeholder="例如 unisound" />
          </label>
          <div class="pm-field">
            <span class="pm-field__label">协议</span>
            <div class="pm-seg" role="group" aria-label="协议">
              <button v-for="option in protocolOptions" :key="option.value" type="button" :class="'pm-seg__item' + (providerForm.protocol === option.value ? ' is-active' : '')" :aria-pressed="providerForm.protocol === option.value ? 'true' : 'false'" :title="option.value" @click="providerForm.protocol = option.value">{{ option.label }}</button>
            </div>
          </div>
          <label class="pm-field">
            <span class="pm-field__label">端点</span>
            <input class="pm-input" type="text" v-model="providerForm.baseUrl" placeholder="https://api.example.com/v1" />
          </label>
          <div class="pm-field">
            <span class="pm-field__label">API 密钥<span class="pm-tip" data-tip="密钥仅保存在本地配置中，不会随会话上传，也不会出现在浏览器请求里。" aria-label="密钥仅保存在本地配置中，不会随会话上传，也不会出现在浏览器请求里。" tabindex="0" role="img">!</span></span>
            <input class="pm-input" type="password" v-model="providerForm.apiKey" :placeholder="providerModalMode === 'edit' ? '留空保持原值' : '可后补；留空表示未配置'" />
          </div>
          <label class="pm-field">
            <span class="pm-field__label">代理</span>
            <input class="pm-input" type="text" v-model="providerForm.proxy" placeholder="http://127.0.0.1:7890（可选）" />
          </label>
          <label class="pm-field">
            <span class="pm-field__label">请求头（每行 Key: Value）</span>
            <textarea class="pm-input" rows="3" v-model="providerForm.headersText" placeholder="X-Api-Version: 2024-01-01"></textarea>
          </label>
        </div>
        <div class="pm-modal__foot">
          <span class="pm-modal__hint">保存后可在聊天中切换模型</span>
          <span class="pm-modal__actions">
            <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="closeProviderModal">取消</button>
            <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" @click="saveProviderForm">{{ providerModalMode === 'edit' ? '保存' : '创建供应商' }}</button>
          </span>
        </div>
      </div>
    </div>

    <div v-if="fetchOpen" class="pm-modal" role="dialog" aria-modal="true" aria-labelledby="pm-fetch-title">
      <div class="pm-modal__backdrop" @click="closeFetch"></div>
      <div class="pm-modal__panel pm-modal__panel--wide">
        <div class="pm-modal__head">
          <h2 id="pm-fetch-title" class="pm-modal__title">
            拉取模型
            <span class="pm-tip pm-tip--below" data-tip="从该端点读取可用模型清单；已经添加过的模型不会重复导入。" aria-label="从该端点读取可用模型清单；已经添加过的模型不会重复导入。" tabindex="0" role="img">!</span>
          </h2>
          <button class="pm-modal__close" type="button" aria-label="关闭" @click="closeFetch">×</button>
        </div>

        <div class="pm-fetch__ctx">
          <span class="pm-fetch__ctx-left">
            <span v-if="selected" :class="'pm-avatar' + avatarTone(selectedIndex) + ' pm-avatar--sm'">{{ initialOf(selected) }}</span>
            <span class="pm-fetch__ctx-text">
              <span class="pm-fetch__ctx-name">{{ selected ? (selected.displayName || selected.id) : '' }}</span>
              <span class="pm-fetch__ctx-url">{{ selected ? (selected.baseUrl || '未设置端点') : '' }}</span>
            </span>
          </span>
          <span class="pm-count">{{ fetching ? '读取中…' : fetchedModels.length + ' 个可用' }}</span>
        </div>

        <div class="pm-fetch__bar">
          <label class="pm-search pm-search--grow">
            <input type="search" v-model="fetchQuery" placeholder="搜索模型 ID" aria-label="搜索模型" />
          </label>
          <label class="pm-check">
            <input type="checkbox" :checked="allFetchSelected.all" :disabled="allFetchSelected.selectable === 0" @change="toggleSelectAll($event.target.checked)" />
            全选
          </label>
          <span class="pm-fetch__picked">{{ selectedFetchCount }} / {{ fetchedModels.length }}</span>
        </div>

        <p v-if="fetching" class="pm-fetch__state">正在拉取模型…</p><p v-else-if="fetchError" class="pm-fetch__state pm-fetch__state--error" role="alert">{{ fetchError }}</p><p v-else-if="fetchedModels.length === 0" class="pm-fetch__state">没有拉取到模型。请确认端点与密钥是否正确。</p><p v-else-if="fetchedVisible.length === 0" class="pm-fetch__state">没有匹配「{{ fetchQuery }}」的模型。</p><div v-else class="pm-fetch__list">
          <label v-for="m in fetchedVisible" :key="m.id" :class="'pm-fetch__row' + (existingModelIds[m.id] ? ' is-added' : '')">
            <input type="checkbox" :checked="selectedFetch[m.id] === true" :disabled="existingModelIds[m.id] === true" @change="toggleFetchModel(m.id, $event.target.checked)" />
            <span class="pm-fetch__id">{{ m.id }}</span>
            <span class="pm-fetch__meta">{{ m.displayName && m.displayName !== m.id ? m.displayName : '' }}</span>
            <span class="pm-fetch__tail">{{ existingModelIds[m.id] ? '已添加' : formatContext(m.contextWindow) }}</span>
          </label>
        </div>

        <div class="pm-modal__foot">
          <span class="pm-modal__hint">{{ selected ? '将追加到 ' + (selected.displayName || selected.id) + ' 的模型列表' : '' }}</span>
          <span class="pm-modal__actions">
            <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="closeFetch">取消</button>
            <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" :disabled="selectedFetchCount === 0" @click="addSelectedModels">导入 {{ selectedFetchCount }} 个模型</button>
          </span>
        </div>
      </div>
    </div>

    <div v-if="modelModalOpen" class="pm-modal" role="dialog" aria-modal="true" aria-labelledby="pm-model-title">
      <div class="pm-modal__backdrop" @click="closeModelModal"></div>
      <div class="pm-modal__panel pm-modal__panel--wide">
        <div class="pm-modal__head">
          <h2 id="pm-model-title" class="pm-modal__title">
            {{ modelModalMode === 'add' ? '添加模型' : '修改模型' }}
            <span v-if="selected" class="pm-modal__tag">{{ selected.displayName || selected.id }}</span>
          </h2>
          <button class="pm-modal__close" type="button" aria-label="关闭" @click="closeModelModal">×</button>
        </div>

        <div class="pm-modal__body">
          <label v-if="modelModalMode === 'add'" class="pm-field">
            <span class="pm-field__label">模型 ID</span>
            <input class="pm-input" type="text" v-model="modelForm.id" placeholder="例如 gpt-4.1" />
          </label><div v-else class="pm-field">
            <span class="pm-field__label">模型 ID<span class="pm-tip" data-tip="模型 ID 由服务商定义，改名会让已有会话找不到这个模型，因此只读。" aria-label="模型 ID 由服务商定义，改名会让已有会话找不到这个模型，因此只读。" tabindex="0" role="img">!</span></span>
            <div class="pm-input pm-input--readonly">
              <code>{{ modelForm.id }}</code>
              <span class="pm-input__flag">只读</span>
            </div>
          </div>

          <label class="pm-field">
            <span class="pm-field__label">显示名称</span>
            <input class="pm-input" type="text" v-model="modelForm.displayName" placeholder="留空则显示模型 ID" />
          </label>

          <div class="pm-form-row">
            <label class="pm-field">
              <span class="pm-field__label">上下文长度</span>
              <input class="pm-input" type="number" v-model="modelForm.contextWindow" placeholder="128000" />
            </label>
            <label class="pm-field">
              <span class="pm-field__label">最大输出</span>
              <input class="pm-input" type="number" v-model="modelForm.maxOutput" placeholder="8192" />
            </label>
          </div>

          <div class="pm-field">
            <span class="pm-field__label">
              能力
              <span class="pm-tip" data-tip="决定模型在聊天里能做什么：输入模态影响能否收图片/音频，工具调用影响能否使用插件与命令。" aria-label="决定模型在聊天里能做什么：输入模态影响能否收图片/音频，工具调用影响能否使用插件与命令。" tabindex="0" role="img">!</span>
            </span>
            <div class="pm-chips pm-chips--input">
              <label v-for="item in inputModes" :key="item.key" :class="'pm-chip-btn' + (modelForm.inputModes.indexOf(item.key) >= 0 ? ' is-active' : '')">
                <input type="checkbox" :checked="modelForm.inputModes.indexOf(item.key) >= 0" @change="toggleInputMode(item.key, $event.target.checked)" />
                {{ item.label }}
              </label>
              <button type="button" :class="'pm-chip-btn' + (modelForm.supportsTools ? ' is-active' : '')" :aria-pressed="modelForm.supportsTools ? 'true' : 'false'" @click="modelForm.supportsTools = !modelForm.supportsTools">工具调用</button>
            </div>
          </div>

          <div class="pm-field">
            <span class="pm-field__label">
              推理级别
              <span class="pm-tip" data-tip="每个模型支持的档位不同，所以这里是可增删的标签；深色为默认档，聊天时优先使用。" aria-label="每个模型支持的档位不同，所以这里是可增删的标签；深色为默认档，聊天时优先使用。" tabindex="0" role="img">!</span>
            </span>
            <div class="pm-levels">
              <span v-for="(level, li) in modelForm.thinkingLevels" :key="level" :class="'pm-level' + (li === 0 ? ' is-default' : '')">
                <span v-if="li === 0" class="pm-level__dot" aria-hidden="true"></span>
                <span class="pm-level__name">{{ level }}</span>
                <button v-if="li !== 0" type="button" class="pm-level__remove" :aria-label="'移除推理级别 ' + level" @click="removeLevel(li)">×</button>
              </span>
              <input v-if="levelInputOpen" class="pm-level__input" type="text" v-model="levelDraft" placeholder="low / high" @keydown="onLevelKeydown" /><button v-else type="button" class="pm-level__add" @click="openLevelInput">+ 添加级别</button>
            </div>
          </div>

          <div class="pm-switch-row">
            <span class="pm-switch-row__text">
              <span class="pm-switch-row__title">在聊天中启用</span>
              <span class="pm-switch-row__hint">停用后不在模型选择器中出现</span>
            </span>
            <label class="pm-switch">
              <input type="checkbox" :checked="!modelForm.disabled" aria-label="在聊天中启用" @change="modelForm.disabled = !$event.target.checked" />
              <span class="pm-switch__track"></span>
            </label>
          </div>
        </div>

        <div class="pm-modal__foot">
          <span class="pm-modal__hint">{{ modelModalMode === 'edit' ? '保存后立即生效，下次对话可选用' : '添加后可在聊天中直接选择' }}</span>
          <span class="pm-modal__actions">
            <button v-if="modelModalMode === 'edit'" class="ada-btn ada-btn--danger ada-btn--sm" type="button" @click="removeModel">删除</button>
            <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="closeModelModal">取消</button>
            <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" @click="saveModel">保存</button>
          </span>
        </div>
      </div>
    </div>

    <div v-if="toast" class="pm-toast" :data-tone="toast.tone" role="status" aria-live="polite">{{ toast.message }}</div>
  </section>
</template>
<script src="./Providers.js"></script>
