<template>
  <div class="cas-page">
    <div class="page-header">
      <div>
        <h2>CAS 秒传管理</h2>
        <p class="page-desc">影视上传满 {{ config.age_days }} 天自动生成 .cas 清单并删除云端源视频释放空间；需要时凭清单一键秒传恢复。</p>
      </div>
      <div class="header-actions">
        <el-button :loading="running" type="primary" @click="runOnce">{{ running ? '执行中…' : '立即执行一轮' }}</el-button>
        <el-button @click="showConfig = true">自动化配置</el-button>
        <el-button type="success" @click="showRestore = true">秒传恢复</el-button>
      </div>
    </div>

    <!-- 筛选 -->
    <div class="filter-row">
      <el-radio-group v-model="filterStatus" @change="load(1)">
        <el-radio-button value="">全部</el-radio-button>
        <el-radio-button value="active">已删源</el-radio-button>
        <el-radio-button value="restored">已恢复</el-radio-button>
        <el-radio-button value="pending">删除失败</el-radio-button>
      </el-radio-group>
      <el-input v-model="keyword" placeholder="按文件名搜索" clearable style="width: 260px" @keyup.enter="load(1)" @clear="load(1)" />
      <el-button @click="load(1)">查询</el-button>
    </div>

    <!-- 列表 -->
    <el-table v-loading="loading" :data="items" class="cas-table" empty-text="暂无 CAS 记录">
      <el-table-column prop="file_name" label="文件" min-width="320" show-overflow-tooltip />
      <el-table-column label="大小" width="110">
        <template #default="{ row }">{{ formatSize(row.file_size) }}</template>
      </el-table-column>
      <el-table-column label="状态" width="110">
        <template #default="{ row }">
          <el-tag :type="statusTag(row.status)">{{ statusLabel(row.status) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="删源时间" width="170">
        <template #default="{ row }">{{ row.deleted_at ? formatTime(row.deleted_at) : '—' }}</template>
      </el-table-column>
      <el-table-column label="恢复时间" width="170">
        <template #default="{ row }">{{ row.restored_at ? formatTime(row.restored_at) : '—' }}</template>
      </el-table-column>
      <el-table-column label="操作" width="280" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" :disabled="row.status === 'restored'" @click="restore(row)">
            {{ row.status === 'restored' ? '已恢复' : '秒传恢复' }}
          </el-button>
          <el-button size="small" @click="exportCas(row)">导出 .cas</el-button>
          <el-button size="small" type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-pagination
      v-model:current-page="page"
      :page-size="pageSize"
      :total="total"
      layout="total, prev, pager, next"
      class="cas-pagination"
      @current-change="load"
    />

    <!-- 秒传恢复对话框 -->
    <el-dialog v-model="showRestore" title="秒传恢复（粘贴 CAS）" width="560px">
      <el-form label-width="100px">
        <el-form-item label="目标账号">
          <el-select v-model="restoreForm.account_id" placeholder="选择天翼云盘账号" style="width: 100%">
            <el-option v-for="a in cloud189Accounts" :key="a.id" :label="a.name" :value="a.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="目标目录 ID">
          <el-input v-model="restoreForm.target_folder_id" placeholder="留空恢复到个人网盘根目录" clearable />
        </el-form-item>
        <el-form-item label="CAS 内容">
          <el-input v-model="restoreForm.cas_content" type="textarea" :rows="8" placeholder="粘贴 .cas 文件内容（JSON / Base64 / cloud189:// / 管道符格式均可）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showRestore = false">取消</el-button>
        <el-button type="primary" :loading="restoring" @click="doRestore">{{ restoring ? '秒传中…' : '秒传恢复' }}</el-button>
      </template>
    </el-dialog>

    <!-- 自动化配置对话框 -->
    <el-dialog v-model="showConfig" title="CAS 自动化配置" width="480px">
      <el-form label-width="160px">
        <el-form-item label="启用自动 CAS 化">
          <el-switch v-model="config.enabled" />
        </el-form-item>
        <el-form-item label="满多少天触发">
          <el-input-number v-model="config.age_days" :min="1" :max="365" />
        </el-form-item>
        <el-form-item label=".cas 写回网盘">
          <el-switch v-model="config.write_back_cloud" />
          <div class="config-tip">开启后除 DB 外另在本地缓存目录保存 .cas 文件（默认仅 DB 记录）</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showConfig = false">取消</el-button>
        <el-button type="primary" @click="saveConfig">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { http } from '@/http/client'
import { SERVER_URL } from '@/const'

interface CasRecord {
  id: number
  file_name: string
  file_size: number
  status: string
  deleted_at: number
  restored_at: number
  cas_content: string
  account_id: number
}

const items = ref<CasRecord[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 50
const loading = ref(false)
const running = ref(false)
const filterStatus = ref('')
const keyword = ref('')
const showRestore = ref(false)
const showConfig = ref(false)
const restoring = ref(false)
const accounts = ref<any[]>([])
const config = ref({ enabled: true, age_days: 30, write_back_cloud: false })
const restoreForm = ref({ account_id: 0 as number, target_folder_id: '', cas_content: '' })

const cloud189Accounts = computed(() => accounts.value.filter((a) => a.source_type === 'cloud189'))

const load = async (p = 1) => {
  page.value = p
  loading.value = true
  try {
    const resp = await http.get(`${SERVER_URL}/cas/records`, {
      params: { page: page.value, page_size: pageSize, status: filterStatus.value, keyword: keyword.value },
    })
    if (resp?.data?.code === 200) {
      items.value = resp.data.data?.items || []
      total.value = resp.data.data?.total || 0
    } else {
      ElMessage.error(resp?.data?.message || '加载失败')
    }
  } finally {
    loading.value = false
  }
}

const loadAccounts = async () => {
  try {
    const resp = await http.get(`${SERVER_URL}/account/list`)
    if (resp?.data?.code === 200) accounts.value = resp.data.data || []
  } catch { /* 忽略 */ }
}

const loadConfig = async () => {
  try {
    const resp = await http.get(`${SERVER_URL}/cas/config`)
    if (resp?.data?.code === 200 && resp.data.data) Object.assign(config.value, resp.data.data)
  } catch { /* 忽略 */ }
}

const saveConfig = async () => {
  try {
    const resp = await http.post(`${SERVER_URL}/cas/config`, config.value)
    if (resp?.data?.code === 200) {
      ElMessage.success('已保存')
      showConfig.value = false
    } else ElMessage.error(resp?.data?.message || '保存失败')
  } catch (e: any) {
    ElMessage.error(e?.response?.data?.message || '保存失败')
  }
}

const runOnce = async () => {
  running.value = true
  try {
    const resp = await http.post(`${SERVER_URL}/cas/run`)
    const d = resp?.data
    if (d?.code === 200) {
      const r = d.data || {}
      ElMessage.success(`完成：生成 ${r.generated} 个清单，删除 ${r.deleted} 个源视频，失败 ${r.failed} 个`)
      load(1)
    } else ElMessage.error(d?.message || '执行失败')
  } finally {
    running.value = false
  }
}

const restore = async (row: CasRecord) => {
  const confirmed = await ElMessageBox.confirm(`确定把「${row.file_name}」秒传恢复到网盘吗？`, '秒传恢复', { type: 'info' }).catch(() => null)
  if (!confirmed) return
  restoring.value = true
  try {
    const resp = await http.post(`${SERVER_URL}/cas/records/${row.id}/restore`, { target_folder_id: '' })
    const d = resp?.data
    if (d?.code === 200) {
      ElMessage.success('秒传成功')
      load(page.value)
    } else ElMessage.error(d?.message || '秒传恢复失败')
  } finally {
    restoring.value = false
  }
}

const doRestore = async () => {
  if (!restoreForm.value.account_id || !restoreForm.value.cas_content) {
    ElMessage.warning('请选择账号并粘贴 CAS 内容')
    return
  }
  restoring.value = true
  try {
    const resp = await http.post(`${SERVER_URL}/cas/restore`, restoreForm.value)
    const d = resp?.data
    if (d?.code === 200) {
      ElMessage.success('秒传成功')
      showRestore.value = false
      load(1)
    } else ElMessage.error(d?.message || '秒传恢复失败')
  } finally {
    restoring.value = false
  }
}

const exportCas = (row: CasRecord) => {
  const blob = new Blob([row.cas_content], { type: 'application/json' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = row.file_name.endsWith('.cas') ? row.file_name : row.file_name + '.cas'
  a.click()
  URL.revokeObjectURL(a.href)
}

const remove = async (row: CasRecord) => {
  const confirmed = await ElMessageBox.confirm(`确定删除「${row.file_name}」的 CAS 记录吗？（不影响云端）`, '删除确认', { type: 'warning' }).catch(() => null)
  if (!confirmed) return
  const resp = await http.delete(`${SERVER_URL}/cas/records/${row.id}`)
  if (resp?.data?.code === 200) {
    ElMessage.success('已删除')
    load(page.value)
  } else ElMessage.error(resp?.data?.message || '删除失败')
}

const formatSize = (n: number) => {
  if (!n) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(i ? 1 : 0) + ' ' + units[i]
}
const formatTime = (ts: number) => new Date(ts * 1000).toLocaleString('zh-CN', { hour12: false })
const statusLabel = (s: string) => ({ active: '已删源', restored: '已恢复', pending: '删除失败' }[s] || s)
const statusTag = (s: string) => ({ active: 'success', restored: 'info', pending: 'danger' }[s] || 'info')

onMounted(() => {
  load(1)
  loadAccounts()
  loadConfig()
})
</script>

<style scoped>
.cas-page { padding: 16px; }
.page-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; }
.page-header h2 { margin: 0 0 6px; }
.page-desc { color: var(--el-text-color-secondary); margin: 0; font-size: 13px; }
.header-actions { display: flex; gap: 8px; flex-shrink: 0; }
.filter-row { display: flex; gap: 12px; align-items: center; margin-bottom: 14px; }
.cas-table { width: 100%; }
.cas-pagination { margin-top: 14px; justify-content: flex-end; }
.config-tip { font-size: 12px; color: var(--el-text-color-secondary); margin-top: 4px; }
</style>
