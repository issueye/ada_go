<template>
  <div class="ada-sidebar" id="chat-sidebar" aria-label="侧栏" :class="sidebarClass">
    <div class="ada-side">
      <a href="/" class="ada-brand" aria-label="返回会话" title="返回会话" @click.prevent="goRoot">
        <span class="ada-brand__mark ada-brand__mark--logo"><span class="ada-brand__logo ada-logo-bg" :style="logoStyle" role="img" aria-label="logo"></span></span>
        <span class="ada-brand__name">{{ brandName }}</span>
        <span v-if="productBadge" class="ada-brand__badge">{{ productBadge }}</span>
      </a>
      <ui-slot name="sidebar.top" />

      <a class="ada-new ada-btn ada-btn--secondary ada-btn--sm" href="/threads" data-new-task-link title="新建任务（Ctrl N）" @click.prevent="go('/threads')">
        <ui-icon name="i-plus" :size="14" />
        <span>新建任务</span><code class="ada-new__kbd">Ctrl N</code>
      </a>

      <div class="ada-ws-head">
        <span class="ada-ws-head__label">工作区</span>
        <button class="ada-ws-head__btn" type="button" title="搜索工作区与会话（Ctrl K）" aria-label="搜索工作区与会话" @click="focusSearch"><ui-icon name="i-search" :size="14" /></button>
        <button class="ada-ws-head__btn" type="button" :title="allOpen ? '全部收起' : '全部展开'" :aria-label="allOpen ? '全部收起' : '全部展开'" @click="toggleAll"><ui-icon :name="allOpen ? 'i-chevron-up' : 'i-chevron-down'" :size="14" /></button>
        <a class="ada-ws-head__btn" href="/workspaces" data-open-workspace :data-current-workspace="workspace" title="打开工作区…" aria-label="打开工作区…" @click.prevent="go('/workspaces')"><ui-icon name="i-folder" :size="14" /></a>
      </div>

      <div class="ada-search">
        <ui-icon name="i-search" :size="14" />
        <input id="chat-thread-search" class="ada-search__input" type="search" v-model="needle" placeholder="搜索会话…" aria-label="搜索会话">
        <code class="ada-search__kbd">Ctrl K</code>
        <span class="ada-search__count" :hidden="!needle">{{ searchLabel }}</span>
      </div>

      <!-- 工作区列表：所有工作区在同一个滚动区里逐组折叠展开（没有「其它工作区」
           分区、也没有上下两块各自滚动）。当前工作区默认展开且会话已加载；其余
           工作区默认折叠，首次展开时才拉会话。 -->
      <div class="ada-thread-list" id="thread-list">
        <p v-if="failed" class="ada-list-empty">{{ failed }}</p>
        <p v-else-if="groups.length === 0" class="ada-list-empty">还没有会话</p>
        <div v-else class="ada-ws-list" data-ws-list>
          <div
            v-for="g in groups"
            :key="g.path"
            class="ada-ws"
            :class="g.current ? 'is-current' : ''"
            :data-ws-key="g.path"
            :data-ws-path="g.path"
            :data-ws-index="String(g.index)"
            :data-ws-open="g.open ? '1' : '0'"
          >
            <button class="ada-ws__summary" type="button" :aria-expanded="g.open ? 'true' : 'false'" :title="g.path" @click="toggleGroup(g)">
              <ui-icon name="i-chevron" :size="12" :class="g.open ? 'ada-ws__chevron is-open' : 'ada-ws__chevron'" />
              <ui-icon name="i-folder" :size="14" />
              <span class="ada-ws__name">{{ g.name }}</span>
              <span v-if="groupRunning(g)" class="ada-ws__running" title="该工作区有会话正在运行"><span class="ada-dot ada-dot--warn"></span></span>
              <span class="ada-ws__count">{{ g.threads.length }}</span>
            </button>
            <div v-if="g.open" class="ada-ws__threads" data-ws-threads>
              <p v-if="!g.loaded" class="ada-ws__empty">正在加载会话…</p>
              <p v-else-if="g.threads.length === 0" class="ada-ws__empty">还没有会话</p>
              <p v-else-if="hitThreads(g).length === 0" class="ada-ws__empty">没有匹配的会话</p>
              <div
                v-for="t in hitThreads(g)"
                :key="g.path + ':' + t.id"
                class="ada-thread"
                :class="t.id === activeThreadID ? 'is-active' : ''"
                :data-thread-row="t.id"
                :data-thread-running="t.running ? '1' : '0'"
              >
                <a class="ada-thread__link" :href="threadURL(t.id)" :title="t.title || t.id" @click.prevent="openThreadIn(t, g)">
                  <span class="ada-thread__title">{{ t.title || "新会话" }}</span>
                  <span class="ada-thread__meta">
                    <span v-if="t.running" class="ada-thread__dot" title="运行中" aria-label="运行中"></span>
                    <span v-if="timeLabel(t)" class="ada-thread__time" :title="'最后活动：' + timeLabel(t)">{{ timeLabel(t) }}</span>
                  </span>
                </a>
                <div class="ada-thread__actions">
                  <button v-if="!t.running" class="ada-thread__action" type="button" title="删除会话" aria-label="删除会话" @click="removeThread(t)"><ui-icon name="i-trash" :size="14" /></button>
                  <button class="ada-thread__action" type="button" title="更多操作" aria-label="更多操作" @click.stop="openMenu(t, $event)"><ui-icon name="i-more" :size="14" /></button>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <nav class="ada-nav ada-nav--util" aria-label="系统">
        <div v-if="pluginNav.length > 0" class="ada-host">
          <div class="ada-nav__label"><span>插件页面</span></div>
          <div class="ada-nav__scroll" role="group" aria-label="插件页面" tabindex="0">
            <a v-for="n in pluginNav" :key="n.path" class="ada-nav__item" :class="isActive(n.path) ? 'is-active' : ''" :href="n.path" :title="n.label" :aria-current="isActive(n.path) ? 'page' : ''" @click.prevent="go(n.path)">
              <ui-icon :name="n.icon || 'i-plug'" :size="14" /><span class="ada-nav__text">{{ n.label }}</span>
            </a>
          </div>
        </div>
        <a class="ada-nav__item" :class="isActive('/extensions') ? 'is-active' : ''" href="/extensions" :aria-current="isActive('/extensions') ? 'page' : ''" @click.prevent="go('/extensions')"><ui-icon name="i-plug" :size="14" /><span class="ada-nav__text">插件</span></a>
        <a class="ada-nav__item" :class="isActive('/') ? 'is-active' : ''" href="/" :aria-current="isActive('/') ? 'page' : ''" @click.prevent="go('/')"><ui-icon name="i-chart" :size="14" /><span class="ada-nav__text">概览</span></a>
        <a class="ada-nav__item" :class="isActive('/threads') ? 'is-active' : ''" href="/threads" :aria-current="isActive('/threads') ? 'page' : ''" @click.prevent="go('/threads')"><ui-icon name="i-chat" :size="14" /><span class="ada-nav__text">会话</span></a>
        <a class="ada-nav__item" :class="isActive('/workspaces') ? 'is-active' : ''" href="/workspaces" :aria-current="isActive('/workspaces') ? 'page' : ''" @click.prevent="go('/workspaces')"><ui-icon name="i-grid" :size="14" /><span class="ada-nav__text">工作区</span></a>
      </nav>
      <ui-slot name="sidebar.bottom" />

      <div class="ada-sidebar__foot">
        <div class="ada-theme-wrap">
          <!-- 主题面板的展开态归组件所有（:hidden 绑定 themeOpen），不再由 themes.js
               直接改 DOM —— SPA 重渲染时静态 hidden 属性会把面板重新盖回去。 -->
          <button class="ada-icon-btn ada-theme" type="button" data-theme-panel-toggle aria-haspopup="true" :aria-expanded="themeOpen ? 'true' : 'false'" aria-controls="ada-theme-panel" title="主题设置" aria-label="主题设置" @click="toggleThemePanel">
            <span class="ada-theme__moon"><ui-icon name="i-moon" :size="14" /></span>
            <span class="ada-theme__sun"><ui-icon name="i-sun" :size="14" /></span>
          </button>
          <div class="ada-theme-pop" id="ada-theme-panel" data-theme-panel aria-label="主题设置" :hidden="!themeOpen">
            <div class="ada-theme-pop__group" role="radiogroup" aria-label="明暗模式">
              <button v-for="m in themeModes" :key="m.id" type="button" class="ada-theme-pop__item" :class="mode === m.id ? 'is-selected' : ''" role="radio" :aria-checked="mode === m.id ? 'true' : 'false'" :data-theme-mode="m.id" :title="m.label">
                <ui-icon :name="m.icon" :size="14" /><span>{{ m.label }}</span>
              </button>
            </div>
            <div class="ada-theme-pop__group" role="radiogroup" aria-label="主题色板">
              <button v-for="p in themePalettes" :key="p.id" type="button" class="ada-theme-pop__item" :class="palette === p.id ? 'is-selected' : ''" role="radio" :aria-checked="palette === p.id ? 'true' : 'false'" :data-theme-pick="p.id" :title="p.label">
                <span class="ada-swatch" :class="'ada-swatch--' + p.id" aria-hidden="true"><span class="ada-swatch__mid"></span></span><span>{{ p.label }}</span>
              </button>
            </div>
          </div>
        </div>
        <span class="ada-gateway" :title="gatewayTitle"><span class="ada-gateway__dot is-on"></span><span class="ada-gateway__addr">{{ gatewayAddr }}</span></span>
      </div>
    </div>

    <div class="ada-sidebar__resizer" data-sidebar-resizer role="separator" aria-orientation="vertical" aria-label="调整侧栏宽度" aria-valuemin="200" aria-valuemax="420" aria-valuenow="264" title="拖动调整侧栏宽度（双击恢复默认）" tabindex="0"></div>

    <div class="ada-rail">
      <a class="ada-rail__mark" href="/" title="返回会话" aria-label="返回会话" @click.prevent="go('/')"><span class="ada-rail__logo ada-logo-bg" :style="logoStyle" role="img" aria-label="logo"></span></a>
      <a class="ada-rail__btn ada-rail__btn--primary" href="/threads" data-new-task-link title="新建任务" aria-label="新建任务" @click.prevent="go('/threads')"><ui-icon name="i-plus" :size="14" /></a>
      <button class="ada-rail__btn" type="button" data-rail-search title="搜索会话（Ctrl K）" aria-label="搜索会话" @click="focusSearch"><ui-icon name="i-search" :size="18" /></button>
      <span class="ada-rail__sep"></span>
      <a v-for="t in railThreads" :key="t.id" class="ada-rail__thumb" :class="t.id === activeThreadID ? 'is-active' : ''" :href="threadURL(t.id)" :title="t.title || t.id" @click.prevent="openThread(t.id)">{{ railInitial(t) }}</a>
      <span class="ada-rail__spacer"></span>
      <a class="ada-rail__btn" :class="isActive('/extensions') ? 'is-active' : ''" href="/extensions" title="插件" @click.prevent="go('/extensions')"><ui-icon name="i-plug" :size="18" /></a>
      <a class="ada-rail__btn" :class="isActive('/') ? 'is-active' : ''" href="/" title="概览" @click.prevent="go('/')"><ui-icon name="i-chart" :size="18" /></a>
      <button class="ada-rail__btn" type="button" data-theme-toggle title="切换深浅色" aria-label="切换深浅色"><ui-icon name="i-moon" :size="14" /></button>
    </div>
  </div>
</template>
<style scoped>
/* 侧栏的少量 SPA 补充：logo 用 background-image（模板白名单没有 <img>），
   其余布局类全部来自 a_da 的 ui/shell.css。
   🔴 不能用 CSS mask：mask 取的是 PNG 的 alpha 通道，而 logo.png 的圆角方块
   内部完全不透明（alpha 恒 255），一比一去遮就成了一个纯色圆角方块
   （2026-11-12 走查：品牌位是一块空白圆角）。图本身自带暖米白底与圆角，
   直接铺图即可。 */
.ada-logo-bg {
	display: inline-block;
	background-color: transparent;
	background-repeat: no-repeat;
	background-position: center;
	background-size: contain;
	border-radius: var(--ada-radius-md);
}
.ada-brand__logo.ada-logo-bg { width: 24px; height: 24px; }
.ada-rail__logo.ada-logo-bg { width: 24px; height: 24px; }
/* .ada-swatch i 的等价物：色卡中段（brand 色）。 */
.ada-swatch__mid { flex: 1; background: var(--sw-brand, currentcolor); }
</style>
<script src="./Sidebar.js"></script>
