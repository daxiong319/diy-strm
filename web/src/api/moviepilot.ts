import { http } from "./client";

// ---------- 类型定义（与后端 internal/api/moviepilot.go 字段一一对应） ----------

/** MoviePilot 全局设置。 */
export interface MoviePilotConfig {
  id: number;
  enabled: boolean;
  base_url: string;
  api_token: string;
  download_root: string;
  local_view_root: string;
  upload_account_id: number;
  upload_root: string;
  upload_root_id: string;
  strm_local_dir: string;
  poll_interval: number;
  notify_enabled: boolean;
  category_config: string;
  promotion_order: string;
  promotion_patience_hours: number;
  seed_retention_hours: number;
  qbittorrent_url: string;
  qbittorrent_user: string;
  qbittorrent_pass: string;
  created_at?: string;
  updated_at?: string;
}

/** 设置更新入参：字段均可选，未提交的字段保持原值。 */
export type MoviePilotConfigInput = Partial<
  Pick<
    MoviePilotConfig,
    | "enabled"
    | "base_url"
    | "api_token"
    | "download_root"
    | "local_view_root"
    | "upload_account_id"
    | "upload_root"
    | "upload_root_id"
    | "strm_local_dir"
    | "poll_interval"
    | "notify_enabled"
    | "category_config"
    | "promotion_order"
    | "promotion_patience_hours"
    | "seed_retention_hours"
    | "qbittorrent_url"
    | "qbittorrent_user"
    | "qbittorrent_pass"
  >
>;

/** 连接测试入参：留空则使用已保存的配置。 */
export interface MoviePilotTestInput {
  base_url?: string;
  api_token?: string;
}

/** MoviePilot 订阅项。 */
export interface MoviePilotSubscribe {
  id: number;
  name: string;
  year: string | number | null;
  /** movie / tv */
  type: string;
  keyword: string;
  tmdbid: number;
  season: number;
  total_episode: number;
  lack_episode: number;
  /** R-订阅中 P-完成 S-停止 */
  state: string;
  save_path: string;
  sites: number[];
  poster: string;
  media_source: string;
  media_id: string;
  include: string;
}

/** 新建订阅入参。 */
export interface MoviePilotCreateSubscribeInput {
  name: string;
  year?: string;
  /** movie / tv */
  type: string;
  tmdbid?: number;
  season?: number;
  total_episode?: number;
  save_path?: string;
  sites?: number[];
  include?: string;
}

/** MoviePilot 下载器任务。 */
export interface MoviePilotDownload {
  hash: string;
  title: string;
  name: string;
  year: string;
  season_episode: string;
  path: string;
  save_path: string;
  content_path: string;
  state: string;
  /** MoviePilot 侧为 0~100 */
  progress: number;
  category: string;
  media: Record<string, unknown>;
}

/** 上传任务。 */
export interface MoviePilotUploadTask {
  id: number;
  torrent_hash: string;
  title: string;
  media_type: string;
  tmdb_id: number;
  season: string;
  local_path: string;
  remote_path: string;
  status: string;
  total_files: number;
  uploaded_files: number;
  total_bytes: number;
  uploaded_bytes: number;
  total_size_text: string;
  error?: string;
  empty_source_since?: string;
  is_running: boolean;
  created_at?: string;
  updated_at?: string;
}

/** 识别失败文件。 */
export interface MoviePilotFailedFile {
  id: number;
  task_id: number;
  file_name: string;
  root_path: string;
  status: string;
  media_type: string;
  title: string;
  tmdb_id: number;
  year: number;
  season: number;
  reason: string;
  created_at?: string;
  updated_at?: string;
}

/** 文件质量线索（后端 FileQuality，键为 JSON tag）。 */
export interface MoviePilotFileQuality {
  resolution: number;
  res_tag: string;
  codec: string;
  codec_tag: string;
  video_format: string;
  bitdepth: string;
  hdr: string;
  audio_tag: string;
  channels: number;
  edition: string;
  customization: string;
  group: string;
  tags: string;
}

/**
 * 失败文件重新识别结果。
 * 后端 IdentifyResult 已显式声明 snake_case json tag，与此处字段一一对应。
 */
export interface MoviePilotIdentifyResult {
  category: string;
  title: string;
  season: number;
  episode: number;
  year: number;
  tmdb_id: number;
  ai_quality?: MoviePilotFileQuality;
}

