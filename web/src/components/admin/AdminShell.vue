<script setup lang="ts">
import SvgIcon from "@/components/icons/SvgIcon.vue";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import AdminAccountChip from "@/components/admin/AdminAccountChip.vue";
import AdminGlobalActions from "@/components/admin/AdminGlobalActions.vue";
import AdminNavIcon from "@/components/admin/AdminNavIcon.vue";
import { useAdminLoadingBar } from "@/composables/useAdminLoadingBar";
import { installPaletteHotkey, openPalette } from "@/composables/useCommandPalette";
import { canHide } from "@/composables/layoutPrefs";

/** ⌘K 快捷键的注销函数；onBeforeUnmount 里要用。 */
let removePaletteHotkey: (() => void) | null = null;

interface NavItem {
  key: string;
  label: string;
  icon: string;
}

const SIDEBAR_COLLAPSED_KEY = "litepan-admin-sidebar-collapsed";
const MOBILE_BREAKPOINT = 768;
// 手机底栏固定显示前三项：管理这台机器的最小闭环（看状态 / 管账号 / 改设置）。
// 取编排后的前三而不是写死三项 —— 用户把某个菜单拖到第一位，底栏就该跟着变。
// 写死前三会让「底栏跟随编排」看起来只在某一种顺序下成立。
const MOBILE_DOCK_SIZE = 3;

const props = withDefaults(
  defineProps<{
    nav: NavItem[];
    modelValue: string;
    pageTitle?: string;
    crumbs?: Array<{ label: string; to?: { page: string; tab?: string } }>;
    lockedKeys?: string[];
    homeReturnMode?: "sidebar" | "top_icon";
    /** 编排态：显示拖拽手柄与「隐藏」入口（父组件持有真实偏好）。 */
    editing?: boolean;
    /** 被藏起来的菜单，编排面板里可以拖回来。 */
    hiddenItems?: NavItem[];
  }>(),
  { homeReturnMode: "top_icon", editing: false },
);
const emit = defineEmits<{
  "update:modelValue": [string];
  preload: [string];
  logout: [];
  goHome: [];
  navigate: [{ page: string; tab?: string }];
  /** 编排操作：重排 / 隐藏 / 放回 / 恢复默认。 */
  reorder: [{ from: number; to: number }];
  hide: [string];
  unhide: [string];
  resetLayout: [];
  "update:editing": [boolean];
}>();

const editing = ref(props.editing);
watch(
  () => props.editing,
  (v) => {
    editing.value = v;
  },
);
function toggleEditing() {
  editing.value = !editing.value;
  emit("update:editing", editing.value);
}

// 拖拽：用 pointerdown/pointerup 而不是 HTML5 drag-and-drop。
// 后者在触摸设备上完全不可用，而「手机底栏跟随编排」意味着这个功能
// 必须能在手机上用 —— 否则编排在手机上是个只能看不能改的摆设。
const dragFrom = ref<number | null>(null);
function onItemPointerDown(index: number, ev: PointerEvent) {
  if (!editing.value || dragFrom.value !== null) return;
  dragFrom.value = index;
  (ev.currentTarget as HTMLElement)?.setPointerCapture?.(ev.pointerId);
}
function onItemPointerEnter(index: number) {
  // 悬停即预载对应页面组件（原来的 @pointerenter 行为）。
  // 合并到这一个处理器里，是因为同一个元素上写两个 @pointerenter
  // 会被编译期判成重复属性（vue-tsc 不报，vite build 才炸）。
  emit("preload", props.nav[index]?.key);
  if (!editing.value || dragFrom.value === null) return;
  if (dragFrom.value === index) return;
  emit("reorder", { from: dragFrom.value, to: index });
  // 拖动源跟着目标走：否则一次手势只能挪一格，跨几位要拖十几次。
  dragFrom.value = index;
}
function endDrag() {
  dragFrom.value = null;
}

