<template>
  <el-dialog
    :model-value="modelValue"
    title="跨盘秒传"
    width="min(960px, calc(100vw - 32px))"
    :close-on-click-modal="false"
    append-to-body
    @update:model-value="(value: boolean) => emit('update:modelValue', value)"
    @open="handleOpen"
    @closed="resetDialog"
  >
    <div class="cross-transfer-form">
      <el-form label-width="90px">
        <el-form-item label="源账号">
          <el-select
            v-model="sourceAccountId"
            filterable
            placeholder="选择源网盘账号"
            style="width: 100%"
            @change="handleSourceAccountChange"
          >
            <el-option
              v-for="account in sourceAccounts"
              :key="account.id"
              :value="account.id"
              :label="`${account.name}（${sourceTypeLabel(account.source_type)}）`"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="源目录">
          <div class="path-select-row">
            <el-input
              :model-value="sourceDir ? sourceDir.path || sourceDir.name : '根目录'"
              readonly
              placeholder="请选择源目录"
            >
              <template #append>
                <el-button :disabled="!sourceAccountId" @click="showSourceSelector = true">
                  选择
                </el-button>
              </template>
            </el-input>
          </div>
        </el-form-item>
        <el-form-item label="目标账号">
          <el-select
            v-model="targetAccountId"
            filterable
            placeholder="选择目标网盘账号"
            style="width: 100%"
            @change="handleTargetAccountChange"
          >
            <el-option
              v-for="account in targetAccounts"
              :key="account.id"
              :value="account.id"
              :label="`${account.name}（${sourceTypeLabel(account.source_type)}）`"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="目标目录">
          <div class="path-select-row">
            <el-input
              :model-value="targetDir ? targetDir.path || targetDir.name : '根目录'"
              readonly
              placeholder="请选择目标目录"
            >
              <template #append>
                <el-button :disabled="!targetAccountId" @click="showTargetSelector = true">
                  选择
                </el-button>
              </template>
            </el-input>
          </div>
        </el-form-item>
        <el-form-item>
          <el-button
            type="primary"
            :loading="scanLoading"
            :disabled="!sourceAccountId || !sourceDir"
            @click="loadScan"
          >
            扫描源目录
          </el-button>
        </el-form-item>
      </el-form>

      <el-alert
        v-if="scanData && scanData.truncated"
        type="warning"
        show-icon
        :closable="false"
        title="扫描数量已达上限"
        description="源目录文件过多，仅显示前 2000 个文件。"
      />

      <template v-if="scanData">
        <div class="scan-summary">
          扫描到 {{ scanData.total }} 个文件
          <span v-if="selectedCount > 0">，已勾选 {{ selectedCount }} 个</span>
        </div>
        <el-table
          ref="fileTableRef"
          :data="scanData.files"
          max-height="320"
          size="small"
          row-key="rel_path"
          @selection-change="handleSelectionChange"
        >
          <el-table-column type="selection" width="42" />
          <el-table-column label="文件" min-width="240" show-overflow-tooltip>
            <template #default="{ row }">{{ row.rel_path }}</template>
          </el-table-column>
          <el-table-column label="大小" width="110" align="right">
            <template #default="{ row }">{{ formatFileSize(row.size) }}</template>
          </el-table-column>
          <el-table-column label="指纹" width="160">
            <template #default="{ row }">
              <span v-if="row.sha1" class="fingerprint">SHA1</span>
              <span v-else-if="row.md5" class="fingerprint">MD5</span>
              <span v-else class="fingerprint-none">无</span>
            </template>
          </el-table-column>
        </el-table>
      </template>

      <div v-if="scanData && selectedFiles.length > 0" class="conflict-row">
        <el-radio-group v-model="conflict" size="small">
          <el-radio-button value="skip">同名跳过</el-radio-button>
          <el-radio-button value="rename">同名重命名</el-radio-button>
          <el-radio-button value="overwrite">同名覆盖</el-radio-button>
        </el-radio-group>
      </div>
    </div>

    <div v-if="executeResult" class="cross-transfer-result">
      <el-divider content-position="left">执行结果</el-divider>
      <el-alert
        :type="executeResult.failed > 0 ? 'warning' : 'success'"
        show-icon
        :closable="false"
        :title="`秒传 ${executeResult.rapid} 个，中转 ${executeResult.relay} 个，失败 ${executeResult.failed} 个`"
      />
      <el-table :data="executeResult.results" max-height="240" size="small" row-key="rel_path">
        <el-table-column label="文件" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">{{ row.rel_path }}</template>
        </el-table-column>
        <el-table-column label="方式" width="110" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.mode === 'rapid'" type="success" size="small">秒传</el-tag>
            <el-tag v-else-if="row.mode === 'relay'" type="primary" size="small">中转</el-tag>
            <el-tag v-else type="danger" size="small">失败</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="说明" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.mode === 'error'" class="result-error">{{ row.error }}</span>
            <span v-else-if="row.mode === 'relay'" class="result-relay">已加入上传队列</span>
            <span v-else class="result-rapid">秒传成功</span>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <template #footer>
      <span class="dialog-footer">
        <el-button @click="emit('update:modelValue', false)">关闭</el-button>
        <el-button
          type="primary"
          :loading="executing"
          :disabled="selectedFiles.length === 0 || !targetAccountId || !targetDir"
          @click="handleExecute"
        >
          开始传输
        </el-button>
      </span>
    </template>

    <el-dialog
      v-model="showSourceSelector"
      title="选择源目录"
      width="min(480px, calc(100vw - 32px))"
      append-to-body
    >
      <DirectorySelector
        :source-type="sourceAccount?.source_type ?? ''"
        :account-id="sourceAccountId ?? 0"
        :model-value="sourceDir"
        @update:model-value="handleSourceDirSelect"
      />
    </el-dialog>

    <el-dialog
      v-model="showTargetSelector"
      title="选择目标目录"
      width="min(480px, calc(100vw - 32px))"
      append-to-body
    >
      <DirectorySelector
        :source-type="targetAccount?.source_type ?? ''"
        :account-id="targetAccountId ?? 0"
        :model-value="targetDir"
        @update:model-value="handleTargetDirSelect"
      />
    </el-dialog>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox, type TableInstance } from 'element-plus'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import { formatFileSize } from '@/utils/fileSizeUtils'
