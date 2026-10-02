<script setup lang="ts">
/**
 * 视觉过滤规则编辑器。
 *
 * ★ 这里编辑的三个词表是**真正拦截转存**的字段，不是装饰性配置：
 *   后端 internal/discover/discovery/subscriptions.go 的
 *   planAndTransferRuleCandidates 会读取 match.message_keywords /
 *   match.must_contain / match.must_not_contain，命中排除条件就把候选标记为
 *   skipped 并 continue，该资源不会被转存。规则因此不是"写着好看"的。
 *
 * 三条词表的语义（顺序即优先级，UI 里必须写清楚，否则用户只能猜）：
 *   message_keywords  任意命中即通过（any-of 白名单闸门）
 *   must_contain      全部命中才通过（all-of 必需项，缺一即拦）
 *   must_not_contain  任意命中即拦截（any-of 排除项）
 *   空列表 = 不做该项过滤（不是"全部拦截"）
 *
 * 预览按钮会把用户粘贴的一批标题发给后端 /subscriptions/preview，
 * 后端复用生产路径的同一套匹配函数，返回逐条"放行 / 拦截 + 原因"。
 */
import { computed, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  previewSubscriptionMatch,
  type RulePreviewResult,
  type SubscriptionRuleMatch,
} from "@/api/discovery";
import AppButton from "@/components/base/AppButton.vue";
import AppIconButton from "@/components/base/AppIconButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import { toast } from "@/composables/useToast";

const props = defineProps<{
  /** 三条词表（v-model）。父组件负责在保存时写进规则的 match 字段 */
  modelValue: SubscriptionRuleMatch;
  /** 保存中：禁用按钮，避免重复提交 */
  saving?: boolean;
  /** 是否显示"保存规则"按钮（只读展示场景可关掉） */
  showSave?: boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [SubscriptionRuleMatch];
  save: [];
}>();

const saving = computed(() => props.saving === true);
const showSave = computed(() => props.showSave !== false);

type ListKey = "message_keywords" | "must_contain" | "must_not_contain";

interface ListMeta {
  key: ListKey;
  title: string;
  /** 语义说明：必须让用户看懂"这一列怎么判定" */
  hint: string;
  /** 判定类型徽标文案 */
  badge: string;
  placeholder: string;
}

const LISTS: ListMeta[] = [
  {
    key: "message_keywords",
    title: "正文关键词",
    hint: "标题或备注命中其中任意一个词即可通过；一个都没有命中会被拦下。留空表示不按关键词过滤。",
    badge: "任一命中",
    placeholder: "例如：庆余年",
  },
  {
    key: "must_contain",
    title: "必须包含",
    hint: "这里列出的词必须**全部**出现在标题或备注里，缺少任意一个都会被拦下。留空表示不做要求。",
    badge: "全部命中",
    placeholder: "例如：4K",
  },
  {
    key: "must_not_contain",
    title: "必须排除",
    hint: "标题或备注命中其中任意一个词就会被拦下，适合排除预告、花絮、抢先版等。留空表示不排除任何内容。",
    badge: "任一命中即拦",
    placeholder: "例如：预告",
  },
];

/** 归一化：保证传入对象缺字段时也能安全渲染 */
function normalize(v: SubscriptionRuleMatch | undefined | null): SubscriptionRuleMatch {
  return {
    message_keywords: [...(v?.message_keywords ?? [])],
    must_contain: [...(v?.must_contain ?? [])],
    must_not_contain: [...(v?.must_not_contain ?? [])],
  };
}

/** 内部编辑副本，改动即时向上同步（v-model） */
const draft = ref<SubscriptionRuleMatch>(normalize(props.modelValue));

function commit(next: SubscriptionRuleMatch) {
  draft.value = next;
  emit("update:modelValue", normalize(next));
}

function list(key: ListKey): string[] {
  return draft.value[key] ?? [];
}

/** 三条词表是否全空 —— 全空等于不做过滤，UI 要明确提示 */
const noFilter = computed(
  () =>
    !draft.value.message_keywords.length &&
    !draft.value.must_contain.length &&
    !draft.value.must_not_contain.length,
);

// ------------------------------ 增删改排 ------------------------------

/** 每行一个输入框，这里记录"正在输入但还没提交"的草稿，回车/失焦才入列 */
const entryDraft = ref<Record<ListKey, string>>({
  message_keywords: "",
  must_contain: "",
  must_not_contain: "",
});

