<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  classificationApi,
  type ClassifyRuleInput,
  type ClassifyRuleItem,
} from "@/api/cloudTools";
import { toast } from "@/composables/useToast";
import AppButton from "@/components/base/AppButton.vue";
import AppModal from "@/components/base/AppModal.vue";
import AdminEnableToggle from "@/components/admin/AdminEnableToggle.vue";

// 用户自定义规则：与内置模板并存的独立引擎。
// 语义（与后端 internal/classifyorganize 保持一致，务必不要「优化」成打分制）：
//   · 规则按顺序自上而下匹配，**首个命中胜出**（不是打分/最佳匹配）；
//   · 条件分「硬条件」与「可选条件(?)」：无 ? 的硬条件必须全部满足(AND)；
//     一旦存在 ? 可选条件，则至少满足其中一个(OR)，同时硬条件仍需全部满足；
//   · 取值支持 `a,b` 表示或、`-x` 表示否定、`a-b` 表示年份闭区间、`+16` 表示强制包含。
const rules = ref<ClassifyRuleItem[]>([]);
const loading = ref(false);
const saving = ref(false);
const errorText = ref("");
const dirty = ref(false);

const editorOpen = ref(false);
const editorMode = ref<"create" | "edit">("create");
const editorID = ref(0);
const form = ref<ClassifyRuleInput>({
  media_type: "tv",
  target_path: "",
  enabled: true,
  remark: "",
  conditions: [],
});

const importOpen = ref(false);
const importText = ref("");
const importMode = ref<"replace" | "append">("replace");
const importWarnings = ref<string[]>([]);

const CONDITION_KEYS: Array<{ key: string; label: string; hint: string }> = [
  { key: "genre_ids", label: "类型 ID", hint: "TMDB genre id，多个用逗号分隔，如 16,35" },
  { key: "keywords", label: "关键词", hint: "匹配影片关键词，多个用逗号分隔" },
  { key: "include_keywords", label: "必须包含关键词", hint: "标题/别名必须包含，多个用逗号分隔" },
  { key: "original_language", label: "原始语言", hint: "如 zh, en, ja" },
  { key: "origin_country", label: "原产国家", hint: "如 CN, US, JP" },
  { key: "year", label: "年份", hint: "如 2000-2099，或单个 2024" },
  { key: "series_keywords", label: "剧集关键词", hint: "仅剧集生效" },
  { key: "series_actors", label: "剧集演员", hint: "仅剧集生效" },
];

const MEDIA_TYPES = [
  { value: "movie", label: "电影" },
  { value: "tv", label: "剧集" },
];

const sortedRules = computed(() => rules.value.slice().sort((a, b) => a.position - b.position));

function mediaLabel(value: string) {
  return MEDIA_TYPES.find((item) => item.value === value)?.label ?? value;
}

function conditionLabel(key: string) {
  return CONDITION_KEYS.find((item) => item.key === key)?.label ?? key;
}

function describeConditions(rule: ClassifyRuleItem) {
  if (!rule.conditions.length) return "无条件（兜底）";
  return rule.conditions
    .map((c) => `${c.optional ? "?" : ""}${conditionLabel(c.key)}=${c.values}`)
    .join(" 且 ");
}

async function load() {
  loading.value = true;
  errorText.value = "";
  try {
    const res = await classificationApi.listRules();
    rules.value = res.rules ?? [];
    dirty.value = false;
  } catch (err) {
    errorText.value = getApiErrorMessage(err, "读取自定义规则失败");
  } finally {
    loading.value = false;
  }
}

function openCreate() {
  editorMode.value = "create";
  editorID.value = 0;
  form.value = {
    media_type: "tv",
    target_path: "",
    enabled: true,
    remark: "",
    conditions: [{ key: "genre_ids", values: "", optional: false }],
  };
  editorOpen.value = true;
}

function openEdit(rule: ClassifyRuleItem) {
  editorMode.value = "edit";
  editorID.value = rule.id;
  form.value = {
    media_type: rule.media_type || "tv",
    target_path: rule.target_path,
    enabled: rule.enabled,
    remark: rule.remark,
    conditions: rule.conditions.length
      ? rule.conditions.map((c) => ({ ...c }))
      : [{ key: "genre_ids", values: "", optional: false }],
  };
  editorOpen.value = true;
}

function addCondition() {
  form.value.conditions.push({ key: "genre_ids", values: "", optional: false });
}

function removeCondition(index: number) {
  form.value.conditions.splice(index, 1);
}

