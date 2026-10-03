<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  subtitleApi,
  type SubtitleConfig,
  type SubtitleConfigPatch,
  type SubtitleProviderError,
  type SubtitleProviderStatus,
  type SubtitleScoredCandidate,
  type SubtitleSearchPayload,
  type SubtitleSyncCheck,
  type SubtitleTask,
} from "@/api/subtitle";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import SettingsBoolSegment from "@/components/admin/SettingsBoolSegment.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import SettingsRow from "@/components/admin/SettingsRow.vue";
import SectionTabBar from "@/components/admin/SectionTabBar.vue";
import AdminStatusPill from "@/components/admin/AdminStatusPill.vue";
import SvgIcon from "@/components/icons/SvgIcon.vue";
import { useSectionTabRoute } from "@/composables/useSectionTabRoute";
import { toast } from "@/composables/useToast";
import { formatTime } from "@/utils/format";

// 字幕智能处理：搜索下载 + 智能匹配 + 时间轴校正。
// 挂在后台单页的「字幕处理」分区下，内部再分四个 tab。

const SEARCH_TAB = "search";
const SOURCE_TAB = "source";
const MATCH_TAB = "match";
const TASK_TAB = "task";

const tabs = [
  { key: SEARCH_TAB, label: "搜索下载" },
  { key: SOURCE_TAB, label: "字幕源" },
  { key: MATCH_TAB, label: "匹配设置" },
  { key: TASK_TAB, label: "任务记录" },
];
const { activeTab, setActiveTab } = useSectionTabRoute(SEARCH_TAB, [
  SEARCH_TAB,
  SOURCE_TAB,
  MATCH_TAB,
  TASK_TAB,
]);

const loading = ref(true);
const saving = ref(false);
const loadError = ref("");

const config = ref<SubtitleConfig | null>(null);

/** 表单只保存用户改过的字段；提交时只发这些 key，避免覆盖并发修改。 */
const dirty = reactive<SubtitleConfigPatch>({});
const dirtyKeys = computed(() => Object.keys(dirty));

/** 凭证输入框（后端只回传 "是否已配置"，不回明文）。 */
const secretInput = reactive({
  assrt: "",
  subhd: "",
  zimuku: "",
  opensubsApiKey: "",
  opensubsAccount: "",
  opensubsPassword: "",
});

const configForm = computed(() => config.value);

function markDirty<K extends keyof SubtitleConfigPatch>(key: K, value: SubtitleConfigPatch[K]) {
  const current = config.value;
  if (!current) return;
  const original = (current as unknown as Record<string, unknown>)[key as string];
  if (original === value) {
    delete dirty[key];
  } else {
    dirty[key] = value;
  }
}

const providers = ref<SubtitleProviderStatus[]>([]);
const providersLoading = ref(false);
const testingProvider = ref("");

const syncCheck = ref<SubtitleSyncCheck | null>(null);

// ---- 搜索表单 ----
const searchForm = reactive({
  title: "",
  originalTitle: "",
  year: 0,
  season: 0,
  episode: 0,
  mediaType: "movie",
  imdbId: "",
  videoPath: "",
  autoParsePath: true,
});

const searching = ref(false);
const searchResults = ref<SubtitleScoredCandidate[]>([]);
const providerErrors = ref<SubtitleProviderError[]>([]);
const searched = ref(false);

const downloadingSlug = ref("");
const downloadForm = reactive({ overwrite: false, autoSync: true });

// ---- 时间轴校正 ----
const syncFilePath = ref("");
const syncVideoPath = ref("");
const syncing = ref(false);

// ---- 任务记录 ----
const tasks = ref<SubtitleTask[]>([]);
const tasksLoading = ref(false);
const tasksMessage = ref("");
const taskPage = ref(1);
const taskTotal = ref(0);
const taskPageSize = 20;

const mediaTypeOptions = [
  { value: "movie", label: "电影" },
  { value: "tvshow", label: "剧集" },
];
const syncModeOptions = [
  { value: "vad", label: "语音对齐（VAD，推荐）" },
  { value: "offset", label: "仅整体平移" },
  { value: "scale", label: "按帧率缩放" },
  { value: "auto", label: "自动选择" },
];
const dirPolicyOptions = [
  { value: "same", label: "与视频同目录" },
  { value: "subtitle", label: "视频目录下的 subtitle/ 子目录" },
];

function errMsg(error: unknown, fallback: string) {
  return getApiErrorMessage(error, fallback);
}

// ---- 加载 ----

async function loadConfig() {
  loading.value = true;
  loadError.value = "";
  try {
    config.value = await subtitleApi.getConfig();
    for (const key of Object.keys(dirty)) delete dirty[key as keyof SubtitleConfigPatch];
  } catch (error) {
    loadError.value = errMsg(error, "加载字幕配置失败");
  } finally {
    loading.value = false;
  }
}

