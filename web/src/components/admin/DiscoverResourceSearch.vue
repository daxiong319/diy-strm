<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  offlineDiscoverResource,
  searchDiscoverResources,
  transferDiscoverResource,
  type DiscoverResourceItem,
  type DiscoverResourceSearchResult,
} from "@/api/discovery";
import AppBadge from "@/components/base/AppBadge.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import FormField from "@/components/base/FormField.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import { toast } from "@/composables/useToast";

/**
 * 影视发现「资源搜索」页：聚合 re0 / 观影 / SeedHub / TG 四源，
 * 支持一键转存（普通网盘分享）与离线下载（magnet / ed2k）。
 *
 * 接口：POST /admin/discovery/resources/{search,transfer,offline}
 */

const props = withDefaults(
  defineProps<{
    /** 可选：从详情页跳转过来时带上 TMDB ID，能显著提升 re0/观影 源命中率 */
    tmdbId?: number | null;
    /** 可选：预填片名 */
    initialTitle?: string;
  }>(),
  { tmdbId: null, initialTitle: "" },
);

// --- 搜索表单 -------------------------------------------------------------
const form = reactive({
  title: props.initialTitle,
  year: "",
  mediaType: "movie",
  providers: [] as string[],
});

/** 来源多选：值与后端 normalizeResourceSources 的合法取值一一对应 */
const SOURCE_OPTIONS = [
  { value: "re0", label: "RE0" },
  { value: "guanying", label: "观影" },
  { value: "seedhub", label: "SeedHub" },
  { value: "tg", label: "TG 频道" },
];
const selectedSources = ref<string[]>(["re0", "guanying", "seedhub", "tg"]);

const mediaTypeOptions = [
  { value: "movie", label: "电影" },
  { value: "tv", label: "剧集" },
];

/** 目标网盘：留空 = 不过滤（后端 provider 为空时返回全部） */
const providerOptions = [
  { value: "", label: "全部网盘（不过滤）" },
  { value: "123", label: "123 网盘" },
  { value: "guangya", label: "光鸭" },
  { value: "pan139", label: "139（移动云盘）" },
];
const targetProvider = ref("");

// --- 结果状态 -------------------------------------------------------------
const loading = ref(false);
const errorMsg = ref("");
const searched = ref(false);
const items = ref<DiscoverResourceItem[]>([]);
/** 部分来源失败信息（★ 后端整体仍返回 200，必须显式消费，否则用户会误判"没资源"） */
const sourceErrors = ref<string[]>([]);
/**
 * 未启用的来源（只是没配置），与 sourceErrors 分开：
 * 没配一个源不是失败，不该红色报错，只在结果页脚做轻提示。
 */
const skippedSources = ref<string[]>([]);

/** 正在进行中的资源 key 集合：★ 防止同一条资源被并发重复提交 */
const pendingKeys = ref<Set<string>>(new Set());
/** 已成功提交的资源 key → 结果文案 */
const submitted = reactive<Record<string, string>>({});

function toggleSource(value: string) {
  const i = selectedSources.value.indexOf(value);
  if (i >= 0) selectedSources.value.splice(i, 1);
  else selectedSources.value.push(value);
}

const canSearch = computed(() => form.title.trim().length > 0 || (props.tmdbId ?? 0) > 0);

