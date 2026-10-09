<script setup lang="ts">
import {
  computed,
  defineAsyncComponent,
  nextTick,
  onMounted,
  ref,
  watch,
  type Component,
} from "vue";
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from "vue-router";
import AdminShell from "@/components/admin/AdminShell.vue";
import CommandPalette from "@/components/admin/CommandPalette.vue";
import SetupWizard from "@/components/admin/SetupWizard.vue";
import WarningBanner from "@/components/admin/WarningBanner.vue";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import AdminAnnouncementModal from "@/components/admin/AdminAnnouncementModal.vue";
import AsyncErrorPanel from "@/components/common/AsyncErrorPanel.vue";
import { useAnnouncement } from "@/composables/useAnnouncement";

const adminPageLoaders = {
  dashboard: () => import("@/components/admin/DashboardManagement.vue"),
  accounts: () => import("@/components/admin/AccountManagement.vue"),
  settings: () => import("@/components/admin/SystemSettings.vue"),
  tasks: () => import("@/components/admin/TaskManagement.vue"),
  tools: () => import("@/components/admin/AuxToolsManagement.vue"),
  "cross-transfer": () => import("@/components/admin/CrossDriveTransferPage.vue"),
  cas: () => import("@/components/admin/CasManagementPage.vue"),
  share: () => import("@/components/admin/FileShareManagement.vue"),
  discover: () => import("@/components/admin/DiscoveryPage.vue"),
  "media-upgrade": () => import("@/components/admin/MediaUpgradePage.vue"),
  rbac: () => import("@/components/admin/RbacManagementPage.vue"),
  request: () => import("@/components/admin/RequestCenterPage.vue"),
  subtitle: () => import("@/components/admin/SubtitleSettings.vue"),
  mcp: () => import("@/components/admin/McpSettings.vue"),
  assistant: () => import("@/components/admin/McpAssistant.vue"),
};
// 后台页面全部为异步 chunk，加载失败时统一落到错误兜底，避免整页静默空白。
const asyncPage = (loader: () => Promise<Component>) =>
  defineAsyncComponent({ loader, errorComponent: AsyncErrorPanel });
const DashboardManagement = asyncPage(adminPageLoaders.dashboard);
const AccountManagement = asyncPage(adminPageLoaders.accounts);
const SystemSettings = asyncPage(adminPageLoaders.settings);
const TaskManagement = asyncPage(adminPageLoaders.tasks);
const AuxToolsManagement = asyncPage(adminPageLoaders.tools);
const CrossDriveTransferPage = asyncPage(adminPageLoaders["cross-transfer"]);
const CasManagementPage = asyncPage(adminPageLoaders.cas);
const FileShareManagement = asyncPage(adminPageLoaders.share);
const DiscoveryPage = asyncPage(adminPageLoaders.discover);
const MediaUpgradePage = asyncPage(adminPageLoaders["media-upgrade"]);
const RbacManagementPage = asyncPage(adminPageLoaders.rbac);
const RequestCenterPage = asyncPage(adminPageLoaders.request);
const SubtitleSettings = asyncPage(adminPageLoaders.subtitle);
const McpSettings = asyncPage(adminPageLoaders.mcp);
const McpAssistant = asyncPage(adminPageLoaders.assistant);
import { logout, fetchSystemConfig } from "@/api/auth";
import { fetchRbacMe } from "@/api/rbac";
import { useAuthStore } from "@/stores/auth";
import { provideAdminPageContext } from "@/composables/useAdminLoadingBar";
import { useUnsavedChanges } from "@/composables/useUnsavedChanges";
import { toast } from "@/composables/useToast";
import {
  applyHidden,
  applyOrder,
  canHide,
  clearLayoutPrefs,
  isHidden,
  loadLayoutPrefs,
  moveItem,
  saveLayoutPrefs,
  setHidden,
  type LayoutPrefs,
} from "@/composables/layoutPrefs";

