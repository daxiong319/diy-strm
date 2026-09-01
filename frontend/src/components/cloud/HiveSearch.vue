<template>
  <div class="main-content-container hs-page">
    <!-- 搜索表单 -->
    <form class="hs-form" @submit.prevent="submitSearch">
      <el-input
        v-model="kw"
        size="small"
        class="hs-input"
        placeholder="输入资源关键词搜索…"
        enter-key-hint="search"
        clearable
        :prefix-icon="Search"
        :disabled="searching || identifying"
      />
      <el-button size="small" type="primary" native-type="submit" :disabled="searching || identifying || !kw.trim()" :loading="searching || identifying">
        {{ searching || identifying ? '搜索中…' : '搜索' }}
      </el-button>
    </form>

    <!-- 历史 chips -->
    <div v-if="history.length" class="hs-history">
      <el-icon :size="14" class="hs-history-ico"><Clock /></el-icon>
      <el-button
        v-for="h in history"
        :key="h"
        size="small"
        class="hs-chip"
        :disabled="searching || identifying"
        @click="quickSearch(h)"
      >{{ h }}</el-button>
    </div>

    <!-- 错误条 -->
    <div v-if="errorBanner" class="hs-error">
      <el-icon><WarningFilled /></el-icon>
      <span>{{ errorBanner }}</span>
    </div>

    <!-- TMDB 确认卡 -->
    <div v-if="showConfirm && !searching" class="hs-confirm">
      <div class="hs-confirm-head">
        <div class="hs-confirm-title">
          <p class="hs-confirm-t">先确认要搜哪一部</p>
          <p class="hs-confirm-s">点选一部，用规范片名去搜</p>
        </div>
        <el-button size="small" plain :disabled="!kw.trim()" @click="searchRaw">用原词搜</el-button>
      </div>
      <p v-if="identifying" class="hs-identifying">
        <el-icon class="hs-spin"><Loading /></el-icon>
        正在识别「{{ kw.trim() }}」…
      </p>
      <el-alert v-else-if="tmdbError" :title="tmdbError" type="error" :closable="false" />
      <el-alert v-else-if="candidates.length === 0" title="TMDB 没有匹配到影视" type="info" :closable="false" description="可直接用原词搜" />
      <div v-else class="hs-cand-grid">
        <div
          v-for="e in candidates"
          :key="`${e.media_type}-${e.tmdb_id}`"
          class="hs-cand"
          :class="{ 'hs-cand-picked': picked?.item?.tmdb_id === e.tmdb_id }"
          role="button"
          tabindex="0"
          :aria-label="`搜索 ${e.title} 的资源`"
          @click="pickCandidate(e)"
          @keydown.enter="pickCandidate(e)"
        >
          <div class="hs-cand-poster">
            <el-image v-if="e.poster_url" :src="e.poster_url" fit="cover" class="hs-cand-img" lazy>
              <template #error><div class="hs-cand-fallback"><el-icon><Film /></el-icon></div></template>
            </el-image>
            <div v-else class="hs-cand-fallback"><el-icon><Film /></el-icon></div>
          </div>
          <div class="hs-cand-info">
            <div class="hs-cand-title-row">
              <p class="hs-cand-title" :title="e.title">{{ e.title }}</p>
              <span class="hs-cand-vote"><el-icon :size="11"><Star /></el-icon>{{ Number(e.vote_average || 0).toFixed(1) }}</span>
            </div>
            <p class="hs-cand-sub">
              {{ e.original_title }}{{ e.year ? ` (${e.year})` : '' }} · {{ e.media_type === 'tvshow' ? '剧集' : '电影' }} · {{ (e.genres || []).slice(0, 2).join(' / ') }}
            </p>
            <el-select
              v-if="e.media_type === 'tvshow' && (e.seasons || []).length"
              :model-value="seasonFor(e.tmdb_id)"
              size="small"
              class="hs-cand-season"
              @click.stop
              @change="(v: number) => setSeason(e.tmdb_id, v)"
            >
              <el-option v-for="s in e.seasons" :key="s.season_number" :value="s.season_number" :label="`第 ${s.season_number} 季 · ${s.episode_count} 集`" />
            </el-select>
            <el-button size="small" type="primary" link class="hs-cand-go" @click.stop="pickCandidate(e)">
              <el-icon><Search /></el-icon>搜资源
            </el-button>
          </div>
        </div>
      </div>
    </div>

    <!-- 搜索上下文条 -->
    <div v-if="!showConfirm && currentKeyword" class="hs-context">
      <div class="hs-context-left">
        <template v-if="picked">
          <el-icon class="hs-ctx-ico hs-ctx-primary"><Search /></el-icon>
          <span>{{ pickedItemLabel }}</span>
          <span class="hs-ctx-kw">{{ currentKeyword }}</span>
        </template>
        <template v-else>
          <span class="hs-ctx-plain">按原词搜索「{{ currentKeyword }}」</span>
        </template>
      </div>
      <el-button size="small" link class="hs-ctx-btn" @click="identifyAgain">
        {{ picked ? '换一部' : '识别影视' }}
      </el-button>
    </div>

    <!-- 引擎进度 -->
    <div v-if="searching && Object.keys(engineState).length" class="hs-engines">
      <div v-for="(st, eng) in engineState" :key="eng" class="hs-engine">
        <el-icon v-if="st === 'searching'" class="hs-spin hs-engine-ico"><Loading /></el-icon>
        <el-icon v-else-if="st === 'done'" class="hs-engine-ico hs-engine-ok"><CircleCheckFilled /></el-icon>
        <el-icon v-else class="hs-engine-ico hs-engine-err"><WarningFilled /></el-icon>
        <span>{{ engineLabels[eng] || eng }}</span>
        <span v-if="st === 'done'" class="hs-engine-count">({{ engineCount(eng) }})</span>
      </div>
    </div>

    <!-- 无结果 -->
    <div v-if="resultsVisible && !searching && totalResults === 0" class="hs-none">
      未找到相关资源
    </div>

    <!-- 结果 -->
    <template v-if="totalResults > 0">
      <el-tabs v-if="tabs.length > 2" v-model="tab" size="small" class="hs-tabs">
        <el-tab-pane v-for="t in tabs" :key="t.key" :name="t.key" :label="`${t.label} (${t.count})`" />
      </el-tabs>
      <div class="hs-grid">
        <!-- Telegram 结果 -->
        <div v-for="e in shownTelegramResults" :key="`tg-${e.channel}-${e.message_id}`" class="hs-card">
          <div class="hs-tg-body">
            <div class="hs-tg-title-row">
              <p class="hs-tg-title">{{ e.title }}</p>
              <a v-if="e.url" :href="e.url" target="_blank" rel="noopener" class="hs-tg-origin" title="原文">
                <el-icon :size="14"><TopRight /></el-icon>
              </a>
            </div>
            <div v-if="tgSpecList(e).length" class="hs-specs">
              <span v-for="(s, i) in tgSpecList(e)" :key="i" class="hs-spec" :class="{ 'hs-spec-lead': i === 0 }">{{ s }}</span>
            </div>
            <div class="hs-tg-meta">
              <el-tag size="small" type="info" effect="dark" disable-transitions>{{ e.channel_title }}</el-tag>
              <span v-if="e.date" class="hs-tg-date">{{ fmtDate(e.date) }}</span>
              <span v-for="tag in (e.tags || []).slice(0, 2)" :key="tag" class="hs-tag-success">{{ tag }}</span>
            </div>
            <div class="hs-transfer-row">
              <template v-if="e.share_link">
                <el-tooltip :content="!e.source_type ? '当前版本仅支持 123 / 光鸭 / 139 分享链接转存' : '转存到云盘'" placement="top">
                  <el-button
                    size="small"
                    plain
                    class="hs-transfer-btn"
                    :disabled="transferringId === tgKey(e) || !e.source_type"
                    :loading="transferringId === tgKey(e)"
                    @click="transfer(e.share_link, e.source_type, e.access_code, tgKey(e))"
                  >{{ transferringId === tgKey(e) ? '转存中…' : '转存' }}</el-button>
                </el-tooltip>
                <el-button size="small" text class="hs-copy-btn" @click="copy(e.share_link, tgKey(e))">
                  {{ copiedId === tgKey(e) ? '已复制' : '复制链接' }}
                </el-button>
                <el-button v-if="e.access_code" size="small" text class="hs-copy-btn" @click="copy(e.access_code, tgKey(e) + '-code')">
                  {{ copiedId === tgKey(e) + '-code' ? '已复制' : '复制分享码' }}
                </el-button>
              </template>
              <template v-else-if="magnetLinks(e).length || ed2kLinks(e).length">
                <el-tooltip content="当前版本暂不支持离线下载" placement="top">
                  <el-button size="small" plain disabled class="hs-dl-btn">
                    <el-icon><Download /></el-icon>云下载
                  </el-button>
                </el-tooltip>
                <el-button size="small" text class="hs-copy-btn" @click="copy(magnetLinks(e).concat(ed2kLinks(e)).join('\n'), tgKey(e))">
                  {{ copiedId === tgKey(e) ? '已复制' : '复制链接' }}
                </el-button>
              </template>
              <a v-else-if="e.url" :href="e.url" target="_blank" rel="noopener" class="hs-view-origin">查看原文获取链接</a>
            </div>
            <div v-if="transferMsg && transferMsg.id === tgKey(e)" class="hs-ef" :class="transferMsg.ok ? 'hs-ef-ok' : 'hs-ef-err'">
              <el-icon :size="11"><CircleCheckFilled v-if="transferMsg.ok" /><WarningFilled v-else /></el-icon>
              {{ transferMsg.msg }}
            </div>
          </div>
        </div>

        <!-- 影巢结果 -->
        <div v-for="e in shownHiveResults" :key="`hdhive-${e.slug}`" class="hs-card hs-card-hive">
          <div class="hs-hive-head">
            <div class="hs-hive-avatar">
              <img v-if="e.user?.avatar_url" :src="e.user.avatar_url" referrerpolicy="no-referrer" alt="" class="hs-hive-avatar-img" />
              <span v-else class="hs-hive-avatar-fb">{{ (e.user?.nickname || '影')[0] }}</span>
            </div>
            <div class="hs-hive-info">
              <div class="hs-hive-user-row">
                <span class="hs-hive-name">{{ e.user?.nickname?.trim() || '影巢资源' }}</span>
                <el-tag size="small" type="info" effect="plain" disable-transitions class="hs-tag-hive">影巢</el-tag>
                <el-tag v-if="e.is_official" size="small" type="warning" effect="plain" disable-transitions class="hs-tag-official">官组</el-tag>
                <el-tag v-if="e.unlock_points" size="small" type="warning" effect="dark" disable-transitions>{{ e.unlock_points }} 积分</el-tag>
              </div>
              <div class="hs-hive-time">
                <span v-if="e.created_at">发布于 {{ fmtDate(e.created_at) }}</span>
                <span :class="e.validate_status === 'valid' ? 'hs-valid' : 'hs-invalid'">
                  <el-icon :size="11"><CircleCheckFilled v-if="e.validate_status === 'valid'" /><WarningFilled v-else /></el-icon>
                  {{ e.validate_status === 'valid' ? '已验证' : '已失效' }}
                </span>
              </div>
              <p class="hs-hive-title">{{ e.title }}</p>
              <p v-if="hiveSpecs(e).length" class="hs-hive-meta">{{ hiveSpecs(e).join(' · ') }}</p>
            </div>
          </div>
          <div v-if="hiveSpecs(e).length" class="hs-specs hs-hive-specs">
            <span v-for="(s, i) in hiveSpecs(e)" :key="i" class="hs-spec" :class="{ 'hs-spec-lead-warn': i === 0 }">{{ s }}</span>
          </div>
          <div class="hs-hive-actions">
            <template v-if="unlocked[e.slug]">
              <template v-if="isMagnetLike(unlocked[e.slug].full_url || unlocked[e.slug].url)">
                <el-tooltip content="当前版本暂不支持离线下载" placement="top">
                  <el-button size="small" plain disabled class="hs-dl-btn">
                    <el-icon><Download /></el-icon>云下载
                  </el-button>
                </el-tooltip>
                <el-button size="small" text class="hs-copy-btn" @click="copy(unlocked[e.slug].full_url || unlocked[e.slug].url, e.slug)">
                  {{ copiedId === e.slug ? '已复制' : '复制链接' }}
                </el-button>
              </template>
              <template v-else>
                <el-tooltip :content="!unlocked[e.slug].source_type ? '当前版本仅支持 123 / 光鸭 / 139 分享链接转存' : '转存到云盘'" placement="top">
                  <el-button
                    size="small"
                    plain
                    class="hs-transfer-btn"
                    :disabled="transferringId === e.slug || !unlocked[e.slug].source_type"
                    :loading="transferringId === e.slug"
                    @click="transfer(unlocked[e.slug].full_url || unlocked[e.slug].url, unlocked[e.slug].source_type, unlocked[e.slug].access_code, e.slug)"
                  >{{ transferringId === e.slug ? '转存中…' : '转存' }}</el-button>
                </el-tooltip>
                <el-button size="small" text class="hs-copy-btn" @click="copy(unlocked[e.slug].full_url || unlocked[e.slug].url, e.slug)">
                  {{ copiedId === e.slug ? '已复制' : '复制链接' }}
                </el-button>
                <el-button v-if="unlocked[e.slug].access_code" size="small" text class="hs-copy-btn" @click="copy(unlocked[e.slug].access_code, e.slug + '-code')">
                  {{ copiedId === e.slug + '-code' ? '已复制' : '复制分享码' }}
                </el-button>
              </template>
            </template>
            <el-button
              v-else
              size="small"
              type="primary"
              class="hs-unlock-btn"
              :disabled="!!unlockingSlug"
              :loading="unlockingSlug === e.slug"
              @click="unlock(e.slug)"
            >
              {{ e.is_unlocked ? '获取链接' : e.unlock_points ? `${e.unlock_points} 积分解锁` : '免费获取' }}
            </el-button>
          </div>
          <div v-if="transferMsg && transferMsg.id === e.slug" class="hs-ef" :class="transferMsg.ok ? 'hs-ef-ok' : 'hs-ef-err'">
            <el-icon :size="11"><CircleCheckFilled v-if="transferMsg.ok" /><WarningFilled v-else /></el-icon>
            {{ transferMsg.msg }}
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  CircleCheckFilled, Clock, Download, Film, Loading, Search, Star, TopRight, WarningFilled,
} from '@element-plus/icons-vue'
import { useHttpClient } from '@/http/client'