const sidebarCollapsed = ref(false);
const mobileDrawerOpen = ref(false);
const isMobile = ref(false);
const { visible: pageLoadingVisible } = useAdminLoadingBar();

const sidebarCompact = computed(() => !isMobile.value && sidebarCollapsed.value);

// 手机底栏：取编排后的前三。编排改一次，这里跟着变，不需要额外同步。
const mobileDock = computed(() => props.nav.slice(0, MOBILE_DOCK_SIZE));

const sidebarToggleLabel = computed(() => {
  if (isMobile.value) return mobileDrawerOpen.value ? "关闭菜单" : "打开菜单";
  return sidebarCollapsed.value ? "展开侧栏" : "收起侧栏";
});

function readCollapsedPref() {
  try {
    sidebarCollapsed.value = localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === "1";
  } catch {
    sidebarCollapsed.value = false;
  }
}

function persistCollapsedPref() {
  try {
    localStorage.setItem(SIDEBAR_COLLAPSED_KEY, sidebarCollapsed.value ? "1" : "0");
  } catch {}
}

function syncViewport() {
  isMobile.value = window.innerWidth <= MOBILE_BREAKPOINT;
  if (!isMobile.value) mobileDrawerOpen.value = false;
}

function syncSidebarWidthVar() {
  const width = isMobile.value ? "0px" : sidebarCollapsed.value ? "64px" : "220px";
  document.documentElement.style.setProperty("--sidebar-width", width);
}

function toggleSidebar() {
  if (isMobile.value) {
    mobileDrawerOpen.value = !mobileDrawerOpen.value;
    return;
  }
  sidebarCollapsed.value = !sidebarCollapsed.value;
  persistCollapsedPref();
}

function closeMobileDrawer() {
  mobileDrawerOpen.value = false;
}

function selectNav(key: string) {
  emit("update:modelValue", key);
  closeMobileDrawer();
}

function goHomeFromSidebar() {
  emit("goHome");
  closeMobileDrawer();
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape" && mobileDrawerOpen.value) closeMobileDrawer();
}

onMounted(() => {
  readCollapsedPref();
  syncViewport();
  syncSidebarWidthVar();
  window.addEventListener("resize", syncViewport);
  window.addEventListener("keydown", onKeydown);
  // ⌘K 装在 capture 阶段：面板可能被某个局部 stopPropagation 的组件盖住，
  // 冒泡阶段就收不到了 —— 而「搜索框失焦后 ⌘K 唤不回来」正是
  // 用户第一次遇到就会放弃的故障。
  removePaletteHotkey = installPaletteHotkey();
});

watch([sidebarCollapsed, isMobile], () => {
  syncSidebarWidthVar();
});

watch(mobileDrawerOpen, (open) => {
  if (typeof document === "undefined") return;
  document.body.style.overflow = open && isMobile.value ? "hidden" : "";
});

onBeforeUnmount(() => {
  window.removeEventListener("resize", syncViewport);
  window.removeEventListener("keydown", onKeydown);
  removePaletteHotkey?.();
  document.body.style.overflow = "";
  document.documentElement.style.removeProperty("--sidebar-width");
});
</script>

