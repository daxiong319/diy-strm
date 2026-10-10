import { http } from "./client";

// 目录巡检（inspection）接口客户端。
//
// 交互口径与后端 internal/inspection 同源，页面必须照着这个来：
//   - 扫描**一个文件都不碰**，只产出结论；
//   - 修复只认「本次扫描的 snapshot_id + 那一批行」，服务端不接受
//     客户端自带的路径或文件名；
//   - 破坏性动作（delete_dir / rename_file）必须带 preview_ack，
//     也就是「用户看过这批预览」这件事要显式传回后端。
//
// 所以「扫描」和「执行修复」是两个分开的按钮，中间那一步就是
// 用户唯一的反悔窗口。

export interface InspectionChecker {
  key: string;
  label: string;
  findings: number;
  skipped: boolean;
  kinds: string[];
  /** 该检查器本轮执行出错时的错误信息；正常时缺省。 */
  error?: string;
}

export interface InspectionPreviewLine {
  finding_id: string;
  checker_key: string;
  kind: string;
  target: string;
  detail: string;
  repair: string;
  repair_label?: string;
  reversible: boolean;
}

export interface InspectionScanReport {
  snapshot_id: string;
  scanned_at: string;
  checkers: InspectionChecker[];
  preview: InspectionPreviewLine[];
  total: number;
}

export interface InspectionExecuteResult {
  finding_id: string;
  kind: string;
  target: string;
  ok: boolean;
  message: string;
}

export function fetchInspectionCheckers() {
  return http.get<{ checkers: { key: string; label: string }[] }>("/admin/tools/inspection/checkers");
}

/**
 * 扫描。全程只读。
 *
 * 刻意不设短超时：一次巡检要走完网盘清单并逐项比对，根目录大的库跑几分钟
 * 是正常的。给它 5 分钟上限就够了 —— 真正的取消手段是页面上的停止按钮
 * （AbortController），不是让请求自己超时。
 */
export function scanInspection(signal?: AbortSignal) {
  return http.postWithTimeout<InspectionScanReport>("/admin/tools/inspection/scan", undefined, 5 * 60 * 1000, signal);
}

export function fetchInspectionPreview(snapshotId: string) {
  return http.get<{ snapshot_id: string; preview: InspectionPreviewLine[] }>(
    "/admin/tools/inspection/preview",
    { snapshot_id: snapshotId },
  );
}

/**
 * 执行修复。previewAck 必须为 true —— 后端会拒绝没带的请求。
 *
 * 传 true 的含义不是「前端记得弹过框」，而是服务端应当校验的前提：
 * 没有它，一个写错的客户端就能跳过预览直接删目录。
 */
export function repairInspection(snapshotId: string, findingIds: string[], previewAck: boolean) {
  return http.post<{ results: InspectionExecuteResult[] }>("/admin/tools/inspection/repair", {
    snapshot_id: snapshotId,
    finding_ids: findingIds,
    preview_ack: previewAck,
  });
}

/** 修复动作是否不可逆。与后端 destructiveKinds 同源。 */
export const INSPECTION_DESTRUCTIVE_KINDS = new Set(["delete_dir", "rename_file"]);
