<template>
  <el-dialog
    :model-value="visible"
    title="夸克网盘登录"
    width="520px"
    :close-on-click-modal="false"
    @update:model-value="emit('update:visible', $event)"
  >
    <el-tabs v-model="mode">
      <el-tab-pane label="扫码登录" name="qr">
        <div v-if="!qrUrl" class="qr-empty">
          <el-button type="primary" :loading="qrLoading" @click="startQr">生成二维码</el-button>
        </div>
        <div v-else class="qr-box">
          <img :src="qrDataUrl" class="qr-img" alt="夸克登录二维码" />
          <p class="qr-tip">用夸克 App 或微信扫码登录</p>
          <p class="qr-status" :class="qrStatus">{{ qrStatusText }}</p>
          <el-button size="small" @click="startQr">重新生成</el-button>
        </div>
      </el-tab-pane>
      <el-tab-pane label="短信验证码登录" name="sms">
        <el-form label-width="90px">
          <el-form-item label="手机号">
            <el-input v-model="smsForm.phone" placeholder="夸克绑定的手机号" maxlength="11" />
          </el-form-item>
          <el-form-item label="验证码">
            <div style="display: flex; gap: 8px; width: 100%">
              <el-input v-model="smsForm.code" placeholder="短信验证码" maxlength="6" />
              <el-button :disabled="smsCountdown > 0 || smsSending" :loading="smsSending" @click="sendSms">
                {{ smsCountdown > 0 ? `${smsCountdown}s` : '获取验证码' }}
              </el-button>
            </div>
          </el-form-item>
          <div id="captcha-element" class="captcha-element"></div>
          <p class="qr-status" :class="smsStatus">{{ smsStatusText }}</p>
          <el-alert type="info" :closable="false" show-icon>
            <p>点击「获取验证码」按提示完成滑块验证后，短信将发送到手机。收到验证码后点「登录」。</p>
          </el-alert>
        </el-form>
      </el-tab-pane>
      <el-tab-pane label="Cookie 登录" name="cookie">
        <el-form label-width="90px">
          <el-form-item label="备注名">
            <el-input v-model="form.name" placeholder="可留空" clearable />
          </el-form-item>
          <el-form-item label="Cookie">
            <el-input
              v-model="form.cookie"
              type="textarea"
              :rows="6"
              placeholder="粘贴夸克网盘 Cookie（需包含 __pus/__puus 会话字段）"
            />
          </el-form-item>
          <el-alert type="info" :closable="false" show-icon>
            <p>获取方式：浏览器打开 pan.quark.cn 登录后 → F12 → 应用/Cookies → 复制 __pus、__puus 等字段，格式如 <code>__pus=xxx; __puus=yyy</code>。</p>
          </el-alert>
        </el-form>
      </el-tab-pane>
    </el-tabs>
    <template #footer>
      <el-button @click="emit('update:visible', false)">取消</el-button>
      <el-button v-if="mode === 'cookie'" type="primary" :loading="submitting" @click="submitCookie">{{ submitting ? '登录中…' : '登录' }}</el-button>
      <el-button v-else-if="mode === 'sms'" type="primary" @click="commitSmsLogin">登录</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, watch, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import { http } from '@/http/client'
import { SERVER_URL } from '@/const'

const props = defineProps<{ visible: boolean; accountId?: number | null; accountName?: string }>()
const emit = defineEmits<{ 'update:visible': [boolean]; confirmed: [] }>()

const mode = ref<'qr' | 'sms' | 'cookie'>('qr')
const submitting = ref(false)
const form = ref({ name: '', cookie: '' })

// 短信验证码登录：后端全代理（会话 cookie 在服务端），前端嵌阿里云滑块收集 captchaVerifyParam
const smsForm = ref({ phone: '', code: '' })
const smsStatus = ref('')
const smsStatusText = ref('')
const smsSending = ref(false)
const smsCountdown = ref(0)
const smsSessionId = `sms_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`
let smsCountdownTimer: ReturnType<typeof setInterval> | null = null
// 阿里云验证码 2.0 回调产物（滑块/无感验证通过后的 captchaVerifyParam）
let captchaVerifyParam = ''
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const anyWin = window as any

const CAPTCHA_CONFIG = {
  prefix: 'gd0da3',
  sceneId: '1ml1ondj',
  slideSceneId: 'y9x7vx0s',
  region: 'cn',
  appKey: 'FFFF0N0000000000ABDE',
  element: '#captcha-element',
  mode: 'embed',
}

