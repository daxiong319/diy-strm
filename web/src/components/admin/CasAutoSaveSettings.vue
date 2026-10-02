<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import { accountsApi } from "@/api/accounts";
import { fetchCasConfig, saveCasConfig, type CasConfig } from "@/api/cas";
import type { Account } from "@/api/types";
import AccountFolderField from "@/components/admin/AccountFolderField.vue";
import FolderPickerModal from "@/components/file/FolderPickerModal.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import FormField from "@/components/base/FormField.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import { toast } from "@/composables/useToast";

// 网盘类型归一：与后端 cas.NormalizeDriveType 保持一致，
// 把驱动名（189_cloud / 139_cloud …）映射为 CAS 清单里的 sourceDrive 取值。
function normalizeDriveType(raw?: string): string {
  const v = (raw ?? "").trim().toLowerCase().replace(/-/g, "_");
  switch (v) {
    case "cloud189":
    case "189cloud":
    case "189_cloud":
    case "189":
    case "tianyi":
      return "cloud189";
    case "cloud139":
    case "pan139":
    case "139cloud":
    case "139_cloud":
    case "139":
    case "yidong":
      return "cloud139";
    case "quark":
      return "quark";
    case "115":
    case "115_open":
    case "115open":
      return "115_open";
    case "123":
    case "123_open":
    case "123open":
    case "123pan":
      return "123_open";
    case "guangya":
      return "guangya";
    default:
      return v;
  }
}

interface DriveRow {
  key: string;
  label: string;
  enabled: boolean;
  accountId: number;
  saveDir: string;
}

// 支持的网盘清单（与后端 cas.AutoSaveCASFiles 的识别类型一致，仅列真正支持秒传的三家）。
const drives = reactive<DriveRow[]>([
  { key: "cloud189", label: "天翼云盘", enabled: true, accountId: 0, saveDir: "" },
  { key: "cloud139", label: "移动云盘", enabled: true, accountId: 0, saveDir: "" },
  { key: "quark", label: "夸克网盘", enabled: true, accountId: 0, saveDir: "" },
]);

const loading = ref(false);
const saving = ref(false);
const errorMsg = ref("");
const accounts = ref<Account[]>([]);

const enabled = ref(true);
const defaultDir = ref("CAS");

// 目录选择器状态：pickingKey 非空表示正在为某个网盘选目录，
// 为 DEFAULT_DIR_KEY 时表示在选「默认保存目录」。
const DEFAULT_DIR_KEY = "__default__";
const pickingKey = ref("");
const pickerOpen = ref(false);

const activeAccounts = computed(() => accounts.value.filter((a) => a.is_active));

// 目录选择器返回的是账号 + 路径：把路径写回对应字段，
// 顺带把账号也一并带过来（用户既然在某个账号里翻了目录，
// 就该转存到那个账号，否则选了目录却存到别的账号会让人费解）。
function driveByKey(key: string): DriveRow | undefined {
  return drives.find((d) => d.key === key);
}

function currentPickingRow(): DriveRow | undefined {
  return driveByKey(pickingKey.value);
}

// 传给 FolderPickerModal 的初始账号：优先本行已选账号，否则用该网盘第一个可用账号。
function pickingAccountId(): number | null {
  if (pickingKey.value === DEFAULT_DIR_KEY) {
    const first = activeAccounts.value[0];
    return first ? first.id : null;
  }
  const row = currentPickingRow();
  if (!row) return null;
  if (row.accountId) return row.accountId;
  const enabledIds = new Set(
    accounts.value
      .filter((a) => a.is_active && normalizeDriveType(a.driver_type) === row.key)
      .map((a) => a.id),
  );
  if (row.accountId && enabledIds.has(row.accountId)) return row.accountId;
  const first = accounts.value.find(
    (a) => a.is_active && normalizeDriveType(a.driver_type) === row.key,
  );
  return first ? first.id : null;
}

function pickingInitialPath(): string {
  if (pickingKey.value === DEFAULT_DIR_KEY) return defaultDir.value;
  return currentPickingRow()?.saveDir ?? "";
}

