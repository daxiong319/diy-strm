<template>
  <el-dialog
    :model-value="modelValue"
    title="光鸭小号秒传"
    width="min(960px, calc(100vw - 32px))"
    :close-on-click-modal="false"
    append-to-body
    @update:model-value="(value: boolean) => emit('update:modelValue', value)"
    @open="handleOpen"
    @closed="resetDialog"
  >
    <el-form label-width="90px">
      <el-form-item label="源账号">
        <el-select
          v-model="accountId"
          filterable
          placeholder="选择光鸭云盘账号"
          style="width: 100%"
          @change="handleAccountChange"
        >
          <el-option
            v-for="account in accounts"
            :key="account.id"
            :value="account.id"
            :label="`${account.name}（光鸭网盘）`"
          />
        </el-select>
      </el-form-item>
    </el-form>

    <template v-if="accountId">
      <el-divider content-position="left">开发者配置</el-divider>
      <div v-if="!settingConfigured" class="setting-row">
        <el-input v-model="settingForm.clientId" placeholder="开发者 client_id" style="width: 280px" />
        <el-input
          v-model="settingForm.clientSecret"
          placeholder="开发者 client_secret"
          type="password"
          show-password
          style="width: 280px"
        />
        <el-button type="primary" :loading="savingSetting" @click="handleSaveSetting">保存</el-button>
      </div>
      <div v-else class="setting-row">
        <el-tag type="success">已配置</el-tag>
        <span class="setting-info">client_id：{{ settingClientId }}（{{ settingSecretHint }}）</span>
        <el-button size="small" @click="settingConfigured = false">修改</el-button>
        <el-button size="small" type="danger" @click="handleDeleteSetting">删除</el-button>
      </div>

      <el-divider content-position="left">接收 TOKEN</el-divider>
      <div class="token-row">
        <el-input v-model="tokenForm.tokenId" placeholder="接收 TOKEN（在小号上生成并分享）" style="width: 320px" />
        <el-input v-model="tokenForm.remark" placeholder="备注（可选）" style="width: 200px" />
        <el-button type="primary" :loading="addingToken" @click="handleAddToken">添加</el-button>
      </div>
      <div v-if="tokens.length > 0" class="token-list">
        <el-tag
          v-for="token in tokens"
          :key="token.id"
          :type="receiverTokenId === token.id ? 'primary' : 'info'"
          closable
          class="token-tag"
          @close="handleDeleteToken(token.id)"
          @click="receiverTokenId = token.id"
        >
          {{ token.token_id }}<span v-if="token.remark">（{{ token.remark }}）</span>
        </el-tag>
      </div>
      <el-alert
        v-else
        type="info"
        show-icon
        :closable="false"
        title="尚未添加接收 TOKEN"
        description="需要一个小号光鸭账号，在该账号的接收 TOKEN 列表中生成 TOKEN 并授权目录后填入上方。"
      />

      <el-divider content-position="left">选择文件</el-divider>
      <div class="path-select-row">
        <el-input :model-value="sourceDir ? sourceDir.path || sourceDir.name : '根目录'" readonly>
          <template #append>
            <el-button @click="showSelector = true">选择目录</el-button>
          </template>
        </el-input>
      </div>
      <el-button
        class="scan-btn"
        type="primary"
        :loading="scanLoading"
        :disabled="!sourceDir"
        @click="loadScan"
      >
        加载目录文件
      </el-button>
      <div v-if="scanData" class="scan-summary">
        共 {{ scanData.total }} 个文件，已选 {{ selectedFiles.length }} / 20
      </div>
      <el-table
        v-if="scanData && scanData.files.length > 0"
        :data="scanData.files"
        max-height="280"
        size="small"
        row-key="source_file_id"
        @selection-change="handleSelectionChange"
      >
        <el-table-column type="selection" width="42" />
        <el-table-column label="文件" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">{{ row.rel_path }}</template>
        </el-table-column>
        <el-table-column label="大小" width="110" align="right">
          <template #default="{ row }">{{ formatFileSize(row.size) }}</template>
        </el-table-column>
      </el-table>
    </template>

    <template v-if="accountId">
      <el-divider content-position="left">任务记录</el-divider>
      <el-table :data="tasks" max-height="220" size="small" row-key="id">
        <el-table-column label="ID" width="64">
          <template #default="{ row }">#{{ row.id }}</template>
        </el-table-column>
        <el-table-column label="TOKEN" min-width="140" show-overflow-tooltip>
          <template #default="{ row }">{{ row.receiver_token }}</template>
        </el-table-column>
        <el-table-column label="数量" width="90" align="center">
          <template #default="{ row }">
            <span v-if="row.status === 'success'">
              {{ row.success_count }}/{{ row.total_count }}
            </span>
            <span v-else>{{ row.total_count }}</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="110" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.status === 'running'" type="primary" size="small">执行中</el-tag>
            <el-tag v-else-if="row.status === 'auditing'" type="warning" size="small">预审中</el-tag>
            <el-tag v-else-if="row.status === 'success'" type="success" size="small">成功</el-tag>
            <el-tag v-else type="danger" size="small">失败</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="创建时间" width="150">
          <template #default="{ row }">{{ formatTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="70" align="center">
          <template #default="{ row }">
            <el-button
              v-if="row.status === 'failed' || row.status === 'success'"
              link
              type="danger"
              size="small"
              @click="handleDeleteTask(row.id)"
            >
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>
      <div v-if="taskError" class="task-error">{{ taskError }}</div>
    </template>

    <template #footer>
      <span class="dialog-footer">
        <el-button @click="emit('update:modelValue', false)">关闭</el-button>
        <el-button
          type="primary"
          :loading="submitting"
          :disabled="selectedFiles.length === 0 || !receiverTokenId"
          @click="handleSubmit"
        >
          开始小号秒传
        </el-button>
      </span>
    </template>

    <el-dialog v-model="showSelector" title="选择目录" width="min(480px, calc(100vw - 32px))" append-to-body>
      <DirectorySelector
        :source-type="'guangyapan'"
        :account-id="accountId ?? 0"
        :model-value="sourceDir"
        @update:model-value="handleDirSelect"
      />
    </el-dialog>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
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
  source_type: string
}

