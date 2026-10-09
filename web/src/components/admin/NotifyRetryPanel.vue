<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  clearNotifyRetries,
  fetchNotifyRetries,
  redriveNotifyRetry,
  type NotifyRetryEntry,
  type NotifyRetryStatus,
} from "@/api/notifyScenes";
import AppBadge from "@/components/base/AppBadge.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import { useConfirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import "@/styles/admin-table.css";

/**
 * T12 · Webhook 补发队列（outbox）可视面板。
 *
 * 只对 webhook 渠道生效：别的渠道（Telegram / Bark 等）投递失败就是失败，
 * 不进队列 —— 那里重发一万次也是同样的结果，不如当场报错。
 */

const props = withDefaults(
  defineProps<{
    accent?: string;
  }>(),
  { accent: "var(--brand)" },
);

const { showConfirm } = useConfirm();

const STATUS_OPTIONS: Array<{ value: string; label: string }> = [
  { value: "", label: "全部状态" },
  { value: "pending", label: "等待重试" },
  { value: "failed", label: "已失败" },
  { value: "sent", label: "已补发" },
];

const STATUS_LABELS: Record<NotifyRetryStatus, string> = {
  pending: "等待重试",
  failed: "已失败",
  sent: "已补发",
};

const STATUS_TONES: Record<NotifyRetryStatus, "info" | "danger" | "success"> = {
  pending: "info",
  failed: "danger",
  sent: "success",
};

const loading = ref(false);
const errorMsg = ref("");
const entries = ref<NotifyRetryEntry[]>([]);
const counts = ref<Record<NotifyRetryStatus, number>>({ pending: 0, sent: 0, failed: 0 });
const maxAttempts = ref(0);
const total = ref(0);
const statusFilter = ref("");
const page = ref(1);
const pageSize = 30;
const offset = ref(0);
const redriving = ref(0);
const clearing = ref(false);
const detail = ref<NotifyRetryEntry | null>(null);
const detailOpen = ref(false);

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)));

function formatTime(v?: string): string {
  if (!v) return "-";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return v;
  return d.toLocaleString();
}

/** 退避档位（1m/5m/30m/2h/12h）是后端定的，前端只做展示，不自己算 */
function backoffHint(e: NotifyRetryEntry): string {
  if (e.status !== "pending") return "—";
  if (!e.next_retry_at) return "即将重试";
  const at = new Date(e.next_retry_at).getTime();
  if (Number.isNaN(at)) return e.next_retry_at;
  const diff = at - Date.now();
  if (diff <= 0) return "即将重试";
  if (diff < 60_000) return `${Math.ceil(diff / 1000)} 秒后`;
  if (diff < 3_600_000) return `${Math.ceil(diff / 60_000)} 分钟后`;
  return `${Math.ceil(diff / 3_600_000)} 小时后`;
}

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    const res = await fetchNotifyRetries({
      status: statusFilter.value || undefined,
      limit: pageSize,
      offset: offset.value,
    });
    entries.value = res.items ?? [];
    total.value = res.total ?? 0;
    counts.value = res.counts ?? { pending: 0, sent: 0, failed: 0 };
    maxAttempts.value = res.max_attempts ?? 0;
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载补发队列失败");
    entries.value = [];
    total.value = 0;
  } finally {
    loading.value = false;
  }
}

function filterChanged() {
  page.value = 1;
  offset.value = 0;
  void load();
}

function nextPage() {
  if (page.value < totalPages.value) {
    page.value++;
    offset.value = (page.value - 1) * pageSize;
    void load();
  }
}

function prevPage() {
  if (page.value > 1) {
    page.value--;
    offset.value = (page.value - 1) * pageSize;
    void load();
  }
}

function openDetail(e: NotifyRetryEntry) {
  detail.value = e;
  detailOpen.value = true;
}

