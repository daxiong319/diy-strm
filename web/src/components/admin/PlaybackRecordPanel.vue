<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  clearPlaybackRecords,
  deletePlaybackRecord,
  fetchPlaybackRecords,
  fetchPlaybackRecordStats,
  type PlaybackRecord,
  type PlaybackRecordStats,
} from "@/api/playbackRecord";
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

/** 网盘筛选项：与其它面板保持一致的标识 → 中文映射 */
const PROVIDER_OPTIONS: Array<{ value: string; label: string }> = [
  { value: "", label: "全部网盘" },
  { value: "115", label: "115网盘" },
  { value: "123", label: "123网盘" },
  { value: "quark", label: "夸克网盘" },
  { value: "aliyun", label: "阿里云盘" },
  { value: "guangya", label: "光雅" },
  { value: "pan139", label: "移动云盘" },
  { value: "baidu", label: "百度网盘" },
  { value: "openlist", label: "OpenList" },
];

const loading = ref(false);
const errorMsg = ref("");
const records = ref<PlaybackRecord[]>([]);
const stats = ref<PlaybackRecordStats | null>(null);
const total = ref(0);
const page = ref(1);
const pageSize = 30;
const keyword = ref("");
const provider = ref("");
const ruleId = ref("");
const deleting = ref(false);

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)));

function providerLabel(key?: string): string {
  if (!key) return "-";
  return PROVIDER_OPTIONS.find((o) => o.value === key)?.label ?? key;
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
    const res = await fetchPlaybackRecords({
      id: ruleId.value.trim() || undefined,
      provider: provider.value || undefined,
      keyword: keyword.value.trim() || undefined,
      page: page.value,
      page_size: pageSize,
    });
    records.value = res.items ?? [];
    total.value = res.total ?? 0;
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载播放记录失败");
    records.value = [];
    total.value = 0;
  } finally {
    loading.value = false;
  }
}

/** 概览统计；失败静默，角标只是锦上添花 */
async function loadStats() {
  try {
    stats.value = await fetchPlaybackRecordStats();
  } catch {
    stats.value = null;
  }
}

function refresh() {
  return Promise.all([load(), loadStats()]);
}

function search() {
  page.value = 1;
  void refresh();
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

/** 删除单条记录（仅删记录，不影响云端文件） */
async function handleDelete(rec: PlaybackRecord) {
  try {
    await showConfirm({
      title: "删除播放记录",
      message: `确定删除「${rec.item_name || rec.strm_path || rec.id}」这条播放记录吗？`,
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
    await deletePlaybackRecord(rec.id);
    toast.success("已删除 1 条播放记录");
    await refresh();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "删除失败"));
  } finally {
    deleting.value = false;
  }
}

/** 清空播放记录（按当前网盘筛选缩小范围，不带筛选 = 清全部） */
async function handleClear() {
  const scope = provider.value ? providerLabel(provider.value) : "全部网盘";
  try {
    await showConfirm({
      title: "清空播放记录",
      message: `确定清空「${scope}」的全部播放记录吗？此操作不可恢复（不影响云端文件与反代规则）。`,
      icon: "warning",
      confirmText: "清空",
      danger: true,
    });
  } catch {
    return;
  }
  deleting.value = true;
  try {
    const res = await clearPlaybackRecords({});
    toast.success(`已清空 ${res.deleted ?? 0} 条播放记录`);
    page.value = 1;
    await refresh();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "清空失败"));
  } finally {
    deleting.value = false;
  }
}

onMounted(() => {
  void refresh();
});
</script>

