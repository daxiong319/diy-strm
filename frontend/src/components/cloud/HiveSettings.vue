<template>
  <div class="main-content-container hs-page">
    <!-- 区块：Telegram 频道搜索 -->
    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">Telegram 频道搜索</h3>
        <p class="mv-sec-desc">订阅频道资源并自动转存到云盘</p>
      </div>
      <div class="mv-sec-body">
        <div class="mv-tg-summary">
          <el-tag size="small" type="info" effect="dark" disable-transitions class="mv-tg-count">
            已启用频道 {{ enabledChannelCount }} 个
          </el-tag>
          <span class="mv-tg-hint">频道管理在「资源订阅」页</span>
          <el-button size="small" type="primary" plain class="mv-tg-go" @click="gotoSubscriptions">前往订阅页</el-button>
        </div>
      </div>
    </section>

    <!-- 区块：影巢搜索 -->
    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">影巢搜索</h3>
        <p class="mv-sec-desc">搜索影巢站内资源并转存；需先完成 OAuth 授权</p>
      </div>
      <div class="mv-sec-body">
        <!-- OAuth 授权卡 -->
        <div class="mv-oauth">
          <div class="mv-oauth-user">
            <el-avatar :size="44" :src="authUser?.avatar_url || undefined" class="mv-oauth-avatar">
              {{ (authUser?.nickname || authUser?.username || '影')[0] }}
            </el-avatar>
            <div class="mv-oauth-info">
              <p class="mv-oauth-name">{{ authUser?.nickname || authUser?.username || (auth?.authorized ? '已授权用户' : '未授权') }}</p>
              <p v-if="authUser" class="mv-oauth-sub">
                <template v-if="authUser.points !== undefined">积分 {{ fmtPoints(authUser.points) }}</template>
                <template v-else-if="auth?.authorized">已授权</template>
                <template v-else>完成授权后即可使用影巢搜索</template>
              </p>
              <p v-else class="mv-oauth-sub">
                <template v-if="waiting">正在检测授权状态…</template>
                <template v-else>完成授权后即可使用影巢搜索</template>
              </p>
            </div>
          </div>
          <div class="mv-oauth-actions">
            <el-button v-if="!auth?.authorized && !waiting" size="small" type="primary" :loading="authing" @click="authorizeHive">
              授权影巢
            </el-button>
            <el-button v-if="auth?.authorized && !waiting" size="small" :loading="checking" @click="checkStatus">
              检测状态
            </el-button>
            <el-button v-if="waiting" size="small" plain @click="cancelWait">取消检测</el-button>
          </div>
        </div>

        <!-- 影巢搜索字段 -->
        <div class="mv-fields">
          <div class="mv-field">
            <div class="mv-field-label">
              <span>启用影巢搜索</span>
              <el-switch v-model="form.hive_enabled" />
            </div>
            <p class="mv-field-desc">开启后订阅搜索与手动搜索可使用影巢渠道。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>自动解锁</span>
              <el-switch v-model="form.auto_unlock" />
            </div>
            <p class="mv-field-desc">搜索到资源后自动消耗积分解锁并转存；关闭时仅搜索记录候选。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>解锁积分上限</span>
              <el-input-number v-model="form.max_points" :min="0" :max="999999" controls-position="right" class="mv-num" />
            </div>
            <p class="mv-field-desc">超过此积分的资源将被跳过，不自动解锁。0 表示不限。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>仅官方通道</span>
              <el-switch v-model="form.only_official" />
            </div>
            <p class="mv-field-desc">开启后订阅引擎只走官方直连通道。</p>
          </div>
          <div class="mv-field mv-field-wide">
            <div class="mv-field-label">
              <span>发布者白名单</span>
              <el-input v-model="form.publisher_whitelist" placeholder="多个发布者用英文逗号分隔，留空则不过滤" clearable class="mv-text" />
            </div>
            <p class="mv-field-desc">仅接受列出的发布者分享，非白名单资源直接跳过。</p>
          </div>
        </div>
      </div>
    </section>

    <!-- 区块：盘搜 -->
    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">盘搜</h3>
        <p class="mv-sec-desc">
          全网网盘资源聚合搜索；需自部署盘搜服务
          <a href="https://github.com/fish2018/pansou" target="_blank" rel="noopener" class="mv-pansou-link">（部署说明）</a>
        </p>
      </div>
      <div class="mv-sec-body">
        <div class="mv-fields">
          <div class="mv-field">
            <div class="mv-field-label">
              <span>启用盘搜</span>
              <el-switch v-model="form.pansou_enabled" />
            </div>
            <p class="mv-field-desc">开启后将「盘搜」加入手动搜索渠道；默认关闭。</p>
          </div>
          <div class="mv-field mv-field-wide">
            <div class="mv-field-label">
              <span>服务地址</span>
              <el-input v-model="form.pansou_base_url" placeholder="例如 http://192.168.1.100:80（部署盘搜服务后填写）" clearable class="mv-text" />
            </div>
            <p class="mv-field-desc">盘搜服务地址；未填写时搜索页会提示「未配置盘搜服务」。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>服务账号</span>
              <el-input v-model="form.pansou_username" placeholder="留空则匿名请求" clearable class="mv-text" />
            </div>
            <p class="mv-field-desc">对应盘搜服务 AUTH_USERS 配置；服务未开启认证时留空即可。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>服务密码</span>
              <el-input v-model="form.pansou_password" type="password" show-password placeholder="留空则保持原密码不变" clearable class="mv-text" />
            </div>
            <p class="mv-field-desc">
              {{ pansouPasswordSet ? '已设置服务密码（出于安全考虑不回显）' : '未设置服务密码（服务未开启认证时无需填写）' }}
              ，留空保存不会修改现有密码。
            </p>
          </div>
          <div v-if="form.pansou_enabled && !form.pansou_base_url" class="mv-pansou-warn">
            <el-icon :size="16"><WarningFilled /></el-icon>
            <span>未配置盘搜服务：请填写服务地址后再启用，否则搜索页将提示「未配置盘搜服务」。</span>
          </div>
        </div>
      </div>
    </section>

    <!-- 区块：订阅设置 -->
    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">订阅设置</h3>
        <p class="mv-sec-desc">定时搜索订阅资源并自动转存</p>
      </div>
      <div class="mv-sec-body">
        <div class="mv-fields">
          <div class="mv-field">
            <div class="mv-field-label">
              <span>定时搜索</span>
              <el-switch v-model="form.timed_search_enabled" />
            </div>
            <p class="mv-field-desc">开启后按间隔定时为所有启用订阅搜索新资源。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>搜索间隔</span>
              <el-input-number v-model="form.poll_interval" :min="5" :max="1440" controls-position="right" class="mv-num" />
              <span class="mv-unit">分钟</span>
            </div>
            <p class="mv-field-desc">引擎按此间隔查询所有影巢订阅的新资源。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>搜索后自动转存</span>
              <el-switch v-model="form.search_transfer" />
            </div>
            <p class="mv-field-desc">关闭时只搜索并记录结果，不自动转存（可在订阅列表手动补全）。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>按订阅建子目录</span>
              <el-switch v-model="form.transfer_use_subdir" />
            </div>
            <p class="mv-field-desc">转存到「云盘路径 / 订阅名 (年份)」子目录。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>转存媒体文件</span>
              <el-switch v-model="form.transfer_media" />
            </div>
            <p class="mv-field-desc">转存视频等媒体文件。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>转存字幕文件</span>
              <el-switch v-model="form.transfer_subtitle" />
            </div>
            <p class="mv-field-desc">转存字幕类文件。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>转存非媒体文件</span>
              <el-switch v-model="form.transfer_non_media" />
            </div>
            <p class="mv-field-desc">默认关闭；开启后一并转存其他杂项文件。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>剧集完结宽限天数</span>
              <el-input-number v-model="form.tv_completion_grace_days" :min="0" :max="90" controls-position="right" class="mv-num" />
              <span class="mv-unit">天</span>
            </div>
            <p class="mv-field-desc">最后一集更新后 N 天内视为进行中，不提前判定已完成。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>搜索前同步媒体库</span>
              <el-switch v-model="form.sync_library" />
            </div>
            <p class="mv-field-desc">搜索前先同步媒体库已入库内容，避免重复转存。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>同步等待</span>
              <el-input-number v-model="form.sync_wait" :min="0" :max="3600" controls-position="right" class="mv-num" />
              <span class="mv-unit">秒</span>
            </div>
            <p class="mv-field-desc">同步媒体库完成后的等待时间。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>执行强度</span>
              <el-select v-model="form.exec_preset" class="mv-select">
                <el-option label="保守（低频率短平快）" value="conservative" />
                <el-option label="均衡（推荐）" value="balanced" />
                <el-option label="激进（高频大批量）" value="aggressive" />
                <el-option label="自定义" value="custom" />
              </el-select>
            </div>
            <p class="mv-field-desc">转存上限 / 转存间隔：保守 3 个 / 45-65 秒；均衡 5 个 / 25-40 秒；激进 10 个 / 10-18 秒。搜索批次大小在「自定义」中设置（0 表示不限）。</p>
          </div>
          <template v-if="form.exec_preset === 'custom'">
            <div class="mv-field">
              <div class="mv-field-label">
                <span>搜索批次大小</span>
                <el-input-number v-model="form.run_batch_size" :min="0" :max="200" controls-position="right" class="mv-num" />
              </div>
              <p class="mv-field-desc">单轮轮询最多搜索的订阅数，0 表示不限。</p>
            </div>
            <div class="mv-field">
              <div class="mv-field-label">
                <span>单轮转存上限</span>
                <el-input-number v-model="form.max_transfers_per_run" :min="1" :max="200" controls-position="right" class="mv-num" />
              </div>
              <p class="mv-field-desc">一轮订阅轮询内最多转存的候选资源数量。</p>
            </div>
            <div class="mv-field">
              <div class="mv-field-label">
                <span>转存最小间隔</span>
                <el-input-number v-model="form.transfer_min_interval" :min="1" :max="3600" controls-position="right" class="mv-num" />
                <span class="mv-unit">秒</span>
              </div>
              <p class="mv-field-desc">相邻两次转存之间的最小间隔（全局生效）。</p>
            </div>
            <div class="mv-field">
              <div class="mv-field-label">
                <span>转存间隔抖动</span>
                <el-input-number v-model="form.transfer_jitter" :min="0" :max="600" controls-position="right" class="mv-num" />
                <span class="mv-unit">秒</span>
              </div>
              <p class="mv-field-desc">在最小间隔基础上附加的随机抖动，避免整点突发。</p>
            </div>
          </template>
        </div>
      </div>
    </section>

    <!-- 附加：四通道负载均衡 -->
    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">四通道负载均衡</h3>
        <p class="mv-sec-desc">订阅引擎按「可用优先」轮转四个通道，通道异常时自动逐个降级尝试</p>
      </div>
      <div class="mv-sec-body">
        <el-table :data="channels" v-loading="channelsLoading" size="small" class="mv-table">
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
      </div>
    </section>

    <!-- 附加：每日自动签到 -->
    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">每日自动签到 · 主账号</h3>
        <p class="mv-sec-desc">开启后每天在指定时间自动为影巢主账号签到，获取积分</p>
      </div>
      <div class="mv-sec-body">
        <div class="mv-fields">
          <div class="mv-field">
            <div class="mv-field-label">
              <span>自动签到</span>
              <el-switch v-model="form.daily_checkin_enabled" />
            </div>
            <p class="mv-field-desc">开启后每天在指定时间自动为影巢主账号签到。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>签到时间</span>
              <el-select v-model="form.daily_checkin_hour" :disabled="!form.daily_checkin_enabled" class="mv-select">
                <el-option v-for="h in hours" :key="h" :label="`${String(h).padStart(2,'0')}:00`" :value="h" />
              </el-select>
            </div>
            <p class="mv-field-desc">服务器时区，每天该时刻触发签到。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>签到模式</span>
              <el-radio-group v-model="form.daily_checkin_mode" :disabled="!form.daily_checkin_enabled" size="small">
                <el-radio-button value="daily">普通签到</el-radio-button>
                <el-radio-button value="gamble">赌狗签到</el-radio-button>
              </el-radio-group>
            </div>
            <p class="mv-field-desc">普通签到积分固定；赌狗签到有概率获得更高积分。</p>
          </div>
        </div>
      </div>
    </section>

    <section class="mv-sec">
      <div class="mv-sec-head">
        <h3 class="mv-sec-title">每日自动签到 · 子账号</h3>
        <p class="mv-sec-desc">开启后每天在指定时间自动为已启用的子账号签到</p>
      </div>
      <div class="mv-sec-body">
        <div class="mv-fields">
          <div class="mv-field">
            <div class="mv-field-label">
              <span>自动签到</span>
              <el-switch v-model="form.sub_checkin_enabled" />
            </div>
            <p class="mv-field-desc">开启后每天在指定时间自动为已启用的子账号签到。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>签到时间</span>
              <el-select v-model="form.sub_checkin_hour" :disabled="!form.sub_checkin_enabled" class="mv-select">
                <el-option v-for="h in hours" :key="h" :label="`${String(h).padStart(2,'0')}:00`" :value="h" />
              </el-select>
            </div>
            <p class="mv-field-desc">服务器时区，每天该时刻触发签到。</p>
          </div>
          <div class="mv-field">
            <div class="mv-field-label">
              <span>签到模式</span>
              <el-radio-group v-model="form.sub_checkin_mode" :disabled="!form.sub_checkin_enabled" size="small">
                <el-radio-button value="daily">普通签到</el-radio-button>
                <el-radio-button value="gamble">赌狗签到</el-radio-button>
              </el-radio-group>
            </div>
            <p class="mv-field-desc">普通签到积分固定；赌狗签到有概率获得更高积分。</p>
          </div>
        </div>
      </div>
    </section>

    <!-- 底部保存条 -->
    <div v-if="dirty || saveState !== 'idle'" class="mv-savebar">
      <div class="mv-savebar-left">
        <el-icon v-if="saveState === 'saving'" class="mv-save-spin"><Loading /></el-icon>
        <el-icon v-else-if="saveState === 'saved'" class="mv-save-ok"><CircleCheckFilled /></el-icon>
        <el-icon v-else-if="saveState === 'error'" class="mv-save-err"><WarningFilled /></el-icon>
        <span>
          {{ saveState === 'saving' ? '保存中…' : saveState === 'saved' ? '已保存' : saveState === 'error' ? '保存失败，请重试' : '有未保存的更改' }}
        </span>
      </div>
      <div class="mv-savebar-right">
        <el-button size="small" @click="load(true)">放弃更改</el-button>
        <el-button size="small" type="primary" :loading="saveState === 'saving'" @click="save">保存设置</el-button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { CircleCheckFilled, Loading, WarningFilled } from '@element-plus/icons-vue'
