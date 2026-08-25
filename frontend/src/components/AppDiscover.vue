<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import { CircleCheck, Refresh } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'

// 影视发现：复刻 tgto123 media_discovery
// 影视探索 / 榜单推荐 / 追剧日历 / 番剧目录 / 收藏 / 基础配置

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

const activeTab = ref('explore')
const loading = ref(false)
const errorMessage = ref('')
const page = ref(1)
const totalPages = ref(1)

// 元数据（筛选器选项）
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

// ------------------------- 影视探索 -------------------------
const exploreSource = ref<'tmdb' | 'douban'>('tmdb')
const exploreMediaType = ref<'movie' | 'tv'>('movie')
const exploreGenre = ref('')
const exploreYear = ref('')
const exploreRegion = ref('')
const exploreSort = ref('popular')
const exploreDoubanTag = ref('热门')
const items = ref<DiscoverItem[]>([])

const sortOptions = [
  { value: 'popular', label: '热门' },
  { value: 'latest', label: '最新' },
  { value: 'rating', label: '高分' },
]

const currentGenres = () =>
  exploreMediaType.value === 'tv' ? meta.value.genres_tv : meta.value.genres_movie

// ------------------------- 榜单推荐 -------------------------
const rankingProvider = ref('hdhive')
const rankingRegion = ref('US')
const rankingMediaType = ref<'movie' | 'tv'>('movie')

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

// ------------------------- 追剧日历 -------------------------
const calendarDaysList = ref<CalendarDay[]>([])
const calendarDays = ref(30)
const calendarKind = ref('all')

const calendarKindOptions = [
  { value: 'all', label: '全部' },
  { value: 'tv', label: '剧集' },
  { value: 'movie', label: '电影' },
  { value: 'upcoming', label: '即将播出' },
  { value: 'on-air', label: '播出中' },
  { value: 'airing-today', label: '今日播出' },
]

// ------------------------- 番剧目录 -------------------------
const animeMode = ref<'calendar' | 'search'>('calendar')
const animeWeekdays = ref<CalendarDay[]>([])
const animeKeyword = ref('')
const animeSource = ref('bangumi')
const animeItems = ref<DiscoverItem[]>([])
const animeSearching = ref(false)

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

// ------------------------- 搜索与订阅（沿用原发现页能力）-------------------------
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

const applyResult = (data: PageResult | null) => {
  items.value = data?.items || []
  totalPages.value = Math.max(data?.total_pages || 1, 1)
}

