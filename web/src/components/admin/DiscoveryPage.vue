<script setup lang="ts">
import { defineAsyncComponent } from "vue";
import SectionTabBar from "@/components/admin/SectionTabBar.vue";
import { useSectionTabRoute } from "@/composables/useSectionTabRoute";

// 四个互斥分区：影视探索（含番剧/演员/收藏子入口）/ 榜单推荐 / 追剧日历 / 基础配置。
// 懒加载避免首屏拉取全部大组件。
const DiscoverExplore = defineAsyncComponent(() => import("@/components/discover/DiscoverExplore.vue"));
const DiscoverRankings = defineAsyncComponent(() => import("@/components/discover/DiscoverRankings.vue"));
const DiscoverCalendar = defineAsyncComponent(() => import("@/components/discover/DiscoverCalendar.vue"));
const DiscoverFavorites = defineAsyncComponent(() => import("@/components/discover/DiscoverFavorites.vue"));

const EXPLORE_TAB = "explore";
const RANKINGS_TAB = "rankings";
const CALENDAR_TAB = "calendar";
const FAVORITES_TAB = "favorites";

const tabs = [
  { key: EXPLORE_TAB, label: "影视探索" },
  { key: RANKINGS_TAB, label: "榜单推荐" },
  { key: CALENDAR_TAB, label: "追剧日历" },
  { key: FAVORITES_TAB, label: "我的收藏" },
];

const { activeTab, setActiveTab } = useSectionTabRoute(EXPLORE_TAB, [
  EXPLORE_TAB,
  RANKINGS_TAB,
  CALENDAR_TAB,
  FAVORITES_TAB,
]);
</script>

<template>
  <div class="discovery-page">
    <SectionTabBar :tabs="tabs" :model-value="activeTab" @update:model-value="setActiveTab" />
    <div v-show="activeTab === EXPLORE_TAB" class="dp-pane">
      <DiscoverExplore />
    </div>
    <div v-show="activeTab === RANKINGS_TAB" class="dp-pane">
      <DiscoverRankings />
    </div>
    <div v-show="activeTab === CALENDAR_TAB" class="dp-pane">
      <DiscoverCalendar />
    </div>
    <div v-show="activeTab === FAVORITES_TAB" class="dp-pane">
      <DiscoverFavorites />
    </div>
  </div>
</template>

<style scoped>
.discovery-page {
  padding-bottom: 24px;
}

.dp-pane {
  margin-top: 4px;
}
</style>
