<template>
  <div class="ada-sidebar" id="chat-sidebar" aria-label="侧栏" :class="sidebarClass">
    <div class="ada-side c-side">
      <button class="c-new" type="button" data-new-task-link title="新建会话（Ctrl N）" @click="go('/threads')">
        <ui-icon name="i-plus" :size="16" />
        <span>新会话</span>
        <span class="c-new__kbd">Ctrl N</span>
      </button>

      <div class="c-list-head">
        <span class="c-list-head__title">会话列表</span>
        <span class="c-sp"></span>
        <button class="c-mini" type="button" title="搜索会话（Ctrl K）" aria-label="搜索会话" @click="focusSearch"><ui-icon name="i-search" :size="14" /></button>
        <button class="c-mini" type="button" :class="onlyRunning ? 'is-on' : ''" :aria-pressed="onlyRunning ? 'true' : 'false'" title="只看运行中的会话" aria-label="只看运行中的会话" @click="toggleOnlyRunning"><ui-icon name="i-list" :size="14" /></button>
      </div>

      <label class="c-search">
        <ui-icon name="i-search" :size="13" />
        <input id="chat-thread-search" class="c-search__input" type="search" v-model="needle" placeholder="搜索会话…" aria-label="搜索会话">
        <code class="c-search__kbd">Ctrl K</code>
      </label>

      <!-- 工作区列表：当前工作区默认展开，其余分组首次展开时懒加载会话。 -->
      <div class="c-thread-list" id="thread-list">
        <p v-if="failed" class="c-list-empty">{{ failed }}</p>
        <p v-else-if="groups.length === 0" class="c-list-empty">还没有会话</p>
        <div v-else class="ada-ws-list" data-ws-list>
          <div
            v-for="g in groups"
            :key="g.path"
            class="c-group"
            :class="g.current ? 'is-current' : ''"
            :data-ws-key="g.path"
            :data-ws-path="g.path"
            :data-ws-index="String(g.index)"
            :data-ws-open="g.open ? '1' : '0'"
          >
            <button class="c-group-head ada-ws__summary" type="button" :aria-expanded="g.open ? 'true' : 'false'" :title="g.path" @click="toggleGroup(g)">
              <ui-icon :name="g.open ? 'i-chevron-down' : 'i-chevron'" :size="12" />
              <ui-icon name="i-folder" :size="13" />
              <span class="c-group-head__name">{{ g.name }}</span>
              <span v-if="groupRunning(g)" class="c-thread__dot" title="该工作区有会话正在运行"></span>
              <span class="c-group-head__count">{{ g.threads.length }}</span>
            </button>
            <div v-if="g.open" class="c-group__threads">
              <p v-if="!g.loaded" class="c-list-empty">正在加载会话…</p>
              <p v-else-if="hitThreads(g).length === 0" class="c-list-empty">{{ needle.trim() !== '' ? '没有匹配的会话' : '这个工作区还没有会话' }}</p>
              <div
                v-for="t in hitThreads(g)"
                :key="g.path + ':' + t.id"
                class="c-thread"
                :class="t.id === activeThreadID ? 'is-active' : ''"
                :data-thread-row="t.id"
                :data-thread-running="t.running ? '1' : '0'"
              >
                <ui-icon name="i-chat" :size="14" />
                <span class="c-thread__title" :title="t.title || t.id">{{ t.title || "新会话" }}</span>
                <span v-if="t.running" class="c-thread__dot" title="运行中" aria-label="运行中"></span>
                <span v-else-if="timeLabel(t)" class="c-thread__time" :title="'最后活动：' + timeLabel(t)">{{ timeLabel(t) }}</span>
                <div class="c-thread__tools">
                  <button class="c-thread__tool" type="button" title="更多操作" aria-label="更多操作" @click.stop="openMenu(t, $event)"><ui-icon name="i-more" :size="14" /></button>
                  <button v-if="!t.running" class="c-thread__tool" type="button" title="删除会话" aria-label="删除会话" @click.stop="removeThread(t)"><ui-icon name="i-trash" :size="14" /></button>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="c-side-bottom">
        <button class="c-side-row" type="button" title="打开设置（供应商 · 主题 · 插件）" @click="openSettings">
          <ui-icon name="i-wrench" :size="14" />
          <span>设置</span>
          <span class="c-side-row__hint">供应商 · 主题 · 插件</span>
        </button>
        <nav class="c-nav" aria-label="页面导航">
          <a class="c-nav__item" :class="isActive('/') ? 'is-active' : ''" href="/" :aria-current="isActive('/') ? 'page' : ''" @click.prevent="go('/')"><ui-icon name="i-chart" :size="13" /><span>概览</span></a>
          <a class="c-nav__item" :class="isActive('/extensions') ? 'is-active' : ''" href="/extensions" :aria-current="isActive('/extensions') ? 'page' : ''" @click.prevent="go('/extensions')"><ui-icon name="i-plug" :size="13" /><span>插件</span></a>
          <a class="c-nav__item" :class="isActive('/workspaces') ? 'is-active' : ''" href="/workspaces" :aria-current="isActive('/workspaces') ? 'page' : ''" @click.prevent="go('/workspaces')"><ui-icon name="i-grid" :size="13" /><span>工作区</span></a>
        </nav>
        <div class="c-side-status">
          <ui-icon name="i-shield" :size="12" />
          <span>本地运行 · 无需账号 · 数据仅存本机</span>
        </div>
      </div>
    </div>

    <div class="ada-sidebar__resizer" data-sidebar-resizer role="separator" aria-orientation="vertical" aria-label="调整侧栏宽度" aria-valuemin="200" aria-valuemax="420" aria-valuenow="264" title="拖动调整侧栏宽度（双击恢复默认）" tabindex="0"></div>

    <div class="ada-rail">
      <a class="ada-rail__mark" href="/" title="返回概览" aria-label="返回概览" @click.prevent="go('/')"><span class="c-logo" role="img" aria-label="logo">{{ brandInitial }}</span></a>
      <a class="ada-rail__btn ada-rail__btn--primary" href="/threads" data-new-task-link title="新建会话" aria-label="新建会话" @click.prevent="go('/threads')"><ui-icon name="i-plus" :size="14" /></a>
      <button class="ada-rail__btn" type="button" data-rail-search title="搜索会话（Ctrl K）" aria-label="搜索会话" @click="focusSearch"><ui-icon name="i-search" :size="18" /></button>
      <span class="ada-rail__sep"></span>
      <a v-for="t in railThreads" :key="t.id" class="ada-rail__thumb" :class="t.id === activeThreadID ? 'is-active' : ''" :href="threadURL(t.id)" :title="t.title || t.id" @click.prevent="openThread(t.id)">{{ railInitial(t) }}</a>
      <span class="ada-rail__spacer"></span>
      <button class="ada-rail__btn" type="button" title="设置" aria-label="设置" @click="openSettings"><ui-icon name="i-wrench" :size="18" /></button>
      <a class="ada-rail__btn" :class="isActive('/') ? 'is-active' : ''" href="/" title="概览" @click.prevent="go('/')"><ui-icon name="i-chart" :size="18" /></a>
      <button class="ada-rail__btn" type="button" data-theme-toggle title="切换深浅色" aria-label="切换深浅色"><ui-icon name="i-moon" :size="14" /></button>
    </div>
  </div>
</template>
<script src="./Sidebar.js"></script>

