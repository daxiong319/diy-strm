<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  clearMonitorRecords,
  deleteMonitorRecords,
  fetchMonitorRecordSources,
  fetchMonitorRecords,
  MONITOR_STATUS_OPTIONS,
  providerLabel,
  DISCOVERY_PROVIDER_OPTIONS,
  type DiscoveryMonitorRecord,
  type MonitorRecordSources,
} from "@/api/discovery";
import AppBadge from "@/components/base/AppBadge.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import { useConfirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import "@/styles/admin-table.css";

const { showConfirm } = useConfirm();

const loading = ref(false);
const errorMsg = ref("");
const records = ref<DiscoveryMonitorRecord[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = 50;
const keyword = ref("");
const status = ref("");
const sourceType = ref("");
const sources = ref<MonitorRecordSources>({});
const selectedIds = ref<number[]>([]);
const deleting = ref(false);

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)));

/** 状态 → Badge 色调（后端状态值是中文常量） */
function statusTone(s: string): "success" | "danger" | "neutral" | "warning" {
  switch (s) {
    case "转存成功":
      return "success";
    case "转存失败":
      return "danger";
    case "已跳过":
      return "neutral";
    case "洗版替换":
      return "warning";
    default:
      return "neutral";
  }
}

/** 入口 → 中文说明 */
function entryLabel(entry: string): string {
  switch (entry) {
    case "channel":
      return "TG频道订阅";
    case "hive":
      return "RE0订阅";
    case "bot":
      return "TG机器人";
    default:
      return entry || "-";
  }
}

function entryTone(entry: string): "info" | "success" | "warning" | "neutral" {
  switch (entry) {
    case "channel":
      return "info";
    case "hive":
      return "success";
    case "bot":
      return "warning";
    default:
      return "neutral";
  }
}

function formatTime(v?: string): string {
  if (!v) return "-";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return v;
  return d.toLocaleString();
}

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    const res = await fetchMonitorRecords({
      source_type: sourceType.value || undefined,
      status: status.value || undefined,
      keyword: keyword.value.trim() || undefined,
      page: page.value,
      page_size: pageSize,
    });
    records.value = res.items ?? [];
    total.value = res.total ?? 0;
    // 翻页/筛选后旧的勾选可能已不在当前页，清空避免误删
    selectedIds.value = [];
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载监控历史失败");
    records.value = [];
    total.value = 0;
  } finally {
    loading.value = false;
  }
}

/** 各来源计数角标；失败静默（角标只是锦上添花，不该弹错误打断主流程） */
async function loadSources() {
  try {
    const res = await fetchMonitorRecordSources();
    sources.value = res.items ?? {};
  } catch {
    sources.value = {};
  }
}

function search() {
  page.value = 1;
  void load();
}

function nextPage() {
  if (page.value < totalPages.value) {
    page.value++;
    void load();
  }
}

function prevPage() {
  if (page.value > 1) {
    page.value--;
    void load();
  }
}

const allChecked = computed(
  () => records.value.length > 0 && selectedIds.value.length === records.value.length,
);

function toggleAll() {
  selectedIds.value = allChecked.value ? [] : records.value.map((r) => r.id);
}

function toggleOne(id: number) {
  if (selectedIds.value.includes(id)) {
    selectedIds.value = selectedIds.value.filter((x) => x !== id);
  } else {
    selectedIds.value = [...selectedIds.value, id];
  }
}

/** 批量删除勾选记录 */
async function handleDeleteSelected() {
  if (!selectedIds.value.length) return;
  const ids = [...selectedIds.value];
  try {
    await showConfirm({
      title: "删除监控记录",
      message: `确定删除选中的 ${ids.length} 条监控历史吗？（仅删除记录，不影响云端文件）`,
      icon: "trash",
      confirmText: "删除",
      danger: true,
    });
  } catch {
    // showConfirm 在关闭弹窗时 reject；不 return 会把"取消"当成"确认"继续删。
    return;
  }
  deleting.value = true;
  try {
    const res = await deleteMonitorRecords(ids);
    toast.success(`已删除 ${res.deleted ?? ids.length} 条记录`);
    await Promise.all([load(), loadSources()]);
  } catch (e) {
    toast.error(getApiErrorMessage(e, "批量删除失败"));
  } finally {
    deleting.value = false;
  }
}

/** 清空历史（按当前筛选的来源网盘；不传时间范围 = 清全部） */
async function handleClear() {
  const scope = sourceType.value ? providerLabel(sourceType.value) : "全部网盘";
  try {
    await showConfirm({
      title: "清空监控历史",
      message: `确定清空「${scope}」的全部监控历史吗？此操作不可恢复（不影响云端文件与转存记录表）。`,
      icon: "warning",
      confirmText: "清空",
      danger: true,
    });
  } catch {
    return;
  }
  deleting.value = true;
  try {
    const res = await clearMonitorRecords({ source_type: sourceType.value || undefined });
    toast.success(`已清空 ${res.deleted ?? 0} 条记录`);
    page.value = 1;
    await Promise.all([load(), loadSources()]);
  } catch (e) {
    toast.error(getApiErrorMessage(e, "清空失败"));
  } finally {
    deleting.value = false;
  }
}

onMounted(() => {
  void load();
  void loadSources();
});
</script>

