<script setup lang="ts">
// 洗版（media upgrade）管理页。
//
// 交互口径刻意做成「扫描」和「提交」两个分开的按钮：
// 扫描一个文件都不碰，只产出待执行记录；只有提交才会删/移文件。
// 中间那一步是用户唯一的反悔窗口，所以提交按钮要二次确认，
// 而且确认文案必须说清「会按规则删败方文件，且不可撤销」。
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  createMediaUpgradeRule,
  createMediaUpgradeScan,
  deleteMediaUpgradeRule,
  executeMediaUpgradeScan,
  fetchMediaUpgradeRecords,
  fetchMediaUpgradeRules,
  fetchMediaUpgradeScans,
  MEDIA_UPGRADE_LOSER_ACTIONS,
  MEDIA_UPGRADE_SOURCES,
  trialMediaUpgradeRule,
  updateMediaUpgradeRule,
  type MediaUpgradeExecuteResult,
  type MediaUpgradeRecord,
  type MediaUpgradeRule,
  type MediaUpgradeScan,
  type MediaUpgradeTrialResult,
  type RejectReason,
} from "@/api/mediaUpgrade";
import type { MediaUpgradeRecordInput as MediaUpgradeRuleInput } from "@/api/mediaUpgrade";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import { classificationApi } from "@/api/cloudTools";
import type { ClassificationCategory } from "@/api/cloudTools";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import AdminStatusPill from "@/components/admin/AdminStatusPill.vue";
import type { AdminStatusPillTone } from "@/components/admin/AdminStatusPill.vue";
import DirRefHint from "@/components/admin/DirRefHint.vue";
import FormField from "@/components/base/FormField.vue";
import SettingsBoolSegment from "@/components/admin/SettingsBoolSegment.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import { useConfirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import { formatSize, formatTime } from "@/utils/format";
import "@/styles/admin-table.css";
import "@/styles/settings-panel.css";

const { showConfirm } = useConfirm();

// ---- 状态与关系 ----
const RELATION_OPTIONS = [
  { value: "", label: "全部判定" },
  { value: "new_wins", label: "新版更优" },
  { value: "new_loses", label: "新版更差" },
  { value: "tie", label: "持平" },
  { value: "no_dimension", label: "无可比维度" },
];

const RECORD_STATUS_META: Record<string, { label: string; tone: AdminStatusPillTone }> = {
  pending: { label: "待提交", tone: "warning" },
  executing: { label: "提交中", tone: "brand" },
  executed: { label: "已执行", tone: "success" },
  expired: { label: "判定已过期", tone: "muted" },
  failed: { label: "失败", tone: "danger" },
  skipped_limit: { label: "超出每部剧上限", tone: "muted" },
  skipped_new_loses: { label: "新版更差，不换", tone: "muted" },
  skipped_no_slot: { label: "不同槽位，两个都保留", tone: "muted" },
  skipped_no_dimension: { label: "无可比维度", tone: "muted" },
  skipped_no_access: { label: "路径不可达", tone: "warning" },
};

const SCAN_STATUS_META: Record<string, { label: string; tone: AdminStatusPillTone }> = {
  pending: { label: "待开始", tone: "muted" },
  running: { label: "扫描中", tone: "brand" },
  success: { label: "完成", tone: "success" },
  partial: { label: "部分完成", tone: "warning" },
  failed: { label: "失败", tone: "danger" },
};

function relationLabel(v: string): string {
  return RELATION_OPTIONS.find((o) => o.value === v)?.label ?? v;
}

function recordStatusMeta(status: string) {
  return RECORD_STATUS_META[status] ?? { label: status, tone: "muted" as AdminStatusPillTone };
}

function scanStatusMeta(status: string) {
  return SCAN_STATUS_META[status] ?? { label: status, tone: "muted" as AdminStatusPillTone };
}

// ---- 规则 ----
const rulesLoading = ref(false);
const rulesError = ref("");
const globalRule = ref<MediaUpgradeRule | null>(null);
const rules = ref<MediaUpgradeRule[]>([]);

const ruleModalOpen = ref(false);
const ruleSaving = ref(false);
const ruleError = ref("");
/** 编辑中的规则草稿；id=0 表示新建。 */
const draft = ref<MediaUpgradeRule>(emptyRule());

// ---- 适用分类（T27）----
// 选项来自分类模板**当前配了什么**，不是磁盘上有什么 —— 配过但还没整理过
// 任何文件的分类也必须在列表里，否则用户第一次配洗版规则时会发现根本选不到
// 自己刚建的分类。
const categoriesLoading = ref(false);
const categories = ref<ClassificationCategory[]>([]);

async function loadCategories() {
  categoriesLoading.value = true;
  try {
    const resp = await classificationApi.categories();
    categories.value = resp.items ?? [];
  } catch (e) {
    // 分类清单拿不到就当"没有分类可选"，但不能挡着用户建规则：
    // 这一栏本来就是可选条件，清空它等于不过筛，行为完全可预期。
    categories.value = [];
    toast.warning(getApiErrorMessage(e, "分类清单加载失败，暂不按分类筛选"));
  } finally {
    categoriesLoading.value = false;
  }
}