import DirectorySelector from './DirectorySelector.vue'
import type { DirInfo } from '@/typing'

interface Props {
  modelValue: boolean
  defaultSourceAccountId?: number
}

interface NetdiskAccount {
  id: number
  name: string
  username: string
  source_type: '115' | '123' | 'openlist' | 'baidupan' | 'pan139' | 'guangyapan'
}

interface CrossTransferScanFile {
  source_file_id: string
  download_id: string
  rel_path: string
  rel_dir: string
  name: string
  size: number
  sha1: string
  md5: string
}

interface CrossTransferScanData {
  files: CrossTransferScanFile[]
  total: number
  truncated: boolean
}

interface CrossTransferResult {
  rel_path: string
  name: string
  mode: 'rapid' | 'relay' | 'skip' | 'error'
  success: boolean
  file_id: string
  error: string
}

interface CrossTransferExecuteData {
  results: CrossTransferResult[]
  rapid: number
  relay: number
  skipped: number
  failed: number
}

const props = defineProps<Props>()
const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  applied: []
}>()

const http = useHttpClient()

const accounts = ref<NetdiskAccount[]>([])
const sourceAccountId = ref<number | null>(null)
const targetAccountId = ref<number | null>(null)
const sourceDir = ref<DirInfo | null>(null)
const targetDir = ref<DirInfo | null>(null)
const showSourceSelector = ref(false)
const showTargetSelector = ref(false)
const scanLoading = ref(false)
const scanData = ref<CrossTransferScanData | null>(null)
const fileTableRef = ref<TableInstance>()
const selectedFiles = ref<CrossTransferScanFile[]>([])
const conflict = ref<'skip' | 'rename' | 'overwrite'>('rename')
const executing = ref(false)
const executeResult = ref<CrossTransferExecuteData | null>(null)

const sourceAccount = computed(() =>
  accounts.value.find((account) => account.id === sourceAccountId.value),
)
const targetAccount = computed(() =>
  accounts.value.find((account) => account.id === targetAccountId.value),
)
const sourceAccounts = computed(() => accounts.value)
const targetAccounts = computed(() => accounts.value)
const selectedCount = computed(() => selectedFiles.value.length)

function sourceTypeLabel(sourceType: string): string {
  const labels: Record<string, string> = {
    '115': '115',
    '123': '123 云盘',
    openlist: 'OpenList',
    baidupan: '百度网盘',
    pan139: '139 网盘',
    guangyapan: '光鸭网盘',
  }
  return labels[sourceType] || sourceType
}