interface GuangYaScanFile {
  source_file_id: string
  rel_path: string
  size: number
}

interface GuangYaScanData {
  files: GuangYaScanFile[]
  total: number
}

interface GuangYaToken {
  id: number
  token_id: string
  remark: string
}

interface GuangYaTask {
  id: number
  receiver_token: string
  total_count: number
  success_count: number
  skipped_count: number
  failed_count: number
  status: string
  error_message: string
  created_at: string
}

const props = defineProps<Props>()
const emit = defineEmits<{
  'update:modelValue': [value: boolean]
}>()

const http = useHttpClient()

const accounts = ref<NetdiskAccount[]>([])
const accountId = ref<number | null>(null)

const settingConfigured = ref(false)
const settingClientId = ref('')
const settingSecretHint = ref('')
const settingForm = ref({ clientId: '', clientSecret: '' })
const savingSetting = ref(false)

const tokens = ref<GuangYaToken[]>([])
const tokenForm = ref({ tokenId: '', remark: '' })
const addingToken = ref(false)
const receiverTokenId = ref<number | null>(null)

const sourceDir = ref<DirInfo | null>(null)
const showSelector = ref(false)
const scanLoading = ref(false)
const scanData = ref<GuangYaScanData | null>(null)
const selectedFiles = ref<GuangYaScanFile[]>([])

const tasks = ref<GuangYaTask[]>([])
const taskError = ref('')
const submitting = ref(false)

let taskTimer: number | null = null

const account = computed(() => accounts.value.find((item) => item.id === accountId.value))

function sourceTypeLabel(): string {
  return '光鸭网盘'
}

function formatTime(value: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getMonth() + 1}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

async function loadAccounts() {
  try {
    const response = await http.get(`${SERVER_URL}/account/list`)
    if (response?.data.code === 200) {
      const all = (response.data.data || []) as NetdiskAccount[]
      accounts.value = all.filter((item) => item.source_type === 'guangyapan')
      if (accountId.value === null && props.defaultSourceAccountId) {
        accountId.value = accounts.value.some((item) => item.id === props.defaultSourceAccountId)
          ? props.defaultSourceAccountId
          : null
      }
      if (accountId.value === null && accounts.value.length === 1) {
        accountId.value = accounts.value[0].id
      }
      if (accountId.value) {
        await loadSetting()
        await loadTokens()
        await loadTasks()
      }
    } else {
      ElMessage.error(response?.data.message || '加载账号列表失败')
    }
  } catch {
    ElMessage.error('加载账号列表失败：网络错误')
  }
}

