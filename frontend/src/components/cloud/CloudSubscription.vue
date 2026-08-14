<template>
  <div class="main-content-container cloud-page">
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>{{ sourceName }} · 频道订阅</span>
          <div class="header-actions">
            <el-button type="primary" size="small" @click="openCreate">新增订阅</el-button>
            <el-button size="small" @click="openPreview">预览频道</el-button>
            <el-button size="small" :loading="loading" @click="load">刷新</el-button>
          </div>
        </div>
      </template>

      <el-alert
        v-if="sourceType === 'guangyapan'"
        type="info"
        :closable="false"
        class="cloud-alert"
        title="光鸭分享链接支持：https://www.guangyapan.com/s/xxxxxx"
        show-icon
      />
      <el-alert
        v-else-if="sourceType === '123'"
        type="info"
        :closable="false"
        class="cloud-alert"
        title="123 分享链接支持：https://www.123pan.com/s/xxxxxx"
        show-icon
      />
      <el-alert
        v-else-if="sourceType === 'pan139'"
        type="info"
        :closable="false"
        class="cloud-alert"
        title="中国移动云盘分享链接支持：https://www.139.com/w/i/xxxxxx"
        show-icon
      />

      <el-table :data="subs" v-loading="loading" empty-text="暂无订阅，点击「新增订阅」创建">
        <el-table-column prop="id" label="ID" width="60" />
        <el-table-column label="频道" min-width="140">
          <template #default="{ row }">
            <span>{{ row.channel }}</span>
          </template>
        </el-table-column>
        <el-table-column label="关键词" min-width="160">
          <template #default="{ row }">
            <template v-if="row.keywords.length">
              <el-tag v-for="k in row.keywords" :key="k" size="small" class="kw-tag">{{ k }}</el-tag>
            </template>
            <span v-else class="muted">全部</span>
          </template>
        </el-table-column>
        <el-table-column label="影片" min-width="150">
          <template #default="{ row }">
            <div v-if="row.tmdb_title">
              <div class="media-title-line">
                <span class="media-title">{{ row.tmdb_title }}</span>
                <el-tag v-if="row.media_type === 'movie'" size="small">电影</el-tag>
                <el-tag v-else size="small" type="warning">剧集</el-tag>
              </div>
              <div class="muted media-sub">
                <span v-if="row.media_type === 'tv'">
                  {{ row.season > 0 ? `S${row.season}` : `全 ${row.total_seasons || '?'} 季` }}
                </span>
                <el-tag v-if="row.wash" size="small" type="danger" class="wash-tag">
                  洗版{{ washTargetLabel(row.wash_target) }}
                </el-tag>
                <el-tag v-if="row.finished_at && row.finished_at !== '0001-01-01T00:00:00Z'" size="small" type="success" class="finished-tag">
                  已完结
                </el-tag>
              </div>
            </div>
            <span v-else class="muted">通用订阅</span>
          </template>
        </el-table-column>
        <el-table-column prop="target_dir" label="目标目录" min-width="140" show-overflow-tooltip />
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-switch
              :model-value="row.enabled"
              @change="(v: boolean) => toggleEnabled(row, v)"
            />
          </template>
        </el-table-column>
        <el-table-column label="上次运行" width="130">
          <template #default="{ row }">
            <span v-if="row.last_run_at && row.last_run_at !== '0001-01-01T00:00:00Z'">
              {{ formatTime(row.last_run_at) }}
            </span>
            <span v-else class="muted">未运行</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="290" fixed="right">
          <template #default="{ row }">
            <el-button size="small" @click="openEdit(row)">编辑</el-button>
            <el-button
              v-if="row.old_count > 0"
              size="small"
              type="warning"
              plain
              :loading="cleaningId === row.id"
              @click="cleanOld(row)"
            >
              清理旧版({{ row.old_count }})
            </el-button>
            <el-button size="small" type="primary" plain :loading="runningId === row.id" @click="run(row)">
              执行
            </el-button>
            <el-button size="small" type="danger" plain @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="formVisible" :title="form.id ? '编辑订阅' : '新增订阅'" width="620px">
      <el-form :model="form" label-width="90px">
        <el-form-item label="频道名" required>
          <el-input v-model="form.channel" placeholder="例如：@dianying" />
          <div class="form-help">填写 TG 公开频道名（以 @ 开头），系统每 5 分钟轮询一次。</div>
        </el-form-item>

        <el-form-item label="选片">
          <div class="pick-block">
            <template v-if="!selectedMedia">
              <div class="pick-search-row">
                <el-input
                  v-model="pickKeyword"
                  placeholder="输入片名搜索（支持中英文），例如：星际穿越"
                  @keyup.enter="doPickSearch"
                />
                <el-button type="primary" :loading="pickSearching" @click="doPickSearch">
                  TMDB 查询
                </el-button>
              </div>
              <div v-if="pickResults.length" class="pick-results">
                <div
                  v-for="r in pickResults"
                  :key="`${r.media_type}-${r.tmdb_id}`"
                  class="pick-item"
                  :class="{ 'pick-item-tv': r.media_type === 'tvshow' }"
                  @click="chooseMedia(r)"
                >
                  <el-image v-if="r.poster_url" :src="r.poster_url" fit="cover" class="pick-poster">
                    <template #error>
                      <div class="pick-poster-fallback">无图</div>
                    </template>
                  </el-image>
                  <div v-else class="pick-poster pick-poster-fallback">无图</div>
                  <div class="pick-info">
                    <div class="pick-title">
                      {{ r.title }}
                      <el-tag size="small" :type="r.media_type === 'tvshow' ? 'warning' : ''">
                        {{ r.media_type === 'tvshow' ? '剧集' : '电影' }}
                      </el-tag>
                      <span v-if="r.year" class="pick-year">{{ r.year }}</span>
                    </div>
                    <div v-if="r.original_title && r.original_title !== r.title" class="pick-ot muted">
                      {{ r.original_title }}
                    </div>
                    <div class="pick-ov muted">{{ r.overview }}</div>
                  </div>
                </div>
              </div>
              <div class="form-help">通过 TMDB 搜索精确选择影片，避免同名混淆（如不同年份 / 不同季）。</div>
            </template>

            <template v-else>
              <div class="pick-selected">
                <el-image v-if="selectedMedia.poster_url" :src="selectedMedia.poster_url" fit="cover" class="pick-poster">
                  <template #error>
                    <div class="pick-poster-fallback">无图</div>
                  </template>
                </el-image>
                <div v-else class="pick-poster pick-poster-fallback">无图</div>
                <div class="pick-info">
                  <div class="pick-title">
                    {{ selectedMedia.title }}
                    <el-tag size="small" :type="selectedMedia.media_type === 'tvshow' ? 'warning' : ''">
                      {{ selectedMedia.media_type === 'tvshow' ? '剧集' : '电影' }}
                    </el-tag>
                    <span v-if="selectedMedia.year" class="pick-year">{{ selectedMedia.year }}</span>
                  </div>
                  <div v-if="selectedMedia.original_title && selectedMedia.original_title !== selectedMedia.title" class="pick-ot muted">
                    {{ selectedMedia.original_title }}
                  </div>
                  <div v-if="selectedMedia.media_type === 'tvshow'" class="pick-season-row">
                    <span class="pick-season-label">收录季：</span>
                    <el-select v-model="selectedSeason" size="small" class="pick-season-select">
                      <el-option :value="0" label="全部季" />
                      <el-option
                        v-for="s in selectedMedia.seasons || []"
                        :key="s.season_number"
                        :value="s.season_number"
                        :label="`第 ${s.season_number} 季（${s.episode_count} 集）`"
                      />
                    </el-select>
                  </div>
                  <div class="pick-finish-hint">
                    <template v-if="selectedMedia.media_type === 'movie'">
                      该片转存一次后自动完结（已收录则不再重复转存）
                    </template>
                    <template v-else-if="selectedSeason > 0">
                      第 {{ selectedSeason }} 季收录后自动完结（S{{ selectedSeason }} 只匹配一次）
                    </template>
                    <template v-else>
                      全部 {{ (selectedMedia.seasons || []).length }} 季收录后自动完结
                    </template>
                  </div>
                  <div class="pick-actions">
                    <el-button size="small" type="danger" plain @click="clearMedia">清除选片</el-button>
                    <el-button size="small" @click="openPickSearch">重新选择</el-button>
                  </div>
                </div>
              </div>
            </template>
          </div>
        </el-form-item>

        <el-form-item label="附加关键词">
          <el-input
            v-model="form.keywords"
            placeholder="可留空；也可补充，如：4K 国语"
            @keyup.enter="confirmForm"
          />
          <div class="form-help">
            <template v-if="selectedMedia">
              已选片：自动匹配
              <el-tag v-for="k in autoKeywords" :key="k" size="small" class="kw-tag">{{ k }}</el-tag>
              <div class="finish-note">保存后自动关键词与附加关键词合并生效。</div>
            </template>
            <template v-else>未选片时：帖子文本命中任一关键词即触发转存；留空表示该频道全部分享都转存。</template>
          </div>
        </el-form-item>
        <el-form-item label="目标目录">
          <div class="dir-input-row">
            <el-input v-model="form.target_dir" placeholder="例如：/影视/待整理（留空使用 /）" />
            <el-button @click="pickerVisible = true">选择</el-button>
          </div>
          <div class="form-help">转存到{{ sourceName }}内的该目录，可通过「选择」浏览网盘目录。</div>
        </el-form-item>
        <el-form-item label="自动完结">
          <el-switch v-model="form.auto_finish" />
          <div class="form-help">
            开启（默认）：影片收录完毕后自动停用该订阅，避免重复转存；关闭：订阅持续运行，已收录的影片/链接仍会去重跳过。
          </div>
        </el-form-item>
        <template v-if="selectedMedia">
          <el-form-item label="洗版">
            <el-switch v-model="form.wash" />
            <div class="form-help">
              开启后：同片出现更高规格资源（4K/2160p &gt; 1080p &gt; 720p，REMUX &gt; BluRay &gt; WEB-DL &gt; WEBRip &gt; HDTV，H265 &gt; H264，DV &gt; HDR &gt; SDR，体积）时自动转存替换旧版本。
            </div>
          </el-form-item>
          <template v-if="form.wash">
            <el-form-item label="洗版目标">
              <el-select v-model="form.wash_target" style="width: 220px">
                <el-option value="" label="无限制（持续升级）" />
                <el-option value="1080p" label="1080p 及以上" />
                <el-option value="4k" label="4K 及以上" />
                <el-option value="4k_remux" label="4K + REMUX" />
              </el-select>
              <div class="form-help">
                已收录版本达到目标后停止洗版并自动完结；无限制时持续寻找更高规格，不会自动完结。
              </div>
            </el-form-item>
            <el-form-item label="旧版本处理">
              <el-radio-group v-model="form.replace_old">
                <el-radio :value="true">转存后自动删除旧文件</el-radio>
                <el-radio :value="false">保留共存，手动清理</el-radio>
              </el-radio-group>
              <div class="form-help">
                选择保留共存时，旧版本不会被删除，可在订阅列表点击「清理旧版」按钮手动删除。
              </div>
            </el-form-item>
          </template>
        </template>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="formVisible = false">取消</el-button>
        <el-button type="primary" :loading="formSaving" @click="confirmForm">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="previewVisible" title="预览频道最近内容" width="640px">
      <div class="preview-input">
        <el-input
          v-model="previewChannel"
          placeholder="例如：@dianying"
          @keyup.enter="doPreview"
        />
        <el-button type="primary" :loading="previewing" @click="doPreview">抓取</el-button>
      </div>
      <el-empty v-if="previewPosts.length === 0 && !previewing" description="输入频道名后点击抓取" />
      <div v-loading="previewing" class="preview-list">
        <div v-for="p in previewPosts" :key="p.post_id" class="preview-item">
          <div class="preview-head">
            <el-tag size="small">{{ p.post_id }}</el-tag>
            <span class="muted">{{ p.time }}</span>
            <span v-if="p.links.length" class="preview-links">{{ p.links.length }} 个分享链接</span>
          </div>
          <div class="preview-text">{{ p.text }}</div>
          <div v-if="p.links.length" class="preview-links-detail">
            <div v-for="(l, i) in p.links" :key="i" class="link-line">{{ l }}</div>
          </div>
        </div>
      </div>
      <template #footer>
        <el-button @click="previewVisible = false">关闭</el-button>
      </template>
    </el-dialog>

    <CloudDirPicker
      :visible="pickerVisible"
      :source-type="sourceType"
      :source-name="sourceName"
      @update:visible="pickerVisible = $event"
      @select="onDirSelected"
    />
  </div>
