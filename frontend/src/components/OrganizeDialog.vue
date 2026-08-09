<template>
  <el-dialog
    :model-value="modelValue"
    title="目录整理"
    width="min(860px, calc(100vw - 32px))"
    :close-on-click-modal="false"
    append-to-body
    @update:model-value="(value: boolean) => emit('update:modelValue', value)"
    @open="handleOpen"
    @closed="resetDialog"
  >
    <div class="organize-form">
      <el-form label-width="90px" inline>
        <el-form-item label="扫描深度">
          <el-select v-model="depth" style="width: 120px">
            <el-option :value="1" label="1 层" />
            <el-option :value="2" label="2 层" />
            <el-option :value="3" label="3 层" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="previewLoading" @click="loadPreview">
            重新扫描
          </el-button>
        </el-form-item>
      </el-form>

      <el-alert
        v-if="unsupportedSource"
        type="warning"
        show-icon
        :closable="false"
        title="该网盘暂不支持目录整理"
        description="天翼云盘与 139 网盘当前不支持移动文件，无法执行整理。"
      />

      <div v-else-if="previewLoading" class="organize-empty" v-loading="true" />
      <template v-else-if="previewData">
        <div class="organize-summary">
          扫描 {{ previewData.scanned }} 项，可整理 {{ previewData.total }} 个视频文件
          <span v-if="previewData.skipped.length > 0">
            ，{{ previewData.skipped.length }} 个无法识别已跳过
          </span>
        </div>

        <el-table
          v-if="previewData.groups.length > 0"
          :data="previewData.groups"
          max-height="360"
          size="small"
          row-key="rel_path"
        >
          <el-table-column type="expand">
            <template #default="{ row }">
              <div class="organize-detail-list">
                <div v-for="action in getGroupActions(row.rel_path)" :key="action.file_id" class="organize-detail-item">
                  <span class="organize-detail-old">{{ action.old_name }}</span>
                  <span class="organize-detail-arrow">→</span>
                  <span class="organize-detail-new">
                    {{ action.new_name !== action.old_name ? action.new_name : action.old_name }}
                  </span>
                  <span class="organize-detail-target">{{ action.target_rel_path }}</span>
                </div>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="目标目录" min-width="240">
            <template #default="{ row }">
              <span class="organize-target-path">{{ row.rel_path }}</span>
            </template>
          </el-table-column>
          <el-table-column label="文件数" width="80" align="right">
            <template #default="{ row }">{{ row.file_count }}</template>
          </el-table-column>
          <el-table-column label="集数范围" width="120" align="center">
            <template #default="{ row }">
              <span v-if="row.category === 'tv'">
                {{ row.episode_min }}{{ row.episode_max !== row.episode_min ? '-' + row.episode_max : '' }}
              </span>
              <span v-else>-</span>
            </template>
          </el-table-column>
        </el-table>
        <el-empty v-else description="当前目录下未发现可整理的视频文件" />
      </template>
    </div>

    <div v-if="applyResult" class="organize-apply-result">
      <el-divider content-position="left">执行结果</el-divider>
      <el-alert
        :type="applyResult.failed.length > 0 ? 'warning' : 'success'"
        show-icon
        :closable="false"
        :title="`成功 ${applyResult.success.length} 个，失败 ${applyResult.failed.length} 个`"
      />
      <div v-if="applyResult.failed.length > 0" class="organize-failed-list">
        <div v-for="failed in applyResult.failed" :key="failed.file_id" class="organize-failed-item">
          <span class="organize-failed-name">{{ failed.name }}</span>
          <span class="organize-failed-reason">{{ failed.reason }}</span>
        </div>
      </div>
    </div>

    <template #footer>
      <span class="dialog-footer">
        <el-button @click="emit('update:modelValue', false)">关闭</el-button>
        <el-button
          type="primary"
          :loading="applying"
          :disabled="!previewData || previewData.total === 0"
          @click="handleApply"
        >
          执行整理
        </el-button>
      </span>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'

interface Props {
  modelValue: boolean
  accountId: number
  accountSourceType: string
  parentId: string
}

interface OrganizeAction {
  file_id: string
  old_name: string
  new_name: string
  category: string
  title: string
  season?: number
  episode?: number
  year?: number
  target_rel_path: string
  supported: boolean
}

