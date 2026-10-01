import { http } from "./client";

export interface CasRecord {
  id: number;
  upload_task_id?: number;
  account_id?: number;
  source_type?: string;
  file_name?: string;
  file_size?: number;
  file_md5?: string;
  slice_md5?: string;
  sha1?: string;
  sha256?: string;
  pre_hash?: string;
  gcid?: string;
  remote_file_id?: string;
  remote_path?: string;
  rapid_drive_types?: string;
  status?: string; // active/restored/pending_delete/pending
  deleted_at?: number;
  restored_at?: number;
  restored_file_id?: string;
  created_at?: string;
}

export interface CasAutoSaveDriveConfig {
  enabled: boolean;
  account_id: number;
  save_dir: string;
}

export interface CasConfig {
  enabled: boolean;
  age_days: number;
  delete_source: boolean;
  delay_delete_hours: number;
  write_back_cloud: boolean;
  cas_notify_auto_save: boolean;
  cas_notify_auto_save_dir: string;
  cas_notify_auto_save_drives?: Record<string, CasAutoSaveDriveConfig>;
}

export interface CasPlayURLResult {
  cas_id: number;
  file_name: string;
  play_file_id: string;
  restored: boolean;
  play_url: string;
}

export interface CasRecordListResult {
  items: CasRecord[];
  total: number;
}

export interface CasRestoreResult {
  file_id: string;
  file_name: string;
  record_id?: number;
  drive_type: string;
}

export function fetchCasRecords(params: { page?: number; page_size?: number; status?: string; keyword?: string }) {
  return http.get<CasRecordListResult>("/admin/cas/records", params as Record<string, string | number | boolean | undefined>);
}

export function fetchCasRecord(id: number) {
  return http.get<CasRecord>(`/admin/cas/records/${id}`);
}

export function deleteCasRecord(id: number) {
  return http.del<{ ok: boolean }>(`/admin/cas/records/${id}`);
}

export function restoreCasRecord(id: number, targetFolderID: string) {
  return http.post<CasRestoreResult>(`/admin/cas/records/${id}/restore`, { target_folder_id: targetFolderID });
}

export function restoreCasFromText(body: { account_id: number; source_type: string; target_folder_id: string; cas_text: string }) {
  return http.post<CasRestoreResult>("/admin/cas/restore", body);
}

export function fetchCasConfig() {
  return http.get<CasConfig>("/admin/cas/config");
}

export function saveCasConfig(cfg: CasConfig) {
  return http.put<CasConfig>("/admin/cas/config", cfg);
}

export interface CasRunOnceResult {
  generated: number;
  deleted: number;
  skipped: number;
  failed: number;
}

export function runCasOnce() {
  return http.post<CasRunOnceResult>("/admin/cas/run-once", {});
}

export function fetchCasPlayURL(id: number) {
  return http.post<CasPlayURLResult>(`/admin/cas/records/${id}/play-url`, {});
}

// ---------------------------------------------------------------------------
// 本地文件生成 CAS 清单
// ---------------------------------------------------------------------------

/** 本地文件的五哈希（全部小写 hex；pre_hash/gcid 本地无法计算，恒为空）。 */
export interface CasLocalHashes {
  sha1?: string;
  sha256?: string;
  fileMd5?: string;
  sliceMd5?: string;
  preHash?: string;
  gcid?: string;
}

export interface CasGenerateLocalRequest {
  /** 本地文件路径，必须落在服务端配置的媒体根目录内 */
  local_path: string;
  /** 可空，默认取路径 basename */
  file_name?: string;
  /** 目标网盘：123 / pan139 / cloud189 / quark / guangya（可空） */
  target_provider?: string;
  /** 目标网盘账号 ID；不传则仅生成清单，无法直接秒传恢复 */
  account_id?: number;
}

export interface CasGenerateLocalResult {
  id: number;
  file_name: string;
  file_size: number;
  hashes: CasLocalHashes;
  rapid_drive_types: string;
  cas_content: string;
  /** 是否指定了真实目标盘账号+类型（可走秒传恢复） */
  restorable: boolean;
  /** 未指定目标盘时的提示，非空时应展示给用户 */
  warning?: string;
}

/**
 * 对本地磁盘文件生成 CAS 清单。
 * 大文件需要完整读盘算哈希，耗时可能从数十秒到数分钟，
 * 故显式放宽到 600 秒（10 分钟），避免默认超时先炸。
 */
export function generateCasFromLocal(body: CasGenerateLocalRequest) {
  return http.postWithTimeout<CasGenerateLocalResult>("/admin/cas/generate-local", body, 600_000);
}
