import { http } from "@/api/client";
import type { PlayState } from "@/api/playbackRecord";

/**
 * 播放监控 / 观影报告的接口契约（对应后端 internal/api/play_monitor.go）。
 *
 * 字段名一律照抄后端 json tag —— 这里多写一个"看起来更合理"的
 * 名字，运行时只会安静地变成 undefined，界面上表现为"数据一直是空的"。
 */

/** 三态：metered 计费中 / cdn CDN 直连 / lan 局域网。 */
export type { PlayState };

/** 实时播放会话（对应后端 domain.PlaySession）。 */
export interface PlaySession {
  id: string;
  state: PlayState;
  /** RBAC 用户 ID。0 = 未知（0028 遗留的 Emby 记录没有这个值）。 */
  app_user_id: number;
  /** 展示用用户名；后端解析不到时回落 Emby 侧标识。 */
  user_name: string;
  /** Emby 侧用户标识（字符串）。 */
  emby_user_id: string;
  item_name: string;
  strm_path: string;
  item_scope: string;
  client_ip: string;
  user_agent: string;
  client: string;
  device_id: string;
  storage_slug: string;
  /** 估算码率（bps）。取流带 Range 时按该段反推，比按整片平均准得多。 */
  bitrate: number;
  /** 已上行字节估算值 = 码率 × 时长。只有计费中才非 0。 */
  uploaded_bytes: number;
  /**
   * 实测字节（写响应体时数出来的），只用于对账。
   * 计费数字仍然取 uploaded_bytes —— 口径是「码率 × 时长」的估算值。
   */
  measured_bytes: number;
  started_at: string;
  last_seen_at: string;
  account_id: number;
}

/** 顶部流量条（今日 / 本月 / 累计 + 三态会话数）。 */
export interface PlayTrafficSummary {
  today_bytes: number;
  month_bytes: number;
  total_bytes: number;
  active_count: number;
  metered_count: number;
  wan_count: number;
  lan_count: number;
  cdn_count: number;
}

/** GET /admin/play-monitor/sessions 的响应。 */
export interface PlaySessionListResult {
  sessions: PlaySession[];
  summary: PlayTrafficSummary;
}

/** 单用户单日上行桶（对应后端 domain.PlayTrafficBucket）。 */
export interface PlayTrafficBucket {
  day: string;
  user_id: number;
  user_name: string;
  uploaded_bytes: number;
}

/** 本月外网上行排行（对应后端 domain.PlayTrafficRank）。 */
export interface PlayTrafficRankRow {
  user_id: number;
  user_name: string;
  uploaded_bytes: number;
  sessions: number;
}

/**
 * GET /admin/play-monitor/traffic 的响应。
 *
 * 注意 today/month/total 是**平铺**的（不嵌在 summary 里）——
 * 这三个数走的是 play_traffic_daily 的落库桶，不是在播会话的实时汇总。
 */
export interface PlayTrafficResult {
  today: number;
  month: number;
  total: number;
  month_rank: PlayTrafficRankRow[];
  /** 首次启用的时刻（RFC3339）。之前的历史不补算。 */
  enabled_since: string;
  /** 后端是否找到过首次启用标记。false = 还没启用过，全部为 0。 */
  has_enabled: boolean;
}

/** 排行里的一行（对应后端 domain.PlayReport）。 */
export interface PlayReportUser {
  app_user_id: number;
  emby_user_id: string;
  user_name: string;
  count: number;
  watched_seconds: number;
  uploaded_bytes: number;
}

/** 按「用户 × 条目」聚合出来的一行（对应后端 domain.PlayReportRule）。 */
export interface PlayReportRule {
  app_user_id: number;
  emby_user_id: string;
  user_name: string;
  item_name: string;
  item_scope: string;
  provider: string;
  app_source: string;
  count: number;
  watched_seconds: number;
  uploaded_bytes: number;
  first_at: string;
  last_at: string;
}

