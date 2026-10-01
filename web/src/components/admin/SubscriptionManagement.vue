<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  deleteSubscription,
  fetchSubscriptionEvents,
  fetchSubscriptionItems,
  fetchSubscriptionRuns,
  fetchSubscriptions,
  providerLabel,
  runDueSubscriptions,
  runSubscription,
  toggleSubscription,
  type DiscoverySubscription,
  type SubscriptionEvent,
  type SubscriptionItem,
  type SubscriptionRun,
} from "@/api/discovery";
import AppBadge from "@/components/base/AppBadge.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import AdminEnableToggle from "@/components/admin/AdminEnableToggle.vue";
import AdminRowActions from "@/components/admin/AdminRowActions.vue";
import AdminTableActionBtn from "@/components/admin/AdminTableActionBtn.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import { useConfirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import "@/styles/admin-table.css";

const { showConfirm } = useConfirm();

const loading = ref(false);
const errorMsg = ref("");
const records = ref<DiscoverySubscription[]>([]);
const enabledFilter = ref("");

// 执行中的订阅 id 集合：runSubscription 是同步阻塞接口，必须逐行锁住按钮防连点，
// 否则用户重复点击会并发触发同一订阅的多轮转存。
const runningIds = ref<number[]>([]);
const runningDue = ref(false);

const enabledOptions = [
  { value: "", label: "全部状态" },
  { value: "true", label: "已启用" },
  { value: "false", label: "已停用" },
];

const statusTone = (s: string): "success" | "info" | "warning" | "neutral" | "danger" => {
  switch (s) {
    case "success":
      return "success";
    case "running":
      return "info";
    case "partial":
      return "warning";
    case "failed":
      return "danger";
    default:
      return "neutral";
  }
};

const statusLabel = (s: string): string => {
  switch (s) {
    case "pending":
      return "待执行";
    case "running":
      return "执行中";
    case "success":
      return "已完成";
    case "partial":
      return "部分成功";
    case "failed":
      return "失败";
    case "no_update":
      return "无更新";
    default:
      return s || "-";
  }
};

function formatTime(v?: string | null): string {
  if (!v) return "-";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return "-";
  return d.toLocaleString();
}

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    const res = await fetchSubscriptions(
      enabledFilter.value === "" ? undefined : enabledFilter.value === "true",
    );
    records.value = res.items ?? [];
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载订阅列表失败");
    records.value = [];
  } finally {
    loading.value = false;
  }
}

/** 立即运行单条订阅；同步阻塞，按钮 loading 直到后端整轮跑完 */
async function handleRun(rec: DiscoverySubscription) {
  if (runningIds.value.includes(rec.id)) return;
  runningIds.value = [...runningIds.value, rec.id];
  try {
    await runSubscription(rec.id);
    toast.success(`「${rec.title}」执行完成`);
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "执行失败"));
  } finally {
    runningIds.value = runningIds.value.filter((id) => id !== rec.id);
  }
}

/** 批量执行全部到期订阅 */
async function handleRunDue() {
  runningDue.value = true;
  try {
    const res = await runDueSubscriptions();
    const n = res.items?.length ?? 0;
    toast.success(n ? `已执行 ${n} 条到期订阅` : "当前没有到期的订阅");
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "批量执行失败"));
  } finally {
    runningDue.value = false;
  }
}

async function handleToggle(rec: DiscoverySubscription, enabled: boolean) {
  // 乐观切换失败要回滚，否则界面会与后端不一致
  try {
    await toggleSubscription(rec.id, enabled);
    rec.enabled = enabled;
    toast.success(enabled ? "订阅已启用" : "订阅已停用");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "切换启用状态失败"));
  }
}

