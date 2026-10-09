// 求片站（request portal）接口客户端。
//
// 这个站跑在**独立端口**上（默认 7812），与管理台不是同一个 origin，
// 所以它只用相对路径、没有任何硬编码 host —— 换端口或挂到子路径都不改前端。
//
// 契约上有两条纪律：
//   1. 前端**不实现任何规则**。能不能求片、要不要审核、名额够不够，
//      一律以后端回的 Permissions / require_review / daily_limit 为准。
//      前端照着后端的结论渲染，只做「把这句话显示出来」。
//   2. 统计接口后端已按本人过滤，前端**不要**再拼一个「看全家」的视图出来。

import { http } from "./client";

export interface PortalMe {
  username: string;
  display_name: string;
  is_super: boolean;
  permissions: string[];
  /** 今天还能求几部；-1 表示不限（配置里填 0）。 */
  daily_limit: number;
  used_today: number;
  /** 这台机器上提交后是否需要审核（超管、命中自动通过的规则时为 false）。 */
  require_review: boolean;
  tag_max_per_user: number;
  tag_max_length: number;
}

/** 搜索结果项（结构与媒体发现一致，只用得到其中几个字段）。 */
export interface PortalSearchItem {
  tmdb_id?: number;
  media_type: string;
  entity_key: string;
  title: string;
  original_title?: string;
  poster?: string;
  overview?: string;
  vote_avg?: number;
  year?: number;
  release_date?: string;
}

export interface PortalSearchPage {
  items: PortalSearchItem[];
  page: number;
  total_pages: number;
  total_items: number;
}

export interface PortalRequestItem {
  id: number;
  tmdb_id: number;
  title: string;
  media_type: "movie" | "tv";
  season: number;
  /** 机器可判的原始状态，界面按它分支。 */
  status: "pending" | "approved" | "rejected" | "fulfilled";
  /** 给人看的状态文案，由后端统一给出（两个页面措辞必须一致）。 */
  status_text: string;
  notes?: string;
  tags: string[];
  /** 只在被驳回时有值。 */
  reject_reason?: string;
  /** 已经建好资源订阅（求片站据此显示「在盯着」）。 */
  subscription_linked: boolean;
  created_at: string;
}

export interface PortalSubmitResult {
  id: number;
  status: string;
  needs_review: boolean;
  /** 一句话反馈，后端已按「直接生效 / 待审核 / 生效了但有隐患」分好三种。 */
  message: string;
  /** 免审但订阅没真正落地时的提醒 —— 必须原样转达，不能吞。 */
  warning?: string;
}

export interface PortalStatsRow {
  day: string;
  requester_id: number;
  media_type: string;
  submitted_count: number;
  approved_count: number;
  rejected_count: number;
  fulfilled_count: number;
}

export const portalApi = {
  login: (username: string, password: string) =>
    http.post<PortalMe>("/login", { username, password }),
  logout: () => http.post<{ ok: boolean }>("/logout"),
  me: () => http.get<PortalMe>("/me"),
  search: (q: string, mediaType: string, page: number) =>
    http.get<PortalSearchPage>("/search", { q, media_type: mediaType, page }),
  submit: (body: {
    tmdb_id: number;
    title: string;
    media_type: "movie" | "tv";
    season?: number;
    notes?: string;
    tags?: string[];
  }) => http.post<PortalSubmitResult>("/request", body),
  mine: () => http.get<{ items: PortalRequestItem[] }>("/mine"),
  /** 替换语义（不是追加）：返回库里真存下的那份。 */
  saveTags: (body: { tmdb_id: number; media_type: string; tags: string[] }) =>
    http.put<{ tags: string[] }>("/tags", body),
  stats: (days = 30) => http.get<{ items: PortalStatsRow[] }>("/stats", { days }),
};

/** 本人是否拥有某项权限。用于「显示不显示入口」，不用于任何放行判断。 */
export function portalCan(me: PortalMe | null, perm: string): boolean {
  return !!me && me.permissions.includes(perm);
}