function addEntry(key: ListKey) {
  const raw = (entryDraft.value[key] ?? "").trim();
  if (!raw) {
    toast.warning("请先输入要添加的词");
    return;
  }
  if (list(key).includes(raw)) {
    toast.warning(`「${raw}」已经在列表里了`);
    return;
  }
  commit({ ...draft.value, [key]: [...list(key), raw] });
  entryDraft.value = { ...entryDraft.value, [key]: "" };
}

function removeEntry(key: ListKey, index: number) {
  const next = [...list(key)];
  next.splice(index, 1);
  commit({ ...draft.value, [key]: next });
}

/** 上移/下移：改变匹配优先级（must_contain 缺词时按顺序报第一条） */
function moveEntry(key: ListKey, index: number, delta: number) {
  const next = [...list(key)];
  const target = index + delta;
  if (target < 0 || target >= next.length) return;
  const [moved] = next.splice(index, 1);
  next.splice(target, 0, moved);
  commit({ ...draft.value, [key]: next });
}

// ------------------------------ 批量粘贴 ------------------------------

const pasteOpen = ref(false);
const pasteText = ref("");
const previewLoading = ref(false);
const previewResults = ref<RulePreviewResult[]>([]);
const previewSummary = ref<{ total: number; blocked: number; passed: number } | null>(null);

/**
 * 把粘贴内容按行拆成标题；支持"标题<TAB>备注"两列写法，
 * 因为生产匹配文本是「标题 + 换行 + 备注」，分开贴才能测出备注里的词。
 */
function parsePaste(text: string): Array<{ title: string; remark: string }> {
  const out: Array<{ title: string; remark: string }> = [];
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const tab = trimmed.indexOf("\t");
    if (tab >= 0) {
      const title = trimmed.slice(0, tab).trim();
      const remark = trimmed.slice(tab + 1).trim();
      if (title) out.push({ title, remark });
      continue;
    }
    out.push({ title: trimmed, remark: "" });
  }
  return out;
}

const pasteCount = computed(() => parsePaste(pasteText.value).length);

async function runPreview() {
  const titles = parsePaste(pasteText.value);
  if (!titles.length) {
    toast.warning("请先粘贴要测试的标题，每行一条");
    return;
  }
  previewLoading.value = true;
  previewResults.value = [];
  previewSummary.value = null;
  try {
    const res = await previewSubscriptionMatch({
      message_keywords: draft.value.message_keywords,
      must_contain: draft.value.must_contain,
      must_not_contain: draft.value.must_not_contain,
      titles,
    });
    previewResults.value = res.items ?? [];
    previewSummary.value = {
      total: res.total ?? 0,
      blocked: res.blocked_count ?? 0,
      passed: res.passed_count ?? 0,
    };
  } catch (e) {
    toast.error(getApiErrorMessage(e, "预览失败，请检查后端是否在运行"));
  } finally {
    previewLoading.value = false;
  }
}

function fillExample() {
  pasteText.value = [
    "庆余年 第二季 4K 国语 中字",
    "庆余年 第二季 4K 预告",
    "庆余年 第二季 1080P 国语",
    "【抢先版】庆余年 第二季 4K",
    "某综艺 2024 4K",
  ].join("\n");
}

function handleSave() {
  emit("save");
}
</script>