async function runSearch() {
  if (!canSearch.value) {
    toast.warning("请输入片名后再搜索");
    return;
  }
  if (selectedSources.value.length === 0) {
    toast.warning("请至少选择一个资源来源");
    return;
  }
  loading.value = true;
  errorMsg.value = "";
  sourceErrors.value = [];
  skippedSources.value = [];
  items.value = [];
  searched.value = true;
  try {
    const body = {
      title: form.title.trim(),
      media_type: form.mediaType,
      year: form.year.trim() || undefined,
      sources: [...selectedSources.value],
      provider: targetProvider.value || undefined,
      tmdb_id: props.tmdbId && props.tmdbId > 0 ? props.tmdbId : undefined,
    };
    const res: DiscoverResourceSearchResult = await searchDiscoverResources(body);
    items.value = Array.isArray(res.items) ? res.items : [];
    // ★ 消费 errors：有些源失败但整体 200，必须告知用户，否则会以为"没资源"其实是源挂了
    const errs = Array.isArray(res.errors) ? res.errors : [];
    sourceErrors.value = errs.map((e) => {
      const label = sourceLabel(e.source) || e.source || "未知来源";
      const msg = e.message || e.error || e.code || "查询失败";
      return `${label}：${msg}`;
    });
    // 未配置的来源单独收：不是错误，只做「已跳过」轻提示
    const skips = Array.isArray(res.skipped) ? res.skipped : [];
    skippedSources.value = skips
      .filter((s) => s.code !== "RESOURCE_SOURCE_ERROR")
      .map((s) => sourceLabel(s.source) || s.source || "未知来源");
    // 只有「依赖没跑」这类真故障才值得警告；未配置不弹任何 toast。
    if (sourceErrors.value.length) {
      const unavailable = errs.some((e) => e.code === "RESOURCE_SOURCE_UNAVAILABLE");
      toast.warning(
        unavailable
          ? `部分来源不可用：${sourceErrors.value.join("；")}`
          : `部分来源查询失败：${sourceErrors.value.join("；")}`,
      );
    }
    if (!items.value.length && !sourceErrors.value.length) {
      toast.info("未找到匹配资源");
    }
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "资源搜索失败");
    items.value = [];
    toast.error(errorMsg.value);
  } finally {
    loading.value = false;
  }
}

function resetSearch() {
  form.title = "";
  form.year = "";
  form.mediaType = "movie";
  targetProvider.value = "";
  selectedSources.value = ["re0", "guanying", "seedhub", "tg"];
  items.value = [];
  sourceErrors.value = [];
  skippedSources.value = [];
  errorMsg.value = "";
  searched.value = false;
}

// --- 资源分类 / 展示辅助 --------------------------------------------------
function sourceLabel(source?: string): string {
  switch ((source || "").toLowerCase()) {
    case "re0":
      return "RE0";
    case "guanying":
      return "观影";
    case "seedhub":
      return "SeedHub";
    case "tg":
      return "TG";
    default:
      return source || "";
  }
}

function sourceTone(source?: string): "success" | "info" | "warning" | "neutral" {
  switch ((source || "").toLowerCase()) {
    case "re0":
      return "warning";
    case "guanying":
      return "info";
    case "seedhub":
      return "success";
    case "tg":
      return "neutral";
    default:
      return "neutral";
  }
}

function providerTone(item: DiscoverResourceItem): "success" | "info" | "warning" | "neutral" {
  if (isOfflineLink(item)) return "warning";
  switch ((item.provider || "").toLowerCase()) {
    case "123":
    case "123pan":
      return "info";
    case "guangyapan":
    case "guangya":
      return "success";
    case "139":
    case "pan139":
      return "neutral";
    default:
      return "neutral";
  }
}

/** ★ link_type 为 magnet/ed2k 时必须走离线通道（转存接口会直接拒绝） */
function isOfflineLink(item: DiscoverResourceItem): boolean {
  const lt = (item.link_type || item.provider || "").toLowerCase();
  return lt === "magnet" || lt === "ed2k";
}

function keyOf(item: DiscoverResourceItem): string {
  return item.item_key || `${item.source}:${item.slug || item.share_url || item.title}`;
}

/** 离线可选的目标网盘（后端 offlineSupportedTargets 语义） */
function offlineTargets(item: DiscoverResourceItem): string[] {
  if (Array.isArray(item.supported_targets) && item.supported_targets.length) {
    return item.supported_targets;
  }
  const lt = (item.link_type || "").toLowerCase();
  if (lt === "magnet") return ["115", "123", "guangya"];
  if (lt === "ed2k") return ["115", "guangya"];
  return [];
}

/** 离线目标网盘展示文案 */
function offlineTargetsText(item: DiscoverResourceItem): string {
  return offlineTargets(item).join(" / ");
}

/**
 * 判断该资源当前能否直接执行动作；不能则返回禁用原因。
 * ★ 缺少可用链接 / 网盘不支持 → 按钮 disabled 并用 title 说明原因。
 */