const http = useHttpClient()
const route = useRoute()

const HIST_KEY = 'hdhive_search_history'
const MAX_HISTORY = 20

// ---------------------------------------------------------------------------
// 基础状态（对齐 mediavault CF：a/s/u/f/m/g/v/b/w/E/O/A/M/P/I/R/B/H/ee/te/le）
// ---------------------------------------------------------------------------
const kw = ref(typeof route.query.keyword === 'string' ? route.query.keyword : '')
const history = ref<string[]>(safeHistory())
const copiedId = ref('')
const transferringId = ref('')
const transferMsg = ref<{ id: string; ok: boolean; msg: string } | null>(null)
const tab = ref('all')
const unlockingSlug = ref('')
const unlocked = ref<Record<string, any>>({})
const identifying = ref(false)
const tmdbError = ref('')
const candidates = ref<any[]>([])
const showConfirm = ref(false)
const picked = ref<{ item: any; season: number } | null>(null)
const seasonPicks = ref<Record<number, number>>({})
const searching = ref(false)
const currentKeyword = ref('')
const resultsVisible = ref(false)
const errorBanner = ref('')
const engineState = ref<Record<string, string>>({})
const engineLabels = ref<Record<string, string>>({})
const tgResults = ref<any[]>([])
const hiveResults = ref<any[]>([])
const enabled = ref<Record<string, boolean>>({})

