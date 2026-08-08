<template>
  <el-dialog
    :model-value="modelValue"
    title="命名对齐"
    width="min(760px, calc(100vw - 32px))"
    :close-on-click-modal="false"
    append-to-body
    @update:model-value="(value: boolean) => emit('update:modelValue', value)"
    @closed="resetDialog"
  >
    <el-form label-width="90px" class="name-align-form">
      <el-form-item label="媒体类型">
        <el-radio-group v-model="mediaType" @change="handleMediaTypeChange">
          <el-radio-button value="tvshow">剧集</el-radio-button>
          <el-radio-button value="movie">电影</el-radio-button>
        </el-radio-group>
      </el-form-item>

      <el-form-item label="媒体标题">
        <div class="media-title-row">
          <el-input
            v-model="mediaTitle"
            placeholder="留空则使用文件名解析出的标题"
            clearable
            @keyup.enter="handleTmdbSearch"
          />
          <el-button :loading="searchLoading" @click="handleTmdbSearch">搜索 TMDB</el-button>
        </div>
        <div v-if="searchResults.length > 0" class="tmdb-result-list">
          <div
            v-for="result in searchResults"
            :key="result.tmdb_id"
            class="tmdb-result-item"
            :class="{ selected: selectedTmdbId === result.tmdb_id }"
            @click="handleSelectTmdbResult(result)"
          >
            <span class="tmdb-result-title">{{ result.title }}</span>
            <span v-if="result.original_title !== result.title" class="tmdb-result-original">
              {{ result.original_title }}
            </span>
            <span class="tmdb-result-year">{{ result.year }}</span>
          </div>
        </div>
      </el-form-item>

      <el-form-item v-if="mediaType === 'movie'" label="年份">
        <el-input-number v-model="year" :min="1900" :max="2100" :controls="false" />
      </el-form-item>

      <el-form-item label="对齐文件">
        <div class="file-select-info">
          共 {{ candidateFiles.length }} 个媒体文件，已选 {{ selectedFileIds.length }} 个
          <el-button link type="primary" @click="toggleSelectAll">
            {{ isAllSelected ? '取消全选' : '全选' }}
          </el-button>
        </div>
        <div class="file-select-list">
          <label v-for="file in candidateFiles" :key="file.id" class="file-select-item">
            <el-checkbox
              :model-value="selectedFileIds.includes(file.id)"
              @change="(checked: boolean | string | number) => toggleSelectFile(file.id, !!checked)"
            />
            <span class="file-select-name">{{ file.name }}</span>
          </label>
        </div>
      </el-form-item>
    </el-form>

    <div class="preview-toolbar">
      <el-button type="primary" :loading="previewLoading" :disabled="selectedFileIds.length === 0" @click="handlePreview">
        预览
      </el-button>
      <span v-if="changedCount > 0" class="preview-summary">
        将有 {{ changedCount }} 个文件重命名
      </span>
      <span v-if="previewMessage" class="preview-message">{{ previewMessage }}</span>
    </div>

    <el-table v-if="previewItems.length > 0" :data="previewItems" max-height="320" size="small">
      <el-table-column label="原名" min-width="220">
        <template #default="{ row }">
          <span class="old-name">{{ row.old_name }}</span>
        </template>
      </el-table-column>
      <el-table-column label="新名" min-width="220">
        <template #default="{ row }">
          <span :class="['new-name', { changed: row.changed }]">{{ row.new_name || row.old_name }}</span>
          <span v-if="row.reason" class="reason-text">{{ row.reason }}</span>
        </template>
      </el-table-column>
    </el-table>

    <template #footer>
      <span class="dialog-footer">
        <el-button @click="emit('update:modelValue', false)">取消</el-button>
        <el-button
          type="primary"
          :loading="applying"
          :disabled="changedCount === 0"
          @click="handleApply"
        >
          应用重命名
        </el-button>
      </span>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useHttpClient } from '@/http/client'
import type { FileSystemItem } from '@/typing'
import { SERVER_URL } from '@/const'

interface Props {
  modelValue: boolean
  accountId: number
  parentId: string
  files: FileSystemItem[]
}

interface TmdbSearchResult {
  tmdb_id: number
  title: string
  original_title: string
  year: string
  poster_url: string
  overview: string
}

interface NameAlignPreviewRow {
  file_id: string
  old_name: string
  new_name: string
  changed: boolean
  reason?: string
}

const props = defineProps<Props>()
const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  applied: []
}>()

const http = useHttpClient()

const mediaType = ref<'tvshow' | 'movie'>('tvshow')
const mediaTitle = ref('')
const year = ref<number | undefined>(undefined)
const selectedFileIds = ref<string[]>([])

const searchLoading = ref(false)
const searchResults = ref<TmdbSearchResult[]>([])
const selectedTmdbId = ref<number | null>(null)

const previewLoading = ref(false)
const previewItems = ref<NameAlignPreviewRow[]>([])
const previewMessage = ref('')
const applying = ref(false)

const candidateFiles = computed(() =>
  props.files.filter((file) => !file.is_directory && file.type !== 'directory'),
)

const isAllSelected = computed(
  () =>
    candidateFiles.value.length > 0 &&
    selectedFileIds.value.length === candidateFiles.value.length,
)

const changedCount = computed(
  () => previewItems.value.filter((item) => item.changed).length,
)

function toggleSelectFile(fileId: string, checked: boolean) {
  if (checked) {
    if (!selectedFileIds.value.includes(fileId)) {
      selectedFileIds.value.push(fileId)
    }
  } else {
    selectedFileIds.value = selectedFileIds.value.filter((id) => id !== fileId)
  }
}

