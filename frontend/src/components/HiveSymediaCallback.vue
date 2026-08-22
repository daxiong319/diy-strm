<template>
  <div class="callback-container">
    <el-card shadow="never" class="callback-card">
      <div v-if="processing" class="callback-status">
        <el-icon class="is-loading"><Loading /></el-icon>
        <p>正在完成影巢授权…</p>
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
import { useHttpClient } from '@/http/client'

const http = useHttpClient()
const route = useRoute()
const processing = ref(true)
const done = ref(false)
const message = ref('')

let handled = false

const finish = async (userid: string, proxyUserKey: string, refreshExpiresAt?: number) => {
  if (handled) return
  handled = true
  processing.value = true
  try {
    const resp = await http.post('/api/cloud/hive/symedia/callback', {
      userid,
      proxy_user_key: proxyUserKey,
      refresh_expires_at: refreshExpiresAt || 0,
    })
    if (resp?.data?.code === 200) {
      done.value = true
      message.value = resp.data.message || '主渠道授权成功'
    } else {
      message.value = resp?.data?.message || '授权失败'
    }
  } catch (e: any) {
    message.value = '授权失败：' + (e?.message || '')
  } finally {
    processing.value = false
  }
}

onMounted(() => {
  // symedia 服务端授权完成后回跳 callback 并带 userid/proxy_user_key 参数
  const userid = (route.query.userid as string) || (route.query.user_id as string) || ''
  const proxyUserKey = (route.query.proxy_user_key as string) || ''
  if (userid && proxyUserKey) {
    finish(userid, proxyUserKey, Number(route.query.refresh_expires_at || 0))
  } else {
    // 等待 postMessage（弹窗模式）；8 秒无消息提示
    const timer = setTimeout(() => {
      if (!handled) {
        processing.value = false
        message.value = '未收到授权结果，请重试或改用电脑浏览器完成授权'
      }
    }, 8000)
    onUnmounted(() => clearTimeout(timer))
  }
})
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
.is-loading {
  font-size: 32px;
}
</style>