const BROWSER_LOCATION_STORAGE_KEY = "litepan:index:browser-location";
const BROWSER_LOCATION_RESET_ONCE_KEY = "litepan:index:reset-once";
const LEGACY_TASK_TOOL_TABS = new Set(["scrape", "aggregate"]);

const nav = [
  { key: "dashboard", label: "仪表盘", icon: "tachometer-alt" },
  { key: "accounts", label: "存储管理", icon: "hdd" },
  { key: "settings", label: "系统设置", icon: "cogs" },
  { key: "tasks", label: "任务管理", icon: "tasks" },
  { key: "tools", label: "辅助工具", icon: "toolbox" },
  { key: "cross-transfer", label: "跨盘传输", icon: "right-left" },
  { key: "cas", label: "CAS 秒传", icon: "film" },
  { key: "share", label: "文件共享", icon: "share-alt" },
  { key: "discover", label: "影视发现", icon: "compass" },
  { key: "media-upgrade", label: "洗版管理", icon: "sync" },
  { key: "subtitle", label: "字幕处理", icon: "closed-captioning-regular" },
  { key: "mcp", label: "MCP 服务", icon: "plug" },
  { key: "assistant", label: "智能助理", icon: "robot" },
  { key: "rbac", label: "用户与权限", icon: "shield" },
  { key: "request", label: "求片中心", icon: "hand-holding-heart" },
];
// navKeys 是「系统里存在的一级页面」，权限过滤前的全集。
// 真正决定渲染的是下面那个 visibleNav —— 它是「过滤后」的那一份，
// normalize / 面包屑 / lockedKeys 全部走它，免得侧边栏藏了一个页面、
// 地址栏却还能直接输进去的那种分叉。

// visibleNav 是真正渲染到侧边栏的那一份。
const visibleNav = computed(() =>
  visibleMenuKeys.value ? nav.filter((n) => visibleMenuKeys.value!.has(n.key)) : nav,
);
// visibleNavKeys 供 normalize / lockedKeys 用，保证「看得见的页面集合」
// 在整个文件里只有一个来源，不会出现「导航里没有但地址栏能进」的分叉。
const visibleNavKeys = computed(() => visibleNav.value.map((n) => n.key));

// ---- 布局编排（T13 · F-2/F-3）----
//
// arrangedNav 排在 visibleNav **之后**：权限决定「能不能进」，编排只决定
// 「在能进的里面怎么排、藏哪几个」。这个顺序不能倒过来 —— 反了的话编排
// 就变成了绕过权限的一道后门，而 visibleNav 的 fail-open 特性会让它
// 看起来像「用户自己加的菜单」。
//
// 侧边栏、手机底栏、⌘G 面板全部读这一个数组：分三处各算一遍的话，
// 用户拖了一个菜单而底栏没跟着动，那正是这个功能要消灭的不一致。
const layoutPrefs = ref<LayoutPrefs>(loadLayoutPrefs());
const arrangedNav = computed(() => applyHidden(applyOrder(visibleNav.value, layoutPrefs.value.order), layoutPrefs.value.hidden));

function persistLayout() {
  saveLayoutPrefs(layoutPrefs.value);
}

function reorderNav(from: number, to: number) {
  const next = moveItem(arrangedNav.value, from, to);
  if (next === arrangedNav.value) return;
  // 存**完整**顺序而不是增量：新增页面后未排序的项会追加到末尾，
  // 只存增量的话下次读取时它们又回到注册顺序，等于白存。
  layoutPrefs.value = { ...layoutPrefs.value, order: next.map((n) => n.key) };
  persistLayout();
}

function toggleNavHidden(key: string) {
  if (!canHide(key)) return; // 仪表盘/插件库不可隐藏
  layoutPrefs.value = { ...layoutPrefs.value, hidden: setHidden(layoutPrefs.value.hidden, key, !isNavHidden(key)) };
  persistLayout();
}

