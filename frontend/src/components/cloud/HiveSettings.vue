<template>
  <div class="main-content-container cloud-page">
    <el-alert
      type="info"
      :closable="false"
      class="cloud-alert"
      title="影巢每日自动签到、四通道负载均衡通道管理，以及订阅引擎轮询配置。OAuth 授权与手动签到也可在「授权签到」页进行。"
      show-icon
    />

    <!-- 影巢通道（四通道负载均衡） -->
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>影巢通道</span>
          <span class="card-sub">订阅引擎按「可用优先」轮转四个通道，通道异常时自动逐个降级尝试</span>
        </div>
      </template>
      <el-table :data="channels" v-loading="channelsLoading" size="small">
        <el-table-column label="通道" min-width="140">
          <template #default="{ row }">
            <div class="chan-name">{{ row.label }}</div>
            <div class="chan-key">{{ row.channel }}</div>
          </template>
        </el-table-column>
        <el-table-column label="授权账号" min-width="180">
          <template #default="{ row }">
            <template v-if="row.account">
              <div v-if="row.account.user" class="chan-user">
                {{ row.account.user.nickname || row.account.user.username || row.account.user.uid || '已授权用户' }}
                <el-tag v-if="row.account.user.id" size="small" type="info" class="chan-uid">UID {{ row.account.user.id }}</el-tag>
              </div>
              <div v-else class="chan-key">{{ row.account.install_hash || ('#' + row.account.id) }}</div>
            </template>
            <span v-else class="chan-key">未配置</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag v-if="row.account?.authorized" type="success" size="small">已授权</el-tag>
            <el-tag v-else type="warning" size="small">未授权</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="健康度" width="150">
          <template #default="{ row }">
            <template v-if="health(row.channel)">
              <el-tag v-if="health(row.channel).cooldown_seconds > 0" type="danger" size="small">
                冷却中 {{ health(row.channel).cooldown_seconds }}s
              </el-tag>
              <el-tag v-else-if="health(row.channel).fails > 0" type="warning" size="small">
                失败 {{ health(row.channel).fails }} 次
              </el-tag>
              <el-tag v-else type="success" size="small">正常</el-tag>
            </template>
            <span v-else class="chan-key">-</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" min-width="230">
          <template #default="{ row }">
            <el-button
              v-if="!row.account?.authorized"
              type="primary"
              size="small"
              :loading="actingOps[row.channel] === 'start'"
              @click="authorize(row)"
            >授权</el-button>
            <template v-else>
              <el-button
                size="small"
                :loading="actingOps[row.channel] === 'refresh'"
                @click="refresh(row)"
              >刷新状态</el-button>
              <el-button
                v-if="hasTest(row.channel)"
                size="small"
                :loading="actingOps[row.channel] === 'test'"
                @click="testChannel(row)"
              >测试</el-button>
            </template>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 每日自动签到 · 主账号 -->
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>每日自动签到 · 主账号</span>
        </div>
      </template>
      <el-form label-width="120px" class="hive-form">
        <el-form-item label="自动签到">
          <el-switch v-model="form.daily_checkin_enabled" />
          <div class="form-help">开启后每天在指定时间自动为影巢主账号签到，获取积分。</div>
        </el-form-item>
        <el-form-item label="签到时间">
          <el-select v-model="form.daily_checkin_hour" :disabled="!form.daily_checkin_enabled" style="width: 140px">
            <el-option v-for="h in hours" :key="h" :label="`${String(h).padStart(2,'0')}:00`" :value="h" />
          </el-select>
          <div class="form-help">服务器时区，每天该时刻触发签到。</div>
        </el-form-item>
        <el-form-item label="签到模式">
          <el-radio-group v-model="form.daily_checkin_mode" :disabled="!form.daily_checkin_enabled">
            <el-radio-button value="daily">普通签到</el-radio-button>
            <el-radio-button value="gamble">赌狗签到</el-radio-button>
          </el-radio-group>
          <div class="form-help">普通签到积分固定；赌狗签到有概率获得更高积分。</div>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 每日自动签到 · 子账号 -->
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>每日自动签到 · 子账号</span>
        </div>
      </template>
      <el-form label-width="120px" class="hive-form">
        <el-form-item label="自动签到">
          <el-switch v-model="form.sub_checkin_enabled" />
          <div class="form-help">开启后每天在指定时间自动为已启用的子账号签到。</div>
        </el-form-item>
        <el-form-item label="签到时间">
          <el-select v-model="form.sub_checkin_hour" :disabled="!form.sub_checkin_enabled" style="width: 140px">
            <el-option v-for="h in hours" :key="h" :label="`${String(h).padStart(2,'0')}:00`" :value="h" />
          </el-select>
          <div class="form-help">服务器时区，每天该时刻触发签到。</div>
        </el-form-item>
        <el-form-item label="签到模式">
          <el-radio-group v-model="form.sub_checkin_mode" :disabled="!form.sub_checkin_enabled">
            <el-radio-button value="daily">普通签到</el-radio-button>
            <el-radio-button value="gamble">赌狗签到</el-radio-button>
          </el-radio-group>
          <div class="form-help">普通签到积分固定；赌狗签到有概率获得更高积分。</div>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 订阅引擎 -->
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>订阅引擎</span>
        </div>
      </template>
      <el-form label-width="140px" class="hive-form">
        <el-form-item label="轮询间隔">
          <el-input-number v-model="form.poll_interval" :min="5" :max="1440" />
          <span class="interval-unit">分钟</span>
          <div class="form-help">引擎按此间隔查询所有影巢订阅的新资源，默认 15 分钟。</div>
        </el-form-item>
        <el-form-item label="解锁积分上限">
          <el-input-number v-model="form.max_points" :min="0" :max="999999" />
          <div class="form-help">超过此积分的资源将被跳过，不自动解锁。0 表示不限。</div>
        </el-form-item>
        <el-form-item label="执行预设">
          <el-select v-model="form.exec_preset" style="width: 220px">
            <el-option label="保守（低频率短平快）" value="conservative" />
            <el-option label="均衡（推荐）" value="balanced" />
            <el-option label="激进（高频大批量）" value="aggressive" />
            <el-option label="自定义" value="custom" />
          </el-select>
          <div class="form-help">控制单轮转存上限与转存间隔：保守 10 个 / 60s+30s 抖动；均衡 20 个 / 30s+15s；激进 50 个 / 15s+8s。</div>
        </el-form-item>
        <template v-if="form.exec_preset === 'custom'">
          <el-form-item label="单轮转存上限">
            <el-input-number v-model="form.max_transfers_per_run" :min="1" :max="200" />
            <div class="form-help">一轮订阅轮询内最多转存的候选资源数量。</div>
          </el-form-item>
          <el-form-item label="转存最小间隔">
            <el-input-number v-model="form.transfer_min_interval" :min="1" :max="3600" />
            <span class="interval-unit">秒</span>
            <div class="form-help">相邻两次转存之间的最小间隔（全局生效）。</div>
          </el-form-item>
          <el-form-item label="转存间隔抖动">
            <el-input-number v-model="form.transfer_jitter" :min="0" :max="600" />
            <span class="interval-unit">秒</span>
            <div class="form-help">在最小间隔基础上附加的随机抖动，避免整点突发。</div>
          </el-form-item>
        </template>
        <el-form-item label="单条失败重试">
          <el-input-number v-model="form.slug_max_attempts" :min="0" :max="10" />
          <div class="form-help">单个资源连续失败此次数后进入惩罚期（确定性失败 7 天，其他 1 天），期间跳过。</div>
        </el-form-item>
        <el-form-item label="仅官方通道">
          <el-switch v-model="form.only_official" />
          <div class="form-help">开启后订阅引擎只走官方直连通道，不再向中转通道发请求（官方通道不可用时订阅会失败）。</div>
        </el-form-item>
        <el-form-item label="发布者白名单">
          <el-input v-model="form.publisher_whitelist" placeholder="多个发布者用英文逗号分隔，留空则不过滤" clearable />
          <div class="form-help">仅接受列出的发布者分享，非白名单资源直接跳过。</div>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="saving" @click="save">保存设置</el-button>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useHttpClient } from '@/http/client'

