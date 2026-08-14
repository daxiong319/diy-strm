<template>
  <div class="main-content-container cloud-page">
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>{{ sourceName }} · 转存目录设置</span>
        </div>
      </template>

      <el-form :label-position="checkIsMobile ? 'top' : 'left'" label-width="140" class="cloud-form">
        <el-form-item label="转存目标目录">
          <div class="dir-input-row">
            <el-input
              v-model="saveDir"
              placeholder="例如：/影视/待整理"
              :disabled="loading"
              class="limited-width-input"
              @keyup.enter="save"
            />
            <el-button :disabled="loading" @click="pickerVisible = true">选择目录</el-button>
          </div>
          <div class="form-help">
            <p>所有转存到{{ sourceName }}的分享（Telegram 消息 / 频道订阅）默认保存到该目录。</p>
            <p>可通过「选择目录」在{{ sourceName }}中浏览选择，也可以手动输入（目录需已存在）。</p>
          </div>
        </el-form-item>

        <el-form-item>
          <el-button type="primary" :loading="saving" @click="save">保存设置</el-button>
          <el-button v-if="sourceType === '123'" :loading="testing" @click="testConnection">
            测试账号连接
          </el-button>
        </el-form-item>
      </el-form>

      <CloudDirPicker
        :visible="pickerVisible"
        :source-type="sourceType"
        :source-name="sourceName"
        @update:visible="pickerVisible = $event"
        @select="onDirSelected"
      />
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { useHttpClient } from '@/http/client'
import { ElMessage } from 'element-plus'
import { onMounted, ref } from 'vue'
import { isMobile } from '@/utils/deviceUtils'
import CloudDirPicker from './CloudDirPicker.vue'

const props = defineProps<{
  sourceType: string
  sourceName: string
}>()

const checkIsMobile = ref(isMobile())
const http = useHttpClient()

const saveDir = ref('')
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const pickerVisible = ref(false)

const onDirSelected = (path: string) => {
  saveDir.value = path
}

const load = async () => {
  loading.value = true
  try {
    const resp = await http.get('/api/cloud/settings', { params: { source_type: props.sourceType } })
    if (resp.data?.code === 200) {
      saveDir.value = resp.data.data || ''
    } else {
      ElMessage.error(resp.data?.message || '加载失败')
    }
  } catch (e: any) {
    ElMessage.error('加载失败：' + (e?.message || ''))
  } finally {
    loading.value = false
  }
}

const save = async () => {
  saving.value = true
  try {
    const resp = await http.post('/api/cloud/settings', {
      source_type: props.sourceType,
      key: 'save_dir',
      value: saveDir.value.trim(),
    })
    if (resp.data?.code === 200) {
      ElMessage.success('保存成功')
    } else {
      ElMessage.error(resp.data?.message || '保存失败')
    }
  } catch (e: any) {
    ElMessage.error('保存失败：' + (e?.message || ''))
  } finally {
    saving.value = false
  }
}

const testConnection = async () => {
  testing.value = true
  try {
    const resp = await http.post('/api/cloud/settings/test-123')
    if (resp.data?.code === 200) {
      ElMessage.success(resp.data.message || '连接成功')
    } else {
      ElMessage.error(resp.data?.message || '测试失败')
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
  padding: 16px;
}
.cloud-card {
  max-width: 760px;
}
.limited-width-input {
  max-width: 420px;
}
.dir-input-row {
  display: flex;
  gap: 8px;
  width: 100%;
  max-width: 560px;
}
.dir-input-row .limited-width-input {
  max-width: none;
  flex: 1;
}
.form-help {
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
  margin-top: 4px;
}
</style>
