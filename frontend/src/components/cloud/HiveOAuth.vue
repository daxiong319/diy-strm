<template>
  <div class="main-content-container cloud-page">
    <!-- 主渠道（symedia 中转，优先调度） -->
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>主渠道</span>
          <div class="header-actions">
            <el-tag size="small" :type="channelHealth.symedia === 0 ? 'success' : 'danger'">
              主渠道{{ channelHealth.symedia === 0 ? '正常' : `连续失败 ${channelHealth.symedia} 次` }}
            </el-tag>
          </div>
        </div>
      </template>

      <el-alert
        type="info"
        :closable="false"
        class="cloud-alert"
        title="资源查询优先走主渠道；主渠道超时或故障时自动切换备用渠道。授权前请先到 hdhive.com 注册影巢账号。"
        show-icon
      />

      <div v-if="mainLoading" class="loading-box">
        <el-skeleton :rows="3" animated />
      </div>

      <template v-else>
        <!-- 未授权 -->
        <template v-if="!main.authorized">
          <el-empty description="尚未完成授权" :image-size="80">
            <el-button type="primary" :loading="authing" @click="openAuth()">授权</el-button>
            <el-button :loading="refreshing" @click="refreshMain">刷新状态</el-button>
          </el-empty>
        </template>

        <!-- 已授权：用户快照 -->
        <template v-else>
          <el-descriptions :column="2" border class="user-snapshot">
            <el-descriptions-item label="账号">
              {{ main.user?.nickname || main.user?.username || '-' }}
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
            <el-descriptions-item label="奖励额度">{{ formatNum(main.user?.bonus_quota) }}</el-descriptions-item>
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

    <!-- 备用渠道（tgtodrive 中转） -->
    <el-card shadow="never" class="cloud-card sub-card">
      <template #header>
        <div class="card-header">
          <span>备用渠道</span>
          <div class="header-actions">
            <el-tag size="small" :type="channelHealth.tgtodrive === 0 ? 'success' : 'danger'">
              备用渠道{{ channelHealth.tgtodrive === 0 ? '正常' : `连续失败 ${channelHealth.tgtodrive} 次` }}
            </el-tag>
          </div>
        </div>
      </template>

      <el-alert
        type="info"
        :closable="false"
        class="cloud-alert"
        title="主渠道超时或故障时自动切换备用渠道，不影响订阅与签到。"
        show-icon
      />

      <div v-if="backupLoading" class="loading-box">
        <el-skeleton :rows="3" animated />
      </div>

      <template v-else>
        <!-- 未授权 -->
        <template v-if="!backup.authorized">
          <el-empty description="尚未完成授权" :image-size="80">
            <el-button type="primary" :loading="backupAuthing" @click="openBackupAuth()">授权</el-button>
            <el-button :loading="backupRefreshing" @click="refreshBackup">刷新状态</el-button>
          </el-empty>
        </template>

        <!-- 已授权：用户快照 -->
        <template v-else>
          <el-descriptions :column="2" border class="user-snapshot">
            <el-descriptions-item label="账号">
              {{ backup.user?.nickname || backup.user?.username || '-' }}
            </el-descriptions-item>
            <el-descriptions-item label="等级">
              {{ backup.user?.level || '-' }}
              <el-tag v-if="backup.user?.is_forever_vip" size="small" type="warning" class="vip-tag">终身VIP</el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="积分">{{ formatNum(backup.user?.points) }}</el-descriptions-item>
            <el-descriptions-item label="签到">
              {{ backup.user?.checked_in_today ? '已签到' : '未签到' }}（累计 {{ backup.user?.checkin_days_total || 0 }} 天）
            </el-descriptions-item>
          </el-descriptions>

          <div class="action-row">
            <el-button type="primary" :loading="backupCheckining" @click="doBackupCheckin()">立即签到</el-button>
            <el-button :loading="backupRefreshing" @click="refreshBackup">刷新状态</el-button>
          </div>
        </template>
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
        <el-table-column label="账号" min-width="140">
          <template #default="{ row }">
            <span class="cell-user">
              <img v-if="row.user?.avatar_url" :src="row.user.avatar_url" class="avatar" alt="" />
              {{ row.user?.nickname || row.user?.username || '-' }}
            </span>
          </template>
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
            <el-button v-if="!row.authorized" size="small" type="primary" link @click="openSubAuth(row)">
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

    <!-- 每日自动签到配置 -->
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">每日自动签到</div>
      </template>
      <el-alert
        type="info"
        :closable="false"
        class="cloud-alert"
        title="开启后每天在指定时间自动为影巢账号签到，获取积分。加上随机分钟会落在选定时段的前 30 分钟内。"
        show-icon
      />

      <!-- 主账号 -->
      <div class="checkin-config-row">
        <div class="checkin-config-label">主账号</div>
        <el-switch v-model="checkinForm.main_enabled" />
        <el-time-select
          v-model="checkinForm.main_time"
          :disabled="!checkinForm.main_enabled"
          start="00:00"
          end="23:00"
          step="01:00"
          format="HH:00"
          placeholder="选择签到时间"
          class="checkin-time-picker"
        />
        <el-radio-group v-model="checkinForm.main_mode" :disabled="!checkinForm.main_enabled" size="small">
          <el-radio-button value="daily">普通签到</el-radio-button>
          <el-radio-button value="gamble">赌狗签到</el-radio-button>
        </el-radio-group>
      </div>

      <!-- 子账号 -->
      <div class="checkin-config-row">
        <div class="checkin-config-label">子账号</div>
        <el-switch v-model="checkinForm.sub_enabled" />
        <el-time-select
          v-model="checkinForm.sub_time"
          :disabled="!checkinForm.sub_enabled"
          start="00:00"
          end="23:00"
          step="01:00"
          format="HH:00"
          placeholder="选择签到时间"
          class="checkin-time-picker"
        />
        <el-radio-group v-model="checkinForm.sub_mode" :disabled="!checkinForm.sub_enabled" size="small">
          <el-radio-button value="daily">普通签到</el-radio-button>
          <el-radio-button value="gamble">赌狗签到</el-radio-button>
        </el-radio-group>
      </div>

      <div class="checkin-config-save">
        <el-button size="small" :loading="checkinSaving" @click="saveCheckinSettings">保存签到设置</el-button>
        <span v-if="checkinSaved" class="checkin-saved-hint">已保存</span>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useHttpClient } from '@/http/client'