// 该网盘的可用账号（用于目录选择器里约束可选账号范围）。
function pickingAccounts(): Account[] {
  if (pickingKey.value === DEFAULT_DIR_KEY) return activeAccounts.value;
  const row = currentPickingRow();
  if (!row) return activeAccounts.value;
  const matched = activeAccounts.value.filter(
    (a) => normalizeDriveType(a.driver_type) === row.key,
  );
  // 没有任何匹配账号时不限制，否则用户想选目录却看不到任何账号。
  return matched.length > 0 ? matched : activeAccounts.value;
}

function openDirPicker(key: string) {
  pickingKey.value = key;
  pickerOpen.value = true;
}

function onDirPicked(payload: { accountId: number; path: string }) {
  const path = (payload.path || "/").trim();
  const normalized = path === "/" ? "" : path.replace(/^\/+|\/+$/g, "");
  if (pickingKey.value === DEFAULT_DIR_KEY) {
    defaultDir.value = normalized;
  } else {
    const row = driveByKey(pickingKey.value);
    if (row) {
      // 只有该行还没指定账号（自动选择）时才用选择器里的账号补上，
      // 避免覆盖用户显式指定的转存账号。
      if (!row.accountId && payload.accountId) row.accountId = payload.accountId;
      row.saveDir = normalized;
    }
  }
  pickerOpen.value = false;
  pickingKey.value = "";
}

// 目录文案：空值表示「未单独设置」，此时该网盘会落到上面的默认保存目录。
// 选了网盘根目录也等价于空（后端约定空目录即根目录），因此统一按未设置展示。
const defaultDirLabel = computed(() => {
  const dir = defaultDir.value.trim().replace(/^\/+|\/+$/g, "");
  return dir ? `/${dir}` : "";
});

const defaultDirTitle = computed(() =>
  defaultDirLabel.value
    ? `转存到该网盘根目录下的 ${defaultDirLabel.value}`
    : "未设置：转存到各网盘根目录",
);

const pickerTitle = computed(() => {
  if (pickingKey.value === DEFAULT_DIR_KEY) return "选择默认保存目录";
  const row = currentPickingRow();
  return row ? `选择「${row.label}」保存目录` : "选择保存目录";
});

function rowDirLabel(row: DriveRow): string {
  const dir = row.saveDir.trim().replace(/^\/+|\/+$/g, "");
  if (!dir) return "";
  const acc = accounts.value.find((a) => a.id === row.accountId);
  return acc?.name ? `${acc.name}·/${dir}` : `/${dir}`;
}

// 该行未单独设置目录时会回落到哪个目录——把这个去向写进提示，用户才不用猜。
const rowDirFallbackTitle = computed(() =>
  defaultDirLabel.value
    ? `未单独设置：将使用上方默认目录 ${defaultDirLabel.value}`
    : "未单独设置：将转存到该网盘根目录",
);

// 每个网盘可选账号（按归一后的驱动类型过滤，仅取启用账号）。
const accountOptions = computed<Record<string, { value: number; label: string }[]>>(() => {
  const out: Record<string, { value: number; label: string }[]> = {};
  for (const row of drives) {
    const opts = accounts.value
      .filter((a) => a.is_active && normalizeDriveType(a.driver_type) === row.key)
      .map((a) => ({ value: a.id, label: a.name || `账号 #${a.id}` }));
    out[row.key] = [{ value: 0, label: "自动选择" }, ...opts];
  }
  return out;
});

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    const [cfg, list] = await Promise.all([fetchCasConfig(), accountsApi.list()]);
    accounts.value = list ?? [];

    enabled.value = cfg.cas_notify_auto_save ?? true;
    if (typeof cfg.cas_notify_auto_save_dir === "string" && cfg.cas_notify_auto_save_dir !== "") {
      defaultDir.value = cfg.cas_notify_auto_save_dir;
    }
    const saved = cfg.cas_notify_auto_save_drives ?? {};
    for (const row of drives) {
      const item = saved[row.key];
      row.enabled = item ? item.enabled !== false : true;
      row.accountId = item?.account_id ?? 0;
      row.saveDir = item?.save_dir ?? "";
    }
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载 CAS 自动转存配置失败");
  } finally {
    loading.value = false;
  }
}

