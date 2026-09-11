<script setup lang="ts">
import { ref, computed, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import { CircleCheck } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'

// 影视发现：复刻参考实现 media_discovery 分区式布局
// 四个互斥分区：影视探索（含番剧/收藏子入口）/ 榜单推荐 / 追剧日历 / 基础配置

interface DiscoverItem {
  source: string
  media_type: string
  entity_key?: string
  tmdb_id?: number
  douban_id?: string
  external_id?: string
  title: string
  original_title?: string
  poster: string
  overview?: string
  vote_avg: number
  release_date?: string
  year?: number
  rank?: number
  providers?: string[]
  genres?: string[]
  air_date?: string
  episode_title?: string
  season_number?: number
  episode_number?: number
  in_emby?: boolean
}

interface PageResult {
  items: DiscoverItem[]
  page: number
  total_pages: number
}

interface CalendarDay {
  date: string
  label: string
  items: DiscoverItem[]
}

// 关联资源（对齐 tgto123 media_discovery 资源卡片字段）
interface ResourceEpisode {
  season_num?: number | null
  episode_num?: number | null
  end_episode_num?: number | null
  total_episode_num?: number | null
  is_complete?: boolean
  is_updated?: boolean
}

interface ResourceItem {
  item_key: string
  source: string
  provider: string
  provider_label: string
  title: string
  slug?: string
  share_url?: string
  link_type?: string
  size?: string
  episode?: ResourceEpisode | null
  is_unlocked?: boolean
  points_known?: boolean
  unlock_points?: number
  unlocked_users_count?: number
  remark?: string
  validate_message?: string
  is_official?: boolean
  sharer?: string
  resource_spec_tags?: string[]
  subtitle_languages?: string[]
  supported_targets?: string[]
  target_provider?: string
}

interface ResourceSearchResult {
  items: ResourceItem[]
  errors: { source: string; code: string; error: string }[]
}

interface GuanyingSessionStatus {
  enabled?: boolean
  configured?: boolean
  session_saved?: boolean
  credentials_saved?: boolean
  account_hint?: string
  last_error?: string
}

// 点选式验证码挑战（观影登录）
interface GuanyingCaptcha {
  attempt_id: string
  text?: string
  image?: string
  type?: string
  width?: number
  height?: number
}

interface DiscoveryFavorite {
  id: number
  entity_key: string
  source: string
  media_type: string
  external_id: string
  tmdb_id: number
  title: string
  original_title?: string
  poster: string
  overview?: string
  vote_avg: number
  year: number
  created_at: string
}

const http = useHttpClient()

// ------------------------- 分区导航 -------------------------
const sections = [
  { key: 'library', title: '影视探索', icon: '🎞️' } as const,
  { key: 'rankings', title: '榜单推荐', icon: '🏆' } as const,
  { key: 'calendar', title: '追剧日历', icon: '🗓️' } as const,
  { key: 'tasks', title: '基础配置', icon: '⚙️' } as const,
]
type SectionKey = (typeof sections)[number]['key']
const activeSection = ref<SectionKey>('library')

// ------------------------- 元数据（筛选器选项） -------------------------
const meta = ref<{
  genres_movie: Record<string, string>
  genres_tv: Record<string, string>
  providers: { key: string; label: string }[]
  regions: { key: string; label: string }[]
  collections: { key: string; label: string }[]
  douban_tags: Record<string, string[]>
  default_source: string
  douban_category: Record<string, string[]>
  douban_sort: { key: string; label: string }[]
  anime_genres: string[]
  anime_regions: { key: string; label: string }[]
  anime_sort: { key: string; label: string }[]
  maoyan_category: { key: string; label: string }[]
}>({
  genres_movie: {},
  genres_tv: {},
  providers: [],
  regions: [],
  collections: [],
  douban_tags: {},
  default_source: 'tmdb',
  douban_category: {},
  douban_sort: [],
  anime_genres: [],
  anime_regions: [],
  anime_sort: [],
  maoyan_category: [],
})

const currentYear = new Date().getFullYear()
const yearOptions = ['', ...Array.from({ length: currentYear - 2006 }, (_, i) => String(currentYear - i))]

const loading = ref(false)
const errorMessage = ref('')

// ------------------------- 影视探索 -------------------------
const librarySources = [
  { key: 'tmdb', label: 'TMDB 片库' },
  { key: 'douban', label: '豆瓣' },
  { key: 'anilist', label: 'AniList 动漫' },
  { key: 'bangumi', label: 'Bangumi 动漫' },
  { key: 'actors', label: '热门演员' },
  { key: 'favorites', label: '收藏' },
] as const
type LibrarySource = (typeof librarySources)[number]['key']
const librarySource = ref<LibrarySource>('tmdb')

const exploreMediaType = ref<'movie' | 'tv'>('movie')
const exploreGenre = ref('')
const exploreYear = ref('')
const exploreRegion = ref('')
const exploreSort = ref('popular')
const exploreDoubanTag = ref('热门')
const exploreDoubanSort = ref('T')
const exploreAnimeGenre = ref('')
const exploreAnimeRegion = ref('')
const exploreAnimeYear = ref('')
const exploreAnimeSort = ref('popular')
const items = ref<DiscoverItem[]>([])

// 目录源状态（豆瓣/AniList/Bangumi：后台预抓 + TMDB 匹配）
interface CatalogMeta {
  catalog_status?: string
  page_state?: string
  pending_count?: number
  cached_item_count?: number
  is_stale?: boolean
  catalog_total?: number
  matched_count?: number
  source_exhausted?: boolean
}
const catalogMeta = ref<CatalogMeta>({})
const isCatalogSource = computed(() => ['douban', 'anilist', 'bangumi'].includes(librarySource.value))
const catalogPendingWhole = computed(
  () =>
    isCatalogSource.value &&
    !items.value.length &&
    ['pending', 'partial'].includes(catalogMeta.value.page_state || '')
)
const catalogCacheNotice = computed(() => {
  if (!isCatalogSource.value || !catalogMeta.value.cached_item_count) return ''
  const cached = catalogMeta.value.cached_item_count
  const pending = catalogMeta.value.pending_count || 0
  if (catalogMeta.value.is_stale) return `正在后台更新目录，当前先展示本地缓存（${cached} 条）`
  if (pending > 0) return `已展示本地缓存 ${cached} 条，正在后台补全 ${pending} 条 TMDB 资料`
  const unmatched = (catalogMeta.value.cached_item_count || 0) - (catalogMeta.value.matched_count || 0)
  if (unmatched > 0) return `正在后台为 ${unmatched} 条条目匹配 TMDB 资料`
  return ''
})

// 演员档案（全页视图）
interface ActorProfile {
  tmdb_id?: number
  title?: string
  original_title?: string
  poster?: string
  overview?: string
  birthday?: string
  place_of_birth?: string
  known_for_department?: string
}
const actorProfileVisible = ref(false)
const actorProfileData = ref<ActorProfile>({})
const actorWorks = ref<DiscoverItem[]>([])
const actorWorksLoading = ref(false)

const sortOptions = [
  { value: 'popular', label: '热度' },
  { value: 'latest', label: '最新' },
  { value: 'rating', label: '高分' },
]

const regionOptions = [
  { value: '', label: '全部' },
  { value: 'zh-CN', label: '大陆' },
  { value: 'zh-HK', label: '香港' },
  { value: 'zh-TW', label: '台湾' },
  { value: 'ja-JP', label: '日本' },
  { value: 'ko-KR', label: '韩国' },
  { value: 'en-US', label: '欧美' },
]

const currentGenres = () =>
  exploreMediaType.value === 'tv' ? meta.value.genres_tv : meta.value.genres_movie

const genreList = computed(() => Object.values(currentGenres()))

// 片库卡片网格：列数 = floor((宽+14)/(144+14))，clamp [2,12]；≤600px 用 CSS 固定 3 列
const gridEl = ref<HTMLElement | null>(null)
const libraryColumns = ref(6)
let lastColumns = 6
const gridStyle = computed(() => ({ '--md-library-columns': libraryColumns.value }))

const computeColumns = () => {
  const w = gridEl.value?.clientWidth || document.querySelector('.md-page')?.clientWidth || 1200
  const cols = Math.max(2, Math.min(12, Math.floor((w + 14) / (144 + 14))))
  libraryColumns.value = cols
  if (cols !== lastColumns && !loading.value && items.value.length) {
    lastColumns = cols
    page.value = 1
    load()
  } else {
    lastColumns = cols
  }
}

// 分页 / 移动端无限滚动
const page = ref(1)
const totalPages = ref(1)
const infiniteMode = ref(false)
const sentinelEl = ref<HTMLElement | null>(null)
let io: IntersectionObserver | null = null

const setupInfinite = () => {
  io?.disconnect()
  io = null
  if (!sentinelEl.value) return
  io = new IntersectionObserver(
    (entries) => {
      if (entries[0]?.isIntersecting && !loading.value && page.value < totalPages.value && page.value > 0) {
        page.value += 1
        load(false, true)
      }
    },
    { rootMargin: '0px 0px 180px 0px', threshold: 0 }
  )
  io.observe(sentinelEl.value)
}

// 搜索与订阅
const searchKeyword = ref('')
const searchMode = ref(false)
const searching = ref(false)
const subscribingIds = ref<Record<number, boolean>>({})
const subscribedMap = ref<Record<string, boolean>>({})
const favBusyKeys = ref<Record<string, boolean>>({})

const isSubscribed = (item: DiscoverItem) => {
  if (!item.tmdb_id) return false
  return !!subscribedMap.value[`${item.media_type || 'movie'}:${item.tmdb_id}`]
}

const isFav = (item: DiscoverItem) => !!item.entity_key && !!favKeySet.value[item.entity_key]

// ------------------------- 榜单推荐 -------------------------
const rankingProvider = ref('hdhive')
const rankingRegion = ref('US')
const rankingMediaType = ref<'' | 'movie' | 'tv'>('')
const rankingGroups = ref<{ kind: string; items: DiscoverItem[] }[]>([])
const rankingTotal = computed(() => rankingGroups.value.reduce((n, g) => n + g.items.length, 0))
const rankingProviderLabel = computed(() => {
  for (const p of meta.value.providers) if (rankingProvider.value === 'hdhive:' + p.key) return p.label
  if (rankingProvider.value === 'hdhive') return '流媒体榜'
  if (rankingProvider.value.startsWith('tmdb:')) return tmdbRankingOptions.find((o) => o.value === rankingProvider.value)?.label || 'TMDB'
  if (rankingProvider.value.startsWith('douban:')) return meta.value.collections.find((c) => rankingProvider.value === 'douban:' + c.key)?.label || '豆瓣'
  return '榜单'
})
const rankingProviderMark = computed(() => {
  const label = rankingProviderLabel.value
  const m = label.match(/[A-Za-z0-9]/)
  return m ? m[0].toUpperCase() : label.slice(0, 1)
})

const tmdbRankingOptions = [
  { value: 'tmdb:popular', label: '热门' },
  { value: 'tmdb:top_rated', label: '高分' },
  { value: 'tmdb:now_playing', label: '正在上映' },
  { value: 'tmdb:upcoming', label: '即将上映' },
  { value: 'tmdb:on_the_air', label: '正在播出' },
  { value: 'tmdb:airing_today', label: '今日播出' },
  { value: 'tmdb:trending_day', label: '今日趋势' },
  { value: 'tmdb:trending_week', label: '本周趋势' },
]

const rankingTypeTabs = [
  { value: '', label: '全部榜单' },
  { value: 'movie', label: '电影 Top 10' },
  { value: 'tv', label: '剧集 Top 10' },
]
const rankingKindLabel = (mt: string) => (mt === 'tv' ? '剧集' : mt === 'movie' ? '电影' : '榜单')

// ------------------------- 追剧日历 -------------------------
const calendarDaysList = ref<CalendarDay[]>([])
const calendarDays = ref(30)
const calendarKind = ref('all')
const selectedDayIndex = ref(0)
const selectedDay = computed(() => calendarDaysList.value[selectedDayIndex.value] || null)
const spotlightItem = computed(() => selectedDay.value?.items?.[0] || null)

const calendarKindOptions = [
  { value: 'all', label: '全部' },
  { value: 'tv', label: '剧集' },
  { value: 'movie', label: '电影' },
  { value: 'upcoming', label: '即将播出' },
  { value: 'on-air', label: '播出中' },
  { value: 'airing-today', label: '今日播出' },
]

const calendarDayPresets = [7, 14, 30]

// ------------------------- 收藏 -------------------------
const favoriteItems = ref<DiscoveryFavorite[]>([])
const favKeySet = ref<Record<string, boolean>>({})

// ------------------------- 基础配置（四组对齐 tgto123 + 原有偏好） -------------------------
const settingsForm = ref<Record<string, any>>({
  default_explore_source: 'tmdb',
  default_explore_sort: 'popular',
  calendar_days: 30,
  calendar_kind: 'all',
  ranking_region: 'US',
  ranking_provider: 'netflix',
  ranking_media_type: 'movie',
  match_douban_tmdb: true,
  emby_check_enabled: true,
  cache_ttl_minutes: 30,
  guanying_enabled: false,
  tg_resource_channels: { '123': [], guangya: [], pan139: [] },
  media_transfer_targets: {
    '123': { folder_path: '', folder_name: '' },
    guangya: { folder_path: '', folder_name: '' },
    pan139: { folder_path: '', folder_name: '' },
  },
  media_emby: { enabled: false, server_url: '', api_key: '' },
  target_provider: '123',
  check_interval_minutes: 360,
  emby_missing_auto_scan: false,
  emby_missing_scan_interval_minutes: 720,
  emby_missing_auto_create_subscriptions: false,
})
const savingSettings = ref(false)
const dirtyGroups = ref<Record<string, boolean>>({})
const settingsBaseline = ref('')
const embyTestBusy = ref(false)
const embyTestMessage = ref('')

const transferProviderNames: Record<string, string> = { '123': '123', guangya: '光鸭', pan139: '139' }

const channelText = (provider: string) =>
  ((settingsForm.value.tg_resource_channels || {})[provider] || []).join('\n')
const setChannelText = (provider: string, value: string) => {
  settingsForm.value.tg_resource_channels[provider] = value
    .split('\n')
    .map((x: string) => x.trim())
    .filter((x: string) => x)
  markDirty('channels')
}
const targetPath = (provider: string) =>
  ((settingsForm.value.media_transfer_targets || {})[provider] || {}).folder_path || ''
const setTargetPath = (provider: string, value: string) => {
  settingsForm.value.media_transfer_targets[provider] = {
    ...(settingsForm.value.media_transfer_targets[provider] || {}),
    folder_path: value.trim(),
    folder_name: value.trim(),
  }
  markDirty('transferTargets')
}

const markDirty = (group: string) => {
  dirtyGroups.value[group] = true
}
const anyDirty = computed(() => Object.values(dirtyGroups.value).some(Boolean))
const dirtyGroupNames = computed(() => {
  const names: Record<string, string> = {
    channels: '资源检索频道',
    transferTargets: '影视发现保存目录',
    emby: 'Emby 媒体库',
    virtualLibraries: '榜单虚拟库',
    missing: '缺集扫描',
  }
  return Object.keys(dirtyGroups.value)
    .filter((k) => dirtyGroups.value[k])
    .map((k) => names[k] || k)
})

// ------------------------- Emby 缺集扫描 -------------------------
interface MissingEpisodeInfo {
  key: string
  season: number
  episode: number
  name?: string
  premiere_date?: string
}
interface MissingResult {
  id: number
  title: string
  library_name: string
  tmdb_id: number
  available_count: number
  missing_count: number
  missing_episodes: MissingEpisodeInfo[]
  subscription_id: number | null
  series_status?: string
  production_year?: number
}
const missingStatus = ref<any>(null)
const missingLibraries = ref<{ id: string; name: string; selected: boolean }[]>([])
const missingResults = ref<MissingResult[]>([])
const missingEvents = ref<any[]>([])
const missingBusy = ref(false)
const missingSelectedResults = ref<Record<number, boolean>>({})
const missingTargetProvider = ref('123')
const missingTimer = ref<ReturnType<typeof setInterval> | null>(null)

const loadMissingStatus = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/media-discovery/emby-missing/status`)
    missingStatus.value = response?.data?.data || null
    const active = missingStatus.value?.active_scan
    if (active) startMissingPolling()
    else stopMissingPolling()
  } catch {
    missingStatus.value = null
  }
}

const loadMissingLibraries = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/media-discovery/emby-missing/libraries`)
    const items = response?.data?.data?.items || []
    missingLibraries.value = items.map((x: any) => ({ id: x.id, name: x.name, selected: true }))
  } catch (err: any) {
    ElMessage.warning(err?.response?.data?.message || '读取电视剧媒体库失败')
  }
}

const startMissingScan = async () => {
  const selected = missingLibraries.value.filter((x) => x.selected).map((x) => x.id)
  if (!selected.length) {
    ElMessage.warning('请选择至少一个电视剧媒体库')
    return
  }
  missingBusy.value = true
  try {
    const response = await http.post(`${SERVER_URL}/media-discovery/emby-missing/scans`, {
      library_ids: selected,
    })
    if (response?.data?.code === 200) {
      ElMessage.success('已进入扫描队列')
      await loadMissingStatus()
      startMissingPolling()
    } else {
      ElMessage.error(response?.data?.message || '启动扫描失败')
    }
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || '启动扫描失败')
  } finally {
    missingBusy.value = false
  }
}

const loadMissingResults = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/media-discovery/emby-missing/results?limit=500`)
    if (response?.data?.code === 200) {
      missingResults.value = (response.data.data?.items || []).map((x: any) => ({
        ...x,
        missing_episodes: x.missing_episodes || [],
      }))
    }
  } catch {
    // 静默
  }
}

const loadMissingEvents = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/media-discovery/emby-missing/events?limit=40`)
    if (response?.data?.code === 200) missingEvents.value = response.data.data?.items || []
  } catch {
    // 静默
  }
}

const startMissingPolling = () => {
  if (missingTimer.value) return
  missingTimer.value = setInterval(async () => {
    await loadMissingStatus()
    await loadMissingResults()
    await loadMissingEvents()
    const active = missingStatus.value?.active_scan
    if (!active) stopMissingPolling()
  }, 2000)
}

const stopMissingPolling = () => {
  if (missingTimer.value) {
    clearInterval(missingTimer.value)
    missingTimer.value = null
  }
}

const createMissingSubscriptions = async () => {
  const ids = Object.keys(missingSelectedResults.value)
    .map(Number)
    .filter((id) => missingSelectedResults.value[id])
  if (!ids.length) {
    ElMessage.warning('请先勾选要补档的剧集')
    return
  }
  try {
    const response = await http.post(`${SERVER_URL}/media-discovery/emby-missing/subscriptions`, {
      result_ids: ids,
      target_provider: missingTargetProvider.value,
    })
    if (response?.data?.code === 200) {
      const data = response.data.data
      const skipped = (data.skipped || []).map((s: any) => s.reason).join('；')
      ElMessage.success(`已创建 ${data.created_count || 0} 个补档订阅${skipped ? '；跳过：' + skipped : ''}`)
      missingSelectedResults.value = {}
      await loadMissingResults()
    } else {
      ElMessage.error(response?.data?.message || '创建补档订阅失败')
    }
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || '创建补档订阅失败')
  }
}

const runMissingSubscription = async (subId: number) => {
  try {
    await http.post(`${SERVER_URL}/media-discovery/emby-missing/subscriptions/${subId}/run`)
    ElMessage.success('已开始检查该补档订阅')
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || '检查失败')
  }
}

const missingProgressPercent = computed(() => {
  const active = missingStatus.value?.active_scan
  if (!active?.total_series) return 0
  return Math.min(100, Math.round(((active.scanned_series || 0) / active.total_series) * 100))
})

const missingEventLabel = (type: string) => {
  const labels: Record<string, string> = {
    scan_queued: '扫描已排队',
    scan_started: '开始扫描',
    scan_finished: '扫描完成',
    scan_failed: '扫描失败',
    subscription_created: '已创建补档订阅',
    missing_scope_updated: '缺集范围已同步',
    missing_resolved: '缺集已解决',
  }
  return labels[type] || type
}

const missingEpisodeChips = (result: MissingResult) => {
  const chips = (result.missing_episodes || []).slice(0, 10).map((m) => m.key)
  const rest = (result.missing_episodes || []).length - chips.length
  if (rest > 0) chips.push(`+${rest}`)
  return chips
}

// 详情页「频道白名单监控」预填
const monitorFromDetail = () => {
  const data = detailData.value
  if (!data) return
  vfScene.value = '123'
  vfRules.value.push({
    id: '',
    media_name: data.title || '',
    media_type: data.media_type === 'tv' ? 'tv' : data.media_type === 'movie' ? 'movie' : '',
    tmdb_id: data.tmdb_id || undefined,
    title: data.title || '',
    poster_url: data.poster || '',
    enabled: true,
  })
  detailPage.value = false
  switchSection('tasks')
  ElMessage.info('已预填标题到频道订阅白名单，保存后生效')
}

// ========================= 数据加载 =========================