const targetDir = ref('/媒体库/待整理')

function safeHistory(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(HIST_KEY) || '[]')
    return Array.isArray(v) ? v.filter((x) => typeof x === 'string').slice(0, MAX_HISTORY) : []
  } catch {
    return []
  }
}
const pushHistory = (t: string) => {
  const next = [t, ...history.value.filter((x) => x !== t)].slice(0, MAX_HISTORY)
  history.value = next
  try {
    localStorage.setItem(HIST_KEY, JSON.stringify(next))
  } catch {
    /* ignore */
  }
}

const seasonFor = (tmdbId: number) => {
  const saved = seasonPicks.value[tmdbId]
  if (saved) return saved
  const c = candidates.value.find((x) => x.tmdb_id === tmdbId)
  const seasons = c?.seasons || []
  const def = seasons.some((s: any) => s.season_number === 1) ? 1 : seasons[0]?.season_number ?? 1
  seasonPicks.value[tmdbId] = def
  return def
}
const setSeason = (tmdbId: number, v: number) => {
  seasonPicks.value[tmdbId] = v
}

const pickedItemLabel = computed(() => {
  const p = picked.value
  if (!p) return ''
  return `${p.item.title}${p.item.year ? ` (${p.item.year})` : ''}${p.item.media_type === 'tvshow' ? ` · 第 ${p.season} 季` : ''}`
})

