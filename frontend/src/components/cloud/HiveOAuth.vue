<template>
  <div class="main-content-container cloud-page">
    <!-- 主账号 OAuth 授权 -->
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>影巢 · OAuth 授权</span>
          <span v-if="main.user" class="nickname">{{ main.user.nickname }}</span>
        </div>
      </template>

      <el-alert
        type="info"
        :closable="false"
        class="cloud-alert"
        title="完成影巢 OAuth 授权后，可启用每日自动签到（普通/赌狗模式）。授权前请先到 hdhive.com 注册影巢账号。"
        show-icon
      />

      <div v-if="mainLoading" class="loading-box">
        <el-skeleton :rows="3" animated />
      </div>

      <template v-else>
        <!-- 未授权 -->
        <template v-if="!main.authorized">
          <el-empty description="尚未完成影巢 OAuth 授权" :image-size="80">
            <el-button type="primary" :loading="authing" @click="openAuth()">前往授权</el-button>
            <el-button :loading="refreshing" @click="refreshMain">刷新状态</el-button>
          </el-empty>
        </template>

        <!-- 已授权：用户快照 -->
        <template v-else>
          <el-descriptions :column="2" border class="user-snapshot">
            <el-descriptions-item label="账号">
              {{ main.user?.nickname || '-' }}
            </el-descriptions-item>
            <el-descriptions-item label="等级">
              {{ main.user?.level || '-' }}
              <el-tag v-if="main.user?.is_forever_vip" size="small" type="warning" class="vip-tag">终身VIP</el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="积分">{{ formatNum(main.user?.points) }}</el-descriptions-item>
            <el-descriptions-item label="签到">
              {{ main.user?.checked_in_today ? '已签到' : '未签到' }}（累计 {{ main.user?.checkin_days_total || 0 }} 天）
            </el-descriptions-item>
            <el-descriptions-item label="周免费额度">
              {{ main.user?.weekly_free_quota_unlimited ? '不限' : formatQuota(main.user) }}
            </el-descriptions-item>
            <el-descriptions-item label="分享数">{{ main.user?.share_num ?? '-' }}</el-descriptions-item>
          </el-descriptions>

          <div class="action-row">
            <el-radio-group v-model="checkinMode" size="default">
              <el-radio-button value="daily">普通签到</el-radio-button>
              <el-radio-button value="gamble">赌狗签到</el-radio-button>
            </el-radio-group>
            <el-button type="primary" :loading="checkining" @click="doCheckin()">立即签到</el-button>
            <el-button :loading="refreshing" @click="refreshMain">刷新状态</el-button>
          </div>
        </template>

        <div v-if="main.last_checkin_at" class="checkin-result">
          上次签到：{{ formatTime(main.last_checkin_at) }} ·
          <span :class="main.last_checkin_ok ? 'ok' : 'fail'">
            {{ main.last_checkin_ok ? '成功' : '失败' }}：{{ main.last_checkin_message }}
          </span>
        </div>
      </template>
    </el-card>

    <!-- 子账号管理 -->
    <el-card shadow="never" class="cloud-card sub-card">
      <template #header>
        <div class="card-header">
          <span>子账号</span>
          <div class="header-actions">
            <el-button size="small" :loading="checkinAllLoading" @click="checkinAll">全部签到</el-button>
            <el-button size="small" type="primary" @click="showAdd = true">新增子账号</el-button>
          </div>
        </div>
      </template>

      <el-table :data="subs" v-loading="subsLoading" size="default" empty-text="暂无子账号，点击「新增子账号」创建">
        <el-table-column prop="label" label="标签" min-width="100" />
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="row.authorized ? 'success' : 'info'" size="small">
              {{ row.authorized ? '已授权' : '未授权' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="账号" min-width="120">
          <template #default="{ row }">{{ row.user?.nickname || '-' }}</template>
        </el-table-column>
        <el-table-column label="签到" min-width="160">
          <template #default="{ row }">
            <span v-if="row.last_checkin_at" :class="row.last_checkin_ok ? 'ok' : 'fail'">
              {{ formatTime(row.last_checkin_at) }} {{ row.last_checkin_ok ? '成功' : '失败' }}
            </span>
            <span v-else>-</span>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="70">
          <template #default="{ row }">
            <el-switch :model-value="row.enabled" @change="(v: any) => toggleEnabled(row, v)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button v-if="!row.authorized" size="small" type="primary" link @click="openAuth(row)">
              授权
            </el-button>
            <el-button size="small" link @click="refreshSub(row)">刷新</el-button>
            <el-button size="small" link @click="doCheckin(row)">签到</el-button>
            <el-button size="small" link type="danger" @click="removeSub(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 新增子账号对话框 -->
    <el-dialog v-model="showAdd" title="新增子账号" width="420px">
      <el-form label-width="80px">
        <el-form-item label="标签">
          <el-input v-model="newLabel" placeholder="留空自动命名（小号 N）" maxlength="80" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showAdd = false">取消</el-button>
        <el-button type="primary" :loading="adding" @click="addSub">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useHttpClient } from '@/http/client'

const http = useHttpClient()

const main = reactive<any>({})
const mainLoading = ref(false)
const refreshing = ref(false)
const authing = ref(false)
const checkining = ref(false)
const checkinMode = ref('daily')

const subs = ref<any[]>([])
const subsLoading = ref(false)
const adding = ref(false)
const checkinAllLoading = ref(false)
const showAdd = ref(false)
const newLabel = ref('')

const loadMain = async () => {
  mainLoading.value = true
  try {
    const resp = await http.get('/api/cloud/hive/oauth/status')
    if (resp.data?.code === 200) {
      const d = resp.data.data || {}
      Object.assign(main, d.account || {})
      main.auth_url = d.auth_url || ''
    } else {
      ElMessage.error(resp.data?.message || '加载失败')
    }
  } catch (e: any) {
    ElMessage.error('加载失败：' + (e?.message || ''))
  } finally {
    mainLoading.value = false
  }
}

const loadSubs = async () => {
  subsLoading.value = true
  try {
    const resp = await http.get('/api/cloud/hive/sub-accounts')
    if (resp.data?.code === 200) {
      subs.value = resp.data.data || []
    }
  } catch (e: any) {
    ElMessage.error('子账号加载失败：' + (e?.message || ''))
  } finally {
    subsLoading.value = false
  }
}

const openAuth = async (row?: any) => {
  authing.value = true
  try {
    let url = ''
    if (row && typeof row.id !== 'undefined') {
      // 子账号授权：只有带 id 的账号行才走此分支（主按钮用 @click="openAuth()" 不会传入事件对象）
      if (!row.id || Number.isNaN(Number(row.id))) {
        ElMessage.error('子账号 ID 无效，请刷新列表后重试')
        return
      }
      const resp = await http.post(`/api/cloud/hive/sub-accounts/${row.id}/auth-url`)
      url = resp.data?.data?.auth_url || ''
    } else {
      // 主账号授权
      const resp = await http.post('/api/cloud/hive/oauth/auth-url')
      url = resp.data?.data?.auth_url || ''
    }
    if (url) {
      const win = window.open(url, '_blank')
      if (!win) {
        ElMessage.warning('浏览器拦截了弹窗：请允许本站弹窗后重试，或手动复制下方链接打开')
        ElMessage('授权链接：' + url)
        return
      }
      ElMessage.warning('请在打开的页面完成授权，完成后点击「刷新状态」')
    } else {
      ElMessage.error('生成授权链接失败')
    }
  } catch (e: any) {
    // 优先展示后端返回的具体错误（如“无效的账号 ID”），axios 默认文案没有诊断价值
    const status = e?.response?.status
    const detail = e?.response?.data?.message
    ElMessage.error(`授权失败${status ? '（HTTP ' + status + '）' : ''}：${detail || e?.message || '未知错误'}`)
  } finally {
    authing.value = false
  }
}

const refreshMain = async () => {
  refreshing.value = true
  try {
    const resp = await http.post('/api/cloud/hive/oauth/refresh')
    ElMessage.info(resp.data?.message || '已刷新')
    await loadMain()
  } catch (e: any) {
    ElMessage.error('刷新失败：' + (e?.message || ''))
  } finally {
    refreshing.value = false
  }
}

const doCheckin = async (row?: any) => {
  checkining.value = true
  try {
    const payload: any = { mode: checkinMode.value }
    const url = row
      ? `/api/cloud/hive/sub-accounts/${row.id}/checkin`
      : '/api/cloud/hive/oauth/checkin'
    if (row) payload.account_id = row.id
    const resp = await http.post(url, payload)
    ElMessage.info(resp.data?.message || '签到完成')
    await Promise.all([loadMain(), loadSubs()])
  } catch (e: any) {
    ElMessage.error('签到失败：' + (e?.message || ''))
  } finally {
    checkining.value = false
  }
}

const checkinAll = async () => {
  checkinAllLoading.value = true
  try {
    const resp = await http.post('/api/cloud/hive/oauth/checkin-all', { mode: checkinMode.value })
    ElMessage.success(resp.data?.message || '全部签到完成')
    await Promise.all([loadMain(), loadSubs()])
  } catch (e: any) {
    ElMessage.error('批量签到失败：' + (e?.message || ''))
  } finally {
    checkinAllLoading.value = false
  }
}

const addSub = async () => {
  adding.value = true
  try {
    const resp = await http.post('/api/cloud/hive/sub-accounts', { label: newLabel.value.trim() })
    if (resp.data?.code === 200) {
      ElMessage.success('子账号已创建，请点击「授权」完成 OAuth 授权')
      showAdd.value = false
      newLabel.value = ''
      await loadSubs()
    } else {
      ElMessage.error(resp.data?.message || '创建失败')
    }
  } catch (e: any) {
    ElMessage.error('创建失败：' + (e?.message || ''))
  } finally {
    adding.value = false
  }
}

const toggleEnabled = async (row: any, v: boolean) => {
  try {
    await http.put(`/api/cloud/hive/sub-accounts/${row.id}`, { enabled: v })
    row.enabled = v
  } catch (e: any) {
    ElMessage.error('更新失败：' + (e?.message || ''))
  }
}

const refreshSub = async (row: any) => {
  try {
    const resp = await http.post(`/api/cloud/hive/sub-accounts/${row.id}/refresh`)
    ElMessage.info(resp.data?.message || '已刷新')
    await loadSubs()
  } catch (e: any) {
    ElMessage.error('刷新失败：' + (e?.message || ''))
  }
}

const removeSub = async (row: any) => {
  try {
    await ElMessageBox.confirm(`确定删除子账号「${row.label}」？`, '删除确认', { type: 'warning' })
    const resp = await http.delete(`/api/cloud/hive/sub-accounts/${row.id}`)
    ElMessage.success(resp.data?.message || '已删除')
    await loadSubs()
  } catch (e: any) {
    if (e !== 'cancel' && e?.message) ElMessage.error('删除失败：' + e.message)
  }
}

const formatNum = (v: any) => {
  if (v === undefined || v === null) return '-'
  const n = Number(v)
  return Number.isInteger(n) ? String(n) : String(n)
}

const formatQuota = (u: any) => {
  if (!u) return '-'
  if (u.weekly_free_quota_unlimited) return '不限'
  if (Number(u.weekly_free_quota) > 0) {
    return `${Number(u.weekly_free_quota_remaining || 0)}/${Number(u.weekly_free_quota)}`
  }
  return '-'
}

const formatTime = (t?: string) => {
  if (!t) return '-'
  return t.replace('T', ' ').slice(0, 16)
}

onMounted(() => {
  loadMain()
  loadSubs()
})
</script>

<style scoped>
.cloud-page {
  padding: 12px;
}
.cloud-card {
  border-radius: 8px;
  margin-bottom: 16px;
}
.card-header {
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.nickname {
  font-weight: 400;
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.cloud-alert {
  margin-bottom: 16px;
}
.loading-box {
  padding: 8px 0;
}
.user-snapshot {
  margin-bottom: 16px;
}
.vip-tag {
  margin-left: 6px;
}
.action-row {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
}
.checkin-result {
  margin-top: 12px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.ok {
  color: var(--el-color-success);
}
.fail {
  color: var(--el-color-danger);
}
.sub-card .header-actions {
  display: flex;
  gap: 8px;
}
</style>