async function save() {
  const target = form.value.target_path.trim();
  if (!target) {
    toast.error("请填写目标目录");
    return;
  }
  const conditions = form.value.conditions
    .map((c) => ({ ...c, values: c.values.trim() }))
    .filter((c) => c.values !== "");
  // 空取值条件会被后端拒绝（视为畸形取值），这里先行过滤并提示。
  if (form.value.conditions.length && !conditions.length) {
    toast.error("请至少填写一个条件的取值");
    return;
  }
  saving.value = true;
  try {
    const payload: ClassifyRuleInput = {
      media_type: form.value.media_type,
      target_path: target,
      enabled: form.value.enabled,
      remark: (form.value.remark ?? "").trim(),
      conditions,
    };
    if (editorMode.value === "create") {
      await classificationApi.createRule(payload);
      toast.success("规则已新增");
    } else {
      await classificationApi.updateRule(editorID.value, payload);
      toast.success("规则已保存");
    }
    editorOpen.value = false;
    await load();
  } catch (err) {
    toast.error(getApiErrorMessage(err, "保存规则失败"));
  } finally {
    saving.value = false;
  }
}

async function removeRule(rule: ClassifyRuleItem) {
  if (!window.confirm(`确定删除规则「${rule.target_path}」吗？`)) return;
  try {
    await classificationApi.deleteRule(rule.id);
    toast.success("规则已删除");
    await load();
  } catch (err) {
    toast.error(getApiErrorMessage(err, "删除规则失败"));
  }
}

async function toggleRule(rule: ClassifyRuleItem, next: boolean) {
  const previous = rule.enabled;
  rule.enabled = next;
  try {
    await classificationApi.updateRule(rule.id, {
      media_type: rule.media_type,
      target_path: rule.target_path,
      enabled: next,
      remark: rule.remark,
      conditions: rule.conditions,
    });
    dirty.value = true;
  } catch (err) {
    rule.enabled = previous;
    toast.error(getApiErrorMessage(err, "切换规则状态失败"));
  }
}

// 顺序即优先级：上移/下移后把完整 ID 序列回传，由后端重排 position。
async function move(index: number, delta: number) {
  const list = sortedRules.value.map((r) => r.id);
  const next = index + delta;
  if (next < 0 || next >= list.length) return;
  [list[index], list[next]] = [list[next], list[index]];
  try {
    const res = await classificationApi.reorderRules(list);
    rules.value = res.rules ?? rules.value;
    dirty.value = true;
  } catch (err) {
    toast.error(getApiErrorMessage(err, "调整顺序失败"));
  }
}

function openImport() {
  importText.value = "";
  importMode.value = "replace";
  importWarnings.value = [];
  importOpen.value = true;
}

async function runImport() {
  if (!importText.value.trim()) {
    toast.error("请粘贴 YAML 内容");
    return;
  }
  saving.value = true;
  try {
    const res = await classificationApi.importRules({
      yaml: importText.value,
      mode: importMode.value,
    });
    rules.value = res.rules ?? [];
    importWarnings.value = res.warnings ?? [];
    dirty.value = true;
    if (importWarnings.value.length) {
      toast.success(`已导入 ${res.imported} 条规则，有 ${importWarnings.value.length} 条提示`);
    } else {
      toast.success(`已导入 ${res.imported} 条规则`);
      importOpen.value = false;
    }
  } catch (err) {
    toast.error(getApiErrorMessage(err, "导入失败"));
  } finally {
    saving.value = false;
  }
}

