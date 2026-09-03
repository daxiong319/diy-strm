<template>
  <div class="main-content-container hive-page">
    <!-- 页头：统计 + 操作 -->
    <div class="hv-header">
      <p class="hv-stat">
        <template v-if="historyView">已完成 {{ completed }} 个</template>
        <template v-else>当前共有 {{ filteredItems.length }} 个订阅</template>
        <span v-if="keyword.trim()" class="hv-match"> · 匹配 {{ filteredItems.length }}</span>
      </p>
      <div class="hv-actions">
        <el-button size="small" :type="historyView ? 'primary' : 'default'" @click="toggleHistory">
          <el-icon v-if="historyView"><Back /></el-icon>
          {{ historyView ? '返回' : '历史' }}
        </el-button>
        <el-button size="small" :type="multiMode ? 'primary' : 'default'" @click="toggleMulti">
          <el-icon><Checked /></el-icon>
          {{ multiMode ? '退出多选' : '多选' }}
        </el-button>
        <template v-if="!historyView">
          <el-button size="small" :loading="runAllLoading" @click="runAllSearch">
            <el-icon><Refresh /></el-icon>补全
          </el-button>
          <el-button size="small" :loading="backfillPosterLoading" title="为缺少海报的订阅按 TMDB 批量补全封面" @click="backfillPosters">
            <el-icon><Picture /></el-icon>补全封面
          </el-button>
          <el-button size="small" title="预设新建订阅的默认参数（电影 / 电视剧各一套）" @click="openDefaults">
            <el-icon><Setting /></el-icon>默认配置
          </el-button>
          <el-button size="small" type="primary" @click="openAdd">
            <el-icon><Plus /></el-icon>添加
          </el-button>
        </template>
      </div>
    </div>

    <!-- 搜索 + 类型筛选 -->
    <div class="hv-filter-row">
      <div class="hv-search">
        <el-input
          v-model="keyword"
          placeholder="搜索订阅名称"
          size="small"
          class="hv-search-input"
          clearable
          :prefix-icon="Search"
        />
      </div>
      <div class="hv-chips">
        <el-button
          v-for="c in typeChips"
          :key="c.value"
          size="small"
          :type="mediaType === c.value ? 'primary' : 'default'"
          @click="switchType(c.value)"
        >
          {{ c.label }}
        </el-button>
      </div>
    </div>

    <!-- 多选操作条 -->
    <div v-if="multiMode" class="hv-multi-bar">
      <div class="hv-multi-left">
        <span>已选 {{ selectedIds.length }} 个</span>
        <el-button link type="primary" size="small" @click="toggleSelectAll">
          {{ allSelected ? '取消全选' : '全选' }}
        </el-button>
      </div>
      <div class="hv-multi-actions">
        <template v-if="!historyView">
          <el-button size="small" type="success" plain :disabled="!selectedIds.length" :loading="batchActing" @click="batchAction('resume')">
            启动
          </el-button>
          <el-button size="small" type="warning" plain :disabled="!selectedIds.length" :loading="batchActing" @click="batchAction('pause')">
            暂停
          </el-button>
        </template>
        <el-button size="small" type="danger" plain :disabled="!selectedIds.length" :loading="batchActing" @click="confirmBatchDelete">
          删除
        </el-button>
      </div>
    </div>

    <!-- 列表区 -->
    <div v-loading="loading" class="hv-body">
      <div v-if="!loading && filteredItems.length === 0" class="hv-empty">
        <el-empty
          v-if="keyword.trim()"
          :description="`未找到匹配「${keyword}」的订阅`"
          :image-size="72"
        />
        <el-empty v-else description="暂无订阅，点右上角「添加」开始" :image-size="72" />
      </div>
      <div v-else class="hv-grid">
        <div
          v-for="t in filteredItems"
          :key="t.id"
          class="hv-card"
          :class="{ 'hv-card-selected': multiMode && selectedIds.includes(t.id), 'hv-card-wash': t.wash }"
          role="button"
          tabindex="0"
          @click="multiMode ? toggleSelect(t.id) : openDetail(t)"
          @keydown.enter="multiMode ? toggleSelect(t.id) : openDetail(t)"
        >
          <div class="hv-poster-wrap">
            <!-- 多选勾 -->
            <span v-if="multiMode" class="hv-check">
              <el-icon :size="16" :color="selectedIds.includes(t.id) ? 'var(--brand)' : 'rgba(255,255,255,0.7)'">
                <Checked />
              </el-icon>
            </span>
            <el-image v-if="t.poster_url" :src="t.poster_url" fit="cover" class="hv-poster" lazy>
              <template #error>
                <div class="hv-poster-fallback"><el-icon :size="26"><Film /></el-icon></div>
              </template>
            </el-image>
            <div v-else class="hv-poster-fallback">
              <el-icon :size="26"><Film /></el-icon>
            </div>
            <span class="hv-badge hv-badge-type">
              <el-icon :size="10"><template v-if="t.media_type === 'tv'"><VideoPlay /></template><template v-else><Film /></template></el-icon>
              {{ t.media_type === 'tv' ? '剧集' : '电影' }}
            </span>
            <span v-if="(t.vote_average || 0) > 0" class="hv-badge hv-badge-vote">
              <el-icon :size="10"><Star /></el-icon>
              {{ Number(t.vote_average).toFixed(1) }}
            </span>
            <span
              v-if="t.media_type === 'tv' && (t.total_episodes || 0) > 0 && (t.existing_episodes || 0) >= t.total_episodes"
              class="hv-badge hv-badge-full"
            >全</span>
            <span v-if="(t.existing_episodes || 0) > 0" class="hv-badge hv-badge-owned" title="媒体库中已存在">
              <el-icon :size="10"><Checked /></el-icon>
              已入库
            </span>
            <span v-if="t.wash" class="hv-badge hv-badge-wash" title="洗版优先：有更优新源时覆盖库内旧版">
              <el-icon :size="10"><Refresh /></el-icon>
              洗版优先
            </span>
          </div>
          <div class="hv-card-info">
            <div class="hv-title-row">
              <p class="hv-title" :title="t.title || t.tmdb_title">{{ t.title || t.tmdb_title || `TMDB ${t.tmdb_id}` }}</p>
              <el-tag
                size="small"
                class="hv-status"
                :type="statusBadge(t).type"
                effect="dark"
                disable-transitions
              >{{ statusBadge(t).label }}</el-tag>
            </div>
            <div class="hv-meta">
              <p class="hv-meta-line">{{ metaLine1(t) }}</p>
              <p class="hv-meta-line">{{ metaLine2(t) }}</p>
              <p class="hv-meta-line" :title="t.target_dir || '/'">{{ t.target_dir || '/' }}</p>
            </div>
            <div v-if="!multiMode" class="hv-actions">
              <el-tooltip content="搜索转存" placement="top">
                <el-button circle size="small" class="hv-act hv-act-primary" @click.stop="runSingle(t)">
                  <el-icon><Search /></el-icon>
                </el-button>
              </el-tooltip>
              <el-tooltip content="编辑" placement="top">
                <el-button circle size="small" class="hv-act hv-act-info" @click.stop="openEdit(t)">
                  <el-icon><EditPen /></el-icon>
                </el-button>
              </el-tooltip>
              <el-tooltip v-if="t.status !== 'completed'" :content="t.status === 'paused' ? '启动' : '暂停'" placement="top">
                <el-button circle size="small" class="hv-act hv-act-success" @click.stop="togglePause(t)">
                  <el-icon><VideoPause v-if="t.status !== 'paused'" /><VideoPlay v-else /></el-icon>
                </el-button>
              </el-tooltip>
              <el-tooltip content="重置已转存集号" placement="top">
                <el-button circle size="small" class="hv-act hv-act-warning" @click.stop="confirmReset(t)">
                  <el-icon><RefreshLeft /></el-icon>
                </el-button>
              </el-tooltip>
              <el-tooltip content="删除" placement="top">
                <el-button circle size="small" class="hv-act hv-act-danger" @click.stop="confirmDelete(t)">
                  <el-icon><Delete /></el-icon>
                </el-button>
              </el-tooltip>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 添加订阅弹窗 -->
    <el-dialog v-model="addVisible" title="添加订阅" width="640px" class="hive-dialog" destroy-on-close>
      <p class="hv-dialog-desc">搜索 TMDB 条目后订阅，剧集可按季订阅。</p>
      <form class="hv-pick-form" @submit.prevent="doPickSearch">
        <el-input
          v-model="pickKeyword"
          placeholder="搜索 TMDB 电影 / 电视剧…"
          class="hv-pick-input"
          :prefix-icon="Search"
          clearable
        />
        <el-button type="primary" :loading="pickSearching" native-type="submit" :disabled="!pickKeyword.trim()">
          搜索
        </el-button>
      </form>

      <template v-if="defaultsEnabledFor(pendingType)">
        <p class="hv-defaults-hint">
          点「订阅」会弹出订阅设置，预填「默认配置」里对应媒体类型的参数，可逐条调整后再创建。
        </p>
      </template>
      <template v-else>
        <div class="hv-options">
          <label class="hv-k-line">
            <el-checkbox v-model="inheritDefaults" />
            <span>
              留空的项套用默认订阅配置
              <span class="hv-k-tip">
                {{ inheritDefaults ? '下面填的是本次添加的覆盖项，留空的项按条目类型套用「默认配置」里的电影 / 电视剧默认参数。' : '不套用默认配置：下面填什么就是什么，留空即为不限 / 不设。' }}
              </span>
            </span>
          </label>
        </div>
      </template>

      <div v-if="!defaultsEnabledFor(pendingType)" class="hv-add-params">
        <HiveSubParamsForm :model-value="addParams" />
      </div>

      <div class="hv-pick-results" :class="{ 'is-opened': pickResults.length || pickSearching }">
        <div v-if="pickSearching" v-loading="true" class="hv-results-loading" />
        <template v-else-if="pickResults.length === 0">
          <p v-if="pickSearched" class="hv-no-result">TMDB 没有找到相关影片</p>
        </template>
        <div
          v-for="e in pickResults"
          :key="`${e.media_type}-${e.tmdb_id}`"
          class="hv-result-card"
          @click="chooseMedia(e)"
        >
          <div class="hv-r-poster">
            <el-image v-if="e.poster_url" :src="e.poster_url" fit="cover" class="hv-r-img" lazy>
              <template #error><div class="hv-r-fallback"><el-icon><Film /></el-icon></div></template>
            </el-image>
            <div v-else class="hv-r-fallback"><el-icon><Film /></el-icon></div>
          </div>
          <div class="hv-r-info">
            <div class="hv-r-title-row">
              <p class="hv-r-title">{{ e.title }}</p>
              <span class="hv-r-vote"><el-icon :size="11"><Star /></el-icon>{{ Number(e.vote_average || 0).toFixed(1) }}</span>
            </div>
            <p class="hv-r-sub">{{ e.original_title }}{{ e.year ? ` (${e.year})` : '' }}</p>
            <div class="hv-r-tags">
              <el-tag size="small" disable-transitions>{{ e.media_type === 'tvshow' ? '剧集' : '电影' }}</el-tag>
              <el-tag
                v-for="g in (e.genres || []).slice(0, 3)"
                :key="g"
                size="small"
                type="info"
                effect="plain"
                disable-transitions
              >{{ g }}</el-tag>
            </div>
            <p class="hv-r-ov">{{ e.overview }}</p>
            <div class="hv-r-actions">
              <el-select
                v-if="e.media_type === 'tvshow' && (e.seasons || []).length"
                :model-value="seasonFor(e.tmdb_id)"
                size="small"
                class="hv-r-season"
                @change="(v: number) => setSeason(e.tmdb_id, v)"
              >
                <el-option v-for="s in e.seasons" :key="s.season_number" :value="s.season_number" :label="`第 ${s.season_number} 季（${s.episode_count} 集）`" />
              </el-select>
              <el-button
                size="small"
                type="primary"
                :loading="addingTmdbId === e.tmdb_id"
                :disabled="addingTmdbId !== null && addingTmdbId !== e.tmdb_id"
                @click.stop="subscribeItem(e)"
              >订阅</el-button>
              <el-button
                v-if="e.media_type === 'tvshow' && (e.seasons || []).length > 1"
                size="small"
                :loading="addingTmdbId === e.tmdb_id"
                @click.stop="subscribeAllSeasons(e)"
              >订阅全部（{{ (e.seasons || []).length }} 季）</el-button>
            </div>
          </div>
        </div>
      </div>
    </el-dialog>

    <!-- 新建订阅确认弹窗（Oz） -->
    <el-dialog v-model="confirmVisible" :title="`订阅「${confirmItem?.title || ''}」`" width="600px" class="hive-dialog" destroy-on-close>
      <template v-if="confirmItem">
        <div class="hv-confirm-media">
          <el-image v-if="confirmItem.poster_url" :src="confirmItem.poster_url" fit="cover" class="hv-c-poster" lazy>
            <template #error><div class="hv-r-fallback"><el-icon><Film /></el-icon></div></template>
          </el-image>
          <div v-else class="hv-c-poster hv-r-fallback"><el-icon><Film /></el-icon></div>
          <div class="hv-c-info">
            <p class="hv-c-title">{{ confirmItem.title }}</p>
            <p class="hv-c-sub">{{ confirmItem.original_title }}{{ confirmItem.year ? ` (${confirmItem.year})` : '' }}</p>
            <p class="hv-c-meta">
              {{ confirmItem.media_type === 'tvshow' ? `剧集` : `电影` }}
              <template v-if="confirmItem.media_type === 'tvshow'">
                · 第 {{ confirmSeason }} 季
                <template v-if="confirmSeasonAll">（共 {{ (confirmItem.seasons || []).length }} 季）</template>
              </template>
            </p>
          </div>
        </div>
        <div class="hv-confirm-params">
          <HiveSubParamsForm :model-value="confirmParams" />
        </div>
      </template>
      <template #footer>
        <el-button @click="confirmVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="submitConfirm">确认订阅</el-button>
      </template>
    </el-dialog>

    <!-- 编辑弹窗 -->
    <el-dialog v-model="editVisible" :title="`编辑「${editItem?.title || ''}」`" width="600px" class="hive-dialog" destroy-on-close>
      <template v-if="editItem">
        <p class="hv-dialog-desc">{{ editItem.title }}{{ editItem.media_type === 'tv' && editItem.season > 0 ? ` · 第 ${editItem.season} 季` : '' }}</p>
        <HiveSubParamsForm :model-value="editParams" />
      </template>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" :loading="savingEdit" @click="saveEdit">保存</el-button>
      </template>
    </el-dialog>

    <!-- 默认配置弹窗 -->
    <el-dialog v-model="defaultsVisible" title="默认订阅配置" width="620px" class="hive-dialog" destroy-on-close>
      <p class="hv-dialog-desc">预设新建订阅的默认参数（电影 / 电视剧各一套），新建订阅时留空的项自动套用。</p>
      <el-tabs v-model="defaultsTab">
        <el-tab-pane label="电影" name="movie">
          <HiveSubParamsForm :model-value="defaultsMovie" />
        </el-tab-pane>
        <el-tab-pane label="剧集" name="tv">
          <HiveSubParamsForm :model-value="defaultsTv" />
        </el-tab-pane>
      </el-tabs>
      <template #footer>
        <el-button @click="defaultsVisible = false">取消</el-button>
        <el-button type="primary" :loading="savingDefaults" @click="saveDefaults">保存</el-button>
      </template>
    </el-dialog>

    <!-- 详情弹窗 -->
    <el-dialog v-model="detailVisible" width="720px" class="hive-dialog hive-detail" destroy-on-close>
      <template v-if="detailItem">
        <div v-loading="detailLoading" class="hv-detail">
          <template v-if="detail">
            <div v-if="detail.backdrop_url" class="hv-d-backdrop">
              <el-image :src="detail.backdrop_url" fit="cover" class="hv-d-backdrop-img" lazy>
                <template #error><div /></template>
              </el-image>
            </div>
            <div class="hv-d-body">
              <div class="hv-d-head">
                <el-image v-if="detail.poster_url" :src="detail.poster_url" fit="cover" class="hv-d-poster" lazy>
                  <template #error><div class="hv-r-fallback"><el-icon><Film /></el-icon></div></template>
                </el-image>
                <div v-else class="hv-d-poster hv-r-fallback"><el-icon><Film /></el-icon></div>
                <div class="hv-d-head-info">
                  <div class="hv-d-title-row">
                    <h3 class="hv-d-title">{{ detail.title }}</h3>
                    <el-button size="small" type="primary" class="hv-d-search-btn" @click="gotoSearch">
                      <el-icon><Search /></el-icon>搜索资源
                    </el-button>
                  </div>
                  <p class="hv-d-ot">{{ detail.original_title }}</p>
                  <div class="hv-d-vote">
                    <span class="hv-d-vote-num"><el-icon :size="11"><Star /></el-icon>{{ Number(detail.vote_average || 0).toFixed(1) }}</span>
                    <el-tag v-for="g in detail.genres || []" :key="g" size="small" type="info" effect="plain" disable-transitions>{{ g }}</el-tag>
                  </div>
                  <div class="hv-d-meta">
                    <span v-if="detail.release_date">{{ detail.release_date }}</span>
                    <span v-if="detail.runtime">{{ detail.runtime }} 分钟</span>
                    <span v-if="detail.number_of_seasons">{{ detail.number_of_seasons }} 季 · {{ detail.number_of_episodes }} 集</span>
                    <span v-if="detail.status">{{ detail.status }}</span>
                  </div>
                  <p v-if="detail.networks?.length" class="hv-d-networks">{{ detail.networks.join(' / ') }}</p>
                </div>
              </div>
              <template v-if="detail.overview">
                <h4 class="hv-d-sec">剧情简介</h4>
                <p class="hv-d-ov">{{ detail.overview }}</p>
              </template>
              <template v-if="detail.crew?.length">
                <h4 class="hv-d-sec">主创</h4>
                <p class="hv-d-crew">{{ detail.crew.map((c: any) => `${c.name} (${c.job})`).join('、') }}</p>
              </template>
              <template v-if="detail.cast?.length">
                <h4 class="hv-d-sec">演员</h4>
                <div class="hv-d-cast">
                  <div v-for="c in detail.cast.slice(0, 12)" :key="c.name" class="hv-d-cast-item">
                    <el-avatar :size="52" :src="c.profile_url || undefined">
                      {{ (c.name || '?')[0] }}
                    </el-avatar>
                    <p class="hv-d-cast-name">{{ c.name }}</p>
                    <p class="hv-d-cast-role">{{ c.character || c.job }}</p>
                  </div>
                </div>
              </template>
              <template v-if="detailItem?.wash">
                <h4 class="hv-d-sec">洗版优先</h4>
                <div class="hv-d-wash">
                  <span class="hv-pill hv-pill-primary">洗版优先</span>
                  <span class="hv-d-wash-tip">
                    有更优新源（分辨率/编码/声道等评分更高）时自动覆盖库内旧版；
                    <template v-if="detailItem.wash_target">目标目录：{{ detailItem.wash_target }}</template>
                    <template v-else>目标为所选分类目录。</template>
                  </span>
                </div>
              </template>
              <template v-if="detailSubId > 0">
                <h4 class="hv-d-sec">订阅记录</h4>
                <div v-loading="logsLoading" class="hv-d-logs">
                  <div v-for="l in logs" :key="l.id" class="hv-d-log">
                    <el-tag size="small" :type="l.action === 'transfer' ? (l.status === 'success' ? 'success' : 'danger') : 'info'" disable-transitions>
                      {{ l.action === 'transfer' ? '转存' : '搜索' }}
                    </el-tag>
                    <span class="hv-d-log-msg">{{ l.message }}</span>
                    <span class="hv-d-log-time">{{ fmtFullTime(l.created_at) }}</span>
                  </div>
                  <el-empty v-if="!logsLoading && logs.length === 0" description="暂无订阅记录" :image-size="56" />
                </div>
              </template>
            </div>
          </template>
        </div>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Back, Checked, Delete, EditPen, Film, Picture, Plus, Refresh, RefreshLeft, Search, Setting, Star,
  VideoPause, VideoPlay,
} from '@element-plus/icons-vue'
import { useHttpClient } from '@/http/client'
import { isMobile } from '@/utils/deviceUtils'
import CloudDirPicker from './CloudDirPicker.vue'
import HiveSubParamsForm, { emptyParams, type HiveSubParams } from './HiveSubParamsForm.vue'

