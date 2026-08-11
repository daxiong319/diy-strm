<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import { CircleCheck } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'

interface DiscoverItem {
  source: string
  media_type: string
  tmdb_id?: number
  douban_id?: string
  title: string
  original_title?: string
  poster: string
  backdrop?: string
  overview?: string
  vote_avg: number
  release_date?: string
  year?: number
  in_emby: boolean
}

const http = useHttpClient()

const activeTab = ref('tmdb-movie')
const loading = ref(false)
const items = ref<DiscoverItem[]>([])
const errorMessage = ref('')
const page = ref(1)
const totalPages = ref(1)

const searchKeyword = ref('')
const searchMode = ref(false)
const searching = ref(false)
const subscribingIds = ref<Record<number, boolean>>({})
const subscribedMap = ref<Record<string, boolean>>({})

const isSubscribed = (item: DiscoverItem) => {
  if (!item.tmdb_id) return false
  return !!subscribedMap.value[`${item.media_type || 'movie'}:${item.tmdb_id}`]
}

const tmdbMovieCategories = [
  { value: 'popular', label: '热门' },
  { value: 'top_rated', label: '高分' },
  { value: 'now_playing', label: '正在上映' },
  { value: 'upcoming', label: '即将上映' },
  { value: 'trending_day', label: '今日趋势' },
  { value: 'trending_week', label: '本周趋势' },
]

const tmdbTvCategories = [
  { value: 'popular', label: '热门' },
  { value: 'top_rated', label: '高分' },
  { value: 'on_the_air', label: '正在播出' },
  { value: 'airing_today', label: '今日播出' },
  { value: 'trending_day', label: '今日趋势' },
  { value: 'trending_week', label: '本周趋势' },
]

const doubanTags = ['热门', '最新', '经典', '豆瓣高分', '冷门佳片', '华语', '欧美', '韩国', '日本', '动作', '喜剧', '爱情', '科幻', '悬疑', '恐怖', '剧情', '纪录片', '动画']
const doubanTvTags = ['热门', '最新', '经典', '国产剧', '美剧', '英剧', '韩剧', '日剧', '动漫', '悬疑', '科幻', '爱情', '喜剧', '动作']

const doubanCollections = [
  { value: 'movie_hot_gaia', label: '电影热门' },
  { value: 'tv_hot_gaia', label: '剧集热门' },
  { value: 'movie_weekly_best', label: '一周口碑电影榜' },
  { value: 'tv_weekly_best', label: '一周口碑剧集榜' },
  { value: 'movie_new_movie', label: '新片速递' },
]

const selectedTmdbMovieCategory = ref('popular')
const selectedTmdbTvCategory = ref('popular')
const selectedDoubanTag = ref('热门')
const selectedDoubanTvTag = ref('热门')
const selectedDoubanCollection = ref('movie_hot_gaia')
const selectedDoubanType = ref('movie')

const isTmdbTab = () => activeTab.value === 'tmdb-movie' || activeTab.value === 'tmdb-tv'

const load = async () => {
  loading.value = true
  errorMessage.value = ''
  try {
    let url = ''
    if (activeTab.value === 'tmdb-movie') {
      url = `${SERVER_URL}/discover/tmdb?type=movie&category=${selectedTmdbMovieCategory.value}&page=${page.value}`
    } else if (activeTab.value === 'tmdb-tv') {
      url = `${SERVER_URL}/discover/tmdb?type=tv&category=${selectedTmdbTvCategory.value}&page=${page.value}`
    } else if (activeTab.value === 'douban-tag') {
      const tag = selectedDoubanType.value === 'tv' ? selectedDoubanTvTag.value : selectedDoubanTag.value
      url = `${SERVER_URL}/discover/douban?source=tag&type=${selectedDoubanType.value}&tag=${encodeURIComponent(tag)}&page=${page.value}`
    } else if (activeTab.value === 'douban-collection') {
      const isTv = selectedDoubanCollection.value.startsWith('tv')
      url = `${SERVER_URL}/discover/douban?source=collection&type=${isTv ? 'tv' : 'movie'}&collection=${selectedDoubanCollection.value}&page=${page.value}`
    }
    const response = await http.get(url)
    if (response && response.data && response.data.code === 200) {
      items.value = response.data.data || []
      totalPages.value = page.value + 1
    } else {
      errorMessage.value = (response?.data?.message as string) || '加载失败'
      items.value = []
    }
  } catch (err) {
    errorMessage.value = '加载失败：' + (err as Error).message
    items.value = []
  } finally {
    loading.value = false
  }
  checkEmbyStatus()
}