const loadMeta = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/media-discovery/meta`)
    if (response?.data?.code === 200 && response.data.data) {
      meta.value = { ...meta.value, ...response.data.data }
    }
  } catch {
    // 元数据加载失败不阻塞主流程
  }
}

const libraryRawItems = ref<DiscoverItem[]>([]) // 原始流（含去重合并）
const catalogRetryTimer = ref<ReturnType<typeof setTimeout> | null>(null)
let catalogRetryCount = 0

const clearCatalogRetry = () => {
  if (catalogRetryTimer.value) {
    clearTimeout(catalogRetryTimer.value)
    catalogRetryTimer.value = null
  }
}

const scheduleCatalogRetry = () => {
  clearCatalogRetry()
  if (catalogRetryCount >= 6) return
  catalogRetryCount += 1
  const delay = Math.min(8000, 1500 * Math.pow(2, catalogRetryCount - 1))
  catalogRetryTimer.value = setTimeout(() => {
    if (activeSection.value === 'library') load(false, false, true)
  }, delay)
}

// 各来源请求 URL（对齐参考实现 libraryRequestUrl：目录源带 wait=20）
const buildLibraryUrl = (force: boolean) => {
  const pageParam = `page=${page.value}`
  const forceParam = force ? '&force=1' : ''
  const waitParam = isCatalogSource.value ? '&wait=20' : ''
  switch (librarySource.value) {
    case 'douban':
      return `${SERVER_URL}/media-discovery/explore/douban/catalog?media_type=${exploreMediaType.value}&tag=${encodeURIComponent(exploreDoubanTag.value)}&sort=${exploreDoubanSort.value}&${pageParam}${forceParam}${waitParam}`
    case 'anilist':
    case 'bangumi':
      return `${SERVER_URL}/media-discovery/anime/catalog?source=${librarySource.value}&genre=${encodeURIComponent(exploreAnimeGenre.value)}&region=${exploreAnimeRegion.value}&year=${exploreAnimeYear.value}&sort=${exploreAnimeSort.value}&${pageParam}${forceParam}${waitParam}`
    case 'actors':
      if (searchMode.value && searchKeyword.value.trim()) {
        return `${SERVER_URL}/media-discovery/search?q=${encodeURIComponent(searchKeyword.value.trim())}&media_type=person&${pageParam}${forceParam}`
      }
      return `${SERVER_URL}/media-discovery/actors?${pageParam}${forceParam}`
    default:
      return `${SERVER_URL}/media-discovery/explore?type=${exploreMediaType.value}&genre=${encodeURIComponent(exploreGenre.value)}&year=${exploreYear.value}&region=${exploreRegion.value}&sort_by=${exploreSort.value}&${pageParam}${forceParam}`
  }
}

// 演员卡片元信息
const isActorsSource = computed(() => librarySource.value === 'actors')
const actorPopularity = (item: DiscoverItem) => (item.vote_avg > 0 ? item.vote_avg.toFixed(0) : '')
const actorKnownFor = (item: DiscoverItem) => (item.genres || []).slice(0, 3).join(' · ')
const actorDepartment = (item: DiscoverItem) => item.overview || '演员'

const load = async (force = false, append = false, isRetry = false) => {
  if (!isRetry) clearCatalogRetry()
  loading.value = true
  errorMessage.value = ''
  try {
    const url = buildLibraryUrl(force)
    const response = await http.get(url)
    if (response?.data?.code === 200) {
      const data: any = response.data.data
      const fresh: DiscoverItem[] = data?.items || []
      catalogMeta.value = {
        catalog_status: data?.catalog_status,
        page_state: data?.page_state,
        pending_count: data?.pending_count,
        cached_item_count: data?.cached_item_count,
        is_stale: data?.is_stale,
        catalog_total: data?.catalog_total,
        matched_count: data?.matched_count,
        source_exhausted: data?.source_exhausted,
      }
      if (append) {
        const known = new Set(
          items.value.map((i) => `${i.source}:${i.entity_key || i.tmdb_id || i.title}`)
        )
        items.value = [
          ...items.value,
          ...fresh.filter((i) => !known.has(`${i.source}:${i.entity_key || i.tmdb_id || i.title}`)),
        ]
      } else {
        items.value = fresh
      }
      totalPages.value = Math.max(data?.total_pages || 0, data?.has_next_page ? page.value + 1 : page.value, 1)
      // 目录源未就绪 → 自动重试（对齐参考实现退避语义）
      const pendingWhole =
        isCatalogSource.value && !items.value.length && ['pending', 'partial'].includes(catalogMeta.value.page_state || '')
      if (pendingWhole) scheduleCatalogRetry()
      else catalogRetryCount = 0
    } else {
      errorMessage.value = response?.data?.message || '加载失败'
      if (!isRetry) items.value = []
    }
  } catch (err) {
    errorMessage.value = '加载失败：' + (err as Error).message
    if (!isRetry) items.value = []
  } finally {
    loading.value = false
  }
  if (!append) afterItemsLoaded()
}

const hasNextPage = computed(() => {
  if (isCatalogSource.value || isActorsSource.value) return totalPages.value > page.value
  return page.value < totalPages.value
})

// 演员档案：作品列表（演员 → 作品回栈）
const openActorProfile = async (item: DiscoverItem) => {
  const id = item.tmdb_id || Number(item.external_id)
  if (!id) return
  actorProfileVisible.value = true
  actorWorksLoading.value = true
  actorProfileData.value = { title: item.title, poster: item.poster }
  actorWorks.value = []
  try {
    const response = await http.get(`${SERVER_URL}/media-discovery/actors/${id}/works`)
    if (response?.data?.code === 200 && response.data.data) {
      const data = response.data.data
      actorProfileData.value = { ...(data.actor || {}), tmdb_id: id }
      actorWorks.value = (data.items || []).map((w: any) => ({
        ...w,
        episode_title: w.episode_title || '',
      }))
    } else {
      ElMessage.error(response?.data?.message || '演员作品加载失败')
    }
  } catch (err) {
    ElMessage.error('演员作品加载失败：' + (err as Error).message)
  } finally {
    actorWorksLoading.value = false
  }
}

const closeActorProfile = () => {
  actorProfileVisible.value = false
  actorProfileData.value = {}
  actorWorks.value = []
}

const afterItemsLoaded = () => {
  checkEmbyStatus()
  refreshFavStatus()
}

const rankingProviderList = computed(() => [
  { key: 'hdhive', label: 'RE0流媒体榜', mark: '影' },
  ...meta.value.providers.map((p) => ({ key: 'hdhive:' + p.key, label: p.label, mark: p.label.slice(0, 1) })),
  { key: 'maoyan', label: '猫眼', mark: '猫' },
])

const maoyanCategory = ref('all')
const maoyanGroups = ref<{ kind: string; category?: string; items: DiscoverItem[] }[]>([])
const isMaoyanProvider = computed(() => rankingProvider.value === 'maoyan')

const selectRankingProvider = (key: string) => {
  rankingProvider.value = key
  if (key === 'maoyan') {
    rankingMediaType.value = ''
    rankingRegion.value = 'CN'
  }
  loadRankings()
}

const selectMaoyanCategory = (key: string) => {
  maoyanCategory.value = key
  loadRankings()
}

const maoyanCategoryLabel = computed(() => {
  if (maoyanCategory.value === 'all') return '全部榜单'
  const found = (meta.value.maoyan_category || []).find((c) => c.key === maoyanCategory.value)
  return found?.label || '全部榜单'
})

const loadMaoyan = async (force = false) => {
  loading.value = true
  errorMessage.value = ''
  try {
    const params = new URLSearchParams({ category: maoyanCategory.value })
    if (force) params.set('force', '1')
    const response = await http.get(`${SERVER_URL}/media-discovery/rankings/maoyan?${params.toString()}`)
    if (response?.data?.code === 200) {
      const data = response.data.data
      maoyanGroups.value = data?.groups || []
      if (!maoyanGroups.value.length && data?.message) errorMessage.value = data.message
    } else {
      errorMessage.value = response?.data?.message || '获取猫眼榜单失败'
      maoyanGroups.value = []
    }
  } catch (err) {
    errorMessage.value = '获取猫眼榜单失败：' + (err as Error).message
    maoyanGroups.value = []
  } finally {
    loading.value = false
  }
}

const loadRankings = async (force = false) => {
  if (isMaoyanProvider.value) {
    await loadMaoyan(force)
    return
  }
  loading.value = true
  errorMessage.value = ''
  try {
    const mediaTypes: string[] = rankingMediaType.value === '' ? ['movie', 'tv'] : [rankingMediaType.value]
    const groups: { kind: string; items: DiscoverItem[] }[] = []
    let lastMsg = ''
    for (const mt of mediaTypes) {
      const params = new URLSearchParams({
        provider: rankingProvider.value,
        media_type: mt,
        page: '1',
      })
      if (rankingProvider.value.startsWith('hdhive') && rankingRegion.value) {
        params.set('region', rankingRegion.value)
      }
      if (force) params.set('force', 'true')
      const response = await http.get(`${SERVER_URL}/media-discovery/rankings?${params.toString()}`)
      if (response?.data?.code === 200) {
        const data: PageResult | null = response.data.data
        if (data?.items?.length) {
          groups.push({ kind: rankingKindLabel(mt), items: data.items })
        }
      } else {
        lastMsg = response?.data?.message || '获取榜单失败'
      }
    }
    rankingGroups.value = groups
    if (!groups.length && lastMsg) errorMessage.value = lastMsg
  } catch (err) {
    errorMessage.value = '获取榜单失败：' + (err as Error).message
    rankingGroups.value = []
  } finally {
    loading.value = false
  }
}

const loadCalendar = async (force = false) => {
  loading.value = true
  errorMessage.value = ''
  try {
    const params = new URLSearchParams({ days: String(calendarDays.value), kind: calendarKind.value })
    if (force) params.set('force', 'true')
    const response = await http.get(`${SERVER_URL}/media-discovery/calendar?${params.toString()}`)
    if (response?.data?.code === 200) {
      calendarDaysList.value = response.data.data || []
      if (selectedDayIndex.value >= calendarDaysList.value.length) selectedDayIndex.value = 0
    } else {
      errorMessage.value = response?.data?.message || '加载失败'
      calendarDaysList.value = []
    }
  } catch (err) {
    errorMessage.value = '加载失败：' + (err as Error).message
    calendarDaysList.value = []
  } finally {
    loading.value = false
  }
}


// ------------------------- 收藏 -------------------------
const loadFavorites = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/media-discovery/favorites`)
    if (response?.data?.code === 200) {
      favoriteItems.value = response.data.data || []
    }
  } catch {
    // 静默
  }
}

const refreshFavStatus = async () => {
  const keys = (items.value || []).map((i) => i.entity_key).filter((k): k is string => !!k)
  if (!keys.length) return
  try {
    const response = await http.post(`${SERVER_URL}/media-discovery/favorites/check`, { keys })
    if (response?.data?.code === 200) {
      favKeySet.value = response.data.data || {}
    }
  } catch {
    // 静默
  }
}

const toggleFavorite = async (item: DiscoverItem) => {
  if (!item.entity_key || favBusyKeys.value[item.entity_key]) return
  favBusyKeys.value[item.entity_key] = true
  try {
    if (isFav(item)) {
      const target = favoriteItems.value.find((f) => f.entity_key === item.entity_key)
      if (!target) {
        ElMessage.warning('请在收藏页管理该收藏')
        return
      }
      const response = await http.delete(`${SERVER_URL}/media-discovery/favorites/${target.id}`)
      if (response?.data?.code === 200) {
        favKeySet.value[item.entity_key] = false
        ElMessage.success('已取消收藏')
      } else {
        ElMessage.error(response?.data?.message || '取消收藏失败')
      }
    } else {
      const response = await http.post(`${SERVER_URL}/media-discovery/favorites`, {
        entity_key: item.entity_key,
        source: item.source,
        media_type: item.media_type,
        external_id: item.external_id || item.douban_id || String(item.tmdb_id || ''),
        tmdb_id: item.tmdb_id || 0,
        title: item.title,
        original_title: item.original_title || '',
        poster: item.poster || '',
        overview: item.overview || '',
        vote_avg: item.vote_avg || 0,
        year: item.year || 0,
      })
      if (response?.data?.code === 200) {
        favKeySet.value[item.entity_key] = true
        ElMessage.success(`已收藏「${item.title}」`)
      } else {
        ElMessage.error(response?.data?.message || '收藏失败')
      }
    }
    loadFavorites()
  } catch (err) {
    ElMessage.error('收藏操作失败：' + (err as Error).message)
  } finally {
    favBusyKeys.value[item.entity_key!] = false
  }
}

const removeFavorite = async (fav: DiscoveryFavorite) => {
  try {
    const response = await http.delete(`${SERVER_URL}/media-discovery/favorites/${fav.id}`)
    if (response?.data?.code === 200) {
      ElMessage.success('已删除收藏')
      loadFavorites()
    } else {
      ElMessage.error(response?.data?.message || '删除失败')
    }
  } catch (err) {
    ElMessage.error('删除失败：' + (err as Error).message)
  }
}

// ------------------------- Emby 入库检测 / MoviePilot 订阅 -------------------------
const embyCheckBusy = ref(false)

// Emby 批量徽章（对齐参考实现 /api/media/emby/cards：已入库/连载中/缺集/未入库）
interface EmbyCardBadge {
  state: string
  display_label: string
  available_count?: number
  missing_count?: number
  message?: string
}
const embyBadgeMap = ref<Record<string, EmbyCardBadge>>({})
const embyBadgePending = ref<string[]>([])
const embyBadgeTimers = ref<ReturnType<typeof setTimeout>[]>([])

const embyBadgeKeyOf = (item: { media_type?: string; tmdb_id?: number }) =>
  `${item.media_type || 'movie'}:${item.tmdb_id || 0}`

const checkEmbyStatus = async () => {
  if (!items.value.length) return
  embyCheckBusy.value = true
  try {
    const payload = items.value
      .filter((item) => item.tmdb_id)
      .map((item) => ({
        key: embyBadgeKeyOf(item),
        media_type: item.media_type || 'movie',
        tmdb_id: item.tmdb_id,
        title: item.title,
        original_title: item.original_title || '',
        year: item.year || 0,
        total_episodes: Number(item.release_date) > 0 ? 0 : 0,
      }))
    if (!payload.length) return
    const response = await http.post(`${SERVER_URL}/media-discovery/emby/cards`, { items: payload })
    if (response && response.data && response.data.code === 200) {
      const data = response.data.data
      const map: Record<string, EmbyCardBadge> = {}
      for (const entry of data.items || []) {
        if (entry?.result) map[entry.key] = entry.result
      }
      embyBadgeMap.value = map
      embyBadgePending.value = data.progress_pending_keys || []
      scheduleBadgeRefresh()
    }
  } catch {
    // Emby 未配置或检测失败时静默跳过
  } finally {
    embyCheckBusy.value = false
  }
}

// pending 徽章延迟梯度刷新（对齐参考实现 0.9/1.8/3.6/7s）
const scheduleBadgeRefresh = () => {
  embyBadgeTimers.value.forEach((t) => clearTimeout(t))
  embyBadgeTimers.value = []
  if (!embyBadgePending.value.length) return
  for (const delay of [900, 1800, 3600, 7000]) {
    const timer = setTimeout(() => {
      if (activeSection.value === 'library' || activeSection.value === 'rankings') checkEmbyStatus()
    }, delay)
    embyBadgeTimers.value.push(timer)
  }
}

const embyBadgeOf = (item: DiscoverItem) => embyBadgeMap.value[embyBadgeKeyOf(item)]

const embyBadgeClass = (badge?: EmbyCardBadge) => {
  if (!badge) return ''
  switch (badge.state) {
    case 'in_library':
      return 'is-complete'
    case 'serializing':
      return 'is-subscribing'
    case 'missing':
      return 'is-missing'
    case 'not_found':
      return 'is-not-found'
    default:
      return 'is-error'
  }
}

const loadSubscribed = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/moviepilot/subscribes`)
    const list = response?.data?.data || []
    const map: Record<string, boolean> = {}
    list.forEach((s: any) => {
      if (s.tmdbid && s.type) map[`${s.type}:${s.tmdbid}`] = true
    })
    subscribedMap.value = map
  } catch {
    // 未配置 MoviePilot 时静默跳过
  }
}

const onSearch = async () => {
  const keyword = searchKeyword.value.trim()
  if (!keyword) {
    ElMessage.warning('请输入搜索关键词')
    return
  }
  searching.value = true
  searchMode.value = true
  errorMessage.value = ''
  page.value = 1
  try {
    if (isActorsSource.value) {
      await load(false)
      return
    }
    if (librarySource.value === 'anilist' || librarySource.value === 'bangumi') {
      // 动漫源搜索：走原番剧搜索（bangumi 主源/anilist 主源）
      const response = await http.get(
        `${SERVER_URL}/media-discovery/anime/search?keyword=${encodeURIComponent(keyword)}&source=${librarySource.value}&page=1`,
        { timeout: 30000 }
      )
      items.value = response?.data?.data?.items || []
      totalPages.value = 1
      catalogMeta.value = {}
      return
    }
    const mediaType = librarySource.value === 'douban' && exploreMediaType.value === 'tv' ? 'tv' : exploreMediaType.value
    const response = await http.get(
      `${SERVER_URL}/media-discovery/search?q=${encodeURIComponent(keyword)}&media_type=${mediaType}&page=1`,
      { timeout: 30000 }
    )
    if (response?.data?.code === 200) {
      const data = response.data.data
      items.value = data?.items || []
      totalPages.value = Math.max(data?.total_pages || 1, 1)
    } else {
      errorMessage.value = response?.data?.message || '搜索失败'
      items.value = []
    }
  } catch (err) {
    errorMessage.value = '搜索失败：' + (err as Error).message
    items.value = []
  } finally {
    searching.value = false
  }
  if (!isActorsSource.value) checkEmbyStatus()
}

const clearSearch = () => {
  searchMode.value = false
  searchKeyword.value = ''
  page.value = 1
  if (isActorsSource.value) {
    load()
    return
  }
  if (librarySource.value === 'anilist' || librarySource.value === 'bangumi') {
    load()
    return
  }
  load()
}

const toggleSubscribe = async (item: DiscoverItem) => {
  if (!item.tmdb_id || isSubscribed(item) || subscribingIds.value[item.tmdb_id]) return
  subscribingIds.value[item.tmdb_id] = true
  try {
    const mediaType = item.media_type || 'movie'
    const payload: Record<string, any> = {
      name: item.title,
      type: mediaType,
      tmdbid: item.tmdb_id,
    }
    if (item.year) payload.year = String(item.year)
    const response = await http.post(`${SERVER_URL}/moviepilot/subscribes`, payload)
    if (response?.data.code === 200) {
      ElMessage.success(`已订阅「${item.title}」`)
      subscribedMap.value[`${mediaType}:${item.tmdb_id}`] = true
    } else {
      ElMessage.error(response?.data.message || '添加订阅失败，请检查 MoviePilot 配置')
    }
  } catch (err) {
    console.error('添加订阅错误：', err)
    ElMessage.error('添加订阅失败')
  } finally {
    subscribingIds.value[item.tmdb_id!] = false
  }
}

// ------------------------- 基础配置 -------------------------
const loadSettings = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/media-discovery/settings`)
    if (response?.data?.code === 200 && response.data.data) {
      settingsForm.value = { ...settingsForm.value, ...response.data.data }
      settingsBaseline.value = JSON.stringify(settingsForm.value)
      dirtyGroups.value = {}
    }
  } catch {
    // 静默
  }
}

const saveSettings = async () => {
  savingSettings.value = true
  try {
    const response = await http.post(`${SERVER_URL}/media-discovery/settings`, settingsForm.value)
    if (response?.data?.code === 200) {
      settingsForm.value = { ...settingsForm.value, ...(response.data.data || {}) }
      settingsBaseline.value = JSON.stringify(settingsForm.value)
      dirtyGroups.value = {}
      ElMessage.success('基础配置已保存')
    } else {
      ElMessage.error(response?.data?.message || '保存失败')
    }
  } catch (err) {
    ElMessage.error('保存失败：' + (err as Error).message)
  } finally {
    savingSettings.value = false
  }
}

const testMediaEmby = async () => {
  embyTestBusy.value = true
  try {
    const response = await http.post(`${SERVER_URL}/media-discovery/emby/test`, {
      media_emby: settingsForm.value.media_emby,
    })
    if (response?.data?.code === 200) {
      embyTestMessage.value = response.data.message || '连接成功'
      ElMessage.success(embyTestMessage.value)
    } else {
      embyTestMessage.value = response?.data?.message || '连接失败'
      ElMessage.error(embyTestMessage.value)
    }
  } catch (err: any) {
    embyTestMessage.value = err?.response?.data?.message || '连接失败'
    ElMessage.error(embyTestMessage.value)
  } finally {
    embyTestBusy.value = false
  }
}

// ------------------------- 工具函数 -------------------------
const posterUrl = (item: { poster: string }) => item.poster || ''

const formatVote = (vote: number) => (vote > 0 ? vote.toFixed(1) : '')

const kindOf = (item: DiscoverItem) => {
  const mt = item.media_type
  if (mt === 'tv') return '剧集'
  if (item.source === 'anilist' || item.source === 'bangumi') return '动漫'
  if (mt === 'movie') return '电影'
  return '影片'
}

const openDetail = (item: DiscoverItem) => {
  let url = ''
  if (item.source === 'douban' && item.douban_id) {
    url = `https://movie.douban.com/subject/${item.douban_id}/`
  } else if (item.source === 'bangumi' && item.external_id) {
    url = `https://bgm.tv/subject/${item.external_id}`
  } else if (item.source === 'anilist' && item.external_id) {
    url = `https://anilist.co/anime/${item.external_id}`
  } else if (item.tmdb_id) {
    url = `https://www.themoviedb.org/${item.media_type === 'tv' ? 'tv' : 'movie'}/${item.tmdb_id}`
  }
  if (url) window.open(url, '_blank')
}

// ------------------------- 作品详情（全页） + 关联资源 + 订阅 -------------------------
const detailPage = ref(false)
const detailData = ref<any>(null)
const detailLoading = ref(false)
const detailError = ref('')
const detailResources = ref<ResourceItem[]>([])
const detailResourceErrors = ref<ResourceSearchResult['errors']>([])
const detailResourceLoading = ref(false)
const detailResourceSourceFilter = ref('all') // all/re0/guanying/tg
const detailResourceFilter = ref('all') // all/123/guangya/pan139/magnet
const copiedLink = ref('')

const resourceSourceChoices = computed(() => {
  const counts = new Map<string, number>()
  for (const r of detailResources.value) counts.set(r.source, (counts.get(r.source) || 0) + 1)
  const defs = [
    { key: 'all', label: '全部来源' },
    { key: 're0', label: 'RE0' },
    { key: 'guanying', label: '观影' },
    { key: 'tg', label: 'TG 频道' },
  ]
  return defs.filter((d) => d.key === 'all' || counts.get(d.key))
})

const resourceProviderKey = (item: ResourceItem) => {
  const p = String(item.provider || '').toLowerCase()
  if (p.includes('guangya') || p === 'gy') return 'guangya'
  if (p === 'pan139' || p === '139') return 'pan139'
  if (p.includes('123')) return '123'
  if (p === 'magnet' || p === 'ed2k') return 'magnet'
  return 'magnet'
}

const detailResourceChoices = computed(() => {
  const counts = new Map<string, number>()
  for (const item of detailResources.value) {
    const key = resourceProviderKey(item)
    counts.set(key, (counts.get(key) || 0) + 1)
  }
  const defs: { key: string; label: string }[] = [
    { key: 'all', label: '全部类型' },
    { key: '123', label: '123' },
    { key: 'guangya', label: '光鸭' },
    { key: 'pan139', label: '139' },
    { key: 'magnet', label: '磁力 / ED2K' },
  ]
  return defs.filter((d) => d.key === 'all' || counts.get(d.key))
})

const detailResourcesFiltered = computed(() => {
  let list = detailResources.value
  if (detailResourceSourceFilter.value !== 'all') {
    list = list.filter((r) => r.source === detailResourceSourceFilter.value)
  }
  if (detailResourceFilter.value !== 'all') {
    list = list.filter((r) => resourceProviderKey(r) === detailResourceFilter.value)
  }
  return list
})

const detailResourceSummary = computed(() => {
  const total = detailResources.value.length
  if (!total) return ''
  const bySource = new Map<string, number>()
  for (const r of detailResources.value) {
    const label = r.source === 're0' ? 'RE0' : r.source === 'guanying' ? '观影' : r.source === 'tg' ? 'TG 频道' : r.source
    bySource.set(label, (bySource.get(label) || 0) + 1)
  }
  const parts = [...bySource.entries()].map(([k, v]) => `${k} ${v}`)
  return `已匹配 ${total} 条资源 · ${parts.join(' · ')}`
})

const episodeTextOf = (item: ResourceItem) => {
  const ep = item.episode
  if (!ep || ep.episode_num == null) return ''
  const pad = (n: number) => (n >= 100 ? String(Math.trunc(n)) : String(Math.trunc(n)).padStart(2, '0'))
  const sNum = pad(ep.season_num == null ? 1 : ep.season_num)
  let text = `S${sNum}E${pad(ep.episode_num)}`
  if (ep.end_episode_num != null) text += `-E${pad(ep.end_episode_num)}`
  return text
}

const episodeTagOf = (item: ResourceItem) => {
  const ep = item.episode
  if (!ep) return ''
  const total = Number(ep.total_episode_num || 0)
  if (ep.episode_num != null) {
    const base = episodeTextOf(item)
    return total > 0 ? `${base}${ep.is_complete ? '（全' + total + '集）' : '（共' + total + '集）'}` : base
  }
  if (ep.season_num != null) {
    const base = `第 ${ep.season_num} 季`
    return total > 0 ? `${base} ${ep.is_complete ? '全' : '共'}${total}集` : base
  }
  return ''
}

const pointTextOf = (item: ResourceItem) => {
  const offline = item.link_type === 'magnet' || item.link_type === 'ed2k'
  if (offline) {
    return item.supported_targets?.length
      ? '可离线到 ' + item.supported_targets.map((p) => (p === 'guangya' ? '光鸭' : p === 'pan139' ? '139' : p)).join(' / ')
      : '离线资源'
  }
  if (item.source === 'guanying') return '分享资源'
  if (item.source === 'tg') return '频道分享'
  if (item.is_unlocked) return '已解锁'
  if (item.points_known) return `${item.unlock_points} 积分`
  return '积分未知'
}

const unlockedCountOf = (item: ResourceItem) =>
  item.unlocked_users_count != null && Number(item.unlocked_users_count) > 0

const specTagsOf = (item: ResourceItem) => item.resource_spec_tags || []

const resourceTransferDisabled = (item: ResourceItem) => {
  const key = resourceProviderKey(item)
  if (key === 'magnet') return false
  const target = detailData.value?.transfer_targets?.[key]
  return !target?.configured
}

const resourceTargetLabel = (item: ResourceItem) => {
  const key = resourceProviderKey(item)
  if (key === 'magnet') return '磁力离线'
  const names: Record<string, string> = { '123': '123', guangya: '光鸭', pan139: '139' }
  return `转存到${names[key] || key}`
}

const sourceLabelOf = (label: string) =>
  label === 're0' ? 'RE0' : label === 'guanying' ? '观影' : label === 'tg' ? 'TG 频道' : label

