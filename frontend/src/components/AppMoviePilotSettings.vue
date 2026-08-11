<template>
  <div>
    <div class="main-content-container moviepilot-settings-container">
      <el-alert
        type="info"
        :show-icon="true"
        style="margin-bottom: 12px"
        title="MoviePilot 联动说明"
        description="开启后系统将轮询 MoviePilot 的下载任务，下载完成的影视资源自动上传到中国移动云盘指定目录，并在整理完成后生成 STRM 文件、触发 Emby 媒体库刷新。"
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
          <el-input
            v-model="formData.download_root"
            placeholder="例如 /downloads"
            :disabled="loading || !formData.enabled"
          />
          <div class="form-help">MoviePilot 下载器在服务器上的根目录，用于将下载文件映射到本机路径</div>
        </el-form-item>

        <el-form-item label="本机目录" prop="local_view_root">
          <el-input
            v-model="formData.local_view_root"
            placeholder="例如 /data/downloads"
            :disabled="loading || !formData.enabled"
          />
          <div class="form-help">下载根目录在本机对应的实际路径（用于扫描已下载文件）</div>
        </el-form-item>

        <el-form-item label="上传根目录" prop="upload_root">
          <el-input
            v-model="formData.upload_root"
            placeholder="例如 /影视/待整理"
            :disabled="loading || !formData.enabled"
          />
          <div class="form-help">139 云盘中存放下载资源的根目录，可填写空字符串表示云盘根目录</div>
        </el-form-item>

        <el-form-item label="同步路径 ID" prop="sync_path_id">
          <el-input
            v-model="formData.sync_path_id"
            placeholder="填写后上传完成后触发指定 STRM 同步路径，留空自动触发全部 STRM 同步"
            :disabled="loading || !formData.enabled"
          />
          <div class="form-help">STRM 同步路径 ID，留空则上传完成后触发全量 STRM 同步</div>
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
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Check } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import { isMobile } from '@/utils/deviceUtils'

interface MoviePilotSettings {
  enabled: boolean
  base_url: string
  api_token: string
  download_root: string
  local_view_root: string
  upload_root: string
  sync_path_id: string
  poll_interval: number
  notify_enabled: boolean
}

interface TestStatus {
  title: string
  type: 'success' | 'warning' | 'error' | 'info'
  description: string
}

const checkIsMobile = ref(isMobile())
const http = useHttpClient()
const loading = ref(false)
const testing = ref(false)
const testStatus = ref<TestStatus | null>(null)

const formData = reactive<MoviePilotSettings>({
  enabled: false,
  base_url: '',
  api_token: '',
  download_root: '',
  local_view_root: '',
  upload_root: '',
  sync_path_id: '',
  poll_interval: 5,
  notify_enabled: true,
})

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
      enabled: formData.enabled ? 1 : 0,
      base_url: formData.base_url,
      api_token: formData.api_token,
      download_root: formData.download_root,
      local_view_root: formData.local_view_root,
      upload_root: formData.upload_root,
      sync_path_id: formData.sync_path_id,
      poll_interval: formData.poll_interval,
      notify_enabled: formData.notify_enabled ? 1 : 0,
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
      formData.upload_root = data.upload_root || ''
      formData.sync_path_id = data.sync_path_id ? String(data.sync_path_id) : ''
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
})
</script>
