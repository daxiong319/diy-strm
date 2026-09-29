<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  fetchDiscoverAnimeCalendar,
  fetchDiscoverCalendar,
  type CalendarDay,
  type CalendarEpisode,
} from "@/api/discovery";
import AppButton from "@/components/base/AppButton.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import { toast } from "@/composables/useToast";

const loading = ref(false);
const errorMsg = ref("");
const days = ref<CalendarDay[]>([]);
const animeMode = ref(false);

const kindOptions = [
  { value: "all", label: "全部" },
  { value: "tv", label: "剧集" },
  { value: "movie", label: "电影" },
  { value: "upcoming", label: "即将上映" },
  { value: "on-air", label: "正在播出" },
  { value: "airing-today", label: "今日播出" },
];
const kind = ref("all");

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    if (animeMode.value) {
      days.value = await fetchDiscoverAnimeCalendar({});
    } else {
      days.value = await fetchDiscoverCalendar({ days: 14, kind: kind.value });
    }
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载日历失败");
    days.value = [];
  } finally {
    loading.value = false;
  }
}

function switchMode(anime: boolean) {
  animeMode.value = anime;
  void load();
}

function applyKind() {
  void load();
}

function episodesOf(day: CalendarDay): CalendarEpisode[] {
  return day.episodes ?? (day.items as CalendarEpisode[] | undefined) ?? [];
}

function weekdayOf(dateStr: string): string {
  const d = new Date(dateStr);
  if (Number.isNaN(d.getTime())) return "";
  return ["日", "一", "二", "三", "四", "五", "六"][d.getDay()];
}

function formatDate(dateStr: string): string {
  const d = new Date(dateStr);
  if (Number.isNaN(d.getTime())) return dateStr;
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

function onEpisodeClick(ep: CalendarEpisode) {
  toast.info(`「${ep.show_title || ep.title}」详情页开发中`);
}

const hasAny = computed(() => days.value.some((d) => episodesOf(d).length > 0));

onMounted(load);
</script>

<template>
  <div class="dc">
    <div class="dc__toolbar">
      <div class="dc__mode-switch">
        <button
          type="button"
          class="dc__mode-btn"
          :class="{ 'dc__mode-btn--on': !animeMode }"
          @click="switchMode(false)"
        >
          追剧日历
        </button>
        <button
          type="button"
          class="dc__mode-btn"
          :class="{ 'dc__mode-btn--on': animeMode }"
          @click="switchMode(true)"
        >
          番剧放送
        </button>
      </div>
      <div v-if="!animeMode" class="dc__kind-switch">
        <button
          v-for="k in kindOptions"
          :key="k.value"
          type="button"
          class="dc__kind-btn"
          :class="{ 'dc__kind-btn--on': kind === k.value }"
          @click="kind = k.value; applyKind()"
        >
          {{ k.label }}
        </button>
      </div>
      <AppButton type="button" variant="secondary" :disabled="loading" @click="load">刷新</AppButton>
    </div>

    <AppStateBlock v-if="loading" message="加载日历中…" loading min-height="300px" />
    <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="300px" />

    <div v-else-if="hasAny" class="dc__list">
      <section v-for="day in days" :key="day.date" class="dc__day">
        <header class="dc__day-header">
          <span class="dc__day-date">{{ formatDate(day.date) }}</span>
          <span class="dc__day-week">周{{ weekdayOf(day.date) }}</span>
          <span v-if="day.label" class="dc__day-label">{{ day.label }}</span>
        </header>
        <div class="dc__episodes">
          <article
            v-for="(ep, idx) in episodesOf(day)"
            :key="ep.entity_key || `${day.date}-${idx}`"
            class="dc__episode"
            @click="onEpisodeClick(ep)"
          >
            <img
              v-if="ep.still || ep.poster"
              :src="ep.still || ep.poster"
              :alt="ep.show_title || ep.title"
              class="dc__episode-img"
              loading="lazy"
            />
            <div class="dc__episode-body">
              <p class="dc__episode-title">{{ ep.show_title || ep.title }}</p>
              <p v-if="ep.season || ep.episode" class="dc__episode-ep">
                {{ ep.season ? `第 ${ep.season} 季` : "" }}
                {{ ep.season && ep.episode ? " · " : "" }}
                {{ ep.episode ? `第 ${ep.episode} 集` : "" }}
              </p>
              <p v-if="ep.overview" class="dc__episode-overview">{{ ep.overview }}</p>
            </div>
          </article>
        </div>
      </section>
    </div>

    <AppStateBlock v-else message="近期没有播出内容" min-height="300px" />
  </div>
</template>

<style scoped>
.dc__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 14px;
  margin-bottom: 16px;
}

.dc__mode-switch {
  display: inline-flex;
  border-radius: var(--radius-sm, 8px);
  background: var(--surface-sunken, #14161c);
  padding: 3px;
}

.dc__mode-btn {
  border: none;
  background: transparent;
  color: var(--text-muted, #6b7280);
  font-size: 13px;
  padding: 6px 14px;
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s ease, color 0.15s ease;
}

.dc__mode-btn--on {
  background: var(--brand, #e50914);
  color: #fff;
}

.dc__kind-switch {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.dc__kind-btn {
  border: 1px solid var(--border, #2a2f3a);
  background: transparent;
  color: var(--text-muted, #6b7280);
  font-size: 12px;
  padding: 5px 11px;
  border-radius: var(--radius-pill, 999px);
  cursor: pointer;
  transition: all 0.15s ease;
}

.dc__kind-btn--on {
  background: var(--brand, #e50914);
  border-color: var(--brand, #e50914);
  color: #fff;
}

.dc__list {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.dc__day-header {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 10px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border-soft, #232733);
}

.dc__day-date {
  font-size: 16px;
  font-weight: 700;
  color: var(--text, #e5e7eb);
}

.dc__day-week {
  font-size: 13px;
  color: var(--text-muted, #6b7280);
}

.dc__day-label {
  font-size: 12px;
  color: var(--brand, #e50914);
}

.dc__episodes {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 10px;
}

.dc__episode {
  display: flex;
  gap: 12px;
  padding: 10px;
  border-radius: var(--radius-md, 10px);
  background: var(--surface, #1c1f26);
  cursor: pointer;
  transition: background 0.15s ease;
}

.dc__episode:hover {
  background: var(--surface-sunken, #14161c);
}

.dc__episode-img {
  width: 110px;
  height: 64px;
  object-fit: cover;
  border-radius: 6px;
  flex-shrink: 0;
  background: var(--surface-sunken, #14161c);
}

.dc__episode-body {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.dc__episode-title {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--text, #e5e7eb);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.dc__episode-ep {
  margin: 0;
  font-size: 12px;
  color: var(--brand, #e50914);
}

.dc__episode-overview {
  margin: 0;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
</style>