async function save() {
  saving.value = true;
  try {
    // 读取-修改-写回：只覆盖自动转存相关字段，避免与 CAS 自动化配置互相覆盖。
    const current: CasConfig = await fetchCasConfig();
    const driveMap: Record<string, { enabled: boolean; account_id: number; save_dir: string }> = {};
    for (const row of drives) {
      driveMap[row.key] = {
        enabled: row.enabled,
        account_id: Number(row.accountId) || 0,
        save_dir: row.saveDir.trim().replace(/^\/+|\/+$/g, ""),
      };
    }
    await saveCasConfig({
      ...current,
      cas_notify_auto_save: enabled.value,
      cas_notify_auto_save_dir: defaultDir.value.trim().replace(/^\/+|\/+$/g, ""),
      cas_notify_auto_save_drives: driveMap,
    });
    toast.success("CAS 自动转存配置已保存");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "保存 CAS 自动转存配置失败"));
  } finally {
    saving.value = false;
  }
}

onMounted(load);
</script>

<template>
  <SettingsCard title="CAS 通知自动转存" accent="var(--brand)">
    <AppStateBlock v-if="loading" message="加载中…" loading min-height="140px" />
    <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="140px" />

    <div v-else class="cas-auto">
      <p class="cas-auto__tip">
        在通知渠道（如 Telegram Bot）里给机器人发送 <code>.cas</code> 清单文件，系统会自动判定该清单归属的网盘，
        并保存到你在这里设置的目录中。
      </p>

      <label class="cas-auto__switch">
        <input v-model="enabled" type="checkbox" />
        <span>启用通知渠道 .cas 文件自动转存</span>
      </label>

      <FormField label="默认保存目录（未单独配置的网盘使用此目录）" class="cas-auto__dir">
        <AccountFolderField
          :display="defaultDirLabel"
          :title="defaultDirTitle"
          placeholder="未设置：转存到各网盘根目录"
          @browse="openDirPicker(DEFAULT_DIR_KEY)"
        />
      </FormField>

      <div class="cas-auto__table-wrap">
        <table class="cas-auto__table">
          <thead>
            <tr>
              <th>网盘</th>
              <th>转存账号</th>
              <th>保存目录</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in drives" :key="row.key">
              <td>
                <label class="cas-auto__drive">
                  <input v-model="row.enabled" type="checkbox" />
                  <span>{{ row.label }}</span>
                </label>
              </td>
              <td>
                <AppSelect v-model="row.accountId" :options="accountOptions[row.key] ?? []" />
              </td>
              <td>
                <AccountFolderField
                  :display="rowDirLabel(row)"
                  :title="rowDirLabel(row) || rowDirFallbackTitle"
                  placeholder="未设置：使用上方默认目录"
                  @browse="openDirPicker(row.key)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="cas-auto__actions">
        <AppButton type="button" variant="primary" :disabled="saving" @click="save">
          {{ saving ? "保存中…" : "保存配置" }}
        </AppButton>
      </div>
    </div>

    <FolderPickerModal
      :open="pickerOpen"
      :accounts="pickingAccounts()"
      :account-id="pickingAccountId()"
      :initial-path="pickingInitialPath()"
      :allow-create-folder="true"
      :title="pickerTitle"
      confirm-text="使用此目录"
      @close="pickerOpen = false"
      @resolve="onDirPicked"
    />
  </SettingsCard>
</template>

<style scoped>
.cas-auto {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.cas-auto__tip {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
  color: var(--text-muted, #6b7280);
}

.cas-auto__tip code {
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--surface-2, rgba(148, 163, 184, 0.16));
  font-size: 12px;
}

.cas-auto__switch,
.cas-auto__drive {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text-regular);
}

.cas-auto__dir {
  max-width: 320px;
}

.cas-auto__table-wrap {
  overflow-x: auto;
}

.cas-auto__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.cas-auto__table th,
.cas-auto__table td {
  padding: 8px 10px;
  text-align: left;
  vertical-align: middle;
  border-bottom: 1px solid var(--border-soft, #232733);
}

.cas-auto__table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted, #6b7280);
}

.cas-auto__table td:nth-child(2),
.cas-auto__table th:nth-child(2) {
  width: 200px;
}

.cas-auto__table td:nth-child(3),
.cas-auto__table th:nth-child(3) {
  width: 300px;
}

.cas-auto__actions {
  display: flex;
  justify-content: flex-end;
}
</style>
