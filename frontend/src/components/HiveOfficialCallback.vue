<template>
  <div class="callback-container">
    <el-card shadow="never" class="callback-card">
      <div v-if="processing" class="callback-status">
        <el-icon class="is-loading"><Loading /></el-icon>
        <p>正在完成影巢官方通道授权…</p>
      </div>
      <div v-else-if="done" class="callback-status success">
        <el-icon color="var(--el-color-success)"><CircleCheckFilled /></el-icon>
        <p>{{ message }}</p>
        <p class="hint">可关闭本窗口并返回影巢设置页</p>
      </div>
      <div v-else class="callback-status error">
        <el-icon color="var(--el-color-danger)"><CircleCloseFilled /></el-icon>
        <p>{{ message || '授权回调参数缺失' }}</p>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { Loading, CircleCheckFilled, CircleCloseFilled } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'

const http = useHttpClient()
const route = useRoute()
const processing = ref(true)
const done = ref(false)
const message = ref('')

let handled = false

const finish = async (code: string, state: string, redirectUri: string) => {
  if (handled) return
  handled = true
  processing.value = true
  try {
    const resp = await http.post(`${SERVER_URL}/cloud/hive/official/callback`, {
      code,
      state,
      redirect_uri: redirectUri,
    })
    if (resp?.data?.code === 200) {
      done.value = true
      message.value = '官方通道授权成功'
    } else {
      message.value = resp?.data?.message || '授权失败'
    }
  } catch (e: any) {
    message.value = '授权失败：' + (e?.message || '')
  } finally {
    processing.value = false
  }
}

const onMessage = (ev: MessageEvent) => {
  // hdhive 授权页 postMessage 回传 {code, state}（回调模式：页面消息）
  const d = ev?.data
  if (d && typeof d === 'object' && d.code && d.state) {
    finish(String(d.code), String(d.state), window.location.origin + '/hive-official/callback')
  }
}

onMounted(() => {
  window.addEventListener('message', onMessage)
  // 降级 redirect：URL 带 code/state 时直接处理
  const code = route.query.code as string
  const state = route.query.state as string
  if (code && state) {
    finish(code, state, window.location.origin + '/hive-official/callback')
  } else {
    // 等待 postMessage（弹窗模式）；5 秒无消息提示
    setTimeout(() => {
      if (!handled) {
        processing.value = false
        message.value = '未收到授权结果，请重试或改用电脑浏览器完成授权'
      }
    }, 5000)
  }
})

onUnmounted(() => window.removeEventListener('message', onMessage))
</script>

<style scoped>
.callback-container {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 60vh;
}
.callback-card {
  width: 420px;
  text-align: center;
}
.callback-status p {
  margin: 12px 0 4px;
  font-size: 15px;
}
.callback-status .hint {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
</style>
