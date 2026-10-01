<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  fetchDiscoverDetails,
  saveSubscription,
  toggleSubscription,
  type SubscriptionUpsertPayload,
} from "@/api/discovery";
import AppButton from "@/components/base/AppButton.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import { toast } from "@/composables/useToast";

// 条目标识（打开弹层时传入）
export interface DetailTarget {
  source: string;
  media_type: string; // movie/tv/person
  external_id: string; // tmdb_id/douban_id
}

const props = defineProps<{
  open: boolean;
  target: DetailTarget | null;
}>();

const emit = defineEmits<{
  close: [];
}>();

const loading = ref(false);
const errorMsg = ref("");
const detail = ref<Record<string, any> | null>(null);

const title = computed(() => detail.value?.title || detail.value?.original_title || "");
const backdrop = computed(() => detail.value?.backdrop || "");
const poster = computed(() => detail.value?.poster || "");
const overview = computed(() => detail.value?.overview || "");
const tagline = computed(() => detail.value?.tagline || "");
const vote = computed(() => (typeof detail.value?.vote_avg === "number" ? detail.value.vote_avg.toFixed(1) : ""));
const voteCount = computed(() => detail.value?.vote_count ?? 0);
const year = computed(() => detail.value?.year ?? "");
const releaseDate = computed(() => detail.value?.release_date || "");
const runtime = computed(() => detail.value?.runtime ?? 0);
const status = computed(() => detail.value?.status || "");
const genres = computed<string[]>(() => {
  const g = detail.value?.genres;
  if (Array.isArray(g)) return g.map((x: any) => (typeof x === "string" ? x : x?.name ?? String(x)));
  return [];
});
const cast = computed<any[]>(() => detail.value?.cast ?? []);
const crew = computed<any[]>(() => detail.value?.crew ?? []);
const subscription = computed<any>(() => detail.value?.subscription ?? null);
const transferTargets = computed<Record<string, any>>(() => detail.value?.transfer_targets ?? {});

// 订阅只支持 123 / 光鸭 / 139（后端 NormalizeTransferProvider 会拒绝 115）。
// 这里只列「已配置保存目录」的网盘，避免用户点完才发现没配目录。
const TARGET_PROVIDER_LABELS: Record<string, string> = {
  "123": "123 网盘",
  guangya: "光鸭",
  pan139: "139 网盘",
};
const targetOptions = computed(() =>
  ["123", "guangya", "pan139"]
    .filter((k) => transferTargets.value[k]?.configured)
    .map((k) => ({ value: k, label: TARGET_PROVIDER_LABELS[k] ?? k })),
);
const targetProvider = ref("123");
const subscribing = ref(false);

// 默认选中第一个「已配置目录」的网盘；用户切回来时保留其选择。
watch(targetOptions, (opts) => {
  if (opts.length > 0 && !opts.some((o) => o.value === targetProvider.value)) {
    targetProvider.value = String(opts[0].value);
  }
});

const canSubscribe = computed(
  () => props.target?.media_type === "movie" || props.target?.media_type === "tv",
);

/** 订阅：以当前条目身份 UPSERT（entity_key = tmdb:<type>:<id>），因此重复点击是安全的。 */
async function subscribe() {
  if (subscribing.value || !props.target) return;
  if (targetOptions.value.length === 0) {
    toast.warning("请先到「影视发现 - 基础配置」配置至少一个网盘的保存目录");
    return;
  }
  subscribing.value = true;
  try {
    const payload: SubscriptionUpsertPayload = {
      source: props.target.source || "tmdb",
      entity_type: props.target.media_type,
      external_id: String(props.target.external_id),
      media_type: props.target.media_type,
      title: title.value,
      original_title: detail.value?.original_title || "",
      poster_url: poster.value,
      target_provider: targetProvider.value,
      transfer_mode: "auto",
      enabled: true,
    };
    const res = await saveSubscription(payload);
    if (res.warning) toast.warning(res.warning);
    toast.success("已订阅追更");
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "订阅失败"));
  } finally {
    subscribing.value = false;
  }
}

