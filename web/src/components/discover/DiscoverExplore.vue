<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  addDiscoverFavorite,
  checkDiscoverFavorites,
  deleteDiscoverFavorite,
  fetchDiscoverExplore,
  fetchDiscoverExploreDouban,
  fetchDiscoverMeta,
  fetchDiscoverSearch,
  type DiscoverItem,
  type DiscoverMeta,
} from "@/api/discovery";
import DiscoverPosterCard from "@/components/discover/DiscoverPosterCard.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import { toast } from "@/composables/useToast";
import "@/styles/admin-table.css";

type SourceTab = "tmdb" | "douban";

const loading = ref(false);
const errorMsg = ref("");
const items = ref<DiscoverItem[]>([]);
const page = ref(1);
const totalPages = ref(1);
const meta = ref<DiscoverMeta | null>(null);
const favoritedMap = ref<Record<string, boolean>>({});

const source = ref<SourceTab>("tmdb");
const searchKeyword = ref("");
const searching = ref(false);

const filters = reactive({
  type: "movie",
  genre: "",
  year: "",
  region: "",
  sort_by: "popular",
  doubanTag: "热门",
});

const typeOptions = [
  { value: "movie", label: "电影" },
  { value: "tv", label: "剧集" },
];

const sortOptions = [
  { value: "popular", label: "人气" },
  { value: "latest", label: "最新" },
  { value: "rating", label: "评分" },
];

const yearOptions = computed(() => {
  const opts = [{ value: "", label: "全部年份" }];
  const now = new Date().getFullYear();
  for (let y = now; y >= now - 40; y--) opts.push({ value: String(y), label: String(y) });
  return opts;
});

const genreOptions = computed(() => {
  const map = filters.type === "tv" ? meta.value?.genres_tv : meta.value?.genres_movie;
  const opts = [{ value: "", label: "全部类型" }];
  for (const [id, name] of Object.entries(map ?? {})) opts.push({ value: id, label: name });
  return opts;
});

const doubanTagOptions = computed(() => {
  const tags = meta.value?.douban_tags?.[filters.type] ?? [];
  return tags.map((t) => ({ value: t, label: t }));
});

const showFilters = computed(() => !searching.value);

async function loadMeta() {
  try {
    meta.value = await fetchDiscoverMeta();
  } catch {
    /* 元数据失败不阻断，仍可用默认筛选 */
  }
}

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    let result;
    if (searching.value && searchKeyword.value.trim()) {
      result = await fetchDiscoverSearch({
        q: searchKeyword.value.trim(),
        media_type: filters.type,
        page: page.value,
      });
    } else if (source.value === "douban") {
      result = await fetchDiscoverExploreDouban({ type: filters.type, tag: filters.doubanTag, page: page.value });
    } else {
      result = await fetchDiscoverExplore({
        type: filters.type,
        genre: filters.genre,
        year: filters.year,
        region: filters.region,
        sort_by: filters.sort_by,
        page: page.value,
      });
    }
    items.value = result.items ?? [];
    totalPages.value = result.total_pages ?? 1;
    await syncFavoriteStates();
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载失败");
    items.value = [];
  } finally {
    loading.value = false;
  }
}

async function syncFavoriteStates() {
  const keys = items.value.map((it) => it.entity_key).filter(Boolean) as string[];
  if (keys.length === 0) return;
  try {
    const res = await checkDiscoverFavorites(keys);
    favoritedMap.value = { ...favoritedMap.value, ...(res.favorited ?? {}) };
  } catch {
    /* 收藏状态查询失败不阻断列表 */
  }
}

function entityKeyOf(item: DiscoverItem): string {
  return item.entity_key || `${item.source}:${item.media_type}:${item.tmdb_id ?? item.douban_id ?? item.title}`;
}

async function toggleFavorite(item: DiscoverItem) {
  const key = entityKeyOf(item);
  if (favoritedMap.value[key]) {
    // 已收藏 → 取消（需先查到收藏 ID）
    try {
      const { fetchDiscoverFavorites } = await import("@/api/discovery");
      const res = await fetchDiscoverFavorites();
      const fav = res.items.find((f) => f.entity_key === key);
      if (fav?.id) await deleteDiscoverFavorite(fav.id);
      favoritedMap.value[key] = false;
      toast.success("已取消收藏");
    } catch (e) {
      toast.error(getApiErrorMessage(e, "取消收藏失败"));
    }
  } else {
    try {
      await addDiscoverFavorite({
        entity_key: key,
        source: item.source,
        media_type: item.media_type,
        external_id: String(item.tmdb_id ?? item.douban_id ?? ""),
        tmdb_id: item.tmdb_id,
        title: item.title,
        original_title: item.original_title,
        poster: item.poster,
        overview: item.overview,
        vote_avg: item.vote_avg,
        year: item.year,
      });
      favoritedMap.value[key] = true;
      toast.success(`已收藏「${item.title}」`);
    } catch (e) {
      toast.error(getApiErrorMessage(e, "收藏失败"));
    }
  }
}