function isNavHidden(key: string): boolean {
  // 锁定项交给 isHidden 自己判断，别在这儿另抄一份名单 ——
  // 两处名单一旦不同步，症状是「按钮禁用了但侧边栏里还是被藏了」。
  return isHidden(key, layoutPrefs.value.hidden);
}

/** 编排面板里的「已隐藏」清单：菜单在，但被用户收起来了。 */
const hiddenNavItems = computed(() =>
  nav.filter((n) => visibleNavKeys.value.includes(n.key) && isNavHidden(n.key)),
);

const layoutEditing = ref(false);

// 拖到被隐藏的菜单上 = 放出来；点隐藏区里的项 = 收回去。
function unhideNav(key: string) {
  layoutPrefs.value = { ...layoutPrefs.value, hidden: setHidden(layoutPrefs.value.hidden, key, false) };
  persistLayout();
}

function resetLayout() {
  clearLayoutPrefs();
  layoutPrefs.value = loadLayoutPrefs();
  toast.success("已恢复默认布局");
}


// ---- 菜单按权限过滤（验收④：菜单前后端一致）----
//
// 可见菜单由后端给（/auth/me 的 menus 或 /auth/menus），前端**不自己算**。
// 在这里再写一份「菜单 → 权限项」的映射，等于把 internal/rbac/menus.go
// 的 menuCatalog 抄一份到前端：后端加一个菜单而前端忘了加映射，
// 那个菜单就会静默消失，而且两边测试都不会红。抄一份必然漂移，所以不抄。
//
// fail-open：拉不到就显示全部菜单。理由是这一层只是「少显示几个入口」，
// 真正的拦截在后端的权限中间件上 —— 直接输网址一样进不去。
// 反过来 fail-closed 的话，一次网络抖动就会让运维以为整个后台挂了。
const visibleMenuKeys = ref<Set<string> | null>(null);

async function loadVisibleMenus() {
  try {
    const me = await fetchRbacMe();
    // 后端在开关关闭时会返回全量菜单并带 enabled=false，
    // 这里照单全收即可，不需要再判 enabled。
    visibleMenuKeys.value = new Set(me.menus ?? []);
  } catch (err) {
    visibleMenuKeys.value = null;
    console.warn("[rbac] 拉取可见菜单失败，本次显示全部菜单：", err);
  }
}

// 各页面 tab 结构：defaultTab 为点击父级面包屑时回落的默认 tab；tabs 为 key→label 映射。
const PAGE_TABS: Record<string, { defaultTab: string; tabs: Record<string, string> }> = {
  dashboard: { defaultTab: "overview", tabs: { overview: "运行概况", logs: "系统日志" } },
  settings: {
    defaultTab: "security",
    // tab key 必须与 SystemSettings.vue 里的常量一致（services/apiKeys/
    // notifyChannels/notifyRetries）。写成 service/api-keys 的话
    // 面包屑 :255 取不到 label，页面上就少一级，而且没人知道是这里错了。
    tabs: {
      security: "账号安全",
      homepage: "首页设置",
      services: "其他设置",
      apiKeys: "API 秘钥",
      notifyChannels: "通知渠道",
      notifyRetries: "补发队列",
    },
  },
  tasks: {
    defaultTab: "strm",
    tabs: {
      strm: "STRM 任务",
      cache: "缓存任务",
      organize: "目录整理",
      automation: "自动联动",
      "play-monitor": "播放监控",
      playback: "播放记录",
      moviepilot: "MoviePilot",
    },
  },
  tools: {
    defaultTab: "scrape",
    tabs: { scrape: "STRM 刮削", enhanced: "增强工具", backup: "备份管理" },
  },
  share: { defaultTab: "webdav", tabs: { webdav: "WebDAV", fuse: "本地挂载" } },
  "cross-transfer": { defaultTab: "plain", tabs: { plain: "跨盘普传", rapid: "跨盘秒传" } },
  cas: { defaultTab: "files", tabs: { files: "秒传清单" } },
};

