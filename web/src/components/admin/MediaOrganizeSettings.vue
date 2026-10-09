<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import {
  fetchMediaOrganizeSettings,
  saveMediaOrganizeSettings,
  testMediaOrganizeTmdb,
  type MediaOrganizeSettings,
} from "@/api/mediaOrganize";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import SettingsBoolSegment from "@/components/admin/SettingsBoolSegment.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import SettingsHelpTooltip from "@/components/admin/SettingsHelpTooltip.vue";
import SettingsRow from "@/components/admin/SettingsRow.vue";
import TmdbHostsHelpTip from "@/components/admin/TmdbHostsHelpTip.vue";
import { useSettingsForm, bindSettingsPanelExpose, useSettingsSave } from "@/composables/useSettingsForm";
import { useSettingsLoad } from "@/composables/useSettingsLoad";
import { useConfirm } from "@/composables/useConfirm";
import { runTmdbTest } from "@/composables/useTmdbTest";
import "@/styles/admin-shared.css";

const ORGANIZE_SETTINGS_ACCENT = "#10b981";
const { showConfirm } = useConfirm();
const ALL_TAG_KEYS = ["screen_size", "frame_rate", "video_codec", "audio_codec", "audio_channels"] as const;
const TAG_LABELS: Record<string, string> = {
  screen_size: "分辨率",
  frame_rate: "帧率",
  video_codec: "视频编码",
  audio_codec: "音频编码",
  audio_channels: "声道数",
};

const tmdbLanguageOptions = [
  { value: "zh-CN", label: "简体中文" },
  { value: "zh-TW", label: "繁体中文" },
  { value: "en-US", label: "English" },
];

const scrapeTargetOptions = [
  { value: "local", label: "本地目录" },
  { value: "cloud", label: "网盘（与媒体文件同目录）" },
];

const skipActionOptions = [
  { value: "keep", label: "留在源目录" },
  { value: "move", label: "移到兜底目录" },
];

const backupTargetOptions = [
  { value: "local", label: "本地目录" },
  { value: "cloud", label: "网盘" },
];

const conflictPolicyOptions = [
  { value: "skip", label: "跳过（推荐）" },
  { value: "overwrite", label: "覆盖" },
];

const { loading, loaded, runLoad } = useSettingsLoad();
const { saving, runSave } = useSettingsSave();
const tmdbTesting = ref(false);
const draggingTagIndex = ref<number | null>(null);
const insertIndex = ref<number | null>(null);
const tagEditorRef = ref<HTMLElement | null>(null);

const tagGhost = reactive({
  visible: false,
  x: 0,
  y: 0,
  width: 0,
  height: 0,
  label: "",
});

let pendingTagDrag: {
  index: number;
  startX: number;
  startY: number;
  offsetX: number;
  offsetY: number;
  width: number;
  height: number;
} | null = null;

const tagGhostStyle = computed(() => ({
  left: `${tagGhost.x}px`,
  top: `${tagGhost.y}px`,
  width: `${tagGhost.width}px`,
  height: `${tagGhost.height}px`,
}));

const {
  settings,
  isDirty: settingsChanged,
  isFieldChanged,
  snapshotBaseline,
  revert: revertToBaseline,
} = useSettingsForm<MediaOrganizeSettings>({
  proxy_enabled: false,
  proxy_url: "",
  proxy_username: "",
  proxy_password: "",
  tmdb_api_key: "",
  tmdb_language: "zh-CN",
  tmdb_api_host: "https://api.themoviedb.org",
  tmdb_image_host: "https://image.tmdb.org",
  tmdb_proxy_url: "",
  api_request_interval_ms: 300,
  tmdb_request_interval_ms: 250,
  file_extensions: "",
  metadata_extensions: "",
  media_tag_order: "",
  align_media_tags: false,
  max_works_per_run: 50,
  overwrite_existing: false,
  scrape_nfo_enabled: false,
  scrape_nfo_target: "local",
  scrape_unrecognized_dir: "未识别",
  scrape_follow_existing_location: false,
  scrape_skip_action: "keep",
  backup_target: "local",
  // T15：风控熔断与小文件隔离的默认值必须和 internal/settings/registry.go
  // 里的 spec 默认值一字不差。前后端各写一份默认值不是问题，两份写得不一样才是。
  scrape_max_calls_per_window: 0,
  scrape_call_window_seconds: 3600,
  scrape_call_pause_seconds: 3600,
  scrape_max_work_minutes: 0,
  scrape_work_pause_minutes: 60,
  min_media_size_bytes: 0,
  quarantine_dir: "",
  small_file_acked: false,
});
const tagOrder = reactive<string[]>([...ALL_TAG_KEYS]);

const disabledTags = computed(() => ALL_TAG_KEYS.filter((k) => !tagOrder.includes(k)));

