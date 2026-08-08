<script setup lang="ts">
import { onBeforeUnmount, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import * as QRCode from 'qrcode'
import { useHttpClient } from '@/http/client'
import { useDeviceType } from '@/composables/useDeviceType'
import { SERVER_URL } from '@/const'

const visible = defineModel<boolean>('visible', { required: true })

const props = defineProps<{
  accountId: number | null
  accountName: string
}>()

const emit = defineEmits<{
  confirmed: []
}>()

const http = useHttpClient()
const { isMobile } = useDeviceType()

const authMode = ref<'qrcode' | 'token'>('qrcode')
const form = reactive({
  authorization: '',
  username: '',
})
const submitting = ref(false)

const qrCodeUrl = ref('')
const qrToken = ref('')
const qrCreating = ref(false)
let qrTimer: ReturnType<typeof setInterval> | null = null
let qrPolling = false

const resetForm = () => {
  clearQrCode()
  authMode.value = 'qrcode'
  form.authorization = ''
  form.username = ''
}

watch(
  () => visible.value,
  (isVisible) => {
    if (isVisible) resetForm()
  },
)

onBeforeUnmount(() => stopQrPolling())

// 创建扫码登录会话并展示二维码
const handleCreateQrCode = async () => {
  if (!props.accountId) {
    ElMessage.error('缺少账号信息')
    return
  }
  qrCreating.value = true
  try {
    const response = await http.post(`${SERVER_URL}/pan139/qrcode`, {})
    const data = response?.data
    if (data?.code === 200) {
      const qrData = data.data
      const dataUrl = await QRCode.toDataURL(qrData.qr_url, {
        width: 200,
        margin: 1,
        color: {
          dark: '#000000',
          light: '#ffffff',
        },
      })
      qrCodeUrl.value = dataUrl
      qrToken.value = qrData.token || ''
      startQrPolling()
      ElMessage.success(data.message || '二维码已生成，请使用移动云盘 App 扫码')
    } else {
      ElMessage.error(data?.message || '获取二维码失败')
    }
  } catch (error) {
    console.error('移动云盘获取二维码错误:', error)
    ElMessage.error('获取二维码失败')
  } finally {
    qrCreating.value = false
  }
}

// 轮询扫码登录状态；成功后自动填充凭据并完成授权
const startQrPolling = () => {
  stopQrPolling()
  qrTimer = setInterval(async () => {
    if (qrPolling) return
    qrPolling = true
    try {
      const response = await http.get(`${SERVER_URL}/pan139/qrcode/status`, {
        params: { token: qrToken.value },
      })
      const state = response?.data?.data?.status
      if (!state) return
      if (state === 'success') {
        stopQrPolling()
        // 自动填充凭据并提交登录（复用凭据授权接口完成验证与保存）
        form.authorization = response?.data?.data?.authorization || ''
        form.username = response?.data?.data?.username || ''
        ElMessage.success('扫码成功，正在完成授权...')
        await handleLogin()
        clearQrCode()
      } else if (state === 'expired') {
        stopQrPolling()
        ElMessage.warning('二维码已过期，请重新获取')
        clearQrCode()
      } else if (state === 'cancelled') {
        stopQrPolling()
        ElMessage.error('扫码登录已取消')
        clearQrCode()
      } else if (state === 'failed') {
        stopQrPolling()
        ElMessage.error(response?.data?.data?.message || '扫码登录失败')
        clearQrCode()
      }
    } catch (error) {
      // 网络抖动时容忍失败，下一轮继续轮询
      console.error('移动云盘扫码状态轮询错误:', error)
    } finally {
      qrPolling = false
    }
  }, 3000)
}

const stopQrPolling = () => {
  if (qrTimer) {
    clearInterval(qrTimer)
    qrTimer = null
  }
  qrPolling = false
}

const clearQrCode = () => {
  stopQrPolling()
  qrCodeUrl.value = ''
  qrToken.value = ''
}

const handleLogin = async () => {
  if (!props.accountId) {
    ElMessage.error('缺少账号信息')
    return
  }
  if (!form.authorization.trim()) {
    ElMessage.error('请输入 Authorization 凭据')
    return
  }
  submitting.value = true
  try {
    const response = await http.post(`${SERVER_URL}/pan139/login`, {
      account_id: props.accountId,
      authorization: form.authorization.trim(),
      username: form.username.trim(),
    })
    if (response?.data.code === 200) {
      ElMessage.success('移动云盘授权成功')
      visible.value = false
      emit('confirmed')
    } else {
      ElMessage.error(response?.data.message || '移动云盘授权失败')
    }
  } catch (error) {
    console.error('移动云盘授权错误:', error)
    ElMessage.error('移动云盘授权失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="`移动云盘授权 - ${accountName || ''}`"
    :width="isMobile ? '90%' : '500px'"
    destroy-on-close
    align-center
    @closed="resetForm"
  >
    <el-alert type="info" :closable="false" show-icon style="margin-bottom: 16px">
      <template #title>
        移动云盘（中国移动云盘 139）支持扫码登录自动获取凭据，或手动粘贴浏览器抓取的 Authorization 凭据；凭据内含令牌与过期时间，到期自动刷新。
      </template>
    </el-alert>
    <el-tabs v-model="authMode">
      <el-tab-pane label="扫码登录" name="qrcode">
        <div v-if="!qrCodeUrl" style="text-align: center; padding: 24px 0">
          <el-button type="primary" plain :loading="qrCreating" @click="handleCreateQrCode">
            获取二维码
          </el-button>
          <div style="margin-top: 8px; font-size: 12px; color: #909399">
            请使用中国移动云盘 App 或微信扫码，确认后自动完成授权
          </div>
        </div>
        <div v-else style="text-align: center">
          <img
            :src="qrCodeUrl"
            alt="移动云盘扫码登录二维码"
            style="width: 200px; height: 200px; border: 1px solid #dcdfe6; border-radius: 8px"
          />
          <div style="margin-top: 8px; font-size: 12px; color: #909399">
            请使用中国移动云盘 App「扫一扫」扫码，并在手机上确认登录
          </div>
          <div style="margin-top: 8px">
            <el-button size="small" link type="danger" @click="clearQrCode">取消并重新获取</el-button>
          </div>
        </div>
      </el-tab-pane>
      <el-tab-pane label="凭据授权" name="token">
        <el-form :model="form" label-width="90px">
          <el-form-item label="Authorization">
            <el-input
              v-model="form.authorization"
              type="textarea"
              :rows="3"
              placeholder="浏览器登录 yun.139.com 后抓取的 Authorization 请求头（base64 编码）"
              clearable
            />
          </el-form-item>
          <el-form-item label="账号名称">
            <el-input
              v-model="form.username"
              placeholder="手机号/邮箱（可选，留空自动从凭据解析）"
              clearable
            />
          </el-form-item>
        </el-form>
      </el-tab-pane>
    </el-tabs>
    <template #footer>
      <span class="dialog-footer">
        <el-button @click="visible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="handleLogin">登录授权</el-button>
      </span>
    </template>
  </el-dialog>
</template>

<style scoped>
.dialog-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