// ---------------------------------------------------------------------------
// TMDB 识别
// ---------------------------------------------------------------------------
const submitSearch = () => {
  const t = kw.value.trim()
  if (!t) return
  pushHistory(t)
  identify(t)
}

const quickSearch = (h: string) => {
  kw.value = h
  identify(h)
}

const identify = async (t: string) => {
  identifying.value = true
  showConfirm.value = true
  tmdbError.value = ''
  candidates.value = []
  try {
    const [movieResp, tvResp] = await Promise.all([
      http.get('/api/scrape/tmdb-search', { params: { name: t, type: 'movie' } }),
      http.get('/api/scrape/tmdb-search', { params: { name: t, type: 'tvshow' } }),
    ])
    const merge = (resp: any, type: 'movie' | 'tvshow') =>
      Array.isArray(resp.data?.data) ? resp.data.data.map((d: any) => ({ ...d, media_type: type })) : []
    const items = [...merge(movieResp, 'movie'), ...merge(tvResp, 'tvshow')]
      .sort((a, b) => (b.vote_average || 0) - (a.vote_average || 0))
      .slice(0, 12)
    candidates.value = items
    if (!items.length) {
      tmdbError.value = 'TMDB 没有匹配到影视'
    }
  } catch (e: any) {
    tmdbError.value = 'TMDB 识别失败：' + (e?.message || '')
    candidates.value = []
  } finally {
    identifying.value = false
  }
}