const embyCheckBusy = ref(false)

const checkEmbyStatus = async () => {
  if (!items.value.length) return
  embyCheckBusy.value = true
  try {
    const payload = items.value.map((item) => ({
      tmdb_id: item.tmdb_id || 0,
      title: item.title,
      year: item.year || 0,
    }))
    const response = await http.post(`${SERVER_URL}/discover/emby-check`, { items: payload })
    if (response && response.data && response.data.code === 200) {
      const resultMap = new Map<number, boolean>()
      for (const result of response.data.data) {
        resultMap.set(result.tmdb_id, result.in_emby)
      }
      items.value = items.value.map((item) => ({
        ...item,
        in_emby: item.tmdb_id ? !!resultMap.get(item.tmdb_id) : false,
      }))
    }
  } catch {
    // Emby 未配置或检测失败时静默跳过
  } finally {
    embyCheckBusy.value = false
  }
}

const changeTab = () => {
  page.value = 1
  load()
}

const changeCategory = () => {
  page.value = 1
  load()
}

const changePage = (p: number) => {
  page.value = p
  load()
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
  const isTv = activeTab.value === 'tmdb-tv'
  searching.value = true
  searchMode.value = true
  errorMessage.value = ''
  try {
    const params: Record<string, string | number> = { name: keyword, type: isTv ? 'tvshow' : 'movie' }
    const response = await http.get(`${SERVER_URL}/scrape/tmdb-search`, { params, timeout: 30000 })
    const data = response?.data?.data
    const list = Array.isArray(data) ? data : data?.list || []
    items.value = list
      .filter((r: any) => r && (r.title || r.name) && r.tmdb_id)
      .map((r: any) => ({
        source: 'tmdb',
        media_type: isTv ? 'tv' : 'movie',
        tmdb_id: Number(r.tmdb_id),
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
    subscribingIds.value[item.tmdb_id] = false
  }
}

const posterUrl = (item: DiscoverItem) => {
  if (item.poster) return item.poster
  return ''
}

const formatVote = (vote: number) => (vote > 0 ? vote.toFixed(1) : '')

const openDetail = (item: DiscoverItem) => {
  let url = ''
  if (item.source === 'douban') {
    url = `https://movie.douban.com/subject/${item.douban_id}/`
  } else if (item.tmdb_id) {
    url = `https://www.themoviedb.org/${item.media_type === 'tv' ? 'tv' : 'movie'}/${item.tmdb_id}`
  }
  if (url) {
    window.open(url, '_blank')
  }
}

onMounted(() => {
  load()
  loadSubscribed()
})

watch(activeTab, () => {
  searchMode.value = false
  searchKeyword.value = ''
  changeTab()
})
</script>

<template>
  <div class="discover-container">
    <div class="page-header">
      <div class="header-content">
        <h1>发现</h1>
        <p>TMDB 与豆瓣热门影片、片单一览，已入库 Emby 的影片会标记勾选，点击爱心一键订阅。</p>
      </div>
      <div class="search-box">
        <el-input
          v-model="searchKeyword"
          placeholder="搜索影片名称"
          clearable
          style="width: 220px"
          @keyup.enter="onSearch"
          @clear="clearSearch"
        />
        <el-button v-if="!searchMode" type="primary" :loading="searching" @click="onSearch">搜索</el-button>
        <el-button v-else type="warning" @click="clearSearch">返回榜单</el-button>
      </div>
    </div>

    <el-tabs v-model="activeTab" class="discover-tabs" @tab-change="changeTab">
      <el-tab-pane label="TMDB 电影" name="tmdb-movie" />
      <el-tab-pane label="TMDB 剧集" name="tmdb-tv" />
      <el-tab-pane label="豆瓣榜单" name="douban-tag" />
      <el-tab-pane label="豆瓣片单" name="douban-collection" />
    </el-tabs>

    <div v-if="!searchMode" class="category-bar">
      <template v-if="activeTab === 'tmdb-movie'">
        <el-radio-group v-model="selectedTmdbMovieCategory" @change="changeCategory">
          <el-radio-button v-for="cat in tmdbMovieCategories" :key="cat.value" :value="cat.value">
            {{ cat.label }}
          </el-radio-button>
        </el-radio-group>
      </template>
      <template v-else-if="activeTab === 'tmdb-tv'">
        <el-radio-group v-model="selectedTmdbTvCategory" @change="changeCategory">
          <el-radio-button v-for="cat in tmdbTvCategories" :key="cat.value" :value="cat.value">
            {{ cat.label }}
          </el-radio-button>
        </el-radio-group>
      </template>
      <template v-else-if="activeTab === 'douban-tag'">
        <el-radio-group v-model="selectedDoubanType" @change="changeCategory" style="margin-right: 16px">
          <el-radio-button value="movie">电影</el-radio-button>
          <el-radio-button value="tv">剧集</el-radio-button>
        </el-radio-group>
        <el-radio-group v-if="selectedDoubanType === 'movie'" v-model="selectedDoubanTag" @change="changeCategory">
          <el-radio-button v-for="tag in doubanTags" :key="tag" :value="tag">{{ tag }}</el-radio-button>
        </el-radio-group>
        <el-radio-group v-else v-model="selectedDoubanTvTag" @change="changeCategory">
          <el-radio-button v-for="tag in doubanTvTags" :key="tag" :value="tag">{{ tag }}</el-radio-button>
        </el-radio-group>
      </template>
      <template v-else-if="activeTab === 'douban-collection'">
        <el-radio-group v-model="selectedDoubanCollection" @change="changeCategory">
          <el-radio-button v-for="col in doubanCollections" :key="col.value" :value="col.value">
            {{ col.label }}
          </el-radio-button>
        </el-radio-group>
      </template>
    </div>

    <div v-if="errorMessage" class="error-tip">
      <el-alert :title="errorMessage" type="warning" show-icon :closable="false" />
    </div>

    <div v-loading="loading" class="grid-wrap">
      <div class="card-grid">
        <div v-for="item in items" :key="item.source + item.tmdb_id + item.douban_id + item.title" class="movie-card" @click="openDetail(item)">
          <div class="poster-wrap">
            <img v-if="posterUrl(item)" :src="posterUrl(item)" class="poster" loading="lazy" alt="" />
            <div v-else class="poster poster-placeholder">
              <span>{{ item.title.slice(0, 4) }}</span>
            </div>
            <div v-if="item.in_emby" class="emby-badge" title="已入库 Emby">
              <el-icon><CircleCheck /></el-icon>
            </div>
            <div v-if="item.tmdb_id" class="heart-btn" :class="{ active: isSubscribed(item), busy: subscribingIds[item.tmdb_id] }"
              :title="isSubscribed(item) ? '已订阅' : '点击订阅'" @click.stop="toggleSubscribe(item)">
              <svg viewBox="0 0 24 24" width="16" height="16">
                <path
                  d="M12 21.35l-1.45-1.32C5.4 15.36 2 12.28 2 8.5 2 5.42 4.42 3 7.5 3c1.74 0 3.41.81 4.5 2.09C13.09 3.81 14.76 3 16.5 3 19.58 3 22 5.42 22 8.5c0 3.78-3.4 6.86-8.55 11.54L12 21.35z"
                />
              </svg>
            </div>
            <div v-if="formatVote(item.vote_avg)" class="score-badge">{{ formatVote(item.vote_avg) }}</div>
          </div>
          <div class="card-title" :title="item.title">{{ item.title }}</div>
          <div class="card-sub" v-if="item.year || item.release_date">
            {{ item.year || item.release_date }}
            <template v-if="item.original_title && item.original_title !== item.title"> · {{ item.original_title }}</template>
          </div>
        </div>
      </div>
      <div v-if="!loading && !items.length && !errorMessage" class="empty-tip">
        <el-empty description="暂无数据" />
      </div>
      <div class="pager" v-if="items.length && !searchMode">
        <el-pagination
          background
          layout="prev, pager, next"
          :total="totalPages * 20"
          :page-size="20"
          :current-page="page"
          @current-change="changePage"
        />
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

.heart-btn {
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
.heart-btn:hover {
  transform: scale(1.12);
  opacity: 1;
}
.heart-btn.active {
  background: rgba(245, 63, 63, 0.9);
}
.heart-btn.busy {
  pointer-events: none;
  opacity: 0.5;
}
.heart-btn svg {
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
</style>