const http = useHttpClient()

const hours = Array.from({ length: 24 }, (_, i) => i)

const form = reactive({
  poll_interval: 15,
  max_points: 0,
  daily_checkin_enabled: true,
  daily_checkin_mode: 'daily' as string,
  daily_checkin_hour: 8,
  sub_checkin_enabled: true,
  sub_checkin_mode: 'daily' as string,
  sub_checkin_hour: 8,
  only_official: false,
  publisher_whitelist: '',
  exec_preset: 'balanced' as string,
  max_transfers_per_run: 20,
  transfer_min_interval: 30,
  transfer_jitter: 15,
  slug_max_attempts: 3,
})

const saving = ref(false)

const load = async () => {
  try {
    const resp = await http.get('/api/cloud/hive/settings')
    if (resp.data?.code === 200) {
      const d = resp.data.data || {}
      form.poll_interval = d.poll_interval > 0 ? d.poll_interval : 15
      form.max_points = d.max_points ?? 0
      form.daily_checkin_enabled = d.daily_checkin_enabled !== false
      form.daily_checkin_mode = d.daily_checkin_mode === 'gamble' ? 'gamble' : 'daily'
      form.daily_checkin_hour = (d.daily_checkin_hour >= 0 && d.daily_checkin_hour <= 23) ? d.daily_checkin_hour : 8
      form.sub_checkin_enabled = d.sub_checkin_enabled !== false
      form.sub_checkin_mode = d.sub_checkin_mode === 'gamble' ? 'gamble' : 'daily'
      form.sub_checkin_hour = (d.sub_checkin_hour >= 0 && d.sub_checkin_hour <= 23) ? d.sub_checkin_hour : 8
      form.only_official = d.only_official === true
      form.publisher_whitelist = d.publisher_whitelist || ''
      form.exec_preset = d.exec_preset || 'balanced'
      form.max_transfers_per_run = d.max_transfers_per_run > 0 ? d.max_transfers_per_run : 20
      form.transfer_min_interval = d.transfer_min_interval > 0 ? d.transfer_min_interval : 30
      form.transfer_jitter = d.transfer_jitter >= 0 ? d.transfer_jitter : 15
      form.slug_max_attempts = d.slug_max_attempts >= 0 ? d.slug_max_attempts : 3
    } else {
      ElMessage.error(resp.data?.message || '加载失败')
    }
  } catch (e: any) {
    ElMessage.error('加载失败：' + (e?.message || ''))
  }
}