async function loadProviders() {
  providersLoading.value = true;
  try {
    const resp = await subtitleApi.providers();
    providers.value = resp.providers ?? [];
  } catch (error) {
    toast.error(errMsg(error, "加载字幕源状态失败"));
  } finally {
    providersLoading.value = false;
  }
}

async function loadSyncCheck() {
  try {
    syncCheck.value = await subtitleApi.syncCheck();
  } catch {
    // 检查失败不影响主流程：校正时后端还会再报一次错。
    syncCheck.value = null;
  }
}

async function loadTasks() {
  tasksLoading.value = true;
  try {
    const resp = await subtitleApi.tasks({ page: taskPage.value, page_size: taskPageSize });
    tasks.value = resp.tasks ?? [];
    taskTotal.value = resp.total ?? 0;
    tasksMessage.value = resp.message ?? "";
  } catch (error) {
    toast.error(errMsg(error, "加载任务记录失败"));
  } finally {
    tasksLoading.value = false;
  }
}

onMounted(async () => {
  await Promise.all([loadConfig(), loadProviders(), loadSyncCheck()]);
});

// ---- 保存 ----

async function saveConfig() {
  if (!config.value || dirtyKeys.value.length === 0) return;
  saving.value = true;
  try {
    const patch: SubtitleConfigPatch = { ...dirty };
    // 凭证字段只在用户真的填了东西时才发送（"没填"和"清空"无法区分）。
    if (secretInput.assrt.trim()) patch.assrt_api_key = secretInput.assrt.trim();
    if (secretInput.subhd.trim()) patch.subhd_cookie = secretInput.subhd.trim();
    if (secretInput.zimuku.trim()) patch.zimuku_cookie = secretInput.zimuku.trim();
    if (secretInput.opensubsApiKey.trim()) patch.opensubtitles_api_key = secretInput.opensubsApiKey.trim();
    if (secretInput.opensubsAccount.trim()) patch.opensubtitles_username = secretInput.opensubsAccount.trim();
    if (secretInput.opensubsPassword.trim()) patch.opensubtitles_password = secretInput.opensubsPassword;

    config.value = await subtitleApi.updateConfig(patch);
    secretInput.assrt = "";
    secretInput.subhd = "";
    secretInput.zimuku = "";
    secretInput.opensubsApiKey = "";
    secretInput.opensubsAccount = "";
    secretInput.opensubsPassword = "";
    for (const key of Object.keys(dirty)) delete dirty[key as keyof SubtitleConfigPatch];
    toast.success("字幕设置已保存");
    await loadProviders();
  } catch (error) {
    toast.error(errMsg(error, "保存字幕设置失败"));
  } finally {
    saving.value = false;
  }
}

function resetConfig() {
  for (const key of Object.keys(dirty)) delete dirty[key as keyof SubtitleConfigPatch];
  secretInput.assrt = "";
  secretInput.subhd = "";
  secretInput.zimuku = "";
  secretInput.opensubsApiKey = "";
  secretInput.opensubsAccount = "";
  secretInput.opensubsPassword = "";
}

async function testProvider(name: string) {
  testingProvider.value = name;
  try {
    const resp = await subtitleApi.testProvider(name);
    const item = (resp.providers ?? [])[0];
    if (item?.healthy) {
      toast.success(`${item.display_name} 连接正常（${item.latency_ms}ms）`);
    } else {
      toast.error(`${name}：${item?.message ?? "连接失败"}`);
    }
    await loadProviders();
  } catch (error) {
    toast.error(errMsg(error, "测试字幕源失败"));
  } finally {
    testingProvider.value = "";
  }
}

// ---- 搜索 ----

function buildSearchPayload(): SubtitleSearchPayload | null {
  const title = searchForm.title.trim();
  const videoPath = searchForm.videoPath.trim();
  if (!title && !videoPath) {
    toast.error("请至少填写标题或视频路径");
    return null;
  }
  if (!title && !searchForm.autoParsePath) {
    toast.error("关闭路径自动解析时必须填写标题");
    return null;
  }
  const payload: SubtitleSearchPayload = {
    title,
    original_title: searchForm.originalTitle.trim(),
    media_type: searchForm.mediaType,
    video_path: videoPath,
    auto_parse_path: searchForm.autoParsePath,
  };
  if (searchForm.year > 0) payload.year = searchForm.year;
  if (searchForm.season > 0) payload.season = searchForm.season;
  if (searchForm.episode > 0) payload.episode = searchForm.episode;
  if (searchForm.imdbId.trim()) payload.imdb_id = searchForm.imdbId.trim();
  return payload;
}

