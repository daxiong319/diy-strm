import { http } from "./client";

export interface CloudTool115Status {
  enabled: boolean;
  cache_count: number;
  available: boolean;
}

export interface LocalUploadMapping {
  name: string;
  path: string;
}

export interface LocalUploadConfig {
  enabled: boolean;
  mappings: LocalUploadMapping[];
}

export interface LocalUploadEntry {
  name: string;
  is_dir: boolean;
  size: number;
  mtime: number;
  rel_path: string;
}

export interface LocalUploadBrowseResult {
  mapping: string;
  path: string;
  items: LocalUploadEntry[];
}

export interface LocalUploadCreatePayload {
  account_id: number;
  mapping: string;
  target_path: string;
  target_display_path?: string;
  conflict_policy: string;
  client_task_id: string;
  display_name?: string;
  items: { rel_path: string; is_dir: boolean }[];
}

export interface AIOrganizeInstance {
  id: string;
  name: string;
  base_url: string;
  api_key: string;
  model: string;
  default: boolean;
}

export interface AIOrganizeConfig {
  enabled: boolean;
  items: AIOrganizeInstance[];
}

export interface AIOrganizeInstanceUpdate {
  id?: string;
  name: string;
  base_url: string;
  api_key: string;
  model: string;
  default?: boolean;
}

export type ClassificationTemplateKind = "media" | "region" | "genre" | "custom";

// 结构化条件字段（C-3）。与 condition 表达式并存：fields 为空时解析 condition，
// 两边都填则以 fields 为准。值的语法统一是「普通=或 / +x=必须命中 / -x=命中即排除」，
// year 额外支持闭区间 2000-2009。
export interface ClassificationRuleFields {
  media_types?: string[];
  genre_ids?: number[];
  origin_country?: string[];
  original_language?: string[];
  year?: { from: number; to: number };
  keywords?: string[];
  series_keywords?: string[];
}

export interface ClassificationRule {
  name: string;
  condition: string;
  fields?: ClassificationRuleFields;
  // 该规则命中后固定追加的系列目录名（T02 C-2）。
  // 留空则改由全局 series 规则按 series_keywords 判定。
  series?: string;
  fallback_mode?: "self" | "directory";
  fallback_dir?: string;
  children?: ClassificationRule[];
}

// 系列目录规则（C-2）。挂在 Config 级而不是某个模板下：同一个「流浪地球系列」
// 在地区模板下是 国产/流浪地球系列、在类型模板下是 科幻奇幻/流浪地球系列，
// 放进模板里就得配四份，改一次漏一处就会出现两个同名系列目录。
export interface ClassificationSeriesRule {
  name: string;
  dir_name?: string;
  media_type?: "movie" | "tv";
  series_keywords?: string[];
  keywords?: string[];
  position?: number;
  remark?: string;
}

export interface ClassificationTemplate {
  kind: ClassificationTemplateKind;
  rules: ClassificationRule[];
}

export interface ClassificationConfig {
  version: number;
  enabled: boolean;
  selected_template: ClassificationTemplateKind;
  templates: ClassificationTemplate[];
  series?: ClassificationSeriesRule[];
}

// 只读分类清单（C-8）。跨模块消费者（洗版规则筛选、清理保护）与前端的
// 下拉框共用同一个数据源。
//
// ⚠️ items 是**规则里配了哪些分类目录**，不是**磁盘上存在哪些目录**：
// 用户刚配了「电影/科幻」但还没影片被整理过去，磁盘上那个目录并不存在；
// 反过来磁盘上有个同名目录但那一级配置早就删了，它也不会出现在这里。
// 所以前端不能拿这个清单去判断"目录是不是已经建好了"。
export interface ClassificationCategory {
  /** 1 / 2 / 3。一级表化（C-3）之后层级是可配的，不要假设一定有三层。 */
  level: number;
  name: string;
  /** 形如 "genre/电影/科幻"。跨模板同名目录靠它区分（region 与 genre 都有「国产」）。 */
  slug: string;
  template: string;
  /** 一级分类的类型键（movie / tv）；二级三级为空串。 */
  primary_key?: string;
  /** 从模板根到本级的目录名路径，形如 "电影/国产"。 */
  path: string;
  enabled: boolean;
  /** 用户自定义规则行的主键；内置模板目录为 0。 */
  rule_id: number;
}

