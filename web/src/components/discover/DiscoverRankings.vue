<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  fetchDiscoverMeta,
  fetchDiscoverRankings,
  fetchDiscoverRankingsMaoyan,
  type DiscoverItem,
  type DiscoverMeta,
  type RankingGroup,
  type RankingItem,
} from "@/api/discovery";
import DiscoverPosterCard from "@/components/discover/DiscoverPosterCard.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import { toast } from "@/composables/useToast";

const loading = ref(false);
const errorMsg = ref("");
const items = ref<RankingItem[]>([]);
const maoyanGroups = ref<RankingGroup[]>([]);
const isMaoyan = ref(false);
const feedStatus = ref("ok");
const page = ref(1);
const totalPages = ref(1);
const meta = ref<DiscoverMeta | null>(null);

const provider = ref("hdhive");
const region = ref("");
const mediaType = ref("");

const providerOptions = computed(() => {
  const opts = [
    { value: "hdhive", label: "RE0 流媒体榜" },
    { value: "maoyan", label: "猫眼热播榜" },
  ];
  for (const c of meta.value?.collections ?? []) {
    if (c.key) opts.push({ value: c.key, label: `豆瓣·${c.label || c.key}` });
  }
  return opts;
});

const mediaTypeOptions = [
  { value: "", label: "全部类型" },
  { value: "movie", label: "电影" },
  { value: "tv", label: "剧集" },
];

async function loadMeta() {
  try {
    meta.value = await fetchDiscoverMeta();
  } catch {
    /* 元数据失败不阻断 */
  }
}

async function load() {
  loading.value = true;
  errorMsg.value = "";
  isMaoyan.value = provider.value === "maoyan";
  try {
    if (isMaoyan.value) {
      const res = await fetchDiscoverRankingsMaoyan({});
      maoyanGroups.value = res.groups ?? [];
      feedStatus.value = res.feed_status ?? "ok";
      items.value = [];
    } else {
      const result = await fetchDiscoverRankings({
        provider: provider.value,
        region: region.value,
        media_type: mediaType.value,
        page: page.value,
      });
      items.value = (result.items ?? []) as RankingItem[];
      totalPages.value = result.total_pages ?? 1;
      maoyanGroups.value = [];
    }
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载榜单失败");
    items.value = [];
    maoyanGroups.value = [];
  } finally {
    loading.value = false;
  }
}

function applyProvider() {
  page.value = 1;
  void load();
}

function onCardClick(item: DiscoverItem) {
  toast.info(`「${item.title}」详情页开发中`);
}

onMounted(() => {
  void loadMeta();
  void load();
});
</script>

<template>
  <div class="dr">
    <div class="dr__toolbar">
      <AppSelect v-model="provider" :options="providerOptions" class="dr__provider" @update:model-value="applyProvider" />
      <AppSelect
        v-if="!isMaoyan"
        v-model="mediaType"
        :options="mediaTypeOptions"
        class="dr__filter"
        @update:model-value="applyProvider"
      />
      <AppButton type="button" variant="secondary" :disabled="loading" @click="load">刷新</AppButton>
    </div>

    <AppStateBlock v-if="loading" message="加载榜单中…" loading min-height="300px" />
    <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="300px" />

    <!-- 猫眼：分组榜单 -->
    <template v-else-if="isMaoyan">
      <AppStateBlock v-if="feedStatus === 'pending'" message="猫眼榜单抓取中，稍后自动重试…" loading min-height="200px" />
      <div v-for="(g, gi) in maoyanGroups" v-else :key="g.key || gi" class="dr__group">
        <h3 class="dr__group-title">{{ g.label || `榜单 ${gi + 1}` }}</h3>
        <div class="dr__grid">
          <DiscoverPosterCard
            v-for="(item, idx) in g.items ?? []"
            :key="item.entity_key || item.tmdb_id || item.douban_id || idx"
            :item="item"
            :rank="idx + 1"
            @click="onCardClick"
          />
        </div>
      </div>
      <AppStateBlock v-if="!maoyanGroups.length && feedStatus !== 'pending'" message="暂无榜单数据" min-height="200px" />
    </template>

    <!-- 其它源：平铺榜单 -->
    <div v-else-if="items.length" class="dr__grid">
      <DiscoverPosterCard
        v-for="(item, idx) in items"
        :key="item.entity_key || item.tmdb_id || item.douban_id || idx"
        :item="item"
        :rank="idx + 1"
        @click="onCardClick"
      />
    </div>

    <AppStateBlock v-else message="暂无榜单数据" min-height="300px" />
  </div>
</template>

<style scoped>
.dr__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-bottom: 16px;
}

.dr__provider {
  min-width: 180px;
}

.dr__filter {
  min-width: 120px;
}

.dr__group {
  margin-bottom: 22px;
}

.dr__group-title {
  margin: 0 0 12px;
  font-size: 15px;
  font-weight: 700;
  color: var(--text, #e5e7eb);
}

.dr__grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 14px;
}
</style>