function disabledReason(item: DiscoverResourceItem): string {
  if (isOfflineLink(item)) {
    if (!offlineLink(item)) return "缺少磁力/eD2k 链接，无法离线下载";
    return "";
  }
  if (!item.slug && !item.share_url) return "缺少资源 slug / 分享链接，无法转存";
  // RE0 积分资源未解锁时无法直接转存
  if (item.source === "re0" && item.is_unlocked === false && !item.share_url) {
    const pts = item.unlock_points > 0 ? `（需 ${item.unlock_points} 积分）` : "";
    return `该资源尚未解锁${pts}，请先前往来源站点解锁`;
  }
  return "";
}

/** 离线链接取值：share_url 优先，回落到 slug（后端同语义） */
function offlineLink(item: DiscoverResourceItem): string {
  return item.share_url || item.slug || "";
}

const specText = computed(() => (item: DiscoverResourceItem) => {
  const tags = Array.isArray(item.resource_spec_tags) ? item.resource_spec_tags.filter(Boolean) : [];
  return tags.slice(0, 4).join(" · ");
});

function episodeText(item: DiscoverResourceItem): string {
  const ep = item.episode;
  if (!ep) return "";
  const parts: string[] = [];
  if (ep.season_num) parts.push(`S${String(ep.season_num).padStart(2, "0")}`);
  if (ep.episode_num) {
    let seg = `E${String(ep.episode_num).padStart(2, "0")}`;
    if (ep.end_episode_num && ep.end_episode_num !== ep.episode_num) {
      seg += `-E${String(ep.end_episode_num).padStart(2, "0")}`;
    }
    parts.push(seg);
  }
  if (ep.is_complete) parts.push("全");
  return parts.join("");
}

function isPending(item: DiscoverResourceItem): boolean {
  return pendingKeys.value.has(keyOf(item));
}

// --- 转存 / 离线 ----------------------------------------------------------
async function submit(item: DiscoverResourceItem) {
  const key = keyOf(item);
  // ★ 防并发重复提交：进行中直接忽略，成功后不再重复提交
  if (pendingKeys.value.has(key)) return;
  if (submitted[key]) {
    toast.info("该资源已提交过，请勿重复操作");
    return;
  }
  const reason = disabledReason(item);
  if (reason) {
    toast.warning(reason);
    return;
  }

  const offline = isOfflineLink(item);
  const body = {
    source: item.source,
    provider: item.provider,
    slug: item.slug || item.share_url || "",
    title: item.title,
    link_type: item.link_type || item.provider,
    share_url: item.share_url || item.slug || "",
  };

  pendingKeys.value = new Set(pendingKeys.value).add(key);
  try {
    if (offline) {
      const res = await offlineDiscoverResource(body);
      const msg = res.message || "离线下载任务已提交";
      submitted[key] = msg;
      toast.success(msg);
    } else {
      // ★ 后端同步阻塞最长 180 秒：必须用 postWithTimeout(200s)，
      //   否则 90 秒默认超时会先炸，用户看到"请求超时"但后端其实还在跑
      const res = await transferDiscoverResource(body);
      const msg = res.success === false ? "转存未完成" : res.message || "转存任务已提交";
      submitted[key] = msg;
      toast.success(msg);
    }
  } catch (e) {
    toast.error(getApiErrorMessage(e, offline ? "离线下载失败" : "转存失败"));
  } finally {
    const next = new Set(pendingKeys.value);
    next.delete(key);
    pendingKeys.value = next;
  }
}
</script>

