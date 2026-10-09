// 求片中心（管理台侧）接口客户端。
//
// 与求片站那套（./request.ts）**共用同一批 DTO 形状**，
// 但走的是管理台路由 /media-request/*，权限闸在管理台这棵树上。
//
// 两条纪律：
//   1. 前端不判断「该不该看得到」—— 能不能打开这个页面、能不能点审核，
//      后端 requirePermission 已经管住了；这里只按后端给的 menus 显示入口。
//   2. 审核的动作（通过/驳回）只有一个接口，服务端做状态机校验；
//      前端不做乐观更新 —— 审核这一下必须以服务端返回的状态为准，
//      因为并发时后到的那个审核员会拿到 ErrNotPending。

import { http } from "./client";
import type { PortalRequestItem, PortalStatsRow } from "./request";

export type MediaRequestStatus = "pending" | "approved" | "rejected" | "fulfilled";

export interface MediaRequestReviewItem extends PortalRequestItem {
  requester_id: number;
  requester_name: string;
  year: number;
  poster_url?: string;
  reviewed_at?: string;
  reviewer_name?: string;
  reviewed_note?: string;
  /** 0 表示还没建订阅 —— 审核台上这是「点了通过但什么都没发生」的信号。 */
  subscription_id: number;
}

export interface MediaRequestRule {
  id: number;
  name: string;
  media_type: string;
  daily_limit: number;
  pending_limit: number;
  auto_approve: boolean;
  applies_to_user_id: number;
  enabled: boolean;
  priority: number;
}

export interface MediaRequestCenterItem {
  item: MediaRequestReviewItem;
  /** 服务端提前算好的提醒（例：这条已通过但订阅还没建起来）。 */
  warning?: string;
}

export const requestCenterApi = {
  pending: (limit = 200) =>
    http.get<{ items: MediaRequestReviewItem[] }>("/media-request/pending", { limit }),
  list: (status: string | "", limit = 200) =>
    http.get<{ items: MediaRequestReviewItem[] }>("/media-request/requests", {
      status: status || undefined,
      limit,
    }),
  detail: (id: number) => http.get<MediaRequestCenterItem>(`/media-request/requests/${id}`),
  review: (id: number, body: { approve: boolean; reason?: string; note?: string }) =>
    http.post<MediaRequestCenterItem & { warning?: string }>(
      `/media-request/requests/${id}/review`,
      body,
    ),
  mine: () => http.get<{ items: PortalRequestItem[] }>("/media-request/mine"),
  stats: () => http.get<{ items: PortalStatsRow[] }>("/media-request/stats"),
  rules: () => http.get<{ items: MediaRequestRule[] }>("/media-request/rules"),
  saveRules: (items: MediaRequestRule[]) =>
    http.put<{ items: MediaRequestRule[] }>("/media-request/rules", { items }),
  reconcile: () => http.post<{ handled: number }>("/media-request/reconcile"),
};

export const REQUEST_STATUS_OPTIONS: { value: MediaRequestStatus | ""; label: string }[] = [
  { value: "", label: "全部" },
  { value: "pending", label: "待审核" },
  { value: "approved", label: "已通过" },
  { value: "rejected", label: "已驳回" },
  { value: "fulfilled", label: "已转存" },
];

/** 媒体类型选项；空串 = 电影与剧集都适用。 */
export const REQUEST_MEDIA_TYPE_OPTIONS: { value: string; label: string }[] = [
  { value: "", label: "电影与剧集" },
  { value: "movie", label: "仅电影" },
  { value: "tv", label: "仅剧集" },
];