/** 按层级分组展示，同名分类保留各自的层级标签而不是合并去重。 */
const categoryGroups = computed(() => {
  const byLevel = new Map<number, ClassificationCategory[]>();
  for (const c of categories.value) {
    const list = byLevel.get(c.level) ?? [];
    list.push(c);
    byLevel.set(c.level, list);
  }
  return [...byLevel.entries()].sort((a, b) => a[0] - b[0]).map(([level, items]) => ({
    level,
    label: LEVEL_LABELS[level] ?? `第 ${level} 级`,
    options: items.map((c) => ({
      value: c.name,
      label: c.level > 1 ? `${c.name}（${LEVEL_LABELS[c.level] ?? `第 ${c.level} 级`}）` : c.name,
    })),
  }));
});

const LEVEL_LABELS: Record<number, string> = { 1: "一级", 2: "二级", 3: "三级" };

function categorySelected(name: string): boolean {
  return (draft.value.categories ?? []).includes(name);
}

/**
 * 勾选分类。选中一级时把同名的二级一起带上 —— 用户心里的"国产剧"通常
 * 指整个子树，而按段位匹配只认目录名，不勾二级就一条都匹配不上。
 */
function toggleCategory(name: string, level: number) {
  const cur = new Set(draft.value.categories ?? []);
  if (cur.has(name)) {
    cur.delete(name);
  } else {
    cur.add(name);
    if (level === 1) {
      for (const c of categories.value) {
        if (c.level > 1 && c.name === name) cur.add(name);
      }
    }
  }
  draft.value.categories = [...cur];
}

function categoryNamesOf(rule: MediaUpgradeRule | null): string {
  const names = rule?.categories ?? [];
  return names.length ? names.join("、") : "全部";
}

/**
 * 候选目录取第一条去查覆盖率（T32）。
 *
 * 这个字段是**列表**（多个目录用逗号/顿号/分号/换行分隔，后端
 * mediaupgrade.SplitRoots 就是这么切的），而目录提示是单目录的。
 * 只提示第一条是因为：
 *   - 逐条渲染一排徽章会把这一栏撑成一个目录清单，用户看不出哪个要紧；
 *   - 分隔符里没有空格是后端的既定口径（见 cloudref.rootListSeps 的注释），
 *     所以「按分隔符切第一条」和后端的切法一致，不会切出后端不认的路径。
 * 其余目录的归属不在这里提示 —— 那是覆盖率报告的事，不是配置页防呆的事。
 */
function firstCandidateRoot(candidateRoots: string): string {
  const items = candidateRoots?.split(/[，,、;；\n\r]/g) ?? [];
  for (const item of items) {
    const trimmed = item.trim();
    if (trimmed !== "") return trimmed;
  }
  return "";
}

function emptyRule(): MediaUpgradeRule {
  return {
    id: 0,
    name: "",
    source: "local",
    library_root: "",
    candidate_roots: "",
    min_resolution: 0,
    min_channels: 0,
    require_subtitle: false,
    max_records_per_series: 0,
    loser_action: "keep",
    move_dir: "",
    group_priority: "",
    wash_rules: "",
    categories: [],
    enabled: true,
    builtin: false,
  };
}

async function loadRules() {
  rulesLoading.value = true;
  rulesError.value = "";
  try {
    const resp = await fetchMediaUpgradeRules();
    globalRule.value = resp.global;
    rules.value = resp.items ?? [];
  } catch (e) {
    rulesError.value = getApiErrorMessage(e, "洗版规则加载失败");
  } finally {
    rulesLoading.value = false;
  }
}

function openCreateRule() {
  draft.value = emptyRule();
  ruleError.value = "";
  ruleModalOpen.value = true;
  if (!categories.value.length) void loadCategories();
}

function openEditRule(rule: MediaUpgradeRule) {
  // 后端可能返回 categories: null（老版本前端序列化或显式 null），
  // 直接展开会把 draft.categories 变成 null，点勾选时 .includes 报红。
  draft.value = { ...rule, categories: [...(rule.categories ?? [])] };
  ruleError.value = "";
  ruleModalOpen.value = true;
  if (!categories.value.length) void loadCategories();
}

async function saveRule() {
  ruleSaving.value = true;
  ruleError.value = "";
  try {
    // 数字字段全程是 number，直接把 AppInput 发出来的字符串原样塞回去会 400。
    const body: MediaUpgradeRuleInput = {
      name: draft.value.name.trim(),
      source: draft.value.source,
      library_root: draft.value.library_root.trim(),
      candidate_roots: draft.value.candidate_roots.trim(),
      min_resolution: toInt(draft.value.min_resolution),
      min_channels: toInt(draft.value.min_channels),
      require_subtitle: draft.value.require_subtitle,
      max_records_per_series: toInt(draft.value.max_records_per_series),
      loser_action: draft.value.loser_action,
      move_dir: draft.value.move_dir.trim(),
      group_priority: draft.value.group_priority.trim(),
      wash_rules: draft.value.wash_rules.trim(),
      categories: [...(draft.value.categories ?? [])],
      enabled: draft.value.enabled,
      id: draft.value.id || undefined,
    };
    if (draft.value.id) {
      await updateMediaUpgradeRule(draft.value.id, body);
    } else {
      await createMediaUpgradeRule(body);
    }
    ruleModalOpen.value = false;
    toast.success(draft.value.id ? "规则已更新" : "规则已创建");
    await loadRules();
  } catch (e) {
    // 校验错误要留在弹窗里：后端回的是「败方动作设为 move 时必须填写移动目标目录」
    // 这类具体原因，直接透传给用户比弹个 toast 后把弹窗关掉有用得多。
    ruleError.value = getApiErrorMessage(e, "规则保存失败");
  } finally {
    ruleSaving.value = false;
  }
}