const http = useHttpClient()

// ---- 主渠道（symedia） ----
const main = reactive<any>({})
const mainLoading = ref(false)
const refreshing = ref(false)
const authing = ref(false)
const checkining = ref(false)
const checkinMode = ref('daily')
const channelHealth = ref<any>({ symedia: 0, tgtodrive: 0 })

const loadMain = async () => {
  mainLoading.value = true
  try {
    const resp = await http.get('/api/cloud/hive/symedia/status')
    if (resp.data?.code === 200) {
      const d = resp.data.data || {}
      Object.assign(main, d.account || {})
      main.auth_url = d.auth_url || ''
      channelHealth.value = d.channel_health || { symedia: 0, tgtodrive: 0 }
    } else {
      ElMessage.error(resp.data?.message || '加载失败')
    }
  } catch (e: any) {
    ElMessage.error('加载失败：' + (e?.message || ''))
  } finally {
    mainLoading.value = false
  }
}

const openAuth = async () => {
  authing.value = true
  try {
    const resp = await http.post('/api/cloud/hive/symedia/start')
    const url = resp.data?.data?.authorize_url || resp.data?.data?.auth_url || ''
    if (!url) {
      ElMessage.error(resp.data?.message || '生成授权链接失败')
      return
    }
    const win = window.open(url, '_blank')
    if (!win) {
      ElMessage.warning('浏览器拦截了弹窗：请允许本站弹窗后重试，或手动复制下方链接打开')
      ElMessage('授权链接：' + url)
      return
    }
    ElMessage.warning('请在打开的页面完成授权，完成后点击「刷新状态」')
  } catch (e: any) {
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
    const resp = await http.post('/api/cloud/hive/symedia/refresh')
    ElMessage.info(resp.data?.message || '已刷新')
    await loadMain()
  } catch (e: any) {
    ElMessage.error('刷新失败：' + (e?.message || ''))
  } finally {
    refreshing.value = false
  }
}