</template>

<script setup lang="ts">
import { useHttpClient } from '@/http/client'
import { ElMessage, ElMessageBox } from 'element-plus'
import { computed, onMounted, reactive, ref } from 'vue'
import { isMobile } from '@/utils/deviceUtils'
import CloudDirPicker from './CloudDirPicker.vue'

const props = defineProps<{
  sourceType: string
  sourceName: string
}>()

const checkIsMobile = ref(isMobile())
const http = useHttpClient()

interface SubRow {
  id: number
  channel: string
  keywords: string | string[]
  target_dir: string
  enabled: boolean
  last_run_at: string
  media_type?: string
  tmdb_id?: number
  tmdb_title?: string
  season?: number
  total_seasons?: number
  auto_finish?: boolean
  wash?: boolean
  wash_target?: string
  replace_old?: boolean
  old_count?: number
  finished_at?: string
}

const subs = ref<SubRow[]>([])
const loading = ref(false)

const parseKeywords = (k: string | string[]): string[] => {
  if (Array.isArray(k)) return k
  try {
    const arr = JSON.parse(k || '[]')
    return Array.isArray(arr) ? arr : []
  } catch {
    return k ? k.split(/\s+/) : []
  }
}

const load = async () => {
  loading.value = true
  try {
    const resp = await http.get('/api/cloud/subscriptions', { params: { source_type: props.sourceType } })
    if (resp.data?.code === 200) {
      subs.value = (resp.data.data || []).map((s: any) => ({
        ...s,
        keywords: parseKeywords(s.keywords),
      }))
    } else {
      ElMessage.error(resp.data?.message || '加载失败')
    }
  } catch (e: any) {
    ElMessage.error('加载失败：' + (e?.message || ''))
  } finally {
    loading.value = false
  }
}