function toggleSelectAll() {
  if (isAllSelected.value) {
    selectedFileIds.value = []
  } else {
    selectedFileIds.value = candidateFiles.value.map((file) => file.id)
  }
}

function handleMediaTypeChange() {
  searchResults.value = []
  selectedTmdbId.value = null
}

async function handleTmdbSearch() {
  const keyword = mediaTitle.value.trim()
  if (!keyword) {
    ElMessage.warning('请先输入媒体标题')
    return
  }

  searchLoading.value = true
  try {
    const params: Record<string, string | number> = {
      name: keyword,
      type: mediaType.value === 'movie' ? 'movie' : 'tv_show',
    }
    if (year.value) {
      params.year = year.value
    }
    const response = await http.get(`${SERVER_URL}/scrape/tmdb-search`, {
      params,
      timeout: 30000,
    })
    if (response?.data.code === 200) {
      searchResults.value = (response.data.data || []) as TmdbSearchResult[]
      if (searchResults.value.length === 0) {
        ElMessage.info('TMDB 未找到匹配结果，可直接使用输入的标题')
      }
    } else {
      ElMessage.error(response?.data.message || '搜索失败')
    }
  } catch {
    ElMessage.error('搜索失败：网络错误')
  } finally {
    searchLoading.value = false
  }
}

function handleSelectTmdbResult(result: TmdbSearchResult) {
  selectedTmdbId.value = result.tmdb_id
  mediaTitle.value = result.title
  const resultYear = parseInt(result.year, 10)
  if (Number.isInteger(resultYear) && resultYear >= 1900) {
    year.value = resultYear
  }
  searchResults.value = []
}

async function handlePreview() {
  const selectedFiles = candidateFiles.value.filter((file) =>
    selectedFileIds.value.includes(file.id),
  )
  if (selectedFiles.length === 0) {
    return
  }

  previewLoading.value = true
  previewMessage.value = ''
  try {
    const response = await http.post(`${SERVER_URL}/files/name-align/preview`, {
      account_id: props.accountId,
      parent_id: props.parentId,
      media_title: mediaTitle.value.trim(),
      media_type: mediaType.value,
      year: year.value || 0,
      items: selectedFiles.map((file) => ({ file_id: file.id, name: file.name })),
    })
    if (response?.data.code === 200) {
      previewItems.value = (response.data.data || []) as NameAlignPreviewRow[]
      const failedCount = previewItems.value.filter((item) => item.reason).length
      previewMessage.value =
        failedCount > 0 ? `共 ${failedCount} 个文件无法对齐，请查看原因` : ''
      if (changedCount.value === 0 && failedCount === 0) {
        ElMessage.info('所有文件命名已符合规范，无需修改')
      }
    } else {
      ElMessage.error(response?.data.message || '预览失败')
    }
  } catch {
    ElMessage.error('预览失败：网络错误')
  } finally {
    previewLoading.value = false
  }
}

async function handleApply() {
  const changedItems = previewItems.value.filter((item) => item.changed)
  if (changedItems.length === 0) {
    return
  }

  applying.value = true
  try {
    const response = await http.post(`${SERVER_URL}/files/name-align/apply`, {
      account_id: props.accountId,
      parent_id: props.parentId,
      items: changedItems.map((item) => ({
        file_id: item.file_id,
        name: item.old_name,
        new_name: item.new_name,
      })),
    })
    if (response?.data.code === 200) {
      const result = response.data.data as {
        success: { file_id: string; old_name: string; new_name: string }[]
        failed: { file_id: string; name: string; reason: string }[]
      }
      if (result.failed.length > 0) {
        ElMessage.warning(`部分重命名失败：${result.failed[0].reason}`)
      } else {
        ElMessage.success(`成功重命名 ${result.success.length} 个文件`)
      }
      emit('applied')
      emit('update:modelValue', false)
    } else {
      ElMessage.error(response?.data.message || '应用失败')
    }
  } catch {
    ElMessage.error('应用失败：网络错误')
  } finally {
    applying.value = false
  }
}

function resetDialog() {
  mediaType.value = 'tvshow'
  mediaTitle.value = ''
  year.value = undefined
  selectedFileIds.value = []
  searchResults.value = []
  selectedTmdbId.value = null
  previewItems.value = []
  previewMessage.value = ''
}
</script>

<style scoped>
.name-align-form :deep(.el-form-item) {
  margin-bottom: 14px;
}

.media-title-row {
  display: flex;
  gap: 8px;
  width: 100%;
}

.tmdb-result-list {
  margin-top: 8px;
  max-height: 200px;
  overflow-y: auto;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
}

.tmdb-result-item {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 6px 10px;
  cursor: pointer;
}

.tmdb-result-item:hover {
  background: var(--el-fill-color-light);
}

.tmdb-result-item.selected {
  background: var(--el-color-primary-light-9);
}

.tmdb-result-title {
  font-weight: 600;
}

.tmdb-result-original {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.tmdb-result-year {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  margin-left: auto;
}

.file-select-info {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  color: var(--el-text-color-secondary);
  font-size: 13px;
}

.file-select-list {
  width: 100%;
  max-height: 220px;
  overflow-y: auto;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  padding: 4px 8px;
}

.file-select-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
  cursor: pointer;
}

.file-select-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.preview-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin: 4px 0 10px;
}

.preview-summary {
  color: var(--el-color-primary);
  font-size: 13px;
}

.preview-message {
  color: var(--el-color-warning);
  font-size: 13px;
}

.old-name {
  color: var(--el-text-color-secondary);
}

.new-name {
  font-weight: 600;
}

.new-name.changed {
  color: var(--el-color-primary);
}

.reason-text {
  display: block;
  color: var(--el-color-warning);
  font-size: 12px;
  font-weight: normal;
}
</style>
