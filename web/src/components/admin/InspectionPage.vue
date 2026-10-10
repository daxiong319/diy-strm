<script setup lang="ts">
// 目录巡检页（T15）。
//
// 这个页面的全部设计都围绕一条：**系统不替用户做删除决定**。
//
//   - 扫描一个文件都不碰，只产出结论；
//   - 每一条结论都自带一句「如果执行会发生什么」，这句话由后端生成
//     （后端才有路径、体积、修复参数），前端原样显示，不自己拼；
//   - 勾选修复 → 弹出确认框，逐条列出将要发生的事 → 确认后才带上
//     preview_ack 提交。后端在 preview_ack 为 false 时直接拒绝，
//     所以「必须先预览」不是这个页面的自觉，而是接口的硬要求。
//
// 危险动作（删目录、改名）与安全动作（删一条索引关联）分开提示：
// 两者的可逆性不同，混在一句「确认执行？」里就等于没提示。
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  fetchInspectionCheckers,
  fetchInspectionPreview,
  INSPECTION_DESTRUCTIVE_KINDS,
  repairInspection,
  scanInspection,
  type InspectionChecker,
  type InspectionExecuteResult,
  type InspectionPreviewLine,
  type InspectionScanReport,
} from "@/api/inspection";
import AppButton from "@/components/base/AppButton.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import AdminStatusPill from "@/components/admin/AdminStatusPill.vue";
import type { AdminStatusPillTone } from "@/components/admin/AdminStatusPill.vue";
import { useConfirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import { formatTime } from "@/utils/format";
import "@/styles/admin-table.css";

const { showConfirm } = useConfirm();

const registeredCheckers = ref<{ key: string; label: string }[]>([]);
const report = ref<InspectionScanReport | null>(null);
const lines = ref<InspectionPreviewLine[]>([]);
const selected = ref<Set<string>>(new Set());
const lastResults = ref<InspectionExecuteResult[]>([]);
const scanning = ref(false);
const repairing = ref(false);
const loadError = ref("");
// abort 让「停止扫描」真的能停：一次巡检要走完网盘清单，
// 大的库跑几分钟是正常的，光靠按钮 disabled 会让人干等。
let abort: AbortController | null = null;

const snapshotId = computed(() => report.value?.snapshot_id ?? "");

// 快照是一次性的：执行之后服务端就把这份快照作废了，页面必须跟着作废。
// 不跟着清的话，用户会看着一份已经过期的预览继续勾选，而下一次提交会
// 撞上「该巡检快照已被执行」。
const snapshotDead = ref(false);

const previewLines = computed(() => (snapshotDead.value ? [] : lines.value));

const fixableLines = computed(() => previewLines.value.filter((l) => l.repair && l.repair_label !== "仅报告，不自动处理"));

const selectedLines = computed(() => previewLines.value.filter((l) => selected.value.has(l.finding_id)));

const selectedDestructive = computed(() => selectedLines.value.filter((l) => isDestructive(l)));

const canRepair = computed(() => selected.value.size > 0 && !repairing.value && !snapshotDead.value);

const checkerByKey = computed(() => {
  const map = new Map<string, InspectionChecker>();
  for (const c of report.value?.checkers ?? []) map.set(c.key, c);
  return map;
});

function isDestructive(line: InspectionPreviewLine): boolean {
  return !line.reversible && INSPECTION_DESTRUCTIVE_KINDS.has(line.kind);
}

function lineTone(line: InspectionPreviewLine): AdminStatusPillTone {
  if (isDestructive(line)) return "danger";
  if (!line.reversible) return "muted";
  return "success";
}

function toggle(id: string) {
  const next = new Set(selected.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  selected.value = next;
}

function toggleAll() {
  selected.value = new Set(fixableLines.value.map((l) => l.finding_id));
}

async function loadCheckers() {
  try {
    const res = await fetchInspectionCheckers();
    registeredCheckers.value = res.checkers ?? [];
  } catch (e) {
    loadError.value = getApiErrorMessage(e, "读取巡检项失败");
  }
}

async function runScan() {
  scanning.value = true;
  snapshotDead.value = false;
  lastResults.value = [];
  selected.value = new Set();
  abort = new AbortController();
  try {
    const res = await scanInspection(abort.signal);
    report.value = res;
    lines.value = res.preview ?? [];
    // 用后端在扫描时存下的那一份，不额外再拉一次：扫描那一刻的预览
    // 就是和这批结论同一份快照，避免两次调用之间目录变化造成
    // 「勾选的是扫描结果，执行的是另一份预览」。
    toast.success(res.total > 0 ? `扫描完成，发现 ${res.total} 项` : "扫描完成，没有发现问题");
  } catch (e) {
    report.value = null;
    lines.value = [];
    if ((e as Error)?.name === "AbortError") return;
    toast.error(getApiErrorMessage(e, "巡检扫描失败"));
  } finally {
    scanning.value = false;
    abort = null;
  }
}

function stopScan() {
  abort?.abort();
}

async function reloadPreview() {
  if (!snapshotId.value) return;
  try {
    const res = await fetchInspectionPreview(snapshotId.value);
    lines.value = res.preview ?? [];
  } catch {
    // 拿不到就如实作废，而不是显示一份「看起来还在」的旧预览 ——
    // 用户对着过期预览勾选，提交时才知道过期，那体验最差。
    snapshotDead.value = true;
    selected.value = new Set();
    toast.warning("这份巡检结果已过期或已被执行，请重新扫描");
  }
}

async function repair() {
  if (!canRepair.value) return;
  const ids = Array.from(selected.value);
  const destructive = selectedDestructive.value;
  const reversible = selectedLines.value.filter((l) => !isDestructive(l));
  try {
    await showConfirm({
      title: destructive.length ? "执行修复（包含不可撤销的动作）" : "执行修复",
      message: [
        `将按本次扫描的结果处理 ${ids.length} 项：`,
        ...selectedLines.value.map((l) => `· ${l.repair}`),
      ].join("\n"),
      hint: destructive.length
        ? `其中 ${destructive.length} 项不可撤销：删除目录或改名。${
            reversible.length ? `另外 ${reversible.length} 项可逆。` : ""
          }执行后这份巡检结果会作废，需要重新扫描才能继续。`
        : "本次执行的动作都可逆或只影响索引记录，不动媒体文件内容。执行后这份巡检结果会作废。",
      icon: destructive.length ? "trash" : "info",
      confirmText: destructive.length ? "确认执行" : "确认",
      danger: destructive.length > 0,
      checkboxLabel: destructive.length ? "我已逐条看过上面的预览，确认这些目录/文件名可以这样动" : undefined,
    });
  } catch {
    return;
  }
  repairing.value = true;
  try {
    const res = await repairInspection(snapshotId.value, ids, true);
    lastResults.value = res.results ?? [];
    const ok = lastResults.value.filter((r) => r.ok).length;
    const failed = lastResults.value.length - ok;
    if (failed > 0) toast.warning(`成功 ${ok} 项，失败 ${failed} 项（详见下方明细）`);
    else toast.success(`已完成 ${ok} 项`);
    // 服务端已把这份快照作废了，页面立刻同步：
    // 留着继续勾选只会让下一次提交撞上「已被执行」。
    snapshotDead.value = true;
    selected.value = new Set();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "执行修复失败"));
  } finally {
    repairing.value = false;
  }
}