const openDetailWithResources = async (item: DiscoverItem) => {
  detailPage.value = true
  detailLoading.value = true
  detailError.value = ''
  detailData.value = null
  detailResources.value = []
  detailResourceErrors.value = []
  detailResourceFilter.value = 'all'
  detailResourceSourceFilter.value = 'all'
  copiedLink.value = ''
  try {
    let source = item.source || 'tmdb'
    let entityType = item.media_type || 'movie'
    const externalId = item.external_id || item.douban_id || String(item.tmdb_id || '')
    if (source === 'hdhive') source = 'tmdb'
    if (source === 'anilist' || source === 'bangumi') entityType = 'anime'
    if (source === 'tmdb' && entityType === 'person') {
      detailPage.value = false
      detailLoading.value = false
      await openActorProfile(item)
      return
    }
    const response = await http.get(
      `${SERVER_URL}/media-discovery/details/${source}/${entityType}/${encodeURIComponent(externalId)}`
    )
    if (response?.data?.code === 200 && response.data.data) {
      detailData.value = response.data.data
    } else {
      detailError.value = response?.data?.message || '作品资料加载失败'
      detailData.value = { ...item, source, entity_type: entityType, external_id: externalId }
    }
  } catch (err) {
    detailError.value = '作品资料加载失败：' + (err as Error).message
    detailData.value = { ...item }
  } finally {
    detailLoading.value = false
  }
  loadDetailResources()
}

const closeDetail = () => {
  detailPage.value = false
  detailData.value = null
  detailResources.value = []
  detailResourceErrors.value = []
}

const loadDetailResources = async () => {
  const item = detailData.value
  if (!item) return
  const title = item.title || ''
  if (!item.tmdb_id && !title) return
  detailResourceLoading.value = true
  const sources = ['re0', 'guanying', 'tg']
  try {
    const responses = await Promise.allSettled(
      sources.map((source) =>
        http.post(
          `${SERVER_URL}/media-discovery/resources/search`,
          {
            title,
            aliases: item.original_title && item.original_title !== title ? [item.original_title] : [],
            tmdb_id: item.tmdb_id || null,
            media_type: item.media_type === 'tv' ? 'tv' : 'movie',
            year: item.year ? String(item.year) : '',
            sources: [source],
          },
          { timeout: 60000 }
        )
      )
    )
    const items: ResourceItem[] = []
    const errors: ResourceSearchResult['errors'] = []
    responses.forEach((res, idx) => {
      if (res.status === 'fulfilled') {
        const data = res.value.data?.data || {}
        items.push(...(data.items || []))
        errors.push(...(data.errors || []))
      } else {
        const msg = (res.reason?.response?.data?.message as string) || res.reason?.message || '请求失败'
        errors.push({ source: sources[idx], code: 'REQUEST_FAILED', error: msg })
      }
    })
    const seen = new Set<string>()
    detailResources.value = items.filter((r) =>
      seen.has(r.item_key) ? false : (seen.add(r.item_key), true)
    )
    detailResourceErrors.value = errors
  } finally {
    detailResourceLoading.value = false
  }
}

const offlineResource = async (item: ResourceItem) => {
  const confirmed = await ElMessageBox.confirm('确认把该链接提交到 qBittorrent 离线队列吗？', '磁力离线', {
    type: 'warning',
  }).catch(() => null)
  if (!confirmed) return
  try {
    const response = await http.post(`${SERVER_URL}/media-discovery/resources/offline`, {
      link: item.share_url || item.slug,
      provider: '123',
    })
    if (response?.data?.code === 200) ElMessage.success(response.data.message || '离线任务已提交')
    else ElMessage.error(response?.data?.message || '离线提交失败')
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || '离线提交失败')
  }
}

const transferResource = async (item: ResourceItem) => {
  const key = resourceProviderKey(item)
  if (key === 'magnet') {
    await offlineResource(item)
    return
  }
  const names: Record<string, string> = { '123': '123', guangya: '光鸭', pan139: '139' }
  const target = detailData.value?.transfer_targets?.[key]
  const confirmed = await ElMessageBox.confirm(
    `确认转存到「${target?.folder_name || names[key]}」目录吗？`,
    '转存确认',
    { type: 'warning' }
  ).catch(() => null)
  if (!confirmed) return
  try {
    const response = await http.post(
      `${SERVER_URL}/media-discovery/resources/transfer`,
      {
        source: item.source,
        provider: key,
        slug: item.slug || '',
        share_url: item.share_url || '',
      },
      { timeout: 200000 }
    )
    if (response?.data?.code === 200) ElMessage.success(response.data.message || '转存成功')
    else ElMessage.error(response?.data?.message || '转存失败')
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || '转存失败')
  }
}

const copyResourceLink = async (item: ResourceItem) => {
  try {
    if (item.source === 're0' && item.slug) {
      const response = await http.post(`${SERVER_URL}/media-discovery/resources/copy-link`, {
        source: 're0',
        provider: item.provider,
        slug: item.slug,
      })
      const link = response.data?.data?.link || ''
      if (link) await writeClipboard(link)
      return
    }
    if (item.share_url) await writeClipboard(item.share_url)
  } catch (error) {
    ElMessage.error('复制链接失败')
  }
}

// ------------------------- 订阅弹窗（多规则编辑器） -------------------------
interface SubscriptionRuleForm {
  name: string
  enabled: boolean
  target_provider: string
  max_points: number
  resolutions: string
  qualities: string
  languages: string
  release_groups: string
  prefer_dolby_vision: boolean
  message_keywords: string
  must_contain: string
  must_not_contain: string
}

interface SubscriptionForm {
  interval_minutes: number
  target_provider: string
  enabled: boolean
  rules: SubscriptionRuleForm[]
}

const subModalVisible = ref(false)
const subSaving = ref(false)
const subEditingId = ref<number | null>(null)
const subForm = ref<SubscriptionForm>(defaultSubForm())
const subExisting = ref<any>(null)

function splitList(v: string): string[] {
  return v
    .split(/[,，;；|、]/)
    .map((x) => x.trim())
    .filter((x) => x)
}

function joinList(v: unknown): string {
  return Array.isArray(v) ? v.join(', ') : ''
}

function defaultRule(): SubscriptionRuleForm {
  return {
    name: '自动规则 1',
    enabled: true,
    target_provider: '123',
    max_points: 4,
    resolutions: '2160p, 1080p',
    qualities: 'Remux, BluRay, WEB-DL',
    languages: '国语, 中字, 中文',
    release_groups: '',
    prefer_dolby_vision: false,
    message_keywords: '',
    must_contain: '',
    must_not_contain: '',
  }
}

function defaultSubForm(): SubscriptionForm {
  return {
    interval_minutes: 360,
    target_provider: '123',
    enabled: true,
    rules: [defaultRule()],
  }
}

const targetProviderOptions = computed(() => {
  const targets = detailData.value?.transfer_targets || {}
  return ['123', 'guangya', 'pan139'].map((key) => {
    const names: Record<string, string> = { '123': '123', guangya: '光鸭', pan139: '139' }
    const configured = targets[key]?.configured
    return { value: key, label: configured ? names[key] : `${names[key]}（目录未配置）` }
  })
})

const openSubscriptionModal = async () => {
  const data = detailData.value
  if (!data) return
  subForm.value = defaultSubForm()
  subEditingId.value = null
  subExisting.value = null
  if (data.subscription) {
    subExisting.value = data.subscription
    subEditingId.value = data.subscription.id
    try {
      const response = await http.get(`${SERVER_URL}/media-discovery/subscriptions`)
      const list = response?.data?.data?.items || []
      const found = list.find((x: any) => x.id === data.subscription.id)
      if (found) {
        subForm.value.interval_minutes = found.interval_minutes || 360
        subForm.value.target_provider = found.target_provider || '123'
        subForm.value.enabled = !!found.enabled
        const rules = (found.rules || []).map((r: any, idx: number) => ({
          name: r.name || `自动规则 ${idx + 1}`,
          enabled: r.enabled !== false,
          target_provider: r.target_provider || '123',
          max_points: r.max_points || 4,
          resolutions: joinList(r.preferences?.resolutions) || '2160p, 1080p',
          qualities: joinList(r.preferences?.qualities) || 'Remux, BluRay, WEB-DL',
          languages: joinList(r.preferences?.languages) || '国语, 中字, 中文',
          release_groups: joinList(r.preferences?.release_groups),
          prefer_dolby_vision: !!r.preferences?.prefer_dolby_vision,
          message_keywords: joinList(r.match?.message_keywords),
          must_contain: joinList(r.match?.must_contain),
          must_not_contain: joinList(r.match?.must_not_contain),
        }))
        if (rules.length) subForm.value.rules = rules
      }
    } catch {
      // 列表读取失败则用默认表单
    }
  }
  subModalVisible.value = true
}

const addSubscriptionRule = () => {
  const rule = defaultRule()
  rule.name = `自动规则 ${subForm.value.rules.length + 1}`
  subForm.value.rules.push(rule)
}

const removeSubscriptionRule = (idx: number) => {
  if (subForm.value.rules.length <= 1) {
    ElMessage.warning('每个影视订阅至少保留一条自动规则')
    return
  }
  subForm.value.rules.splice(idx, 1)
}

const saveSubscription = async () => {
  if (!detailData.value) return
  subSaving.value = true
  try {
    const form = subForm.value
    const rules = form.rules.map((r) => ({
      name: r.name,
      enabled: r.enabled,
      target_provider: r.target_provider,
      max_points: r.max_points,
      preferences: {
        resolutions: splitList(r.resolutions),
        qualities: splitList(r.qualities),
        languages: splitList(r.languages),
        release_groups: splitList(r.release_groups),
        prefer_dolby_vision: r.prefer_dolby_vision,
      },
      message_keywords: splitList(r.message_keywords),
      must_contain: splitList(r.must_contain),
      must_not_contain: splitList(r.must_not_contain),
    }))
    const payload: any = {
      source: 'tmdb',
      entity_type: detailData.value.entity_type === 'person' ? 'person' : detailData.value.media_type || detailData.value.entity_type || 'movie',
      external_id: String(detailData.value.external_id || detailData.value.tmdb_id || ''),
      tmdb_id: detailData.value.tmdb_id || 0,
      media_type: detailData.value.media_type || 'movie',
      title: detailData.value.title || '',
      original_title: detailData.value.original_title || '',
      poster_url: detailData.value.poster || '',
      target_provider: form.rules[0]?.target_provider || form.target_provider,
      transfer_mode: 'auto',
      enabled: form.enabled,
      interval_minutes: form.interval_minutes,
      preferences: {
        max_points: form.rules[0]?.max_points || 4,
        resolutions: splitList(form.rules[0]?.resolutions || ''),
        qualities: splitList(form.rules[0]?.qualities || ''),
        languages: splitList(form.rules[0]?.languages || ''),
      },
      rules,
      metadata: { media_type: detailData.value.media_type || 'movie' },
    }
    const response = subEditingId.value
      ? await http.patch(`${SERVER_URL}/media-discovery/subscriptions/${subEditingId.value}`, payload)
      : await http.post(`${SERVER_URL}/media-discovery/subscriptions`, payload)
    if (response?.data?.code === 200) {
      ElMessage.success(response.data.message || '订阅已保存')
      subModalVisible.value = false
      if (response.data.data?.subscription) {
        const sub = response.data.data.subscription
        detailData.value.subscription = {
          id: sub.id,
          status: sub.status,
          enabled: sub.enabled,
          target_provider: sub.target_provider,
          rules_count: (sub.rules || []).length,
        }
      }
    } else {
      ElMessage.error(response?.data?.message || '订阅保存失败')
    }
  } catch (err: any) {
    ElMessage.error(err?.response?.data?.message || '订阅保存失败')
  } finally {
    subSaving.value = false
  }
}

const isDetailSubscribed = computed(() => !!detailData.value?.subscription)

const detailDoubanLink = computed(() => {
  if (detailData.value?.source === 'douban' && detailData.value?.external_id) {
    return `https://movie.douban.com/subject/${detailData.value.external_id}/`
  }
  return ''
})

// 详情页收藏状态
const detailIsFav = computed(() => !!detailData.value?.entity_key && !!favKeySet.value[detailData.value.entity_key])

const toggleDetailFavorite = async () => {
  const data = detailData.value
  if (!data?.entity_key) return
  await toggleFavorite({
    ...(data as DiscoverItem),
    entity_key: data.entity_key,
    source: data.source,
    media_type: data.media_type,
    external_id: data.external_id,
    tmdb_id: data.tmdb_id,
    title: data.title,
    original_title: data.original_title,
    poster: data.poster,
    vote_avg: data.vote_avg,
    year: data.year,
  } as DiscoverItem)
}

const writeClipboard = async (text: string) => {
  try {
    await navigator.clipboard.writeText(text)
    copiedLink.value = text
    ElMessage.success('链接已复制')
  } catch {
    ElMessage.warning('浏览器拒绝了剪贴板访问')
  }
}

// ------------------------- 观影设置与登录 -------------------------
const guanyingStatus = ref<GuanyingSessionStatus>({})
const guanyingUsername = ref('')
const guanyingPassword = ref('')
const guanyingLogging = ref(false)
const guanyingCaptchaState = ref<GuanyingCaptcha | null>(null)
const guanyingCaptchaPoints = ref<{ x: number; y: number }[]>([])
const guanyingCaptchaImg = ref('')

