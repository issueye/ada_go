<template>
  <div v-if="hasData" class="tu-widget">
    <div class="tu-widget__head">
      <span class="tu-widget__title">Token 用量</span>
      <span class="tu-widget__total">{{ formatNumber(workspaceTotal) }}</span>
    </div>
    <p class="tu-widget__meta">
      工作区：输入 {{ formatNumber(num(workspace, 'inputTokens')) }} · 输出 {{ formatNumber(num(workspace, 'outputTokens')) }} · 缓存 {{ formatNumber(num(workspace, 'cacheTokens')) }} · 成本 {{ formatCost(num(workspace, 'costMicros')) }}
    </p>
    <p v-if="thread" class="tu-widget__meta">
      当前会话：输入 {{ formatNumber(num(thread, 'inputTokens')) }} · 输出 {{ formatNumber(num(thread, 'outputTokens')) }} · 缓存 {{ formatNumber(num(thread, 'cacheTokens')) }}
    </p>
  </div>
</template>
<script src="./UsageWidget.js"></script>