// 「用原词搜」：不选影片，直接按输入词搜索
const searchRaw = () => {
  const t = kw.value.trim()
  if (!t) return
  picked.value = null
  showConfirm.value = false
  runSearch(t, {})
}

// 「换一部 / 识别影视」
const identifyAgain = () => {
  if (candidates.value.length) {
    showConfirm.value = true
  } else {
    identify(currentKeyword.value || kw.value.trim() || selectedCandidateKeyword())
  }
}
const selectedCandidateKeyword = () => picked.value?.item?.title || kw.value.trim() || ''

const pickCandidate = (e: any) => {
  const season = e.media_type === 'tvshow' ? seasonFor(e.tmdb_id) : 0
  picked.value = { item: e, season }
  showConfirm.value = false
  runSearch(buildKeywords(e), {
    tmdb_id: e.tmdb_id,
    media_type: e.media_type === 'tvshow' ? 'tv' : 'movie',
    season,
  })
}

// 构造搜索关键词（对齐 mediavault aF：tv 多季 → 「第 N 季」中文数字）
const cnNums = ['一', '二', '三', '四', '五', '六', '七', '八', '九', '十', '十一', '十二', '十三', '十四', '十五', '十六', '十七', '十八', '十九', '二十', '二十一', '二十二', '二十三', '二十四', '二十五', '二十六', '二十七', '二十八', '二十九', '三十']
const buildKeywords = (e: any) => {
  const title = e.title || ''
  const season = e.media_type === 'tvshow' ? seasonFor(e.tmdb_id) : 0
  if (e.media_type === 'tvshow' && season > 1) {
    return `${title} 第${cnNums[season - 1] || season}季`
  }
  return title
}

// ---------------------------------------------------------------------------
// SSE 流式搜索（对齐 mediavault be：init/progress/result/done 帧）
// ---------------------------------------------------------------------------
const runSearch = async (keyword: string, extra: Record<string, any>) => {
  searching.value = true
  resultsVisible.value = false
  errorBanner.value = ''
  currentKeyword.value = keyword
  engineState.value = {}
  tgResults.value = []
  hiveResults.value = []
  transferMsg.value = null
  try {
    const resp = await fetch('/api/cloud/hive/search/stream', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ keyword, ...extra }),
    })
    const ct = resp.headers.get('content-type') || ''
    if (!resp.ok || (ct && !ct.includes('text/event-stream'))) {
      let msg = ''
      try {
        const j = await resp.json()
        msg = j?.message || ''
      } catch {
        /* ignore */
      }
      errorBanner.value = msg || `搜索请求失败（${resp.status}）`
      return
    }
    const reader = resp.body?.getReader()
    if (!reader) {
      errorBanner.value = '搜索请求失败'
      return
    }
    const decoder = new TextDecoder()
    let buf = ''
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buf += decoder.decode(value, { stream: true })
      const frames = buf.split('\n\n')
      buf = frames.pop() || ''
      for (const raw of frames) {
        const line = raw.trim()
        if (!line.startsWith('data:')) continue
        try {
          handleFrame(JSON.parse(line.slice(5).trim()))
        } catch {
          /* 跳过坏帧 */
        }
      }
    }
    if (buf.trim().startsWith('data:')) {
      try {
        handleFrame(JSON.parse(buf.trim().slice(5).trim()))
      } catch {
        /* ignore */
      }
    }
  } catch (e: any) {
    errorBanner.value = '搜索请求失败：' + (e?.message || '')
  } finally {
    searching.value = false
    resultsVisible.value = true
  }
}

const handleFrame = (ev: any) => {
  if (!ev || typeof ev.type !== 'string') return
  if (ev.type === 'init') {
    engineLabels.value = ev.data?.labels || {}
    for (const e of ev.data?.engines || []) {
      engineState.value[e] = 'searching'
    }
  } else if (ev.type === 'progress') {
    engineState.value[ev.engine] = ev.status || 'done'
    if (ev.message) {
      errorBanner.value = `${engineLabels.value[ev.engine] || ev.engine}：${ev.message}`
    }
  } else if (ev.type === 'result') {
    const list = ev.data || []
    if (ev.engine === 'telegram') {
      tgResults.value = tgResults.value.concat(
        list
          .filter((x: any) => !x.error)
          .map((x: any) => {
            const first = (x.links || [])[0] || {}
            return {
              ...x,
              access_code: first.pwd || '',
              source_type: detectSourceByUrl(x.share_link || ''),
            }
          }),
      )
    } else if (ev.engine === 'hdhive') {
      const items = list
        .filter((x: any) => !!x.pan_type)
        .map((x: any) => ({ ...x, source_type: detectSource(x.pan_type || '') }))
      hiveResults.value = hiveResults.value.concat(items)
      for (const x of list) {
        if (x.is_unlocked && x.slug) {
          unlocked.value[x.slug] = {
            full_url: x.url || x.full_url || '',
            access_code: '',
            source_type: detectSource(x.pan_type || ''),
          }
        }
      }
    }
  } else if (ev.type === 'done') {
    enabled.value = ev.data?.enabled || {}
  }
}