/** 取消订阅：复用 toggle 接口显式传 false，语义比「取反」更明确。 */
async function unsubscribe() {
  if (subscribing.value || !subscription.value?.id) return;
  subscribing.value = true;
  try {
    await toggleSubscription(subscription.value.id, false);
    toast.success("已取消订阅");
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "取消订阅失败"));
  } finally {
    subscribing.value = false;
  }
}

function runtimeText(min: number): string {
  if (!min) return "";
  const h = Math.floor(min / 60);
  const m = min % 60;
  return h ? `${h} 小时 ${m} 分钟` : `${m} 分钟`;
}

async function load() {
  if (!props.target) return;
  loading.value = true;
  errorMsg.value = "";
  detail.value = null;
  try {
    detail.value = await fetchDiscoverDetails(
      props.target.source,
      props.target.media_type,
      props.target.external_id,
    );
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "作品资料加载失败");
  } finally {
    loading.value = false;
  }
}

watch(
  () => [props.open, props.target],
  ([open]) => {
    if (open) void load();
  },
  { immediate: true },
);
</script>

<template>
  <AppModal :open="open" size="account" :title="title || '作品详情'" @close="emit('close')">
    <div class="ddm">
      <AppStateBlock v-if="loading" message="加载详情中…" loading min-height="240px" />
      <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="240px" />

      <template v-else-if="detail">
        <!-- 顶部横幅 -->
        <div v-if="backdrop" class="ddm__backdrop">
          <img :src="backdrop" :alt="title" class="ddm__backdrop-img" />
        </div>

        <div class="ddm__head">
          <img v-if="poster" :src="poster" :alt="title" class="ddm__poster" />
          <div class="ddm__head-meta">
            <h2 class="ddm__title">{{ title }}</h2>
            <p v-if="detail.original_title && detail.original_title !== title" class="ddm__original">
              {{ detail.original_title }}
            </p>
            <p v-if="tagline" class="ddm__tagline">{{ tagline }}</p>

            <div class="ddm__facts">
              <span v-if="vote" class="ddm__fact ddm__fact--vote">★ {{ vote }}{{ voteCount ? ` (${voteCount})` : "" }}</span>
              <span v-if="year" class="ddm__fact">{{ year }}</span>
              <span v-if="releaseDate" class="ddm__fact">{{ releaseDate }}</span>
              <span v-if="runtime" class="ddm__fact">{{ runtimeText(runtime) }}</span>
              <span v-if="status" class="ddm__fact">{{ status }}</span>
            </div>

            <div v-if="genres.length" class="ddm__genres">
              <span v-for="g in genres" :key="g" class="ddm__genre">{{ g }}</span>
            </div>

            <div v-if="canSubscribe" class="ddm__actions">
              <template v-if="!subscription">
                <AppSelect
                  v-if="targetOptions.length > 1"
                  v-model="targetProvider"
                  :options="targetOptions"
                />
                <AppButton
                  type="button"
                  variant="primary"
                  :disabled="subscribing || targetOptions.length === 0"
                  :title="targetOptions.length === 0 ? '请先到「影视发现 - 基础配置」配置网盘保存目录' : ''"
                  @click="subscribe"
                >
                  {{ subscribing ? "订阅中…" : "订阅追更" }}
                </AppButton>
              </template>
              <template v-else>
                <span class="ddm__sub-state">
                  已订阅{{ subscription.status ? `（${subscription.status}）` : "" }}
                  <template v-if="subscription.target_provider">
                    · {{ TARGET_PROVIDER_LABELS[subscription.target_provider] ?? subscription.target_provider }}
                  </template>
                </span>
                <AppButton type="button" variant="secondary" :disabled="subscribing" @click="unsubscribe">
                  {{ subscribing ? "处理中…" : "取消订阅" }}
                </AppButton>
              </template>
            </div>
          </div>
        </div>

        <!-- 剧情 -->
        <section v-if="overview" class="ddm__section">
          <h3 class="ddm__section-title">剧情简介</h3>
          <p class="ddm__overview">{{ overview }}</p>
        </section>

        <!-- 演职员 -->
        <section v-if="crew.length || cast.length" class="ddm__section">
          <h3 class="ddm__section-title">演职员</h3>
          <div v-if="crew.length" class="ddm__people">
            <div v-for="(p, i) in crew.slice(0, 4)" :key="`crew-${i}`" class="ddm__person">
              <p class="ddm__person-name">{{ p.name }}</p>
              <p class="ddm__person-role">{{ p.job || p.role || "主创" }}</p>
            </div>
          </div>
          <div v-if="cast.length" class="ddm__people">
            <div v-for="(p, i) in cast.slice(0, 8)" :key="`cast-${i}`" class="ddm__person">
              <p class="ddm__person-name">{{ p.name }}</p>
              <p class="ddm__person-role">{{ p.character || p.role || "演员" }}</p>
            </div>
          </div>
        </section>

        <!-- 转存目标状态（调试/扩展） -->
        <section v-if="Object.keys(transferTargets).length" class="ddm__section">
          <h3 class="ddm__section-title">转存到网盘</h3>
          <div class="ddm__targets">
            <div v-for="(v, k) in transferTargets" :key="k" class="ddm__target">
              <span class="ddm__target-name">{{ k }}</span>
              <span class="ddm__target-state" :class="{ 'ddm__target-state--on': v?.configured }">
                {{ v?.configured ? (v?.folder_name || "已配置") : "未配置" }}
              </span>
            </div>
          </div>
        </section>
      </template>
    </div>
  </AppModal>