onMounted(loadCheckers);
</script>

<template>
  <div class="inspection-page">
    <header class="inspection-page__head">
      <div>
        <h2>目录巡检</h2>
        <p class="inspection-page__sub">
          扫描只读，一个文件都不会动。修复必须先看预览，确认后才会执行；执行完这份结果就作废，需要重新扫描。
        </p>
      </div>
      <div class="inspection-page__actions">
        <AppButton v-if="!scanning" variant="primary" @click="runScan">开始扫描</AppButton>
        <template v-else>
          <AppButton variant="secondary" disabled>扫描中…</AppButton>
          <AppButton variant="ghost" @click="stopScan">停止</AppButton>
        </template>
      </div>
    </header>

    <AppStateBlock v-if="loadError" type="error" :message="loadError" />

    <section v-if="registeredCheckers.length" class="inspection-page__checkers">
      <span class="inspection-page__label">巡检范围</span>
      <AdminStatusPill
        v-for="c in registeredCheckers"
        :key="c.key"
        tone="muted"
      >
        {{ c.label }}
        <template v-if="checkerByKey.get(c.key)">
          · {{ checkerByKey.get(c.key)!.findings }} 项
        </template>
        <template v-else>· 未扫描</template>
      </AdminStatusPill>
    </section>

    <section v-if="report" class="inspection-page__report">
      <div class="inspection-page__report-head">
        <span>
          共 {{ report.total }} 项发现
          <span v-if="report.scanned_at" class="inspection-page__time">（{{ formatTime(report.scanned_at) }}）</span>
        </span>
        <AppButton variant="ghost" :disabled="!snapshotId || snapshotDead" @click="reloadPreview">
          刷新预览
        </AppButton>
      </div>
      <ul v-if="report.checkers?.some((c) => c.error)" class="inspection-page__errors">
        <li v-for="c in report.checkers.filter((c) => c.error)" :key="c.key">
          <strong>{{ c.label }}</strong>：{{ c.error }}
          <span class="inspection-page__error-hint">（这一项本轮没查成，其余项的结论仍然有效）</span>
        </li>
      </ul>
    </section>

    <section v-if="snapshotDead" class="inspection-page__dead">
      这份巡检结果已经作废（已执行或已过期），下面的预览不可再用于执行。
      <AppButton variant="secondary" @click="runScan">重新扫描</AppButton>
    </section>

    <AdminEmptyState
      v-else-if="!report"
      icon="search"
      title="还没有扫描结果"
      description="巡检会遍历整理根目录下的全部文件，与索引、TMDB 对照。范围来自整理任务配置的「目标根」，没配目标根的任务不会被巡检。"
    />

    <template v-else-if="previewLines.length">
      <div class="inspection-page__toolbar">
        <label class="inspection-page__selectall">
          <input
            type="checkbox"
            :checked="selected.size > 0 && selected.size === fixableLines.length"
            :disabled="!fixableLines.length"
            @change="toggleAll"
          />
          全选可修复项（{{ fixableLines.length }}）
        </label>
        <span v-if="selectedDestructive.length" class="inspection-page__danger-hint">
          含 {{ selectedDestructive.length }} 项不可撤销动作
        </span>
        <AppButton variant="primary" :disabled="!canRepair" @click="repair">
          执行选中的 {{ selected.size }} 项
        </AppButton>
      </div>

      <table class="admin-table">
        <thead>
          <tr>
            <th class="inspection-page__col-check"></th>
            <th>巡检项</th>
            <th>对象</th>
            <th>详情</th>
            <th>修复动作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="line in previewLines" :key="line.finding_id">
            <td class="inspection-page__col-check">
              <input
                type="checkbox"
                :checked="selected.has(line.finding_id)"
                :disabled="!line.repair || line.repair_label === '仅报告，不自动处理'"
                @change="toggle(line.finding_id)"
              />
            </td>
            <td>{{ registeredCheckers.find((c) => c.key === line.checker_key)?.label ?? line.checker_key }}</td>
            <td class="inspection-page__col-target">{{ line.target }}</td>
            <td class="inspection-page__col-detail">{{ line.detail }}</td>
            <td>
              <AdminStatusPill :tone="lineTone(line)">
                {{ line.repair_label || "仅报告" }}
              </AdminStatusPill>
              <p class="inspection-page__preview">{{ line.repair }}</p>
            </td>
          </tr>
        </tbody>
      </table>
    </template>

    <AdminEmptyState
      v-else
      icon="check-circle"
      title="没有发现问题"
      description="整理根目录下的文件、索引与 TMDB 编号都对得上。"
    />

    <section v-if="lastResults.length" class="inspection-page__results">
      <h3>上一次执行结果</h3>
      <ul>
        <li v-for="r in lastResults" :key="r.finding_id">
          <AdminStatusPill :tone="r.ok ? 'success' : 'danger'">{{ r.ok ? "成功" : "失败" }}</AdminStatusPill>
          <span class="inspection-page__col-target">{{ r.target }}</span>
          <span>{{ r.message }}</span>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.inspection-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.inspection-page__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}