/** 空串/非法输入按 0 处理；后端把 0 解释为「不限制」。 */
function toInt(v: unknown): number {
  const n = Number(v);
  return Number.isFinite(n) ? Math.trunc(n) : 0;
}

async function removeRule(rule: MediaUpgradeRule) {
  try {
    await showConfirm({
      title: "删除洗版规则",
      message: `确定删除规则「${rule.name || rule.id}」吗？已产生的扫描记录不受影响。`,
      icon: "trash",
      confirmText: "删除",
    });
  } catch {
    return;
  }
  try {
    await deleteMediaUpgradeRule(rule.id);
    toast.success("规则已删除");
    await loadRules();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "规则删除失败"));
  }
}

// ---- 扫描 ----
const scansLoading = ref(false);
const scans = ref<MediaUpgradeScan[]>([]);
const scanning = ref(false);
const executingScanId = ref(0);

/** 扫描用哪套规则：0=全局设置，否则选中的具名规则 id。 */
const scanRuleId = ref(0);
const ruleChoices = computed(() => [
  { value: 0, label: "全局设置" },
  ...rules.value.map((r) => ({ value: r.id, label: r.name || `规则 #${r.id}` })),
]);

async function loadScans() {
  scansLoading.value = true;
  try {
    const resp = await fetchMediaUpgradeScans(50);
    scans.value = resp.items ?? [];
  } catch (e) {
    toast.error(getApiErrorMessage(e, "扫描记录加载失败"));
  } finally {
    scansLoading.value = false;
  }
}

async function runScan() {
  scanning.value = true;
  try {
    const resp = await createMediaUpgradeScan(scanRuleId.value || undefined);
    toast.success(resp.item?.message || "扫描完成");
    await Promise.all([loadScans(), loadRecords()]);
  } catch (e) {
    // 总开关关闭 / 没配媒体库目录等都在这里冒出来，原样回显。
    toast.error(getApiErrorMessage(e, "扫描失败"));
  } finally {
    scanning.value = false;
  }
}

const lastExecuteResult = ref<MediaUpgradeExecuteResult | null>(null);

async function executeScan(scan: MediaUpgradeScan) {
  const destructive = destructiveRules.value;
  try {
    await showConfirm({
      title: "提交洗版判定",
      message: `将对扫描 #${scan.id} 的 ${scan.new_wins_count} 条待执行判定逐条复核快照，${
        destructive ? "并按规则删除或移动败方文件" : "按当前规则处理败方文件"
      }。`,
      hint: destructive
        ? "被处理的是同一版本槽位里较差的旧文件。复核不通过（文件已被改动）的判定会整条作废、不动任何文件；同一条判定只会被执行一次。"
        : "当前败方动作为「保留」，本次提交不会删除任何文件。",
      icon: destructive ? "trash" : "info",
      confirmText: destructive ? "确认提交" : "确认",
      danger: destructive,
      checkboxLabel: destructive ? "我已确认将被删除的文件不在我需要保留的范围内" : undefined,
    });
  } catch {
    return;
  }
  executingScanId.value = scan.id;
  try {
    const res = await executeMediaUpgradeScan(scan.id);
    lastExecuteResult.value = res;
    toast.success(res.message || "提交完成");
    await Promise.all([loadScans(), loadRecords()]);
  } catch (e) {
    toast.error(getApiErrorMessage(e, "提交失败"));
  } finally {
    executingScanId.value = 0;
  }
}

/** 当前生效规则里败方动作是不是会动文件（delete/move）。决定确认框的措辞与危险样式。 */
const destructiveRules = computed(() => {
  const target = rules.value.find((r) => r.id === scanRuleId.value);
  const action = target ? target.loser_action : globalRule.value?.loser_action;
  return action === "delete" || action === "move";
});

// ---- 判定记录 ----
const recordsLoading = ref(false);
const records = ref<MediaUpgradeRecord[]>([]);
const recordStatus = ref("");
const recordScanId = ref(0);
const expandedRecordId = ref(0);

const recordStatusChoices = computed(() => [
  { value: 0, label: "全部扫描" },
  ...scans.value.slice(0, 30).map((s) => ({ value: s.id, label: `#${s.id}（${formatTime(s.created_at)}）` })),
]);

async function loadRecords() {
  recordsLoading.value = true;
  try {
    const resp = await fetchMediaUpgradeRecords({
      scan_id: recordScanId.value || undefined,
      status: recordStatus.value || undefined,
      limit: 200,
    });
    records.value = resp.items ?? [];
  } catch (e) {
    toast.error(getApiErrorMessage(e, "判定记录加载失败"));
  } finally {
    recordsLoading.value = false;
  }
}

function toggleRecord(id: number) {
  expandedRecordId.value = expandedRecordId.value === id ? 0 : id;
}

/** delete_failures 是 JSON 文本，解析失败要能显示原文而不是整块报错。 */
function parseDeleteFailures(raw: string): Array<{ path: string; stage: string; reason: string }> {
  if (!raw?.trim()) return [];
  try {
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [{ path: raw, stage: "?", reason: "失败明细不是合法 JSON，原文照录" }];
  }
}

function oldFileNames(record: MediaUpgradeRecord): string[] {
  if (!record.old_files?.trim()) return [];
  try {
    const parsed = JSON.parse(record.old_files);
    if (!Array.isArray(parsed)) return [];
    return parsed.map((f: { name?: string; path?: string }) => f.name || f.path || "");
  } catch {
    return [];
  }
}