const conflictPolicy = computed({
  get: () => (settings.overwrite_existing ? "overwrite" : "skip"),
  set: (val: string) => {
    settings.overwrite_existing = val === "overwrite";
  },
});

function parseMediaTagOrder(raw: MediaOrganizeSettings["media_tag_order"]): string[] | null {
  if (Array.isArray(raw)) return raw.filter((k) => ALL_TAG_KEYS.includes(k as (typeof ALL_TAG_KEYS)[number]));
  if (typeof raw === "string" && raw.trim()) {
    try {
      let parsed: unknown = JSON.parse(raw);
      if (typeof parsed === "string") parsed = JSON.parse(parsed);
      if (Array.isArray(parsed)) {
        return parsed.filter((k) => ALL_TAG_KEYS.includes(k as (typeof ALL_TAG_KEYS)[number]));
      }
    } catch {
      return null;
    }
  }
  return null;
}

function syncTagsFromSettings() {
  const order = parseMediaTagOrder(settings.media_tag_order);
  tagOrder.splice(0, tagOrder.length, ...(order ?? [...ALL_TAG_KEYS]));
}

function flushTagOrderToSettings() {
  settings.media_tag_order = JSON.stringify([...tagOrder]);
}

function removeTag(key: string) {
  const idx = tagOrder.indexOf(key);
  if (idx >= 0) tagOrder.splice(idx, 1);
  flushTagOrderToSettings();
}

function addTag(key: string) {
  if (!tagOrder.includes(key)) tagOrder.push(key);
  flushTagOrderToSettings();
}

function startTagPointerDrag(index: number, e: PointerEvent) {
  if (e.button !== 0 || draggingTagIndex.value !== null) return;
  if ((e.target as HTMLElement | null)?.closest(".tag-chip__remove")) return;
  const chip = e.currentTarget as HTMLElement;
  const rect = chip.getBoundingClientRect();
  pendingTagDrag = {
    index,
    startX: e.clientX,
    startY: e.clientY,
    offsetX: e.clientX - rect.left,
    offsetY: e.clientY - rect.top,
    width: rect.width,
    height: rect.height,
  };
  document.addEventListener("pointermove", handleTagPointerMove);
  document.addEventListener("pointerup", finishTagPointerDrag);
  document.addEventListener("pointercancel", cancelTagPointerDrag);
}

function beginTagDrag(e: PointerEvent) {
  if (!pendingTagDrag) return;
  const index = pendingTagDrag.index;
  draggingTagIndex.value = index;
  insertIndex.value = index;
  tagGhost.visible = true;
  tagGhost.label = TAG_LABELS[tagOrder[index]] ?? tagOrder[index];
  tagGhost.width = pendingTagDrag.width;
  tagGhost.height = pendingTagDrag.height;
  tagGhost.x = e.clientX - pendingTagDrag.offsetX;
  tagGhost.y = e.clientY - pendingTagDrag.offsetY;
  document.body.classList.add("mo-tag-dragging");
}

function handleTagPointerMove(e: PointerEvent) {
  if (!pendingTagDrag) return;
  const dx = Math.abs(e.clientX - pendingTagDrag.startX);
  const dy = Math.abs(e.clientY - pendingTagDrag.startY);
  if (draggingTagIndex.value === null) {
    if (dx < 4 && dy < 4) return;
    beginTagDrag(e);
  }
  e.preventDefault();
  tagGhost.x = e.clientX - pendingTagDrag.offsetX;
  tagGhost.y = e.clientY - pendingTagDrag.offsetY;
  updateInsertIndex(e.clientX, e.clientY);
}

function updateInsertIndex(clientX: number, clientY: number) {
  const root = tagEditorRef.value;
  if (!root || draggingTagIndex.value === null) return;

  const chips = Array.from(root.querySelectorAll<HTMLElement>(".tag-chip[data-tag-index]"));
  if (!chips.length) {
    insertIndex.value = 0;
    return;
  }

  const rowChips = chips.filter((el) => {
    const rect = el.getBoundingClientRect();
    return clientY >= rect.top - 12 && clientY <= rect.bottom + 12;
  });
  const targets = (rowChips.length ? rowChips : chips).sort(
    (a, b) => a.getBoundingClientRect().left - b.getBoundingClientRect().left,
  );

  for (const el of targets) {
    const rect = el.getBoundingClientRect();
    const tagIndex = Number(el.dataset.tagIndex);
    if (Number.isNaN(tagIndex)) continue;
    if (clientX < rect.left + rect.width / 2) {
      insertIndex.value = tagIndex;
      return;
    }
  }

  const lastIndex = Number(targets[targets.length - 1].dataset.tagIndex);
  insertIndex.value = Number.isNaN(lastIndex) ? tagOrder.length : lastIndex + 1;
}