<template>
  <div class="vfr">
    <!-- 语义说明：让用户不必猜哪个列表说了算 -->
    <div class="vfr__intro">
      <p class="vfr__lead">
        这里的词表会真正拦截自动转存：候选资源的
        <strong>标题和备注</strong>先按下面的规则判定，被拦下的资源不会转存到网盘，
        并在候选明细里标记为「已跳过」。
      </p>
      <p class="vfr__order">
        判定顺序：<span class="vfr__pill">正文关键词</span> →
        <span class="vfr__pill">必须包含</span> →
        <span class="vfr__pill">必须排除</span>，先触发的那一条决定拦截原因。
      </p>
      <p v-if="noFilter" class="vfr__nofilter">
        当前三条词表都是空的 —— 不会过滤任何资源，所有搜到的候选都会被考虑转存。
      </p>
    </div>

    <div class="vfr__lists">
      <section v-for="meta in LISTS" :key="meta.key" class="vfr__list">
        <header class="vfr__list-head">
          <h4 class="vfr__list-title">
            {{ meta.title }}
            <span class="vfr__badge">{{ meta.badge }}</span>
            <span class="vfr__count">{{ list(meta.key).length }} 项</span>
          </h4>
          <p class="vfr__hint">{{ meta.hint }}</p>
        </header>

        <ul v-if="list(meta.key).length" class="vfr__entries">
          <li v-for="(entry, idx) in list(meta.key)" :key="`${meta.key}-${entry}-${idx}`" class="vfr__entry">
            <span class="vfr__entry-index">{{ idx + 1 }}</span>
            <span class="vfr__entry-text" :title="entry">{{ entry }}</span>
            <span class="vfr__entry-ops">
              <AppIconButton
                icon="chevron-up"
                label="上移"
                size="xs"
                :disabled="idx === 0"
                @click="moveEntry(meta.key, idx, -1)"
              />
              <AppIconButton
                icon="chevron-down"
                label="下移"
                size="xs"
                :disabled="idx === list(meta.key).length - 1"
                @click="moveEntry(meta.key, idx, 1)"
              />
              <AppIconButton
                icon="trash"
                label="删除"
                variant="danger"
                size="xs"
                @click="removeEntry(meta.key, idx)"
              />
            </span>
          </li>
        </ul>
        <p v-else class="vfr__empty">暂无词条，留空表示不按该项过滤。</p>

        <div class="vfr__add">
          <AppInput
            v-model="entryDraft[meta.key]"
            :placeholder="meta.placeholder"
            @keyup.enter="addEntry(meta.key)"
          />
          <AppButton type="button" variant="secondary" size="sm" @click="addEntry(meta.key)">
            添加
          </AppButton>
        </div>
      </section>
    </div>

    <!-- 批量粘贴预览：保存前先看会拦掉什么 -->
    <section class="vfr__preview">
      <header class="vfr__preview-head">
        <div>
          <h4 class="vfr__list-title">批量测试</h4>
          <p class="vfr__hint">
            粘贴一批标题（每行一条）预览判定结果。需要连备注一起测时，用 Tab 分隔「标题」和「备注」两列。
          </p>
        </div>
        <div class="vfr__preview-ops">
          <AppButton type="button" variant="ghost" size="sm" @click="fillExample">填入示例</AppButton>
          <AppButton
            type="button"
            variant="secondary"
            size="sm"
            :disabled="pasteOpen && !pasteOpen"
            @click="pasteOpen = !pasteOpen"
          >
            {{ pasteOpen ? "收起" : "展开" }}
          </AppButton>
        </div>
      </header>

      <div v-if="pasteOpen" class="vfr__paste">
        <textarea
          v-model="pasteText"
          class="vfr__textarea"
          rows="7"
          placeholder="庆余年 第二季 4K 国语&#10;庆余年 第二季 4K 预告&#10;某综艺 2024 4K"
        ></textarea>
        <div class="vfr__paste-ops">
          <span class="vfr__paste-count">已识别 {{ pasteCount }} 条标题</span>
          <AppButton
            type="button"
            variant="primary"
            size="sm"
            :disabled="previewLoading || pasteCount === 0"
            @click="runPreview"
          >
            {{ previewLoading ? "检测中…" : "检测命中结果" }}
          </AppButton>
        </div>
      </div>

      <AppStateBlock v-if="previewLoading" message="正在按生产规则判定…" loading min-height="90px" />

      <div v-else-if="previewSummary" class="vfr__results">
        <div class="vfr__summary">
          <span class="vfr__summary-item vfr__summary-item--pass">
            通过 {{ previewSummary.passed }}
          </span>
          <span class="vfr__summary-item vfr__summary-item--block">
            被拦截 {{ previewSummary.blocked }}
          </span>
          <span class="vfr__summary-item">共 {{ previewSummary.total }} 条</span>
        </div>
        <ul class="vfr__result-list">
          <li
            v-for="(item, idx) in previewResults"
            :key="`res-${idx}`"
            class="vfr__result"
            :class="item.blocked ? 'is-blocked' : 'is-passed'"
          >
            <span class="vfr__result-icon">{{ item.blocked ? "✕" : "✓" }}</span>
            <span class="vfr__result-title" :title="item.title">{{ item.title }}</span>
            <span v-if="item.blocked" class="vfr__result-reason">{{ item.reason }}</span>
            <span v-else class="vfr__result-reason vfr__result-reason--pass">会正常参与转存</span>
          </li>
        </ul>
      </div>
    </section>

    <footer v-if="showSave" class="vfr__footer">
      <AppButton type="button" variant="primary" :disabled="saving" @click="handleSave">
        {{ saving ? "保存中…" : "保存规则" }}
      </AppButton>
    </footer>
  </div>
