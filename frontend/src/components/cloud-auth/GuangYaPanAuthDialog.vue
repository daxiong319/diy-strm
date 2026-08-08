<script setup lang="ts">
import { onBeforeUnmount, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
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

const form = reactive({
  auth_mode: 'sms' as 'sms' | 'qrcode' | 'token',
  phone_number: '',
  verification_code: '',
  verification_id: '',
  captcha_token: '',
  access_token: '',
  refresh_token: '',
})
const submitting = ref(false)
const sendCodeLoading = ref(false)
const countdown = ref(0)
let countdownTimer: ReturnType<typeof setInterval> | null = null

const qrCodeUrl = ref('')
const qrUserCode = ref('')
const qrVerificationUri = ref('')
const qrCreating = ref(false)
let qrTimer: ReturnType<typeof setInterval> | null = null
let qrPolling = false

const resetForm = () => {
  if (countdownTimer) {
    clearInterval(countdownTimer)
    countdownTimer = null
  }
  countdown.value = 0
  clearQrCode()
  form.auth_mode = 'sms'
  form.phone_number = ''
  form.verification_code = ''
  form.verification_id = ''
  form.captcha_token = ''
  form.access_token = ''
  form.refresh_token = ''
}

watch(
  () => visible.value,
  (isVisible) => {
    if (isVisible) resetForm()
  },
)

onBeforeUnmount(() => {
  if (countdownTimer) {
    clearInterval(countdownTimer)
  }
  stopQrPolling()
})

const startCountdown = () => {
  countdown.value = 60
  if (countdownTimer) {
    clearInterval(countdownTimer)
  }
  countdownTimer = setInterval(() => {
    countdown.value--
    if (countdown.value <= 0 && countdownTimer) {
      clearInterval(countdownTimer)
      countdownTimer = null
    }
  }, 1000)
}

// 创建扫码登录会话并展示二维码
const handleCreateQrCode = async () => {
  if (!props.accountId) {
    ElMessage.error('缺少账号信息')
    return
  }
  qrCreating.value = true
  try {
    const response = await http.post(`${SERVER_URL}/guangyapan/qrcode`, {
      account_id: props.accountId,
    })
    const data = response?.data
    if (data?.code === 200) {
      const qrData = data.data
      const dataUrl = await QRCode.toDataURL(qrData.verification_uri, {
        width: 200,
        margin: 1,
        color: {
          dark: '#000000',
          light: '#ffffff',
        },
      })
      qrCodeUrl.value = dataUrl
      qrUserCode.value = qrData.user_code || ''
      qrVerificationUri.value = qrData.verification_uri || ''
      startQrPolling(qrData.interval)
      ElMessage.success(data.message || '二维码已生成，请使用光鸭云盘 App 扫码')
    } else {
      ElMessage.error(data?.message || '获取二维码失败')
    }
  } catch (error) {
    console.error('光鸭云盘获取二维码错误:', error)
    ElMessage.error('获取二维码失败')
  } finally {
    qrCreating.value = false
  }
}

// 轮询扫码登录状态
const startQrPolling = (intervalSec: number) => {
  stopQrPolling()
  const intervalMs = Math.max(intervalSec || 3, 2) * 1000
  qrTimer = setInterval(async () => {
    if (qrPolling) return
    qrPolling = true
    try {
      const response = await http.get(`${SERVER_URL}/guangyapan/qrcode/status`, {
        params: { account_id: props.accountId },
      })
      const state = response?.data?.data?.state
      if (!state) return
      if (state === 'success') {
        stopQrPolling()
        ElMessage.success('光鸭云盘扫码授权成功')
        visible.value = false
        emit('confirmed')
      } else if (state === 'denied') {
        stopQrPolling()
        ElMessage.error('扫码登录已取消')
        clearQrCode()
      } else if (state === 'expired') {
        stopQrPolling()
        ElMessage.warning('二维码已过期，请重新获取')
        clearQrCode()
      } else if (state === 'error') {
        stopQrPolling()
        ElMessage.error(response?.data?.data?.message || '扫码登录失败')
        clearQrCode()
      }
    } catch (error) {
      // 网络抖动时容忍失败，下一轮继续轮询
      console.error('光鸭云盘扫码状态轮询错误:', error)
    } finally {
      qrPolling = false
    }
  }, intervalMs)
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
  qrUserCode.value = ''
  qrVerificationUri.value = ''
}

const openQrPage = () => {
  if (qrVerificationUri.value) {
    window.open(qrVerificationUri.value, '_blank')
  }
}

// 发送短信验证码
const handleSendCode = async () => {
  if (!props.accountId) {
    ElMessage.error('缺少账号信息')
    return
  }
  const phone = form.phone_number.trim()
  if (!phone) {
    ElMessage.error('请输入手机号')
    return
  }
  sendCodeLoading.value = true
  try {
    const response = await http.post(`${SERVER_URL}/guangyapan/send-code`, {
      account_id: props.accountId,
      phone_number: phone,
    })
    const data = response?.data
    if (data?.code === 200) {
      form.verification_id = data.data?.verification_id || ''
      form.captcha_token = data.data?.captcha_token || ''
      startCountdown()
      ElMessage.success(data.message || '验证码已发送，请注意查收短信')
    } else if (data?.data?.need_captcha) {
      const captchaUrl = data.data.captcha_url || ''
      ElMessage.warning('光鸭云盘要求完成人机验证')
      if (captchaUrl) {
        ElMessageBox.confirm(
          '光鸭云盘要求完成人机验证，是否在新窗口打开验证页面？完成后可重新发送验证码。',
          '人机验证',
          {
            confirmButtonText: '打开验证',
            cancelButtonText: '取消',
            type: 'warning',
          },
        )
          .then(() => {
            window.open(captchaUrl, '_blank')
          })
          .catch(() => {})
      }
    } else {
      ElMessage.error(data?.message || '发送验证码失败')
    }
  } catch (error) {
    console.error('光鸭云盘发送验证码错误:', error)
    ElMessage.error('发送验证码失败')
  } finally {
    sendCodeLoading.value = false
  }
}

const handleSubmit = async () => {
  if (!props.accountId) {
    ElMessage.error('缺少账号信息')
    return
  }
  const isSms = form.auth_mode === 'sms'
  if (isSms) {
    if (!form.phone_number.trim()) {
      ElMessage.error('请输入手机号')
      return
    }
    if (!form.verification_code.trim()) {
      ElMessage.error('请输入短信验证码')
      return
    }
    if (!form.verification_id) {
      ElMessage.error('请先点击"发送验证码"')
      return
    }
  } else if (!form.access_token.trim()) {
    ElMessage.error('请输入光鸭云盘访问令牌')
    return
  }
  submitting.value = true
  try {
    const response = await http.post(`${SERVER_URL}/guangyapan/login`, {
      account_id: props.accountId,
      phone_number: form.phone_number.trim(),
      verification_code: form.verification_code.trim(),
      verification_id: form.verification_id,
      captcha_token: form.captcha_token,
      access_token: form.access_token.trim(),
      refresh_token: form.refresh_token.trim(),
    })
    if (response?.data.code === 200) {
      ElMessage.success('光鸭云盘授权成功')
      visible.value = false
      emit('confirmed')
    } else {
      ElMessage.error(response?.data.message || '光鸭云盘授权失败')
    }
  } catch (error) {
    console.error('光鸭云盘授权错误:', error)
    ElMessage.error('光鸭云盘授权失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="`光鸭云盘授权 - ${accountName || ''}`"
    :width="isMobile ? '90%' : '500px'"
    destroy-on-close
    align-center
    @closed="resetForm"
  >
    <el-alert type="info" :closable="false" show-icon style="margin-bottom: 16px">
      <template #title>
        光鸭云盘支持三种授权方式：手机号+短信验证码登录、扫码登录，或使用访问令牌（Access Token）。
      </template>
    </el-alert>
    <el-tabs v-model="form.auth_mode">
      <el-tab-pane label="手机号验证码" name="sms">
        <el-form :model="form" label-width="80px">
          <el-form-item label="手机号">
            <el-input v-model="form.phone_number" placeholder="请输入光鸭云盘绑定手机号" clearable>
              <template #prepend>+86</template>
            </el-input>
          </el-form-item>
          <el-form-item label="验证码">
            <div style="display: flex; gap: 8px; width: 100%">
              <el-input v-model="form.verification_code" placeholder="请输入短信验证码" clearable />
              <el-button
                type="primary"
                plain
                :disabled="countdown > 0"
                :loading="sendCodeLoading"
                style="flex-shrink: 0"
                @click="handleSendCode"
              >
                {{ countdown > 0 ? `${countdown}s 后重发` : '发送验证码' }}
              </el-button>
            </div>
          </el-form-item>
        </el-form>
      </el-tab-pane>
      <el-tab-pane label="扫码登录" name="qrcode">
        <div v-if="!qrCodeUrl" style="text-align: center; padding: 24px 0">
          <el-button type="primary" plain :loading="qrCreating" @click="handleCreateQrCode">
            获取二维码
          </el-button>
          <div style="margin-top: 8px; font-size: 12px; color: #909399">
            使用光鸭云盘 App 扫码确认登录
          </div>
        </div>
        <div v-else style="text-align: center">
          <img
            :src="qrCodeUrl"
            alt="光鸭云盘扫码登录二维码"
            style="width: 200px; height: 200px; border: 1px solid #dcdfe6; border-radius: 8px"
          />
          <div style="margin-top: 8px; font-size: 16px; font-weight: 600; letter-spacing: 2px; color: #409eff">
            {{ qrUserCode }}
          </div>
          <div style="margin-top: 4px; font-size: 12px; color: #909399">
            使用光鸭云盘 App 扫码，或访问验证页面输入上方验证码确认登录
          </div>
          <div style="margin-top: 8px">
            <el-button size="small" link type="primary" @click="openQrPage">打开验证页面</el-button>
            <el-button size="small" link type="danger" @click="clearQrCode">取消并重新获取</el-button>
          </div>
        </div>
      </el-tab-pane>
      <el-tab-pane label="令牌授权" name="token">
        <el-form :model="form" label-width="80px">
          <el-form-item label="访问令牌">
            <el-input
              v-model="form.access_token"
              type="textarea"
              :rows="2"
              placeholder="请输入光鸭云盘 Access Token"
              clearable
            />
          </el-form-item>
          <el-form-item label="刷新令牌">
            <el-input
              v-model="form.refresh_token"
              type="textarea"
              :rows="2"
              placeholder="请输入光鸭云盘 Refresh Token（可选）"
              clearable
            />
          </el-form-item>
        </el-form>
      </el-tab-pane>
    </el-tabs>
    <template #footer>
      <span class="dialog-footer">
        <el-button @click="visible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="handleSubmit">登录授权</el-button>
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
