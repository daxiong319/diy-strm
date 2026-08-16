<template>
  <el-dialog
    :model-value="modelValue"
    title="批量重命名"
    width="min(1040px, calc(100vw - 32px))"
    :close-on-click-modal="false"
    append-to-body
    @update:model-value="(value: boolean) => emit('update:modelValue', value)"
    @closed="resetDialog"
  >
    <el-tabs v-model="view" class="batch-rename-tabs">
      <el-tab-pane label="编辑规则" name="editor">
        <div class="rename-candidates">
          <span class="candidate-info">
            共 {{ candidateFiles.length }} 个条目，已选 {{ selectedIds.size }} 个
          </span>
          <el-button link type="primary" :disabled="candidateFiles.length === 0" @click="toggleSelectAll">
            {{ isAllSelected ? '取消全选' : '全选' }}
          </el-button>
          <el-button link type="danger" :disabled="selectedIds.size === 0" @click="clearSelection">
            清空
          </el-button>
        </div>

        <div class="rule-toolbar">
          <el-select v-model="addRuleType" size="small" style="width: 160px">
            <el-option v-for="item in ruleTypeOptions" :key="item.value" :label="item.label" :value="item.value" />
          </el-select>
          <el-button size="small" type="primary" @click="addRule">+ 添加规则</el-button>
          <el-button size="small" @click="handleSavePresetPrompt">保存为常用组合</el-button>
        </div>

        <div v-if="rules.length === 0" class="rename-empty">暂无规则</div>
        <div v-for="(rule, index) in rules" :key="index" class="rule-card">
          <div class="rule-head">
            <span class="rule-title">{{ index + 1 }}. {{ ruleTypeLabel(rule.type) }}</span>
            <el-button size="small" :disabled="index === 0" @click="moveRule(index, -1)">上移</el-button>
            <el-button size="small" :disabled="index === rules.length - 1" @click="moveRule(index, 1)">下移</el-button>
            <el-button size="small" type="danger" @click="removeRule(index)">删除</el-button>
          </div>
          <el-form label-width="86px" size="small" class="rule-fields">
            <el-form-item label="规则类型">
              <el-select v-model="rule.type" style="width: 220px" @change="handleRuleTypeChange(index, rule)">
                <el-option v-for="item in ruleTypeOptions" :key="item.value" :label="item.label" :value="item.value" />
              </el-select>
            </el-form-item>
            <template v-if="rule.type === 'replace'">
              <el-form-item label="查找">
                <el-input v-model="rule.find" placeholder="要查找的文字" />
              </el-form-item>
              <el-form-item label="替换为">
                <el-input v-model="rule.replace" placeholder="留空表示删除" />
              </el-form-item>
              <el-form-item label="选项">
                <el-checkbox v-model="rule.case_sensitive">区分大小写</el-checkbox>
                <el-checkbox v-model="rule.first_only">仅替换第一处</el-checkbox>
              </el-form-item>
            </template>
            <template v-else-if="rule.type === 'folder'">
              <el-form-item label="文件夹名">
                <el-input v-model="rule.folder_name" :placeholder="folderName || '输入目录名'" />
              </el-form-item>
              <el-form-item label="分隔符">
                <el-input v-model="rule.separator" placeholder="例如 -" />
              </el-form-item>
              <el-form-item label="添加位置">
                <el-radio-group v-model="rule.position">
                  <el-radio-button value="prefix">名称之前</el-radio-button>
                  <el-radio-button value="suffix">名称之后</el-radio-button>
                </el-radio-group>
              </el-form-item>
            </template>
            <template v-else-if="rule.type === 'regex'">
              <el-form-item label="正则表达式">
                <el-input v-model="rule.pattern" placeholder="例如 ^(.+)$" />
              </el-form-item>
              <el-form-item label="替换模板">
                <el-input v-model="rule.replace" placeholder="例如 $1" />
              </el-form-item>
            </template>
            <template v-else-if="rule.type === 'setname'">
              <el-form-item label="名称模板">
                <el-input v-model="rule.pattern" placeholder="支持 {name} 和 {n}" />
              </el-form-item>
              <el-form-item label="开始序号">
                <el-input-number v-model="rule.start" :min="0" :controls="false" style="width: 140px" />
              </el-form-item>
              <el-form-item label="序号位数">
                <el-input-number v-model="rule.digits" :min="1" :max="9" :controls="false" style="width: 140px" />
              </el-form-item>
            </template>
            <template v-else-if="rule.type === 'number'">
              <el-form-item label="序号添加到">
                <el-radio-group v-model="rule.position">
                  <el-radio-button value="replace">替换名称</el-radio-button>
                  <el-radio-button value="prefix">名称之前</el-radio-button>
                  <el-radio-button value="suffix">名称之后</el-radio-button>
                </el-radio-group>
              </el-form-item>
              <el-form-item label="开始序号">
                <el-input-number v-model="rule.start" :min="0" :controls="false" style="width: 140px" />
              </el-form-item>
              <el-form-item label="序号最少位数">
                <el-input-number v-model="rule.digits" :min="1" :max="9" :controls="false" style="width: 140px" />
              </el-form-item>
              <el-form-item label="序号前缀">
                <el-input v-model="rule.prefix" placeholder="可选" />
              </el-form-item>
              <el-form-item label="序号后缀">
                <el-input v-model="rule.suffix" placeholder="可选" />
              </el-form-item>
            </template>
            <template v-else-if="rule.type === 'separator' || rule.type === 'add'">
              <el-form-item :label="rule.type === 'separator' ? '分隔符' : '添加字符'">
                <el-input v-model="rule.text" :placeholder="rule.type === 'separator' ? '例如 - 或 _' : '输入要添加的内容'" />
              </el-form-item>
              <el-form-item label="添加位置">
                <el-radio-group v-model="rule.position">
                  <el-radio-button value="start">名称开头</el-radio-button>
                  <el-radio-button value="end">名称结尾</el-radio-button>
                  <el-radio-button value="index">指定位置</el-radio-button>
                </el-radio-group>
              </el-form-item>
              <el-form-item v-if="rule.position === 'index'" label="字符位置">
                <el-input-number v-model="rule.index" :min="1" :controls="false" style="width: 140px" />
              </el-form-item>
            </template>
            <template v-else-if="rule.type === 'delete'">
              <el-form-item label="删除方式">
                <el-radio-group v-model="rule.mode">
                  <el-radio-button value="text">指定字符</el-radio-button>
                  <el-radio-button value="range">指定位置</el-radio-button>
                </el-radio-group>
              </el-form-item>
              <el-form-item v-if="rule.mode === 'range'" label="开始位置">
                <el-input-number v-model="rule.start" :min="1" :controls="false" style="width: 140px" />
              </el-form-item>
              <el-form-item v-if="rule.mode === 'range'" label="删除长度">
                <el-input-number v-model="rule.length" :min="1" :controls="false" style="width: 140px" />
              </el-form-item>
              <el-form-item v-else label="删除字符">
                <el-input v-model="rule.text" placeholder="输入要删除的内容" />
              </el-form-item>
            </template>
            <template v-else-if="rule.type === 'move'">
              <el-form-item label="开始位置">
                <el-input-number v-model="rule.start" :min="1" :controls="false" style="width: 140px" />
              </el-form-item>
              <el-form-item label="字符长度">
                <el-input-number v-model="rule.length" :min="1" :controls="false" style="width: 140px" />
              </el-form-item>
              <el-form-item label="移动到">
                <el-input-number v-model="rule.to" :min="1" :controls="false" style="width: 140px" />
              </el-form-item>
            </template>
            <template v-else-if="rule.type === 'case'">
              <el-form-item label="转换方式">
                <el-radio-group v-model="rule.mode">
                  <el-radio-button value="upper">全部大写</el-radio-button>
                  <el-radio-button value="lower">全部小写</el-radio-button>
                  <el-radio-button value="title">首字母大写</el-radio-button>
                </el-radio-group>
              </el-form-item>
            </template>
            <template v-else-if="rule.type === 'space'">
              <el-form-item label="清理方式">
                <el-radio-group v-model="rule.mode">
                  <el-radio-button value="trim">清理首尾空格</el-radio-button>
                  <el-radio-button value="collapse">合并连续空格</el-radio-button>
                  <el-radio-button value="all">删除所有空格</el-radio-button>
                </el-radio-group>
              </el-form-item>
            </template>
            <template v-else-if="rule.type === 'width'">
              <el-form-item label="转换方式">
                <el-radio-group v-model="rule.mode">
                  <el-radio-button value="half">转为半角</el-radio-button>
                  <el-radio-button value="full">转为全角</el-radio-button>
                </el-radio-group>
              </el-form-item>
            </template>
          </el-form>
        </div>

        <div class="preview-toolbar">
          <el-checkbox v-model="keepExt">保留文件扩展名</el-checkbox>
          <span v-if="changedCount > 0 || previewErrors.length > 0" class="preview-summary">
            共 {{ totalCount }} 项，将重命名 {{ changedCount }} 项
          </span>
          <el-button size="small" type="primary" :loading="previewLoading" :disabled="selectedIds.size === 0" @click="handlePreview">
            刷新预览
          </el-button>
        </div>

        <div v-if="previewErrors.length > 0" class="preview-errors">
          <div v-for="(error, index) in previewErrors" :key="index">{{ error }}</div>
        </div>

        <el-table v-if="previewRows.length > 0" :data="previewRows" max-height="300" size="small">
          <el-table-column label="原名" min-width="260">
            <template #default="{ row }">
              <span class="old-name">{{ row.target.name }}</span>
            </template>
          </el-table-column>
          <el-table-column label="新名" min-width="260">
            <template #default="{ row }">
              <span :class="['new-name', { changed: row.changed }]">{{ row.target.new_name }}</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="90">
            <template #default="{ row }">
              <el-tag v-if="row.changed" size="small" type="primary">修改</el-tag>
              <el-tag v-else size="small" type="info">不变</el-tag>
            </template>
          </el-table-column>
        </el-table>
        <div v-else-if="previewRequested && !previewLoading" class="rename-empty">
          暂无可重命名的条目（请先勾选文件并添加规则）
        </div>
      </el-tab-pane>

      <el-tab-pane label="常用组合" name="presets">
        <div class="preset-save-row">
          <el-input v-model="presetName" placeholder="常用组合名称" style="width: 280px" />
          <el-button type="primary" :disabled="rules.length === 0" @click="handleSavePreset">保存当前规则</el-button>
        </div>
        <div class="preset-list">
          <div v-if="presets.length === 0" class="rename-empty">暂无常用组合</div>
          <el-card v-for="preset in presets" :key="preset.id" shadow="never" class="preset-card">
            <div class="preset-card-head">
              <span class="preset-card-title">{{ preset.name }}</span>
              <span class="preset-meta">使用 {{ preset.use_count }} 次</span>
            </div>
            <div class="preset-actions">
              <el-button size="small" type="primary" @click="applyPreset(preset)">应用</el-button>
              <el-button size="small" type="danger" @click="deletePreset(preset)">删除</el-button>
            </div>
          </el-card>
        </div>
      </el-tab-pane>

      <el-tab-pane label="历史记录" name="history">
        <div v-if="histories.length === 0" class="rename-empty">暂无历史记录</div>
        <el-card v-for="history in histories" :key="history.id" shadow="never" class="history-card">
          <div class="preset-card-head">
            <span class="preset-card-title">{{ history.name }}</span>
            <span class="preset-meta">
              {{ formatTime(history.created_at) }} · 共 {{ history.item_count }} 项 · 已改 {{ history.change_count }} 项
            </span>
          </div>
          <div class="preset-actions">
            <el-button size="small" type="warning" @click="rollbackHistory(history)">按原名回滚</el-button>
          </div>
        </el-card>
      </el-tab-pane>
    </el-tabs>

    <template #footer>
      <span class="dialog-footer">
        <el-button @click="emit('update:modelValue', false)">取消</el-button>
        <el-button
          v-if="view === 'editor'"
          type="primary"
          :loading="applying"
          :disabled="changedCount === 0 || previewErrors.length > 0"
          @click="handleApply"
        >
          确定重命名（{{ changedCount }}）
        </el-button>
      </span>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useHttpClient } from '@/http/client'