const formVisible = ref(false)
const formSaving = ref(false)
const pickerVisible = ref(false)
const form = reactive({ id: 0, channel: '', keywords: '', target_dir: '/影视/待整理', enabled: true, auto_finish: true, wash: false, wash_target: '', replace_old: true })

const onDirSelected = (path: string) => {
  form.target_dir = path
}

// ---- TMDB 选片 ----
interface PickItem {
  tmdb_id: number
  title: string
  original_title: string
  year: number
  poster_url: string
  overview: string
  vote_average: number
  media_type: 'movie' | 'tvshow'
  seasons?: { season_number: number; name: string; episode_count: number; air_date: string }[]
  total_seasons?: number
}

const pickKeyword = ref('')
const pickSearching = ref(false)
const pickResults = ref<PickItem[]>([])
const selectedMedia = ref<PickItem | null>(null)
const selectedSeason = ref(0)

const CN_NUMS = ['', '一', '二', '三', '四', '五', '六', '七', '八', '九', '十', '十一', '十二']

const autoKeywords = computed<string[]>(() => {
  const m = selectedMedia.value
  if (!m) return []
  const parts: string[] = []
  const t = (m.title || '').trim()
  const ot = (m.original_title || '').trim()
  const push = (v: string) => {
    if (v && !parts.includes(v)) parts.push(v)
  }
  push(t)
  if (ot && ot.toLowerCase() !== t.toLowerCase()) push(ot)
  const compact = (s: string) => s.replace(/\s+/g, '')
  push(compact(t))
  if (ot && ot.toLowerCase() !== t.toLowerCase()) push(compact(ot))
  if (m.media_type === 'tvshow' && selectedSeason.value > 0) {
    const s = selectedSeason.value
    push(`S${s}`)
    push(`S${String(s).padStart(2, '0')}`)
    push(`第${s}季`)
    push(`第${String(s).padStart(2, '0')}季`)
    if (CN_NUMS[s]) push(`第${CN_NUMS[s]}季`)
  }
  return parts
})