const loadGuanyingStatus = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/media-discovery/guanying/session`)
    guanyingStatus.value = response.data?.data || {}
  } catch {
    guanyingStatus.value = {}
  }
}

const guanyingLogin = async () => {
  const username = guanyingUsername.value.trim()
  const password = guanyingPassword.value
  if (!username || !password) {
    ElMessage.warning('请输入观影账号和密码')
    return
  }
  guanyingLogging.value = true
  try {
    const response = await http.post(`${SERVER_URL}/media-discovery/guanying/login`, {
      username,
      password,
      attempt_id: guanyingCaptchaState.value?.attempt_id,
    })
    const data = response.data?.data || {}
    if (data.captcha_required) {
      await loadGuanyingCaptcha(data.attempt_id || data.captcha?.attempt_id)
      ElMessage.info('请按顺序点击验证码文字')
      return
    }
    ElMessage.success(response.data?.message || '观影登录成功，登录态已安全保存')
    resetGuanyingLogin()
    await loadGuanyingStatus()
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '观影登录失败')
  } finally {
    guanyingLogging.value = false
  }
}

const loadGuanyingCaptcha = async (attemptId: string) => {
  try {
    const response = await http.post(`${SERVER_URL}/media-discovery/guanying/captcha`, { attempt_id: attemptId })
    const data = response.data?.data || {}
    guanyingCaptchaState.value = { attempt_id: data.attempt_id || attemptId, ...data }
    guanyingCaptchaImg.value = data.image || ''
    guanyingCaptchaPoints.value = []
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '观影验证码获取失败')
  }
}

const onCaptchaClick = (event: MouseEvent) => {
  const target = event.currentTarget as HTMLElement
  const rect = target.getBoundingClientRect()
  guanyingCaptchaPoints.value.push({
    x: Math.round(event.clientX - rect.left),
    y: Math.round(event.clientY - rect.top),
  })
}

const undoCaptchaPoint = () => {
  guanyingCaptchaPoints.value.pop()
}

const verifyGuanyingCaptcha = async () => {
  if (!guanyingCaptchaState.value) return
  const chars = (guanyingCaptchaState.value.text || '').length
  if (guanyingCaptchaPoints.value.length !== chars) {
    ElMessage.warning(`请按顺序点击 ${chars} 个文字（已点 ${guanyingCaptchaPoints.value.length} 个）`)
    return
  }
  guanyingLogging.value = true
  try {
    await http.post(`${SERVER_URL}/media-discovery/guanying/captcha/verify`, {
      attempt_id: guanyingCaptchaState.value.attempt_id,
      points: guanyingCaptchaPoints.value,
    })
    await guanyingLogin()
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '验证码校验失败')
    if (guanyingCaptchaState.value) await loadGuanyingCaptcha(guanyingCaptchaState.value.attempt_id)
  } finally {
    guanyingLogging.value = false
  }
}

const guanyingRelogin = async () => {
  guanyingLogging.value = true
  try {
    const response = await http.post(`${SERVER_URL}/media-discovery/guanying/relogin`, {})
    const data = response.data?.data || {}
    if (data.captcha_required) {
      await loadGuanyingCaptcha(data.attempt_id || data.captcha?.attempt_id)
      ElMessage.info('会话已失效，请完成验证码恢复登录')
      return
    }
    ElMessage.success(response.data?.message || '观影登录已恢复')
    resetGuanyingLogin()
    await loadGuanyingStatus()
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '观影恢复失败')
  } finally {
    guanyingLogging.value = false
  }
}

const clearGuanyingSession = async () => {
  try {
    await ElMessageBox.confirm('确认清除已保存的观影会话与自动恢复账号密码吗？', '提示', { type: 'warning' })
  } catch {
    return
  }
  try {
    await http.delete(`${SERVER_URL}/media-discovery/guanying/session`)
    ElMessage.success('观影登录信息已清除')
    resetGuanyingLogin()
    await loadGuanyingStatus()
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '清除失败')
  }
}

const resetGuanyingLogin = () => {
  guanyingUsername.value = ''
  guanyingPassword.value = ''
  guanyingCaptchaState.value = null
  guanyingCaptchaImg.value = ''
  guanyingCaptchaPoints.value = []
}

const airTimeOf = (item: DiscoverItem) => (item.air_date && item.air_date.length > 10 ? item.air_date.slice(11, 16) : '')

const epLabelOf = (item: DiscoverItem) => {
  if (item.season_number !== undefined && item.episode_number !== undefined && item.media_type === 'tv') {
    return `S${String(item.season_number).padStart(2, '0')}E${String(item.episode_number).padStart(2, '0')}`
  }
  return ''
}

const filterActive = computed(() => {
  if (librarySource.value === 'douban') {
    return exploreMediaType.value !== 'movie' || exploreDoubanTag.value !== '热门' || exploreDoubanSort.value !== 'T'
  }
  if (librarySource.value === 'anilist' || librarySource.value === 'bangumi') {
    return !!exploreAnimeGenre.value || !!exploreAnimeRegion.value || !!exploreAnimeYear.value || exploreAnimeSort.value !== 'popular'
  }
  return (
    exploreMediaType.value !== 'movie' ||
    !!exploreGenre.value ||
    !!exploreYear.value ||
    exploreRegion.value !== '' ||
    exploreSort.value !== 'popular' ||
    (librarySource.value === 'douban' && exploreDoubanTag.value !== '热门')
  )
})

const resetLibraryFilters = () => {
  exploreMediaType.value = 'movie'
  exploreGenre.value = ''
  exploreYear.value = ''
  exploreRegion.value = ''
  exploreSort.value = 'popular'
  exploreDoubanTag.value = '热门'
  exploreDoubanSort.value = 'T'
  exploreAnimeGenre.value = ''
  exploreAnimeRegion.value = ''
  exploreAnimeYear.value = ''
  exploreAnimeSort.value = 'popular'
  page.value = 1
  load()
}

// 筛选面板标题（对齐参考实现 renderLibraryFilterPanel）
const filterPanelTitle = computed(() => {
  if (librarySource.value === 'douban') return '按豆瓣分类浏览'
  if (librarySource.value === 'anilist' || librarySource.value === 'bangumi') return '按偏好探索动漫'
  return '按偏好探索片库'
})

// ------------------------- 分区切换与初始化 -------------------------
const switchSection = (key: SectionKey) => {
  activeSection.value = key
  if (key === 'library') {
    if (!items.value.length && !loading.value) load()
  } else if (key === 'rankings') {
    if (!rankingGroups.value.length && !loading.value) loadRankings()
  } else if (key === 'calendar') {
    if (!calendarDaysList.value.length && !loading.value) loadCalendar()
  } else if (key === 'tasks') {
    loadSettings()
    loadMissingStatus()
    loadMissingLibraries()
    loadMissingResults()
    loadMissingEvents()
  }
  nextTick(computeColumns)
}

watch(librarySource, () => {
  searchMode.value = false
  searchKeyword.value = ''
  errorMessage.value = ''
  page.value = 1
  clearCatalogRetry()
  catalogMeta.value = {}
  catalogRetryCount = 0
  if (librarySource.value === 'favorites') {
    loadFavorites()
  } else {
    load()
  }
  nextTick(computeColumns)
})

watch(exploreMediaType, () => {
  exploreGenre.value = ''
  exploreDoubanTag.value = '热门'
  page.value = 1
  if (!searchMode.value && librarySource.value !== 'favorites') load()
})

watch(calendarKind, () => {
  selectedDayIndex.value = 0
  loadCalendar()
})

watch(calendarDays, () => {
  selectedDayIndex.value = 0
  loadCalendar()
})

const onWindowResize = () => {
  computeColumns()
  setupInfinite()
}


// ------------------------- 频道白名单可视化筛选 -------------------------
interface VfRule {
  id: string
  media_name: string
  media_type: string
  tmdb_id?: number
  title?: string
  poster_url?: string
  enabled: boolean
  created_at?: string
}
const vfScene = ref('123')
const vfSceneLabels = ref<Record<string, string>>({ '123': '123 频道订阅白名单', guangya: '光鸭频道订阅白名单', '139': '移动云盘频道订阅白名单' })
const vfRules = ref<VfRule[]>([])
const vfParseMode = ref('advanced')
const vfSaving = ref(false)
const vfLoaded = ref('')

const loadVfConfig = async (scene: string) => {
  if (vfLoaded.value === scene) return
  try {
    const response = await http.get(`${SERVER_URL}/visual-filter/config`, { params: { scene } })
    const data = response?.data?.data
    if (data?.current) {
      vfRules.value = data.current.rules || []
      vfParseMode.value = data.current.parse_mode || 'advanced'
      if (data.scene_labels) vfSceneLabels.value = data.scene_labels
      vfLoaded.value = scene
    }
  } catch { /* 静默 */ }
}

const vfAddRule = () => {
  vfRules.value.push({ id: '', media_name: '', media_type: '', enabled: true })
}

const vfLookupPoster = async (rule: VfRule) => {
  const keyword = (rule.media_name || '').trim()
  if (!keyword) return
  try {
    const type = rule.media_type === 'movie' || rule.media_type === 'tv' ? rule.media_type : 'multi'
    const response = await http.get(`${SERVER_URL}/visual-filter/tmdb/search`, { params: { query: keyword, type } })
    const data = response?.data?.data
    const results = data?.results || []
    if (!results.length) return
    const match = results.find((r: { title: string }) => r.title === keyword) || results[0]
    rule.tmdb_id = match.id
    rule.title = match.title
    rule.poster_url = match.poster_path ? `${(data.image_base_url || 'https://image.tmdb.org/t/p').replace(/\/$/, '')}/w185${match.poster_path}` : ''
    if (!rule.media_type && match.media_type) rule.media_type = match.media_type
  } catch { /* 海报补全失败静默 */ }
}

const vfSave = async () => {
  vfSaving.value = true
  try {
    const rules = vfRules.value.filter((r) => (r.media_name || '').trim())
    const response = await http.post(`${SERVER_URL}/visual-filter/config`, {
      scene: vfScene.value,
      parse_mode: vfParseMode.value,
      rules,
    })
    if (response?.data?.code === 200) {
      if (response.data.data?.rules) vfRules.value = response.data.data.rules
      ElMessage.success('白名单规则已保存')
    } else {
      ElMessage.error(response?.data?.message || '保存失败')
    }
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '保存失败')
  } finally { vfSaving.value = false }
}

watch(vfScene, (scene) => {
  vfLoaded.value = ''
  loadVfConfig(scene)
})

onMounted(async () => {
  loadMeta()
  loadSubscribed()
  loadFavorites()
  load()
  loadGuanyingStatus()
  loadVfConfig(vfScene.value)
  await nextTick()
  computeColumns()
  setupInfinite()
  window.addEventListener('resize', onWindowResize)
})

onBeforeUnmount(() => {
  io?.disconnect()
  window.removeEventListener('resize', onWindowResize)
  clearCatalogRetry()
  stopMissingPolling()
  embyBadgeTimers.value.forEach((t) => clearTimeout(t))
})
</script>

<template>
  <div class="md-page">
    <!-- 分区导航 -->
    <nav class="md-page-nav">
      <button
        v-for="sec in sections"
        :key="sec.key"
        class="md-page-nav-item"
        :class="{ active: activeSection === sec.key }"
        @click="switchSection(sec.key)"
      >
        <span class="md-nav-emoji">{{ sec.icon }}</span>
        <span>{{ sec.title }}</span>
      </button>
    </nav>

    <!-- ================= 影视探索 ================= -->
    <section v-show="activeSection === 'library'" class="md-section">
      <div class="md-hero md-hero-blue">
        <div class="md-hero-aurora"></div>
        <div class="md-hero-eyebrow">🎞️ LIBRARY · 影视探索</div>
        <h2>探索片库</h2>
        <p>按偏好探索RE0片库、豆瓣片单与番剧放送，收藏心仪作品并联动 MoviePilot 订阅下载。</p>
      </div>

      <!-- 工具栏：来源 Tab + 搜索 -->
      <div class="md-toolbar md-library-toolbar">
        <div class="md-tabs">
          <button
            v-for="src in librarySources"
            :key="src.key"
            class="md-tab"
            :class="{ 'is-active': librarySource === src.key }"
            @click="librarySource = src.key"
          >
            {{ src.label }}
          </button>
        </div>
        <div class="md-toolbar-spacer"></div>
        <div class="md-library-toolbar-actions">
          <div v-if="librarySource !== 'favorites' && librarySource !== 'douban'" class="md-library-inline-search">
            <input
              v-model="searchKeyword"
              class="md-input"
              type="search"
              :placeholder="
                isActorsSource
                  ? '输入演员姓名，按 Enter 搜索'
                  : librarySource === 'anilist' || librarySource === 'bangumi'
                    ? '输入动漫名称，按 Enter 搜索'
                    : '输入电影或电视剧名称，按 Enter 搜索'
              "
              @keyup.enter="onSearch()"
            />
            <button
              class="md-btn is-primary"
              :disabled="searching || !searchKeyword.trim()"
              @click="onSearch()"
            >
              搜索
            </button>
            <button v-if="searchMode" class="md-btn" @click="clearSearch()">清空</button>
          </div>
          <button class="md-btn is-soft" @click="searchMode ? clearSearch() : (librarySource === 'favorites' ? loadFavorites() : load(true))">↻ 刷新</button>
        </div>
      </div>

      <!-- 演员介绍面板（对齐参考实现 PEOPLE SPOTLIGHT） -->
      <div v-if="isActorsSource && !searchMode" class="md-library-actors-intro">
        <div class="md-library-filter-panel-head">
          <div>
            <span class="md-kicker">PEOPLE SPOTLIGHT</span>
            <strong>本周热门演员</strong>
          </div>
        </div>
        <p class="md-actors-intro-text">
          按 TMDB 热度浏览或搜索人物资料；点击肖像进入演员档案，查看完整作品列表或订阅其后续新作。
        </p>
        <div class="md-actors-intro-tags">
          <span>热门浏览</span><span>人物搜索</span><span>作品直达</span>
        </div>
      </div>

      <!-- 筛选面板（TMDB / 豆瓣 / 动漫） -->
      <div v-if="librarySource === 'tmdb' || librarySource === 'douban' || librarySource === 'anilist' || librarySource === 'bangumi'" class="md-library-filter-panel">
        <div class="md-library-filter-panel-head">
          <div>
            <span class="md-kicker">EXPLORE FILTERS</span>
            <strong>{{ filterPanelTitle }}</strong>
          </div>
          <button class="md-library-filter-reset" :disabled="!filterActive" @click="resetLibraryFilters">
            重置筛选
          </button>
        </div>

        <!-- TMDB 源 -->
        <template v-if="librarySource === 'tmdb'">
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">类别</span>
            <div class="md-library-filter-options">
              <button
                v-for="mt in (['movie', 'tv'] as const)"
                :key="mt"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreMediaType === mt }"
                @click="exploreMediaType = mt"
              >
                {{ mt === 'movie' ? '电影' : '剧集' }}
              </button>
            </div>
          </div>
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">类型</span>
            <div class="md-library-filter-options">
              <button
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreGenre === '' }"
                @click="exploreGenre = ''; page = 1; load()"
              >
                全部
              </button>
              <button
                v-for="g in genreList"
                :key="g"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreGenre === g }"
                @click="exploreGenre = g; page = 1; load()"
              >
                {{ g }}
              </button>
            </div>
          </div>
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">地区</span>
            <div class="md-library-filter-options">
              <button
                v-for="r in regionOptions"
                :key="r.value"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreRegion === r.value }"
                @click="exploreRegion = r.value; page = 1; load()"
              >
                {{ r.label }}
              </button>
            </div>
          </div>
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">年份</span>
            <div class="md-library-filter-options">
              <button
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreYear === '' }"
                @click="exploreYear = ''; page = 1; load()"
              >
                全部
              </button>
              <button
                v-for="y in yearOptions.slice(1)"
                :key="y"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreYear === y }"
                @click="exploreYear = y; page = 1; load()"
              >
                {{ y }}
              </button>
            </div>
          </div>
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">排序</span>
            <div class="md-library-filter-options">
              <button
                v-for="opt in sortOptions"
                :key="opt.value"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreSort === opt.value }"
                @click="exploreSort = opt.value; page = 1; load()"
              >
                {{ opt.label }}
              </button>
            </div>
          </div>
        </template>

        <!-- 豆瓣源（目录流：分类 + 排序） -->
        <template v-else-if="librarySource === 'douban'">
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">类别</span>
            <div class="md-library-filter-options">
              <button
                v-for="mt in (['movie', 'tv'] as const)"
                :key="mt"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreMediaType === mt }"
                @click="exploreMediaType = mt"
              >
                {{ mt === 'movie' ? '电影' : '剧集' }}
              </button>
            </div>
          </div>
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">分类</span>
            <div class="md-library-filter-options">
              <button
                v-for="tag in meta.douban_category[exploreMediaType] || ['热门']"
                :key="tag"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreDoubanTag === tag }"
                @click="exploreDoubanTag = tag; page = 1; load()"
              >
                {{ tag }}
              </button>
            </div>
          </div>
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">排序</span>
            <div class="md-library-filter-options">
              <button
                v-for="opt in meta.douban_sort"
                :key="opt.key"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreDoubanSort === opt.key }"
                @click="exploreDoubanSort = opt.key; page = 1; load()"
              >
                {{ opt.label }}
              </button>
            </div>
          </div>
        </template>

        <!-- 动漫源（AniList / Bangumi 目录流） -->
        <template v-else>
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">类型</span>
            <div class="md-library-filter-options">
              <button
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreAnimeGenre === '' }"
                @click="exploreAnimeGenre = ''; page = 1; load()"
              >
                全部
              </button>
              <button
                v-for="g in meta.anime_genres"
                :key="g"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreAnimeGenre === g }"
                @click="exploreAnimeGenre = g; page = 1; load()"
              >
                {{ g }}
              </button>
            </div>
          </div>
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">地区</span>
            <div class="md-library-filter-options">
              <button
                v-for="r in meta.anime_regions"
                :key="r.key"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreAnimeRegion === r.key }"
                @click="exploreAnimeRegion = r.key; page = 1; load()"
              >
                {{ r.label }}
              </button>
            </div>
          </div>
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">年份</span>
            <div class="md-library-filter-options">
              <button
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreAnimeYear === '' }"
                @click="exploreAnimeYear = ''; page = 1; load()"
              >
                全部
              </button>
              <button
                v-for="y in yearOptions.slice(1)"
                :key="y"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreAnimeYear === y }"
                @click="exploreAnimeYear = y; page = 1; load()"
              >
                {{ y }}
              </button>
            </div>
          </div>
          <div class="md-library-filter-row">
            <span class="md-library-filter-label">排序</span>
            <div class="md-library-filter-options">
              <button
                v-for="opt in meta.anime_sort"
                :key="opt.key"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreAnimeSort === opt.key }"
                @click="exploreAnimeSort = opt.key; page = 1; load()"
              >
                {{ opt.label }}
              </button>
            </div>
          </div>
        </template>
      </div>

      <div v-if="errorMessage" class="md-error-tip">{{ errorMessage }}</div>

      <!-- 热门演员网格（人物卡：肖像 + 热度 + 代表作） -->
      <template v-else-if="isActorsSource">
        <div v-loading="loading" class="md-section-block">
          <div ref="gridEl" class="md-library-grid md-library-display-grid" :style="gridStyle">
            <article
              v-for="item in items"
              :key="item.entity_key || item.tmdb_id"
              class="md-library-tile md-actor-tile"
              @click="openActorProfile(item)"
            >
              <div class="md-library-tile-poster">
                <img v-if="posterUrl(item)" :src="posterUrl(item)" loading="lazy" alt="" />
                <div v-else class="md-library-tile-placeholder">🎭</div>
                <span class="md-library-tile-kind">人物</span>
                <span v-if="actorPopularity(item)" class="md-library-tile-score">热度 {{ actorPopularity(item) }}</span>
              </div>
              <div class="md-library-tile-copy">
                <strong :title="item.title">{{ item.title }}</strong>
                <small class="md-actor-dept">{{ actorDepartment(item) }}</small>
                <small v-if="actorKnownFor(item)" class="md-actor-known" :title="actorKnownFor(item)">代表作 · {{ actorKnownFor(item) }}</small>
                <small v-else class="md-actor-known">点击查看演员档案与作品列表</small>
                <small class="md-actor-link">查看作品 →</small>
              </div>
            </article>
          </div>
          <div v-if="loading && !items.length" class="md-skeleton-grid" :style="gridStyle">
            <div v-for="n in 12" :key="n" class="md-skeleton-tile"></div>
          </div>
          <div v-if="!loading && !items.length && !errorMessage" class="md-state">🎭 暂无热门演员</div>
          <div v-if="items.length" class="md-pagination">
            <button class="md-btn" :disabled="page <= 1" @click="page--; load()">‹ 上一页</button>
            <span class="md-page-indicator">第 {{ page }} 页</span>
            <button class="md-btn" :disabled="!hasNextPage" @click="page++; load()">下一页 ›</button>
          </div>
        </div>
      </template>

      <!-- 收藏网格 -->
      <template v-else-if="librarySource === 'favorites'">
        <div class="md-section-block">
          <div class="md-library-grid md-library-display-grid" :style="gridStyle">
            <article v-for="fav in favoriteItems" :key="fav.id" class="md-library-tile">
              <div class="md-library-tile-poster" @click="openDetailWithResources(fav as any)">
                <img v-if="fav.poster" :src="fav.poster" loading="lazy" alt="" />
                <div v-else class="md-library-tile-placeholder">◉</div>
                <span class="md-library-tile-kind">{{ kindOf(fav as any) }}</span>
                <span v-if="formatVote(fav.vote_avg)" class="md-library-tile-score">★ {{ formatVote(fav.vote_avg) }}</span>
                <button class="md-tile-action md-tile-fav active" title="删除收藏" @click.stop="removeFavorite(fav)">
                  <svg viewBox="0 0 24 24" width="13" height="13"><path d="M6 7h12l-1 13H7L6 7zm3-3h6l1 2h4v2H4V6h4l1-2z" /></svg>
                </button>
              </div>
              <div class="md-library-tile-copy">
                <strong :title="fav.title">{{ fav.title }}</strong>
                <small>{{ fav.source }}{{ fav.year ? ' · ' + fav.year : '' }}</small>
              </div>
            </article>
          </div>
          <div v-if="!favoriteItems.length" class="md-state">⭐ 暂无收藏，去影视探索点星标收藏吧</div>
        </div>
      </template>

      <!-- 片库 / 搜索结果网格 -->
      <template v-else>
        <!-- 目录源整页等待（对齐参考实现：正在后台准备 + 自动重试） -->
        <div v-if="catalogPendingWhole && !loading" class="md-catalog-pending">
          <span class="md-catalog-pending-icon">⏳</span>
          <strong>{{ librarySource === 'douban' ? '豆瓣目录正在后台准备' : '动漫目录正在后台准备' }}</strong>
          <p>{{ librarySource === 'douban' ? '正在补齐当前展示页所需的后续条目，页面会自动重试加载。' : '正在后台拉取目录并匹配 TMDB，页面会自动重试加载。' }}</p>
        </div>
        <!-- 目录源缓存提示条 -->
        <div v-if="catalogCacheNotice" class="md-library-cache-notice">{{ catalogCacheNotice }}</div>
        <div v-loading="loading" class="md-section-block">
          <div ref="gridEl" class="md-library-grid md-library-display-grid" :style="gridStyle">
            <article v-for="item in items" :key="(item.entity_key || '') + item.source + item.tmdb_id + item.douban_id + item.title" class="md-library-tile">
              <div class="md-library-tile-poster" @click="openDetailWithResources(item)">
                <img v-if="posterUrl(item)" :src="posterUrl(item)" loading="lazy" alt="" />
                <div v-else class="md-library-tile-placeholder">◉</div>
                <span v-if="!searchMode && item.rank" class="md-library-tile-rank">{{ item.rank }}</span>
                <span v-else class="md-library-tile-kind">{{ kindOf(item) }}</span>
                <span
                  v-if="embyBadgeOf(item)"
                  class="md-emby-chip"
                  :class="embyBadgeClass(embyBadgeOf(item))"
                  :title="embyBadgeOf(item).message || embyBadgeOf(item).display_label"
                >{{ embyBadgeOf(item).display_label }}</span>
                <span v-if="formatVote(item.vote_avg)" class="md-library-tile-score">★ {{ formatVote(item.vote_avg) }}</span>
                <button
                  v-if="item.tmdb_id"
                  class="md-tile-action"
                  :class="{ active: isSubscribed(item), busy: subscribingIds[item.tmdb_id] }"
                  :title="isSubscribed(item) ? '已订阅' : '订阅 MoviePilot'"
                  @click.stop="toggleSubscribe(item)"
                >
                  <svg viewBox="0 0 24 24" width="15" height="15"><path d="M12 21.35l-1.45-1.32C5.4 15.36 2 12.28 2 8.5 2 5.42 4.42 3 7.5 3c1.74 0 3.41.81 4.5 2.09C13.09 3.81 14.76 3 16.5 3 19.58 3 22 5.42 22 8.5c0 3.78-3.4 6.86-8.55 11.54L12 21.35z" /></svg>
                </button>
                <button
                  v-if="item.entity_key"
                  class="md-tile-action md-tile-fav"
                  :class="{ active: isFav(item), busy: favBusyKeys[item.entity_key] }"
                  :title="isFav(item) ? '取消收藏' : '收藏'"
                  @click.stop="toggleFavorite(item)"
                >
                  <svg viewBox="0 0 24 24" width="14" height="14"><path d="M12 2l2.4 4.9 5.4.8-3.9 3.8.9 5.4L12 14.4l-4.8 2.5.9-5.4L4.2 7.7l5.4-.8z" /></svg>
                </button>
              </div>
              <div class="md-library-tile-copy">
                <strong :title="item.title">{{ searchMode ? item.title : (item.rank ? item.rank + '. ' : '') + item.title }}</strong>
                <small v-if="item.year || item.release_date">
                  {{ item.year || (item.release_date || '').slice(0, 10) }}
                  <template v-if="item.original_title && item.original_title !== item.title"> · {{ item.original_title }}</template>
                </small>
                <small v-else-if="item.providers && item.providers.length">{{ item.providers.join(' / ') }}</small>
              </div>
            </article>
          </div>

          <!-- 加载骨架 -->
          <div v-if="loading && !items.length" class="md-skeleton-grid" :style="gridStyle">
            <div v-for="n in 12" :key="n" class="md-skeleton-tile"></div>
          </div>
          <div v-if="!loading && !items.length && !errorMessage" class="md-state">
            {{ searchMode ? '🔍 无搜索结果' : '🎬 暂无数据，试试调整筛选' }}
          </div>

          <div v-if="items.length && !infiniteMode" class="md-pagination">
            <button class="md-btn" :disabled="page <= 1" @click="page--; load()">‹ 上一页</button>
            <span class="md-page-indicator">第 {{ page }} 页</span>
            <button class="md-btn" :disabled="!hasNextPage" @click="page++; load()">下一页 ›</button>
          </div>
          <button
            v-if="items.length && infiniteMode"
            ref="sentinelEl"
            class="md-library-infinite-scroll"
            :class="{ 'is-loading': loading }"
            @click="hasNextPage ? (page++, load(false, true)) : undefined"
          >
            {{ loading ? '正在加载更多…' : hasNextPage ? '继续下滑加载更多' : '已加载全部内容' }}
          </button>
        </div>
      </template>
    </section>

    <!-- ================= 榜单推荐 ================= -->
    <section v-show="activeSection === 'rankings'" class="md-section">
      <div class="md-ranking-hero">
        <div class="md-hero-aurora"></div>
        <div class="md-ranking-hero-main">
          <div class="md-ranking-kicker">
            <span class="md-ranking-brand-mark">{{ rankingProviderMark }}</span>
            RANKINGS · 榜单推荐
          </div>
          <h2>流媒体榜单</h2>
          <p>RE0流媒体榜聚合 Netflix、Disney+、Prime Video 等平台 Top 10，也支持 TMDB 分类榜与豆瓣片单。</p>
        </div>
        <div class="md-ranking-hero-side">
          <strong>{{ rankingTotal }}</strong>
          <span>部上榜作品</span>
        </div>
      </div>

      <div class="md-ranking-control-panel">
        <div class="md-panel-head">
          <div>
            <span class="md-kicker">STREAMING PLATFORMS</span>
            <strong>选择平台 / 流媒体榜单</strong>
          </div>
          <button class="md-btn is-soft" @click="loadRankings(true)">↻ 刷新数据</button>
        </div>
        <div class="md-streaming-tabs">
          <button
            v-for="p in rankingProviderList"
            :key="p.key"
            class="md-streaming-tab"
            :class="{ 'is-active': rankingProvider === p.key }"
            :title="p.label"
            @click="selectRankingProvider(p.key)"
          >
            <span class="md-streaming-tab-icon" :class="p.key === 'maoyan' ? 'brand-maoyan' : p.key === 'hdhive' ? 'hive' : 'brand-' + p.key.replace('hdhive:', '')">{{ p.mark }}</span>
            <span class="md-streaming-tab-label">{{ p.label }}</span>
          </button>
        </div>
        <div class="md-ranking-filter-row">
          <div v-if="isMaoyanProvider" class="md-ranking-type-tabs">
            <button
              v-for="c in [{ key: 'all', label: '全部榜单' }, ...(meta.maoyan_category || [])]"
              :key="c.key"
              class="md-ranking-type"
              :class="{ 'is-active': maoyanCategory === c.key }"
              @click="selectMaoyanCategory(c.key)"
            >
              {{ c.label }}
            </button>
          </div>
          <div v-else class="md-ranking-type-tabs">
            <button
              v-for="t in rankingTypeTabs"
              :key="t.value"
              class="md-ranking-type"
              :class="{ 'is-active': rankingMediaType === t.value }"
              @click="rankingMediaType = t.value as '' | 'movie' | 'tv'; loadRankings()"
            >
              {{ t.label }}
            </button>
          </div>
          <label v-if="rankingProvider.startsWith('hdhive')" class="md-ranking-country">
            地区
            <select v-model="rankingRegion" class="md-select" @change="loadRankings()">
              <option v-for="r in meta.regions" :key="r.key" :value="r.key">{{ r.label }}</option>
            </select>
          </label>
          <label v-if="!isMaoyanProvider" class="md-ranking-country">
            扩展榜单
            <select
              v-model="rankingProvider"
              class="md-select"
              @change="rankingMediaType = rankingProvider.startsWith('hdhive') ? '' : 'movie'; loadRankings()"
            >
              <optgroup label="RE0流媒体榜">
                <option value="hdhive">默认平台（按设置）</option>
              </optgroup>
              <optgroup label="TMDB 分类榜">
                <option v-for="opt in tmdbRankingOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
              </optgroup>
              <optgroup label="豆瓣片单">
                <option v-for="col in meta.collections" :key="col.key" :value="'douban:' + col.key">{{ col.label }}</option>
              </optgroup>
            </select>
          </label>
        </div>
      </div>

      <div v-if="errorMessage && !rankingGroups.length" class="md-error-tip">{{ errorMessage }}</div>

      <div v-loading="loading" class="md-ranking-results-shell">
        <div class="md-calendar-head">
          <div>
            <span class="md-kicker">{{ isMaoyanProvider ? '猫眼 · ' + maoyanCategoryLabel : rankingProvider.startsWith('hdhive') ? 'RE0 · ' + rankingProviderLabel : rankingProviderLabel }}</span>
            <h3>{{ isMaoyanProvider ? '猫眼全国热度榜' : rankingProviderLabel }}</h3>
          </div>
          <span class="md-head-note">{{ isMaoyanProvider ? maoyanCategoryLabel + ' · 中国' : (rankingMediaType === '' ? '电影 + 剧集' : rankingKindLabel(rankingMediaType)) + ' · ' + rankingRegion + ' · Top ' + Math.max(rankingTotal, 1) }}</span>
        </div>

        <div v-if="isMaoyanProvider && errorMessage" class="md-error-tip">{{ errorMessage }}</div>

        <div v-if="isMaoyanProvider && !loading && !maoyanGroups.length" class="md-state">🐱 正在后台抓取猫眼榜单并匹配 TMDB，完成后将自动显示。</div>

        <template v-if="isMaoyanProvider">
          <div v-for="group in maoyanGroups" :key="group.category || group.kind" class="md-ranking-group">
            <div class="md-ranking-group-head">
              <span>{{ group.kind }}</span>
              <h4>{{ group.kind }} Top {{ group.items.length }}</h4>
              <small>猫眼 · 中国 · {{ group.items.length }} 部</small>
            </div>
            <div class="md-ranking-grid" :style="{ '--md-ranking-columns': group.items.length, '--md-ranking-row-max-width': 'none' }">
              <article v-for="(item, idx) in group.items" :key="group.kind + idx + item.title" class="md-ranking-tile">
                <div class="md-ranking-tile-poster" @click="openDetailWithResources(item)">
                  <img v-if="posterUrl(item)" :src="posterUrl(item)" loading="lazy" alt="" />
                  <div v-else class="md-library-tile-placeholder">◉</div>
                  <span class="md-rank">{{ idx + 1 }}</span>
                  <span v-if="formatVote(item.vote_avg)" class="md-score">● ★ {{ formatVote(item.vote_avg) }}</span>
                </div>
                <div class="md-ranking-tile-copy">
                  <strong :title="item.title">{{ item.title }}</strong>
                  <small>{{ item.year || '—' }} · {{ kindOf(item) }}</small>
                </div>
              </article>
            </div>
          </div>
        </template>

        <template v-else>
        <div v-for="group in rankingGroups" :key="group.kind" class="md-ranking-group">
          <div class="md-ranking-group-head">
            <span>{{ group.kind }}</span>
            <h4>{{ group.kind }} Top {{ group.items.length }}</h4>
            <small>{{ rankingProviderLabel }} · {{ rankingRegion }} · {{ group.items.length }} 部</small>
          </div>
          <div
            class="md-ranking-grid"
            :style="{ '--md-ranking-columns': group.items.length, '--md-ranking-row-max-width': 'none' }"
          >
            <article v-for="(item, idx) in group.items" :key="group.kind + idx + item.title" class="md-ranking-tile">
              <div class="md-ranking-tile-poster" @click="openDetailWithResources(item)">
                <img v-if="posterUrl(item)" :src="posterUrl(item)" loading="lazy" alt="" />
                <div v-else class="md-library-tile-placeholder">◉</div>
                <span class="md-rank">{{ idx + 1 }}</span>
                <span v-if="formatVote(item.vote_avg)" class="md-score">● ★ {{ formatVote(item.vote_avg) }}</span>
              </div>
              <div class="md-ranking-tile-copy">
                <strong :title="item.title">{{ item.title }}</strong>
                <small>{{ item.year || (item.release_date || '').slice(0, 10) }} · {{ kindOf(item) }}</small>
              </div>
            </article>
          </div>
        </div>
        </template>

        <div v-if="!isMaoyanProvider && !loading && !rankingGroups.length && !errorMessage" class="md-state">🏆 暂无榜单数据</div>
      </div>
    </section>

    <!-- ================= 追剧日历 ================= -->
    <section v-show="activeSection === 'calendar'" class="md-section">
      <div v-if="spotlightItem" class="md-calendar-spotlight">
        <div class="md-calendar-spotlight-backdrop" :style="spotlightItem.poster ? { backgroundImage: `url(${spotlightItem.poster})` } : {}"></div>
        <div class="md-calendar-spotlight-main">
          <span class="md-calendar-spotlight-kicker">RE0 · 未来播出</span>
          <time v-if="airTimeOf(spotlightItem) || spotlightItem.air_date">{{ spotlightItem.air_date ? spotlightItem.air_date.replace('T', ' ').slice(0, 16) : '' }}</time>
          <h2>{{ spotlightItem.title }}</h2>
          <p>{{ spotlightItem.overview || (spotlightItem.episode_title ? spotlightItem.episode_title : spotlightItem.title) }}</p>
          <div class="md-calendar-feature-meta">
            <span v-if="epLabelOf(spotlightItem)">{{ epLabelOf(spotlightItem) }}</span>
            <span v-if="kindOf(spotlightItem)">{{ kindOf(spotlightItem) }}</span>
            <span v-if="formatVote(spotlightItem.vote_avg)">★ {{ formatVote(spotlightItem.vote_avg) }}</span>
            <span v-if="spotlightItem.air_date">{{ spotlightItem.air_date.slice(0, 10) }}</span>
          </div>
        </div>
        <div class="md-calendar-spotlight-stat">
          <strong>{{ calendarDaysList.reduce((n, d) => n + d.items.length, 0) }}</strong>
          <span>条播出安排</span>
        </div>
      </div>

      <div class="md-calendar-shell">
        <div class="md-calendar-head">
          <div>
            <span class="md-kicker">WATCH SCHEDULE</span>
            <h3>追剧日历</h3>
          </div>
          <div class="md-calendar-head-controls">
            <div class="md-calendar-kind-tabs">
              <button
                v-for="opt in calendarKindOptions"
                :key="opt.value"
                class="md-calendar-kind"
                :class="{ 'is-active': calendarKind === opt.value }"
                @click="calendarKind = opt.value"
              >
                {{ opt.label }}
              </button>
            </div>
            <div class="md-calendar-day-presets">
              <button
                v-for="d in calendarDayPresets"
                :key="d"
                class="md-calendar-kind"
                :class="{ 'is-active': calendarDays === d }"
                @click="calendarDays = d"
              >
                {{ d }} 天
              </button>
            </div>
            <button class="md-btn is-soft" @click="loadCalendar(true)">↻ 刷新数据</button>
          </div>
        </div>

        <div class="md-calendar-date-rail-wrap">
          <button class="md-calendar-rail-arrow" @click="selectedDayIndex = Math.max(0, selectedDayIndex - 1)">‹</button>
          <div class="md-calendar-date-rail">
            <button
              v-for="(day, idx) in calendarDaysList"
              :key="day.date"
              class="md-calendar-date"
              :class="{ 'is-active': idx === selectedDayIndex }"
              @click="selectedDayIndex = idx"
            >
              <span>{{ day.label }}</span>
              <strong>{{ Number(day.date.slice(8, 10)) }}</strong>
              <em>{{ Number(day.date.slice(5, 7)) }}月</em>
              <i :class="{ empty: !day.items.length }">{{ day.items.length }}</i>
            </button>
          </div>
          <button
            class="md-calendar-rail-arrow"
            :disabled="selectedDayIndex >= calendarDaysList.length - 1"
            @click="selectedDayIndex = Math.min(calendarDaysList.length - 1, selectedDayIndex + 1)"
          >
            ›
          </button>
        </div>

        <div v-if="errorMessage" class="md-error-tip">{{ errorMessage }}</div>

        <div v-loading="loading" class="md-calendar-day-block" v-if="selectedDay">
          <div class="md-calendar-day-head">
            <span>{{ selectedDay.label }}</span>
            <h4>{{ selectedDay.date.slice(0, 10).replace('-', '年').replace('-', '月') }}日</h4>
            <p>{{ selectedDay.items.length }} 条播出安排</p>
          </div>
          <div class="md-calendar-card-grid">
            <article v-for="ep in selectedDay.items" :key="selectedDay.date + (ep.entity_key || '') + ep.episode_title" class="md-calendar-card">
              <div class="md-calendar-card-poster" @click="openDetailWithResources(ep)">
                <img v-if="posterUrl(ep)" :src="posterUrl(ep)" loading="lazy" alt="" />
                <div v-else class="md-library-tile-placeholder">◉</div>
                <span class="md-library-tile-kind">{{ kindOf(ep) }}</span>
                <span v-if="formatVote(ep.vote_avg)" class="md-library-tile-score">★ {{ formatVote(ep.vote_avg) }}</span>
                <span v-if="epLabelOf(ep) || airTimeOf(ep)" class="md-calendar-card-schedule">
                  <b v-if="epLabelOf(ep)">{{ epLabelOf(ep) }}</b>
                  <time v-if="airTimeOf(ep)">{{ airTimeOf(ep) }}</time>
                </span>
              </div>
              <div class="md-calendar-card-copy">
                <strong :title="ep.title">{{ ep.title }}</strong>
                <small v-if="ep.episode_title" :title="ep.episode_title">{{ ep.episode_title }}</small>
                <span v-else-if="ep.genres && ep.genres.length">{{ ep.genres.join(' / ') }}</span>
                <span class="md-calendar-card-meta">
                  {{ ep.year || (ep.release_date || '').slice(0, 10) }} · {{ kindOf(ep) }}
                </span>
              </div>
            </article>
          </div>
          <div v-if="!loading && !selectedDay.items.length" class="md-calendar-empty-day">📅 当日暂无播出安排</div>
        </div>
        <div v-else-if="!loading && !calendarDaysList.length && !errorMessage" class="md-state">🗓️ 暂无播出安排</div>
      </div>
    </section>

    <!-- ================= 基础配置 ================= -->
    <section v-show="activeSection === 'tasks'" class="md-section">
      <div class="md-hero md-hero-teal">
        <div class="md-hero-aurora"></div>
        <div class="md-hero-eyebrow">⚙️ SETTINGS · 基础配置</div>
        <h2>发现配置</h2>
        <p>配置影视探索默认来源、追剧日历与流媒体榜单偏好，保存后即时生效。</p>
      </div>

      <form class="md-basic-config-form" @submit.prevent="saveSettings">
        <div class="md-basic-config-savebar">
          <span class="md-basic-config-savebar-title">基础配置</span>
          <div class="md-basic-config-savebar-actions">
            <span v-if="savingSettings" class="md-basic-config-spinner">保存中…</span>
            <button type="submit" class="md-btn is-primary" :disabled="savingSettings">保存基础配置</button>
          </div>
        </div>

        <div class="md-basic-config-grid">
          <div class="md-panel">
            <div class="md-panel-head">
              <div>
                <span class="md-kicker">EXPLORE</span>
                <h3>影视探索</h3>
                <p>探索页默认来源与排序方式</p>
              </div>
            </div>
            <div class="md-panel-body">
              <div class="md-field">
                <span class="md-field-label">默认探索来源</span>
                <div class="md-field-control md-field-chips">
                  <button
                    v-for="s in ([{ v: 'tmdb', l: 'TMDB' }, { v: 'douban', l: '豆瓣' }, { v: 'anime', l: '番剧' }] as const)"
                    :key="s.v"
                    class="md-library-filter-chip"
                    :class="{ 'is-active': settingsForm.default_explore_source === s.v }"
                    @click="settingsForm.default_explore_source = s.v"
                  >
                    {{ s.l }}
                  </button>
                </div>
              </div>
              <div class="md-field">
                <span class="md-field-label">默认排序</span>
                <div class="md-field-control md-field-chips">
                  <button
                    v-for="opt in sortOptions"
                    :key="opt.value"
                    class="md-library-filter-chip"
                    :class="{ 'is-active': settingsForm.default_explore_sort === opt.value }"
                    @click="settingsForm.default_explore_sort = opt.value"
                  >
                    {{ opt.label }}
                  </button>
                </div>
              </div>
            </div>
          </div>

          <div class="md-panel">
            <div class="md-panel-head">
              <div>
                <span class="md-kicker">CALENDAR</span>
                <h3>追剧日历</h3>
                <p>日历默认天数与类型</p>
              </div>
            </div>
            <div class="md-panel-body">
              <div class="md-field">
                <span class="md-field-label">日历天数</span>
                <div class="md-field-control">
                  <input v-model.number="settingsForm.calendar_days" type="number" min="1" max="60" class="md-input" style="width: 90px" />
                </div>
              </div>
              <div class="md-field">
                <span class="md-field-label">日历类型</span>
                <div class="md-field-control">
                  <select v-model="settingsForm.calendar_kind" class="md-select">
                    <option v-for="opt in calendarKindOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
                  </select>
                </div>
              </div>
            </div>
          </div>

          <div class="md-panel">
            <div class="md-panel-head">
              <div>
                <span class="md-kicker">RANKINGS</span>
                <h3>流媒体榜单</h3>
                <p>榜单地区、平台与媒体类型默认值</p>
              </div>
            </div>
            <div class="md-panel-body">
              <div class="md-field">
                <span class="md-field-label">榜单地区</span>
                <div class="md-field-control">
                  <select v-model="settingsForm.ranking_region" class="md-select">
                    <option v-for="r in meta.regions" :key="r.key" :value="r.key">{{ r.label }}</option>
                  </select>
                </div>
              </div>
              <div class="md-field">
                <span class="md-field-label">流媒体平台</span>
                <div class="md-field-control">
                  <select v-model="settingsForm.ranking_provider" class="md-select">
                    <option v-for="p in meta.providers" :key="p.key" :value="p.key">{{ p.label }}</option>
                  </select>
                </div>
              </div>
              <div class="md-field">
                <span class="md-field-label">榜单媒体类型</span>
                <div class="md-field-control md-field-chips">
                  <button
                    v-for="mt in ([{ v: 'movie', l: '电影' }, { v: 'tv', l: '剧集' }] as const)"
                    :key="mt.v"
                    class="md-library-filter-chip"
                    :class="{ 'is-active': settingsForm.ranking_media_type === mt.v }"
                    @click="settingsForm.ranking_media_type = mt.v"
                  >
                    {{ mt.l }}
                  </button>
                </div>
              </div>
            </div>
          </div>

          <div class="md-panel">
            <div class="md-panel-head">
              <div>
                <span class="md-kicker">ADVANCED</span>
                <h3>高级</h3>
                <p>缓存时长与辅助选项</p>
              </div>
            </div>
            <div class="md-panel-body">
              <div class="md-field">
                <span class="md-field-label">缓存时长（分钟）</span>
                <div class="md-field-control">
                  <input v-model.number="settingsForm.cache_ttl_minutes" type="number" min="1" max="1440" class="md-input" style="width: 90px" />
                </div>
              </div>
              <div class="md-field md-field-row">
                <span class="md-field-label">豆瓣条目匹配 TMDB</span>
                <div class="md-field-control">
                  <label class="md-switch">
                    <input v-model="settingsForm.match_douban_tmdb" type="checkbox" />
                    <span class="md-switch-track"></span>
                  </label>
                </div>
              </div>
              <div class="md-field md-field-row">
                <span class="md-field-label">探索页 Emby 入库检测</span>
                <div class="md-field-control">
                  <label class="md-switch">
                    <input v-model="settingsForm.emby_check_enabled" type="checkbox" />
                    <span class="md-switch-track"></span>
                  </label>
                </div>
              </div>
              <div class="md-field md-field-row">
                <span class="md-field-label">观影资源源</span>
                <div class="md-field-control">
                  <label class="md-switch">
                    <input v-model="settingsForm.guanying_enabled" type="checkbox" />
                    <span class="md-switch-track"></span>
                  </label>
                </div>
              </div>
              <div class="md-guanying-auth">
                <div class="md-guanying-auth-status">
                  <template v-if="guanyingStatus.session_saved">
                    已登录（账号 {{ guanyingStatus.account_hint || '—' }}）
                  </template>
                  <template v-else>未登录。登录后可在影视详情中检索观影的 115、123、光鸭与磁力资源；凭据在本机加密保存，会话失效可一键恢复。</template>
                </div>
                <div class="md-guanying-auth-actions">
                  <input v-model="guanyingUsername" class="md-input" placeholder="观影账号" autocomplete="username" />
                  <input v-model="guanyingPassword" class="md-input" type="password" placeholder="观影密码" autocomplete="current-password" />
                  <button type="button" class="md-btn is-primary" :disabled="guanyingLogging" @click="guanyingLogin">登录</button>
                  <button v-if="guanyingStatus.credentials_saved" type="button" class="md-btn" :disabled="guanyingLogging" @click="guanyingRelogin">恢复会话</button>
                  <button v-if="guanyingStatus.session_saved" type="button" class="md-btn" :disabled="guanyingLogging" @click="clearGuanyingSession">清除</button>
                </div>
                <div v-if="guanyingCaptchaState" class="md-guanying-captcha">
                  <p class="md-guanying-captcha-hint">按顺序点击文字：{{ guanyingCaptchaState.text }}（已点 {{ guanyingCaptchaPoints.length }} 个）</p>
                  <div class="md-guanying-captcha-box" :style="{ width: (guanyingCaptchaState.width || 350) + 'px', height: (guanyingCaptchaState.height || 200) + 'px' }" @click="onCaptchaClick">
                    <img v-if="guanyingCaptchaImg" :src="guanyingCaptchaImg" alt="观影验证码" />
                    <span v-for="(p, idx) in guanyingCaptchaPoints" :key="idx" class="md-guanying-captcha-point" :style="{ left: p.x - 9 + 'px', top: p.y - 9 + 'px' }">{{ idx + 1 }}</span>
                  </div>
                  <div class="md-guanying-captcha-actions">
                    <button type="button" class="md-btn is-primary" :disabled="guanyingLogging" @click="verifyGuanyingCaptcha">确认</button>
                    <button type="button" class="md-btn" :disabled="guanyingLogging" @click="undoCaptchaPoint">撤销一点</button>
                  </div>
                </div>
              </div>
              <div class="md-guanying-auth">
                <div class="md-guanying-auth-status">频道白名单可视化筛选（规则卡片与正则双向同步，TMDB 海报补全）</div>
                <div class="md-guanying-auth-actions">
                  <select v-model="vfScene" class="md-input" style="width: 200px">
                    <option v-for="(label, scene) in vfSceneLabels" :key="scene" :value="scene">{{ label }}</option>
                  </select>
                  <button type="button" class="md-btn" @click="vfAddRule">加规则</button>
                  <button type="button" class="md-btn is-primary" :loading="vfSaving" :disabled="vfSaving" @click="vfSave">保存白名单</button>
                </div>
                <div class="vf-rules">
                  <div v-for="(rule, idx) in vfRules" :key="rule.id || idx" class="vf-rule-card" :class="{ 'is-disabled': !rule.enabled }">
                    <img v-if="rule.poster_url" :src="rule.poster_url" class="vf-rule-poster" />
                    <div class="vf-rule-main">
                      <input v-model="rule.media_name" class="md-input" placeholder="关键词/片名" @change="vfLookupPoster(rule)" />
                      <div class="vf-rule-meta">
                        <select v-model="rule.media_type" class="md-input vf-type" @change="vfLookupPoster(rule)">
                          <option value="">通用</option>
                          <option value="movie">电影</option>
                          <option value="tv">剧集</option>
                        </select>
                        <label class="vf-enabled"><input v-model="rule.enabled" type="checkbox" />启用</label>
                        <span v-if="rule.title" class="vf-title">{{ rule.title }}</span>
                      </div>
                    </div>
                    <button type="button" class="md-btn" @click="vfRules.splice(idx, 1)">删除</button>
                  </div>
                  <p v-if="!vfRules.length" class="vf-empty">暂无规则；「加规则」逐条添加关键词，保存后以 | 连接写回白名单正则。</p>
                </div>
              </div>
            </div>

            <!-- 资源检索频道（对齐 tgto123 PUBLIC CHANNELS） -->
            <div class="md-panel" :class="{ 'is-dirty': dirtyGroups.channels }">
              <div class="md-panel-head">
                <div>
                  <span class="md-kicker">PUBLIC CHANNELS</span>
                  <h3>资源检索频道</h3>
                  <p>按网盘类型分别填写公开频道，每行一个。系统直接读取公开频道近期可见消息，无需 TG API 或登录。</p>
                </div>
                <span v-if="dirtyGroups.channels" class="md-dirty-badge">未保存</span>
              </div>
              <div class="md-panel-body">
                <p class="md-field-hint">公开频道直查：仅支持 https://t.me/频道名 或 @频道名；不支持私有邀请链接或单条消息链接。</p>
                <div class="md-channel-grid">
                  <div v-for="p in (['123', 'guangya', 'pan139'] as const)" :key="p" class="md-channel-card">
                    <strong>{{ transferProviderNames[p] }} 频道</strong>
                    <textarea
                      :value="channelText(p)"
                      class="md-input md-channel-textarea"
                      rows="5"
                      :placeholder="p === '123' ? '每行一个公开 123 资源频道\n例如：https://t.me/QukanMovie 或 @QukanMovie' : p === 'guangya' ? '每行一个公开光鸭资源频道' : '每行一个公开 139 资源频道'"
                      @input="setChannelText(p, ($event.target as HTMLTextAreaElement).value)"
                    ></textarea>
                    <small>{{ ((settingsForm.tg_resource_channels || {})[p] || []).length }} 个频道</small>
                  </div>
                </div>
              </div>
            </div>

            <!-- 影视发现保存目录（对齐 tgto123 transfer targets） -->
            <div class="md-panel" :class="{ 'is-dirty': dirtyGroups.transferTargets }">
              <div class="md-panel-head">
                <div>
                  <span class="md-kicker">TRANSFER TARGETS</span>
                  <h3>影视发现保存目录</h3>
                  <p>影视发现中的资源将转存到此目录；留空表示未配置，转存按钮将不可用。</p>
                </div>
                <span v-if="dirtyGroups.transferTargets" class="md-dirty-badge">未保存</span>
              </div>
              <div class="md-panel-body">
                <div class="md-channel-grid">
                  <div v-for="p in (['123', 'guangya', 'pan139'] as const)" :key="p" class="md-channel-card">
                    <strong>{{ transferProviderNames[p] }} 保存目录</strong>
                    <input
                      :value="targetPath(p)"
                      class="md-input"
                      placeholder="例如：/媒体库/影视发现（网盘内路径）"
                      @input="setTargetPath(p, ($event.target as HTMLInputElement).value)"
                    />
                    <small>{{ targetPath(p) ? '目录 ' + targetPath(p) : '尚未选择目录' }}</small>
                  </div>
                </div>
              </div>
            </div>

            <!-- Emby 媒体库（对齐 tgto123 media_emby：徽章/缺集扫描数据源） -->
            <div class="md-panel" :class="{ 'is-dirty': dirtyGroups.emby }">
              <div class="md-panel-head">
                <div>
                  <span class="md-kicker">EMBY LIBRARY</span>
                  <h3>Emby 媒体库</h3>
                  <p>开启后在影视卡片显示本地 Emby 媒体库状态（已入库、连载中、缺集或未入库），并作为缺集扫描的数据源。</p>
                </div>
                <span v-if="dirtyGroups.emby" class="md-dirty-badge">未保存</span>
              </div>
              <div class="md-panel-body">
                <div class="md-field md-field-row">
                  <span class="md-field-label">在影视卡片显示本地 Emby 媒体库状态</span>
                  <div class="md-field-control">
                    <label class="md-switch">
                      <input v-model="settingsForm.media_emby.enabled" type="checkbox" @change="markDirty('emby')" />
                      <span class="md-switch-track"></span>
                    </label>
                  </div>
                </div>
                <div class="md-sub-grid">
                  <label>Emby 服务器地址<input v-model="settingsForm.media_emby.server_url" class="md-input" type="url" placeholder="例如：https://emby.example.com" @input="markDirty('emby')" /></label>
                  <label>Emby API Key<input v-model="settingsForm.media_emby.api_key" class="md-input" type="password" placeholder="已安全保存；需要替换时再输入新 Key" @input="markDirty('emby')" /></label>
                </div>
                <div class="md-emby-test-row">
                  <button type="button" class="md-btn" :disabled="embyTestBusy" @click="testMediaEmby">测试连接</button>
                  <span v-if="embyTestMessage" class="md-field-hint">{{ embyTestMessage }}</span>
                  <span v-else class="md-field-hint">API Key 仅保存在本机；留空保存会自动保留已保存的 Key。</span>
                </div>
              </div>
            </div>

            <!-- Emby 缺集扫描（对齐 tgto123 emby-missing） -->
            <div class="md-panel md-missing-panel">
              <div class="md-panel-head">
                <div>
                  <span class="md-kicker">EMBY MISSING</span>
                  <h3>Emby 缺集扫描</h3>
                  <p>选择电视剧媒体库后开始扫描，结果会保留为可追溯快照；带 TMDB ID 的条目可安全创建补档订阅。</p>
                </div>
              </div>
              <div class="md-panel-body">
                <div class="md-missing-status-row">
                  <span class="md-badge" :class="(missingStatus?.emby?.configured) ? 'is-unlocked' : 'is-warn'">
                    {{ missingStatus?.emby?.configured ? 'Emby 缺集扫描已就绪' : (missingStatus?.emby?.message || '请先完成 Emby 配置') }}
                  </span>
                  <button type="button" class="md-btn" :disabled="missingBusy || !missingStatus?.emby?.configured" @click="startMissingScan">开始扫描</button>
                  <button type="button" class="md-btn is-soft" @click="loadMissingStatus(); loadMissingResults(); loadMissingEvents()">↻ 刷新</button>
                </div>

                <div v-if="missingStatus?.active_scan" class="md-missing-progress">
                  <strong>正在扫描 Emby 缺集</strong>
                  <div class="md-missing-progress-bar">
                    <div
                      class="md-missing-progress-fill"
                      :style="{ width: missingProgressPercent + '%' }"
                    ></div>
                  </div>
                  <small>剧集 {{ missingStatus.active_scan.scanned_series || 0 }}/{{ missingStatus.active_scan.total_series || 0 }} · 缺集 {{ missingStatus.active_scan.missing_episodes || 0 }} · 异常 {{ missingStatus.active_scan.error_series || 0 }}</small>
                </div>

                <div v-if="missingLibraries.length" class="md-missing-libraries">
                  <div class="md-missing-libraries-head">
                    <strong>{{ missingLibraries.filter((x) => x.selected).length }} / {{ missingLibraries.length }} 个媒体库已选择</strong>
                    <small>可取消不需要扫描的媒体库；扫描范围仅限电视剧正片集。</small>
                  </div>
                  <div class="md-missing-library-grid">
                    <button
                      v-for="lib in missingLibraries"
                      :key="lib.id"
                      type="button"
                      class="md-chip"
                      :class="{ 'is-active': lib.selected }"
                      @click="lib.selected = !lib.selected"
                    >TV · {{ lib.name }}</button>
                  </div>
                </div>

                <div v-if="missingResults.length" class="md-missing-results">
                  <div class="md-missing-results-head">
                    <strong>缺集列表（共 {{ missingResults.length }} 部剧集、{{ missingResults.reduce((n, r) => n + (r.missing_count || 0), 0) }} 集缺失）</strong>
                    <div class="md-missing-results-actions">
                      <select v-model="missingTargetProvider" class="md-select" style="width: 110px">
                        <option value="123">123</option>
                        <option value="guangya">光鸭</option>
                        <option value="pan139">139</option>
                      </select>
                      <button type="button" class="md-btn is-small is-primary" @click="createMissingSubscriptions">创建补档订阅</button>
                    </div>
                  </div>
                  <div class="md-missing-result-list">
                    <div v-for="result in missingResults" :key="result.id" class="md-missing-result-card">
                      <label class="md-missing-check">
                        <input v-model="missingSelectedResults[result.id]" type="checkbox" :disabled="!result.tmdb_id" />
                      </label>
                      <div class="md-missing-result-main">
                        <strong>{{ result.title }} <small v-if="result.production_year">（{{ result.production_year }}）</small></strong>
                        <div class="md-missing-result-meta">
                          <span class="md-badge">{{ result.library_name || '—' }}</span>
                          <span class="md-badge" :class="{ 'is-unlocked': !!result.tmdb_id }">TMDB {{ result.tmdb_id || '未匹配' }}</span>
                          <span class="md-badge">已入库 {{ result.available_count }} 集</span>
                          <span class="md-badge is-warn">缺失 {{ result.missing_count }} 集</span>
                          <span v-if="result.subscription_id" class="md-badge is-unlocked">补档已启用</span>
                        </div>
                        <div v-if="missingEpisodeChips(result).length" class="md-missing-episodes">
                          <span v-for="chip in missingEpisodeChips(result)" :key="chip" class="md-badge is-spec">{{ chip }}</span>
                        </div>
                        <p v-if="!result.tmdb_id" class="md-field-hint">Emby 未提供 TMDB ID。为避免把同名剧误匹配，此条不会自动创建订阅。</p>
                      </div>
                      <button
                        v-if="result.subscription_id"
                        type="button"
                        class="md-btn is-small"
                        @click="runMissingSubscription(result.subscription_id)"
                      >立即匹配</button>
                    </div>
                  </div>
                </div>
                <p v-else class="md-field-hint">尚未扫描媒体库；扫描完成后这里会展示缺集快照。</p>

                <div v-if="missingEvents.length" class="md-missing-events">
                  <strong>事件流</strong>
                  <div v-for="event in missingEvents.slice(0, 12)" :key="event.id" class="md-missing-event">
                    <span class="md-badge">{{ missingEventLabel(event.event_type) }}</span>
                    <small>{{ event.message }}</small>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </form>
    </section>

    <!-- ================= 作品详情（全页，对齐 tgto123 media-work-detail-section） ================= -->
    <transition name="md-detail-fade">
      <section v-if="detailPage" class="md-work-detail">
        <div class="md-work-detail-nav">
          <button class="md-btn" @click="closeDetail">‹ 返回影视发现</button>
          <span class="md-work-detail-crumb">影视发现 / 作品详情</span>
        </div>

        <div v-if="detailLoading" class="md-state md-detail-loading">正在读取作品资料 · 正在整理海报、简介与关联资源。</div>

        <template v-else-if="detailData">
          <!-- Hero -->
          <div class="md-work-hero" :style="detailData.backdrop ? { backgroundImage: `url(${detailData.backdrop})` } : {}">
            <div class="md-work-hero-mask"></div>
            <div class="md-work-hero-body">
              <div class="md-work-hero-poster">
                <img v-if="detailData.poster" :src="detailData.poster" :alt="detailData.title" />
                <div v-else class="md-library-tile-placeholder">◉</div>
              </div>
              <div class="md-work-hero-main">
                <span class="md-kicker">{{ detailData.entity_type === 'anime' ? 'ANIME PROFILE' : 'TMDB ' + (detailData.media_type === 'tv' ? 'TV' : 'MOVIE') }}</span>
                <h2>
                  {{ detailData.title }}
                  <small v-if="detailData.year">（{{ detailData.year }}）</small>
                </h2>
                <div class="md-work-hero-meta">
                  <span v-if="detailData.vote_avg > 0" class="md-work-score">★ {{ detailData.vote_avg.toFixed(1) }}</span>
                  <span class="md-badge">{{ detailData.provider_label || sourceLabelOf(detailData.source) }}</span>
                  <span v-if="detailData.media_type" class="md-badge">{{ detailData.media_type === 'tv' ? '电视剧' : '电影' }}</span>
                  <span v-if="detailData.number_of_seasons" class="md-badge">{{ detailData.number_of_seasons }} 季</span>
                  <span v-if="detailData.number_of_episodes" class="md-badge">{{ detailData.number_of_episodes }} 集</span>
                  <span v-if="detailData.runtime" class="md-badge">{{ detailData.runtime }} 分钟</span>
                  <span v-if="detailData.release_date" class="md-badge">{{ String(detailData.release_date).slice(0, 10) }}</span>
                </div>
                <p v-if="detailData.tagline" class="md-work-tagline">{{ detailData.tagline }}</p>
                <p class="md-work-overview">{{ detailData.overview || '暂无简介' }}</p>
                <div class="md-work-genres" v-if="(detailData.genres || []).length">
                  <span v-for="g in detailData.genres" :key="g" class="md-badge is-soft">{{ g }}</span>
                </div>
                <div class="md-work-actions">
                  <button
                    class="md-btn"
                    :class="{ 'is-fav-active': detailIsFav }"
                    @click="toggleDetailFavorite"
                  >{{ detailIsFav ? '★ 已收藏' : '☆ 加入收藏' }}</button>
                  <button
                    v-if="detailData.media_type !== 'person'"
                    class="md-btn is-primary"
                    @click="openSubscriptionModal"
                  >{{ isDetailSubscribed ? '编辑订阅' : '创建订阅' }}</button>
                  <span v-if="isDetailSubscribed" class="md-sub-status">
                    已订阅 · {{ detailData.subscription.target_provider === 'guangya' ? '光鸭' : detailData.subscription.target_provider === 'pan139' ? '139' : detailData.subscription.target_provider }} · {{ detailData.subscription.status }}
                  </span>
                  <button v-if="detailData.tmdb_id" class="md-btn is-soft" @click="monitorFromDetail">加入频道监控</button>
                  <a
                    v-if="detailDoubanLink"
                    class="md-btn is-soft"
                    :href="detailDoubanLink"
                    target="_blank"
                    rel="noopener"
                  >查看源站</a>
                </div>
              </div>
            </div>
          </div>

          <div class="md-work-columns">
            <!-- 左侧：作品资料 -->
            <aside class="md-work-aside">
              <div class="md-panel">
                <div class="md-panel-head">
                  <div>
                    <span class="md-kicker">WORK PROFILE</span>
                    <h3>作品资料</h3>
                  </div>
                </div>
                <dl class="md-work-facts">
                  <template v-if="detailData.media_type === 'tv' || detailData.entity_type === 'anime'">
                    <div v-if="detailData.number_of_seasons"><dt>季数</dt><dd>{{ detailData.number_of_seasons }} 季</dd></div>
                    <div v-if="detailData.number_of_episodes"><dt>集数</dt><dd>{{ detailData.number_of_episodes }} 集</dd></div>
                    <div v-if="detailData.status"><dt>状态</dt><dd>{{ detailData.status }}</dd></div>
                    <div v-if="detailData.release_date"><dt>首播</dt><dd>{{ String(detailData.release_date).slice(0, 10) }}</dd></div>
                    <div v-else-if="detailData.release_date"><dt>首播</dt><dd>{{ detailData.release_date }}</dd></div>
                  </template>
                  <template v-else>
                    <div v-if="detailData.runtime"><dt>时长</dt><dd>{{ detailData.runtime }} 分钟</dd></div>
                    <div v-if="detailData.release_date"><dt>上映</dt><dd>{{ String(detailData.release_date).slice(0, 10) }}</dd></div>
                    <div v-if="detailData.status"><dt>状态</dt><dd>{{ detailData.status }}</dd></div>
                  </template>
                  <div v-if="detailData.info"><dt>豆瓣资料</dt><dd>{{ detailData.info }}</dd></div>
                </dl>
                <div v-if="(detailData.genres || []).length" class="md-work-genres is-aside">
                  <span class="md-tag-label">题材标签</span>
                  <span v-for="g in detailData.genres" :key="g" class="md-badge is-soft">{{ g }}</span>
                </div>
                <p v-if="!(detailData.genres || []).length && !detailData.info && !detailData.runtime" class="md-work-facts-empty">暂未提供更多作品资料。</p>
              </div>
            </aside>

            <!-- 右侧：季集 + 关联资源 -->
            <div class="md-work-main">
              <div v-if="(detailData.seasons || []).length" class="md-panel md-work-seasons">
                <div class="md-panel-head">
                  <div>
                    <span class="md-kicker">SEASONS</span>
                    <h3>季列表</h3>
                  </div>
                </div>
                <div class="md-season-list">
                  <div v-for="season in detailData.seasons" :key="season.season_number" class="md-season-item">
                    <img v-if="season.poster" :src="season.poster" loading="lazy" alt="" />
                    <div class="md-season-copy">
                      <strong>{{ season.name }}</strong>
                      <small>{{ season.episode_count }} 集{{ season.air_date ? ' · ' + season.air_date : '' }}</small>
                      <p v-if="season.overview">{{ season.overview }}</p>
                    </div>
                  </div>
                </div>
              </div>

              <!-- 关联资源面板 -->
              <div class="md-panel md-resource-panel">
                <div class="md-panel-head">
                  <div>
                    <span class="md-kicker">RELATED RESOURCES</span>
                    <h3>关联资源</h3>
                  </div>
                  <span v-if="detailResourceSummary" class="md-resource-summary">{{ detailResourceSummary }}</span>
                  <button type="button" class="md-btn is-small" :disabled="detailResourceLoading" @click="loadDetailResources()">
                    {{ detailResourceLoading ? '匹配中…' : '重新匹配' }}
                  </button>
                </div>
                <div class="md-resource-filter-rows">
                  <div class="md-resource-filter-row">
                    <span class="md-tag-label">数据来源</span>
                    <button
                      v-for="choice in resourceSourceChoices"
                      :key="choice.key"
                      type="button"
                      class="md-chip"
                      :class="{ 'is-active': detailResourceSourceFilter === choice.key }"
                      @click="detailResourceSourceFilter = choice.key"
                    >{{ choice.label }}</button>
                  </div>
                  <div class="md-resource-filter-row">
                    <span class="md-tag-label">资源类型</span>
                    <button
                      v-for="choice in detailResourceChoices"
                      :key="choice.key"
                      type="button"
                      class="md-chip"
                      :class="{ 'is-active': detailResourceFilter === choice.key }"
                      @click="detailResourceFilter = choice.key"
                    >{{ choice.label }}</button>
                  </div>
                </div>
                <p v-for="(err, idx) in detailResourceErrors" :key="idx" class="md-resource-error">
                  {{ sourceLabelOf(err.source) }}：{{ err.error }}
                </p>
                <div v-if="detailResourceLoading" class="md-resource-empty">资源匹配中…</div>
                <div v-else-if="!detailResourcesFiltered.length" class="md-resource-empty">
                  {{ detailResources.length ? '当前筛选下暂无资源' : '暂未匹配到资源；稍后重新匹配或配置更多资源频道。' }}
                </div>
                <div v-else class="md-resource-list">
                  <article
                    v-for="item in detailResourcesFiltered"
                    :key="item.item_key"
                    class="md-resource-card"
                    :class="{ 'is-offline': item.link_type === 'magnet' || item.link_type === 'ed2k' }"
                  >
                    <div class="md-resource-main">
                      <div class="md-resource-title-row">
                        <h5 class="md-resource-title" :title="item.title">{{ item.title }}</h5>
                        <span v-if="item.is_official" class="md-resource-official">官组</span>
                        <span v-if="item.sharer" class="md-resource-publisher">发布者：{{ item.sharer }}</span>
                      </div>
                      <div class="md-resource-meta">
                        <span class="md-badge is-source">{{ sourceLabelOf(item.source) }}</span>
                        <span class="md-badge">{{ item.provider_label }}</span>
                        <span class="md-badge" :class="{ 'is-unlocked': item.is_unlocked }">{{ pointTextOf(item) }}</span>
                        <span v-if="item.size" class="md-badge is-size">{{ item.size }}</span>
                        <span v-if="unlockedCountOf(item)" class="md-badge">已解锁 {{ item.unlocked_users_count }} 人</span>
                      </div>
                      <div v-if="episodeTagOf(item)" class="md-resource-tagline"><span class="md-tag-label">季集</span>{{ episodeTagOf(item) }}</div>
                      <div v-if="specTagsOf(item).length" class="md-resource-tagline">
                        <span class="md-tag-label">规格</span>
                        <span v-for="tag in specTagsOf(item)" :key="tag" class="md-badge is-spec">{{ tag }}</span>
                      </div>
                      <div v-if="item.subtitle_languages?.length" class="md-resource-tagline">
                        <span class="md-tag-label">字幕</span>{{ item.subtitle_languages.join(' / ') }}
                      </div>
                      <div v-if="item.remark" class="md-resource-note">{{ item.remark }}</div>
                      <div v-if="item.validate_message" class="md-resource-note is-warn">{{ item.validate_message }}</div>
                    </div>
                    <div class="md-resource-actions">
                      <button type="button" class="md-btn is-small" @click="copyResourceLink(item)">复制链接</button>
                      <button
                        type="button"
                        class="md-btn is-small is-primary"
                        :disabled="resourceTransferDisabled(item)"
                        :title="resourceTransferDisabled(item) ? '请先在基础配置中配置保存目录' : ''"
                        @click="transferResource(item)"
                      >{{ resourceTargetLabel(item) }}</button>
                    </div>
                  </article>
                </div>
              </div>
            </div>
          </div>
        </template>

        <div v-else class="md-state">作品资料加载失败 · <button class="md-btn is-small" @click="closeDetail">返回上一页</button></div>
      </section>
    </transition>

    <!-- ================= 演员档案（全页） ================= -->
    <transition name="md-detail-fade">
      <section v-if="actorProfileVisible" class="md-work-detail">
        <div class="md-work-detail-nav">
          <button class="md-btn" @click="closeActorProfile">‹ 返回影视发现</button>
          <span class="md-work-detail-crumb">影视发现 / 人物资料</span>
        </div>
        <div v-if="actorWorksLoading" class="md-state md-detail-loading">正在读取人物资料…</div>
        <template v-else>
          <div class="md-work-hero md-work-hero-person" :style="actorProfileData.poster ? { backgroundImage: `url(${actorProfileData.poster})` } : {}">
            <div class="md-work-hero-mask"></div>
            <div class="md-work-hero-body">
              <div class="md-work-hero-poster">
                <img v-if="actorProfileData.poster" :src="actorProfileData.poster" :alt="actorProfileData.title" />
                <div v-else class="md-library-tile-placeholder">🎭</div>
              </div>
              <div class="md-work-hero-main">
                <span class="md-kicker">TMDB PERSON PROFILE</span>
                <h2>{{ actorProfileData.title }} <small v-if="actorProfileData.original_title && actorProfileData.original_title !== actorProfileData.title">{{ actorProfileData.original_title }}</small></h2>
                <div class="md-work-hero-meta">
                  <span v-if="actorProfileData.known_for_department" class="md-badge">{{ actorProfileData.known_for_department }}</span>
                  <span v-if="actorProfileData.birthday" class="md-badge">{{ actorProfileData.birthday }}</span>
                  <span v-if="actorProfileData.place_of_birth" class="md-badge">{{ actorProfileData.place_of_birth }}</span>
                </div>
                <p class="md-work-overview">{{ actorProfileData.overview || '暂无人物简介' }}</p>
              </div>
            </div>
          </div>
          <div class="md-work-columns">
            <aside class="md-work-aside">
              <div class="md-panel">
                <div class="md-panel-head">
                  <div>
                    <span class="md-kicker">PROFILE</span>
                    <h3>人物资料</h3>
                  </div>
                </div>
                <dl class="md-work-facts">
                  <div v-if="actorProfileData.birthday"><dt>生日</dt><dd>{{ actorProfileData.birthday }}</dd></div>
                  <div v-if="actorProfileData.place_of_birth"><dt>出生地</dt><dd>{{ actorProfileData.place_of_birth }}</dd></div>
                  <div v-if="actorProfileData.known_for_department"><dt>身份</dt><dd>{{ actorProfileData.known_for_department }}</dd></div>
                </dl>
              </div>
            </aside>
            <div class="md-work-main">
              <div class="md-panel">
                <div class="md-panel-head">
                  <div>
                    <span class="md-kicker">FILMOGRAPHY</span>
                    <h3>作品列表 {{ actorWorks.length ? '· ' + actorWorks.length : '' }}</h3>
                  </div>
                  <span class="md-head-note">按上映日期与热度整理；点击卡片进入作品详情。</span>
                </div>
                <div class="md-library-grid md-library-display-grid" :style="gridStyle">
                  <article
                    v-for="work in actorWorks"
                    :key="work.entity_key || work.tmdb_id"
                    class="md-library-tile"
                    @click="closeActorProfile(); openDetailWithResources(work)"
                  >
                    <div class="md-library-tile-poster">
                      <img v-if="posterUrl(work)" :src="posterUrl(work)" loading="lazy" alt="" />
                      <div v-else class="md-library-tile-placeholder">◉</div>
                      <span class="md-library-tile-kind">{{ kindOf(work) }}</span>
                      <span v-if="formatVote(work.vote_avg)" class="md-library-tile-score">★ {{ formatVote(work.vote_avg) }}</span>
                    </div>
                    <div class="md-library-tile-copy">
                      <strong :title="work.title">{{ work.title }}</strong>
                      <small v-if="work.episode_title">{{ work.episode_title }}</small>
                      <small v-else-if="work.year">{{ work.year }}</small>
                    </div>
                  </article>
                </div>
                <div v-if="!actorWorks.length" class="md-state">暂无作品记录</div>
              </div>
            </div>
          </div>
        </template>
      </section>
    </transition>

    <!-- ================= 订阅弹窗（创建/编辑，多规则编辑器） ================= -->
    <el-dialog v-model="subModalVisible" :title="subEditingId ? '编辑订阅' : '创建订阅'" width="760px" append-to-body class="md-sub-dialog">
      <div class="md-sub-callout">
        订阅「{{ detailData?.title }}」。每条规则都会独立检索并自动转存；积分未知、网盘不匹配或季集无法识别的资源会被安全跳过。
      </div>
      <div class="md-sub-form">
        <div class="md-sub-field-row">
          <label>检查间隔（分钟）</label>
          <input v-model.number="subForm.interval_minutes" type="number" min="15" max="10080" class="md-input" style="width: 120px" />
          <label class="md-sub-inline">
            <input v-model="subForm.enabled" type="checkbox" /> 启用订阅
          </label>
        </div>
        <div v-for="(rule, idx) in subForm.rules" :key="idx" class="md-sub-rule-card">
          <div class="md-sub-rule-head">
            <strong>自动规则 {{ idx + 1 }}</strong>
            <button v-if="subForm.rules.length > 1" type="button" class="md-btn is-small" @click="removeSubscriptionRule(idx)">删除</button>
          </div>
          <div class="md-sub-grid">
            <label>规则名称<input v-model="rule.name" class="md-input" placeholder="例如：123 高码率中文字幕" /></label>
            <label>目标网盘
              <select v-model="rule.target_provider" class="md-select">
                <option v-for="opt in targetProviderOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
              </select>
            </label>
            <label>自动解锁积分上限<input v-model.number="rule.max_points" type="number" min="0" max="99999" class="md-input" /></label>
            <label>分辨率优先级<input v-model="rule.resolutions" class="md-input" placeholder="2160p, 1080p" /></label>
            <label>片源优先级<input v-model="rule.qualities" class="md-input" placeholder="Remux, BluRay, WEB-DL" /></label>
            <label>语言 / 字幕偏好<input v-model="rule.languages" class="md-input" placeholder="国语, 中字, 中文" /></label>
            <label>发布组优先级<input v-model="rule.release_groups" class="md-input" placeholder="HiveWeb, ADWeb, HHWEB" /></label>
            <label class="md-sub-inline">
              <input v-model="rule.prefer_dolby_vision" type="checkbox" /> 优先杜比视界
            </label>
            <label class="md-sub-inline">
              <input v-model="rule.enabled" type="checkbox" /> 启用这条自动规则
            </label>
          </div>
          <div class="md-sub-match">
            <p class="md-sub-match-hint">消息匹配（匹配范围为频道标题 + 消息正文；多个词用 ; 分隔）</p>
            <label>消息正文关键词<input v-model="rule.message_keywords" class="md-input" placeholder="例如：WEB-DL；2160p；中字" /></label>
            <label>必须包含<input v-model="rule.must_contain" class="md-input" placeholder="多个词用 ; 分隔" /></label>
            <label>必须不包含<input v-model="rule.must_not_contain" class="md-input" placeholder="多个词用 ; 分隔" /></label>
          </div>
        </div>
        <button type="button" class="md-btn" @click="addSubscriptionRule">＋ 添加规则</button>
      </div>
      <template #footer>
        <button class="md-btn" @click="subModalVisible = false">取消</button>
        <button class="md-btn is-primary" :disabled="subSaving" @click="saveSubscription">保存订阅</button>
      </template>
    </el-dialog>
  </div>
</template>令牌（对齐参考实现 media_discovery 视觉） ============ */
.md-page {
  --md-primary: #6366f1;
  --md-primary-hover: #4f46e5;
  --md-primary-soft: rgba(99, 102, 241, 0.12);
  --md-primary-soft-strong: rgba(99, 102, 241, 0.2);
  --md-surface: #ffffff;
  --md-raised: rgba(255, 255, 255, 0.88);
  --md-text: #1f2937;
  --md-muted: #64748b;
  --md-border: rgba(71, 85, 105, 0.16);
  --md-border-strong: rgba(99, 102, 241, 0.35);
  --md-radius-xs: 11px;
  --md-radius-sm: 15px;
  --md-radius-md: 20px;
  --md-radius-lg: 26px;
  --md-shadow-card: 0 12px 32px rgba(15, 23, 42, 0.065);
  --md-shadow-float: 0 22px 48px rgba(15, 23, 42, 0.12);
  --md-control-height: 44px;
  --md-page-max: 1480px;
  max-width: var(--md-page-max);
  margin: 0 auto;
  padding: 2px 1px 30px;
  color: var(--md-text);
  font-family: var(--font-sans);
}

:root[data-theme='dark'] .md-page {
  --md-primary: #818cf8;
  --md-primary-hover: #a5b4fc;
  --md-primary-soft: rgba(129, 140, 248, 0.14);
  --md-primary-soft-strong: rgba(129, 140, 248, 0.24);
  --md-surface: #111c33;
  --md-raised: rgba(20, 31, 55, 0.9);
  --md-text: #e5e7eb;
  --md-muted: #94a3b8;
  --md-border: rgba(148, 163, 184, 0.16);
  --md-shadow-card: 0 12px 32px rgba(0, 0, 0, 0.3);
  --md-shadow-float: 0 22px 48px rgba(0, 0, 0, 0.4);
}

.md-page * {
  box-sizing: border-box;
}

/* ============ 分区导航 ============ */
.md-page-nav {
  display: flex;
  gap: 10px;
  padding: 14px 0 4px;
  flex-wrap: wrap;
}

.md-page-nav-item {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-height: 40px;
  padding: 0 18px;
  border: 1px solid var(--md-border);
  border-radius: 13px;
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  box-shadow: var(--md-shadow-card), inset 0 1px 0 rgba(255, 255, 255, 0.45);
  color: var(--md-text);
  font-size: 13.5px;
  font-weight: 750;
  cursor: pointer;
  transition: transform 0.18s ease, border-color 0.18s ease, box-shadow 0.18s ease;
}

.md-page-nav-item:hover {
  transform: translateY(-1px);
}

.md-page-nav-item.active {
  background: linear-gradient(135deg, var(--md-primary), var(--md-primary-hover));
  border-color: transparent;
  color: #fff;
  box-shadow: 0 10px 20px rgba(79, 70, 229, 0.25);
}

.md-nav-emoji {
  font-size: 15px;
  line-height: 1;
}

.md-section {
  margin-top: 12px;
}

.md-section-block {
  margin-top: 16px;
}

/* ============ Hero ============ */
.md-hero {
  position: relative;
  overflow: hidden;
  min-height: 188px;
  border-radius: var(--md-radius-lg);
  padding: 31px 34px 29px;
  background:
    radial-gradient(60% 120% at 82% 8%, rgba(37, 99, 235, 0.28) 0%, transparent 55%),
    radial-gradient(45% 100% at 12% 100%, rgba(14, 165, 233, 0.22) 0%, transparent 60%),
    linear-gradient(118deg, #10172d, #17244a, #263d78);
  color: #fff;
}

.md-hero::after {
  content: '';
  position: absolute;
  inset: 0;
  background-image: linear-gradient(rgba(255, 255, 255, 0.05) 1px, transparent 1px),
    linear-gradient(90deg, rgba(255, 255, 255, 0.05) 1px, transparent 1px);
  background-size: 30px 30px;
  mask-image: linear-gradient(90deg, #000 0%, transparent 65%);
  -webkit-mask-image: linear-gradient(90deg, #000 0%, transparent 65%);
  pointer-events: none;
}

.md-hero-aurora {
  position: absolute;
  right: -70px;
  top: -90px;
  width: 300px;
  height: 300px;
  border-radius: 50%;
  background: radial-gradient(circle, rgba(255, 255, 255, 0.14) 0%, transparent 60%);
  pointer-events: none;
}

.md-hero-eyebrow {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 14px;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.12);
  font-size: 10px;
  font-weight: 800;
  letter-spacing: 0.16em;
  z-index: 1;
}

.md-hero h2 {
  position: relative;
  margin: 14px 0 6px;
  font-size: clamp(29px, 3.15vw, 43px);
  font-weight: 800;
  letter-spacing: -0.04em;
  z-index: 1;
}

.md-hero p {
  position: relative;
  margin: 0;
  max-width: 640px;
  font-size: 13.5px;
  line-height: 1.7;
  color: rgba(255, 255, 255, 0.78);
  z-index: 1;
}

/* ============ 工具栏 / Tab / 按钮 / 输入控件 ============ */
.md-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 16px;
  padding: 14px 16px;
  border: 1px solid var(--md-border);
  border-radius: var(--md-radius-md);
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  box-shadow: var(--md-shadow-card), inset 0 1px 0 rgba(255, 255, 255, 0.45);
  flex-wrap: wrap;
}

.md-tabs {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.md-tab {
  min-height: 38px;
  padding: 0 16px;
  border-radius: 12px;
  border: 1px solid transparent;
  background: transparent;
  color: var(--md-muted);
  font-size: 13px;
  font-weight: 750;
  cursor: pointer;
  transition: all 0.18s ease;
}

.md-tab:hover {
  background: var(--md-primary-soft);
  color: var(--md-primary);
}

.md-tab.is-active {
  background: linear-gradient(135deg, var(--md-primary), var(--md-primary-hover));
  color: #fff;
  box-shadow: 0 8px 16px rgba(79, 70, 229, 0.22);
}

.md-toolbar-spacer {
  flex: 1;
}

.md-library-toolbar-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.md-library-inline-search {
  display: flex;
  align-items: center;
  gap: 8px;
}

.md-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 40px;
  padding: 0 16px;
  border-radius: 13px;
  border: 1px solid var(--md-border);
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  color: var(--md-text);
  font-size: 13px;
  font-weight: 700;
  cursor: pointer;
  transition: transform 0.18s ease, box-shadow 0.18s ease, background 0.18s ease;
}

.md-btn:hover:not(:disabled) {
  transform: translateY(-1px);
}

.md-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.md-btn.is-primary {
  background: linear-gradient(135deg, var(--md-primary), var(--md-primary-hover));
  border-color: transparent;
  color: #fff;
  box-shadow: 0 10px 20px rgba(79, 70, 229, 0.25);
}

.md-btn.is-soft {
  background: var(--md-primary-soft);
  border-color: transparent;
  color: var(--md-primary);
}

.md-input,
.md-select,
.md-textarea {
  min-height: var(--md-control-height, 42px);
  padding: 0 14px;
  border: 1px solid var(--md-border);
  border-radius: 13px;
  background: rgba(255, 255, 255, 0.62);
  color: var(--md-text);
  font-size: 13.5px;
  transition: border-color 0.18s ease, box-shadow 0.18s ease;
}

:root[data-theme='dark'] .md-input,
:root[data-theme='dark'] .md-select {
  background: rgba(15, 23, 42, 0.3);
}

.md-input:focus,
.md-select:focus {
  outline: none;
  border-color: var(--md-primary);
  box-shadow: 0 0 0 3px var(--md-primary-soft-strong);
}

.md-library-inline-search .md-input {
  width: clamp(188px, 20vw, 292px);
}

/* ============ 筛选面板 ============ */
.md-library-filter-panel {
  position: relative;
  margin-top: 14px;
  border: 1px solid var(--md-border);
  border-radius: var(--md-radius-md);
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  box-shadow: var(--md-shadow-card), inset 0 1px 0 rgba(255, 255, 255, 0.45);
  padding: 16px 18px 10px;
}

.md-library-filter-panel::before {
  content: '';
  position: absolute;
  left: 18px;
  right: 18px;
  bottom: -1px;
  height: 1px;
  background: linear-gradient(90deg, transparent, var(--md-primary), transparent);
  opacity: 0.7;
}

.md-library-filter-panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}

.md-library-filter-panel-head strong {
  display: block;
  margin-top: 3px;
  font-size: 15px;
  font-weight: 850;
  letter-spacing: -0.02em;
}

.md-kicker {
  font-size: 9.5px;
  font-weight: 900;
  letter-spacing: 0.13em;
  color: var(--md-primary);
}

.md-library-filter-reset {
  padding: 4px 10px;
  border: none;
  border-radius: 9px;
  background: transparent;
  color: var(--md-muted);
  font-size: 12px;
  font-weight: 650;
  cursor: pointer;
}

.md-library-filter-reset:disabled {
  opacity: 0.42;
  cursor: default;
}

.md-library-filter-row {
  display: grid;
  grid-template-columns: 62px minmax(0, 1fr);
  gap: 12px;
  align-items: start;
  min-height: 42px;
  padding: 7px 0;
  border-top: 1px solid var(--md-border);
}

.md-library-filter-label {
  padding-top: 7px;
  font-size: 12px;
  font-weight: 820;
  color: var(--md-muted);
}

.md-library-filter-options {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.md-library-filter-chip {
  min-height: 31px;
  padding: 0 13px;
  border: 1px solid transparent;
  border-radius: 9px;
  background: transparent;
  color: var(--md-text);
  font-size: 12px;
  font-weight: 720;
  cursor: pointer;
  transition: all 0.15s ease;
}

.md-library-filter-chip:hover {
  background: var(--md-primary-soft);
  color: var(--md-primary);
}

.md-library-filter-chip.is-active {
  background: linear-gradient(135deg, rgba(99, 102, 241, 0.18), rgba(99, 102, 241, 0.08));
  color: var(--md-primary);
  font-weight: 850;
}

:root[data-theme='dark'] .md-library-filter-chip.is-active {
  background: linear-gradient(135deg, rgba(129, 140, 248, 0.24), rgba(129, 140, 248, 0.14));
}

/* ============ 片库卡片网格 ============ */
.md-library-grid.md-library-display-grid {
  display: grid;
  grid-template-columns: repeat(var(--md-library-columns, 6), minmax(0, 1fr));
  gap: 14px;
  align-items: start;
}

.md-library-tile {
  border: 1px solid var(--md-border);
  border-radius: 16px;
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  box-shadow: var(--md-shadow-card), inset 0 1px 0 rgba(255, 255, 255, 0.4);
  overflow: hidden;
  transition: transform 0.2s ease, border-color 0.2s ease, box-shadow 0.2s ease;
}

.md-library-tile:hover {
  transform: translateY(-3px);
  border-color: rgba(129, 140, 248, 0.58);
  box-shadow: var(--md-shadow-float), inset 0 1px 0 rgba(255, 255, 255, 0.45);
}

.md-library-tile-poster {
  position: relative;
  aspect-ratio: 2 / 2.72;
  overflow: hidden;
  cursor: pointer;
}

.md-library-tile-poster::after {
  content: '';
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  height: 42%;
  background: linear-gradient(to top, rgba(0, 0, 0, 0.55), transparent);
  pointer-events: none;
}

.md-library-tile-poster img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  transition: transform 0.25s ease;
}

.md-library-tile:hover .md-library-tile-poster img {
  transform: scale(1.045);
}

.md-library-tile-placeholder {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(160deg, #34466d, #111827);
  color: rgba(255, 255, 255, 0.35);
  font-size: 26px;
}

.md-library-tile-kind {
  position: absolute;
  left: 8px;
  top: 8px;
  min-height: 22px;
  padding: 0 9px;
  display: inline-flex;
  align-items: center;
  border-radius: 8px;
  background: rgba(15, 23, 42, 0.72);
  color: #fff;
  font-size: 10.5px;
  font-weight: 750;
  backdrop-filter: blur(4px);
  z-index: 1;
}

.md-library-tile-rank {
  position: absolute;
  left: 8px;
  top: 8px;
  min-width: 26px;
  height: 26px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 9px;
  background: linear-gradient(135deg, #f59e0b, #f97316);
  color: #fff;
  font-size: 13px;
  font-weight: 900;
  font-variant-numeric: tabular-nums;
  box-shadow: 0 4px 10px rgba(245, 158, 11, 0.4);
  z-index: 1;
}

.md-library-tile-score {
  position: absolute;
  right: 8px;
  top: 8px;
  min-height: 23px;
  padding: 0 8px;
  display: inline-flex;
  align-items: center;
  border-radius: 9px;
  background: rgba(15, 23, 42, 0.72);
  color: #fde68a;
  font-size: 11px;
  font-weight: 800;
  font-variant-numeric: tabular-nums;
  backdrop-filter: blur(4px);
  z-index: 1;
}

.md-emby-status {
  position: absolute;
  right: 8px;
  bottom: 8px;
  min-height: 23px;
  padding: 0 8px;
  display: inline-flex;
  align-items: center;
  border-radius: 9px;
  background: linear-gradient(135deg, #10b981, #059669);
  color: #fff;
  font-size: 12px;
  z-index: 1;
}

.md-tile-action {
  position: absolute;
  right: 8px;
  bottom: 34px;
  width: 30px;
  height: 30px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: none;
  border-radius: 10px;
  background: rgba(15, 23, 42, 0.6);
  color: #fff;
  cursor: pointer;
  backdrop-filter: blur(4px);
  z-index: 2;
  transition: background 0.15s ease, transform 0.15s ease;
}

.md-tile-action:hover {
  background: rgba(79, 70, 229, 0.85);
  transform: scale(1.06);
}

.md-tile-action.active {
  background: linear-gradient(135deg, #ec4899, #db2777);
}

.md-tile-action.busy {
  opacity: 0.6;
}

.md-tile-action.md-tile-fav {
  bottom: 34px;
  right: 42px;
}

.md-tile-action.md-tile-match {
  bottom: 36px;
  right: 10px;
}

.md-tile-action.md-tile-fav.active {
  background: linear-gradient(135deg, #f59e0b, #d97706);
}

.md-library-tile-copy {
  padding: 10px 10px 11px;
}

.md-library-tile-copy strong {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  min-height: 32px;
  font-size: 12.5px;
  font-weight: 840;
  line-height: 1.35;
}

.md-library-tile-copy small {
  display: block;
  margin-top: 4px;
  font-size: 10.5px;
  font-weight: 720;
  color: var(--md-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 骨架屏 */
.md-skeleton-grid {
  display: grid;
  grid-template-columns: repeat(var(--md-library-columns, 6), minmax(0, 1fr));
  gap: 14px;
  margin-top: 16px;
}

.md-skeleton-tile {
  aspect-ratio: 2 / 3.2;
  border-radius: 16px;
  background: linear-gradient(100deg, rgba(148, 163, 184, 0.14) 35%, rgba(148, 163, 184, 0.3) 50%, rgba(148, 163, 184, 0.14) 65%);
  background-size: 200% 100%;
  animation: md-shimmer 1.35s infinite;
}

@keyframes md-shimmer {
  from {
    background-position: 120% 0;
  }
  to {
    background-position: -80% 0;
  }
}

/* 分页 / 无限滚动 */
.md-pagination {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 14px;
  margin-top: 22px;
}

.md-page-indicator {
  font-size: 12.5px;
  font-weight: 750;
  color: var(--md-muted);
  font-variant-numeric: tabular-nums;
}

.md-library-infinite-scroll {
  display: block;
  width: 100%;
  min-height: 44px;
  margin-top: 18px;
  border: 1px dashed var(--md-border);
  border-radius: 13px;
  background: transparent;
  color: var(--md-muted);
  font-size: 12.5px;
  font-weight: 700;
  cursor: pointer;
}

.md-library-infinite-scroll.is-loading::before {
  content: '';
  display: inline-block;
  width: 13px;
  height: 13px;
  margin-right: 8px;
  border: 2px solid var(--md-primary-soft-strong);
  border-top-color: var(--md-primary);
  border-radius: 50%;
  animation: md-spin 0.8s linear infinite;
  vertical-align: -2px;
}

@keyframes md-spin {
  to {
    transform: rotate(360deg);
  }
}

/* 空状态 / 错误 */
.md-state {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 250px;
  margin-top: 16px;
  border: 1px dashed var(--md-border);
  border-radius: var(--md-radius-md);
  color: var(--md-muted);
  font-size: 14px;
  font-weight: 650;
}

.md-error-tip {
  margin: 12px 0 0;
  padding: 11px 16px;
  border-radius: 12px;
  background: rgba(245, 158, 11, 0.12);
  border: 1px solid rgba(245, 158, 11, 0.3);
  color: #b45309;
  font-size: 13px;
}

/* ============ 榜单推荐 ============ */
.md-ranking-hero {
  position: relative;
  overflow: hidden;
  min-height: 238px;
  border-radius: var(--md-radius-lg);
  padding: 34px 36px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
  background:
    radial-gradient(55% 110% at 85% 0%, rgba(124, 58, 237, 0.4) 0%, transparent 60%),
    radial-gradient(45% 100% at 5% 100%, rgba(59, 130, 246, 0.24) 0%, transparent 60%),
    linear-gradient(118deg, #111b39, #1f2861, #3d2c77);
  color: #fff;
}

.md-ranking-hero-main {
  position: relative;
  z-index: 1;
}

.md-ranking-kicker {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  font-size: 11px;
  font-weight: 850;
  letter-spacing: 0.13em;
  opacity: 0.88;
}

.md-ranking-brand-mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 29px;
  height: 29px;
  border-radius: 9px;
  background: rgba(255, 255, 255, 0.14);
  font-size: 14px;
  font-weight: 900;
  letter-spacing: 0;
}

.md-ranking-hero h2 {
  margin: 14px 0 6px;
  font-size: clamp(31px, 4vw, 48px);
  font-weight: 900;
  letter-spacing: -0.045em;
}

.md-ranking-hero p {
  margin: 0;
  max-width: 640px;
  font-size: 13.5px;
  line-height: 1.7;
  color: rgba(255, 255, 255, 0.76);
}

.md-ranking-hero-side {
  position: relative;
  z-index: 1;
  flex-shrink: 0;
  min-width: 139px;
  padding: 18px 22px;
  border-radius: var(--md-radius-sm);
  background: rgba(255, 255, 255, 0.1);
  backdrop-filter: blur(10px);
  border: 1px solid rgba(255, 255, 255, 0.16);
  text-align: center;
}

.md-ranking-hero-side strong {
  display: block;
  font-size: 32px;
  font-weight: 900;
  font-variant-numeric: tabular-nums;
}

.md-ranking-hero-side span {
  font-size: 11.5px;
  color: rgba(255, 255, 255, 0.72);
}

.md-ranking-control-panel {
  margin-top: 16px;
  padding: 16px;
  border: 1px solid var(--md-border);
  border-radius: var(--md-radius-md);
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  box-shadow: var(--md-shadow-card), inset 0 1px 0 rgba(255, 255, 255, 0.45);
}

.md-panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.md-panel-head strong {
  display: block;
  margin-top: 3px;
  font-size: 15px;
  font-weight: 850;
  letter-spacing: -0.02em;
}

.md-streaming-tabs {
  display: flex;
  gap: 10px;
  margin-top: 14px;
  flex-wrap: wrap;
}

.md-streaming-tab {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 44px;
  padding: 0 14px 0 8px;
  border: 1px solid var(--md-border);
  border-radius: 12px;
  background: transparent;
  color: var(--md-text);
  font-family: inherit;
  cursor: pointer;
  transition: all 0.18s ease;
}

.md-streaming-tab:hover {
  border-color: var(--md-primary);
}

.md-streaming-tab.is-active {
  background: linear-gradient(135deg, rgba(99, 102, 241, 0.18), rgba(99, 102, 241, 0.08));
  border-color: var(--md-primary-soft-strong);
  box-shadow: 0 6px 14px rgba(79, 70, 229, 0.14);
}

.md-streaming-tab-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 8px;
  background: #111827;
  color: #fff;
  font-size: 13px;
  font-weight: 850;
}

.md-streaming-tab-icon.brand-netflix {
  background: #000;
  color: #e50914;
}

.md-streaming-tab-icon.brand-disney {
  background: linear-gradient(135deg, #0f1f91, #45a0f6);
}

.md-streaming-tab-icon.brand-hbo {
  background: #9413dc;
}

.md-streaming-tab-icon.brand-amazon,
.md-streaming-tab-icon.brand-prime,
.md-streaming-tab-icon.brand-primevideo {
  background: linear-gradient(135deg, #00a8e1, #00e1c8);
  color: #082032;
}

.md-streaming-tab-icon.brand-apple,
.md-streaming-tab-icon.brand-appletv {
  background: linear-gradient(135deg, #555, #aaa);
}

.md-streaming-tab-icon.brand-hulu {
  background: linear-gradient(135deg, #1ce783, #00a84c);
  color: #062;
}

.md-streaming-tab-icon.hive {
  background: linear-gradient(135deg, #6366f1, #8b5cf6);
}

.md-streaming-tab-label {
  font-size: 12px;
  font-weight: 800;
}

.md-ranking-filter-row {
  display: flex;
  align-items: center;
  gap: 18px;
  margin-top: 14px;
  padding-top: 14px;
  border-top: 1px solid var(--md-border);
  flex-wrap: wrap;
}

.md-ranking-type-tabs {
  display: flex;
  gap: 8px;
}

.md-ranking-type,
.md-calendar-kind {
  min-height: 36px;
  padding: 0 16px;
  border: 1px solid var(--md-border);
  border-radius: 10px;
  background: transparent;
  color: var(--md-muted);
  font-size: 12.5px;
  font-weight: 750;
  cursor: pointer;
  transition: all 0.16s ease;
}

.md-ranking-type.is-active,
.md-calendar-kind.is-active {
  background: linear-gradient(135deg, var(--md-primary), var(--md-primary-hover));
  border-color: transparent;
  color: #fff;
}

.md-ranking-country {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  font-weight: 750;
  color: var(--md-muted);
}

.md-ranking-country .md-select {
  min-height: 37px;
  padding: 0 10px;
  font-size: 12.5px;
}

/* 榜单结果 */
.md-ranking-results-shell {
  margin-top: 16px;
  padding: 22px;
  border: 1px solid var(--md-border);
  border-radius: 22px;
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  box-shadow: var(--md-shadow-card), inset 0 1px 0 rgba(255, 255, 255, 0.45);
}

.md-calendar-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
}

.md-calendar-head h3 {
  margin: 4px 0 0;
  font-size: 22px;
  font-weight: 900;
  letter-spacing: -0.055em;
}

.md-head-note {
  font-size: 12px;
  color: var(--md-muted);
  font-weight: 650;
}

.md-ranking-group {
  margin-top: 31px;
  padding-top: 27px;
  border-top: 1px solid var(--md-border);
}

.md-ranking-group-head span {
  font-size: 11px;
  font-weight: 900;
  color: var(--md-primary);
  letter-spacing: 0.1em;
}

.md-ranking-group-head h4 {
  margin: 5px 0 3px;
  font-size: 18px;
  font-weight: 880;
  letter-spacing: -0.03em;
}

.md-ranking-group-head small {
  font-size: 11px;
  color: var(--md-muted);
  font-weight: 650;
}

.md-ranking-grid {
  display: grid;
  grid-template-columns: repeat(var(--md-ranking-columns, 5), minmax(0, 1fr));
  gap: 12px;
  margin-top: 14px;
}

.md-ranking-tile {
  border: 1px solid var(--md-border);
  border-radius: 14px;
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  box-shadow: var(--md-shadow-card), inset 0 1px 0 rgba(255, 255, 255, 0.4);
  overflow: hidden;
  transition: transform 0.2s ease, border-color 0.2s ease;
}

.md-ranking-tile:hover {
  transform: translateY(-3px);
  border-color: rgba(129, 140, 248, 0.58);
}

.md-ranking-tile-poster {
  position: relative;
  aspect-ratio: 2 / 2.82;
  overflow: hidden;
  cursor: pointer;
}

.md-ranking-tile-poster img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  transition: transform 0.25s ease;
}

.md-ranking-tile:hover .md-ranking-tile-poster img {
  transform: scale(1.045);
}

.md-rank {
  position: absolute;
  left: 8px;
  top: 8px;
  min-width: 29px;
  height: 29px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 10px;
  background: rgba(15, 23, 42, 0.78);
  color: #fde68a;
  font-size: 14px;
  font-weight: 900;
  font-variant-numeric: tabular-nums;
  backdrop-filter: blur(4px);
  z-index: 1;
}

.md-score {
  position: absolute;
  right: 8px;
  top: 8px;
  min-height: 23px;
  padding: 0 8px;
  display: inline-flex;
  align-items: center;
  border-radius: 9px;
  background: rgba(15, 23, 42, 0.78);
  color: #fde68a;
  font-size: 10.5px;
  font-weight: 800;
  font-variant-numeric: tabular-nums;
  backdrop-filter: blur(4px);
  z-index: 1;
}

.md-ranking-tile-copy {
  padding: 9px 10px 11px;
}

.md-ranking-tile-copy strong {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  min-height: 32px;
  font-size: 12.5px;
  font-weight: 840;
  line-height: 1.35;
}

.md-ranking-tile-copy small {
  display: block;
  margin-top: 4px;
  font-size: 10.5px;
  color: var(--md-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* ============ 追剧日历 ============ */
.md-calendar-spotlight {
  position: relative;
  overflow: hidden;
  min-height: 278px;
  border-radius: var(--md-radius-lg);
  padding: 32px 36px;
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 20px;
  background:
    radial-gradient(60% 120% at 88% 0%, rgba(244, 114, 182, 0.34) 0%, transparent 60%),
    radial-gradient(50% 100% at 6% 100%, rgba(124, 58, 237, 0.34) 0%, transparent 62%),
    linear-gradient(118deg, #1a1335, #2a1b45, #4a2558);
  color: #fff;
}

.md-calendar-spotlight-backdrop {
  position: absolute;
  inset: 0;
  background-size: cover;
  background-position: center;
  opacity: 0.22;
}

.md-calendar-spotlight-backdrop::after {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(90deg, rgba(10, 12, 30, 0.9) 20%, rgba(10, 12, 30, 0.45) 55%, rgba(10, 12, 30, 0.25));
}

.md-calendar-spotlight-main {
  position: relative;
  z-index: 1;
  max-width: 720px;
}

.md-calendar-spotlight-kicker {
  display: inline-block;
  padding: 6px 14px;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.14);
  font-size: 10px;
  font-weight: 800;
  letter-spacing: 0.16em;
}

.md-calendar-spotlight-main time {
  display: block;
  margin-top: 12px;
  font-size: 12.5px;
  color: rgba(255, 255, 255, 0.72);
  font-variant-numeric: tabular-nums;
}

.md-calendar-spotlight-main h2 {
  margin: 6px 0 8px;
  font-size: clamp(30px, 4vw, 47px);
  font-weight: 900;
  letter-spacing: -0.045em;
}

.md-calendar-spotlight-main p {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
  color: rgba(255, 255, 255, 0.78);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.md-calendar-feature-meta {
  display: flex;
  gap: 8px;
  margin-top: 14px;
  flex-wrap: wrap;
}

.md-calendar-feature-meta span {
  padding: 4px 11px;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.12);
  font-size: 11px;
  font-weight: 750;
}

.md-calendar-spotlight-stat {
  position: relative;
  z-index: 1;
  flex-shrink: 0;
  min-width: 139px;
  padding: 18px 22px;
  border-radius: var(--md-radius-sm);
  background: rgba(255, 255, 255, 0.1);
  backdrop-filter: blur(10px);
  border: 1px solid rgba(255, 255, 255, 0.16);
  text-align: center;
}

.md-calendar-spotlight-stat strong {
  display: block;
  font-size: 32px;
  font-weight: 900;
  font-variant-numeric: tabular-nums;
}

.md-calendar-spotlight-stat span {
  font-size: 11.5px;
  color: rgba(255, 255, 255, 0.72);
}

.md-calendar-shell {
  margin-top: 16px;
  padding: 22px;
  border: 1px solid var(--md-border);
  border-radius: 22px;
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  box-shadow: var(--md-shadow-card), inset 0 1px 0 rgba(255, 255, 255, 0.45);
}

.md-calendar-head-controls {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.md-calendar-kind-tabs,
.md-calendar-day-presets {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

/* 日期轨道 */
.md-calendar-date-rail-wrap {
  display: grid;
  grid-template-columns: 36px 1fr 36px;
  gap: 10px;
  align-items: center;
  margin-top: 18px;
  padding: 10px;
  border: 1px solid var(--md-border);
  border-radius: 17px;
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
}

.md-calendar-rail-arrow {
  width: 36px;
  height: 36px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--md-border);
  border-radius: 11px;
  background: transparent;
  color: var(--md-muted);
  font-size: 17px;
  cursor: pointer;
}

.md-calendar-rail-arrow:disabled {
  opacity: 0.35;
  cursor: default;
}

.md-calendar-date-rail {
  display: flex;
  gap: 8px;
  overflow-x: auto;
  padding: 2px;
  scrollbar-width: thin;
}

.md-calendar-date {
  position: relative;
  flex-shrink: 0;
  width: 52px;
  height: 67px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 1px;
  border: 1px solid var(--md-border);
  border-radius: 12px;
  background: transparent;
  color: var(--md-muted);
  cursor: pointer;
  transition: all 0.16s ease;
}

.md-calendar-date span {
  font-size: 9.5px;
  font-weight: 750;
}

.md-calendar-date strong {
  font-size: 19px;
  font-weight: 880;
  font-variant-numeric: tabular-nums;
  line-height: 1.1;
}

.md-calendar-date em {
  font-style: normal;
  font-size: 9px;
}

.md-calendar-date i {
  position: absolute;
  right: 4px;
  top: 4px;
  min-width: 15px;
  height: 15px;
  padding: 0 3px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  background: var(--md-primary-soft-strong);
  color: var(--md-primary);
  font-style: normal;
  font-size: 9px;
  font-weight: 850;
  font-variant-numeric: tabular-nums;
}

.md-calendar-date i.empty {
  background: transparent;
  color: var(--md-muted);
}

.md-calendar-date.is-active {
  background: linear-gradient(135deg, var(--md-primary), #7c3aed);
  border-color: transparent;
  color: #fff;
  box-shadow: 0 8px 16px rgba(99, 102, 241, 0.28);
}

.md-calendar-date.is-active i {
  background: rgba(255, 255, 255, 0.2);
  color: #fff;
}

/* 每日区块 */
.md-calendar-day-block {
  margin-top: 20px;
}

.md-calendar-day-head span {
  font-size: 11px;
  font-weight: 900;
  color: var(--md-primary);
  letter-spacing: 0.1em;
}

.md-calendar-day-head h4 {
  margin: 5px 0 3px;
  font-size: 21px;
  font-weight: 900;
  letter-spacing: -0.04em;
}

.md-calendar-day-head p {
  margin: 0;
  font-size: 11.5px;
  color: var(--md-muted);
}

.md-calendar-card-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 14px;
  margin-top: 14px;
}

.md-calendar-card {
  border: 1px solid var(--md-border);
  border-radius: 16px;
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  box-shadow: var(--md-shadow-card), inset 0 1px 0 rgba(255, 255, 255, 0.4);
  overflow: hidden;
  transition: transform 0.2s ease, border-color 0.2s ease;
}

.md-calendar-card:hover {
  transform: translateY(-3px);
  border-color: rgba(129, 140, 248, 0.58);
}

.md-calendar-card-poster {
  position: relative;
  aspect-ratio: 2 / 2.84;
  overflow: hidden;
  cursor: pointer;
}

.md-calendar-card-poster::after {
  content: '';
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  height: 43%;
  background: linear-gradient(to top, rgba(0, 0, 0, 0.55), transparent);
  pointer-events: none;
}

.md-calendar-card-poster img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  transition: transform 0.25s ease;
}

.md-calendar-card:hover .md-calendar-card-poster img {
  transform: scale(1.045);
}

.md-calendar-card-schedule {
  position: absolute;
  left: 8px;
  right: 8px;
  bottom: 8px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  z-index: 1;
}

.md-calendar-card-schedule b {
  font-size: 12px;
  font-weight: 900;
  color: #fff;
  font-variant-numeric: tabular-nums;
  text-shadow: 0 1px 4px rgba(0, 0, 0, 0.6);
}

.md-calendar-card-schedule time {
  font-size: 10.5px;
  font-weight: 800;
  color: #fde68a;
  font-variant-numeric: tabular-nums;
  text-shadow: 0 1px 4px rgba(0, 0, 0, 0.6);
}

.md-calendar-card-copy {
  padding: 10px;
}

.md-calendar-card-copy strong {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  min-height: 32px;
  font-size: 12.5px;
  font-weight: 840;
  line-height: 1.35;
}

.md-calendar-card-copy small {
  display: block;
  margin-top: 4px;
  font-size: 10.5px;
  color: var(--md-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.md-calendar-card-meta {
  display: block;
  margin-top: 4px;
  font-size: 10px;
  color: var(--md-muted);
}

.md-calendar-empty-day {
  margin-top: 14px;
  padding: 40px 0;
  text-align: center;
  border: 1px dashed var(--md-border);
  border-radius: var(--md-radius-md);
  color: var(--md-muted);
  font-size: 13px;
  font-weight: 650;
}

/* 番剧放送日历 */
.md-anime-shell {
  margin-top: 16px;
}

.md-anime-weekday {
  margin-top: 18px;
}

.md-anime-weekday-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.md-anime-weekday-head .md-kicker {
  font-size: 11px;
}

.md-badge {
  display: inline-flex;
  align-items: center;
  min-height: 25px;
  padding: 0 10px;
  border-radius: 9px;
  background: var(--md-primary-soft);
  color: var(--md-primary);
  font-size: 11px;
  font-weight: 800;
  font-variant-numeric: tabular-nums;
}

/* ============ 基础配置 ============ */
.md-basic-config-form {
  margin-top: 16px;
}

.md-basic-config-savebar {
  position: sticky;
  top: 12px;
  z-index: 8;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 18px;
  border: 1px solid var(--md-primary-soft-strong);
  border-radius: 15px;
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  box-shadow: var(--md-shadow-card), inset 0 1px 0 rgba(255, 255, 255, 0.45);
}

.md-basic-config-savebar-title {
  font-size: 14px;
  font-weight: 850;
}

.md-basic-config-savebar-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.md-basic-config-spinner {
  font-size: 12px;
  color: var(--md-muted);
}

.md-basic-config-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
  margin-top: 16px;
}

.md-panel {
  border: 1px solid var(--md-border);
  border-radius: var(--md-radius-md);
  background: linear-gradient(145deg, var(--md-raised), var(--md-surface));
  box-shadow: var(--md-shadow-card), inset 0 1px 0 rgba(255, 255, 255, 0.45);
  overflow: hidden;
}

.md-panel-head {
  padding: 16px 18px;
  border-bottom: 1px solid var(--md-border);
}

.md-panel-head h3 {
  position: relative;
  margin: 6px 0 3px;
  padding-left: 12px;
  font-size: 18px;
  font-weight: 800;
  letter-spacing: -0.03em;
}

.md-panel-head h3::before {
  content: '';
  position: absolute;
  left: 0;
  top: 2px;
  bottom: 2px;
  width: 4px;
  border-radius: 4px;
  background: linear-gradient(135deg, var(--md-primary), var(--md-primary-hover));
}

.md-panel-head p {
  margin: 0;
  font-size: 11.5px;
  color: var(--md-muted);
}

.md-panel-body {
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.md-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.md-field-row {
  flex-direction: row;
  align-items: center;
  justify-content: space-between;
}

.md-field-label {
  font-size: 11.5px;
  font-weight: 750;
  color: var(--md-muted);
}

.md-field-control {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.md-field-control .md-input,
.md-field-control .md-select {
  min-height: 38px;
  font-size: 13px;
}

/* 开关 */
.md-switch {
  position: relative;
  display: inline-flex;
  cursor: pointer;
}

.md-switch input {
  position: absolute;
  opacity: 0;
  width: 0;
  height: 0;
}

.md-switch-track {
  width: 42px;
  height: 24px;
  border-radius: 999px;
  background: var(--md-border);
  position: relative;
  transition: background 0.2s ease;
}

.md-switch-track::after {
  content: '';
  position: absolute;
  left: 3px;
  top: 3px;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: #fff;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.25);
  transition: transform 0.2s ease;
}

.md-switch input:checked + .md-switch-track {
  background: linear-gradient(135deg, var(--md-primary), var(--md-primary-hover));
}

.md-switch input:checked + .md-switch-track::after {
  transform: translateX(18px);
}

/* ============ 响应式 ============ */
@media (max-width: 980px) {
  .md-basic-config-grid {
    grid-template-columns: 1fr;
  }

  .md-ranking-hero {
    flex-direction: column;
    align-items: flex-start;
  }

  .md-calendar-spotlight {
    flex-direction: column;
    align-items: flex-start;
  }
}

@media (max-width: 600px) {
  .md-library-grid.md-library-display-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .md-skeleton-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .md-pagination {
    display: none;
  }

  .md-streaming-tabs {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
  }

  .md-ranking-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .md-page-nav-item {
    padding: 0 12px;
    font-size: 12.5px;
  }
}

/* ========================= 详情弹窗 + 关联资源 ========================= */
.md-detail-head {
  display: flex;
  gap: 16px;
  margin-bottom: 16px;
}

.md-detail-poster {
  width: 120px;
  border-radius: 10px;
  flex-shrink: 0;
  object-fit: cover;
}

.md-detail-meta {
  flex: 1;
  min-width: 0;
}

.md-detail-line {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
  margin: 0 0 8px;
}

.md-detail-sub {
  color: var(--md-text-secondary, #94a3b8);
  font-size: 13px;
  margin: 0 0 6px;
}

.md-detail-overview {
  font-size: 13px;
  line-height: 1.65;
  color: var(--md-text-secondary, #94a3b8);
  margin: 0 0 10px;
  display: -webkit-box;
  -webkit-line-clamp: 4;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.md-detail-links {
  display: flex;
  gap: 8px;
}

.md-badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 999px;
  background: rgba(148, 163, 184, 0.16);
  color: inherit;
  font-size: 12px;
  line-height: 1.5;
  white-space: nowrap;
}

.md-badge.is-score {
  background: rgba(245, 158, 11, 0.18);
  color: #f59e0b;
}

.md-badge.is-emby {
  background: rgba(34, 197, 94, 0.18);
  color: #22c55e;
}

.md-badge.is-source {
  background: rgba(45, 167, 234, 0.18);
  color: #2da7ea;
}

.md-badge.is-unlocked {
  background: rgba(34, 197, 94, 0.16);
  color: #22c55e;
}

.md-badge.is-size {
  background: rgba(148, 163, 184, 0.14);
}

.md-badge.is-spec {
  background: rgba(139, 92, 246, 0.14);
}

/* 观影授权块 */
.md-guanying-auth {
  margin-top: 16px;
  padding: 14px;
  border: 1px solid rgba(148, 163, 184, 0.25);
  border-radius: 12px;
}

.md-guanying-auth-status {
  font-size: 13px;
  color: var(--md-text-secondary, #94a3b8);
  margin-bottom: 10px;
}

.md-guanying-auth-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.md-guanying-auth-actions .md-input {
  width: 180px;
}

.md-guanying-captcha {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px dashed rgba(148, 163, 184, 0.3);
}

.md-guanying-captcha-hint {
  font-size: 13px;
  margin: 0 0 8px;
}

.md-guanying-captcha-box {
  position: relative;
  cursor: pointer;
  border-radius: 8px;
  overflow: hidden;
  margin-bottom: 10px;
}

.md-guanying-captcha-box img {
  width: 100%;
  height: 100%;
  display: block;
}

.md-guanying-captcha-point {
  position: absolute;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: rgba(45, 167, 234, 0.85);
  color: #fff;
  font-size: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  pointer-events: none;
}

.md-guanying-captcha-actions {
  display: flex;
  gap: 8px;
}

/* 资源面板 */
.md-resource-panel {
  border-top: 1px solid rgba(148, 163, 184, 0.2);
  padding-top: 14px;
}

.md-resource-panel-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.md-resource-panel-head h4 {
  margin: 0;
  font-size: 15px;
}

.md-resource-summary {
  font-size: 12.5px;
  color: var(--md-text-secondary, #94a3b8);
}

.md-resource-filters {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 10px;
}

.md-chip {
  padding: 4px 12px;
  border-radius: 999px;
  border: 1px solid rgba(148, 163, 184, 0.35);
  background: transparent;
  color: inherit;
  font-size: 12.5px;
  cursor: pointer;
}

.md-chip.is-active {
  background: rgba(45, 167, 234, 0.16);
  border-color: rgba(45, 167, 234, 0.6);
  color: #2da7ea;
}

.md-resource-error {
  font-size: 12.5px;
  color: #f59e0b;
  margin: 4px 0;
}

.md-resource-empty {
  padding: 28px 0;
  text-align: center;
  color: var(--md-text-secondary, #94a3b8);
  font-size: 13px;
}

.md-resource-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 420px;
  overflow-y: auto;
}

.md-resource-card {
  display: flex;
  gap: 12px;
  padding: 12px 14px;
  border: 1px solid rgba(148, 163, 184, 0.22);
  border-radius: 12px;
  align-items: flex-start;
}

.md-resource-card.is-offline {
  border-style: dashed;
}

.md-resource-main {
  flex: 1;
  min-width: 0;
}

.md-resource-title-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
}

.md-resource-title {
  margin: 0;
  font-size: 14px;
  overflow-wrap: anywhere;
}

.md-resource-official {
  padding: 1px 6px;
  border-radius: 4px;
  background: rgba(245, 158, 11, 0.18);
  color: #f59e0b;
  font-size: 11px;
}

.md-resource-publisher {
  font-size: 12px;
  color: var(--md-text-secondary, #94a3b8);
}

.md-resource-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 6px;
}

.md-resource-tagline {
  font-size: 12.5px;
  color: var(--md-text-secondary, #94a3b8);
  margin-bottom: 4px;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
}

.md-tag-label {
  color: rgba(148, 163, 184, 0.8);
  flex-shrink: 0;
}

.md-resource-note {
  font-size: 12px;
  color: var(--md-text-secondary, #94a3b8);
  overflow-wrap: anywhere;
}

.md-resource-note.is-warn {
  color: #f59e0b;
}

.md-resource-actions {
  display: flex;
  flex-direction: column;
  gap: 6px;
  flex-shrink: 0;
}

/* 通用按钮（对齐页面已有 md-btn 若无则补） */
.md-btn {
  padding: 5px 14px;
  border-radius: 8px;
  border: 1px solid rgba(148, 163, 184, 0.35);
  background: transparent;
  color: inherit;
  font-size: 13px;
  cursor: pointer;
}

.md-btn.is-primary {
  background: #2da7ea;
  border-color: #2da7ea;
  color: #fff;
}

.md-btn.is-small {
  padding: 3px 10px;
  font-size: 12px;
}

.md-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* 白名单可视化筛选规则卡 */
.vf-rules {
  margin-top: 10px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.vf-rule-card {
  display: flex;
  gap: 10px;
  align-items: center;
  padding: 8px 10px;
  border: 1px solid rgba(148, 163, 184, 0.25);
  border-radius: 10px;
}

.vf-rule-card.is-disabled {
  opacity: 0.55;
}

.vf-rule-poster {
  width: 40px;
  border-radius: 6px;
  flex-shrink: 0;
}

.vf-rule-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.vf-rule-meta {
  display: flex;
  gap: 10px;
  align-items: center;
  font-size: 12.5px;
}

.vf-type {
  width: 90px;
}

.vf-enabled {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.vf-title {
  color: #2da7ea;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.vf-empty {
  font-size: 12.5px;
  color: var(--md-text-secondary, #94a3b8);
}

<style scoped>
/* ============ 发现页复刻扩展样式（对齐参考实现 media_discovery.css 关键参数） ============ */

/* 目录源等待/缓存提示 */
.md-catalog-pending {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 46px 20px;
  border-radius: 17px;
  background: rgba(99, 102, 241, 0.06);
  border: 1px dashed rgba(99, 102, 241, 0.28);
  text-align: center;
  margin-bottom: 14px;
}
.md-catalog-pending-icon { font-size: 30px; }
.md-catalog-pending strong { font-size: 16px; }
.md-catalog-pending p { margin: 0; font-size: 13px; opacity: 0.72; max-width: 420px; }

.md-library-cache-notice {
  border-radius: 12px;
  background: rgba(99, 102, 241, 0.07);
  border: 1px solid rgba(99, 102, 241, 0.22);
  color: var(--md-primary, #6366f1);
  padding: 9px 14px;
  font-size: 12.5px;
  margin-bottom: 12px;
}

/* 演员介绍面板 + 人物卡 */
.md-library-actors-intro {
  border-radius: 17px;
  background: rgba(99, 102, 241, 0.05);
  border: 1px solid rgba(99, 102, 241, 0.18);
  padding: 14px 18px;
  margin-bottom: 14px;
}
.md-actors-intro-text { margin: 6px 0 8px; font-size: 13px; opacity: 0.78; }
.md-actors-intro-tags { display: flex; gap: 8px; flex-wrap: wrap; }
.md-actors-intro-tags span {
  font-size: 11.5px;
  padding: 3px 10px;
  border-radius: 999px;
  background: rgba(99, 102, 241, 0.12);
}
.md-actor-tile { cursor: pointer; }
.md-actor-dept { opacity: 0.66; }
.md-actor-known {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.md-actor-link { color: var(--md-primary, #6366f1); font-weight: 700; }

/* Emby 徽章 chip（对齐参考实现 tone 配色） */
.md-emby-chip {
  position: absolute;
  top: 8px;
  right: 8px;
  z-index: 2;
  font-size: 10.5px;
  font-weight: 800;
  padding: 3px 8px;
  border-radius: 999px;
  color: #fff;
  letter-spacing: 0.02em;
  text-shadow: 0 1px 2px rgba(15, 23, 42, 0.4);
}
.md-emby-chip.is-complete { background: #10b981; }
.md-emby-chip.is-subscribing { background: #f59e0b; }
.md-emby-chip.is-missing { background: #ef4444; }
.md-emby-chip.is-not-found { background: rgba(15, 23, 42, 0.72); }
.md-emby-chip.is-error { background: #94a3b8; }

/* 全页详情（对齐参考实现 md-work-detail / hero 362px） */
.md-detail-fade-enter-active, .md-detail-fade-leave-active { transition: opacity 0.22s ease; }
.md-detail-fade-enter-from, .md-detail-fade-leave-to { opacity: 0; }

.md-work-detail {
  position: fixed;
  inset: 0;
  z-index: 60;
  overflow-y: auto;
  background: var(--md-bg, #0b1020);
  padding: 18px clamp(16px, 4vw, 42px) 60px;
}
.md-work-detail-nav {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 16px;
}
.md-work-detail-crumb { font-size: 12.5px; opacity: 0.6; }
.md-detail-loading { padding: 60px 0; }

.md-work-hero {
  position: relative;
  min-height: 362px;
  border-radius: 25px;
  overflow: hidden;
  display: flex;
  align-items: flex-end;
  background:
    linear-gradient(135deg, #111c36 0%, #152849 55%, #202564 100%);
  background-size: cover;
  background-position: center 22%;
  margin-bottom: 22px;
}
.md-work-hero-mask {
  position: absolute;
  inset: 0;
  background: linear-gradient(90deg, rgba(9, 14, 32, 0.88) 8%, rgba(9, 14, 32, 0.42) 58%, rgba(9, 14, 32, 0.2));
}
.md-work-hero-body {
  position: relative;
  display: flex;
  gap: 26px;
  padding: 34px;
  align-items: flex-end;
  width: 100%;
}
.md-work-hero-poster {
  flex: 0 0 194px;
  aspect-ratio: 2 / 2.9;
  border-radius: 16px;
  overflow: hidden;
  box-shadow: 0 18px 44px rgba(2, 6, 23, 0.55);
  background: rgba(255, 255, 255, 0.06);
}
.md-work-hero-poster img { width: 100%; height: 100%; object-fit: cover; }
.md-work-hero-main { flex: 1; min-width: 0; }
.md-work-hero-main h2 {
  margin: 6px 0 10px;
  font-size: clamp(30px, 4vw, 48px);
  line-height: 1.12;
  letter-spacing: -0.5px;
}
.md-work-hero-main h2 small { font-size: 0.5em; opacity: 0.66; font-weight: 600; }
.md-work-hero-meta { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 10px; }
.md-work-score {
  font-weight: 900;
  color: #fde68a;
  background: rgba(146, 92, 9, 0.2);
  border: 1px solid rgba(251, 191, 36, 0.28);
  border-radius: 9px;
  padding: 3px 10px;
}
.md-work-tagline { font-style: italic; opacity: 0.72; margin: 4px 0; }
.md-work-overview {
  margin: 8px 0 12px;
  max-width: 760px;
  font-size: 13.5px;
  line-height: 1.75;
  opacity: 0.86;
  display: -webkit-box;
  -webkit-line-clamp: 4;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.md-work-genres { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 12px; }
.md-work-genres.is-aside { margin: 10px 0 0; }
.md-work-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.md-work-actions .is-fav-active { border-color: #f59e0b; color: #fbbf24; }
.md-sub-status { font-size: 12.5px; opacity: 0.75; }

.md-work-columns {
  display: grid;
  grid-template-columns: 300px minmax(0, 1fr);
  gap: 20px;
  align-items: start;
}
.md-work-facts { margin: 0; display: grid; gap: 9px; }
.md-work-facts > div { display: flex; justify-content: space-between; gap: 14px; border-bottom: 1px dashed rgba(148, 163, 184, 0.16); padding-bottom: 7px; }
.md-work-facts dt { opacity: 0.55; font-size: 12.5px; }
.md-work-facts dd { margin: 0; font-weight: 700; font-size: 13px; text-align: right; }
.md-work-facts-empty { font-size: 12.5px; opacity: 0.55; }

.md-season-list { display: grid; gap: 12px; }
.md-season-item { display: flex; gap: 14px; align-items: flex-start; }
.md-season-item img { width: 64px; border-radius: 10px; }
.md-season-copy strong { display: block; }
.md-season-copy small { opacity: 0.6; }
.md-season-copy p { margin: 4px 0 0; font-size: 12.5px; opacity: 0.72; }

/* 资源筛选行（对齐参考实现两行 chip + 计数） */
.md-resource-filter-rows { display: grid; gap: 8px; margin: 10px 0; }
.md-resource-filter-row { display: flex; align-items: center; flex-wrap: wrap; gap: 7px; }

/* 订阅弹窗（对齐参考实现规则编辑器） */
.md-sub-callout {
  border-radius: 12px;
  background: rgba(99, 102, 241, 0.08);
  border: 1px solid rgba(99, 102, 241, 0.22);
  padding: 11px 14px;
  font-size: 13px;
  margin-bottom: 14px;
}
.md-sub-field-row { display: flex; align-items: center; gap: 14px; margin-bottom: 14px; }
.md-sub-inline { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; }
.md-sub-rule-card {
  border: 1px solid rgba(148, 163, 184, 0.2);
  border-radius: 14px;
  padding: 14px;
  margin-bottom: 12px;
  background: rgba(148, 163, 184, 0.04);
}
.md-sub-rule-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; }
.md-sub-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
  gap: 10px 14px;
  margin-bottom: 10px;
}
.md-sub-grid label, .md-sub-match label {
  display: flex;
  flex-direction: column;
  gap: 5px;
  font-size: 12px;
  opacity: 0.85;
}
.md-sub-match { display: grid; gap: 8px; border-top: 1px dashed rgba(148, 163, 184, 0.2); padding-top: 10px; }
.md-sub-match-hint { margin: 0 0 2px; font-size: 12px; opacity: 0.6; }

/* 基础配置：脏标 + 频道/目录卡 + Emby 测试 */
.is-dirty { border-color: rgba(245, 158, 11, 0.5) !important; }
.md-dirty-badge {
  font-size: 11px;
  font-weight: 800;
  color: #f59e0b;
  border: 1px solid rgba(245, 158, 11, 0.4);
  border-radius: 999px;
  padding: 3px 10px;
}
.md-field-hint { font-size: 12px; opacity: 0.6; margin: 4px 0; }
.md-channel-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 14px; }
.md-channel-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  border: 1px solid rgba(148, 163, 184, 0.18);
  border-radius: 14px;
  padding: 13px;
}
.md-channel-card small { opacity: 0.55; }
.md-channel-textarea { font-family: inherit; min-height: 96px; resize: vertical; }
.md-emby-test-row { display: flex; align-items: center; gap: 12px; margin-top: 10px; flex-wrap: wrap; }

/* 缺集扫描面板 */
.md-missing-status-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 12px; }
.md-missing-progress { display: grid; gap: 6px; margin-bottom: 14px; }
.md-missing-progress-bar {
  height: 8px;
  border-radius: 999px;
  background: rgba(148, 163, 184, 0.18);
  overflow: hidden;
}
.md-missing-progress-fill {
  height: 100%;
  border-radius: 999px;
  background: linear-gradient(90deg, var(--md-primary, #6366f1), #7c3aed);
  transition: width 0.5s ease;
}
.md-missing-libraries { margin-bottom: 14px; }
.md-missing-libraries-head { display: flex; flex-direction: column; gap: 3px; margin-bottom: 8px; }
.md-missing-libraries-head small { opacity: 0.6; font-size: 12px; }
.md-missing-library-grid { display: flex; flex-wrap: wrap; gap: 7px; }
.md-missing-results-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
.md-missing-results-actions { display: flex; gap: 8px; align-items: center; }
.md-missing-result-list { display: grid; gap: 10px; max-height: 460px; overflow-y: auto; }
.md-missing-result-card {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  border: 1px solid rgba(148, 163, 184, 0.16);
  border-radius: 13px;
  padding: 11px 13px;
}
.md-missing-result-main { flex: 1; min-width: 0; display: grid; gap: 6px; }
.md-missing-result-meta { display: flex; flex-wrap: wrap; gap: 6px; }
.md-missing-episodes { display: flex; flex-wrap: wrap; gap: 5px; }
.md-missing-check { padding-top: 3px; }
.md-missing-events { display: grid; gap: 7px; margin-top: 16px; border-top: 1px dashed rgba(148, 163, 184, 0.2); padding-top: 12px; }
.md-missing-event { display: flex; align-items: center; gap: 10px; }
.md-missing-event small { opacity: 0.72; }

@media (max-width: 900px) {
  .md-work-columns { grid-template-columns: 1fr; }
  .md-work-hero-body { flex-direction: column; align-items: flex-start; }
  .md-work-hero-poster { flex-basis: auto; width: 150px; }
}
</style>