const load = async (force = false) => {
  loading.value = true
  errorMessage.value = ''
  try {
    let url = ''
    if (activeTab.value === 'explore') {
      if (exploreSource.value === 'douban') {
        url = `${SERVER_URL}/media-discovery/explore/douban?type=${exploreMediaType.value}&tag=${encodeURIComponent(exploreDoubanTag.value)}&page=${page.value}`
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
    } else if (activeTab.value === 'rankings') {
      const params = new URLSearchParams({
        provider: rankingProvider.value,
        region: rankingRegion.value,
        media_type: rankingMediaType.value,
        page: String(page.value),
      })
      if (force) params.set('force', 'true')
      url = `${SERVER_URL}/media-discovery/rankings?${params.toString()}`
    }
    if (!url) return
    const response = await http.get(url)
    if (response?.data?.code === 200) {
      applyResult(response.data.data)
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
  afterItemsLoaded()
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
    const params = new URLSearchParams({
      keyword,
      source: animeSource.value,
      page: '1',
    })
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

// 番剧条目一键匹配 TMDB
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
  const keys = (items.value || [])
    .map((i) => i.entity_key)
    .filter((k): k is string => !!k)
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
      // 已收藏 → 从收藏列表找到对应记录删除
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

const afterItemsLoaded = () => {
  checkEmbyStatus()
  refreshFavStatus()
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
const changePage = (p: number) => {
  page.value = p
  load()
}

const reloadCurrentTab = () => {
  if (activeTab.value === 'calendar') loadCalendar(true)
  else if (activeTab.value === 'anime') loadAnime(true)
  else if (activeTab.value === 'favorites') loadFavorites()
  else load(true)
}

watch(activeTab, () => {
  searchMode.value = false
  searchKeyword.value = ''
  errorMessage.value = ''
  page.value = 1
  if (activeTab.value === 'calendar') loadCalendar()
  else if (activeTab.value === 'anime') loadAnime()
  else if (activeTab.value === 'favorites') loadFavorites()
  else if (activeTab.value === 'settings') loadSettings()
})

watch(exploreMediaType, () => {
  exploreGenre.value = ''
  exploreDoubanTag.value = '热门'
  page.value = 1
  if (!searchMode.value) load()
})

const posterUrl = (item: { poster: string }) => item.poster || ''

const formatVote = (vote: number) => (vote > 0 ? vote.toFixed(1) : '')

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

onMounted(() => {
  loadMeta()
  loadSubscribed()
  loadFavorites()
  load()
})
</script>

<template>
  <div class="discover-container">
    <div class="page-header">
      <div class="header-content">
        <h1>影视发现</h1>
        <p>影视探索 · 榜单推荐 · 追剧日历 · 番剧目录，支持收藏与个性化配置。</p>
      </div>
      <div class="search-box">
        <el-input
          v-if="activeTab === 'explore'"
          v-model="searchKeyword"
          placeholder="搜索影片名称"
          clearable
          style="width: 220px"
          @keyup.enter="onSearch"
          @clear="clearSearch"
        />
        <el-button v-if="activeTab === 'explore' && !searchMode" type="primary" :loading="searching" @click="onSearch">搜索</el-button>
        <el-button v-else-if="activeTab === 'explore'" type="warning" @click="clearSearch">返回榜单</el-button>
        <el-button :icon="Refresh" circle title="强制刷新（跳过缓存）" @click="reloadCurrentTab" />
      </div>
    </div>

    <el-tabs v-model="activeTab" class="discover-tabs">
      <el-tab-pane label="影视探索" name="explore" />
      <el-tab-pane label="榜单推荐" name="rankings" />
      <el-tab-pane label="追剧日历" name="calendar" />
      <el-tab-pane label="番剧目录" name="anime" />
      <el-tab-pane label="收藏" name="favorites" />
      <el-tab-pane label="基础配置" name="settings" />
    </el-tabs>

    <!-- 影视探索筛选栏 -->
    <div v-if="activeTab === 'explore'" class="category-bar">
      <el-radio-group v-model="exploreSource" style="margin-right: 16px">
        <el-radio-button value="tmdb">TMDB</el-radio-button>
        <el-radio-button value="douban">豆瓣</el-radio-button>
      </el-radio-group>
      <el-radio-group v-model="exploreMediaType" style="margin-right: 16px">
        <el-radio-button value="movie">电影</el-radio-button>
        <el-radio-button value="tv">剧集</el-radio-button>
      </el-radio-group>
      <template v-if="exploreSource === 'tmdb'">
        <el-select v-model="exploreGenre" placeholder="全部类型" clearable style="width: 130px; margin-right: 12px">
          <el-option v-for="(label, key) in currentGenres()" :key="key" :label="label" :value="label" />
        </el-select>
        <el-input v-model="exploreYear" placeholder="年份" clearable style="width: 100px; margin-right: 12px" @keyup.enter="page = 1; load()" />
        <el-select v-model="exploreRegion" placeholder="地区" clearable style="width: 110px; margin-right: 12px">
          <el-option label="华语" value="zh-CN" /><el-option label="美国" value="en-US" />
          <el-option label="日本" value="ja-JP" /><el-option label="韩国" value="ko-KR" />
        </el-select>
        <el-radio-group v-model="exploreSort">
          <el-radio-button v-for="opt in sortOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</el-radio-button>
        </el-radio-group>
      </template>
      <template v-else>
        <el-select v-model="exploreDoubanTag" style="width: 140px">
          <el-option v-for="tag in (exploreMediaType === 'tv' ? meta.douban_tags.tv : meta.douban_tags.movie) || []" :key="tag" :label="tag" :value="tag" />
        </el-select>
      </template>
    </div>

    <!-- 榜单推荐筛选栏 -->
    <div v-else-if="activeTab === 'rankings'" class="category-bar">
      <el-select v-model="rankingProvider" style="width: 180px; margin-right: 12px" @change="page = 1; load()">
        <el-option label="── 影巢流媒体榜 ──" value="__group_hive" disabled />
        <el-option label="默认平台（按设置）" value="hdhive" />
        <el-option v-for="p in meta.providers" :key="p.key" :label="'　' + p.label" :value="'hdhive:' + p.key" />
        <el-option label="── TMDB 分类榜 ──" value="__group_tmdb" disabled />
        <el-option v-for="opt in tmdbRankingOptions" :key="opt.value" :label="'　' + opt.label" :value="opt.value" />
        <el-option label="── 豆瓣片单 ──" value="__group_douban" disabled />
        <el-option v-for="col in meta.collections" :key="col.key" :label="'　' + col.label" :value="'douban:' + col.key" />
      </el-select>
      <el-radio-group v-model="rankingMediaType" style="margin-right: 12px" @change="page = 1; load()">
        <el-radio-button value="movie">电影</el-radio-button>
        <el-radio-button value="tv">剧集</el-radio-button>
      </el-radio-group>
      <el-select v-if="rankingProvider.startsWith('hdhive')" v-model="rankingRegion" style="width: 120px" @change="load()">
        <el-option v-for="r in meta.regions" :key="r.key" :label="r.label" :value="r.key" />
      </el-select>
    </div>

    <!-- 追剧日历筛选栏 -->
    <div v-else-if="activeTab === 'calendar'" class="category-bar">
      <el-radio-group v-model="calendarKind" style="margin-right: 16px" @change="loadCalendar()">
        <el-radio-button v-for="opt in calendarKindOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</el-radio-button>
      </el-radio-group>
      <span class="filter-label">天数</span>
      <el-input-number v-model="calendarDays" :min="1" :max="30" style="width: 110px; margin-right: 12px" @change="loadCalendar()" />
    </div>

    <!-- 番剧目录工具栏 -->
    <div v-else-if="activeTab === 'anime'" class="category-bar">
      <el-input
        v-model="animeKeyword"
        placeholder="搜索番剧名称"
        clearable
        style="width: 240px"
        @keyup.enter="onAnimeSearch"
      >
        <template #append>
          <el-select v-model="animeSource" style="width: 100px">
            <el-option label="Bangumi" value="bangumi" />
            <el-option label="AniList" value="anilist" />
          </el-select>
        </template>
      </el-input>
      <el-button type="primary" :loading="animeSearching" @click="onAnimeSearch">搜索</el-button>
      <el-button v-if="animeMode === 'search'" type="warning" @click="clearAnimeSearch">返回放送日历</el-button>
    </div>

    <div v-if="errorMessage" class="error-tip">
      <el-alert :title="errorMessage" type="warning" show-icon :closable="false" />
    </div>

    <!-- 追剧日历视图 -->
    <div v-if="activeTab === 'calendar'" v-loading="loading" class="grid-wrap">
      <div v-for="day in calendarDaysList" :key="day.date" class="cal-day-section">
        <div class="cal-day-header">
          <span class="cal-date">{{ day.date }}</span>
          <span class="cal-label">{{ day.label }}</span>
          <span class="cal-count">{{ day.items.length }} 部</span>
        </div>
        <div class="card-grid cal-grid">
          <div v-for="ep in day.items" :key="day.date + (ep.entity_key || '') + ep.episode_title" class="movie-card" @click="openDetail(ep)">
            <div class="poster-wrap">
              <img v-if="posterUrl(ep)" :src="posterUrl(ep)" class="poster" loading="lazy" alt="" />
              <div v-else class="poster poster-placeholder"><span>{{ ep.title.slice(0, 4) }}</span></div>
              <div v-if="formatVote(ep.vote_avg)" class="score-badge">{{ formatVote(ep.vote_avg) }}</div>
              <div v-if="airTimeOf(ep)" class="time-badge">{{ airTimeOf(ep) }}</div>
            </div>
            <div class="card-title" :title="ep.title">{{ ep.title }}</div>
            <div class="card-sub">
              {{ epLabelOf(ep) }}<template v-if="ep.episode_title"> {{ ep.episode_title }}</template>
            </div>
          </div>
        </div>
      </div>
      <div v-if="!loading && !calendarDaysList.length && !errorMessage" class="empty-tip"><el-empty description="暂无播出安排" /></div>
    </div>

    <!-- 番剧目录视图 -->
    <div v-else-if="activeTab === 'anime'" v-loading="loading || animeSearching" class="grid-wrap">
      <template v-if="animeMode === 'calendar'">
        <div v-for="wd in animeWeekdays" :key="wd.date" class="cal-day-section">
          <div class="cal-day-header">
            <span class="cal-label">{{ wd.label }}</span>
            <span class="cal-count">{{ wd.items.length }} 部</span>
          </div>
          <div class="card-grid cal-grid">
            <div v-for="item in wd.items" :key="wd.date + item.entity_key" class="movie-card" @click="openDetail(item)">
              <div class="poster-wrap">
                <img v-if="posterUrl(item)" :src="posterUrl(item)" class="poster" loading="lazy" alt="" />
                <div v-else class="poster poster-placeholder"><span>{{ item.title.slice(0, 4) }}</span></div>
                <div v-if="formatVote(item.vote_avg)" class="score-badge">{{ formatVote(item.vote_avg) }}</div>
                <div class="heart-btn" title="匹配 TMDB 并保存" @click.stop="matchAnimeTMDB(item)">
                  <svg viewBox="0 0 24 24" width="14" height="14"><path d="M12 2l2.4 4.9 5.4.8-3.9 3.8.9 5.4L12 14.4l-4.8 2.5.9-5.4L4.2 7.7l5.4-.8z" /></svg>
                </div>
              </div>
              <div class="card-title" :title="item.title">{{ item.title }}</div>
              <div class="card-sub" v-if="item.release_date">{{ item.release_date }}</div>
              <div class="card-sub" v-if="item.genres && item.genres.length">{{ item.genres.join(' / ') }}</div>
            </div>
          </div>
        </div>
        <div v-if="!loading && !animeWeekdays.length && !errorMessage" class="empty-tip"><el-empty description="暂无放送数据" /></div>
      </template>
      <template v-else>
        <div class="card-grid">
          <div v-for="item in animeItems" :key="item.entity_key" class="movie-card" @click="openDetail(item)">
            <div class="poster-wrap">
              <img v-if="posterUrl(item)" :src="posterUrl(item)" class="poster" loading="lazy" alt="" />
              <div v-else class="poster poster-placeholder"><span>{{ item.title.slice(0, 4) }}</span></div>
              <div v-if="formatVote(item.vote_avg)" class="score-badge">{{ formatVote(item.vote_avg) }}</div>
              <div class="heart-btn" :class="{ active: item.tmdb_id }" title="匹配 TMDB 并保存" @click.stop="matchAnimeTMDB(item)">
                <svg viewBox="0 0 24 24" width="14" height="14"><path d="M12 2l2.4 4.9 5.4.8-3.9 3.8.9 5.4L12 14.4l-4.8 2.5.9-5.4L4.2 7.7l5.4-.8z" /></svg>
              </div>
            </div>
            <div class="card-title" :title="item.title">{{ item.title }}</div>
            <div class="card-sub" v-if="item.original_title">{{ item.original_title }}</div>
            <div class="card-sub" v-if="item.genres && item.genres.length">{{ item.genres.join(' / ') }}</div>
          </div>
        </div>
        <div v-if="!animeSearching && !animeItems.length && !errorMessage" class="empty-tip"><el-empty description="无搜索结果" /></div>
      </template>
    </div>

    <!-- 收藏视图 -->
    <div v-else-if="activeTab === 'favorites'" v-loading="loading" class="grid-wrap">
      <div class="card-grid">
        <div v-for="fav in favoriteItems" :key="fav.id" class="movie-card" @click="openDetail(fav as any)">
          <div class="poster-wrap">
            <img v-if="fav.poster" :src="fav.poster" class="poster" loading="lazy" alt="" />
            <div v-else class="poster poster-placeholder"><span>{{ fav.title.slice(0, 4) }}</span></div>
            <div v-if="formatVote(fav.vote_avg)" class="score-badge">{{ formatVote(fav.vote_avg) }}</div>
            <div class="heart-btn active busy" title="删除收藏" @click.stop="removeFavorite(fav)">
              <svg viewBox="0 0 24 24" width="14" height="14"><path d="M6 7h12l-1 13H7L6 7zm3-3h6l1 2h4v2H4V6h4l1-2z" /></svg>
            </div>
          </div>
          <div class="card-title" :title="fav.title">{{ fav.title }}</div>
          <div class="card-sub">{{ fav.source }}{{ fav.year ? ' · ' + fav.year : '' }}</div>
        </div>
      </div>
      <div v-if="!loading && !favoriteItems.length" class="empty-tip"><el-empty description="暂无收藏，去影视探索点星标收藏吧" /></div>
    </div>

    <!-- 基础配置视图 -->
    <div v-else-if="activeTab === 'settings'" class="settings-panel">
      <el-form label-width="160px" class="settings-form">
        <el-form-item label="默认探索来源">
          <el-radio-group v-model="settingsForm.default_explore_source">
            <el-radio-button value="tmdb">TMDB</el-radio-button>
            <el-radio-button value="douban">豆瓣</el-radio-button>
            <el-radio-button value="anime">番剧</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="默认排序">
          <el-radio-group v-model="settingsForm.default_explore_sort">
            <el-radio-button v-for="opt in sortOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="追剧日历天数">
          <el-input-number v-model="settingsForm.calendar_days" :min="1" :max="60" />
        </el-form-item>
        <el-form-item label="追剧日历类型">
          <el-select v-model="settingsForm.calendar_kind" style="width: 180px">
            <el-option v-for="opt in calendarKindOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="流媒体榜单地区">
          <el-select v-model="settingsForm.ranking_region" style="width: 180px">
            <el-option v-for="r in meta.regions" :key="r.key" :label="r.label" :value="r.key" />
          </el-select>
        </el-form-item>
        <el-form-item label="流媒体平台">
          <el-select v-model="settingsForm.ranking_provider" style="width: 180px">
            <el-option v-for="p in meta.providers" :key="p.key" :label="p.label" :value="p.key" />
          </el-select>
        </el-form-item>
        <el-form-item label="榜单媒体类型">
          <el-radio-group v-model="settingsForm.ranking_media_type">
            <el-radio-button value="movie">电影</el-radio-button>
            <el-radio-button value="tv">剧集</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="缓存时长（分钟）">
          <el-input-number v-model="settingsForm.cache_ttl_minutes" :min="1" :max="1440" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="savingSettings" @click="saveSettings">保存设置</el-button>
        </el-form-item>
      </el-form>
    </div>

    <!-- 影视探索 / 榜单推荐网格 -->
    <div v-else v-loading="loading" class="grid-wrap">
      <div class="card-grid">
        <div v-for="item in items" :key="(item.entity_key || '') + item.source + item.tmdb_id + item.douban_id + item.title" class="movie-card" @click="openDetail(item)">
          <div class="poster-wrap">
            <img v-if="posterUrl(item)" :src="posterUrl(item)" class="poster" loading="lazy" alt="" />
            <div v-else class="poster poster-placeholder"><span>{{ item.title.slice(0, 4) }}</span></div>
            <div v-if="item.rank" class="rank-badge" :class="{ top: item.rank <= 3 }">{{ item.rank }}</div>
            <div v-if="item.in_emby" class="emby-badge" title="已入库 Emby"><el-icon><CircleCheck /></el-icon></div>
            <div
              v-if="item.tmdb_id"
              class="heart-btn"
              :class="{ active: isSubscribed(item), busy: subscribingIds[item.tmdb_id] }"
              :title="isSubscribed(item) ? '已订阅' : '点击订阅 MoviePilot'"
              @click.stop="toggleSubscribe(item)"
            >
              <svg viewBox="0 0 24 24" width="16" height="16">
                <path d="M12 21.35l-1.45-1.32C5.4 15.36 2 12.28 2 8.5 2 5.42 4.42 3 7.5 3c1.74 0 3.41.81 4.5 2.09C13.09 3.81 14.76 3 16.5 3 19.58 3 22 5.42 22 8.5c0 3.78-3.4 6.86-8.55 11.54L12 21.35z" />
              </svg>
            </div>
            <div
              v-if="item.entity_key"
              class="star-btn"
              :class="{ active: isFav(item), busy: favBusyKeys[item.entity_key] }"
              :title="isFav(item) ? '取消收藏' : '收藏'"
              @click.stop="toggleFavorite(item)"
            >
              <svg viewBox="0 0 24 24" width="15" height="15"><path d="M12 2l2.4 4.9 5.4.8-3.9 3.8.9 5.4L12 14.4l-4.8 2.5.9-5.4L4.2 7.7l5.4-.8z" /></svg>
            </div>
            <div v-if="formatVote(item.vote_avg)" class="score-badge">{{ formatVote(item.vote_avg) }}</div>
          </div>
          <div class="card-title" :title="item.title">{{ item.rank ? item.rank + '. ' : '' }}{{ item.title }}</div>
          <div class="card-sub" v-if="item.year || item.release_date">
            {{ item.year || (item.release_date || '').slice(0, 10) }}
            <template v-if="item.original_title && item.original_title !== item.title"> · {{ item.original_title }}</template>
          </div>
          <div class="card-sub" v-if="item.providers && item.providers.length">{{ item.providers.join(' / ') }}</div>
        </div>
      </div>
      <div v-if="!loading && !items.length && !errorMessage" class="empty-tip"><el-empty description="暂无数据" /></div>
      <div class="pager" v-if="items.length && !searchMode && activeTab !== 'rankings'">
        <el-pagination background layout="prev, pager, next" :total="totalPages * 20" :page-size="20" :current-page="page" @current-change="changePage" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.discover-container {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
  padding: 20px 24px;
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  border-radius: 16px;
  color: white;
  flex-wrap: wrap;
}

.search-box {
  display: flex;
  align-items: center;
  gap: 8px;
}

.header-content h1 {
  margin: 0 0 4px 0;
  font-size: 28px;
  font-weight: 700;
}

.header-content p {
  margin: 0;
  font-size: 14px;
  opacity: 0.9;
}

.discover-tabs {
  padding: 0 4px;
}

.category-bar {
  padding: 0 4px;
  overflow-x: auto;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.filter-label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.error-tip {
  padding: 0 4px;
}

.grid-wrap {
  min-height: 200px;
}

.card-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 16px;
  padding: 4px;
}

.cal-day-section {
  margin-bottom: 24px;
}

.cal-day-header {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 8px 4px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  margin-bottom: 12px;
}

.cal-date {
  font-size: 16px;
  font-weight: 700;
}

.cal-label {
  font-size: 14px;
  color: var(--el-color-primary);
  font-weight: 600;
}

.cal-count {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.movie-card {
  cursor: pointer;
  transition: transform 0.2s;
}

.movie-card:hover {
  transform: translateY(-4px);
}

.poster-wrap {
  position: relative;
  width: 100%;
  aspect-ratio: 2 / 3;
  border-radius: 10px;
  overflow: hidden;
  background: #e5e7eb;
}

.poster {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

.poster-placeholder {
  display: flex;
  align-items: center;
  justify-content: center;
  color: #9ca3af;
  font-size: 18px;
  font-weight: 600;
}

.emby-badge {
  position: absolute;
  left: 8px;
  bottom: 8px;
  width: 26px;
  height: 26px;
  border-radius: 50%;
  background: rgba(16, 185, 129, 0.95);
  color: white;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 16px;
  box-shadow: 0 2px 6px rgba(0, 0, 0, 0.3);
  z-index: 2;
}

.score-badge {
  position: absolute;
  right: 8px;
  bottom: 8px;
  padding: 2px 8px;
  border-radius: 8px;
  background: rgba(0, 0, 0, 0.65);
  color: #fbbf24;
  font-size: 13px;
  font-weight: 700;
  z-index: 2;
}

.time-badge {
  position: absolute;
  top: 8px;
  left: 8px;
  padding: 2px 8px;
  border-radius: 8px;
  background: rgba(0, 0, 0, 0.65);
  color: #93c5fd;
  font-size: 12px;
  font-weight: 600;
  z-index: 2;
}

.rank-badge {
  position: absolute;
  top: 8px;
  left: 8px;
  min-width: 24px;
  height: 24px;
  padding: 0 6px;
  border-radius: 8px;
  background: rgba(0, 0, 0, 0.65);
  color: white;
  font-size: 13px;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 2;
}

.rank-badge.top {
  background: rgba(245, 158, 11, 0.95);
  color: #fff;
}

.heart-btn,
.star-btn {
  position: absolute;
  top: 8px;
  right: 8px;
  width: 30px;
  height: 30px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.45);
  color: #fff;
  cursor: pointer;
  transition: all 0.2s ease;
  z-index: 3;
  opacity: 0.85;
}
.star-btn {
  top: 44px;
}
.heart-btn:hover,
.star-btn:hover {
  transform: scale(1.12);
  opacity: 1;
}
.heart-btn.active {
  background: rgba(245, 63, 63, 0.9);
}
.star-btn.active {
  background: rgba(250, 204, 21, 0.92);
  color: #78350f;
}
.heart-btn.busy,
.star-btn.busy {
  pointer-events: none;
  opacity: 0.5;
}
.heart-btn svg,
.star-btn svg {
  fill: currentColor;
}

.card-title {
  margin-top: 8px;
  font-size: 14px;
  font-weight: 600;
  line-height: 1.3;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.card-sub {
  margin-top: 2px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pager {
  display: flex;
  justify-content: center;
  padding: 16px 0;
}

.empty-tip {
  padding: 40px 0;
}

.settings-panel {
  padding: 8px 4px;
}

.settings-form {
  max-width: 560px;
}
</style>
