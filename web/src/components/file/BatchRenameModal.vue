<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import AppModal from "@/components/base/AppModal.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import FormField from "@/components/base/FormField.vue";
import SvgIcon from "@/components/icons/SvgIcon.vue";
import type { FileItem } from "@/api/types";
import {
  BATCH_RENAME_RULE_LABELS,
  batchRenameApi,
  defaultBatchRenameRule,
  normalizeBatchRenameRule,
  type BatchRenameApplyResult,
  type BatchRenameHistoryEntry,
  type BatchRenameItem,
  type BatchRenamePreset,
  type BatchRenamePreviewResult,
  type BatchRenameRule,
  type BatchRenameRuleType,
} from "@/api/batchRename";

const props = defineProps<{
  open: boolean;
  accountId: number;
  /** 当前目录 id，作为 items 的缺省 parent_id。 */
  parentId: string;
  /** 当前目录名，作为「添加文件夹名」规则的缺省值。 */
  folderName: string;
  /** 勾选的文件（用于重命名）。 */
  selectedFiles: FileItem[];
  /** 当前目录下未勾选的文件名，用于服务端冲突校验。 */
  existingNames: string[];
}>();

const emit = defineEmits<{
  close: [];
  /** 全部成功应用后通知父组件刷新列表。 */
  applied: [];
}>();

type ViewTab = "editor" | "presets" | "history";

const RULE_TYPES = Object.keys(BATCH_RENAME_RULE_LABELS) as BatchRenameRuleType[];
const ruleTypeOptions = RULE_TYPES.map((type) => ({
  value: type,
  label: BATCH_RENAME_RULE_LABELS[type],
}));

const view = ref<ViewTab>("editor");
const rules = ref<BatchRenameRule[]>([defaultBatchRenameRule("replace")]);
const addRuleType = ref<BatchRenameRuleType>("replace");
const keepExt = ref(true);
/** 用户手动取消勾选的条目 id；默认全部参与。 */
const excludedIds = ref<string[]>([]);

const preview = ref<BatchRenamePreviewResult | null>(null);
const previewLoading = ref(false);
const previewError = ref("");
const previewRequested = ref(false);

const applying = ref(false);
const applyResult = ref<BatchRenameApplyResult | null>(null);

const presets = ref<BatchRenamePreset[]>([]);
const presetsLoading = ref(false);
const history = ref<BatchRenameHistoryEntry[]>([]);
const historyLoading = ref(false);
const rollingBackId = ref<number | null>(null);
const presetName = ref("");

const selectionOptions = computed(() => ({
  keepExtOptions: [
    { value: "keep", label: "保留扩展名" },
    { value: "raw", label: "不保留扩展名" },
  ],
}));

const keepExtValue = computed({
  get: () => (keepExt.value ? "keep" : "raw"),
  set: (value: string | number | boolean) => {
    keepExt.value = value === "keep";
  },
});

/** 参与本次重命名的条目。 */
const targetFiles = computed(() => props.selectedFiles.filter((f) => !excludedIds.value.includes(f.id)));

const changedRows = computed(() => (preview.value?.items ?? []).filter((row) => row.new_name !== row.name));

const canApply = computed(
  () =>
    !applying.value &&
    !previewLoading.value &&
    changedRows.value.length > 0 &&
    (preview.value?.errors.length ?? 0) === 0,
);

const summaryText = computed(() => {
  if (!previewRequested.value) return "点击「刷新预览」生成结果";
  const total = preview.value?.total_count ?? 0;
  const changed = preview.value?.changed_count ?? 0;
  return `共 ${total} 项，其中 ${changed} 项将改名`;
});

let previewTimer: number | undefined;

function buildItems(): BatchRenameItem[] {
  const parentId = props.parentId || "0";
  return targetFiles.value.map((file) => ({
    file_id: file.id,
    name: file.name,
    type: file.is_dir ? 1 : 0,
    parent_id: parentId,
  }));
}

/** 规则变更后 200ms 防抖刷新预览，与老版行为一致。 */
function schedulePreview() {
  if (previewTimer !== undefined) window.clearTimeout(previewTimer);
  previewTimer = window.setTimeout(() => {
    previewTimer = undefined;
    void loadPreview();
  }, 200);
}

