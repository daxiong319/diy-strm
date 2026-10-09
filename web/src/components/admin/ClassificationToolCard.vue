<script setup lang="ts">
import { containsQuery } from "@/utils/format";
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  classificationApi,
  CLASSIFICATION_DEGRADED_REASONS,
  type ClassificationConfig,
  type ClassificationPreviewResult,
  type ClassificationRule,
  type ClassificationSeriesRule,
  type ClassificationTemplate,
  type ClassificationTemplateKind,
  type ClassificationTMDBDetail,
} from "@/api/cloudTools";
import { toast } from "@/composables/useToast";
import AppButton from "@/components/base/AppButton.vue";
import AppModal from "@/components/base/AppModal.vue";
import ToolCard from "@/components/admin/ToolCard.vue";
import SettingsHelpTooltip from "@/components/admin/SettingsHelpTooltip.vue";
import ClassificationRulesPanel from "@/components/admin/ClassificationRulesPanel.vue";

const props = withDefaults(defineProps<{ searchQuery?: string }>(), { searchQuery: "" });

const templateMeta: Array<{ kind: ClassificationTemplateKind; name: string; desc: string }> = [
  { kind: "media", name: "内置模板一", desc: "仅按 电影 / 剧集分类" },
  { kind: "region", name: "内置模板二", desc: "按原产国家二级分类" },
  { kind: "genre", name: "内置模板三", desc: "按影片类型二级分类" },
  { kind: "custom", name: "自定义模板", desc: "自由组合目录层级与匹配条件" },
];

const emptyConfig = (): ClassificationConfig => ({
  version: 1,
  enabled: false,
  selected_template: "media",
  templates: [],
  series: [],
});

const config = ref<ClassificationConfig>(emptyConfig());
const draft = ref<ClassificationConfig>(emptyConfig());
const open = ref(false);
const helpOpen = ref(false);
// 自定义规则弹窗（规则面板自带增删改/导入导出，独立于模板配置）
const rulesOpen = ref(false);
const saving = ref(false);
const detailTMDBID = ref("");
const detailMediaType = ref<"movie" | "tv">("movie");
const detailLoading = ref(false);
const detailResult = ref<ClassificationTMDBDetail | null>(null);

// 树状态：选中的节点（rootIdx = 一级下标，childIdx = -1 表示选中一级）
const selectedRootIdx = ref(-1);
const selectedChildIdx = ref(-1);
// 一级节点折叠状态（按一级下标）
const folded = ref<Record<string, boolean>>({});

// 二级/三级的折叠键拼成一个字符串：折叠状态按「哪一条二级」索引，
// 拿数字下标会撞 —— 一级、二级、三级各有自己的下标空间。
function grandFoldKey(rootIdx: number, childIdx: number) {
  return `g-${rootIdx}-${childIdx}`;
}

function cloneConfig(value: ClassificationConfig): ClassificationConfig {
  return JSON.parse(JSON.stringify(value)) as ClassificationConfig;
}

function matches(title: string) {
  return containsQuery(title, props.searchQuery);
}

function templateLabel(kind: ClassificationTemplateKind) {
  return templateMeta.find((item) => item.kind === kind)?.name ?? kind;
}

const selectedTemplate = computed(() =>
  draft.value.templates.find((item) => item.kind === draft.value.selected_template),
);

// 三级节点（T02 C-1）。childIdx = -1 表示选中的是二级本身。
const selectedGrandIdx = ref(-1);

// 当前选中的节点对象（用于右侧编辑绑定）。
// level 0/1/2 对应一级/二级/三级。三级之下不能再配第四级 —— 后端会在保存时报错，
// 与其让用户配完保存失败，不如界面上根本不提供这个入口。
const selectedNode = computed<{ rule: ClassificationRule; level: 0 | 1 | 2 } | null>(() => {
  const template = selectedTemplate.value;
  if (!template || selectedRootIdx.value < 0 || selectedRootIdx.value >= template.rules.length) return null;
  const rule = template.rules[selectedRootIdx.value];
  if (selectedChildIdx.value < 0) return { rule, level: 0 };
  const child = rule.children?.[selectedChildIdx.value];
  if (!child) return { rule, level: 0 };
  if (selectedGrandIdx.value < 0) return { rule: child, level: 1 };
  const grand = child.children?.[selectedGrandIdx.value];
  if (!grand) return { rule: child, level: 1 };
  return { rule: grand, level: 2 };
});

// 内置模板（非 custom）的一级条件只读
const editingLocked = computed(() => {
  const node = selectedNode.value;
  if (!node || node.level !== 0) return false;
  return selectedTemplate.value?.kind !== "custom";
});

// 占位符按层级给不同的例子。一级只有 type 可用（后端硬校验），
// 给个 origin_country 的例子会被保存时直接拒绝。
const conditionPlaceholder = computed(() => {
  const node = selectedNode.value;
  if (!node) return "origin_country=CN;HK";
  if (node.level === 0) return "type=movie";
  if (node.level === 2) return "year=2000-2009";
  const kind = selectedTemplate.value?.kind;
  if (kind === "region") return "origin_country=CN;HK";
  if (kind === "genre") return "genres=犯罪;悬疑";
  return "type=tv，genres=真人秀";
});

async function load() {
  try {
    config.value = await classificationApi.getConfig();
  } catch (error) {
    toast.error(getApiErrorMessage(error, "加载分类整理配置失败"));
  }
}

onMounted(load);

function openSettings() {
  draft.value = cloneConfig(config.value);
  helpOpen.value = false;
  detailResult.value = null;
  previewResult.value = null;
  previewError.value = "";
  seriesDraft.value = emptySeriesRule();
  selectFirst();
  open.value = true;
}

function selectFirst() {
  const template = selectedTemplate.value;
  if (template && template.rules.length) {
    selectedRootIdx.value = 0;
    selectedChildIdx.value = -1;
    selectedGrandIdx.value = -1;
    collapseOthers(0);
  } else {
    selectedRootIdx.value = -1;
    selectedChildIdx.value = -1;
    selectedGrandIdx.value = -1;
  }
}

function selectTemplate(kind: ClassificationTemplateKind) {
  draft.value.selected_template = kind;
  selectFirst();
}

function selectRoot(index: number) {
  selectedRootIdx.value = index;
  selectedChildIdx.value = -1;
  selectedGrandIdx.value = -1;
  collapseOthers(index);
}

function selectChild(rootIdx: number, childIdx: number) {
  selectedRootIdx.value = rootIdx;
  selectedChildIdx.value = childIdx;
  selectedGrandIdx.value = -1;
  collapseOthers(rootIdx);
}

function selectGrand(rootIdx: number, childIdx: number, grandIdx: number) {
  selectedRootIdx.value = rootIdx;
  selectedChildIdx.value = childIdx;
  selectedGrandIdx.value = grandIdx;
  collapseOthers(rootIdx);
}

