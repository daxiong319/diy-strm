<script setup lang="ts">
import { ref, computed, onMounted, defineAsyncComponent } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'

interface LogFileInfo {
  name: string
  path: string
  size: number
  mtime: number
}

const AppLogViewer = defineAsyncComponent(() => import('./AppLogViewer.vue'))

const SELECTED_LOG_FILE_KEY = 'diy_strm_selected_logfile'

// 已知日志文件的中文友好名
const LOG_FILE_TITLES: Record<string, string> = {
  'app.log': '全局日志',
  'web.log': 'Web 服务',
  'tmdb.log': 'TMDB 刮削',
  '115.log': '115 云盘',
  'openList.log': 'OpenList 资源',
  'baidupan.log': '百度云盘',
  'guangyapan.log': '光鸭云盘',
  '139.log': '移动云盘',
  'pan123.log': '123 云盘',
  'automove.log': '自动整理',
  'rename.log': '批量重命名',
}

function formatBytes(size: number): string {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / (1024 * 1024)).toFixed(1)} MB`
}

function logFileTitle(path: string): string {
  const dir = path.includes('/') ? path.split('/')[0] : ''
  const name = path.split('/').pop() ?? path
  if (dir === 'sync') {
    const taskNo = name.replace(/^sync_/, '').replace(/\.log$/, '')
    return `同步任务 ${taskNo}`
  }
  if (dir === 'libs') {
    const taskNo = name.replace(/^sync_/, '').replace(/\.log$/, '')
    return `同步任务(旧) ${taskNo}`
  }
  return LOG_FILE_TITLES[name] ?? path
}

const logFiles = ref<LogFileInfo[]>([])
const selectedLogPath = ref(localStorage.getItem(SELECTED_LOG_FILE_KEY) || 'app.log')
const loadingFiles = ref(false)

const selectedFile = computed(() => logFiles.value.find((file) => file.path === selectedLogPath.value))

const loadLogFiles = async () => {
  loadingFiles.value = true
  try {
    const response = await fetch(`${SERVER_URL}/logs/files`, { credentials: 'include' })
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }
    const body = (await response.json()) as { files?: LogFileInfo[] }
    logFiles.value = body.files || []
    // 记忆的文件被清理后回退到全局日志
    if (!logFiles.value.some((file) => file.path === selectedLogPath.value)) {
      selectedLogPath.value = 'app.log'
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : '未知错误'
    ElMessage.error(`加载日志文件列表失败：${message}`)
  } finally {
    loadingFiles.value = false
  }
}

const onSelectLogFile = (path: string) => {
  selectedLogPath.value = path
  localStorage.setItem(SELECTED_LOG_FILE_KEY, path)
}

onMounted(() => {
  void loadLogFiles()
})
</script>

<template>
  <div class="log-page-container">
    <div class="page-header">
      <div class="header-content">
        <h1>运行日志</h1>
        <p>系统运行日志实时查看、筛选与下载</p>
      </div>
      <div class="header-controls">
        <el-select
          v-model="selectedLogPath"
          class="log-file-select"
          placeholder="选择日志文件"
          :loading="loadingFiles"
          filterable
          @change="onSelectLogFile"
        >
          <el-option
            v-for="file in logFiles"
            :key="file.path"
            :label="`${logFileTitle(file.path)} · ${file.name}`"
            :value="file.path"
          />
        </el-select>
        <el-button :icon="Refresh" circle title="刷新日志文件列表" @click="loadLogFiles" />
      </div>
    </div>
    <div v-if="selectedFile" class="log-file-meta">
      当前文件：<b>{{ logFileTitle(selectedFile.path) }}</b>（{{ selectedFile.path }}）
      <span class="meta-dim">大小 {{ formatBytes(selectedFile.size) }}</span>
      <span class="meta-dim">
        更新时间 {{ new Date(selectedFile.mtime * 1000).toLocaleString('zh-CN') }}
      </span>
    </div>
    <AppLogViewer
      :log-path="selectedLogPath"
      :is-real-time="true"
      fullscreen
      height="calc(100dvh - 300px)"
      mobile-height="calc(100dvh - 250px)"
    />
  </div>
</template>

<style scoped>
.log-page-container {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 0;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
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

.header-controls {
  display: flex;
  align-items: center;
  gap: 8px;
}

.log-file-select {
  width: 240px;
}

.log-file-meta {
  display: flex;
  align-items: center;
  gap: 16px;
  font-size: 13px;
  color: #303133;
  background: #f5f7fa;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  padding: 8px 12px;
  flex-wrap: wrap;
}

.meta-dim {
  color: #909399;
}

@media (max-width: 768px) {
  .page-header {
    flex-direction: column;
    align-items: stretch;
    padding: 16px;
  }

  .log-file-select {
    width: 100%;
  }
}
</style>