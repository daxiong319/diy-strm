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
  username: '',
  password: '',
})
const submitting = ref(false)

const resetForm = () => {
  form.username = ''
  form.password = ''
}

watch(
  () => visible.value,
  (isVisible) => {
    if (isVisible) resetForm()
  },
)

const handleSubmit = async () => {
  if (!props.accountId) return
  if (!form.username.trim() || !form.password) {
    ElMessage.warning('请输入 123 云盘用户名（邮箱或手机号）和密码')
    return
  }
  submitting.value = true
  try {
    const response = await http.post(`${SERVER_URL}/pan123/login`, {
      account_id: props.accountId,
      username: form.username.trim(),
      password: form.password,
    })
    if (response?.data.code === 200) {
      ElMessage.success('123 云盘授权成功')
      visible.value = false
      emit('confirmed')
    } else {
      ElMessage.error(response?.data.message || '123 云盘授权失败')
    }
  } catch (error) {
    console.error('123 云盘授权错误：', error)
    ElMessage.error('123 云盘授权失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog
    v-model="visible"
    title="123 云盘授权"
    width="420px"
    destroy-on-close
    align-center
  >
    <div class="pan123-auth-dialog">
      <div class="pan123-auth-dialog__name">{{ accountName }}</div>
      <el-form label-position="top">
        <el-form-item label="用户名（邮箱或手机号）">
          <el-input
            v-model="form.username"
            placeholder="请输入 123 云盘用户名"
            clearable
          />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="form.password"
            type="password"
            placeholder="请输入 123 云盘密码"
            show-password
            @keyup.enter="handleSubmit"
          />
        </el-form-item>
      </el-form>
    </div>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="handleSubmit">登录</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.pan123-auth-dialog {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.pan123-auth-dialog__name {
  max-width: 100%;
  overflow-wrap: anywhere;
  color: #606266;
  margin-bottom: 8px;
}
</style>