// 三级默认条件给年份区间：三级最常见的用法就是「这十年拍的」。
// 给一个空条件的话，保存时不会报错（空条件规则被跳过），但用户会以为配好了。
function addGrandRule(parent: ClassificationRule, childIdx: number) {
  (parent.children ??= [])[childIdx].children ??= [];
  const kids = parent.children[childIdx].children ?? [];
  kids.push({ name: "新三级", condition: "year=2000-2009", children: [] });
  const rootIdx = selectedRootIdx.value;
  selectGrand(rootIdx, childIdx, kids.length - 1);
}

// 删除三级后修正选中。parent 是三级所在的那条二级。
function removeGrand(parent: ClassificationRule, grandIdx: number) {
  const kids = parent.children;
  if (!kids?.length) return;
  removeRule(kids, grandIdx);
  if (!kids.length) selectedGrandIdx.value = -1;
  else if (grandIdx < selectedGrandIdx.value) selectedGrandIdx.value = grandIdx;
  else if (grandIdx === selectedGrandIdx.value) selectedGrandIdx.value = kids.length - 1;
}

// 手风琴：只展开选中的一级目录，其余收起，避免列表过长
function collapseOthers(activeIndex: number) {
  const template = selectedTemplate.value;
  if (!template) return;
  // 一级收起，但清掉所有二三级折叠键 —— 否则换一个一级时，
  // 上一个一级展开过的二级会继承到新一级上（键里带着下标但语义已经变了）。
  const next: Record<string, boolean> = {};
  template.rules.forEach((_, i) => {
    next[String(i)] = i !== activeIndex;
  });
  folded.value = next;
}

function toggleFold(index: number) {
  const next = !folded.value[index];
  folded.value = { ...folded.value, [index]: next };
}

// 二三级折叠键是字符串（见 grandFoldKey），不能复用只收 number 的 toggleFold。
// Record<number, ...> 会把 "g-0-1" 静默当成另一个键，所以这里必须走字符串版。
function toggleFoldKey(key: string) {
  folded.value = { ...folded.value, [key]: !folded.value[key] };
}

function addCustomRootRule(template: ClassificationTemplate) {
  template.rules.push({ name: "新分类", condition: "type=tv", children: [] });
  selectRoot(template.rules.length - 1);
}

function addChildRule(template: ClassificationTemplate, parent: ClassificationRule, rootIdx: number) {
  const condition = template.kind === "region"
    ? "origin_country=CN"
    : template.kind === "genre"
      ? "genres=剧情"
      : template.kind === "custom"
        ? "genres=剧情"
        : "type=movie";
  (parent.children ??= []).push({ name: "新分类", condition, children: [] });
  selectChild(rootIdx, parent.children.length - 1);
}

function removeRule(rules: ClassificationRule[], index: number) {
  rules.splice(index, 1);
}

function useFallbackDirectory(rule: ClassificationRule, enabled: boolean) {
  rule.fallback_mode = enabled ? "directory" : "self";
  if (enabled && !rule.fallback_dir?.trim()) rule.fallback_dir = "其他";
}

// 删除一级后修正选中
function removeRoot(template: ClassificationTemplate, index: number) {
  const activeRoot = template.rules[selectedRootIdx.value];
  const removedRoot = template.rules[index];
  removeRule(template.rules, index);
  if (!template.rules.length) {
    selectedRootIdx.value = -1;
    selectedChildIdx.value = -1;
    selectedGrandIdx.value = -1;
    folded.value = {};
    return;
  }
  if (activeRoot && activeRoot !== removedRoot) {
    const nextIndex = template.rules.indexOf(activeRoot);
    if (nextIndex >= 0) {
      selectedRootIdx.value = nextIndex;
      collapseOthers(nextIndex);
      return;
    }
  }
  selectRoot(Math.min(index, template.rules.length - 1));
}


// 删除二级后修正选中
function removeChild(rule: ClassificationRule, childIdx: number) {
  const children = rule.children ?? [];
  const rootIdx = selectedTemplate.value?.rules.indexOf(rule) ?? -1;
  const isActiveRoot = selectedRootIdx.value === rootIdx;
  const activeChild = isActiveRoot ? children[selectedChildIdx.value] : undefined;
  const removedChild = children[childIdx];
  removeRule(rule.children ?? [], childIdx);
  if (!isActiveRoot) return;
  if (activeChild && activeChild !== removedChild) {
    selectedChildIdx.value = (rule.children ?? []).indexOf(activeChild);
    return;
  }
  selectedChildIdx.value = -1;
  selectedGrandIdx.value = -1;
}

function objectStringValues(value: unknown, key: string) {
  if (!Array.isArray(value)) return [];
  return value
    .map((item) => {
      if (typeof item === "string") return item;
      if (item && typeof item === "object") {
        const raw = (item as Record<string, unknown>)[key];
        return typeof raw === "string" ? raw : "";
      }
      return "";
    })
    .filter(Boolean);
}

const detailTitle = computed(() => {
  const detail = detailResult.value;
  if (!detail) return "";
  return String(detail.title ?? detail.name ?? `TMDB ${detail.id ?? detailTMDBID.value}`);
});

const detailConditions = computed(() => {
  const detail = detailResult.value;
  if (!detail) return [];
  const conditions: Array<{ label: string; value: string }> = [
    { label: "媒体类型", value: `type=${detail.media_type ?? detailMediaType.value}` },
  ];
  const originCountries = objectStringValues(detail.origin_country, "iso_3166_1");
  const genres = objectStringValues(detail.genres, "name");
  if (originCountries.length) conditions.push({ label: "原产地区", value: `origin_country=${originCountries.join(";")}` });
  if (genres.length) conditions.push({ label: "影片类型", value: `genres=${genres.join(";")}` });
  return conditions;
});

const detailJSON = computed(() => JSON.stringify(detailResult.value, null, 2));

async function lookupTMDBDetail() {
  const tmdbID = detailTMDBID.value.trim();
  if (!/^\d{1,10}$/.test(tmdbID) || Number(tmdbID) <= 0) {
    toast.error("请输入 1～10 位有效 TMDB ID");
    return;
  }
  detailLoading.value = true;
  detailResult.value = null;
  try {
    detailResult.value = await classificationApi.lookupTMDBDetail({
      tmdb_id: tmdbID,
      media_type: detailMediaType.value,
    });
  } catch (error) {
    toast.error(getApiErrorMessage(error, "查询 TMDB 详情失败"));
  } finally {
    detailLoading.value = false;
  }
}

async function copyCondition(value: string) {
  try {
    await navigator.clipboard.writeText(value);
    toast.success("匹配条件已复制");
  } catch {
    toast.error("复制失败，请手动选择文本");
  }
}

async function persist(next: ClassificationConfig, successMessage: string) {
  saving.value = true;
  try {
    config.value = await classificationApi.saveConfig(next);
    toast.success(successMessage);
    return true;
  } catch (error) {
    toast.error(getApiErrorMessage(error, "保存分类整理配置失败"));
    return false;
  } finally {
    saving.value = false;
  }
}