const route = useRoute();
const router = useRouter();
const auth = useAuthStore();
const { dirty, confirmLeave, discardChanges } = useUnsavedChanges();
let resetBrowserLocationOnLeave = false;
const preloadedPages = new Set<string>();

const mustChangePassword = computed(() => auth.mustChangePassword);
const passwordChangeReason = computed(() => auth.passwordChangeReason);

const announcement = useAnnouncement();

const passwordChangeMessage = computed(() => {
  if (passwordChangeReason.value === "default_credentials") {
    return "当前仍在使用默认管理员口令（admin/admin）。请修改密码后继续，或在下方导入旧备份恢复原有设置。";
  }
  if (passwordChangeReason.value === "temporary_password") {
    return "当前会话使用临时密码登录，请先到系统设置 → 账号安全修改密码。";
  }
  return "当前管理员密码为非安全状态。请先到系统设置 → 账号安全修改密码。";
});

// 面包屑：后台（可点回首页）/ 页面（有 tab 时可点回默认 tab）/ 当前 tab
const crumbs = computed(() => {
  const pageDef = visibleNav.value.find((n) => n.key === page.value);
  const pageLabel = pageDef?.label ?? page.value;
  const tabCfg = PAGE_TABS[page.value];
  const items: { label: string; to?: { page: string; tab?: string } }[] = [
    { label: "后台", to: { page: "dashboard" } },
  ];
  if (tabCfg) {
    const tabLabel = tabCfg.tabs[String(route.query.tab ?? "")];
    if (tabLabel) {
      items.push({ label: pageLabel, to: { page: page.value, tab: tabCfg.defaultTab } });
      items.push({ label: tabLabel });
    } else {
      items.push({ label: pageLabel });
    }
  } else {
    items.push({ label: pageLabel });
  }
  return items;
});

function navigateCrumb(to?: { page: string; tab?: string }) {
  if (!to) return;
  void router.push({ query: to });
}

function normalize(value: unknown): string {
  const raw = String(value ?? "").trim();
  const v = raw;
  if (mustChangePassword.value && v !== "settings") return "settings";
  return visibleNavKeys.value.includes(v) ? v : "dashboard";
}

const page = ref(normalize(route.query.page));
provideAdminPageContext(page);
const adminHomeReturnMode = ref<"sidebar" | "top_icon">("top_icon");
const cachedPageComponents: Record<string, Component> = {
  dashboard: DashboardManagement,
  accounts: AccountManagement,
  tasks: TaskManagement,
  tools: AuxToolsManagement,
};
const cachedPageComponent = computed(() => cachedPageComponents[page.value] ?? null);

const pageTitle = computed(() => visibleNav.value.find((n) => n.key === page.value)?.label ?? "后台");

function preloadAdminPage(key: string) {
  const loader = adminPageLoaders[key as keyof typeof adminPageLoaders];
  if (!loader || preloadedPages.has(key)) return;
  preloadedPages.add(key);
  void loader().catch(() => preloadedPages.delete(key));
}

async function loadAdminUiConfig() {
  try {
    const cfg = await fetchSystemConfig();
    adminHomeReturnMode.value = cfg.admin_home_return_mode === "sidebar" ? "sidebar" : "top_icon";
  } catch {
    adminHomeReturnMode.value = "top_icon";
  }
}

function isPageLocked(key: string): boolean {
  return mustChangePassword.value && key !== "settings";
}

async function changePage(next: string) {
  if (isPageLocked(next)) return;
  if (next === page.value) return;
  await router.push({ query: buildPageQuery(next) });
}

async function goHome() {
  resetBrowserLocationOnLeave = true;
  try {
    await router.push("/");
  } finally {
    resetBrowserLocationOnLeave = false;
  }
}

async function handleLogout() {
  if (!(await confirmPendingChanges())) return;
  try {
    await logout();
  } catch {
    /* 即使接口失败也清本地状态 */
  }
  auth.clear();
  toast.success("已退出登录");
  await router.push("/login");
}