async function runSearch() {
  const payload = buildSearchPayload();
  if (!payload) return;
  searching.value = true;
  searched.value = true;
  try {
    const resp = await subtitleApi.search(payload);
    searchResults.value = resp.results ?? [];
    providerErrors.value = resp.provider_errors ?? [];
    if (searchResults.value.length === 0 && providerErrors.value.length === 0) {
      toast.info("没有搜索到匹配的字幕");
    }
  } catch (error) {
    toast.error(errMsg(error, "搜索字幕失败"));
    searchResults.value = [];
    providerErrors.value = [];
  } finally {
    searching.value = false;
  }
}

async function downloadCandidate(item: SubtitleScoredCandidate) {
  const videoPath = searchForm.videoPath.trim();
  if (!videoPath) {
    toast.error("下载前需要在搜索条件里填写视频路径");
    return;
  }
  downloadingSlug.value = item.candidate.slug;
  try {
    const resp = await subtitleApi.download({
      provider: item.candidate.provider,
      candidate: item.candidate,
      video_path: videoPath,
      format: item.candidate.format || undefined,
      overwrite: downloadForm.overwrite,
      auto_sync: downloadForm.autoSync,
    });
    const task = resp.task;
    if (task?.error_message) {
      // 下载成功但校正失败也走这里：字幕已经在盘上了。
      toast.info(`字幕已下载：${task.subtitle_path}（${task.error_message}）`);
    } else {
      toast.success(`字幕已下载：${task?.subtitle_path ?? ""}`);
    }
  } catch (error) {
    toast.error(errMsg(error, "下载字幕失败"));
  } finally {
    downloadingSlug.value = "";
  }
}

// ---- 时间轴校正 ----

async function runSync() {
  const videoPath = syncVideoPath.value.trim();
  const subtitlePath = syncFilePath.value.trim();
  if (!videoPath || !subtitlePath) {
    toast.error("请填写视频路径与字幕路径");
    return;
  }
  syncing.value = true;
  try {
    const resp = await subtitleApi.sync({ video_path: videoPath, subtitle_path: subtitlePath });
    const result = resp.result;
    const parts = [
      `偏移 ${result.offset_ms}ms`,
      `缩放 ${result.scale.toFixed(6)}`,
      `置信度 ${result.confidence.toFixed(2)}`,
    ];
    if (result.applied) {
      toast.success(`校正完成：${parts.join("，")}`);
    } else {
      const warn = (result.warnings ?? []).join("；") || "未做调整";
      toast.info(`未写入文件：${warn}（${parts.join("，")}）`);
    }
  } catch (error) {
    toast.error(errMsg(error, "时间轴校正失败"));
  } finally {
    syncing.value = false;
  }
}

// ---- 任务记录 ----

function scoreTone(score: number): "success" | "warning" | "danger" {
  if (score >= 80) return "success";
  if (score >= 60) return "warning";
  return "danger";
}

function statusTone(status: string): "success" | "warning" | "danger" | "muted" {
  switch (status) {
    case "done":
      return "success";
    case "failed":
      return "danger";
    case "skipped":
      return "warning";
    default:
      return "muted";
  }
}

const statusLabels: Record<string, string> = {
  pending: "等待中",
  searching: "搜索中",
  matching: "匹配中",
  downloading: "下载中",
  syncing: "校正中",
  done: "已完成",
  failed: "失败",
  skipped: "已跳过",
};

async function retryTask(task: SubtitleTask) {
  try {
    await subtitleApi.retryTask(task.id);
    toast.success("已重新执行");
    await loadTasks();
  } catch (error) {
    toast.error(errMsg(error, "重试失败"));
  }
}

async function removeTask(task: SubtitleTask) {
  try {
    await subtitleApi.deleteTask(task.id);
    toast.success("已删除记录");
    await loadTasks();
  } catch (error) {
    toast.error(errMsg(error, "删除失败"));
  }
}

const totalPages = computed(() => Math.max(1, Math.ceil(taskTotal.value / taskPageSize)));

function goPage(page: number) {
  if (page < 1 || page > totalPages.value) return;
  taskPage.value = page;
  void loadTasks();
}
</script>

