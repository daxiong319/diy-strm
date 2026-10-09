// 免登录分享页接口客户端（管理端 + 访客端两套）。
//
// 契约上有三条纪律，每条都对应验收里的一条：
//   1. **令牌只走 X-Share-Token 头**（验收③）。不拼进 URL、不落 cookie ——
//      URL 会进浏览器历史、进 Referer、进服务器访问日志；
//      cookie 会随每个请求自动发出去，等于把这把钥匙撒向整个站点。
//   2. **访客侧调用走绝对路径**。http.get/post 会自动加 `/api` 前缀，
//      而访客路由挂在 `/share-play/*`（不在 /api 下），所以访客这层
//      自己发 fetch，不复用 http —— 免得哪天 client.ts 加个前缀把免登录路由打死。
//   3. **访客侧不缓存任何带凭证的东西**。令牌放 localStorage，
//      键名与参考实现一致（vyo-playback-token），方便同一套运维习惯。
//      ⚠️ 但 visitor_id 单独一个键：换令牌时要用它，
//      丢了会退化成「每次刷新都算新访客」，把 24 小时去重刷穿。

import { http } from "./client";

/** 令牌请求头名，必须与 Go 侧 ShareTokenHeader 一致。 */
export const SHARE_TOKEN_HEADER = "X-Share-Token";
/** 访客令牌在 localStorage 里的键名。 */
export const SHARE_TOKEN_KEY = "vyo-playback-token";
/** 访客标识在 localStorage 里的键名。 */
export const SHARE_VISITOR_KEY = "vyo-share-visitor";

/** 五种有效期，0 表示永久。与 Go 侧 ExpireDayOptions 同源。 */
export interface ExpireDayOption {
  value: number;
  label: string;
}

export const EXPIRE_DAY_OPTIONS: ExpireDayOption[] = [
  { value: 1, label: "1 天" },
  { value: 3, label: "3 天" },
  { value: 7, label: "7 天" },
  { value: 30, label: "30 天" },
  { value: 0, label: "永久有效" },
];

/** 管理端出参。code 只在「刚创建」那一次响应里出现。 */
export interface LibraryShareItem {
  id: string;
  title: string;
  account_id: number;
  file_id: string;
  season: number;
  comment: string;
  has_password: boolean;
  /** 空串 = 永久。 */
  expires_at: string;
  /** 剩余天数，0 或负数 = 永久。 */
  expire_days: number;
  max_devices: number;
  view_count: number;
  play_count: number;
  visitor_count: number;
  /** 24 小时内有动作的设备数（与 max_devices 对着看）。 */
  active: number;
  revoked: boolean;
  expired: boolean;
  created_at: string;
  created_by: number;
  url: string;
}

export interface LibraryShareVisit {
  id: number;
  visitor_id: string;
  /** IP 已脱敏到网段（IPv4 /24、IPv6 /64）。 */
  ip_masked: string;
  user_agent: string;
  counted: boolean;
  last_seen_at: string;
}

export interface LibrarySharePlay {
  id: string;
  visitor_id: string;
  file_id: string;
  item_label: string;
  method: string;
  ip_masked: string;
  started_at: string;
}

export interface LibraryShareStats {
  share_id: string;
  title: string;
  view_count: number;
  play_count: number;
  visitor_count: number;
  max_devices: number;
  active_devices: number;
  expires_at: string;
  created_at: string;
  recent_visits: LibraryShareVisit[];
  recent_plays: LibrarySharePlay[];
}

export interface LibraryShareCreateResult {
  item: LibraryShareItem;
  /** 明文短码，**只有这一次**会回传。 */
  code: string;
  has_password: boolean;
  url: string;
}

/** 管理端 API（全部要管理台会话 + share.manage 权限）。 */
export const libraryShareApi = {
  list: () => http.get<{ items: LibraryShareItem[] }>("/admin/library-shares/"),
  create: (body: {
    account_id: number;
    file_id: string;
    title: string;
    season?: number;
    comment?: string;
    password?: string;
    expire_days?: number;
    max_devices?: number;
  }) => http.post<LibraryShareCreateResult>("/admin/library-shares/", body),
  setExpiry: (id: string, expireDays: number) =>
    http.patch<{ item: LibraryShareItem }>(`/admin/library-shares/${encodeURIComponent(id)}`, {
      expire_days: expireDays,
    }),
  remove: (id: string) =>
    http.del<{ id: string }>(`/admin/library-shares/${encodeURIComponent(id)}`),
  stats: (id: string) =>
    http.get<LibraryShareStats>(`/admin/library-shares/${encodeURIComponent(id)}/stats`),
};

// ---------------------------------------------------------------- 访客端

/** 访客看到的分享信息 —— 后端只给这些字段，别的都不给。 */
export interface ShareVisitorInfo {
  title: string;
  comment: string;
  season: number;
  has_password: boolean;
  expires_at: string;
  expires_in_days: number;
}

export interface ShareTokenResult {
  /** 只有新会话才非空。 */
  token: string;
  has_token: boolean;
  is_new_visit: boolean;
  share: ShareVisitorInfo;
}

export class ShareGuestError extends Error {
  readonly status: number;
  readonly errorType: string;
  constructor(message: string, status: number, errorType: string) {
    super(message);
    this.name = "ShareGuestError";
    this.status = status;
    this.errorType = errorType;
  }
}

