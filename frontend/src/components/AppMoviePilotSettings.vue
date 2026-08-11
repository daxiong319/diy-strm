<template>
  <div>
    <div class="main-content-container moviepilot-settings-container">
      <el-alert
        type="info"
        :show-icon="true"
        style="margin-bottom: 12px"
        title="MoviePilot 联动说明"
        description="开启后系统将轮询 MoviePilot 的下载任务，下载完成的影视资源自动上传到所选网盘账号的指定目录，并按文件名解析规则自动整理（重命名 + 分类移动），整理成功的影片生成 STRM 文件到本地输出目录，触发 Emby 媒体库刷新。"
      />
      <el-form
        :model="formData"
        :label-position="checkIsMobile ? 'top' : 'left'"
        :label-width="130"
        class="moviepilot-form"
      >
        <el-form-item label="启用" prop="enabled">
          <div class="enable-switch">
            <el-switch
              v-model="formData.enabled"
              :loading="loading"
              size="large"
              active-text="已启用"
              inactive-text="已禁用"
            />
            <div class="form-help">关闭后系统不再轮询 MoviePilot 下载任务</div>
          </div>
        </el-form-item>

        <el-form-item label="MoviePilot 地址" prop="base_url">
          <el-input
            v-model="formData.base_url"
            placeholder="例如 http://192.168.1.100:3000"
            :disabled="loading || !formData.enabled"
          />
          <div class="form-help">MoviePilot 的 Web 访问地址（同服务器可填 http://127.0.0.1:3000）</div>
        </el-form-item>

        <el-form-item label="API Token" prop="api_token">
          <el-input
            v-model="formData.api_token"
            placeholder="MoviePilot 设置-系统-安全中的 API 令牌"
            :disabled="loading || !formData.enabled"
            show-password
          />
          <div class="form-help">MoviePilot 中 API 令牌，用于调用订阅、下载等接口</div>
        </el-form-item>

        <el-form-item label="下载目录" prop="download_root">
          <div class="path-input-row">
            <el-input
              v-model="formData.download_root"
              placeholder="例如 /downloads"
              :disabled="loading || !formData.enabled"
            />
            <el-button
              :disabled="loading || !formData.enabled"
              @click="openLocalSelector('download_root')"
            >
              选择
            </el-button>
          </div>
          <div class="form-help">MoviePilot 下载器在服务器上的根目录，用于将下载文件映射到本机路径</div>
        </el-form-item>

        <el-form-item label="本机目录" prop="local_view_root">
          <div class="path-input-row">
            <el-input
              v-model="formData.local_view_root"
              placeholder="例如 /data/downloads"
              :disabled="loading || !formData.enabled"
            />
            <el-button
              :disabled="loading || !formData.enabled"
              @click="openLocalSelector('local_view_root')"
            >
              选择
            </el-button>
          </div>
          <div class="form-help">下载根目录在本机对应的实际路径（用于扫描已下载文件）</div>
        </el-form-item>

        <el-form-item label="上传账号" prop="upload_account_id">
          <el-select
            v-model="formData.upload_account_id"
            placeholder="选择目标网盘账号"
            :disabled="loading || !formData.enabled"
            style="width: 100%"
          >
            <el-option
              v-for="account in accounts"
              :key="account.id"
              :label="accountLabel(account)"
              :value="account.id"
            />
          </el-select>
          <div class="form-help">下载完成后上传到该网盘账号；支持 115 / 123 / 百度 / OpenList / 139 / 光鸭</div>
        </el-form-item>

        <el-form-item label="上传根目录" prop="upload_root">
          <div class="path-input-row">
            <el-input
              v-model="formData.upload_root"
              placeholder="例如 /影视/订阅下载"
              :disabled="loading || !formData.enabled"
            />
            <el-button
              :disabled="loading || !formData.enabled || !selectedAccount"
              @click="showNetdiskSelector = true"
            >
              选择
            </el-button>
          </div>
          <div class="form-help">目标网盘中存放下载资源的根目录，留空表示网盘根目录</div>
        </el-form-item>

        <el-form-item label="STRM 输出目录" prop="strm_local_dir">
          <div class="path-input-row">
            <el-input
              v-model="formData.strm_local_dir"
              placeholder="例如 /mnt/strm"
              :disabled="loading || !formData.enabled"
            />
            <el-button
              :disabled="loading || !formData.enabled"
              @click="openLocalSelector('strm_local_dir')"
            >
              选择
            </el-button>
          </div>
          <div class="form-help">整理成功后生成的 STRM 文件输出到本机该目录（留空则不生成 STRM）</div>
        </el-form-item>

        <el-form-item label="轮询间隔" prop="poll_interval">
          <el-input-number
            v-model="formData.poll_interval"
            :min="1"
            :max="60"
            :disabled="loading || !formData.enabled"
            style="width: 200px"
          />
          <div class="form-help">检测 MoviePilot 下载任务完成情况的间隔（分钟），默认 5 分钟</div>
        </el-form-item>

        <el-form-item label="通知" prop="notify_enabled">
          <div class="enable-switch">
            <el-switch
              v-model="formData.notify_enabled"
              :loading="loading"
              active-text="开启"
              inactive-text="关闭"
              :disabled="!formData.enabled"
            />
            <div class="form-help">上传完成、任务失败时通过已配置的通知渠道发送消息</div>
          </div>
        </el-form-item>

        <el-form-item>
          <div class="form-actions">
            <el-button
              type="primary"
              @click="testConnection"
              :loading="testing"
              :disabled="loading || !formData.enabled"
              size="large"
            >
              测试连接
            </el-button>
            <el-button
              type="success"
              @click="saveSettings"
              :loading="loading"
              :disabled="testing"
              size="large"
              :icon="Check"
            >
              保存设置
            </el-button>
          </div>
        </el-form-item>
      </el-form>
    </div>
    <el-alert
      v-if="testStatus"
      :title="testStatus.title"
      :type="testStatus.type"
      :description="testStatus.description"
      :closable="false"
      show-icon
      class="test-status"
      style="margin-top: 12px"
    />

    <el-dialog
      v-model="showNetdiskSelector"
      title="选择网盘上传根目录"
      width="min(480px, calc(100vw - 32px))"
      append-to-body
    >
      <DirectorySelector
        :source-type="selectedAccount?.source_type ?? ''"
        :account-id="formData.upload_account_id"
        :model-value="uploadRootDir"
        @update:model-value="handleNetdiskDirSelect"
      />
    </el-dialog>

    <el-dialog
      v-model="showLocalSelector"
      :title="localSelectorTitle"
      width="min(480px, calc(100vw - 32px))"
      append-to-body
    >
      <LocalDirectorySelector
        :model-value="localSelectorValue"
        @update:model-value="handleLocalDirSelect"
      />
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Check } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import { isMobile } from '@/utils/deviceUtils'
import DirectorySelector from './DirectorySelector.vue'
import LocalDirectorySelector from './LocalDirectorySelector.vue'
import type { DirInfo } from '@/typing'