async function toggleEnabled() {
  await persist(
    { ...cloneConfig(config.value), enabled: !config.value.enabled },
    config.value.enabled ? "分类整理已停用" : "分类整理已启用",
  );
}

// ---------------------------------------------------------------------------
// 系列目录规则（T02 C-2）
// ---------------------------------------------------------------------------

const seriesDraft = ref<ClassificationSeriesRule>(emptySeriesRule());

function emptySeriesRule(): ClassificationSeriesRule {
  return {
    name: "",
    dir_name: "",
    // 留空表示不限媒体类型。这里不能写 media_type: "" ——
    // 类型是 "movie" | "tv"，空串既不匹配任何一条，也不是「未设置」。
    // 不设这个键，界面上「不限类型」选项就能正确回显。
    series_keywords: [],
    keywords: [],
    position: 0,
    remark: "",
  };
}

// 关键词在 UI 上是逗号分隔的字符串，配置里是数组。分开存两份会很麻烦：
// 用户在逗号串里删掉最后一个词时数组得跟着空掉，而空数组和「没填」必须能区分开。
const seriesRuleText = computed({
  get: () => (seriesDraft.value.series_keywords ?? []).join(", "),
  set: (value: string) => {
    seriesDraft.value.series_keywords = splitKeywords(value);
  },
});