const http = useHttpClient()
const router = useRouter()
const mobile = computed(() => isMobile())

// ---------------------------------------------------------------------------
// 列表数据（mediavault Gz 数据流：media_type + history 双键查询 + refreshing 轮询）
// ---------------------------------------------------------------------------
const subs = ref<any[]>([])
const loading = ref(false)
const mediaType = ref('') // '' | movie | tv
const historyView = ref(false)
const keyword = ref('')
const refreshing = ref(false)
const pollKey = ref('')
const pollAttempts = ref(0)
let pollTimer: ReturnType<typeof setTimeout> | null = null

const typeChips = [
  { label: '全部', value: '' },
  { label: '电影', value: 'movie' },
  { label: '剧集', value: 'tv' },
]

const filteredItems = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  let list = subs.value
  if (historyView.value) {
    list = subs.value // 历史视图不过滤
  } else {
    list = subs.value.filter((e) => e.status !== 'completed')
  }
  if (!q) return list
  return list.filter(
    (e) =>
      (e.title || '').toLowerCase().includes(q) ||
      (e.original_title || '').toLowerCase().includes(q) ||
      (e.search_keyword || '').toLowerCase().includes(q),
  )
})

const total = computed(() => subs.value.length)
const completed = computed(() => subs.value.filter((e) => e.status === 'completed' || e.finished_at).length)