.inspection-page__head h2 {
  margin: 0 0 4px;
  font-size: 18px;
  color: var(--text);
}
.inspection-page__sub {
  margin: 0;
  max-width: 720px;
  font-size: 13px;
  line-height: 1.7;
  color: var(--text-muted);
}
.inspection-page__actions {
  display: flex;
  gap: 8px;
}
.inspection-page__checkers,
.inspection-page__toolbar,
.inspection-page__report-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.inspection-page__label,
.inspection-page__time,
.inspection-page__error-hint {
  font-size: 12px;
  color: var(--text-muted);
}
.inspection-page__danger-hint {
  font-size: 12px;
  color: var(--danger);
}
.inspection-page__errors {
  margin: 8px 0 0;
  padding-left: 18px;
  font-size: 12px;
  line-height: 1.8;
  color: var(--text-muted);
}
.inspection-page__dead {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 12px 14px;
  border: 1px solid var(--border-soft);
  border-radius: var(--radius-md);
  font-size: 13px;
  color: var(--text-muted);
}
.inspection-page__col-check {
  width: 36px;
}
.inspection-page__col-target {
  max-width: 320px;
  word-break: break-all;
  color: var(--text-muted);
}
.inspection-page__col-detail {
  max-width: 320px;
  font-size: 12px;
  color: var(--text-muted);
}
.inspection-page__preview {
  margin: 6px 0 0;
  font-size: 12px;
  line-height: 1.7;
  color: var(--text-muted);
}
.inspection-page__selectall {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
}
.inspection-page__results ul {
  margin: 8px 0 0;
  padding-left: 0;
  list-style: none;
}
.inspection-page__results li {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 6px 0;
  font-size: 13px;
}
</style>
