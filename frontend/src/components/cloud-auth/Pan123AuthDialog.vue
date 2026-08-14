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

const authMode = ref<'qrcode' | 'password'>('qrcode')
const form = reactive({
  username: '',
  password: '',
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
  form.username = ''
  form.password = ''
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
    const response = await http.post(`${SERVER_URL}/pan123/qrcode`, {})
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
      ElMessage.success(data.message || '二维码已生成，请使用 123 云盘 App 扫码')
    } else {
      ElMessage.error(data?.message || '获取二维码失败')
    }
  } catch (error) {
    console.error('123 云盘获取二维码错误:', error)
    ElMessage.error('获取二维码失败')
  } finally {
    qrCreating.value = false
  }
}

// 轮询扫码登录状态；成功后自动完成授权
const startQrPolling = () => {
  stopQrPolling()
  qrTimer = setInterval(async () => {
    if (qrPolling) return
    qrPolling = true
    try {
      const response = await http.get(`${SERVER_URL}/pan123/qrcode/status`, {
        params: { token: qrToken.value },
      })
      const state = response?.data?.data?.status
      if (!state) return
      if (state === 'confirmed') {
        stopQrPolling()
        ElMessage.success('扫码成功，正在完成授权...')
        const realToken = response?.data?.data?.token || qrToken.value
        await handleConfirmQrLogin(realToken)
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
      console.error('123 云盘扫码状态轮询错误:', error)
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

// 扫码确认：拿令牌完成账号授权（token 优先用轮询返回的真实 Bearer 令牌）
const handleConfirmQrLogin = async (token = '') => {
  if (!props.accountId) {
    ElMessage.error('缺少账号信息')
    return
  }
  if (!token) {
    token = qrToken.value
  }
  submitting.value = true
  try {
    const response = await http.post(`${SERVER_URL}/pan123/qrcode/confirm`, {
      account_id: props.accountId,
      token,
    })
    if (response?.data.code === 200) {
      ElMessage.success('123 云盘授权成功')
      visible.value = false
      emit('confirmed')
    } else {
      ElMessage.error(response?.data.message || '123 云盘授权失败')
    }
  } catch (error) {
    console.error('123 云盘扫码授权错误:', error)
    ElMessage.error('123 云盘授权失败')
  } finally {
    submitting.value = false
  }
}

// 密码登录（备用；境外 IP 可能触发验证）
const handleSubmit = async () => {
  if (!props.accountId) return
  if (!form.username.trim() || !form.password) {
    ElMessage.warning('请输入 123 云盘用户名（邮箱或手机号）和密码')
    return
  }
  submitting.value = true
  try {
    const response = await http.post(`${SERVER_URL}/pan123/login`, {
      account_id: props.accountId,
      username: form.username.trim(),
      password: form.password,
    })
    if (response?.data.code === 200) {
      ElMessage.success('123 云盘授权成功')
      visible.value = false
      emit('confirmed')
    } else {
      ElMessage.error(response?.data.message || '123 云盘授权失败')
    }
  } catch (error) {
    console.error('123 云盘授权错误：', error)
    ElMessage.error('123 云盘授权失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="`123 云盘授权 - ${accountName || ''}`"
    :width="isMobile ? '90%' : '500px'"
    destroy-on-close
    align-center
    @closed="resetForm"
  >
    <el-alert type="info" :closable="false" show-icon style="margin-bottom: 16px">
      <template #title>
        123 云盘支持 App 扫码登录（境外 IP 不受密码验证限制，令牌 90 天有效，到期需重新扫码）；也可使用用户名密码登录。
      </template>
    </el-alert>
    <el-tabs v-model="authMode">
      <el-tab-pane label="扫码登录" name="qrcode">
        <div v-if="!qrCodeUrl" style="text-align: center; padding: 24px 0">
          <el-button type="primary" plain :loading="qrCreating" @click="handleCreateQrCode">
            获取二维码
          </el-button>
          <div style="margin-top: 8px; font-size: 12px; color: #909399">
            请使用 123 云盘 App 或扫码软件扫描，在手机上确认后自动完成授权
          </div>
        </div>
        <div v-else style="text-align: center">
          <img
            :src="qrCodeUrl"
            alt="扫码登录二维码"
            style="width: 200px; height: 200px; display: inline-block"
          />
          <div style="margin-top: 8px; font-size: 12px; color: #909399">
            请使用 123 云盘 App 扫码，二维码 5 分钟内有效
          </div>
          <div style="margin-top: 8px">
            <el-button size="small" @click="clearQrCode">取消</el-button>
          </div>
        </div>
      </el-tab-pane>
      <el-tab-pane label="用户名密码" name="password">
        <div class="pan123-auth-dialog">
          <div class="pan123-auth-dialog__name">{{ accountName }}</div>
          <el-form label-position="top">
            <el-form-item label="用户名（邮箱或手机号）">
              <el-input
                v-model="form.username"
                placeholder="请输入 123 云盘用户名"
                clearable
              />
            </el-form-item>
            <el-form-item label="密码">
              <el-input
                v-model="form.password"
                type="password"
                placeholder="请输入 123 云盘密码"
                show-password
                @keyup.enter="handleSubmit"
              />
            </el-form-item>
          </el-form>
        </div>
      </el-tab-pane>
    </el-tabs>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button
        v-if="authMode === 'password'"
        type="primary"
        :loading="submitting"
        @click="handleSubmit"
      >
        登录
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.pan123-auth-dialog {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.pan123-auth-dialog__name {
  max-width: 100%;
  overflow-wrap: anywhere;
  color: #606266;
  margin-bottom: 8px;
}
</style>