async function runExport() {
  try {
    const res = await classificationApi.exportRules();
    const blob = new Blob([res.yaml ?? ""], { type: "text/yaml;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "classification-rules.yaml";
    a.click();
    URL.revokeObjectURL(url);
    toast.success("已导出 YAML");
  } catch (err) {
    toast.error(getApiErrorMessage(err, "导出失败"));
  }
}

onMounted(load);
</script>

<template>
  <section class="cls-rules">
    <header class="cls-rules__head">
      <div>
        <strong>自定义分类规则</strong>
        <p>
          规则按顺序自上而下匹配，<b>首个命中胜出</b>；未命中任何自定义规则时回落到上方所选模板。
          条件中不带 <code>?</code> 的为硬条件（需全部满足），带 <code>?</code> 的为可选条件（满足其一即可）。
        </p>
      </div>
      <div class="cls-rules__ops">
        <AppButton size="sm" variant="secondary" :disabled="loading" @click="load">刷新</AppButton>
        <AppButton size="sm" variant="secondary" :disabled="saving" @click="openImport">导入 YAML</AppButton>
        <AppButton size="sm" variant="secondary" :disabled="loading" @click="runExport">导出 YAML</AppButton>
        <AppButton size="sm" @click="openCreate">新增规则</AppButton>
      </div>
    </header>

    <p v-if="errorText" class="cls-rules__error">{{ errorText }}</p>

    <p v-if="loading" class="cls-rules__empty">正在读取规则…</p>
    <p v-else-if="!sortedRules.length" class="cls-rules__empty">
      还没有自定义规则。新增规则或导入一份 YAML 规则文件后，目录整理会优先按这些规则分类。
    </p>

    <ol v-else class="cls-rules__list">
      <li v-for="(rule, index) in sortedRules" :key="rule.id" :class="{ off: !rule.enabled }">
        <div class="cls-rules__order">
          <span>{{ index + 1 }}</span>
          <button type="button" title="上移" :disabled="index === 0" @click="move(index, -1)">↑</button>
          <button
            type="button"
            title="下移"
            :disabled="index === sortedRules.length - 1"
            @click="move(index, 1)"
          >↓</button>
        </div>
        <div class="cls-rules__body">
          <div class="cls-rules__title">
            <span class="cls-rules__badge">{{ mediaLabel(rule.media_type) }}</span>
            <strong>{{ rule.target_path }}</strong>
          </div>
          <p class="cls-rules__cond">{{ describeConditions(rule) }}</p>
          <p v-if="rule.remark" class="cls-rules__remark">{{ rule.remark }}</p>
        </div>
        <div class="cls-rules__actions">
          <AdminEnableToggle
            :enabled="rule.enabled"
            :disabled="saving"
            @enable="(v: boolean) => toggleRule(rule, v)"
          />
          <AppButton size="sm" variant="secondary" @click="openEdit(rule)">编辑</AppButton>
          <AppButton size="sm" variant="danger" @click="removeRule(rule)">删除</AppButton>
        </div>
      </li>
    </ol>

    <!-- 规则编辑 -->
    <AppModal
      :open="editorOpen"
      size="md"
      :title="editorMode === 'create' ? '新增分类规则' : '编辑分类规则'"
      @close="editorOpen = false"
    >
      <div class="cls-rules__form">
        <label>
          <span>媒体类型</span>
          <select v-model="form.media_type">
            <option v-for="item in MEDIA_TYPES" :key="item.value" :value="item.value">
              {{ item.label }}
            </option>
          </select>
        </label>
        <label>
          <span>目标目录</span>
          <input v-model="form.target_path" type="text" placeholder="如 剧集/日番" />
        </label>
        <label>
          <span>备注（可选）</span>
          <input v-model="form.remark" type="text" placeholder="便于自己识别这条规则的用途" />
        </label>
        <label class="cls-rules__checkbox">
          <input v-model="form.enabled" type="checkbox" />
          <span>启用该规则</span>
        </label>

        <div class="cls-rules__condhead">
          <span>匹配条件</span>
          <AppButton size="sm" variant="secondary" @click="addCondition">添加条件</AppButton>
        </div>
        <div v-for="(cond, index) in form.conditions" :key="index" class="cls-rules__condrow">
          <select v-model="cond.key">
            <option v-for="item in CONDITION_KEYS" :key="item.key" :value="item.key">
              {{ item.label }}
            </option>
          </select>
          <input v-model="cond.values" type="text" placeholder="取值，如 16,35 或 2000-2099" />
          <label class="cls-rules__opt" title="可选条件：满足其一即可">
            <input v-model="cond.optional" type="checkbox" />
            <span>可选 ?</span>
          </label>
          <button type="button" class="cls-rules__del" title="移除条件" @click="removeCondition(index)">×</button>
        </div>
        <p class="cls-rules__hint">
          取值语法：<code>a,b</code> 表示或；<code>-x</code> 表示否定；<code>2000-2099</code> 表示年份闭区间；
          <code>+16</code> 表示强制包含。全部条件留空即为兜底规则。
        </p>
      </div>
      <template #footer>
        <AppButton variant="secondary" :disabled="saving" @click="editorOpen = false">取消</AppButton>
        <AppButton :disabled="saving" @click="save">{{ saving ? "保存中…" : "保存" }}</AppButton>
      </template>
    </AppModal>

    <!-- YAML 导入 -->
    <AppModal :open="importOpen" size="md" title="导入 YAML 规则" @close="importOpen = false">
      <div class="cls-rules__form">
        <label>
          <span>导入方式</span>
          <select v-model="importMode">
            <option value="replace">替换现有规则</option>
            <option value="append">追加到现有规则</option>
          </select>
        </label>
        <label>
          <span>YAML 内容</span>
          <textarea v-model="importText" rows="12" placeholder="movie:&#10;  电影/动作片:&#10;    genre_ids: 28"></textarea>
        </label>
        <div v-if="importWarnings.length" class="cls-rules__warn">
          <strong>导入提示</strong>
          <ul>
            <li v-for="(w, i) in importWarnings" :key="i">{{ w }}</li>
          </ul>
        </div>
      </div>
      <template #footer>
        <AppButton variant="secondary" :disabled="saving" @click="importOpen = false">关闭</AppButton>
        <AppButton :disabled="saving" @click="runImport">{{ saving ? "导入中…" : "导入" }}</AppButton>
      </template>
    </AppModal>
  </section>
</template>

<style scoped>
.cls-rules { margin-top: 14px; padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-md); background: var(--surface-sunken); }
.cls-rules__head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.cls-rules__head strong { font-size: 13px; font-weight: 600; }
.cls-rules__head p { margin: 5px 0 0; max-width: 640px; color: var(--text-muted); font-size: 12.5px; line-height: 1.6; }
.cls-rules__head code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; background: var(--surface); border-radius: var(--radius-xs); padding: 1px 5px; }
.cls-rules__ops { display: flex; gap: 6px; flex-wrap: wrap; }
.cls-rules__error { margin: 10px 0 0; color: var(--danger, #d64545); font-size: 12.5px; }
.cls-rules__empty { margin: 10px 0 0; color: var(--text-muted); font-size: 12.5px; }
.cls-rules__list { display: grid; gap: 8px; margin: 10px 0 0; padding: 0; list-style: none; }
.cls-rules__list li { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; gap: 10px; align-items: center; padding: 9px 10px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--surface); }
.cls-rules__list li.off { opacity: .55; }
.cls-rules__order { display: flex; align-items: center; gap: 3px; }
.cls-rules__order span { min-width: 18px; color: var(--text-muted); font-size: 12px; text-align: right; }
.cls-rules__order button { width: 22px; height: 22px; padding: 0; border: 1px solid var(--border); border-radius: var(--radius-xs); background: var(--surface); color: var(--text); cursor: pointer; }
.cls-rules__order button:disabled { opacity: .35; cursor: default; }
.cls-rules__title { display: flex; align-items: center; gap: 7px; }
.cls-rules__title strong { overflow-wrap: anywhere; font-size: 13px; font-weight: 600; }
.cls-rules__badge { flex: none; padding: 1px 6px; border-radius: var(--radius-xs); background: var(--surface-sunken); color: var(--text-muted); font-size: 11.5px; }
.cls-rules__cond { margin: 4px 0 0; color: var(--text-regular); font-size: 12px; overflow-wrap: anywhere; }
.cls-rules__remark { margin: 3px 0 0; color: var(--text-muted); font-size: 12px; }
.cls-rules__actions { display: flex; align-items: center; gap: 6px; }
.cls-rules__form { display: grid; gap: 10px; }
.cls-rules__form label { display: grid; gap: 5px; font-size: 12.5px; }
.cls-rules__form input[type="text"], .cls-rules__form select, .cls-rules__form textarea { box-sizing: border-box; width: 100%; border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 8px 9px; background: var(--surface); color: var(--text); font-size: 13px; }
.cls-rules__form textarea { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; resize: vertical; }
.cls-rules__checkbox { display: flex !important; align-items: center; gap: 6px; }
.cls-rules__checkbox input { width: auto; }
.cls-rules__condhead { display: flex; align-items: center; justify-content: space-between; font-size: 12.5px; }
.cls-rules__condrow { display: grid; grid-template-columns: 150px minmax(0, 1fr) auto auto; gap: 7px; align-items: center; }
.cls-rules__opt { display: flex !important; align-items: center; gap: 4px; white-space: nowrap; }
.cls-rules__opt input { width: auto; }
.cls-rules__del { width: 26px; height: 26px; padding: 0; border: 1px solid var(--border); border-radius: var(--radius-xs); background: var(--surface); color: var(--text-muted); font-size: 15px; line-height: 1; cursor: pointer; }
.cls-rules__hint { margin: 0; color: var(--text-muted); font-size: 12px; line-height: 1.6; }
.cls-rules__hint code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; background: var(--surface); border-radius: var(--radius-xs); padding: 1px 5px; }
.cls-rules__warn { padding: 9px 10px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--surface); font-size: 12px; }
.cls-rules__warn ul { margin: 5px 0 0; padding-left: 18px; color: var(--text-muted); }
@media (max-width: 760px) {
  .cls-rules__list li { grid-template-columns: 1fr; }
  .cls-rules__condrow { grid-template-columns: 1fr; }
}
</style>
