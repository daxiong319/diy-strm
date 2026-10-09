import { http } from "./client";

/**
 * T12 · 通知场景（scene）与 Webhook 补发队列（outbox）。
 *
 * ⚠️ 场景清单的**唯一真相来源是后端** `GET /admin/notify-scenes`：
 * 前端不维护任何硬编码的场景列表。后端按 Muvyo 文档的触发时机表落地了 7 个
 * 场景，且「这个场景目前有没有通知产出点」也是后端说了算（categories 为空）。
 * 前端自己写一份必然漂移，而漂移的表现是「用户勾掉了一个框但通知照发」。
 */

/** 单个通知场景（对应后端 domain.NotificationSceneInfo） */
export interface NotifyScene {
  scene: string;
  label: string;
  /** 触发时机说明，直接展示给用户 */
  trigger: string;
  /** 归入该场景的通知分类；空数组 = 该场景目前还没有通知产出点 */
  categories: string[];
}

/** 归类清单：items 是场景，unmapped 是恒送达（不受场景开关影响）的告警分类 */
export interface NotifySceneList {
  items: NotifyScene[];
  unmapped: Array<{ scene: string; label: string; trigger: string }>;
  notes: string[];
}

export interface NotifySceneListResp {
  items: NotifyScene[];
  unmapped: NotifySceneList["unmapped"];
  notes: string[];
}

/** 补发队列状态（对应后端 domain.NotifyRetryStatus） */
export type NotifyRetryStatus = "pending" | "sent" | "failed";

/** 补发队列里的一条记录（对应后端 domain.NotifyRetryEntry） */
export interface NotifyRetryEntry {
  id: number;
  /** 触发来源场景；空串 = 该通知未归入任何场景（不影响投递） */
  scene: string;
  scene_label: string;
  channel_type: string;
  channel_name: string;
  /** 发送当时的目标地址快照（渠道后来改地址也不会改这里） */
  target_url: string;
  title: string;
  content: string;
  tone: string;
  /** 已尝试次数 */
  attempts: number;
  max_attempts: number;
  /** 下次重试时间；非 pending 时为空串 */
  next_retry_at: string;
  last_error: string;
  status: NotifyRetryStatus;
  /** 是否可手动重投（只有 failed 态为 true） */
  redrivable: boolean;
  created_at: string;
  updated_at: string;
}

export interface NotifyRetryListResp {
  items: NotifyRetryEntry[];
  total: number;
  counts: Record<NotifyRetryStatus, number>;
  max_attempts: number;
}

// 索引签名是必需的：http 客户端的 Query 类型为 Record<string, ...>，
// 缺少它时无法作为查询参数传入（TS2345）。
export interface NotifyRetryListParams {
  /** 空串 = 全部状态 */
  status?: string;
  limit?: number;
  offset?: number;
  [key: string]: string | number | boolean | undefined;
}

/** 通知渠道 config 里存场景订阅的键（英文逗号分隔的场景 ID，空串 = 全部订阅） */
export const NOTIFY_SCENES_CONFIG_KEY = "scenes";

/** 渠道 config 里存通知分类白名单的键 */
export const NOTIFY_EVENTS_CONFIG_KEY = "events";

export function fetchNotifyScenes() {
  return http.get<NotifySceneListResp>("/admin/notify-scenes");
}

export function fetchNotifyRetries(params?: NotifyRetryListParams) {
  return http.get<NotifyRetryListResp>("/admin/notify-retries", params);
}

export function fetchNotifyRetry(id: number) {
  return http.get<{ item: NotifyRetryEntry }>(`/admin/notify-retries/${id}`);
}

/** 手动重投一条 failed 记录；后端会立刻跑一轮补发再返回最新状态 */
export function redriveNotifyRetry(id: number) {
  return http.post<{ item: NotifyRetryEntry }>(`/admin/notify-retries/${id}/redrive`);
}

/** 清理补发记录；status 留空（undefined 或 ""）= 清全部 */
export function clearNotifyRetries(status?: string) {
  return http.post<{ cleared: number }>("/admin/notify-retries/clear", { status: status ?? "" });
}

// ── 场景订阅的纯函数编解码 ────────────────────────────────────────────────
// 放在 api 层而不是组件里，是因为 NotifyChannelSettings 与任何将来要展示
// 「这个渠道订阅了哪些场景」的地方都要用同一份解析口径 —— 解析分叉过一次，
// 列表页就会把「全订阅」显示成「没订阅任何场景」。

/** 解析 config.scenes；空串/空值 = 全部订阅（与后端 ParseSceneSubscriptions 同口径） */
export function parseSceneSubscriptions(raw?: string | null): string[] {
  const trimmed = (raw ?? "").trim();
  if (!trimmed) return [];
  return trimmed
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

/** 序列化回 config.scenes；空集合 = 空串（= 全部订阅） */
export function encodeSceneSubscriptions(scenes: string[]): string {
  const seen = new Set<string>();
  const parts: string[] = [];
  for (const s of scenes) {
    const v = s.trim();
    if (!v || seen.has(v)) continue;
    seen.add(v);
    parts.push(v);
  }
  return parts.join(",");
}
