<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
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

const form = reactive({
  access_token: '',
  refresh_token: '',
})
const submitting = ref(false)

const resetForm = () => {
  form.access_token = ''
  form.refresh_token = ''
}

watch(
  () => visible.value,
  (isVisible) => {
    if (isVisible) resetForm()
  },
)

const handleSubmit = async () => {
  if (!props.accountId) return
  if (!form.access_token.trim()) {
    ElMessage.warning('请输入光鸭云盘访问令牌')
    return
  }
  submitting.value = true
  try {
    const response = await http.post(`${SERVER_URL}/guangyapan/login`, {
      account_id: props.accountId,
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
    console.error('光鸭云盘授权错误：', error)
    ElMessage.error('光鸭云盘授权失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog
    v-model="visible"
    title="光鸭云盘授权"
    width="440px"
    destroy-on-close
    align-center
  >
    <div class="guangya-auth-dialog">
      <div class="guangya-auth-dialog__name">{{ accountName }}</div>
      <el-form label-position="top">
        <el-form-item label="访问令牌（Access Token）">
          <el-input
            v-model="form.access_token"
            type="password"
            placeholder="请输入光鸭云盘访问令牌"
            show-password
          />
        </el-form-item>
        <el-form-item label="刷新令牌（Refresh Token，可选）">
          <el-input
            v-model="form.refresh_token"
            type="password"
            placeholder="留空则无法自动续期"
            show-password
            @keyup.enter="handleSubmit"
          />
        </el-form-item>
      </el-form>
      <p class="guangya-auth-dialog__tip">
        令牌可在光鸭云盘网页端登录后从浏览器开发者工具中获取。
      </p>
    </div>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="handleSubmit">验证</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.guangya-auth-dialog {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.guangya-auth-dialog__name {
  max-width: 100%;
  overflow-wrap: anywhere;
  color: #606266;
  margin-bottom: 8px;
}

.guangya-auth-dialog__tip {
  margin: 4px 0 0;
  font-size: 12px;
  color: #909399;
  line-height: 1.5;
}
</style>
