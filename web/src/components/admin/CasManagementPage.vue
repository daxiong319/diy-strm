<script setup lang="ts">
import { defineAsyncComponent } from "vue";
import AsyncErrorPanel from "@/components/common/AsyncErrorPanel.vue";

// CAS 秒传一级板块：清单管理 + 自动化配置 + 通知渠道 .cas 自动转存。
// 拆成两个懒加载块，避免首屏把列表与设置一起拉起来。
const CasManagePanel = defineAsyncComponent({
  loader: () => import("@/components/admin/CasManagePanel.vue"),
  errorComponent: AsyncErrorPanel,
});
const CasAutoSaveSettings = defineAsyncComponent({
  loader: () => import("@/components/admin/CasAutoSaveSettings.vue"),
  errorComponent: AsyncErrorPanel,
});
</script>

<template>
  <div class="cas-page">
    <CasManagePanel />
    <CasAutoSaveSettings />
  </div>
</template>

<style scoped>
.cas-page {
  display: flex;
  flex-direction: column;
}
</style>