// 源类型检测：仅 123 / 光鸭 / 139 支持服务端转存
const detectSource = (s: string) => {
  const v = (s || '').toLowerCase()
  if (v === '123' || v === '123pan') return '123'
  if (v === 'guangyapan' || v === '光鸭') return 'guangyapan'
  if (v === 'pan139' || v === '139') return 'pan139'
  return ''
}
const detectSourceByUrl = (u: string) => {
  if (/guangyapan\.com/.test(u)) return 'guangyapan'
  if (/123pan\.com/.test(u) || /123684|123865/.test(u)) return '123'
  if (/139\.com/.test(u)) return 'pan139'
  return ''
}
const isMagnetLike = (u: string) => /^magnet:/.test(u) || /^ed2k:/.test(u)

// ---------------------------------------------------------------------------
// 过滤与计数
// ---------------------------------------------------------------------------
const tabs = computed(() => {
  const t = [{ key: 'all', label: '全部', count: totalResults.value }]
  if (enabled.value.telegram) t.push({ key: 'telegram', label: 'Telegram', count: tgResults.value.length })
  if (enabled.value.hdhive) t.push({ key: 'hdhive', label: '影巢', count: hiveResults.value.length })
  return t
})
const totalResults = computed(() => tgResults.value.length + hiveResults.value.length)
const engineCount = (eng: string) => (eng === 'telegram' ? tgResults.value.length : hiveResults.value.length)
const shownTelegramResults = computed(() => (tab.value === 'all' || tab.value === 'telegram' ? tgResults.value : []))
const shownHiveResults = computed(() => (tab.value === 'all' || tab.value === 'hdhive' ? hiveResults.value : []))

// ---------------------------------------------------------------------------
// 卡片渲染辅助
// ---------------------------------------------------------------------------
const parseInfo = (text: string) => {
  const info: Record<string, string> = {}
  const res = text.match(/(4K|2160P?|1080P?|720P?|2K|1440P?)/i)?.[0]?.replace(/^(\d+)$/, '$1P').toUpperCase()
  if (res) info.resolution = res.replace(/P$/i, 'P').replace(/^4K$/i, '4K')
  const se = text.match(/S\d{1,2}E?(\d{1,2})?/i)?.[0]
  if (se) info.season_episode = se.toUpperCase()
  for (const c of ['H.264', 'H.265', 'HEVC', 'AVC', 'AV1', 'x264', 'x265']) {
    if (new RegExp(c, 'i').test(text)) {
      info.codec = c
      break
    }
  }
  for (const h of ['HDR10', 'HDR', 'Dolby Vision', '杜比视界']) {
    if (text.includes(h)) {
      info.hdr = h
      break
    }
  }
  for (const s of ['中字', '国配', '双语', '特效', '内封']) {
    if (text.includes(s)) {
      info.subtitle = s
      break
    }
  }
  return info
}
const tgInfo = (e: any) => {
  if (!e._info) e._info = parseInfo(e.title || '')
  return e._info
}
const tgSpecList = (e: any) => {
  const i = tgInfo(e)
  const order = ['source', 'channel', 'resolution', 'season_episode', 'subtitle', 'group', 'codec', 'hdr']
  return order.map((k) => i[k]).filter(Boolean)
}
const tgKey = (e: any) => `${e.channel}-${e.message_id}`
const magnetLinks = (e: any) =>
  (e.links || [])
    .filter((l: any) => (l.type || '').toLowerCase() === 'magnet' || (l.url || '').toLowerCase().startsWith('magnet:'))
    .map((l: any) => l.url)
const ed2kLinks = (e: any) =>
  (e.links || [])
    .filter((l: any) => (l.type || '').toLowerCase() === 'ed2k' || (l.url || '').toLowerCase().startsWith('ed2k:'))
    .map((l: any) => l.url)
const hiveSpecs = (e: any) => {
  const parts: string[] = []
  if (e.source) parts.push(e.source)
  if (e.video_resolution) parts.push(e.video_resolution)
  if (e.size) parts.push(e.size)
  if (e.subtitle_language) parts.push(e.subtitle_language)
  if (e.remark) parts.push(e.remark)
  return parts.filter(Boolean)
}
const fmtDate = (t: string) => {
  const d = new Date(t)
  if (isNaN(d.getTime())) return t || ''
  return `${d.getFullYear()}/${String(d.getMonth() + 1).padStart(2, '0')}/${String(d.getDate()).padStart(2, '0')}`
}

