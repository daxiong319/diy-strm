import { http } from "./client";

export interface StrmTask {
  id?: number;
  name: string;
  account_id: number;
  parent_id: string;
  path: string;
  recursive: boolean;
  scan_interval: number;
  scan_mode: string;
  extensions: string;
  output_folder: string;
  group_dir: string;
  api_interval: number;
  exclude_dir_keywords: string;
  exclude_file_keywords: string;
  sync_metadata: boolean;
  branch_check_enabled: boolean;
  time_window_enabled: boolean;
  time_start: string;
  time_end: string;
  schedule_mode: string;
  status: string;
  paused_reason?: string;
  error_message?: string;
  scanned_count: number;
  generated_count: number;
  updated_count: number;
  removed_count: number;
  last_scan?: string;
  last_scan_status?: string;
  /** 后端计算的下一次自动扫描时间；缺省表示不参与自动调度（手动或未启用）。 */
  next_run_at?: string;
  automation_managed?: boolean;
  is_scanning?: boolean;
  scan_phase?:
    | "scanning"
    | "comparing_metadata"
    | "syncing_metadata"
    | "uploading_metadata"
    | "cleaning_metadata";
  current_label?: string;
  scanned_dirs?: number;
  scanned_files?: number;
  metadata_total?: number;
  metadata_done?: number;
  started_at?: string;
  current_duration_ms?: number;
  created_at?: string;
  updated_at?: string;
}

export interface StrmBranch {
  id?: number;
  task_id?: number;
  account_id?: number;
  parent_id: string;
  path: string;
  relative_path?: string;
  // relative_dirs 只用于提交（相对目录的目录段数组），服务端不会回传。
  // 目录名自带斜杠时，显示路径无法还原段边界，必须靠它。
  relative_dirs?: string[];
  recursive: boolean;
  retention_days: number;
  branch_type: string;
  status?: string;
  source?: string;
}

export interface StrmSettings {
  token: string;
  base_url: string;
  signature_enabled: boolean;
  default_scan_interval: number;
  default_extensions: string;
  iso_filename_enabled: boolean;
  min_file_size_mb: number;
  conflict_policy: string;
  task_concurrency: number;
  metadata_extensions: string;
  metadata_max_size_mb: number;
  metadata_parent_enabled: boolean;
  metadata_sync_mode: "cloud_primary" | "local_primary" | "bidirectional";
}

export type StrmTaskInput = Pick<
  StrmTask,
  | "name"
  | "account_id"
  | "parent_id"
  | "path"
  | "recursive"
  | "scan_interval"
  | "scan_mode"
  | "extensions"
  | "output_folder"
  | "group_dir"
  | "api_interval"
  | "exclude_dir_keywords"
  | "exclude_file_keywords"
  | "sync_metadata"
  | "branch_check_enabled"
  | "time_window_enabled"
  | "time_start"
  | "time_end"
  | "schedule_mode"
>;

export const SCAN_MODES = [
  { value: "incremental_missing", label: "补缺" },
  { value: "incremental_update", label: "更新" },
  { value: "full_sync", label: "全量" },
] as const;

export const CONFLICT_POLICIES = [
  { value: "size_desc", label: "大文件优先" },
  { value: "size_asc", label: "小文件优先" },
  { value: "name_asc", label: "文件名靠前优先" },
] as const;

export type StrmRunMode = "auto" | "full" | "branch";

export function fetchStrmTasks() {
  return http.get<StrmTask[]>("/admin/strm/tasks");
}

export function fetchStrmStartupRemaining() {
  return http.get<{ startup_remaining: number }>("/admin/strm/startup");
}

export function createStrmTask(body: StrmTaskInput) {
  return http.post<StrmTask>("/admin/strm/tasks", body);
}

export interface StrmAccountRepairPrecheck {
  needs_prompt: boolean;
  has_history_strm: boolean;
  old_account_id?: number;
  sample_total: number;
  sample_matched: number;
  can_repair: boolean;
  message?: string;
}

export interface StrmAccountRepairResult {
  total: number;
  updated: number;
  skipped: number;
  failed: number;
}

export function precheckStrmAccountRepair(body: {
  account_id: number;
  parent_id: string;
  recursive: boolean;
  output_folder: string;
}) {
  return http.post<StrmAccountRepairPrecheck>("/admin/strm/tasks/precheck-account-repair", body);
}

export function repairStrmAccountReferences(body: {
  account_id: number;
  old_account_id: number;
  parent_id: string;
  recursive: boolean;
  output_folder: string;
}) {
  return http.post<StrmAccountRepairResult>("/admin/strm/tasks/repair-account-references", body);
}

export function updateStrmTask(id: number, body: StrmTaskInput) {
  return http.put<StrmTask>(`/admin/strm/tasks/${id}`, body);
}

export function deleteStrmTask(id: number, deleteStrmFiles = false) {
  return http.del<{ id: number }>(
    `/admin/strm/tasks/${id}`,
    undefined,
    deleteStrmFiles ? { delete_strm_files: true } : undefined,
  );
}

export function toggleStrmTask(id: number) {
  return http.post<StrmTask>(`/admin/strm/tasks/${id}/toggle`, {});
}

export function runStrmTaskNow(id: number, mode: StrmRunMode = "auto") {
  return http.post<StrmTask>(`/admin/strm/tasks/${id}/run`, {}, { mode });
}

export function forceStopStrmTask(id: number) {
  return http.post<null>(`/admin/strm/tasks/${id}/force-stop`, {});
}

export interface StrmCurrentDirectoryItem {
  id: string;
  name: string;
  size: number;
  is_dir: boolean;
}

