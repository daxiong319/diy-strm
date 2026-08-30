<script setup lang="ts">
import { ref, computed, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import { CircleCheck } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'

// 影视发现：复刻 tgto123 media_discovery 分区式布局
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
}>({
  genres_movie: {},
  genres_tv: {},
  providers: [],
  regions: [],
  collections: [],
  douban_tags: {},
  default_source: 'tmdb',
})

const currentYear = new Date().getFullYear()
const yearOptions = ['', ...Array.from({ length: currentYear - 2006 }, (_, i) => String(currentYear - i))]

const loading = ref(false)
const errorMessage = ref('')

// ------------------------- 影视探索 -------------------------
const librarySources = [
  { key: 'tmdb', label: '影巢片库' },
  { key: 'douban', label: '豆瓣' },
  { key: 'anime', label: '番剧' },
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
const items = ref<DiscoverItem[]>([])

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
      if (entries[0]?.isIntersecting && !loading.value && page.value < totalPages.value) {
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

// ------------------------- 番剧 -------------------------
const animeWeekdays = ref<CalendarDay[]>([])
const animeKeyword = ref('')
const animeSource = ref('bangumi')
const animeItems = ref<DiscoverItem[]>([])
const animeSearching = ref(false)
const animeMode = ref<'calendar' | 'search'>('calendar')

// ------------------------- 收藏 -------------------------
const favoriteItems = ref<DiscoveryFavorite[]>([])
const favKeySet = ref<Record<string, boolean>>({})

// ------------------------- 基础配置 -------------------------
const settingsForm = ref<Record<string, any>>({
  default_explore_source: 'tmdb',
  default_explore_sort: 'popular',
  calendar_days: 30,
  calendar_kind: 'all',
  ranking_region: 'US',
  ranking_provider: 'netflix',
  ranking_media_type: 'movie',
  match_douban_tmdb: true,
  emby_check_enabled: false,
  cache_ttl_minutes: 30,
})
const savingSettings = ref(false)

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

const load = async (force = false, append = false) => {
  loading.value = true
  errorMessage.value = ''
  try {
    let url = ''
    if (librarySource.value === 'douban') {
      url = `${SERVER_URL}/media-discovery/explore/douban?type=${exploreMediaType.value}&tag=${encodeURIComponent(exploreDoubanTag.value)}&page=${page.value}${force ? '&force=true' : ''}`
    } else {
      const params = new URLSearchParams({
        type: exploreMediaType.value,
        genre: exploreGenre.value,
        year: exploreYear.value,
        region: exploreRegion.value,
        sort_by: exploreSort.value,
        page: String(page.value),
      })
      if (force) params.set('force', 'true')
      url = `${SERVER_URL}/media-discovery/explore?${params.toString()}`
    }
    const response = await http.get(url)
    if (response?.data?.code === 200) {
      const data: PageResult | null = response.data.data
      const fresh = data?.items || []
      if (append) {
        const known = new Set(items.value.map((i) => `${i.source}:${i.entity_key || i.tmdb_id || i.title}`))
        items.value = [
          ...items.value,
          ...fresh.filter((i) => !known.has(`${i.source}:${i.entity_key || i.tmdb_id || i.title}`)),
        ]
      } else {
        items.value = fresh
      }
      totalPages.value = Math.max(data?.total_pages || 1, 1)
    } else {
      errorMessage.value = response?.data?.message || '加载失败'
      items.value = []
    }
  } catch (err) {
    errorMessage.value = '加载失败：' + (err as Error).message
    items.value = []
  } finally {
    loading.value = false
  }
  if (!append) afterItemsLoaded()
}

const afterItemsLoaded = () => {
  checkEmbyStatus()
  refreshFavStatus()
}

const loadRankings = async (force = false) => {
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

const loadAnime = async (force = false) => {
  if (animeMode.value === 'search') return
  loading.value = true
  errorMessage.value = ''
  try {
    const response = await http.get(`${SERVER_URL}/media-discovery/anime/calendar${force ? '?force=true' : ''}`)
    if (response?.data?.code === 200) {
      animeWeekdays.value = response.data.data || []
    } else {
      errorMessage.value = response?.data?.message || '加载失败'
      animeWeekdays.value = []
    }
  } catch (err) {
    errorMessage.value = '加载失败：' + (err as Error).message
    animeWeekdays.value = []
  } finally {
    loading.value = false
  }
}

const onAnimeSearch = async () => {
  const keyword = animeKeyword.value.trim()
  if (!keyword) {
    ElMessage.warning('请输入番剧关键词')
    return
  }
  animeMode.value = 'search'
  animeSearching.value = true
  errorMessage.value = ''
  try {
    const params = new URLSearchParams({ keyword, source: animeSource.value, page: '1' })
    const response = await http.get(`${SERVER_URL}/media-discovery/anime/search?${params.toString()}`, { timeout: 30000 })
    if (response?.data?.code === 200) {
      animeItems.value = response.data.data?.items || []
    } else {
      errorMessage.value = response?.data?.message || '搜索失败'
      animeItems.value = []
    }
  } catch (err) {
    errorMessage.value = '搜索失败：' + (err as Error).message
    animeItems.value = []
  } finally {
    animeSearching.value = false
  }
}

const clearAnimeSearch = () => {
  animeMode.value = 'calendar'
  animeKeyword.value = ''
  animeItems.value = []
  loadAnime()
}

const matchAnimeTMDB = async (item: DiscoverItem) => {
  if (!item.entity_key) return
  try {
    const response = await http.post(`${SERVER_URL}/media-discovery/anime/match`, { entity_key: item.entity_key })
    if (response?.data?.code === 200) {
      ElMessage.success(`已匹配 TMDB：${response.data.data?.tmdb_id}`)
      item.tmdb_id = Number(response.data.data?.tmdb_id || 0)
    } else {
      ElMessage.error(response?.data?.message || '匹配失败')
    }
  } catch (err) {
    ElMessage.error('匹配失败：' + (err as Error).message)
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

const checkEmbyStatus = async () => {
  if (!items.value.length) return
  embyCheckBusy.value = true
  try {
    const payload = items.value
      .filter((item) => item.tmdb_id)
      .map((item) => ({ tmdb_id: item.tmdb_id, title: item.title, year: item.year || 0 }))
    if (!payload.length) return
    const response = await http.post(`${SERVER_URL}/discover/emby-check`, { items: payload })
    if (response && response.data && response.data.code === 200) {
      const resultMap = new Map<number, boolean>()
      for (const result of response.data.data) {
        resultMap.set(result.tmdb_id, result.in_emby)
      }
      items.value = items.value.map((item) => ({
        ...item,
        in_emby: item.tmdb_id ? !!resultMap.get(item.tmdb_id!) : false,
      }))
    }
  } catch {
    // Emby 未配置或检测失败时静默跳过
  } finally {
    embyCheckBusy.value = false
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
  try {
    const isTv = exploreMediaType.value === 'tv'
    const params: Record<string, string | number> = { name: keyword, type: isTv ? 'tvshow' : 'movie' }
    const response = await http.get(`${SERVER_URL}/scrape/tmdb-search`, { params, timeout: 30000 })
    const data = response?.data?.data
    const list = Array.isArray(data) ? data : data?.list || []
    items.value = list
      .filter((r: any) => r && (r.title || r.name) && r.tmdb_id)
      .map((r: any) => ({
        source: 'tmdb',
        media_type: isTv ? 'tv' : 'movie',
        entity_key: `tmdb:${isTv ? 'tv' : 'movie'}:${r.tmdb_id}`,
        tmdb_id: Number(r.tmdb_id),
        external_id: String(r.tmdb_id),
        title: r.title || r.name,
        original_title: r.original_title,
        poster: r.poster_url || '',
        vote_avg: Number(r.vote_average || 0),
        year: r.year || 0,
        in_emby: false,
      }))
    totalPages.value = 1
  } catch (err) {
    errorMessage.value = '搜索失败：' + (err as Error).message
    items.value = []
  } finally {
    searching.value = false
  }
  checkEmbyStatus()
}

const clearSearch = () => {
  searchMode.value = false
  searchKeyword.value = ''
  page.value = 1
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
      ElMessage.success('发现设置已保存')
    } else {
      ElMessage.error(response?.data?.message || '保存失败')
    }
  } catch (err) {
    ElMessage.error('保存失败：' + (err as Error).message)
  } finally {
    savingSettings.value = false
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

const airTimeOf = (item: DiscoverItem) => (item.air_date && item.air_date.length > 10 ? item.air_date.slice(11, 16) : '')

const epLabelOf = (item: DiscoverItem) => {
  if (item.season_number !== undefined && item.episode_number !== undefined && item.media_type === 'tv') {
    return `S${String(item.season_number).padStart(2, '0')}E${String(item.episode_number).padStart(2, '0')}`
  }
  return ''
}

const filterActive = computed(
  () =>
    exploreMediaType.value !== 'movie' ||
    !!exploreGenre.value ||
    !!exploreYear.value ||
    exploreRegion.value !== '' ||
    exploreSort.value !== 'popular' ||
    (librarySource.value === 'douban' && exploreDoubanTag.value !== '热门')
)

const resetLibraryFilters = () => {
  exploreMediaType.value = 'movie'
  exploreGenre.value = ''
  exploreYear.value = ''
  exploreRegion.value = ''
  exploreSort.value = 'popular'
  exploreDoubanTag.value = '热门'
  page.value = 1
  load()
}

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
  }
  nextTick(computeColumns)
}

watch(librarySource, () => {
  searchMode.value = false
  searchKeyword.value = ''
  errorMessage.value = ''
  page.value = 1
  if (librarySource.value === 'anime') {
    animeMode.value = 'calendar'
    loadAnime()
  } else if (librarySource.value === 'favorites') {
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
  if (!searchMode.value && librarySource.value !== 'anime' && librarySource.value !== 'favorites') load()
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

onMounted(async () => {
  loadMeta()
  loadSubscribed()
  loadFavorites()
  load()
  await nextTick()
  computeColumns()
  setupInfinite()
  window.addEventListener('resize', onWindowResize)
})

onBeforeUnmount(() => {
  io?.disconnect()
  window.removeEventListener('resize', onWindowResize)
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
        <p>按偏好探索影巢片库、豆瓣片单与番剧放送，收藏心仪作品并联动 MoviePilot 订阅下载。</p>
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
          <div v-if="librarySource !== 'favorites'" class="md-library-inline-search">
            <input
              v-model="searchKeyword"
              class="md-input"
              type="search"
              :placeholder="
                librarySource === 'anime'
                  ? '输入番剧名称，按 Enter 搜索'
                  : '输入电影或电视剧名称，按 Enter 搜索'
              "
              @keyup.enter="
                librarySource === 'anime' ? onAnimeSearch() : onSearch()
              "
            />
            <button
              v-if="librarySource !== 'anime' || animeMode === 'search'"
              class="md-btn is-primary"
              :disabled="searching || animeSearching || (!searchKeyword.trim() && librarySource !== 'anime')"
              @click="librarySource === 'anime' ? onAnimeSearch() : onSearch()"
            >
              搜索
            </button>
            <button
              v-if="searchMode || animeMode === 'search'"
              class="md-btn"
              @click="librarySource === 'anime' ? clearAnimeSearch() : clearSearch()"
            >
              清空
            </button>
          </div>
          <button v-if="librarySource === 'anime' && animeMode === 'calendar'" class="md-btn" @click="loadAnime(true)">↻ 刷新</button>
          <button v-else class="md-btn is-soft" @click="searchMode ? clearSearch() : (librarySource === 'favorites' ? loadFavorites() : load(true))">↻ 刷新</button>
        </div>
      </div>

      <!-- 筛选面板（TMDB / 豆瓣） -->
      <div v-if="librarySource === 'tmdb' || librarySource === 'douban'" class="md-library-filter-panel">
        <div class="md-library-filter-panel-head">
          <div>
            <span class="md-kicker">EXPLORE FILTERS</span>
            <strong>按偏好探索片库</strong>
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

        <!-- 豆瓣源 -->
        <template v-else>
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
                v-for="tag in (exploreMediaType === 'tv' ? meta.douban_tags.tv : meta.douban_tags.movie) || []"
                :key="tag"
                class="md-library-filter-chip"
                :class="{ 'is-active': exploreDoubanTag === tag }"
                @click="exploreDoubanTag = tag; page = 1; load()"
              >
                {{ tag }}
              </button>
            </div>
          </div>
        </template>
      </div>

      <div v-if="errorMessage" class="md-error-tip">{{ errorMessage }}</div>

      <!-- 番剧：放送日历 / 搜索结果 -->
      <template v-if="librarySource === 'anime'">
        <div v-if="animeMode === 'calendar'" v-loading="loading" class="md-calendar-shell md-anime-shell">
          <div class="md-calendar-head">
            <div>
              <span class="md-kicker">ANIME WEEKLY</span>
              <h3>番剧放送日历</h3>
            </div>
            <span class="md-head-note">Bangumi 每周放送 · 点击右下角匹配 TMDB</span>
          </div>
          <div v-for="wd in animeWeekdays" :key="wd.date" class="md-anime-weekday">
            <div class="md-anime-weekday-head">
              <span class="md-kicker">{{ wd.label }}</span>
              <span class="md-badge">{{ wd.items.length }} 部</span>
            </div>
            <div class="md-library-grid md-library-display-grid" :style="gridStyle">
              <article v-for="item in wd.items" :key="wd.date + item.entity_key" class="md-library-tile">
                <div class="md-library-tile-poster" @click="openDetail(item)">
                  <img v-if="posterUrl(item)" :src="posterUrl(item)" loading="lazy" alt="" />
                  <div v-else class="md-library-tile-placeholder">◉</div>
                  <span class="md-library-tile-kind">动漫</span>
                  <span v-if="formatVote(item.vote_avg)" class="md-library-tile-score">★ {{ formatVote(item.vote_avg) }}</span>
                  <button
                    class="md-tile-action md-tile-match"
                    :class="{ active: !!item.tmdb_id }"
                    title="匹配 TMDB 并保存"
                    @click.stop="matchAnimeTMDB(item)"
                  >
                    <svg viewBox="0 0 24 24" width="14" height="14"><path d="M12 21.35l-1.45-1.32C5.4 15.36 2 12.28 2 8.5 2 5.42 4.42 3 7.5 3c1.74 0 3.41.81 4.5 2.09C13.09 3.81 14.76 3 16.5 3 19.58 3 22 5.42 22 8.5c0 3.78-3.4 6.86-8.55 11.54L12 21.35z" /></svg>
                  </button>
                </div>
                <div class="md-library-tile-copy">
                  <strong :title="item.title">{{ item.title }}</strong>
                  <small v-if="item.release_date">{{ item.release_date.slice(0, 10) }}</small>
                  <small v-else-if="item.genres && item.genres.length">{{ item.genres.join(' / ') }}</small>
                </div>
              </article>
            </div>
          </div>
          <div v-if="!loading && !animeWeekdays.length && !errorMessage" class="md-state">😴 暂无放送数据</div>
        </div>
        <div v-else v-loading="animeSearching" class="md-section-block">
          <div class="md-library-grid md-library-display-grid" :style="gridStyle">
            <article v-for="item in animeItems" :key="item.entity_key" class="md-library-tile">
              <div class="md-library-tile-poster" @click="openDetail(item)">
                <img v-if="posterUrl(item)" :src="posterUrl(item)" loading="lazy" alt="" />
                <div v-else class="md-library-tile-placeholder">◉</div>
                <span class="md-library-tile-kind">动漫</span>
                <span v-if="formatVote(item.vote_avg)" class="md-library-tile-score">★ {{ formatVote(item.vote_avg) }}</span>
                <button
                  class="md-tile-action md-tile-match"
                  :class="{ active: !!item.tmdb_id }"
                  title="匹配 TMDB 并保存"
                  @click.stop="matchAnimeTMDB(item)"
                >
                  <svg viewBox="0 0 24 24" width="14" height="14"><path d="M12 21.35l-1.45-1.32C5.4 15.36 2 12.28 2 8.5 2 5.42 4.42 3 7.5 3c1.74 0 3.41.81 4.5 2.09C13.09 3.81 14.76 3 16.5 3 19.58 3 22 5.42 22 8.5c0 3.78-3.4 6.86-8.55 11.54L12 21.35z" /></svg>
                </button>
              </div>
              <div class="md-library-tile-copy">
                <strong :title="item.title">{{ item.title }}</strong>
                <small v-if="item.original_title">{{ item.original_title }}</small>
                <small v-else-if="item.genres && item.genres.length">{{ item.genres.join(' / ') }}</small>
              </div>
            </article>
          </div>
          <div v-if="!animeSearching && !animeItems.length && !errorMessage" class="md-state">🔍 无搜索结果</div>
        </div>
      </template>

      <!-- 收藏网格 -->
      <template v-else-if="librarySource === 'favorites'">
        <div class="md-section-block">
          <div class="md-library-grid md-library-display-grid" :style="gridStyle">
            <article v-for="fav in favoriteItems" :key="fav.id" class="md-library-tile">
              <div class="md-library-tile-poster" @click="openDetail(fav as any)">
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
        <div v-loading="loading" class="md-section-block">
          <div ref="gridEl" class="md-library-grid md-library-display-grid" :style="gridStyle">
            <article v-for="item in items" :key="(item.entity_key || '') + item.source + item.tmdb_id + item.douban_id + item.title" class="md-library-tile">
              <div class="md-library-tile-poster" @click="openDetail(item)">
                <img v-if="posterUrl(item)" :src="posterUrl(item)" loading="lazy" alt="" />
                <div v-else class="md-library-tile-placeholder">◉</div>
                <span v-if="!searchMode && item.rank" class="md-library-tile-rank">{{ item.rank }}</span>
                <span v-else class="md-library-tile-kind">{{ kindOf(item) }}</span>
                <span v-if="item.in_emby" class="md-emby-status is-complete" title="已入库 Emby"><el-icon><CircleCheck /></el-icon></span>
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
            <button class="md-btn" :disabled="page >= totalPages" @click="page++; load()">下一页 ›</button>
          </div>
          <button
            v-if="items.length && infiniteMode"
            ref="sentinelEl"
            class="md-library-infinite-scroll"
            :class="{ 'is-loading': loading }"
            @click="page < totalPages ? (page++, load(false, true)) : undefined"
          >
            {{ loading ? '正在加载更多…' : page < totalPages ? '继续下滑加载更多' : '已加载全部内容' }}
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
          <p>影巢流媒体榜聚合 Netflix、Disney+、Prime Video 等平台 Top 10，也支持 TMDB 分类榜与豆瓣片单。</p>
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
            class="md-streaming-tab"
            :class="{ 'is-active': rankingProvider === 'hdhive' }"
            @click="rankingProvider = 'hdhive'; loadRankings()"
          >
            <span class="md-streaming-tab-icon hive">影</span>
            <span class="md-streaming-tab-label">影巢流媒体榜</span>
          </button>
          <button
            v-for="p in meta.providers"
            :key="p.key"
            class="md-streaming-tab"
            :class="{ 'is-active': rankingProvider === 'hdhive:' + p.key }"
            :title="p.label"
            @click="rankingProvider = 'hdhive:' + p.key; loadRankings()"
          >
            <span class="md-streaming-tab-icon" :class="'brand-' + p.key">{{ p.label.slice(0, 1) }}</span>
            <span class="md-streaming-tab-label">{{ p.label }}</span>
          </button>
        </div>
        <div class="md-ranking-filter-row">
          <div class="md-ranking-type-tabs">
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
          <label class="md-ranking-country">
            扩展榜单
            <select
              v-model="rankingProvider"
              class="md-select"
              @change="rankingMediaType = rankingProvider.startsWith('hdhive') ? '' : 'movie'; loadRankings()"
            >
              <optgroup label="影巢流媒体榜">
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
            <span class="md-kicker">{{ rankingProvider.startsWith('hdhive') ? '影巢 · ' + rankingProviderLabel : rankingProviderLabel }}</span>
            <h3>{{ rankingProviderLabel }}</h3>
          </div>
          <span class="md-head-note">{{ rankingMediaType === '' ? '电影 + 剧集' : rankingKindLabel(rankingMediaType) }} · {{ rankingRegion }} · Top {{ Math.max(rankingTotal, 1) }}</span>
        </div>

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
              <div class="md-ranking-tile-poster" @click="openDetail(item)">
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

        <div v-if="!loading && !rankingGroups.length && !errorMessage" class="md-state">🏆 暂无榜单数据</div>
      </div>
    </section>

    <!-- ================= 追剧日历 ================= -->
    <section v-show="activeSection === 'calendar'" class="md-section">
      <div v-if="spotlightItem" class="md-calendar-spotlight">
        <div class="md-calendar-spotlight-backdrop" :style="spotlightItem.poster ? { backgroundImage: `url(${spotlightItem.poster})` } : {}"></div>
        <div class="md-calendar-spotlight-main">
          <span class="md-calendar-spotlight-kicker">影巢 · 未来播出</span>
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
              <div class="md-calendar-card-poster" @click="openDetail(ep)">
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
            </div>
          </div>
        </div>
      </form>
    </section>
  </div>
</template>

<style scoped>
/* ============ 令牌（对齐 tgto123 media_discovery 视觉） ============ */
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
</style>