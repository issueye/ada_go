<template>
  <div class="ada-sidebar" id="chat-sidebar" aria-label="侧栏" :class="sidebarClass">
    <div class="ada-side">
      <a href="/" class="ada-brand" aria-label="返回会话" title="返回会话" @click.prevent="goRoot">
        <span class="ada-brand__mark ada-brand__mark--logo"><span class="ada-brand__logo ada-logo-mask" :style="logoStyle" role="img" aria-label="logo"></span></span>
        <span class="ada-brand__name">{{ brandName }}</span>
        <span v-if="productBadge" class="ada-brand__badge">{{ productBadge }}</span>
      </a>
      <ui-slot name="sidebar.top" />

      <a class="ada-new ada-btn ada-btn--secondary ada-btn--sm" href="/threads" data-new-task-link title="新建任务（Ctrl N）" @click.prevent="go('/threads')">
        <ui-icon name="i-plus" :size="14" />
        <span>新建任务</span><code class="ada-new__kbd">Ctrl N</code>
      </a>

      <div class="ada-ws-head"><span class="ada-ws-head__label">工作区</span>
        <a class="ada-ws-head__add" href="/workspaces" data-open-workspace :data-current-workspace="workspace" title="打开工作区…" aria-label="打开工作区…" @click.prevent="go('/workspaces')"><ui-icon name="i-plus" :size="14" /></a>
      </div>

      <div class="ada-search">
        <ui-icon name="i-search" :size="14" />
        <input id="chat-thread-search" class="ada-search__input" type="search" v-model="needle" placeholder="搜索会话…" aria-label="搜索会话">
        <code class="ada-search__kbd">Ctrl K</code>
        <span class="ada-search__count" :hidden="!needle">{{ searchLabel }}</span>
      </div>

      <div class="ada-thread-list" :class="groups.length > 1 ? 'ada-thread-list--split' : ''" id="thread-list">
        <p v-if="groups.length === 0" class="ada-list-empty">还没有会话</p>
        <div v-for="(g, gi) in groups" :key="g.path" class="ada-host">
          <div v-if="gi === 0" class="ada-ws-scroll ada-ws-scroll--active" data-ws-active>            <div class="ada-ws" :data-ws-key="g.path" :data-ws-path="g.path" :data-ws-index="String(g.index)">
              <details class="ada-ws__details" :id="wsDOMID(g.path)" open>
                <summary class="ada-ws__summary" :title="g.path">
                  <ui-icon name="i-chevron" :size="12" /><ui-icon name="i-folder" :size="14" />
                  <span class="ada-ws__name">{{ g.name }}</span>
                  <span v-if="groupRunning(g)" class="ada-ws__running" title="该工作区有会话正在运行"><span class="ada-dot ada-dot--warn"></span></span>
                  <span class="ada-ws__count">{{ g.threads.length }}</span>
                </summary>
                <div class="ada-ws__threads" data-ws-threads>
                  <p v-if="g.threads.length === 0" class="ada-ws__empty">还没有会话</p>
                  <div v-for="t in visibleThreads(g)" :key="t.id" class="ada-thread" :class="t.id === activeThreadID ? 'is-active' : ''" :data-thread-row="t.id" :data-thread-running="t.running ? '1' : '0'" :hidden="!threadHit(t)">
                    <a class="ada-thread__link" :href="threadURL(t.id)" :title="t.title || t.id" @click.prevent="openThread(t.id)">
                      <span class="ada-thread__title">{{ t.title || "新会话" }}</span>
                      <span class="ada-thread__meta"><span v-if="t.running" class="ada-thread__dot" title="运行中" aria-label="运行中"></span><span v-if="timeLabel(t)" class="ada-thread__time" :title="'最后活动：' + timeLabel(t)">{{ timeLabel(t) }}</span></span>
                    </a>
                    <div class="ada-thread__actions">
                      <button v-if="!t.running" class="ada-thread__action" type="button" title="删除会话" aria-label="删除会话" @click="removeThread(t)"><ui-icon name="i-trash" :size="14" /></button>
                      <button class="ada-thread__action" type="button" title="更多操作" aria-label="更多操作" @click.stop="openMenu(t, $event)"><ui-icon name="i-more" :size="14" /></button>
                    </div>
                  </div>
                </div>
              </details>
              <div class="ada-ws__actions">
                <a class="ada-ws__action" href="/threads" title="在此工作区新建任务" aria-label="在此工作区新建任务" @click.prevent="go('/threads')"><ui-icon name="i-plus" :size="14" /></a>
              </div>
            </div>
          </div>
          <div v-else class="ada-ws-scroll ada-ws-scroll--others" data-ws-others>
            <p v-if="gi === 1" class="ada-ws__section" data-ws-section><span class="ada-ws__section-label">其它工作区</span><span class="ada-ws__section-count">{{ otherCount }}</span></p>
            <div class="ada-ws-scroll--workArea">
              <div class="ada-ws" :data-ws-key="g.path" :data-ws-path="g.path" :data-ws-index="String(g.index)">
                <details class="ada-ws__details" :id="wsDOMID(g.path)" @toggle="onToggle($event, g)">
                  <summary class="ada-ws__summary" :title="g.path">
                    <ui-icon name="i-chevron" :size="12" /><ui-icon name="i-folder" :size="14" />
                    <span class="ada-ws__name">{{ g.name }}</span>
                    <span v-if="groupRunning(g)" class="ada-ws__running" title="该工作区有会话正在运行"><span class="ada-dot ada-dot--warn"></span></span>
                    <span class="ada-ws__count" data-ws-count :hidden="!g.loaded">{{ g.threads.length }}</span>
                  </summary>
                  <div class="ada-ws__threads" data-ws-threads>
                    <p v-if="!g.loaded" class="ada-ws__empty" data-ws-loading>展开以加载会话…</p>
                    <p v-else-if="g.threads.length === 0" class="ada-ws__empty">还没有会话</p>
                    <div v-else class="ada-host">
                      <div v-for="t in g.threads" :key="t.id" class="ada-thread" :data-thread-row="t.id" :data-thread-running="t.running ? '1' : '0'">
                        <a class="ada-thread__link" :href="threadURL(t.id)" :title="t.title || t.id" @click.prevent="openThreadIn(t, g)">
                          <span class="ada-thread__title">{{ t.title || "新会话" }}</span>
                          <span class="ada-thread__meta"><span v-if="t.running" class="ada-thread__dot" title="运行中" aria-label="运行中"></span><span v-if="timeLabel(t)" class="ada-thread__time" :title="'最后活动：' + timeLabel(t)">{{ timeLabel(t) }}</span></span>
                        </a>
                        <div class="ada-thread__actions">
                          <button v-if="!t.running" class="ada-thread__action" type="button" title="删除会话" aria-label="删除会话" @click="removeThread(t, g)"><ui-icon name="i-trash" :size="14" /></button>
                          <button class="ada-thread__action" type="button" title="更多操作" aria-label="更多操作" @click.stop="openMenu(t, $event)"><ui-icon name="i-more" :size="14" /></button>
                        </div>
                      </div>
                    </div>
                  </div>
                </details>
                <div class="ada-ws__actions">
                  <button class="ada-ws__action" type="button" title="切换到此工作区" aria-label="切换到此工作区" @click="switchWorkspace(g.path)"><ui-icon name="i-check" :size="14" /></button>
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
              <ui-icon name="i-plug" :size="14" /><span class="ada-nav__text">{{ n.label }}</span>
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
          <button class="ada-icon-btn ada-theme" type="button" data-theme-panel-toggle aria-haspopup="true" aria-expanded="false" aria-controls="ada-theme-panel" title="主题设置" aria-label="主题设置">
            <span class="ada-theme__moon"><ui-icon name="i-moon" :size="14" /></span>
            <span class="ada-theme__sun"><ui-icon name="i-sun" :size="14" /></span>
          </button>
          <div class="ada-theme-pop" id="ada-theme-panel" data-theme-panel aria-label="主题设置" hidden>
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
      <a class="ada-rail__mark" href="/" title="返回会话" aria-label="返回会话" @click.prevent="go('/')"><span class="ada-rail__logo ada-logo-mask" :style="logoStyle" role="img" aria-label="logo"></span></a>
      <a class="ada-rail__btn ada-rail__btn--primary" href="/threads" data-new-task-link title="新建任务" aria-label="新建任务" @click.prevent="go('/threads')"><ui-icon name="i-plus" :size="14" /></a>
      <button class="ada-rail__btn" type="button" data-rail-search title="搜索会话（Ctrl K）" aria-label="搜索会话"><ui-icon name="i-search" :size="18" /></button>
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
/* 侧栏的少量 SPA 补充：logo 用 CSS mask（模板白名单没有 img），其余布局类
   全部来自 a_da 的 ui/shell.css。 */
.ada-logo-mask {
	display: inline-block;
	width: 28px;
	height: 28px;
	background-color: currentcolor;
	-webkit-mask-image: url("/static/logo.png");
	mask-image: url("/static/logo.png");
	-webkit-mask-size: contain;
	mask-size: contain;
	-webkit-mask-repeat: no-repeat;
	mask-repeat: no-repeat;
	-webkit-mask-position: center;
	mask-position: center;
}
.ada-brand__logo.ada-logo-mask { width: 24px; height: 24px; }
.ada-rail__logo.ada-logo-mask { width: 24px; height: 24px; }
/* .ada-swatch i 的等价物：色卡中段（brand 色）。 */
.ada-swatch__mid { flex: 1; background: var(--sw-brand, currentcolor); }
</style>
<script src="./Sidebar.js"></script>