// ---- T31 · 结构化驳回理由 ----
//
// 理由是 JSON 串，解析失败必须显示原文而不是整块报错：
// 一条渲染不出来的驳回理由，恰好是最需要被人看见的那一条。
const REJECT_REASON_LABEL: Record<string, string> = {
  inferior_dimension: "关键维度更差",
  below_min_resolution: "低于最低分辨率",
  below_min_channels: "低于最低声道数",
  missing_required_subtitle: "缺少要求的字幕",
  no_comparable_dimension: "无可比维度",
  no_slot: "不同版本槽位",
};

function parseRejectReasons(raw?: string): RejectReason[] {
  if (!raw?.trim()) return [];
  try {
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((r) => r && typeof r.code === "string");
  } catch {
    return [];
  }
}

function rejectReasonLabel(code: string): string {
  // 认不出来的 code 原样显示：前端枚举落后于后端时，
  // 悄悄显示成"其它原因"会让人以为看懂了。
  return REJECT_REASON_LABEL[code] ?? code;
}

/** 理由的一句话描述；只在该字段有值时才提字段名。 */
function rejectReasonText(reason: RejectReason): string {
  const label = rejectReasonLabel(reason.code);
  const parts: string[] = [];
  if (reason.field) parts.push(reason.field);
  if (reason.new || reason.old) parts.push(`${reason.new || "—"} / ${reason.old || "—"}`);
  return parts.length ? `${label}（${parts.join("，")}）` : label;
}

const trialNewName = ref("");
const trialOldName = ref("");
const trialNewSize = ref("");
const trialOldSize = ref("");
const trialResult = ref<MediaUpgradeTrialResult | null>(null);
const trialError = ref("");
const trialBusy = ref(false);
// 草稿里的 wash_rules 是 JSON 串，试算得连草稿一起发过去，
// 否则用户试的永远是库里那条规则 —— 界面上显示的却是草稿。
function trialRulePayload(): MediaUpgradeRuleInput {
  // 解引用 ref：直接展开 ref 得到的是 Ref 对象本身，
  // 发出去后端会收到一坨带 get/set 的东西而不是规则字段。
  return { ...draft.value } as unknown as MediaUpgradeRuleInput;
}

async function runTrial(): Promise<void> {
  trialError.value = "";
  if (!trialNewName.value.trim() || !trialOldName.value.trim()) {
    trialError.value = "新文件和现版的文件名都要填";
    return;
  }
  trialBusy.value = true;
  try {
    const sizeOf = (v: string) => {
      const n = Number(v);
      return Number.isFinite(n) && n > 0 ? n : undefined;
    };
    const newSize = sizeOf(trialNewSize.value);
    const oldSize = sizeOf(trialOldSize.value);
    const resp = await trialMediaUpgradeRule({
      rule: trialRulePayload(),
      new_name: trialNewName.value.trim(),
      new_size: newSize ?? 0,
      has_new_size: newSize !== undefined,
      old_name: trialOldName.value.trim(),
      old_size: oldSize ?? 0,
      has_old_size: oldSize !== undefined,
    });
    trialResult.value = resp.item;
  } catch (err) {
    // 试算失败只清空结论，绝不保留上一次的结论：
    // 留着旧结果会让人以为「刚才那次还是这样」。
    trialResult.value = null;
    trialError.value = getApiErrorMessage(err, "试算失败");
  } finally {
    trialBusy.value = false;
  }
}

const TRIAL_RELATION_LABEL: Record<string, string> = {
  new_wins: "新版更优",
  new_loses: "新版更差",
  tie: "持平",
  no_dimension: "无可比维度",
};

const sourceOptions = computed(() => MEDIA_UPGRADE_SOURCES);
const loserActionOptions = computed(() => MEDIA_UPGRADE_LOSER_ACTIONS);

onMounted(async () => {
  await loadRules();
  await Promise.all([loadScans(), loadRecords()]);
});
</script>