<template>
  <div
    class="admin"
    :class="{
      'admin--collapsed': sidebarCompact,
      'admin--drawer-open': isMobile && mobileDrawerOpen,
      'admin--mobile': isMobile,
    }"
  >
    <div
      v-if="isMobile"
      class="sidebar-backdrop"
      :class="{ 'sidebar-backdrop--visible': mobileDrawerOpen }"
      aria-hidden="true"
      @click="closeMobileDrawer"
    />

    <aside class="sidebar">
      <header class="sidebar__header">
        <!-- 点击 logo 展开/收缩侧栏（桌面端）；hover 显示自定义 tooltip 提示 -->
        <span
          class="sidebar__logo-wrap"
          :class="{ 'sidebar__logo-wrap--clickable': !isMobile }"
          @click="!isMobile && toggleSidebar()"
        >
          <img
            :src="sidebarCompact ? '/static/img/logo-l.png' : '/static/img/logo.png'"
            alt="diy-strm"
            class="sidebar__logo"
          />
          <span v-if="!isMobile" class="sidebar-logo-tip" role="tooltip">
            {{ sidebarCollapsed ? "展开侧栏" : "收缩侧栏" }}
          </span>
        </span>
      </header>

      <nav
        class="sidebar__nav"
        @pointerup="endDrag"
        @pointercancel="endDrag"
        @pointerleave="endDrag"
      >
        <div v-for="(item, i) in nav" :key="item.key" class="nav-slot">
          <button
            class="nav-item"
            :class="{
              'nav-item--active': item.key === modelValue,
              'nav-item--locked': lockedKeys?.includes(item.key),
              'nav-item--dragging': editing && dragFrom === i,
            }"
            :disabled="lockedKeys?.includes(item.key)"
            @focus="emit('preload', item.key)"
            @pointerdown="onItemPointerDown(i, $event)"
            @pointerenter="onItemPointerEnter(i)"
            @pointerup="endDrag"
            @click="selectNav(item.key)"
          >
            <AdminNavIcon :name="item.icon" class="nav-item__icon" />
            <span class="nav-item__label">{{ item.label }}</span>
          </button>
          <button
            v-if="editing && !sidebarCompact"
            type="button"
            class="nav-item-hide"
            :disabled="!canHide(item.key)"
            :title="canHide(item.key) ? `隐藏「${item.label}」` : '该入口不可隐藏'"
            @click.stop="emit('hide', item.key)"
          >
            <span aria-hidden="true">×</span>
            <span class="nav-item-hide__text">{{ canHide(item.key) ? "隐藏" : "锁定" }}</span>
          </button>
        </div>
        <button
          v-if="homeReturnMode === 'sidebar'"
          type="button"
          class="nav-item nav-item--home"
          @click="goHomeFromSidebar"
        >
          <AdminNavIcon name="home" class="nav-item__icon" />
          <span class="nav-item__label">返回首页</span>
        </button>
      </nav>

      <footer class="sidebar__footer">
        <AdminAccountChip :compact="sidebarCompact" @logout="emit('logout')" />
      </footer>
    </aside>

    <!-- 编排面板：只在编排态出现 -->
    <div v-if="editing" class="nav-editor">
      <div class="nav-editor__head">
        <span class="nav-editor__title">编排菜单</span>
        <div class="nav-editor__actions">
          <button type="button" class="nav-editor__btn" @click="emit('resetLayout')">恢复默认</button>
          <button type="button" class="nav-editor__btn" @click="toggleEditing">完成</button>
        </div>
      </div>
      <p class="nav-editor__hint">拖动菜单调整顺序。手机底栏固定跟随排序后的前三项。</p>
      <ul v-if="hiddenItems?.length" class="nav-editor__hidden">
        <li v-for="item in hiddenItems" :key="item.key">
          <span>{{ item.label }}</span>
          <button type="button" class="nav-editor__btn" @click="emit('unhide', item.key)">放回</button>
        </li>
      </ul>
      <p v-else class="nav-editor__empty">当前没有隐藏的菜单。</p>
    </div>

    <header class="global-chrome">
      <!-- 移动端：汉堡按钮打开抽屉（桌面端收缩入口移到侧栏边缘按钮） -->
      <button
        v-if="isMobile"
        type="button"
        class="sidebar-toggle"
        :class="{ 'sidebar-toggle--active': mobileDrawerOpen }"
        :aria-label="sidebarToggleLabel"
        :aria-expanded="mobileDrawerOpen"
        @click="toggleSidebar"
      >
        <SvgIcon :name="mobileDrawerOpen ? 'hand-close' : 'hand-menu'" :size="18" />
      </button>

      <!-- 真面包屑：后台 / 页面 / 当前 tab（可点击项跳转，当前项高亮） -->
      <div v-if="crumbs?.length" class="global-chrome__context global-chrome__context--crumbs">
        <template v-for="(crumb, i) in crumbs" :key="`${crumb.label}-${i}`">
          <button
            v-if="crumb.to"
            type="button"
            class="global-chrome__crumb-link"
            @click="emit('navigate', crumb.to)"
          >
            {{ crumb.label }}
          </button>
          <span v-else class="global-chrome__crumb-current">{{ crumb.label }}</span>
          <span v-if="i < crumbs.length - 1" class="global-chrome__sep">/</span>
        </template>
      </div>
      <div v-else-if="pageTitle" class="global-chrome__context">
        <span class="global-chrome__crumb">后台</span>
        <span class="global-chrome__sep">/</span>
        <span class="global-chrome__title">{{ pageTitle }}</span>
      </div>
      <div class="global-chrome__spacer" />
      <button
        type="button"
        class="global-chrome__edit"
        :aria-pressed="editing"
        title="编排菜单"
        @click="toggleEditing"
      >
        <SvgIcon name="sliders-h" :size="16" />
        <span class="global-chrome__edit-text">编排</span>
      </button>
      <button
        type="button"
        class="global-chrome__search"
        title="功能直达（Ctrl/⌘ + K）"
        @click="openPalette()"
      >
        <SvgIcon name="search" :size="16" />
        <span class="global-chrome__search-text">功能直达</span>
        <kbd class="global-chrome__kbd">⌘K</kbd>
      </button>
      <AdminGlobalActions
        :show-home-return="homeReturnMode === 'top_icon'"
        @go-home="emit('goHome')"
      />
      <Transition name="admin-loading-bar">
        <div v-if="pageLoadingVisible" class="global-loading-bar" aria-hidden="true">
          <span />
        </div>
      </Transition>
    </header>

    <main class="admin__body">
      <slot />
    </main>

    <!-- 手机底栏：编排后的前三项 -->
    <nav v-if="isMobile" class="mobile-dock" aria-label="快捷导航">
      <button
        v-for="item in mobileDock"
        :key="item.key"
        type="button"
        class="mobile-dock__item"
        :class="{ 'mobile-dock__item--active': item.key === modelValue }"
        :aria-current="item.key === modelValue ? 'page' : undefined"
        @pointerenter="emit('preload', item.key)"
        @focus="emit('preload', item.key)"
        @click="selectNav(item.key)"
      >
        <AdminNavIcon :name="item.icon" />
        <span class="mobile-dock__label">{{ item.label }}</span>
      </button>
    </nav>
  </div>