const loadCaptchaScript = (): Promise<void> =>
  new Promise((resolve, reject) => {
    if (anyWin.initAliyunCaptcha) return resolve()
    ;(anyWin as Record<string, unknown>).AliyunCaptchaConfig = {
      region: CAPTCHA_CONFIG.region,
      prefix: CAPTCHA_CONFIG.prefix,
    }
    const el = document.createElement('script')
    el.src = 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js'
    el.charset = 'utf-8'
    el.onload = () => {
      const timer = setInterval(() => {
        if (anyWin.initAliyunCaptcha) {
          clearInterval(timer)
          resolve()
        }
      }, 100)
      setTimeout(() => {
        clearInterval(timer)
        reject(new Error('阿里云验证码 SDK 加载超时'))
      }, 8000)
    }
    el.onerror = () => reject(new Error('阿里云验证码 SDK 加载失败'))
    document.head.appendChild(el)
  })

const initCaptcha = async () => {
  try {
    await loadCaptchaScript()
    if (anyWin.AliyunCaptchaV2?.captchaInstance) return
    anyWin.initAliyunCaptcha({
      ...CAPTCHA_CONFIG,
      captchaVerifyCallback: async (param: string) => {
        captchaVerifyParam = param
        return { captchaResult: true, bizResult: true }
      },
      onBizResultCallback: () => {},
      getInstance: (instance: unknown) => {
        anyWin.AliyunCaptchaV2 = anyWin.AliyunCaptchaV2 || {}
        anyWin.AliyunCaptchaV2.captchaInstance = instance
      },
    })
  } catch (e: any) {
    smsStatus.value = 'error'
    smsStatusText.value = e?.message || '滑块组件加载失败'
  }
}

const startCountdown = () => {
  smsCountdown.value = 60
  smsCountdownTimer = setInterval(() => {
    smsCountdown.value -= 1
    if (smsCountdown.value <= 0 && smsCountdownTimer) {
      clearInterval(smsCountdownTimer)
      smsCountdownTimer = null
    }
  }, 1000)
}

const sendSms = async () => {
  const phone = smsForm.value.phone.trim()
  if (!/^1\d{10}$/.test(phone)) {
    ElMessage.warning('请输入正确的手机号')
    return
  }
  if (!captchaVerifyParam) {
    ElMessage.warning('请先完成滑块验证')
    return
  }
  smsSending.value = true
  smsStatus.value = 'waiting'
  smsStatusText.value = '正在发送验证码…'
  try {
    const resp = await http.post(`${SERVER_URL}/quark/sms/send`, {
      session_id: smsSessionId,
      phone,
      captcha_data: captchaVerifyParam,
    })
    const data = resp?.data
    if (data?.code === 200) {
      captchaVerifyParam = ''
      smsStatus.value = 'waiting'
      smsStatusText.value = '验证码已发送，请查收短信'
      ElMessage.success('验证码已发送')
      startCountdown()
    } else {
      smsStatus.value = 'error'
      smsStatusText.value = data?.message || '发送失败'
      ElMessage.error(data?.message || '发送失败')
      // 重置滑块允许重试
      captchaVerifyParam = ''
      anyWin.AliyunCaptchaV2?.reset?.()
    }
  } catch (e: any) {
    smsStatus.value = 'error'
    smsStatusText.value = e?.response?.data?.message || '发送失败'
    ElMessage.error(smsStatusText.value)
  } finally {
    smsSending.value = false
  }
}

const commitSmsLogin = async () => {
  const phone = smsForm.value.phone.trim()
  const code = smsForm.value.code.trim()
  if (!/^1\d{10}$/.test(phone)) {
    ElMessage.warning('请输入正确的手机号')
    return
  }
  if (!code) {
    ElMessage.warning('请输入短信验证码')
    return
  }
  smsStatus.value = 'waiting'
  smsStatusText.value = '正在登录…'
  try {
    const resp = await http.post(`${SERVER_URL}/quark/sms/commit`, {
      session_id: smsSessionId,
      phone,
      sms_code: code,
      account_id: props.accountId ?? 0,
    })
    const data = resp?.data
    if (data?.code === 200) {
      smsStatus.value = 'success'
      smsStatusText.value = '登录成功'
      ElMessage.success(data.message || '短信验证码登录成功')
      emit('confirmed')
      emit('update:visible', false)
    } else {
      smsStatus.value = 'error'
      smsStatusText.value = data?.message || '登录失败'
      ElMessage.error(data?.message || '登录失败')
    }
  } catch (e: any) {
    smsStatus.value = 'error'
    smsStatusText.value = e?.response?.data?.message || '登录失败'
    ElMessage.error(smsStatusText.value)
  }
}

const qrLoading = ref(false)
const qrUrl = ref('')
const qrDataUrl = ref('')
const qrStatus = ref('')
const qrStatusText = ref('')
const qrSessionId = ref('')
let pollTimer: ReturnType<typeof setInterval> | null = null

