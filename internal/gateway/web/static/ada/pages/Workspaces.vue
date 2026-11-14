<template>
  <div class="ada-scroll">
    <div class="ada-wrap ada-wrap--page">
      <div class="ada-section-head">
        <div>
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="ada-page-title">打开工作区</h2>
            <span class="ada-chip">{{ rootCount }} 个位置</span>
          </div>
          <p class="ada-page-sub">工作区与各自的会话已经直接列在左侧；这里用来从磁盘打开新的工作区目录。切换只需点侧栏里的工作区组。</p>
        </div>
      </div>

      <div v-if="failed" class="live-status" role="alert" data-level="error">{{ failed }}</div>

      <div v-if="current">
        <div class="ada-picker-current">
          <div class="min-w-0">
            <p class="ada-eyebrow">当前浏览目录</p>
            <p class="ada-picker-current__path" :title="current">{{ current }}</p>
          </div>
          <button v-if="selectable" class="ada-btn ada-btn--primary ada-btn--sm" type="button" :disabled="busy" @click="useCurrent">使用此目录</button>
          <span v-else class="ada-chip" title="Gateway 的工作区在启动时固定">非配置工作区</span>
        </div>
      </div>

      <div class="ada-picker">
        <div class="ada-panel ada-picker__side">
          <div class="ada-picker__section">
            <p class="ada-eyebrow">磁盘与常用位置</p>
            <div v-if="roots.length > 0" class="ada-host">
              <a v-for="r in roots" :key="r.path" class="ada-picker__root" :class="'ada-picker__root--' + (r.kind || 'disk') + (r.path === current ? ' is-active' : '')" href="#" @click.prevent="browse(r.path)" :title="r.path">
                <ui-icon name="i-folder" :size="13" />
                <span class="ada-picker__root-label">{{ r.label }}</span>
                <span class="ada-picker__root-path">{{ r.path }}</span>
              </a>
            </div>
            <p v-else class="ada-picker__empty">没有可用的根目录</p>
          </div>

          <div class="ada-picker__section">
            <p class="ada-eyebrow">最近工作区</p>
            <div v-if="recent.length > 0" class="ada-host">
              <button v-for="w in recent" :key="w.path" class="ada-picker__recent" :class="w.path === bootstrapWorkspace ? 'is-active' : ''" type="button" :title="w.path" @click="switchTo(w.path)">
                <span class="ada-picker__recent-name">{{ w.name }}</span>
                <span class="ada-picker__recent-meta">{{ w.meta }}</span>
              </button>
            </div>
            <p v-else class="ada-picker__empty">还没有带会话的工作区</p>
          </div>
        </div>

        <section class="ada-panel ada-picker__main">
          <div class="ada-picker__crumbs">
            <button v-if="parentPath" class="ada-btn ada-btn--ghost ada-btn--sm" type="button" @click="browse(parentPath)">上一级</button>
            <span class="ada-picker__path">{{ current }}</span>
          </div>

          <form class="ada-picker__mkdir" @submit.prevent="mkdir">
            <input class="ada-input" name="name" v-model="newDirName" placeholder="新建文件夹名称" aria-label="新建文件夹名称" required>
            <button class="ada-btn ada-btn--secondary ada-btn--sm" type="submit" :disabled="busy">新建目录</button>
          </form>

          <p v-if="truncated" class="ada-picker__notice">目录项过多，已省略 {{ omitted }} 项；可用上方路径直接进入更深的目录。</p>

          <div v-if="entries.length > 0" class="ada-picker__body">
            <ul class="ada-picker__list">
              <li v-for="e in entries" :key="e.path">
                <a class="ada-picker__entry" href="#" @click.prevent="browse(e.path)" :title="e.path">
                  <ui-icon name="i-folder" :size="14" />
                  <span>{{ e.name }}</span>
                </a>
              </li>
            </ul>
          </div>
          <div v-else class="ada-empty-card">
            <p class="text-sm font-medium text-ink">这个目录下没有子目录</p>
            <p class="mt-1 text-xs leading-5 text-faint">可以直接选择当前工作区，或在上面新建一个文件夹。</p>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>
<script src="./Workspaces.js"></script>