import { useHttpClient } from '@/http/client'

const http = useHttpClient()
const router = useRouter()

const hours = Array.from({ length: 24 }, (_, i) => i)

const form = reactive({
  hive_enabled: true,
  auto_unlock: true,
  max_points: 0,
  only_official: false,
  publisher_whitelist: '',
  timed_search_enabled: true,
  poll_interval: 15,
  search_transfer: true,
  transfer_use_subdir: true,
  transfer_media: true,
  transfer_subtitle: true,
  transfer_non_media: false,
  tv_completion_grace_days: 7,
  sync_library: false,
  sync_wait: 60,
  exec_preset: 'balanced' as string,
  run_batch_size: 12,
  max_transfers_per_run: 5,
  transfer_min_interval: 30,
  transfer_jitter: 15,
  slug_max_attempts: 3,
  daily_checkin_enabled: true,
  daily_checkin_mode: 'daily' as string,
  daily_checkin_hour: 8,
  sub_checkin_enabled: true,
  sub_checkin_mode: 'daily' as string,
  sub_checkin_hour: 8,
  pansou_enabled: false,
  pansou_base_url: '',
  pansou_username: '',
  pansou_password: '',
})
const pansouPasswordSet = ref(false)

// 变更追踪
const base = ref('')
const dirty = ref(false)
const saveState = ref<'idle' | 'saving' | 'saved' | 'error'>('idle')
watch(
  form,
  () => {
    if (saveState.value !== 'saving') dirty.value = JSON.stringify(form) !== base.value
  },
  { deep: true },
)