const load = async (silent = false) => {
  if (!silent) loading.value = true
  try {
    const params: Record<string, any> = { resource_source: 'hdhive' }
    if (mediaType.value) params.media_type = mediaType.value
    if (historyView.value) params.status = 'completed'
    const resp = await http.get('/api/cloud/subscriptions', { params })
    if (resp.data?.code === 200) {
      subs.value = (resp.data.data?.items || []).map((s: any) => ({ ...s }))
      refreshing.value = !!resp.data.data?.refreshing_counts
    } else {
      ElMessage.error(resp.data?.message || '加载失败')
    }
  } catch (e: any) {
    ElMessage.error('加载失败：' + (e?.message || ''))
  } finally {
    loading.value = false
  }
}

// refreshing_counts 轮询（1.5s × 最多 10 次；引擎正在后台补全搜索时自动刷新）
const refreshCounterKey = computed(() => `${mediaType.value}:${historyView.value ? 'history' : 'active'}`)
watch(refreshCounterKey, () => {
  pollKey.value = refreshCounterKey.value
  pollAttempts.value = 0
})
watch(
  () => !refreshing.value,
  () => {
    // 引擎不再刷新时重置轮询计数
    pollAttempts.value = 0
  },
)
const schedulePoll = () => {
  if (!refreshing.value) return
  if (pollAttempts.value >= 10) return
  pollAttempts.value += 1
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = setTimeout(() => {
    load(true)
    schedulePoll()
  }, 1500)
}

