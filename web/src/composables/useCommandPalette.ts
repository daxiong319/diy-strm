import { computed, ref, shallowRef } from "vue";
import { useRouter } from "vue-router";
import { fetchSettingsIndex, type SettingIndexEntry } from "@/api/settings";

/**
 * 全局「功能直达」（⌘K / Ctrl+K）。
 *
 * 三个刻意决定：
 *
 * 1. **索引来自后端，前端不维护清单。**
 *    索引由 `GET /admin/settings/index` 从设置注册表派生。
 *    前端手写一份清单必然会漏，而漏掉的症状是「设置页上找得到、
 *    搜索里搜不到」，用户只会得出「这个功能没做」这一个结论。
 *
 * 2. **模块级单例，不用 Pinia。**
 *    与 modalStack / useToast / useAnnouncement 一致：这个面板要被
 *    顶栏按钮、键盘快捷键、以及各个页面同时唤起，装进 store 只会
 *    让「打开面板」变成一件需要先想清楚谁持有它的事。
 *
 * 3. **跳转只写 query，不直接改组件状态。**
 *    后台所有页面都是 `/admin?page=&tab=`（见 web/src/router/index.ts，
 *    一共只有 5 条路由、零二级路由）。走 router 是唯一能同时触发
 *    页面切换、tab 切换、以及 tab 切换前那套「有未保存改动就拦一下」
 *    逻辑的路径。直接改 ref 会绕过守卫，表现为「点搜索结果跳过去了，
 *    但上一个页面没保存的修改被静默丢掉」。
 */

/** 面板是否打开。模块级 ref —— 见上面第 2 点。 */
export const paletteOpen = ref(false);

/** 当前搜索词。 */
export const paletteQuery = ref("");

/** 上一次用过的词：面板重新打开时先清空，但把历史留在内存里备查。 */
let lastQuery = "";

const entries = shallowRef<SettingIndexEntry[]>([]);
const loaded = ref(false);
const loadError = ref("");
const coverage = ref("");
/** 命中的序号，用于 ↑↓ 与 Enter。 */
const cursor = ref(0);

/** 单例路由：面板可能在任何组件树里被唤起，但路由只有一个。 */
let routerRef: ReturnType<typeof useRouter> | null = null;

function ensureRouter() {
  if (!routerRef) {
    try {
      routerRef = useRouter();
    } catch {
      // 在 setup 之外被调用（例如测试里）时拿不到路由实例。
      // 这不是错误路径：只是那一次无法跳转，面板照常显示结果。
      routerRef = null;
    }
  }
  return routerRef;
}

/**
 * 载入索引。缓存到本次会话 —— 注册表是编译期常量，
 * 用户在一次浏览过程中不可能给它加一项。
 */
export async function loadPaletteIndex(force = false) {
  if (loaded.value && !force) return;
  loadError.value = "";
  try {
    const payload = await fetchSettingsIndex();
    entries.value = payload.items ?? [];
    coverage.value = payload.coverage ?? "";
    loaded.value = true;
  } catch (err) {
    // ⚠️ 报错时不要把结果清空成「空索引」：
    // 界面上那会是「一个搜不到任何东西的框」，看起来像功能坏了。
    loadError.value = err instanceof Error ? err.message : String(err);
  }
}

/**
 * 把索引条目按查询词过滤。
 *
 * 匹配规则刻意做成「全部词都要命中」，而不是「命中任意一个」：
 * 搜「缓存 条数」应该只给缓存条目数那一项，给出一堆「缓存」相关的
 * 其它项等于把筛选的活推回给用户。
 */
export function filterPaletteItems(
  items: SettingIndexEntry[],
  rawQuery: string,
): SettingIndexEntry[] {
  const terms = rawQuery.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) {
    // 空查询给一个短名单而不是全部 150 条：全部铺开的话第一屏
    // 全是性能类设置，用户会以为搜索坏了。
    return items.slice(0, 12);
  }
  const out: SettingIndexEntry[] = [];
  for (const it of items) {
    const haystack = [
      it.key,
      it.label,
      it.category_label,
      it.description ?? "",
      ...(it.keywords ?? []),
    ]
      .join("\n")
      .toLowerCase();
    if (terms.every((t) => haystack.includes(t))) out.push(it);
  }
  // 标签命中的排前面：用户输入的通常是他记得的中文名，不是 key。
  return out.sort((a, b) => score(b, terms) - score(a, terms));
}

