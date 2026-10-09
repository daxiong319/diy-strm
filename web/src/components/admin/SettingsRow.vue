<script setup lang="ts">
import "@/styles/settings-panel.css";

withDefaults(
  defineProps<{
    changed?: boolean;
    showChangedBadge?: boolean;
    /**
     * 这行对应的设置 key。
     *
     * 有了它，行的 DOM id 就是 `setting-<key>` —— 与后端索引里给的
     * 锚点（internal/settings/index.go 的 Anchor 字段）同一个约定。
     * ⚠️ 这条约定只有一处定义、两处使用（后端算锚点、前端渲染 id）；
     * 两边各写一份时症状是「搜索能跳到页面却滚不到那一行」。
     */
    settingKey?: string;
  }>(),
  { showChangedBadge: false },
);
</script>

<template>
  <div
    class="settings-row"
    :class="{ 'settings-row--changed': changed }"
    :id="settingKey ? `setting-${settingKey}` : undefined"
    :data-setting-key="settingKey || undefined"
  >
    <div class="settings-row__info">
      <slot name="info" />
      <span v-if="changed && showChangedBadge" class="settings-row__badge">已修改</span>
    </div>
    <div v-if="$slots.control" class="settings-row__control">
      <slot name="control" />
    </div>
  </div>
</template>
