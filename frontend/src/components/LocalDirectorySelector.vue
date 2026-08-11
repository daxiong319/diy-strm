<template>
  <div class="local-directory-selector">
    <div class="selector-toolbar">
      <el-breadcrumb class="directory-breadcrumb" separator="/" aria-label="当前目录位置">
        <el-breadcrumb-item>
          <button type="button" class="breadcrumb-button" @click="navigateTo('')">根目录</button>
        </el-breadcrumb-item>
        <el-breadcrumb-item v-for="(part, index) in pathParts" :key="index">
          <button type="button" class="breadcrumb-button" @click="navigateTo(part.join('/'))">
            {{ part[part.length - 1] }}
          </button>
        </el-breadcrumb-item>
      </el-breadcrumb>
      <el-button plain :icon="Refresh" :loading="loading" aria-label="刷新目录" @click="loadDirectories">
        刷新
      </el-button>
    </div>

    <div v-loading="loading" class="tree-loading-container">
      <div v-if="dirs.length === 0" class="empty-state">
        <el-empty description="暂无子目录" />
      </div>
      <div v-else class="dir-list">
        <div
          v-for="dir in dirs"
          :key="dir.path"
          class="dir-item"
          :class="{ selected: selectedPath === dir.path }"
          @click="toggleSelect(dir.path)"
          @dblclick="navigateTo(dir.path)"
        >
          <el-icon><Folder /></el-icon>
          <span class="dir-name">{{ dir.name }}</span>
        </div>
      </div>
    </div>

    <div class="footer-buttons">
      <el-button @click="handleCancel">取消</el-button>
      <el-button type="primary" :disabled="!selectedPath || loading" @click="handleSelect">
        选择
      </el-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Folder, Refresh } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'

interface Props {
  modelValue?: string
}

interface LocalDir {
  name: string
  path: string
}

const props = withDefaults(defineProps<Props>(), {
  modelValue: '',
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  cancel: []
  select: []
}>()

const http = useHttpClient()
const loading = ref(false)
const currentPath = ref('')
const dirs = ref<LocalDir[]>([])
const selectedPath = ref(props.modelValue || '')

const pathParts = computed(() => {
  const parts: string[][] = []
  let acc = ''
  for (const seg of currentPath.value.split('/').filter(Boolean)) {
    acc = acc ? `${acc}/${seg}` : seg
    parts.push([acc])
  }
  return parts
})

watch(
  () => props.modelValue,
  (v) => {
    selectedPath.value = v || ''
  },
)

const normalize = (p: string) => {
  if (!p) return ''
  return p.replace(/\\/g, '/').replace(/\/+$/, '')
}

const loadDirectories = async () => {
  try {
    loading.value = true
    const response = await http.get(`${SERVER_URL}/path/local`, {
      params: { path: currentPath.value },
    })
    if (response?.data.code === 200 && response.data.data) {
      dirs.value = (response.data.data.dirs || []) as LocalDir[]
      if (currentPath.value !== (response.data.data.current_path || '')) {
        currentPath.value = response.data.data.current_path || ''
      }
    } else {
      ElMessage.warning(response?.data?.message || '获取本地目录失败')
    }
  } catch (error) {
    console.error('获取本地目录错误：', error)
    ElMessage.error('获取本地目录失败')
  } finally {
    loading.value = false
  }
}

const navigateTo = (path: string) => {
  currentPath.value = normalize(path)
  void loadDirectories()
}

const toggleSelect = (path: string) => {
  selectedPath.value = selectedPath.value === path ? '' : path
}

const handleSelect = () => {
  emit('update:modelValue', selectedPath.value)
  emit('select')
}

const handleCancel = () => {
  emit('cancel')
}

onMounted(() => {
  void loadDirectories()
})
</script>

<style scoped>
.local-directory-selector {
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-height: 320px;
}
.selector-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.directory-breadcrumb {
  flex: 1;
  overflow: hidden;
  white-space: nowrap;
}
.breadcrumb-button {
  border: none;
  background: none;
  padding: 0;
  color: var(--el-color-primary);
  cursor: pointer;
  font-size: inherit;
}
.breadcrumb-button:hover {
  text-decoration: underline;
}
.tree-loading-container {
  flex: 1;
  min-height: 220px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  overflow-y: auto;
  padding: 4px 0;
}
.empty-state {
  padding: 24px 0;
}
.dir-list {
  display: flex;
  flex-direction: column;
}
.dir-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  cursor: pointer;
  border-radius: 4px;
}
.dir-item:hover {
  background: var(--el-fill-color-light);
}
.dir-item.selected {
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
}
.dir-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.footer-buttons {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