<template>
  <div class="drs">
    <SettingsCard title="资源搜索" accent="var(--brand)">
      <div class="drs__form">
        <FormField label="片名" required class="drs__field drs__field--wide">
          <AppInput
            v-model="form.title"
            placeholder="输入片名后回车搜索，如：沙丘"
            @keyup.enter="runSearch"
          />
        </FormField>
        <FormField label="年份" class="drs__field drs__field--sm">
          <AppInput v-model="form.year" placeholder="如 2024" @keyup.enter="runSearch" />
        </FormField>
        <FormField label="媒体类型" class="drs__field drs__field--sm">
          <AppSelect v-model="form.mediaType" :options="mediaTypeOptions" />
        </FormField>
        <FormField label="目标网盘" class="drs__field drs__field--sm">
          <AppSelect v-model="targetProvider" :options="providerOptions" />
        </FormField>

        <FormField label="资源来源" class="drs__field drs__field--wide">
          <div class="drs__sources">
            <label
              v-for="opt in SOURCE_OPTIONS"
              :key="opt.value"
              class="drs__source"
              :class="{ 'drs__source--on': selectedSources.includes(opt.value) }"
            >
              <input
                type="checkbox"
                :checked="selectedSources.includes(opt.value)"
                @change="toggleSource(opt.value)"
              />
              <span>{{ opt.label }}</span>
            </label>
          </div>
        </FormField>

        <div class="drs__actions">
          <AppButton variant="primary" :disabled="loading || !canSearch" @click="runSearch">
            {{ loading ? "搜索中…" : "搜索" }}
          </AppButton>
          <AppButton variant="ghost" :disabled="loading" @click="resetSearch">重置</AppButton>
        </div>
      </div>

      <p v-if="tmdbId" class="drs__tmdb">已关联 TMDB ID：{{ tmdbId }}</p>
    </SettingsCard>

    <!-- 部分来源不可用：内联提示（toast 会消失，这里常驻可见）。
         ★ 这类是真故障（依赖进程没跑/端口不通），提示里已含修复建议。 -->
    <div v-if="sourceErrors.length" class="drs__src-errors">
      <AppBadge tone="warning">部分来源不可用</AppBadge>
      <ul class="drs__src-error-list">
        <li v-for="(msg, i) in sourceErrors" :key="i">{{ msg }}</li>
      </ul>
    </div>

    <!-- 未启用的来源：不是错误，只做一行轻提示，避免让用户以为出问题了 -->
    <p v-if="skippedSources.length" class="drs__src-skipped">
      <AppBadge tone="neutral">已跳过</AppBadge>
      以下来源尚未配置，本次未参与检索：{{ skippedSources.join("、") }}
    </p>

    <!-- 加载中 -->
    <AppStateBlock v-if="loading" message="正在搜索各来源资源…" loading />

    <!-- 失败 -->
    <AppStateBlock v-else-if="errorMsg" :message="errorMsg" />

    <!-- 未搜索 -->
    <AppStateBlock v-else-if="!searched" message="输入片名开始搜索" />

    <!-- 无结果 -->
    <AppStateBlock
      v-else-if="!items.length"
      :message="sourceErrors.length ? '未找到资源（部分来源查询失败）' : '未找到资源'"
    />

    <!-- 结果网格 -->
    <div v-else class="drs__grid">
      <article v-for="item in items" :key="keyOf(item)" class="drs-card">
        <div class="drs-card__head">
          <div class="drs-card__badges">
            <AppBadge :tone="sourceTone(item.source)">{{ sourceLabel(item.source) }}</AppBadge>
            <AppBadge :tone="providerTone(item)">
              {{ item.provider_label || item.provider || "未知网盘" }}
            </AppBadge>
            <AppBadge v-if="item.is_official" tone="success">官方</AppBadge>
          </div>
          <span v-if="submitted[keyOf(item)]" class="drs-card__done">已提交</span>
        </div>

        <h4 class="drs-card__title" :title="item.title">{{ item.title || "未命名资源" }}</h4>

        <div class="drs-card__meta">
          <span v-if="item.size" class="drs-card__size">{{ item.size }}</span>
          <span v-if="episodeText(item)" class="drs-card__ep">{{ episodeText(item) }}</span>
        </div>

        <p v-if="item.resource_spec_tags?.length" class="drs-card__spec">{{ specText(item) }}</p>

        <p v-if="item.subtitle_languages?.length" class="drs-card__sub">
          字幕：{{ item.subtitle_languages.slice(0, 3).join(" / ") }}
        </p>
        <p v-if="item.remark" class="drs-card__remark" :title="item.remark">{{ item.remark }}</p>
        <p v-if="item.sharer" class="drs-card__sharer">分享者：{{ item.sharer }}</p>

        <p v-if="isOfflineLink(item) && offlineTargets(item).length" class="drs-card__sub">
          可离线到：{{ offlineTargetsText(item) }}
        </p>

        <p v-if="item.validate_message" class="drs-card__warn">{{ item.validate_message }}</p>

        <!-- ★ 操作按钮按 link_type 分流：magnet/ed2k → 离线下载；其它 → 转存 -->
        <div class="drs-card__foot">
          <AppButton
            :variant="submitted[keyOf(item)] ? 'secondary' : 'primary'"
            size="sm"
            :disabled="isPending(item) || !!submitted[keyOf(item)] || !!disabledReason(item)"
            :title="disabledReason(item) || undefined"
            @click="submit(item)"
          >
            <template v-if="isPending(item)">
              {{ isOfflineLink(item) ? "提交中…" : "转存中…" }}
            </template>
            <template v-else-if="submitted[keyOf(item)]">已提交</template>
            <template v-else>{{ isOfflineLink(item) ? "离线下载" : "转存" }}</template>
          </AppButton>
          <span v-if="isPending(item)" class="drs-card__hint">
            {{ isOfflineLink(item) ? "正在提交离线任务…" : "正在转存，可能需要 1-3 分钟，请勿离开页面" }}
          </span>
          <span v-else-if="disabledReason(item)" class="drs-card__hint drs-card__hint--off">
            {{ disabledReason(item) }}
          </span>
        </div>
      </article>
    </div>
  </div>
