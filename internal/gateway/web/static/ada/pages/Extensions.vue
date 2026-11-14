<template>
  <div class="ada-scroll">
  <div class="ada-wrap ada-wrap--page">
    <div class="ex-head">
      <div class="ex-head__title">
        <h2 class="ada-page-title">插件</h2>
        <span class="ex-count">{{ count }}</span>
        <span class="ex-tip" data-tip="列表合并了两侧来源的扩展；停用后不再贡献工具与页面，重载插件后生效。状态来自插件清单。" tabindex="0" role="img">!</span>
      </div>
      <button class="ex-btn ex-btn--primary" type="button" :disabled="reloading" @click="reloadPlugins"><ui-icon name="i-refresh" :size="13" />重载插件</button>
    </div>

    <div class="ex-island">
      <div class="ex-toolbar">
        <label class="ex-search">
          <input class="ex-search__input" type="text" v-model="needle" placeholder="搜索插件名 / ID" aria-label="搜索插件">
          <span class="ex-search__kbd">⌘K</span>
        </label>
        <div class="ex-seg" role="group" aria-label="按状态筛选">
          <button class="ex-seg__item" :class="filter === 'all' ? 'is-active' : ''" type="button" :aria-pressed="filter === 'all' ? 'true' : 'false'" @click="filter = 'all'">全部 {{ count }}</button>
          <button class="ex-seg__item" :class="filter === 'on' ? 'is-active' : ''" type="button" :aria-pressed="filter === 'on' ? 'true' : 'false'" @click="filter = 'on'">已启用 {{ enabledCount }}</button>
          <button class="ex-seg__item" :class="filter === 'off' ? 'is-active' : ''" type="button" :aria-pressed="filter === 'off' ? 'true' : 'false'" @click="filter = 'off'">未启用 {{ disabledCount }}</button>
        </div>
        <span class="ex-toolbar__meta">{{ count }} 个已安装</span>
      </div>

      <div v-if="failed" class="ada-empty">
        <span class="ada-empty__mark"><ui-icon name="i-alert" :size="20" /></span>
        <h2 class="ada-empty__title">加载失败</h2>
        <p class="ada-empty__desc">{{ failed }}</p>
        <div class="ada-empty__action"><button class="ada-btn ada-btn--primary ada-btn--md" type="button" @click="load">重试</button></div>
      </div>

      <div v-else-if="filtered.length > 0" class="ex-list">
        <div class="ex-row ex-row--head">
          <span>插件</span><span>版本</span><span>状态</span><span>能力</span><span>操作</span>
        </div>
        <div v-for="p in filtered" :key="p.id" class="ex-row" :data-enabled="String(p.enabled)">
          <div class="ex-row__main">
            <a class="ex-row__name" :href="detailURL(p.id)" @click.prevent="openDetail(p.id)">{{ p.name || p.id }}</a>
            <span class="ex-row__id">{{ p.id }}</span>
            <span v-if="p.error" class="ex-row__error">{{ p.error }}</span>
          </div>
          <span class="ex-row__version">{{ p.version ? "v" + p.version : "—" }}</span>
          <span class="ex-row__status" :data-tone="p.statusTone"><span class="ex-dot"></span>{{ p.statusLabel }}</span>
          <div class="ex-row__caps">
            <span v-if="p.tools" class="ex-chip">{{ p.tools }} 工具</span>
            <span v-if="capPages(p)" class="ex-chip">{{ capPages(p) }} 页面</span>
            <span v-if="capSlots(p)" class="ex-chip">{{ capSlots(p) }} 插槽</span>
            <span v-if="capDashboard(p)" class="ex-chip ex-chip--ok">dashboard</span>
            <span v-if="!p.tools && !capPages(p) && !capSlots(p) && !capDashboard(p)" class="ex-row__dash">—</span>
          </div>
          <div class="ex-row__actions">
            <button v-if="p.enabled" class="ex-action" type="button" :disabled="busy" @click="toggle(p)">停用</button>
            <button v-else class="ex-action ex-action--primary" type="button" :disabled="busy" @click="toggle(p)">启用</button>
            <a class="ex-link" :href="detailURL(p.id)" @click.prevent="openDetail(p.id)">详情</a>
          </div>
        </div>
      </div>

      <div v-else class="ex-empty">
        <p class="ex-empty__title">没有插件</p>
        <p class="ex-empty__desc">插件可能尚未装配，或网关暂时不可用。查看服务端日志确认。</p>
      </div>
    </div>
    <ui-slot name="extensions.list" />
    <div class="ex-foot">
      <span class="ex-foot__left"><span class="ex-tip ex-tip--sm" data-tip="启停会写回工作区配置；前端扩展在装配期固定，重载网关后新配置才会生效。" tabindex="0" role="img">!</span>停用后插件不再贡献工具与页面；重载插件后生效</span>
      <span class="ex-foot__right">状态来自插件清单</span>
    </div>
  </div>
  </div>
</template>
<script src="./Extensions.js"></script>