// 预览端点（C-7）：只算不写。回答「这个文件会被放到哪个目录」。
export interface ClassificationPreviewResult {
  path: string;
  segments: string[];
  matched_rule: string;
  degraded_reason: string;
  template: string;
  category: string;
  applied: boolean;
  matched: boolean;
  evidence?: Record<string, unknown>;
}

// 降级原因码（C-5）。tmdb_unavailable 与 no_rule_matched 必须分开看：
// 前者是「我们还不知道它属于哪一类」，后者是「它不属于任何已配置类型」。
export const CLASSIFICATION_DEGRADED_REASONS: Record<string, string> = {
  tmdb_unavailable: "TMDB 暂时不可用，暂时沿用上一次判断",
  tmdb_detail_failed: "TMDB 查询失败，已按不完整信息判断",
  tmdb_detail_unavailable: "没有可用的 TMDB 信息（未配置 TMDB 或影片 ID）",
  no_rule_matched: "没有匹配到任何分类规则",
  ambiguous_rule_matched: "多条规则同分，未能确定分类",
};

export interface ClassificationTMDBGenre {
  id?: number;
  name?: string;
}

export interface ClassificationTMDBDetail extends Record<string, unknown> {
  id?: number;
  media_type?: "movie" | "tv";
  title?: string;
  name?: string;
  original_title?: string;
  original_name?: string;
  origin_country?: string[];
  original_language?: string;
  genres?: ClassificationTMDBGenre[];
}

export interface QuarkTVBinding {
  account_id: number;
  account_name: string;
  tv_nickname: string;
  preferred_resolution: string;
  allow_dolby: boolean;
  membership: string;
}

export interface QuarkTVStatus {
  enabled: boolean;
  available: boolean;
  play_mode: "split" | "adaptive" | "direct";
  client_list_mode: "direct_list" | "proxy_list";
  proxy_clients: string;
  bindings: QuarkTVBinding[];
}

export interface QuarkTVAccount {
  id: number;
  name: string;
}

export interface QuarkTVBindStart {
  token: string;
  qr_image: string;
  expires_in: number;
}

export interface QuarkTVBindPoll {
  status: "waiting" | "success" | "failed" | "expired";
  message: string;
}

export interface QuarkTVBindingSettingsPayload {
  account_id: number;
  preferred_resolution: string;
  allow_dolby: boolean;
  play_mode: "split" | "adaptive" | "direct";
  client_list_mode: "direct_list" | "proxy_list";
  proxy_clients: string;
}

export interface QuarkTVBindingSettingsResult {
  binding: QuarkTVBinding;
  play_mode: "split" | "adaptive" | "direct";
  client_list_mode: "direct_list" | "proxy_list";
  proxy_clients: string;
}

export const cloudToolsApi = {
  status115: () => http.get<CloudTool115Status>("/admin/tools/115-strm/status"),
  set115Enabled: (enabled: boolean) =>
    http.post<{ enabled: boolean }>("/admin/tools/115-strm/enabled", { enabled }),
  clear115Cache: (accountId = 0) =>
    http.post<{ removed: number }>("/admin/tools/115-strm/cache/clear", { account_id: accountId }),
};

export const localUploadApi = {
  getConfig: () => http.get<LocalUploadConfig>("/admin/tools/local-upload/config"),
  saveConfig: (payload: LocalUploadConfig) =>
    http.put<LocalUploadConfig>("/admin/tools/local-upload/config", payload),
  browse: (mapping: string, path = "") =>
    http.post<LocalUploadBrowseResult>("/admin/tools/local-upload/browse", { mapping, path }),
  upload: (payload: LocalUploadCreatePayload) =>
    http.post<{ accepted: boolean; count: number }>("/admin/tools/local-upload/upload", payload),
};