const doPickSearch = async () => {
  const q = pickKeyword.value.trim()
  if (!q) {
    ElMessage.warning('请输入片名')
    return
  }
  pickSearching.value = true
  pickResults.value = []
  try {
    const [movieResp, tvResp] = await Promise.all([
      http.get('/api/scrape/tmdb-search', { params: { name: q, type: 'movie' } }),
      http.get('/api/scrape/tmdb-search', { params: { name: q, type: 'tvshow' } }),
    ])
    const merge = (resp: any, type: 'movie' | 'tvshow') => {
      if (resp.data?.code === 200 && Array.isArray(resp.data.data)) {
        return resp.data.data.map((d: any) => ({ ...d, media_type: type }))
      }
      return []
    }
    const items = [...merge(movieResp, 'movie'), ...merge(tvResp, 'tvshow')]
      .sort((a, b) => (b.vote_average || 0) - (a.vote_average || 0))
      .slice(0, 12)
    if (!items.length) {
      ElMessage.warning('TMDB 没有找到相关影片')
      return
    }
    pickResults.value = items
  } catch (e: any) {
    ElMessage.error('查询失败：' + (e?.message || ''))
  } finally {
    pickSearching.value = false
  }
}

const chooseMedia = async (item: PickItem) => {
  selectedSeason.value = 0
  pickResults.value = []
  if (item.media_type === 'tvshow') {
    try {
      const resp = await http.get('/api/scrape/tmdb-search', {
        params: { type: 'tvshow', tmdb_id: item.tmdb_id },
      })
      if (resp.data?.code === 200 && resp.data.data?.length) {
        const detail = resp.data.data[0]
        selectedMedia.value = {
          ...item,
          seasons: detail.seasons || [],
          total_seasons: (detail.seasons || []).length,
        }
        return
      }
    } catch {
      /* 详情获取失败也允许用搜索结果 */
    }
  }
  selectedMedia.value = item
}

