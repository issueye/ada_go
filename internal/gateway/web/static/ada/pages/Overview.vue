<template>
  <div class="ada-scroll">
    <div class="ada-wrap ada-wrap--page">
      <div class="ov-head">
        <div class="ov-head__title">
          <h2 class="ada-page-title">概览</h2>
          <span class="ov-tip" data-tip="agent 状态、最近会话与插件贡献；统计来自本地会话、工作区与插件清单，插件贡献来自 dashboard 插槽。" tabindex="0" role="img">!</span>
        </div>
        <div class="ov-actions">
          <a class="ov-btn" href="/threads" @click.prevent="goThreads">新建任务</a>
          <a class="ov-btn ov-btn--primary" href="/threads" @click.prevent="goThreads">去对话</a>
        </div>
      </div>

      <div class="ov-grid">
        <div class="ov-island">
          <div v-if="failed" class="ada-empty">
            <span class="ada-empty__mark"><ui-icon name="i-alert" :size="20" /></span>
            <h2 class="ada-empty__title">加载失败</h2>
            <p class="ada-empty__desc">{{ failed }}</p>
            <div class="ada-empty__action"><button class="ada-btn ada-btn--primary ada-btn--md" type="button" @click="load">重试</button></div>
          </div>
          <div v-else-if="!data" class="ada-empty">
            <span class="ada-empty__mark"><ui-icon name="i-chart" :size="20" /></span>
            <h2 class="ada-empty__title">正在加载…</h2>
            <p class="ada-empty__desc">统计来自本地会话、工作区与插件清单。</p>
          </div>
          <div v-else class="ada-host">
            <div class="ov-kpis">
              <div v-for="(s, i) in data.stats" :key="s.label" class="ov-kpi" :data-tone="s.tone">
                <span class="ov-kpi__label">{{ s.label }}</span>
                <span :class="s.numeric ? 'ov-kpi__value' : 'ov-kpi__value ov-kpi__value--text'">{{ s.value }}</span>
                <span class="ov-kpi__note">{{ s.hint }}</span>
              </div>
            </div>

            <section class="ov-recent" aria-label="最近会话">
              <div class="ov-recent__head">
                <div class="ov-recent__title">
                  <span>最近会话</span>
                  <span class="ov-count">{{ recentShown }}</span>
                </div>
                <a class="ov-link" href="/threads" @click.prevent="goThreads">全部会话 →</a>
              </div>
              <div v-if="recentShown > 0" class="ov-recent__list">
                <a v-for="r in recentSlice" :key="r.id" class="ov-thread" :href="threadURL(r.id)" @click.prevent="openThread(r.id)">
                  <span class="ov-thread__main">
                    <span class="ov-thread__title">{{ r.title ? r.title : "新会话" }}<span v-if="r.running" class="ov-chip ov-chip--warn">运行中</span></span>
                    <span class="ov-thread__path">{{ r.workspace }}</span>
                  </span>
                  <span class="ov-thread__time">{{ r.updatedLabel }}</span>
                </a>
              </div>
              <div v-else class="ov-recent__empty">还没有会话。去「对话」页新建一个。</div>
              <a class="ov-recent__more" href="/threads" @click.prevent="goThreads">查看全部会话 →</a>
            </section>
          </div>
        </div>

        <section class="ov-contrib" aria-label="插件贡献">
          <div class="ov-contrib__head">
            <span class="ov-contrib__title">插件贡献</span>
            <span class="ov-tip ov-tip--sm" data-tip="插件通过 dashboard.widgets 插槽贡献的片段，随插件启停变化。" tabindex="0" role="img">!</span>
          </div>
          <p class="ov-contrib__desc">来自插件的 dashboard 片段</p>
          <div class="ov-contrib__slot"><ui-slot name="dashboard.widgets" /></div>
          <a class="ov-contrib__more" href="/extensions" @click.prevent="goExtensions">管理扩展 →</a>
        </section>
      </div>

      <div class="ov-status">
        <span class="ov-status__left"><span class="ov-status__dot"></span><span>服务已连接</span></span>
        <span class="ov-status__right">{{ gatewayLabel }}</span>
      </div>
    </div>
  </div>
</template>
<script src="./Overview.js"></script>