function score(it: SettingIndexEntry, terms: string[]): number {
  const label = it.label.toLowerCase();
  const cat = it.category_label.toLowerCase();
  let n = 0;
  for (const t of terms) {
    if (label === t) n += 8;
    else if (label.startsWith(t)) n += 5;
    else if (label.includes(t)) n += 3;
    if (it.key.toLowerCase() === t) n += 6;
    if (cat.includes(t)) n += 1;
  }
  return n;
}

const results = computed(() => filterPaletteItems(entries.value, paletteQuery.value));

/** 打开面板。顺带把索引拉起来（已加载过则不发请求）。 */
export function openPalette() {
  paletteOpen.value = true;
  paletteQuery.value = "";
  cursor.value = 0;
  void loadPaletteIndex();
}

export function closePalette() {
  paletteOpen.value = false;
  // 记住这次的词：下次打开时清空，但用户中途关掉再打开
  // 常常是为了改一下刚才的词，别把它弄丢了。
  lastQuery = paletteQuery.value;
  paletteQuery.value = "";
}

/** 键盘上下移动光标。 */
export function movePaletteCursor(delta: number) {
  const n = results.value.length;
  if (n === 0) return;
  cursor.value = (cursor.value + delta + n) % n;
}

/**
 * 激活一条结果：跳到对应页面/tab/锚点。
 *
 * 返回 false 表示跳不过去（缺 page、或拿不到路由实例）——
 * 面板据此留在原地并提示，而不是假装已经跳了。
 */
export async function activatePaletteItem(item: SettingIndexEntry | undefined): Promise<boolean> {
  if (!item || !item.page) return false;
  const router = ensureRouter();
  if (!router) return false;
  await router.push({
    path: "/admin",
    query: {
      page: item.page,
      ...(item.tab ? { tab: item.tab } : {}),
      // field 交给目标页去 scrollIntoView。用 query 而不是
      // 全局事件：页面可能还没挂载，事件那时没有接收者。
      ...(item.anchor ? { field: item.key } : {}),
    },
  });
  closePalette();
  return true;
}

/** 输入事件里要挡掉的场景：在这些元素里按 ⌘K 不该劫持输入法/快捷键。 */
function inEditableTarget(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el || typeof el.tagName !== "string") return false;
  const tag = el.tagName.toUpperCase();
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  return el.isContentEditable === true;
}

/**
 * 装一次全局快捷键。返回注销函数。
 *
 * 用 capture 阶段监听：面板可能被某个局部 stopPropagation 的组件盖住，
 * 冒泡阶段就收不到 ⌘K 了 —— 而「搜索框失焦后 ⌘K 唤不回来」正是
 * 用户第一次遇到就会放弃的故障。
 *
 * ⚠️ 刻意不管 ⌘G：浏览器里 ⌘G 是「查找下一个」（Firefox），
 * 抢走它会让用户在任何页面都找不到浏览器自带的查找。
 */
export function installPaletteHotkey(target: Window = window): () => void {
  const onKey = (e: KeyboardEvent) => {
    if (e.defaultPrevented) return;
    if (!(e.metaKey || e.ctrlKey)) return;
    if (e.altKey || e.shiftKey) return;
    if (e.key.toLowerCase() !== "k") return;
    // 在输入框里不劫持：那里的 ⌘K 可能是输入法或别的组件的快捷键。
    if (inEditableTarget(e.target)) return;
    e.preventDefault();
    if (paletteOpen.value) {
      closePalette();
    } else {
      openPalette();
    }
  };
  target.addEventListener("keydown", onKey, true);
  return () => target.removeEventListener("keydown", onKey, true);
}

/**
 * 面板里要展示的「上一次搜过」的词。
 * 只在内存里（不落 localStorage）：搜索词可能含账号名、目录名，
 * 留在磁盘上等下一次别人用这台机器就能看到。
 */
export function paletteLastQuery(): string {
  return lastQuery;
}

/**
 * 打包给组件用。返回的是同一批 ref —— 模块级单例，
 * 谁调用拿到的都是同一个面板状态（第二个面板实例是个 bug，不是特性）。
 */
export function useCommandPalette() {
  return {
    paletteOpen,
    paletteQuery,
    cursor,
    results,
    loadError,
    coverage,
    openPalette,
    closePalette,
    movePaletteCursor,
    activatePaletteItem,
    loadPaletteIndex,
    paletteLastQuery,
  };
}