/** 手动重投：后端会立刻跑一轮补发并返回最新状态 */
async function handleRedrive(e: NotifyRetryEntry) {
  try {
    await showConfirm({
      title: "重新投递",
      message: `将「${e.title || `补发 #${e.id}`}」重置为待投递并立即发一次。若对方仍未恢复，会重新开始退避。`,
      icon: "info",
      confirmText: "重投",
    });
  } catch {
    // showConfirm 在关闭弹窗时 reject；不 return 会把"取消"当成"确认"继续投。
    return;
  }
  redriving.value = e.id;
  try {
    const res = await redriveNotifyRetry(e.id);
    const updated = res.item;
    if (updated?.status === "sent") {
      toast.success("补发成功");
    } else if (updated?.status === "failed") {
      toast.error(`重投仍失败：${updated.last_error || "对方未接受"}`);
    } else {
      toast.info("已重新进入等待队列");
    }
    await load();
  } catch (e2) {
    toast.error(getApiErrorMessage(e2, "重投失败"));
  } finally {
    redriving.value = 0;
  }
}

/** 清理：默认清当前筛选的状态；筛选为"全部状态"时清全部 */
async function handleClear() {
  const scope = statusFilter.value ? `「${STATUS_LABELS[statusFilter.value as NotifyRetryStatus]}」` : "全部状态的";
  try {
    await showConfirm({
      title: "清理补发记录",
      message: `确定清理${scope}补发记录吗？此操作不可恢复。`,
      icon: "warning",
      confirmText: "清理",
      danger: true,
    });
  } catch {
    return;
  }
  clearing.value = true;
  try {
    const res = await clearNotifyRetries(statusFilter.value || undefined);
    toast.success(`已清理 ${res.cleared ?? 0} 条补发记录`);
    page.value = 1;
    offset.value = 0;
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "清理失败"));
  } finally {
    clearing.value = false;
  }
}

onMounted(load);

defineExpose({ reload: load });
</script>

<template>
  <SettingsCard title="通知补发队列" :accent="props.accent">
    <template #head-aside>
      <div class="nr__stats">
        <AppBadge tone="info">等待 {{ counts.pending ?? 0 }}</AppBadge>
        <AppBadge tone="danger">失败 {{ counts.failed ?? 0 }}</AppBadge>
        <AppBadge tone="success">已补发 {{ counts.sent ?? 0 }}</AppBadge>
      </div>
    </template>

    <p class="nr__meta">
      自定义 Webhook 投递失败的通知会进入这里，按 1 分钟 / 5 分钟 / 30 分钟 / 2 小时 / 12 小时
      五档退避重发（共 {{ maxAttempts || 5 }} 次）；用尽后标记为「已失败」，可在下方手动重投。
      Telegram 等非 Webhook 渠道不进入补发队列。
    </p>

    <div class="nr__toolbar">
      <AppSelect
        v-model="statusFilter"
        :options="STATUS_OPTIONS"
        class="nr__filter"
        @update:model-value="filterChanged"
      />
      <AppButton type="button" variant="secondary" :disabled="loading" @click="load">刷新</AppButton>
      <AppButton type="button" variant="danger" :disabled="clearing || !total" @click="handleClear">
        清理
      </AppButton>
    </div>

    <AppStateBlock v-if="loading" message="加载中…" loading min-height="200px" />
    <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="200px" />

    <div v-else-if="entries.length" class="nr__table-scroll">
      <table class="nr__table admin-table">
        <thead>
          <tr>
            <th>标题</th>
            <th>场景</th>
            <th>渠道</th>
            <th>状态</th>
            <th>尝试</th>
            <th>下次重试</th>
            <th>最近错误</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in entries" :key="e.id" class="nr__row">
            <td>
              <span class="nr__clamp" :title="`${e.title}\n${e.content}`" @click="openDetail(e)">
                {{ e.title || `#${e.id}` }}
              </span>
            </td>
            <td>
              <span class="nr__muted">{{ e.scene_label || e.scene || "—" }}</span>
            </td>
            <td>
              <span class="nr__clamp" :title="e.target_url">{{ e.channel_name || e.channel_type }}</span>
            </td>
            <td>
              <AppBadge :tone="STATUS_TONES[e.status]">{{ STATUS_LABELS[e.status] ?? e.status }}</AppBadge>
            </td>
            <td class="nr__muted">{{ e.attempts }} / {{ e.max_attempts || maxAttempts }}</td>
            <td class="nr__muted">{{ formatTime(e.next_retry_at) }}<br /><small>{{ backoffHint(e) }}</small></td>
            <td>
              <span class="nr__clamp nr__muted" :title="e.last_error">{{ e.last_error || "—" }}</span>
            </td>
            <td>
              <div class="nr__actions">
                <AppButton
                  type="button"
                  variant="secondary"
                  :disabled="!e.redrivable || redriving === e.id"
                  :title="e.redrivable ? '重置退避并立即重发一次' : '仅失败记录可重投'"
                  @click="handleRedrive(e)"
                >
                  重投
                </AppButton>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <AppStateBlock v-else message="暂无补发记录" min-height="200px" />

    <div v-if="totalPages > 1 && entries.length" class="nr__pager">
      <AppButton type="button" variant="secondary" :disabled="page <= 1 || loading" @click="prevPage">
        上一页
      </AppButton>
      <span class="nr__page-info">{{ page }} / {{ totalPages }}（共 {{ total }} 条）</span>
      <AppButton
        type="button"
        variant="secondary"
        :disabled="page >= totalPages || loading"
        @click="nextPage"
      >
        下一页
      </AppButton>
    </div>

    <AppModal :open="detailOpen" size="account" title="补发详情" @close="detailOpen = false">
      <div v-if="detail" class="nr__detail">
        <div class="nr__detail-row">
          <span class="nr__detail-key">标题</span>
          <span>{{ detail.title || "—" }}</span>
        </div>
        <div class="nr__detail-row">
          <span class="nr__detail-key">正文</span>
          <pre class="nr__detail-pre">{{ detail.content || "—" }}</pre>
        </div>
        <div class="nr__detail-row">
          <span class="nr__detail-key">目标地址</span>
          <span class="nr__detail-pre">{{ detail.target_url || "—" }}</span>
        </div>
        <div class="nr__detail-row">
          <span class="nr__detail-key">最近错误</span>
          <pre class="nr__detail-pre">{{ detail.last_error || "—" }}</pre>
        </div>
        <div class="nr__detail-row">
          <span class="nr__detail-key">尝试次数</span>
          <span>{{ detail.attempts }} / {{ detail.max_attempts || maxAttempts }}</span>
        </div>
        <div class="nr__detail-row">
          <span class="nr__detail-key">入队时间</span>
          <span>{{ formatTime(detail.created_at) }}</span>
        </div>
      </div>
    </AppModal>
  </SettingsCard>
