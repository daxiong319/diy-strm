<template>
  <el-dialog
    :model-value="modelValue"
    title="中国移动云盘 · 保存分享"
    width="760px"
    :close-on-click-modal="false"
    @update:model-value="(v: boolean) => emit('update:modelValue', v)"
  >
    <el-form label-width="80px" @submit.prevent>
      <el-form-item label="分享链接">
        <el-input
          v-model="linkInput"
          placeholder="粘贴完整链接（自动解析 linkId 和提取码），或直接输入 linkId"
          clearable
        />
      </el-form-item>
      <el-form-item label="提取码">
        <div style="display: flex; gap: 10px; width: 100%">
          <el-input v-model="passwd" placeholder="分享链接的提取码（没有则留空）" clearable />
          <el-button type="primary" :loading="queryLoading" @click="queryShare">查询</el-button>
        </div>
      </el-form-item>
    </el-form>

    <div v-if="loaded" class="share-body">
      <div class="share-info">
        <span v-if="linkName" class="share-link-name">{{ linkName }}</span>
        <span v-if="expireTime" class="share-expire">有效期至 {{ expireTime }}</span>
        <span v-if="totalCount > 0" class="share-total">共 {{ totalCount }} 项</span>
      </div>
      <div class="share-nav">
        <el-breadcrumb separator="/">
          <el-breadcrumb-item @click="navigateShare('root', '')" style="cursor: pointer">
            分享根目录
          </el-breadcrumb-item>
          <el-breadcrumb-item
            v-for="(crumb, index) in shareCrumbs"
            :key="index"
            @click="navigateShare(crumb.caID, crumb.name)"
            style="cursor: pointer"
          >
            {{ crumb.name }}
          </el-breadcrumb-item>
        </el-breadcrumb>
      </div>
      <el-table
        :data="shareItems"
        height="300"
        style="width: 100%"
        size="small"
        @row-click="handleShareRowClick"
      >
        <el-table-column width="50">
          <template #default="{ row }">
            <el-checkbox
              :model-value="isShareSelected(row)"
              @click.stop
              @change="(v: boolean | string | number) => toggleShareSelect(row, !!v)"
            />
          </template>
        </el-table-column>
        <el-table-column label="名称" min-width="280">
          <template #default="{ row }">
            <div style="display: flex; align-items: center; gap: 6px">
              <el-icon :size="16">
                <Folder v-if="row.is_dir" />
                <Document v-else />
              </el-icon>
              <span>{{ row.name }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="大小" width="120" align="right">
          <template #default="{ row }">
            <span v-if="!row.is_dir">{{ formatFileSize(row.size) }}</span>
          </template>
        </el-table-column>
      </el-table>
      <div class="share-tip">提示：双击目录可进入查看；勾选要转存的文件或目录</div>
    </div>

    <el-form v-if="loaded" label-width="80px" style="margin-top: 12px">
      <el-form-item label="保存到">
        <el-input v-model="targetCatalogID" placeholder="目标目录 ID（默认当前目录，留空为根目录）" clearable />
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button @click="emit('update:modelValue', false)">关闭</el-button>
      <el-button
        type="primary"
        :loading="saveLoading"
        :disabled="!loaded || selectedPaths.length === 0"
        @click="saveShare"
      >
        转存到当前目标
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, computed, watch, inject } from 'vue'
import { ElMessage } from 'element-plus'
import { Document, Folder } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import type { AxiosStatic } from 'axios'

const props = defineProps<{
  modelValue: boolean
  accountId: number | null
  defaultTargetCatalogID: string
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'saved'): void
}>()

interface ShareRow {
  id: string
  name: string
  is_dir: boolean
  size: number
  path: string
  parentCaID: string
}

const http: AxiosStatic | undefined = inject('$http')

const linkInput = ref('')
const passwd = ref('')
const queryLoading = ref(false)
const saveLoading = ref(false)
const loaded = ref(false)
const linkName = ref('')
const expireTime = ref('')
const totalCount = ref(0)
const shareItems = ref<ShareRow[]>([])
const currentCaID = ref('root')
const shareCrumbs = ref<{ caID: string; name: string }[]>([])
const selected = ref<Record<string, ShareRow>>({})
const targetCatalogID = ref('')

watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      targetCatalogID.value = props.defaultTargetCatalogID || ''
    }
  }
)

const selectedPaths = computed(() => Object.values(selected.value).map((row) => row.path))

