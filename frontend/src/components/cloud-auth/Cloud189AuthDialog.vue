<template>
  <el-dialog
    :model-value="visible"
    title="天翼云盘登录"
    width="520px"
    :close-on-click-modal="false"
    @update:model-value="emit('update:visible', $event)"
  >
    <el-tabs v-model="mode">
      <el-tab-pane label="扫码登录" name="qr">
        <div v-if="!qrDataUrl" class="qr-empty">
          <el-button type="primary" :loading="qrLoading" @click="startQr">生成二维码</el-button>
        </div>
        <div v-else class="qr-box">
          <img :src="qrDataUrl" class="qr-img" alt="天翼登录二维码" />
          <p class="qr-tip">用「天翼云盘 App / 小翼管家 / 支付宝」扫码登录</p>
          <p class="qr-status" :class="qrStatus">{{ qrStatusText }}</p>
          <el-button size="small" @click="startQr">重新生成</el-button>
        </div>
      </el-tab-pane>
      <el-tab-pane label="AccessToken 登录" name="token">
        <el-form label-width="90px">
          <el-form-item label="备注名">
            <el-input v-model="tokenForm.name" placeholder="可留空" clearable />
          </el-form-item>
          <el-form-item label="accessToken">
            <el-input
              v-model="tokenForm.accessToken"
              type="textarea"
              :rows="4"
              placeholder="粘贴天翼云盘 accessToken"
            />
          </el-form-item>
          <el-alert type="success" :closable="false" show-icon>
            <p>获取方式：浏览器登录 pan.cloud.189.cn 后 → F12 → 网络面板任意一个 <code>api.cloud.189.cn</code> 请求 → 请求头里的 <code>accessToken</code> 或响应里的 <code>accessToken</code> 字段。此方式直连业务网关，不受认证网关网络限制。</p>
          </el-alert>
        </el-form>
      </el-tab-pane>
      <el-tab-pane label="账号密码登录" name="password">
        <el-alert type="warning" :closable="false" show-icon>
          <p>密码登录依赖 <code>open.e.189.cn</code> 认证网关，若服务器网络无法访问该域名会失败。建议优先用「AccessToken 登录」。</p>
        </el-alert>
        <el-form label-width="90px">
          <el-form-item label="备注名">
            <el-input v-model="form.name" placeholder="可留空，默认用账号名" clearable />
          </el-form-item>
          <el-form-item label="账号">
            <el-input v-model="form.username" placeholder="天翼云盘手机号/邮箱" clearable />
          </el-form-item>
          <el-form-item label="密码">
            <el-input v-model="form.password" type="password" show-password placeholder="请输入密码" />
          </el-form-item>
          <template v-if="captchaImage">
            <el-form-item label="验证码">
              <div class="captcha-row">
                <img :src="captchaImage" class="captcha-img" alt="验证码" />
                <el-input v-model="form.validateCode" placeholder="请输入图中验证码" clearable />
              </div>
            </el-form-item>
          </template>
        </el-form>
      </el-tab-pane>
      <el-tab-pane label="Cookie 登录" name="cookie">
        <el-form label-width="90px">
          <el-form-item label="备注名">
            <el-input v-model="cookieForm.name" placeholder="可留空" clearable />
          </el-form-item>
          <el-form-item label="SSON">
            <el-input
              v-model="cookieForm.cookie"
              type="textarea"
              :rows="4"
              placeholder="粘贴天翼云盘 Cookie 中的 SSON 值（loginSubmit.do 响应 set-cookie 里的 SSON=xxxxx 中的 xxxxx）"
            />
          </el-form-item>
          <el-alert type="info" :closable="false" show-icon>
            <p>获取方式：浏览器打开天翼云盘登录页 → F12 网络面板勾选「保留日志」→ 正常登录 → 找 loginSubmit.do → 复制 set-cookie 中的 SSON 值。</p>
          </el-alert>
        </el-form>
      </el-tab-pane>
    </el-tabs>
    <template #footer>
      <el-button @click="emit('update:visible', false)">取消</el-button>
      <el-button v-if="mode !== 'qr'" type="primary" :loading="submitting" @click="submit">{{ submitting ? '登录中…' : '登录' }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import { http } from '@/http/client'
import { SERVER_URL } from '@/const'

const props = defineProps<{ visible: boolean; accountId?: number | null; accountName?: string }>()
const emit = defineEmits<{ 'update:visible': [boolean]; confirmed: [] }>()

const mode = ref<'qr' | 'token' | 'password' | 'cookie'>('qr')
const submitting = ref(false)
const captchaImage = ref('')
const form = ref({ name: '', username: '', password: '', validateCode: '' })
const cookieForm = ref({ name: '', cookie: '' })
const tokenForm = ref({ name: '', accessToken: '' })

const qrLoading = ref(false)
const qrDataUrl = ref('')
const qrStatus = ref('')
const qrStatusText = ref('')
const qrSessionId = ref('')
let pollTimer: ReturnType<typeof setInterval> | null = null

const startQr = async () => {
  stopPoll()
  qrLoading.value = true
  qrStatus.value = ''
  qrStatusText.value = ''
  try {
    const resp = await http.post(`${SERVER_URL}/cloud189/qrcode`)
    const data = resp?.data
    if (data?.code === 200) {
      qrSessionId.value = data.data.session_id
      qrDataUrl.value = await renderQr(data.data.qr_content)
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

const renderQr = async (content: string): Promise<string> => {
  try {
    const QRCode = (await import('qrcode')).default
    return await QRCode.toDataURL(content, { width: 220, margin: 1 })
  } catch {
    return ''
  }
}

const startPoll = () => {
  pollTimer = setInterval(async () => {
    if (!qrSessionId.value) return
    try {
      const resp = await http.post(`${SERVER_URL}/cloud189/qrcode/poll`, { session_id: qrSessionId.value, account_id: props.accountId ?? 0 })
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
          stopPoll()
          qrStatus.value = 'error'
          qrStatusText.value = '二维码已过期，请重新生成'
        } else if (status === 'failed') {
          stopPoll()
          qrStatus.value = 'error'
          qrStatusText.value = '登录失败'
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

onUnmounted(stopPoll)

const submit = async () => {
  if (mode.value === 'token') {
    if (!tokenForm.value.accessToken) {
      ElMessage.warning('请粘贴 accessToken')
      return
    }
    submitting.value = true
    try {
      const resp = await http.post(`${SERVER_URL}/cloud189/login-token`, {
        name: tokenForm.value.name || props.accountName || '',
        access_token: tokenForm.value.accessToken,
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
    return
  }
  if (mode.value === 'password') {
    if (!form.value.username || !form.value.password) {
      ElMessage.warning('请输入账号和密码')
      return
    }
    submitting.value = true
    try {
      const resp = await http.post(`${SERVER_URL}/cloud189/login`, {
        name: form.value.name || props.accountName || '',
        username: form.value.username,
        password: form.value.password,
        validate_code: form.value.validateCode,
      })
      const data = resp?.data
      if (data?.code === 200) {
        if (data.data?.need_captcha) {
          captchaImage.value = data.data.captcha_image
          ElMessage.warning('需要输入验证码后重新登录')
        } else {
          ElMessage.success(data.message || '登录成功')
          emit('confirmed')
          emit('update:visible', false)
        }
      } else {
        ElMessage.error(data?.message || '登录失败')
      }
    } catch (e: any) {
      ElMessage.error(e?.response?.data?.message || '登录失败')
    } finally {
      submitting.value = false
    }
    return
  }
  if (!cookieForm.value.cookie) {
    ElMessage.warning('请粘贴 SSON Cookie')
    return
  }
  submitting.value = true
  try {
    const resp = await http.post(`${SERVER_URL}/cloud189/login-cookie`, {
      name: cookieForm.value.name || props.accountName || '',
      cookie: cookieForm.value.cookie,
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
.captcha-row { display: flex; gap: 12px; align-items: center; width: 100%; }
.captcha-img { height: 40px; border: 1px solid var(--el-border-color); border-radius: 4px; cursor: pointer; }
.qr-box { display: flex; flex-direction: column; align-items: center; gap: 10px; padding: 16px 0; }
.qr-img { width: 220px; height: 220px; border: 1px solid var(--el-border-color); border-radius: 8px; }
.qr-empty { display: flex; justify-content: center; padding: 40px 0; }
.qr-tip { margin: 0; color: var(--el-text-color-secondary); font-size: 13px; }
.qr-status { margin: 0; font-size: 13px; }
.qr-status.waiting { color: var(--el-color-info); }
.qr-status.success { color: var(--el-color-success); }
.qr-status.error { color: var(--el-color-danger); }
</style>
