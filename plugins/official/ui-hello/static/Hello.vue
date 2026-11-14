<template>
  <div class="ext-hello">
    <p class="ext-hello__line">你好，{{ who }}！</p>
    <button class="ada-btn" @click="bump()">点了 {{ n }} 次</button>
  </div>
</template>
<style scoped>
.ext-hello { padding: var(--ada-space-2, 8px); }
.ext-hello__line { color: var(--ph-text-primary, inherit); }
</style>
<script src="./Hello.js"></script>