function applyTagMove(from: number, insert: number) {
  if (insert === from || insert === from + 1) return;
  const item = tagOrder.splice(from, 1)[0];
  const target = from < insert ? insert - 1 : insert;
  tagOrder.splice(target, 0, item);
  flushTagOrderToSettings();
}

function finishTagPointerDrag() {
  if (draggingTagIndex.value !== null && insertIndex.value !== null) {
    applyTagMove(draggingTagIndex.value, insertIndex.value);
  }
  endTagDrag();
  cleanupTagPointerListeners();
}

function cancelTagPointerDrag() {
  endTagDrag();
  cleanupTagPointerListeners();
}

function cleanupTagPointerListeners() {
  pendingTagDrag = null;
  document.removeEventListener("pointermove", handleTagPointerMove);
  document.removeEventListener("pointerup", finishTagPointerDrag);
  document.removeEventListener("pointercancel", cancelTagPointerDrag);
}

function endTagDrag() {
  draggingTagIndex.value = null;
  insertIndex.value = null;
  tagGhost.visible = false;
  document.body.classList.remove("mo-tag-dragging");
}

onBeforeUnmount(() => {
  cleanupTagPointerListeners();
  endTagDrag();
});

async function loadSettings(options?: { silent?: boolean }) {
  await runLoad(async () => {
    const data = await fetchMediaOrganizeSettings();
    Object.assign(settings, data);
    if (settings.max_works_per_run == null) settings.max_works_per_run = 50;
    syncTagsFromSettings();
    snapshotBaseline();
  }, "加载整理设置失败", options);
}

// 把「小文件会被移走」这件事在保存这一刻再说一次。
//
// 为什么放在保存时而不是靠那个勾选框就够了：勾选框和阈值是同一次保存里
// 一起改的，用户很容易顺手把两个都打勾而没细看阈值是多少。这里问的是
// 一个不同的问题——「你知不知道下一次整理就会开始搬文件」，和勾选框
// 表达的「我承认有这回事」不是一回事。
async function confirmQuarantineIfTurningOn() {
  const prevMin = Number(snapshotBaseline.value.min_media_size_bytes ?? 0) || 0;
  const nextMin = Number(settings.min_media_size_bytes ?? 0) || 0;
  if (nextMin <= 0 || prevMin > 0) return true;
  const dir = settings.quarantine_dir.trim() || "每个整理根目录下的 _隔离 子目录";
  try {
    await showConfirm({
      title: "开启小文件隔离",
      message: [
        `从下一次整理开始，move 模式下小于 ${nextMin} 字节的文件会被移入：`,
        dir,
        "",
        "这些文件不会被删除，但会从原来的目录里消失。",
        "如果某个目录里只剩这些小文件，那个目录会被判定为空并进入清理流程。",
      ].join("\n"),
      hint: "这是不可逆的位置变化：文件内容不会丢，但 strm 指针、刮削工具不会自动把它们搬回去。",
      icon: "trash",
      confirmText: "我确认，开启隔离",
      danger: true,
      checkboxLabel: "我已经检查过上面的隔离目录路径，确认无误",
    });
  } catch {
    return false;
  }
  return true;
}

async function saveSettings() {
  if (!settingsChanged.value) return;
  if (!(await confirmQuarantineIfTurningOn())) return;
  await runSave(async () => {
    flushTagOrderToSettings();
    const data = await saveMediaOrganizeSettings({ ...settings });
    Object.assign(settings, data);
    syncTagsFromSettings();
    snapshotBaseline();
  }, { successMessage: "整理设置已保存", errorMessage: "保存整理设置失败" });
}

async function testTmdb() {
  tmdbTesting.value = true;
  try {
    await runTmdbTest(() => testMediaOrganizeTmdb({
      tmdb_api_key: settings.tmdb_api_key,
      tmdb_language: settings.tmdb_language,
      tmdb_api_host: settings.tmdb_api_host,
      tmdb_image_host: settings.tmdb_image_host,
      proxy_enabled: settings.proxy_enabled,
      proxy_url: settings.proxy_url,
      proxy_username: settings.proxy_username,
      proxy_password: settings.proxy_password,
    }));
  } finally {
    tmdbTesting.value = false;
  }
}

onMounted(() => {
  void loadSettings();
});

function revertPanelSettings() {
  revertToBaseline();
  syncTagsFromSettings();
}

defineExpose(
  bindSettingsPanelExpose({
    isDirty: settingsChanged,
    saving,
    save: saveSettings,
    reload: () => loadSettings({ silent: loaded.value }),
    revert: revertPanelSettings,
  }),
);
</script>

