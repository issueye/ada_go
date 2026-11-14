<template>
  <span v-if="!ready" class="ada-telemetry__pending" aria-hidden="true"></span>
  <div v-else class="ada-aux">
    <!-- 指标行（2026-11-12 按新版输入框设计）：卡下方居中三项 —— 生成速度、
         缓存命中率、上下文占用。每项自带图标，长度变化只影响自己。 -->
    <span v-if="speedLabel" class="ada-aux__item ada-aux__metric" :title="speedTitle">
      <ui-icon name="i-wave" :size="13" /><span>{{ speedLabel }}</span>
    </span>
    <span v-if="cacheLabel" class="ada-aux__item ada-aux__metric" title="缓存命中率：输入 token 中命中缓存的比例">
      <ui-icon name="i-database" :size="13" /><span>缓存命中 {{ cacheLabel }}</span>
    </span>
    <details v-if="contextInfo" class="ada-context">
      <summary class="ada-aux__item ada-aux__metric ada-context__summary" :aria-label="'上下文已用 ' + contextInfo.percent + '% · 约 ' + contextInfo.tokens + ' / ' + contextInfo.windowLabel">
        <ui-icon name="i-clock" :size="13" /><span>{{ contextInfo.percent }}%</span>
      </summary>
      <div class="ada-context__panel" role="group" aria-label="上下文用量明细">
        <p class="ada-context__head">
          <span class="ada-context__head-main">上下文已用 {{ contextInfo.percent }}%</span>
          <span class="ada-context__head-total">约 {{ contextInfo.tokens }} / {{ contextInfo.windowLabel }}</span>
        </p>
        <div class="ada-telemetry-meter" aria-hidden="true">
          <div class="ada-telemetry-meter__fill" :class="contextInfo.high ? 'is-high' : ''" :style="{ width: contextInfo.percent + '%' }"></div>
        </div>
        <ul class="ada-context__list">
          <li class="ada-context__row">
            <span class="ada-context__dot ada-context__dot--system" aria-hidden="true"></span>
            <span class="ada-context__name">系统提示词</span>
            <span class="ada-context__value">约 {{ contextInfo.system }}</span>
          </li>
          <li class="ada-context__row">
            <span class="ada-context__dot ada-context__dot--tools" aria-hidden="true"></span>
            <span class="ada-context__name">工具定义</span>
            <span class="ada-context__value">约 {{ contextInfo.tools }}</span>
          </li>
          <li class="ada-context__row">
            <span class="ada-context__dot ada-context__dot--messages" aria-hidden="true"></span>
            <span class="ada-context__name">对话消息</span>
            <span class="ada-context__value">约 {{ contextInfo.messages }}</span>
          </li>
        </ul>
        <p v-if="contextInfo.hint" class="ada-context__hint">{{ contextInfo.hint }}</p>
      </div>
    </details>
  </div>
</template>
<style scoped>
/* 旧实现的 SVG 环/条（白名单不含 svg）降级为文本百分比 + div 计量条；
   颜色只引用 --ph-* / --ada-* 令牌（与插件 styles 纪律一致）。 */
.ada-telemetry-pct {
  font-size: var(--ada-text-xs, 12px);
  font-weight: 600;
  color: var(--ph-muted, inherit);
}
.ada-telemetry-pct.is-high {
  color: var(--ph-danger-ink, inherit);
}
.ada-telemetry-meter {
  height: 4px;
  margin-top: 8px;
  overflow: hidden;
  border-radius: var(--ada-radius-pill, 2px);
  background: var(--ph-line, transparent);
}
.ada-telemetry-meter__fill {
  height: 100%;
  border-radius: var(--ada-radius-pill, 2px);
  background: var(--ph-brand, currentColor);
  transition: width var(--ada-transition, 0.2s);
}
.ada-telemetry-meter__fill.is-high {
  background: var(--ph-danger, currentColor);
}
</style>
<script src="./Telemetry.js"></script>