async function handleAccountChange() {
  sourceDir.value = null
  scanData.value = null
  selectedFiles.value = []
  receiverTokenId.value = null
  if (accountId.value) {
    await loadSetting()
    await loadTokens()
    await loadTasks()
  }
}

async function loadSetting() {
  settingConfigured.value = false
  settingClientId.value = ''
  settingSecretHint.value = ''
  if (!accountId.value) return
  try {
    const response = await http.get(
      `${SERVER_URL}/guangya/developer-setting?account_id=${accountId.value}`,
    )
    if (response?.data.code === 200 && response.data.data?.configured) {
      settingConfigured.value = true
      settingClientId.value = response.data.data.client_id
      settingSecretHint.value = response.data.data.secret_hint
    }
  } catch {
    // 忽略加载失败，保持未配置状态
  }
}

async function handleSaveSetting() {
  if (!accountId.value) return
  if (!settingForm.value.clientId.trim() || !settingForm.value.clientSecret.trim()) {
    ElMessage.warning('请填写 client_id 和 client_secret')
    return
  }
  savingSetting.value = true
  try {
    const response = await http.post(`${SERVER_URL}/guangya/developer-setting`, {
      account_id: accountId.value,
      client_id: settingForm.value.clientId.trim(),
      client_secret: settingForm.value.clientSecret.trim(),
    })
    if (response?.data.code === 200) {
      ElMessage.success(response.data.message || '已保存')
      settingForm.value = { clientId: '', clientSecret: '' }
      await loadSetting()
    } else {
      ElMessage.error(response?.data.message || '保存失败')
    }
  } catch {
    ElMessage.error('保存失败：网络错误')
  } finally {
    savingSetting.value = false
  }
}

async function handleDeleteSetting() {
  if (!accountId.value) return
  try {
    await ElMessageBox.confirm('确认删除该账号的开发者配置？', '删除开发者配置', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
    })
  } catch {
    return
  }
  try {
    const response = await http.delete(
      `${SERVER_URL}/guangya/developer-setting?account_id=${accountId.value}`,
    )
    if (response?.data.code === 200) {
      ElMessage.success(response.data.message || '已删除')
      settingConfigured.value = false
    } else {
      ElMessage.error(response?.data.message || '删除失败')
    }
  } catch {
    ElMessage.error('删除失败：网络错误')
  }
}

async function loadTokens() {
  tokens.value = []
  receiverTokenId.value = null
  if (!accountId.value) return
  try {
    const response = await http.get(`${SERVER_URL}/guangya/receiver-tokens?account_id=${accountId.value}`)
    if (response?.data.code === 200) {
      tokens.value = (response.data.data || []) as GuangYaToken[]
      if (tokens.value.length > 0) {
        receiverTokenId.value = tokens.value[0].id
      }
    }
  } catch {
    // 忽略
  }
}

async function handleAddToken() {
  if (!accountId.value) return
  if (!tokenForm.value.tokenId.trim()) {
    ElMessage.warning('请填写接收 TOKEN')
    return
  }
  addingToken.value = true
  try {
    const response = await http.post(`${SERVER_URL}/guangya/receiver-tokens`, {
      account_id: accountId.value,
      token_id: tokenForm.value.tokenId.trim(),
      remark: tokenForm.value.remark.trim(),
    })
    if (response?.data.code === 200) {
      ElMessage.success(response.data.message || '已添加')
      tokenForm.value = { tokenId: '', remark: '' }
      await loadTokens()
    } else {
      ElMessage.error(response?.data.message || '添加失败')
    }
  } catch {
    ElMessage.error('添加失败：网络错误')
  } finally {
    addingToken.value = false
  }
}

async function handleDeleteToken(id: number) {
  try {
    await ElMessageBox.confirm('确认删除该接收 TOKEN？', '删除接收 TOKEN', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
    })
  } catch {
    return
  }
  try {
    const response = await http.delete(`${SERVER_URL}/guangya/receiver-tokens/${id}`)
    if (response?.data.code === 200) {
      ElMessage.success(response.data.message || '已删除')
      await loadTokens()
    } else {
      ElMessage.error(response?.data.message || '删除失败')
    }
  } catch {
    ElMessage.error('删除失败：网络错误')
  }
}

