<template>
  <div class="mv-page mv-page-wide">
    <!-- 主渠道（symedia 中转，优先调度） -->
    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">
          主渠道
          <el-tag v-if="main.authorized" size="small" type="success" effect="plain" disable-transitions>已授权</el-tag>
          <el-tag v-else size="small" type="warning" effect="plain" disable-transitions>未授权</el-tag>
        </h3>
        <div class="mv-sec-actions">
          <el-tag size="small" :type="channelHealth.symedia === 0 ? 'success' : 'danger'">
            主渠道{{ channelHealth.symedia === 0 ? '正常' : `连续失败 ${channelHealth.symedia} 次` }}
          </el-tag>
          <el-button v-if="!main.authorized" size="small" type="primary" :loading="authing" @click="openAuth()">授权</el-button>
          <el-button size="small" :loading="refreshing" @click="refreshMain">刷新状态</el-button>
        </div>
      </div>
      <div class="mv-sec-body">
        <p class="mv-note" style="margin-bottom: 12px">
          资源查询优先走主渠道；主渠道超时或故障时自动切换备用渠道。授权前请先到 hdhive.com 注册影巢账号。
        </p>

        <div v-if="mainLoading" class="loading-box">
          <el-skeleton :rows="3" animated />
        </div>

        <template v-else>
          <template v-if="!main.authorized">
            <div class="mv-empty">
              尚未完成授权
              <div style="margin-top: 12px">
                <el-button type="primary" :loading="authing" @click="openAuth()">授权</el-button>
                <el-button :loading="refreshing" @click="refreshMain">刷新状态</el-button>
              </div>
            </div>
          </template>

          <template v-else>
            <!-- token 到期提醒（S2） -->
            <div class="mv-token-row">
              <span v-if="refreshExpireText(main)" :class="refreshExpireTone(main)">
                <el-icon style="margin-right: 4px"><AlarmClock /></el-icon>
                Refresh Token 将于 {{ refreshExpireText(main) }} 到期{{ refreshExpireUrgent(main) ? '，请尽快重新授权' : '' }}
              </span>
              <span v-else class="mv-note">Refresh Token 有效期未知（无关通道或未返回）</span>
            </div>

            <!-- 用户快照 hero -->
            <div class="mv-hero">
              <div class="mv-stat tone-primary">
                <span class="mv-stat-num">{{ formatNum(main.user?.points) }}</span>
                <span class="mv-stat-label">积分</span>
              </div>
              <div class="mv-stat tone-info">
                <span class="mv-stat-num">{{ main.user?.checked_in_today ? '已签到' : '未签到' }}</span>
                <span class="mv-stat-label">今日签到（累计 {{ main.user?.checkin_days_total || 0 }} 天）</span>
              </div>
              <div class="mv-stat tone-warn">
                <span class="mv-stat-num">{{ main.user?.weekly_free_quota_unlimited ? '不限' : formatQuota(main.user) }}</span>
                <span class="mv-stat-label">周免费额度</span>
              </div>
              <div class="mv-stat tone-success">
                <span class="mv-stat-num">{{ formatNum(main.user?.bonus_quota) }}</span>
                <span class="mv-stat-label">奖励额度</span>
              </div>
            </div>

            <div class="mv-toolbar" style="margin-top: 14px">
              <div style="display: flex; align-items: center; gap: 8px">
                <span class="mv-note">签到方式：</span>
                <el-radio-group v-model="checkinMode" size="small">
                  <el-radio-button value="daily">普通签到</el-radio-button>
                  <el-radio-button value="gamble">赌狗签到</el-radio-button>
                </el-radio-group>
              </div>
              <div style="flex: 1"></div>
              <el-button type="primary" :loading="checkining" @click="doCheckin()">立即签到</el-button>
              <el-button :loading="refreshing" @click="refreshMain">刷新状态</el-button>
            </div>

            <div v-if="main.last_checkin_at" class="checkin-result">
              上次签到：{{ formatTime(main.last_checkin_at) }} · 连续 {{ main.last_checkin_streak || 0 }} 天 ·
              <span :class="main.last_checkin_ok ? 'ok' : 'fail'">
                {{ main.last_checkin_ok ? '成功' : '失败' }}：{{ main.last_checkin_message }}
              </span>
              <template v-if="main.last_checkin_points !== undefined && main.last_checkin_points !== null">
                · 本次 +{{ main.last_checkin_points }} 积分，余额 {{ main.last_checkin_balance }}
              </template>
            </div>
          </template>
        </template>
      </div>
    </section>

    <!-- 备用渠道（tgtodrive 中转） -->
    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">
          备用渠道
          <el-tag v-if="backup.authorized" size="small" type="success" effect="plain" disable-transitions>已授权</el-tag>
          <el-tag v-else size="small" type="warning" effect="plain" disable-transitions>未授权</el-tag>
        </h3>
        <div class="mv-sec-actions">
          <el-tag size="small" :type="channelHealth.tgtodrive === 0 ? 'success' : 'danger'">
            备用渠道{{ channelHealth.tgtodrive === 0 ? '正常' : `连续失败 ${channelHealth.tgtodrive} 次` }}
          </el-tag>
          <el-button v-if="!backup.authorized" size="small" type="primary" :loading="backupAuthing" @click="openBackupAuth()">授权</el-button>
          <el-button size="small" :loading="backupRefreshing" @click="refreshBackup">刷新状态</el-button>
        </div>
      </div>
      <div class="mv-sec-body">
        <p class="mv-note" style="margin-bottom: 12px">主渠道超时或故障时自动切换备用渠道，不影响订阅与签到。</p>

        <div v-if="backupLoading" class="loading-box">
          <el-skeleton :rows="3" animated />
        </div>

        <template v-else>
          <template v-if="!backup.authorized">
            <div class="mv-empty">
              尚未完成授权
              <div style="margin-top: 12px">
                <el-button type="primary" :loading="backupAuthing" @click="openBackupAuth()">授权</el-button>
                <el-button :loading="backupRefreshing" @click="refreshBackup">刷新状态</el-button>
              </div>
            </div>
          </template>

          <template v-else>
            <div class="mv-token-row">
              <span v-if="refreshExpireText(backup)" :class="refreshExpireTone(backup)">
                <el-icon style="margin-right: 4px"><AlarmClock /></el-icon>
                Refresh Token 将于 {{ refreshExpireText(backup) }} 到期{{ refreshExpireUrgent(backup) ? '，请尽快重新授权' : '' }}
              </span>
            </div>

            <div class="mv-hero" style="margin-top: 8px">
              <div class="mv-stat tone-primary">
                <span class="mv-stat-num">{{ formatNum(backup.user?.points) }}</span>
                <span class="mv-stat-label">积分</span>
              </div>
              <div class="mv-stat tone-info">
                <span class="mv-stat-num">{{ backup.user?.checked_in_today ? '已签到' : '未签到' }}</span>
                <span class="mv-stat-label">今日签到（累计 {{ backup.user?.checkin_days_total || 0 }} 天）</span>
              </div>
            </div>

            <div class="mv-toolbar" style="margin-top: 14px">
              <div style="flex: 1"></div>
              <el-button type="primary" :loading="backupCheckining" @click="doBackupCheckin()">立即签到</el-button>
              <el-button :loading="backupRefreshing" @click="refreshBackup">刷新状态</el-button>
            </div>

            <div v-if="backup.last_checkin_at" class="checkin-result">
              上次签到：{{ formatTime(backup.last_checkin_at) }} · 连续 {{ backup.last_checkin_streak || 0 }} 天 ·
              <span :class="backup.last_checkin_ok ? 'ok' : 'fail'">
                {{ backup.last_checkin_ok ? '成功' : '失败' }}：{{ backup.last_checkin_message }}
              </span>
            </div>
          </template>
        </template>
      </div>
    </section>

    <!-- 子账号管理 -->
    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">子账号</h3>
        <div class="mv-sec-actions">
          <el-button size="small" :loading="checkinAllLoading" @click="checkinAll">全部签到</el-button>
          <el-button size="small" type="primary" @click="showAdd = true">新增子账号</el-button>
        </div>
      </div>
      <div class="mv-sec-body">
        <div class="mv-table-wrap">
          <table class="mv-table">
            <thead>
              <tr>
                <th>标签</th>
                <th>状态</th>
                <th>账号</th>
                <th>签到</th>
                <th>启用</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in subs" :key="row.id">
                <td>{{ row.label }}</td>
                <td>
                  <el-tag :type="row.authorized ? 'success' : 'info'" size="small">
                    {{ row.authorized ? '已授权' : '未授权' }}
                  </el-tag>
                </td>
                <td>
                  <span style="display: inline-flex; align-items: center">
                    <img v-if="row.user?.avatar_url" :src="row.user.avatar_url" class="avatar" alt="" />
                    {{ row.user?.nickname || row.user?.username || '-' }}
                  </span>
                </td>
                <td>
                  <span v-if="row.last_checkin_at" :class="row.last_checkin_ok ? 'ok' : 'fail'">
                    {{ formatTime(row.last_checkin_at) }} {{ row.last_checkin_ok ? '成功' : '失败' }}
                    <template v-if="row.last_checkin_streak">（连签 {{ row.last_checkin_streak }} 天）</template>
                  </span>
                  <span v-else>-</span>
                </td>
                <td>
                  <el-switch :model-value="row.enabled" @change="(v: any) => toggleEnabled(row, v)" />
                </td>
                <td>
                  <el-button v-if="!row.authorized" size="small" type="primary" link @click="openSubAuth(row)">授权</el-button>
                  <el-button size="small" link @click="refreshSub(row)">刷新</el-button>
                  <el-button size="small" link @click="doCheckin(row)">签到</el-button>
                  <el-button size="small" link type="danger" @click="removeSub(row)">删除</el-button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="!subs.length" class="mv-empty">暂无子账号，点击「新增子账号」创建</div>
      </div>
    </section>

    <!-- 每日自动签到配置 -->
    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">
          每日自动签到
          <span class="mv-pill mv-pill-primary">随机窗口</span>
        </h3>
      </div>
      <div class="mv-sec-body">
        <p class="mv-note" style="margin-bottom: 12px">
          开启后每天在指定时段内为影巢账号随机签到取积分（随机分钟落在选定时段前 30 分钟内）；失败自动重试，同一天只签到一次。
        </p>

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

        <p class="mv-note" style="margin-top: 8px">
          更精确的随机签到窗口（HH:MM ~ HH:MM，自定义跨度）请在「影巢设置 → 签到与限额」中配置；此处保存后将沿用小时级策略。
        </p>

        <div class="checkin-config-save">
          <el-button size="small" :loading="checkinSaving" @click="saveCheckinSettings">保存签到设置</el-button>
          <span v-if="checkinSaved" class="checkin-saved-hint">已保存</span>
        </div>
      </div>
    </section>

    <!-- 签到历史（S3） -->
    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">
          签到历史
          <span class="mv-pill mv-pill-muted">每账号保留最近 500 条</span>
        </h3>
        <div class="mv-sec-actions">
          <el-select v-model="historyAccountId" size="small" style="width: 160px" @change="loadHistory">
            <el-option label="全部账号" :value="0" />
            <el-option
              v-for="acc in historyAccounts"
              :key="acc.key"
              :label="acc.label"
              :value="acc.key as number"
            />
          </el-select>
          <el-button size="small" @click="loadHistory">刷新</el-button>
          <el-button size="small" link type="danger" @click="clearHistory">清空</el-button>
        </div>
      </div>
      <div class="mv-sec-body">
        <div class="mv-table-wrap">
          <table class="mv-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>账号</th>
                <th>通道</th>
                <th>方式</th>
                <th>结果</th>
                <th>积分</th>
                <th>余额</th>
                <th>连签</th>
                <th>触发</th>
                <th>说明</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in history" :key="r.id" :class="{ 'mv-row-dim': !r.ok }">
                <td><span class="mv-note">{{ formatTime(r.checkin_at) }}</span></td>
                <td>{{ r.label }}<span v-if="r.is_main" class="mv-pill mv-pill-primary" style="margin-left: 4px">主</span></td>
                <td>{{ channelLabel(r.channel) }}</td>
                <td>{{ r.mode === 'gamble' ? '赌狗' : '普通' }}</td>
                <td>
                  <span v-if="r.ok" class="ok">成功</span>
                  <span v-else class="fail">失败</span>
                </td>
                <td>
                  <span v-if="r.points !== null && r.points !== undefined" :class="r.points >= 0 ? 'ok' : 'fail'">
                    {{ r.points >= 0 ? '+' : '' }}{{ r.points }}
                  </span>
                  <span v-else class="mv-note">-</span>
                </td>
                <td>{{ r.balance ?? '-' }}</td>
                <td>{{ r.streak || '-' }}</td>
                <td>
                  <span class="mv-pill mv-pill-muted">{{ triggerLabel(r.trigger) }}</span>
                </td>
                <td>
                  <span class="mv-note" style="max-width: 320px; word-break: break-all">{{ r.message || '-' }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="!history.length" class="mv-empty">暂无签到记录（完成一次签到后产生）</div>
      </div>
    </section>

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
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { AlarmClock } from '@element-plus/icons-vue'
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
    await Promise.all([loadMain(), loadSubs(), loadHistory()])
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
    await loadHistory()
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
    await loadHistory()
  } catch (e: any) {
    ElMessage.error('签到失败：' + (e?.message || ''))
  } finally {
    backupCheckining.value = false
  }
}

