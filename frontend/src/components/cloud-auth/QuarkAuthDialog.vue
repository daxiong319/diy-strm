<template>
  <el-dialog
    :model-value="visible"
    title="夸克网盘登录"
    width="520px"
    :close-on-click-modal="false"
    @update:model-value="emit('update:visible', $event)"
  >
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
    <template #footer>
      <el-button @click="emit('update:visible', false)">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="submit">{{ submitting ? '登录中…' : '登录' }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { http } from '@/http/client'
import { SERVER_URL } from '@/const'

const props = defineProps<{ visible: boolean; accountId?: number | null; accountName?: string }>()
const emit = defineEmits<{ 'update:visible': [boolean]; confirmed: [] }>()

const submitting = ref(false)
const form = ref({ name: '', cookie: '' })

const submit = async () => {
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