async function loadPreview() {
  if (!props.accountId || buildItems().length === 0) {
    preview.value = null;
    previewRequested.value = false;
    return;
  }
  previewLoading.value = true;
  previewError.value = "";
  previewRequested.value = true;
  try {
    preview.value = await batchRenameApi.preview({
      account_id: props.accountId,
      parent_id: props.parentId || "0",
      folder_name: props.folderName,
      keep_ext: keepExt.value,
      rules: rules.value.map(normalizeBatchRenameRule),
      items: buildItems(),
      existing_names: props.existingNames,
    });
  } catch (error) {
    preview.value = null;
    previewError.value = extractError(error, "批量重命名预览失败");
  } finally {
    previewLoading.value = false;
  }
}

function extractError(error: unknown, fallback: string): string {
  if (error instanceof Error && error.message) return error.message;
  return fallback;
}

function addRule() {
  rules.value.push(defaultBatchRenameRule(addRuleType.value));
  schedulePreview();
}

function removeRule(index: number) {
  rules.value.splice(index, 1);
  if (rules.value.length === 0) rules.value.push(defaultBatchRenameRule("replace"));
  schedulePreview();
}

function moveRule(index: number, delta: number) {
  const next = index + delta;
  if (next < 0 || next >= rules.value.length) return;
  const [item] = rules.value.splice(index, 1);
  rules.value.splice(next, 0, item);
  schedulePreview();
}

function onRuleTypeChange(rule: BatchRenameRule, type: BatchRenameRuleType) {
  Object.assign(rule, defaultBatchRenameRule(type));
  schedulePreview();
}

function toggleFile(fileId: string) {
  const index = excludedIds.value.indexOf(fileId);
  if (index >= 0) excludedIds.value.splice(index, 1);
  else excludedIds.value.push(fileId);
  schedulePreview();
}

async function loadPresets() {
  presetsLoading.value = true;
  try {
    const result = await batchRenameApi.listPresets();
    presets.value = result.items ?? [];
  } catch {
    presets.value = [];
  } finally {
    presetsLoading.value = false;
  }
}

async function loadHistory() {
  historyLoading.value = true;
  try {
    const result = await batchRenameApi.history();
    history.value = result.items ?? [];
  } catch {
    history.value = [];
  } finally {
    historyLoading.value = false;
  }
}

/** 应用常用组合：补齐缺省字段，避免旧组合缺字段导致预览异常。 */
function applyPreset(preset: BatchRenamePreset) {
  rules.value = (preset.rules ?? []).map((rule) => ({
    ...defaultBatchRenameRule(rule.type),
    ...rule,
  }));
  keepExt.value = preset.keep_ext;
  view.value = "editor";
  schedulePreview();
}

async function savePreset() {
  const name = presetName.value.trim() || props.folderName || "批量重命名";
  try {
    await batchRenameApi.savePreset({
      name,
      keep_ext: keepExt.value,
      rules: rules.value.map(normalizeBatchRenameRule),
    });
    presetName.value = "";
    await loadPresets();
  } catch (error) {
    previewError.value = extractError(error, "保存常用组合失败");
  }
}

async function deletePreset(id: number) {
  try {
    await batchRenameApi.deletePreset(id);
    await loadPresets();
  } catch (error) {
    previewError.value = extractError(error, "删除常用组合失败");
  }
}

async function handleApply() {
  if (!canApply.value || !preview.value) return;
  applying.value = true;
  previewError.value = "";
  applyResult.value = null;
  const parentId = props.parentId || "0";
  try {
    const result = await batchRenameApi.apply({
      account_id: props.accountId,
      parent_id: parentId,
      label: props.folderName || "批量重命名",
      keep_ext: keepExt.value,
      rules: rules.value.map(normalizeBatchRenameRule),
      items: changedRows.value.map((row) => ({
        file_id: row.file_id,
        name: row.name,
        new_name: row.new_name,
        type: row.type ?? 0,
        parent_id: row.parent_id || parentId,
      })),
    });
    applyResult.value = result;
    await loadPreview();
    await loadHistory();
    emit("applied");
  } catch (error) {
    previewError.value = extractError(error, "批量重命名执行失败");
  } finally {
    applying.value = false;
  }
}