</template>

<style scoped>
.admin {
  --admin-chrome-h: 44px;
  --sidebar-width: 220px;
  display: grid;
  grid-template-columns: var(--sidebar-width) minmax(0, 1fr);
  grid-template-rows: var(--admin-chrome-h) minmax(0, 1fr);
  height: 100vh;
  overflow: hidden;
  background: var(--bg);
}

.admin--collapsed {
  --sidebar-width: 64px;
}

.admin--mobile {
  --sidebar-width: 0px;
  grid-template-columns: minmax(0, 1fr);
}

.sidebar-backdrop {
  display: none;
}

.sidebar {
  grid-column: 1;
  grid-row: 1 / -1;
  z-index: 120;
  position: relative;
  min-height: 0;
  display: flex;
  flex-direction: column;
  background: var(--admin-sidebar-bg);
  border-right: 1px solid var(--admin-sidebar-border);
  box-shadow: var(--admin-sidebar-shadow);
  color: #fff;
  border-top-right-radius: var(--radius-lg);
  transition: transform 0.28s ease, box-shadow 0.28s ease;
}

/* 点击 logo 展开/收缩侧栏（桌面端）；hover 显示自定义 tooltip */
.sidebar__header {
  position: relative;
}

.sidebar__logo-wrap {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  width: 100%;
}