async function handlePasswordUpdated() {
  await auth.load();
  if (!auth.mustChangePassword) {
    toast.success("密码已更新，后台功能已解锁");
  }
}

function buildPageQuery(pageKey: string): Record<string, string> {
  const query: Record<string, string> = { page: pageKey };
  if (pageKey === "settings" && mustChangePassword.value) {
    query.tab = "security";
  }
  return query;
}

async function confirmPendingChanges(): Promise<boolean> {
  if (!dirty.value) return true;
  if (!(await confirmLeave())) return false;
  discardChanges();
  return true;
}

onBeforeRouteUpdate(() => {
  // 干净页面同步放行，避免每次 sidebar/tab 导航都多等一轮异步守卫。
  if (!dirty.value) return true;
  return confirmPendingChanges();
});

onBeforeRouteLeave(async (to) => {
  if (!(await confirmPendingChanges())) return false;
  if (resetBrowserLocationOnLeave && to.name === "home") {
    localStorage.removeItem(BROWSER_LOCATION_STORAGE_KEY);
    sessionStorage.setItem(BROWSER_LOCATION_RESET_ONCE_KEY, "1");
  }
  return true;
});

watch(
  () => [route.query.page, route.query.tab] as const,
  ([qPage, qTab]) => {
    const pageKey = String(qPage ?? "").trim();
    const tabKey = String(qTab ?? "").trim();
    // 旧书签：任务管理里的刮削 → 辅助工具；聚合已下线，落到刮削页
    if (pageKey === "tasks" && LEGACY_TASK_TOOL_TABS.has(tabKey)) {
      const tab = tabKey === "aggregate" ? "scrape" : tabKey;
      void router.replace({ query: { ...route.query, page: "tools", tab } });
      return;
    }
    if (pageKey === "tools" && tabKey === "aggregate") {
      void router.replace({ query: { ...route.query, page: "tools", tab: "scrape" } });
      return;
    }
    const target = normalize(qPage);
    if (target !== page.value) page.value = target;
  },
  { immediate: true },
);

watch(mustChangePassword, (locked) => {
  if (locked) {
    page.value = "settings";
  }
});

// ---- 功能直达的页内定位（T13 · F-1）----
//
// 索引给出的锚点约定是 `setting-<key>`（后端 internal/settings/index.go 的
// Anchor 字段，前端 SettingsRow 按同一个约定渲染 id）。
// 用 query 而不是全局事件传 field：目标页可能还没挂载（异步 chunk），
// 事件那时没有接收者，症状是「跳过去了但没滚到那一行」。
async function focusSettingField(key: string | undefined) {
  const k = String(key ?? "").trim();
  if (!k) return;
  // 最多试几次：异步页面 chunk 加载完之前锚点还不存在。
  // 失败就安静收场 —— 页面已经跳对了，定位不到不该弹错误打扰用户。
  for (let attempt = 0; attempt < 20; attempt++) {
    const el = document.querySelector(`#setting-${CSS.escape(k)}`);
    if (el) {
      el.scrollIntoView({ block: "center", behavior: "smooth" });
      el.classList.add("settings-row--flash");
      window.setTimeout(() => el.classList.remove("settings-row--flash"), 1600);
      return;
    }
    await new Promise((r) => window.setTimeout(r, 50));
  }
}

watch(
  () => [route.query.page, route.query.tab, route.query.field] as const,
  async ([qPage, , qField], prev) => {
    // 只在自己真的换了 field 时定位，否则每切一次 tab 都白滚一次。
    if (prev && prev[2] === qField) return;
    const target = normalize(qPage);
    if (target !== page.value) page.value = target;
    await nextTick();
    void focusSettingField(String(qField ?? ""));
  },
);