const clearMedia = () => {
  selectedMedia.value = null
  selectedSeason.value = 0
  pickKeyword.value = ''
  pickResults.value = []
}

const openPickSearch = () => {
  pickKeyword.value = ''
  pickResults.value = []
}

const resetPick = () => {
  clearMedia()
  form.keywords = ''
}

const openCreate = () => {
  Object.assign(form, { id: 0, channel: '', keywords: '', target_dir: '/影视/待整理', enabled: true, auto_finish: true, wash: false, wash_target: '', replace_old: true })
  resetPick()
  formVisible.value = true
}

const openEdit = (row: SubRow) => {
  const kws = Array.isArray(row.keywords) ? row.keywords.join(' ') : String(row.keywords || '')
  Object.assign(form, {
    id: row.id,
    channel: row.channel,
    keywords: kws,
    target_dir: row.target_dir,
    enabled: row.enabled,
    auto_finish: row.auto_finish !== false,
    wash: !!row.wash,
    wash_target: row.wash_target || '',
    replace_old: row.replace_old !== false,
  })
  // 回填选片
  const rowMediaType = row.media_type || ''
  const rowSeason = row.season || 0
  const rowTotalSeasons = row.total_seasons || 0
  if (rowMediaType) {
    selectedMedia.value = {
      tmdb_id: row.tmdb_id || 0,
      title: row.tmdb_title || '',
      original_title: '',
      year: 0,
      poster_url: '',
      overview: '',
      vote_average: 0,
      media_type: rowMediaType === 'tv' ? 'tvshow' : 'movie',
    }
    selectedSeason.value = rowSeason
    if (rowMediaType === 'tv' && rowTotalSeasons > 0) {
      const seasons: any[] = []
      for (let i = 1; i <= rowTotalSeasons; i++) {
        seasons.push({ season_number: i, name: `第 ${i} 季`, episode_count: 0, air_date: '' })
      }
      selectedMedia.value = { ...selectedMedia.value!, seasons }
    }
  } else {
    resetPick()
  }
  pickKeyword.value = ''
  pickResults.value = []
  formVisible.value = true
}