<template>
  <div class="mo-settings">
    <div v-if="loading" class="settings-card__loading">加载中…</div>

    <template v-else>
      <SettingsCard title="代理设置" :accent="ORGANIZE_SETTINGS_ACCENT">
        <template #head-aside>
          <TmdbHostsHelpTip />
        </template>
        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('proxy_enabled')">
          <template #info>
            <div class="settings-row__label"><span>启用代理</span></div>
          </template>
          <template #control>
            <SettingsBoolSegment v-model="settings.proxy_enabled" label="启用代理" />
          </template>
        </SettingsRow>

        <template v-if="settings.proxy_enabled">
          <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('proxy_url')">
            <template #info>
              <div class="settings-row__label"><span>代理地址</span></div>
            </template>
            <template #control>
              <AppInput v-model="settings.proxy_url" placeholder="http://127.0.0.1:1080 或 socks5://127.0.0.1:1080" />
            </template>
          </SettingsRow>

          <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('proxy_username')">
            <template #info>
              <div class="settings-row__label"><span>代理用户名</span></div>
            </template>
            <template #control>
              <AppInput
                v-model="settings.proxy_username"
                autocomplete="off"
                placeholder="可选"
              />
            </template>
          </SettingsRow>

          <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('proxy_password')">
            <template #info>
              <div class="settings-row__label"><span>代理密码</span></div>
            </template>
            <template #control>
              <AppInput v-model="settings.proxy_password" type="password" autocomplete="new-password" placeholder="可选" />
            </template>
          </SettingsRow>
        </template>
      </SettingsCard>

      <SettingsCard title="TMDB 设置" :accent="ORGANIZE_SETTINGS_ACCENT">
        <template #head-actions>
          <AppButton type="button" variant="secondary" size="sm" :disabled="tmdbTesting" @click="testTmdb">
            {{ tmdbTesting ? "测试中…" : "测试连通性" }}
          </AppButton>
        </template>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('tmdb_api_key')">
          <template #info>
            <div class="settings-row__label"><span>TMDB API Key</span></div>
          </template>
          <template #control>
            <AppInput
              v-model="settings.tmdb_api_key"
              type="password"
              placeholder="请填写 TMDB API Key（必填）"
              :ignore-autofill="true"
            />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('tmdb_language')">
          <template #info>
            <div class="settings-row__label"><span>TMDB 语言（影响搜索和命名）</span></div>
          </template>
          <template #control>
            <AppSelect v-model="settings.tmdb_language" :options="tmdbLanguageOptions" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('tmdb_api_host')">
          <template #info>
            <div class="settings-row__label">
              <span>TMDB API 主域名</span>
              <SettingsHelpTooltip title="TMDB API 主域名说明">
                <p>自建反代时填写主域名，程序自动补 /3；默认使用官方地址。</p>
                <p>国内网络可尝试填写 https://api.tmdb.org（与官方域名解析到不同节点，部分地区可直连，效果因网络环境而异）。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput v-model="settings.tmdb_api_host" placeholder="https://api.themoviedb.org" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('tmdb_image_host')">
          <template #info>
            <div class="settings-row__label">
              <span>TMDB 图片主域名</span>
              <SettingsHelpTooltip title="TMDB 图片主域名说明">
                <p>自建反代时填写主域名，程序自动补 /t/p；默认使用官方地址。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput v-model="settings.tmdb_image_host" placeholder="https://image.tmdb.org" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('tmdb_proxy_url')">
          <template #info>
            <div class="settings-row__label">
              <span>TMDB HTTP 代理</span>
              <SettingsHelpTooltip title="TMDB HTTP 代理说明">
                <p>仅用于 TMDB API 出站请求（影视发现与媒体整理），不影响其它功能。</p>
                <p>格式：http://host:port 或 socks5://host:port；留空表示直连。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput v-model="settings.tmdb_proxy_url" placeholder="http://host:port 或 socks5://host:port，留空=直连" />
          </template>
        </SettingsRow>
      </SettingsCard>

      <SettingsCard title="API 请求节流" :accent="ORGANIZE_SETTINGS_ACCENT">
        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('api_request_interval_ms')">
          <template #info>
            <div class="settings-row__label"><span>API 额外补偿间隔（毫秒）</span></div>
          </template>
          <template #control>
            <AppInput v-model="settings.api_request_interval_ms" type="number" min="100" max="10000" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('tmdb_request_interval_ms')">
          <template #info>
            <div class="settings-row__label"><span>TMDB 请求间隔（毫秒）</span></div>
          </template>
          <template #control>
            <AppInput v-model="settings.tmdb_request_interval_ms" type="number" min="100" max="5000" />
          </template>
        </SettingsRow>
      </SettingsCard>

      <SettingsCard title="文件识别与整理规则" :accent="ORGANIZE_SETTINGS_ACCENT">
        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('file_extensions')">
          <template #info>
            <div class="settings-row__label"><span>媒体文件后缀（分号分隔）</span></div>
          </template>
          <template #control>
            <AppInput v-model="settings.file_extensions" placeholder="mkv;mp4;avi;ts;mov…" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('metadata_extensions')">
          <template #info>
            <div class="settings-row__label"><span>元数据文件后缀（分号分隔）</span></div>
          </template>
          <template #control>
            <AppInput v-model="settings.metadata_extensions" placeholder="nfo;ass;srt;sub…" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('max_works_per_run')">
          <template #info>
            <div class="settings-row__label">
              <span>每次最多整理作品数</span>
              <SettingsHelpTooltip title="分批整理说明">
                <p>每次生成计划最多包含这么多部作品（一部电影或一部剧集算 1 部），达到上限后停止扫描。</p>
                <p>已整理过的（带 tmdb 标识）不计入此数。</p>
                <p>执行完后再次生成计划即可处理剩余作品。0 表示不限制（不推荐用于大库）。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput v-model="settings.max_works_per_run" type="number" min="0" max="10000" placeholder="50" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('overwrite_existing')">
          <template #info>
            <div class="settings-row__label">
              <span>同名冲突处理</span>
              <SettingsHelpTooltip title="同名冲突处理说明">
                <p>执行整理时，若目标目录已存在同名文件：</p>
                <p><b>跳过</b>：保留目标已有文件，跳过该项（推荐，更安全）</p>
                <p><b>覆盖</b>：先删除目标已有同名文件，再写入新文件</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppSelect v-model="conflictPolicy" :options="conflictPolicyOptions" />
          </template>
        </SettingsRow>
      </SettingsCard>

      <SettingsCard title="媒体信息标签排序" :accent="ORGANIZE_SETTINGS_ACCENT">
        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('align_media_tags')">
          <template #info>
            <div class="settings-row__label">
              <span>强迫症模式</span>
              <SettingsHelpTooltip title="强迫症模式说明">
                <p>开启后，同一后缀文件将保持媒体信息一致。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <SettingsBoolSegment v-model="settings.align_media_tags" label="强迫症模式" />
          </template>
        </SettingsRow>

        <div class="mo-tag-row">
          <SettingsRow
            :show-changed-badge="true"
            :changed="isFieldChanged('media_tag_order')"
          >
            <template #info>
              <div class="settings-row__label">
                <span>媒体信息顺序</span>
                <SettingsHelpTooltip title="媒体信息顺序说明">
                  <p>拖拽标签调整顺序，点击 × 移除。文件名按此顺序生成媒体信息。</p>
                </SettingsHelpTooltip>
              </div>
            </template>
            <template #control>
              <div class="mo-tag-editor">
                <div
                  ref="tagEditorRef"
                  class="mo-tag-editor__active"
                  :class="{ 'mo-tag-editor__active--dragging': draggingTagIndex !== null }"
                >
                  <template v-if="draggingTagIndex === null">
                    <span
                      v-for="(key, index) in tagOrder"
                      :key="key"
                      class="tag-chip"
                      :data-tag-index="index"
                      @pointerdown="startTagPointerDrag(index, $event)"
                    >
                      <span class="tag-chip__text">{{ TAG_LABELS[key] }}</span>
                      <span class="tag-chip__remove" @click.stop="removeTag(key)">×</span>
                    </span>
                    <span v-if="tagOrder.length === 0" class="mo-tag-editor__placeholder">点击下方标签添加</span>
                  </template>
                  <template v-else>
                    <template v-for="slot in tagOrder.length + 1" :key="`tag-slot-${slot - 1}`">
                      <span
                        v-if="insertIndex === slot - 1"
                        class="tag-insert-preview"
                        :data-insert-index="slot - 1"
                      />
                      <span
                        v-if="slot - 1 < tagOrder.length && slot - 1 !== draggingTagIndex"
                        :key="tagOrder[slot - 1]"
                        class="tag-chip"
                        :data-tag-index="slot - 1"
                      >
                        <span class="tag-chip__text">{{ TAG_LABELS[tagOrder[slot - 1]] }}</span>
                        <span class="tag-chip__remove" @click.stop="removeTag(tagOrder[slot - 1])">×</span>
                      </span>
                    </template>
                  </template>
                </div>
                <Teleport to="body">
                  <span
                    v-if="tagGhost.visible"
                    class="tag-chip tag-chip--ghost"
                    :style="tagGhostStyle"
                  >
                    <span class="tag-chip__text">{{ tagGhost.label }}</span>
                    <span class="tag-chip__remove tag-chip__remove--ghost" aria-hidden="true">×</span>
                  </span>
                </Teleport>
                <div v-if="disabledTags.length" class="mo-tag-editor__pool">
                  <span
                    v-for="key in disabledTags"
                    :key="key"
                    class="tag-chip tag-chip--add"
                    @click="addTag(key)"
                  >
                    <span class="tag-chip__addon">+</span>
                    <span class="tag-chip__text">{{ TAG_LABELS[key] }}</span>
                  </span>
                </div>
              </div>
            </template>
          </SettingsRow>
        </div>
      </SettingsCard>

      <SettingsCard title="刮削元数据落盘" :accent="ORGANIZE_SETTINGS_ACCENT">
        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('scrape_nfo_enabled')">
          <template #info>
            <div class="settings-row__label">
              <span>刮削落盘</span>
              <SettingsHelpTooltip title="刮削落盘说明">
                <p>整理完成后自动生成 NFO 与海报。格式取社区通用写法，Emby / Jellyfin 都能直接识别。</p>
                <p>图片下载失败不会让整理失败，只会在任务日志里记一条警告。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <SettingsBoolSegment v-model="settings.scrape_nfo_enabled" label="刮削落盘" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('scrape_nfo_target')">
          <template #info>
            <div class="settings-row__label"><span>落盘目标</span></div>
          </template>
          <template #control>
            <AppSelect v-model="settings.scrape_nfo_target" :options="scrapeTargetOptions" />
          </template>
        </SettingsRow>
      </SettingsCard>

      <SettingsCard title="未识别兜底" :accent="ORGANIZE_SETTINGS_ACCENT">
        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('scrape_skip_action')">
          <template #info>
            <div class="settings-row__label">
              <span>未识别文件动作</span>
              <SettingsHelpTooltip title="未识别文件动作说明">
                <p>识别不出标题的文件，默认留在源目录只记一条「媒体识别失败」。</p>
                <p>选「移到兜底目录」后才会有一个确定去处。注意：动作是「原地重命名」的任务里兜底不生效——原地重命名的约定就是只改名不动位置。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppSelect v-model="settings.scrape_skip_action" :options="skipActionOptions" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('scrape_unrecognized_dir')">
          <template #info>
            <div class="settings-row__label">
              <span>兜底目录</span>
              <SettingsHelpTooltip title="兜底目录的三种填法">
                <p><b>/ 开头</b>：绝对路径，直接落那个目录。</p>
                <p><b>纯目录名</b>：落在「目标根目录 / 媒体类型 / 该目录名」下。</p>
                <p><b>留空</b>：仍留在源目录，等于只记一条识别失败。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput
              v-model="settings.scrape_unrecognized_dir"
              placeholder="未识别"
            />
          </template>
        </SettingsRow>

        <SettingsRow
          :show-changed-badge="true"
          :changed="isFieldChanged('scrape_follow_existing_location')"
        >
          <template #info>
            <div class="settings-row__label">
              <span>沿用媒体库已有位置</span>
              <SettingsHelpTooltip title="沿用已有位置说明">
                <p>开启后先向 Emby 反查这个作品已经在库里的位置，查到就落那里。</p>
                <p>只支持 Emby，且需要 Emby 在线；查不到就安静落回兜底目录。查错作品比不查更糟，所以命中判定很严：名称必须完全一致，年份要对得上才算。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <SettingsBoolSegment
              v-model="settings.scrape_follow_existing_location"
              label="沿用媒体库已有位置"
            />
          </template>
        </SettingsRow>
      </SettingsCard>

      <SettingsCard title="备份恢复目标" :accent="ORGANIZE_SETTINGS_ACCENT">
        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('backup_target')">
          <template #info>
            <div class="settings-row__label">
              <span>恢复目标</span>
              <SettingsHelpTooltip title="备份恢复目标说明">
                <p>备份里的配置与 STRM 目录恢复到本地还是网盘。恢复时网盘会先落一份本地暂存再上传，传完即删。</p>
                <p>STRM 目录只在「完整备份」时才会被打进去——设置级备份带上几百 MB 指针文件会让「先导出一份配置试试」变成苦等。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppSelect v-model="settings.backup_target" :options="backupTargetOptions" />
          </template>
        </SettingsRow>
      </SettingsCard>

      <SettingsCard title="风控熔断" :accent="ORGANIZE_SETTINGS_ACCENT">
        <p class="mo-risk-intro">
          下面是硬性保护，不是提示：触顶后整理与刮削会被<b>强制暂停</b>，状态存在库里，重启也不会丢。
          之所以默认全关，是因为这两个闸门只在你明确知道自己的网盘能承受多少调用时才有意义；
          宁可先跑出问题，也不要被一个猜出来的阈值卡住。
        </p>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('scrape_max_calls_per_window')">
          <template #info>
            <div class="settings-row__label">
              <span>连续调用上限（次）</span>
              <SettingsHelpTooltip title="连续调用上限说明">
                <p>在统计窗口内累计调用网盘接口到这个次数就熔断，0 表示不限。</p>
                <p>调用不是只在「整理」时发生：列目录、查详情、搬文件各算一次。大库一次全量扫描很容易上万次。</p>
                <p>建议设成你所用网盘单日安全调用量的一小部分。这个闸门的作用是「宁可慢也别把账号用废」——账号封了，搬走的是几百 GB 的整理成果。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput v-model="settings.scrape_max_calls_per_window" type="number" min="0" max="1000000" placeholder="0（不限）" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('scrape_call_window_seconds')">
          <template #info>
            <div class="settings-row__label">
              <span>调用统计窗口（秒）</span>
              <SettingsHelpTooltip title="统计窗口说明">
                <p>「连续调用次数」在多长的窗口内累计，默认 1 小时。</p>
                <p>窗口太短会把一次正常的批量扫描切成好几段，等于把上限废掉了；太长则会在你已经停手之后才熔断，失去意义。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput v-model="settings.scrape_call_window_seconds" type="number" min="60" max="86400" placeholder="3600" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('scrape_call_pause_seconds')">
          <template #info>
            <div class="settings-row__label">
              <span>调用暂停时长（秒）</span>
              <SettingsHelpTooltip title="暂停时长说明">
                <p>调用触顶后暂停多久，默认 1 小时，最长 86400 秒（24 小时），填更大的值会被截到这个上限。</p>
                <p>暂停期内启动的任务会被直接拒绝，不会排队、也不会等到期后偷偷补跑。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput v-model="settings.scrape_call_pause_seconds" type="number" min="60" max="86400" placeholder="3600" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('scrape_max_work_minutes')">
          <template #info>
            <div class="settings-row__label">
              <span>连续整理时长上限（分钟）</span>
              <SettingsHelpTooltip title="整理时长上限说明">
                <p>一轮整理跑过这么久就熔断，0 表示不限，默认不限。</p>
                <p>和调用次数是两道独立的闸门：一个防「单轮跑太久把账号打爆」，一个防「单轮太长把连接挂住」。大库建议开一个 120~240 分钟的上限。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput v-model="settings.scrape_max_work_minutes" type="number" min="0" max="10080" placeholder="0（不限）" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('scrape_work_pause_minutes')">
          <template #info>
            <div class="settings-row__label">
              <span>整理暂停时长（分钟）</span>
              <SettingsHelpTooltip title="整理暂停时长说明">
                <p>整理时长触顶后暂停多久，默认 60 分钟，最长 1440 分钟（24 小时），填更大的值会被截到这个上限。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput v-model="settings.scrape_work_pause_minutes" type="number" min="1" max="1440" placeholder="60" />
          </template>
        </SettingsRow>
      </SettingsCard>

      <SettingsCard title="小文件隔离" :accent="ORGANIZE_SETTINGS_ACCENT">
        <p class="mo-risk-intro">
          move 模式要腾出目标目录时，只移动大文件会把几百 KB 的 <code>.nfo</code>、<code>.txt</code>、封面图留在原地，
          目录因此永远不被判定为空，整理会一直失败。开这个开关让它们被移走。
        </p>
        <p class="mo-risk-warn">
          <b>注意语义是「移走」，不是「删除」。</b>
          被判为小文件的会移到隔离目录（默认整理根下的 <code>_隔离</code>），文件完整保留、随时可以搬回去。
          之所以不做真删：占空间的代价远小于误删一个没有备份的文件的代价。
        </p>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('min_media_size_bytes')">
          <template #info>
            <div class="settings-row__label">
              <span>媒体文件最小体积（字节）</span>
              <SettingsHelpTooltip title="最小体积说明">
                <p>低于这个体积的文件在 move 模式下会被移入隔离目录。0 表示不启用（默认）。</p>
                <p>参考值：一集 1080p 剧约 700MB~2GB，720p 约 300~800MB。想把 nfo/字幕/封面这类几十 KB 的杂物挪走，设 1 MB（1048576）通常就够了。</p>
                <p>只有 move 模式受影响：copy 模式本来就不动源目录，rename 模式不腾位置。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput v-model="settings.min_media_size_bytes" type="number" min="0" max="1073741824" placeholder="0（不启用）" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('quarantine_dir')">
          <template #info>
            <div class="settings-row__label">
              <span>隔离目录</span>
              <SettingsHelpTooltip title="隔离目录说明">
                <p>网盘路径。留空则用整理根目录下的 <code>_隔离</code> 子目录（不存在会自动创建）。</p>
                <p>隔离目录本身不会被巡检当成孤儿目录清理：它一直在被使用。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <AppInput v-model="settings.quarantine_dir" placeholder="留空使用 _隔离" />
          </template>
        </SettingsRow>

        <SettingsRow :show-changed-badge="true" :changed="isFieldChanged('small_file_acked')">
          <template #info>
            <div class="settings-row__label">
              <span>已确认小文件会被移走</span>
              <SettingsHelpTooltip title="知情确认说明">
                <p>必须先勾这一项，小文件隔离才会真正生效。没勾时设置页会照常保存，整理页会明确列出「因未确认而跳过的文件」，不会静默搬走。</p>
                <p>取消勾选即刻停止隔离，已移走的文件不会被自动搬回来——它们还在隔离目录里，手动处理即可。</p>
              </SettingsHelpTooltip>
            </div>
          </template>
          <template #control>
            <SettingsBoolSegment v-model="settings.small_file_acked" label="我知道文件会被移动到隔离目录" />
          </template>
        </SettingsRow>
      </SettingsCard>
    </template>
  </div>
