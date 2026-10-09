import { http } from "./client";

export interface MediaOrganizeTaskConfig {
  target_directory?: string;
  target_directory_id?: string;
  action_type?: string;
  target_root?: string;
  target_root_id?: string;
  media_type?: string;
  rename_marker?: string;
  use_tmdb?: boolean;
  overwrite_existing?: boolean;
  recursive?: boolean;
  account_id?: string | number;
}

export interface MediaOrganizeTask {
  id: string;
  task_name: string;
  account_id: number;
  config: MediaOrganizeTaskConfig;
  status: string;
  last_run_at?: string;
  last_run_result?: MediaOrganizeRunResult;
  created_at?: string;
  updated_at?: string;
  is_running?: boolean;
}

export interface MediaOrganizeRunResult {
  total?: number;
  renamed?: number;
  moved?: number;
  skipped?: number;
  normal_skipped?: number;
  abnormal_skipped?: number;
  failed?: number;
  pending?: number;
  stopped?: boolean;
}

export interface MediaOrganizePlanAction {
  id: string;
  kind: string;
  source_id?: string;
  source_name?: string;
  source_parent_id?: string;
  target_parent_id?: string;
  target_name?: string;
  reason?: string;
  confidence?: number;
  metadata?: Record<string, unknown>;
  status?: string;
  error?: string;
}

export interface MediaOrganizePlan {
  task_id?: string;
  created_at?: string;
  target_root_id?: string;
  target_parent_id?: string;
  actions?: MediaOrganizePlanAction[];
  skipped?: Array<Record<string, unknown>>;
  diagnostics?: Record<string, unknown>;
}

export interface MediaOrganizeProgress {
  stage?: string;
  scanned_dirs?: number;
  scanned_files?: number;
  groups?: number;
  actions?: number;
  skipped?: number;
  current_dir?: string;
  planned_works?: number;
  max_works?: number;
  quota_reached?: boolean;
  ai_total?: number;
  ai_completed?: number;
  ai_cached?: number;
  ai_failed?: number;
  ai_chunk?: number;
  ai_chunks?: number;
  ai_batch_size?: number;
  ai_split_depth?: number;
  ai_attempt_started_at?: number;
  ai_attempt_timeout_seconds?: number;
  ai_retrying?: boolean;
}

export interface MediaOrganizeLogEntry {
  time: string;
  message: string;
}

export interface MediaOrganizeSettings {
  proxy_enabled: boolean;
  proxy_url: string;
  proxy_username: string;
  proxy_password: string;
  tmdb_api_key: string;
  tmdb_language: string;
  tmdb_api_host: string;
  tmdb_image_host: string;
  tmdb_proxy_url: string;
  api_request_interval_ms: number;
  tmdb_request_interval_ms: number;
  file_extensions: string;
  metadata_extensions: string;
  media_tag_order: string[] | string;
  align_media_tags: boolean;
  max_works_per_run: number;
  overwrite_existing: boolean;
  /** T14：整理完成后自动生成 NFO 与海报（社区通用格式）。 */
  scrape_nfo_enabled: boolean;
  /** T14：NFO 与海报写本地目录还是网盘。 */
  scrape_nfo_target: string;
  /** T14：识别不出标题的文件往哪放（三种填法）。 */
  scrape_unrecognized_dir: string;
  /** T14：开启后先向 Emby 反查作品已有位置（只支持 Emby，需在线）。 */
  scrape_follow_existing_location: boolean;
  /** T14：识别失败的文件留源目录（keep）还是移到兜底目录（move）。 */
  scrape_skip_action: string;
  /** T14：备份恢复目标：local 或 cloud。 */
  backup_target: string;
  /**
   * T15：风控熔断阈值。
   *
   * 这一组值决定「整理/刮削什么时候被强制暂停」，直接关系到网盘账号会不会被限流或封禁，
   * 所以在 UI 上必须连同解释一起出现，不给「一个孤零零的数字输入框」。
   */
  /** 统计窗口内累计到这么多次网盘调用就暂停，0 = 不限。 */
  scrape_max_calls_per_window: number;
  /** 调用次数的统计窗口（秒）。 */
  scrape_call_window_seconds: number;
  /** 调用触顶后暂停多久（秒，后端会截到 86400）。 */
  scrape_call_pause_seconds: number;
  /** 一轮连续整理超过这么多分钟就暂停，0 = 不限。 */
  scrape_max_work_minutes: number;
  /** 整理时长触顶后暂停多久（分钟，后端会截到 1440）。 */
  scrape_work_pause_minutes: number;
  /**
   * T15：媒体文件最小体积（字节），0 = 不启用。
   * 注意语义：是「移到隔离目录」，不是删除。
   */
  min_media_size_bytes: number;
  /** 小文件隔离目录（网盘路径），留空用整理根下的「_隔离」。 */
  quarantine_dir: string;
  /**
   * T15：小文件隔离的知情确认。
   * 没有它，min_media_size_bytes 只会让整理把文件标成 skipped —— 宁可不做，
   * 也不在用户没意识到的情况下把文件搬走。
   */
  small_file_acked: boolean;
}

