<script setup lang="ts">
// Emby 缺集补档面板。
// 数据链路：启动扫描（后端异步）→ 轮询 status 拿进度 → 拉 results 列缺集 → 勾选后批量建补档订阅。
// 设计取舍：扫描是异步的，所以不做「点了就等结果」，而是用轮询驱动进度条，用户可随时离开。
import { computed, onMounted, onUnmounted, reactive, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  createEmbyMissingSubscriptions,
  fetchEmbyMissingConfig,
  fetchEmbyMissingEvents,
  fetchEmbyMissingLibraries,
  fetchEmbyMissingResults,
  fetchEmbyMissingScans,
  fetchEmbyMissingStatus,
  saveEmbyMissingConfig,
  startEmbyMissingScan,
  testEmbyMissingConfig,
  type EmbyLibrary,
  type EmbyMissingEvent,
  type EmbyMissingResult,
  type EmbyMissingScan,
  type EmbyMissingStatus,
} from "@/api/discovery";
import AppBadge from "@/components/base/AppBadge.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import FormField from "@/components/base/FormField.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import { toast } from "@/composables/useToast";
import "@/styles/admin-table.css";

// 轮询间隔：后端的缺集扫描按「剧」推进，2 秒粒度足够及时又不至于打爆 SQLite 单连接。
const POLL_INTERVAL_MS = 2000;

const TARGET_PROVIDER_OPTIONS = [
  { value: "123", label: "123 网盘" },
  { value: "guangya", label: "光鸭" },
  { value: "pan139", label: "139 网盘" },
];

const status = ref<EmbyMissingStatus | null>(null);
const libraries = ref<EmbyLibrary[]>([]);
const scans = ref<EmbyMissingScan[]>([]);
const results = ref<EmbyMissingResult[]>([]);
const events = ref<EmbyMissingEvent[]>([]);
const selectedScanId = ref<number | null>(null);
const selectedResultIds = ref<number[]>([]);
const targetProvider = ref("123");
const intervalMinutes = ref(720);

const loading = ref(false);
const scanning = ref(false);
const subscribing = ref(false);
const errorMsg = ref("");
const eventsOpenFor = ref<number | null>(null);

let pollTimer: number | null = null;

const embyInfo = computed(() => status.value?.emby ?? {});
const activeScan = computed(() => status.value?.active_scan ?? null);
/**
 * 只有「配置齐全 + 已启用」才算就绪。
 * ★ 不能只看 configured：后端现在会如实区分未配置 / 未启用 / 连不上，
 *   这里也必须跟着区分，否则「填了但连不上」还会被当成可以扫描。
 */
const ready = computed(
  () =>
    Boolean(embyInfo.value.configured) &&
    Boolean(embyInfo.value.enabled) &&
    embyInfo.value.state !== "unreachable",
);

/** 配置状态中文名（state 缺失时按 configured/enabled 兜底推断，兼容旧后端） */
function embyStateLabel(state?: string): string {
  switch (state) {
    case "ready":
      return "已就绪";
    case "unreachable":
      return "无法连接";
    case "disabled":
      return "未启用";
    case "unconfigured":
      return "未配置";
    default:
      if (!embyInfo.value.configured) return "未配置";
      if (!embyInfo.value.enabled) return "未启用";
      return "已就绪";
  }
}

function embyStateTone(state?: string): "success" | "warning" | "danger" | "neutral" {
  switch (state) {
    case "ready":
      return "success";
    case "unreachable":
      return "danger";
    case "disabled":
    case "unconfigured":
      return "warning";
    default:
      return embyInfo.value.configured ? "success" : "warning";
  }
}

/** 扫描进度百分比：后端不保证 total_series 一定大于 0，所以要做零除保护。 */
const scanProgress = computed(() => {
  const scan = activeScan.value;
  if (!scan || !scan.total_series) return null;
  const done = scan.scanned_series ?? 0;
  return Math.min(100, Math.round((done / scan.total_series) * 100));
});

const selectableResults = computed(() => results.value.filter((r) => r.missing_count > 0));

const allSelected = computed(
  () =>
    selectableResults.value.length > 0 &&
    selectableResults.value.every((r) => selectedResultIds.value.includes(r.id)),
);