const load = async (silent = false) => {
  try {
    const resp = await http.get('/api/cloud/hive/settings')
    if (resp.data?.code === 200) {
      const d = resp.data.data || {}
      form.hive_enabled = d.hive_enabled !== false
      form.auto_unlock = d.auto_unlock === true
      form.max_points = d.max_points ?? 0
      form.only_official = d.only_official === true
      form.publisher_whitelist = d.publisher_whitelist || ''
      form.timed_search_enabled = d.timed_search_enabled !== false
      form.poll_interval = d.poll_interval > 0 ? d.poll_interval : 15
      form.search_transfer = d.search_transfer !== false
      form.transfer_use_subdir = d.transfer_use_subdir !== false
      form.transfer_media = d.transfer_media !== false
      form.transfer_subtitle = d.transfer_subtitle !== false
      form.transfer_non_media = d.transfer_non_media === true
      form.tv_completion_grace_days = d.tv_completion_grace_days >= 0 ? d.tv_completion_grace_days : 7
      form.sync_library = d.sync_library === true
      form.sync_wait = d.sync_wait >= 0 ? d.sync_wait : 60
      form.exec_preset = d.exec_preset || 'balanced'
      form.run_batch_size = d.run_batch_size >= 0 ? d.run_batch_size : 12
      form.max_transfers_per_run = d.max_transfers_per_run > 0 ? d.max_transfers_per_run : 5
      form.transfer_min_interval = d.transfer_min_interval > 0 ? d.transfer_min_interval : 30
      form.transfer_jitter = d.transfer_jitter >= 0 ? d.transfer_jitter : 15
      form.slug_max_attempts = d.slug_max_attempts >= 0 ? d.slug_max_attempts : 3
      form.daily_checkin_enabled = d.daily_checkin_enabled !== false
      form.daily_checkin_mode = d.daily_checkin_mode === 'gamble' ? 'gamble' : 'daily'
      form.daily_checkin_hour = (d.daily_checkin_hour >= 0 && d.daily_checkin_hour <= 23) ? d.daily_checkin_hour : 8
      form.sub_checkin_enabled = d.sub_checkin_enabled !== false
      form.sub_checkin_mode = d.sub_checkin_mode === 'gamble' ? 'gamble' : 'daily'
      form.sub_checkin_hour = (d.sub_checkin_hour >= 0 && d.sub_checkin_hour <= 23) ? d.sub_checkin_hour : 8
      form.pansou_enabled = d.pansou_enabled === true
      form.pansou_base_url = d.pansou_base_url || ''
      form.pansou_username = d.pansou_username || ''
      pansouPasswordSet.value = d.pansou_password_set === true
      base.value = JSON.stringify(form)
      dirty.value = false
    } else {
      ElMessage.error(resp.data?.message || '加载失败')
    }
  } catch (e: any) {
    ElMessage.error('加载失败：' + (e?.message || ''))
  } finally {
    saveState.value = 'idle'
  }
}

