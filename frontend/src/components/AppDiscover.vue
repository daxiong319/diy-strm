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
})

watch(activeTab, changeTab)
</script>

<template>
  <div class="discover-container">
    <div class="page-header">
      <div class="header-content">
        <h1>发现</h1>
        <p>TMDB 与豆瓣热门影片、片单一览，已入库 Emby 的影片会标记勾选</p>
      </div>
    </div>

    <el-tabs v-model="activeTab" class="discover-tabs" @tab-change="changeTab">
      <el-tab-pane label="TMDB 电影" name="tmdb-movie" />
      <el-tab-pane label="TMDB 剧集" name="tmdb-tv" />
      <el-tab-pane label="豆瓣榜单" name="douban-tag" />
      <el-tab-pane label="豆瓣片单" name="douban-collection" />
    </el-tabs>

    <div class="category-bar">
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
      <div class="pager" v-if="items.length">
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
  padding: 20px 24px;
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  border-radius: 16px;
  color: white;
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