.sidebar__logo-wrap--clickable {
  cursor: pointer;
}

/* 自定义 tooltip：黑底白字，浮在 logo 下方、分割线上方（header 内），深浅色主题通用 */
.sidebar-logo-tip {
  position: absolute;
  top: calc(50% + 28px);
  left: 50%;
  transform: translateX(-50%);
  z-index: 220;
  padding: 3px 9px;
  border-radius: var(--radius-xs);
  background: #18181b;
  color: #fff;
  font-size: 11px;
  line-height: 1.35;
  white-space: nowrap;
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.15s ease;
  box-shadow: 0 2px 8px rgba(15, 23, 42, 0.3);
}

.sidebar__logo-wrap:hover .sidebar-logo-tip,
.sidebar-logo-tip:focus-visible {
  opacity: 1;
}

.sidebar__header {
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 98px;
  padding: 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.15);
}

.sidebar__logo {
  max-width: 128px;
  max-height: 52px;
  width: auto;
  height: auto;
  object-fit: contain;
  object-position: center;
  transition: max-width 0.2s ease, max-height 0.2s ease;
}

.sidebar__nav {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 16px 16px;
  overflow-y: auto;
  scrollbar-width: thin;
  scrollbar-color: rgba(255, 255, 255, 0.35) transparent;
}

.sidebar__nav::-webkit-scrollbar {
  width: 6px;
}

.sidebar__nav::-webkit-scrollbar-thumb {
  background: rgba(255, 255, 255, 0.3);
  border-radius: 4px;
}

.sidebar__nav::-webkit-scrollbar-track {
  background: transparent;
}

.sidebar__footer {
  flex-shrink: 0;
  padding: 16px;
  border-top: 1px solid var(--admin-footer-border);
}

.nav-item {
  display: flex;
  align-items: center;
  height: 50px;
  padding: 0 20px;
  width: 100%;
  text-align: left;
  border: none;
  background: transparent;
  color: rgba(255, 255, 255, 0.85);
  border-radius: var(--radius-md);
  font-size: 14px;
  font-weight: 500;
  transition: all 0.2s ease;
  cursor: pointer;
}

.nav-item:hover:not(.nav-item--active):not(:disabled) {
  background: var(--admin-nav-hover-bg);
  color: #fff;
}

.nav-item--active,
.nav-item--active:hover {
  background: var(--admin-nav-active-bg);
  color: var(--admin-nav-active-color);
  font-weight: 600;
  box-shadow: var(--admin-nav-active-shadow);
}

.nav-item--home {
  text-decoration: none;
  margin-top: 4px;
}

.nav-item--locked,
.nav-item:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}

.nav-item:disabled:hover {
  background: transparent;
  color: rgba(255, 255, 255, 0.85);
}

.nav-item__icon {
  margin-right: 24px;
  flex-shrink: 0;
}

.nav-item__label {
  min-width: 0;
}

.admin--collapsed .sidebar__header {
  height: 98px;
  padding: 0;
}

.admin--collapsed .sidebar__logo {
  max-width: 28px;
  max-height: 34px;
}

.admin--collapsed .sidebar__nav {
  padding: 10px 8px 12px;
}

.admin--collapsed .sidebar__footer {
  padding: 8px;
}

.admin--collapsed .nav-item {
  justify-content: center;
  padding: 0;
  height: 50px;
}

.admin--collapsed .nav-item__icon {
  margin-right: 0;
}

.admin--collapsed .nav-item__label {
  display: none;
}

.sidebar-toggle {
  flex-shrink: 0;
  width: 36px;
  height: 36px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  transition: var(--transition);
}

.sidebar-toggle:hover,
.sidebar-toggle--active {
  color: var(--brand);
  background: var(--surface-sunken);
}