interface MoviePilotSettings {
  enabled: boolean
  base_url: string
  api_token: string
  download_root: string
  local_view_root: string
  upload_account_id: number
  upload_root: string
  strm_local_dir: string
  poll_interval: number
  notify_enabled: boolean
}

interface TestStatus {
  title: string
  type: 'success' | 'warning' | 'error' | 'info'
  description: string
}

interface NetdiskAccount {
  id: number
  name: string
  username: string
  source_type: '115' | '123' | 'openlist' | 'baidupan' | 'pan139' | 'guangyapan'
}

const checkIsMobile = ref(isMobile())
const http = useHttpClient()
const loading = ref(false)
const testing = ref(false)
const testStatus = ref<TestStatus | null>(null)
const accounts = ref<NetdiskAccount[]>([])

const formData = reactive<MoviePilotSettings>({
  enabled: false,
  base_url: '',
  api_token: '',
  download_root: '',
  local_view_root: '',
  upload_account_id: 0,
  upload_root: '',
  strm_local_dir: '',
  poll_interval: 5,
  notify_enabled: true,
})

const selectedAccount = computed(() =>
  accounts.value.find((account) => account.id === formData.upload_account_id),
)

const accountLabel = (account: NetdiskAccount) => {
  const typeMap: Record<string, string> = {
    '115': '115 网盘',
    '123': '123 云盘',
    openlist: 'OpenList',
    baidupan: '百度网盘',
    pan139: '中国移动云盘',
    guangyapan: '光鸭云盘',
  }
  const typeName = typeMap[account.source_type] || account.source_type
  return `${account.name || account.username || '未命名'}（${typeName}）`
}