export type MediaOrganizeTaskInput = {
  task_name: string;
  account_id: number;
  target_directory: string;
  target_directory_id: string;
  action_type: string;
  target_root?: string;
  target_root_id?: string;
  media_type: string;
  rename_marker?: string;
  use_tmdb: boolean;
  overwrite_existing?: boolean;
  recursive?: boolean;
};

export function fetchMediaOrganizeTasks() {
  return http.get<MediaOrganizeTask[]>("/admin/media-organize/tasks");
}

export function createMediaOrganizeTask(input: MediaOrganizeTaskInput) {
  return http.post<MediaOrganizeTask>("/admin/media-organize/tasks", input);
}

export function updateMediaOrganizeTask(id: string, input: Partial<MediaOrganizeTaskInput>) {
  return http.put<MediaOrganizeTask>(`/admin/media-organize/tasks/${id}`, input);
}

export function deleteMediaOrganizeTask(id: string) {
  return http.del<{ id: string; stopping?: boolean }>(`/admin/media-organize/tasks/${id}`);
}

export interface MediaOrganizePlanResult {
  plan: MediaOrganizePlan;
  summary?: { actions?: number; skipped?: number };
}

export function planMediaOrganizeTask(id: string) {
  return http.postWithTimeout<MediaOrganizePlanResult>(
    `/admin/media-organize/tasks/${id}/plan`,
    undefined,
    2 * 60 * 60 * 1000,
  );
}

export function fetchMediaOrganizePlan(id: string) {
  return http.get<MediaOrganizePlan>(`/admin/media-organize/tasks/${id}/plan`);
}

export function applyMediaOrganizeTask(id: string) {
  return http.post<Record<string, unknown>>(`/admin/media-organize/tasks/${id}/apply`);
}

export function stopMediaOrganizeTask(id: string) {
  return http.post<{ stopping: boolean }>(`/admin/media-organize/tasks/${id}/stop`);
}

export function fetchMediaOrganizeLogs(id: string) {
  return http.get<{
    logs: MediaOrganizeLogEntry[];
    status: string;
    last_run_result?: MediaOrganizeRunResult;
  }>(`/admin/media-organize/tasks/${id}/logs`);
}

export function fetchMediaOrganizeProgress(id: string) {
  return http.get<MediaOrganizeProgress>(`/admin/media-organize/tasks/${id}/progress`);
}

export function updateMediaOrganizePlanAction(taskId: string, actionId: string, targetName: string) {
  return http.put<{ action?: MediaOrganizePlanAction; changed?: boolean }>(
    `/admin/media-organize/tasks/${taskId}/plan/actions/${actionId}`,
    { target_name: targetName },
  );
}

export function deleteMediaOrganizePlanAction(taskId: string, actionId: string) {
  return http.del<{ removed?: string }>(`/admin/media-organize/tasks/${taskId}/plan/actions/${actionId}`);
}

export function batchDeleteMediaOrganizePlanActions(taskId: string, actionIds: string[]) {
  return http.post<{ removed?: string[] }>(`/admin/media-organize/tasks/${taskId}/plan/actions/batch-delete`, {
    action_ids: actionIds,
  });
}

export function testMediaOrganizeTmdb(payload?: Partial<MediaOrganizeSettings>) {
  return http.post<{
    ok: boolean;
    api_ok?: boolean;
    image_ok?: boolean;
    image_status?: number;
    language?: string;
    proxy_used?: boolean;
  }>("/admin/media-organize/test-tmdb", payload ?? {});
}

export interface MediaOrganizeTmdbSearchHit {
  id?: number | string;
  title?: string;
  name?: string;
  original_title?: string;
  original_name?: string;
  release_date?: string;
  first_air_date?: string;
  poster_path?: string;
  media_type?: string;
  overview?: string;
}

export function searchMediaOrganizeTmdb(params: {
  query: string;
  year?: number;
  language?: string;
  media_type?: string;
}) {
  return http.get<MediaOrganizeTmdbSearchHit[]>("/admin/media-organize/search-tmdb", {
    query: params.query,
    year: params.year,
    language: params.language,
    media_type: params.media_type ?? "auto",
  });
}

export function setMediaOrganizeBinding(taskId: string, groupUid: string, tmdbId: string, mediaType: "movie" | "tv") {
  return http.post<{ group_uid: string; tmdb_id: string; media_type: string; plan?: MediaOrganizePlan }>(
    `/admin/media-organize/tasks/${taskId}/bindings`,
    { group_uid: groupUid, tmdb_id: tmdbId, media_type: mediaType },
  );
}

export function fetchMediaOrganizeSettings() {
  return http.get<MediaOrganizeSettings>("/admin/media-organize/settings");
}

export function saveMediaOrganizeSettings(settings: Partial<MediaOrganizeSettings>) {
  return http.put<MediaOrganizeSettings>("/admin/media-organize/settings", settings);
}