function splitKeywords(value: string): string[] {
  return value
    .split(/[,，、;；]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

const seriesDirtyWarning = computed(() => {
  if (!seriesDraft.value.name.trim()) return "系列名称不能为空";
  if (!(seriesDraft.value.series_keywords ?? []).length && !(seriesDraft.value.keywords ?? []).length) {
    return "系列关键词不能为空：没有关键词的规则会对每部影片成立，等于把所有影片塞进同一个目录";
  }
  return "";
});

// media_type 的类型是 "movie" | "tv"，没有第三个成员表示「不限」。
// 直接让 select 绑这个字段的话，「不限类型」那一项只能写空串进去 ——
// 后端 trim 后确实也认，但配置里就多了一个不在类型里的值，
// 导出成 YAML 时还会多一行 media_type: ""。这里用一层中转把空串变回「没有这个键」。
const seriesMediaType = computed({
  get: () => seriesDraft.value.media_type ?? "",
  set: (value: string) => {
    if (value === "") delete seriesDraft.value.media_type;
    else seriesDraft.value.media_type = value as "movie" | "tv";
  },
});

function addSeriesRule() {
  seriesDraft.value = emptySeriesRule();
}

function editSeriesRule(rule: ClassificationSeriesRule) {
  seriesDraft.value = { ...emptySeriesRule(), ...rule };
}

function removeSeriesRule(index: number) {
  (draft.value.series ??= []).splice(index, 1);
}

function saveSeriesRule() {
  if (seriesDirtyWarning.value) {
    toast.error(seriesDirtyWarning.value);
    return;
  }
  const list = (draft.value.series ??= []);
  const next = { ...seriesDraft.value };
  const index = list.findIndex((item) => item.name === seriesDraft.value.name);
  if (index >= 0) list.splice(index, 1, next);
  else list.push(next);
  toast.success(index >= 0 ? "系列规则已更新" : "系列规则已添加");
}

// ---------------------------------------------------------------------------
// 分类预览（T02 C-7）
// ---------------------------------------------------------------------------
//
// 挂在这里而不是规则面板里，是因为它回答的是「这个文件会被放到哪」这个
// **整理计划**的问题，而不是「这条规则长什么样」。用户改规则时最想知道的就是
// 改完之后的落地路径，让它出现在规则编辑的同一屏比切到另一个面板有用。
// 服务端接口是纯函数：不落库、不建目录、不调网盘，可以随手调。

const previewMediaType = ref<"movie" | "tv">("movie");
const previewTMDBID = ref("");
const previewTitle = ref("");
const previewYear = ref("");
const previewResult = ref<ClassificationPreviewResult | null>(null);
const previewError = ref("");
const previewLoading = ref(false);

const previewPathSegments = computed(() => previewResult.value?.path ?? "");

const previewDegradedText = computed(() => {
  const reason = previewResult.value?.degraded_reason ?? "";
  if (!reason) return "";
  return CLASSIFICATION_DEGRADED_REASONS[reason] ?? `降级原因：${reason}`;
});

async function runPreview() {
  const year = previewYear.value.trim();
  if (year && !/^\d{1,4}$/.test(year)) {
    previewError.value = "年份只能是 1～4 位数字";
    return;
  }
  previewLoading.value = true;
  previewError.value = "";
  try {
    previewResult.value = await classificationApi.preview({
      media_type: previewMediaType.value,
      tmdb_id: previewTMDBID.value.trim(),
      title: previewTitle.value.trim(),
      year: year ? Number(year) : 0,
    });
  } catch (error) {
    previewResult.value = null;
    previewError.value = getApiErrorMessage(error, "预览失败");
  } finally {
    previewLoading.value = false;
  }
}

async function saveSettings() {
  if (await persist(cloneConfig(draft.value), "分类模板已保存")) open.value = false;
}
</script>

<template>
  <div v-show="matches('目录整理分类')">
    <ToolCard
      :enabled="config.enabled"
      name="目录整理分类"
      driver="移动整理 · 按模板生成分类目录"
      logo-src="/logos/classification.png"
      logo-alt="目录整理分类"
      :stat-value="templateLabel(config.selected_template)"
      :compact-stat="true"
    >
      <template #toggle>
        <button
          class="check-toggle"
          type="button"
          :class="{ on: config.enabled }"
          :aria-label="config.enabled ? '停用分类整理' : '启用分类整理'"
          :disabled="saving"
          title="启用 / 停用"
          @click="toggleEnabled"
        >
          <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3.5 8.5 6.5 11.5 12.5 4.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
        </button>
      </template>
      移动整理时按所选模板放入分类目录；无法识别影视类型时放入目标根目录，本地重命名模式不受影响。
      <template #actions>
        <AppButton size="sm" variant="secondary" :disabled="saving" @click="rulesOpen = true">自定义规则</AppButton>
        <AppButton size="sm" variant="secondary" :disabled="saving" @click="openSettings">分类设置</AppButton>
      </template>
    </ToolCard>

    <AppModal :open="rulesOpen" size="lg" title="自定义分类规则" @close="rulesOpen = false">
      <ClassificationRulesPanel />
      <template #footer>
        <AppButton variant="secondary" @click="rulesOpen = false">关闭</AppButton>
      </template>
    </AppModal>

    <AppModal :open="open" size="lg" title="请选择分类模板" footer-divider body-flush @close="open = false">
      <div class="classification-template-tabs">
          <button
            v-for="item in templateMeta"
            :key="item.kind"
            type="button"
            class="classification-template-tab"
            :class="{ active: draft.selected_template === item.kind }"
            @click="selectTemplate(item.kind)"
          >
            <strong>{{ item.name }}</strong><span>{{ item.desc }}</span>
          </button>
        </div>

        <!-- 文件树 + 右侧编辑 -->
        <div v-if="selectedTemplate" class="cls-workspace">
          <!-- 左侧目录树 -->
          <aside class="cls-tree">
            <div class="cls-tree__cap">分类目录</div>
            <div class="cls-tree__list">
              <template v-for="(rule, index) in selectedTemplate.rules" :key="`root-${index}`">
                <div
                  class="cls-tn"
                  :class="{ active: selectedRootIdx === index && selectedChildIdx < 0 }"
                  @click="selectRoot(index)"
                >
                  <span
                    class="cls-tn__arrow"
                    :class="{ open: !folded[index], leaf: !(rule.children?.length) }"
                    @click.stop="toggleFold(index)"
                  >▶</span>
                  <span class="cls-tn__fic">{{ folded[index] ? "📁" : "📂" }}</span>
                  <b>{{ rule.name }}</b>
                  <button
                    v-if="selectedTemplate.kind !== 'media'"
                    type="button"
                    class="cls-tn__add"
                    title="添加二级分类"
                    @click.stop="addChildRule(selectedTemplate, rule, index)"
                  >＋</button>
                  <button
                    v-if="selectedTemplate.kind === 'custom'"
                    type="button"
                    class="cls-tn__del"
                    :disabled="selectedTemplate.rules.length === 1"
                    title="删除"
                    @click.stop="removeRoot(selectedTemplate, index)"
                  >×</button>
                </div>
                <div v-if="!folded[index]" class="cls-tn__children">
                  <div v-for="(child, childIndex) in (rule.children ?? [])" :key="`child-${index}-${childIndex}`">
                    <div
                      class="cls-tn cls-tn--child"
                      :class="{ active: selectedRootIdx === index && selectedChildIdx === childIndex && selectedGrandIdx < 0 }"
                      @click="selectChild(index, childIndex)"
                    >
                      <span
                        class="cls-tn__arrow"
                        :class="{ open: !folded[`g-${index}-${childIndex}`], leaf: !(child.children?.length) }"
                        @click.stop="toggleFoldKey(grandFoldKey(index, childIndex))"
                      >▶</span>
                      <span class="cls-tn__fic">📂</span>
                      <b>{{ child.name }}</b>
                      <button
                        type="button"
                        class="cls-tn__add"
                        :title="selectedTemplate.kind === 'media' ? '内置模板一没有二级分类' : '添加三级分类'"
                        :disabled="selectedTemplate.kind === 'media'"
                        @click.stop="addGrandRule(rule, childIndex)"
                      >＋</button>
                      <button type="button" class="cls-tn__del" title="删除" @click.stop="removeChild(rule, childIndex)">×</button>
                    </div>
                    <!-- 三级必须嵌在自己的二级里。用同级 v-for + v-show 的话，
                         折叠某一个二级会把整层三级全藏掉 —— 三级既不属于被折叠的
                         那一条，也无法按它自己的开合状态显示。 -->
                    <div v-if="!folded[`g-${index}-${childIndex}`]" class="cls-tn__children">
                      <div
                        v-for="(grand, grandIndex) in (child.children ?? [])"
                        :key="`grand-${index}-${childIndex}-${grandIndex}`"
                        class="cls-tn cls-tn--grand"
                        :class="{ active: selectedRootIdx === index && selectedChildIdx === childIndex && selectedGrandIdx === grandIndex }"
                        @click="selectGrand(index, childIndex, grandIndex)"
                      >
                        <span class="cls-tn__arrow leaf">▶</span>
                        <span class="cls-tn__fic">📄</span>
                        <b>{{ grand.name }}</b>
                        <button type="button" class="cls-tn__del" title="删除" @click.stop="removeGrand(child, grandIndex)">×</button>
                      </div>
                    </div>
                  </div>
                </div>
              </template>
              <div v-if="!selectedTemplate.rules.length" class="cls-tree__empty">还没有分类目录</div>
            </div>
            <button
              v-if="selectedTemplate.kind === 'custom'"
              type="button"
              class="cls-tree__add"
              @click="addCustomRootRule(selectedTemplate)"
            >＋ 添加一级分类</button>
          </aside>

          <!-- 右侧编辑 -->
          <section class="cls-edit">
            <template v-if="selectedNode">
              <div class="cls-edit__head">
                <div class="cls-edit__title">
                  <span class="cls-edit__fic">{{ ["📁", "📂", "📄"][selectedNode.level] }}</span>
                  <span>{{ selectedNode.rule.name || "未命名" }}</span>
                  <span class="cls-edit__lvl">{{ ["一级", "二级", "三级"][selectedNode.level] }}</span>
                </div>
              </div>

              <div class="cls-field">
                <div class="cls-field__head"><span class="cls-field__label">目录名称</span></div>
                <input v-model.trim="selectedNode.rule.name" maxlength="120" placeholder="目录名" />
              </div>

              <div class="cls-field">
                <div class="cls-field__head">
                  <span class="cls-field__label">匹配条件</span>
                  <span v-if="editingLocked" class="cls-field__lock">
                    🔒 内置固定 <span class="cls-field__k">{{ selectedNode.rule.condition }}</span>
                  </span>
                </div>
                <input
                  v-model.trim="selectedNode.rule.condition"
                  class="cls-field__mono"
                  maxlength="500"
                  :readonly="editingLocked"
                  :placeholder="conditionPlaceholder"
                />
                <p class="cls-field__hint">
                  多值用 <code>;</code>；<code>+值</code> 表示必须命中、<code>-值</code> 表示命中即排除；
                  年份支持区间 <code>2000-2009</code> 或单值 <code>2009</code>。
                  规则按顺序评估，<b>首个命中即停</b>。
                </p>
              </div>

              <!-- 命中后固定追加的系列目录（该规则自带系列名时优先于全局系列规则） -->
              <div class="cls-field">
                <div class="cls-field__head">
                  <span class="cls-field__label">系列目录名</span>
                  <SettingsHelpTooltip title="系列目录说明">
                    <p>留空则由下面的「系列目录规则」按关键词判定。</p>
                    <p>填写后只要命中这条规则，就一定会挂上这个目录，不再看关键词。</p>
                  </SettingsHelpTooltip>
                </div>
                <input v-model.trim="selectedNode.rule.series" maxlength="120" placeholder="留空则由系列规则判定" />
              </div>

              <div
                v-if="selectedNode.level === 0 && (selectedNode.rule.children?.length ?? 0) > 0"
                class="cls-fallback"
              >
                <div class="cls-fallback__head">
                  <span>子分类均未命中时</span>
                  <SettingsHelpTooltip title="未命中处理说明">
                    <p>已识别为电影或电视剧，但未命中二级分类时，按这里的设置放置。</p>
                    <p>影视类型也无法识别时，将放在任务目标根目录。</p>
                  </SettingsHelpTooltip>
                </div>
                <div class="cls-fallback__options">
                  <button
                    type="button"
                    class="cls-fallback__choice"
                    :class="{ active: selectedNode.rule.fallback_mode !== 'directory' }"
                    @click="useFallbackDirectory(selectedNode.rule, false)"
                  >
                    <span class="cls-fallback__radio"></span>
                    <span>放入一级分类</span>
                  </button>
                  <div
                    class="cls-fallback__choice cls-fallback__choice--custom"
                    :class="{ active: selectedNode.rule.fallback_mode === 'directory' }"
                    @click="useFallbackDirectory(selectedNode.rule, true)"
                  >
                    <span class="cls-fallback__radio"></span>
                    <span>放入指定目录</span>
                    <input
                      v-model.trim="selectedNode.rule.fallback_dir"
                      maxlength="120"
                      placeholder="其他"
                      aria-label="未命中目录名称"
                      @focus="useFallbackDirectory(selectedNode.rule, true)"
                      @click.stop
                    />
                  </div>
                </div>
              </div>
            </template>
            <div v-else class="cls-edit__empty">从左侧选择一个分类目录开始编辑</div>
          </section>
        </div>

      <!-- 系列目录规则 + 分类预览 -->
      <div class="cls-extras">
        <section class="cls-extras__col">
          <div class="cls-extras__head">
            <strong>系列目录规则</strong>
            <AppButton size="sm" variant="secondary" @click="addSeriesRule">＋ 添加</AppButton>
          </div>
          <p class="cls-extras__note">
            命中关键词的影片会在路径末尾多挂一层系列目录（如「流浪地球系列」）。
            挂在配置根而不是模板里：同一个系列在不同模板下位置不同，改一次漏一处就会出现两个同名目录。
          </p>
          <div class="cls-series-list">
            <div v-for="(item, index) in draft.series ?? []" :key="`series-${index}`" class="cls-series-item">
              <div class="cls-series-item__head">
                <b>{{ item.name || "未命名" }}</b>
                <code>{{ item.dir_name || `${item.name}系列` }}</code>
                <span v-if="item.media_type" class="cls-series-item__type">{{ item.media_type === "movie" ? "仅电影" : "仅剧集" }}</span>
                <button type="button" class="cls-tn__del" title="删除" @click="removeSeriesRule(index)">×</button>
              </div>
              <p class="cls-series-item__kw">{{ (item.series_keywords ?? []).join("、") || "无系列关键词" }}</p>
              <button type="button" class="cls-series-item__edit" @click="editSeriesRule(item)">编辑</button>
            </div>
            <div v-if="!(draft.series ?? []).length" class="cls-tree__empty">还没有系列目录规则</div>
          </div>
          <div class="cls-series-form">
            <input v-model.trim="seriesDraft.name" maxlength="120" placeholder="系列名称，如 流浪地球" aria-label="系列名称" />
            <input v-model.trim="seriesDraft.dir_name" maxlength="120" placeholder="目录名（留空用「名称+系列」）" aria-label="系列目录名" />
            <input v-model.trim="seriesRuleText" maxlength="500" placeholder="系列关键词，逗号分隔" aria-label="系列关键词" />
            <select v-model="seriesMediaType" aria-label="系列适用类型">
              <option value="">不限类型</option>
              <option value="movie">仅电影</option>
              <option value="tv">仅剧集</option>
            </select>
            <AppButton size="sm" variant="primary" :disabled="!!seriesDirtyWarning" @click="saveSeriesRule">保存系列</AppButton>
            <p v-if="seriesDirtyWarning" class="cls-extras__warn">{{ seriesDirtyWarning }}</p>
          </div>
        </section>

        <section class="cls-extras__col">
          <div class="cls-extras__head"><strong>分类预览</strong></div>
          <p class="cls-extras__note">
            填一份影片信息，看它会被放到哪条路径。只做计算，不落库、不建目录、不碰网盘。
          </p>
          <form class="cls-preview-form" @submit.prevent="runPreview">
            <select v-model="previewMediaType" aria-label="预览媒体类型">
              <option value="movie">电影</option>
              <option value="tv">剧集</option>
            </select>
            <input v-model.trim="previewTMDBID" inputmode="numeric" maxlength="10" placeholder="TMDB ID（可空）" aria-label="预览 TMDB ID" />
            <input v-model.trim="previewTitle" maxlength="200" placeholder="标题（可空）" aria-label="预览标题" />
            <input v-model.trim="previewYear" inputmode="numeric" maxlength="4" placeholder="年份（可空）" aria-label="预览年份" />
            <AppButton type="submit" variant="secondary" :disabled="previewLoading">
              {{ previewLoading ? "计算中…" : "预览" }}
            </AppButton>
          </form>
          <p v-if="previewError" class="cls-extras__warn">{{ previewError }}</p>
          <div v-if="previewResult" class="cls-preview-result">
            <code class="cls-preview-result__path">{{ previewPathSegments }}</code>
            <p v-if="previewResult.matched_rule" class="cls-preview-result__rule">
              命中：<b>{{ previewResult.matched_rule }}</b>
            </p>
            <p v-else class="cls-preview-result__rule">没有命中任何分类规则，将放在任务目标根目录</p>
            <p v-if="previewDegradedText" class="cls-preview-result__degraded">{{ previewDegradedText }}</p>
          </div>
        </section>
      </div>

      <template #footer>
        <AppButton class="classification-help-button" variant="secondary" @click="helpOpen = true">查看帮助</AppButton>
        <AppButton variant="primary" :disabled="saving" @click="saveSettings">{{ saving ? "保存中…" : "保存" }}</AppButton>
      </template>
    </AppModal>

    <AppModal :open="helpOpen" title="分类帮助" size="lg" nested @close="helpOpen = false">
      <template #header>
        <div class="cls-help-head">
          <h3 class="modal-help-title">分类帮助</h3>
          <span class="cls-help-head__sub">移动整理时，影片会按模板放进分类目录；二级未命中时使用对应一级目录的设置，影视类型也无法识别时才放在目标根目录。</span>
        </div>
      </template>

      <!-- 条件怎么写 -->
      <section class="cls-help-sec">
        <div class="cls-help-sec__head"><span class="cls-help-sec__ic">∑</span><span class="cls-help-sec__title">条件怎么写</span></div>
        <div class="cls-help-syntax">
          <div class="cls-help-syntax__row">
            <span class="cls-help-syntax__op">或</span>
            <span class="cls-help-syntax__ex">origin_country=CN;US</span>
            <span class="cls-help-syntax__note">同字段多值用分号，CN 或 US 都命中</span>
          </div>
          <div class="cls-help-syntax__row">
            <span class="cls-help-syntax__op">且</span>
            <span class="cls-help-syntax__ex">type=tv，genres=动画</span>
            <span class="cls-help-syntax__note">不同字段用逗号，需同时满足（仅自定义）</span>
          </div>
        </div>
        <div class="cls-help-fields">
          <span>常用字段：</span><code>type</code><code>origin_country</code><code>genres</code>
          <span class="cls-help-fields__tip">不确定真实返回值？用下面的查询工具看实际字段。</span>
        </div>
      </section>

      <!-- TMDB 字段查询 -->
      <section class="cls-help-sec">
        <div class="cls-help-sec__head"><span class="cls-help-sec__ic">🔎</span><span class="cls-help-sec__title">TMDB 字段查询</span></div>
        <div class="classification-lookup">
          <div class="classification-lookup__intro">
            <strong>查真实字段</strong>
            <span>输入 ID 查看字段值，点「复制」直接用作匹配条件</span>
          </div>
          <form class="classification-lookup__form" @submit.prevent="lookupTMDBDetail">
            <select v-model="detailMediaType" aria-label="TMDB 媒体类型">
              <option value="movie">电影</option>
              <option value="tv">电视剧</option>
            </select>
            <input v-model.trim="detailTMDBID" inputmode="numeric" maxlength="10" aria-label="TMDB ID" placeholder="TMDB ID，如 281495" />
            <AppButton type="submit" variant="secondary" :disabled="detailLoading">
              {{ detailLoading ? "查询中…" : "查询" }}
            </AppButton>
          </form>

          <div v-if="detailResult" class="classification-detail">
            <div class="classification-detail__title">
              <strong>{{ detailTitle }}</strong>
              <span>TMDB {{ detailResult.id }} · {{ detailResult.media_type === "tv" ? "电视剧" : "电影" }}</span>
            </div>
            <div class="classification-detail__conditions">
              <button
                v-for="item in detailConditions"
                :key="item.value"
                type="button"
                :title="`复制 ${item.value}`"
                @click="copyCondition(item.value)"
              >
                <span>{{ item.label }}</span><code>{{ item.value }}</code><b>复制</b>
              </button>
            </div>
            <details class="classification-detail__raw">
              <summary>查看全部 TMDB 详情字段</summary>
              <pre>{{ detailJSON }}</pre>
            </details>
          </div>
        </div>
      </section>

      <template #footer>
        <AppButton variant="primary" @click="helpOpen = false">关闭</AppButton>
      </template>
    </AppModal>
  </div>
</template>

<style scoped>
.classification-template-tabs { display: flex; gap: 4px; border-bottom: 1px solid var(--border); }
.classification-template-tab { min-width: 0; flex: 1; display: flex; flex-direction: column; gap: 2px; align-items: center; padding: 10px 8px 12px; border: 0; border-bottom: 2px solid transparent; background: transparent; color: var(--text-muted); cursor: pointer; transition: 0.15s; font-family: inherit; }
.classification-template-tab strong { font-size: 13px; font-weight: 600; }
.classification-template-tab span { color: var(--text-faint); font-size: 11px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%; }
.classification-template-tab:hover { color: var(--text); }
.classification-template-tab.active { color: var(--brand); border-bottom-color: var(--brand); }
.classification-template-tab.active span { color: var(--text-muted); }

/* ── 文件树 + 右侧编辑工作台 ── */
.cls-workspace { display: grid; grid-template-columns: 250px minmax(0, 1fr); min-height: 420px; }

/* 左侧树 */
.cls-tree { border-right: 1px solid var(--border); background: var(--surface-sunken); padding: 14px 12px; display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.cls-tree__cap { font-size: 11px; color: var(--text-muted); letter-spacing: .06em; padding: 2px 6px 6px; }
.cls-tree__list { overflow-y: auto; flex: 1; display: flex; flex-direction: column; gap: 1px; padding-bottom: 10px; min-height: 0; }
.cls-tree__empty { padding: 22px 8px; text-align: center; color: var(--text-muted); font-size: 12px; }
.cls-tree__add { margin-top: auto; width: 100%; padding: 8px 12px; border-radius: var(--radius-sm); border: 1px dashed var(--border2); background: transparent; color: var(--text-muted); font-size: 13px; cursor: pointer; transition: .12s; }
.cls-tree__add:hover { border-color: var(--brand); color: var(--brand); background: var(--brand-soft); }

.cls-tn { position: relative; display: flex; align-items: center; gap: 6px; padding: 6px 8px; border-radius: var(--radius-sm); border: 1px solid transparent; cursor: pointer; transition: .1s; min-width: 0; user-select: none; }
.cls-tn:hover { background: var(--surface); }
.cls-tn.active { background: var(--brand-soft); border-color: color-mix(in srgb, var(--brand) 40%, transparent); }
.cls-tn__arrow { width: 14px; height: 14px; border-radius: 4px; display: flex; align-items: center; justify-content: center; font-size: 8px; color: var(--text-muted); flex-shrink: 0; transition: .12s; }
.cls-tn__arrow:hover { background: var(--surface); }
.cls-tn__arrow.open { transform: rotate(90deg); }
.cls-tn__arrow.leaf { visibility: hidden; }
.cls-tn__fic { font-size: 15px; flex-shrink: 0; }
.cls-tn b { font-size: 13px; font-weight: 500; color: var(--text); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
.cls-tn__add { width: 20px; height: 20px; border: 0; border-radius: var(--radius-xs); background: transparent; color: var(--brand); font-size: 13px; line-height: 1; cursor: pointer; transition: .12s; flex-shrink: 0; display: flex; align-items: center; justify-content: center; padding: 0; opacity: .55; }
.cls-tn__add:hover { background: var(--brand-soft); opacity: 1; }
.cls-tn__del { width: 20px; height: 20px; border: 0; border-radius: var(--radius-xs); background: transparent; color: var(--text-muted); font-size: 12px; cursor: pointer; transition: .12s; flex-shrink: 0; display: flex; align-items: center; justify-content: center; padding: 0; opacity: .55; }
.cls-tn__del:hover { background: rgba(239, 68, 68, .12); color: var(--danger); opacity: 1; }
.cls-tn__del:disabled { opacity: 0; cursor: default; }
.cls-tn--child { padding-left: 24px; }
.cls-tn--child::before { content: ""; position: absolute; left: 10px; top: 0; bottom: 0; width: 1px; background: var(--border2); }
.cls-tn--child::after { content: ""; position: absolute; left: 10px; top: 50%; width: 11px; height: 1px; background: var(--border2); }

/* 右侧编辑 */
.cls-edit { padding: 18px 24px 20px; display: flex; flex-direction: column; gap: 15px; overflow-y: auto; min-width: 0; background: var(--surface); }
.cls-edit__head { display: flex; align-items: center; }
.cls-edit__title { font-size: 15px; font-weight: 600; color: var(--text); display: flex; align-items: center; gap: 8px; }
.cls-edit__fic { font-size: 16px; }
.cls-edit__lvl { display: inline-flex; align-items: center; height: 20px; margin-left: 4px; padding: 0 9px; border-radius: var(--radius-pill); background: var(--brand-soft); color: var(--brand); font-size: 11px; flex-shrink: 0; }
.cls-edit__empty { flex: 1; display: flex; align-items: center; justify-content: center; color: var(--text-muted); font-size: 13px; }

.cls-field { display: flex; flex-direction: column; gap: 7px; }
.cls-field__head { display: flex; align-items: center; gap: 6px; }
.cls-field__label { font-size: 13px; font-weight: 500; color: var(--text-regular); }
.cls-field__lock { margin-left: auto; display: inline-flex; align-items: center; gap: 4px; font-size: 11px; color: var(--text-muted); }
.cls-field__k { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 10.5px; background: var(--surface-sunken); border-radius: 4px; padding: 1px 5px; color: var(--text-muted); }
.cls-field input { width: 100%; padding: 9px 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--surface); color: var(--text); font-size: 13px; outline: none; transition: border-color .15s, box-shadow .15s; font-family: inherit; box-sizing: border-box; }
.cls-field input:focus { border-color: var(--brand); box-shadow: 0 0 0 3px var(--brand-soft); }
.cls-field input::placeholder { color: var(--text-faint); }
.cls-field input:read-only { background: var(--surface-sunken); color: var(--text-muted); cursor: default; }
.cls-field__mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12.5px; }

.cls-fallback { display: flex; flex-direction: column; gap: 7px; min-width: 0; padding: 0; }
.cls-fallback__head { display: flex; align-items: center; gap: 6px; color: var(--text-regular); font-size: 13px; font-weight: 500; white-space: nowrap; }
.cls-fallback__options { display: flex; align-items: center; gap: 7px; min-width: 0; overflow-x: auto; scrollbar-width: none; }
.cls-fallback__options::-webkit-scrollbar { display: none; }
.cls-fallback__choice { min-height: 34px; display: flex; align-items: center; gap: 7px; padding: 4px 10px; border: 1px solid var(--border); border-radius: var(--radius-sm); color: var(--text-regular); background: transparent; font-family: inherit; font-size: 12.5px; white-space: nowrap; cursor: pointer; transition: border-color .15s, color .15s, background .15s; }
.cls-fallback__choice:hover { border-color: color-mix(in srgb, var(--brand) 45%, var(--border)); }
.cls-fallback__choice.active { border-color: var(--brand); color: var(--brand); background: var(--brand-soft); }
.cls-fallback__choice--custom { flex: 1; min-width: 280px; }
.cls-fallback__radio { width: 14px; height: 14px; border: 1.5px solid var(--border2); border-radius: 50%; background: var(--surface); box-shadow: inset 0 0 0 3px var(--surface); flex-shrink: 0; }
.cls-fallback__choice.active .cls-fallback__radio { border-color: var(--brand); background: var(--brand); }
.cls-fallback__choice input { min-width: 100px; width: 100%; height: 25px; padding: 0 8px; border: 0; border-left: 1px solid var(--border); border-radius: 0; outline: none; color: var(--text); background: transparent; font-family: inherit; font-size: 12px; }
.cls-fallback__choice:not(.active) input { color: var(--text-faint); background: var(--surface-sunken); pointer-events: none; }
.cls-fallback__choice.active input:focus { border-color: var(--brand); box-shadow: 0 0 0 2px color-mix(in srgb, var(--brand) 14%, transparent); }

.classification-help-button { margin-right: auto; }

/* ── 帮助弹窗头部：标题 + 副标题 ── */
.cls-help-head { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.cls-help-head .modal-help-title { margin: 0; font-size: 15px; font-weight: 600; color: var(--text); }
.cls-help-head__sub { font-size: 12.5px; color: var(--text-muted); line-height: 1.5; }

/* ── 帮助弹窗：区块排版 ── */
.cls-help-sec { display: flex; flex-direction: column; gap: 10px; }
.cls-help-sec + .cls-help-sec { margin-top: 16px; }
.cls-help-sec__head { display: flex; align-items: center; gap: 8px; }
.cls-help-sec__ic { width: 24px; height: 24px; border-radius: var(--radius-sm); background: var(--brand-soft); color: var(--brand); display: flex; align-items: center; justify-content: center; font-size: 12px; flex-shrink: 0; }
.cls-help-sec__title { font-size: 14px; font-weight: 600; color: var(--text); }

.cls-help-syntax { display: flex; flex-direction: column; gap: 8px; }
.cls-help-syntax__row { display: grid; grid-template-columns: 56px minmax(0, 1fr) auto; gap: 10px; align-items: center; border: 1px solid var(--border); border-radius: var(--radius-control); padding: 9px 12px; background: var(--surface-sunken); }
.cls-help-syntax__op { font-size: 12px; font-weight: 600; color: var(--brand); background: var(--brand-soft); border-radius: var(--radius-xs); padding: 3px 8px; text-align: center; }
.cls-help-syntax__ex { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12.5px; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cls-help-syntax__note { font-size: 12.5px; color: var(--muted); white-space: nowrap; }

.cls-help-fields { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 13px; color: var(--text-regular); }
.cls-help-fields code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; background: var(--surface-sunken); border-radius: var(--radius-xs); padding: 2px 7px; color: var(--text); }
.cls-help-fields__tip { color: var(--text-muted); font-size: 12px; }

/* ── bare 自绘弹窗：贴边结构 ── */
.classification-lookup { margin-top: 10px; padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-md); background: var(--surface-sunken); }
.classification-lookup__intro { display: flex; align-items: baseline; gap: 8px; }
.classification-lookup__intro strong { font-size: 13px; font-weight: 600; }
.classification-lookup__intro span { color: var(--text-muted); font-size: 12.5px; }
.classification-lookup__form { display: grid; grid-template-columns: 110px minmax(180px, 1fr) auto; gap: 8px; margin-top: 9px; }
.classification-lookup__form select, .classification-lookup__form input { box-sizing: border-box; min-width: 0; width: 100%; border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 8px 9px; background: var(--surface); color: var(--text); font-size: 13px; }
.classification-detail { margin-top: 11px; padding-top: 11px; border-top: 1px solid var(--border); }
.classification-detail__title { display: flex; align-items: baseline; gap: 8px; }
.classification-detail__title strong { font-size: 14px; font-weight: 600; }
.classification-detail__title span { color: var(--text-muted); font-size: 12.5px; }
.classification-detail__conditions { display: grid; gap: 6px; margin-top: 9px; }
.classification-detail__conditions button { display: grid; grid-template-columns: 72px minmax(0, 1fr) auto; gap: 8px; align-items: center; width: 100%; padding: 7px 9px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--surface); color: var(--text); text-align: left; cursor: pointer; }
.classification-detail__conditions button:hover { border-color: var(--brand); }
.classification-detail__conditions span { color: var(--text-muted); font-size: 12.5px; }
.classification-detail__conditions code { overflow-wrap: anywhere; white-space: normal; }
.classification-detail__conditions b { color: var(--brand); font-size: 12px; }
.classification-detail__raw { margin-top: 9px; color: var(--text-regular); font-size: 12.5px; }
.classification-detail__raw summary { cursor: pointer; color: var(--brand); }
.classification-detail__raw pre { box-sizing: border-box; max-height: 280px; overflow: auto; margin: 8px 0 0; padding: 10px; border-radius: var(--radius-sm); background: var(--surface); color: var(--text); font-size: 11.5px; line-height: 1.55; white-space: pre-wrap; overflow-wrap: anywhere; }

/* ── 三级目录 ── */
/* 三级比二级再缩进一级。第三层用虚线树线而不是实线：前两层是「分类」，
   第三层是「分类内部的细分」，视觉上弱一级，避免用户以为两者地位相同。 */
.cls-tn--grand { padding-left: 40px; }
.cls-tn--grand::before { content: ""; position: absolute; left: 26px; top: 0; bottom: 0; width: 1px; background: repeating-linear-gradient(to bottom, var(--border2) 0 3px, transparent 3px 6px); }
.cls-tn--grand::after { content: ""; position: absolute; left: 26px; top: 50%; width: 11px; height: 1px; background: var(--border2); }
.cls-tn--grand b { font-weight: 400; color: var(--text-regular); }
.cls-field__hint { margin: 0; color: var(--text-muted); font-size: 11.5px; line-height: 1.6; }

/* ── 系列目录规则 + 分类预览 ── */
.cls-extras { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 18px; padding: 16px 20px; border-top: 1px solid var(--border); background: var(--surface-sunken); }
.cls-extras__col { display: flex; flex-direction: column; gap: 9px; min-width: 0; }
.cls-extras__head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.cls-extras__head strong { font-size: 13px; font-weight: 600; color: var(--text); }
.cls-extras__note { margin: 0; color: var(--text-muted); font-size: 11.5px; line-height: 1.6; }
.cls-extras__warn { margin: 0; color: var(--warning, var(--brand)); font-size: 11.5px; line-height: 1.6; }

.cls-series-list { display: flex; flex-direction: column; gap: 6px; max-height: 190px; overflow-y: auto; }
.cls-series-item { position: relative; display: flex; flex-direction: column; gap: 4px; padding: 8px 10px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--surface); }
.cls-series-item__head { display: flex; align-items: center; gap: 7px; min-width: 0; }
.cls-series-item__head b { font-size: 12.5px; font-weight: 500; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cls-series-item__head code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; color: var(--brand); background: var(--brand-soft); border-radius: 4px; padding: 1px 5px; white-space: nowrap; }
.cls-series-item__type { flex-shrink: 0; padding: 1px 6px; border-radius: 999px; background: var(--surface-sunken); color: var(--text-muted); font-size: 10.5px; white-space: nowrap; }
.cls-series-item__kw { margin: 0; color: var(--text-muted); font-size: 11.5px; overflow-wrap: anywhere; }
.cls-series-item__edit { align-self: flex-start; padding: 0; border: 0; background: transparent; color: var(--brand); font-size: 11.5px; font-family: inherit; cursor: pointer; }
.cls-series-item__edit:hover { text-decoration: underline; }