<template>
  <div class="subtitle-page">
    <SectionTabBar :tabs="tabs" :model-value="activeTab" @update:model-value="setActiveTab">
      <template #actions>
        <span v-if="dirtyKeys.length" class="subtitle-dirty">{{ dirtyKeys.length }} 项待保存</span>
        <AppButton v-if="dirtyKeys.length" size="sm" variant="ghost" :disabled="saving" @click="resetConfig">
          放弃修改
        </AppButton>
        <AppButton
          v-if="dirtyKeys.length"
          size="sm"
          variant="primary"
          :disabled="saving"
          @click="saveConfig"
        >
          {{ saving ? "保存中…" : "保存设置" }}
        </AppButton>
      </template>
    </SectionTabBar>

    <AppStateBlock v-if="loading" loading message="正在加载字幕配置…" />
    <AppStateBlock v-else-if="loadError" :message="loadError" />
    <div v-if="!loading && loadError" class="subtitle-reload">
      <AppButton size="sm" @click="loadConfig">重新加载</AppButton>
    </div>

    <template v-else-if="configForm">
      <!-- ============ 搜索下载 ============ -->
      <div v-show="activeTab === SEARCH_TAB" class="subtitle-pane">
        <SettingsCard title="搜索条件">
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">标题</div>
              <div class="subtitle-desc">影片名或剧集名；留空时从视频路径解析。</div>
            </template>
            <template #control>
              <AppInput v-model="searchForm.title" placeholder="例如：流浪地球" />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">原始标题</div>
              <div class="subtitle-desc">外语原名，用于提高匹配命中率。</div>
            </template>
            <template #control>
              <AppInput v-model="searchForm.originalTitle" placeholder="例如：The Wandering Earth" />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">视频路径</div>
              <div class="subtitle-desc">用于解析标题/年份/季集，下载时也作为字幕落盘位置。</div>
            </template>
            <template #control>
              <AppInput v-model="searchForm.videoPath" placeholder="/media/电影/流浪地球.2019.1080p.mkv" />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">类型</div>
            </template>
            <template #control>
              <AppSelect v-model="searchForm.mediaType" :options="mediaTypeOptions" />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">年份 / 季 / 集</div>
              <div class="subtitle-desc">0 表示不限制；剧集建议填季与集。</div>
            </template>
            <template #control>
              <div class="subtitle-inline">
                <AppInput v-model.number="searchForm.year" type="number" placeholder="年份" />
                <AppInput v-model.number="searchForm.season" type="number" placeholder="季" />
                <AppInput v-model.number="searchForm.episode" type="number" placeholder="集" />
              </div>
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">路径自动解析</div>
              <div class="subtitle-desc">从视频文件名提取标题、年份与季集；显式填写的字段优先。</div>
            </template>
            <template #control>
              <SettingsBoolSegment v-model="searchForm.autoParsePath" label="路径自动解析" />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">下载选项</div>
              <div class="subtitle-desc">覆盖已有字幕需要显式勾选；自动校正在下载后立即对齐时间轴。</div>
            </template>
            <template #control>
              <div class="subtitle-inline">
                <SettingsBoolSegment v-model="downloadForm.overwrite" label="覆盖已有字幕" />
                <SettingsBoolSegment v-model="downloadForm.autoSync" label="下载后自动校正" />
              </div>
            </template>
          </SettingsRow>
          <template #head-actions>
            <AppButton variant="primary" :disabled="searching" @click="runSearch">
              <SvgIcon name="search" />
              {{ searching ? "搜索中…" : "搜索字幕" }}
            </AppButton>
          </template>
        </SettingsCard>

        <SettingsCard v-if="providerErrors.length" title="来源异常">
          <SettingsRow v-for="(pe, idx) in providerErrors" :key="`${pe.provider}-${idx}`">
            <template #info>
              <div class="subtitle-label">{{ pe.provider || "字幕源" }}</div>
            </template>
            <template #control>
              <span class="subtitle-error">{{ pe.message }}</span>
            </template>
          </SettingsRow>
        </SettingsCard>

        <SettingsCard v-if="searched" title="搜索结果">
          <AppStateBlock
            v-if="searchResults.length === 0"
            message="没有搜索到匹配的字幕，可放宽年份/季集限制或检查字幕源配置"
          />
          <div v-else class="subtitle-results">
            <div v-for="item in searchResults" :key="`${item.candidate.provider}-${item.candidate.slug}`" class="subtitle-result">
              <div class="subtitle-result__head">
                <div class="subtitle-result__title">
                  <AdminStatusPill :tone="scoreTone(item.score)">{{ `${item.score} 分` }}</AdminStatusPill>
                  <span class="subtitle-result__name">{{ item.candidate.title || item.candidate.file_name }}</span>
                </div>
                <AppButton
                  size="sm"
                  variant="primary"
                  :disabled="downloadingSlug === item.candidate.slug"
                  @click="downloadCandidate(item)"
                >
                  {{ downloadingSlug === item.candidate.slug ? "下载中…" : "下载" }}
                </AppButton>
              </div>
              <div class="subtitle-result__meta">
                <span>{{ item.candidate.provider }}</span>
                <span v-if="item.candidate.language">语言：{{ item.candidate.language }}</span>
                <span v-if="item.candidate.format">格式：{{ item.candidate.format }}</span>
                <span v-if="item.candidate.release_group">组：{{ item.candidate.release_group }}</span>
                <span v-if="item.candidate.download_count">下载：{{ item.candidate.download_count }}</span>
                <span v-if="item.candidate.hash_matched" class="subtitle-hash">hash 命中</span>
              </div>
              <div v-if="item.reasons?.length" class="subtitle-result__reasons">
                <span v-for="(r, i) in item.reasons" :key="`r-${i}`" class="subtitle-tag subtitle-tag--good">{{ r }}</span>
              </div>
              <div v-if="item.penalties?.length" class="subtitle-result__penalties">
                <span v-for="(p, i) in item.penalties" :key="`p-${i}`" class="subtitle-tag subtitle-tag--bad">{{ p }}</span>
              </div>
            </div>
          </div>
        </SettingsCard>

        <SettingsCard title="时间轴校正">
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">校正环境</div>
              <div class="subtitle-desc">校正依赖外部 ffmpeg / ffprobe。</div>
            </template>
            <template #control>
              <div class="subtitle-inline">
                <AdminStatusPill
                  :tone="syncCheck?.ffmpeg.available ? 'success' : 'danger'">{{ syncCheck?.ffmpeg.available ? 'ffmpeg 可用' : 'ffmpeg 缺失' }}</AdminStatusPill>
                <AdminStatusPill
                  :tone="syncCheck?.ffprobe.available ? 'success' : 'warning'">{{ syncCheck?.ffprobe.available ? 'ffprobe 可用' : 'ffprobe 缺失' }}</AdminStatusPill>
              </div>
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">视频路径</div>
              <div class="subtitle-desc">校正以该视频的音轨为基准。</div>
            </template>
            <template #control>
              <AppInput v-model="syncVideoPath" placeholder="/media/电影/流浪地球.2019.1080p.mkv" />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">字幕路径</div>
              <div class="subtitle-desc">待校正的字幕文件；是否改写由「匹配设置」里的 dry-run 决定。</div>
            </template>
            <template #control>
              <AppInput v-model="syncFilePath" placeholder="/media/电影/流浪地球.2019.1080p.srt" />
            </template>
          </SettingsRow>
          <template #head-actions>
            <AppButton variant="primary" :disabled="syncing" @click="runSync">
              {{ syncing ? "校正中…" : "执行校正" }}
            </AppButton>
          </template>
        </SettingsCard>
      </div>

      <!-- ============ 字幕源 ============ -->
      <div v-show="activeTab === SOURCE_TAB" class="subtitle-pane">
        <SettingsCard title="模块开关">
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">启用字幕智能处理</div>
              <div class="subtitle-desc">关闭后所有字幕检索、下载与校正接口都会拒绝执行。</div>
            </template>
            <template #control>
              <SettingsBoolSegment
                :model-value="configForm.enabled"
                label="启用字幕智能处理"
                @update:model-value="(v: boolean) => { if (configForm) configForm.enabled = v; markDirty('enabled', v); }"
              />
            </template>
          </SettingsRow>
        </SettingsCard>

        <SettingsCard title="射手网 (assrt)">
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">启用</div>
              <div class="subtitle-desc">需要 API Token，可在 assrt.net 申请。</div>
            </template>
            <template #control>
              <SettingsBoolSegment
                :model-value="configForm.assrt_enabled"
                label="启用射手网"
                @update:model-value="(v: boolean) => { if (configForm) configForm.assrt_enabled = v; markDirty('assrt_enabled', v); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">API Token</div>
              <div class="subtitle-desc">
                <span v-if="configForm.assrt_api_key_set" class="subtitle-set">已配置（留空则不修改）</span>
                <span v-else>尚未配置</span>
              </div>
            </template>
            <template #control>
              <AppInput v-model="secretInput.assrt" type="password" ignore-autofill placeholder="填入新的 API Token" />
            </template>
          </SettingsRow>
        </SettingsCard>

        <SettingsCard title="SubHD">
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">启用</div>
              <div class="subtitle-desc">无需 Key，但站点可能有反爬，Cookie 可显著提高成功率。</div>
            </template>
            <template #control>
              <SettingsBoolSegment
                :model-value="configForm.subhd_enabled"
                label="启用 SubHD"
                @update:model-value="(v: boolean) => { if (configForm) configForm.subhd_enabled = v; markDirty('subhd_enabled', v); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">Cookie</div>
              <div class="subtitle-desc">
                <span v-if="configForm.subhd_cookie_set" class="subtitle-set">已配置（留空则不修改）</span>
                <span v-else>尚未配置</span>
              </div>
            </template>
            <template #control>
              <AppInput v-model="secretInput.subhd" type="password" ignore-autofill placeholder="浏览器中复制的 Cookie" />
            </template>
          </SettingsRow>
        </SettingsCard>

        <SettingsCard title="字幕库 (zimuku)">
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">启用</div>
              <div class="subtitle-desc">站点域名历史上多次变动，Cookie 失效时请更新。</div>
            </template>
            <template #control>
              <SettingsBoolSegment
                :model-value="configForm.zimuku_enabled"
                label="启用字幕库"
                @update:model-value="(v: boolean) => { if (configForm) configForm.zimuku_enabled = v; markDirty('zimuku_enabled', v); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">Cookie</div>
              <div class="subtitle-desc">
                <span v-if="configForm.zimuku_cookie_set" class="subtitle-set">已配置（留空则不修改）</span>
                <span v-else>尚未配置</span>
              </div>
            </template>
            <template #control>
              <AppInput v-model="secretInput.zimuku" type="password" ignore-autofill placeholder="浏览器中复制的 Cookie" />
            </template>
          </SettingsRow>
        </SettingsCard>

        <SettingsCard title="OpenSubtitles">
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">启用</div>
              <div class="subtitle-desc">需要 API Key；配置账号后可提升下载配额。</div>
            </template>
            <template #control>
              <SettingsBoolSegment
                :model-value="configForm.opensubtitles.enabled"
                label="启用 OpenSubtitles"
                @update:model-value="(v: boolean) => { if (configForm) configForm.opensubtitles.enabled = v; markDirty('opensubtitles_enabled', v); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">API Key</div>
              <div class="subtitle-desc">
                <span v-if="configForm.opensubtitles.api_key_set" class="subtitle-set">已配置（留空则不修改）</span>
                <span v-else>尚未配置</span>
              </div>
            </template>
            <template #control>
              <AppInput v-model="secretInput.opensubsApiKey" type="password" ignore-autofill placeholder="OpenSubtitles API Key" />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">账号 / 密码</div>
              <div class="subtitle-desc">可选；用于领取更高的下载额度。</div>
            </template>
            <template #control>
              <div class="subtitle-inline">
                <AppInput v-model="secretInput.opensubsAccount" placeholder="用户名" autocomplete="off" />
                <AppInput v-model="secretInput.opensubsPassword" type="password" ignore-autofill placeholder="密码（留空不修改）" />
              </div>
            </template>
          </SettingsRow>
        </SettingsCard>

        <SettingsCard title="来源状态">
          <AppStateBlock v-if="providersLoading" loading message="正在检测来源…" />
          <SettingsRow v-for="p in providers" v-else :key="p.name">
            <template #info>
              <div class="subtitle-label">{{ p.display_name }}</div>
              <div class="subtitle-desc">
                <span v-if="!p.enabled">未启用</span>
                <span v-else-if="!p.configured">已启用但缺少必需凭证</span>
                <span v-else>{{ p.message }}<template v-if="p.latency_ms"> · {{ p.latency_ms }}ms</template></span>
              </div>
            </template>
            <template #control>
              <div class="subtitle-inline">
                <AdminStatusPill
                  :tone="!p.enabled ? 'muted' : p.healthy ? 'success' : p.configured ? 'danger' : 'warning'">{{ !p.enabled ? '未启用' : p.healthy ? '正常' : p.configured ? '异常' : '待配置' }}</AdminStatusPill>
                <AppButton
                  size="sm"
                  :disabled="!p.enabled || testingProvider === p.name"
                  @click="testProvider(p.name)"
                >
                  {{ testingProvider === p.name ? "测试中…" : "测试" }}
                </AppButton>
              </div>
            </template>
          </SettingsRow>
        </SettingsCard>
      </div>

      <!-- ============ 匹配设置 ============ -->
      <div v-show="activeTab === MATCH_TAB" class="subtitle-pane">
        <SettingsCard title="语言与格式偏好">
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">语言优先级</div>
              <div class="subtitle-desc">逗号分隔，靠前优先。可选：zh-cn、zh-tw、zh、en、ja、ko。</div>
            </template>
            <template #control>
              <AppInput
                :model-value="configForm.language_priority"
                placeholder="zh-cn,zh-tw,zh,en"
                @update:model-value="(v: string) => { if (configForm) configForm.language_priority = v; markDirty('language_priority', v); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">格式优先级</div>
              <div class="subtitle-desc">逗号分隔。注意：sup 为图形字幕，无法做时间轴校正。</div>
            </template>
            <template #control>
              <AppInput
                :model-value="configForm.format_priority"
                placeholder="ass,srt,ssa,sub,sup"
                @update:model-value="(v: string) => { if (configForm) configForm.format_priority = v; markDirty('format_priority', v); }"
              />
            </template>
          </SettingsRow>
        </SettingsCard>

        <SettingsCard title="自动处理">
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">整理时自动匹配</div>
              <div class="subtitle-desc">媒体整理流程中为缺失字幕的视频自动检索并打分；已有同名字幕的视频直接跳过。</div>
            </template>
            <template #control>
              <SettingsBoolSegment
                :model-value="configForm.auto_match"
                label="整理时自动匹配"
                @update:model-value="(v: boolean) => { if (configForm) configForm.auto_match = v; markDirty('auto_match', v); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">自动下载</div>
              <div class="subtitle-desc">整理流程中命中候选后直接下载，无需人工确认。</div>
            </template>
            <template #control>
              <SettingsBoolSegment
                :model-value="configForm.auto_download"
                label="自动下载"
                @update:model-value="(v: boolean) => { if (configForm) configForm.auto_download = v; markDirty('auto_download', v); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">最低匹配分</div>
              <div class="subtitle-desc">0-100，低于该分数的候选不会被自动下载。</div>
            </template>
            <template #control>
              <AppInput
                :model-value="configForm.min_match_score"
                type="number"
                @update:model-value="(v: string) => { if (configForm) configForm.min_match_score = Number(v) || 0; markDirty('min_match_score', Number(v) || 0); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">目标目录策略</div>
            </template>
            <template #control>
              <AppSelect
                :model-value="configForm.target_dir_policy"
                :options="dirPolicyOptions"
                @update:model-value="(v: string | number | boolean) => { if (configForm) configForm.target_dir_policy = String(v); markDirty('target_dir_policy', String(v)); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">覆盖已有字幕</div>
              <div class="subtitle-desc">关闭时目标文件已存在会拒绝下载。</div>
            </template>
            <template #control>
              <SettingsBoolSegment
                :model-value="configForm.overwrite"
                label="覆盖已有字幕"
                @update:model-value="(v: boolean) => { if (configForm) configForm.overwrite = v; markDirty('overwrite', v); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">并发数 / 超时</div>
              <div class="subtitle-desc">并发数 0-16；超时单位秒，主要用于上游站点响应。</div>
            </template>
            <template #control>
              <div class="subtitle-inline">
                <AppInput
                  :model-value="configForm.concurrency"
                  type="number"
                  placeholder="并发数"
                  @update:model-value="(v: string) => { if (configForm) configForm.concurrency = Number(v) || 0; markDirty('concurrency', Number(v) || 0); }"
                />
                <AppInput
                  :model-value="configForm.timeout_seconds"
                  type="number"
                  placeholder="超时(秒)"
                  @update:model-value="(v: string) => { if (configForm) configForm.timeout_seconds = Number(v) || 0; markDirty('timeout_seconds', Number(v) || 0); }"
                />
              </div>
            </template>
          </SettingsRow>
        </SettingsCard>

        <SettingsCard title="时间轴校正">
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">下载后自动校正</div>
              <div class="subtitle-desc">下载字幕后立即用音轨对齐时间轴。</div>
            </template>
            <template #control>
              <SettingsBoolSegment
                :model-value="configForm.auto_sync"
                label="下载后自动校正"
                @update:model-value="(v: boolean) => { if (configForm) configForm.auto_sync = v; markDirty('auto_sync', v); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">校正方式</div>
              <div class="subtitle-desc">语音对齐最稳；整体平移适合固定延迟；按帧率缩放适合 PAL/NTSC 差异。</div>
            </template>
            <template #control>
              <AppSelect
                :model-value="configForm.sync_mode"
                :options="syncModeOptions"
                @update:model-value="(v: string | number | boolean) => { if (configForm) configForm.sync_mode = String(v); markDirty('sync_mode', String(v)); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">置信度阈值</div>
              <div class="subtitle-desc">0-1，低于该值只报告不写文件。</div>
            </template>
            <template #control>
              <AppInput
                :model-value="configForm.sync_min_confidence"
                type="number"
                @update:model-value="(v: string) => { if (configForm) configForm.sync_min_confidence = Number(v) || 0; markDirty('sync_min_confidence', Number(v) || 0); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">演练模式（dry-run）</div>
              <div class="subtitle-desc">开启时只计算偏移并回报，不修改字幕文件。建议先观察一段时间再关闭。</div>
            </template>
            <template #control>
              <SettingsBoolSegment
                :model-value="configForm.sync_dry_run"
                label="演练模式"
                @update:model-value="(v: boolean) => { if (configForm) configForm.sync_dry_run = v; markDirty('sync_dry_run', v); }"
              />
            </template>
          </SettingsRow>
          <SettingsRow>
            <template #info>
              <div class="subtitle-label">保留原字幕</div>
              <div class="subtitle-desc">改写前把原文件另存为 .bak。</div>
            </template>
            <template #control>
              <SettingsBoolSegment
                :model-value="configForm.keep_original"
                label="保留原字幕"
                @update:model-value="(v: boolean) => { if (configForm) configForm.keep_original = v; markDirty('keep_original', v); }"
              />
            </template>
          </SettingsRow>
        </SettingsCard>
      </div>

      <!-- ============ 任务记录 ============ -->
      <div v-show="activeTab === TASK_TAB" class="subtitle-pane">
        <SettingsCard title="任务记录">
          <template #head-actions>
            <AppButton size="sm" :disabled="tasksLoading" @click="loadTasks">
              {{ tasksLoading ? "加载中…" : "刷新" }}
            </AppButton>
          </template>
          <AppStateBlock
            v-if="tasks.length === 0"
            :message="tasksMessage || '暂无字幕任务记录'"
          />
          <div v-else class="subtitle-table">
            <table>
              <thead>
                <tr>
                  <th>标题 / 视频</th>
                  <th>状态</th>
                  <th>来源</th>
                  <th>匹配分</th>
                  <th>校正</th>
                  <th>时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="task in tasks" :key="task.id">
                  <td>
                    <div class="subtitle-cell-title">{{ task.title || "—" }}</div>
                    <div class="subtitle-cell-path">{{ task.video_path }}</div>
                  </td>
                  <td>
                    <AdminStatusPill
                      :tone="statusTone(task.status)">{{ statusLabels[task.status] ?? task.status }}</AdminStatusPill>
                    <div v-if="task.error_message" class="subtitle-cell-path">{{ task.error_message }}</div>
                  </td>
                  <td>{{ task.provider || "—" }}</td>
                  <td>{{ task.match_score || "—" }}</td>
                  <td>
                    <template v-if="task.sync_offset_ms || task.sync_confidence">
                      {{ task.sync_offset_ms }}ms / {{ task.sync_confidence.toFixed(2) }}
                      <span v-if="task.sync_applied" class="subtitle-applied">已应用</span>
                    </template>
                    <template v-else>—</template>
                  </td>
                  <td>{{ formatTime(task.created_at) }}</td>
                  <td>
                    <div class="subtitle-inline">
                      <AppButton size="sm" @click="retryTask(task)">重试</AppButton>
                      <AppButton size="sm" variant="danger" @click="removeTask(task)">删除</AppButton>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
            <div v-if="totalPages > 1" class="subtitle-pager">
              <AppButton size="sm" :disabled="taskPage <= 1" @click="goPage(taskPage - 1)">上一页</AppButton>
              <span>第 {{ taskPage }} / {{ totalPages }} 页（共 {{ taskTotal }} 条）</span>
              <AppButton size="sm" :disabled="taskPage >= totalPages" @click="goPage(taskPage + 1)">下一页</AppButton>
            </div>
          </div>
        </SettingsCard>
      </div>
    </template>
  </div>
