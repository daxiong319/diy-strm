import { http } from "@/api/client";

/** 一条 Emby 302 反代播放记录（对应后端 domain.PlaybackRecord） */
export interface PlaybackRecord {
  id: number;
  rule_id: string;
  user_id: string;
  client: string;
  device_id: string;
  item_name: string;
  strm_path: string;
  provider: string;
  playback_at: string;
}

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