</template>

<style scoped>
.mo-risk-intro {
  margin: 0 0 4px;
  font-size: 12px;
  line-height: 1.8;
  color: var(--text-muted);
}
.mo-risk-warn {
  margin: 0 0 12px;
  padding: 10px 12px;
  border: 1px solid var(--danger);
  border-radius: var(--radius-md);
  font-size: 12px;
  line-height: 1.8;
  color: var(--text-muted);
}
.mo-risk-warn b {
  color: var(--danger);
}
.mo-risk-intro code,
.mo-risk-warn code {
  padding: 1px 4px;
  border-radius: 3px;
  background: var(--bg-soft);
  font-size: 11px;
}
.mo-settings {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.mo-tag-row :deep(.settings-row) {
  align-items: flex-start;
}

.mo-tag-row :deep(.settings-row__info) {
  padding-top: 4px;
}

.mo-tag-editor {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
}

.mo-tag-editor__active {
  --tag-chip-width: calc(4em + 40px);
  --tag-chip-height: 32px;
}

.mo-tag-editor__active,
.mo-tag-editor__pool {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.mo-tag-editor__active--dragging {
  flex-wrap: nowrap;
}

:global(body.mo-tag-dragging) {
  cursor: grabbing;
  user-select: none;
}

.mo-tag-editor__placeholder {
  font-size: 13px;
  color: var(--text-muted);
}

.tag-chip {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  box-sizing: border-box;
  flex: 0 0 var(--tag-chip-width, calc(4em + 40px));
  width: var(--tag-chip-width, calc(4em + 40px));
  min-height: var(--tag-chip-height, 32px);
  padding: 6px 10px;
  background: color-mix(in srgb, var(--brand) 10%, var(--surface));
  border: 1px solid color-mix(in srgb, var(--brand) 25%, var(--border));
  border-radius: var(--radius-xs);
  font-size: 13px;
  line-height: 1.2;
  color: var(--brand);
  cursor: grab;
  user-select: none;
  touch-action: none;
}

.tag-chip:active {
  cursor: grabbing;
}

.tag-insert-preview {
  display: inline-flex;
  box-sizing: border-box;
  flex: 0 0 var(--tag-chip-width, calc(4em + 40px));
  width: var(--tag-chip-width, calc(4em + 40px));
  height: var(--tag-chip-height, 32px);
  min-height: var(--tag-chip-height, 32px);
  border: 1px dashed var(--brand);
  border-radius: var(--radius-xs);
  background: color-mix(in srgb, var(--brand) 6%, transparent);
}

.tag-chip--ghost {
  position: fixed;
  z-index: 10000;
  margin: 0;
  pointer-events: none;
  cursor: grabbing;
  box-sizing: border-box;
  box-shadow: 0 8px 20px color-mix(in srgb, var(--brand) 22%, transparent);
}

.tag-chip__text {
  flex: 0 0 4em;
  width: 4em;
  text-align: center;
  font-weight: 500;
}

.tag-chip__addon {
  flex: 0 0 12px;
  width: 12px;
  text-align: center;
  font-weight: 500;
}

.tag-chip__remove {
  flex: 0 0 16px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  font-size: 12px;
  cursor: pointer;
}

.tag-chip__remove:hover {
  background: color-mix(in srgb, var(--brand) 20%, transparent);
}

.tag-chip__remove--ghost {
  visibility: hidden;
  pointer-events: none;
}

.tag-chip--add {
  cursor: pointer;
  flex: 0 0 calc(4em + 28px);
  width: calc(4em + 28px);
  min-height: var(--tag-chip-height, 32px);
  background: var(--surface-sunken);
  border: 1px dashed var(--border-soft);
  color: var(--text-muted);
}

.tag-chip--add .tag-chip__text {
  color: var(--text-muted);
}

.tag-chip--add:hover {
  background: color-mix(in srgb, var(--brand) 8%, var(--surface));
  border-color: color-mix(in srgb, var(--brand) 40%, var(--border));
  color: var(--brand);
}
</style>