.cls-series-form { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 7px; align-content: start; }
.cls-series-form input, .cls-series-form select, .cls-preview-form input, .cls-preview-form select { box-sizing: border-box; width: 100%; padding: 8px 10px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--surface); color: var(--text); font-size: 12.5px; font-family: inherit; outline: none; transition: border-color .15s, box-shadow .15s; }
.cls-series-form input:focus, .cls-series-form select:focus, .cls-preview-form input:focus, .cls-preview-form select:focus { border-color: var(--brand); box-shadow: 0 0 0 3px var(--brand-soft); }
.cls-series-form input::placeholder, .cls-preview-form input::placeholder { color: var(--text-faint); }
.cls-series-form > input:nth-of-type(3) { grid-column: 1 / -1; }
.cls-series-form > select { grid-column: 1 / 2; }
.cls-series-form > .cls-series-form__save { grid-column: 2 / 3; }

.cls-preview-form { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 7px; align-content: start; }
.cls-preview-form select { grid-column: 1 / 2; }
.cls-preview-form input:nth-of-type(1) { grid-column: 2 / 3; }
.cls-preview-form > *:nth-last-child(2), .cls-preview-form > *:nth-last-child(1) { grid-column: span 1; }
.cls-preview-form > button, .cls-preview-form > .cls-preview-form__go { grid-column: 1 / -1; }