async function handleDelete(rec: DiscoverySubscription) {
  try {
    await showConfirm({
      title: "删除订阅",
      message: `确定删除「${rec.title}」的订阅追更吗？已转存的资源不会被删除。`,
      icon: "trash",
      confirmText: "删除",
      danger: true,
    });
  } catch {
    // showConfirm 在用户关闭弹窗时会 reject，这里必须直接 return，
    // 否则取消操作会被当成确认继续往下执行删除。
    return;
  }
  try {
    await deleteSubscription(rec.id);
    records.value = records.value.filter((x) => x.id !== rec.id);
    toast.success("订阅已删除");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "删除失败"));
  }
}

// ---------------------------- 详情弹窗（runs / events / items） ----------------------------

type DetailTab = "runs" | "events" | "items";

const detailOpen = ref(false);
const detailTarget = ref<DiscoverySubscription | null>(null);
const detailTab = ref<DetailTab>("runs");
const detailLoading = ref(false);

const runs = ref<SubscriptionRun[]>([]);
const events = ref<SubscriptionEvent[]>([]);
const items = ref<SubscriptionItem[]>([]);

const detailTabs = [
  { key: "runs", label: "执行轮次" },
  { key: "events", label: "事件流" },
  { key: "items", label: "候选明细" },
];

async function openDetail(rec: DiscoverySubscription) {
  detailTarget.value = rec;
  detailOpen.value = true;
  detailTab.value = "runs";
  await loadDetail();
}

/** 三个子接口一起拉：数据量都很小（limit 20/50/100），分开按需拉反而更啰嗦 */
async function loadDetail() {
  const rec = detailTarget.value;
  if (!rec) return;
  detailLoading.value = true;
  runs.value = [];
  events.value = [];
  items.value = [];
  try {
    const [r, e, i] = await Promise.all([
      fetchSubscriptionRuns(rec.id),
      fetchSubscriptionEvents(rec.id),
      fetchSubscriptionItems(rec.id),
    ]);
    runs.value = r.items ?? [];
    events.value = e.items ?? [];
    items.value = i.items ?? [];
  } catch (err) {
    toast.error(getApiErrorMessage(err, "加载订阅详情失败"));
  } finally {
    detailLoading.value = false;
  }
}

function itemStatusTone(s: string): "success" | "info" | "warning" | "neutral" | "danger" {
  switch (s) {
    case "transferred":
      return "success";
    case "selected":
    case "transferring":
      return "info";
    case "failed":
      return "danger";
    case "skipped":
      return "neutral";
    default:
      return "warning";
  }
}

const detailEmptyText = computed(() => {
  if (detailTab.value === "runs") return "暂无执行记录";
  if (detailTab.value === "events") return "暂无事件";
  return "暂无候选明细";
});

onMounted(() => {
  void load();
});
</script>

