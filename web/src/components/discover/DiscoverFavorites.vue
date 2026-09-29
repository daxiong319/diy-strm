<script setup lang="ts">
import { onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  deleteDiscoverFavorite,
  fetchDiscoverFavorites,
  type DiscoveryFavorite,
} from "@/api/discovery";
import DiscoverPosterCard from "@/components/discover/DiscoverPosterCard.vue";
import DiscoverDetailModal, { type DetailTarget } from "@/components/discover/DiscoverDetailModal.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import { useConfirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";

const { showConfirm } = useConfirm();

const loading = ref(false);
const errorMsg = ref("");
const favorites = ref<DiscoveryFavorite[]>([]);

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    const res = await fetchDiscoverFavorites();
    favorites.value = res.items ?? [];
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载收藏失败");
    favorites.value = [];
  } finally {
    loading.value = false;
  }
}

function toItem(f: DiscoveryFavorite) {
  return {
    source: f.source,
    media_type: f.media_type,
    tmdb_id: f.tmdb_id,
    title: f.title,
    original_title: f.original_title,
    poster: f.poster,
    overview: f.overview,
    vote_avg: f.vote_avg,
    year: f.year,
    entity_key: f.entity_key,
  } as const;
}

async function handleDelete(f: DiscoveryFavorite) {
  if (!f.id) return;
  try {
    await showConfirm({
      title: "取消收藏",
      message: `确定取消收藏「${f.title}」吗？`,
      icon: "trash",
      confirmText: "取消收藏",
      danger: true,
    });
  } catch {
    return;
  }
  try {
    await deleteDiscoverFavorite(f.id);
    favorites.value = favorites.value.filter((x) => x.id !== f.id);
    toast.success("已取消收藏");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "取消收藏失败"));
  }
}

const detailOpen = ref(false);
const detailTarget = ref<DetailTarget | null>(null);

function onCardClick(f: DiscoveryFavorite) {
  const externalID = String(f.tmdb_id ?? f.external_id ?? "");
  if (!externalID) {
    toast.info("该条目暂不支持详情");
    return;
  }
  detailTarget.value = { source: f.source, media_type: f.media_type, external_id: externalID };
  detailOpen.value = true;
}

onMounted(load);
</script>

<template>
  <div class="df">
    <div class="df__toolbar">
      <p class="df__count">共 {{ favorites.length }} 个收藏</p>
      <AppButton type="button" variant="secondary" :disabled="loading" @click="load">刷新</AppButton>
    </div>

    <AppStateBlock v-if="loading" message="加载收藏中…" loading min-height="300px" />
    <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="300px" />

    <div v-else-if="favorites.length" class="df__grid">
      <div v-for="f in favorites" :key="f.id" class="df__cell">
        <DiscoverPosterCard :item="toItem(f)" favorited @click="() => onCardClick(f)" />
        <button type="button" class="df__remove" @click="handleDelete(f)">取消收藏</button>
      </div>
    </div>

    <AppStateBlock v-else message="还没有收藏，去「影视探索」页收藏感兴趣的影片吧" min-height="300px" />

    <DiscoverDetailModal :open="detailOpen" :target="detailTarget" @close="detailOpen = false" />
  </div>
</template>

<style scoped>
.df__toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.df__count {
  margin: 0;
  font-size: 13px;
  color: var(--text-muted, #6b7280);
}

.df__grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 14px;
}

.df__cell {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.df__remove {
  border: 1px solid var(--border, #2a2f3a);
  background: transparent;
  color: var(--text-muted, #6b7280);
  font-size: 12px;
  padding: 6px 0;
  border-radius: var(--radius-sm, 8px);
  cursor: pointer;
  transition: all 0.15s ease;
}

.df__remove:hover {
  border-color: var(--danger, #ef4444);
  color: var(--danger, #ef4444);
}
</style>