const save = async () => {
  saveState.value = 'saving'
  try {
    const resp = await http.post('/api/cloud/hive/settings', { ...form })
    if (resp.data?.code === 200) {
      base.value = JSON.stringify(form)
      dirty.value = false
      saveState.value = 'saved'
      setTimeout(() => {
        if (saveState.value === 'saved') saveState.value = 'idle'
      }, 2000)
    } else {
      saveState.value = 'error'
      ElMessage.error(resp.data?.message || '保存失败')
    }
  } catch (e: any) {
    saveState.value = 'error'
    ElMessage.error('保存失败：' + (e?.message || ''))
  }
}

// ---------------------------------------------------------------------------
// OAuth 授权卡（mediavault vN：3s 轮询 × 6 次 = 18s 超时）
// ---------------------------------------------------------------------------
const auth = ref<any>(null)
const waiting = ref(false)
const authing = ref(false)
const checking = ref(false)
const waitTimer = ref<ReturnType<typeof setInterval> | null>(null)
const waitTimeout = ref<ReturnType<typeof setTimeout> | null>(null)

const authUser = computed(() => auth.value?.account?.user)

const fmtPoints = (p: number) => (Number.isInteger(p) ? String(p) : Number(p).toFixed(1))

const loadAuth = async () => {
  try {
    const resp = await http.get('/api/cloud/hive/oauth/status')
    if (resp.data?.code === 200) {
      auth.value = resp.data.data
    }
  } catch {
    /* 状态查询失败静默 */
  }
}

