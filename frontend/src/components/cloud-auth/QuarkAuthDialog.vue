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

const mode = ref<'qr' | 'cookie'>('qr')
const submitting = ref(false)
const form = ref({ name: '', cookie: '' })

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

onUnmounted(stopPoll)

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
</style>