async function loadAccounts() {
  try {
    const response = await http.get(`${SERVER_URL}/account/list`)
    if (response?.data.code === 200) {
      accounts.value = (response.data.data || []) as NetdiskAccount[]
      if (sourceAccountId.value === null && props.defaultSourceAccountId) {
        sourceAccountId.value = accounts.value.some(
          (account) => account.id === props.defaultSourceAccountId,
        )
          ? props.defaultSourceAccountId
          : null
      }
    } else {
      ElMessage.error(response?.data.message || '加载账号列表失败')
    }
  } catch {
    ElMessage.error('加载账号列表失败：网络错误')
  }
}

function handleSourceAccountChange() {
  sourceDir.value = null
  scanData.value = null
  executeResult.value = null
  selectedFiles.value = []
}

function handleTargetAccountChange() {
  targetDir.value = null
  executeResult.value = null
}

function handleSourceDirSelect(dir: DirInfo | null) {
  showSourceSelector.value = false
  sourceDir.value = dir
  scanData.value = null
  executeResult.value = null
  selectedFiles.value = []
}

function handleTargetDirSelect(dir: DirInfo | null) {
  showTargetSelector.value = false
  targetDir.value = dir
  executeResult.value = null
}

function handleSelectionChange(rows: CrossTransferScanFile[]) {
  selectedFiles.value = rows
}

async function loadScan() {
  if (!sourceAccountId.value || !sourceDir.value) return
  scanLoading.value = true
  scanData.value = null
  executeResult.value = null
  selectedFiles.value = []
  try {
    const response = await http.post(`${SERVER_URL}/crosstransfer/scan`, {
      account_id: sourceAccountId.value,
      path: sourceDir.value.id,
    })
    if (response?.data.code === 200) {
      scanData.value = (response.data.data || {}) as CrossTransferScanData
      if (scanData.value.total === 0) {
        ElMessage.info('该目录下没有文件')
      }
    } else {
      ElMessage.error(response?.data.message || '扫描失败')
    }
  } catch {
    ElMessage.error('扫描失败：网络错误')
  } finally {
    scanLoading.value = false
  }
}

async function handleExecute() {
  if (selectedFiles.value.length === 0 || !targetAccountId.value || !targetDir.value) return
  try {
    await ElMessageBox.confirm(
      `将把 ${selectedFiles.value.length} 个文件传输到目标网盘，未命中秒传的文件会自动加入上传队列中转，确认执行？`,
      '开始跨盘传输',
      {
        confirmButtonText: '开始传输',
        cancelButtonText: '取消',
        type: 'warning',
      },
    )
  } catch {
    return
  }

  executing.value = true
  executeResult.value = null
  try {
    const response = await http.post(`${SERVER_URL}/crosstransfer/execute`, {
      source_account_id: sourceAccountId.value,
      target_account_id: targetAccountId.value,
      source_path: sourceDir.value?.id ?? '',
      target_path: targetDir.value?.id ?? '',
      conflict: conflict.value,
      files: selectedFiles.value.map((file) => ({
        source_file_id: file.source_file_id,
        download_id: file.download_id,
        rel_path: file.rel_path,
        rel_dir: file.rel_dir,
        name: file.name,
        size: file.size,
        sha1: file.sha1,
        md5: file.md5,
      })),
    })
    if (response?.data.code === 200) {
      executeResult.value = (response.data.data || {}) as CrossTransferExecuteData
      ElMessage.success(response.data.message || '传输完成')
      emit('applied')
    } else {
      ElMessage.error(response?.data.message || '执行失败')
    }
  } catch {
    ElMessage.error('执行失败：网络错误')
  } finally {
    executing.value = false
  }
}

function handleOpen() {
  if (accounts.value.length === 0) {
    void loadAccounts()
  }
}

function resetDialog() {
  scanData.value = null
  executeResult.value = null
  selectedFiles.value = []
}

onMounted(() => {
  void loadAccounts()
})
</script>

<style scoped>
.cross-transfer-form {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.path-select-row {
  width: 100%;
}

.scan-summary {
  color: var(--el-text-color-secondary);
  font-size: 13px;
  margin-bottom: 6px;
}

.fingerprint {
  color: var(--el-color-success);
  font-size: 12px;
}

.fingerprint-none {
  color: var(--el-text-color-placeholder);
  font-size: 12px;
}

.conflict-row {
  margin-top: 10px;
  display: flex;
  justify-content: flex-end;
}

.cross-transfer-result {
  margin-top: 4px;
}

.result-error {
  color: var(--el-color-danger);
  font-size: 13px;
}

.result-relay {
  color: var(--el-color-primary);
  font-size: 13px;
}

.result-rapid {
  color: var(--el-color-success);
  font-size: 13px;
}
</style>
