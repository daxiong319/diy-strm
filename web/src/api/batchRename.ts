import { http } from "./client";

/** 批量重命名规则类型，与后端 renamerule.RuleTypes 一一对应。 */
export type BatchRenameRuleType =
  | "replace"
  | "folder"
  | "regex"
  | "setname"
  | "number"
  | "separator"
  | "add"
  | "delete"
  | "move"
  | "case"
  | "space"
  | "width";

/**
 * 一条重命名规则。字段与后端 renamerule.Rule 对齐。
 * 所有字段均为必填：defaultBatchRenameRule() 会补齐全部缺省值，
 * 与后端 Defaults() 的语义一致，避免表单绑定出现 undefined。
 */
export interface BatchRenameRule {
  id: string;
  type: BatchRenameRuleType;
  find: string;
  replace: string;
  pattern: string;
  position: string;
  separator: string;
  folder_name: string;
  start: string;
  digits: string;
  prefix: string;
  suffix: string;
  text: string;
  index: string;
  mode: string;
  length: string;
  to: string;
  case_sensitive: boolean;
  first_only: boolean;
}

/** 待重命名的条目。type 为 0 表示文件、1 表示目录。 */
export interface BatchRenameItem {
  file_id: string;
  name: string;
  type?: number;
  parent_id?: string;
}

/** 预览结果条目：在原始条目上追加 new_name。 */
export interface BatchRenamePreviewItem extends BatchRenameItem {
  new_name: string;
}

export interface BatchRenamePreviewResult {
  items: BatchRenamePreviewItem[];
  errors: string[];
  changed_count: number;
  total_count: number;
}

export interface BatchRenamePreviewPayload {
  account_id: number;
  parent_id?: string;
  folder_name?: string;
  keep_ext?: boolean;
  rules: BatchRenameRule[];
  items: BatchRenameItem[];
  existing_names?: string[];
}

export interface BatchRenameApplyItem extends BatchRenameItem {
  new_name: string;
}

export interface BatchRenameApplyPayload {
  account_id: number;
  parent_id?: string;
  label?: string;
  keep_ext?: boolean;
  rules: BatchRenameRule[];
  items: BatchRenameApplyItem[];
}

export interface BatchRenameApplyFailed {
  file_id: string;
  name: string;
  reason: string;
}

export interface BatchRenameApplySuccess {
  file_id: string;
  old_name: string;
  new_name: string;
}

export interface BatchRenameApplyResult {
  success: BatchRenameApplySuccess[];
  failed: BatchRenameApplyFailed[];
  success_count: number;
  fail_count: number;
}

export interface BatchRenameHistoryEntry {
  id: number;
  name: string;
  rules: BatchRenameRule[];
  keep_ext: boolean;
  item_count: number;
  change_count: number;
  created_at: string;
}

export interface BatchRenameRollbackResult {
  success: BatchRenameApplySuccess[];
  fail_count: number;
}

export interface BatchRenamePreset {
  id: number;
  name: string;
  rules: BatchRenameRule[];
  keep_ext: boolean;
  use_count: number;
}

/** 各规则类型的默认字段值，与后端 renamerule.Defaults 保持一致。 */
export function defaultBatchRenameRule(type: BatchRenameRuleType): BatchRenameRule {
  const base: BatchRenameRule = {
    id: "",
    type,
    find: "",
    replace: "",
    pattern: "",
    position: "",
    separator: "",
    folder_name: "",
    start: "",
    digits: "",
    prefix: "",
    suffix: "",
    text: "",
    index: "",
    mode: "",
    length: "",
    to: "",
    case_sensitive: false,
    first_only: false,
  };
  switch (type) {
    case "folder":
      return { ...base, position: "prefix", separator: "-" };
    case "setname":
      return { ...base, pattern: "{name}", start: "1", digits: "2" };
    case "number":
      return { ...base, position: "replace", start: "1", digits: "2" };
    case "separator":
    case "add":
      return { ...base, position: "end", text: "-", index: "1" };
    case "delete":
      return { ...base, mode: "text", start: "1", length: "1" };
    case "move":
      return { ...base, start: "1", length: "1", to: "1" };
    case "case":
      return { ...base, mode: "upper" };
    case "space":
      return { ...base, mode: "trim" };
    case "width":
      return { ...base, mode: "half" };
    default:
      return base;
  }
}

/** 规则类型的中文名，与后端 renamerule.ruleTypeLabels 一致。 */
export const BATCH_RENAME_RULE_LABELS: Record<BatchRenameRuleType, string> = {
  replace: "查找替换",
  folder: "添加文件夹名",
  regex: "基于正则重命名",
  setname: "名称模板",
  number: "修改名称/添加序号",
  separator: "添加分隔符",
  add: "添加字符",
  delete: "删除字符",
  move: "移动字符",
  case: "大小写字母转换",
  space: "清理空格",
  width: "全角半角转换",
};

/**
 * 把规则中的数字类字段统一成字符串，避免输入框数字型与字符串型混用。
 * 与老版 BatchRenameDialog.vue 的 normalizeRule 等价。
 */
export function normalizeBatchRenameRule(rule: BatchRenameRule): BatchRenameRule {
  return {
    ...rule,
    start: String(rule.start ?? "").trim(),
    digits: String(rule.digits ?? "").trim(),
    index: String(rule.index ?? "").trim(),
    length: String(rule.length ?? "").trim(),
    to: String(rule.to ?? "").trim(),
    case_sensitive: Boolean(rule.case_sensitive),
    first_only: Boolean(rule.first_only),
  };
}

export const batchRenameApi = {
  preview: (payload: BatchRenamePreviewPayload) =>
    http.post<BatchRenamePreviewResult>("/files/batch-rename/preview", payload),

  apply: (payload: BatchRenameApplyPayload) =>
    http.post<BatchRenameApplyResult>("/files/batch-rename/apply", payload),

  history: () =>
    http.get<{ items: BatchRenameHistoryEntry[] }>("/files/batch-rename/history"),

  rollback: (payload: { account_id: number; history_id: number }) =>
    http.post<BatchRenameRollbackResult>("/files/batch-rename/rollback", payload),

  listPresets: () => http.get<{ items: BatchRenamePreset[] }>("/files/batch-rename/presets"),

  savePreset: (payload: { name: string; keep_ext: boolean; rules: BatchRenameRule[] }) =>
    http.post<{ id: number }>("/files/batch-rename/presets", payload),

  // 注意：项目 http 助手的删除方法名是 del（不是 delete）。
  deletePreset: (id: number) => http.del<null>("/files/batch-rename/presets", { id }),
};
