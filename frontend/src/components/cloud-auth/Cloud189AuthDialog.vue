<template>
  <el-dialog
    :model-value="visible"
    title="天翼云盘登录"
    width="520px"
    :close-on-click-modal="false"
    @update:model-value="emit('update:visible', $event)"
  >
    <el-tabs v-model="mode">
      <el-tab-pane label="账号密码登录" name="password">
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

const mode = ref<'password' | 'cookie'>('password')
const submitting = ref(false)
const captchaImage = ref('')
const form = ref({ name: '', username: '', password: '', validateCode: '' })
const cookieForm = ref({ name: '', cookie: '' })

const submit = async () => {
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
</style>