const save = async () => {
  saving.value = true
  try {
    const resp = await http.post('/api/cloud/hive/settings', {
      poll_interval: form.poll_interval,
      max_points: form.max_points,
      daily_checkin_enabled: form.daily_checkin_enabled,
      daily_checkin_mode: form.daily_checkin_mode,
      daily_checkin_hour: form.daily_checkin_hour,
      sub_checkin_enabled: form.sub_checkin_enabled,
      sub_checkin_mode: form.sub_checkin_mode,
      sub_checkin_hour: form.sub_checkin_hour,
      only_official: form.only_official,
      publisher_whitelist: form.publisher_whitelist,
      exec_preset: form.exec_preset,
      max_transfers_per_run: form.max_transfers_per_run,
      transfer_min_interval: form.transfer_min_interval,
      transfer_jitter: form.transfer_jitter,
      slug_max_attempts: form.slug_max_attempts,
    })
    if (resp.data?.code === 200) {
      ElMessage.success('影巢设置已保存')
    } else {
      ElMessage.error(resp.data?.message || '保存失败')
    }
  } catch (e: any) {
    ElMessage.error('保存失败：' + (e?.message || ''))
  } finally {
    saving.value = false
  }
}

// ---------------------------------------------------------------------------
// 四通道管理
// ---------------------------------------------------------------------------
interface ChannelRow {
  channel: string
  label: string
  account?: any
}

const channels = ref<ChannelRow[]>([])
const channelHealth = ref<Record<string, { fails: number; cooldown_seconds: number }>>({})
const channelsLoading = ref(false)
const actingOps = reactive<Record<string, string>>({})

const health = (ch: string) => channelHealth.value[ch]

const canTest = new Set(['symedia', 'nanshare', 'official'])
const hasTest = (ch: string) => canTest.has(ch)

