// 布局偏好（T13 · F-2 侧边栏编排 / F-3 偏好持久化）。
//
// 这是编排的唯一来源：侧边栏、手机底栏、⌘G 面板都从这里读同一份顺序与
// 可见性。分成三处各算一遍的话，用户拖了一个菜单、底栏却没跟着动 ——
// 那正是这个功能要消灭的那种「界面和界面不一致」。
//
// 为什么先落 localStorage 而不是服务端：
//   1. 编排是纯界面偏好，没有别的消费方，也不需要跨设备同步才说得通；
//   2. 服务端存就要先有用户体系（T08），在那之前只能造一张没有归属的表；
//   3. localStorage 写入是同步的，拖完立刻刷新就生效，不需要等网络往返。
// 接用户体系时只需把 load/save 换成异步的 storage 接口 —— 见下面
// LayoutStorage 的注释，那里就是留给 T08 的接缝。
//
// ⚠️ 与权限的边界：这里只决定「在已经允许的菜单里怎么排、藏哪几个」，
// 绝不决定「能不能进」。真正的准入在 visibleNav（后端给的菜单集）里。
// 一个用户在这里把所有菜单藏起来只会让自己少几个入口，不可能多出一个
// 他没权限的页面 —— 因为本模块产出的顺序永远是在输入集上重排。

const ORDER_KEY = "litepan-admin-nav-order";
const HIDDEN_KEY = "litepan-admin-nav-hidden";
const GROUPS_KEY = "litepan-admin-nav-groups";

/**
 * MUST_ALWAYS_VISIBLE 永远不可隐藏的一级页面。
 *
 * 仪表盘是「一切异常的回原地」，插件库（MCP 服务 + 智能助理）是这台机器
 * 上能力开关的入口。把它们藏掉的话，用户遇到「某功能好像没了」时，
 * 唯一能确认「是不是被我关掉了」的地方也不见了 —— 那就从偏好问题
 * 变成了排障问题。
 */
export const MUST_ALWAYS_VISIBLE = ["dashboard", "mcp", "assistant"] as const;

/** 导航分组：一个标题 + 它包含的菜单 key。空分组不渲染。 */
export interface NavGroup {
  id: string;
  title: string;
}

export interface LayoutPrefs {
  /** 菜单 key 的排序；未出现在表里的新菜单按原始顺序追加到末尾。 */
  order: string[];
  /** 被用户藏起来的菜单 key。 */
  hidden: string[];
  /** 用户自定义分组（按顺序）。 */
  groups: NavGroup[];
}

const EMPTY: LayoutPrefs = { order: [], hidden: [], groups: [] };

function safeParse(raw: string | null): unknown {
  if (!raw) return null;
  try {
    return JSON.parse(raw);
  } catch {
    // 存储里的东西被手改坏过是常事（调试、跨版本、手滑）。
    // 直接丢弃这一份而不是让整个后台白屏 —— 布局偏好不是关键状态。
    return null;
  }
}

function readStringArray(raw: string | null): string[] {
  const v = safeParse(raw);
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string" && x !== "") : [];
}

function readGroups(raw: string | null): NavGroup[] {
  const v = safeParse(raw);
  if (!Array.isArray(v)) return [];
  return v
    .filter(
      (g): g is NavGroup =>
        !!g && typeof g === "object" && typeof (g as NavGroup).id === "string" && typeof (g as NavGroup).title === "string",
    )
    .map((g) => ({ id: g.id, title: g.title }));
}

export function loadLayoutPrefs(): LayoutPrefs {
  if (typeof localStorage === "undefined") return { ...EMPTY };
  return {
    order: readStringArray(localStorage.getItem(ORDER_KEY)),
    hidden: readStringArray(localStorage.getItem(HIDDEN_KEY)),
    groups: readGroups(localStorage.getItem(GROUPS_KEY)),
  };
}