</template>

<style scoped>
.subtitle-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.subtitle-pane {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

/* AppStateBlock 本身不带操作按钮，重试入口单独排一行。 */
.subtitle-reload {
  display: flex;
  justify-content: center;
  padding-bottom: 8px;
}

.subtitle-label {
  font-weight: 600;
  color: var(--text-primary, #e5e7eb);
}

.subtitle-desc {
  font-size: 12px;
  color: var(--text-secondary, #9ca3af);
  margin-top: 2px;
  line-height: 1.5;
}

.subtitle-set {
  color: var(--success, #22c55e);
}

.subtitle-dirty {
  font-size: 12px;
  color: var(--warning, #f59e0b);
  margin-right: 8px;
}

.subtitle-inline {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}

.subtitle-error {
  font-size: 12px;
  color: var(--danger, #ef4444);
}

.subtitle-results {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.subtitle-result {
  border: 1px solid var(--border, #374151);
  border-radius: 8px;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.subtitle-result__head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
}

.subtitle-result__title {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.subtitle-result__name {
  font-weight: 600;
  word-break: break-all;
}

.subtitle-result__meta {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--text-secondary, #9ca3af);
}

.subtitle-hash {
  color: var(--success, #22c55e);
  font-weight: 600;
}

.subtitle-result__reasons,
.subtitle-result__penalties {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.subtitle-tag {
  font-size: 11px;
  padding: 2px 6px;
  border-radius: 4px;
}

.subtitle-tag--good {
  background: rgba(34, 197, 94, 0.15);
  color: var(--success, #22c55e);
}

.subtitle-tag--bad {
  background: rgba(239, 68, 68, 0.15);
  color: var(--danger, #ef4444);
}

.subtitle-table {
  overflow-x: auto;
}

.subtitle-table table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.subtitle-table th,
.subtitle-table td {
  text-align: left;
  padding: 8px 10px;
  border-bottom: 1px solid var(--border, #374151);
  vertical-align: top;
}

.subtitle-cell-title {
  font-weight: 600;
}

.subtitle-cell-path {
  font-size: 11px;
  color: var(--text-secondary, #9ca3af);
  word-break: break-all;
  max-width: 420px;
}

.subtitle-applied {
  color: var(--success, #22c55e);
  font-size: 11px;
}

.subtitle-pager {
  display: flex;
  align-items: center;
  gap: 12px;
  justify-content: center;
  padding-top: 12px;
  font-size: 12px;
}
</style>