function parseLink(text: string) {
  const trimmed = text.trim()
  const linkIdMatch = trimmed.match(/(?:linkId|linkID|shareId)=([A-Za-z0-9_\-]+)/i)
  const pwdMatch = trimmed.match(/[?&]pwd=([^&\s#]+)/i)
  if (linkIdMatch) {
    return { linkId: linkIdMatch[1], pwd: pwdMatch ? decodeURIComponent(pwdMatch[1]) : '' }
  }
  if (/^[A-Za-z0-9_\-]{8,}$/.test(trimmed)) {
    return { linkId: trimmed, pwd: '' }
  }
  return null
}

async function queryShare() {
  if (!http) {
    ElMessage.error('HTTP 客户端未注入')
    return
  }
  if (!props.accountId) {
    ElMessage.error('未选择网盘账号')
    return
  }
  const parsed = parseLink(linkInput.value)
  if (!parsed || !parsed.linkId) {
    ElMessage.error('无法解析分享链接，请输入有效的分享链接或 linkId')
    return
  }
  if (!passwd.value && parsed.pwd) {
    passwd.value = parsed.pwd
  }
  queryLoading.value = true
  try {
    const response = await http.post(`${SERVER_URL}/pan139/share/info`, {
      account_id: props.accountId,
      link_id: parsed.linkId,
      passwd: passwd.value || parsed.pwd || '',
      ca_id: 'root',
    })
    const body = response?.data
    if (body?.code !== 200) {
      ElMessage.error(body?.message || '查询分享链接失败')
      return
    }
    const data = body.data || {}
    loaded.value = true
    linkName.value = data.link_name || ''
    expireTime.value = data.expire_time || ''
    totalCount.value = data.nod_num || 0
    currentCaID.value = 'root'
    shareCrumbs.value = []
    selected.value = {}
    buildItems(data)
  } catch (error) {
    console.error('查询分享链接失败:', error)
    ElMessage.error('查询分享链接失败：网络错误')
  } finally {
    queryLoading.value = false
  }
}

function buildItems(data: any) {
  const items: ShareRow[] = []
  for (const folder of data.folder_list || []) {
    items.push({
      id: folder.catalog_id,
      name: folder.ca_name,
      is_dir: true,
      size: 0,
      path: folder.path || `${currentCaID.value}/${folder.catalog_id}`,
      parentCaID: currentCaID.value,
    })
  }
  for (const file of data.file_list || []) {
    items.push({
      id: file.content_id,
      name: file.co_name,
      is_dir: false,
      size: Number(file.co_size || 0),
      path: file.path || `${currentCaID.value}/${file.content_id}`,
      parentCaID: currentCaID.value,
    })
  }
  shareItems.value = items
}

async function navigateShare(caID: string, name: string) {
  if (!http || !props.accountId || !linkInput.value) {
    return
  }
  const parsed = parseLink(linkInput.value)
  if (!parsed) {
    return
  }
  queryLoading.value = true
  try {
    const response = await http.post(`${SERVER_URL}/pan139/share/info`, {
      account_id: props.accountId,
      link_id: parsed.linkId,
      passwd: passwd.value,
      ca_id: caID || 'root',
    })
    const body = response?.data
    if (body?.code !== 200) {
      ElMessage.error(body?.message || '进入目录失败')
      return
    }
    const data = body.data || {}
    if (caID === 'root' || name === '') {
      shareCrumbs.value = []
    } else {
      const idx = shareCrumbs.value.findIndex((c) => c.caID === caID)
      if (idx >= 0) {
        shareCrumbs.value = shareCrumbs.value.slice(0, idx + 1)
      } else {
        shareCrumbs.value.push({ caID, name })
      }
    }
    currentCaID.value = caID || 'root'
    totalCount.value = data.nod_num || 0
    buildItems(data)
  } catch (error) {
    console.error('进入分享目录失败:', error)
    ElMessage.error('进入目录失败：网络错误')
  } finally {
    queryLoading.value = false
  }
}

function handleShareRowClick(row: ShareRow) {
  if (row.is_dir) {
    navigateShare(row.id, row.name)
  }
}

function isShareSelected(row: ShareRow) {
  return !!selected.value[row.id]
}

function toggleShareSelect(row: ShareRow, checked: boolean) {
  if (checked) {
    selected.value[row.id] = row
  } else {
    delete selected.value[row.id]
  }
  selected.value = { ...selected.value }
}

async function saveShare() {
  if (!http || !props.accountId) {
    return
  }
  const parsed = parseLink(linkInput.value)
  if (!parsed) {
    ElMessage.error('无法解析分享链接')
    return
  }
  const filePaths = Object.values(selected.value)
    .filter((row) => !row.is_dir)
    .map((row) => row.path)
  const dirPaths = Object.values(selected.value)
    .filter((row) => row.is_dir)
    .map((row) => row.path)
  saveLoading.value = true
  try {
    const response = await http.post(
      `${SERVER_URL}/pan139/share/save`,
      {
        account_id: props.accountId,
        link_id: parsed.linkId,
        passwd: passwd.value,
        target_catalog_id: targetCatalogID.value || 'root',
        file_paths: filePaths,
        dir_paths: dirPaths,
        wait_visible: true,
      },
      { timeout: 60000 }
    )
    const body = response?.data
    if (body?.code !== 200) {
      ElMessage.error(body?.message || '转存失败')
      return
    }
    ElMessage.success(body?.message || '转存完成')
    loaded.value = false
    selected.value = {}
    emit('saved')
  } catch (error) {
    console.error('转存失败:', error)
    ElMessage.error('转存失败：网络错误')
  } finally {
    saveLoading.value = false
  }
}
</script>

<style scoped>
.share-body {
  margin-bottom: 4px;
}
.share-info {
  display: flex;
  gap: 16px;
  align-items: baseline;
  margin-bottom: 8px;
  font-size: 13px;
}
.share-link-name {
  font-weight: 600;
}
.share-expire {
  color: var(--el-text-color-secondary);
}
.share-total {
  color: var(--el-text-color-secondary);
}
.share-nav {
  margin-bottom: 8px;
}
.share-tip {
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