const confirmForm = async () => {
  const channel = form.channel.trim()
  if (!channel) {
    ElMessage.warning('请填写频道名')
    return
  }
  if (!channel.startsWith('@')) {
    ElMessage.warning('频道名必须以 @ 开头')
    return
  }
  formSaving.value = true
  try {
    const m = selectedMedia.value
    const extra = form.keywords
      .trim()
      .split(/\s+/)
      .filter(Boolean)
    const merged = [...new Set([...autoKeywords.value, ...extra])]
    const payload: any = {
      source_type: props.sourceType,
      channel,
      keywords: merged.join(' '),
      target_dir: form.target_dir.trim() || '/',
      enabled: form.enabled,
      auto_finish: form.auto_finish,
      wash: form.wash,
      wash_target: form.wash_target,
      replace_old: form.replace_old,
      media_type: m ? (m.media_type === 'movie' ? 'movie' : 'tv') : '',
      tmdb_id: m ? m.tmdb_id : 0,
      tmdb_title: m ? m.title : '',
      season: m && m.media_type === 'tvshow' ? (selectedSeason.value || 0) : 0,
      total_seasons: m && m.media_type === 'tvshow' ? (m.seasons || []).length : 0,
    }
    const resp =
      form.id > 0
        ? await http.put(`/api/cloud/subscriptions/${form.id}`, payload)
        : await http.post('/api/cloud/subscriptions', payload)
    if (resp.data?.code === 200) {
      ElMessage.success(form.id > 0 ? '订阅已更新' : '订阅已创建')
      formVisible.value = false
      await load()
    } else {
      ElMessage.error(resp.data?.message || '保存失败')
    }
  } catch (e: any) {
    ElMessage.error('保存失败：' + (e?.message || ''))
  } finally {
    formSaving.value = false
  }
}

const toggleEnabled = async (row: SubRow, enabled: boolean) => {
  try {
    const resp = await http.put(`/api/cloud/subscriptions/${row.id}`, { enabled })
    if (resp.data?.code === 200) {
      row.enabled = enabled
      ElMessage.success(enabled ? '已启用' : '已禁用')
    } else {
      ElMessage.error(resp.data?.message || '操作失败')
    }
  } catch (e: any) {
    ElMessage.error('操作失败：' + (e?.message || ''))
  }
}

const cleaningId = ref(0)
const cleanOld = async (row: SubRow) => {
  try {
    await ElMessageBox.confirm(
      `确定清理订阅 #${row.id}（${row.tmdb_title || row.channel}）的 ${row.old_count} 个旧版本文件吗？删除后不可恢复。`,
      '清理旧版本',
      { type: 'warning' }
    )
  } catch {
    return
  }
  cleaningId.value = row.id
  try {
    const resp = await http.post('/api/cloud/subscriptions/clean-old', { id: row.id })
    if (resp.data?.code === 200) {
      ElMessage.success({ message: resp.data.message || '清理完成', duration: 6000, showClose: true })
    } else {
      ElMessage.error(resp.data?.message || '清理失败')
    }
    await load()
  } catch (e: any) {
    ElMessage.error('清理失败：' + (e?.message || ''))
  } finally {
    cleaningId.value = 0
  }
}

const washTargetLabel = (t?: string) => {
  if (!t) return ''
  if (t === '1080p') return ' · 目标1080p'
  if (t === '4k') return ' · 目标4K'
  if (t === '4k_remux') return ' · 目标4K+REMUX'
  return ''
}

const runningId = ref(0)
const run = async (row: SubRow) => {
  runningId.value = row.id
  try {
    const resp = await http.post('/api/cloud/subscriptions/run', { id: row.id })
    if (resp.data?.code === 200) {
      ElMessage.success({ message: resp.data.message || '执行完成', duration: 8000, showClose: true })
    } else {
      ElMessage.error(resp.data?.message || '执行失败')
    }
    await load()
  } catch (e: any) {
    ElMessage.error('执行失败：' + (e?.message || ''))
  } finally {
    runningId.value = 0
  }
}

const remove = async (row: SubRow) => {
  try {
    await ElMessageBox.confirm(`确定删除订阅 #${row.id}（${row.channel}）吗？`, '删除确认', {
      type: 'warning',
    })
  } catch {
    return
  }
  try {
    const resp = await http.delete(`/api/cloud/subscriptions/${row.id}`)
    if (resp.data?.code === 200) {
      ElMessage.success('订阅已删除')
      await load()
    } else {
      ElMessage.error(resp.data?.message || '删除失败')
    }
  } catch (e: any) {
    ElMessage.error('删除失败：' + (e?.message || ''))
  }
}

const previewVisible = ref(false)
const previewChannel = ref('')
const previewing = ref(false)
const previewPosts = ref<any[]>([])

const openPreview = () => {
  previewChannel.value = ''
  previewPosts.value = []
  previewVisible.value = true
}