</template>

<style scoped>
.nr__stats {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.nr__meta {
  margin: 0 0 4px;
  padding-bottom: 12px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--text-muted);
  border-bottom: 1px solid var(--border-soft);
}

.nr__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin: 12px 0;
}

.nr__filter {
  min-width: 140px;
}

.nr__table-scroll {
  overflow-x: auto;
  margin: 0 -22px;
  padding-bottom: 16px;
}

.nr__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.nr__table th,
.nr__table td {
  padding: 12px 22px;
  text-align: left;
  vertical-align: middle;
  border-bottom: 1px solid var(--border-soft);
}

.nr__table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  white-space: nowrap;
}

.nr__table tbody tr:last-child td {
  border-bottom: none;
}

.nr__row {
  transition: background-color 0.18s ease;
}

.nr__row:hover {
  background: color-mix(in srgb, var(--brand) 4%, transparent);
}

.nr__clamp {
  display: block;
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
}

.nr__muted {
  color: var(--text-muted);
  font-size: 12px;
}

.nr__actions {
  display: flex;
  justify-content: center;
  gap: 8px;
}

.nr__pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  padding-top: 8px;
}

.nr__page-info {
  font-size: 12px;
  color: var(--text-muted);
}

.nr__detail {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.nr__detail-row {
  display: grid;
  grid-template-columns: 80px minmax(0, 1fr);
  gap: 10px;
  font-size: 13px;
  line-height: 1.6;
}

.nr__detail-key {
  color: var(--text-muted);
}

.nr__detail-pre {
  margin: 0;
  padding: 8px 10px;
  border-radius: var(--radius-sm);
  background: var(--surface-sunken);
  font-family: inherit;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>