.sidebar-toggle svg {
  width: 18px;
  height: 18px;
  stroke: currentColor;
  stroke-width: 2;
  fill: none;
  stroke-linecap: round;
  stroke-linejoin: round;
}

.global-chrome {
  grid-column: 1 / -1;
  grid-row: 1;
  position: relative;
  /* 顶栏（含铃铛下拉面板）必须高于内容区里 position+z-index 的元素（如账号卡片的菜单/色条 z-index:2），
     否则通知面板会被内容覆盖；sidebar(z:120) 与移动端遮罩(z:110) 仍在其上。 */
  z-index: 50;
  height: var(--admin-chrome-h);
  display: flex;
  align-items: center;
  gap: 10px;
  /* 顶栏左侧内边距对齐正文（24px），面包屑不顶着侧栏 */
  padding-left: calc(var(--sidebar-width) + 24px);
  padding-right: 22px;
  background: var(--surface);
  border-bottom: 1px solid var(--border-soft);
  box-shadow: var(--shadow-card);
}

.global-chrome__context {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  font-size: 13px;
}

/* 真面包屑：可点击项 / 当前项 */
.global-chrome__crumb-link {
  border: none;
  background: transparent;
  padding: 2px 4px;
  border-radius: var(--radius-sm);
  color: var(--text-muted);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition:
    color 0.15s ease,
    background 0.15s ease;
}

.global-chrome__crumb-link:hover {
  color: var(--brand);
  background: var(--surface-sunken);
}

.global-chrome__crumb-current {
  color: var(--text);
  font-weight: 700;
  white-space: nowrap;
}

.global-chrome__crumb {
  flex-shrink: 0;
  color: var(--text-muted);
}

.global-chrome__sep {
  flex-shrink: 0;
  color: var(--border);
}

.global-chrome__title {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  color: var(--text);
  font-weight: 700;
}

.global-chrome__spacer {
  flex: 1;
  min-width: 12px;
}

.global-loading-bar {
  position: absolute;
  left: var(--sidebar-width);
  right: 0;
  bottom: -1px;
  height: 2px;
  overflow: hidden;
  pointer-events: none;
}

.global-loading-bar span {
  position: absolute;
  inset: 0;
  background: linear-gradient(
    90deg,
    transparent 0%,
    var(--brand-start) 45%,
    var(--brand-end) 55%,
    transparent 100%
  );
  transform: translateX(-100%);
  animation: admin-loading-slide 0.9s ease-in-out infinite;
}

.admin-loading-bar-enter-active,
.admin-loading-bar-leave-active {
  transition: opacity 0.16s ease;
}

.admin-loading-bar-enter-from,
.admin-loading-bar-leave-to {
  opacity: 0;
}

@keyframes admin-loading-slide {
  to {
    transform: translateX(100%);
  }
}

@media (prefers-reduced-motion: reduce) {
  .global-loading-bar span {
    animation: none;
    transform: none;
    background: var(--brand);
  }
}

.admin__body {
  grid-column: 2;
  grid-row: 2;
  min-height: 0;
  padding: 24px;
  overflow-x: clip;
  overflow-y: auto;
  background: var(--bg);
}

/* ---- 编排（F-2）---- */
.nav-slot {
  display: flex;
  align-items: stretch;
  gap: 4px;
}

.nav-item--dragging {
  opacity: 0.5;
}

