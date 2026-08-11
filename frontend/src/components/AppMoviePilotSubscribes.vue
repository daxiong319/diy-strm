<template>
  <div class="moviepilot-subscribes-container">
    <el-tabs v-model="activeTab" type="border-card">
      <el-tab-pane label="订阅管理" name="subscribes">
        <div class="card-header" style="padding-bottom: 12px">
          <div>
            <h2 class="hide-on-mobile">MoviePilot 订阅</h2>
            <p class="queue-description">在 MoviePilot 中订阅剧集/电影，下载完成后自动上传网盘并生成 STRM。</p>
          </div>
          <div class="header-actions">
            <el-button type="primary" :icon="Plus" @click="openCreateDialog">添加订阅</el-button>
            <el-button type="info" @click="loadSubscribes" :loading="subscribesLoading">刷新</el-button>
          </div>
        </div>
        <div class="quick-search-bar">
          <el-input
            v-model="quickKeyword"
            placeholder="输入影视名称直接搜索订阅，如：我的荒糖恋爱"
            clearable
            style="max-width: 340px"
            @keyup.enter="quickSearch"
          />
          <el-radio-group v-model="quickType">
            <el-radio-button value="tv">剧集</el-radio-button>
            <el-radio-button value="movie">电影</el-radio-button>
          </el-radio-group>
          <el-button type="primary" :icon="Search" :loading="quickSearching" @click="quickSearch">搜索</el-button>
        </div>
        <div v-if="quickResults.length > 0" class="quick-results">
          <div v-for="r in quickResults" :key="r.tmdb_id" class="quick-result-row">
            <el-image :src="r.poster_url" fit="cover" class="quick-poster" lazy>
              <template #error>
                <div class="quick-poster-placeholder">暂无封面</div>
              </template>
            </el-image>
            <div class="quick-info">
              <div class="quick-title">
                {{ r.title || r.name }}
                <span v-if="r.year" class="quick-year">（{{ r.year }}）</span>
                <el-tag v-if="r.vote_average" size="small" type="warning" class="quick-score">{{ r.vote_average.toFixed(1) }}</el-tag>
              </div>
              <div class="quick-overview">{{ r.overview || '暂无简介' }}</div>
            </div>
            <el-button size="small" type="primary" :loading="r._subscribing" @click="quickSubscribe(r)">订阅</el-button>
          </div>
        </div>
        <el-table :data="subscribes" v-loading="subscribesLoading" empty-text="暂无订阅" style="width: 100%">
          <el-table-column prop="name" label="名称" min-width="160" show-overflow-tooltip />
          <el-table-column prop="year" label="年份" width="80" />
          <el-table-column label="类型" width="80">
            <template #default="scope">
              <el-tag :type="scope.row.type === 'tv' ? 'success' : 'warning'">
                {{ scope.row.type === 'tv' ? '剧集' : '电影' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="scope">
              <el-tag :type="getStateTag(scope.row.state)">{{ getStateText(scope.row.state) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="240" fixed="right">
            <template #default="scope">
              <el-button size="small" type="primary" :loading="scope.row._searching" @click="searchSubscribe(scope.row)"
                >搜索</el-button
              >
              <el-button size="small" type="warning" @click="toggleSubscribeState(scope.row)">
                {{ scope.row.state === 'R' ? '暂停' : '恢复' }}
              </el-button>
              <el-button size="small" type="danger" @click="deleteSubscribe(scope.row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <el-tab-pane label="下载任务" name="downloads">
        <div class="card-header" style="padding-bottom: 12px">
          <h2 class="hide-on-mobile">MoviePilot 下载任务</h2>
          <el-button type="info" @click="loadDownloads" :loading="downloadsLoading">刷新</el-button>
        </div>
        <el-table :data="downloads" v-loading="downloadsLoading" empty-text="暂无下载任务" style="width: 100%">
          <el-table-column prop="title" label="标题" min-width="200" show-overflow-tooltip />
          <el-table-column prop="season_episode" label="集数" width="90" />
          <el-table-column label="状态" width="100">
            <template #default="scope">
              <el-tag :type="getDownloadStateTag(scope.row.state)">{{ scope.row.state }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="上传状态" width="120">
            <template #default="scope">
              <el-tag v-if="scope.row.upload_status" :type="getUploadStatusTag(scope.row.upload_status)">
                {{ getUploadStatusText(scope.row.upload_status) }}
              </el-tag>
              <span v-else>-</span>
            </template>
          </el-table-column>
          <el-table-column label="进度" min-width="140">
            <template #default="scope">
              <el-progress :percentage="Number(scope.row.progress || 0)" />
            </template>
          </el-table-column>
          <el-table-column prop="save_path" label="保存路径" min-width="180" show-overflow-tooltip />
        </el-table>
      </el-tab-pane>

      <el-tab-pane label="网盘上传任务" name="upload-tasks">
        <div class="card-header" style="padding-bottom: 12px">
          <div>
            <h2 class="hide-on-mobile">网盘上传任务</h2>
            <p class="queue-description">MoviePilot 下载完成后自动上传到目标网盘，并执行文件名解析整理（重命名 + 分类移动）的任务进度。</p>
          </div>
          <div>
            <el-select v-model="uploadStatusFilter" style="width: 140px; margin-right: 8px" @change="loadUploadTasks">
              <el-option label="全部" value="" />
              <el-option label="等待上传" value="pending" />
              <el-option label="上传中" value="uploading" />
              <el-option label="已完成" value="uploaded" />
              <el-option label="失败" value="failed" />
              <el-option label="已取消" value="canceled" />
            </el-select>
            <el-button type="info" @click="loadUploadTasks" :loading="uploadTasksLoading">刷新</el-button>
          </div>
        </div>
        <el-table :data="uploadTasks" v-loading="uploadTasksLoading" empty-text="暂无上传任务" style="width: 100%">
          <el-table-column prop="title" label="标题" min-width="180" show-overflow-tooltip />
          <el-table-column prop="remote_path" label="云盘目标目录" min-width="180" show-overflow-tooltip />
          <el-table-column label="状态" width="100">
            <template #default="scope">
              <el-tag :type="getUploadStatusTag(scope.row.status)">{{ getUploadStatusText(scope.row.status) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="文件进度" min-width="150">
            <template #default="scope">
              <el-progress
                :percentage="getFilePercent(scope.row)"
                :status="scope.row.status === 'failed' ? 'exception' : scope.row.status === 'uploaded' ? 'success' : undefined"
              />
            </template>
          </el-table-column>
          <el-table-column label="大小" width="110">
            <template #default="scope">{{ formatBytes(scope.row.uploaded_bytes) }} / {{ formatBytes(scope.row.total_bytes) }}</template>
          </el-table-column>
          <el-table-column prop="error" label="错误信息" min-width="160" show-overflow-tooltip />
          <el-table-column label="操作" width="160" fixed="right">
            <template #default="scope">
              <el-button size="small" type="warning" :disabled="scope.row.status !== 'failed'" @click="retryUploadTask(scope.row)"
                >重试</el-button
              >
              <el-button
                size="small"
                type="danger"
                :disabled="!['pending', 'uploading'].includes(scope.row.status)"
                @click="cancelUploadTask(scope.row)"
                >取消</el-button
              >
            </template>
          </el-table-column>
        </el-table>
        <div style="margin-top: 12px; text-align: right">
          <el-pagination
            layout="prev, pager, next, total"
            :total="uploadTotal"
            :page-size="uploadPageSize"
            :current-page="uploadPage"
            @current-change="handleUploadPageChange"
          />
        </div>
      </el-tab-pane>
    </el-tabs>

    <el-dialog v-model="createDialogVisible" title="添加订阅" width="560px" :close-on-click-modal="false">
      <el-form :model="createForm" label-width="110px">
        <el-form-item label="媒体名称" required>
          <el-input v-model="createForm.name" placeholder="输入名称，或点击下方 TMDB 搜索自动填充" />
        </el-form-item>
        <el-form-item label="TMDB 搜索">
          <div style="display: flex; gap: 8px; width: 100%">
            <el-input v-model="searchKeyword" placeholder="输入关键词" />
            <el-select v-model="searchType" style="width: 110px">
              <el-option label="电影" value="movie" />
              <el-option label="剧集" value="tv_show" />
            </el-select>
            <el-button type="primary" :loading="tmdbSearching" @click="tmdbSearch">搜索</el-button>
          </div>
          <div v-if="tmdbResults.length > 0" style="margin-top: 8px; width: 100%; max-height: 200px; overflow: auto">
            <el-table :data="tmdbResults" size="small" highlight-current-row @current-change="selectTmdbResult">
              <el-table-column prop="title" label="标题" min-width="160" show-overflow-tooltip />
              <el-table-column label="年份" width="70">
                <template #default="scope">{{ scope.row.year || scope.row.release_date?.slice(0, 4) }}</template>
              </el-table-column>
              <el-table-column label="类型" width="70">
                <template #default="scope">
                  <el-tag size="small">{{ scope.row.media_type === 'movie' ? '电影' : '剧集' }}</el-tag>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </el-form-item>
        <el-form-item label="年份">
          <el-input v-model="createForm.year" placeholder="可选" />
        </el-form-item>
        <el-form-item label="类型" required>
          <el-select v-model="createForm.type" style="width: 100%">
            <el-option label="剧集" value="tv" />
            <el-option label="电影" value="movie" />
          </el-select>
        </el-form-item>
        <el-form-item label="TMDB ID">
          <el-input v-model="createForm.tmdbid" placeholder="可选，填了更精准" />
        </el-form-item>
        <el-form-item label="季节" v-if="createForm.type === 'tv'">
          <el-input v-model="createForm.season" placeholder="可选，如 S01" />
        </el-form-item>
        <el-form-item label="总集数" v-if="createForm.type === 'tv'">
          <el-input-number v-model="createForm.total_episode" :min="1" style="width: 100%" />
        </el-form-item>
        <el-form-item label="保存路径">
          <el-input v-model="createForm.save_path" placeholder="可选，MoviePilot 下载保存目录" />
        </el-form-item>
        <el-form-item label="搜索站点">
          <el-input v-model="createForm.sites" placeholder="可选，逗号分隔的站点名，留空使用默认" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="createSubscribe">确认添加</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Search } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'

const http = useHttpClient()
const activeTab = ref('subscribes')

const subscribes = ref<any[]>([])
const subscribesLoading = ref(false)

const downloads = ref<any[]>([])
const downloadsLoading = ref(false)

const uploadTasks = ref<any[]>([])
const uploadTasksLoading = ref(false)
const uploadTotal = ref(0)
const uploadPage = ref(1)
const uploadPageSize = ref(20)
const uploadStatusFilter = ref('')

const createDialogVisible = ref(false)
const creating = ref(false)
const searchKeyword = ref('')
const searchType = ref('tvshow')
const tmdbSearching = ref(false)
const tmdbResults = ref<any[]>([])

const quickKeyword = ref('')
const quickType = ref('tv')
const quickSearching = ref(false)
const quickResults = ref<any[]>([])

const createForm = ref<any>({
  name: '',
  year: '',
  type: 'tv',
  tmdbid: '',
  season: '',
  total_episode: 0,
  save_path: '',
  sites: '',
})

const getStateText = (state: string) => {
  const map: Record<string, string> = { R: '订阅中', S: '已订阅', P: '暂停' }
  return map[state] || state || '-'
}
const getStateTag = (state: string) => {
  const map: Record<string, string> = { R: 'success', S: 'success', P: 'info' }
  return map[state] || 'info'
}
const getDownloadStateTag = (state: string) => {
  const map: Record<string, string> = { downloading: 'primary', seeding: 'success', completed: 'success', failed: 'danger' }
  return map[state] || 'info'
}
const getUploadStatusText = (status: string) => {
  const map: Record<string, string> = { pending: '等待上传', uploading: '上传中', uploaded: '已完成', failed: '失败', canceled: '已取消' }
  return map[status] || status
}
const getUploadStatusTag = (status: string) => {
  const map: Record<string, string> = { pending: 'info', uploading: 'primary', uploaded: 'success', failed: 'danger', canceled: 'info' }
  return map[status] || 'info'
}
const getFilePercent = (row: any) => {
  if (!row.total_files) return 0
  return Math.min(100, Math.round((row.uploaded_files / row.total_files) * 100))
}
const formatBytes = (bytes: number) => {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = bytes
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

const loadSubscribes = async () => {
  subscribesLoading.value = true
  try {
    const response = await http.get(`${SERVER_URL}/moviepilot/subscribes`)
    if (response?.data.code === 200) {
      subscribes.value = response.data.data || []
    } else {
      ElMessage.error(response?.data.message || '加载订阅列表失败')
    }
  } catch (error) {
    console.error('加载订阅列表错误：', error)
    ElMessage.error('加载订阅列表失败')
  } finally {
    subscribesLoading.value = false
  }
}

const loadDownloads = async () => {
  downloadsLoading.value = true
  try {
    const response = await http.get(`${SERVER_URL}/moviepilot/downloads`)
    if (response?.data.code === 200) {
      downloads.value = response.data.data || []
    } else {
      ElMessage.error(response?.data.message || '加载下载任务失败')
    }
  } catch (error) {
    console.error('加载下载任务错误：', error)
    ElMessage.error('加载下载任务失败')
  } finally {
    downloadsLoading.value = false
  }
}

const loadUploadTasks = async () => {
  uploadTasksLoading.value = true
  try {
    const response = await http.get(`${SERVER_URL}/moviepilot/upload-tasks`, {
      params: { page: uploadPage.value, page_size: uploadPageSize.value, status: uploadStatusFilter.value },
    })
    if (response?.data.code === 200) {
      uploadTasks.value = response.data.data?.list || []
      uploadTotal.value = Number(response.data.data?.total || 0)
    } else {
      ElMessage.error(response?.data.message || '加载上传任务失败')
    }
  } catch (error) {
    console.error('加载上传任务错误：', error)
    ElMessage.error('加载上传任务失败')
  } finally {
    uploadTasksLoading.value = false
  }
}

const handleUploadPageChange = (page: number) => {
  uploadPage.value = page
  loadUploadTasks()
}

const openCreateDialog = () => {
  createForm.value = { name: '', year: '', type: 'tv', tmdbid: '', season: '', total_episode: 0, save_path: '', sites: '' }
  searchKeyword.value = ''
  tmdbResults.value = []
  createDialogVisible.value = true
}

const tmdbSearch = async () => {
  if (!searchKeyword.value) {
    ElMessage.warning('请输入搜索关键词')
    return
  }
  tmdbSearching.value = true
  try {
    const params: Record<string, string | number> = { name: searchKeyword.value, type: searchType.value }
    const response = await http.get(`${SERVER_URL}/scrape/tmdb-search`, { params, timeout: 30000 })
    const data = response?.data?.data
    const list = Array.isArray(data) ? data : data?.list || []
    tmdbResults.value = list.filter((item: any) => item && (item.title || item.name))
  } catch (error) {
    console.error('TMDB 搜索错误：', error)
    ElMessage.error('TMDB 搜索失败')
  } finally {
    tmdbSearching.value = false
  }
}

const selectTmdbResult = (row: any) => {
  if (!row) return
  createForm.value.name = row.title || row.name || ''
  createForm.value.year = row.year ? String(row.year) : row.release_date ? row.release_date.slice(0, 4) : ''
  createForm.value.tmdbid = row.id ? String(row.id) : ''
  createForm.value.type = row.media_type === 'movie' ? 'movie' : 'tv'
}

const quickSearch = async () => {
  if (!quickKeyword.value.trim()) {
    ElMessage.warning('请输入搜索关键词')
    return
  }
  quickSearching.value = true
  try {
    const params: Record<string, string | number> = { name: quickKeyword.value.trim(), type: quickType.value === 'movie' ? 'movie' : 'tvshow' }
    const response = await http.get(`${SERVER_URL}/scrape/tmdb-search`, { params, timeout: 30000 })
    const data = response?.data?.data
    const list = Array.isArray(data) ? data : data?.list || []
    quickResults.value = list
      .filter((item: any) => item && (item.title || item.name) && item.tmdb_id)
      .map((item: any) => ({ ...item, _subscribing: false }))
    if (quickResults.value.length === 0) {
      ElMessage.info('未找到匹配的影片')
    }
  } catch (error) {
    console.error('快速搜索错误：', error)
    ElMessage.error('搜索失败')
  } finally {
    quickSearching.value = false
  }
}

const quickSubscribe = async (row: any) => {
  row._subscribing = true
  try {
    const payload: Record<string, any> = {
      name: row.title || row.name,
      type: quickType.value,
      tmdbid: Number(row.tmdb_id),
    }
    if (row.year) payload.year = String(row.year)
    const response = await http.post(`${SERVER_URL}/moviepilot/subscribes`, payload)
    if (response?.data.code === 200) {
      ElMessage.success(`已订阅「${payload.name}」`)
      quickResults.value = quickResults.value.filter((item) => item.tmdb_id !== row.tmdb_id)
      loadSubscribes()
    } else {
      ElMessage.error(response?.data.message || '添加订阅失败')
    }
  } catch (error) {
    console.error('快速订阅错误：', error)
    ElMessage.error('添加订阅失败')
  } finally {
    row._subscribing = false
  }
}

const createSubscribe = async () => {
  if (!createForm.value.name) {
    ElMessage.warning('请填写媒体名称')
    return
  }
  creating.value = true
  try {
    const payload: Record<string, any> = {
      name: createForm.value.name,
      type: createForm.value.type,
    }
    if (createForm.value.year) payload.year = String(createForm.value.year).trim()
    if (createForm.value.tmdbid) payload.tmdbid = Number(createForm.value.tmdbid)
    if (createForm.value.type === 'tv') {
      if (createForm.value.season) payload.season = parseInt(createForm.value.season, 10) || 0
      if (createForm.value.total_episode > 0) payload.total_episode = createForm.value.total_episode
    }
    if (createForm.value.save_path) payload.save_path = createForm.value.save_path
    if (createForm.value.sites) {
      payload.sites = String(createForm.value.sites)
        .split(/[,，]/)
        .map((s: string) => parseInt(s.trim(), 10))
        .filter((n: number) => !isNaN(n))
    }
    const response = await http.post(`${SERVER_URL}/moviepilot/subscribes`, payload)
    if (response?.data.code === 200) {
      ElMessage.success('订阅添加成功')
      createDialogVisible.value = false
      loadSubscribes()
    } else {
      ElMessage.error(response?.data.message || '添加订阅失败')
    }
  } catch (error) {
    console.error('添加订阅错误：', error)
    ElMessage.error('添加订阅失败')
  } finally {
    creating.value = false
  }
}

const searchSubscribe = async (row: any) => {
  row._searching = true
  try {
    const response = await http.post(`${SERVER_URL}/moviepilot/subscribes/${row.id}/search`)
    if (response?.data.code === 200) {
      ElMessage.success('已触发搜索')
    } else {
      ElMessage.error(response?.data.message || '触发搜索失败')
    }
  } catch (error) {
    console.error('触发搜索错误：', error)
    ElMessage.error('触发搜索失败')
  } finally {
    row._searching = false
  }
}

const toggleSubscribeState = async (row: any) => {
  const target = row.state === 'R' ? 'P' : 'R'
  try {
    const response = await http.put(`${SERVER_URL}/moviepilot/subscribes/${row.id}/status`, { state: target })
    if (response?.data.code === 200) {
      ElMessage.success('订阅状态已更新')
      loadSubscribes()
    } else {
      ElMessage.error(response?.data.message || '更新订阅状态失败')
    }
  } catch (error) {
    console.error('更新订阅状态错误：', error)
    ElMessage.error('更新订阅状态失败')
  }
}

const deleteSubscribe = async (row: any) => {
  try {
    await ElMessageBox.confirm(`确定删除订阅「${row.name}」吗？`, '删除订阅', { type: 'warning' })
    const response = await http.delete(`${SERVER_URL}/moviepilot/subscribes/${row.id}`)
    if (response?.data.code === 200) {
      ElMessage.success('订阅已删除')
      loadSubscribes()
    } else {
      ElMessage.error(response?.data.message || '删除订阅失败')
    }
  } catch (error: any) {
    if (error !== 'cancel' && error !== 'close') {
      console.error('删除订阅错误：', error)
      ElMessage.error('删除订阅失败')
    }
  }
}

const retryUploadTask = async (row: any) => {
  try {
    const response = await http.post(`${SERVER_URL}/moviepilot/upload-tasks/${row.id}/retry`)
    if (response?.data.code === 200) {
      ElMessage.success('已重新加入上传队列')
      loadUploadTasks()
    } else {
      ElMessage.error(response?.data.message || '重试失败')
    }
  } catch (error) {
    console.error('重试上传任务错误：', error)
    ElMessage.error('重试失败')
  }
}

const cancelUploadTask = async (row: any) => {
  try {
    await ElMessageBox.confirm('确定取消该上传任务吗？', '取消上传', { type: 'warning' })
    const response = await http.post(`${SERVER_URL}/moviepilot/upload-tasks/${row.id}/cancel`)
    if (response?.data.code === 200) {
      ElMessage.success('上传任务已取消')
      loadUploadTasks()
    } else {
      ElMessage.error(response?.data.message || '取消失败')
    }
  } catch (error: any) {
    if (error !== 'cancel' && error !== 'close') {
      console.error('取消上传任务错误：', error)
      ElMessage.error('取消失败')
    }
  }
}

onMounted(() => {
  loadSubscribes()
  loadDownloads()
  loadUploadTasks()
})
</script>

<style scoped>
.quick-search-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 0 12px;
}
.quick-results {
  max-height: 320px;
  overflow-y: auto;
  border: 1px solid var(--el-border-color-light);
  border-radius: 6px;
  margin-bottom: 12px;
}
.quick-result-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.quick-result-row:last-child {
  border-bottom: none;
}
.quick-result-row:hover {
  background: var(--el-fill-color-light);
}
.quick-poster {
  width: 56px;
  height: 80px;
  border-radius: 4px;
  flex-shrink: 0;
}
.quick-poster-placeholder {
  width: 56px;
  height: 80px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
}
.quick-info {
  flex: 1;
  min-width: 0;
}
.quick-title {
  font-weight: 600;
  font-size: 14px;
  margin-bottom: 4px;
}
.quick-year {
  color: var(--el-text-color-secondary);
  font-weight: 400;
}
.quick-score {
  margin-left: 8px;
}
.quick-overview {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