// ---------------------------------------------------------------------------
// 多选
// ---------------------------------------------------------------------------
const multiMode = ref(false)
const selectedIds = ref<number[]>([])
const allSelected = computed(
  () => filteredItems.value.length > 0 && filteredItems.value.every((e) => selectedIds.value.includes(e.id)),
)
const toggleMulti = () => {
  multiMode.value = !multiMode.value
  selectedIds.value = []
}
const toggleSelect = (id: number) => {
  const i = selectedIds.value.indexOf(id)
  if (i >= 0) selectedIds.value.splice(i, 1)
  else selectedIds.value.push(id)
}
const toggleSelectAll = () => {
  selectedIds.value = allSelected.value ? [] : filteredItems.value.map((e) => e.id)
}
const toggleHistory = () => {
  historyView.value = !historyView.value
  selectedIds.value = []
  load()
}
const switchType = (v: string) => {
  mediaType.value = v
  load()
}

// ---------------------------------------------------------------------------
// 批量操作
// ---------------------------------------------------------------------------
const batchActing = ref(false)
const batchAction = async (action: 'pause' | 'resume') => {
  if (!selectedIds.value.length) return
  batchActing.value = true
  try {
    const resp = await http.post(`/api/cloud/subscriptions/batch/${action}`, { ids: selectedIds.value })
    if (resp.data?.code === 200) {
      ElMessage.success(resp.data.message || '操作完成')
      selectedIds.value = []
      multiMode.value = false
      await load()
    } else {
      ElMessage.error(resp.data?.message || '操作失败')
    }
  } catch (e: any) {
    ElMessage.error('操作失败：' + (e?.message || ''))
  } finally {
    batchActing.value = false
  }
}
const confirmBatchDelete = async () => {
  try {
    await ElMessageBox.confirm(
      `确定删除选中的 ${selectedIds.value.length} 个订阅？此操作不可撤销。`,
      '批量删除订阅',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  batchActing.value = true
  try {
    const resp = await http.post('/api/cloud/subscriptions/batch/delete', { ids: selectedIds.value })
    if (resp.data?.code === 200) {
      ElMessage.success(resp.data.message || '删除完成')
      selectedIds.value = []
      multiMode.value = false
      await load()
    } else {
      ElMessage.error(resp.data?.message || '批量删除失败')
    }
  } catch (e: any) {
    ElMessage.error('批量删除失败：' + (e?.message || ''))
  } finally {
    batchActing.value = false
  }
}

// ---------------------------------------------------------------------------
// 单条操作
// ---------------------------------------------------------------------------
const runAllLoading = ref(false)

// 批量补全缺失海报（TMDB 回填）
const backfillPosterLoading = ref(false)
const backfillPosters = async () => {
  backfillPosterLoading.value = true
  try {
    const resp = await http.post('/api/cloud/subscriptions/backfill-posters')
    if (resp.data?.code === 200) {
      ElMessage.success({ message: resp.data.message || '海报回填已提交，后台执行中', duration: 6000, showClose: true })
      setTimeout(() => load(), 6000)
    } else {
      ElMessage.error(resp.data?.message || '操作失败')
    }
  } catch (e: any) {
    ElMessage.error('操作失败：' + (e?.message || ''))
  } finally {
    backfillPosterLoading.value = false
  }
}

const runAllSearch = async () => {
  runAllLoading.value = true
  try {
    const resp = await http.post('/api/cloud/subscriptions/run-search')
    if (resp.data?.code === 200) {
      ElMessage.success({ message: resp.data.message || '订阅搜索已触发，后台执行中', duration: 6000, showClose: true })
      refreshing.value = true
      load(true)
      schedulePoll()
    } else {
      ElMessage.error(resp.data?.message || '操作失败')
    }
  } catch (e: any) {
    ElMessage.error('操作失败：' + (e?.message || ''))
  } finally {
    runAllLoading.value = false
  }
}

const runSingle = async (t: any) => {
  try {
    const resp = await http.post(`/api/cloud/subscriptions/run-search/${t.id}`)
    if (resp.data?.code === 200) {
      ElMessage.success({ message: `「${t.title || t.tmdb_title}」搜索已触发，后台执行中`, duration: 6000, showClose: true })
      refreshing.value = true
      load(true)
      schedulePoll()
    } else {
      ElMessage.error(resp.data?.message || '操作失败')
    }
  } catch (e: any) {
    ElMessage.error('操作失败：' + (e?.message || ''))
  }
}

const togglePause = async (t: any) => {
  const paused = t.status !== 'paused'
  try {
    const resp = await http.post('/api/cloud/subscriptions/pause', { id: t.id, paused })
    if (resp.data?.code === 200) {
      ElMessage.success(paused ? '已暂停' : '已启动')
      t.status = paused ? 'paused' : 'subscribing'
    } else {
      ElMessage.error(resp.data?.message || '操作失败')
    }
  } catch (e: any) {
    ElMessage.error('操作失败：' + (e?.message || ''))
  }
}

const confirmReset = async (t: any) => {
  try {
    await ElMessageBox.confirm(
      `确定重置「${t.title || t.tmdb_title}」的已转存记录？重置后将重新搜索转存。`,
      '重置已转存记录',
      { type: 'warning', confirmButtonText: '重置', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  try {
    const resp = await http.post(`/api/cloud/subscriptions/${t.id}/reset-transferred`)
    if (resp.data?.code === 200) {
      ElMessage.success(resp.data.message || '已重置')
      await load(true)
    } else {
      ElMessage.error(resp.data?.message || '操作失败')
    }
  } catch (e: any) {
    ElMessage.error('操作失败：' + (e?.message || ''))
  }
}

const confirmDelete = async (t: any) => {
  try {
    await ElMessageBox.confirm(`确定删除「${t.title || t.tmdb_title}」的订阅？`, '删除订阅', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    const resp = await http.delete(`/api/cloud/subscriptions/${t.id}`)
    if (resp.data?.code === 200) {
      ElMessage.success('已删除')
      await load()
    } else {
      ElMessage.error(resp.data?.message || '删除失败')
    }
  } catch (e: any) {
    ElMessage.error('删除失败：' + (e?.message || ''))
  }
}

// ---------------------------------------------------------------------------
// 状态 / 元信息渲染
// ---------------------------------------------------------------------------
const statusBadge = (t: any) => {
  if (t.status === 'completed' || t.finished_at) return { label: '已完成', type: 'success' as any }
  if (t.status === 'paused') return { label: '已暂停', type: 'info' as any }
  return { label: '进行中', type: 'primary' as any }
}
const metaLine1 = (t: any) => {
  const parts: string[] = []
  if (t.year) parts.push(String(t.year))
  if (t.media_type === 'tv') {
    if (t.season > 0) parts.push(`第 ${t.season} 季`)
    if (t.total_episodes > 0) parts.push(`共 ${t.total_episodes} 集`)
  }
  return parts.join(' · ') || ' '
}
const metaLine2 = (t: any) => {
  const parts: string[] = []
  if (t.media_type === 'tv') parts.push(`已有 ${t.existing_episodes || 0} 集`)
  parts.push(t.resolution || '不限')
  if (t.effect) parts.push(t.effect)
  if (t.search_sources) parts.push(t.search_sources)
  return parts.join(' · ').replace(/^ · /, '') || ' '
}

// ---------------------------------------------------------------------------
// TMDB 选片与添加
// ---------------------------------------------------------------------------
interface PickItem {
  tmdb_id: number
  title: string
  original_title: string
  year: number
  poster_url: string
  overview: string
  vote_average: number
  media_type: 'movie' | 'tvshow'
  genres?: string[]
  seasons?: { season_number: number; name: string; episode_count: number }[]
}

const addVisible = ref(false)
const pickKeyword = ref('')
const pickSearching = ref(false)
const pickSearched = ref(false)
const pickResults = ref<PickItem[]>([])
const seasonChoices = ref<Record<number, number>>({})
const addingTmdbId = ref<number | null>(null)
const inheritDefaults = ref(true)

const addParams = reactive(emptyParams())
const pendingType = ref<'movie' | 'tv' | ''>('')
const defaults = reactive<{ movie: HiveSubParams; tv: HiveSubParams }>({ movie: emptyParams(), tv: emptyParams() })

const defaultsEnabledFor = (t: string) => {
  if (t !== 'movie' && t !== 'tv') return false
  const d = t === 'movie' ? defaults.movie : defaults.tv
  return !!(d && (d.target_dir || d.resolution || d.search_keyword || d.include_regex || d.exclude_regex || d.search_sources))
}

const openAdd = async () => {
  addVisible.value = true
  pickKeyword.value = ''
  pickResults.value = []
  pickSearched.value = false
  Object.assign(addParams, emptyParams())
  inheritDefaults.value = true
  pendingType.value = ''
  await fetchDefaults()
}

const fetchDefaults = async () => {
  try {
    const resp = await http.get('/api/cloud/hive/settings')
    if (resp.data?.code === 200) {
      const d = resp.data.data || {}
      Object.assign(defaults.movie, { ...emptyParams(), ...(safeParseJson(d.subscription_defaults_movie) || {}) })
      Object.assign(defaults.tv, { ...emptyParams(), ...(safeParseJson(d.subscription_defaults_tv) || {}) })
    }
  } catch {
    /* 默认配置获取失败不阻塞 */
  }
}
const safeParseJson = (s: string) => {
  try {
    const v = JSON.parse(s || '')
    return typeof v === 'object' && v ? v : null
  } catch {
    return null
  }
}

const doPickSearch = async () => {
  const q = pickKeyword.value.trim()
  if (!q) return
  pickSearching.value = true
  pickSearched.value = false
  pickResults.value = []
  try {
    const [movieResp, tvResp] = await Promise.all([
      http.get('/api/scrape/tmdb-search', { params: { name: q, type: 'movie' } }),
      http.get('/api/scrape/tmdb-search', { params: { name: q, type: 'tvshow' } }),
    ])
    const merge = (resp: any, type: 'movie' | 'tvshow') =>
      resp.data?.code === 200 && Array.isArray(resp.data.data)
        ? resp.data.data.map((d: any) => ({ ...d, media_type: type }))
        : []
    const items = [...merge(movieResp, 'movie'), ...merge(tvResp, 'tvshow')]
      .sort((a, b) => (b.vote_average || 0) - (a.vote_average || 0))
      .slice(0, 12)
    pickResults.value = items
  } catch (e: any) {
    ElMessage.error('查询失败：' + (e?.message || ''))
  } finally {
    pickSearched.value = true
    pickSearching.value = false
  }
}

const chooseMedia = async (item: PickItem) => {
  // 点击结果卡默认选择第一个可用季（不弹详情；订阅按钮在卡内）
  if (item.media_type === 'tvshow' && !seasonChoices.value[item.tmdb_id]) {
    const seasons = item.seasons || []
    if (seasons.length && !seasonChoices.value[item.tmdb_id]) {
      seasonChoices.value[item.tmdb_id] = seasons.some((s) => s.season_number === 1)
        ? 1
        : seasons[0]?.season_number || 1
    }
  }
}
const seasonFor = (tmdbId: number) => seasonChoices.value[tmdbId] || 1
const setSeason = (tmdbId: number, v: number) => {
  seasonChoices.value[tmdbId] = v
}

// 每条订阅的构造参数：默认配置套用 + 本次表单覆盖
const buildParams = (item: PickItem, inherit: boolean): HiveSubParams => {
  const type = item.media_type === 'tvshow' ? 'tv' : 'movie'
  const defaultsForType = type === 'tv' ? defaults.tv : defaults.movie
  const merged: HiveSubParams = emptyParams()
  if (inherit && defaultsEnabledFor(type)) {
    Object.assign(merged, JSON.parse(JSON.stringify(defaultsForType)))
  }
  // 本次表单覆盖（非空才覆盖）
  const over = addParams
  if (over.source_type) merged.source_type = over.source_type
  if (over.target_dir) merged.target_dir = over.target_dir
  if (over.search_keyword) merged.search_keyword = over.search_keyword
  if (over.resolution) merged.resolution = over.resolution
  if (over.effect) merged.effect = over.effect
  if (over.search_sources) merged.search_sources = over.search_sources
  if (over.include_regex) merged.include_regex = over.include_regex
  if (over.exclude_regex) merged.exclude_regex = over.exclude_regex
  merged.auto_finish = over.auto_finish
  merged.wash = over.wash
  if (over.wash) {
    merged.wash_target = over.wash_target
    merged.replace_old = over.replace_old
  }
  return merged
}

const payloadFor = (item: PickItem, season: number, params: HiveSubParams) => {
  const mediaType = item.media_type === 'tvshow' ? 'tv' : 'movie'
  return {
    source_type: params.source_type,
    resource_source: 'hdhive',
    target_dir: params.target_dir,
    enabled: true,
    auto_finish: params.auto_finish,
    wash: params.wash,
    wash_target: params.wash_target,
    replace_old: params.replace_old,
    media_type: mediaType,
    tmdb_id: item.tmdb_id,
    tmdb_title: item.title,
    original_title: item.original_title || '',
    year: item.year || 0,
    poster_url: item.poster_url || '',
    overview: item.overview || '',
    vote_average: item.vote_average || 0,
    genres: (item.genres || []).join(','),
    search_keyword: params.search_keyword || item.title,
    resolution: params.resolution,
    effect: params.effect,
    search_sources: params.search_sources,
    include_regex: params.include_regex,
    exclude_regex: params.exclude_regex,
    season,
    total_seasons: mediaType === 'tv' ? item.seasons?.length || 0 : 0,
    total_episodes: mediaType === 'tv' ? (item as any).number_of_episodes || 0 : 0,
  }
}

const subscribing = ref(false)
const confirmVisible = ref(false)
const confirmItem = ref<PickItem | null>(null)
const confirmSeason = ref(1)
const confirmSeasonAll = ref(false)
const confirmParams = reactive(emptyParams())
const submitting = ref(false)

const subscribeItem = async (item: PickItem) => {
  pendingType.value = item.media_type === 'tvshow' ? 'tv' : 'movie'
  const params = buildParams(item, !defaultsEnabledFor(pendingType.value) ? inheritDefaults.value : true)
  const season = item.media_type === 'tvshow' ? seasonChoices.value[item.tmdb_id] || 1 : 0
  if (item.media_type === 'tvshow' && (item.seasons || []).length === 0) {
    ElMessage.warning('未获取到季数信息，请重试')
    return
  }
  if (defaultsEnabledFor(pendingType.value)) {
    // 有默认配置：弹出确认弹窗，可逐条调整
    confirmItem.value = item
    confirmSeason.value = season || 1
    confirmSeasonAll.value = false
    Object.assign(confirmParams, JSON.parse(JSON.stringify(params)))
    confirmVisible.value = true
  } else if (item.media_type === 'tvshow' && (item.seasons || []).length > 1 && inheritDefaults.value) {
    confirmItem.value = item
    confirmSeason.value = season || 1
    confirmSeasonAll.value = false
    Object.assign(confirmParams, JSON.parse(JSON.stringify(params)))
    confirmVisible.value = true
  } else {
    await doSubscribe(payloadFor(item, season || 0, params))
  }
}

const subscribeAllSeasons = async (item: PickItem) => {
  pendingType.value = 'tv'
  const params = buildParams(item, !defaultsEnabledFor('tv') ? inheritDefaults.value : true)
  if (defaultsEnabledFor('tv') || inheritDefaults.value) {
    // 弹确认：一次订阅全部季
    confirmItem.value = item
    confirmSeason.value = 1
    confirmSeasonAll.value = true
    Object.assign(confirmParams, JSON.parse(JSON.stringify(params)))
    confirmVisible.value = true
  } else {
    await doSubscribe(payloadFor(item, 0, params))
  }
}

const submitConfirm = async () => {
  const item = confirmItem.value
  if (!item) return
  submitting.value = true
  try {
    const season = item.media_type === 'tvshow' ? confirmSeason.value : 0
    const p = payloadFor(item, season, confirmParams)
    if (confirmSeasonAll.value) {
      const seasons = (item.seasons || []).map((s) => s.season_number).filter((n) => n > 0)
      const payloads = seasons.map((n) => payloadFor(item, n, confirmParams))
      const ok = await doSubscribeBulk(payloads)
      const fail = seasons.length - ok
      if (ok > 0) {
        ElMessage.success(seasons.length > 1 ? `已订阅 ${ok}/${seasons.length} 季` : '订阅添加成功')
      }
      if (fail > 0) {
        ElMessage.error(`${fail} 季订阅失败，请稍后重试`)
      }
    } else {
      // 单季订阅
      if (item.media_type === 'tvshow') {
        // 需要补总集数：一次只订一季时 total_episodes 用该季集数
        p.total_episodes = (item.seasons || [])[confirmSeason.value]
          ? (item.seasons || []).find((s) => s.season_number === confirmSeason.value)?.episode_count || 0
          : 0
      }
      await doSubscribe(p)
    }
    confirmVisible.value = false
  } finally {
    submitting.value = false
  }
}

const doSubscribeBulk = async (payloads: any[]): Promise<number> => {
  let ok = 0
  for (const p of payloads) {
    try {
      const resp = await http.post('/api/cloud/subscriptions', p)
      if (resp.data?.code === 200) ok++
    } catch {
      /* 单个失败继续 */
    }
  }
  return ok
}

const doSubscribe = async (p: any) => {
  addingTmdbId.value = p.tmdb_id
  try {
    const resp = await http.post('/api/cloud/subscriptions', p)
    if (resp.data?.code === 200) {
      ElMessage.success('订阅添加成功')
      await load()
    } else {
      ElMessage.error(resp.data?.message || '添加订阅失败')
    }
  } catch (e: any) {
    ElMessage.error('添加订阅失败：' + (e?.message || ''))
  } finally {
    addingTmdbId.value = null
  }
}

// ---------------------------------------------------------------------------
// 编辑
// ---------------------------------------------------------------------------
const editVisible = ref(false)
const editItem = ref<any>(null)
const editParams = reactive(emptyParams())
const savingEdit = ref(false)

const openEdit = (t: any) => {
  editItem.value = t
  Object.assign(editParams, {
    source_type: t.source_type || '123',
    target_dir: t.target_dir || '',
    search_keyword: t.search_keyword || t.tmdb_title || '',
    resolution: t.resolution || '',
    effect: t.effect || '',
    search_sources: t.search_sources || '',
    include_regex: t.include_regex || '',
    exclude_regex: t.exclude_regex || '',
    auto_finish: t.auto_finish !== false,
    wash: !!t.wash,
    wash_target: t.wash_target || '',
    replace_old: t.replace_old !== false,
  })
  editVisible.value = true
}

const saveEdit = async () => {
  const t = editItem.value
  if (!t) return
  savingEdit.value = true
  try {
    const resp = await http.put(`/api/cloud/subscriptions/${t.id}`, {
      search_keyword: editParams.search_keyword,
      source_type: editParams.source_type,
      target_dir: editParams.target_dir,
      resolution: editParams.resolution,
      effect: editParams.effect,
      search_sources: editParams.search_sources,
      include_regex: editParams.include_regex.trim(),
      exclude_regex: editParams.exclude_regex.trim(),
      auto_finish: editParams.auto_finish,
      wash: editParams.wash,
      wash_target: editParams.wash_target,
      replace_old: editParams.replace_old,
    })
    if (resp.data?.code === 200) {
      ElMessage.success('订阅已更新')
      editVisible.value = false
      await load(true)
    } else {
      ElMessage.error(resp.data?.message || '保存失败')
    }
  } catch (e: any) {
    ElMessage.error('保存失败：' + (e?.message || ''))
  } finally {
    savingEdit.value = false
  }
}

// ---------------------------------------------------------------------------
// 默认配置
// ---------------------------------------------------------------------------
const defaultsVisible = ref(false)
const defaultsTab = ref('movie')
const savingDefaults = ref(false)
const defaultsMovie = reactive(emptyParams())
const defaultsTv = reactive(emptyParams())

const openDefaults = async () => {
  await fetchDefaults()
  Object.assign(defaultsMovie, JSON.parse(JSON.stringify(defaults.movie)))
  Object.assign(defaultsTv, JSON.parse(JSON.stringify(defaults.tv)))
  defaultsVisible.value = true
}

const saveDefaults = async () => {
  savingDefaults.value = true
  try {
    const resp = await http.post('/api/cloud/hive/settings', {
      subscription_defaults_movie: JSON.stringify(defaultsMovie),
      subscription_defaults_tv: JSON.stringify(defaultsTv),
    })
    if (resp.data?.code === 200) {
      ElMessage.success('默认订阅配置已保存')
      Object.assign(defaults.movie, JSON.parse(JSON.stringify(defaultsMovie)))
      Object.assign(defaults.tv, JSON.parse(JSON.stringify(defaultsTv)))
      defaultsVisible.value = false
    } else {
      ElMessage.error(resp.data?.message || '保存失败')
    }
  } catch (e: any) {
    ElMessage.error('保存失败：' + (e?.message || ''))
  } finally {
    savingDefaults.value = false
  }
}

// ---------------------------------------------------------------------------
// 详情（TMDB 详情 + 主创 + 订阅记录）
// ---------------------------------------------------------------------------
const detailVisible = ref(false)
const detailItem = ref<any>(null)
const detail = ref<any>(null)
const detailLoading = ref(false)
const detailSubId = ref(0)
const logs = ref<any[]>([])
const logsLoading = ref(false)

const openDetail = async (t: any) => {
  detailItem.value = t
  detailVisible.value = true
  detail.value = null
  detailLoading.value = true
  detailSubId.value = t.id || 0
  logs.value = []
  try {
    const [detailResp, logsResp] = await Promise.all([
      http.get(`/api/cloud/subscriptions/detail/${t.tmdb_id}`, { params: { media_type: t.media_type } }),
      t.id ? http.get(`/api/cloud/subscriptions/${t.id}/logs`) : Promise.resolve(null),
    ])
    if (detailResp.data?.code === 200) detail.value = detailResp.data.data
    if (logsResp?.data?.code === 200) logs.value = logsResp.data.data?.logs || []
  } catch {
    /* 网络错误由界面空状态呈现 */
  } finally {
    detailLoading.value = false
  }
}

// 构造搜索关键词（对齐 mediavault aF：tv + 第 N 季 → 中文数字 / S0N / 原标题）
const cnNums = ['一', '二', '三', '四', '五', '六', '七', '八', '九', '十', '十一', '十二', '十三', '十四', '十五', '十六', '十七', '十八', '十九', '二十', '二十一', '二十二', '二十三', '二十四', '二十五', '二十六', '二十七', '二十八', '二十九', '三十']
const buildSearchKeywords = (title: string, mediaType: string, season: number) => {
  const kw = (detail.value?.search_keyword) || title || ''
  if (mediaType === 'tv' && season > 1) {
    return `${kw} 第${cnNums[season - 1] || season}季 / ${kw} S${String(season).padStart(2, '0')} / ${kw}`
  }
  return kw
}
const gotoSearch = () => {
  const t = detailItem.value
  const kw = buildSearchKeywords(t?.title || '', t?.media_type || '', t?.season || 0)
  detailVisible.value = false
  router.push({ path: '/cloud-hdhive/search', query: { keyword: kw } })
}

const fmtFullTime = (t: string) => {
  const d = new Date(t)
  if (isNaN(d.getTime())) return t || ''
  return d.toLocaleString('zh-CN', { hour12: false })
}

// ---------------------------------------------------------------------------
onMounted(() => {
  load()
  // 列表轮询
  const pollFn = () => {
    schedulePoll()
    if (pollTimer) clearTimeout(pollTimer)
    pollTimer = setTimeout(pollFn, 3600_000)
  }
  pollFn()
})
</script>

<style scoped>
.hive-page {
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.hv-header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.hv-stat {
  font-size: 13px;
  color: var(--text-muted);
}
.hv-match {
  color: var(--brand);
}
.hv-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.hv-filter-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.hv-search {
  flex: 1;
  min-width: 180px;
  max-width: 260px;
}
.hv-chips {
  display: flex;
  gap: 6px;
}
.hv-multi-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  border: 1px solid color-mix(in srgb, var(--brand) 30%, transparent);
  background: color-mix(in srgb, var(--brand) 7%, transparent);
  border-radius: 8px;
  padding: 8px 12px;
}
.hv-multi-left {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 13px;
  color: var(--text-muted);
}
.hv-multi-actions {
  display: flex;
  gap: 6px;
}
.hv-body {
  min-height: 120px;
}
.hv-empty {
  padding: 40px 0;
}
.hv-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}
@media (min-width: 640px) {
  .hv-grid { grid-template-columns: repeat(4, minmax(0, 1fr)); }
}
@media (min-width: 768px) {
  .hv-grid { grid-template-columns: repeat(6, minmax(0, 1fr)); }
}
@media (min-width: 1024px) {
  .hv-grid { grid-template-columns: repeat(7, minmax(0, 1fr)); }
}
@media (min-width: 1280px) {
  .hv-grid { grid-template-columns: repeat(9, minmax(0, 1fr)); }
}
.hv-card {
  cursor: pointer;
  border: 1px solid var(--border);
  border-radius: 10px;
  overflow: hidden;
  transition: border-color 0.2s, box-shadow 0.2s;
  background: var(--surface);
}
.hv-card:hover {
  border-color: var(--brand-strong);
  box-shadow: var(--shadow-soft);
}
.hv-card-selected {
  border-color: var(--brand);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--brand) 40%, transparent);
}
.hv-poster-wrap {
  position: relative;
  aspect-ratio: 2 / 3;
  overflow: hidden;
  background: var(--surface-muted);
}
.hv-poster,
.hv-poster-fallback {
  width: 100%;
  height: 100%;
}
.hv-poster-fallback {
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  background: linear-gradient(160deg, var(--surface-muted), var(--border-soft));
}
.hv-check {
  position: absolute;
  top: 8px;
  left: 0;
  right: 0;
  width: fit-content;
  margin: 0 auto;
  z-index: 10;
}
.hv-badge {
  position: absolute;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  border-radius: 4px;
  padding: 2px 6px;
  font-size: 10px;
  line-height: 1.3;
  color: #fff;
  background: rgba(0, 0, 0, 0.6);
}
.hv-badge-type { top: 8px; left: 8px; }
.hv-badge-vote {
  top: 8px;
  right: 8px;
  color: var(--warning);
}
.hv-badge-full {
  right: 8px;
  bottom: 8px;
  background: color-mix(in srgb, var(--success) 20%, transparent);
  color: var(--success);
  font-weight: 500;
}
.hv-badge-owned {
  bottom: 8px;
  left: 8px;
  color: var(--success);
}
.hv-badge-wash {
  bottom: 8px;
  left: 50%;
  transform: translateX(-50%);
  color: var(--brand);
  background: color-mix(in srgb, var(--brand) 22%, rgba(0, 0, 0, 0.6));
  font-weight: 500;
  white-space: nowrap;
  z-index: 5;
}
.hv-card-wash {
  border-color: color-mix(in srgb, var(--brand) 45%, var(--border));
  box-shadow: 0 0 0 1px color-mix(in srgb, var(--brand) 25%, transparent), 0 4px 16px rgba(62, 224, 208, 0.12);
}
.hv-card-wash:hover {
  border-color: var(--brand);
  box-shadow: 0 0 0 1px color-mix(in srgb, var(--brand) 40%, transparent), var(--shadow-soft);
}
.hv-card-info {
  padding: 6px 8px;
}
.hv-title-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.hv-title {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  font-weight: 500;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hv-status {
  flex-shrink: 0;
}
.hv-meta {
  margin-top: 2px;
}
.hv-meta-line {
  font-size: 11px;
  line-height: 1.5;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hv-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  align-items: center;
  gap: 2px;
  margin-top: 4px;
  padding-top: 2px;
}
.hv-act {
  color: var(--text-muted);
}
.hv-act-primary:hover { color: var(--brand); }
.hv-act-info:hover { color: var(--info); }
.hv-act-success:hover { color: var(--success); }
.hv-act-warning:hover { color: var(--warning); }
.hv-act-danger:hover { color: var(--danger); }
.hv-dialog-desc {
  font-size: 12px;
  color: var(--text-muted);
  margin-bottom: 10px;
}
.hv-pick-form {
  display: flex;
  gap: 8px;
  margin-bottom: 10px;
}
.hv-pick-input {
  flex: 1;
}
.hv-defaults-hint {
  border-bottom: 1px solid var(--border);
  padding: 0 0 10px;
  font-size: 11px;
  color: var(--text-muted);
}
.hv-options {
  border-bottom: 1px solid var(--border);
  padding-bottom: 10px;
  margin-bottom: 10px;
}
.hv-k-line {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 11px;
  cursor: pointer;
}
.hv-k-tip {
  display: block;
  margin-top: 2px;
  color: var(--text-muted);
}
.hv-add-params {
  margin-bottom: 10px;
}
.hv-pick-results {
  max-height: 320px;
  overflow-y: auto;
  border: 1px solid var(--border);
  border-radius: 8px;
}
.hv-pick-results.is-opened { border-color: var(--brand); }
.hv-results-loading {
  min-height: 80px;
}
.hv-no-result {
  padding: 24px;
  text-align: center;
  font-size: 13px;
  color: var(--text-muted);
}
.hv-result-card {
  display: flex;
  gap: 12px;
  padding: 10px;
  border-bottom: 1px solid var(--border-soft);
  cursor: pointer;
  transition: background 0.15s;
}
.hv-result-card:hover {
  background: var(--surface-muted);
}
.hv-result-card:last-child {
  border-bottom: none;
}
.hv-r-poster {
  width: 64px;
  height: 96px;
  border-radius: 6px;
  overflow: hidden;
  flex-shrink: 0;
  background: var(--surface-muted);
}
.hv-r-img {
  width: 100%;
  height: 100%;
}
.hv-r-fallback {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  color: var(--text-muted);
}
.hv-r-info {
  flex: 1;
  min-width: 0;
}
.hv-r-title-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.hv-r-title {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hv-r-vote {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
  font-size: 12px;
  color: var(--warning);
}
.hv-r-sub {
  font-size: 11px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hv-r-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 4px;
}
.hv-r-ov {
  margin-top: 4px;
  font-size: 12px;
  color: var(--text-muted);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.hv-r-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
}
.hv-r-season {
  width: 168px;
}
.hv-confirm-media {
  display: flex;
  gap: 12px;
  margin-bottom: 14px;
}
.hv-c-poster {
  width: 72px;
  height: 108px;
  border-radius: 6px;
  overflow: hidden;
  flex-shrink: 0;
  background: var(--surface-muted);
}
.hv-c-info {
  flex: 1;
  min-width: 0;
}
.hv-c-title {
  font-size: 15px;
  font-weight: 600;
}
.hv-c-sub {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 2px;
}
.hv-c-meta {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 6px;
}
.hv-confirm-params {
  border-top: 1px solid var(--border-soft);
  padding-top: 12px;
  max-height: 46vh;
  overflow-y: auto;
}
.hv-detail {
  min-height: 200px;
}
.hv-d-backdrop {
  position: relative;
  height: 150px;
  overflow: hidden;
  border-radius: 8px 8px 0 0;
  margin: -20px -20px 0;
  background: var(--surface-muted);
}
.hv-d-backdrop-img {
  width: 100%;
  height: 100%;
  opacity: 0.45;
}
.hv-d-body {
  padding: 16px 0 0;
}
.hv-d-head {
  display: flex;
  gap: 16px;
}
.hv-d-poster {
  width: 96px;
  height: 144px;
  border-radius: 8px;
  overflow: hidden;
  flex-shrink: 0;
  background: var(--surface-muted);
}
.hv-d-head-info {
  flex: 1;
  min-width: 0;
}
.hv-d-title-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}
.hv-d-title {
  font-size: 16px;
  font-weight: 600;
  line-height: 1.4;
}
.hv-d-search-btn {
  flex-shrink: 0;
}
.hv-d-ot {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 2px;
}
.hv-d-vote {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-top: 10px;
}
.hv-d-vote-num {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 12px;
  font-weight: 600;
  color: var(--warning);
  background: color-mix(in srgb, var(--warning) 10%, transparent);
  border-radius: 4px;
  padding: 2px 6px;
}
.hv-d-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 12px;
  font-size: 12px;
  color: var(--text-muted);
}
.hv-d-networks {
  margin-top: 6px;
  font-size: 12px;
  color: var(--text-muted);
}
.hv-d-sec {
  margin-top: 16px;
  font-size: 13px;
  font-weight: 600;
}
.hv-d-ov {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.7;
  color: var(--text-regular);
}
.hv-d-crew {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.7;
  color: var(--text-regular);
}
.hv-d-cast {
  display: flex;
  gap: 12px;
  overflow-x: auto;
  padding: 10px 2px 4px;
}
.hv-d-cast-item {
  width: 64px;
  flex-shrink: 0;
  text-align: center;
}
.hv-d-cast-name {
  font-size: 11px;
  margin-top: 4px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hv-d-cast-role {
  font-size: 10px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  margin-top: 2px;
}
.hv-d-logs {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 8px;
  max-height: 260px;
  overflow-y: auto;
}
.hv-d-log {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 12px;
}
.hv-d-log-msg {
  flex: 1;
  min-width: 0;
  color: var(--text-regular);
  line-height: 1.6;
  word-break: break-all;
}
.hv-d-log-time {
  flex-shrink: 0;
  font-size: 11px;
  color: var(--text-muted);
}
.hv-d-wash {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  border: 1px solid color-mix(in srgb, var(--brand) 30%, var(--border));
  background: color-mix(in srgb, var(--brand) 6%, transparent);
  border-radius: 8px;
  padding: 10px 12px;
}
.hv-d-wash-tip {
  font-size: 12px;
  color: var(--text-muted);
}
.hv-pill {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 9px;
  border-radius: 999px;
  font-size: 12px;
  line-height: 18px;
  white-space: nowrap;
}
.hv-pill-primary {
  color: var(--brand);
  background: color-mix(in srgb, var(--brand) 15%, transparent);
}
</style>