<template>
  <div class="pbh">
    <SettingsCard title="播放记录" accent="var(--brand)">
      <div class="pbh__toolbar">
        <AppSelect
          v-model="provider"
          :options="PROVIDER_OPTIONS"
          class="pbh__filter"
          @update:model-value="search"
        />
        <AppInput v-model="ruleId" placeholder="规则 ID" class="pbh__rule" @keyup.enter="search" />
        <AppInput
          v-model="keyword"
          placeholder="搜索文件名/路径…"
          class="pbh__search"
          @keyup.enter="search"
        />
        <AppButton type="button" variant="secondary" @click="search">查询</AppButton>
        <AppButton type="button" variant="secondary" :disabled="loading" @click="refresh">
          刷新
        </AppButton>
        <AppButton type="button" variant="danger" :disabled="deleting || !total" @click="handleClear">
          清空
        </AppButton>
      </div>

      <!-- 概览：一眼看出记录规模与最近播放时间 -->
      <div v-if="stats" class="pbh__stats">
        <AppBadge tone="success">共 {{ stats.total }} 条</AppBadge>
        <AppBadge tone="info">{{ stats.user_count }} 个用户</AppBadge>
        <AppBadge tone="info">{{ stats.item_count }} 个条目</AppBadge>
        <span class="pbh__stats-label">最近播放：{{ formatTime(stats.last_at) }}</span>
      </div>

      <AppStateBlock v-if="loading" message="加载中…" loading min-height="200px" />
      <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="200px" />

      <div v-else-if="records.length" class="pbh__table-scroll">
        <table class="pbh__table admin-table">
          <thead>
            <tr>
              <th>播放时间</th>
              <th>网盘</th>
              <th>用户</th>
              <th>文件名</th>
              <th>路径</th>
              <th>客户端</th>
              <th>设备</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="rec in records" :key="rec.id" class="pbh__row">
              <td class="pbh__muted">{{ formatTime(rec.playback_at) }}</td>
              <td><AppBadge tone="info">{{ providerLabel(rec.provider) }}</AppBadge></td>
              <td class="pbh__muted">{{ rec.user_id || "-" }}</td>
              <td>
                <span class="pbh__clamp" :title="rec.item_name">{{ rec.item_name || "-" }}</span>
              </td>
              <td>
                <span class="pbh__clamp" :title="rec.strm_path">{{ rec.strm_path || "-" }}</span>
              </td>
              <td class="pbh__muted">{{ rec.client || "-" }}</td>
              <td class="pbh__muted">{{ rec.device_id || "-" }}</td>
              <td>
                <AppButton
                  type="button"
                  variant="danger"
                  :disabled="deleting"
                  @click="handleDelete(rec)"
                >
                  删除
                </AppButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <AppStateBlock v-else message="暂无播放记录" min-height="200px" />

      <div v-if="totalPages > 1 && records.length" class="pbh__pager">
        <AppButton type="button" variant="secondary" :disabled="page <= 1 || loading" @click="prevPage">
          上一页
        </AppButton>
        <span class="pbh__page-info">{{ page }} / {{ totalPages }}（共 {{ total }} 条）</span>
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
.pbh__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.pbh__filter {
  width: 140px;
}

.pbh__rule {
  width: 110px;
}

.pbh__search {
  width: 220px;
}

.pbh__stats {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-bottom: 14px;
}

.pbh__stats-label {
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}

.pbh__table-scroll {
  overflow-x: auto;
}

.pbh__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
  table-layout: fixed;
}

.pbh__table th,
.pbh__table td {
  padding: 10px 12px;
  text-align: left;
  vertical-align: middle;
  border-bottom: 1px solid var(--border-soft, #232733);
}

.pbh__table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted, #6b7280);
  white-space: nowrap;
}

/* 列宽：时间/网盘/用户/客户端/设备/操作固定，文件名与路径吃剩余空间 */
.pbh__table th:nth-child(1),
.pbh__table td:nth-child(1) {
  width: 158px;
}
.pbh__table th:nth-child(2),
.pbh__table td:nth-child(2) {
  width: 92px;
}
.pbh__table th:nth-child(3),
.pbh__table td:nth-child(3) {
  width: 96px;
}
.pbh__table th:nth-child(6),
.pbh__table td:nth-child(6) {
  width: 120px;
}
.pbh__table th:nth-child(7),
.pbh__table td:nth-child(7) {
  width: 110px;
}
.pbh__table th:nth-child(8),
.pbh__table td:nth-child(8) {
  width: 82px;
}

.pbh__clamp {
  display: block;
  max-width: 100%;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.pbh__muted {
  color: var(--text-muted, #6b7280);
  white-space: nowrap;
}

.pbh__pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 14px;
  margin-top: 16px;
}

.pbh__page-info {
  font-size: 13px;
  color: var(--text-muted, #6b7280);
}
</style>