<template>
  <div class="mup">
    <SettingsCard title="洗版规则" accent="var(--brand)">
      <template #head-aside>
        <AdminStatusPill :tone="globalRule?.enabled ? 'success' : 'muted'">
          {{ globalRule?.enabled ? "总开关已开启" : "总开关关闭" }}
        </AdminStatusPill>
      </template>
      <template #head-actions>
        <AppButton type="button" variant="secondary" @click="openCreateRule">新建规则</AppButton>
      </template>

      <p class="mup__danger-note">
        ⚠️
        洗版是一个会删用户文件的功能，默认关闭。扫描只做判定、不动任何文件，只有「提交」才会删或移动败方文件，且每条判定在动手前都会重新核对文件是否还是判定时那一批。
      </p>

      <AppStateBlock v-if="rulesLoading" message="加载中…" loading min-height="120px" />
      <AppStateBlock v-else-if="rulesError" :message="rulesError" min-height="120px" />

      <template v-else>
        <div class="mup__global">
          <span class="mup__global-label">全局设置（规则 ID 0）</span>
          <span class="mup__global-value">
            源：{{ sourceOptions.find((o) => o.value === globalRule?.source)?.label ?? globalRule?.source }} ·
            库目录：{{ globalRule?.library_root || "未配置" }} · 候选目录：{{ globalRule?.candidate_roots || "未配置" }} ·
            败方动作：{{ loserActionOptions.find((o) => o.value === globalRule?.loser_action)?.label ?? globalRule?.loser_action }}
          </span>
          <span v-if="globalRule?.builtin" class="mup__muted">全局设置来自系统设置页，不可直接编辑</span>
        </div>

        <AdminEmptyState
          v-if="!rules.length"
          title="没有具名规则"
          description="扫描使用全局设置即可；需要按不同媒体库分别配置时才新建具名规则。"
        />
        <div v-else class="mup__table-scroll">
          <table class="mup__table admin-table">
            <thead>
              <tr>
                <th>名称</th>
                <th>源</th>
                <th>媒体库目录</th>
                <th>适用分类</th>
                <th>败方动作</th>
                <th>每部剧上限</th>
                <th>状态</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="rule in rules" :key="rule.id">
                <td>{{ rule.name || `规则 #${rule.id}` }}</td>
                <td>{{ sourceOptions.find((o) => o.value === rule.source)?.label ?? rule.source }}</td>
                <td class="mup__cell-path">{{ rule.library_root || "-" }}</td>
                <td>{{ categoryNamesOf(rule) }}</td>
                <td>{{ loserActionOptions.find((o) => o.value === rule.loser_action)?.label ?? rule.loser_action }}</td>
                <td>{{ rule.max_records_per_series > 0 ? rule.max_records_per_series : "不限" }}</td>
                <td>
                  <AdminStatusPill :tone="rule.enabled ? 'success' : 'muted'">
                    {{ rule.enabled ? "启用" : "停用" }}
                  </AdminStatusPill>
                </td>
                <td class="mup__cell-actions">
                  <AppButton type="button" variant="secondary" size="sm" @click="openEditRule(rule)">编辑</AppButton>
                  <AppButton type="button" variant="danger" size="sm" @click="removeRule(rule)">删除</AppButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </SettingsCard>

    <SettingsCard title="扫描与提交" accent="var(--brand)">
      <template #head-actions>
        <AppSelect
          v-model="scanRuleId"
          :options="ruleChoices"
          class="mup__rule-select"
          :disabled="scanning || executingScanId > 0"
        />
        <AppButton type="button" variant="primary" :disabled="scanning || executingScanId > 0" @click="runScan">
          {{ scanning ? "扫描中…" : "扫描（不动文件）" }}
        </AppButton>
      </template>

      <div v-if="lastExecuteResult" class="mup__result">
        {{ lastExecuteResult.message }}
      </div>

      <AppStateBlock v-if="scansLoading" message="加载中…" loading min-height="140px" />
      <AdminEmptyState
        v-else-if="!scans.length"
        title="还没有扫描记录"
        description="选好规则后点「扫描」，它只会判定哪些集该换版本，不会碰任何文件。"
      />
      <div v-else class="mup__table-scroll">
        <table class="mup__table admin-table">
          <thead>
            <tr>
              <th>#</th>
              <th>时间</th>
              <th>状态</th>
              <th>库内文件</th>
              <th>候选文件</th>
              <th>判定记录</th>
              <th>新版更优</th>
              <th>跳过</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="scan in scans" :key="scan.id">
              <td>{{ scan.id }}</td>
              <td>{{ formatTime(scan.created_at) }}</td>
              <td>
                <AdminStatusPill :tone="scanStatusMeta(scan.status).tone">
                  {{ scanStatusMeta(scan.status).label }}
                </AdminStatusPill>
              </td>
              <td>{{ scan.library_files }}</td>
              <td>{{ scan.candidate_files }}</td>
              <td>{{ scan.total_records }}</td>
              <td>{{ scan.new_wins_count }}</td>
              <td>{{ scan.skipped_count }}</td>
              <td class="mup__cell-actions">
                <AppButton
                  type="button"
                  variant="primary"
                  size="sm"
                  :disabled="scan.new_wins_count <= 0 || executingScanId > 0"
                  :title="scan.new_wins_count <= 0 ? '这次扫描没有待执行的判定' : '复核快照后才会动文件'"
                  @click="executeScan(scan)"
                >
                  {{ executingScanId === scan.id ? "提交中…" : "提交" }}
                </AppButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </SettingsCard>

    <SettingsCard title="判定记录" accent="var(--brand)">
      <template #head-actions>
        <AppSelect v-model="recordStatus" :options="RELATION_OPTIONS" class="mup__filter" @update:model-value="loadRecords" />
        <AppSelect v-model="recordScanId" :options="recordStatusChoices" class="mup__filter" @update:model-value="loadRecords" />
        <AppButton type="button" variant="secondary" :disabled="recordsLoading" @click="loadRecords">刷新</AppButton>
      </template>

      <AppStateBlock v-if="recordsLoading" message="加载中…" loading min-height="140px" />
      <AdminEmptyState v-else-if="!records.length" title="没有判定记录" description="扫描之后这里会列出每一条判定及其结论。" />
      <div v-else class="mup__table-scroll">
        <table class="mup__table admin-table">
          <thead>
            <tr>
              <th>作品</th>
              <th>集号</th>
              <th>新版</th>
              <th>判定</th>
              <th>败方处理</th>
              <th>状态</th>
              <th>快照时间</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="record in records" :key="record.id">
              <tr class="mup__row" @click="toggleRecord(record.id)">
                <td>{{ record.series_title || record.series_key }}</td>
                <td>{{ record.episode_key || "-" }}</td>
                <td class="mup__cell-path">{{ record.new_file_name }}</td>
                <td>{{ relationLabel(record.quality_relation) }}</td>
                <td>
                  <span v-if="record.loser_path">{{ loserActionOptions.find((o) => o.value === record.loser_action)?.label ?? record.loser_action }}</span>
                  <span v-else class="mup__muted">-</span>
                </td>
                <td>
                  <AdminStatusPill :tone="recordStatusMeta(record.status).tone">
                    {{ recordStatusMeta(record.status).label }}
                  </AdminStatusPill>
                </td>
                <td>{{ formatTime(record.snapshot_at) }}</td>
              </tr>
              <tr v-if="expandedRecordId === record.id" class="mup__detail">
                <td colspan="7">
                  <div class="mup__detail-grid">
                    <div>
                      <span class="mup__detail-key">版本槽位</span>
                      <span>{{ record.slot_key }}</span>
                    </div>
                    <div>
                      <span class="mup__detail-key">新版大小</span>
                      <span>{{ formatSize(record.new_size) }}</span>
                    </div>
                    <div>
                      <span class="mup__detail-key">败方路径</span>
                      <span>{{ record.loser_path || "无" }}</span>
                    </div>
                    <div>
                      <span class="mup__detail-key">快照指纹</span>
                      <span>{{ record.snapshot_hash || "无" }}</span>
                    </div>
                    <div>
                      <span class="mup__detail-key">说明</span>
                      <span>{{ record.message || "-" }}</span>
                    </div>
                  </div>
                  <div v-if="oldFileNames(record).length" class="mup__detail-block">
                    <span class="mup__detail-key">判定时的库内文件</span>
                    <ul class="mup__detail-list">
                      <li v-for="name in oldFileNames(record)" :key="name">{{ name }}</li>
                    </ul>
                  </div>
                  <div v-if="record.trace" class="mup__detail-block">
                    <span class="mup__detail-key">判定依据</span>
                    <pre class="mup__trace">{{ record.trace }}</pre>
                  </div>
                  <div v-if="parseRejectReasons(record.reject_reasons).length" class="mup__detail-block">
                    <span class="mup__detail-key">驳回理由</span>
                    <ul class="mup__detail-list">
                      <li v-for="(r, i) in parseRejectReasons(record.reject_reasons)" :key="i" class="mup__reason">
                        <AdminStatusPill tone="danger">{{ rejectReasonLabel(r.code) }}</AdminStatusPill>
                        <span>{{ rejectReasonText(r) }}</span>
                      </li>
                    </ul>
                  </div>
                  <div v-if="record.rule_fingerprint" class="mup__detail-block">
                    <span class="mup__detail-key">规则指纹</span>
                    <span>{{ record.rule_fingerprint }}</span>
                  </div>
                  
                  <div v-if="parseDeleteFailures(record.delete_failures).length" class="mup__detail-block">
                    <span class="mup__detail-key mup__detail-key--danger">删除失败</span>
                    <ul class="mup__detail-list">
                      <li v-for="f in parseDeleteFailures(record.delete_failures)" :key="f.path">
                        {{ f.path }}（{{ f.stage === "batch" ? "批量接口就失败" : "降级逐条也失败" }}：{{ f.reason }}）
                      </li>
                    </ul>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </SettingsCard>

    <AppModal :open="ruleModalOpen" :title="draft.id ? '编辑洗版规则' : '新建洗版规则'" size="lg" @close="ruleModalOpen = false">
      <div class="mup__form">
        <p v-if="ruleError" class="mup__form-error">{{ ruleError }}</p>

        <FormField label="规则名称" required>
          <AppInput v-model="draft.name" placeholder="例如：4K 原盘库" />
        </FormField>

        <FormField label="扫描源">
          <AppSelect :model-value="draft.source" :options="sourceOptions" @update:model-value="draft.source = String($event)" />
          <span class="mup__hint">
            local 会直接遍历目录并直接删除本地文件；emby / jellyfin 读媒体索引，索引里的路径在本机不存在时会跳过而不是盲删。Plex 不支持洗版。
          </span>
        </FormField>

        <FormField label="媒体库根目录" required>
          <AppInput v-model="draft.library_root" placeholder="/media/tv" />
          <span class="mup__hint">只有这个目录里的文件会被判定、也只有这里的文件可能被删。</span>
          <DirRefHint :path="draft.library_root" field="媒体库根目录" />
        </FormField>

        <FormField label="候选目录">
          <AppInput v-model="draft.candidate_roots" placeholder="/downloads/tv, /downloads/movie" />
          <span class="mup__hint">多个目录用逗号或换行分隔。不填则只用媒体库根目录（库内已有更好版本时也能收敛重复文件）。</span>
          <DirRefHint :path="firstCandidateRoot(draft.candidate_roots)" field="候选目录（第一条）" />
        </FormField>

        <div class="mup__form-row">
          <FormField label="最低分辨率">
            <AppInput
              :model-value="draft.min_resolution"
              type="number"
              placeholder="0"
              @update:model-value="draft.min_resolution = toInt($event)"
            />
          </FormField>
          <FormField label="最低声道数">
            <AppInput
              :model-value="draft.min_channels"
              type="number"
              placeholder="0"
              @update:model-value="draft.min_channels = toInt($event)"
            />
          </FormField>
          <FormField label="每部剧记录上限">
            <AppInput
              :model-value="draft.max_records_per_series"
              type="number"
              placeholder="0"
              @update:model-value="draft.max_records_per_series = toInt($event)"
            />
          </FormField>
        </div>
        <span class="mup__hint">
          三个数值都是 0 表示不限制。每部剧上限用来防止银魂这类几百集的长剧一次性刷出几百条判定。
        </span>

        <FormField label="要求字幕">
          <SettingsBoolSegment v-model="draft.require_subtitle" label="要求字幕" />
          <span class="mup__hint">
            仅从文件名里的「字幕 / 中字 / 简繁 / chs / cht / Big5 / zh-CN」等标记推断，本仓没有做媒体探测，所以这是推测值而不是可靠检测。
          </span>
        </FormField>

        <FormField label="败方动作" required>
          <AppSelect
            :model-value="draft.loser_action"
            :options="loserActionOptions"
            @update:model-value="draft.loser_action = String($event)"
          />
          <span class="mup__hint">
            败方=被替换掉的旧文件。默认「保留」不会删任何文件；选「删除」或「移动」才会动文件。不同版本槽位（分辨率/编码/制作组/音轨/字幕/容器任一不同）按设计两个都保留，不走这个动作。
          </span>
        </FormField>

        <FormField v-if="draft.loser_action === 'move'" label="移动目标目录" required>
          <AppInput v-model="draft.move_dir" placeholder="/media/archived" />
          <span class="mup__hint">败方文件移到这里。这个目录不在媒体库内的话，移过去之后文件不会被删除，但也不会被 Emby 扫到。</span>
          <DirRefHint :path="draft.move_dir" field="移动目标目录" />
        </FormField>

        <FormField label="制作组优先级">
          <AppInput v-model="draft.group_priority" placeholder="HDSPTV, BeAst, CHD" />
          <span class="mup__hint">从左到右优先级递减，仅在各项质量持平时用来分胜负。留空则只比质量。</span>
        </FormField>

        <FormField label="自定义洗版规则（JSON，可留空）">
          <AppInput v-model="draft.wash_rules" placeholder="留空使用默认：分辨率 → 编码 → 格式 → 声道 → 色深 → 制作组" />
        </FormField>

        <FormField label="适用分类">
          <div class="mup__cats">
            <p class="mup__muted">
              按分类目录名匹配媒体库里的文件。不勾任何分类即扫描全部媒体库；勾一级分类会连同名二级一起带上。
            </p>
            <p v-if="categoriesLoading" class="mup__muted">正在加载分类清单…</p>
            <p v-else-if="!categoryGroups.length" class="mup__muted">
              当前分类模板里没有配置任何分类，或分类清单加载失败。不选即按全部媒体库扫描。
            </p>
            <div v-for="group in categoryGroups" :key="group.level" class="mup__cats-group">
              <span class="mup__cats-label">{{ group.label }}</span>
              <div class="mup__cats-items">
                <label
                  v-for="opt in group.options"
                  :key="`${group.level}:${String(opt.value)}`"
                  class="mup__chip"
                >
                  <input
                    type="checkbox"
                    :checked="categorySelected(String(opt.value))"
                    @change="toggleCategory(String(opt.value), group.level)"
                  />
                  <span>{{ opt.label }}</span>
                </label>
              </div>
            </div>
          </div>
        </FormField>

        <FormField label="启用该规则">
          <SettingsBoolSegment v-model="draft.enabled" label="启用该规则" />
        </FormField>

        <!-- T31 · 规则试算 -->
        <div class="mup__trial">
          <div class="mup__trial-head">
            <span class="mup__detail-key">规则试算</span>
            <span class="mup__hint">
              填两个文件名看这份规则会怎么判。只按文件名与体积算，纯只读不落库，也不碰网盘；
              需要媒体探测才能得到的维度（真实码率、时长等）不会猜，界面标为「跳过」。
            </span>
          </div>
          <div class="mup__trial-grid">
            <FormField label="新文件名">
              <AppInput v-model="trialNewName" placeholder="剧名.S01E01.2160p.x265.TrueHD.中字.mkv" />
            </FormField>
            <FormField label="新文件体积（字节，可留空）">
              <AppInput v-model="trialNewSize" placeholder="留空则体积不参与比较" />
            </FormField>
            <FormField label="现版文件名">
              <AppInput v-model="trialOldName" placeholder="剧名.S01E01.2160p.x265.10bit.mkv" />
            </FormField>
            <FormField label="现版体积（字节，可留空）">
              <AppInput v-model="trialOldSize" placeholder="留空则体积不参与比较" />
            </FormField>
          </div>
          <div class="mup__trial-actions">
            <AppButton type="button" variant="secondary" :disabled="trialBusy" @click="runTrial">
              {{ trialBusy ? "试算中…" : "试算" }}
            </AppButton>
            <span v-if="trialError" class="mup__form-error">{{ trialError }}</span>
          </div>
          <div v-if="trialResult" class="mup__trial-result">
            <div class="mup__trial-summary">
              <AdminStatusPill :tone="trialResult.relation === 'new_wins' ? 'success' : trialResult.relation === 'new_loses' ? 'danger' : 'muted'">
                {{ TRIAL_RELATION_LABEL[trialResult.relation] ?? trialResult.relation }}
              </AdminStatusPill>
              <span class="mup__trace">{{ trialResult.trace }}</span>
            </div>
            <div v-if="trialResult.gate_reasons.length" class="mup__trial-gate">
              <span class="mup__detail-key mup__detail-key--danger">候选未达规则门槛</span>
              <ul class="mup__detail-list">
                <li v-for="(r, i) in trialResult.gate_reasons" :key="`g${i}`" class="mup__reason">
                  <AdminStatusPill tone="danger">{{ rejectReasonLabel(r.code) }}</AdminStatusPill>
                  <span>{{ rejectReasonText(r) }}</span>
                </li>
              </ul>
              <span class="mup__hint">没过门槛的候选在真实扫描里不会进入比较，所以这类理由只在试算里看得到。</span>
            </div>
            <div v-if="trialResult.dimensions.length" class="mup__trial-dims">
              <span class="mup__detail-key">逐维度</span>
              <table class="mup__trial-table">
                <thead>
                  <tr><th>维度</th><th>新</th><th>现版</th><th>结论</th></tr>
                </thead>
                <tbody>
                  <tr v-for="d in trialResult.dimensions" :key="d.Field">
                    <td>{{ d.Label || d.Field }}</td>
                    <td>{{ d.New || "跳过" }}</td>
                    <td>{{ d.Old || "跳过" }}</td>
                    <td>
                      <AdminStatusPill :tone="d.Better ? 'success' : d.Worse ? 'danger' : 'muted'">
                        {{ d.Better ? "新优" : d.Worse ? "新差" : "同档" }}
                      </AdminStatusPill>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div class="mup__hint">规则指纹 {{ trialResult.rule_fingerprint }}</div>
          </div>
        </div>
      </div>
      <template #footer>
        <AppButton type="button" variant="cancel" @click="ruleModalOpen = false">取消</AppButton>
        <AppButton type="button" variant="primary" :disabled="ruleSaving" @click="saveRule">
          {{ ruleSaving ? "保存中…" : "保存" }}
        </AppButton>
      </template>
    </AppModal>
  </div>
