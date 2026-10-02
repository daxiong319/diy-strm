<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  cancelMoviePilotUploadTask,
  createMoviePilotSubscribe,
  deleteMoviePilotSubscribe,
  fetchMoviePilotDownloads,
  fetchMoviePilotFailedFiles,
  fetchMoviePilotOrganizeHistory,
  fetchMoviePilotSetting,
  fetchMoviePilotSubscribes,
  fetchMoviePilotUploadTasks,
  identifyMoviePilotFailedFile,
  resolveMoviePilotFailedFile,
  retryMoviePilotUploadTask,
  searchMoviePilotSubscribe,
  skipMoviePilotFailedFile,
  testMoviePilotConnection,
  updateMoviePilotSetting,
  updateMoviePilotSubscribeStatus,
  type MoviePilotConfig,
  type MoviePilotConfigInput,
  type MoviePilotDownload,
  type MoviePilotFailedFile,
  type MoviePilotIdentifyResult,
  type MoviePilotOrganizeHistory,
  type MoviePilotSubscribe,
  type MoviePilotUploadTask,
} from "@/api/moviepilot";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import FormField from "@/components/base/FormField.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import SectionTabBar from "@/components/admin/SectionTabBar.vue";
import AdminStatusPill from "@/components/admin/AdminStatusPill.vue";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import SvgIcon from "@/components/icons/SvgIcon.vue";
import { useSectionTabRoute } from "@/composables/useSectionTabRoute";
import { confirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import { formatTime } from "@/utils/format";
import "@/styles/admin-table.css";

const SETTING_TAB = "setting";
const SUBSCRIBE_TAB = "subscribe";
const DOWNLOAD_TAB = "download";
const UPLOAD_TAB = "upload";
const FAILED_TAB = "failed";
const HISTORY_TAB = "history";

const tabs = [
  { key: SETTING_TAB, label: "设置" },
  { key: SUBSCRIBE_TAB, label: "订阅" },
  { key: DOWNLOAD_TAB, label: "下载" },
  { key: UPLOAD_TAB, label: "上传任务" },
  { key: FAILED_TAB, label: "失败文件" },
  { key: HISTORY_TAB, label: "整理历史" },
];

const { activeTab, setActiveTab } = useSectionTabRoute(SETTING_TAB, [
  SETTING_TAB,
  SUBSCRIBE_TAB,
  DOWNLOAD_TAB,
  UPLOAD_TAB,
  FAILED_TAB,
  HISTORY_TAB,
]);

// 面板按 tab 惰性加载，首次访问后保持挂载。
const visited = reactive<Record<string, boolean>>({});
watch(
  activeTab,
  (tab) => {
    visited[tab] = true;
    void loadTab(tab);
  },
  { immediate: true },
);

function loadTab(tab: string) {
  if (tab === SETTING_TAB) return loadSetting();
  if (tab === SUBSCRIBE_TAB) return loadSubscribes();
  if (tab === DOWNLOAD_TAB) return loadDownloads();
  if (tab === UPLOAD_TAB) return loadUploadTasks();
  if (tab === FAILED_TAB) return loadFailedFiles();
  if (tab === HISTORY_TAB) return loadHistory();
  return Promise.resolve();
}

// ---------------- 设置 ----------------

const settingForm = reactive<MoviePilotConfig>({
  id: 0,
  enabled: false,
  base_url: "",
  api_token: "",
  download_root: "",
  local_view_root: "",
  upload_account_id: 0,
  upload_root: "",
  upload_root_id: "",
  strm_local_dir: "",
  poll_interval: 5,
  notify_enabled: false,
  category_config: "",
  promotion_order: "",
  promotion_patience_hours: 12,
  seed_retention_hours: 0,
  qbittorrent_url: "",
  qbittorrent_user: "",
  qbittorrent_pass: "",
  created_at: "",
  updated_at: "",
});

const settingLoading = ref(false);
const settingSaving = ref(false);
const testing = ref(false);
const settingError = ref("");

async function loadSetting() {
  settingLoading.value = true;
  settingError.value = "";
  try {
    const cfg = await fetchMoviePilotSetting();
    Object.assign(settingForm, cfg);
  } catch (error) {
    settingError.value = getApiErrorMessage(error, "请稍后重试");
    toast.error("加载 MoviePilot 设置失败: " + settingError.value);
  } finally {
    settingLoading.value = false;
  }
}

function buildSettingPayload(): MoviePilotConfigInput {
  return {
    enabled: settingForm.enabled,
    base_url: settingForm.base_url,
    api_token: settingForm.api_token,
    download_root: settingForm.download_root,
    local_view_root: settingForm.local_view_root,
    upload_account_id: Number(settingForm.upload_account_id) || 0,
    upload_root: settingForm.upload_root,
    upload_root_id: settingForm.upload_root_id,
    strm_local_dir: settingForm.strm_local_dir,
    poll_interval: Number(settingForm.poll_interval) || 0,
    notify_enabled: settingForm.notify_enabled,
    category_config: settingForm.category_config,
    promotion_order: settingForm.promotion_order,
    promotion_patience_hours: Number(settingForm.promotion_patience_hours) || 0,
    seed_retention_hours: Number(settingForm.seed_retention_hours) || 0,
    qbittorrent_url: settingForm.qbittorrent_url,
    qbittorrent_user: settingForm.qbittorrent_user,
    qbittorrent_pass: settingForm.qbittorrent_pass,
  };
}

async function saveSetting() {
  if (settingSaving.value) return;
  settingSaving.value = true;
  try {
    const cfg = await updateMoviePilotSetting(buildSettingPayload());
    Object.assign(settingForm, cfg);
    toast.success("设置已保存");
  } catch (error) {
    toast.error("保存设置失败: " + getApiErrorMessage(error, "请稍后重试"));
  } finally {
    settingSaving.value = false;
  }
}

async function testConnection() {
  if (testing.value) return;
  testing.value = true;
  try {
    // 已保存过配置时允许后端回退，因此这里始终提交当前表单值。
    const res = await testMoviePilotConnection({
      base_url: settingForm.base_url,
      api_token: settingForm.api_token,
    });
    toast.success(res?.message || "连接成功");
  } catch (error) {
    toast.error("连接失败: " + getApiErrorMessage(error, "请检查地址与令牌"));
  } finally {
    testing.value = false;
  }
}

// ---------------- 订阅 ----------------

const subscribes = ref<MoviePilotSubscribe[]>([]);
const subscribesLoading = ref(false);
const subscribesError = ref("");
const subscribeSaving = ref(false);
const createOpen = ref(false);
const creating = ref(false);
const createForm = reactive({
  name: "",
  year: "",
  type: "movie" as "movie" | "tv",
  tmdbid: "",
  season: "1",
  total_episode: "0",
  save_path: "",
  include: "",
});

async function loadSubscribes() {
  subscribesLoading.value = true;
  subscribesError.value = "";
  try {
    subscribes.value = (await fetchMoviePilotSubscribes()) ?? [];
  } catch (error) {
    subscribesError.value = getApiErrorMessage(error, "请稍后重试");
    toast.error("加载订阅失败: " + subscribesError.value);
  } finally {
    subscribesLoading.value = false;
  }
}

function openCreate() {
  createForm.name = "";
  createForm.year = "";
  createForm.type = "movie";
  createForm.tmdbid = "";
  createForm.season = "1";
  createForm.total_episode = "0";
  createForm.save_path = "";
  createForm.include = "";
  createOpen.value = true;
}

async function submitCreate() {
  if (!createForm.name.trim()) {
    toast.error("请填写名称");
    return;
  }
  creating.value = true;
  try {
    await createMoviePilotSubscribe({
      name: createForm.name.trim(),
      year: createForm.year.trim(),
      type: createForm.type,
      tmdbid: Number(createForm.tmdbid) || 0,
      season: Number(createForm.season) || 0,
      total_episode: Number(createForm.total_episode) || 0,
      save_path: createForm.save_path.trim(),
      include: createForm.include.trim(),
    });
    toast.success("订阅已创建");
    createOpen.value = false;
    await loadSubscribes();
  } catch (error) {
    toast.error("创建订阅失败: " + getApiErrorMessage(error, "请稍后重试"));
  } finally {
    creating.value = false;
  }
}

async function searchSubscribe(item: MoviePilotSubscribe) {
  subscribeSaving.value = true;
  try {
    const res = await searchMoviePilotSubscribe(item.id);
    toast.success(res?.message || "已触发搜索");
  } catch (error) {
    toast.error("触发搜索失败: " + getApiErrorMessage(error, "请稍后重试"));
  } finally {
    subscribeSaving.value = false;
  }
}

async function toggleSubscribe(item: MoviePilotSubscribe) {
  subscribeSaving.value = true;
  try {
    // R-订阅中 -> S-停止；其余状态（含 P-完成）恢复为 R。
    const next = item.state === "R" ? "S" : "R";
    const res = await updateMoviePilotSubscribeStatus(item.id, next);
    toast.success(res?.message || "已更新");
    await loadSubscribes();
  } catch (error) {
    toast.error("更新订阅状态失败: " + getApiErrorMessage(error, "请稍后重试"));
  } finally {
    subscribeSaving.value = false;
  }
}

async function removeSubscribe(item: MoviePilotSubscribe) {
  try {
    await confirm({
      title: "删除订阅",
      message: `确定删除订阅「${item.name}」吗？`,
      confirmText: "删除",
      danger: true,
    });
  } catch {
    return;
  }
  subscribeSaving.value = true;
  try {
    const res = await deleteMoviePilotSubscribe(item.id);
    toast.success(res?.message || "已删除");
    await loadSubscribes();
  } catch (error) {
    toast.error("删除订阅失败: " + getApiErrorMessage(error, "请稍后重试"));
  } finally {
    subscribeSaving.value = false;
  }
}

function subscribeStateLabel(state: string) {
  if (state === "R") return "订阅中";
  if (state === "P") return "已完成";
  if (state === "S") return "已停止";
  return state || "-";
}

function subscribeStateTone(state: string): "success" | "warning" | "brand" | "danger" | "muted" {
  if (state === "R") return "brand";
  if (state === "P") return "success";
  if (state === "S") return "muted";
  return "muted";
}

function subscribeTypeLabel(type: string) {
  if (type === "movie") return "电影";
  if (type === "tv") return "电视剧";
  return type || "-";
}

// ---------------- 下载 ----------------

const downloads = ref<MoviePilotDownload[]>([]);
const downloadsLoading = ref(false);
const downloadsError = ref("");

async function loadDownloads() {
  downloadsLoading.value = true;
  downloadsError.value = "";
  try {
    downloads.value = (await fetchMoviePilotDownloads()) ?? [];
  } catch (error) {
    downloadsError.value = getApiErrorMessage(error, "请稍后重试");
    toast.error("加载下载任务失败: " + downloadsError.value);
  } finally {
    downloadsLoading.value = false;
  }
}

function downloadProgress(item: MoviePilotDownload) {
  const value = Number(item.progress) || 0;
  return Math.max(0, Math.min(100, value));
}

// ---------------- 上传任务 ----------------

const uploadTasks = ref<MoviePilotUploadTask[]>([]);
const uploadLoading = ref(false);
const uploadError = ref("");
const uploadActionId = ref<number | null>(null);
const uploadStatus = ref("");

async function loadUploadTasks() {
  uploadLoading.value = true;
  uploadError.value = "";
  try {
    const page = await fetchMoviePilotUploadTasks(1, 50, uploadStatus.value);
    uploadTasks.value = page?.items ?? [];
  } catch (error) {
    uploadError.value = getApiErrorMessage(error, "请稍后重试");
    toast.error("加载上传任务失败: " + uploadError.value);
  } finally {
    uploadLoading.value = false;
  }
}

async function retryUpload(item: MoviePilotUploadTask) {
  uploadActionId.value = item.id;
  try {
    const res = await retryMoviePilotUploadTask(item.id);
    toast.success(res?.queued ? "已重新排队" : "已提交重试");
    await loadUploadTasks();
  } catch (error) {
    toast.error("重试失败: " + getApiErrorMessage(error, "请稍后重试"));
  } finally {
    uploadActionId.value = null;
  }
}

async function cancelUpload(item: MoviePilotUploadTask) {
  uploadActionId.value = item.id;
  try {
    const res = await cancelMoviePilotUploadTask(item.id);
    toast.success(res?.message || "已取消");
    await loadUploadTasks();
  } catch (error) {
    toast.error("取消失败: " + getApiErrorMessage(error, "请稍后重试"));
  } finally {
    uploadActionId.value = null;
  }
}

function uploadProgress(item: MoviePilotUploadTask) {
  if (item.total_bytes > 0) {
    return Math.max(0, Math.min(100, (item.uploaded_bytes / item.total_bytes) * 100));
  }
  if (item.total_files > 0) {
    return Math.max(0, Math.min(100, (item.uploaded_files / item.total_files) * 100));
  }
  return 0;
}

// ---------------- 失败文件 ----------------

const failedFiles = ref<MoviePilotFailedFile[]>([]);
const failedLoading = ref(false);
const failedError = ref("");
const failedStatus = ref("");
const failedActionId = ref<number | null>(null);
const resolveOpen = ref(false);
const resolving = ref(false);
const resolveTarget = ref<MoviePilotFailedFile | null>(null);
const identifyResult = ref<MoviePilotIdentifyResult | null>(null);
const identifyingId = ref<number | null>(null);
const resolveForm = reactive({
  media_type: "movie",
  title: "",
  year: "",
  season: "0",
  tmdb_id: "",
});

async function loadFailedFiles() {
  failedLoading.value = true;
  failedError.value = "";
  try {
    const page = await fetchMoviePilotFailedFiles(1, 50, failedStatus.value);
    failedFiles.value = page?.items ?? [];
  } catch (error) {
    failedError.value = getApiErrorMessage(error, "请稍后重试");
    toast.error("加载失败文件失败: " + failedError.value);
  } finally {
    failedLoading.value = false;
  }
}

/** 先调用后端识别接口拿到建议值，再填充到手工整理表单。 */
async function identifyFailedFile(item: MoviePilotFailedFile) {
  identifyingId.value = item.id;
  identifyResult.value = null;
  try {
    const result = await identifyMoviePilotFailedFile(item.id);
    identifyResult.value = result ?? null;
    resolveTarget.value = item;
    resolveForm.media_type = result?.category === "tv" ? "tv" : "movie";
    resolveForm.title = result?.title || item.title || "";
    resolveForm.year = String(result?.year || item.year || "");
    resolveForm.season = String(result?.season ?? item.season ?? 0);
    resolveForm.tmdb_id = String(result?.tmdb_id || item.tmdb_id || "");
    resolveOpen.value = true;
    toast.success("已识别，请确认后整理");
  } catch (error) {
    toast.error("识别失败: " + getApiErrorMessage(error, "请稍后重试"));
  } finally {
    identifyingId.value = null;
  }
}

function openResolve(item: MoviePilotFailedFile) {
  identifyResult.value = null;
  resolveTarget.value = item;
  resolveForm.media_type = item.media_type === "tv" ? "tv" : "movie";
  resolveForm.title = item.title || "";
  resolveForm.year = String(item.year || "");
  resolveForm.season = String(item.season ?? 0);
  resolveForm.tmdb_id = String(item.tmdb_id || "");
  resolveOpen.value = true;
}

async function submitResolve() {
  const target = resolveTarget.value;
  if (!target) return;
  if (!resolveForm.title.trim()) {
    toast.error("请填写标题");
    return;
  }
  resolving.value = true;
  try {
    const res = await resolveMoviePilotFailedFile(target.id, {
      media_type: resolveForm.media_type,
      title: resolveForm.title.trim(),
      year: Number(resolveForm.year) || 0,
      season: Number(resolveForm.season) || 0,
      tmdb_id: Number(resolveForm.tmdb_id) || 0,
    });
    toast.success(res?.new_name ? `已整理为 ${res.new_name}` : "已提交整理");
    resolveOpen.value = false;
    await loadFailedFiles();
  } catch (error) {
    toast.error("整理失败: " + getApiErrorMessage(error, "请稍后重试"));
  } finally {
    resolving.value = false;
  }
}

async function skipFailedFile(item: MoviePilotFailedFile) {
  failedActionId.value = item.id;
  try {
    const res = await skipMoviePilotFailedFile(item.id);
    toast.success(res?.message || "已忽略");
    await loadFailedFiles();
  } catch (error) {
    toast.error("忽略失败: " + getApiErrorMessage(error, "请稍后重试"));
  } finally {
    failedActionId.value = null;
  }
}

// ---------------- 整理历史 ----------------

const history = ref<MoviePilotOrganizeHistory[]>([]);
const historyLoading = ref(false);
const historyError = ref("");

async function loadHistory() {
  historyLoading.value = true;
  historyError.value = "";
  try {
    history.value = (await fetchMoviePilotOrganizeHistory(100)) ?? [];
  } catch (error) {
    historyError.value = getApiErrorMessage(error, "请稍后重试");
    toast.error("加载整理历史失败: " + historyError.value);
  } finally {
    historyLoading.value = false;
  }
}

function historyStatusTone(status: string): "success" | "warning" | "brand" | "danger" | "muted" {
  const s = (status || "").toLowerCase();
  if (s === "success" || s === "done" || s === "ok") return "success";
  if (s === "failed" || s === "error") return "danger";
  if (s === "skip" || s === "skipped") return "muted";
  return "warning";
}

function rowTitle(item: MoviePilotFailedFile) {
  const name = item.title || item.file_name;
  const bits: string[] = [];
  if (item.year) bits.push(String(item.year));
  if (item.season) bits.push(`S${String(item.season).padStart(2, "0")}`);
  return bits.length ? `${name} (${bits.join(" · ")})` : name;
}

const isSettingTab = computed(() => activeTab.value === SETTING_TAB);
const isSubscribeTab = computed(() => activeTab.value === SUBSCRIBE_TAB);
const isDownloadTab = computed(() => activeTab.value === DOWNLOAD_TAB);
const isUploadTab = computed(() => activeTab.value === UPLOAD_TAB);
const isFailedTab = computed(() => activeTab.value === FAILED_TAB);
const isHistoryTab = computed(() => activeTab.value === HISTORY_TAB);

onMounted(() => {
  void loadTab(activeTab.value);
});
</script>

<template>
  <div class="moviepilot-page">
    <SectionTabBar :model-value="activeTab" :tabs="tabs" @update:model-value="setActiveTab">
      <template #actions>
        <AppButton v-if="isSettingTab" type="button" variant="secondary" :disabled="testing" @click="testConnection">
          <SvgIcon name="plug" size="1em" />
          {{ testing ? "测试中…" : "测试连接" }}
        </AppButton>
        <AppButton v-if="isSettingTab" type="button" variant="primary" :disabled="settingSaving" @click="saveSetting">
          {{ settingSaving ? "保存中…" : "保存设置" }}
        </AppButton>
        <AppButton v-if="isSubscribeTab" type="button" variant="primary" @click="openCreate">
          <SvgIcon name="plus" size="1em" />
          新增订阅
        </AppButton>
        <AppButton v-if="isDownloadTab" type="button" variant="secondary" :disabled="downloadsLoading" @click="loadDownloads">
          <SvgIcon name="rotate" size="1em" />
          刷新
        </AppButton>
        <AppButton v-if="isHistoryTab" type="button" variant="secondary" :disabled="historyLoading" @click="loadHistory">
          <SvgIcon name="rotate" size="1em" />
          刷新
        </AppButton>
      </template>
    </SectionTabBar>

    <!-- 设置 -->
    <div v-if="visited[SETTING_TAB]" v-show="isSettingTab">
      <AppStateBlock v-if="settingLoading" message="正在加载设置…" loading />
      <AppStateBlock v-else-if="settingError" :message="`加载设置失败：${settingError}`" />
      <div v-else class="moviepilot-setting">
        <SettingsCard title="基础配置" accent="var(--brand)">
          <div class="modal-form">
            <div class="modal-form__row">
              <FormField label="启用 MoviePilot 集成">
                <label class="moviepilot-switch">
                  <input v-model="settingForm.enabled" type="checkbox" />
                  <span>启用后才会轮询下载、做种与订阅</span>
                </label>
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="MoviePilot 地址">
                <AppInput v-model="settingForm.base_url" placeholder="http://192.168.1.10:3000" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="API 令牌">
                <AppInput
                  v-model="settingForm.api_token"
                  type="password"
                  ignore-autofill
                  placeholder="MoviePilot 用户设置中的 API Token"
                />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="下载目录">
                <AppInput v-model="settingForm.download_root" placeholder="/downloads" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="本地可浏览目录">
                <AppInput v-model="settingForm.local_view_root" placeholder="与下载目录对应的本地路径" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="STRM 本地目录">
                <AppInput v-model="settingForm.strm_local_dir" placeholder="STRM 文件生成目录" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="轮询间隔（分钟）">
                <AppInput v-model="settingForm.poll_interval" type="number" placeholder="5" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="开启通知">
                <label class="moviepilot-switch">
                  <input v-model="settingForm.notify_enabled" type="checkbox" />
                  <span>整理与上传结果推送到通知渠道</span>
                </label>
              </FormField>
            </div>
          </div>
        </SettingsCard>

        <SettingsCard title="上传目标" accent="var(--brand)">
          <div class="modal-form">
            <div class="modal-form__row">
              <FormField label="上传账号 ID">
                <AppInput v-model="settingForm.upload_account_id" type="number" placeholder="0" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="上传根目录">
                <AppInput v-model="settingForm.upload_root" placeholder="/媒体库" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="上传根目录 ID">
                <AppInput v-model="settingForm.upload_root_id" placeholder="选填，留空则按路径解析" />
              </FormField>
            </div>
          </div>
        </SettingsCard>

        <SettingsCard title="做种与促销策略" accent="var(--brand)">
          <div class="modal-form">
            <div class="modal-form__row">
              <FormField label="促销优先级">
                <AppInput v-model="settingForm.promotion_order" placeholder="free,2xfree,normal,half,2xhalf" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="促销等待时长（小时）">
                <AppInput v-model="settingForm.promotion_patience_hours" type="number" placeholder="12" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="做种保留时长（小时）">
                <AppInput v-model="settingForm.seed_retention_hours" type="number" placeholder="0（不自动删种）" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="分类映射">
                <AppInput v-model="settingForm.category_config" placeholder='如 {"电影":"movie","电视剧":"tv"}' />
              </FormField>
            </div>
          </div>
        </SettingsCard>

        <SettingsCard title="qBittorrent（选填）" accent="var(--brand)">
          <div class="modal-form">
            <div class="modal-form__row">
              <FormField label="qBittorrent 地址">
                <AppInput v-model="settingForm.qbittorrent_url" placeholder="http://192.168.1.10:8080" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="用户名">
                <AppInput v-model="settingForm.qbittorrent_user" placeholder="admin" />
              </FormField>
            </div>
            <div class="modal-form__row">
              <FormField label="密码">
                <AppInput v-model="settingForm.qbittorrent_pass" type="password" ignore-autofill placeholder="密码" />
              </FormField>
            </div>
          </div>
        </SettingsCard>
      </div>
    </div>

    <!-- 订阅 -->
    <div v-if="visited[SUBSCRIBE_TAB]" v-show="isSubscribeTab">
      <section class="admin-panel-table-wrap">
        <div class="panel-head">
          <div>
            <div class="panel-title">订阅</div>
            <div class="panel-sub">与 MoviePilot 上的订阅保持同步，可在此触发搜索、启停与删除。</div>
          </div>
        </div>
        <AppStateBlock v-if="subscribesLoading" message="正在加载订阅…" loading />
        <AppStateBlock v-else-if="subscribesError" :message="`加载订阅失败：${subscribesError}`" />
        <AdminEmptyState
          v-else-if="subscribes.length === 0"
          icon="film"
          title="暂无订阅"
          description="点右上角「新增订阅」创建第一条订阅。"
        />
        <div v-else class="table-wrap">
          <table class="admin-table">
            <thead>
              <tr>
                <th>名称</th>
                <th>类型</th>
                <th>状态</th>
                <th>进度</th>
                <th class="admin-table__actions">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in subscribes" :key="item.id">
                <td>
                  <div class="cell-strong">{{ item.name || "-" }}</div>
                  <div class="cell-sub">
                    {{ [item.year, item.keyword || item.tmdbid ? `TMDB ${item.tmdbid}` : ""].filter(Boolean).join(" · ") || "-" }}
                  </div>
                </td>
                <td>{{ subscribeTypeLabel(item.type) }}</td>
                <td>
                  <AdminStatusPill :tone="subscribeStateTone(item.state)">
                    {{ subscribeStateLabel(item.state) }}
                  </AdminStatusPill>
                </td>
                <td>
                  <div v-if="item.type === 'tv'" class="cell-sub">
                    已得 {{ item.total_episode - item.lack_episode }} / {{ item.total_episode }} 集
                  </div>
                  <div v-else class="cell-sub">-</div>
                </td>
                <td class="admin-table__actions">
                  <AppButton type="button" size="sm" variant="secondary" :disabled="subscribeSaving" @click="searchSubscribe(item)">
                    搜索
                  </AppButton>
                  <AppButton type="button" size="sm" variant="secondary" :disabled="subscribeSaving" @click="toggleSubscribe(item)">
                    {{ item.state === "R" ? "停止" : "启用" }}
                  </AppButton>
                  <AppButton type="button" size="sm" variant="danger" :disabled="subscribeSaving" @click="removeSubscribe(item)">
                    删除
                  </AppButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <!-- 下载 -->
    <div v-if="visited[DOWNLOAD_TAB]" v-show="isDownloadTab">
      <section class="admin-panel-table-wrap">
        <div class="panel-head">
          <div>
            <div class="panel-title">下载任务</div>
            <div class="panel-sub">来自 MoviePilot 下载器的实时任务（只读）。</div>
          </div>
        </div>
        <AppStateBlock v-if="downloadsLoading" message="正在加载下载任务…" loading />
        <AppStateBlock v-else-if="downloadsError" :message="`加载下载任务失败：${downloadsError}`" />
        <AdminEmptyState v-else-if="downloads.length === 0" icon="download" title="暂无下载任务" description="下载器中没有进行中的任务。" />
        <div v-else class="table-wrap">
          <table class="admin-table">
            <thead>
              <tr>
                <th>任务</th>
                <th>状态</th>
                <th>进度</th>
                <th>保存路径</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in downloads" :key="item.hash || item.title">
                <td>
                  <div class="cell-strong">{{ item.title || item.name || "-" }}</div>
                  <div class="cell-sub">{{ item.hash || "-" }}</div>
                </td>
                <td>
                  <AdminStatusPill :tone="downloadProgress(item) >= 100 ? 'success' : 'brand'">
                    {{ item.state || "-" }}
                  </AdminStatusPill>
                </td>
                <td>
                  <div class="progress">
                    <div class="progress__bar" :style="{ width: `${downloadProgress(item)}%` }" />
                  </div>
                  <div class="cell-sub">{{ downloadProgress(item).toFixed(1) }}%</div>
                </td>
                <td class="cell-path">{{ item.save_path || item.content_path || item.path || "-" }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <!-- 上传任务 -->
    <div v-if="visited[UPLOAD_TAB]" v-show="isUploadTab">
      <section class="admin-panel-table-wrap">
        <div class="panel-head">
          <div>
            <div class="panel-title">上传任务</div>
            <div class="panel-sub">下载完成后的上传队列，可重试失败任务或取消进行中的任务。</div>
          </div>
        </div>
        <AppStateBlock v-if="uploadLoading" message="正在加载上传任务…" loading />
        <AppStateBlock v-else-if="uploadError" :message="`加载上传任务失败：${uploadError}`" />
        <AdminEmptyState v-else-if="uploadTasks.length === 0" icon="upload" title="暂无上传任务" description="下载完成后会自动生成上传任务。" />
        <div v-else class="table-wrap">
          <table class="admin-table">
            <thead>
              <tr>
                <th>任务</th>
                <th>状态</th>
                <th>进度</th>
                <th>大小</th>
                <th class="admin-table__actions">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in uploadTasks" :key="item.id">
                <td>
                  <div class="cell-strong">{{ item.title || item.torrent_hash || `#${item.id}` }}</div>
                  <div class="cell-sub">{{ item.remote_path || item.local_path || "-" }}</div>
                  <div v-if="item.error" class="cell-sub cell-sub--danger">{{ item.error }}</div>
                </td>
                <td>
                  <AdminStatusPill :tone="item.is_running ? 'brand' : item.error ? 'danger' : 'muted'">
                    {{ item.is_running ? "进行中" : item.status || "-" }}
                  </AdminStatusPill>
                </td>
                <td>
                  <div class="progress">
                    <div class="progress__bar" :style="{ width: `${uploadProgress(item)}%` }" />
                  </div>
                  <div class="cell-sub">{{ item.uploaded_files }} / {{ item.total_files }} 个文件</div>
                </td>
                <td>{{ item.total_size_text || "-" }}</td>
                <td class="admin-table__actions">
                  <AppButton
                    type="button"
                    size="sm"
                    variant="secondary"
                    :disabled="uploadActionId === item.id || item.is_running"
                    @click="retryUpload(item)"
                  >
                    重试
                  </AppButton>
                  <AppButton
                    type="button"
                    size="sm"
                    variant="danger"
                    :disabled="uploadActionId === item.id || !item.is_running"
                    @click="cancelUpload(item)"
                  >
                    取消
                  </AppButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <!-- 失败文件 -->
    <div v-if="visited[FAILED_TAB]" v-show="isFailedTab">
      <section class="admin-panel-table-wrap">
        <div class="panel-head">
          <div>
            <div class="panel-title">识别失败文件</div>
            <div class="panel-sub">自动识别失败的文件，可重新识别、手工指定媒体信息后整理，或直接忽略。</div>
          </div>
        </div>
        <AppStateBlock v-if="failedLoading" message="正在加载失败文件…" loading />
        <AppStateBlock v-else-if="failedError" :message="`加载失败文件失败：${failedError}`" />
        <AdminEmptyState
          v-else-if="failedFiles.length === 0"
          icon="check-circle"
          title="没有失败文件"
          description="所有文件都已完成识别与整理。"
        />
        <div v-else class="table-wrap">
          <table class="admin-table">
            <thead>
              <tr>
                <th>文件</th>
                <th>状态</th>
                <th>失败原因</th>
                <th class="admin-table__actions">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in failedFiles" :key="item.id">
                <td>
                  <div class="cell-strong">{{ rowTitle(item) }}</div>
                  <div class="cell-sub">{{ item.file_name || "-" }}</div>
                  <div class="cell-sub">{{ item.root_path || "-" }}</div>
                </td>
                <td>
                  <AdminStatusPill tone="warning">{{ item.status || "待处理" }}</AdminStatusPill>
                </td>
                <td class="cell-path">{{ item.reason || "-" }}</td>
                <td class="admin-table__actions">
                  <AppButton
                    type="button"
                    size="sm"
                    variant="secondary"
                    :disabled="identifyingId === item.id"
                    @click="identifyFailedFile(item)"
                  >
                    {{ identifyingId === item.id ? "识别中…" : "重新识别" }}
                  </AppButton>
                  <AppButton type="button" size="sm" variant="secondary" @click="openResolve(item)">整理</AppButton>
                  <AppButton
                    type="button"
                    size="sm"
                    variant="danger"
                    :disabled="failedActionId === item.id"
                    @click="skipFailedFile(item)"
                  >
                    忽略
                  </AppButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <!-- 整理历史 -->
    <div v-if="visited[HISTORY_TAB]" v-show="isHistoryTab">
      <section class="admin-panel-table-wrap">
        <div class="panel-head">
          <div>
            <div class="panel-title">整理历史</div>
            <div class="panel-sub">最近 100 条整理记录（只读）。</div>
          </div>
        </div>
        <AppStateBlock v-if="historyLoading" message="正在加载整理历史…" loading />
        <AppStateBlock v-else-if="historyError" :message="`加载整理历史失败：${historyError}`" />
        <AdminEmptyState v-else-if="history.length === 0" icon="clock-rotate-left" title="暂无整理历史" description="完成首次整理后会在此显示。" />
        <div v-else class="table-wrap">
          <table class="admin-table">
            <thead>
              <tr>
                <th>文件</th>
                <th>类型</th>
                <th>目标</th>
                <th>状态</th>
                <th>时间</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in history" :key="item.id">
                <td>
                  <div class="cell-strong">{{ item.file_name || "-" }}</div>
                  <div class="cell-sub">{{ item.source_path || "-" }}</div>
                </td>
                <td>{{ subscribeTypeLabel(item.media_type) }}</td>
                <td class="cell-path">
                  <div>{{ item.target_path || "-" }}</div>
                  <div v-if="item.message" class="cell-sub">{{ item.message }}</div>
                </td>
                <td>
                  <AdminStatusPill :tone="historyStatusTone(item.status)">{{ item.status || "-" }}</AdminStatusPill>
                </td>
                <td>{{ item.created_at ? formatTime(item.created_at) : "-" }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <!-- 新增订阅 -->
    <AppModal :open="createOpen" title="新增订阅" size="md" @close="createOpen = false">
      <div class="modal-form">
        <div class="modal-form__row">
          <FormField label="名称" required>
            <AppInput v-model="createForm.name" placeholder="影片或剧集名称" />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="类型">
            <AppSelect
              v-model="createForm.type"
              :options="[
                { value: 'movie', label: '电影' },
                { value: 'tv', label: '电视剧' },
              ]"
            />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="年份">
            <AppInput v-model="createForm.year" placeholder="2026" />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="TMDB ID">
            <AppInput v-model="createForm.tmdbid" type="number" placeholder="留空则按名称搜索" />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="季号">
            <AppInput v-model="createForm.season" type="number" placeholder="1" />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="总集数">
            <AppInput v-model="createForm.total_episode" type="number" placeholder="0" />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="保存路径">
            <AppInput v-model="createForm.save_path" placeholder="留空使用 MoviePilot 默认目录" />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="包含关键词">
            <AppInput v-model="createForm.include" placeholder="选填，如 中字" />
          </FormField>
        </div>
      </div>
      <template #footer>
        <div class="modal-form__footer">
          <AppButton type="button" variant="secondary" @click="createOpen = false">取消</AppButton>
          <AppButton type="button" variant="primary" :disabled="creating" @click="submitCreate">
            {{ creating ? "提交中…" : "创建" }}
          </AppButton>
        </div>
      </template>
    </AppModal>

    <!-- 手工整理 -->
    <AppModal :open="resolveOpen" title="手工整理" size="md" @close="resolveOpen = false">
      <div class="modal-form">
        <div class="modal-form__row">
          <FormField label="文件">
            <div class="cell-sub">{{ resolveTarget?.file_name || "-" }}</div>
          </FormField>
        </div>
        <div v-if="identifyResult?.ai_quality" class="modal-form__row">
          <FormField label="识别线索">
            <div class="cell-sub">
              {{
                [
                  identifyResult.ai_quality.res_tag,
                  identifyResult.ai_quality.codec_tag,
                  identifyResult.ai_quality.video_format,
                  identifyResult.ai_quality.group,
                ]
                  .filter(Boolean)
                  .join(" · ") || "-"
              }}
            </div>
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="类型">
            <AppSelect
              v-model="resolveForm.media_type"
              :options="[
                { value: 'movie', label: '电影' },
                { value: 'tv', label: '电视剧' },
              ]"
            />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="标题" required>
            <AppInput v-model="resolveForm.title" placeholder="标准媒体名称" />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="年份">
            <AppInput v-model="resolveForm.year" type="number" placeholder="2026" />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="季号">
            <AppInput v-model="resolveForm.season" type="number" placeholder="0" />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="TMDB ID">
            <AppInput v-model="resolveForm.tmdb_id" type="number" placeholder="0" />
          </FormField>
        </div>
      </div>
      <template #footer>
        <div class="modal-form__footer">
          <AppButton type="button" variant="secondary" @click="resolveOpen = false">取消</AppButton>
          <AppButton type="button" variant="primary" :disabled="resolving" @click="submitResolve">
            {{ resolving ? "整理中…" : "确认整理" }}
          </AppButton>
        </div>
      </template>
    </AppModal>
  </div>
</template>

<style scoped>
.moviepilot-page {
  --panel: var(--surface);
  --soft: var(--surface-sunken);
  --line: var(--border);
  --ink: var(--text);
  --muted: var(--text-muted);
}

.moviepilot-setting {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.moviepilot-switch {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--muted);
  font-size: 13px;
  cursor: pointer;
}

.panel-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px 10px;
}

.panel-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--ink);
}

.panel-sub {
  margin-top: 4px;
  font-size: 12.5px;
  color: var(--muted);
}

.table-wrap {
  overflow-x: auto;
  padding: 0 4px 8px;
}

.cell-strong {
  font-weight: 600;
  color: var(--ink);
}

.cell-sub {
  margin-top: 2px;
  font-size: 12px;
  color: var(--muted);
  word-break: break-all;
}

.cell-sub--danger {
  color: var(--danger);
}

.cell-path {
  max-width: 320px;
  font-size: 12px;
  color: var(--muted);
  word-break: break-all;
}

.progress {
  width: 120px;
  height: 6px;
  border-radius: 999px;
  background: var(--soft);
  overflow: hidden;
}

.progress__bar {
  height: 100%;
  border-radius: 999px;
  background: var(--brand);
  transition: width 0.3s ease;
}
</style>