/** 观影报告（对应后端 domain.PlayReportResult）。 */
export interface PlayReportResult {
  /** 报告实际统计的区间起止（RFC3339）。 */
  since: string;
  until: string;
  /** 周期天数（7 / 14 / 30）。 */
  period: number;
  source: string;
  min_seconds: number;
  gap_minutes: number;
  top: number;
  /** 首次启用的时刻（RFC3339）；为空 = 后端没找到启用标记。 */
  enabled_since: string;
  /** 按用户汇总的排行（已按播放次数倒序）。 */
  users: PlayReportUser[];
  /** 按「用户 × 条目」明细（已按次数倒序）。 */
  items: PlayReportRule[];
  /**
   * 统计口径说明。
   * ⚠️ 必须原样渲染，不要在前端复述或改写 ——
   * 「从启用起累计、不补算」「中断超半小时算新的一次」这些前提
   * 一旦被前端简化，用户就会拿报告对不上账。
   */
  notices: string[];
  total_count: number;
  total_watched_seconds: number;
  total_uploaded_bytes: number;
}

/** GET /admin/play-monitor/report 的响应。 */
export interface PlayReportResponse {
  report: PlayReportResult;
  /** chart=1 时的 PNG data URI；为 undefined 表示这次没带图。 */
  chart_png?: string;
}

/** 下拉选项（对应后端 GET /admin/play-monitor/options）。 */
export interface PlayMonitorOptions {
  states: Array<{ value: string; label: string }>;
  periods: Array<{ value: number; label: string }>;
  sources: Array<{ value: string; label: string }>;
  tops: Array<{ value: number }>;
  defaults: {
    period: number;
    top: number;
    source: string;
    min_seconds: number;
    gap_minutes: number;
    sample_secs: number;
    idle_secs: number;
  };
}

/** 报告查询参数（后端读的键名：period / top / source / min_seconds / gap_minutes / chart）。 */
export interface PlayReportQuery {
  period?: number;
  top?: number;
  source?: string;
  min_seconds?: number;
  gap_minutes?: number;
}

/** 实时播放会话 + 汇总。 */
export function fetchPlaySessions() {
  return http.get<PlaySessionListResult>("/admin/play-monitor/sessions");
}

/** 流量汇总 + 本月排行（前端按 10 秒轮询，不跟着 2 秒的会话列表抖）。 */
export function fetchPlayTraffic() {
  return http.get<PlayTrafficResult>("/admin/play-monitor/traffic");
}

/**
 * 清空上行统计（不可撤销）。
 *
 * 清的是数字，不是统计起点：首次启用标记会保留，所以清空之后
 * 报告仍然从首次启用算起，但不会再补算清空之前的历史。
 */
export function clearPlayTraffic() {
  return http.post<{ cleared: number }>("/admin/play-monitor/traffic/clear", {});
}

/** 观影报告。chart=true 时附带 PNG 的 data URI。 */
export function fetchPlayReport(query: PlayReportQuery & { chart?: boolean } = {}) {
  return http.get<PlayReportResponse>("/admin/play-monitor/report", {
    period: query.period,
    top: query.top,
    source: query.source || undefined,
    min_seconds: query.min_seconds,
    gap_minutes: query.gap_minutes,
    chart: query.chart ? 1 : undefined,
  });
}

/** 排行图直出 PNG 的地址（给 <img src> 用，浏览器自动带会话）。 */
export function playReportChartURL(query: PlayReportQuery = {}): string {
  const q = new URLSearchParams();
  if (query.period) q.set("period", String(query.period));
  if (query.top) q.set("top", String(query.top));
  if (query.source) q.set("source", query.source);
  if (query.min_seconds) q.set("min_seconds", String(query.min_seconds));
  if (query.gap_minutes) q.set("gap_minutes", String(query.gap_minutes));
  return `/api/admin/play-monitor/report/chart?${q.toString()}`;
}

/** 报告 / 排行图参数选项。 */
export function fetchPlayMonitorOptions() {
  return http.get<PlayMonitorOptions>("/admin/play-monitor/options");
}