<template>
  <div class="subm">
    <SettingsCard title="订阅管理" accent="var(--brand)">
      <div class="subm__toolbar">
        <AppSelect
          v-model="enabledFilter"
          :options="enabledOptions"
          class="subm__filter"
          @update:model-value="load"
        />
        <AppButton type="button" variant="secondary" :disabled="loading" @click="load">刷新</AppButton>
        <AppButton type="button" variant="primary" :disabled="runningDue" @click="handleRunDue">
          {{ runningDue ? "执行中…" : "执行到期订阅" }}
        </AppButton>
      </div>

      <AppStateBlock v-if="loading" message="加载中…" loading min-height="200px" />
      <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="200px" />

      <div v-else-if="records.length" class="subm__table-scroll">
        <table class="subm__table admin-table">
          <thead>
            <tr>
              <th>ID</th>
              <th>标题</th>
              <th>类型</th>
              <th>目标网盘</th>
              <th>状态</th>
              <th>间隔</th>
              <th>上次检查</th>
              <th>下次检查</th>
              <th>启用</th>
              <th class="admin-table__actions">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="rec in records" :key="rec.id" class="subm__row">
              <td class="subm__muted">{{ rec.id }}</td>
              <td>
                <span class="subm__title" :title="rec.title">{{ rec.title }}</span>
                <span v-if="rec.original_title && rec.original_title !== rec.title" class="subm__sub">
                  {{ rec.original_title }}
                </span>
              </td>
              <td><AppBadge tone="info">{{ rec.media_type === "tv" ? "剧集" : "电影" }}</AppBadge></td>
              <td class="subm__muted">{{ providerLabel(rec.target_provider) }}</td>
              <td><AppBadge :tone="statusTone(rec.status)">{{ statusLabel(rec.status) }}</AppBadge></td>
              <td class="subm__muted">{{ rec.interval_minutes }} 分钟</td>
              <td class="subm__muted">{{ formatTime(rec.last_checked_at) }}</td>
              <td class="subm__muted">{{ formatTime(rec.next_check_at) }}</td>
              <td>
                <AdminEnableToggle
                  :enabled="rec.enabled"
                  aria-label="订阅启用切换"
                  @enable="handleToggle(rec, $event)"
                />
              </td>
              <td class="admin-table__actions">
                <AdminRowActions>
                  <div class="subm__actions">
                    <AdminTableActionBtn
                      icon="play"
                      title="立即运行"
                      :disabled="runningIds.includes(rec.id)"
                      @click="handleRun(rec)"
                    />
                    <AdminTableActionBtn icon="log" title="执行明细" @click="openDetail(rec)" />
                    <AdminTableActionBtn icon="delete" title="删除" danger @click="handleDelete(rec)" />
                  </div>
                  <template #menu>
                    <button
                      type="button"
                      class="admin-row-actions__item"
                      :disabled="runningIds.includes(rec.id)"
                      @click="handleRun(rec)"
                    >
                      立即运行
                    </button>
                    <button type="button" class="admin-row-actions__item" @click="openDetail(rec)">
                      执行明细
                    </button>
                    <button
                      type="button"
                      class="admin-row-actions__item admin-row-actions__item--danger"
                      @click="handleDelete(rec)"
                    >
                      删除
                    </button>
                  </template>
                </AdminRowActions>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <AppStateBlock v-else message="暂无订阅，可在影视探索/榜单里点开作品详情创建订阅" min-height="200px" />
    </SettingsCard>

    <AppModal
      :open="detailOpen"
      size="lg"
      :title="`订阅明细 · ${detailTarget?.title ?? ''}`"
      @close="detailOpen = false"
    >
      <div class="subm__detail">
        <div class="subm__detail-head">
          <span class="subm__detail-meta">
            目标网盘：{{ providerLabel(detailTarget?.target_provider) }}
            · 间隔：{{ detailTarget?.interval_minutes }} 分钟
            · 状态：{{ statusLabel(detailTarget?.status ?? "") }}
          </span>
          <AppButton type="button" variant="secondary" size="sm" :disabled="detailLoading" @click="loadDetail">
            重新加载
          </AppButton>
        </div>

        <div class="subm__seg">
          <button
            v-for="t in detailTabs"
            :key="t.key"
            type="button"
            class="subm__seg-btn"
            :class="{ 'subm__seg-btn--active': detailTab === t.key }"
            @click="detailTab = t.key as DetailTab"
          >
            {{ t.label }}
          </button>
        </div>

        <AppStateBlock v-if="detailLoading" message="加载中…" loading min-height="180px" />

        <template v-else>
          <!-- 执行轮次 -->
          <div v-if="detailTab === 'runs' && runs.length" class="subm__table-scroll">
            <table class="subm__table admin-table">
              <thead>
                <tr>
                  <th>#</th>
                  <th>触发</th>
                  <th>状态</th>
                  <th>发现/选中/转存</th>
                  <th>开始</th>
                  <th>消息</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in runs" :key="r.id">
                  <td class="subm__muted">{{ r.id }}</td>
                  <td class="subm__muted">{{ r.trigger_type }}</td>
                  <td><AppBadge :tone="statusTone(r.status)">{{ statusLabel(r.status) }}</AppBadge></td>
                  <td class="subm__muted">
                    {{ r.resource_count }} / {{ r.selected_count }} / {{ r.transferred_count }}
                  </td>
                  <td class="subm__muted">{{ formatTime(r.started_at) }}</td>
                  <td>
                    <span class="subm__clamp" :title="r.message">{{ r.message || "-" }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- 事件流 -->
          <div v-else-if="detailTab === 'events' && events.length" class="subm__table-scroll">
            <table class="subm__table admin-table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>类型</th>
                  <th>状态</th>
                  <th>消息</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="ev in events" :key="ev.id">
                  <td class="subm__muted">{{ formatTime(ev.created_at) }}</td>
                  <td class="subm__muted">{{ ev.event_type }}</td>
                  <td class="subm__muted">{{ ev.status || "-" }}</td>
                  <td>
                    <span class="subm__clamp" :title="ev.message">{{ ev.message || "-" }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- 候选明细 -->
          <div v-else-if="detailTab === 'items' && items.length" class="subm__table-scroll">
            <table class="subm__table admin-table">
              <thead>
                <tr>
                  <th>标题</th>
                  <th>来源</th>
                  <th>状态</th>
                  <th>转存时间</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="it in items" :key="it.id">
                  <td>
                    <span class="subm__clamp" :title="it.title">{{ it.title }}</span>
                  </td>
                  <td><AppBadge tone="info">{{ providerLabel(it.provider) }}</AppBadge></td>
                  <td><AppBadge :tone="itemStatusTone(it.status)">{{ it.status }}</AppBadge></td>
                  <td class="subm__muted">{{ formatTime(it.transferred_at) }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <AppStateBlock v-else :message="detailEmptyText" min-height="180px" />
        </template>
      </div>
    </AppModal>
  </div>
</template>

<style scoped>
.subm__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}

.subm__filter {
  width: 140px;
}

.subm__table-scroll {
  overflow-x: auto;
}

.subm__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
  table-layout: fixed;
}

.subm__table th:nth-child(1),
.subm__table td:nth-child(1) {
  width: 56px;
}
.subm__table th:nth-child(2),
.subm__table td:nth-child(2) {
  width: 20%;
}
.subm__table th:nth-child(3),
.subm__table td:nth-child(3) {
  width: 72px;
}
.subm__table th:nth-child(4),
.subm__table td:nth-child(4) {
  width: 90px;
}
.subm__table th:nth-child(5),
.subm__table td:nth-child(5) {
  width: 92px;
}
.subm__table th:nth-child(6),
.subm__table td:nth-child(6) {
  width: 78px;
}
.subm__table th:nth-child(7),
.subm__table td:nth-child(7),
.subm__table th:nth-child(8),
.subm__table td:nth-child(8) {
  width: 132px;
}
.subm__table th:nth-child(9),
.subm__table td:nth-child(9) {
  width: 96px;
}

.subm__table th,
.subm__table td {
  padding: 10px 12px;
  text-align: left;
  vertical-align: middle;
  border-bottom: 1px solid var(--border-soft, #232733);
}

.subm__table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted, #6b7280);
  white-space: nowrap;
}

.subm__title {
  display: block;
  font-weight: 600;
  color: var(--text, #e5e7eb);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.subm__sub {
  display: block;
  margin-top: 2px;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.subm__clamp {
  display: block;
  max-width: 100%;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.subm__muted {
  color: var(--text-muted, #6b7280);
}

.subm__actions {
  display: flex;
  justify-content: center;
  gap: 4px;
}

.subm__detail {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.subm__detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}

.subm__detail-meta {
  font-size: 13px;
  color: var(--text-muted, #6b7280);
}

.subm__seg {
  display: inline-flex;
  gap: 4px;
  padding: 3px;
  border: 1px solid var(--border-soft, #232733);
  border-radius: var(--radius-sm, 8px);
  background: var(--surface-sunken, #14161c);
  align-self: flex-start;
}

.subm__seg-btn {
  padding: 5px 12px;
  border: 0;
  border-radius: var(--radius-xs, 6px);
  background: transparent;
  color: var(--text-muted, #6b7280);
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
}

.subm__seg-btn--active {
  background: var(--surface, #1b1e26);
  color: var(--brand, #e50914);
}
</style>
