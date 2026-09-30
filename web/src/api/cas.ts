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

export interface CasConfig {
  enabled: boolean;
  age_days: number;
  delete_source: boolean;
  delay_delete_hours: number;
  write_back_cloud: boolean;
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
