import { http } from "./client";

// 洗版（media upgrade）管理接口客户端。
//
// 安全口径（与 internal/mediaupgrade 包同源）：
//   - 扫描**不会**碰任何文件，只产出待执行记录；
//   - 提交才动文件，且内部会先复核快照，对不上就整条作废；
//   - 总开关关闭时后端会直接拒绝，返回的是明确的提示而不是空结果。
//
// 所以前端刻意把「扫描」和「提交」做成两个分开的按钮，
// 且提交按钮带二次确认：中间那一步是用户唯一的反悔窗口。

export type MediaUpgradeSource = "local" | "emby" | "jellyfin";

export type MediaUpgradeLoserAction = "keep" | "delete" | "move";

export const MEDIA_UPGRADE_SOURCES: Array<{ value: MediaUpgradeSource; label: string }> = [
  { value: "local", label: "本地目录" },
  { value: "emby", label: "Emby" },
  { value: "jellyfin", label: "Jellyfin" },
];

export const MEDIA_UPGRADE_LOSER_ACTIONS: Array<{ value: MediaUpgradeLoserAction; label: string }> = [
  { value: "keep", label: "保留（默认，不删任何文件）" },
  { value: "delete", label: "删除败方文件" },
  { value: "move", label: "把败方文件移到归档目录" },
];

export interface MediaUpgradeRule {
  id: number;
  name: string;
  source: MediaUpgradeSource | string;
  library_root: string;
  candidate_roots: string;
  min_resolution: number;
  min_channels: number;
  require_subtitle: boolean;
  max_records_per_series: number;
  loser_action: MediaUpgradeLoserAction | string;
  move_dir: string;
  group_priority: string;
  wash_rules: string;
  // 适用分类：分类目录**名**的数组，空数组 = 不按分类筛选。
  // 刻意存目录名而不是分类 ID：用户改名或改层级后老规则不会莫名其妙变成筛选不到，
  // 代价是改过名之后这一列需要用户自己重新勾一次（比指向一个已不存在的 ID 更好解释）。
  categories?: string[];
  enabled: boolean;
  builtin: boolean;
}

export interface MediaUpgradeScan {
  id: number;
  source: string;
  library_root: string;
  candidate_roots: string;
  rule_id: number;
  status: string;
  library_files: number;
  candidate_files: number;
  total_records: number;
  new_wins_count: number;
  skipped_count: number;
  failed_count: number;
  message: string;
  created_at: string;
  started_at?: string | null;
  finished_at?: string | null;
  updated_at?: string;
}

export interface MediaUpgradeRecord {
  id: number;
  scan_id: number;
  rule_id: number;
  series_key: string;
  series_title: string;
  episode_key: string;
  slot_key: string;
  new_file_path: string;
  new_file_name: string;
  new_size: number;
  new_quality: string;
  old_files: string;
  quality_relation: string;
  trace: string;
  /** T31：结构化驳回理由（JSON 串，空串表示没有驳回理由）。 */
  reject_reasons: string;
  /** T31：产出这条结论的规则指纹。规则一改，旧的 trace 含义就变了。 */
  rule_fingerprint: string;
  loser_action: string;
  loser_path: string;
  snapshot: string;
  snapshot_hash: string;
  snapshot_at: string;
  status: string;
  message: string;
  delete_failures: string;
  executed_at?: string | null;
  created_at?: string;
  updated_at?: string;
}

export interface MediaUpgradeExecuteResult {
  scan_id: number;
  total: number;
  executed: number;
  expired: number;
  skipped: number;
  failed: number;
  excluded: number;
  message: string;
}

export type MediaUpgradeRecordInput = Omit<MediaUpgradeRule, "id" | "builtin"> & { id?: number };

export function fetchMediaUpgradeRules() {
  return http.get<{ global: MediaUpgradeRule; items: MediaUpgradeRule[] }>("/admin/media-upgrade/rules");
}