/** 后端 status 字段是英文枚举，这里统一翻成中文以便与页面其它状态展示一致。 */
function scanStatusLabel(s?: string): string {
  switch (s) {
    case "queued":
      return "排队中";
    case "running":
      return "扫描中";
    case "success":
      return "已完成";
    case "partial":
      return "部分完成";
    case "failed":
      return "失败";
    default:
      return s || "-";
  }
}

function scanStatusTone(s?: string): "neutral" | "info" | "success" | "warning" | "danger" {
  switch (s) {
    case "running":
    case "queued":
      return "info";
    case "success":
      return "success";
    case "partial":
      return "warning";
    case "failed":
      return "danger";
    default:
      return "neutral";
  }
}

function formatTime(v?: string | null): string {
  if (!v) return "-";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return v;
  return d.toLocaleString("zh-CN", { hour12: false });
}

/** 把缺集明细压成 S01E02、S01E05 这样的紧凑串，避免一屏放不下。 */
function episodeSummary(r: EmbyMissingResult): string {
  const eps = r.missing_episodes ?? [];
  if (eps.length === 0) return `缺 ${r.missing_count} 集`;
  const shown = eps.slice(0, 12).map((e) => `S${String(e.season).padStart(2, "0")}E${String(e.episode).padStart(2, "0")}`);
  const suffix = eps.length > shown.length ? ` 等 ${eps.length} 集` : "";
  return shown.join("、") + suffix;
}

function stopPolling() {
  if (pollTimer !== null) {
    window.clearInterval(pollTimer);
    pollTimer = null;
  }
}

async function loadStatus() {
  try {
    status.value = await fetchEmbyMissingStatus();
    const active = status.value.active_scan;
    if (active) {
      startPolling();
    } else {
      stopPolling();
    }
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载 Emby 缺集状态失败");
  }
}

async function loadScans() {
  try {
    const res = await fetchEmbyMissingScans(20);
    scans.value = res.items ?? [];
    // 首次进入时自动选中最近一次扫描，省去用户一次点击。
    if (selectedScanId.value === null && scans.value.length > 0) {
      await selectScan(scans.value[0].id);
    }
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载扫描历史失败");
  }
}

async function loadLibraries() {
  try {
    const res = await fetchEmbyMissingLibraries();
    libraries.value = res.items ?? [];
  } catch {
    // 媒体库拉取失败不阻断主流程（扫描接口允许 library_ids 为空 = 全部库），静默降级。
    libraries.value = [];
  }
}

async function selectScan(scanId: number) {
  selectedScanId.value = scanId;
  selectedResultIds.value = [];
  eventsOpenFor.value = null;
  try {
    const [res, evts] = await Promise.all([
      fetchEmbyMissingResults(scanId, 200),
      fetchEmbyMissingEvents(scanId, 100),
    ]);
    results.value = res.items ?? [];
    events.value = evts.items ?? [];
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载缺集结果失败");
  }
}

async function refreshAll() {
  loading.value = true;
  errorMsg.value = "";
  try {
    await loadStatus();
    // 未配置就自动展开配置区：这是用户进来第一件要做的事，
    // 省掉一次「点立即配置」的点击。
    if (configNeeded.value) configOpen.value = true;
    await Promise.all([loadScans(), loadLibraries(), loadConfig()]);
  } finally {
    loading.value = false;
  }
}

function startPolling() {
  if (pollTimer !== null) return;
  pollTimer = window.setInterval(async () => {
    await loadStatus();
    // 扫描结束的那一刻刷新一次列表，把新结果带出来。
    if (!status.value?.active_scan) {
      stopPolling();
      await loadScans();
    }
  }, POLL_INTERVAL_MS);
}

async function runScan() {
  if (scanning.value) return;
  scanning.value = true;
  errorMsg.value = "";
  try {
    await startEmbyMissingScan([]);
    toast.success("缺集扫描已启动");
    await loadStatus();
    startPolling();
  } catch (e) {
    const msg = getApiErrorMessage(e, "启动缺集扫描失败");
    errorMsg.value = msg;
    toast.error(msg);
  } finally {
    scanning.value = false;
  }
}

function toggleResult(id: number) {
  const idx = selectedResultIds.value.indexOf(id);
  if (idx >= 0) selectedResultIds.value.splice(idx, 1);
  else selectedResultIds.value.push(id);
}