// ---- token 到期（S2） ----
const refreshExpireText = (acc: any): string => {
  if (!acc?.refresh_expires_at) return ''
  const t = new Date(acc.refresh_expires_at)
  if (isNaN(t.getTime())) return ''
  const days = Math.ceil((t.getTime() - Date.now()) / 86400000)
  if (days < 0) return '已过期'
  if (days <= 1) {
    const h = Math.max(1, Math.ceil((t.getTime() - Date.now()) / 3600000))
    return `${h} 小时后`
  }
  return `${days} 天后`
}
const refreshExpireUrgent = (acc: any): boolean => {
  if (!acc?.refresh_expires_at) return false
  const t = new Date(acc.refresh_expires_at)
  if (isNaN(t.getTime())) return false
  return t.getTime() - Date.now() < 7 * 86400000
}
const refreshExpireTone = (acc: any): string => {
  if (refreshExpireUrgent(acc)) return 'token-urgent'
  return 'token-ok'
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
    await loadHistory()
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
    await Promise.all([loadMain(), loadBackup(), loadSubs(), loadHistory()])
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

// ---- 签到历史（S3） ----
interface CheckinRecord {
  id: number
  account_id: number
  label: string
  is_main: boolean
  channel: string
  mode: string
  ok: boolean
  points: number | null
  balance: number | null
  streak: number
  trigger: string
  message: string
  checkin_at: string
}

const history = ref<CheckinRecord[]>([])
const historyAccountId = ref(0)

// 账号筛选下拉：主/备用渠道 + 子账号
const historyAccounts = computed(() => {
  const seen = new Set<number>()
  const list: { key: number; label: string }[] = []
  const push = (id: number, label: string) => {
    if (!id || seen.has(id)) return
    seen.add(id)
    list.push({ key: id, label })
  }
  if (main?.id) push(main.id, '主渠道')
  if (backup?.id && backup.id !== main?.id) push(backup.id, '备用渠道')
  for (const s of subs.value) push(s.id, s.label || '小号')
  return list
})

const loadHistory = async () => {
  try {
    const resp = await http.get('/api/cloud/hive/checkin/records', {
      params: { account_id: historyAccountId.value || undefined, limit: 200 },
    })
    if (resp.data?.code === 200) {
      history.value = resp.data.data || []
    }
  } catch (e: any) {
    ElMessage.error('加载签到历史失败：' + (e?.message || ''))
  }
}

const clearHistory = async () => {
  try {
    await ElMessageBox.confirm('确定清空签到历史吗？', '清空历史', { type: 'warning' })
  } catch {
    return
  }
  try {
    const resp = await http.delete('/api/cloud/hive/checkin/records', {
      params: { account_id: historyAccountId.value || undefined },
    })
    ElMessage.success(resp.data?.message || '已清空')
    history.value = []
  } catch (e: any) {
    ElMessage.error('清空失败：' + (e?.message || ''))
  }
}

const channelLabel = (ch: string) => {
  const map: Record<string, string> = {
    symedia: '主渠道',
    tgtodrive: '备用渠道',
    nanshare: '南巷',
    official: '官方',
  }
  return map[ch] || ch
}

const triggerLabel = (t: string) => {
  const map: Record<string, string> = {
    manual: '手动',
    daily: '定时',
    catchup: '补签',
    retry: '重试',
  }
  return map[t] || t
}

// ---- 工具 ----
const formatNum = (v: any) => {
  if (v === undefined || v === null) return '-'
  return String(Number(v))
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

onMounted(async () => {
  await Promise.all([loadMain(), loadBackup(), loadSubs(), loadCheckinSettings()])
  loadHistory()
})
</script>

<style scoped>
.loading-box {
  padding: 8px 0;
}
.ok {
  color: var(--success, var(--el-color-success));
  font-weight: 500;
}
.fail {
  color: var(--danger, var(--el-color-danger));
  font-weight: 500;
}
.checkin-result {
  margin-top: 12px;
  font-size: 13px;
  color: var(--text-muted, var(--el-text-color-secondary));
  padding: 10px 12px;
  border: 1px solid var(--border-soft, var(--el-border-color-lighter));
  border-radius: var(--radius-sm, 8px);
  background: var(--surface-sunken, transparent);
}
.avatar {
  width: 22px;
  height: 22px;
  border-radius: 50%;
  margin-right: 6px;
  vertical-align: middle;
}
.mv-token-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  margin-bottom: 14px;
  padding: 8px 12px;
  border-radius: var(--radius-sm, 8px);
}
.token-ok {
  color: var(--text-muted, var(--el-text-color-secondary));
  background: transparent;
  border: 1px solid var(--border-soft, var(--el-border-color-lighter));
}
.token-urgent {
  color: var(--warning, #e6a23c);
  background: color-mix(in srgb, var(--warning, #e6a23c) 10%, transparent);
  border: 1px solid color-mix(in srgb, var(--warning, #e6a23c) 30%, transparent);
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
  color: var(--text-regular, var(--el-text-color-regular));
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
  border-top: 1px solid var(--border-soft, var(--el-border-color-lighter));
}
.checkin-saved-hint {
  font-size: 12px;
  color: var(--success, var(--el-color-success));
}
</style>