export const aiOrganizeApi = {
  getConfig: () => http.get<AIOrganizeConfig>("/admin/tools/ai-organize/config"),
  saveConfig: (payload: { enabled: boolean; items: AIOrganizeInstanceUpdate[] }) =>
    http.put<AIOrganizeConfig>("/admin/tools/ai-organize/config", payload),
  testConfig: (payload: AIOrganizeInstanceUpdate) =>
    http.post<{ ok: boolean }>("/admin/tools/ai-organize/test", payload),
};

// 用户自定义分类规则（与内置模板并存的独立引擎，按 position 顺序首个命中胜出）
export interface ClassifyRuleCondition {
  key: string;
  values: string;
  optional: boolean;
}

export interface ClassifyRuleItem {
  id: number;
  media_type: string;
  target_path: string;
  enabled: boolean;
  remark: string;
  conditions: ClassifyRuleCondition[];
  position: number;
  created_at: string;
  updated_at: string;
}

export interface ClassifyRuleInput {
  media_type: string;
  target_path: string;
  enabled?: boolean;
  remark?: string;
  conditions: ClassifyRuleCondition[];
}

export interface ClassifyRulesImportResult {
  rules: ClassifyRuleItem[];
  warnings: string[];
  imported: number;
}

export const classificationApi = {
  getConfig: () => http.get<ClassificationConfig>("/admin/tools/classification/config"),
  saveConfig: (payload: ClassificationConfig) =>
    http.put<ClassificationConfig>("/admin/tools/classification/config", payload),
  lookupTMDBDetail: (payload: { tmdb_id: string; media_type: "movie" | "tv" }) =>
    http.post<ClassificationTMDBDetail>("/admin/tools/classification/tmdb-detail", payload),
  listRules: () => http.get<{ rules: ClassifyRuleItem[] }>("/admin/tools/classification/rules"),
  createRule: (payload: ClassifyRuleInput) =>
    http.post<ClassifyRuleItem>("/admin/tools/classification/rules", payload),
  updateRule: (id: number, payload: ClassifyRuleInput) =>
    http.put<ClassifyRuleItem>(`/admin/tools/classification/rules/${id}`, payload),
  deleteRule: (id: number) => http.del<void>(`/admin/tools/classification/rules/${id}`),
  reorderRules: (ids: number[]) =>
    http.post<{ rules: ClassifyRuleItem[] }>("/admin/tools/classification/rules/reorder", { ids }),
  importRules: (payload: { yaml: string; mode?: "replace" | "append" }) =>
    http.post<ClassifyRulesImportResult>("/admin/tools/classification/rules/import", payload),
  exportRules: () => http.get<{ yaml: string }>("/admin/tools/classification/rules/export"),
  categories: () =>
    http.get<{ items: ClassificationCategory[]; levels: number[] }>(
      "/admin/tools/classification/categories",
    ),
  preview: (payload: {
    media_type: "movie" | "tv";
    tmdb_id?: string;
    title?: string;
    year?: number;
    raw?: Record<string, unknown>;
  }) => http.post<ClassificationPreviewResult>("/admin/media-organize/classification/preview", payload),
};

export const quarkTVApi = {
  status: () => http.get<QuarkTVStatus>("/admin/tools/quarktv/status"),
  setEnabled: (enabled: boolean) =>
    http.post<{ enabled: boolean }>("/admin/tools/quarktv/enabled", { enabled }),
  accounts: () => http.get<{ accounts: QuarkTVAccount[] }>("/admin/tools/quarktv/accounts"),
  bindStart: (accountId: number) =>
    http.post<QuarkTVBindStart>("/admin/tools/quarktv/bind/start", { account_id: accountId }),
  bindPoll: (token: string) =>
    http.post<QuarkTVBindPoll>("/admin/tools/quarktv/bind/poll", { token }),
  updateBindingSettings: (payload: QuarkTVBindingSettingsPayload) =>
    http.put<QuarkTVBindingSettingsResult>("/admin/tools/quarktv/binding/settings", payload),
  unbind: (accountId: number) =>
    http.del<{ removed: boolean }>("/admin/tools/quarktv/bind", { account_id: accountId }),
};