function toggleAll() {
  selectedResultIds.value = allSelected.value ? [] : selectableResults.value.map((r) => r.id);
}

async function submitSubscriptions() {
  if (subscribing.value) return;
  if (selectedResultIds.value.length === 0) {
    toast.warning("请先勾选要补档的剧集");
    return;
  }
  if (selectedScanId.value === null) {
    toast.warning("请先选择一次扫描结果");
    return;
  }
  subscribing.value = true;
  try {
    await createEmbyMissingSubscriptions({
      result_ids: [...selectedResultIds.value],
      scan_id: selectedScanId.value,
      target_provider: targetProvider.value,
      transfer_mode: "auto",
      interval_minutes: intervalMinutes.value,
      enabled: true,
    });
    toast.success(`已创建 ${selectedResultIds.value.length} 个补档订阅`);
    selectedResultIds.value = [];
    await Promise.all([loadScans(), loadStatus()]);
  } catch (e) {
    const msg = getApiErrorMessage(e, "创建补档订阅失败");
    toast.error(msg);
  } finally {
    subscribing.value = false;
  }
}

async function toggleEvents(scanId: number) {
  if (eventsOpenFor.value === scanId) {
    eventsOpenFor.value = null;
    return;
  }
  eventsOpenFor.value = scanId;
  try {
    const res = await fetchEmbyMissingEvents(scanId, 100);
    events.value = res.items ?? [];
  } catch (e) {
    toast.error(getApiErrorMessage(e, "加载扫描事件失败"));
  }
}

// --- 本功能专属 Emby 配置 ---------------------------------------------------
// ★ 缺集补档的 Emby 配置与其它功能（影巢反代等）完全隔离，后端不会替用户预填，
//   所以表单初始为空，必须由用户自己填写。这里也刻意不读全局 Emby 配置。
const configOpen = ref(false);
const configLoading = ref(false);
const configSaving = ref(false);
const configTesting = ref(false);
const configMsg = ref("");
const configError = ref("");
/** 已保存过 API Key 时提示「留空即沿用」，避免用户以为必须重填 */
const apiKeySaved = ref(false);
const configForm = reactive({
  enabled: false,
  serverUrl: "",
  apiKey: "",
});

/** 未配置时自动展开配置区，用户一进来就知道要自己填 */
const configNeeded = computed(() => !embyInfo.value.configured);

async function loadConfig() {
  configLoading.value = true;
  configError.value = "";
  try {
    const res = await fetchEmbyMissingConfig();
    configForm.enabled = res.enabled ?? false;
    // 后端未配置时返回空串 → 表单留空（这正是本次要保证的行为）
    configForm.serverUrl = res.server_url ?? "";
    configForm.apiKey = "";
    apiKeySaved.value = res.api_key_set ?? false;
  } catch (e) {
    configError.value = getApiErrorMessage(e, "加载 Emby 配置失败");
  } finally {
    configLoading.value = false;
  }
}

async function saveConfig() {
  if (configSaving.value) return;
  configError.value = "";
  configMsg.value = "";
  if (configForm.enabled && !configForm.serverUrl.trim()) {
    configError.value = "启用前请先填写 Emby 服务器地址";
    return;
  }
  if (configForm.enabled && !configForm.apiKey.trim() && !apiKeySaved.value) {
    configError.value = "首次配置需同时填写 API Key";
    return;
  }
  configSaving.value = true;
  try {
    const res = await saveEmbyMissingConfig({
      enabled: configForm.enabled,
      server_url: configForm.serverUrl.trim(),
      api_key: configForm.apiKey.trim() || undefined,
    });
    apiKeySaved.value = res.api_key_set ?? apiKeySaved.value;
    configForm.apiKey = "";
    configMsg.value = res.configured
      ? "配置已保存，可点击「测试连接」确认可达"
      : "配置已保存（尚未具备扫描条件）";
    toast.success("Emby 配置已保存");
    await loadStatus();
    configOpen.value = false;
  } catch (e) {
    configError.value = getApiErrorMessage(e, "保存 Emby 配置失败");
  } finally {
    configSaving.value = false;
  }
}