const doPreview = async () => {
  const channel = previewChannel.value.trim()
  if (!channel) {
    ElMessage.warning('请填写频道名')
    return
  }
  previewing.value = true
  try {
    const resp = await http.post('/api/cloud/subscriptions/preview', { channel, limit: 10 })
    if (resp.data?.code === 200) {
      previewPosts.value = resp.data.data || []
    } else {
      ElMessage.error(resp.data?.message || '抓取失败')
    }
  } catch (e: any) {
    ElMessage.error('抓取失败：' + (e?.message || ''))
  } finally {
    previewing.value = false
  }
}

const formatTime = (t: string) => {
  const d = new Date(t)
  if (isNaN(d.getTime())) return t
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

onMounted(load)
</script>

<style scoped>
.cloud-page {
  padding: 16px;
}
.cloud-card {
  max-width: 1100px;
}
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.header-actions {
  display: flex;
  gap: 8px;
}
.cloud-alert {
  margin-bottom: 14px;
}
.kw-tag {
  margin: 2px 4px 2px 0;
}
.muted {
  color: #909399;
}
.form-help {
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
  margin-top: 4px;
}
.dir-input-row {
  display: flex;
  gap: 8px;
  width: 100%;
}
.dir-input-row .el-input {
  flex: 1;
}
.pick-block {
  width: 100%;
}
.pick-search-row {
  display: flex;
  gap: 8px;
  width: 100%;
}
.pick-search-row .el-input {
  flex: 1;
}
.pick-results {
  max-height: 260px;
  overflow-y: auto;
  border: 1px solid #e4e7ed;
  border-radius: 6px;
  margin-top: 8px;
}
.pick-item {
  display: flex;
  gap: 10px;
  padding: 8px 10px;
  cursor: pointer;
  border-bottom: 1px solid #f0f2f5;
}
.pick-item:last-child {
  border-bottom: none;
}
.pick-item:hover {
  background: #f5f7fa;
}
.pick-poster {
  width: 52px;
  height: 72px;
  border-radius: 4px;
  flex-shrink: 0;
}
.pick-poster-fallback {
  display: flex;
  align-items: center;
  justify-content: center;
  background: #f0f2f5;
  color: #909399;
  font-size: 12px;
}
.pick-info {
  flex: 1;
  min-width: 0;
}
.pick-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  font-weight: 600;
  color: #303133;
}
.pick-year {
  font-size: 12px;
  color: #909399;
  font-weight: 400;
}
.pick-ot {
  font-size: 12px;
  margin-top: 2px;
}
.pick-ov {
  font-size: 12px;
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
}
.pick-selected {
  display: flex;
  gap: 12px;
  border: 1px solid #e4e7ed;
  border-radius: 6px;
  padding: 10px;
}
.pick-season-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
}
.pick-season-label {
  font-size: 13px;
  color: #606266;
  white-space: nowrap;
}
.pick-season-select {
  width: 200px;
}
.pick-finish-hint {
  margin-top: 6px;
  font-size: 12px;
  color: #e6a23c;
  line-height: 1.6;
}
.pick-actions {
  margin-top: 8px;
  display: flex;
  gap: 8px;
}
.finish-note {
  margin-top: 2px;
}
.media-title-line {
  display: flex;
  align-items: center;
  gap: 6px;
}
.media-title {
  font-weight: 600;
  color: #303133;
}
.media-sub {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 2px;
  font-size: 12px;
}
.finished-tag {
  margin-left: 6px;
}
.wash-tag {
  margin-left: 6px;
}
.preview-input {
  display: flex;
  gap: 8px;
  margin-bottom: 14px;
}
.preview-list {
  max-height: 420px;
  overflow-y: auto;
}
.preview-item {
  border: 1px solid #e4e7ed;
  border-radius: 6px;
  padding: 10px 12px;
  margin-bottom: 10px;
}
.preview-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}
.preview-links {
  font-size: 12px;
  color: #409eff;
}
.preview-text {
  font-size: 13px;
  color: #303133;
  white-space: pre-wrap;
  word-break: break-all;
}
.preview-links-detail {
  margin-top: 6px;
  border-top: 1px dashed #e4e7ed;
  padding-top: 6px;
}
.link-line {
  font-size: 12px;
  color: #409eff;
  word-break: break-all;
}
</style>
