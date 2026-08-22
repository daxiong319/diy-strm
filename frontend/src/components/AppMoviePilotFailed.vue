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

    <el-dialog v-model="resolveDialogVisible" title="确认整理（可选 TMDB 候选）" width="640px" :close-on-click-modal="false">
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
          <div style="display: flex; gap: 8px; width: 100%">
            <el-input v-model="resolveForm.title" placeholder="媒体标题，如：遮天" @keyup.enter="searchTmdb" />
            <el-button :loading="searching" @click="searchTmdb">搜索 TMDB</el-button>
          </div>
        </el-form-item>
        <el-form-item label="年份">
          <el-input-number v-model="resolveForm.year" :min="0" :max="2100" :controls="false" placeholder="可选" style="width: 100%" />
        </el-form-item>
        <el-form-item v-if="resolveForm.media_type === 'tv'" label="季号">
          <el-input-number v-model="resolveForm.season" :min="1" :max="100" :controls="false" style="width: 100%" />
        </el-form-item>
        <el-form-item label="TMDB ID">
          <div style="display: flex; align-items: center; gap: 8px; width: 100%">
            <el-input-number
              v-model="resolveForm.tmdb_id"
              :min="0"
              :controls="false"
              placeholder="可选；从下方候选点击自动填入，避免同名歧义"
              style="width: 100%"
            />
            <el-tag v-if="resolveForm.tmdb_id > 0" type="success" size="small">精确匹配</el-tag>
          </div>
        </el-form-item>
        <el-form-item v-if="candidates.length || searching" label="TMDB 候选">
          <div v-loading="searching" class="candidate-list">
            <div
              v-for="cand in candidates"
              :key="cand.media_type + cand.tmdb_id"
              class="candidate-card"
              :class="{ active: resolveForm.tmdb_id === cand.tmdb_id && resolveForm.media_type === cand.media_type }"
              @click="selectCandidate(cand)"
            >
              <img v-if="cand.poster_url" :src="cand.poster_url" class="candidate-poster" loading="lazy" referrerpolicy="no-referrer" />
              <div v-else class="candidate-poster candidate-poster-empty">无海报</div>
              <div class="candidate-body">
                <div class="candidate-title">
                  <span>{{ cand.title }}</span>
                  <span v-if="cand.year" class="candidate-year">({{ cand.year }})</span>
                  <el-tag size="small" :type="cand.media_type === 'tv' ? 'success' : 'warning'" style="margin-left: 6px">
                    {{ cand.media_type === 'tv' ? '剧集' : '电影' }}
                  </el-tag>
                </div>
                <div class="candidate-overview">{{ cand.overview || '暂无简介' }}</div>
                <div class="candidate-tmdb">tmdb={{ cand.tmdb_id }}</div>
              </div>
            </div>
            <div v-if="!candidates.length && !searching" class="candidate-empty">未搜到候选，可修改标题后重试</div>
          </div>
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
const resolveForm = ref<any>({ media_type: 'movie', title: '', year: 0, season: 1, tmdb_id: 0 })

// TMDB 候选（对齐 tgto123 识别测试：候选卡片一键选中，避免同名歧义）
interface TmdbCandidate {
  title: string
  original_title?: string
  year: number
  tmdb_id: number
  media_type: string
  poster_url: string
  overview: string
}
const candidates = ref<TmdbCandidate[]>([])
const searching = ref(false)

const selectCandidate = (cand: TmdbCandidate) => {
  resolveForm.value.tmdb_id = cand.tmdb_id
  resolveForm.value.media_type = cand.media_type
  resolveForm.value.title = cand.title
  resolveForm.value.year = cand.year || 0
}

// 用当前标题搜 TMDB 候选（复用识别接口返回的候选结构；标题手动改动后可重新搜索）
const searchTmdb = async () => {
  const title = (resolveForm.value.title || '').trim()
  if (!title) {
    ElMessage.warning('请先填写标题再搜索')
    return
  }
  searching.value = true
  try {
    const resp = await http.post(`${SERVER_URL}/organize-history/recognize-test`, {
      file_name: buildRecognizeName(),
      media_type: '',
    })
    if (resp?.data?.code === 200) {
      const list = (resp.data.data?.candidates || []).map((c: any) => ({
        title: c.title,
        original_title: c.original_title,
        year: c.year,
        tmdb_id: c.tmdb_id,
        media_type: c.media_type,
        poster_url: c.poster_path ? tmdbPosterUrl(c.poster_path) : '',
        overview: '',
      }))
      candidates.value = list
      if (!list.length) ElMessage.info('未搜到 TMDB 候选')
    } else {
      ElMessage.error(resp?.data?.message || 'TMDB 搜索失败')
    }
  } catch (e: any) {
    ElMessage.error('TMDB 搜索失败：' + (e?.message || ''))
  } finally {
    searching.value = false
  }
}

// recognize-test 按文件名解析标题，这里希望直接用用户填的标题：构造一个「标题即文件名」的输入
const buildRecognizeName = () => {
  const f = resolveForm.value
  const yearPart = f.year > 0 ? ` ${f.year}` : ''
  return `${(f.title || '').trim()}${yearPart}`
}

const tmdbPosterUrl = (path: string) => `https://image.tmdb.org/t/p/w185${path}`

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
        tmdb_id: data.tmdb_id || 0,
      }
      candidates.value = (data.candidates || []).map((c: any) => ({
        title: c.title,
        original_title: c.original_title,
        year: c.year,
        tmdb_id: c.tmdb_id,
        media_type: c.media_type,
        poster_url: c.poster_url || '',
        overview: c.overview || '',
      }))
      currentFile.value = row
      resolveDialogVisible.value = true
    } else {
      // AI 与正则均未命中：仍打开弹窗走手动搜索流程
      candidates.value = []
      currentFile.value = row
      resolveForm.value = { media_type: 'movie', title: '', year: 0, season: 1, tmdb_id: 0 }
      resolveDialogVisible.value = true
      ElMessage.warning(response?.data.message || '识别未命中，请手动填写并搜索 TMDB')
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
    tmdb_id: row.tmdb_id || 0,
  }
  candidates.value = []
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
      tmdb_id: resolveForm.value.tmdb_id || 0,
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
.candidate-list {
  width: 100%;
  max-height: 320px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.candidate-card {
  display: flex;
  gap: 10px;
  padding: 8px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  cursor: pointer;
  transition: border-color 0.15s;
}
.candidate-card:hover {
  border-color: var(--el-color-primary);
}
.candidate-card.active {
  border-color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
}
.candidate-poster {
  width: 46px;
  height: 66px;
  border-radius: 4px;
  object-fit: cover;
  flex-shrink: 0;
}
.candidate-poster-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
}
.candidate-body {
  flex: 1;
  min-width: 0;
}
.candidate-title {
  display: flex;
  align-items: center;
  font-weight: 500;
}
.candidate-year {
  margin-left: 6px;
  color: var(--el-text-color-secondary);
  font-weight: 400;
}
.candidate-overview {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.candidate-tmdb {
  margin-top: 4px;
  font-size: 11px;
  color: var(--el-text-color-placeholder);
}
.candidate-empty {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  padding: 8px;
}
</style>