const loadChannels = async () => {
  channelsLoading.value = true
  try {
    const resp = await http.get('/api/cloud/hive/channels')
    if (resp.data?.code === 200) {
      channels.value = resp.data.data?.channels || []
      channelHealth.value = resp.data.data?.channel_health || {}
    } else {
      ElMessage.error(resp.data?.message || '通道查询失败')
    }
  } catch (e: any) {
    ElMessage.error('通道查询失败：' + (e?.message || ''))
  } finally {
    channelsLoading.value = false
  }
}

const startEndpoints: Record<string, string> = {
  symedia: '/api/cloud/hive/symedia/start',
  tgtodrive: '/api/cloud/hive/oauth/auth-url',
  nanshare: '/api/cloud/hive/nanshare/start',
  official: '/api/cloud/hive/official/start',
}

const authorize = async (row: ChannelRow) => {
  const ep = startEndpoints[row.channel]
  if (!ep) {
    ElMessage.warning('该通道暂不支持在此发起授权')
    return
  }
  actingOps[row.channel] = 'start'
  try {
    const resp = await http.post(ep)
    if (resp.data?.code !== 200) {
      ElMessage.error(resp.data?.message || '发起授权失败')
      return
    }
    const d = resp.data.data || {}
    const url = d.authorize_url || d.auth_url || ''
    if (!url) {
      ElMessage.error('未取得授权地址')
      return
    }
    window.open(url, '_blank')
    setTimeout(loadChannels, 8000)
  } catch (e: any) {
    ElMessage.error('发起授权失败：' + (e?.message || ''))
  } finally {
    actingOps[row.channel] = ''
  }
}

const refreshEndpoints: Record<string, string> = {
  symedia: '/api/cloud/hive/symedia/refresh',
  tgtodrive: '/api/cloud/hive/oauth/refresh',
  nanshare: '/api/cloud/hive/nanshare/refresh',
  official: '/api/cloud/hive/official/refresh',
}

const refresh = async (row: ChannelRow) => {
  const ep = refreshEndpoints[row.channel]
  if (!ep) return
  actingOps[row.channel] = 'refresh'
  try {
    const resp = await http.post(ep)
    if (resp.data?.code === 200) {
      const authorized = resp.data.data?.authorized === true
      ElMessage.success(resp.data.message || (authorized ? '授权有效' : '授权已失效'))
      await loadChannels()
    } else {
      ElMessage.error(resp.data?.message || '刷新失败')
    }
  } catch (e: any) {
    ElMessage.error('刷新失败：' + (e?.message || ''))
  } finally {
    actingOps[row.channel] = ''
  }
}

const testEndpoints: Record<string, string> = {
  symedia: '/api/cloud/hive/symedia/test',
  nanshare: '/api/cloud/hive/nanshare/test',
  official: '/api/cloud/hive/official/test',
}

const testChannel = async (row: ChannelRow) => {
  const ep = testEndpoints[row.channel]
  if (!ep) return
  actingOps[row.channel] = 'test'
  try {
    const resp = await http.post(ep)
    if (resp.data?.code === 200) {
      ElMessage.success(resp.data.message || '通道连接正常')
    } else {
      ElMessage.error(resp.data?.message || '测试失败')
    }
  } catch (e: any) {
    ElMessage.error('测试失败：' + (e?.message || ''))
  } finally {
    actingOps[row.channel] = ''
  }
}

onMounted(() => {
  load()
  loadChannels()
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
  align-items: baseline;
  gap: 12px;
}
.card-sub {
  font-size: 12px;
  font-weight: 400;
  color: var(--el-text-color-secondary);
}
.cloud-alert {
  margin-bottom: 16px;
}
.hive-form {
  max-width: 640px;
}
.form-help {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.5;
  margin-top: 4px;
  width: 100%;
}
.interval-unit {
  margin-left: 8px;
  color: var(--el-text-color-secondary);
}
.chan-name {
  font-weight: 500;
}
.chan-key {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  margin-top: 2px;
}
.chan-user {
  display: flex;
  align-items: center;
  gap: 6px;
}
.chan-uid {
  transform: scale(0.9);
}
</style>