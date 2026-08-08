<script setup lang="ts">
import { onBeforeUnmount, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import * as QRCode from 'qrcode'
import { useHttpClient } from '@/http/client'
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

const activeTab = ref<'qrcode' | 'token'>('qrcode')

const qrLoading = ref(false)
const qrUrl = ref('')
const qrToken = ref('')
const qrTip = ref('')
const qrStatus = ref<'idle' | 'waiting' | 'success' | 'expired' | 'failed'>('idle')
let qrTimer: number | undefined

const tokenForm = reactive({
  authorization: '',
  username: '',
})
const submitting = ref(false)

const stopPolling = () => {
  if (qrTimer !== undefined) {
    window.clearInterval(qrTimer)
    qrTimer = undefined
  }
}

const resetAll = () => {
  stopPolling()
  qrUrl.value = ''
  qrToken.value = ''
  qrTip.value = ''
  qrStatus.value = 'idle'
  tokenForm.authorization = ''
  tokenForm.username = ''
}

watch(
  () => visible.value,
  (isVisible) => {
    if (isVisible) {
      if (activeTab.value === 'qrcode') void startQrLogin()
    } else {
      stopPolling()
    }
  },
)

watch(activeTab, (tab) => {
  stopPolling()
  if (tab === 'qrcode' && visible.value) void startQrLogin()
})

onBeforeUnmount(() => stopPolling())

const renderQrCode = async (content: string) => {
  await new Promise<void>((resolve) => {
    const el = document.getElementById('pan139-qr-canvas')
    if (!el) {
      resolve()
      return
    }
    void QRCode.toCanvas(el, content, {
      width: 220,
      margin: 2,
      errorCorrectionLevel: 'M',
      color: { dark: '#1f2937', light: '#ffffff' },
    })
      .then(() => resolve())
      .catch((error) => {
        console.error('移动云盘二维码生成失败：', error)
        resolve()
      })
  })
}

const startQrLogin = async () => {
  if (!props.accountId || qrLoading.value) return
  stopPolling()
  qrLoading.value = true
  qrStatus.value = 'idle'
  qrTip.value = ''
  try {
    const response = await http.get(`${SERVER_URL}/pan139/qrcode`)
    if (response?.data.code === 200 && response.data.data) {
      qrUrl.value = response.data.data.qr_url
      qrToken.value = response.data.data.token
      await renderQrCode(response.data.data.qr_url)
      qrTip.value = '请用中国移动云盘 App「扫一扫」扫码并确认登录'
      qrStatus.value = 'waiting'
      startPolling()
    } else {
      ElMessage.error(response?.data.message || '获取移动云盘二维码失败')
    }
  } catch (error) {
    console.error('获取移动云盘二维码错误：', error)
    ElMessage.error('获取移动云盘二维码失败')
  } finally {
    qrLoading.value = false
  }
}

const startPolling = () => {
  stopPolling()
  qrTimer = window.setInterval(async () => {
    if (!qrToken.value) return
    try {
      const response = await http.get(`${SERVER_URL}/pan139/qrcode/status`, {
        params: { token: qrToken.value },
      })
      if (response?.data.code !== 200) {
        ElMessage.error(response?.data.message || '查询扫码状态失败')
        stopPolling()
        return
      }
      const data = response.data.data
      if (data?.status === 'success') {
        stopPolling()
        qrStatus.value = 'success'
        qrTip.value = '扫码成功，正在保存凭据…'
        await submitAuthorization(data.authorization, data.username || '')
        return
      }
      if (data?.status === 'expired' || data?.status === 'failed' || data?.status === 'cancelled') {
        stopPolling()
        qrStatus.value = 'failed'
        qrTip.value = data.message || '扫码已失效，请重新获取二维码'
        return
      }
      qrStatus.value = 'waiting'
      if (data?.message) qrTip.value = data.message
    } catch (error) {
      console.error('查询移动云盘扫码状态错误：', error)
      stopPolling()
      ElMessage.error('查询扫码状态失败')
    }
  }, 2000)
}

const submitAuthorization = async (authorization: string, username: string) => {
  if (!props.accountId || !authorization) return
  try {
    const response = await http.post(`${SERVER_URL}/pan139/login`, {
      account_id: props.accountId,
      authorization,
      username,
    })
    if (response?.data.code === 200) {
      ElMessage.success('移动云盘授权成功')
      visible.value = false
      emit('confirmed')
    } else {
      ElMessage.error(response?.data.message || '移动云盘凭据保存失败')
      qrStatus.value = 'failed'
      qrTip.value = response?.data.message || '凭据保存失败'
    }
  } catch (error) {
    console.error('移动云盘凭据保存错误：', error)
    ElMessage.error('移动云盘凭据保存失败')
    qrStatus.value = 'failed'
  }
}

const handleTokenSubmit = async () => {
  if (!props.accountId) return
  if (!tokenForm.authorization.trim()) {
    ElMessage.warning('请输入 Authorization 凭据')
    return
  }
  submitting.value = true
  try {
    const response = await http.post(`${SERVER_URL}/pan139/login`, {
      account_id: props.accountId,
      authorization: tokenForm.authorization.trim(),
      username: tokenForm.username.trim(),
    })
    if (response?.data.code === 200) {
      ElMessage.success('移动云盘授权成功')
      visible.value = false
      emit('confirmed')
    } else {
      ElMessage.error(response?.data.message || '移动云盘授权失败')
    }
  } catch (error) {
    console.error('移动云盘授权错误：', error)
    ElMessage.error('移动云盘授权失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog
    v-model="visible"
    title="移动云盘授权"
    width="420px"
    destroy-on-close
    align-center
    @closed="resetAll"
  >
    <div class="pan139-auth-dialog">
      <div class="pan139-auth-dialog__name">{{ accountName }}</div>
      <el-tabs v-model="activeTab">
        <el-tab-pane label="扫码登录" name="qrcode">
          <div class="pan139-auth-dialog__qr">
            <div v-if="qrUrl" class="pan139-auth-dialog__qr-box">
              <canvas id="pan139-qr-canvas" width="220" height="220" />
            </div>
            <el-skeleton v-else animated>
              <template #template>
                <el-skeleton-item variant="rect" style="width: 220px; height: 220px" />
              </template>
            </el-skeleton>
            <el-tag
              v-if="qrTip"
              :type="qrStatus === 'failed' ? 'danger' : qrStatus === 'success' ? 'success' : 'info'"
              class="pan139-auth-dialog__tip"
            >
              {{ qrTip }}
            </el-tag>
            <el-button
              :icon="undefined"
              :loading="qrLoading"
              :disabled="qrStatus === 'waiting'"
              @click="startQrLogin"
            >
              重新获取二维码
            </el-button>
          </div>
        </el-tab-pane>
        <el-tab-pane label="凭据登录" name="token">
          <el-form label-position="top">
            <el-form-item label="Authorization 凭据（Base64）">
              <el-input
                v-model="tokenForm.authorization"
                type="password"
                placeholder="浏览器抓取的 Authorization（base64 编码）"
                show-password
              />
            </el-form-item>
            <el-form-item label="账号显示名（可选）">
              <el-input
                v-model="tokenForm.username"
                placeholder="手机号/邮箱，留空自动解析"
                clearable
                @keyup.enter="handleTokenSubmit"
              />
            </el-form-item>
          </el-form>
        </el-tab-pane>
      </el-tabs>
    </div>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button
        v-if="activeTab === 'token'"
        type="primary"
        :loading="submitting"
        @click="handleTokenSubmit"
      >
        验证
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.pan139-auth-dialog {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.pan139-auth-dialog__name {
  max-width: 100%;
  overflow-wrap: anywhere;
  color: #606266;
  margin-bottom: 4px;
}

.pan139-auth-dialog__qr {
  display: grid;
  justify-items: center;
  gap: 12px;
}

.pan139-auth-dialog__qr-box {
  width: 220px;
  height: 220px;
}

.pan139-auth-dialog__tip {
  width: 100%;
  max-width: 100%;
  box-sizing: border-box;
  white-space: normal;
  text-align: center;
  line-height: 1.5;
  height: auto;
  padding: 6px 9px;
}
</style>
