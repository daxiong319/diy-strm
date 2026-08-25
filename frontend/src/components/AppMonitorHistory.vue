<template>
  <div class="monitor-history-container">
    <div class="card-header" style="padding-bottom: 12px">
      <div>
        <h2 class="hide-on-mobile">监控历史</h2>
        <p class="queue-description">
          TG 频道订阅 / 影巢订阅 / TG 机器人监控转存记录（成功 / 失败 / 跳过全量审计）。可按网盘来源、状态筛选与结果搜索。
        </p>
      </div>
      <div class="header-actions">
        <el-switch v-model="autoRefresh" active-text="自动刷新" style="margin-right: 12px" />
        <el-button type="warning" plain @click="clearDialogVisible = true">清理记录</el-button>
        <el-button type="info" @click="load" :loading="loading">刷新</el-button>
      </div>
    </div>

    <!-- 来源标签（对齐 tgto123：115/123/光鸭/天翼 → 本项目 123/光鸭/移动） -->
    <div class="filter-row">
      <el-radio-group v-model="sourceFilter" @change="handleFilterChange">
        <el-radio-button value="">全部</el-radio-button>
        <el-radio-button v-for="s in sources" :key="s.name" :value="s.name">
          {{ s.label }}
          <span v-if="s.count" class="tag-count">{{ s.count }}</span>
        </el-radio-button>
      </el-radio-group>
    </div>

    <!-- 状态筛选 + 结果搜索 -->
    <div class="filter-row" style="margin-top: 10px">
      <el-select v-model="statusFilter" style="width: 160px" @change="handleFilterChange">
        <el-option label="全部状态" value="" />
        <el-option v-for="st in statusOptions" :key="st" :label="`${st}（${statusCounts[st] || 0}）`" :value="st" />
      </el-select>
      <el-input
        v-model="keyword"
        placeholder="搜索「结果」列关键字"
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
        <el-button type="danger" plain :disabled="selection.length === 0" @click="batchDelete"
          >批量删除（{{ selection.length }}）</el-button
        >
      </div>
    </div>

    <el-table :data="records" v-loading="loading" style="margin-top: 10px" @selection-change="onSelectionChange">
      <el-table-column type="selection" width="45" />
      <el-table-column label="时间" width="160" sortable prop="transfer_time">
        <template #default="scope">{{ scope.row.transfer_time }}</template>
      </el-table-column>
      <el-table-column label="来源" width="100">
        <template #default="scope">{{ scope.row.source_label }}</template>
      </el-table-column>
      <el-table-column label="入口" width="95">
        <template #default="scope">{{ scope.row.entry_label }}</template>
      </el-table-column>
      <el-table-column label="影片" min-width="190" show-overflow-tooltip>
        <template #default="scope">
          <div v-if="scope.row.tmdb_id || scope.row.title">
            <a v-if="scope.row.tmdb_id" :href="tmdbUrl(scope.row)" target="_blank" rel="noopener noreferrer" class="table-link">
              {{ scope.row.title || ('TMDB ' + scope.row.tmdb_id) }}
            </a>
            <span v-else>{{ scope.row.title }}</span>
            <div class="media-meta">
              <el-tag v-if="scope.row.media_type" size="small" effect="plain">{{ mediaTypeLabel(scope.row.media_type) }}</el-tag>
              <span v-if="scope.row.season || scope.row.episode" class="media-se">
                {{ scope.row.season ? 'S' + scope.row.season : '' }}{{ scope.row.episode ? ' ' + scope.row.episode : '' }}
              </span>
            </div>
          </div>
          <span v-else>-</span>
        </template>
      </el-table-column>
      <el-table-column label="频道" min-width="120" show-overflow-tooltip>
        <template #default="scope">{{ scope.row.channel || '-' }}</template>
      </el-table-column>
      <el-table-column label="消息ID" width="90">
        <template #default="scope">{{ scope.row.message_id || '-' }}</template>
      </el-table-column>
      <el-table-column label="状态" width="95">
        <template #default="scope">
          <el-tag :type="statusTagType(scope.row.transfer_status)">{{ scope.row.transfer_status || '-' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="结果" min-width="220" show-overflow-tooltip>
        <template #default="scope">
          <span :style="scope.row.transfer_status === '转存失败' ? 'color: var(--el-color-danger)' : ''">{{
            scope.row.transfer_result || '-'
          }}</span>
        </template>
      </el-table-column>
      <el-table-column label="消息链接" width="85">
        <template #default="scope">
          <a v-if="scope.row.message_url" :href="scope.row.message_url" target="_blank" rel="noopener noreferrer" class="table-link">查看</a>
          <span v-else>-</span>
        </template>
      </el-table-column>
      <el-table-column label="网盘链接" width="85">
        <template #default="scope">
          <a v-if="scope.row.target_url" :href="scope.row.target_url" target="_blank" rel="noopener noreferrer" class="table-link">打开</a>
          <span v-else>-</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="80" fixed="right">
        <template #default="scope">
          <el-button size="small" type="danger" plain @click="deleteOne(scope.row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <div style="margin-top: 12px; display: flex; justify-content: space-between; align-items: center">
      <span class="queue-description">总记录 {{ total }} 条</span>
      <el-pagination
        layout="prev, pager, next, total"
        :total="total"
        :page-size="pageSize"
        :current-page="page"
        @current-change="handlePageChange"
      />
    </div>

    <!-- 清理弹窗 -->
    <el-dialog v-model="clearDialogVisible" title="清理监控历史" width="480px">
      <el-form :model="clearForm" label-width="100px">
        <el-form-item label="来源">
          <el-select v-model="clearForm.source" style="width: 100%">
            <el-option label="全部来源" value="" />
            <el-option v-for="s in sources" :key="s.name" :label="s.label" :value="s.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="起始日期">
          <el-date-picker v-model="clearForm.startDate" type="date" value-format="YYYY-MM-DD" placeholder="可选" style="width: 100%" />
        </el-form-item>
        <el-form-item label="结束日期">
          <el-date-picker v-model="clearForm.endDate" type="date" value-format="YYYY-MM-DD" placeholder="可选" style="width: 100%" />
        </el-form-item>
        <el-form-item>
          <el-checkbox v-model="clearForm.clearAll">一键清空（忽略来源与日期）</el-checkbox>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="clearDialogVisible = false">取消</el-button>
        <el-button type="danger" :loading="clearing" @click="doClear">清理</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'

const http = useHttpClient()

const records = ref<any[]>([])
const sources = ref<any[]>([])
const statusOptions = ref<string[]>([])
const statusCounts = ref<Record<string, number>>({})
const loading = ref(false)
const total = ref(0)
const page = ref(1)
const pageSize = ref(30)
const sourceFilter = ref('')
const statusFilter = ref('')
const keyword = ref('')
const selection = ref<any[]>([])

// 自动刷新（对齐 tgto123 的 180 秒静默刷新）
const autoRefresh = ref(false)
let refreshTimer: ReturnType<typeof setInterval> | null = null
const toggleAutoRefresh = (on: boolean) => {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
  if (on) {
    refreshTimer = setInterval(() => load(true), 180000)
  }
}
watch(autoRefresh, toggleAutoRefresh)

const statusTagType = (status: string): string => {
  switch (status) {
    case '转存成功':
      return 'success'
    case '转存失败':
      return 'danger'
    case '已跳过':
      return 'info'
    case '洗版替换':
      return 'warning'
    default:
      return 'info'
  }
}

// 影片类型中文
const mediaTypeLabel = (t: string): string => {
  if (t === 'tv') return '剧集'
  if (t === 'movie') return '电影'
  return t || ''
}

// 构造 TMDB 影片详情链接（按类型区分 movie/tv）
const tmdbUrl = (row: any): string => {
  const type = row.media_type === 'tv' ? 'tv' : 'movie'
  return `https://www.themoviedb.org/${type}/${row.tmdb_id}`
}

const load = async (silent = false) => {
  if (!silent) loading.value = true
  try {
    const resp = await http.get(`${SERVER_URL}/monitor-history`, {
      params: {
        page: page.value,
        page_size: pageSize.value,
        source: sourceFilter.value,
        status: statusFilter.value,
        keyword: keyword.value,
      },
    })
    if (resp?.data?.code === 200) {
      const data = resp.data.data || {}
      records.value = data.items || []
      total.value = Number(data.total || 0)
      sources.value = data.sources || []
      statusOptions.value = data.status_options || []
      statusCounts.value = data.status_counts || {}
    } else if (!silent) {
      ElMessage.error(resp?.data?.message || '加载监控历史失败')
    }
  } catch (e: any) {
    if (!silent) ElMessage.error('加载监控历史失败：' + (e?.message || ''))
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

// ---------- 删除 ----------
const deleteOne = async (row: any) => {
  try {
    await ElMessageBox.confirm(`确定删除这条「${row.transfer_status}」记录吗？`, '删除确认', { type: 'warning' })
  } catch {
    return
  }
  const resp = await http.post(`${SERVER_URL}/monitor-history/delete`, { id: row.id })
  if (resp?.data?.code === 200) {
    ElMessage.success('已删除')
    load()
  } else {
    ElMessage.error(resp?.data?.message || '删除失败')
  }
}

const batchDelete = async () => {
  try {
    await ElMessageBox.confirm(`确定删除选中的 ${selection.value.length} 条记录吗？`, '批量删除', { type: 'warning' })
  } catch {
    return
  }
  const resp = await http.post(`${SERVER_URL}/monitor-history/delete`, {
    ids: selection.value.map((r) => r.id),
  })
  if (resp?.data?.code === 200) {
    ElMessage.success('已删除')
    load()
  } else {
    ElMessage.error(resp?.data?.message || '删除失败')
  }
}

// ---------- 清理 ----------
const clearDialogVisible = ref(false)
const clearing = ref(false)
const clearForm = ref({ source: '', startDate: '', endDate: '', clearAll: false })
const doClear = async () => {
  if (
    !clearForm.value.clearAll &&
    !clearForm.value.source &&
    !clearForm.value.startDate &&
    !clearForm.value.endDate
  ) {
    ElMessage.warning('请指定来源或日期范围，或勾选一键清空')
    return
  }
  try {
    await ElMessageBox.confirm('清理后不可恢复，确定继续吗？', '清理确认', { type: 'warning' })
  } catch {
    return
  }
  clearing.value = true
  try {
    const resp = await http.post(`${SERVER_URL}/monitor-history/clear`, {
      source: clearForm.value.source,
      start_date: clearForm.value.startDate,
      end_date: clearForm.value.endDate,
      clear_all: clearForm.value.clearAll,
    })
    if (resp?.data?.code === 200) {
      ElMessage.success(resp.data.message || '清理完成')
      clearDialogVisible.value = false
      page.value = 1
      load()
    } else {
      ElMessage.error(resp?.data?.message || '清理失败')
    }
  } finally {
    clearing.value = false
  }
}

onMounted(() => load())
onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
})
</script>

<style scoped>
.monitor-history-container {
  padding: 16px;
}
.filter-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.tag-count {
  margin-left: 4px;
  opacity: 0.75;
  font-size: 12px;
}
.table-link {
  color: var(--el-color-primary);
  text-decoration: none;
}
.table-link:hover {
  text-decoration: underline;
}
.media-meta {
  margin-top: 2px;
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.media-se {
  white-space: nowrap;
}
</style>