</template>

<style scoped>
.mup {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding-bottom: 24px;
}

.mup__danger-note {
  margin: 0 0 12px;
  padding: 10px 12px;
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--warning) 10%, transparent);
  color: var(--warning);
  font-size: 13px;
  line-height: 1.6;
}

.mup__global {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding-bottom: 12px;
  margin-bottom: 12px;
  border-bottom: 1px solid var(--border-soft);
  font-size: 13px;
}

.mup__global-label {
  font-weight: 600;
  color: var(--text);
}

.mup__global-value {
  color: var(--text-regular);
  word-break: break-all;
}

.mup__muted,
.mup__hint {
  color: var(--text-muted);
  font-size: 12px;
  line-height: 1.6;
}

.mup__table-scroll {
  overflow-x: auto;
}

.mup__table {
  width: 100%;
}

.mup__cell-path {
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 分类多选：用 chip 而不是下拉，是因为二级分类动辄十几个，
   下拉里要勾十几个框还得按住 Ctrl，用户多半会以为只能选一个。 */
.mup__cats {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.mup__cats-group {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}
.mup__cats-label {
  flex: 0 0 auto;
  min-width: 32px;
  padding-top: 4px;
  color: var(--text-muted);
  font-size: 12px;
}
.mup__cats-items {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.mup__chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border: 1px solid var(--border-color, var(--border));
  border-radius: 999px;
  font-size: 12px;
  cursor: pointer;
  user-select: none;
}
.mup__chip:hover {
  border-color: var(--brand);
}
.mup__chip input {
  margin: 0;
  accent-color: var(--brand);
}

.mup__cell-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}

.mup__row {
  cursor: pointer;
}

.mup__detail > td {
  background: var(--surface-sunken);
}

.mup__detail-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 8px 16px;
  font-size: 12px;
  color: var(--text-regular);
  word-break: break-all;
}