async function rollback(entry: BatchRenameHistoryEntry) {
  if (!props.accountId || rollingBackId.value !== null) return;
  rollingBackId.value = entry.id;
  try {
    await batchRenameApi.rollback({ account_id: props.accountId, history_id: entry.id });
    await loadHistory();
    await loadPreview();
    emit("applied");
  } catch (error) {
    previewError.value = extractError(error, "回滚失败");
  } finally {
    rollingBackId.value = null;
  }
}

function handleClose() {
  if (applying.value) return;
  emit("close");
}

function handleTab(tab: ViewTab) {
  view.value = tab;
  if (tab === "presets") void loadPresets();
  if (tab === "history") void loadHistory();
}

function reset() {
  if (previewTimer !== undefined) {
    window.clearTimeout(previewTimer);
    previewTimer = undefined;
  }
  view.value = "editor";
  rules.value = [defaultBatchRenameRule("replace")];
  addRuleType.value = "replace";
  keepExt.value = true;
  excludedIds.value = [];
  preview.value = null;
  previewLoading.value = false;
  previewError.value = "";
  previewRequested.value = false;
  applying.value = false;
  applyResult.value = null;
  presetName.value = "";
}

// 打开时重置并立即预览；关闭时清理定时器。
watch(
  () => props.open,
  (open) => {
    if (open) {
      reset();
      void loadPreview();
    } else {
      reset();
    }
  },
);

onUnmounted(() => {
  if (previewTimer !== undefined) window.clearTimeout(previewTimer);
});
</script>