const startWait = () => {
  waiting.value = true
  let tries = 0
  waitTimer.value = setInterval(async () => {
    tries++
    try {
      const resp = await http.get('/api/cloud/hive/oauth/status')
      if (resp.data?.code === 200) {
        auth.value = resp.data.data
        if (resp.data.data?.account?.authorized) {
          stopWait()
          ElMessage.success('影巢授权成功')
        }
      }
    } catch {
      /* 继续 */
    }
    if (tries >= 6) stopWait()
  }, 3000)
  waitTimeout.value = setTimeout(stopWait, 18000)
}

const stopWait = () => {
  if (waitTimer.value) {
    clearInterval(waitTimer.value)
    waitTimer.value = null
  }
  if (waitTimeout.value) {
    clearTimeout(waitTimeout.value)
    waitTimeout.value = null
  }
  waiting.value = false
}

const authorizeHive = async () => {
  authing.value = true
  try {
    const resp = await http.post('/api/cloud/hive/oauth/auth-url')
    if (resp.data?.code !== 200 || !resp.data.data?.auth_url) {
      ElMessage.error(resp.data?.message || '获取授权地址失败')
      return
    }
    const win = window.open(resp.data.data.auth_url, '_blank')
    if (!win) {
      ElMessage.warning('浏览器拦截了弹出窗口，请允许弹窗后重试')
      return
    }
    ElMessage.info('已打开影巢授权页，完成授权后将自动检测状态')
    startWait()
  } catch (e: any) {
    ElMessage.error('获取授权地址失败：' + (e?.message || ''))
  } finally {
    authing.value = false
  }
}

