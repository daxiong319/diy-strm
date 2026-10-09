import { http } from "./client";

// 用户与权限（RBAC）接口客户端。
//
// 判定口径全在后端（internal/rbac），前端**不重复实现任何一条规则**：
//   - 「这一项能不能勾」看 super_only，后端说什么就是什么；
//   - 「这个用户现在能不能跑」看 allowed/source，不在前端推断。
//
// 前端唯一需要理解的是「继承」：组权限表和用户覆盖表都是稀疏的，
// 没出现的键 = 继承用户级覆盖 = 没表态，所以界面上是三态（继承/允许/拒绝）
// 而不是两态。这一点做错的典型后果是把「没表态」当成「拒绝」，
// 于是上层给的权限在矩阵里看不见，管理员会以为系统没生效。

export type RbacEffect = "" | "allow" | "deny";

export interface RbacCategory {
  key: string;
  label: string;
  sort_order: number;
}

export interface RbacPermission {
  key: string;
  label: string;
  category: string;
  /** 为真时该项永远不下放，界面必须显示为只读。 */
  super_only: boolean;
  sort_order: number;
  /** 当前会话是否拥有这一项。 */
  allowed: boolean;
  /** 归因：super / group_allow / group_deny / user_allow / user_deny / none。 */
  source?: string;
}

export interface RbacUser {
  id: number;
  username: string;
  display_name: string;
  enabled: boolean;
  password_set: boolean;
  groups: string[];
  last_login_at?: string;
}

export interface RbacGroup {
  id: number;
  name: string;
  description: string;
  builtin: boolean;
  member_count: number;
  member_ids: number[];
  /** key → allow/deny 的稀疏映射；没有的键表示这一项没表态。 */
  permissions: Record<string, RbacEffect>;
}

export interface RbacMe {
  /** 总开关。关着时后端不做任何判定。 */
  enabled: boolean;
  /** 0 表示超管。 */
  user_id: number;
  username: string;
  display_name?: string;
  is_super: boolean;
  groups?: string[];
  /** 可见的一级导航 key。 */
  menus: string[];
  permissions: string[];
}

/** 当前身份与可见范围。登录后调一次，拿到「我是谁 + 我能看到什么」。 */
export function fetchRbacMe() {
  // 后端直接返回 DTO 本体（不是 {me: ...} 包装），与 authMenus 保持一致。
  return http.get<RbacMe>("/auth/me");
}

/** 可见的一级导航 key。与 fetchRbacMe 同源，但只取菜单，省一点解析。 */
export function fetchRbacMenus() {
  return http.get<{ enabled: boolean; menus: string[] }>("/auth/menus");
}

/** 权限目录 + 当前会话在每一项上的判定结果。 */
export function fetchRbacPermissions() {
  return http.get<{
    items: RbacPermission[];
    categories: RbacCategory[];
    super_only: string[];
  }>("/rbac/permissions");
}

export function fetchRbacUsers() {
  return http.get<{ items: RbacUser[] }>("/rbac/users").then((r) => r.items);
}

export function createRbacUser(body: {
  username: string;
  display_name?: string;
  password: string;
  enabled?: boolean;
}) {
  return http.post<RbacUser>("/rbac/users", body);
}

// 登录名不可改：它出现在登录框里、旧会话里和审计记录里，
// 改了就追不回「当时在组里的那个人是谁」。后端同样硬拦，这里不给入口。
export function updateRbacUser(id: number, body: { display_name?: string }) {
  return http.put<RbacUser>(`/rbac/users/${id}`, body);
}

export function deleteRbacUser(id: number) {
  return http.del<{ id: number }>(`/rbac/users/${id}`);
}

export function setRbacUserEnabled(id: number, enabled: boolean) {
  return http.post<{ id: number; enabled: boolean }>(`/rbac/users/${id}/enabled`, { enabled });
}

export function setRbacUserPassword(id: number, password: string) {
  return http.post<{ id: number }>(`/rbac/users/${id}/password`, { password });
}

/** 全量替换该用户的组归属。后端会校验组存在，不会写出悬空行。 */
export function setRbacUserGroups(id: number, group_ids: number[]) {
  return http.post<{ id: number; group_ids: number[] }>(`/rbac/users/${id}/groups`, {
    group_ids,
  });
}

export function fetchRbacUserOverrides(id: number) {
  return http.get<{ user_id: number; overrides: Record<string, RbacEffect> }>(
    `/rbac/users/${id}/overrides`,
  );
}

/** 全量替换该用户的用户级覆盖。空串表示「继承」，后端会删掉那一行。 */
export function setRbacUserOverrides(id: number, overrides: Record<string, RbacEffect>) {
  return http.post<{ user_id: number; overrides: Record<string, RbacEffect> }>(
    `/rbac/users/${id}/overrides`,
    { overrides },
  );
}

export function fetchRbacGroups() {
  return http.get<{ items: RbacGroup[] }>("/rbac/groups").then((r) => r.items);
}

export function createRbacGroup(body: { name: string; description?: string }) {
  return http.post<RbacGroup>("/rbac/groups", body);
}

export function updateRbacGroup(id: number, body: { name?: string; description?: string }) {
  return http.put<RbacGroup>(`/rbac/groups/${id}`, body);
}

export function deleteRbacGroup(id: number) {
  return http.del<{ id: number }>(`/rbac/groups/${id}`);
}

/** 全量替换该组的成员。 */
export function setRbacGroupMembers(id: number, user_ids: number[]) {
  return http.post<{ id: number; user_ids: number[] }>(`/rbac/groups/${id}/members`, {
    user_ids,
  });
}

/**
 * 全量替换该组的权限矩阵。
 *
 * 超管专属那两项（新增订阅 / 运行离线下载）后端会**静默丢弃**，不报错。
 * 界面在渲染时会把它们显示为只读，所以正常路径下不会出现在这里；
 * 万一真传了也是安全的一侧 —— 少一项授权，而不是整个提交失败。
 */
export function setRbacGroupPermissions(id: number, permissions: Record<string, RbacEffect>) {
  // 返回的是**后端实际存下来的**那一份（含被静默丢弃的超管专属项），
  // 界面用这个覆盖本地状态，而不是拿提交前那份回显 ——
  // 两者不一致时，「界面上还勾着但库里没有」会让人误以为没生效。
  return http.post<{ id: number; permissions: Record<string, RbacEffect> }>(
    `/rbac/groups/${id}/permissions`,
    { permissions },
  );
}