/** localStorage 在隐私模式下可能抛异常（Safari 无痕），不能让它炸掉整页。 */
function safeStorage(key: string): string {
  try {
    return window.localStorage.getItem(key) || "";
  } catch {
    return "";
  }
}

function safeSetStorage(key: string, value: string) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    /* 隐私模式：本次会话内令牌用内存兜底，刷新后需要重新输口令 */
  }
}

export function readShareToken(): string {
  return safeStorage(SHARE_TOKEN_KEY);
}

export function readShareVisitorID(): string {
  return safeStorage(SHARE_VISITOR_KEY);
}

/**
 * 记下访客标识。
 *
 * 必须在**第一次访问**就落盘，而不是等换到令牌才落：
 * 浏览器如果拦了 localStorage（隐私模式），每次刷新都是新访客，
 * visitor 计数会被刷高 —— 这是统计口径问题，不是安全问题。
 */
export function ensureShareVisitorID(): string {
  const existing = readShareVisitorID();
  if (existing) return existing;
  const id = randomVisitorID();
  safeSetStorage(SHARE_VISITOR_KEY, id);
  return id;
}

function randomVisitorID(): string {
  const buf = new Uint8Array(24);
  if (typeof crypto !== "undefined" && typeof crypto.getRandomValues === "function") {
    crypto.getRandomValues(buf);
  } else {
    // 没有 CSPRNG 时退化：仍然随机，但只够当浏览器侧标识用，
    // 服务端签发令牌不依赖它的不可预测性（它只是同浏览器去重的依据）。
    for (let i = 0; i < buf.length; i++) buf[i] = Math.floor(Math.random() * 256);
  }
  let out = "";
  for (const b of buf) out += b.toString(16).padStart(2, "0");
  return out;
}

function storeShareToken(token: string) {
  if (token) safeSetStorage(SHARE_TOKEN_KEY, token);
}

/** 清掉本机令牌：换了链接、链接被改期、或者访客主动想重来。 */
export function clearShareToken() {
  safeSetStorage(SHARE_TOKEN_KEY, "");
  try {
    window.localStorage.removeItem(SHARE_TOKEN_KEY);
  } catch {
    /* 忽略 */
  }
}

/**
 * 访客侧请求。
 *
 * 自己发 fetch 而不是复用 client.ts 的 http：后者会加 `/api` 前缀并读
 * 统一响应包装，而访客路由挂在 `/share-play/*`。响应体是同一个 Resp 包装，
 * 但为了让这段代码不依赖 client.ts 的内部约定，这里只解自己用到的字段。
 */
async function guestFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 20_000);
  let resp: Response;
  try {
    resp = await fetch(path, {
      credentials: "omit",
      signal: controller.signal,
      ...init,
      headers: { "Content-Type": "application/json", ...(init.headers || {}) },
    });
  } catch {
    throw new ShareGuestError("网络请求失败，请检查网络后重试", 0, "network_error");
  } finally {
    clearTimeout(timer);
  }
  let payload: { success?: boolean; data?: T; message?: string; error_type?: string } | null = null;
  try {
    payload = await resp.json();
  } catch {
    /* 下面统一按状态码处理 */
  }
  if (payload && payload.success) return payload.data as T;
  throw new ShareGuestError(
    payload?.message || `请求失败 (${resp.status})`,
    resp.status,
    payload?.error_type || "unknown",
  );
}

/** 用短码（+ 口令）换访客令牌。 */
export async function shareIssueToken(
  code: string,
  password?: string,
): Promise<ShareTokenResult> {
  const res = await guestFetch<ShareTokenResult>(
    `/share-play/${encodeURIComponent(code)}/token`,
    {
      method: "POST",
      body: JSON.stringify({ password: password || "", visitor_id: ensureShareVisitorID() }),
    },
  );
  // 复用已有会话时 token 是空串 —— 这时不要清本地，
  // 因为 localStorage 里那一枚就是仍然有效的那枚。
  storeShareToken(res.token);
  return res;
}

/** 上报 open / play 事件。不要求令牌 —— 访客可能还卡在口令框。 */
export async function shareReportEvent(
  code: string,
  event: "open" | "play",
): Promise<void> {
  try {
    await guestFetch(`/share-play/${encodeURIComponent(code)}/event`, {
      method: "POST",
      body: JSON.stringify({ event, visitor_id: ensureShareVisitorID() }),
    });
  } catch {
    // 统计上报失败绝不能影响播放：访客看到「打不开」比统计少一条严重得多。
  }
}

/** 播放地址。令牌不在 URL 里 —— 由 <video> 的 fetch/请求头带上。 */
export function shareStreamURL(code: string): string {
  return `/share-play/${encodeURIComponent(code)}/stream`;
}

/**
 * 取流用的请求头。
 *
 * ⚠️ 关键点：原生 `<video src>` **带不了自定义请求头**，
 * 所以必须用 fetch 拉成 blob 再喂给播放器（SharePage 这么做的原因）。
 */
export function shareStreamHeaders(): Record<string, string> {
  const token = readShareToken();
  return token ? { [SHARE_TOKEN_HEADER]: token } : {};
}