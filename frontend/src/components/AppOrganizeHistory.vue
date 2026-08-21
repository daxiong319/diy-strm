<template>
  <div class="organize-history-container">
    <div class="card-header" style="padding-bottom: 12px">
      <div>
        <h2 class="hide-on-mobile">整理历史</h2>
        <p class="queue-description">
          记录所有整理动作（自动整理 / 目录整理 / 上传整理）。可查看整理结果、手动指定 TMDB 重新整理、按来源与日期清理。
        </p>
      </div>
      <div class="header-actions">
        <el-button type="primary" plain :loading="running" @click="runOrganize">手动整理</el-button>
        <el-button type="warning" plain @click="clearDialogVisible = true">清理记录</el-button>
        <el-button type="info" @click="load" :loading="loading">刷新</el-button>
      </div>
    </div>

    <!-- 来源标签 -->
    <div class="filter-row">
      <el-radio-group v-model="sourceFilter" @change="handleFilterChange">
        <el-radio-button value="">全部</el-radio-button>
        <el-radio-button v-for="s in sources" :key="s.name" :value="s.name">
          {{ s.name }}
          <span v-if="s.count" class="tag-count">{{ s.count }}</span>
        </el-radio-button>
      </el-radio-group>
    </div>

    <!-- 状态筛选 + 搜索 -->
    <div class="filter-row" style="margin-top: 10px">
      <el-select v-model="statusFilter" style="width: 150px" @change="handleFilterChange">
        <el-option label="全部状态" value="" />
        <el-option label="成功" value="success" />
        <el-option label="失败" value="failed" />
        <el-option label="跳过" value="skipped" />
        <el-option label="洗版替换" value="replace" />
        <el-option label="其他" value="unknown" />
      </el-select>
      <el-input
        v-model="keyword"
        placeholder="搜索文件名 / 标题 / 消息"
        clearable
        style="width: 260px; margin-left: 8px"
        @keyup.enter="handleFilterChange"
        @clear="handleFilterChange"
      >
        <template #append>
          <el-button @click="handleFilterChange">搜索</el-button>
        </template>
      </el-input>
      <div style="margin-left: auto">
        <el-button
          type="danger"
          plain
          :disabled="selection.length === 0"
          @click="batchDelete"
          >批量删除（{{ selection.length }}）</el-button
        >
        <el-button
          type="primary"
          plain
          :disabled="selection.length === 0"
          @click="openBatchReorganize"
          >批量重整理</el-button
        >
      </div>
    </div>

    <el-table
      ref="tableRef"
      :data="records"
      v-loading="loading"
      empty-text="暂无整理记录"
      style="width: 100%"
      @selection-change="onSelectionChange"
    >
      <el-table-column type="selection" width="45" />
      <el-table-column label="时间" width="150">
        <template #default="scope">{{ scope.row.event_time }}</template>
      </el-table-column>
      <el-table-column prop="source" label="来源" width="100" />
      <el-table-column label="文件名" min-width="220" show-overflow-tooltip>
        <template #default="scope">
          <span>{{ scope.row.file_name || scope.row.original_file_name }}</span>
          <el-tag
            v-if="scope.row.file_name && scope.row.original_file_name && scope.row.file_name !== scope.row.original_file_name"
            size="small"
            type="info"
            style="margin-left: 4px"
            >已重命名</el-tag
          >
        </template>
      </el-table-column>
      <el-table-column label="媒体信息" min-width="180" show-overflow-tooltip>
        <template #default="scope">
          <template v-if="scope.row.title">
            <span>{{ scope.row.title }}</span>
            <el-tag v-if="scope.row.year" size="small" style="margin-left: 4px">{{ scope.row.year }}</el-tag>
            <el-tag
              size="small"
              :type="scope.row.media_type === 'TV' ? 'success' : 'warning'"
              style="margin-left: 4px"
              >{{ scope.row.media_type === 'TV' ? '剧集' : '电影' }}</el-tag
            >
            <span v-if="scope.row.media_type === 'TV' && scope.row.season_num" style="margin-left: 4px"
              >S{{ scope.row.season_num }}<template v-if="scope.row.episode_num">E{{ scope.row.episode_num }}</template></span
            >
            <span v-if="scope.row.tmdb_id" style="margin-left: 4px; color: var(--el-text-color-secondary)"
              >tmdb={{ scope.row.tmdb_id }}</span
            >
          </template>
          <span v-else>-</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="95">
        <template #default="scope">
          <el-tag :type="statusTagMap[scope.row.status] || 'info'">{{ statusTextMap[scope.row.status] || scope.row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="消息" min-width="200" show-overflow-tooltip>
        <template #default="scope">
          <span :style="scope.row.status === 'failed' ? 'color: var(--el-color-danger)' : ''">{{
            scope.row.error_message || scope.row.message || '-'
          }}</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="220" fixed="right">
        <template #default="scope">
          <el-button size="small" type="primary" plain @click="openDetail(scope.row)">详情</el-button>
          <el-button size="small" type="success" plain @click="openReorganize(scope.row)">重新整理</el-button>
          <el-button size="small" type="danger" plain @click="deleteOne(scope.row)">删除</el-button>
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

    <!-- 详情弹窗 -->
    <el-dialog v-model="detailVisible" title="整理历史详情" width="640px">
      <el-descriptions :column="2" border size="small" v-if="currentRecord">
        <el-descriptions-item label="时间">{{ currentRecord.event_time }}</el-descriptions-item>
        <el-descriptions-item label="来源">{{ currentRecord.source }}</el-descriptions-item>
        <el-descriptions-item label="状态" :span="2">
          <el-tag :type="statusTagMap[currentRecord.status] || 'info'">{{
            statusTextMap[currentRecord.status] || currentRecord.status
          }}</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="原文件名" :span="2">{{ currentRecord.original_file_name || '-' }}</el-descriptions-item>
        <el-descriptions-item label="处理后文件名" :span="2">{{ currentRecord.file_name || '-' }}</el-descriptions-item>
        <el-descriptions-item label="标题" :span="2">{{ currentRecord.title || '-' }}</el-descriptions-item>
        <el-descriptions-item label="年份">{{ currentRecord.year || '-' }}</el-descriptions-item>
        <el-descriptions-item label="媒体类型">{{ currentRecord.media_type || '-' }}</el-descriptions-item>
        <el-descriptions-item v-if="currentRecord.media_type === 'TV'" label="季/集">
          S{{ currentRecord.season_num || '-' }}<template v-if="currentRecord.episode_num"> E{{ currentRecord.episode_num }}</template>
        </el-descriptions-item>
        <el-descriptions-item label="TMDB ID">{{ currentRecord.tmdb_id || '-' }}</el-descriptions-item>
        <el-descriptions-item label="源路径" :span="2">{{ currentRecord.source_path || '-' }}</el-descriptions-item>
        <el-descriptions-item label="目标路径" :span="2">{{ currentRecord.target_path || '-' }}</el-descriptions-item>
        <el-descriptions-item label="消息" :span="2">{{ currentRecord.message || '-' }}</el-descriptions-item>
        <el-descriptions-item v-if="currentRecord.error_message" label="错误信息" :span="2">
          <span style="color: var(--el-color-danger)">{{ currentRecord.error_message }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="账号 ID">{{ currentRecord.extra?.account_id || '-' }}</el-descriptions-item>
        <el-descriptions-item label="任务 ID">{{ currentRecord.extra?.task_id || '-' }}</el-descriptions-item>
      </el-descriptions>
    </el-dialog>

    <!-- 重新整理弹窗（单条） -->
    <el-dialog v-model="reorganizeVisible" title="重新整理（手动指定 TMDB）" width="620px" :close-on-click-modal="false">
      <el-form :model="reorganizeForm" label-width="100px">
        <el-form-item label="原文件名">
          <el-input :model-value="reorganizeForm.file_name" disabled />
        </el-form-item>
        <el-form-item label="媒体类型" required>
          <el-radio-group v-model="reorganizeForm.media_type">
            <el-radio value="movie">电影</el-radio>
            <el-radio value="tv">剧集</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="标题" required>
          <el-input v-model="reorganizeForm.title" placeholder="媒体标题，如：遮天" />
        </el-form-item>
        <el-form-item label="年份">
          <el-input-number v-model="reorganizeForm.year" :min="0" :max="2100" :controls="false" placeholder="可选" style="width: 100%" />
        </el-form-item>
        <el-form-item label="TMDB ID" required>
          <el-input-number v-model="reorganizeForm.tmdb_id" :min="1" :controls="false" placeholder="如：287496" style="width: 100%" />
        </el-form-item>
        <el-form-item label="识别测试">
          <div style="width: 100%">
            <div style="display: flex; gap: 8px">
              <el-input v-model="testFileName" placeholder="输入文件名测试识别，获取 TMDB 候选" />
              <el-button :loading="testing" @click="runRecognizeTest">测试</el-button>
            </div>
            <div v-if="testCandidates.length" style="margin-top: 8px; max-height: 200px; overflow-y: auto">
              <div
                v-for="cand in testCandidates"
                :key="cand.tmdb_id"
                class="candidate-item"
                :class="{ active: reorganizeForm.tmdb_id === cand.tmdb_id }"
                @click="selectCandidate(cand)"
              >
                <span>{{ cand.title }}</span>
                <span v-if="cand.year" style="margin-left: 4px; color: var(--el-text-color-secondary)">{{ cand.year }}</span>
                <el-tag size="small" :type="cand.media_type === 'tv' ? 'success' : 'warning'" style="margin-left: 6px">{{
                  cand.media_type === 'tv' ? '剧集' : '电影'
                }}</el-tag>
                <span style="margin-left: auto; color: var(--el-text-color-secondary)">tmdb={{ cand.tmdb_id }}</span>
              </div>
            </div>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="reorganizeVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="submitReorganize">提交重整理</el-button>
      </template>
    </el-dialog>

    <!-- 清理弹窗 -->
    <el-dialog v-model="clearDialogVisible" title="清理整理历史" width="480px">
      <el-form :model="clearForm" label-width="100px">
        <el-form-item label="来源">
          <el-select v-model="clearForm.source" style="width: 100%" placeholder="全部来源">
            <el-option label="全部来源" value="" />
            <el-option v-for="s in sources" :key="s.name" :label="s.name" :value="s.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="开始日期">
          <el-date-picker v-model="clearForm.start_date" type="date" value-format="YYYY-MM-DD" style="width: 100%" placeholder="不限制" />
        </el-form-item>
        <el-form-item label="结束日期">
          <el-date-picker v-model="clearForm.end_date" type="date" value-format="YYYY-MM-DD" style="width: 100%" placeholder="不限制" />
        </el-form-item>
        <el-form-item label="清空全部">
          <el-checkbox v-model="clearForm.clear_all">一键清空所有整理历史（慎用）</el-checkbox>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="clearDialogVisible = false">取消</el-button>
        <el-button type="danger" :loading="clearing" @click="submitClear">确认清理</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'

const http = useHttpClient()

const records = ref<any[]>([])
const sources = ref<any[]>([])
const loading = ref(false)
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const sourceFilter = ref('')
const statusFilter = ref('')
const keyword = ref('')
const selection = ref<any[]>([])

const statusTextMap: Record<string, string> = {
  success: '成功',
  failed: '失败',
  skipped: '跳过',
  replace: '洗版替换',
  unknown: '其他',
}
const statusTagMap: Record<string, string> = {
  success: 'success',
  failed: 'danger',
  skipped: 'info',
  replace: 'warning',
  unknown: 'default',
}

const load = async () => {
  loading.value = true
  try {
    const resp = await http.get(`${SERVER_URL}/organize-history`, {
      params: {
        page: page.value,
        page_size: pageSize.value,
        source: sourceFilter.value,
        status: statusFilter.value,
        keyword: keyword.value,
      },
    })
    if (resp?.data?.code === 200) {
      records.value = resp.data.data?.items || []
      total.value = Number(resp.data.data?.total || 0)
      sources.value = resp.data.data?.sources || []
    } else {
      ElMessage.error(resp?.data?.message || '加载整理历史失败')
    }
  } catch (e: any) {
    ElMessage.error('加载整理历史失败：' + (e?.message || ''))
  } finally {
    loading.value = false
  }
}

const handleFilterChange = () => {
  page.value = 1
  load()
}
const handlePageChange = (p: number) => {
  page.value = p
  load()
}
const onSelectionChange = (rows: any[]) => {
  selection.value = rows
}

// ---------- 详情 ----------
const detailVisible = ref(false)
const currentRecord = ref<any>(null)
const openDetail = (row: any) => {
  currentRecord.value = row
  detailVisible.value = true
}

// ---------- 删除 ----------
const deleteOne = async (row: any) => {
  try {
    await ElMessageBox.confirm(`确定删除该整理记录（${row.original_file_name || row.file_name}）吗？`, '删除确认', { type: 'warning' })
  } catch {
    return
  }
  const resp = await http.post(`${SERVER_URL}/organize-history/delete`, { id: row.id })
  if (resp?.data?.code === 200) {
    ElMessage.success(resp.data.message || '已删除')
    load()
  } else {
    ElMessage.error(resp?.data?.message || '删除失败')
  }
}

const batchDelete = async () => {
  if (selection.value.length === 0) return
  try {
    await ElMessageBox.confirm(`确定删除选中的 ${selection.value.length} 条记录吗？`, '批量删除确认', { type: 'warning' })
  } catch {
    return
  }
  const resp = await http.post(`${SERVER_URL}/organize-history/delete`, {
    ids: selection.value.map((r) => r.id),
  })
  if (resp?.data?.code === 200) {
    ElMessage.success(resp.data.message || '已删除')
    load()
  } else {
    ElMessage.error(resp?.data?.message || '删除失败')
  }
}

// ---------- 清理 ----------
const clearDialogVisible = ref(false)
const clearing = ref(false)
const clearForm = ref<any>({ source: '', start_date: '', end_date: '', clear_all: false })
const submitClear = async () => {
  if (!clearForm.value.clear_all && !clearForm.value.source && !clearForm.value.start_date && !clearForm.value.end_date) {
    ElMessage.warning('请选择清理范围（来源/日期/一键清空）')
    return
  }
  clearing.value = true
  try {
    const resp = await http.post(`${SERVER_URL}/organize-history/clear`, clearForm.value)
    if (resp?.data?.code === 200) {
      ElMessage.success(resp.data.message || '清理完成')
      clearDialogVisible.value = false
      clearForm.value = { source: '', start_date: '', end_date: '', clear_all: false }
      load()
    } else {
      ElMessage.error(resp?.data?.message || '清理失败')
    }
  } finally {
    clearing.value = false
  }
}

// ---------- 手动整理 ----------
const running = ref(false)
const runOrganize = async () => {
  running.value = true
  try {
    const resp = await http.post(`${SERVER_URL}/organize-history/run`, {})
    if (resp?.data?.code === 200) {
      ElMessage.success({ message: resp.data.message || '已开始整理，稍后刷新查看结果', duration: 8000, showClose: true })
    } else {
      ElMessage.error(resp?.data?.message || '触发失败')
    }
  } catch (e: any) {
    ElMessage.error('触发失败：' + (e?.message || ''))
  } finally {
    running.value = false
  }
}

// ---------- 重新整理 ----------
const reorganizeVisible = ref(false)
const submitting = ref(false)
const testing = ref(false)
const currentReorganizeRows = ref<any[]>([])
const testFileName = ref('')
const testCandidates = ref<any[]>([])
const reorganizeForm = ref<any>({ media_type: 'tv', title: '', year: 0, tmdb_id: 0 })

const openReorganize = (row: any) => {
  currentReorganizeRows.value = [row]
  reorganizeForm.value = {
    media_type: row.media_type === 'Movie' ? 'movie' : row.media_type === 'TV' ? 'tv' : 'tv',
    title: row.title || '',
    year: row.year || 0,
    tmdb_id: 0,
  }
  testFileName.value = row.original_file_name || row.file_name || ''
  testCandidates.value = []
  reorganizeVisible.value = true
}

const openBatchReorganize = () => {
  const notTv = selection.value.filter((r) => r.media_type !== 'TV')
  if (notTv.length > 0) {
    ElMessage.warning('批量重整理仅支持剧集记录，已排除 ' + notTv.length + ' 条非剧集记录')
  }
  const tvRows = selection.value.filter((r) => r.media_type === 'TV')
  if (tvRows.length === 0) {
    ElMessage.warning('请选择剧集记录进行批量重整理')
    return
  }
  currentReorganizeRows.value = tvRows
  reorganizeForm.value = { media_type: 'tv', title: tvRows[0].title || '', year: tvRows[0].year || 0, tmdb_id: 0 }
  testFileName.value = ''
  testCandidates.value = []
  reorganizeVisible.value = true
}

const runRecognizeTest = async () => {
  if (!testFileName.value.trim()) {
    ElMessage.warning('请输入文件名')
    return
  }
  testing.value = true
  testCandidates.value = []
  try {
    const resp = await http.post(`${SERVER_URL}/organize-history/recognize-test`, {
      file_name: testFileName.value.trim(),
      media_type: reorganizeForm.value.media_type,
    })
    if (resp?.data?.code === 200) {
      const data = resp.data.data
      testCandidates.value = data?.candidates || []
      if (data?.title) {
        reorganizeForm.value.title = data.title
      }
      if (data?.year) {
        reorganizeForm.value.year = data.year
      }
      if (testCandidates.value.length === 0) {
        ElMessage.warning('未找到 TMDB 候选，请手动填写')
      }
    } else {
      ElMessage.error(resp?.data?.message || '识别测试失败')
    }
  } catch (e: any) {
    ElMessage.error('识别测试失败：' + (e?.message || ''))
  } finally {
    testing.value = false
  }
}

const selectCandidate = (cand: any) => {
  reorganizeForm.value.tmdb_id = cand.tmdb_id
  reorganizeForm.value.title = cand.title
  reorganizeForm.value.year = cand.year || 0
  reorganizeForm.value.media_type = cand.media_type
}

const submitReorganize = async () => {
  if (!reorganizeForm.value.title.trim()) {
    ElMessage.warning('请填写媒体标题')
    return
  }
  if (!reorganizeForm.value.tmdb_id) {
    ElMessage.warning('请填写 TMDB ID（可用识别测试获取）')
    return
  }
  submitting.value = true
  try {
    const resp = await http.post(`${SERVER_URL}/organize-history/reorganize`, {
      ids: currentReorganizeRows.value.map((r) => r.id),
      tmdb_id: reorganizeForm.value.tmdb_id,
      title: reorganizeForm.value.title.trim(),
      year: reorganizeForm.value.year || 0,
      media_type: reorganizeForm.value.media_type,
    })
    if (resp?.data?.code === 200) {
      const taskId = resp.data.data?.task_id
      ElMessage.success({ message: `已提交重整理任务（${resp.data.data?.total || 0} 条），处理中…`, duration: 8000, showClose: true })
      reorganizeVisible.value = false
      if (taskId) {
        pollTaskStatus(taskId)
      }
    } else {
      ElMessage.error(resp?.data?.message || '提交失败')
    }
  } catch (e: any) {
    ElMessage.error('提交失败：' + (e?.message || ''))
  } finally {
    submitting.value = false
  }
}

// ---------- 任务轮询 ----------
let taskTimer: any = null
const pollTaskStatus = async (taskId: string) => {
  if (taskTimer) {
    clearInterval(taskTimer)
  }
  const check = async () => {
    try {
      const resp = await http.get(`${SERVER_URL}/organize-history/task-status`, {
        params: { task_id: taskId },
      })
      const st = resp?.data?.data
      if (!st) {
        clearInterval(taskTimer)
        taskTimer = null
        return
      }
      if (st.status === 'success') {
        clearInterval(taskTimer)
        taskTimer = null
        ElMessage.success(st.message || '重整理完成')
        load()
      } else if (st.status === 'failed') {
        clearInterval(taskTimer)
        taskTimer = null
        ElMessage.error(st.message || '重整理失败')
        load()
      }
      // running 继续轮询
    } catch {
      // 忽略单次轮询错误
    }
  }
  taskTimer = setInterval(check, 1500)
}

onMounted(() => {
  load()
})
onUnmounted(() => {
  if (taskTimer) {
    clearInterval(taskTimer)
    taskTimer = null
  }
})
</script>

<style scoped>
.organize-history-container {
  padding: 4px;
}
.filter-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.tag-count {
  margin-left: 4px;
  font-size: 12px;
  opacity: 0.7;
}
.candidate-item {
  display: flex;
  align-items: center;
  padding: 6px 10px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  margin-bottom: 6px;
  cursor: pointer;
  transition: all 0.2s;
}
.candidate-item:hover {
  border-color: var(--el-color-primary);
}
.candidate-item.active {
  border-color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
}
.header-actions {
  display: flex;
  gap: 8px;
}
</style>