/** 失败文件重新整理入参。 */
export interface MoviePilotResolveFailedInput {
  media_type: string;
  title: string;
  year: number;
  season: number;
  tmdb_id: number;
}

/** 失败文件重新整理结果。 */
export interface MoviePilotResolveFailedResult {
  target_dir: string;
  new_name: string;
  title: string;
  tmdb_id: number;
}

/** 整理历史。 */
export interface MoviePilotOrganizeHistory {
  id: number;
  account_id: number;
  task_id: number;
  file_name: string;
  source_path: string;
  target_path: string;
  media_type: string;
  title: string;
  year: number;
  season_num: number;
  episode_num: number;
  tmdb_id: number;
  status: string;
  message: string;
  created_at?: string;
}

/** 统一分页响应。 */
export interface MoviePilotPage<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
}

// ---------- 设置 ----------

export function fetchMoviePilotSetting() {
  return http.get<MoviePilotConfig>("/admin/moviepilot/setting");
}

export function updateMoviePilotSetting(body: MoviePilotConfigInput) {
  return http.put<MoviePilotConfig>("/admin/moviepilot/setting", body);
}

export function testMoviePilotConnection(body: MoviePilotTestInput = {}) {
  return http.post<{ message: string }>("/admin/moviepilot/setting/test", body);
}

// ---------- 订阅 ----------

export function fetchMoviePilotSubscribes() {
  return http.get<MoviePilotSubscribe[]>("/admin/moviepilot/subscribes");
}

export function createMoviePilotSubscribe(body: MoviePilotCreateSubscribeInput) {
  return http.post<{ id: number }>("/admin/moviepilot/subscribes", body);
}

export function searchMoviePilotSubscribe(id: number) {
  return http.post<{ message: string }>(`/admin/moviepilot/subscribes/${id}/search`, {});
}

export function deleteMoviePilotSubscribe(id: number) {
  return http.del<{ message: string }>(`/admin/moviepilot/subscribes/${id}`);
}

export function updateMoviePilotSubscribeStatus(id: number, state: string) {
  return http.put<{ message: string }>(`/admin/moviepilot/subscribes/${id}/status`, { state });
}

// ---------- 下载 ----------

export function fetchMoviePilotDownloads() {
  return http.get<MoviePilotDownload[]>("/admin/moviepilot/downloads");
}

// ---------- 上传任务 ----------

export function fetchMoviePilotUploadTasks(page = 1, pageSize = 20, status = "") {
  const query: Record<string, string | number> = { page, page_size: pageSize };
  if (status) query.status = status;
  return http.get<MoviePilotPage<MoviePilotUploadTask>>("/admin/moviepilot/upload-tasks", query);
}

export function retryMoviePilotUploadTask(id: number) {
  return http.post<{ queued: boolean }>(`/admin/moviepilot/upload-tasks/${id}/retry`, {});
}

export function cancelMoviePilotUploadTask(id: number) {
  return http.post<{ message: string }>(`/admin/moviepilot/upload-tasks/${id}/cancel`, {});
}

// ---------- 失败文件 ----------

export function fetchMoviePilotFailedFiles(page = 1, pageSize = 20, status = "") {
  const query: Record<string, string | number> = { page, page_size: pageSize };
  if (status) query.status = status;
  return http.get<MoviePilotPage<MoviePilotFailedFile>>("/admin/moviepilot/failed-files", query);
}

export function identifyMoviePilotFailedFile(id: number) {
  return http.post<MoviePilotIdentifyResult>(`/admin/moviepilot/failed-files/${id}/identify`, {});
}

export function resolveMoviePilotFailedFile(id: number, body: MoviePilotResolveFailedInput) {
  return http.post<MoviePilotResolveFailedResult>(`/admin/moviepilot/failed-files/${id}/resolve`, body);
}

export function skipMoviePilotFailedFile(id: number) {
  return http.post<{ message: string }>(`/admin/moviepilot/failed-files/${id}/skip`, {});
}

// ---------- 整理历史 ----------

export function fetchMoviePilotOrganizeHistory(limit = 50) {
  return http.get<MoviePilotOrganizeHistory[]>("/admin/moviepilot/organize-history", { limit });
}