const openSubAuth = async (row: any) => {
  if (!row || !row.id) {
    ElMessage.error('子账号 ID 无效，请刷新列表后重试')
    return
  }
  try {
    const resp = await http.post(`/api/cloud/hive/sub-accounts/${row.id}/auth-url`)
    const url = resp.data?.data?.auth_url || ''
    if (!url) {
      ElMessage.error(resp.data?.message || '生成授权链接失败')
      return
    }
    const win = window.open(url, '_blank')
    if (!win) {
      ElMessage.warning('浏览器拦截了弹窗：请允许本站弹窗后重试，或手动复制下方链接打开')
      ElMessage('授权链接：' + url)
      return
    }
    ElMessage.warning('请在打开的页面完成授权，完成后点击「刷新」')
  } catch (e: any) {
    const status = e?.response?.status
    const detail = e?.response?.data?.message
    ElMessage.error(`授权失败${status ? '（HTTP ' + status + '）' : ''}：${detail || e?.message || '未知错误'}`)
  }
}

const doCheckin = async (row?: any) => {
  checkining.value = true
  try {
    const payload: any = { mode: checkinMode.value }
    const url = row
      ? `/api/cloud/hive/sub-accounts/${row.id}/checkin`
      : '/api/cloud/hive/symedia/checkin'
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

// ---- 备用渠道（tgtodrive 主账号） ----
const backup = reactive<any>({})
const backupLoading = ref(false)
const backupAuthing = ref(false)
const backupRefreshing = ref(false)
const backupCheckining = ref(false)

const loadBackup = async () => {
  backupLoading.value = true
  try {
    const resp = await http.get('/api/cloud/hive/oauth/status')
    if (resp.data?.code === 200) {
      const d = resp.data.data || {}
      Object.assign(backup, d.account || {})
      backup.auth_url = d.auth_url || ''
      if (d.channel_health) {
        channelHealth.value = d.channel_health
      }
    }
  } catch (e: any) {
    ElMessage.error('备用渠道加载失败：' + (e?.message || ''))
  } finally {
    backupLoading.value = false
  }
}

const openBackupAuth = async () => {
  backupAuthing.value = true
  try {
    const resp = await http.post('/api/cloud/hive/oauth/auth-url')
    const url = resp.data?.data?.auth_url || ''
    if (!url) {
      ElMessage.error(resp.data?.message || '生成授权链接失败')
      return
    }
    const win = window.open(url, '_blank')
    if (!win) {
      ElMessage.warning('浏览器拦截了弹窗：请允许本站弹窗后重试，或手动复制下方链接打开')
      ElMessage('授权链接：' + url)
      return
    }
    ElMessage.warning('请在打开的页面完成授权，完成后点击「刷新状态」')
  } catch (e: any) {
    ElMessage.error('授权失败：' + (e?.message || ''))
  } finally {
    backupAuthing.value = false
  }
}

const refreshBackup = async () => {
  backupRefreshing.value = true
  try {
    const resp = await http.post('/api/cloud/hive/oauth/refresh')
    ElMessage.info(resp.data?.message || '已刷新')
    await loadBackup()
  } catch (e: any) {
    ElMessage.error('刷新失败：' + (e?.message || ''))
  } finally {
    backupRefreshing.value = false
  }
}

const doBackupCheckin = async () => {
  backupCheckining.value = true
  try {
    const resp = await http.post('/api/cloud/hive/oauth/checkin', { mode: checkinMode.value })
    ElMessage.info(resp.data?.message || '签到完成')
    await loadBackup()
  } catch (e: any) {
    ElMessage.error('签到失败：' + (e?.message || ''))
  } finally {
    backupCheckining.value = false
  }
}

// ---- 子账号 ----
const subs = ref<any[]>([])
const subsLoading = ref(false)
const adding = ref(false)
const checkinAllLoading = ref(false)
const showAdd = ref(false)
const newLabel = ref('')

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

const checkinAll = async () => {
  checkinAllLoading.value = true
  try {
    const resp = await http.post('/api/cloud/hive/oauth/checkin-all', { mode: checkinMode.value })
    ElMessage.success(resp.data?.message || '全部签到完成')
    await Promise.all([loadMain(), loadBackup(), loadSubs()])
  } catch (e: any) {
    ElMessage.error('批量签到失败：' + (e?.message || ''))
  } finally {
    checkinAllLoading.value = false
  }
}

// ---- 每日自动签到配置 ----
const checkinForm = reactive({
  main_enabled: true,
  main_time: '08:00',
  main_mode: 'daily' as string,
  sub_enabled: true,
  sub_time: '08:00',
  sub_mode: 'daily' as string,
})
const checkinSaving = ref(false)
const checkinSaved = ref(false)

// hour(0-23) -> "HH:00"
const hourToTime = (h: any) => {
  const n = Number(h)
  if (!Number.isInteger(n) || n < 0 || n > 23) return '08:00'
  return `${String(n).padStart(2, '0')}:00`
}

const loadCheckinSettings = async () => {
  try {
    const resp = await http.get('/api/cloud/hive/settings')
    if (resp.data?.code === 200) {
      const d = resp.data.data || {}
      checkinForm.main_enabled = d.daily_checkin_enabled !== false
      checkinForm.main_time = hourToTime(d.daily_checkin_hour)
      checkinForm.main_mode = d.daily_checkin_mode === 'gamble' ? 'gamble' : 'daily'
      checkinForm.sub_enabled = d.sub_checkin_enabled !== false
      checkinForm.sub_time = hourToTime(d.sub_checkin_hour)
      checkinForm.sub_mode = d.sub_checkin_mode === 'gamble' ? 'gamble' : 'daily'
    }
  } catch {
    /* 静默 */
  }
}

const saveCheckinSettings = async () => {
  checkinSaving.value = true
  checkinSaved.value = false
  try {
    const hour = (t: string) => {
      const n = parseInt(String(t).split(':')[0] || '8', 10)
      return Number.isInteger(n) && n >= 0 && n <= 23 ? n : 8
    }
    const resp = await http.post('/api/cloud/hive/settings', {
      daily_checkin_enabled: checkinForm.main_enabled,
      daily_checkin_hour: hour(checkinForm.main_time),
      daily_checkin_mode: checkinForm.main_mode,
      sub_checkin_enabled: checkinForm.sub_enabled,
      sub_checkin_hour: hour(checkinForm.sub_time),
      sub_checkin_mode: checkinForm.sub_mode,
    })
    if (resp.data?.code === 200) {
      ElMessage.success('签到设置已保存')
      checkinSaved.value = true
      setTimeout(() => (checkinSaved.value = false), 2500)
    } else {
      ElMessage.error(resp.data?.message || '保存失败')
    }
  } catch (e: any) {
    ElMessage.error('保存失败：' + (e?.message || ''))
  } finally {
    checkinSaving.value = false
  }
}

// ---- 工具 ----
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
  loadBackup()
  loadSubs()
  loadCheckinSettings()
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
  display: inline-flex;
  align-items: center;
}
.avatar {
  width: 22px;
  height: 22px;
  border-radius: 50%;
  margin-right: 6px;
  vertical-align: middle;
}
.cell-user {
  display: inline-flex;
  align-items: center;
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
.checkin-config-row {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 10px 0;
  flex-wrap: wrap;
}
.checkin-config-label {
  width: 64px;
  font-size: 13px;
  font-weight: 500;
  flex-shrink: 0;
}
.checkin-time-picker {
  width: 150px;
}
.checkin-config-save {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--el-border-color-lighter);
}
.checkin-saved-hint {
  font-size: 12px;
  color: var(--el-color-success);
}
</style>
