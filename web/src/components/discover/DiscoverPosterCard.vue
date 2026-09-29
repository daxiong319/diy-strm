<script setup lang="ts">
import { computed, ref } from "vue";
import type { DiscoverItem } from "@/api/discovery";

const props = withDefaults(
  defineProps<{
    item: DiscoverItem;
    favorited?: boolean;
    rank?: number | null;
    showSource?: boolean;
  }>(),
  { favorited: false, rank: null, showSource: true },
);

const emit = defineEmits<{
  click: [item: DiscoverItem];
  favorite: [item: DiscoverItem];
}>();

const imgFailed = ref(false);

const poster = computed(() => props.item.poster || props.item.backdrop || "");
const title = computed(() => props.item.title || props.item.original_title || "未命名");
const year = computed(() => props.item.year || (props.item.release_date ? Number(String(props.item.release_date).slice(0, 4)) : 0));
const vote = computed(() => (typeof props.item.vote_avg === "number" ? props.item.vote_avg.toFixed(1) : ""));
const sourceLabel = computed(() => {
  switch (props.item.source) {
    case "tmdb":
      return "TMDB";
    case "douban":
      return "豆瓣";
    case "bangumi":
      return "Bangumi";
    case "anilist":
      return "AniList";
    default:
      return props.item.source || "";
  }
});

function onImgError() {
  imgFailed.value = true;
}
</script>

<template>
  <div class="dpc" @click="emit('click', item)">
    <div class="dpc__poster">
      <img
        v-if="poster && !imgFailed"
        :src="poster"
        :alt="title"
        class="dpc__img"
        loading="lazy"
        @error="onImgError"
      />
      <div v-else class="dpc__placeholder">
        <span class="dpc__placeholder-text">{{ title.slice(0, 2) }}</span>
      </div>

      <span v-if="rank !== null && rank !== undefined" class="dpc__rank" :data-top="rank <= 3">{{ rank }}</span>
      <span v-if="vote" class="dpc__vote">{{ vote }}</span>
      <span v-if="showSource && sourceLabel" class="dpc__source">{{ sourceLabel }}</span>

      <button
        type="button"
        class="dpc__fav"
        :class="{ 'dpc__fav--on': favorited }"
        :title="favorited ? '取消收藏' : '收藏'"
        @click.stop="emit('favorite', item)"
      >
        <svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor">
          <path d="M12 21.35l-1.45-1.32C5.4 15.36 2 12.28 2 8.5 2 5.42 4.42 3 7.5 3c1.74 0 3.41.81 4.5 2.09C13.09 3.81 14.76 3 16.5 3 19.58 3 22 5.42 22 8.5c0 3.78-3.4 6.86-8.55 11.54L12 21.35z" />
        </svg>
      </button>

      <div class="dpc__overlay">
        <p v-if="item.overview" class="dpc__overview">{{ item.overview }}</p>
      </div>
    </div>
    <div class="dpc__meta">
      <p class="dpc__title" :title="title">{{ title }}</p>
      <p v-if="year || item.media_type" class="dpc__sub">
        <span v-if="year">{{ year }}</span>
        <span v-if="year && item.media_type"> · </span>
        <span v-if="item.media_type">{{ item.media_type === "tv" ? "剧集" : item.media_type === "movie" ? "电影" : item.media_type }}</span>
      </p>
    </div>
  </div>
</template>

<style scoped>
.dpc {
  display: flex;
  flex-direction: column;
  cursor: pointer;
  border-radius: var(--radius-md, 10px);
  overflow: hidden;
  background: var(--surface, #1c1f26);
  transition: transform 0.18s ease, box-shadow 0.18s ease;
}

.dpc:hover {
  transform: translateY(-3px);
  box-shadow: 0 8px 22px rgb(0 0 0 / 35%);
}

.dpc__poster {
  position: relative;
  aspect-ratio: 2 / 3;
  background: var(--surface-sunken, #14161c);
  overflow: hidden;
}

.dpc__img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

.dpc__placeholder {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, #232733, #14161c);
}

.dpc__placeholder-text {
  font-size: 28px;
  font-weight: 700;
  color: var(--text-muted, #6b7280);
}

.dpc__rank {
  position: absolute;
  top: 8px;
  left: 8px;
  min-width: 22px;
  height: 22px;
  padding: 0 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  background: rgb(0 0 0 / 65%);
  color: #fff;
  font-size: 12px;
  font-weight: 700;
}

.dpc__rank[data-top="true"] {
  background: var(--brand, #e50914);
}

.dpc__vote {
  position: absolute;
  bottom: 8px;
  left: 8px;
  padding: 2px 6px;
  border-radius: 6px;
  background: rgb(0 0 0 / 65%);
  color: #fbbf24;
  font-size: 12px;
  font-weight: 700;
}

.dpc__source {
  position: absolute;
  top: 8px;
  right: 8px;
  padding: 2px 6px;
  border-radius: 6px;
  background: rgb(0 0 0 / 55%);
  color: #d1d5db;
  font-size: 11px;
}

.dpc__fav {
  position: absolute;
  bottom: 8px;
  right: 8px;
  width: 30px;
  height: 30px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: none;
  border-radius: 50%;
  background: rgb(0 0 0 / 55%);
  color: #9ca3af;
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.15s ease, color 0.15s ease, background 0.15s ease;
}

.dpc:hover .dpc__fav {
  opacity: 1;
}

.dpc__fav--on {
  opacity: 1;
  color: #f472b6;
  background: rgb(0 0 0 / 70%);
}

.dpc__fav:hover {
  background: var(--brand, #e50914);
  color: #fff;
}

.dpc__overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: flex-end;
  padding: 10px;
  background: linear-gradient(to top, rgb(0 0 0 / 82%), transparent 55%);
  opacity: 0;
  transition: opacity 0.18s ease;
  pointer-events: none;
}

.dpc:hover .dpc__overlay {
  opacity: 1;
}

.dpc__overview {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: #e5e7eb;
  display: -webkit-box;
  -webkit-line-clamp: 4;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.dpc__meta {
  padding: 8px 10px 10px;
}

.dpc__title {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--text, #e5e7eb);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.dpc__sub {
  margin: 3px 0 0;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}
</style>