// ---------------------------------------------------------------------------
// 复制 / 解锁 / 转存
// ---------------------------------------------------------------------------
const copy = async (text: string, id: string) => {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    copiedId.value = id
    setTimeout(() => {
      if (copiedId.value === id) copiedId.value = ''
    }, 2000)
  } catch {
    ElMessage.error('复制失败')
  }
}

const unlock = async (slug: string) => {
  unlockingSlug.value = slug
  try {
    const resp = await http.post('/api/cloud/hive/unlock', { slug })
    if (resp.data?.code === 200) {
      const d = resp.data.data || {}
      const url = d.full_url || d.url || ''
      unlocked.value[slug] = {
        ...d,
        full_url: url,
        source_type: d.pan_type ? detectSource(d.pan_type) : detectSourceByUrl(url),
      }
    } else {
      ElMessage.error(resp.data?.message || '解锁失败')
    }
  } catch (e: any) {
    ElMessage.error('解锁失败：' + (e?.message || ''))
  } finally {
    unlockingSlug.value = ''
  }
}

const transfer = async (url: string, sourceType: string, accessCode: string | undefined, id: string) => {
  transferringId.value = id
  transferMsg.value = null
  try {
    const resp = await http.post('/api/cloud/hive/transfer', {
      url,
      access_code: accessCode || '',
      source_type: sourceType || '',
      target_dir: targetDir.value,
    })
    if (resp.data?.code === 200) {
      transferMsg.value = { id, ok: true, msg: resp.data.message }
    } else {
      transferMsg.value = { id, ok: false, msg: resp.data?.message || '转存失败' }
    }
  } catch (e: any) {
    transferMsg.value = { id, ok: false, msg: '转存异常：' + (e?.message || '') }
  } finally {
    transferringId.value = ''
  }
}

// ---------------------------------------------------------------------------
onMounted(async () => {
  // 默认目标目录：跟随「默认配置」电影/剧集项
  try {
    const resp = await http.get('/api/cloud/hive/settings')
    if (resp.data?.code === 200) {
      const d = resp.data.data || {}
      for (const key of ['subscription_defaults_movie', 'subscription_defaults_tv']) {
        try {
          const obj = JSON.parse(d[key] || '{}')
          if (obj && typeof obj === 'object' && obj.target_dir) {
            targetDir.value = obj.target_dir
            break
          }
        } catch {
          /* ignore */
        }
      }
    }
  } catch {
    /* ignore */
  }
  // 带 keyword 参数进入（详情页「搜索资源」）→ 直接识别
  if (kw.value.trim()) {
    pushHistory(kw.value.trim())
    identify(kw.value.trim())
  }
})
</script>

