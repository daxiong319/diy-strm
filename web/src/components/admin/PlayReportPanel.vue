<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { getApiErrorMessage } from "@/api/client";
import { fetchPlayReport, type PlayReportResult, type PlayReportUser } from "@/api/playMonitor";
import AppBadge from "@/components/base/AppBadge.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";

/**
 * 观影报告面板。
 *
 * ⚠️ 这个面板最要紧的不是排行，而是把统计口径摆在明面上：
 * 「从启用起累计、不补算」「中断超半小时算新的一次」这两条
 * 一旦没写，用户看到「本周只看了 2 次」时会以为统计漏了。
 * 所以 notices 由后端下发、前端原样渲染，不在这里复述改写。
 */

const props = defineProps<{
  period: number;
  top: number;
  source: string;
  periodOptions: Array<{ value: string; label: string }>;
  topOptions: Array<{ value: string; label: string }>;
  sourceOptions: Array<{ value: string; label: string }>;
  chartUrl: string;
}>();

const emit = defineEmits<{
  (e: "update:period", v: number): void;
  (e: "update:top", v: number): void;
  (e: "update:source", v: string): void;
}>();

const loading = ref(false);
const errorMsg = ref("");
const report = ref<PlayReportResult | null>(null);
const chartPNG = ref("");
const chartBroken = ref(false);

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

function formatDuration(secs?: number | null): string {
  const v = Math.max(0, Math.floor(Number(secs ?? 0)));
  if (v <= 0) return "0s";
  const h = Math.floor(v / 3600);
  const m = Math.floor((v % 3600) / 60);
  if (h > 0) return `${h}h${String(m).padStart(2, "0")}m`;
  if (m > 0) return `${m}m`;
  return `${v}s`;
}

function formatDateTime(v?: string): string {
  if (!v) return "—";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return v;
  return d.toLocaleString();
}

function userKey(u: PlayReportUser): string {
  return u.app_user_id > 0 ? `app:${u.app_user_id}` : `emby:${u.emby_user_id}`;
}

const users = computed(() => report.value?.users ?? []);
const maxCount = computed(() => Math.max(1, ...users.value.map((u) => u.count)));

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    // chart=true 时后端把 PNG 内联成 data URI：
    // 排行和图必须来自同一次汇总，分成两个请求会出现
    // 「数据是今天的、图是三小时前的」。
    const res = await fetchPlayReport({
      period: props.period,
      top: props.top,
      source: props.source,
      chart: true,
    });
    report.value = res.report ?? null;
    chartPNG.value = res.chart_png ?? "";
    chartBroken.value = false;
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "生成观影报告失败");
    report.value = null;
    chartPNG.value = "";
  } finally {
    loading.value = false;
  }
}

// 参数一变就重算：报告是「按当前条件的一次快照」，
// 留着上次的数字配新的周期选择是最容易误导人的。
watch(() => [props.period, props.top, props.source], load, { immediate: true });
</script>