.nav-item-hide {
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 0 6px;
  border: 1px solid var(--border-soft, rgba(15, 23, 42, 0.12));
  border-radius: var(--radius-sm, 6px);
  background: transparent;
  color: var(--text-muted, #64748b);
  font-size: 11px;
  cursor: pointer;
}

.nav-item-hide:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.nav-editor {
  grid-column: 2;
  grid-row: 2;
  align-self: start;
  justify-self: end;
  width: min(320px, 92vw);
  margin: 12px;
  padding: 12px 14px;
  z-index: 130;
  border: 1px solid var(--border-soft, rgba(15, 23, 42, 0.12));
  border-radius: var(--radius-md, 10px);
  background: var(--surface, #fff);
  box-shadow: 0 12px 32px rgba(15, 23, 42, 0.18);
  font-size: 12px;
}

.nav-editor__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.nav-editor__title {
  font-weight: 600;
}

.nav-editor__actions {
  display: flex;
  gap: 6px;
}

.nav-editor__btn {
  padding: 3px 8px;
  border: 1px solid var(--border-soft, rgba(15, 23, 42, 0.12));
  border-radius: var(--radius-sm, 6px);
  background: transparent;
  color: inherit;
  font-size: 12px;
  cursor: pointer;
}

.nav-editor__hint,
.nav-editor__empty {
  margin: 8px 0 0;
  color: var(--text-muted, #64748b);
  line-height: 1.6;
}

.nav-editor__hidden {
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 240px;
  overflow-y: auto;
}

.nav-editor__hidden li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

/* ---- 顶栏入口 ---- */
.global-chrome__edit,
.global-chrome__search {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-right: 8px;
  padding: 4px 10px;
  border: 1px solid var(--border-soft, rgba(15, 23, 42, 0.12));
  border-radius: var(--radius-pill, 999px);
  background: transparent;
  color: var(--text-muted, #64748b);
  font-size: 12px;
  cursor: pointer;
}

.global-chrome__edit[aria-pressed='true'] {
  border-color: var(--brand, #3b82f6);
  color: var(--brand, #3b82f6);
}

.global-chrome__kbd {
  font-size: 11px;
  opacity: 0.75;
}

/* ---- 手机底栏（F-2）---- */
.mobile-dock {
  display: none;
}

@media (max-width: 768px) {
  .mobile-dock {
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    /* 高于抽屉（110/120）但低于顶栏以外的弹层，避免盖住模态。 */
    z-index: 115;
    display: grid;
    grid-auto-flow: column;
    grid-auto-columns: 1fr;
    align-items: stretch;
    background: var(--admin-sidebar-bg, #fff);
    border-top: 1px solid var(--admin-sidebar-border, rgba(15, 23, 42, 0.1));
  }

  .mobile-dock__item {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
    padding: 6px 2px 8px;
    border: 0;
    background: transparent;
    color: var(--text-muted, #64748b);
    font-size: 11px;
    cursor: pointer;
  }

  .mobile-dock__item--active {
    color: var(--brand, #3b82f6);
  }

  .mobile-dock__label {
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* 底栏占了正文底部，给滚动区留出等高空白，
     否则最后一行表单项会被固定底栏永久遮住。 */
  .admin__body {
    padding-bottom: 68px;
  }
}

@media (max-width: 768px) {
  .sidebar-backdrop {
    display: block;
    position: fixed;
    inset: 0;
    z-index: 110;
    background: rgba(15, 23, 42, 0.35);
    opacity: 0;
    pointer-events: none;
    transition: opacity 0.22s ease;
  }

  .sidebar-backdrop--visible {
    opacity: 1;
    pointer-events: auto;
  }

  .sidebar {
    position: fixed;
    top: 0;
    left: 0;
    width: min(260px, 82vw);
    height: 100vh;
    transform: translateX(-100%);
    border-top-right-radius: 0;
  }

  .admin--drawer-open .sidebar {
    transform: translateX(0);
    box-shadow: 2px 0 16px rgba(15, 23, 42, 0.18);
  }

  .global-chrome {
    --admin-chrome-h: 42px;
    grid-column: 1;
    padding-left: 14px;
    padding-right: 14px;
  }

  .global-chrome__context {
    display: none;
  }

  .global-loading-bar {
    left: 0;
  }

  .admin__body {
    grid-column: 1;
    padding: 16px 14px 20px;
  }
}
</style>