// 生成二维码（用 qrcode 库渲染 URL，或后端返回可显示的 URL）
const startQr = async () => {
  stopPoll()
  qrLoading.value = true
  qrStatus.value = ''
  qrStatusText.value = ''
  try {
    const resp = await http.post(`${SERVER_URL}/quark/qrcode`)
    const data = resp?.data
    if (data?.code === 200) {
      qrSessionId.value = data.data.session_id
      qrUrl.value = data.data.qr_url
      // 用第三方 qrcode 服务渲染图片（后端返回的是登录页 URL，直接用二维码图片服务编码）
      qrDataUrl.value = await renderQr(data.data.qr_url)
      startPoll()
    } else {
      ElMessage.error(data?.message || '生成二维码失败')
    }
  } catch (e: any) {
    ElMessage.error(e?.response?.data?.message || '生成二维码失败')
  } finally {
    qrLoading.value = false
  }
}

// 用 qrcode 编码 URL 为 dataURL（简单实现：用 qrserver 免费 API，或本地 canvas）
const renderQr = async (url: string): Promise<string> => {
  // 用本地 qrcode 库（若无则回退到显示 URL 文本，让用户用夸克 App 扫）
  try {
    const QRCode = (await import('qrcode')).default
    return await QRCode.toDataURL(url, { width: 220, margin: 1 })
  } catch {
    // 回退：用后端图片接口或直接返回 URL（前端显示文本）
    return ''
  }
}

const startPoll = () => {
  pollTimer = setInterval(async () => {
    if (!qrSessionId.value) return
    try {
      const resp = await http.post(`${SERVER_URL}/quark/qrcode/poll`, { session_id: qrSessionId.value, account_id: props.accountId ?? 0 })
      const data = resp?.data
      if (data?.code === 200) {
        const status = data.data.status
        if (status === 'success') {
          stopPoll()
          qrStatus.value = 'success'
          qrStatusText.value = '登录成功'
          ElMessage.success(data.message || '扫码登录成功')
          emit('confirmed')
          emit('update:visible', false)
        } else if (status === 'expired') {
          // 二维码过期（token 约 3 分钟失效）：自动重新生成
          stopPoll()
          qrStatus.value = 'error'
          qrStatusText.value = '二维码已过期，正在刷新…'
          startQr()
        } else {
          qrStatus.value = 'waiting'
          qrStatusText.value = '等待扫码…'
        }
      } else {
        qrStatus.value = 'error'
        qrStatusText.value = data?.message || '轮询失败'
        stopPoll()
      }
    } catch {
      // 网络瞬断不中断轮询
    }
  }, 2000)
}

const stopPoll = () => {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

onUnmounted(() => {
  stopPoll()
  if (smsCountdownTimer) clearInterval(smsCountdownTimer)
})

// 进入短信 tab 时绑定 message 监听，离开时解绑
watch(mode, (m) => {
  if (m === 'sms') {
    smsStatus.value = ''
    smsStatusText.value = '输入手机号完成滑块后获取验证码'
    void initCaptcha()
  }
})

const submitCookie = async () => {
  if (!form.value.cookie) {
    ElMessage.warning('请粘贴 Cookie')
    return
  }
  submitting.value = true
  try {
    const resp = await http.post(`${SERVER_URL}/quark/login`, {
      name: form.value.name || props.accountName || '',
      cookie: form.value.cookie,
    })
    const data = resp?.data
    if (data?.code === 200) {
      ElMessage.success(data.message || '登录成功')
      emit('confirmed')
      emit('update:visible', false)
    } else {
      ElMessage.error(data?.message || '登录失败')
    }
  } catch (e: any) {
    ElMessage.error(e?.response?.data?.message || '登录失败')
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.qr-box { display: flex; flex-direction: column; align-items: center; gap: 10px; padding: 16px 0; }
.qr-img { width: 220px; height: 220px; border: 1px solid var(--el-border-color); border-radius: 8px; }
.qr-empty { display: flex; justify-content: center; padding: 40px 0; }
.qr-tip { margin: 0; color: var(--el-text-color-secondary); font-size: 13px; }
.qr-status { margin: 0; font-size: 13px; }
.qr-status.waiting { color: var(--el-color-info); }
.qr-status.scanned { color: var(--el-color-warning); }
.qr-status.success { color: var(--el-color-success); }
.qr-status.error { color: var(--el-color-danger); }
.sms-box { display: flex; flex-direction: column; align-items: center; gap: 10px; padding: 8px 0; }
.captcha-element { width: 100%; min-height: 74px; margin-bottom: 8px; }
</style>