<style scoped>
.hs-page {
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.hs-form {
  display: flex;
  gap: 8px;
  align-items: stretch;
}
.hs-input {
  flex: 1;
}
.hs-history {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.hs-history-ico {
  color: var(--text-muted);
  flex-shrink: 0;
}
.hs-chip {
  max-width: 160px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hs-error {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent);
  background: color-mix(in srgb, var(--danger) 10%, transparent);
  color: var(--danger);
  border-radius: 8px;
  padding: 8px 12px;
  font-size: 13px;
  line-height: 1.5;
}
.hs-confirm {
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 12px;
}
.hs-confirm-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.hs-confirm-t {
  font-size: 14px;
  font-weight: 500;
}
.hs-confirm-s {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 2px;
}
.hs-identifying {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-muted);
  margin-top: 10px;
}
.hs-spin {
  animation: hs-rot 1s linear infinite;
}
@keyframes hs-rot {
  to {
    transform: rotate(360deg);
  }
}
.hs-cand-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 8px;
  margin-top: 10px;
}
@media (min-width: 640px) {
  .hs-cand-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (min-width: 1280px) {
  .hs-cand-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
.hs-cand {
  display: flex;
  align-items: center;
  gap: 10px;
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 8px;
  cursor: pointer;
  transition: border-color 0.15s;
}
.hs-cand:hover {
  border-color: color-mix(in srgb, var(--brand) 60%, transparent);
}
.hs-cand-picked {
  border-color: var(--brand);
}
.hs-cand-poster {
  width: 48px;
  height: 72px;
  border-radius: 6px;
  overflow: hidden;
  flex-shrink: 0;
  background: var(--surface-muted);
}
.hs-cand-img {
  width: 100%;
  height: 100%;
}
.hs-cand-fallback {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  color: var(--text-muted);
}
.hs-cand-info {
  flex: 1;
  min-width: 0;
}
.hs-cand-title-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.hs-cand-title {
  flex: 1;
  min-width: 0;
  font-size: 14px;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hs-cand-vote {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  font-size: 12px;
  color: var(--warning);
  flex-shrink: 0;
}
.hs-cand-sub {
  font-size: 11px;
  color: var(--text-muted);
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hs-cand-season {
  width: 100%;
  margin-top: 6px;
}
.hs-cand-go {
  margin-top: 4px;
}
.hs-context {
  display: flex;
  align-items: center;
  gap: 8px;
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 8px 12px;
  font-size: 12px;
}
.hs-context-left {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.hs-ctx-ico {
  flex-shrink: 0;
}
.hs-ctx-primary {
  color: var(--brand);
}
.hs-ctx-kw {
  color: var(--text-muted);
  font-family: var(--font-mono, monospace);
  display: none;
}
@media (min-width: 640px) {
  .hs-ctx-kw {
    display: inline;
  }
}
.hs-ctx-plain {
  color: var(--text-muted);
}
.hs-ctx-btn {
  margin-left: auto;
  flex-shrink: 0;
}
.hs-engines {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 16px;
}
.hs-engine {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
}
.hs-engine-ico {
  flex-shrink: 0;
}
.hs-engine-ok {
  color: var(--success);
}
.hs-engine-err {
  color: var(--danger);
}
.hs-engine-count {
  color: var(--text-muted);
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
.hs-none {
  padding: 40px 0;
  text-align: center;
  font-size: 13px;
  color: var(--text-muted);
}
.hs-tabs {
  margin-bottom: 4px;
}
.hs-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 12px;
}
@media (min-width: 640px) {
  .hs-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (min-width: 1024px) {
  .hs-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
@media (min-width: 1280px) {
  .hs-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}
.hs-card {
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 12px;
  transition: border-color 0.15s;
}
.hs-card:hover {
  border-color: var(--border-strong);
}
.hs-card-hive:hover {
  border-color: color-mix(in srgb, var(--warning) 50%, transparent);
}
.hs-tg-body {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.hs-tg-title-row {
  display: flex;
  align-items: flex-start;
  gap: 6px;
}
.hs-tg-title {
  flex: 1;
  min-width: 0;
  font-size: 14px;
  font-weight: 500;
  line-height: 1.45;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.hs-tg-origin {
  color: var(--text-muted);
  flex-shrink: 0;
  padding: 2px;
}
.hs-tg-origin:hover {
  color: var(--brand);
}
.hs-specs {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.hs-spec {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--surface-muted);
  color: var(--text-regular);
}
.hs-spec-lead {
  background: color-mix(in srgb, var(--brand) 12%, transparent);
  color: var(--brand);
}
.hs-spec-lead-warn {
  background: color-mix(in srgb, var(--warning) 12%, transparent);
  color: var(--warning);
}
.hs-tg-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  font-size: 10px;
}
.hs-tg-date {
  color: var(--text-muted);
}
.hs-tag-success {
  color: var(--success);
}
.hs-transfer-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.hs-transfer-btn {
  color: var(--brand);
  border-color: color-mix(in srgb, var(--brand) 50%, transparent);
}
.hs-transfer-btn:hover:not(:disabled) {
  color: #fff;
  background: var(--brand);
  border-color: var(--brand);
}
.hs-copy-btn {
  color: var(--text-muted);
}
.hs-copy-btn:hover {
  color: var(--brand);
}
.hs-dl-btn {
  color: var(--text-muted);
}
.hs-view-origin {
  font-size: 12px;
  color: var(--info);
  text-decoration: none;
}
.hs-view-origin:hover {
  text-decoration: underline;
}
.hs-hive-head {
  display: flex;
  gap: 10px;
}
.hs-hive-avatar {
  width: 36px;
  height: 36px;
  border-radius: 10px;
  overflow: hidden;
  flex-shrink: 0;
  background: var(--surface-muted);
}
.hs-hive-avatar-img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.hs-hive-avatar-fb {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  font-size: 14px;
  color: var(--text-muted);
}
.hs-hive-info {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.hs-hive-user-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.hs-hive-name {
  font-size: 13px;
  font-weight: 600;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hs-tag-hive {
  color: var(--info);
}
.hs-tag-official {
  color: var(--brand);
}
.hs-hive-time {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  font-size: 11px;
  color: var(--text-muted);
}
.hs-valid {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  color: var(--success);
}
.hs-invalid {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  color: var(--danger);
}
.hs-hive-title {
  font-size: 15px;
  font-weight: 600;
  line-height: 1.4;
  letter-spacing: -0.01em;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.hs-hive-meta {
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.5;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.hs-hive-specs {
  margin-top: 10px;
}
.hs-hive-actions {
  margin-top: 10px;
}
.hs-unlock-btn {
  width: 100%;
}
.hs-ef {
  margin-top: 6px;
  font-size: 10.5px;
  display: flex;
  align-items: center;
  gap: 4px;
  line-height: 1.4;
}
.hs-ef-ok {
  color: var(--success);
}
.hs-ef-err {
  color: var(--danger);
}
</style>