<template>
  <AppModal :open="open" size="lg" @close="handleClose">
    <template #header>
      <div class="batch-rename__title-wrap">
        <h3 class="batch-rename__title">批量重命名</h3>
        <span class="batch-rename__subtitle">组合规则批量改写文件名，执行前请核对预览。</span>
      </div>
    </template>

    <div class="batch-rename">
      <div class="batch-rename__tabs">
        <button
          type="button"
          class="batch-rename__tab"
          :class="{ 'batch-rename__tab--active': view === 'editor' }"
          @click="handleTab('editor')"
        >
          <SvgIcon name="wand-magic-sparkles" :size="14" />
          规则编辑
        </button>
        <button
          type="button"
          class="batch-rename__tab"
          :class="{ 'batch-rename__tab--active': view === 'presets' }"
          @click="handleTab('presets')"
        >
          <SvgIcon name="save" :size="14" />
          常用组合
        </button>
        <button
          type="button"
          class="batch-rename__tab"
          :class="{ 'batch-rename__tab--active': view === 'history' }"
          @click="handleTab('history')"
        >
          <SvgIcon name="clock-rotate-left" :size="14" />
          历史记录
        </button>
      </div>

      <p v-if="previewError" class="batch-rename__error">
        <SvgIcon name="triangle-exclamation" :size="14" />
        {{ previewError }}
      </p>

      <!-- 规则编辑 -->
      <div v-if="view === 'editor'" class="batch-rename__body">
        <div class="batch-rename__section">
          <div class="batch-rename__section-head">
            <span class="batch-rename__section-title">规则（按顺序依次应用）</span>
            <div class="batch-rename__section-actions">
              <AppSelect v-model="addRuleType" :options="ruleTypeOptions" />
              <AppButton variant="primary" size="sm" @click="addRule">
                <SvgIcon name="plus" :size="13" />
                添加规则
              </AppButton>
            </div>
          </div>

          <div v-for="(rule, index) in rules" :key="index" class="batch-rename__rule">
            <div class="batch-rename__rule-head">
              <span class="batch-rename__rule-index">{{ index + 1 }}</span>
              <AppSelect
                :model-value="rule.type"
                :options="ruleTypeOptions"
                @update:model-value="onRuleTypeChange(rule, $event as BatchRenameRuleType)"
              />
              <button
                type="button"
                class="batch-rename__icon-btn"
                :disabled="index === 0"
                title="上移"
                @click="moveRule(index, -1)"
              >
                <SvgIcon name="chevron-up" :size="13" />
              </button>
              <button
                type="button"
                class="batch-rename__icon-btn"
                :disabled="index === rules.length - 1"
                title="下移"
                @click="moveRule(index, 1)"
              >
                <SvgIcon name="chevron-down" :size="13" />
              </button>
              <button
                type="button"
                class="batch-rename__icon-btn batch-rename__icon-btn--danger"
                title="删除规则"
                @click="removeRule(index)"
              >
                <SvgIcon name="trash" :size="13" />
              </button>
            </div>

            <div class="batch-rename__rule-body">
              <!-- 查找替换 -->
              <template v-if="rule.type === 'replace'">
                <FormField label="查找内容">
                  <AppInput v-model="rule.find" placeholder="要查找的文字" @update:model-value="schedulePreview" />
                </FormField>
                <FormField label="替换为">
                  <AppInput v-model="rule.replace" placeholder="留空表示删除" @update:model-value="schedulePreview" />
                </FormField>
                <div class="batch-rename__checks">
                  <label class="batch-rename__check">
                    <input v-model="rule.case_sensitive" type="checkbox" @change="schedulePreview" />
                    区分大小写
                  </label>
                  <label class="batch-rename__check">
                    <input v-model="rule.first_only" type="checkbox" @change="schedulePreview" />
                    仅替换首个
                  </label>
                </div>
              </template>

              <!-- 添加文件夹名 -->
              <template v-else-if="rule.type === 'folder'">
                <FormField label="文件夹名">
                  <AppInput
                    v-model="rule.folder_name"
                    :placeholder="folderName || '留空则使用当前目录名'"
                    @update:model-value="schedulePreview"
                  />
                </FormField>
                <FormField label="位置">
                  <AppSelect
                    v-model="rule.position"
                    :options="[
                      { value: 'prefix', label: '前缀' },
                      { value: 'suffix', label: '后缀' },
                    ]"
                  />
                </FormField>
                <FormField label="分隔符">
                  <AppInput v-model="rule.separator" placeholder="-" @update:model-value="schedulePreview" />
                </FormField>
              </template>

              <!-- 正则重命名 -->
              <template v-else-if="rule.type === 'regex'">
                <FormField label="正则表达式">
                  <AppInput v-model="rule.pattern" placeholder="^(.+?)\.(\d+)$" @update:model-value="schedulePreview" />
                </FormField>
                <FormField label="替换为">
                  <AppInput v-model="rule.replace" placeholder="$2-$1" @update:model-value="schedulePreview" />
                </FormField>
              </template>

              <!-- 名称模板 -->
              <template v-else-if="rule.type === 'setname'">
                <FormField label="名称模板">
                  <AppInput v-model="rule.pattern" placeholder="{name} {n}" @update:model-value="schedulePreview" />
                </FormField>
                <FormField label="起始编号">
                  <AppInput v-model="rule.start" type="number" @update:model-value="schedulePreview" />
                </FormField>
                <FormField label="位数">
                  <AppInput v-model="rule.digits" type="number" @update:model-value="schedulePreview" />
                </FormField>
              </template>

              <!-- 序号 -->
              <template v-else-if="rule.type === 'number'">
                <FormField label="位置">
                  <AppSelect
                    v-model="rule.position"
                    :options="[
                      { value: 'prefix', label: '前缀' },
                      { value: 'suffix', label: '后缀' },
                      { value: 'replace', label: '替换原名称' },
                    ]"
                  />
                </FormField>
                <FormField label="起始编号">
                  <AppInput v-model="rule.start" type="number" @update:model-value="schedulePreview" />
                </FormField>
                <FormField label="位数">
                  <AppInput v-model="rule.digits" type="number" @update:model-value="schedulePreview" />
                </FormField>
                <FormField label="前缀文字">
                  <AppInput v-model="rule.prefix" placeholder="EP-" @update:model-value="schedulePreview" />
                </FormField>
                <FormField label="后缀文字">
                  <AppInput v-model="rule.suffix" @update:model-value="schedulePreview" />
                </FormField>
              </template>

              <!-- 添加分隔符 / 添加字符 -->
              <template v-else-if="rule.type === 'separator' || rule.type === 'add'">
                <FormField label="字符">
                  <AppInput v-model="rule.text" placeholder="-" @update:model-value="schedulePreview" />
                </FormField>
                <FormField label="位置">
                  <AppSelect
                    v-model="rule.position"
                    :options="[
                      { value: 'start', label: '开头' },
                      { value: 'end', label: '结尾' },
                      { value: 'index', label: '指定位置' },
                    ]"
                  />
                </FormField>
                <FormField v-if="rule.position === 'index'" label="位置下标">
                  <AppInput v-model="rule.index" type="number" @update:model-value="schedulePreview" />
                </FormField>
              </template>

              <!-- 删除字符 -->
              <template v-else-if="rule.type === 'delete'">
                <FormField label="模式">
                  <AppSelect
                    v-model="rule.mode"
                    :options="[
                      { value: 'text', label: '按文字删除' },
                      { value: 'range', label: '按范围删除' },
                    ]"
                  />
                </FormField>
                <FormField v-if="rule.mode === 'range'" label="起始位置">
                  <AppInput v-model="rule.start" type="number" @update:model-value="schedulePreview" />
                </FormField>
                <FormField v-if="rule.mode === 'range'" label="长度">
                  <AppInput v-model="rule.length" type="number" @update:model-value="schedulePreview" />
                </FormField>
                <FormField v-else label="删除的文字">
                  <AppInput v-model="rule.text" @update:model-value="schedulePreview" />
                </FormField>
              </template>

              <!-- 移动字符 -->
              <template v-else-if="rule.type === 'move'">
                <FormField label="起始位置">
                  <AppInput v-model="rule.start" type="number" @update:model-value="schedulePreview" />
                </FormField>
                <FormField label="长度">
                  <AppInput v-model="rule.length" type="number" @update:model-value="schedulePreview" />
                </FormField>
                <FormField label="移动到">
                  <AppInput v-model="rule.to" type="number" @update:model-value="schedulePreview" />
                </FormField>
              </template>

              <!-- 大小写 -->
              <template v-else-if="rule.type === 'case'">
                <FormField label="转换方式">
                  <AppSelect
                    v-model="rule.mode"
                    :options="[
                      { value: 'upper', label: '全部大写' },
                      { value: 'lower', label: '全部小写' },
                      { value: 'title', label: '首字母大写' },
                    ]"
                  />
                </FormField>
              </template>

              <!-- 空格 -->
              <template v-else-if="rule.type === 'space'">
                <FormField label="处理方式">
                  <AppSelect
                    v-model="rule.mode"
                    :options="[
                      { value: 'trim', label: '去除首尾空格' },
                      { value: 'collapse', label: '合并连续空格' },
                      { value: 'all', label: '删除全部空格' },
                    ]"
                  />
                </FormField>
              </template>

              <!-- 全角半角 -->
              <template v-else-if="rule.type === 'width'">
                <FormField label="转换方式">
                  <AppSelect
                    v-model="rule.mode"
                    :options="[
                      { value: 'half', label: '全角转半角' },
                      { value: 'full', label: '半角转全角' },
                    ]"
                  />
                </FormField>
              </template>
            </div>
          </div>

          <div class="batch-rename__options">
            <FormField label="扩展名">
              <AppSelect v-model="keepExtValue" :options="selectionOptions.keepExtOptions" />
            </FormField>
            <div class="batch-rename__preset-save">
              <FormField label="另存为常用组合">
                <AppInput v-model="presetName" :placeholder="folderName || '批量重命名'" />
              </FormField>
              <AppButton variant="secondary" size="sm" @click="savePreset">
                <SvgIcon name="save" :size="13" />
                保存
              </AppButton>
            </div>
          </div>
        </div>

        <div class="batch-rename__section">
          <div class="batch-rename__section-head">
            <span class="batch-rename__section-title">
              参与重命名（{{ targetFiles.length }} / {{ selectedFiles.length }}）
            </span>
            <AppButton variant="secondary" size="sm" :disabled="previewLoading" @click="loadPreview">
              <SvgIcon name="rotate" :size="13" />
              刷新预览
            </AppButton>
          </div>

          <div class="batch-rename__files">
            <button
              v-for="file in selectedFiles"
              :key="file.id"
              type="button"
              class="batch-rename__file"
              :class="{ 'batch-rename__file--off': excludedIds.includes(file.id) }"
              @click="toggleFile(file.id)"
            >
              <SvgIcon :name="file.is_dir ? 'folder' : 'file'" :size="13" />
              <span class="batch-rename__file-name">{{ file.name }}</span>
            </button>
          </div>

          <p v-if="preview?.errors.length" class="batch-rename__issues">
            <SvgIcon name="triangle-exclamation" :size="14" />
            <span>
              <span v-for="(issue, index) in preview?.errors ?? []" :key="index" class="batch-rename__issue">
                {{ issue }}
              </span>
            </span>
          </p>
        </div>

        <div class="batch-rename__section">
          <div class="batch-rename__section-head">
            <span class="batch-rename__section-title">预览结果</span>
            <span class="batch-rename__summary">{{ summaryText }}</span>
          </div>
          <AppStateBlock v-if="previewLoading" message="正在生成预览…" loading min-height="120px" />
          <AppStateBlock v-else-if="!previewRequested" message="点击「刷新预览」生成结果" min-height="120px" />
          <AppStateBlock v-else-if="changedRows.length === 0" message="当前规则不会改变任何文件名" min-height="120px" />
          <ul v-else class="batch-rename__preview">
            <li v-for="row in preview?.items ?? []" :key="row.file_id" class="batch-rename__preview-row">
              <span class="batch-rename__old">{{ row.name }}</span>
              <SvgIcon name="arrow-right-long" :size="13" />
              <span
                class="batch-rename__new"
                :class="{ 'batch-rename__new--same': row.new_name === row.name }"
              >
                {{ row.new_name }}
              </span>
            </li>
          </ul>
        </div>

        <p v-if="applyResult" class="batch-rename__result">
          <SvgIcon name="circle-info" :size="14" />
          成功 {{ applyResult.success_count }} 个，失败 {{ applyResult.fail_count }} 个
          <span v-for="fail in applyResult.failed" :key="fail.file_id" class="batch-rename__issue">
            {{ fail.name }}：{{ fail.reason }}
          </span>
        </p>
      </div>

      <!-- 常用组合 -->
      <div v-else-if="view === 'presets'" class="batch-rename__body">
        <AppStateBlock v-if="presetsLoading" message="正在读取常用组合…" loading min-height="120px" />
        <AppStateBlock v-else-if="presets.length === 0" message="暂无常用组合，可在规则编辑页保存" min-height="120px" />
        <ul v-else class="batch-rename__list">
          <li v-for="preset in presets" :key="preset.id" class="batch-rename__list-row">
            <div class="batch-rename__list-main">
              <span class="batch-rename__list-title">{{ preset.name }}</span>
              <span class="batch-rename__list-meta">
                {{ preset.rules.length }} 条规则 · 使用 {{ preset.use_count }} 次
              </span>
            </div>
            <AppButton variant="secondary" size="sm" @click="applyPreset(preset)">应用</AppButton>
            <button
              type="button"
              class="batch-rename__icon-btn batch-rename__icon-btn--danger"
              title="删除"
              @click="deletePreset(preset.id)"
            >
              <SvgIcon name="trash" :size="13" />
            </button>
          </li>
        </ul>
      </div>

      <!-- 历史记录 -->
      <div v-else class="batch-rename__body">
        <AppStateBlock v-if="historyLoading" message="正在读取历史记录…" loading min-height="120px" />
        <AppStateBlock v-else-if="history.length === 0" message="暂无重命名历史" min-height="120px" />
        <ul v-else class="batch-rename__list">
          <li v-for="entry in history" :key="entry.id" class="batch-rename__list-row">
            <div class="batch-rename__list-main">
              <span class="batch-rename__list-title">{{ entry.name }}</span>
              <span class="batch-rename__list-meta">
                {{ entry.created_at }} · 共 {{ entry.item_count }} 项，改动 {{ entry.change_count }} 项
              </span>
            </div>
            <AppButton
              variant="secondary"
              size="sm"
              :disabled="rollingBackId !== null || entry.change_count === 0"
              @click="rollback(entry)"
            >
              <SvgIcon name="clock-rotate-left" :size="13" />
              回滚
            </AppButton>
          </li>
        </ul>
      </div>
    </div>

    <template #footer>
      <AppButton variant="cancel" @click="handleClose">关闭</AppButton>
      <AppButton
        v-if="view === 'editor'"
        variant="primary"
        :disabled="!canApply"
        @click="handleApply"
      >
        <SvgIcon name="check" :size="13" />
        {{ applying ? "正在执行…" : `执行重命名（${changedRows.length}）` }}
      </AppButton>
    </template>
  </AppModal>
