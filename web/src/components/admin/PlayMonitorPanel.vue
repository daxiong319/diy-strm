<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  clearPlayTraffic,
  fetchPlayMonitorOptions,
  fetchPlaySessions,
  fetchPlayTraffic,
  playReportChartURL,
  type PlayMonitorOptions,
  type PlaySession,
  type PlayTrafficResult,
} from "@/api/playMonitor";
import type { PlayState } from "@/api/playbackRecord";
import AppBadge from "@/components/base/AppBadge.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import PlayReportPanel from "@/components/admin/PlayReportPanel.vue";
import { useConfirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";

/**
 * 播放监控面板：实时播放会话（三态）+ 顶部上行流量条。
 *
 * 三态判定在**后端**完成（PickAction 决定走 302 还是流代理，
 * 叠加客户端 IP 判断内网），前端只按 state 上色 —— 前端自己再判一遍
 * 就会出现两套真相，且三态判错的后果是记账错误，不是显示错误。
 *
 * 轮询节奏：会话 2 秒（要跟得上起停播），流量 10 秒
 * （报表数字不该跟着会话一起抖）。
 */

const { showConfirm } = useConfirm();

const SESSION_POLL_MS = 2000;
const TRAFFIC_POLL_MS = 10000;

const SESSION_TABS = [
  { value: "", label: "全部" },
  { value: "metered", label: "计费中" },
  { value: "cdn", label: "CDN 直连" },
  { value: "lan", label: "局域网" },
] as const;

const STATE_TONE: Record<PlayState, "success" | "warning" | "neutral"> = {
  metered: "success",
  cdn: "warning",
  lan: "neutral",
};

const STATE_HINT: Record<PlayState, string> = {
  metered: "字节流经过自己的服务器，计入上行",
  cdn: "走网盘直链，字节流不经过自己的服务器，计 0",
  lan: "内网播放，不计入上行",
};

const loading = ref(true);
const errorMsg = ref("");
const sessions = ref<PlaySession[]>([]);
const summary = ref<{
  today_bytes: number;
  month_bytes: number;
  total_bytes: number;
  active_count: number;
  metered_count: number;
  wan_count: number;
  lan_count: number;
  cdn_count: number;
} | null>(null);
const traffic = ref<PlayTrafficResult | null>(null);
const trafficError = ref("");
const stateFilter = ref<string>("");
const options = ref<PlayMonitorOptions | null>(null);
const reportPeriod = ref(7);
const reportTop = ref(10);
const reportSource = ref("");
const clearing = ref(false);

// lastLoaded 记住最后一次成功刷新的时刻，用来标注这批数字是什么时候的。
// 轮询失败时数字会停住 —— 不标时间的话，用户会把一个五分钟前的数字当实时看。
const lastLoaded = ref("");

let sessionTimer: number | undefined;
let trafficTimer: number | undefined;

/** 字节数展示：上行量级跨度大，固定单位在几百 B 和几十 GB 之间都不好看。 */
function formatBytes(n?: number | null): string {
  const v = Number(n ?? 0);
  if (!Number.isFinite(v) || v <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let idx = 0;
  let val = v;
  while (val >= 1024 && idx < units.length - 1) {
    val /= 1024;
    idx += 1;
  }
  return `${val >= 100 || idx === 0 ? Math.round(val) : val.toFixed(1)} ${units[idx]}`;
}

/** 码率按 bit/s 展示（后端就是 bps，前端不要换算成 KB/s 又让人猜）。 */
function formatBitrate(bps?: number | null): string {
  const v = Number(bps ?? 0);
  if (!Number.isFinite(v) || v <= 0) return "—";
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)} Mbps`;
  return `${Math.round(v / 1000)} Kbps`;
}

/** 时长口径：秒 → 1h02m / 12m / 40s */
function formatDuration(secs?: number | null): string {
  const v = Math.max(0, Math.floor(Number(secs ?? 0)));
  if (v <= 0) return "0s";
  const h = Math.floor(v / 3600);
  const m = Math.floor((v % 3600) / 60);
  if (h > 0) return `${h}h${String(m).padStart(2, "0")}m`;
  if (m > 0) return `${m}m`;
  return `${v}s`;
}

function formatClock(v?: string): string {
  if (!v) return "—";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return v;
  return d.toLocaleTimeString();
}

function formatDateTime(v?: string): string {
  if (!v) return "—";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return v;
  return d.toLocaleString();
}

function sessionDuration(s: PlaySession): number {
  const t = new Date(s.last_seen_at ?? s.started_at).getTime();
  if (Number.isNaN(t)) return 0;
  return Math.max(0, Math.floor((Date.now() - t) / 1000));
}

/** 过滤后的会话列表：面板上展示的是筛选结果，所以计数也要跟着变，
 *  否则会出现「显示 2 个会话，表头写着在播 3 个」。 */
const visibleSessions = computed(() => {
  if (!stateFilter.value) return sessions.value;
  return sessions.value.filter((s) => s.state === stateFilter.value);
});

const stateTabsWithCount = computed(() =>
  SESSION_TABS.map((t) => ({
    ...t,
    count: t.value === "" ? sessions.value.length : sessions.value.filter((s) => s.state === t.value).length,
  })),
);

async function loadSessions() {
  try {
    const res = await fetchPlaySessions();
    sessions.value = res.sessions ?? [];
    summary.value = res.summary ?? null;
    errorMsg.value = "";
    lastLoaded.value = new Date().toLocaleTimeString();
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载播放会话失败");
  } finally {
    loading.value = false;
  }
}

async function loadTraffic() {
  try {
    traffic.value = await fetchPlayTraffic();
    trafficError.value = "";
  } catch (e) {
    trafficError.value = getApiErrorMessage(e, "加载流量统计失败");
  }
}

async function loadOptions() {
  try {
    const res = await fetchPlayMonitorOptions();
    options.value = res;
    reportPeriod.value = res.defaults?.period ?? 7;
    reportTop.value = res.defaults?.top ?? 10;
    reportSource.value = res.defaults?.source ?? "";
  } catch {
    // 选项拉不到就用前端的默认值，不挡着整个面板。
    options.value = null;
  }
}

/**
 * 清空上行统计（不可撤销）。
 *
 * 这里必须二次确认：后端不设防是为了不让「加个确认参数」变成
 * 一种绕过手段 —— 但界面上仍然要拦一道，否则一个手滑就永久丢数。
 * 确认文案要说清清的是数字、不是统计起点。
 */
async function handleClearTraffic() {
  const ok = await showConfirm({
    title: "清空上行统计",
    message: "将删除今日 / 本月 / 累计的全部上行流量与排行，且不可撤销。观影报告的统计起点不受影响，清空之后只统计新产生的播放。",
    confirmText: "清空",
    danger: true,
  });
  if (!ok) return;
  clearing.value = true;
  try {
    const res = await clearPlayTraffic();
    toast.success(`已清空 ${res?.cleared ?? 0} 天的统计`);
    await loadTraffic();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "清空失败"));
  } finally {
    clearing.value = false;
  }
}

const periodOptions = computed(() =>
  (options.value?.periods ?? [{ value: 7, label: "7 天" }, { value: 14, label: "14 天" }, { value: 30, label: "30 天" }]).map(
    (p) => ({ value: String(p.value), label: p.label }),
  ),
);
const topOptions = computed(() =>
  (options.value?.tops ?? [{ value: 5 }, { value: 10 }]).map((t) => ({ value: String(t.value), label: `Top ${t.value}` })),
);
const sourceOptions = computed(() => [
  { value: "", label: "全部来源" },
  ...(options.value?.sources ?? []).map((s) => ({ value: s.value, label: s.label })),
]);

onMounted(() => {
  void loadOptions();
  void loadSessions();
  void loadTraffic();
  sessionTimer = window.setInterval(() => void loadSessions(), SESSION_POLL_MS);
  trafficTimer = window.setInterval(() => void loadTraffic(), TRAFFIC_POLL_MS);
});

onBeforeUnmount(() => {
  if (sessionTimer) window.clearInterval(sessionTimer);
  if (trafficTimer) window.clearInterval(trafficTimer);
});
</script>

<template>
  <div class="pm">
    <SettingsCard title="播放监控" description="实时播放会话与外网上行估算。计费口径按三态判定，只有字节流经过自己服务器的会话才计入上行。">
      <div class="pm__traffic">
        <div class="pm__traffic-cell">
          <span class="pm__traffic-label">今日上行</span>
          <strong class="pm__traffic-value">{{ formatBytes(traffic?.today) }}</strong>
        </div>
        <div class="pm__traffic-cell">
          <span class="pm__traffic-label">本月上行</span>
          <strong class="pm__traffic-value">{{ formatBytes(traffic?.month) }}</strong>
        </div>
        <div class="pm__traffic-cell">
          <span class="pm__traffic-label">累计上行</span>
          <strong class="pm__traffic-value">{{ formatBytes(traffic?.total) }}</strong>
        </div>
        <div class="pm__traffic-cell">
          <span class="pm__traffic-label">当前在播</span>
          <strong class="pm__traffic-value">
            {{ summary?.active_count ?? 0 }}
            <small class="pm__traffic-sub">其中计费中 {{ summary?.metered_count ?? 0 }}</small>
          </strong>
        </div>
        <div class="pm__traffic-cell pm__traffic-cell--wide">
          <span class="pm__traffic-label">三态分布</span>
          <span class="pm__traffic-tags">
            <AppBadge tone="success">计费中 {{ summary?.metered_count ?? 0 }}</AppBadge>
            <AppBadge tone="warning">CDN 直连 {{ summary?.cdn_count ?? 0 }}</AppBadge>
            <AppBadge tone="neutral">局域网 {{ summary?.lan_count ?? 0 }}</AppBadge>
          </span>
        </div>
        <div class="pm__traffic-actions">
          <AppButton type="button" variant="secondary" :disabled="clearing" @click="loadTraffic">
            刷新
          </AppButton>
          <AppButton type="button" variant="danger" :disabled="clearing || !traffic?.has_enabled" @click="handleClearTraffic">
            清空统计
          </AppButton>
        </div>
      </div>

      <p class="pm__note">
        上行数字是<strong>估算值（码率 × 时长）</strong>，约每 5 秒累计一次，不是网盘侧的精确账单。
        <template v-if="traffic?.has_enabled">
          统计自 {{ formatDateTime(traffic.enabled_since) }} 起累计，启用之前的历史不补算。
        </template>
        <template v-else>播放监控尚未启用，因此暂无统计。</template>
        <span v-if="lastLoaded">（最近更新 {{ lastLoaded }}）</span>
      </p>
      <p v-if="trafficError" class="pm__error">{{ trafficError }}</p>

      <div v-if="traffic?.month_rank?.length" class="pm__rank">
        <div class="pm__rank-title">本月外网上行排行（按用户汇总）</div>
        <div v-for="(row, i) in traffic.month_rank" :key="row.user_id" class="pm__rank-row">
          <span class="pm__rank-no">{{ i + 1 }}</span>
          <span class="pm__rank-name">{{ row.user_name || `用户 #${row.user_id}` }}</span>
          <span class="pm__rank-bar">
            <span
              class="pm__rank-bar-fill"
              :style="{ width: `${Math.max(4, (row.uploaded_bytes / Math.max(1, traffic.month_rank[0]?.uploaded_bytes || 1)) * 100)}%` }"
            />
          </span>
          <span class="pm__rank-bytes">{{ formatBytes(row.uploaded_bytes) }}</span>
        </div>
      </div>
    </SettingsCard>

    <SettingsCard title="实时播放会话" description="停播约 1 分钟后从列表消失；同一设备同一影片暂停再继续只算一次播放。">
      <div class="pm__tabs">
        <button
          v-for="tab in stateTabsWithCount"
          :key="tab.value"
          type="button"
          class="pm__tab"
          :class="{ 'pm__tab--on': stateFilter === tab.value }"
          @click="stateFilter = tab.value"
        >
          {{ tab.label }}<span class="pm__tab-count">{{ tab.count }}</span>
        </button>
        <span class="pm__tabs-spacer" />
        <span class="pm__tabs-hint">
          <AppBadge v-for="(hint, st) in STATE_HINT" :key="st" :tone="STATE_TONE[st]" pill>
            {{ hint }}
          </AppBadge>
        </span>
      </div>

      <AppStateBlock v-if="loading" message="加载中…" loading min-height="200px" />
      <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="200px" />

      <div v-else-if="visibleSessions.length" class="pm__session-list">
        <div v-for="s in visibleSessions" :key="s.id" class="pm__session">
          <div class="pm__session-main">
            <AppBadge :tone="STATE_TONE[s.state] ?? 'neutral'">
              {{ s.state === "metered" ? "计费中" : s.state === "cdn" ? "CDN 直连" : "局域网" }}
            </AppBadge>
            <span class="pm__session-item" :title="s.item_name">{{ s.item_name || "未知影片" }}</span>
          </div>
          <div class="pm__session-meta">
            <span class="pm__meta-cell" :title="s.user_name || s.emby_user_id">
              {{ s.user_name || s.emby_user_id || "未知用户" }}
            </span>
            <span class="pm__meta-cell">{{ formatBitrate(s.bitrate) }}</span>
            <span class="pm__meta-cell" :class="{ 'pm__meta-cell--zero': !s.uploaded_bytes }">
              上行 {{ formatBytes(s.uploaded_bytes) }}
            </span>
            <span class="pm__meta-cell">已播 {{ formatDuration(sessionDuration(s)) }}</span>
            <span class="pm__meta-cell" :title="s.client_ip">{{ s.client_ip || "—" }}</span>
            <span class="pm__meta-cell">更新 {{ formatClock(s.last_seen_at) }}</span>
          </div>
        </div>
      </div>

      <AppStateBlock v-else :message="stateFilter ? '该状态下暂无会话' : '当前没有正在播放的会话'" min-height="200px" />
    </SettingsCard>

    <PlayReportPanel
      :period="reportPeriod"
      :top="reportTop"
      :source="reportSource"
      :period-options="periodOptions"
      :top-options="topOptions"
      :source-options="sourceOptions"
      :chart-url="playReportChartURL({ period: reportPeriod, top: reportTop, source: reportSource })"
      @update:period="reportPeriod = Number($event)"
      @update:top="reportTop = Number($event)"
      @update:source="reportSource = $event"
    />
  </div>
</template>

<style scoped>
.pm {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.pm__traffic {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px 28px;
}

.pm__traffic-cell {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.pm__traffic-cell--wide {
  min-width: 220px;
}

.pm__traffic-label {
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}

.pm__traffic-value {
  font-size: 18px;
  font-weight: 600;
}

.pm__traffic-sub {
  font-size: 12px;
  font-weight: 400;
  color: var(--text-muted, #6b7280);
}

.pm__traffic-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.pm__traffic-actions {
  display: flex;
  gap: 8px;
  margin-left: auto;
}

.pm__note {
  margin: 12px 0 0;
  font-size: 12px;
  line-height: 1.7;
  color: var(--text-muted, #6b7280);
}

.pm__error {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--danger, #ef4444);
}

.pm__rank {
  margin-top: 16px;
  padding-top: 14px;
  border-top: 1px solid var(--border-soft, #232733);
}

.pm__rank-title {
  margin-bottom: 10px;
  font-size: 13px;
  font-weight: 600;
}

.pm__rank-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 5px 0;
  font-size: 12px;
}

.pm__rank-no {
  width: 18px;
  color: var(--text-muted, #6b7280);
}

.pm__rank-name {
  width: 120px;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.pm__rank-bar {
  flex: 1;
  height: 8px;
  overflow: hidden;
  background: var(--border-soft, #232733);
  border-radius: 4px;
}

.pm__rank-bar-fill {
  display: block;
  height: 100%;
  background: var(--accent, #3b82f6);
}

.pm__rank-bytes {
  width: 90px;
  text-align: right;
  color: var(--text-muted, #6b7280);
}

.pm__tabs {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.pm__tabs-spacer {
  flex: 1;
}

.pm__tabs-hint {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.pm__tab {
  padding: 4px 12px;
  font-size: 12px;
  color: var(--text-main, #e5e7eb);
  cursor: pointer;
  background: transparent;
  border: 1px solid var(--border-soft, #232733);
  border-radius: 999px;
}

.pm__tab--on {
  border-color: var(--accent, #3b82f6);
}

.pm__tab-count {
  margin-left: 6px;
  color: var(--text-muted, #6b7280);
}

.pm__session-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.pm__session {
  padding: 10px 12px;
  border: 1px solid var(--border-soft, #232733);
  border-radius: 8px;
}

.pm__session-main {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 6px;
}

.pm__session-item {
  overflow: hidden;
  font-size: 13px;
  font-weight: 500;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.pm__session-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 16px;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}

.pm__meta-cell {
  white-space: nowrap;
}

.pm__meta-cell--zero {
  opacity: 0.55;
}
</style>