function handleDirSelect(dir: DirInfo | null) {
  showSelector.value = false
  sourceDir.value = dir
  scanData.value = null
  selectedFiles.value = []
}

async function loadScan() {
  if (!accountId.value || !sourceDir.value) return
  scanLoading.value = true
  scanData.value = null
  selectedFiles.value = []
  try {
    const response = await http.post(`${SERVER_URL}/crosstransfer/scan`, {
      account_id: accountId.value,
      path: sourceDir.value.id,
    })
    if (response?.data.code === 200) {
      scanData.value = (response.data.data || {}) as GuangYaScanData
      if (scanData.value.total === 0) {
        ElMessage.info('该目录下没有文件')
      }
    } else {
      ElMessage.error(response?.data.message || '加载失败')
    }
  } catch {
    ElMessage.error('加载失败：网络错误')
  } finally {
    scanLoading.value = false
  }
}

function handleSelectionChange(rows: GuangYaScanFile[]) {
  selectedFiles.value = rows.slice(0, 20)
}

async function handleSubmit() {
  if (!accountId.value || !receiverTokenId.value || selectedFiles.value.length === 0) return
  try {
    await ElMessageBox.confirm(
      `将把 ${selectedFiles.value.length} 个文件通过开发者接口秒传到接收 TOKEN 账号，确认执行？`,
      '开始小号秒传',
      {
        confirmButtonText: '开始秒传',
        cancelButtonText: '取消',
        type: 'warning',
      },
    )
  } catch {
    return
  }
  submitting.value = true
  try {
    const response = await http.post(`${SERVER_URL}/guangya/small-transfer`, {
      account_id: accountId.value,
      receiver_token_id: receiverTokenId.value,
      file_ids: selectedFiles.value.map((file) => file.source_file_id),
    })
    if (response?.data.code === 200) {
      ElMessage.success(response.data.message || '任务已创建')
      selectedFiles.value = []
      await loadTasks()
    } else {
      ElMessage.error(response?.data.message || '创建任务失败')
    }
  } catch {
    ElMessage.error('创建任务失败：网络错误')
  } finally {
    submitting.value = false
  }
}

async function loadTasks() {
  if (!accountId.value) return
  try {
    const response = await http.get(`${SERVER_URL}/guangya/small-transfer?account_id=${accountId.value}`)
    if (response?.data.code === 200) {
      tasks.value = (response.data.data || []) as GuangYaTask[]
      const running = tasks.value.find(
        (task) => task.status === 'running' || task.status === 'auditing',
      )
      taskError.value = ''
      if (running?.status === 'failed' && running.error_message) {
        taskError.value = running.error_message
      }
      if (!running) {
        stopTaskTimer()
      } else if (taskTimer === null) {
        startTaskTimer()
      }
    }
  } catch {
    // 忽略
  }
}

async function handleDeleteTask(id: number) {
  try {
    const response = await http.delete(`${SERVER_URL}/guangya/small-transfer/${id}`)
    if (response?.data.code === 200) {
      ElMessage.success(response.data.message || '已删除')
      await loadTasks()
    } else {
      ElMessage.error(response?.data.message || '删除失败')
    }
  } catch {
    ElMessage.error('删除失败：网络错误')
  }
}

function startTaskTimer() {
  taskTimer = window.setInterval(() => {
    void loadTasks()
  }, 3000)
}

function stopTaskTimer() {
  if (taskTimer !== null) {
    window.clearInterval(taskTimer)
    taskTimer = null
  }
}

function handleOpen() {
  if (accounts.value.length === 0) {
    void loadAccounts()
  } else if (accountId.value) {
    void loadTasks()
  }
}

function resetDialog() {
  stopTaskTimer()
  scanData.value = null
  selectedFiles.value = []
}

onBeforeUnmount(() => {
  stopTaskTimer()
})
</script>

<style scoped>
.setting-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.setting-info {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}

.token-row {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.token-list {
  margin-top: 8px;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.token-tag {
  cursor: pointer;
}

.path-select-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}

.scan-btn {
  margin-bottom: 8px;
}

.scan-summary {
  color: var(--el-text-color-secondary);
  font-size: 13px;
  margin-bottom: 6px;
}

.task-error {
  color: var(--el-color-danger);
  font-size: 13px;
  margin-top: 6px;
}
</style>