// 菜单加载完（含 fail-open 的 null）后重新校正当前页：
// 用户可能带着一个无权访问的 ?page=... 直接进来（收藏的旧链接、别人发的地址）。
// 这里把他送回第一个可见页面，而不是让他停在一个空白的页面上。
watch(visibleNavKeys, (keys) => {
  if (keys.includes(page.value)) return;
  const fallback = keys[0] ?? "dashboard";
  page.value = fallback;
  if (String(route.query.page ?? "") !== fallback) {
    void router.replace({ query: buildPageQuery(fallback) });
  }
});

onMounted(async () => {
  // 守卫进入后台时已拉取过认证状态，有缓存则跳过，避免重复的 /auth/status 往返。
  if (!auth.loaded) await auth.load();
  // 后台 UI 配置只影响“返回首页”按钮样式，不在首屏关键路径上，后台并行拉取。
  void loadAdminUiConfig();
  void announcement.check();
  // 菜单过滤晚于首屏渲染：先按全集显示，拉回来再裁。
  // 反过来要先等接口才画侧边栏，会让每次进后台都多一个白屏往返。
  void loadVisibleMenus();
  if (mustChangePassword.value) {
    page.value = "settings";
    router.replace({ query: buildPageQuery("settings") });
  }
});
</script>

<template>
  <AdminShell
    :nav="arrangedNav"
    :model-value="page"
    :page-title="pageTitle"
    :crumbs="crumbs"
    @navigate="navigateCrumb"
    :home-return-mode="adminHomeReturnMode"
    :locked-keys="mustChangePassword ? visibleNavKeys.filter((k) => k !== 'settings') : []"
    @update:model-value="changePage"
    @preload="preloadAdminPage"
    :editing="layoutEditing"
    :hidden-items="hiddenNavItems"
    @update:editing="layoutEditing = $event"
    @reorder="(p) => reorderNav(p.from, p.to)"
    @hide="toggleNavHidden"
    @unhide="unhideNav"
    @reset-layout="resetLayout"
    @go-home="goHome"
    @logout="handleLogout"
  >
    <WarningBanner v-if="mustChangePassword">
      <span>🛡️</span>
      <span>{{ passwordChangeMessage }}</span>
    </WarningBanner>

    <AdminAnnouncementModal
      :open="announcement.open.value"
      :item="announcement.item.value"
      @close="announcement.dismiss()"
    />

    <AdminEmptyState
      v-if="
        !cachedPageComponent &&
        !['settings', 'cross-transfer', 'cas', 'share', 'discover', 'subtitle', 'mcp', 'assistant', 'request'].includes(page)
      "
      icon="screwdriver-wrench"
      :title="`「${visibleNav.find((n) => n.key === page)?.label}」功能开发中`"
    />
    <KeepAlive>
      <SystemSettings
        v-if="page === 'settings'"
        :force-password-change="mustChangePassword"
        :password-change-reason="passwordChangeReason"
        @password-updated="handlePasswordUpdated"
        @admin-ui-updated="loadAdminUiConfig"
      />
      <CrossDriveTransferPage v-else-if="page === 'cross-transfer'" />
      <CasManagementPage v-else-if="page === 'cas'" />
      <FileShareManagement v-else-if="page === 'share'" />
      <DiscoveryPage v-else-if="page === 'discover'" />
      <MediaUpgradePage v-else-if="page === 'media-upgrade'" />
      <RbacManagementPage v-else-if="page === 'rbac'" />
      <RequestCenterPage v-else-if="page === 'request'" />
      <SubtitleSettings v-else-if="page === 'subtitle'" />
      <McpSettings v-else-if="page === 'mcp'" />
      <McpAssistant v-else-if="page === 'assistant'" />
      <component :is="cachedPageComponent" v-else-if="cachedPageComponent" :key="page" />
    </KeepAlive>

    <!-- 功能直达（⌘K）。放在 AdminShell 外面而不是 slot 里：
         面板要在任何后台页面上都能唤起，塞进 slot 会被 KeepAlive
         与页面切换一起缓存/销毁。 -->
    <CommandPalette />
    <SetupWizard />
  </AdminShell>
</template>

<style scoped>
</style>
