<template>
  <div class="moviepilot-failed-container">
    <div class="card-header" style="padding-bottom: 12px">
      <div>
        <h2 class="hide-on-mobile">识别失败文件</h2>
        <p class="queue-description">
          上传整理时无法自动识别（正则 + AI 兜底均未命中）的文件。可手动确认媒体信息后重新整理，或直接跳过。
        </p>
      </div>
      <div>
        <el-select v-model="statusFilter" style="width: 140px; margin-right: 8px" @change="handleFilterChange">
          <el-option label="全部" value="" />
          <el-option label="待处理" value="pending" />
          <el-option label="已整理" value="resolved" />
          <el-option label="已跳过" value="skipped" />
        </el-select>
        <el-button type="info" @click="loadFailedFiles" :loading="loading">刷新</el-button>
      </div>
    </div>

    <el-table :data="files" v-loading="loading" empty-text="暂无识别失败文件" style="width: 100%">
      <el-table-column prop="file_name" label="文件名" min-width="200" show-overflow-tooltip />
      <el-table-column prop="task_title" label="所属任务" min-width="150" show-overflow-tooltip />
      <el-table-column label="媒体信息" min-width="160">
        <template #default="scope">
          <template v-if="scope.row.title">
            <span>{{ scope.row.title }}</span>
            <el-tag size="small" style="margin-left: 6px" :type="scope.row.media_type === 'tv' ? 'success' : 'warning'">
              {{ scope.row.media_type === 'tv' ? '剧集' : '电影' }}
            </el-tag>
            <span v-if="scope.row.media_type === 'tv'" style="margin-left: 4px">S{{ scope.row.season }}</span>
            <span v-if="scope.row.year" style="margin-left: 4px">({{ scope.row.year }})</span>
          </template>
          <span v-else>-</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="scope">
          <el-tag :type="getStatusTag(scope.row.status)">{{ getStatusText(scope.row.status) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="reason" label="原因" min-width="160" show-overflow-tooltip />
      <el-table-column label="时间" width="150">
        <template #default="scope">{{ formatTime(scope.row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="220" fixed="right">
        <template #default="scope">
          <template v-if="scope.row.status === 'pending'">
            <el-button
              size="small"
              type="primary"
              :loading="scope.row._identifying"
              @click="identifyFile(scope.row)"
              >AI 识别</el-button
            >
            <el-button size="small" type="success" @click="openResolveDialog(scope.row)">确认整理</el-button>
            <el-button size="small" type="info" @click="skipFile(scope.row)">跳过</el-button>
          </template>
          <span v-else>-</span>
        </template>
      </el-table-column>
    </el-table>
    <div style="margin-top: 12px; text-align: right">
      <el-pagination
        layout="prev, pager, next, total"
        :total="total"
        :page-size="pageSize"
        :current-page="page"
        @current-change="handlePageChange"
      />
    </div>

    <el-dialog v-model="resolveDialogVisible" title="确认整理" width="520px" :close-on-click-modal="false">
      <el-form :model="resolveForm" label-width="110px">
        <el-form-item label="文件名">
          <el-input :model-value="currentFile?.file_name" disabled />
        </el-form-item>
        <el-form-item label="媒体类型" required>
          <el-radio-group v-model="resolveForm.media_type">
            <el-radio value="movie">电影</el-radio>
            <el-radio value="tv">剧集</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="标题" required>
          <el-input v-model="resolveForm.title" placeholder="媒体标题，如：遮天" />
        </el-form-item>
        <el-form-item label="年份">
          <el-input-number v-model="resolveForm.year" :min="0" :max="2100" :controls="false" placeholder="可选" style="width: 100%" />
        </el-form-item>
        <el-form-item v-if="resolveForm.media_type === 'tv'" label="季号">
          <el-input-number v-model="resolveForm.season" :min="1" :max="100" :controls="false" style="width: 100%" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="resolveDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="resolving" @click="resolveFile">确认并整理</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'

const http = useHttpClient()

const files = ref<any[]>([])
const loading = ref(false)
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const statusFilter = ref('')

const resolveDialogVisible = ref(false)
const resolving = ref(false)
const currentFile = ref<any>(null)
const resolveForm = ref<any>({ media_type: 'movie', title: '', year: 0, season: 1 })

const getStatusText = (status: string) => {
  const map: Record<string, string> = { pending: '待处理', resolved: '已整理', skipped: '已跳过' }
  return map[status] || status
}
const getStatusTag = (status: string) => {
  const map: Record<string, string> = { pending: 'warning', resolved: 'success', skipped: 'info' }
  return map[status] || 'info'
}
const formatTime = (ts: number) => {
  if (!ts) return '-'
  return new Date(ts * 1000).toLocaleString('zh-CN', { hour12: false })
}

const loadFailedFiles = async () => {
  loading.value = true
  try {
    const response = await http.get(`${SERVER_URL}/moviepilot/failed-files`, {
      params: { page: page.value, page_size: pageSize.value, status: statusFilter.value },
    })
    if (response?.data.code === 200) {
      files.value = response.data.data?.list || []
      total.value = Number(response.data.data?.total || 0)
    } else {
      ElMessage.error(response?.data.message || '加载识别失败文件失败')
    }
  } catch (error) {
    console.error('加载识别失败文件错误：', error)
    ElMessage.error('加载识别失败文件失败')
  } finally {
    loading.value = false
  }
}

const handleFilterChange = () => {
  page.value = 1
  loadFailedFiles()
}
const handlePageChange = (p: number) => {
  page.value = p
  loadFailedFiles()
}

const identifyFile = async (row: any) => {
  row._identifying = true
  try {
    const response = await http.post(`${SERVER_URL}/moviepilot/failed-files/${row.id}/identify`)
    if (response?.data.code === 200) {
      const data = response.data.data
      resolveForm.value = {
        media_type: data.category === 'tv' ? 'tv' : 'movie',
        title: data.title,
        year: data.year || 0,
        season: data.season || 1,
      }
      currentFile.value = row
      resolveDialogVisible.value = true
    } else {
      ElMessage.warning(response?.data.message || 'AI 识别失败，请手动填写媒体信息')
    }
  } catch (error) {
    console.error('AI 识别错误：', error)
    ElMessage.error('AI 识别失败')
  } finally {
    row._identifying = false
  }
}

const openResolveDialog = (row: any) => {
  currentFile.value = row
  resolveForm.value = {
    media_type: row.media_type || 'movie',
    title: row.title || '',
    year: row.year || 0,
    season: row.season || 1,
  }
  resolveDialogVisible.value = true
}

const resolveFile = async () => {
  if (!resolveForm.value.title.trim()) {
    ElMessage.warning('请填写媒体标题')
    return
  }
  resolving.value = true
  try {
    const response = await http.post(`${SERVER_URL}/moviepilot/failed-files/${currentFile.value.id}/resolve`, {
      media_type: resolveForm.value.media_type,
      title: resolveForm.value.title.trim(),
      year: resolveForm.value.year || 0,
      season: resolveForm.value.media_type === 'tv' ? resolveForm.value.season || 1 : 0,
    })
    if (response?.data.code === 200) {
      ElMessage.success('整理完成')
      resolveDialogVisible.value = false
      loadFailedFiles()
    } else {
      ElMessage.error(response?.data.message || '整理失败')
    }
  } catch (error) {
    console.error('确认整理错误：', error)
    ElMessage.error('整理失败')
  } finally {
    resolving.value = false
  }
}

const skipFile = async (row: any) => {
  try {
    const response = await http.post(`${SERVER_URL}/moviepilot/failed-files/${row.id}/skip`)
    if (response?.data.code === 200) {
      ElMessage.success('已跳过')
      loadFailedFiles()
    } else {
      ElMessage.error(response?.data.message || '操作失败')
    }
  } catch (error) {
    console.error('跳过错误：', error)
    ElMessage.error('操作失败')
  }
}

onMounted(loadFailedFiles)
</script>

<style scoped>
.moviepilot-failed-container {
  padding: 4px;
}
</style>