export interface StrmCurrentDirectoryResult {
  matched_task_id: number;
  created: number;
  updated: number;
  skipped_existing: number;
  skipped_conflict: number;
  skipped_path_too_long: number;
  deleted: number;
  media_count: number;
  metadata_created: number;
  metadata_uploaded: number;
  metadata_deleted: number;
}

export function generateCurrentDirectoryStrm(body: {
  account_id: number;
  parent_id: string;
  path: string;
  // dirs：当前目录的目录段数组（相对账号根）。显示路径用 "/" 拼起来后无法还原
  // 段边界（目录名自带斜杠时和多层目录长得一样），所以一并传上去。
  dirs?: string[];
  items: StrmCurrentDirectoryItem[];
}) {
  return http.post<StrmCurrentDirectoryResult>("/admin/strm/generate-current-directory", body);
}

export interface StrmDirectoryStatus {
  matched_task_id: number;
  pending_strm: number;
  pending_metadata: number;
}

export function fetchStrmDirectoryStatus(body: {
  account_id: number;
  parent_id: string;
  path: string;
  dirs?: string[];
  items: StrmCurrentDirectoryItem[];
}, signal?: AbortSignal) {
  return http.post<StrmDirectoryStatus>("/admin/strm/directory-status", body, undefined, signal);
}

export function fetchStrmBranches(taskId: number) {
  return http.get<StrmBranch[]>(`/admin/strm/tasks/${taskId}/branches`);
}

export function createStrmBranch(taskId: number, body: Omit<StrmBranch, "id" | "task_id">) {
  return http.post<StrmBranch>(`/admin/strm/tasks/${taskId}/branches`, body);
}

export function updateStrmBranch(taskId: number, branchId: number, body: Partial<StrmBranch>) {
  return http.put<StrmBranch>(`/admin/strm/tasks/${taskId}/branches/${branchId}`, body);
}

export function deleteStrmBranch(taskId: number, branchId: number) {
  return http.del<{ id: number }>(`/admin/strm/tasks/${taskId}/branches/${branchId}`);
}

export function fetchStrmSettings() {
  return http.get<StrmSettings>("/admin/strm/settings");
}

export function saveStrmSettings(body: Partial<Record<keyof StrmSettings, string | boolean | number>>) {
  const payload: Record<string, string> = {};
  const map: Array<[keyof StrmSettings, (v: unknown) => string]> = [
    ["token", (v) => String(v)],
    ["base_url", (v) => String(v)],
    ["signature_enabled", (v) => (v ? "true" : "false")],
    ["default_scan_interval", (v) => String(v)],
    ["default_extensions", (v) => String(v)],
    ["iso_filename_enabled", (v) => (v ? "true" : "false")],
    ["min_file_size_mb", (v) => String(v)],
    ["conflict_policy", (v) => String(v)],
    ["task_concurrency", (v) => String(v)],
    ["metadata_extensions", (v) => String(v)],
    ["metadata_max_size_mb", (v) => String(v)],
    ["metadata_parent_enabled", (v) => (v ? "true" : "false")],
    ["metadata_sync_mode", (v) => String(v)],
  ];
  for (const [key, fmt] of map) {
    if (body[key] !== undefined) payload[key] = fmt(body[key]);
  }
  return http.put<StrmSettings>("/admin/strm/settings", payload);
}

export interface ReplaceStrmBaseURLResult {
  total: number;
  updated: number;
  base_url: string;
}

export function replaceStrmBaseURL(newBaseURL: string) {
  return http.post<ReplaceStrmBaseURLResult>("/admin/strm/replace-base-url", {
    new_base_url: newBaseURL,
  });
}

// ─────────────────────────────────────────────────────────────────────────────
// T17 播放路径映射
//
// 这套规则是**静默改写路径**的：匹配不上不报错、写反了也不报错。
// 所以前端除了编辑规则，还必须给两处能说真话的地方 ——
// 每条规则的命中统计，和一个走服务端同一段逻辑的「测试路径」。
// ─────────────────────────────────────────────────────────────────────────────

export interface PlayPathRule {
  id: string;
  source: string;
  target: string;
  note?: string;
}

/** 命中统计。重启归零是刻意的：它回答的是「刚才这一小时有没有生效过」。 */
export interface PlayPathRuleReport extends PlayPathRule {
  hits: number;
  last_hit: string;
  /** 后两段只在服务端带上时才出现（omitempty），所以是可选的。 */
  last_from?: string;
  last_to?: string;
}

/** 被更靠前的规则盖住、永远不会生效的条目。 */
export interface PlayPathConflict {
  id: string;
  source: string;
  reason: string;
}

export interface PlayPathMappingState {
  enabled: boolean;
  rules: PlayPathRuleReport[];
  conflicts: PlayPathConflict[];
}

export interface PlayPathMappingSaveResult extends PlayPathMappingState {
  saved: number;
}

/** 服务端用来区分「命中 / 被遮蔽 / 从未命中」的三态结论。 */
export interface PlayPathTestResult {
  matched: boolean;
  rule_id: string;
  source: string;
  from: string;
  to: string;
  result: string;
  candidates: PlayPathRule[];
  message: string;
}

export function fetchPlayPathMapping() {
  return http.get<PlayPathMappingState>("/admin/strm/path-mapping");
}

export function savePlayPathMapping(enabled: boolean, rules: PlayPathRule[]) {
  return http.put<PlayPathMappingSaveResult>("/admin/strm/path-mapping", { enabled, rules });
}

export function testPlayPathMapping(path: string) {
  return http.post<PlayPathTestResult>("/admin/strm/path-mapping/test", { path });
}