<template>
  <div class="monh">
    <SettingsCard title="监控历史" accent="var(--brand)">
      <div class="monh__toolbar">
        <AppSelect
          v-model="sourceType"
          :options="DISCOVERY_PROVIDER_OPTIONS"
          class="monh__filter"
          @update:model-value="search"
        />
        <AppSelect
          v-model="status"
          :options="MONITOR_STATUS_OPTIONS"
          class="monh__filter"
          @update:model-value="search"
        />
        <AppInput
          v-model="keyword"
          placeholder="搜索标题/频道…"
          class="monh__search"
          @keyup.enter="search"
        />
        <AppButton type="button" variant="secondary" @click="search">查询</AppButton>
        <AppButton type="button" variant="secondary" :disabled="loading" @click="load">刷新</AppButton>
        <AppButton
          type="button"
          variant="danger"
          :disabled="deleting || !records.length"
          @click="handleDeleteSelected"
        >
          删除选中{{ selectedIds.length ? `（${selectedIds.length}）` : "" }}
        </AppButton>
        <AppButton type="button" variant="danger" :disabled="deleting || !total" @click="handleClear">
          清空
        </AppButton>
      </div>

      <!-- 各来源计数角标：一眼看出哪个网盘的记录最多 -->
      <div v-if="Object.keys(sources).length" class="monh__counts">
        <span class="monh__counts-label">各来源记录数：</span>
        <AppBadge
          v-for="(n, k) in sources"
          :key="k"
          :tone="k === sourceType ? 'success' : 'neutral'"
        >
          {{ providerLabel(String(k)) }} {{ n }}
        </AppBadge>
      </div>

      <AppStateBlock v-if="loading" message="加载中…" loading min-height="200px" />
      <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="200px" />

      <div v-else-if="records.length" class="monh__table-scroll">
        <table class="monh__table admin-table">
          <thead>
            <tr>
              <th class="monh__col-check">
                <input type="checkbox" :checked="allChecked" @change="toggleAll" />
              </th>
              <th>时间</th>
              <th>网盘</th>
              <th>入口</th>
              <th>标题</th>
              <th>状态</th>
              <th>集号</th>
              <th>目标目录</th>
              <th>结果</th>
              <th>帖子</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="rec in records" :key="rec.id" class="monh__row">
              <td class="monh__col-check">
                <input
                  type="checkbox"
                  :checked="selectedIds.includes(rec.id)"
                  @change="toggleOne(rec.id)"
                />
              </td>
              <td class="monh__muted">{{ formatTime(rec.transfer_time) }}</td>
              <td><AppBadge tone="info">{{ providerLabel(rec.source_type) }}</AppBadge></td>
              <td><AppBadge :tone="entryTone(rec.entry)">{{ entryLabel(rec.entry) }}</AppBadge></td>
              <td>
                <span class="monh__clamp" :title="rec.title">{{ rec.title || "-" }}</span>
              </td>
              <td>
                <AppBadge :tone="statusTone(rec.transfer_status)">
                  {{ rec.transfer_status || "-" }}
                </AppBadge>
              </td>
              <td class="monh__muted">{{ rec.episode || "-" }}</td>
              <td>
                <span class="monh__clamp" :title="rec.target_dir">{{ rec.target_dir || "-" }}</span>
              </td>
              <td>
                <span class="monh__clamp" :title="rec.transfer_result">{{ rec.transfer_result || "-" }}</span>
              </td>
              <td>
                <a
                  v-if="rec.message_url"
                  :href="rec.message_url"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="monh__link"
                >
                  查看
                </a>
                <span v-else class="monh__muted">-</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <AppStateBlock v-else message="暂无监控记录" min-height="200px" />

      <div v-if="totalPages > 1 && records.length" class="monh__pager">
        <AppButton type="button" variant="secondary" :disabled="page <= 1 || loading" @click="prevPage">
          上一页
        </AppButton>
        <span class="monh__page-info">{{ page }} / {{ totalPages }}（共 {{ total }} 条）</span>
        <AppButton
          type="button"
          variant="secondary"
          :disabled="page >= totalPages || loading"
          @click="nextPage"
        >
          下一页
        </AppButton>
      </div>
    </SettingsCard>
  </div>
</template>

<style scoped>
.monh__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.monh__filter {
  width: 140px;
}

.monh__search {
  width: 220px;
}

.monh__counts {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-bottom: 14px;
}

.monh__counts-label {
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}

.monh__table-scroll {
  overflow-x: auto;
}

.monh__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
  table-layout: fixed;
}

.monh__col-check {
  width: 40px;
  text-align: center;
}

.monh__table th,
.monh__table td {
  padding: 10px 12px;
  text-align: left;
  vertical-align: middle;
  border-bottom: 1px solid var(--border-soft, #232733);
}

.monh__table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted, #6b7280);
  white-space: nowrap;
}

/* 列宽：时间/网盘/入口/状态/集号/帖子固定，标题与目录、结果吃剩余空间 */
.monh__table th:nth-child(2),
.monh__table td:nth-child(2) {
  width: 148px;
}
.monh__table th:nth-child(3),
.monh__table td:nth-child(3) {
  width: 92px;
}
.monh__table th:nth-child(4),
.monh__table td:nth-child(4) {
  width: 108px;
}
.monh__table th:nth-child(6),
.monh__table td:nth-child(6) {
  width: 90px;
}
.monh__table th:nth-child(7),
.monh__table td:nth-child(7) {
  width: 56px;
}
.monh__table th:nth-child(10),
.monh__table td:nth-child(10) {
  width: 62px;
}

.monh__clamp {
  display: block;
  max-width: 100%;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.monh__muted {
  color: var(--text-muted, #6b7280);
  white-space: nowrap;
}

.monh__link {
  color: var(--brand, #e50914);
  text-decoration: none;
  font-weight: 600;
}

.monh__link:hover {
  text-decoration: underline;
}

.monh__pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 14px;
  margin-top: 16px;
}

.monh__page-info {
  font-size: 13px;
  color: var(--text-muted, #6b7280);
}
</style>