export function createMediaUpgradeRule(input: MediaUpgradeRecordInput) {
  return http.post<{ item: MediaUpgradeRule }>("/admin/media-upgrade/rules", input);
}

export function updateMediaUpgradeRule(id: number, input: MediaUpgradeRecordInput) {
  return http.put<{ item: MediaUpgradeRule }>(`/admin/media-upgrade/rules/${id}`, input);
}

export function deleteMediaUpgradeRule(id: number) {
  return http.del<{ deleted: number }>(`/admin/media-upgrade/rules/${id}`);
}

export function fetchMediaUpgradeScans(limit = 50) {
  return http.get<{ items: MediaUpgradeScan[] }>("/admin/media-upgrade/scans", { limit });
}

export function fetchMediaUpgradeScan(id: number) {
  return http.get<{ item: MediaUpgradeScan }>(`/admin/media-upgrade/scans/${id}`);
}

/** 触发扫描：只判定，不动任何文件。ruleId 省略表示用全局设置。 */
export function createMediaUpgradeScan(ruleId?: number) {
  return http.post<{ item: MediaUpgradeScan }>("/admin/media-upgrade/scans", ruleId ? { rule_id: ruleId } : {});
}

/** 提交：复核快照后才动文件。 */
export function executeMediaUpgradeScan(id: number) {
  return http.post<MediaUpgradeExecuteResult>(`/admin/media-upgrade/scans/${id}/execute`);
}

export function fetchMediaUpgradeRecords(params?: {
  scan_id?: number;
  status?: string;
  limit?: number;
}) {
  // 空串/0 一律不发：后端 queryIntOr 会把非法值静默兜底，
  // 但把"没填"和"填了 0"原样传过去会让排查时分不清是哪一种。
  return http.get<{ items: MediaUpgradeRecord[] }>("/admin/media-upgrade/records", {
    ...(params?.scan_id ? { scan_id: params.scan_id } : {}),
    ...(params?.status ? { status: params.status } : {}),
    limit: params?.limit ?? 200,
  });
}

export function fetchMediaUpgradeRecord(id: number) {
  return http.get<{ item: MediaUpgradeRecord }>(`/admin/media-upgrade/records/${id}`);
}

// ---- 规则试算（T31）----

/** 结构化驳回理由。code 是后端枚举，前端只负责翻译成人话。 */
export type RejectReasonCode =
  | "inferior_dimension"
  | "below_min_resolution"
  | "below_min_channels"
  | "missing_required_subtitle"
  | "no_comparable_dimension"
  | "no_slot";

export interface RejectReason {
  code: RejectReasonCode;
  field: string;
  new: string;
  old: string;
}

/** 逐维度明细，字段名与后端 moviepilot.QualityDimension 一致。 */
export interface QualityDimension {
  Field: string;
  Label: string;
  New: string;
  Old: string;
  Better: boolean;
  Worse: boolean;
}

export interface MediaUpgradeTrialResult {
  relation: string;
  trace: string;
  reasons: RejectReason[];
  dimensions: QualityDimension[];
  rule_fingerprint: string;
  gate_reasons: RejectReason[];
  same_slot: boolean;
  new_slot_label: string;
  old_slot_label: string;
}

/** 体积用 has_* 单独表达是否参与，"没填"和"填了 0"是两件事。 */
export interface MediaUpgradeTrialInput {
  rule?: MediaUpgradeRecordInput;
  rule_id?: number;
  new_name: string;
  new_size?: number;
  has_new_size?: boolean;
  old_name: string;
  old_size?: number;
  has_old_size?: boolean;
}

/**
 * 规则试算：填两个文件名看逐维度得分。
 *
 * 纯只读、不落库 —— 用户可以放心对着一份还没保存的草稿规则反复试。
 */
export function trialMediaUpgradeRule(input: MediaUpgradeTrialInput) {
  return http.post<{ item: MediaUpgradeTrialResult }>("/admin/media-upgrade/rule-trial", input);
}