</template>

<style scoped>
.ddm__backdrop {
  margin: -16px -16px 14px;
  aspect-ratio: 16 / 7;
  overflow: hidden;
  border-radius: var(--radius-sm, 8px) var(--radius-sm, 8px) 0 0;
  background: var(--surface-sunken, #14161c);
}

.ddm__backdrop-img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.ddm__head {
  display: flex;
  gap: 16px;
  margin-bottom: 16px;
}

.ddm__poster {
  width: 120px;
  aspect-ratio: 2 / 3;
  object-fit: cover;
  border-radius: var(--radius-sm, 8px);
  flex-shrink: 0;
  background: var(--surface-sunken, #14161c);
}

.ddm__head-meta {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.ddm__title {
  margin: 0;
  font-size: 20px;
  font-weight: 700;
  color: var(--text, #e5e7eb);
}

.ddm__original {
  margin: 0;
  font-size: 13px;
  color: var(--text-muted, #6b7280);
}

.ddm__tagline {
  margin: 0;
  font-size: 13px;
  font-style: italic;
  color: var(--text-muted, #6b7280);
}

.ddm__facts {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.ddm__fact {
  padding: 3px 9px;
  border-radius: var(--radius-pill, 999px);
  background: var(--surface-sunken, #14161c);
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}

.ddm__fact--vote {
  color: #fbbf24;
  font-weight: 700;
}

.ddm__genres {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.ddm__genre {
  padding: 3px 10px;
  border-radius: var(--radius-pill, 999px);
  border: 1px solid var(--border, #2a2f3a);
  font-size: 12px;
  color: var(--text, #d1d5db);
}

.ddm__actions {
  margin-top: 4px;
  display: flex;
  align-items: center;
  gap: 10px;
}

.ddm__sub-state {
  color: var(--text-muted);
  font-size: 13px;
}

.ddm__section {
  margin-top: 16px;
  padding-top: 14px;
  border-top: 1px solid var(--border-soft, #232733);
}

.ddm__section-title {
  margin: 0 0 10px;
  font-size: 14px;
  font-weight: 700;
  color: var(--text, #e5e7eb);
}

.ddm__overview {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
  color: var(--text, #d1d5db);
}

.ddm__people {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
  gap: 8px;
  margin-bottom: 8px;
}

.ddm__person {
  padding: 8px 10px;
  border-radius: var(--radius-sm, 8px);
  background: var(--surface-sunken, #14161c);
}

.ddm__person-name {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--text, #e5e7eb);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.ddm__person-role {
  margin: 2px 0 0;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}

.ddm__targets {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}

.ddm__target {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border-radius: var(--radius-sm, 8px);
  background: var(--surface-sunken, #14161c);
  font-size: 13px;
}

.ddm__target-name {
  font-weight: 600;
  color: var(--text, #e5e7eb);
}

.ddm__target-state {
  color: var(--text-muted, #6b7280);
}

.ddm__target-state--on {
  color: #34d399;
}

@media (max-width: 640px) {
  .ddm__head {
    flex-direction: column;
  }

  .ddm__poster {
    width: 100px;
  }
}
</style>