<template>
  <SettingsCard title="观影报告" description="按周期汇总各用户的播放次数、时长与上行估算，并给出排行图。">
    <div class="pr__toolbar">
      <AppSelect
        class="pr__select"
        :model-value="String(period)"
        :options="periodOptions"
        @update:model-value="emit('update:period', Number($event))"
      />
      <AppSelect
        class="pr__select"
        :model-value="String(top)"
        :options="topOptions"
        @update:model-value="emit('update:top', Number($event))"
      />
      <AppSelect
        class="pr__select"
        :model-value="source"
        :options="sourceOptions"
        @update:model-value="emit('update:source', String($event))"
      />
      <AppButton type="button" variant="secondary" :disabled="loading" @click="load">重新统计</AppButton>
    </div>

    <div v-if="report?.notices?.length" class="pr__notices">
      <div class="pr__notices-title">统计口径</div>
      <ul class="pr__notices-list">
        <li v-for="(n, i) in report.notices" :key="i">{{ n }}</li>
      </ul>
    </div>

    <p class="pr__range">
      统计区间 {{ formatDateTime(report?.since) }} ~ {{ formatDateTime(report?.until) }}
      <template v-if="report?.enabled_since">
        ；自 {{ formatDateTime(report.enabled_since) }} 起累计。
      </template>
      <template v-else>；尚未启用过播放监控，暂无可统计的数据。</template>
    </p>

    <AppStateBlock v-if="loading" message="统计中…" loading min-height="180px" />
    <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="180px" />
    <AppStateBlock v-else-if="!report || !report.users.length" message="所选周期内没有播放记录" min-height="180px" />

    <template v-else>
      <div class="pr__summary">
        <AppBadge tone="info">总播放 {{ report.total_count }} 次</AppBadge>
        <AppBadge tone="info">总时长 {{ formatDuration(report.total_watched_seconds) }}</AppBadge>
        <AppBadge tone="info">上行合计 {{ formatBytes(report.total_uploaded_bytes) }}</AppBadge>
        <AppBadge tone="neutral">参与用户 {{ report.users.length }} 人</AppBadge>
      </div>

      <div class="pr__body">
        <div class="pr__chart">
          <img v-if="chartPNG && !chartBroken" :src="chartPNG" alt="观影排行图" class="pr__chart-img" @error="chartBroken = true" />
          <div v-else-if="chartBroken" class="pr__chart-fallback">
            排行图生成失败，下面的文字排行仍然完整。
          </div>
          <div v-else class="pr__chart-fallback">正在生成排行图…</div>
        </div>

        <div class="pr__table-scroll">
          <table class="pr__table">
            <thead>
              <tr>
                <th>#</th>
                <th>用户</th>
                <th>播放次数</th>
                <th>观看时长</th>
                <th>上行估算</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(u, i) in users" :key="userKey(u)">
                <td class="pr__muted">{{ i + 1 }}</td>
                <td>
                  {{ u.user_name || (u.emby_user_id ? `Emby:${u.emby_user_id}` : `用户 #${u.app_user_id}`) }}
                </td>
                <td>
                  <span class="pr__bar-cell">
                    <span class="pr__bar">
                      <span class="pr__bar-fill" :style="{ width: `${Math.max(3, (u.count / maxCount) * 100)}%` }" />
                    </span>
                    <span>{{ u.count }}</span>
                  </span>
                </td>
                <td class="pr__muted">{{ formatDuration(u.watched_seconds) }}</td>
                <td class="pr__muted">{{ formatBytes(u.uploaded_bytes) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <details v-if="report.items?.length" class="pr__details">
        <summary>按影片展开（用户 × 条目）</summary>
        <div class="pr__table-scroll">
          <table class="pr__table">
            <thead>
              <tr>
                <th>用户</th>
                <th>影片</th>
                <th>次数</th>
                <th>时长</th>
                <th>上行估算</th>
                <th>最近一次</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(it, i) in report.items" :key="`${userKey(it)}#${i}`">
                <td>{{ it.user_name || it.emby_user_id || `用户 #${it.app_user_id}` }}</td>
                <td><span class="pr__clamp" :title="it.item_name">{{ it.item_name || "-" }}</span></td>
                <td class="pr__muted">{{ it.count }}</td>
                <td class="pr__muted">{{ formatDuration(it.watched_seconds) }}</td>
                <td class="pr__muted">{{ formatBytes(it.uploaded_bytes) }}</td>
                <td class="pr__muted">{{ formatDateTime(it.last_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </details>
    </template>
  </SettingsCard>
</template>

<style scoped>
.pr__toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 14px;
}

.pr__select {
  width: 130px;
}

.pr__notices {
  padding: 12px 14px;
  margin-bottom: 12px;
  background: var(--bg-soft, #161a22);
  border: 1px solid var(--border-soft, #232733);
  border-radius: 8px;
}

.pr__notices-title {
  margin-bottom: 6px;
  font-size: 12px;
  font-weight: 600;
}

.pr__notices-list {
  padding-left: 18px;
  margin: 0;
  font-size: 12px;
  line-height: 1.8;
  color: var(--text-muted, #6b7280);
}

.pr__range {
  margin: 0 0 14px;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}

.pr__summary {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 14px;
}

.pr__body {
  display: flex;
  flex-wrap: wrap;
  gap: 18px;
}

.pr__chart {
  flex: 0 0 100%;
  max-width: 640px;
}

.pr__chart-img {
  width: 100%;
  height: auto;
  border: 1px solid var(--border-soft, #232733);
  border-radius: 8px;
}

.pr__chart-fallback {
  padding: 24px;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
  text-align: center;
  border: 1px dashed var(--border-soft, #232733);
  border-radius: 8px;
}

.pr__table-scroll {
  flex: 1;
  min-width: 380px;
  overflow-x: auto;
}

.pr__table {
  width: 100%;
  font-size: 13px;
  border-collapse: collapse;
}

.pr__table th,
.pr__table td {
  padding: 8px 10px;
  text-align: left;
  vertical-align: middle;
  border-bottom: 1px solid var(--border-soft, #232733);
}

.pr__table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted, #6b7280);
  white-space: nowrap;
}

.pr__bar-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}

.pr__bar {
  display: inline-block;
  width: 80px;
  height: 6px;
  overflow: hidden;
  background: var(--border-soft, #232733);
  border-radius: 3px;
}

.pr__bar-fill {
  display: block;
  height: 100%;
  background: var(--accent, #3b82f6);
}

.pr__muted {
  color: var(--text-muted, #6b7280);
  white-space: nowrap;
}

.pr__clamp {
  display: block;
  max-width: 220px;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.pr__details {
  margin-top: 18px;
  font-size: 13px;
}

.pr__details summary {
  cursor: pointer;
  color: var(--text-muted, #6b7280);
}
</style>