.mup__detail-key {
  display: block;
  color: var(--text-muted);
}

.mup__detail-key--danger {
  color: var(--danger);
}

.mup__detail-block {
  margin-top: 10px;
  font-size: 12px;
  color: var(--text-regular);
}

.mup__detail-list {
  margin: 4px 0 0;
  padding-left: 18px;
}

.mup__trace {
  margin: 4px 0 0;
  padding: 8px;
  border-radius: var(--radius-sm);
  background: var(--surface);
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
}

.mup__reason {
  display: flex;
  align-items: center;
  gap: 8px;
}

.mup__trial {
  margin-top: 12px;
  padding: 12px;
  border: 1px solid var(--border-soft);
  border-radius: var(--radius-md);
  background: var(--surface-sunken);
}

.mup__trial-head {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 10px;
}

.mup__trial-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.mup__trial-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 10px;
}

.mup__trial-result {
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--border-soft);
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.mup__trial-summary {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}

.mup__trial-gate {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.mup__trial-dims {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.mup__trial-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}

.mup__trial-table th,
.mup__trial-table td {
  padding: 6px 8px;
  border-bottom: 1px solid var(--border-soft);
  text-align: left;
}

@media (max-width: 720px) {
  .mup__trial-grid {
    grid-template-columns: 1fr;
  }
}

.mup__result {
  margin-bottom: 12px;
  padding: 10px 12px;
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--brand) 10%, transparent);
  color: var(--text-regular);
  font-size: 13px;
  line-height: 1.6;
}

.mup__filter {
  min-width: 140px;
}

.mup__rule-select {
  min-width: 160px;
}

.mup__form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.mup__form-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 12px;
}

.mup__form-error {
  margin: 0;
  padding: 8px 10px;
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--danger) 10%, transparent);
  color: var(--danger);
  font-size: 13px;
}
</style>