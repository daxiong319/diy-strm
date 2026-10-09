<script setup lang="ts">
/**
 * 全局「功能直达」面板（⌘K / Ctrl+K）。
 *
 * 状态全部在 web/src/composables/useCommandPalette.ts（模块级单例），
 * 这个组件只负责渲染与键盘交互 —— 面板可以被顶栏按钮、快捷键、
 * 以及任何页面唤起，不该各自持有一份。
 */
import { computed, nextTick, ref, watch } from "vue";
import AppModal from "@/components/base/AppModal.vue";
import type { SettingIndexEntry } from "@/api/settings";
import { useCommandPalette } from "@/composables/useCommandPalette";

const {
  paletteOpen,
  paletteQuery,
  cursor,
  results,
  loadError,
  coverage,
  closePalette,
  movePaletteCursor,
  activatePaletteItem,
  paletteLastQuery,
} = useCommandPalette();

const listEl = ref<HTMLElement | null>(null);
/** 没能跳转时给一句提示，而不是面板无声地合上。 */
const jumpFailed = ref("");

/** 换一批结果时光标回到第一条。 */
watch(paletteQuery, () => {
  cursor.value = 0;
  jumpFailed.value = "";
});

const cursorItem = computed<SettingIndexEntry | undefined>(() => results.value[cursor.value]);

async function onPick(item: SettingIndexEntry | undefined) {
  if (!item) return;
  const ok = await activatePaletteItem(item);
  if (!ok) {
    jumpFailed.value = "这条设置还没有可直达的页面，只能看看说明";
  }
}

async function onKeydown(e: KeyboardEvent) {
  switch (e.key) {
    case "ArrowDown":
      e.preventDefault();
      movePaletteCursor(1);
      break;
    case "ArrowUp":
      e.preventDefault();
      movePaletteCursor(-1);
      break;
    case "Enter":
      e.preventDefault();
      await onPick(cursorItem.value);
      break;
    case "Escape":
      e.preventDefault();
      closePalette();
      break;
  }
}

/** 光标移动后把选中项滚进视野。 */
watch(cursor, async () => {
  await nextTick();
  listEl.value?.querySelector(".palette__item--active")?.scrollIntoView({ block: "nearest" });
});

/** 打开时清掉上一次的失败提示。 */
watch(paletteOpen, (open) => {
  if (open) jumpFailed.value = "";
});
</script>

<template>
  <AppModal
    :open="paletteOpen"
    title="功能直达"
    size="md"
    bare
    @close="closePalette"
  >
    <div class="palette" @keydown="onKeydown">
      <input
        v-model="paletteQuery"
        class="palette__input"
        type="text"
        placeholder="搜设置项，例如「缓存条数」「保留天数」「cas」"
        autofocus
      />

      <p v-if="loadError" class="palette__error">
        索引没能载入：{{ loadError }}
      </p>

      <template v-else-if="results.length">
        <ul ref="listEl" class="palette__list" role="listbox">
          <li
            v-for="(it, i) in results"
            :key="it.key"
            class="palette__item"
            :class="{ 'palette__item--active': i === cursor }"
            role="option"
            :aria-selected="i === cursor"
            @mouseenter="cursor = i"
            @click="onPick(it)"
          >
            <span class="palette__item-main">
              <span class="palette__item-title">{{ it.label || it.key }}</span>
              <span class="palette__item-cat">{{ it.category_label }}</span>
            </span>
            <span v-if="it.description" class="palette__item-desc">{{ it.description }}</span>
          </li>
        </ul>
      </template>

      <p v-else class="palette__empty">
        没有匹配的设置项。<template v-if="paletteLastQuery()">上次搜的是「{{ paletteLastQuery() }}」。</template>
      </p>

      <p v-if="jumpFailed" class="palette__error">{{ jumpFailed }}</p>
      <p v-if="coverage" class="palette__coverage">{{ coverage }}</p>
    </div>
  </AppModal>
</template>

<style scoped>
.palette {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 220px;
}

.palette__input {
  width: 100%;
  padding: 10px 12px;
  font-size: 14px;
  border: 1px solid var(--border-soft, rgba(15, 23, 42, 0.14));
  border-radius: var(--radius-sm, 8px);
  background: var(--surface-sunken, #f8fafc);
  color: inherit;
}

.palette__input:focus {
  outline: none;
  border-color: var(--brand, #3b82f6);
}

.palette__list {
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 46vh;
  overflow-y: auto;
}

.palette__item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px 10px;
  border-radius: var(--radius-sm, 8px);
  cursor: pointer;
}

.palette__item--active {
  background: color-mix(in srgb, var(--brand, #3b82f6) 10%, transparent);
}

.palette__item-main {
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.palette__item-title {
  font-size: 13px;
  font-weight: 600;
}

.palette__item-cat {
  font-size: 11px;
  color: var(--text-muted, #64748b);
}

.palette__item-desc {
  font-size: 12px;
  color: var(--text-muted, #64748b);
  line-height: 1.5;
  /* 说明可能很长，限两行：面板高度有限，全展开会把结果挤到看不见。 */
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.palette__empty,
.palette__error {
  margin: 0;
  font-size: 13px;
  color: var(--text-muted, #64748b);
}

.palette__error {
  color: var(--danger, #dc2626);
}

.palette__coverage {
  margin: 0;
  font-size: 11px;
  color: var(--text-muted, #64748b);
  line-height: 1.6;
}
</style>