.cls-preview-result { display: flex; flex-direction: column; gap: 6px; padding: 10px 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--surface); }
.cls-preview-result__path { display: block; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; color: var(--text); overflow-wrap: anywhere; line-height: 1.6; }
.cls-preview-result__rule { margin: 0; color: var(--text-muted); font-size: 12px; }
.cls-preview-result__rule b { color: var(--brand); font-weight: 600; }
.cls-preview-result__degraded { margin: 0; padding-top: 6px; border-top: 1px dashed var(--border); color: var(--warning, var(--brand)); font-size: 11.5px; line-height: 1.6; }

@media (max-width: 760px) {
  .classification-template-tabs { flex-wrap: wrap; }
  .classification-template-tab { flex: 1 1 45%; }
  .cls-workspace { grid-template-columns: 1fr; }
  .cls-tree { border-right: 0; border-bottom: 1px solid var(--border); }
  .classification-lookup__intro, .classification-detail__title { align-items: flex-start; flex-direction: column; gap: 2px; }
  .classification-lookup__form { grid-template-columns: 1fr; }
  .classification-detail__conditions button { grid-template-columns: 1fr auto; }
  .classification-detail__conditions span { grid-column: 1 / -1; }
  /* 系列规则与预览在窄屏各自占满整行：两栏并排时每栏只剩 150px 上下，
     目录名和关键词都会被截断成看不出差别的两行字。 */
  .cls-extras { grid-template-columns: 1fr; padding: 14px 16px; }
  .cls-series-form, .cls-preview-form { grid-template-columns: 1fr; }
  .cls-series-form > input:nth-of-type(3), .cls-series-form > select,
  .cls-series-form > .cls-series-form__save,
  .cls-preview-form select, .cls-preview-form input:nth-of-type(1),
  .cls-preview-form > *:nth-last-child(-n+2) { grid-column: 1 / -1; }
}
@media (max-width: 480px) {
  .classification-template-tabs { flex-direction: column; align-items: stretch; }
  .classification-template-tab { border-bottom: 0; border-left: 2px solid transparent; align-items: flex-start; padding: 8px 10px; }
  .classification-template-tab.active { border-left-color: var(--brand); border-bottom-color: transparent; }
}
</style>