</template>

<style scoped>
.vfr {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.vfr__intro {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface-2, var(--surface));
}
.vfr__lead {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
  color: var(--text-regular);
}
.vfr__order {
  margin: 0;
  font-size: 12px;
  color: var(--text-muted, var(--text-regular));
}
.vfr__pill {
  display: inline-block;
  padding: 1px 8px;
  margin: 0 2px;
  border-radius: 999px;
  background: var(--surface);
  border: 1px solid var(--border);
  font-size: 12px;
}
.vfr__nofilter {
  margin: 0;
  font-size: 12px;
  color: var(--warning, #d08700);
}
.vfr__lists {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 14px;
}
.vfr__list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface);
}
.vfr__list-head {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.vfr__list-title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  font-size: 13px;
  font-weight: 600;
}
.vfr__badge {
  padding: 1px 7px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 500;
  background: var(--brand-soft, var(--surface-2, var(--surface)));
  color: var(--brand);
  border: 1px solid var(--border);
}
.vfr__count {
  margin-left: auto;
  font-size: 11px;
  font-weight: 400;
  color: var(--text-muted, var(--text-regular));
}
.vfr__hint {
  margin: 0;
  font-size: 12px;
  line-height: 1.55;
  color: var(--text-muted, var(--text-regular));
}
.vfr__entries {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 220px;
  overflow-y: auto;
}
.vfr__entry {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 6px;
  border-radius: var(--radius-sm);
  background: var(--surface-2, var(--surface));
  font-size: 13px;
}
.vfr__entry-index {
  min-width: 16px;
  font-size: 11px;
  color: var(--text-muted, var(--text-regular));
}
.vfr__entry-text {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.vfr__entry-ops {
  display: inline-flex;
  gap: 2px;
}
.vfr__empty {
  margin: 0;
  padding: 6px 0;
  font-size: 12px;
  color: var(--text-muted, var(--text-regular));
}
.vfr__add {
  display: flex;
  gap: 6px;
}
.vfr__preview {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
}
.vfr__preview-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}
.vfr__preview-ops {
  display: inline-flex;
  gap: 6px;
  flex-shrink: 0;
}
.vfr__paste {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.vfr__textarea {
  width: 100%;
  padding: 9px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface);
  color: var(--text);
  font-family: inherit;
  font-size: 13px;
  resize: vertical;
}
.vfr__textarea:focus {
  outline: none;
  border-color: var(--brand);
}
.vfr__paste-ops {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.vfr__paste-count {
  font-size: 12px;
  color: var(--text-muted, var(--text-regular));
}
.vfr__results {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.vfr__summary {
  display: flex;
  gap: 12px;
  font-size: 12px;
}
.vfr__summary-item--pass {
  color: var(--success, #16a34a);
}
.vfr__summary-item--block {
  color: var(--danger);
}
.vfr__result-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 260px;
  overflow-y: auto;
}
.vfr__result {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 8px;
  border-radius: var(--radius-sm);
  font-size: 13px;
  border-left: 3px solid transparent;
}
.vfr__result.is-passed {
  background: var(--surface-2, var(--surface));
  border-left-color: var(--success, #16a34a);
}
.vfr__result.is-blocked {
  background: var(--surface-2, var(--surface));
  border-left-color: var(--danger);
}
.vfr__result-icon {
  width: 14px;
  text-align: center;
  font-weight: 700;
}
.vfr__result.is-passed .vfr__result-icon {
  color: var(--success, #16a34a);
}
.vfr__result.is-blocked .vfr__result-icon {
  color: var(--danger);
}
.vfr__result-title {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.vfr__result-reason {
  flex-shrink: 0;
  font-size: 12px;
  color: var(--danger);
}
.vfr__result-reason--pass {
  color: var(--text-muted, var(--text-regular));
}
.vfr__footer {
  display: flex;
  justify-content: flex-end;
}
</style>