function switchSource(s: SourceTab) {
  source.value = s;
  page.value = 1;
  void load();
}

function applyFilters() {
  page.value = 1;
  void load();
}

function doSearch() {
  if (!searchKeyword.value.trim()) {
    searching.value = false;
    void load();
    return;
  }
  searching.value = true;
  page.value = 1;
  void load();
}

function clearSearch() {
  searchKeyword.value = "";
  searching.value = false;
  page.value = 1;
  void load();
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

function onCardClick(item: DiscoverItem) {
  // 详情弹层在后续迭代接入；当前先提示。
  toast.info(`「${item.title}」详情页开发中`);
}

onMounted(() => {
  void loadMeta();
  void load();
});
</script>

<template>
  <div class="de">
    <!-- 工具栏：源切换 + 搜索 -->
    <div class="de__toolbar">
      <div class="de__source-switch">
        <button
          v-for="s in (['tmdb', 'douban'] as SourceTab[])"
          :key="s"
          type="button"
          class="de__source-btn"
          :class="{ 'de__source-btn--on': source === s && !searching }"
          @click="switchSource(s)"
        >
          {{ s === "tmdb" ? "TMDB" : "豆瓣" }}
        </button>
      </div>
      <form class="de__search" @submit.prevent="doSearch">
        <AppInput v-model="searchKeyword" placeholder="搜索电影 / 剧集 / 演员…" class="de__search-input" />
        <AppButton type="submit" variant="primary">搜索</AppButton>
        <AppButton v-if="searching" type="button" variant="secondary" @click="clearSearch">清除</AppButton>
      </form>
    </div>

    <!-- 筛选器 -->
    <div v-if="showFilters" class="de__filters">
      <AppSelect v-model="filters.type" :options="typeOptions" class="de__filter" @update:model-value="applyFilters" />
      <template v-if="source === 'tmdb'">
        <AppSelect v-model="filters.genre" :options="genreOptions" class="de__filter" @update:model-value="applyFilters" />
        <AppSelect v-model="filters.year" :options="yearOptions" class="de__filter" @update:model-value="applyFilters" />
        <AppSelect v-model="filters.sort_by" :options="sortOptions" class="de__filter" @update:model-value="applyFilters" />
      </template>
      <AppSelect
        v-else
        v-model="filters.doubanTag"
        :options="doubanTagOptions"
        class="de__filter"
        @update:model-value="applyFilters"
      />
    </div>

    <!-- 内容 -->
    <AppStateBlock v-if="loading" message="加载中…" loading min-height="300px" />
    <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="300px" />

    <div v-else-if="items.length" class="de__grid">
      <DiscoverPosterCard
        v-for="(item, idx) in items"
        :key="item.entity_key || item.tmdb_id || item.douban_id || idx"
        :item="item"
        :favorited="!!favoritedMap[entityKeyOf(item)]"
        @click="onCardClick"
        @favorite="toggleFavorite"
      />
    </div>

    <AppStateBlock v-else message="暂无内容，调整筛选或换个关键词试试" min-height="300px" />

    <!-- 分页 -->
    <div v-if="totalPages > 1 && items.length" class="de__pager">
      <AppButton type="button" variant="secondary" :disabled="page <= 1 || loading" @click="prevPage">上一页</AppButton>
      <span class="de__page-info">{{ page }} / {{ totalPages }}</span>
      <AppButton
        type="button"
        variant="secondary"
        :disabled="page >= totalPages || loading"
        @click="nextPage"
      >
        下一页
      </AppButton>
    </div>
  </div>
</template>

<style scoped>
.de__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 14px;
  margin-bottom: 14px;
}

.de__source-switch {
  display: inline-flex;
  border-radius: var(--radius-sm, 8px);
  background: var(--surface-sunken, #14161c);
  padding: 3px;
}

.de__source-btn {
  border: none;
  background: transparent;
  color: var(--text-muted, #6b7280);
  font-size: 13px;
  padding: 6px 14px;
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s ease, color 0.15s ease;
}

.de__source-btn--on {
  background: var(--brand, #e50914);
  color: #fff;
}

.de__search {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 240px;
}

.de__search-input {
  flex: 1;
  max-width: 380px;
}

.de__filters {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 16px;
}

.de__filter {
  min-width: 130px;
}

.de__grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 14px;
}

.de__pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 14px;
  margin-top: 20px;
}

.de__page-info {
  font-size: 13px;
  color: var(--text-muted, #6b7280);
}
</style>
