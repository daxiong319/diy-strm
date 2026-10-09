import { http } from "./client";

// RSS 订阅源（T16）。
//
// 路径前缀是 /admin/rss-* 而不是 /admin/discovery/rss-*：前端写
// /admin/ 前缀、后端就挂在 /admin 子树里，两边不会走岔。T10 踩过的坑是
// 后端挂在 /admin 之外、前端按 /admin/ 写，五个操作全 404。
// 权限闸（rbac.PermDiscoverView）由后端 router 自己加，前端不用管。

export interface RssSource {
  id: number;
  name: string;
  rss_url: string;
  target_path: string;
  storage: string;
  media_server: string;
  poster_url: string;
  include_regex: string;
  exclude_regex: string;
  media_type: string;
  action: string;
  enabled: boolean;
  last_sync_at: string;
  last_status: string;
  last_message: string;
  created_at: string;
  updated_at: string;
}

export interface RssSourceUpsert {
  name?: string;
  rss_url?: string;
  target_path?: string;
  storage?: string;
  media_server?: string;
  poster_url?: string;
  include_regex?: string;
  exclude_regex?: string;
  media_type?: string;
  action?: string;
  enabled?: boolean;
}

export interface RssSourceListResult {
  items: RssSource[];
  total: number;
}

export interface RssSyncCounters {
  added: number;
  skipped: number;
  failed: number;
}

export interface RssItem {
  title: string;
  status: string;
  reason: string;
  link: string;
}

export interface RssItemSync {
  source: RssSource;
  counters: RssSyncCounters;
  status: string;
  message: string;
  /** 本轮是不是「停机超阈值，从最新一条开始」的追赶模式。 */
  catchup: boolean;
  total: number;
  fetched_at: string;
  detail: RssItem[];
  notices: string[];
}

export interface RssSyncAllResult {
  items: RssItemSync[];
  counters: RssSyncCounters;
}

export interface RssHistory {
  id: number;
  source_id: number;
  source_name: string;
  guid: string;
  title: string;
  link: string;
  download_url: string;
  target_path: string;
  status: string;
  message: string;
  published_at: string;
  created_at: string;
}

export interface RssHistoryListParams {
  source_id?: number;
  status?: string;
  since?: string;
  limit?: number;
  // 带索引签名：http.get 的 query 形参是 Record<string, ...>，
  // 少这一行 TS2345。
  [key: string]: string | number | boolean | undefined;
}

export interface RssHistoryListResult {
  items: RssHistory[];
  total: number;
}

export interface RssPreviewItem {
  title: string;
  status: string;
  reason: string;
  link: string;
}

export interface RssPreviewResult {
  feed_title: string;
  items: RssPreviewItem[];
  total: number;
  filtered: number;
  no_resource: number;
  content_type: string;
  bytes: number;
  charsets: string[];
}

export interface RssPreviewRequest {
  rss_url: string;
  include_regex?: string;
  exclude_regex?: string;
}

export interface RssOptions {
  charsets: string[];
  formats: string[];
  kinds: string[];
  media_types: { value: string; label: string }[];
  actions: { value: string; label: string }[];
  // 媒体库三态："" = 跨全部已启用媒体库；"__none__" = 不判定；
  // 其它值 = 具体媒体库 slug（后端目前只下发前两种，实际库列表随部署变化）。
  media_servers: { value: string; label: string }[];
  limits: {
    max_feed_bytes: number;
    notice: string;
  };
}

export function listRssSources(params?: {
  keyword?: string;
  enabled?: boolean;
  limit?: number;
}) {
  return http.get<RssSourceListResult>("/admin/rss-sources", params);
}

export function getRssSource(id: number) {
  return http.get<{ source: RssSource }>(`/admin/rss-sources/${id}`);
}

export function createRssSource(payload: RssSourceUpsert) {
  return http.post<{ source: RssSource }>("/admin/rss-sources", payload);
}

export function updateRssSource(id: number, payload: RssSourceUpsert) {
  return http.put<{ source: RssSource }>(`/admin/rss-sources/${id}`, payload);
}

export function deleteRssSource(id: number, deleteHistory: boolean) {
  // DELETE 带查询参数的写法：第二参（body）必须显式占位 undefined。
  return http.del<{ deleted: boolean; history_removed: number }>(
    `/admin/rss-sources/${id}`,
    undefined,
    deleteHistory ? { delete_history: 1 } : undefined,
  );
}

// 同步一个源用长超时：一次同步要串行做「抓 feed → 逐条查重 → 逐条提交
// 离线下载」，300 条的源很容易超过默认的 90 秒。
export function syncRssSource(id: number) {
  return http.postWithTimeout<RssItemSync>(
    `/admin/rss-sources/${id}/sync`,
    undefined,
    180_000,
  );
}

export function syncAllRssSources() {
  return http.postWithTimeout<RssSyncAllResult>("/admin/rss-sync", undefined, 300_000);
}

export function listRssHistory(params?: RssHistoryListParams) {
  return http.get<RssHistoryListResult>("/admin/rss-history", params);
}

// 删一条历史 = 把这个条目加入豁免：下一轮同步还会重新处理它。
export function deleteRssHistory(id: number) {
  return http.del<{ deleted: boolean; id: number }>(`/admin/rss-history/${id}`);
}

export function previewRssSource(payload: RssPreviewRequest) {
  return http.postWithTimeout<RssPreviewResult>(
    "/admin/rss-preview",
    payload,
    60_000,
  );
}

export function getRssOptions() {
  return http.get<RssOptions>("/admin/rss-options");
}
