<template>
  <el-dialog
    :model-value="visible"
    :title="`选择 ${sourceName} 目录`"
    :width="checkIsMobile ? '92%' : '620px'"
    :close-on-click-modal="false"
    @close="handleClose"
    @open="handleOpen"
  >
    <div v-if="accountsLoading" v-loading="true" class="picker-loading" />
    <div v-else-if="!accounts.length" class="picker-empty">
      <el-empty description="未配置账号，请先到「网盘账号」页面登录">
        <el-button type="primary" size="small" @click="$router.push('/accounts')">
          前往网盘账号
        </el-button>
      </el-empty>
    </div>
    <div v-else>
      <div v-if="accounts.length > 1" class="account-row">
        <span class="account-label">账号：</span>
        <el-select v-model="selectedAccountId" size="small" class="account-select">
          <el-option v-for="a in accounts" :key="a.id" :label="a.name || a.username" :value="a.id" />
        </el-select>
      </div>
      <DirectorySelector
        :key="`${selectedAccountId}-${refreshKey}`"
        v-model="selectedDir"
        :source-type="sourceType"
        :account-id="selectedAccountId"
        @cancel="handleClose"
        @select="handleSelect"
      />
    </div>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import { isMobile } from '@/utils/deviceUtils'
import DirectorySelector from '../DirectorySelector.vue'
import type { DirInfo } from '@/typing'

const props = defineProps<{
  visible: boolean
  sourceType: string
  sourceName: string
}>()

const emit = defineEmits<{
  'update:visible': [value: boolean]
  select: [path: string]
}>()

const checkIsMobile = ref(isMobile())
const http = useHttpClient()

const accounts = ref<any[]>([])
const accountsLoading = ref(false)
const selectedAccountId = ref(0)
const selectedDir = ref<DirInfo | null>(null)
const refreshKey = ref(0)

const loadAccounts = async () => {
  accountsLoading.value = true
  try {
    const resp = await http.get(`${SERVER_URL}/account/list`)
    if (resp.data?.code === 200) {
      accounts.value = (resp.data.data || []).filter(
        (a: any) => a.source_type === props.sourceType,
      )
      if (accounts.value.length) {
        selectedAccountId.value = accounts.value[0].id
      }
    } else {
      accounts.value = []
    }
  } catch {
    accounts.value = []
  } finally {
    accountsLoading.value = false
  }
}

const handleOpen = () => {
  refreshKey.value++
  loadAccounts()
}

const handleClose = () => {
  emit('update:visible', false)
}

const handleSelect = () => {
  if (!selectedDir.value) return
  const path = selectedDir.value.path || selectedDir.value.name
  emit('select', path)
  emit('update:visible', false)
  ElMessage.success(`已选择：${path}`)
}

watch(
  () => props.visible,
  (v) => {
    if (v) refreshKey.value++
  },
)
</script>

<style scoped>
.picker-loading {
  min-height: 200px;
}
.picker-empty {
  padding: 10px 0 20px;
}
.account-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.account-label {
  font-size: 13px;
  color: #606266;
  white-space: nowrap;
}
.account-select {
  width: 220px;
}
</style>