async function testConfig() {
  if (configTesting.value) return;
  configError.value = "";
  configMsg.value = "";
  if (!configForm.serverUrl.trim()) {
    configError.value = "请先填写 Emby 服务器地址";
    return;
  }
  configTesting.value = true;
  try {
    const res = await testEmbyMissingConfig({
      server_url: configForm.serverUrl.trim(),
      api_key: configForm.apiKey.trim() || undefined,
    });
    configMsg.value = `连接成功：识别到 ${res.library_count ?? 0} 个剧集媒体库`;
    toast.success("Emby 连接成功");
  } catch (e) {
    configError.value = getApiErrorMessage(e, "无法连接 Emby");
  } finally {
    configTesting.value = false;
  }
}

onMounted(refreshAll);
onUnmounted(stopPolling);
</script>

<template>
  <div class="emby-missing">
    <AppStateBlock
      v-if="loading && !status"
      message="正在加载 Emby 缺集状态…"
      loading
      min-height="220px"
    />

    <template v-else>
      <div v-if="errorMsg" class="em__banner em__banner--error">{{ errorMsg }}</div>

      <!--
        状态分三类，语气必须不同：
        - unconfigured：本功能还没配 → 引导去填，不是报错
        - unreachable：填了但连不上 → 这是真故障，用 error 色
        - disabled：填了没启用 → 提示开启即可
      -->
      <div
        v-if="!ready"
        class="em__banner"
        :class="
          embyInfo.state === 'unreachable' ? 'em__banner--error' : 'em__banner--warn'
        "
      >
        <span>{{ embyInfo.message || "请先完成本功能的 Emby 配置" }}</span>
        <AppButton size="sm" variant="secondary" @click="configOpen = !configOpen">
          {{ configOpen ? "收起配置" : "立即配置" }}
        </AppButton>
      </div>
      <div v-else-if="embyInfo.state === 'disabled'" class="em__banner em__banner--warn">
        {{ embyInfo.message || "Emby 已填写但未启用" }}
      </div>

      <!-- 本功能专属 Emby 配置（★ 与其它功能的全局 Emby 配置隔离，需用户自行填写） -->
      <SettingsCard title="Emby 服务器配置（缺集补档专用）">
        <template #head-actions>
          <AppButton variant="ghost" size="sm" @click="configOpen = !configOpen">
            {{ configOpen ? "收起" : "编辑配置" }}
          </AppButton>
        </template>

        <p class="em__config-hint">
          本功能的服务器地址与 API Key 独立保存，<strong>不会继承</strong>影巢反代等其它功能的
          Emby 配置，请按下方的空白表单自行填写。
        </p>

        <div class="em__meta">
          <span>服务器：{{ embyInfo.server_url || "未配置" }}</span>
          <span>
            状态：
            <AppBadge :tone="embyStateTone(embyInfo.state)">
              {{ embyStateLabel(embyInfo.state) }}
            </AppBadge>
          </span>
        </div>

        <div v-if="configOpen" class="em__config">
          <AppStateBlock v-if="configLoading" message="正在加载配置…" loading min-height="80px" />
          <template v-else>
            <label class="em__config-toggle">
              <input v-model="configForm.enabled" type="checkbox" />
              <span>启用缺集扫描</span>
            </label>

            <FormField label="Emby 服务器地址">
              <AppInput
                v-model="configForm.serverUrl"
                placeholder="例如 http://192.168.1.10:8096"
                ignore-autofill
              />
            </FormField>

            <FormField label="API Key">
              <AppInput
                v-model="configForm.apiKey"
                type="password"
                :placeholder="apiKeySaved ? '已保存，留空表示不修改' : '请填写 Emby 的 API Key'"
                ignore-autofill
              />
            </FormField>

            <div class="em__config-actions">
              <AppButton variant="primary" size="sm" :disabled="configSaving" @click="saveConfig">
                {{ configSaving ? "保存中…" : "保存配置" }}
              </AppButton>
              <AppButton variant="secondary" size="sm" :disabled="configTesting" @click="testConfig">
                {{ configTesting ? "测试中…" : "测试连接" }}
              </AppButton>
            </div>

            <p v-if="configMsg" class="em__config-msg">{{ configMsg }}</p>
            <p v-if="configError" class="em__config-msg em__config-msg--error">{{ configError }}</p>
          </template>
        </div>
      </SettingsCard>

      <SettingsCard title="缺集扫描">
        <template #head-actions>
          <AppButton variant="primary" :disabled="scanning || !ready || !!activeScan" @click="runScan">
            {{ scanning ? "启动中…" : activeScan ? "扫描进行中" : "开始扫描" }}
          </AppButton>
        </template>

        <div class="em__meta">
          <span>服务器：{{ embyInfo.server_url || "未配置" }}</span>
          <span>补档订阅：{{ status?.subscription_count ?? 0 }} 个</span>
          <span>
            自动扫描：{{ status?.settings?.auto_scan ? "已开启" : "已关闭" }}
            （每 {{ status?.settings?.scan_interval_minutes ?? 720 }} 分钟）
          </span>
          <span>自动建订阅：{{ status?.settings?.auto_create_subscriptions ? "已开启" : "已关闭" }}</span>
        </div>

        <div v-if="activeScan" class="em__progress">
          <div class="em__progress-head">
            <AppBadge :tone="scanStatusTone(activeScan.status)">
              {{ scanStatusLabel(activeScan.status) }}
            </AppBadge>
            <span>
              已扫描 {{ activeScan.scanned_series ?? 0 }} / {{ activeScan.total_series ?? 0 }} 部剧
              <template v-if="scanProgress !== null">（{{ scanProgress }}%）</template>
            </span>
            <span v-if="activeScan.phase" class="em__phase">{{ activeScan.phase }}</span>
          </div>
          <div class="em__progress-track">
            <div class="em__progress-fill" :style="{ width: `${scanProgress ?? 0}%` }" />
          </div>
          <div class="em__progress-foot">
            缺集剧集 {{ activeScan.missing_series ?? 0 }} 部 / 共缺 {{ activeScan.missing_episodes ?? 0 }} 集
            <template v-if="activeScan.error_series">，{{ activeScan.error_series }} 部出错</template>
          </div>
          <div v-if="activeScan.message" class="em__progress-msg">{{ activeScan.message }}</div>
        </div>
      </SettingsCard>

      <SettingsCard title="扫描历史">
        <AppStateBlock v-if="scans.length === 0" message="还没有扫描记录" min-height="120px" />
        <div v-else class="admin-table-wrap">
          <table class="admin-table">
            <thead>
              <tr>
                <th>扫描</th>
                <th>状态</th>
                <th>剧集（扫/缺）</th>
                <th>缺集数</th>
                <th>开始时间</th>
                <th>结束时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in scans" :key="s.id">
                <td>#{{ s.id }}</td>
                <td>
                  <AppBadge :tone="scanStatusTone(s.status)">{{ scanStatusLabel(s.status) }}</AppBadge>
                </td>
                <td>{{ s.scanned_series ?? 0 }} / {{ s.missing_series ?? 0 }}</td>
                <td>{{ s.missing_episodes ?? 0 }}</td>
                <td>{{ formatTime(s.started_at ?? s.created_at) }}</td>
                <td>{{ formatTime(s.finished_at) }}</td>
                <td class="em__row-actions">
                  <AppButton variant="secondary" @click="selectScan(s.id)">查看结果</AppButton>
                  <AppButton variant="ghost" @click="toggleEvents(s.id)">
                    {{ eventsOpenFor === s.id ? "收起事件" : "事件" }}
                  </AppButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-if="eventsOpenFor !== null" class="em__events">
          <div class="em__events-title">扫描 #{{ eventsOpenFor }} 事件</div>
          <AppStateBlock v-if="events.length === 0" message="暂无事件" min-height="80px" />
          <ul v-else class="em__events-list">
            <li v-for="e in events" :key="e.id">
              <span class="em__events-time">{{ formatTime(e.created_at) }}</span>
              <AppBadge :tone="e.status === 'failed' ? 'danger' : e.status === 'success' ? 'success' : 'neutral'">
                {{ e.event_type || e.status }}
              </AppBadge>
              <span>{{ e.message }}</span>
            </li>
          </ul>
        </div>
      </SettingsCard>

      <SettingsCard title="缺集结果">
        <template #head-aside>
          <template v-if="results.length > 0">
            <AppSelect v-model="targetProvider" :options="TARGET_PROVIDER_OPTIONS" />
            <AppButton
              variant="primary"
              :disabled="subscribing || selectedResultIds.length === 0"
              @click="submitSubscriptions"
            >
              {{ subscribing ? "创建中…" : `为选中 ${selectedResultIds.length} 部剧补档` }}
            </AppButton>
          </template>
        </template>

        <AppStateBlock
          v-if="results.length === 0"
          :message="selectedScanId === null ? '请先在上方选择一次扫描' : '该次扫描没有发现缺集剧集'"
          min-height="140px"
        />
        <div v-else class="admin-table-wrap">
          <table class="admin-table">
            <thead>
              <tr>
                <th class="em__col-check">
                  <input type="checkbox" :checked="allSelected" @change="toggleAll" />
                </th>
                <th>剧名</th>
                <th>媒体库</th>
                <th>TMDB</th>
                <th>已有/缺</th>
                <th>缺集明细</th>
                <th>补档订阅</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in results" :key="r.id">
                <td class="em__col-check">
                  <input
                    type="checkbox"
                    :checked="selectedResultIds.includes(r.id)"
                    @change="toggleResult(r.id)"
                  />
                </td>
                <td>
                  <div class="em__title">{{ r.title || r.series_key }}</div>
                  <div class="em__sub">
                    {{ r.production_year || "-" }}
                    <template v-if="r.series_status"> · {{ r.series_status }}</template>
                  </div>
                </td>
                <td>{{ r.library_name || "-" }}</td>
                <td>{{ r.tmdb_id || "-" }}</td>
                <td>{{ r.available_count }} / {{ r.missing_count }}</td>
                <td class="em__eps">{{ episodeSummary(r) }}</td>
                <td>
                  <AppBadge v-if="r.subscription_id" tone="success">已建 #{{ r.subscription_id }}</AppBadge>
                  <span v-else class="em__sub">未建</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </SettingsCard>
    </template>
  </div>
