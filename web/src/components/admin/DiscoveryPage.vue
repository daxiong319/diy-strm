<script setup lang="ts">
import { defineAsyncComponent, type Component } from "vue";
import SectionTabBar from "@/components/admin/SectionTabBar.vue";
import AsyncErrorPanel from "@/components/common/AsyncErrorPanel.vue";
import { useSectionTabRoute } from "@/composables/useSectionTabRoute";

// 分区：影视探索（含番剧/演员子入口）/ 榜单推荐 / 追剧日历 / 我的收藏 /
// 资源搜索 / 订阅管理 / 频道管理 / 监控历史 / 缺集补档。
// 懒加载避免首屏拉取全部大组件；加载失败时落到错误兜底，避免整块静默空白。
const asyncPanel = (loader: () => Promise<Component>) =>
  defineAsyncComponent({ loader, errorComponent: AsyncErrorPanel });
const DiscoverExplore = asyncPanel(() => import("@/components/discover/DiscoverExplore.vue"));
const DiscoverRankings = asyncPanel(() => import("@/components/discover/DiscoverRankings.vue"));
const DiscoverCalendar = asyncPanel(() => import("@/components/discover/DiscoverCalendar.vue"));
const DiscoverFavorites = asyncPanel(() => import("@/components/discover/DiscoverFavorites.vue"));
const DiscoverResourceSearch = asyncPanel(() => import("@/components/admin/DiscoverResourceSearch.vue"));
const SubscriptionManagement = asyncPanel(() => import("@/components/admin/SubscriptionManagement.vue"));
const ChannelManagement = asyncPanel(() => import("@/components/admin/ChannelManagement.vue"));
const MonitorHistoryPanel = asyncPanel(() => import("@/components/admin/MonitorHistoryPanel.vue"));
const EmbyMissingPanel = asyncPanel(() => import("@/components/admin/EmbyMissingPanel.vue"));
const RssSubscriptionPanel = asyncPanel(() => import("@/components/admin/RssSubscriptionPanel.vue"));

const EXPLORE_TAB = "explore";
const RANKINGS_TAB = "rankings";
const CALENDAR_TAB = "calendar";
const FAVORITES_TAB = "favorites";
const RESOURCES_TAB = "resources";
const SUBSCRIPTIONS_TAB = "subscriptions";
const CHANNELS_TAB = "channels";
const MONITOR_TAB = "monitor";
const EMBY_MISSING_TAB = "emby-missing";
const RSS_TAB = "rss";

const tabs = [
  { key: EXPLORE_TAB, label: "影视探索" },
  { key: RANKINGS_TAB, label: "榜单推荐" },
  { key: CALENDAR_TAB, label: "追剧日历" },
  { key: FAVORITES_TAB, label: "我的收藏" },
  { key: RESOURCES_TAB, label: "资源搜索" },
  { key: SUBSCRIPTIONS_TAB, label: "订阅管理" },
  { key: CHANNELS_TAB, label: "频道管理" },
  { key: RSS_TAB, label: "RSS 订阅" },
  { key: MONITOR_TAB, label: "监控历史" },
  { key: EMBY_MISSING_TAB, label: "缺集补档" },
];

const { activeTab, setActiveTab } = useSectionTabRoute(EXPLORE_TAB, [
  EXPLORE_TAB,
  RANKINGS_TAB,
  CALENDAR_TAB,
  FAVORITES_TAB,
  RESOURCES_TAB,
  SUBSCRIPTIONS_TAB,
  CHANNELS_TAB,
  MONITOR_TAB,
  EMBY_MISSING_TAB,
  RSS_TAB,
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
    <div v-show="activeTab === RESOURCES_TAB" class="dp-pane">
      <DiscoverResourceSearch />
    </div>
    <div v-show="activeTab === SUBSCRIPTIONS_TAB" class="dp-pane">
      <SubscriptionManagement />
    </div>
    <div v-show="activeTab === CHANNELS_TAB" class="dp-pane">
      <ChannelManagement />
    </div>
    <div v-show="activeTab === MONITOR_TAB" class="dp-pane">
      <MonitorHistoryPanel />
    </div>
    <div v-show="activeTab === EMBY_MISSING_TAB" class="dp-pane">
      <EmbyMissingPanel />
    </div>
    <div v-show="activeTab === RSS_TAB" class="dp-pane">
      <RssSubscriptionPanel />
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