const checkStatus = async () => {
  checking.value = true
  try {
    const resp = await http.post('/api/cloud/hive/oauth/refresh')
    if (resp.data?.code === 200) {
      await loadAuth()
      ElMessage.success(resp.data.message || '检测完成')
    } else {
      ElMessage.error(resp.data?.message || '检测失败')
    }
  } catch (e: any) {
    ElMessage.error('检测失败：' + (e?.message || ''))
  } finally {
    checking.value = false
  }
}

const cancelWait = () => {
  stopWait()
}

// ---------------------------------------------------------------------------
// Telegram 频道摘要
// ---------------------------------------------------------------------------
const enabledChannelCount = ref(0)
const gotoSubscriptions = () => {
  router.push('/cloud-hdhive/subscriptions')
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
    const win = window.open(url, '_blank')
    if (!win) {
      ElMessage.warning('浏览器拦截了弹出窗口，请允许弹窗后重试')
      return
    }
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

onMounted(async () => {
  load()
  loadChannels()
  loadAuth()
  // 频道摘要
  try {
    const resp = await http.get('/api/cloud/channels')
    if (resp.data?.code === 200 && Array.isArray(resp.data.data)) {
      enabledChannelCount.value = resp.data.data.filter((c: any) => c.enabled).length
    }
  } catch {
    /* 忽略 */
  }
})
</script>

<style scoped>
.hs-page {
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding-bottom: 72px;
}
.mv-sec {
  border: 1px solid var(--border);
  border-radius: 10px;
  overflow: hidden;
  background: var(--surface);
}
.mv-sec-head {
  padding: 12px 16px;
  border-bottom: 1px solid var(--border-soft);
}
.mv-sec-title {
  font-size: 14px;
  font-weight: 600;
}
.mv-sec-desc {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 2px;
}
.mv-sec-body {
  padding: 14px 16px;
}
.mv-sec-disabled {
  opacity: 0.75;
}
.mv-pansou-warn {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--warning);
  background: rgba(230, 162, 60, 0.08);
  border: 1px solid rgba(230, 162, 60, 0.25);
  border-radius: 6px;
  padding: 8px 12px;
  width: 100%;
}
.mv-pansou-link {
  color: var(--brand);
  text-decoration: none;
}
.mv-tg-summary {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
}
.mv-tg-count {
}
.mv-tg-hint {
  font-size: 12px;
  color: var(--text-muted);
}
.mv-tg-go {
  margin-left: auto;
}
.mv-oauth {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  border: 1px solid color-mix(in srgb, var(--brand) 25%, transparent);
  background: color-mix(in srgb, var(--brand) 5%, transparent);
  border-radius: 10px;
  padding: 12px 14px;
  margin-bottom: 14px;
}
.mv-oauth-user {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}
.mv-oauth-avatar {
  flex-shrink: 0;
}
.mv-oauth-info {
  min-width: 0;
}
.mv-oauth-name {
  font-size: 14px;
  font-weight: 600;
}
.mv-oauth-sub {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 2px;
}
.mv-oauth-actions {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}
.mv-fields {
  display: grid;
  grid-template-columns: 1fr;
  gap: 14px 24px;
}
@media (min-width: 1280px) {
  .mv-fields {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
.mv-field {
  min-width: 0;
}
.mv-field-wide {
  grid-column: span 1;
}
@media (min-width: 1280px) {
  .mv-field-wide {
    grid-column: span 2;
  }
}
.mv-field-label {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 13px;
  font-weight: 500;
  flex-wrap: wrap;
}
.mv-field-desc {
  font-size: 11px;
  color: var(--text-muted);
  line-height: 1.5;
  margin-top: 4px;
}
.mv-num {
  width: 140px;
}
.mv-unit {
  font-size: 12px;
  color: var(--text-muted);
}
.mv-text {
  flex: 1;
  min-width: 200px;
}
.mv-select {
  width: 220px;
}
.mv-table {
  width: 100%;
}
.chan-name {
  font-weight: 500;
}
.chan-key {
  color: var(--text-muted);
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
.mv-savebar {
  position: fixed;
  left: 50%;
  bottom: 16px;
  transform: translateX(-50%);
  z-index: 2000;
  display: flex;
  align-items: center;
  gap: 16px;
  border: 1px solid var(--border-strong);
  border-radius: 12px;
  background: var(--surface);
  box-shadow: 0 8px 28px rgba(0, 0, 0, 0.18);
  padding: 10px 16px;
  font-size: 13px;
  max-width: calc(100vw - 32px);
}
.mv-savebar-left {
  display: flex;
  align-items: center;
  gap: 8px;
  white-space: nowrap;
}
.mv-save-spin {
  animation: mv-rot 1s linear infinite;
}
@keyframes mv-rot {
  to {
    transform: rotate(360deg);
  }
}
.mv-save-ok {
  color: var(--success);
}
.mv-save-err {
  color: var(--danger);
}
.mv-savebar-right {
  display: flex;
  gap: 6px;
}
</style>