<template>
  <div class="ada-scroll">
    <div class="ada-wrap ada-wrap--page">
      <div class="ada-section-head">
        <div>
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="ada-page-title">会话</h2>
            <span class="ada-chip">{{ totalCount }} 个会话</span>
          </div>
          <p class="ada-page-sub">按工作区分组；运行中的会话带状态点。点击任意会话继续对话。</p>
        </div>
        <div class="ada-section-head__actions">
          <a class="ada-btn ada-btn--primary ada-btn--sm" href="/threads" @click.prevent="goNewTask"><ui-icon name="i-plus" :size="14" /><span>新建会话</span></a>
        </div>
      </div>

      <div v-if="failed" class="ada-empty">
        <span class="ada-empty__mark"><ui-icon name="i-alert" :size="20" /></span>
        <h2 class="ada-empty__title">加载失败</h2>
        <p class="ada-empty__desc">{{ failed }}</p>
        <div class="ada-empty__action"><button class="ada-btn ada-btn--primary ada-btn--md" type="button" @click="load">重试</button></div>
      </div>

      <div v-else class="ex-list">
        <div class="ex-row ex-row--head">
          <span>会话</span><span>工作区</span><span>状态</span><span>最后活动</span>
        </div>
        <div v-for="g in groups" :key="g.path" class="ada-host">
          <div class="ex-row ex-row--head ex-group-head">
            <span><ui-icon name="i-folder" :size="13" /> {{ g.name }}</span>
            <span :title="g.path">{{ g.path }}</span>
            <span>{{ g.loaded ? g.threads.length + " 个" : "未加载" }}</span>
            <span>
              <button v-if="!g.loaded" class="ex-action" type="button" @click="loadGroup(g)">加载会话</button>
            </span>
          </div>
          <div v-if="g.loaded" class="ada-host">
            <div v-if="g.threads.length === 0" class="ada-list-empty">这个工作区还没有会话</div>
            <div v-for="t in g.threads" :key="g.path + ':' + t.id" class="ada-thread" :data-thread-row="t.id" :data-thread-running="t.running ? '1' : '0'">
              <a class="ada-thread__link" :href="threadURL(t.id)" @click.prevent="openThread(t.id)" :title="t.title || t.id">
                <span class="ada-thread__title">{{ t.title || "新会话" }}<span v-if="t.running" class="ada-thread__dot" title="运行中" aria-label="运行中"></span></span>
                <span class="ada-thread__meta"><span class="ada-thread__time" :title="'最后活动：' + timeLabel(t)">{{ timeLabel(t) }}</span></span>
              </a>
              <div class="ada-thread__actions">
                <button class="ada-thread__action" type="button" title="更多操作" aria-label="更多操作" @click.stop="openMenu(t, $event)"><ui-icon name="i-more" :size="14" /></button>
              </div>
            </div>
          </div>
        </div>
        <div v-if="groups.length === 0 && !loading" class="ada-list-empty">还没有会话</div>
      </div>
    </div>
  </div>
</template>
<script src="./Threads.js"></script>