</template>

<style scoped>
.emby-missing {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.em__banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  border-radius: 8px;
  font-size: 14px;
}

.em__banner--error {
  background: color-mix(in srgb, var(--danger, #d33) 12%, transparent);
  color: var(--danger, #d33);
}

.em__banner--warn {
  background: color-mix(in srgb, var(--warning, #d90) 14%, transparent);
  color: var(--warning, #d90);
}

.em__config-hint {
  margin: 0 0 12px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--text-muted);
}

.em__config {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface-2, rgba(127, 127, 127, 0.06));
}

.em__config-toggle {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text-regular);
  cursor: pointer;
  user-select: none;
}

.em__config-actions {
  display: flex;
  gap: 8px;
}

.em__config-msg {
  margin: 0;
  font-size: 13px;
  color: var(--text-muted);
}

.em__config-msg--error {
  color: var(--danger, #d33);
}

.em__meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 20px;
  margin-bottom: 14px;
  color: var(--text-muted);
  font-size: 13px;
}

.em__progress {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 14px;
  border-radius: 8px;
  background: var(--surface-2, rgba(127, 127, 127, 0.06));
}

.em__progress-head {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 14px;
}

.em__phase {
  color: var(--text-muted);
  font-size: 13px;
}

.em__progress-track {
  height: 8px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--surface-3, rgba(127, 127, 127, 0.16));
}

.em__progress-fill {
  height: 100%;
  border-radius: 999px;
  background: var(--brand);
  transition: width 0.3s ease;
}

.em__progress-foot,
.em__progress-msg {
  color: var(--text-muted);
  font-size: 13px;
}

.em__row-actions {
  display: flex;
  gap: 8px;
}

.em__events {
  margin-top: 14px;
}

.em__events-title {
  margin-bottom: 8px;
  font-weight: 600;
  font-size: 14px;
}

.em__events-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 280px;
  margin: 0;
  padding: 0;
  overflow-y: auto;
  list-style: none;
  font-size: 13px;
}

.em__events-list li {
  display: flex;
  align-items: center;
  gap: 10px;
}

.em__events-time {
  flex-shrink: 0;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
}

.em__col-check {
  width: 40px;
  text-align: center;
}

.em__title {
  font-weight: 500;
}

.em__sub {
  color: var(--text-muted);
  font-size: 12px;
}

.em__eps {
  max-width: 360px;
  color: var(--text-muted);
  font-size: 12px;
  line-height: 1.5;
}
</style>
