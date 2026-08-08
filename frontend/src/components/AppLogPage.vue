<script setup lang="ts">
import { defineAsyncComponent } from 'vue'

const AppLogViewer = defineAsyncComponent(() => import('./AppLogViewer.vue'))
</script>

<template>
  <div class="log-page-container">
    <div class="page-header">
      <div class="header-content">
        <h1>运行日志</h1>
        <p>系统运行日志实时查看与下载</p>
      </div>
    </div>
    <AppLogViewer
      log-path="app.log"
      :is-real-time="true"
      fullscreen
      height="calc(100dvh - 230px)"
      mobile-height="calc(100dvh - 170px)"
    />
  </div>
</template>

<style scoped>
.log-page-container {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 24px;
  padding: 0;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 20px 24px;
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  border-radius: 16px;
  color: white;
}

.header-content h1 {
  margin: 0 0 4px 0;
  font-size: 28px;
  font-weight: 700;
}

.header-content p {
  margin: 0;
  font-size: 14px;
  opacity: 0.9;
}
</style>
