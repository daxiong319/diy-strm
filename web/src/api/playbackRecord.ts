import { http } from "@/api/client";

/** 一条 Emby 302 反代播放记录（对应后端 domain.PlaybackRecord） */
export interface PlaybackRecord {
  id: number;
  rule_id: string;
  /** Emby 侧用户标识（字符串）。0043 起了双用户标识：这是 Emby 那边传来的，不是 RBAC 用户。 */
  emby_user_id: string;
  /** RBAC 用户 ID（数字）。0 = 未知（0028 遗留记录没有这个值）。 */
  app_user_id: number;
  client: string;
  device_id: string;
  item_name: string;
  strm_path: string;
  provider: string;
  playback_at: string;
  /** 三态：metered 计费中 / cdn CDN 直连 / lan 局域网。 */
  state?: PlayState;
  /** 是否计费（= state === "metered"）。 */
  metered?: boolean;
  app_source?: string;
  item_scope?: string;
  app_item_name?: string;
  storage_slug?: string;
  storage_type?: string;
  request_type?: string;
  request_url?: string;
  original_url?: string;
  client_ip?: string;
  user_agent?: string;
  response_status?: number;
  /** 上行字节估算值（码率×时长）。只有计费中的记录才非 0。 */
  uploaded_bytes?: number;
  watched_seconds?: number;
  timestamp?: string;
}

/** 三态判定结果（照搬 Muvyo）。 */
export type PlayState = "metered" | "cdn" | "lan";

/** 播放记录概览统计 */
export interface PlaybackRecordStats {
  total: number;
  last_at: string;
  user_count: number;
  item_count: number;
}

// 索引签名是必需的：http 客户端的 Query 类型为 Record<string, ...>，
// 缺少它时接口无法作为查询参数传入（TS2345）。
export interface PlaybackRecordListParams {
  /** 反代规则 ID（空 = 全部） */
  id?: string;
  user_id?: string;
  provider?: string;
  /** 条目名 / 路径模糊匹配 */
  keyword?: string;
  page?: number;
  page_size?: number;
  [key: string]: string | number | boolean | undefined;
}

export interface PlaybackRecordListResult {
  items: PlaybackRecord[];
  total: number;
  page: number;
  page_size: number;
}

/** 分页查询播放记录（对齐老版 GET /api/emby302/playback-records 契约） */
export function fetchPlaybackRecords(params: PlaybackRecordListParams = {}) {
  return http.get<PlaybackRecordListResult>("/admin/playback-records", params);
}

/** 播放记录概览统计 */
export function fetchPlaybackRecordStats() {
  return http.get<PlaybackRecordStats>("/admin/playback-records/stats");
}

/** 删除单条播放记录 */
export function deletePlaybackRecord(id: number) {
  return http.del<{ deleted: number }>(`/admin/playback-records/${id}`);
}

/**
 * 清空播放记录。
 * 两个条件都为空即清空全部；后端允许空请求体。
 */
export function clearPlaybackRecords(params: { rule_id?: string; user_id?: string } = {}) {
  return http.post<{ deleted: number }>("/admin/playback-records/clear", params);
}