const loadAccounts = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/account/list`)
    accounts.value = (response.data.data || []) as NetdiskAccount[]
  } catch (error) {
    console.error('加载网盘账号错误：', error)
  }
}

// 网盘目录选择器
const showNetdiskSelector = ref(false)
const uploadRootDir = ref<DirInfo | null>(null)

const handleNetdiskDirSelect = (dir: DirInfo | null) => {
  if (dir) {
    formData.upload_root = dir.path || ''
  }
  showNetdiskSelector.value = false
}

// 本地目录选择器
const showLocalSelector = ref(false)
const localSelectorField = ref('')
const localSelectorValue = ref('')

const localSelectorTitle = computed(() => {
  const titles: Record<string, string> = {
    download_root: '选择下载目录',
    local_view_root: '选择本机目录',
    strm_local_dir: '选择 STRM 输出目录',
  }
  return titles[localSelectorField.value] || '选择本地目录'
})

const openLocalSelector = (field: string) => {
  localSelectorField.value = field
  localSelectorValue.value = formData[field as keyof MoviePilotSettings] as string
  showLocalSelector.value = true
}

const handleLocalDirSelect = (path: string) => {
  if (localSelectorField.value) {
    ;(formData as Record<string, unknown>)[localSelectorField.value] = path
  }
  showLocalSelector.value = false
}

const testConnection = async () => {
  if (!formData.enabled) {
    ElMessage.warning('请先启用 MoviePilot 联动功能')
    return
  }
  if (!formData.base_url || !formData.api_token) {
    ElMessage.warning('请先填写 MoviePilot 地址和 API Token')
    return
  }
  try {
    testing.value = true
    testStatus.value = null
    const response = await http.post(`${SERVER_URL}/setting/moviepilot/test`, {
      base_url: formData.base_url,
      api_token: formData.api_token,
    })
    if (response?.data.code === 200) {
      testStatus.value = {
        title: '连接测试成功',
        type: 'success',
        description: response?.data?.data?.message || 'MoviePilot 连接正常，订阅和下载接口可用',
      }
      ElMessage.success('MoviePilot 连接测试成功')
    } else {
      testStatus.value = {
        title: '连接测试失败',
        type: 'error',
        description: response?.data.message || '无法连接 MoviePilot，请检查地址和 Token',
      }
      ElMessage.error(response?.data.message || 'MoviePilot 连接测试失败')
    }
  } catch (error) {
    console.error('MoviePilot 连接测试错误：', error)
    testStatus.value = {
      title: '连接测试出错',
      type: 'error',
      description: '测试过程中发生错误，请检查网络连接和配置信息',
    }
    ElMessage.error('MoviePilot 连接测试出错')
  } finally {
    testing.value = false
  }
}

const saveSettings = async () => {
  try {
    loading.value = true
    const response = await http.put(`${SERVER_URL}/setting/moviepilot`, {
      enabled: !!formData.enabled,
      base_url: formData.base_url,
      api_token: formData.api_token,
      download_root: formData.download_root,
      local_view_root: formData.local_view_root,
      upload_account_id: formData.upload_account_id,
      upload_root: formData.upload_root,
      strm_local_dir: formData.strm_local_dir,
      poll_interval: formData.poll_interval,
      notify_enabled: !!formData.notify_enabled,
    })
    if (response?.data.code === 200) {
      ElMessage.success(formData.enabled ? 'MoviePilot 配置已保存并启用' : 'MoviePilot 联动已关闭')
      testStatus.value = {
        title: '保存成功',
        type: 'success',
        description: '配置已保存，下载检测与上传任务将按新配置执行',
      }
    } else {
      ElMessage.error(response?.data.message || '保存设置失败，请重试')
    }
  } catch (error) {
    console.error('保存 MoviePilot 设置错误：', error)
    ElMessage.error('保存设置失败，请重试')
  } finally {
    loading.value = false
  }
}

const loadSettings = async () => {
  try {
    loading.value = true
    const response = await http.get(`${SERVER_URL}/setting/moviepilot`)
    if (response?.data.code === 200 && response.data.data) {
      const data = response.data.data
      formData.enabled = !!data.enabled
      formData.base_url = data.base_url || ''
      formData.api_token = data.api_token || ''
      formData.download_root = data.download_root || ''
      formData.local_view_root = data.local_view_root || ''
      formData.upload_account_id = data.upload_account_id || 0
      formData.upload_root = data.upload_root || ''
      formData.strm_local_dir = data.strm_local_dir || ''
      formData.poll_interval = data.poll_interval || 5
      formData.notify_enabled = data.notify_enabled !== undefined ? !!data.notify_enabled : true
    }
  } catch (error) {
    console.error('加载 MoviePilot 设置错误：', error)
    ElMessage.warning('加载已保存的设置失败')
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadSettings()
  loadAccounts()
})
</script>

<style scoped>
.path-input-row {
  display: flex;
  gap: 8px;
  width: 100%;
}
.path-input-row .el-input {
  flex: 1;
}
</style>
