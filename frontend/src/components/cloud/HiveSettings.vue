<template>
  <div class="main-content-container cloud-page">
    <el-alert
      type="info"
      :closable="false"
      class="cloud-alert"
      title="影巢每日自动签到与订阅引擎轮询配置。OAuth 授权与手动签到请在「授权签到」页进行。"
      show-icon
    />

    <!-- 每日自动签到 · 主账号 -->
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>每日自动签到 · 主账号</span>
        </div>
      </template>
      <el-form label-width="120px" class="hive-form">
        <el-form-item label="自动签到">
          <el-switch v-model="form.daily_checkin_enabled" />
          <div class="form-help">开启后每天在指定时间自动为影巢主账号签到，获取积分。</div>
        </el-form-item>
        <el-form-item label="签到时间">
          <el-select v-model="form.daily_checkin_hour" :disabled="!form.daily_checkin_enabled" style="width: 140px">
            <el-option v-for="h in hours" :key="h" :label="`${String(h).padStart(2,'0')}:00`" :value="h" />
          </el-select>
          <div class="form-help">服务器时区，每天该时刻触发签到。</div>
        </el-form-item>
        <el-form-item label="签到模式">
          <el-radio-group v-model="form.daily_checkin_mode" :disabled="!form.daily_checkin_enabled">
            <el-radio-button value="daily">普通签到</el-radio-button>
            <el-radio-button value="gamble">赌狗签到</el-radio-button>
          </el-radio-group>
          <div class="form-help">普通签到积分固定；赌狗签到有概率获得更高积分。</div>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 每日自动签到 · 子账号 -->
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>每日自动签到 · 子账号</span>
        </div>
      </template>
      <el-form label-width="120px" class="hive-form">
        <el-form-item label="自动签到">
          <el-switch v-model="form.sub_checkin_enabled" />
          <div class="form-help">开启后每天在指定时间自动为已启用的子账号签到。</div>
        </el-form-item>
        <el-form-item label="签到时间">
          <el-select v-model="form.sub_checkin_hour" :disabled="!form.sub_checkin_enabled" style="width: 140px">
            <el-option v-for="h in hours" :key="h" :label="`${String(h).padStart(2,'0')}:00`" :value="h" />
          </el-select>
          <div class="form-help">服务器时区，每天该时刻触发签到。</div>
        </el-form-item>
        <el-form-item label="签到模式">
          <el-radio-group v-model="form.sub_checkin_mode" :disabled="!form.sub_checkin_enabled">
            <el-radio-button value="daily">普通签到</el-radio-button>
            <el-radio-button value="gamble">赌狗签到</el-radio-button>
          </el-radio-group>
          <div class="form-help">普通签到积分固定；赌狗签到有概率获得更高积分。</div>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 订阅引擎 -->
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>订阅引擎</span>
        </div>
      </template>
      <el-form label-width="120px" class="hive-form">
        <el-form-item label="轮询间隔">
          <el-input-number v-model="form.poll_interval" :min="5" :max="1440" />
          <span class="interval-unit">分钟</span>
          <div class="form-help">引擎按此间隔查询所有影巢订阅的新资源，默认 15 分钟。</div>
        </el-form-item>
        <el-form-item label="解锁积分上限">
          <el-input-number v-model="form.max_points" :min="0" :max="999999" />
          <div class="form-help">超过此积分的资源将被跳过，不自动解锁。0 表示不限。</div>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="saving" @click="save">保存设置</el-button>
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

const hours = Array.from({ length: 24 }, (_, i) => i)

const form = reactive({
  poll_interval: 15,
  max_points: 0,
  daily_checkin_enabled: true,
  daily_checkin_mode: 'daily' as string,
  daily_checkin_hour: 8,
  sub_checkin_enabled: true,
  sub_checkin_mode: 'daily' as string,
  sub_checkin_hour: 8,
})

const saving = ref(false)

const load = async () => {
  try {
    const resp = await http.get('/api/cloud/hive/settings')
    if (resp.data?.code === 200) {
      const d = resp.data.data || {}
      form.poll_interval = d.poll_interval > 0 ? d.poll_interval : 15
      form.max_points = d.max_points ?? 0
      form.daily_checkin_enabled = d.daily_checkin_enabled !== false
      form.daily_checkin_mode = d.daily_checkin_mode === 'gamble' ? 'gamble' : 'daily'
      form.daily_checkin_hour = (d.daily_checkin_hour >= 0 && d.daily_checkin_hour <= 23) ? d.daily_checkin_hour : 8
      form.sub_checkin_enabled = d.sub_checkin_enabled !== false
      form.sub_checkin_mode = d.sub_checkin_mode === 'gamble' ? 'gamble' : 'daily'
      form.sub_checkin_hour = (d.sub_checkin_hour >= 0 && d.sub_checkin_hour <= 23) ? d.sub_checkin_hour : 8
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
      poll_interval: form.poll_interval,
      max_points: form.max_points,
      daily_checkin_enabled: form.daily_checkin_enabled,
      daily_checkin_mode: form.daily_checkin_mode,
      daily_checkin_hour: form.daily_checkin_hour,
      sub_checkin_enabled: form.sub_checkin_enabled,
      sub_checkin_mode: form.sub_checkin_mode,
      sub_checkin_hour: form.sub_checkin_hour,
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

onMounted(load)
</script>

<style scoped>
.cloud-page {
  padding: 12px;
}
.cloud-card {
  border-radius: 8px;
  margin-bottom: 16px;
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
