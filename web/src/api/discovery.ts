import { http } from "./client";

// ---------------------------------------------------------------------------
// 影视发现 API（对齐后端 /admin/discovery/*）
// ---------------------------------------------------------------------------

export interface DiscoverItem {
  source: string;
  media_type: string;
  tmdb_id?: number;
  douban_id?: string;
  title: string;
  original_title?: string;
  poster?: string;
  backdrop?: string;
  overview?: string;
  vote_avg?: number;
  release_date?: string;
  year?: number;
  entity_key?: string;
  [key: string]: unknown;
}

export interface PageResult {
  items: DiscoverItem[];
  page: number;
  total_pages?: number;
  total_results?: number;
  [key: string]: unknown;
}

export interface CalendarEpisode {
  entity_key?: string;
  title?: string;
  show_title?: string;
  overview?: string;
  poster?: string;
  still?: string;
  air_date?: string;
  season?: number;
  episode?: number;
  tmdb_id?: number;
  [key: string]: unknown;
}

export interface CalendarDay {
  date: string;
  label?: string;
  episodes?: CalendarEpisode[];
  items?: DiscoverItem[];
  [key: string]: unknown;
}

export interface DiscoverMeta {
  genres_movie: Record<string, string>;
  genres_tv: Record<string, string>;
  providers: Array<Record<string, string>>;
  regions: Array<Record<string, string>>;
  collections: Array<Record<string, string>>;
  douban_tags: Record<string, string[]>;
  default_source: string;
  douban_category: Record<string, string[]>;
  douban_sort: Array<Record<string, string>>;
  anime_genres: string[];
  anime_regions: Array<Record<string, string>>;
  anime_sort: Array<Record<string, string>>;
  maoyan_category: Array<{ key?: string; label?: string; value?: string }>;
}

export interface DiscoveryFavorite {
  id: number;
  entity_key: string;
  source: string;
  media_type: string;
  external_id: string;
  tmdb_id?: number;
  title: string;
  original_title?: string;
  poster?: string;
  overview?: string;
  vote_avg?: number;
  year?: number;
  created_at?: string;
}

export interface RankingItem extends DiscoverItem {
  rank?: number;
  heat?: number | string;
  [key: string]: unknown;
}

export interface RankingGroup {
  key?: string;
  label?: string;
  items?: RankingItem[];
  [key: string]: unknown;
}

export function fetchDiscoverMeta() {
  return http.get<DiscoverMeta>("/admin/discovery/meta");
}