import type { FileSystemItem } from '@/typing'
import { SERVER_URL } from '@/const'

interface Props {
  modelValue: boolean
  accountId: number
  parentId: string
  files: FileSystemItem[]
  folderName: string
}

const props = defineProps<Props>()
const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
  (e: 'renamed'): void
}>()

const http = useHttpClient()

interface RenameRule {
  id: string
  type: string
  find: string
  replace: string
  pattern: string
  position: string
  separator: string
  folder_name: string
  start: number | string
  digits: number | string
  prefix: string
  suffix: string
  text: string
  index: number | string
  mode: string
  length: number | string
  to: number | string
  case_sensitive: boolean
  first_only: boolean
}

interface PreviewTarget {
  file_id: string
  name: string
  new_name: string
  type: number
  parent_id: string
}

interface PreviewRow {
  target: PreviewTarget
  changed: boolean
}

const ruleTypeOptions = [
  { value: 'replace', label: '查找替换' },
  { value: 'folder', label: '添加文件夹名' },
  { value: 'regex', label: '基于正则重命名' },
  { value: 'setname', label: '名称模板' },
  { value: 'number', label: '修改名称/添加序号' },
  { value: 'separator', label: '添加分隔符' },
  { value: 'add', label: '添加字符' },
  { value: 'delete', label: '删除字符' },
  { value: 'move', label: '移动字符' },
  { value: 'case', label: '大小写字母转换' },
  { value: 'space', label: '清理空格' },
  { value: 'width', label: '全角半角转换' },
]

