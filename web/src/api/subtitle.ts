import { http } from "./client";

// 字幕智能处理：搜索下载 + 智能匹配 + 时间轴校正。
//
// 所有端点都挂在 /api 前缀下、由 requireAdmin 保护（对应后端
// internal/api/subtitle.go 的 RegisterSubtitleRoutes）。与 mcp.ts 同一约定：
// 该组挂在 /api 而非 /api/admin，base 不能写成 "/admin/subtitle"，
// 否则所有请求都会 404（router_test.go 有路由存在性测试锁死）。

export interface SubtitleProviderStatus {
  name: string;
  display_name: string;
  enabled: boolean;
  configured: boolean;
  healthy: boolean;
  message: string;
  latency_ms: number;
}

export interface SubtitleConfig {
  enabled: boolean;

  assrt_enabled: boolean;
  assrt_api_key_set: boolean;
  subhd_enabled: boolean;
  subhd_cookie_set: boolean;
  zimuku_enabled: boolean;
  zimuku_cookie_set: boolean;
  opensubtitles: {
    enabled: boolean;
    api_key_set: boolean;
    username: string;
    password_set: boolean;
    user_agent: string;
  };

  language_priority: string;
  format_priority: string;

  auto_match: boolean;
  auto_download: boolean;
  min_match_score: number;

  auto_sync: boolean;
  sync_mode: string;
  sync_dry_run: boolean;
  sync_min_confidence: number;

  target_dir_policy: string;
  keep_original: boolean;
  overwrite: boolean;
  concurrency: number;
  timeout_seconds: number;
}

/** 配置更新体：只发要改的字段（后端按字段是否出现做部分更新）。 */
export type SubtitleConfigPatch = Partial<{
  enabled: boolean;
  assrt_enabled: boolean;
  assrt_api_key: string;
  subhd_enabled: boolean;
  subhd_cookie: string;
  zimuku_enabled: boolean;
  zimuku_cookie: string;
  opensubtitles_enabled: boolean;
  opensubtitles_api_key: string;
  opensubtitles_username: string;
  opensubtitles_password: string;
  language_priority: string;
  format_priority: string;
  auto_match: boolean;
  auto_download: boolean;
  min_match_score: number;
  auto_sync: boolean;
  sync_mode: string;
  sync_dry_run: boolean;
  sync_min_confidence: number;
  target_dir_policy: string;
  keep_original: boolean;
  overwrite: boolean;
  concurrency: number;
  timeout_seconds: number;
}>;

export interface SubtitleCandidate {
  provider: string;
  slug: string;
  title: string;
  language: string;
  format: string;
  hash_matched: boolean;
  release_group: string;
  download_url: string;
  page_url: string;
  publisher: string;
  rating: number;
  download_count: number;
  file_name: string;
}

export interface SubtitleScoreBreakdown {
  release: number;
  title: number;
  language: number;
  year: number;
  season_episode: number;
  format: number;
  popularity: number;
  total: number;
}

export interface SubtitleScoredCandidate {
  candidate: SubtitleCandidate;
  score: number;
  breakdown: SubtitleScoreBreakdown;
  reasons: string[] | null;
  penalties: string[] | null;
}

export interface SubtitleProviderError {
  provider: string;
  message: string;
}

export interface SubtitleSearchPayload {
  title?: string;
  original_title?: string;
  year?: number;
  season?: number;
  episode?: number;
  media_type?: string;
  tmdb_id?: number;
  imdb_id?: string;
  languages?: string[];
  video_path?: string;
  video_file_name?: string;
  video_hash?: string;
  auto_parse_path?: boolean;
}

export interface SubtitleTask {
  id: number;
  media_id: number;
  video_path: string;
  subtitle_path: string;
  status: string;
  title: string;
  year: number;
  season: number;
  episode: number;
  media_type: string;
  provider: string;
  candidate_slug: string;
  match_score: number;
  match_reason: string;
  sync_offset_ms: number;
  sync_scale: number;
  sync_confidence: number;
  sync_applied: boolean;
  error_message: string;
  duration_ms: number;
  created_at: string;
  updated_at: string;
}

export interface SubtitleSyncResult {
  applied: boolean;
  dry_run: boolean;
  mode: string;
  offset_ms: number;
  scale: number;
  confidence: number;
  speech_ratio: number;
  warnings: string[] | null;
  backup_path?: string;
}

export interface SubtitleSyncCheck {
  available: boolean;
  ffmpeg: { available: boolean; message: string };
  ffprobe: { available: boolean; message: string };
}

// 注意：字幕端点挂在 /api/subtitle 下，没有 /admin 段。
// router.go 里 RegisterSubtitleRoutes 虽然在 requireAdmin 组内，但该组挂在
// /api 分组下（与 RegisterMcpAdminRoutes 同级），而不是 /api/admin。
// 写成 "/admin/subtitle" 会让所有请求打到 /api/admin/subtitle/* 并得到 404。
const base = "/subtitle";

export const subtitleApi = {
  getConfig: () => http.get<SubtitleConfig>(`${base}/config`),
  updateConfig: (patch: SubtitleConfigPatch) => http.put<SubtitleConfig>(`${base}/config`, patch),

  providers: () => http.get<{ providers: SubtitleProviderStatus[] }>(`${base}/providers`),
  testProvider: (name: string) =>
    http.post<{ providers: SubtitleProviderStatus[] }>(`${base}/providers/test`, { name }),

  search: (payload: SubtitleSearchPayload) =>
    http.post<{
      results: SubtitleScoredCandidate[];
      provider_errors: SubtitleProviderError[] | null;
      total: number;
    }>(`${base}/search`, payload),

  match: (payload: SubtitleSearchPayload & { candidates: SubtitleCandidate[] }) =>
    http.post<{ results: SubtitleScoredCandidate[]; total: number }>(`${base}/match`, payload),

  download: (payload: {
    provider?: string;
    candidate: SubtitleCandidate;
    video_path: string;
    subtitle_path?: string;
    format?: string;
    overwrite?: boolean;
    auto_sync?: boolean;
  }) => http.postWithTimeout<{ task: SubtitleTask }>(`${base}/download`, payload, 10 * 60_000),

  sync: (payload: {
    video_path: string;
    subtitle_path: string;
    mode?: string;
    dry_run?: boolean;
    keep_original?: boolean;
    min_confidence?: number;
  }) => http.postWithTimeout<{ result: SubtitleSyncResult }>(`${base}/sync`, payload, 30 * 60_000),

  syncCheck: () => http.post<SubtitleSyncCheck>(`${base}/sync/check`),

  tasks: (query: { page?: number; page_size?: number; status?: string }) =>
    http.get<{
      tasks: SubtitleTask[];
      total: number;
      page: number;
      page_size: number;
      message?: string;
    }>(`${base}/tasks`, query),

  retryTask: (id: number) => http.postWithTimeout<{ task: SubtitleTask }>(
    `${base}/tasks/${id}/retry`, undefined, 10 * 60_000,
  ),

  deleteTask: (id: number) => http.del<{ deleted: boolean; id: number }>(`${base}/tasks/${id}`),
};
