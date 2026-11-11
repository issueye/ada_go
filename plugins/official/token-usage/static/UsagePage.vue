<template>
  <div class="tu-page">
    <div class="tu-head">
      <div class="tu-title-row">
        <h1 class="tu-title">Token 用量</h1>
        <span class="tu-tip" data-tip="工作区聚合与当前会话累计，数据来自插件用 events.read 读取的每轮原始计数；聚合口径在插件内，基座不再提供展示投影。成本按 costMicros 折算，为 0 时显示 —。" aria-label="工作区聚合与当前会话累计，数据来自插件用 events.read 读取的每轮原始计数；聚合口径在插件内，基座不再提供展示投影。成本按 costMicros 折算，为 0 时显示 —。" tabindex="0" role="img">!</span>
      </div>
      <div class="tu-actions">
        <button class="tu-btn" type="button" @click="copySummary">复制汇总</button>
        <button class="tu-btn tu-btn--primary" type="button" @click="exportCSV">导出 CSV</button>
      </div>
    </div>

    <div class="tu-meta">
      <span class="tu-meta__label">数据由插件聚合</span>
      <div class="tu-seg" role="group" aria-label="切换统计范围">
        <button type="button" :class="'tu-seg__item' + (view === 'workspace' ? ' is-active' : '')" :aria-pressed="view === 'workspace' ? 'true' : 'false'" @click="view = 'workspace'">工作区</button>
        <button type="button" :class="'tu-seg__item' + (view === 'thread' ? ' is-active' : '')" :aria-pressed="view === 'thread' ? 'true' : 'false'" @click="view = 'thread'">当前会话</button>
      </div>
      <span class="tu-meta__right">随页面读取 · 累计聚合</span>
    </div>

    <div class="tu-kpis">
      <div class="tu-kpi">
        <span class="tu-kpi__label">累计 Token</span>
        <span class="tu-kpi__value">{{ formatNumber(totals.total) }}</span>
        <span class="tu-kpi__note">输入 + 输出</span>
      </div>
      <div class="tu-kpi">
        <span class="tu-kpi__label">输入</span>
        <span class="tu-kpi__value">{{ formatNumber(totals.input) }}</span>
        <span class="tu-kpi__note">占累计 {{ share(totals.input, totals.total) }}</span>
      </div>
      <div class="tu-kpi">
        <span class="tu-kpi__label">输出</span>
        <span class="tu-kpi__value">{{ formatNumber(totals.output) }}</span>
        <span class="tu-kpi__note">占累计 {{ share(totals.output, totals.total) }}</span>
      </div>
      <div class="tu-kpi">
        <span class="tu-kpi__label">缓存命中率</span>
        <span class="tu-kpi__value">{{ cacheRate(current) }}</span>
        <span class="tu-kpi__note">命中 {{ formatNumber(totals.cache) }} tokens</span>
      </div>
    </div>

    <div class="tu-lower">
      <section class="tu-panel" aria-label="用量构成">
        <div class="tu-panel__head">
          <span class="tu-panel__title">用量构成</span>
          <span class="tu-panel__hint">{{ view === 'thread' ? '当前会话' : '工作区' }} · 累计 {{ formatNumber(totals.total) }} tokens</span>
        </div>
        <div v-if="totals.whole > 0" class="tu-panel__body">
          <div class="tu-row">
            <span class="tu-row__label">输入</span>
            <span class="tu-meter"><span :class="meterClass(totals.input, totals.total) + ' tu-meter__tone--ink'"></span></span>
            <span class="tu-row__value">{{ formatNumber(totals.input) }}</span>
            <span class="tu-row__pct">{{ share(totals.input, totals.total) }}</span>
          </div>
          <div class="tu-row">
            <span class="tu-row__label">输出</span>
            <span class="tu-meter"><span :class="meterClass(totals.output, totals.total) + ' tu-meter__tone--mid'"></span></span>
            <span class="tu-row__value">{{ formatNumber(totals.output) }}</span>
            <span class="tu-row__pct">{{ share(totals.output, totals.total) }}</span>
          </div>
          <div class="tu-row">
            <span class="tu-row__label">缓存命中</span>
            <span class="tu-meter"><span :class="meterClass(totals.cache, totals.input) + ' tu-meter__tone--soft'"></span></span>
            <span class="tu-row__value">{{ formatNumber(totals.cache) }}</span>
            <span class="tu-row__pct">{{ share(totals.cache, totals.input) }}</span>
          </div>
          <p class="tu-panel__note">累计 = 输入 + 输出；缓存是输入里命中缓存的部分，不重复计入。</p>
        </div><p v-else class="tu-panel__empty">这个范围还没有记录到用量。</p>
      </section>

      <section class="tu-panel" aria-label="模型与成本">
        <div class="tu-panel__head">
          <span class="tu-panel__title">模型与成本</span>
          <span class="tu-panel__hint">当前会话 {{ formatNumber(totalOf(thread)) }}</span>
        </div>
        <div class="tu-panel__body">
          <div class="tu-detail">
            <span class="tu-detail__label">本次会话累计</span>
            <span class="tu-detail__value">{{ formatNumber(totalOf(thread)) }}</span>
          </div>
          <div class="tu-detail">
            <span class="tu-detail__label">成本</span>
            <span class="tu-detail__value">{{ formatCost(num(current, 'costMicros')) }}</span>
          </div>
          <div class="tu-detail">
            <span class="tu-detail__label">模型</span>
            <span class="tu-detail__value tu-detail__value--mono">{{ modelText || '多模型合并' }}</span>
          </div>
          <p class="tu-panel__note">
            {{ modelText ? '单一 provider/model 分组时显示来源；多分组会被后端折叠成一个聚合值。' : '多组 provider/model 由插件聚合；这里不做按模型拆分。' }}
          </p>
        </div>
      </section>
    </div>

    <div class="tu-foot">
      <span class="tu-foot__left">
        <span class="tu-tip" data-tip="成本按 Core 记录的 costMicros 折算；为 0 时显示 —。缓存命中率 = cacheTokens / inputTokens。" aria-label="成本按 Core 记录的 costMicros 折算；为 0 时显示 —。缓存命中率 = cacheTokens / inputTokens。" tabindex="0" role="img">!</span>
        <span>成本按 Core 记录折算；缓存命中率 = 缓存 / 输入</span>
      </span>
      <span class="tu-foot__right">events.read · 插件聚合</span>
    </div>

    <div v-if="toast" class="tu-toast" :data-tone="toast.tone" role="status" aria-live="polite">{{ toast.message }}</div>
  </div>
</template>
<script src="./UsagePage.js"></script>