interface OrganizeGroup {
  category: string
  title: string
  season: number
  year: number
  rel_path: string
  file_count: number
  episode_min: number
  episode_max: number
}

interface OrganizePreviewData {
  actions: OrganizeAction[]
  groups: OrganizeGroup[]
  skipped: string[]
  scanned: number
  total: number
}

interface OrganizeApplyItem {
  file_id: string
  new_name: string
  rel_path: string
}

interface OrganizeApplyFailed {
  file_id: string
  name: string
  reason: string
}

interface OrganizeApplyResult {
  success: OrganizeApplyItem[]
  failed: OrganizeApplyFailed[]
}

const props = defineProps<Props>()
const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  applied: []
}>()

const http = useHttpClient()

const depth = ref(2)
const previewLoading = ref(false)
const previewData = ref<OrganizePreviewData | null>(null)
const applying = ref(false)
const applyResult = ref<OrganizeApplyResult | null>(null)

const unsupportedSource = computed(
  () => props.accountSourceType === 'guangyapan' || props.accountSourceType === 'pan139',
)

function getGroupActions(relPath: string): OrganizeAction[] {
  return previewData.value?.actions.filter((action) => action.target_rel_path === relPath) ?? []
}

async function loadPreview() {
  if (!props.parentId) {
    ElMessage.warning('请先进入要整理的目录')
    return
  }
  previewLoading.value = true
  previewData.value = null
  applyResult.value = null
  try {
    const response = await http.post(`${SERVER_URL}/organize/preview`, {
      account_id: props.accountId,
      path: props.parentId,
      depth: depth.value,
    })
    if (response?.data.code === 200) {
      previewData.value = (response.data.data || {}) as OrganizePreviewData
    } else {
      ElMessage.error(response?.data.message || '扫描失败')
    }
  } catch {
    ElMessage.error('扫描失败：网络错误')
  } finally {
    previewLoading.value = false
  }
}

async function handleApply() {
  if (!previewData.value || previewData.value.total === 0) {
    return
  }
  try {
    await ElMessageBox.confirm(
      `将整理 ${previewData.value.total} 个视频文件到目标目录（移动并重命名），确认执行？`,
      '执行整理',
      {
        confirmButtonText: '执行',
        cancelButtonText: '取消',
        type: 'warning',
      },
    )
  } catch {
    return
  }

  applying.value = true
  applyResult.value = null
  try {
    const items = previewData.value.actions.map((action) => ({
      file_id: action.file_id,
      new_name: action.new_name,
      rel_path: action.target_rel_path,
    }))
    const response = await http.post(`${SERVER_URL}/organize/apply`, {
      account_id: props.accountId,
      path: props.parentId,
      items,
    })
    if (response?.data.code === 200) {
      applyResult.value = (response.data.data || {}) as OrganizeApplyResult
      ElMessage.success(response.data.message || '整理完成')
      emit('applied')
    } else {
      ElMessage.error(response?.data.message || '执行失败')
    }
  } catch {
    ElMessage.error('执行失败：网络错误')
  } finally {
    applying.value = false
  }
}

function handleOpen() {
  if (!previewData.value) {
    loadPreview()
  }
}

function resetDialog() {
  previewData.value = null
  applyResult.value = null
}
</script>

<style scoped>
.organize-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.organize-summary {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}

.organize-target-path {
  font-weight: 500;
}

.organize-detail-list {
  padding: 4px 12px 12px 48px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.organize-detail-item {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  flex-wrap: wrap;
}

.organize-detail-old {
  color: var(--el-text-color-secondary);
  text-decoration: line-through;
}

.organize-detail-arrow {
  color: var(--el-text-color-placeholder);
}

.organize-detail-new {
  font-weight: 500;
}

.organize-detail-target {
  color: var(--el-color-primary);
  font-size: 12px;
}

.organize-empty {
  min-height: 100px;
}

.organize-apply-result {
  margin-top: 4px;
}

.organize-failed-list {
  margin-top: 8px;
  max-height: 180px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.organize-failed-item {
  display: flex;
  gap: 8px;
  font-size: 13px;
  align-items: baseline;
}

.organize-failed-name {
  font-weight: 500;
  white-space: nowrap;
}

.organize-failed-reason {
  color: var(--el-text-color-secondary);
}
</style>