const ruleTypeLabelsMap = Object.fromEntries(ruleTypeOptions.map((item) => [item.value, item.label]))

function ruleTypeLabel(type: string): string {
  return ruleTypeLabelsMap[type] || '批量重命名'
}

function defaultRule(type: string): RenameRule {
  const rule: RenameRule = {
    id: `rule-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    type,
    find: '',
    replace: '',
    pattern: '',
    position: '',
    separator: '',
    folder_name: '',
    start: '',
    digits: '',
    prefix: '',
    suffix: '',
    text: '',
    index: '',
    mode: '',
    length: '',
    to: '',
    case_sensitive: false,
    first_only: false,
  }
  if (type === 'folder') {
    rule.position = 'prefix'
    rule.separator = '-'
  } else if (type === 'setname') {
    rule.pattern = '{name}'
    rule.start = 1
    rule.digits = 2
  } else if (type === 'number') {
    rule.position = 'replace'
    rule.start = 1
    rule.digits = 2
  } else if (type === 'separator' || type === 'add') {
    rule.position = 'end'
    rule.text = type === 'separator' ? '-' : ''
    rule.index = 1
  } else if (type === 'delete') {
    rule.mode = 'text'
    rule.start = 1
    rule.length = 1
  } else if (type === 'move') {
    rule.start = 1
    rule.length = 1
    rule.to = 1
  } else if (type === 'case') {
    rule.mode = 'upper'
  } else if (type === 'space') {
    rule.mode = 'trim'
  } else if (type === 'width') {
    rule.mode = 'half'
  }
  return rule
}

const view = ref<'editor' | 'presets' | 'history'>('editor')
const addRuleType = ref('replace')
const rules = ref<RenameRule[]>([defaultRule('replace')])
const keepExt = ref(true)
const selectedIds = ref<Set<string>>(new Set())
const previewRows = ref<PreviewRow[]>([])
const previewErrors = ref<string[]>([])
const previewLoading = ref(false)
const previewRequested = ref(false)
const applying = ref(false)
const presetName = ref('')
const presets = ref<Array<{ id: number; name: string; rules: RenameRule[]; keep_ext: boolean; use_count: number }>>([])
const histories = ref<Array<{ id: number; name: string; item_count: number; change_count: number; created_at: string }>>([])

const candidateFiles = computed(() => props.files || [])
const isAllSelected = computed(() => candidateFiles.value.length > 0 && selectedIds.value.size === candidateFiles.value.length)
const changedCount = computed(() => previewRows.value.filter((row) => row.changed).length)
const totalCount = computed(() => previewRows.value.length)

function toggleSelectAll() {
  if (isAllSelected.value) {
    selectedIds.value.clear()
  } else {
    candidateFiles.value.forEach((file) => selectedIds.value.add(String(file.id)))
  }
  void loadPreview()
}

function toggleSelectFile(id: string) {
  if (selectedIds.value.has(id)) {
    selectedIds.value.delete(id)
  } else {
    selectedIds.value.add(id)
  }
  void loadPreview()
}

function clearSelection() {
  selectedIds.value.clear()
  void loadPreview()
}

function selectedExistingNames(): string[] {
  const selectedIdsSet = selectedIds.value
  return candidateFiles.value
    .filter((file) => !selectedIdsSet.has(String(file.id)))
    .map((file) => file.name)
}

function selectedItems() {
  return candidateFiles.value
    .filter((file) => selectedIds.value.has(String(file.id)))
    .map((file) => ({
      file_id: String(file.id),
      name: file.name,
      type: file.is_directory ? 1 : 0,
      parent_id: props.parentId || '0',
    }))
}

function normalizeRule(rule: RenameRule) {
  return {
    id: rule.id,
    type: rule.type,
    find: rule.find ?? '',
    replace: rule.replace ?? '',
    pattern: rule.pattern ?? '',
    position: rule.position ?? '',
    separator: rule.separator ?? '',
    folder_name: rule.folder_name ?? '',
    start: String(rule.start ?? '').trim(),
    digits: String(rule.digits ?? '').trim(),
    prefix: rule.prefix ?? '',
    suffix: rule.suffix ?? '',
    text: rule.text ?? '',
    index: String(rule.index ?? '').trim(),
    mode: rule.mode ?? '',
    length: String(rule.length ?? '').trim(),
    to: String(rule.to ?? '').trim(),
    case_sensitive: !!rule.case_sensitive,
    first_only: !!rule.first_only,
  }
}

let previewTimer = 0
function schedulePreview() {
  window.clearTimeout(previewTimer)
  previewTimer = window.setTimeout(() => void loadPreview(), 200)
}

async function loadPreview() {
  if (selectedIds.value.size === 0 || rules.value.length === 0) {
    previewRows.value = []
    previewErrors.value = []
    previewRequested.value = false
    return
  }
  previewLoading.value = true
  previewRequested.value = true
  try {
    const response = await http.post(`${SERVER_URL}/files/batch-rename/preview`, {
      account_id: props.accountId,
      parent_id: props.parentId || '0',
      folder_name: props.folderName,
      keep_ext: keepExt.value,
      rules: rules.value.map(normalizeRule),
      items: selectedItems(),
      existing_names: selectedExistingNames(),
    })
    const data = response?.data
    if (data?.code !== 200) {
      previewRows.value = []
      previewErrors.value = [data?.message || '预览失败']
      return
    }
    previewRows.value = data.data?.items || []
    previewErrors.value = data.data?.errors || []
  } catch {
    previewRows.value = []
    previewErrors.value = ['预览失败，请稍后重试']
  } finally {
    previewLoading.value = false
  }
}

function handlePreview() {
  void loadPreview()
}

function addRule() {
  rules.value.push(defaultRule(addRuleType.value))
  schedulePreview()
}

function removeRule(index: number) {
  rules.value.splice(index, 1)
  if (rules.value.length === 0) rules.value.push(defaultRule(addRuleType.value))
  schedulePreview()
}

function moveRule(index: number, offset: number) {
  const targetIndex = index + offset
  if (targetIndex < 0 || targetIndex >= rules.value.length) return
  const list = [...rules.value]
  const item = list[index]
  list[index] = list[targetIndex]
  list[targetIndex] = item
  rules.value = list
  schedulePreview()
}

function handleRuleTypeChange(index: number, rule: RenameRule) {
  const id = rule.id
  rules.value[index] = defaultRule(rule.type)
  rules.value[index].id = id
  schedulePreview()
}

watch(
  () => [props.modelValue, props.accountId, props.parentId, props.folderName],
  ([open]) => {
    if (open) {
      void loadPresets()
      void loadHistories()
      setDefaultSelection()
    }
  },
)

watch(
  () => props.files,
  () => {
    if (props.modelValue) setDefaultSelection()
  },
  { deep: true },
)

watch(rules, () => schedulePreview(), { deep: true })
watch(keepExt, () => schedulePreview())

function setDefaultSelection() {
  selectedIds.value = new Set(candidateFiles.value.map((file) => String(file.id)))
  void loadPreview()
}

function resetDialog() {
  view.value = 'editor'
  rules.value = [defaultRule('replace')]
  keepExt.value = true
  selectedIds.value = new Set()
  previewRows.value = []
  previewErrors.value = []
  previewRequested.value = false
  presetName.value = ''
}

async function handleApply() {
  if (changedCount.value === 0 || previewErrors.value.length > 0) return
  applying.value = true
  try {
    const response = await http.post(`${SERVER_URL}/files/batch-rename/apply`, {
      account_id: props.accountId,
      parent_id: props.parentId || '0',
      label: '批量重命名',
      keep_ext: keepExt.value,
      rules: rules.value.map(normalizeRule),
      items: previewRows.value.filter((row) => row.changed).map((row) => ({
        file_id: row.target.file_id,
        name: row.target.name,
        new_name: row.target.new_name,
        parent_id: row.target.parent_id || props.parentId || '0',
      })),
    })
    const data = response?.data
    if (data?.code !== 200) {
      ElMessage.error(data?.message || '重命名失败')
      return
    }
    const result = data.data || {}
    ElMessage.success(`重命名完成：成功 ${result.success_count ?? 0} 个，失败 ${result.fail_count ?? 0} 个`)
    emit('renamed')
    emit('update:modelValue', false)
  } catch {
    ElMessage.error('重命名失败')
  } finally {
    applying.value = false
  }
}

async function handleSavePresetPrompt() {
  if (rules.value.length === 0) return
  const name = await ElMessageBox.prompt('请输入常用组合名称', '保存为常用组合', {
    inputValue: props.folderName || '批量重命名',
  }).catch(() => null)
  if (!name) return
  presetName.value = name.value
  await handleSavePreset()
}

async function handleSavePreset() {
  const name = presetName.value.trim()
  if (!name) {
    ElMessage.warning('请输入常用组合名称')
    return
  }
  try {
    const response = await http.post(`${SERVER_URL}/files/batch-rename/presets`, {
      name,
      keep_ext: keepExt.value,
      rules: rules.value.map(normalizeRule),
    })
    if (response?.data?.code === 200) {
      ElMessage.success('常用组合已保存')
      presetName.value = ''
      await loadPresets()
    } else {
      ElMessage.error(response?.data?.message || '保存失败')
    }
  } catch {
    ElMessage.error('保存失败')
  }
}

async function loadPresets() {
  try {
    const response = await http.get(`${SERVER_URL}/files/batch-rename/presets`)
    if (response?.data?.code === 200) {
      presets.value = response.data.data || []
    }
  } catch {
    presets.value = []
  }
}

function applyPreset(preset: { rules: RenameRule[]; keep_ext: boolean }) {
  const presetRules = (preset.rules || []).map((rule) => ({ ...defaultRule(rule.type), ...rule }))
  if (presetRules.length > 0) {
    rules.value = presetRules
  }
  keepExt.value = Boolean(preset.keep_ext)
  view.value = 'editor'
  schedulePreview()
}

async function deletePreset(preset: { id: number; name: string }) {
  try {
    const response = await http.delete(`${SERVER_URL}/files/batch-rename/presets`, { data: { id: preset.id } })
    if (response?.data?.code === 200) {
      ElMessage.success('已删除')
      await loadPresets()
    } else {
      ElMessage.error(response?.data?.message || '删除失败')
    }
  } catch {
    ElMessage.error('删除失败')
  }
}

async function loadHistories() {
  try {
    const response = await http.get(`${SERVER_URL}/files/batch-rename/history`)
    if (response?.data?.code === 200) {
      histories.value = response.data.data || []
    }
  } catch {
    histories.value = []
  }
}

async function rollbackHistory(history: { id: number }) {
  if (!props.accountId) {
    ElMessage.warning('请先选择网盘账号')
    return
  }
  try {
    const response = await http.post(`${SERVER_URL}/files/batch-rename/rollback`, {
      account_id: props.accountId,
      history_id: history.id,
    })
    const data = response?.data
    if (data?.code === 200) {
      ElMessage.success(data.message || '回滚完成')
      emit('renamed')
      await loadHistories()
    } else {
      ElMessage.error(data?.message || '回滚失败')
    }
  } catch {
    ElMessage.error('回滚失败')
  }
}

function formatTime(value: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return String(value)
  return date.toLocaleString()
}
</script>

<style scoped>
.batch-rename-tabs {
  min-height: 420px;
}

.candidate-info {
  margin-right: 8px;
  color: var(--el-text-color-secondary);
  font-size: 13px;
}

.rename-candidates {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-bottom: 10px;
  padding: 8px 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
}

.rule-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}

.rule-card {
  margin-bottom: 10px;
  padding: 10px 12px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
}

.rule-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.rule-title {
  flex: 1;
  font-weight: 600;
  font-size: 14px;
}

.rule-fields {
  margin-top: 4px;
}

.rename-empty {
  padding: 20px 8px;
  text-align: center;
  color: var(--el-text-color-placeholder);
}

.preview-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin: 10px 0;
}

.preview-summary {
  color: var(--el-color-primary);
  font-size: 13px;
}

.preview-errors {
  margin-bottom: 10px;
  padding: 8px 12px;
  border: 1px solid var(--el-color-danger);
  border-radius: 6px;
  color: var(--el-color-danger);
  font-size: 13px;
  background: var(--el-color-danger-light-9);
}

.old-name {
  color: var(--el-text-color-secondary);
  overflow-wrap: anywhere;
}

.new-name.changed {
  color: var(--el-color-primary);
  font-weight: 600;
}

.preset-save-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}

.preset-list,
.history-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.preset-card,
.history-card {
  margin-bottom: 10px;
}

.preset-card-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}

.preset-card-title {
  font-weight: 600;
}

.preset-meta {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.preset-actions {
  margin-top: 10px;
  display: flex;
  gap: 8px;
}
</style>