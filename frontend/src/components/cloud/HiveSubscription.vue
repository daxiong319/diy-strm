<template>
  <div class="main-content-container cloud-page">
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>影巢 · 资源订阅</span>
          <div class="header-actions">
            <el-button type="primary" size="small" @click="openCreate">新增订阅</el-button>
            <el-button size="small" :loading="loading" @click="load">刷新</el-button>
          </div>
        </div>
      </template>

      <el-alert
        type="info"
        :closable="false"
        class="cloud-alert"
        title="影巢（HDHive）订阅：按 TMDB 影片订阅网盘资源，引擎定期查询新资源并自动解锁转存。免费/收费资源均自动解锁（扣积分），资源网盘类型需与订阅目标一致（123 / 光鸭 / 139）。"
        show-icon
      />

      <el-table :data="subs" v-loading="loading" empty-text="暂无订阅，点击「新增订阅」创建">
        <el-table-column prop="id" label="ID" width="60" />
        <el-table-column label="影片" min-width="180">
          <template #default="{ row }">
            <div class="media-cell">
              <span class="media-title">{{ row.tmdb_title || `TMDB ${row.tmdb_id}` }}</span>
              <el-tag v-if="row.media_type === 'tv'" size="small" type="warning">剧集</el-tag>
              <el-tag v-else-if="row.media_type === 'movie'" size="small">电影</el-tag>
              <el-tag v-if="row.media_type === 'tv' && row.season > 0" size="small">
                第 {{ row.season }} 季
              </el-tag>
              <el-tag v-if="row.wash" size="small" type="danger" class="wash-tag">
                洗版{{ washTargetLabel(row.wash_target) }}
              </el-tag>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="目标网盘" width="110">
          <template #default="{ row }">
            <span>{{ sourceTypeName(row.source_type) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="target_dir" label="目标目录" min-width="140" show-overflow-tooltip />
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag v-if="row.finished_at" type="info" size="small">已完结</el-tag>
            <el-tag v-else-if="!row.enabled" type="warning" size="small">已停用</el-tag>
            <el-tag v-else type="success" size="small">运行中</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="上次运行" width="150">
          <template #default="{ row }">
            <span class="muted">{{ fmtTime(row.last_run_at) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="300" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="toggleEnabled(row)">
              {{ row.enabled ? '停用' : '启用' }}
            </el-button>
            <el-button v-if="row.old_count > 0" link type="warning" size="small" @click="cleanOld(row)">
              清理旧版({{ row.old_count }})
            </el-button>
            <el-button link type="primary" size="small" @click="runOnce(row)">执行</el-button>
            <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="formVisible" :title="form.id ? '编辑影巢订阅' : '新增影巢订阅'" width="620px">
      <el-form label-width="90px">
        <el-form-item label="选片" required>
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
              <div class="form-help">通过 TMDB 搜索选择影片，引擎将定期查询影巢上该片的新资源。</div>
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
                      第 {{ selectedSeason }} 季收录后自动完结
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

        <el-form-item label="目标网盘" required>
          <el-select v-model="form.source_type" class="full-width">
            <el-option value="123" label="123 云盘" />
            <el-option value="guangyapan" label="光鸭云盘" />
            <el-option value="pan139" label="中国移动云盘" />
          </el-select>
          <div class="form-help">影巢上仅该网盘类型的资源会被转存。</div>
        </el-form-item>

        <el-form-item label="目标目录">
          <div class="dir-input-row">
            <el-input v-model="form.target_dir" placeholder="例如：/媒体库/待整理（留空使用 /）" />
            <el-button size="small" @click="pickerVisible = true">选择目录</el-button>
          </div>
        </el-form-item>

        <el-form-item label="自动完结">
          <el-switch v-model="form.auto_finish" />
          <div class="form-help">影片/剧集收录完毕后自动停用订阅，避免重复转存。</div>
        </el-form-item>

        <el-form-item label="洗版">
          <el-switch v-model="form.wash" />
        </el-form-item>
        <template v-if="form.wash">
          <el-form-item label="洗版目标">
            <el-select v-model="form.wash_target" class="full-width">
              <el-option value="" label="无限制（持续升级到最好规格）" />
              <el-option value="1080p" label="1080p（达 1080p 即止）" />
              <el-option value="4k" label="4K（达 4K 即止）" />
              <el-option value="4k_remux" label="4K REMUX（达 4K 原盘即止）" />
            </el-select>
          </el-form-item>
          <el-form-item label="旧版处理">
            <el-radio-group v-model="form.replace_old">
              <el-radio :value="true">删除旧版本</el-radio>
              <el-radio :value="false">保留共存</el-radio>
            </el-radio-group>
            <div class="form-help">保留共存时旧文件不会被自动删除，可在列表中「清理旧版」手动删除。</div>
          </el-form-item>
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

    <CloudDirPicker
      :visible="pickerVisible"
      :source-type="form.source_type"
      :source-name="'影巢转存'"
      @update:visible="pickerVisible = $event"
      @select="onDirSelected"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useHttpClient } from '@/http/client'
import { isMobile } from '@/utils/deviceUtils'
import CloudDirPicker from './CloudDirPicker.vue'

const http = useHttpClient()

const subs = ref<any[]>([])
const loading = ref(false)

const washTargetLabel = (t: string) => {
  if (!t) return ''
  const map: Record<string, string> = { '1080p': '→1080p', '4k': '→4K', '4k_remux': '→4K REMUX' }
  return map[t] || `→${t}`
}

const sourceTypeName = (t: string) => {
  const map: Record<string, string> = { '123': '123 云盘', guangyapan: '光鸭云盘', pan139: '移动云盘' }
  return map[t] || t || '-'
}

const fmtTime = (t: string) => {
  if (!t) return '-'
  const d = new Date(t)
  return isNaN(d.getTime()) ? '-' : d.toLocaleString('zh-CN', { hour12: false })
}

const parseKeywords = (k: string) => {
  try {
    const v = JSON.parse(k || '[]')
    return Array.isArray(v) ? v : []
  } catch {
    return []
  }
}

const load = async () => {
  loading.value = true
  try {
    const resp = await http.get('/api/cloud/subscriptions', { params: { resource_source: 'hdhive' } })
    if (resp.data?.code === 200) {
      subs.value = (resp.data.data || []).map((s: any) => ({ ...s, keywords: parseKeywords(s.keywords) }))
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
const form = reactive({
  id: 0,
  source_type: '123',
  channel: '',
  keywords: '',
  target_dir: '/媒体库/待整理',
  enabled: true,
  auto_finish: true,
  wash: false,
  wash_target: '',
  replace_old: true,
})

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
  Object.assign(form, {
    id: 0,
    source_type: '123',
    target_dir: '/媒体库/待整理',
    enabled: true,
    auto_finish: true,
    wash: false,
    wash_target: '',
    replace_old: true,
  })
  resetPick()
  formVisible.value = true
}

const openEdit = (row: any) => {
  Object.assign(form, {
    id: row.id,
    source_type: row.source_type || '123',
    target_dir: row.target_dir || '',
    enabled: row.enabled !== false,
    auto_finish: row.auto_finish !== false,
    wash: !!row.wash,
    wash_target: row.wash_target || '',
    replace_old: row.replace_old !== false,
  })
  const rowMediaType = row.media_type || ''
  const rowSeason = row.season || 0
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
  } else {
    resetPick()
  }
  formVisible.value = true
}

const confirmForm = async () => {
  if (!selectedMedia.value) {
    ElMessage.warning('请先选择影片')
    return
  }
  const media = selectedMedia.value
  const mediaType = media.media_type === 'tvshow' ? 'tv' : 'movie'
  const season = mediaType === 'tv' ? selectedSeason.value : 0
  const totalSeasons = mediaType === 'tv' ? selectedMedia.value?.seasons?.length || 0 : 0
  if (mediaType === 'tv' && season === 0 && totalSeasons === 0) {
    ElMessage.warning('请选择要收录的季（未获取到季数信息）')
    return
  }
  formSaving.value = true
  try {
    const payload = {
      source_type: form.source_type,
      resource_source: 'hdhive',
      target_dir: form.target_dir,
      enabled: form.enabled,
      auto_finish: form.auto_finish,
      wash: form.wash,
      wash_target: form.wash_target,
      replace_old: form.replace_old,
      media_type: mediaType,
      tmdb_id: media.tmdb_id,
      tmdb_title: media.title,
      season,
      total_seasons: totalSeasons,
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

const toggleEnabled = async (row: any) => {
  try {
    const resp = await http.put(`/api/cloud/subscriptions/${row.id}`, { enabled: !row.enabled })
    if (resp.data?.code === 200) {
      row.enabled = !row.enabled
      ElMessage.success(row.enabled ? '已启用' : '已停用')
    } else {
      ElMessage.error(resp.data?.message || '操作失败')
    }
  } catch (e: any) {
    ElMessage.error('操作失败：' + (e?.message || ''))
  }
}

const runOnce = async (row: any) => {
  try {
    const resp = await http.post('/api/cloud/subscriptions/run', { id: row.id })
    ElMessage.success(resp.data?.message || '已执行')
  } catch (e: any) {
    ElMessage.error('执行失败：' + (e?.message || ''))
  }
}

const cleanOld = async (row: any) => {
  try {
    const resp = await http.post('/api/cloud/subscriptions/clean-old', { id: row.id })
    ElMessage.success(resp.data?.message || '已清理')
    await load()
  } catch (e: any) {
    ElMessage.error('清理失败：' + (e?.message || ''))
  }
}

const remove = async (row: any) => {
  try {
    await ElMessageBox.confirm(`确定删除订阅「${row.tmdb_title || row.id}」？相关转存记录将一并删除。`, '删除确认', {
      type: 'warning',
    })
  } catch {
    return
  }
  try {
    const resp = await http.delete(`/api/cloud/subscriptions/${row.id}`)
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

const mobile = computed(() => isMobile())
onMounted(load)
</script>

<style scoped>
.cloud-page {
  padding: 12px;
}
.cloud-card {
  border-radius: 8px;
}
.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-weight: 600;
}
.header-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.cloud-alert {
  margin-bottom: 14px;
}
.media-cell {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.media-title {
  font-weight: 500;
}
.wash-tag {
  margin-left: 2px;
}
.muted {
  color: #909399;
  font-size: 12px;
}
.full-width {
  width: 100%;
}
.pick-block {
  width: 100%;
}
.pick-search-row {
  display: flex;
  gap: 8px;
}
.pick-results {
  margin-top: 10px;
  max-height: 300px;
  overflow-y: auto;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
}
.pick-item {
  display: flex;
  gap: 10px;
  padding: 8px;
  cursor: pointer;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.pick-item:last-child {
  border-bottom: none;
}
.pick-item:hover {
  background: var(--el-fill-color-light);
}
.pick-poster {
  width: 56px;
  height: 80px;
  border-radius: 4px;
  flex-shrink: 0;
  background: var(--el-fill-color);
}
.pick-poster-fallback {
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.pick-info {
  flex: 1;
  min-width: 0;
}
.pick-title {
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.pick-year {
  color: var(--el-text-color-secondary);
}
.pick-ot {
  font-size: 12px;
}
.pick-ov {
  font-size: 12px;
  margin-top: 4px;
  overflow: hidden;
  text-overflow: ellipsis;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
}
.pick-selected {
  display: flex;
  gap: 12px;
  padding: 10px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  align-items: flex-start;
}
.pick-season-row {
  margin-top: 8px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.pick-season-select {
  width: 200px;
}
.pick-finish-hint {
  margin-top: 8px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.pick-actions {
  margin-top: 10px;
  display: flex;
  gap: 8px;
}
.form-help {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.5;
  margin-top: 4px;
}
.dir-input-row {
  display: flex;
  gap: 8px;
  width: 100%;
}
</style>
