<template>
  <div class="main-content-container cloud-page">
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>影巢 · 设置</span>
        </div>
      </template>

      <el-alert
        type="info"
        :closable="false"
        class="cloud-alert"
        title="影巢（HDHive）资源订阅需要 API Key。请到影巢官网（hdhive.com）开放平台申请应用 Key，填入后点击「测试连接」验证有效性。"
        show-icon
      />

      <el-form label-width="130px" class="hive-form">
        <el-form-item label="API Key">
          <el-input v-model="form.api_key" type="password" show-password placeholder="hdhive.com 开放平台申请的应用 Key" />
          <div class="form-help">保存后用于资源查询与解锁；修改将在下一次轮询时生效。</div>
        </el-form-item>

        <el-form-item label="收费资源">
          <el-switch v-model="form.allow_points" />
          <div class="form-help">
            {{ form.allow_points ? '开启：允许自动扣积分解锁收费资源（注意积分余额）' : '关闭：仅解锁免费资源，收费资源跳过' }}
          </div>
        </el-form-item>

        <el-form-item label="轮询间隔">
          <el-input-number v-model="form.poll_interval" :min="5" :max="1440" />
          <span class="interval-unit">分钟</span>
          <div class="form-help">引擎按此间隔查询所有影巢订阅的新资源，默认 15 分钟。</div>
        </el-form-item>

        <el-form-item>
          <el-button type="primary" :loading="saving" @click="save">保存设置</el-button>
          <el-button :loading="testing" @click="test">测试连接</el-button>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useHttpClient } from '@/http/client'

const http = useHttpClient()

const form = reactive({ api_key: '', allow_points: true, poll_interval: 15 })
const saving = ref(false)
const testing = ref(false)

const load = async () => {
  try {
    const resp = await http.get('/api/cloud/hive/settings')
    if (resp.data?.code === 200) {
      const d = resp.data.data || {}
      form.api_key = d.api_key || ''
      form.allow_points = d.allow_points !== false
      form.poll_interval = d.poll_interval > 0 ? d.poll_interval : 15
    } else {
      ElMessage.error(resp.data?.message || '加载失败')
    }
  } catch (e: any) {
    ElMessage.error('加载失败：' + (e?.message || ''))
  }
}

const save = async () => {
  saving.value = true
  try {
    const resp = await http.post('/api/cloud/hive/settings', {
      api_key: form.api_key.trim(),
      allow_points: form.allow_points,
      poll_interval: form.poll_interval,
    })
    if (resp.data?.code === 200) {
      ElMessage.success('影巢设置已保存')
    } else {
      ElMessage.error(resp.data?.message || '保存失败')
    }
  } catch (e: any) {
    ElMessage.error('保存失败：' + (e?.message || ''))
  } finally {
    saving.value = false
  }
}

const test = async () => {
  testing.value = true
  try {
    const resp = await http.post('/api/cloud/hive/test')
    ElMessage.success(resp.data?.message || '已测试')
    if (resp.data?.code !== 200 && resp.data?.code !== 0) {
      ElMessage.warning(resp.data?.message || '连接失败')
    }
  } catch (e: any) {
    ElMessage.error('测试失败：' + (e?.message || ''))
  } finally {
    testing.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.cloud-page {
  padding: 12px;
}
.cloud-card {
  border-radius: 8px;
}
.card-header {
  font-weight: 600;
}
.cloud-alert {
  margin-bottom: 16px;
}
.hive-form {
  max-width: 560px;
}
.form-help {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.5;
  margin-top: 4px;
  width: 100%;
}
.interval-unit {
  margin-left: 8px;
  color: var(--el-text-color-secondary);
}
</style>