export function saveLayoutPrefs(prefs: LayoutPrefs): void {
  if (typeof localStorage === "undefined") return;
  // 三项各自 try/catch：隐私模式下 setItem 会抛。
  // 编排丢一次只是刷新后回到默认，而抛出去会连带打断拖拽手势。
  try {
    localStorage.setItem(ORDER_KEY, JSON.stringify(prefs.order));
    localStorage.setItem(HIDDEN_KEY, JSON.stringify(prefs.hidden));
    localStorage.setItem(GROUPS_KEY, JSON.stringify(prefs.groups));
  } catch (err) {
    console.warn("[layout] 保存布局偏好失败：", err);
  }
}

export function clearLayoutPrefs(): void {
  if (typeof localStorage === "undefined") return;
  for (const k of [ORDER_KEY, HIDDEN_KEY, GROUPS_KEY]) {
    try {
      localStorage.removeItem(k);
    } catch {
      /* 忽略：清不掉也只是下次还是旧的 */
    }
  }
}

/** 永远不可隐藏的 key 集合。 */
export function mustAlwaysVisible(): Set<string> {
  return new Set(MUST_ALWAYS_VISIBLE);
}

/**
 * applyOrder 按偏好给一份「原始菜单」重新排序。
 *
 * 未出现在 order 里的**追加到末尾**而不是插回原位：老用户升级后新增的
 * 页面如果按「注册顺序」插回去，会被塞到已保存顺序的中间某处 —— 用户
 * 明明没动过它，它却自己跳到了第 3 位，而界面上没有任何解释。
 */
export function applyOrder<T extends { key: string }>(items: T[], order: string[]): T[] {
  if (order.length === 0) return items;
  const byKey = new Map(items.map((it) => [it.key, it]));
  const out: T[] = [];
  const used = new Set<string>();
  for (const key of order) {
    const it = byKey.get(key);
    // 表里可能留着已经被后端下线的菜单 key，跳过而不是崩。
    if (it && !used.has(key)) {
      out.push(it);
      used.add(key);
    }
  }
  for (const it of items) {
    if (!used.has(it.key)) out.push(it);
  }
  return out;
}

/**
 * applyHidden 去掉被隐藏的菜单，**并强制保留 MUST_ALWAYS_VISIBLE**。
 *
 * 强制保留放在这一层而不是调用方，是为了让「不可隐藏」这条规则只有一处实现。
 * 放在 UI 里禁用删除按钮是不够的：localStorage 里的旧数据、手改的存储、
 * 未来的导入功能，任何一条路径都能绕过按钮。
 */
export function applyHidden<T extends { key: string }>(items: T[], hidden: string[]): T[] {
  if (hidden.length === 0) return items;
  const locked = mustAlwaysVisible();
  const hide = new Set(hidden.filter((k) => !locked.has(k)));
  if (hide.size === 0) return items;
  return items.filter((it) => !hide.has(it.key));
}

/** canHide 判断某个菜单能不能被用户隐藏。 */
export function canHide(key: string): boolean {
  return !mustAlwaysVisible().has(key);
}

/** 一个菜单在当前偏好下是否处于隐藏状态（锁定项永远 false）。 */
export function isHidden(key: string, hidden: string[]): boolean {
  if (!canHide(key)) return false;
  return hidden.includes(key);
}

/** setHidden 返回新的 hidden 列表；锁定项的隐藏请求被忽略。 */
export function setHidden(hidden: string[], key: string, hide: boolean): string[] {
  if (!canHide(key)) return hidden;
  const next = hidden.filter((k) => k !== key);
  if (hide) next.push(key);
  return next;
}

/** moveItem 把一项挪到目标下标，返回新数组；下标越界时原样返回。 */
export function moveItem<T>(items: T[], from: number, to: number): T[] {
  if (from === to) return items;
  if (from < 0 || from >= items.length) return items;
  const clamped = Math.max(0, Math.min(items.length - 1, to));
  const out = items.slice();
  const [it] = out.splice(from, 1);
  out.splice(clamped, 0, it);
  return out;
}