</template>

<style scoped>
.batch-rename__title-wrap {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.batch-rename__title {
  margin: 0;
  font-size: 17px;
  color: var(--text);
}
.batch-rename__subtitle {
  font-size: 12px;
  color: var(--text-muted);
}
.batch-rename {
  display: flex;
  flex-direction: column;
  gap: 14px;
  min-height: 380px;
}
.batch-rename__tabs {
  display: flex;
  gap: 6px;
  border-bottom: 1px solid var(--border);
  padding-bottom: 8px;
}
.batch-rename__tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 7px 14px;
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-regular);
  font-size: 13px;
  cursor: pointer;
  transition: var(--transition);
}
.batch-rename__tab:hover {
  background: var(--border-soft);
}
.batch-rename__tab--active {
  background: color-mix(in srgb, var(--brand) 10%, var(--surface));
  border-color: color-mix(in srgb, var(--brand) 30%, var(--border));
  color: var(--brand);
  font-weight: 600;
}
.batch-rename__error,
.batch-rename__result {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin: 0;
  padding: 9px 12px;
  border-radius: var(--radius-sm);
  font-size: 13px;
}
.batch-rename__error {
  background: color-mix(in srgb, var(--danger) 10%, var(--surface));
  color: var(--danger);
}
.batch-rename__result {
  background: color-mix(in srgb, var(--brand) 8%, var(--surface));
  color: var(--text-regular);
}
.batch-rename__body {
  display: flex;
  flex-direction: column;
  gap: 16px;
  max-height: 60vh;
  overflow-y: auto;
  padding-right: 4px;
}
.batch-rename__section {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.batch-rename__section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.batch-rename__section-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.batch-rename__section-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 260px;
}
.batch-rename__summary {
  font-size: 12px;
  color: var(--text-muted);
}
.batch-rename__rule {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface-soft, var(--surface));
}
.batch-rename__rule-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.batch-rename__rule-head :deep(.select) {
  max-width: 240px;
}
.batch-rename__rule-index {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: var(--radius-pill);
  background: color-mix(in srgb, var(--brand) 12%, var(--surface));
  color: var(--brand);
  font-size: 12px;
  font-weight: 600;
  flex-shrink: 0;
}
.batch-rename__rule-body {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 10px;
}
.batch-rename__checks {
  display: flex;
  align-items: center;
  gap: 14px;
  font-size: 13px;
  color: var(--text-regular);
}
.batch-rename__check {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
}
.batch-rename__icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface);
  color: var(--text-regular);
  cursor: pointer;
  transition: var(--transition);
  flex-shrink: 0;
}
.batch-rename__icon-btn:hover:not(:disabled) {
  border-color: var(--brand);
  color: var(--brand);
}
.batch-rename__icon-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.batch-rename__icon-btn--danger:hover:not(:disabled) {
  border-color: var(--danger);
  color: var(--danger);
}
.batch-rename__options {
  display: grid;
  grid-template-columns: minmax(160px, 200px) 1fr;
  gap: 12px;
  align-items: end;
}
.batch-rename__preset-save {
  display: flex;
  align-items: flex-end;
  gap: 8px;
}
.batch-rename__preset-save :deep(.form-field) {
  flex: 1;
}
.batch-rename__files {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  max-height: 130px;
  overflow-y: auto;
}
.batch-rename__file {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  padding: 5px 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius-pill);
  background: var(--surface);
  color: var(--text-regular);
  font-size: 12px;
  cursor: pointer;
  transition: var(--transition);
}
.batch-rename__file--off {
  opacity: 0.45;
  text-decoration: line-through;
}
.batch-rename__file-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 220px;
}
.batch-rename__issues {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0;
  padding: 9px 12px;
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--warning) 12%, var(--surface));
  color: #b45309;
  font-size: 12px;
}
.batch-rename__issue {
  display: block;
}
.batch-rename__preview {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 260px;
  overflow-y: auto;
}
.batch-rename__preview-row {
  display: grid;
  grid-template-columns: 1fr auto 1fr;
  align-items: center;
  gap: 10px;
  padding: 7px 10px;
  border-radius: var(--radius-xs);
  background: var(--border-soft);
  font-size: 12.5px;
}
.batch-rename__old {
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.batch-rename__new {
  color: var(--brand);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.batch-rename__new--same {
  color: var(--text-muted);
  font-weight: 400;
}
.batch-rename__list {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.batch-rename__list-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface);
}
.batch-rename__list-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1;
  min-width: 0;
}
.batch-rename__list-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.batch-rename__list-meta {
  font-size: 12px;
  color: var(--text-muted);
}
</style>
