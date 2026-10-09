import { http } from "./client";

export type SettingType = "string" | "int" | "bool" | "select";

export interface SettingOption {
  value: string;
  label: string;
}

export interface SettingItem {
  key: string;
  type: SettingType;
  category: string;
  label: string;
  description?: string;
  value: string;
  default: string;
  is_default: boolean;
  unit?: string;
  min?: number;
  max?: number;
  options?: SettingOption[];
  sensitive?: boolean;
}

export interface SettingCategory {
  id: string;
  label: string;
}

export interface SettingsPayload {
  categories: SettingCategory[];
  items: SettingItem[];
}

export function fetchSettings(options?: { includeHidden?: boolean }) {
  // includeHidden：连带取回 Hidden 键（界面偏好等），供前端读回用户习惯。
  const query = options?.includeHidden ? "?include_hidden=1" : "";
  return http.get<SettingsPayload>(`/admin/settings${query}`);
}

// 仅提交改动过的键值（字符串形式），后端按类型校验并返回最新快照。
export function saveSettings(values: Record<string, string>) {
  return http.put<SettingsPayload>("/admin/settings", values);
}

// ---- 设置项搜索索引（⌘G 功能直达）----
//
// 索引由后端从注册表派生，**前端不维护任何清单**。
// 手写清单必然会漏，而漏掉的症状是「设置页上找得到、搜索里搜不到」——
// 用户只会得出「这个功能没做」这一个结论。

/** 搜索索引条目。页面/锚点信息同样由后端给出。 */
export interface SettingIndexEntry {
  key: string;
  type: SettingType;
  category: string;
  /** 分组的中文名（后端兜好了未登记的分组）。 */
  category_label: string;
  label: string;
  description?: string;
  /** 承载这个设置的后台页面 key（/admin?page=…）。 */
  page?: string;
  /** 该页面内的 tab key。 */
  tab?: string;
  /** 页内定位锚点，形如 setting-<key>。 */
  anchor?: string;
  /** 锚点是否真的指向一个可滚动的行。为 false 时不要假装已经定位。 */
  anchored: boolean;
  /** 小写搜索词：key、key 拆词、标签、分组名、选项名与人工别名。 */
  keywords: string[];
}

export interface SettingsIndex {
  categories: SettingCategory[];
  items: SettingIndexEntry[];
  /** 索引的覆盖边界，原样展示给用户，不让「搜不到」被误读成「没做」。 */
  coverage: string;
}

export function fetchSettingsIndex() {
  return http.get<SettingsIndex>("/admin/settings/index");
}
