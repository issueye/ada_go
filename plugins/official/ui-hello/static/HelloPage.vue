<template>
  <div class="ext-hello-page">
    <h2 class="ext-hello-page__title">Hello SFC</h2>
    <p>这是一个运行时装载的单文件组件：没有 SSR、没有构建步骤，改完刷新即生效。</p>
    <ui-panel title="运行时栈">
      <ul>
        <li v-for="f in stack" :key="f">{{ f }}</li>
      </ul>
    </ui-panel>
    <p>装载时刻：{{ mountedAt }}</p>
  </div>
</template>
<style scoped>
.ext-hello-page { max-inline-size: 560px; }
.ext-hello-page__title { color: var(--ph-text-primary, inherit); }
</style>
<script src="./HelloPage.js"></script>