export function fetchDiscoverExplore(params: {
  type?: string;
  genre?: string;
  year?: string;
  region?: string;
  sort_by?: string;
  page?: number;
  force?: boolean;
}) {
  return http.get<PageResult>("/admin/discovery/explore", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverExploreDouban(params: { type?: string; tag?: string; page?: number; force?: boolean }) {
  return http.get<PageResult>("/admin/discovery/explore/douban", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverRankings(params: {
  provider?: string;
  region?: string;
  media_type?: string;
  page?: number;
  force?: boolean;
}) {
  return http.get<PageResult>("/admin/discovery/rankings", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverRankingsMaoyan(params: { category?: string; force?: boolean }) {
  return http.get<{ groups?: RankingGroup[]; feed_status?: string }>(
    "/admin/discovery/rankings/maoyan",
    params as Record<string, string | number | boolean | undefined>,
  );
}

export function fetchDiscoverCalendar(params: { days?: number | string; kind?: string; force?: boolean }) {
  return http.get<CalendarDay[]>("/admin/discovery/calendar", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverAnimeCalendar(params: { force?: boolean }) {
  return http.get<CalendarDay[]>("/admin/discovery/anime/calendar", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverAnimeSearch(params: { keyword?: string; source?: string; page?: number; force?: boolean }) {
  return http.get<PageResult>("/admin/discovery/anime/search", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverActors(params: { page?: number; force?: boolean }) {
  return http.get<PageResult>("/admin/discovery/actors", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverActorWorks(id: number | string) {
  return http.get<{ works?: DiscoverItem[]; items?: DiscoverItem[] }>(`/admin/discovery/actors/${id}/works`);
}

export function fetchDiscoverSearch(params: { q?: string; media_type?: string; page?: number; force?: boolean }) {
  return http.get<PageResult>("/admin/discovery/search", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverDetails(source: string, type: string, id: string) {
  return http.get<Record<string, unknown>>(`/admin/discovery/details/${source}/${type}/${id}`);
}

export function fetchDiscoverFavorites() {
  return http.get<{ items: DiscoveryFavorite[] }>("/admin/discovery/favorites");
}

export function addDiscoverFavorite(body: Partial<DiscoveryFavorite> & { entity_key: string }) {
  return http.post<{ ok: boolean }>("/admin/discovery/favorites", body);
}

export function deleteDiscoverFavorite(id: number) {
  return http.del<{ ok: boolean }>(`/admin/discovery/favorites/${id}`);
}

export function checkDiscoverFavorites(keys: string[]) {
  return http.post<{ favorited: Record<string, boolean> }>("/admin/discovery/favorites/check", { keys });
}

export function fetchDiscoverSettings() {
  return http.get<Record<string, unknown>>("/admin/discovery/settings");
}

export function updateDiscoverSettings(values: Record<string, unknown>) {
  return http.put<Record<string, unknown>>("/admin/discovery/settings", values);
}

// ---------------------------------------------------------------------------
// 影视发现「关联资源」搜索 / 转存 / 离线
// 对齐后端 internal/api/discover_resources.go + internal/api/discover_transfer.go
// 路由：/admin/discovery/resources/{search,transfer,offline}
// ---------------------------------------------------------------------------

/** 可搜索的资源来源（后端 normalizeResourceSources 仅接受这 4 个） */
export type DiscoverResourceSource = "re0" | "guanying" | "seedhub" | "tg";

/** 资源季集信息（仅 re0 源可能返回） */
export interface DiscoverResourceEpisode {
  season_num?: number;
  episode_num?: number;
  end_episode_num?: number;
  total_episode_num?: number;
  is_complete?: boolean;
  is_updated?: boolean;
}

/**
 * 单条资源卡片。
 * 字段名严格对齐后端 resourceItem（internal/api/discover_resources.go:37-61）。
 */
export interface DiscoverResourceItem {
  item_key: string;
  source: string;
  /** 网盘类型原始值：123/guangyapan/139/magnet/ed2k… */
  provider: string;
  /** 网盘展示名：123/光鸭/移动云盘/磁力/ED2K… */
  provider_label: string;
  title: string;
  slug: string;
  share_url: string;
  /** 链接类型，magnet/ed2k 必须走离线通道 */
  link_type: string;
  size: string;
  episode?: DiscoverResourceEpisode;
  is_unlocked: boolean;
  points_known: boolean;
  unlock_points: number;
  unlocked_users_count: number;
  remark: string;
  validate_message: string;
  is_official: boolean;
  sharer: string;
  /** 清晰度/片源规格标签 */
  resource_spec_tags: string[];
  subtitle_languages: string[];
  subtitle_types: string[];
  /** 该链接可离线到的网盘（magnet/ed2k 才有） */
  supported_targets: string[];
  target_provider: string;
  [key: string]: unknown;
}

/** 单来源失败信息（★ 部分源失败时整体仍 200，必须消费 errors） */
export interface DiscoverResourceSourceError {
  source: string;
  code?: string;
  message?: string;
  error?: string;
}

export interface DiscoverResourceSearchResult {
  items: DiscoverResourceItem[];
  errors: DiscoverResourceSourceError[];
}

export interface DiscoverResourceSearchBody {
  title: string;
  aliases?: string[];
  tmdb_id?: number;
  media_type?: string;
  year?: string | number;
  sources?: string[];
  provider?: string;
  season?: number;
  episode?: number;
}

/** 转存 / 离线请求体（对齐后端 resourceTransferRequest） */
export interface DiscoverResourceActionBody {
  source: string;
  provider: string;
  slug: string;
  title: string;
  link_type: string;
  share_url?: string;
  account_id?: number;
}

export interface DiscoverResourceTransferResult {
  success: boolean;
  provider: string;
  message: string;
  title: string;
  finished_at: string;
}

export interface DiscoverResourceOfflineResult {
  success: boolean;
  provider?: string;
  link_type?: string;
  account_id?: number;
  message?: string;
  title?: string;
  created_at?: string;
  [key: string]: unknown;
}

/** 资源搜索（多源聚合，单源失败进 errors 而非抛错） */
export function searchDiscoverResources(body: DiscoverResourceSearchBody) {
  return http.post<DiscoverResourceSearchResult>("/admin/discovery/resources/search", body);
}

/**
 * 一键转存到目标网盘。
 * ★ 后端同步阻塞最长 180 秒（internal/api/discover_transfer.go:54），
 *   必须用 postWithTimeout 放宽到 200 秒，否则默认 90 秒超时会先炸。
 */
export function transferDiscoverResource(body: DiscoverResourceActionBody, timeoutMs = 200_000) {
  return http.postWithTimeout<DiscoverResourceTransferResult>("/admin/discovery/resources/transfer", body, timeoutMs);
}

/** 离线下载（仅 magnet / ed2k）；account_id 可选，后端会按网盘类型自动选账号 */
export function offlineDiscoverResource(body: DiscoverResourceActionBody) {
  return http.postWithTimeout<DiscoverResourceOfflineResult>("/admin/discovery/resources/offline", body, 200_000);
}

// ---------------------------------------------------------------------------
// 影视发现后台：订阅追更 / 频道订阅 / 监控历史 / Emby 缺集补档
// 对齐后端 internal/api/discover_subscription.go、discover_channels.go、
// discover_emby_missing.go。JSON 键统一 snake_case，
// 唯一例外是 ChannelPost（见其注释：tgchannel 结构体无 JSON tag）。
// ---------------------------------------------------------------------------

/** 目标网盘 key：123 / guangya / pan139 */
export type DiscoveryProvider = "123" | "guangya" | "pan139";

/** 网盘 key → 展示名（后端只存 key，中文名由前端负责） */
export const DISCOVERY_PROVIDER_LABELS: Record<string, string> = {
  "123": "123 网盘",
  guangya: "光鸭",
  pan139: "139 网盘",
};

/** 后台通用「全部网盘」下拉选项 */
export const DISCOVERY_PROVIDER_OPTIONS: Array<{ value: string; label: string }> = [
  { value: "", label: "全部网盘" },
  { value: "123", label: DISCOVERY_PROVIDER_LABELS["123"] },
  { value: "guangya", label: DISCOVERY_PROVIDER_LABELS.guangya },
  { value: "pan139", label: DISCOVERY_PROVIDER_LABELS.pan139 },
];

export function providerLabel(key?: string): string {
  if (!key) return "-";
  return DISCOVERY_PROVIDER_LABELS[key] ?? key;
}

// ------------------------------ 订阅追更 ------------------------------

/** 订阅（对齐 discovery.DiscoverySubscription） */
export interface DiscoverySubscription {
  id: number;
  entity_key: string;
  source: string;
  entity_type: string;
  external_id: string;
  tmdb_id: number;
  media_type: string;
  title: string;
  original_title: string;
  /** 库里字段名是 poster，但写入时必须走 poster_url（见 SubscriptionUpsertPayload） */
  poster: string;
  target_provider: string;
  transfer_mode: string;
  enabled: boolean;
  interval_minutes: number;
  /** pending / running / success / partial / failed / no_update */
  status: string;
  last_checked_at: string | null;
  next_check_at: string | null;
  created_at: string;
  updated_at: string;
  /** 非持久化：后端把 JSON 列解析成对象返回 */
  preferences?: Record<string, unknown>;
  metadata?: Record<string, unknown>;
  rules?: Array<Record<string, unknown>>;
}

/**
 * 订阅创建 / 更新载荷。
 *
 * ★ 键名陷阱：模型字段叫 Poster，但后端 SubscriptionUpsertPayload 的 JSON tag 是
 *   `poster_url`（internal/discover/discovery/subscriptions.go:202）。传 `poster`
 *   会被 encoding/json 静默忽略，订阅列表里就没海报 —— 调用方必须显式映射。
 *
 * 服务端约束：title 必填；external_id 与 tmdb_id 至少一个非空；
 * interval_minutes 缺省 360 并被夹到 [15,10080]。
 */
export interface SubscriptionUpsertPayload {
  source: string;
  entity_type: string;
  external_id?: string;
  tmdb_id?: number;
  media_type?: string;
  title: string;
  original_title?: string;
  poster_url?: string;
  target_provider?: string;
  transfer_mode?: string;
  enabled?: boolean;
  interval_minutes?: number;
  preferences?: Record<string, unknown>;
  rules?: Array<Record<string, unknown>>;
  metadata?: Record<string, unknown>;
}

/** 订阅执行轮次 */
export interface SubscriptionRun {
  id: number;
  subscription_id: number | null;
  /** scheduled / manual / emby_missing_manual */
  trigger_type: string;
  /** running / success / partial / failed / no_update */
  status: string;
  resource_count: number;
  selected_count: number;
  transferred_count: number;
  message: string;
  started_at: string;
  finished_at: string | null;
}

/** 订阅事件流（步骤级留痕，排查"为什么没转存"用） */
export interface SubscriptionEvent {
  id: number;
  subscription_id: number | null;
  run_id: number | null;
  item_id: number | null;
  event_type: string;
  status: string;
  message: string;
  created_at: string;
}

/** 订阅候选条目（资源级明细） */
export interface SubscriptionItem {
  id: number;
  subscription_id: number;
  item_key: string;
  run_id: number | null;
  provider: string;
  slug: string;
  title: string;
  /** discovered / selected / transferring / transferred / failed / skipped */
  status: string;
  first_seen_at: string;
  last_seen_at: string;
  transferred_at: string | null;
}

/** 订阅列表（enabled 传 undefined = 全部） */
export function fetchSubscriptions(enabled?: boolean) {
  return http.get<{ items: DiscoverySubscription[] }>("/admin/discovery/subscriptions", { enabled });
}

export function fetchSubscription(id: number) {
  return http.get<{ item: DiscoverySubscription }>(`/admin/discovery/subscriptions/${id}`);
}

/** 按 entity_key 反查订阅（详情弹窗判断"是否已订阅"用） */
export function fetchSubscriptionByKey(entityKey: string) {
  return http.get<{ item: DiscoverySubscription | null; subscribed: boolean }>(
    "/admin/discovery/subscriptions/by-key",
    { entity_key: entityKey },
  );
}

/**
 * 创建 / 更新订阅。
 * ★ 返回的 warning 非空时必须 toast.warning 提示 —— 典型场景是保存目录未配置，
 *   订阅能建成功但转存必然失败，不提示的话用户会以为一切正常。
 */
export function saveSubscription(payload: SubscriptionUpsertPayload) {
  return http.post<{ item: DiscoverySubscription; warning: string }>(
    "/admin/discovery/subscriptions",
    payload,
  );
}

export function deleteSubscription(id: number) {
  return http.del<Record<string, unknown>>(`/admin/discovery/subscriptions/${id}`);
}

export function toggleSubscription(id: number, enabled?: boolean) {
  return http.post<{ item: DiscoverySubscription }>(`/admin/discovery/subscriptions/${id}/toggle`, {
    enabled,
  });
}

/**
 * 立即执行一次订阅检查。
 * ★ 同步阻塞接口：后端要跑完整的搜索 + 筛选 + 转存流程，调用方必须给按钮加
 *   loading 态并防止连点，否则并发触发会重复转存。
 */
export function runSubscription(id: number) {
  return http.post<{ run: SubscriptionRun }>(`/admin/discovery/subscriptions/${id}/run`);
}

export function fetchSubscriptionRuns(id: number, limit = 20) {
  return http.get<{ items: SubscriptionRun[] }>(`/admin/discovery/subscriptions/${id}/runs`, { limit });
}

export function fetchSubscriptionEvents(id: number, limit = 50) {
  return http.get<{ items: SubscriptionEvent[] }>(`/admin/discovery/subscriptions/${id}/events`, {
    limit,
  });
}

export function fetchSubscriptionItems(id: number, status?: string, limit = 100) {
  return http.get<{ items: SubscriptionItem[] }>(`/admin/discovery/subscriptions/${id}/items`, {
    status,
    limit,
  });
}

/** 批量执行全部到期订阅（后端默认最多取 5 条，同样同步阻塞） */
export function runDueSubscriptions(limit = 5) {
  return http.post<{ items: SubscriptionRun[] }>(
    "/admin/discovery/subscriptions/run-due",
    undefined,
    { limit },
  );
}

// ------------------------------ 频道订阅 ------------------------------

/**
 * TG 公开频道订阅。
 * channel 存的是**归一化后**的频道名（后端 ChannelName() 依次剥掉
 * https://t.me/s/、https://t.me/、t.me/s/、t.me/、@ 前缀与尾部 /），
 * 唯一索引建在 (source_type, channel) 上 —— 所以用户粘链接和手输 @名
 * 都能被去重拦下，也让"自动归一化"的提示文案有意义。
 */
export interface DiscoveryChannel {
  id: number;
  source_type: string;
  channel: string;
  enabled: boolean;
  /** 增量游标；空串表示尚未初始化，下一轮会从头回溯 */
  last_post_id: string;
  last_run_at: string;
  created_at: string;
  updated_at: string;
}

/**
 * 频道帖内的网盘分享链接。
 *
 * ★ tgchannel 的 ShareLink / ChannelPost 结构体**没有任何 JSON tag**
 *   （internal/discover/tgchannel/tgchannel_parse.go:16-40），
 *   所以线上走 Go 默认字段名（PascalCase），不是 snake_case。
 *
 * ★ 提取码陷阱：`ShareLink.FullURL()` 是**方法**，不会被序列化，
 *   因此 `URL` 里**不含** `?pwd=`，提取码只在 `Pwd`。
 *   前端拼展示链接时必须自己补 `?pwd=xxx`，否则用户拿到的是缺码死链。
 */
export interface ChannelShareLink {
  URL: string;
  Pwd: string;
  /** 123 / guangyapan / pan139 */
  Type: string;
}

/** 频道帖子（Time 是 Go time.Time，序列化为 RFC3339 字符串） */
export interface ChannelPost {
  PostID: string;
  Time: string;
  Text: string;
  Links: ChannelShareLink[];
}

/**
 * 拼出可直接打开的分享链接（补上提取码）。
 * 后端 FullURL() 不会随 JSON 下发，所以这一步必须在前端做。
 */
export function channelLinkFullURL(link: ChannelShareLink): string {
  if (!link?.URL) return "";
  if (!link.Pwd) return link.URL;
  const sep = link.URL.includes("?") ? "&" : "?";
  return `${link.URL}${sep}pwd=${link.Pwd}`;
}

export function fetchChannels(sourceType?: string) {
  return http.get<{ items: DiscoveryChannel[] }>("/admin/discovery/channels", {
    source_type: sourceType,
  });
}

/**
 * 新增 / 更新频道（id 为 0 或缺省则新建）。
 * channel 支持 @name、t.me/s/xxx 或裸名，后端统一归一化后再落库。
 */
export function saveChannel(payload: {
  id?: number;
  source_type: string;
  channel: string;
  enabled?: boolean;
}) {
  return http.post<{ item: DiscoveryChannel }>("/admin/discovery/channels", payload);
}

export function deleteChannel(id: number) {
  return http.del<Record<string, unknown>>(`/admin/discovery/channels/${id}`);
}

/** 启用 / 停用频道（enabled 不传时后端取反当前值） */
export function toggleChannel(id: number, enabled?: boolean) {
  return http.post<{ enabled: boolean }>(`/admin/discovery/channels/${id}/toggle`, { enabled });
}

/**
 * 重置增量游标（last_post_id 清空）。
 * ★ 会带来重复转存的真实风险：下一轮从头回溯抓帖，历史帖里的资源
 *   会被重新解析一遍，调用方必须二次确认，文案要讲清这个后果。
 */
export function resetChannelCursor(id: number) {
  return http.post<Record<string, unknown>>(`/admin/discovery/channels/${id}/reset-cursor`);
}

/**
 * 预览频道最新帖（只读，不改游标、不转存）。
 * channel 可传 @name / t.me/s/xxx / 裸名；limit 超出 (0,50] 时后端回落 10。
 * 抓取失败时后端直接返回错误响应，按 getApiErrorMessage 处理即可。
 */
export function previewChannel(channel: string, limit = 10) {
  return http.get<{ items: ChannelPost[] }>("/admin/discovery/channels/preview", { channel, limit });
}

/** 立即执行一轮全部频道抓取（source_type 为空 = 所有网盘） */
export function runChannelsNow(sourceType?: string) {
  return http.post<{ summary: string }>("/admin/discovery/channels/run-now", {
    source_type: sourceType,
  });
}

// ------------------------------ 监控历史 ------------------------------

/**
 * 转存状态是**中文常量**（discovery/channels.go:315-318 的
 * MonitorStatusSuccess / Failed / Skipped / Wash），筛选下拉直接用这些字面量。
 */
export const MONITOR_STATUSES = ["转存成功", "转存失败", "已跳过", "洗版替换"] as const;
export type MonitorStatus = (typeof MONITOR_STATUSES)[number];

/** 监控历史筛选下拉选项 */
export const MONITOR_STATUS_OPTIONS: Array<{ value: string; label: string }> = [
  { value: "", label: "全部状态" },
  ...MONITOR_STATUSES.map((s) => ({ value: s, label: s })),
];

/** 监控历史（记录每一次转存尝试，含失败与跳过；区别于只记成功的转存记录表） */
export interface DiscoveryMonitorRecord {
  id: number;
  source_type: string;
  /** channel=TG频道订阅 / hive=RE0订阅 / bot=TG机器人 */
  entry: string;
  channel: string;
  message_id: string;
  message_url: string;
  target_url: string;
  /** 转存成功 / 转存失败 / 已跳过 / 洗版替换 */
  transfer_status: string;
  transfer_time: string;
  transfer_result: string;
  title: string;
  total: number;
  target_dir: string;
  subscription_id: number;
  tmdb_id: number;
  media_type: string;
  season: string;
  episode: string;
  created_at: string;
}

/** 各来源网盘的记录数（后端返回 map[source_type]count） */
export type MonitorRecordSources = Record<string, number>;

export function fetchMonitorRecords(params: {
  source_type?: string;
  status?: string;
  keyword?: string;
  page?: number;
  page_size?: number;
}) {
  return http.get<{
    items: DiscoveryMonitorRecord[];
    total: number;
    page: number;
    page_size: number;
  }>("/admin/discovery/monitor-records", params);
}

export function fetchMonitorRecordSources() {
  return http.get<{ items: MonitorRecordSources }>("/admin/discovery/monitor-records/sources");
}

export function deleteMonitorRecords(ids: number[]) {
  return http.post<{ deleted: number }>("/admin/discovery/monitor-records/delete", { ids });
}

/** 清空监控历史；start / end 必须是 RFC3339 字符串（留空 = 不限） */
export function clearMonitorRecords(payload: { source_type?: string; start?: string; end?: string }) {
  return http.post<{ deleted: number }>("/admin/discovery/monitor-records/clear", payload);
}

// ------------------------------ Emby 缺集补档 ------------------------------

/** Emby 缺集总览（后端直接透出 map，字段按需读取） */
export interface EmbyMissingStatus {
  emby?: {
    configured?: boolean;
    enabled?: boolean;
    server_url?: string;
    message?: string;
  };
  active_scan?: EmbyMissingScan | null;
  latest_scan?: EmbyMissingScan | null;
  subscription_count?: number;
  settings?: {
    auto_scan?: boolean;
    scan_interval_minutes?: number;
    auto_create_subscriptions?: boolean;
  };
}

/** Emby 媒体库（id/name 为主，其余字段后端可能扩展） */
export interface EmbyLibrary {
  id?: string;
  name?: string;
  [key: string]: unknown;
}

/** 一次缺集扫描 */
export interface EmbyMissingScan {
  id: number;
  /** queued / running / success / partial / failed */
  status: string;
  phase?: string;
  total_series?: number;
  scanned_series?: number;
  missing_series?: number;
  missing_episodes?: number;
  error_series?: number;
  message?: string;
  created_at?: string;
  started_at?: string | null;
  finished_at?: string | null;
}

/** 单集缺集明细 */
export interface EmbyMissingEpisode {
  key: string;
  season: number;
  episode: number;
  name?: string;
  premiere_date?: string;
}

/** 一部剧的缺集结果 */
export interface EmbyMissingResult {
  id: number;
  scan_id: number;
  series_key: string;
  emby_series_id: string;
  library_id: string;
  library_name: string;
  tmdb_id: number;
  title: string;
  original_title: string;
  production_year: number;
  series_status: string;
  available_count: number;
  missing_count: number;
  subscription_id: number | null;
  created_at: string;
  updated_at: string;
  /** 非持久化：后端把 JSON 列解析成数组返回 */
  missing_episodes?: EmbyMissingEpisode[];
}

/** 缺集扫描事件流 */
export interface EmbyMissingEvent {
  id: number;
  scan_id: number | null;
  result_id: number | null;
  subscription_id: number | null;
  event_type: string;
  status: string;
  message: string;
  created_at: string;
}

/**
 * 为选中缺集批量创建补档订阅。
 * ★ transfer_mode 的合法取值 Go 侧未做白名单校验（模型注释只写了 "auto"），
 *   前端目前传 "auto"，待后端确认后收紧。
 */
export interface EmbyMissingSubscriptionPayload {
  result_ids: number[];
  scan_id: number;
  target_provider: string;
  transfer_mode: string;
  interval_minutes: number;
  enabled: boolean;
}

export function fetchEmbyMissingStatus() {
  return http.get<EmbyMissingStatus>("/admin/discovery/emby-missing/status");
}

export function fetchEmbyMissingLibraries() {
  return http.get<{ items: EmbyLibrary[] }>("/admin/discovery/emby-missing/libraries");
}

/** 启动一次缺集扫描（library_ids 为空 = 全部电视剧库） */
export function startEmbyMissingScan(libraryIds: string[]) {
  return http.post<{ scan: EmbyMissingScan }>("/admin/discovery/emby-missing/scan", {
    library_ids: libraryIds,
  });
}

export function fetchEmbyMissingScans(limit = 20) {
  return http.get<{ items: EmbyMissingScan[] }>("/admin/discovery/emby-missing/scans", { limit });
}

export function fetchEmbyMissingResults(scanId: number, limit = 200) {
  return http.get<{ items: EmbyMissingResult[] }>(
    `/admin/discovery/emby-missing/scans/${scanId}/results`,
    { limit },
  );
}

export function fetchEmbyMissingEvents(scanId: number, limit = 100) {
  return http.get<{ items: EmbyMissingEvent[] }>(
    `/admin/discovery/emby-missing/scans/${scanId}/events`,
    { limit },
  );
}

export function createEmbyMissingSubscriptions(payload: EmbyMissingSubscriptionPayload) {
  return http.post<Record<string, unknown>>("/admin/discovery/emby-missing/subscriptions", payload);
}