</template>

<style scoped>
.drs {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.drs__form {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 12px;
}

.drs__field {
  flex: 0 0 auto;
}
.drs__field--wide {
  flex: 1 1 280px;
  min-width: 220px;
}
.drs__field--sm {
  width: 160px;
}

.drs__sources {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.drs__source {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-pill, 999px);
  background: var(--surface);
  color: var(--text-muted);
  font-size: 13px;
  cursor: pointer;
  user-select: none;
  transition: var(--transition);
}
.drs__source--on {
  border-color: var(--brand);
  color: var(--text);
  background: color-mix(in srgb, var(--brand) 10%, var(--surface));
}
.drs__source input {
  cursor: pointer;
}

.drs__actions {
  display: flex;
  gap: 8px;
}

.drs__tmdb {
  margin: 10px 0 0;
  font-size: 12px;
  color: var(--text-muted);
}

.drs__src-errors {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 10px 14px;
  border: 1px solid color-mix(in srgb, var(--warning) 35%, var(--border));
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--warning) 8%, var(--surface));
}
.drs__src-error-list {
  margin: 2px 0 0;
  padding-left: 18px;
  font-size: 13px;
  color: var(--text-regular);
  line-height: 1.6;
}

/* 未启用来源：中性样式，刻意不用警示色——没配置不是错误 */
.drs__src-skipped {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  padding: 8px 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface-subtle, var(--surface));
  font-size: 13px;
  color: var(--text-muted, var(--text-regular));
}

.drs__grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 14px;
}

.drs-card {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-md, 10px);
  background: var(--surface);
  transition: var(--transition);
}
.drs-card:hover {
  border-color: color-mix(in srgb, var(--brand) 45%, var(--border));
  box-shadow: 0 6px 18px rgb(0 0 0 / 22%);
}

.drs-card__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.drs-card__badges {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.drs-card__done {
  flex: 0 0 auto;
  font-size: 12px;
  font-weight: 600;
  color: var(--success);
}

.drs-card__title {
  margin: 2px 0 0;
  font-size: 14px;
  font-weight: 600;
  line-height: 1.45;
  color: var(--text);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.drs-card__meta {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  font-size: 12px;
  color: var(--text-muted);
}
.drs-card__size {
  font-weight: 600;
  color: var(--text-regular);
}

.drs-card__spec {
  margin: 0;
  font-size: 12px;
  color: var(--info);
}

.drs-card__sub,
.drs-card__sharer {
  margin: 0;
  font-size: 12px;
  color: var(--text-muted);
}

.drs-card__remark {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--text-muted);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.drs-card__warn {
  margin: 0;
  font-size: 12px;
  color: var(--warning);
}

.drs-card__foot {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: auto;
  padding-top: 10px;
}

.drs-card__hint {
  font-size: 12px;
  line-height: 1.4;
  color: var(--text-muted);
}
.drs-card__hint--off {
  color: var(--warning);
}
</style>
