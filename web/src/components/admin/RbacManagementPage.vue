<script setup lang="ts">
// 用户与权限管理页。
//
// 三块：用户、用户组、权限矩阵。
//
// 这个页面上唯一的规则性认知是**三态**：组权限与用户级覆盖都是稀疏的，
// 「继承」是第三种取值，不是「拒绝」也不是「允许」。所以每一格都是三选一，
// 而��是勾选框 —— 用勾选框表达三态，管理员会以为「不勾 = 拒绝」，
// 然后把上层给的权限一条条覆盖成拒绝，问题排查起来极其痛苦。
//
// 界面**不重复实现任何判定**。哪些项是超管专属、当前会话能不能跑，
// 全部由后端在 permissions 响应的 super_only / allowed / source 里给出。
// 这里只做展示与提交。
import { computed, onMounted, ref } from "vue";
import { ApiError, getApiErrorMessage } from "@/api/client";
import {
  createRbacGroup,
  createRbacUser,
  deleteRbacGroup,
  deleteRbacUser,
  fetchRbacGroups,
  fetchRbacMe,
  fetchRbacPermissions,
  fetchRbacUserOverrides,
  fetchRbacUsers,
  setRbacGroupMembers,
  setRbacGroupPermissions,
  setRbacUserEnabled,
  setRbacUserGroups,
  setRbacUserOverrides,
  setRbacUserPassword,
  updateRbacGroup,
  updateRbacUser,
  type RbacCategory,
  type RbacEffect,
  type RbacGroup,
  type RbacPermission,
  type RbacUser,
} from "@/api/rbac";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import FormField from "@/components/base/FormField.vue";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import AdminStatusPill from "@/components/admin/AdminStatusPill.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import SectionTabBar from "@/components/admin/SectionTabBar.vue";
import { confirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import "@/styles/admin-table.css";
import "@/styles/settings-panel.css";

type Tab = "users" | "groups" | "matrix";

const EFFECT_OPTIONS: { value: RbacEffect; label: string }[] = [
  { value: "", label: "继承" },
  { value: "allow", label: "允许" },
  { value: "deny", label: "拒绝" },
];

/**
 * <select> 的取值来自 DOM，只能是 string，所以在这里收窄成 RbacEffect。
 * 认不出来的值一律当「继承」：宁可让人重新点一次，也不要把一个
 * 非法字符串原样提交给后端 —— 后端会把未知键静默丢弃，
 * 界面却显示成「已保存」，等于是骗人。
 */
function toEffect(raw: string): RbacEffect {
  return raw === "allow" || raw === "deny" ? raw : "";
}

// ---- 总体状态 ----
const loading = ref(false);
const loadError = ref("");
// disabled 为真表示后端把功能关着（总开关关闭或未装配）。
// 这不是一个错误状态，所以单独一个字段，不混进 loadError。
const disabled = ref(false);

const me = ref<{ enabled: boolean; username: string; is_super: boolean } | null>(null);

const tab = ref<Tab>("users");
const tabs = computed(() => [
  { key: "users" as const, label: `用户（${users.value.length}）` },
  { key: "groups" as const, label: `用户组（${groups.value.length}）` },
  { key: "matrix" as const, label: "权限矩阵" },
]);

const users = ref<RbacUser[]>([]);
const groups = ref<RbacGroup[]>([]);
const permissions = ref<RbacPermission[]>([]);
const categories = ref<RbacCategory[]>([]);
const superOnlyKeys = ref<string[]>([]);

// 权限目录按分类分组，矩阵页直接按这个顺序渲染。
const permissionsByCategory = computed(() => {
  const order = new Map(categories.value.map((c, i) => [c.key, i]));
  const buckets = new Map<string, RbacPermission[]>();
  for (const p of permissions.value) {
    const arr = buckets.get(p.category) ?? [];
    arr.push(p);
    buckets.set(p.category, arr);
  }
  return [...buckets.entries()]
    .sort((a, b) => (order.get(a[0]) ?? 99) - (order.get(b[0]) ?? 99))
    .map(([key, items]) => ({
      key,
      label: categories.value.find((c) => c.key === key)?.label ?? key,
      items: items.slice().sort((a, b) => a.sort_order - b.sort_order),
    }));
});

/**
 * 功能未启用时后端返回 501（error_type = NOT_IMPLEMENT）。
 *
 * 刻意按 status 判，不去正则匹配中文文案：文案一改，判定就静默失灵，
 * 表现是「明明没开功能，页面却报一句看不懂的加载失败」。
 */
function isDisabledError(err: unknown): boolean {
  return err instanceof ApiError && err.status === 501;
}

async function loadAll() {
  loading.value = true;
  loadError.value = "";
  try {
    const [m, perms, us, gs] = await Promise.all([
      fetchRbacMe(),
      fetchRbacPermissions(),
      fetchRbacUsers(),
      fetchRbacGroups(),
    ]);
    me.value = m;
    permissions.value = perms.items;
    categories.value = perms.categories;
    superOnlyKeys.value = perms.super_only;
    users.value = us;
    groups.value = gs;
    disabled.value = !m.enabled;
  } catch (err) {
    if (isDisabledError(err)) {
      disabled.value = true;
    } else {
      loadError.value = getApiErrorMessage(err, "加载用户与权限失败");
    }
  } finally {
    loading.value = false;
  }
}

onMounted(loadAll);

// ---- 用户 ----
const userModalOpen = ref(false);
const userEditing = ref<RbacUser | null>(null);
const userForm = ref({ username: "", display_name: "", password: "" });
const userFormError = ref("");
const userSaving = ref(false);

function openCreateUser() {
  userEditing.value = null;
  userForm.value = { username: "", display_name: "", password: "" };
  userFormError.value = "";
  userModalOpen.value = true;
}

function openEditUser(u: RbacUser) {
  userEditing.value = u;
  // 登录名不给改：它出现在登录框、旧会话和审计记录里，
  // 改了就追不回「当时在组里的那个人是谁」。
  userForm.value = { username: u.username, display_name: u.display_name, password: "" };
  userFormError.value = "";
  userModalOpen.value = true;
}

async function submitUser() {
  userFormError.value = "";
  const editing = userEditing.value;
  if (!editing) {
    if (!userForm.value.username.trim()) {
      userFormError.value = "请填写用户名";
      return;
    }
    if (!userForm.value.password) {
      userFormError.value = "请设置初始密码";
      return;
    }
  }
  userSaving.value = true;
  try {
    if (editing) {
      await updateRbacUser(editing.id, { display_name: userForm.value.display_name.trim() });
      toast.success("已保存");
    } else {
      await createRbacUser({
        username: userForm.value.username.trim(),
        display_name: userForm.value.display_name.trim(),
        password: userForm.value.password,
      });
      toast.success("已创建用户");
    }
    userModalOpen.value = false;
    await loadAll();
  } catch (err) {
    userFormError.value = getApiErrorMessage(err, "保存失败");
  } finally {
    userSaving.value = false;
  }
}

async function toggleUserEnabled(u: RbacUser) {
  try {
    await setRbacUserEnabled(u.id, !u.enabled);
    toast.success(u.enabled ? "已停用" : "已启用");
    await loadAll();
  } catch (err) {
    toast.error(getApiErrorMessage(err, "操作失败"));
  }
}

async function removeUser(u: RbacUser) {
  const ok = await confirm({
    title: `删除用户「${u.username}」`,
    message: "删除后该用户的登录名立即失效，它在用户组里的成员关系和用户级覆盖也会一并清除。此操作不可撤销。",
    confirmText: "删除",
  }).catch(() => false);
  if (!ok) return;
  try {
    await deleteRbacUser(u.id);
    toast.success("已删除");
    await loadAll();
  } catch (err) {
    toast.error(getApiErrorMessage(err, "删除失败"));
  }
}

// ---- 重置密码 ----
const passwordModalOpen = ref(false);
const passwordTarget = ref<RbacUser | null>(null);
const passwordValue = ref("");
const passwordSaving = ref(false);

function openResetPassword(u: RbacUser) {
  passwordTarget.value = u;
  passwordValue.value = "";
  passwordModalOpen.value = true;
}

async function submitPassword() {
  const target = passwordTarget.value;
  if (!target) return;
  if (!passwordValue.value) {
    toast.error("请填写新密码");
    return;
  }
  passwordSaving.value = true;
  try {
    await setRbacUserPassword(target.id, passwordValue.value);
    toast.success("密码已重置");
    passwordModalOpen.value = false;
    await loadAll();
  } catch (err) {
    toast.error(getApiErrorMessage(err, "重置失败"));
  } finally {
    passwordSaving.value = false;
  }
}

// ---- 用户所属组 ----
const membershipModalOpen = ref(false);
const membershipTarget = ref<RbacUser | null>(null);
const membershipDraft = ref<number[]>([]);

function openMembership(u: RbacUser) {
  membershipTarget.value = u;
  // 从 group.member_ids 反查，避免再发一次请求。
  const mine = groups.value.filter((g) => g.member_ids.includes(u.id)).map((g) => g.id);
  membershipDraft.value = mine;
  membershipModalOpen.value = true;
}

function toggleMembershipDraft(gid: number, checked: boolean) {
  const set = new Set(membershipDraft.value);
  if (checked) set.add(gid);
  else set.delete(gid);
  membershipDraft.value = [...set].sort((a, b) => a - b);
}

async function submitMembership() {
  const target = membershipTarget.value;
  if (!target) return;
  try {
    await setRbacUserGroups(target.id, membershipDraft.value);
    toast.success("已保存所属组");
    membershipModalOpen.value = false;
    await loadAll();
  } catch (err) {
    toast.error(getApiErrorMessage(err, "保存失败"));
  }
}

// ---- 用户级覆盖 ----
const overrideModalOpen = ref(false);
const overrideTarget = ref<RbacUser | null>(null);
const overrideDraft = ref<Record<string, RbacEffect>>({});

function openOverrides(u: RbacUser) {
  overrideTarget.value = u;
  overrideDraft.value = {};
  overrideModalOpen.value = true;
  fetchRbacUserOverrides(u.id)
    .then((r) => {
      overrideDraft.value = { ...r.overrides };
    })
    .catch((err) => toast.error(getApiErrorMessage(err, "读取用户级覆盖失败")));
}

function setOverrideDraft(key: string, raw: string) {
  overrideDraft.value = { ...overrideDraft.value, [key]: toEffect(raw) };
}

async function submitOverrides() {
  const target = overrideTarget.value;
  if (!target) return;
  try {
    // 整体提交，键值对里空串表示「继承」——后端会删掉那一行，
    // 而不是在库里留一条指向「空效果」的死记录。
    await setRbacUserOverrides(target.id, overrideDraft.value);
    toast.success("已保存用户级覆盖");
    overrideModalOpen.value = false;
    await loadAll();
  } catch (err) {
    toast.error(getApiErrorMessage(err, "保存失败"));
  }
}

// ---- 用户组 ----
const groupModalOpen = ref(false);
const groupEditing = ref<RbacGroup | null>(null);
const groupForm = ref({ name: "", description: "" });
const groupFormError = ref("");

function openCreateGroup() {
  groupEditing.value = null;
  groupForm.value = { name: "", description: "" };
  groupFormError.value = "";
  groupModalOpen.value = true;
}

function openEditGroup(g: RbacGroup) {
  groupEditing.value = g;
  groupForm.value = { name: g.name, description: g.description };
  groupFormError.value = "";
  groupModalOpen.value = true;
}

async function submitGroup() {
  groupFormError.value = "";
  const editing = groupEditing.value;
  if (!groupForm.value.name.trim()) {
    groupFormError.value = "请填写组名";
    return;
  }
  try {
    if (editing) {
      await updateRbacGroup(editing.id, {
        name: groupForm.value.name.trim(),
        description: groupForm.value.description.trim(),
      });
      toast.success("已保存");
    } else {
      await createRbacGroup({
        name: groupForm.value.name.trim(),
        description: groupForm.value.description.trim(),
      });
      toast.success("已创建用户组");
    }
    groupModalOpen.value = false;
    await loadAll();
  } catch (err) {
    groupFormError.value = getApiErrorMessage(err, "保存失败");
  }
}

async function removeGroup(g: RbacGroup) {
  const ok = await confirm({
    title: `删除用户组「${g.name}」`,
    message: "删除后该组的成员关系和权限配置会一并清除，属于该组的授权立刻失效。此操作不可撤销。",
    confirmText: "删除",
  }).catch(() => false);
  if (!ok) return;
  try {
    await deleteRbacGroup(g.id);
    toast.success("已删除");
    await loadAll();
  } catch (err) {
    toast.error(getApiErrorMessage(err, "删除失败"));
  }
}

// ---- 组成员 ----
const memberModalOpen = ref(false);
const memberTarget = ref<RbacGroup | null>(null);
const memberDraft = ref<number[]>([]);

function openMembers(g: RbacGroup) {
  memberTarget.value = g;
  memberDraft.value = [...g.member_ids];
  memberModalOpen.value = true;
}

function toggleMemberDraft(uid: number, checked: boolean) {
  const set = new Set(memberDraft.value);
  if (checked) set.add(uid);
  else set.delete(uid);
  memberDraft.value = [...set].sort((a, b) => a - b);
}

async function submitMembers() {
  const target = memberTarget.value;
  if (!target) return;
  try {
    await setRbacGroupMembers(target.id, memberDraft.value);
    toast.success("已保存成员");
    memberModalOpen.value = false;
    await loadAll();
  } catch (err) {
    toast.error(getApiErrorMessage(err, "保存失败"));
  }
}

// ---- 权限矩阵（草稿 + 逐行保存）----
const draft = ref<Record<string, Record<string, RbacEffect>>>({});

function storedEffect(groupId: number, key: string): string {
  return groups.value.find((g) => g.id === groupId)?.permissions[key] ?? "";
}

/**
 * 读某一格的当前取值：**草稿优先，回落到库里存的值**。
 *
 * 回落这一步是必须的，不是保险。草稿只存「改过的格」，刷新页面后草稿清空；
 * 如果直接返回 draft[gid][key]，界面会显示「继承」，而库里其实存着「允许」——
 * 管理员会以为上一轮保存没生效，然后重新勾一遍。
 */
function draftEffect(groupId: number, key: string): string {
  const gid = String(groupId);
  const row = draft.value[gid];
  if (row && key in row) return row[key];
  return storedEffect(groupId, key);
}

function setDraftEffect(groupId: number, key: string, raw: string) {
  const gid = String(groupId);
  const row = { ...(draft.value[gid] ?? {}) };
  const value = toEffect(raw);
  if (value) row[key] = value;
  else delete row[key];
  draft.value = { ...draft.value, [gid]: row };
}

function isSuperOnly(key: string): boolean {
  return superOnlyKeys.value.includes(key);
}

/** 某一行是否有未保存的改动。比对的是「解析后的取值」，不是草稿键集合。 */
function rowDirty(groupId: number): boolean {
  const g = groups.value.find((x) => x.id === groupId);
  if (!g) return false;
  const gid = String(groupId);
  const current = draft.value[gid] ?? {};
  const keys = new Set([
    ...Object.keys(g.permissions),
    ...Object.keys(current),
    ...permissions.value.map((p) => p.key),
  ]);
  for (const k of keys) if (storedEffect(groupId, k) !== (current[k] ?? storedEffect(groupId, k))) return true;
  return false;
}

/** 提交一行的改动，成功后用后端返回的清单覆盖本地草稿（不重新拉全量）。 */
async function persistGroupRow(group: RbacGroup): Promise<boolean> {
  const gid = String(group.id);
  try {
    const r = await setRbacGroupPermissions(group.id, draft.value[gid] ?? {});
    // 用**后端返回的**那一份覆盖草稿：超管专属项会被静默丢弃，
    // 界面上如果还留着勾选会让人以为没生效。
    draft.value = { ...draft.value, [gid]: { ...r.permissions } };
    // 落库结果也要立刻反映到 groups 上，否则 rowDirty 拿旧值对比，
    // 会认为「改完了但还是脏的」，保存按钮一直亮着。
    groups.value = groups.value.map((g) =>
      g.id === group.id ? { ...g, permissions: { ...r.permissions } } : g,
    );
    return true;
  } catch (err) {
    toast.error(getApiErrorMessage(err, `保存「${group.name}」失败`));
    return false;
  }
}

async function saveGroupRow(group: RbacGroup) {
  if (await persistGroupRow(group)) {
    toast.success(`已保存「${group.name}」的权限`);
    await loadAll();
  }
}

function revertGroupRow(group: RbacGroup) {
  const gid = String(group.id);
  // 清掉草稿即可：draftEffect 会自动回落到库里存的值。
  const { [gid]: _dropped, ...rest } = draft.value;
  draft.value = rest;
}

async function saveAllRows() {
  const dirty = groups.value.filter((g) => rowDirty(g.id));
  if (!dirty.length) {
    toast.info("没有待保存的改动");
    return;
  }
  let ok = 0;
  for (const g of dirty) {
    if (await persistGroupRow(g)) ok += 1;
  }
  toast.success(`已保存 ${ok} 个用户组的权限`);
  await loadAll();
}
</script>

<template>
  <div class="rbac">
    <AppStateBlock v-if="loading" message="加载中…" loading min-height="160px" />

    <SettingsCard v-else-if="disabled" title="用户与权限" accent="var(--brand)">
      <AdminEmptyState
        icon="lock"
        title="用户与权限未启用"
        description="功能默认关闭，此时后台行为与开启前完全一致：只有配置里的管理员账号能登录，没有任何权限判定。到「系统设置 → 其他设置」把「启用用户与权限」打开即可。"
      />
    </SettingsCard>

    <AppStateBlock v-else-if="loadError" :message="loadError" min-height="160px" />

    <template v-else>
      <SettingsCard title="用户与权限" accent="var(--brand)">
        <template #head-aside>
          <AdminStatusPill :tone="me?.is_super ? 'brand' : 'muted'">
            {{ me?.is_super ? `当前：${me?.username}（超级管理员）` : `当前：${me?.username}` }}
          </AdminStatusPill>
        </template>

        <p class="rbac__note">
          配置里的管理员账号（{{ me?.username }}）自动就是超级管理员，绕过一切权限，且不能被停用、删除或改权限 —— 他在用户列表里根本不会出现。其余账号必须由下面建出来、并加入用户组，才有相应权限。
        </p>
        <p class="rbac__note">
          有两项永远是超级管理员专属、不能下放：<b>新增订阅</b>和<b>运行离线下载</b> —— 它们花的是站点账号的额度，不能由别人代你花。
        </p>

        <SectionTabBar v-model="tab" :tabs="tabs" />
      </SettingsCard>

      <!-- 用户 -->
      <SettingsCard v-if="tab === 'users'" title="用户" accent="var(--brand)">
        <template #head-actions>
          <AppButton type="button" variant="secondary" @click="openCreateUser">新建用户</AppButton>
        </template>

        <AdminEmptyState v-if="!users.length" title="还没有受管用户" description="点右上角新建。建出来的账号可以登录后台，权限由它所属的用户组决定。" />

        <div v-else class="rbac__table-scroll">
          <table class="admin-table">
            <thead>
              <tr>
                <th>用户名</th>
                <th>显示名</th>
                <th>所属组</th>
                <th>状态</th>
                <th>最后登录</th>
                <th class="rbac__col-actions"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="u in users" :key="u.id">
                <td class="rbac__mono">{{ u.username }}</td>
                <td>{{ u.display_name || "—" }}</td>
                <td>
                  <span v-if="!u.groups.length" class="rbac__muted">未加入任何组</span>
                  <template v-else>
                    <AdminStatusPill v-for="g in u.groups" :key="g" tone="muted" class="rbac__pill-gap">
                      {{ g }}
                    </AdminStatusPill>
                  </template>
                </td>
                <td>
                  <AdminStatusPill :tone="u.enabled ? 'success' : 'muted'">
                    {{ u.enabled ? "启用" : "停用" }}
                  </AdminStatusPill>
                </td>
                <td class="rbac__muted">{{ u.last_login_at || "从未登录" }}</td>
                <td class="rbac__col-actions">
                  <AppButton type="button" variant="secondary" size="sm" @click="openMembership(u)">所属组</AppButton>
                  <AppButton type="button" variant="secondary" size="sm" @click="openOverrides(u)">用户级覆盖</AppButton>
                  <AppButton type="button" variant="secondary" size="sm" @click="openResetPassword(u)">重置密码</AppButton>
                  <AppButton type="button" variant="secondary" size="sm" @click="openEditUser(u)">编辑</AppButton>
                  <AppButton type="button" variant="secondary" size="sm" @click="toggleUserEnabled(u)">
                    {{ u.enabled ? "停用" : "启用" }}
                  </AppButton>
                  <AppButton type="button" variant="danger" size="sm" @click="removeUser(u)">删除</AppButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </SettingsCard>

      <!-- 用户组 -->
      <SettingsCard v-else-if="tab === 'groups'" title="用户组" accent="var(--brand)">
        <template #head-actions>
          <AppButton type="button" variant="secondary" @click="openCreateGroup">新建用户组</AppButton>
        </template>

        <p class="rbac__note">
          用户属于一个或多个组，最终权限由所在各组**合并**得出：任何一组拒绝就拒绝；没有组拒绝时，只要有任一组允许就允许。两个内置组（普通用户、求片审核员）不能删除。
        </p>

        <AdminEmptyState v-if="!groups.length" title="还没有用户组" description="至少建一个组并授予权限，新建的账号才会有可用的菜单。" />

        <div v-else class="rbac__table-scroll">
          <table class="admin-table">
            <thead>
              <tr>
                <th>组名</th>
                <th>说明</th>
                <th>成员数</th>
                <th>已授权限项</th>
                <th class="rbac__col-actions"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="g in groups" :key="g.id">
                <td>
                  {{ g.name }}
                  <AdminStatusPill v-if="g.builtin" tone="muted" class="rbac__pill-gap">内置</AdminStatusPill>
                </td>
                <td class="rbac__muted">{{ g.description || "—" }}</td>
                <td>{{ g.member_count }}</td>
                <td class="rbac__muted">{{ Object.keys(g.permissions).length }}</td>
                <td class="rbac__col-actions">
                  <AppButton type="button" variant="secondary" size="sm" @click="openMembers(g)">成员</AppButton>
                  <AppButton type="button" variant="secondary" size="sm" @click="openEditGroup(g)">编辑</AppButton>
                  <AppButton type="button" variant="danger" size="sm" :disabled="g.builtin" @click="removeGroup(g)">
                    删除
                  </AppButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </SettingsCard>

      <!-- 权限矩阵 -->
      <SettingsCard v-else title="权限矩阵" accent="var(--brand)">
        <template #head-actions>
          <AppButton type="button" @click="saveAllRows">保存全部改动</AppButton>
        </template>

        <p class="rbac__note">
          每格三选一：<b>继承</b>（这一项不表态，看用户级覆盖与其它组）· <b>允许</b> · <b>拒绝</b>。<b>拒绝优先于允许</b> —— 同一个用户只要所在任意一组拒绝了，这一项就是拒绝。标为「超管专属」的两行不可点，永远不下放。
        </p>

        <div class="rbac__table-scroll">
          <table class="admin-table rbac__matrix">
            <thead>
              <tr>
                <th class="rbac__col-perm">权限项</th>
                <th v-for="g in groups" :key="g.id">{{ g.name }}</th>
              </tr>
            </thead>
            <tbody v-for="cat in permissionsByCategory" :key="cat.key">
              <tr class="rbac__matrix-cat">
                <td :colspan="groups.length + 1">{{ cat.label }}</td>
              </tr>
              <tr v-for="p in cat.items" :key="p.key">
                <td class="rbac__col-perm">
                  {{ p.label }}
                  <AdminStatusPill v-if="isSuperOnly(p.key)" tone="warning" class="rbac__pill-gap">超管专属</AdminStatusPill>
                </td>
                <td v-for="g in groups" :key="g.id">
                  <select
                    v-if="!isSuperOnly(p.key)"
                    class="rbac__select"
                    :value="draftEffect(g.id, p.key)"
                    @change="setDraftEffect(g.id, p.key, ($event.target as HTMLSelectElement).value)"
                  >
                    <option v-for="o in EFFECT_OPTIONS" :key="o.value" :value="o.value">{{ o.label }}</option>
                  </select>
                  <span v-else class="rbac__muted">不下放</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-for="g in groups" :key="g.id" class="rbac__row-actions">
          <span class="rbac__muted">{{ g.name }}</span>
          <AppButton type="button" variant="secondary" size="sm" :disabled="!rowDirty(g.id)" @click="revertGroupRow(g)">
            撤销
          </AppButton>
          <AppButton type="button" size="sm" :disabled="!rowDirty(g.id)" @click="saveGroupRow(g)">保存该组</AppButton>
        </div>
      </SettingsCard>
    </template>

    <!-- 新建/编辑用户 -->
    <AppModal
      :open="userModalOpen"
      :title="userEditing ? '编辑用户' : '新建用户'"
      @close="userModalOpen = false"
    >
      <div class="rbac__form">
        <FormField v-if="!userEditing" label="用户名" required>
          <AppInput v-model="userForm.username" placeholder="登录名，创建后不可修改" />
        </FormField>
        <FormField v-else label="用户名">
          <AppInput :model-value="userEditing.username" disabled />
        </FormField>
        <FormField label="显示名">
          <AppInput v-model="userForm.display_name" placeholder="可选，只用于界面显示" />
        </FormField>
        <FormField v-if="!userEditing" label="初始密码" required>
          <AppInput v-model="userForm.password" type="password" placeholder="至少 8 位" />
        </FormField>
        <p v-if="userFormError" class="rbac__error">{{ userFormError }}</p>
      </div>
      <template #footer>
        <AppButton type="button" variant="cancel" @click="userModalOpen = false">取消</AppButton>
        <AppButton type="button" :disabled="userSaving" @click="submitUser">
          {{ userEditing ? "保存" : "创建" }}
        </AppButton>
      </template>
    </AppModal>

    <!-- 重置密码 -->
    <AppModal :open="passwordModalOpen" title="重置密码" @close="passwordModalOpen = false">
      <div class="rbac__form">
        <p class="rbac__note">
          将把「{{ passwordTarget?.username }}」的密码改成下面这个值。该用户下次登录后不需要再改密码。
        </p>
        <FormField label="新密码" required>
          <AppInput v-model="passwordValue" type="password" placeholder="至少 8 位" />
        </FormField>
      </div>
      <template #footer>
        <AppButton type="button" variant="cancel" @click="passwordModalOpen = false">取消</AppButton>
        <AppButton type="button" :disabled="passwordSaving" @click="submitPassword">确认重置</AppButton>
      </template>
    </AppModal>

    <!-- 所属组 -->
    <AppModal :open="membershipModalOpen" title="所属用户组" @close="membershipModalOpen = false">
      <p class="rbac__note">
        「{{ membershipTarget?.username }}」属于哪些组。用户可以同时属于多个组，最终权限按各组合并（拒绝优先）。
      </p>
      <div class="rbac__check-list">
        <label v-for="g in groups" :key="g.id" class="rbac__check">
          <input
            type="checkbox"
            :checked="membershipDraft.includes(g.id)"
            @change="toggleMembershipDraft(g.id, ($event.target as HTMLInputElement).checked)"
          />
          <span>{{ g.name }}</span>
        </label>
      </div>
      <template #footer>
        <AppButton type="button" variant="cancel" @click="membershipModalOpen = false">取消</AppButton>
        <AppButton type="button" @click="submitMembership">保存</AppButton>
      </template>
    </AppModal>

    <!-- 用户级覆盖 -->
    <AppModal :open="overrideModalOpen" title="用户级覆盖" size="lg" @close="overrideModalOpen = false">
      <p class="rbac__note">
        这里是「{{ overrideTarget?.username }}」这一行自己的表态，优先级高于所在组。
        <b>拒绝仍然优先</b>：组里拒绝的这一项，用户级允许也救不回来。选择「继承」表示不表态。
      </p>
      <div class="rbac__table-scroll">
        <table class="admin-table">
          <tbody v-for="cat in permissionsByCategory" :key="cat.key">
            <tr class="rbac__matrix-cat">
              <td :colspan="2">{{ cat.label }}</td>
            </tr>
            <tr v-for="p in cat.items" :key="p.key">
              <td class="rbac__col-perm">
                {{ p.label }}
                <AdminStatusPill v-if="isSuperOnly(p.key)" tone="warning" class="rbac__pill-gap">超管专属</AdminStatusPill>
              </td>
              <td>
                <select
                  v-if="!isSuperOnly(p.key)"
                  class="rbac__select"
                  :value="overrideDraft[p.key] ?? ''"
                  @change="setOverrideDraft(p.key, ($event.target as HTMLSelectElement).value)"
                >
                  <option v-for="o in EFFECT_OPTIONS" :key="o.value" :value="o.value">{{ o.label }}</option>
                </select>
                <span v-else class="rbac__muted">不下放</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <template #footer>
        <AppButton type="button" variant="cancel" @click="overrideModalOpen = false">取消</AppButton>
        <AppButton type="button" @click="submitOverrides">保存</AppButton>
      </template>
    </AppModal>

    <!-- 新建/编辑用户组 -->
    <AppModal :open="groupModalOpen" :title="groupEditing ? '编辑用户组' : '新建用户组'" @close="groupModalOpen = false">
      <div class="rbac__form">
        <FormField label="组名" required>
          <AppInput v-model="groupForm.name" placeholder="例如：运维、审核员" />
        </FormField>
        <FormField label="说明">
          <AppInput v-model="groupForm.description" placeholder="可选" />
        </FormField>
        <p v-if="groupFormError" class="rbac__error">{{ groupFormError }}</p>
      </div>
      <template #footer>
        <AppButton type="button" variant="cancel" @click="groupModalOpen = false">取消</AppButton>
        <AppButton type="button" @click="submitGroup">{{ groupEditing ? "保存" : "创建" }}</AppButton>
      </template>
    </AppModal>

    <!-- 组成员 -->
    <AppModal :open="memberModalOpen" :title="`「${memberTarget?.name ?? ''}」的成员`" @close="memberModalOpen = false">
      <p class="rbac__note">勾上的用户属于这个组。成员关系也可以在用户那边的「所属组」里改，两边等效。</p>
      <div class="rbac__check-list">
        <AdminEmptyState v-if="!users.length" title="还没有用户" description="先到「用户」页建几个账号。" />
        <label v-for="u in users" :key="u.id" class="rbac__check">
          <input
            type="checkbox"
            :checked="memberDraft.includes(u.id)"
            @change="toggleMemberDraft(u.id, ($event.target as HTMLInputElement).checked)"
          />
          <span>{{ u.username }}</span>
        </label>
      </div>
      <template #footer>
        <AppButton type="button" variant="cancel" @click="memberModalOpen = false">取消</AppButton>
        <AppButton type="button" @click="submitMembers">保存</AppButton>
      </template>
    </AppModal>
  </div>
</template>

<style scoped>
.rbac__note {
  margin: 0 0 8px;
  font-size: 13px;
  line-height: 1.7;
  color: var(--text-regular);
}
.rbac__muted {
  color: var(--text-secondary, var(--text-muted, #909399));
  font-size: 12px;
}
.rbac__mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.rbac__pill-gap {
  margin-right: 4px;
}
.rbac__table-scroll {
  overflow-x: auto;
}
.rbac__col-actions {
  white-space: nowrap;
  text-align: right;
}
.rbac__col-actions > * + * {
  margin-left: 4px;
}
.rbac__col-perm {
  min-width: 200px;
}
.rbac__matrix td,
.rbac__matrix th {
  text-align: left;
}
.rbac__matrix-cat td {
  background: var(--surface-muted, rgba(127, 127, 127, 0.08));
  font-weight: 600;
  font-size: 12px;
  color: var(--text-secondary, var(--text-muted, #909399));
}
.rbac__select {
  min-width: 92px;
  padding: 4px 6px;
  font-size: 13px;
}
.rbac__row-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 0;
  border-top: 1px solid var(--border-color, rgba(127, 127, 127, 0.18));
}
.rbac__row-actions > * + * {
  margin-left: 0;
}
.rbac__form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.rbac__error {
  margin: 0;
  color: var(--danger);
  font-size: 13px;
}
.rbac__check-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 320px;
  overflow-y: auto;
  margin-top: 10px;
}
.rbac__check {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  cursor: pointer;
}
</style>