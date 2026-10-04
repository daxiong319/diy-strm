<script setup lang="ts">
import AppButton from "@/components/base/AppButton.vue";

// defineAsyncComponent 的 errorComponent：chunk 加载失败时兜底展示，避免整页静默空白。
function reload() {
  window.location.reload();
}
</script>

<template>
  <div class="async-error-panel">
    <p class="async-error-panel__text">页面组件加载失败，请刷新重试（若持续出现请清浏览器缓存）</p>
    <AppButton variant="secondary" size="sm" @click="reload">刷新页面</AppButton>
  </div>
</template>

<style scoped>
.async-error-panel {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
  padding: 48px 16px;
  color: var(--danger, #e5484d);
  font-size: 13px;
  text-align: center;
}

.async-error-panel__text {
  margin: 0;
}
</style>
