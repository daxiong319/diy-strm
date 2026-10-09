<script setup lang="ts">
// 免登录分享的管理面板（分享管理页的第三个 tab）。
//
// 五块东西：创建、列表、改有效期、撤销、看统计。放在一个面板里而不是拆成
// 独立页面，是因为这四件事共用一个心智模型（「一条分享」），拆开之后
// 「撤销」和「看统计」会离得比预期远。
//
// 三个需要解释的取舍：
//
// 1. **明文短码只出现一次**。服务端只在创建那一次响应里回传 code/url，
//    之后列表里再拿不到了。所以创建成功后弹一次「复制链接」，并明说
//    「关掉就找不回来了」—— 这不是界面小气，是短码本身就是凭证的一部分。
//
// 2. **有效设备数是显示给人看的决策依据**，不是装饰。它和 max_devices 放在
//    同一列是因为这两数必须对着看：顶满时新访客会被 429 挡掉，
//    管理员只有在这里能提前看出来「该提上限了」。
//
// 3. **统计弹窗里的访客 IP 是脱敏网段**（IPv4 /24、IPv6 /64），
//    跟库里存的一致。这里不做二次还原：还原功能等于把一个隐私承诺变成
//    一个可被顺手调用的事故源。
import { computed, ref, watch } from "vue";
import { getApiErrorMessage } from "@/api/client";
import { filesApi } from "@/api/files";
import {
  EXPIRE_DAY_OPTIONS,
  libraryShareApi,
  type LibraryShareCreateResult,
  type LibraryShareItem,
  type LibraryShareStats,
} from "@/api/libraryShare";
import type { Account, FileItem } from "@/api/types";
import { accountsApi } from "@/api/accounts";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import AdminStatusPill from "@/components/admin/AdminStatusPill.vue";
import type { AdminStatusPillTone } from "@/components/admin/AdminStatusPill.vue";
import FormField from "@/components/base/FormField.vue";
import { copyTextToClipboard, toast } from "@/composables/useToast";
import { formatTime } from "@/utils/format";
import "@/styles/admin-table.css";

const MAX_TITLE_LEN = 200;
const MAX_COMMENT_LEN = 500;

const items = ref<LibraryShareItem[]>([]);
const loading = ref(false);
const loadError = ref("");

async function load() {
  loading.value = true;
  loadError.value = "";
  try {
    items.value = (await libraryShareApi.list()).items;
  } catch (e) {
    loadError.value = getApiErrorMessage(e, "加载分享列表失败");
  } finally {
    loading.value = false;
  }
}

void load();

// ---------------------------------------------------------------- 创建

const createOpen = ref(false);
const creating = ref(false);
const accounts = ref<Account[]>([]);
const createErr = ref("");

const form = ref({
  accountID: 0,
  fileID: "",
  title: "",
  season: "-1",
  comment: "",
  password: "",
  expireDays: -1, // -1 = 用服务端默认值
  maxDevices: -1, // -1 = 用服务端默认值
});

const accountOptions = computed(() =>
  accounts.value.map((a) => ({ value: a.id, label: a.name })),
);

const expireOptions = computed(() => {
  const list = EXPIRE_DAY_OPTIONS.map((o) => ({
    value: o.value,
    label: o.label,
  }));
  // -1 排在最前：默认项必须显式可见，否则用户不知道自己没选就是 7 天。
  return [{ value: -1, label: "默认（按系统设置）" }, ...list];
});

function resetForm() {
  form.value = {
    accountID: accounts.value[0]?.id ?? 0,
    fileID: "",
    title: "",
    season: "-1",
    comment: "",
    password: "",
    expireDays: -1,
    maxDevices: -1,
  };
  createErr.value = "";
}

async function openCreate() {
  resetForm();
  if (!accounts.value.length) {
    try {
      accounts.value = await accountsApi.list();
    } catch (e) {
      createErr.value = getApiErrorMessage(e, "加载网盘账号失败");
    }
  }
  createOpen.value = true;
}

async function submitCreate() {
  createErr.value = "";
  if (!form.value.accountID) {
    createErr.value = "先选一个网盘账号";
    return;
  }
  if (!form.value.fileID) {
    createErr.value = "先选要分享的文件";
    return;
  }
  const title = form.value.title.trim();
  if (!title) {
    createErr.value = "给这份分享起个名字（访客看到的标题）";
    return;
  }
  // AppInput 只吐字符串，数字字段在这里转一次。
  // 不在这里转的话 season="abc" 会一路带着走，到服务端才报错。
  const season = Number.parseInt(form.value.season, 10);
  if (!Number.isFinite(season)) {
    createErr.value = "季数要填整数，不确定就留 -1";
    return;
  }
  creating.value = true;
  try {
    const res = await libraryShareApi.create({
      account_id: form.value.accountID,
      file_id: form.value.fileID,
      title,
      season,
      comment: form.value.comment.trim(),
      password: form.value.password,
      expire_days: form.value.expireDays,
      max_devices: form.value.maxDevices,
    });
    createOpen.value = false;
    await load();
    created.value = res;
  } catch (e) {
    createErr.value = getApiErrorMessage(e, "创建分享失败");
  } finally {
    creating.value = false;
  }
}

// ---------------------------------------------------------------- 文件选择

const pickerOpen = ref(false);
const pickerAccount = ref(0);
const pickerParentID = ref("");
const pickerPath = ref<string[]>([]);
const pickerItems = ref<FileItem[]>([]);
const pickerLoading = ref(false);
const pickerError = ref("");

const pickerAccountOptions = computed(() =>
  accounts.value.map((a) => ({ value: a.id, label: a.name })),
);

async function listPickerDir(parentID: string) {
  pickerLoading.value = true;
  pickerError.value = "";
  try {
    pickerItems.value = (await filesApi.list(pickerAccount.value, parentID)).items;
  } catch (e) {
    pickerItems.value = [];
    pickerError.value = getApiErrorMessage(e, "读取目录失败");
  } finally {
    pickerLoading.value = false;
  }
}

watch(pickerAccount, () => {
  if (!pickerOpen.value) return;
  // 换账号必须回到根目录：旧账号的目录 id 在新账号里不存在，
  // 留着会一直报「读取目录失败」，而用户看不出是自己选错了账号。
  pickerParentID.value = "";
  pickerPath.value = [];
  void listPickerDir("");
});

function openPicker() {
  if (!form.value.accountID) {
    createErr.value = "先选一个网盘账号，再选文件";
    return;
  }
  pickerOpen.value = true;
  pickerAccount.value = form.value.accountID;
  pickerParentID.value = "";
  pickerPath.value = [];
  void listPickerDir("");
}

function enterDir(item: FileItem) {
  if (!item.is_dir) return;
  pickerPath.value = [...pickerPath.value, item.name];
  pickerParentID.value = item.id;
  void listPickerDir(item.id);
}

function goCrumb(index: number) {
  pickerPath.value = pickerPath.value.slice(0, index);
  pickerParentID.value = "";
  void listPickerDir("");
}

function pickFile(item: FileItem) {
  if (item.is_dir) return;
  form.value.fileID = item.id;
  // 标题可以预填，但允许改：同一个剧的第 1 季和第 2 季文件名完全一样，
  // 预填成文件名会让两个分享在列表里无法区分。
  if (!form.value.title.trim()) form.value.title = item.name;
  pickerOpen.value = false;
}

// ---------------------------------------------------------------- 有效期

const expiryOpen = ref(false);
const expiryTarget = ref<LibraryShareItem | null>(null);
const expiryDays = ref(7);
const expirySaving = ref(false);
const expiryError = ref("");

function openExpiry(item: LibraryShareItem) {
  expiryTarget.value = item;
  // 服务端给的是「还剩几天」，改期是「设成还剩几天」，
  // 直接把剩余值塞进选项里，管理员看到的就是「再延 3 天」。
  expiryDays.value = item.expire_days > 0 ? item.expire_days : 7;
  expiryError.value = "";
  expiryOpen.value = true;
}

const expiryOptions = computed(() =>
  EXPIRE_DAY_OPTIONS.map((o) => ({ value: o.value, label: o.label })),
);

async function saveExpiry() {
  if (!expiryTarget.value) return;
  expiryError.value = "";
  expirySaving.value = true;
  try {
    await libraryShareApi.setExpiry(expiryTarget.value.id, expiryDays.value);
    expiryOpen.value = false;
    await load();
    toast.success("有效期已更新");
  } catch (e) {
    expiryError.value = getApiErrorMessage(e, "更新有效期失败");
  } finally {
    expirySaving.value = false;
  }
}

// ---------------------------------------------------------------- 撤销

async function revoke(item: LibraryShareItem) {
  const ok = window.confirm(
    `撤销后「${item.title}」的链接立即失效，已拿到令牌的访客也播不了。确定撤销？`,
  );
  if (!ok) return;
  try {
    await libraryShareApi.remove(item.id);
    await load();
    toast.success("已撤销");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "撤销失败"));
  }
}

// ---------------------------------------------------------------- 统计

const statsOpen = ref(false);
const statsLoading = ref(false);
const statsError = ref("");
const stats = ref<LibraryShareStats | null>(null);

async function openStats(item: LibraryShareItem) {
  statsOpen.value = true;
  statsLoading.value = true;
  statsError.value = "";
  stats.value = null;
  try {
    stats.value = await libraryShareApi.stats(item.id);
  } catch (e) {
    statsError.value = getApiErrorMessage(e, "加载统计失败");
  } finally {
    statsLoading.value = false;
  }
}

// ---------------------------------------------------------------- 刚创建

const created = ref<LibraryShareCreateResult | null>(null);

async function copyCreated() {
  if (!created.value) return;
  const ok = await copyTextToClipboard(created.value.url, { successMessage: "链接已复制" });
  if (!ok) toast.warning("复制失败，请手动选中链接");
}

async function copyLink(item: LibraryShareItem) {
  const ok = await copyTextToClipboard(item.url, { successMessage: "链接已复制" });
  if (!ok) toast.warning("复制失败，请手动选中链接");
}

function closeCreated() {
  created.value = null;
}

// ---------------------------------------------------------------- 展示

function statusOf(item: LibraryShareItem): { tone: AdminStatusPillTone; text: string } {
  if (item.revoked) return { tone: "muted", text: "已撤销" };
  if (item.expired) return { tone: "danger", text: "已过期" };
  if (!item.expires_at) return { tone: "success", text: "永久" };
  if (item.expire_days <= 1) return { tone: "warning", text: "今天内失效" };
  return { tone: "brand", text: `${item.expire_days} 天后失效` };
}

function deviceText(item: LibraryShareItem): string {
  return `${item.active} / ${item.max_devices}`;
}

/** 顶满时用警示色：这是「新访客会被挡在外面」的唯一提前预警。 */
function deviceTone(item: LibraryShareItem): AdminStatusPillTone {
  if (item.active >= item.max_devices) return "warning";
  return "muted";
}

defineExpose({ load });
</script>

<template>
  <div class="lsp">
    <AppStateBlock v-if="loading && !items.length" message="加载中…" loading />
    <div v-else-if="loadError" class="lsp__error">{{ loadError }}</div>
    <AdminEmptyState
      v-else-if="items.length === 0"
      icon="share-2"
      title="还没有免登录分享"
      description="选一个媒体文件生成链接，发给家人，他们不用登录就能直接看。"
    >
      <AppButton variant="primary" size="sm" @click="openCreate">创建分享</AppButton>
    </AdminEmptyState>

    <template v-else>
      <div class="lsp__actions">
        <AppButton variant="primary" size="sm" @click="openCreate">创建分享</AppButton>
        <AppButton size="sm" :disabled="loading" @click="load">刷新</AppButton>
      </div>

      <div class="admin-panel-table-wrap">
        <table class="admin-table">
          <thead>
            <tr>
              <th>标题</th>
              <th>状态</th>
              <th>设备</th>
              <th>访问 / 播放 / 访客</th>
              <th>创建时间</th>
              <th class="admin-table__actions">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in items" :key="item.id">
              <td>
                <div class="lsp__title">{{ item.title }}</div>
                <div class="lsp__sub">
                  {{ item.comment || "无备注" }}
                  <span v-if="item.has_password" class="lsp__badge">有口令</span>
                </div>
              </td>
              <td>
                <AdminStatusPill :tone="statusOf(item).tone">{{ statusOf(item).text }}</AdminStatusPill>
              </td>
              <td>
                <AdminStatusPill :tone="deviceTone(item)">{{ deviceText(item) }}</AdminStatusPill>
              </td>
              <td class="lsp__sub">
                {{ item.view_count }} / {{ item.play_count }} / {{ item.visitor_count }}
              </td>
              <td class="lsp__sub">{{ formatTime(item.created_at) }}</td>
              <td class="admin-table__actions">
                <AppButton size="sm" variant="ghost" @click="copyLink(item)">复制链接</AppButton>
                <AppButton size="sm" variant="ghost" @click="openExpiry(item)">改有效期</AppButton>
                <AppButton size="sm" variant="ghost" @click="openStats(item)">统计</AppButton>
                <AppButton
                  size="sm"
                  variant="ghost"
                  :disabled="item.revoked"
                  @click="revoke(item)"
                >
                  撤销
                </AppButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 创建 -->
    <AppModal :open="createOpen" title="创建免登录分享" size="md" @close="createOpen = false">
      <div class="lsp__form">
        <div v-if="createErr" class="lsp__error">{{ createErr }}</div>

        <FormField label="网盘账号" required>
          <AppSelect v-model="form.accountID" :options="accountOptions" placeholder="选择账号" />
        </FormField>

        <FormField label="分享文件" required>
          <div class="lsp__row">
            <AppButton size="sm" @click="openPicker">选择文件…</AppButton>
            <span class="lsp__sub">{{ form.fileID || "还没选" }}</span>
          </div>
        </FormField>

        <FormField label="访客看到的标题" required>
          <AppInput
            v-model="form.title"
            :placeholder="`最多 ${MAX_TITLE_LEN} 字`"
          />
        </FormField>

        <div class="lsp__grid">
          <FormField label="季数（-1 = 不限）">
            <AppInput v-model="form.season" type="number" />
          </FormField>
          <FormField label="有效期">
            <AppSelect v-model="form.expireDays" :options="expireOptions" />
          </FormField>
        </div>

        <FormField label="同时在线设备上限">
          <AppInput v-model="form.maxDevices" type="number" placeholder="留空按系统设置" />
        </FormField>

        <FormField label="访问口令">
          <template #help>留空表示任何人点开链接就能看。</template>
          <AppInput v-model="form.password" type="password" autocomplete="new-password" />
        </FormField>

        <FormField label="备注">
          <AppInput
            v-model="form.comment"
            :placeholder="`只给自己看的备注，访客看不到（最多 ${MAX_COMMENT_LEN} 字）`"
          />
        </FormField>
      </div>

      <template #footer>
        <AppButton @click="createOpen = false">取消</AppButton>
        <AppButton variant="primary" :disabled="creating" @click="submitCreate">
          {{ creating ? "创建中…" : "创建并生成链接" }}
        </AppButton>
      </template>
    </AppModal>

    <!-- 文件选择 -->
    <AppModal
      :open="pickerOpen"
      title="选择要分享的文件"
      size="lg"
      nested
      @close="pickerOpen = false"
    >
      <div class="lsp__picker">
        <AppSelect v-model="pickerAccount" :options="pickerAccountOptions" />
        <div class="lsp__crumbs">
          <button type="button" @click="goCrumb(0)">根目录</button>
          <template v-for="(seg, idx) in pickerPath" :key="`${idx}-${seg}`">
            <span class="lsp__crumb-sep">/</span>
            <button type="button" @click="goCrumb(idx + 1)">{{ seg }}</button>
          </template>
        </div>
      </div>

      <AppStateBlock v-if="pickerLoading" message="加载中…" loading />
      <div v-else-if="pickerError" class="lsp__error">{{ pickerError }}</div>
      <AdminEmptyState v-else-if="pickerItems.length === 0" title="这个目录是空的" />
      <ul v-else class="lsp__files">
        <li v-for="it in pickerItems" :key="it.id">
          <button
            type="button"
            :class="['lsp__file', { 'lsp__file--selected': it.id === form.fileID }]"
            @click="it.is_dir ? enterDir(it) : pickFile(it)"
          >
            <span class="lsp__file-icon">{{ it.is_dir ? "📁" : "🎬" }}</span>
            <span class="lsp__file-name">{{ it.name }}</span>
          </button>
        </li>
      </ul>

      <template #footer>
        <AppButton @click="pickerOpen = false">取消</AppButton>
      </template>
    </AppModal>

    <!-- 改有效期 -->
    <AppModal :open="expiryOpen" title="修改有效期" size="sm" @close="expiryOpen = false">
      <div class="lsp__form">
        <div v-if="expiryError" class="lsp__error">{{ expiryError }}</div>
        <div class="lsp__sub">{{ expiryTarget?.title }}</div>
        <FormField label="新的有效期">
          <AppSelect v-model="expiryDays" :options="expiryOptions" />
        </FormField>
        <p class="lsp__hint">
          从现在起算。选「永久有效」这条链接就一直有效，除非手动撤销。
        </p>
      </div>
      <template #footer>
        <AppButton @click="expiryOpen = false">取消</AppButton>
        <AppButton variant="primary" :disabled="expirySaving" @click="saveExpiry">
          {{ expirySaving ? "保存中…" : "保存" }}
        </AppButton>
      </template>
    </AppModal>

    <!-- 统计 -->
    <AppModal :open="statsOpen" title="分享统计" size="lg" @close="statsOpen = false">
      <AppStateBlock v-if="statsLoading" message="加载中…" loading />
      <div v-else-if="statsError" class="lsp__error">{{ statsError }}</div>
      <div v-else-if="stats" class="lsp__stats">
        <div class="lsp__stat-row">
          <div class="lsp__stat"><span>访问</span><strong>{{ stats.view_count }}</strong></div>
          <div class="lsp__stat"><span>播放</span><strong>{{ stats.play_count }}</strong></div>
          <div class="lsp__stat"><span>访客</span><strong>{{ stats.visitor_count }}</strong></div>
          <div class="lsp__stat">
            <span>活跃设备</span><strong>{{ stats.active_devices }} / {{ stats.max_devices }}</strong>
          </div>
        </div>

        <h4 class="lsp__section">最近访客</h4>
        <div v-if="!stats.recent_visits.length" class="lsp__sub">还没有访客。</div>
        <div v-else class="admin-panel-table-wrap">
          <table class="admin-table">
            <thead>
              <tr><th>访客标识</th><th>来源（已脱敏）</th><th>设备</th><th>最近活动</th></tr>
            </thead>
            <tbody>
              <tr v-for="v in stats.recent_visits" :key="v.id">
                <td class="lsp__sub">{{ v.visitor_id.slice(0, 8) }}…</td>
                <td class="lsp__sub">{{ v.ip_masked || "未知" }}</td>
                <td class="lsp__sub">{{ v.user_agent || "未知" }}</td>
                <td class="lsp__sub">{{ formatTime(v.last_seen_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <h4 class="lsp__section">最近播放</h4>
        <div v-if="!stats.recent_plays.length" class="lsp__sub">还没有播放记录。</div>
        <div v-else class="admin-panel-table-wrap">
          <table class="admin-table">
            <thead>
              <tr><th>开始时间</th><th>来源（已脱敏）</th><th>方式</th></tr>
            </thead>
            <tbody>
              <tr v-for="p in stats.recent_plays" :key="p.id">
                <td class="lsp__sub">{{ formatTime(p.started_at) }}</td>
                <td class="lsp__sub">{{ p.ip_masked || "未知" }}</td>
                <td class="lsp__sub">{{ p.method }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <template #footer>
        <AppButton @click="statsOpen = false">关闭</AppButton>
      </template>
    </AppModal>

    <!-- 刚创建：明文短码只在这里出现一次 -->
    <AppModal :open="Boolean(created)" title="链接已生成" size="sm" @close="closeCreated">
      <div v-if="created" class="lsp__form">
        <p class="lsp__hint lsp__hint--warn">
          这条链接只显示这一次。关掉之后只能重新创建一份 —— 服务端不保存明文短码。
        </p>
        <div class="lsp__link">{{ created.url }}</div>
        <div v-if="created.has_password" class="lsp__sub">对方还需要口令才能看。</div>
      </div>
      <template #footer>
        <AppButton variant="primary" @click="copyCreated">复制链接</AppButton>
      </template>
    </AppModal>
  </div>
</template>

<style scoped>
.lsp {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.lsp__actions {
  display: flex;
  gap: 8px;
}

.lsp__title {
  font-weight: 600;
  color: var(--text);
}

.lsp__sub {
  font-size: 12px;
  color: var(--text-muted);
}

.lsp__badge {
  margin-left: 6px;
  padding: 1px 6px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--brand) 14%, transparent);
  color: var(--brand);
  font-size: 11px;
}

.lsp__error {
  padding: 10px 12px;
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, #ef4444 12%, transparent);
  color: #ef4444;
  font-size: 13px;
}

.lsp__hint {
  margin: 0;
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.6;
}

.lsp__hint--warn {
  color: var(--warning, #f59e0b);
}

.lsp__form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.lsp__grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.lsp__row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.lsp__picker {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.lsp__crumbs {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
  font-size: 12px;
}

.lsp__crumbs button {
  border: none;
  background: none;
  color: var(--brand);
  cursor: pointer;
  padding: 2px 4px;
  font-size: 12px;
}

.lsp__crumb-sep {
  color: var(--text-muted);
}

.lsp__files {
  list-style: none;
  margin: 12px 0 0;
  padding: 0;
  max-height: 340px;
  overflow-y: auto;
  border: 1px solid var(--border-soft);
  border-radius: var(--radius-sm);
}

.lsp__file {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 12px;
  border: none;
  background: none;
  cursor: pointer;
  text-align: left;
  font-size: 13px;
  color: var(--text);
}

.lsp__file:hover {
  background: color-mix(in srgb, var(--brand) 8%, transparent);
}

.lsp__file--selected {
  background: color-mix(in srgb, var(--brand) 14%, transparent);
}

.lsp__file-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.lsp__link {
  padding: 10px 12px;
  border-radius: var(--radius-sm);
  background: var(--surface-sunken, var(--surface));
  border: 1px solid var(--border-soft);
  font-family: var(--font-mono, monospace);
  font-size: 13px;
  word-break: break-all;
  user-select: all;
}

.lsp__stats {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.lsp__stat-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 10px;
}

.lsp__stat {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 12px;
  border-radius: var(--radius-sm);
  background: var(--surface);
  border: 1px solid var(--border-soft);
}

.lsp__stat span {
  font-size: 12px;
  color: var(--text-muted);
}

.lsp__stat strong {
  font-size: 18px;
}

.lsp__section {
  margin: 6px 0 0;
  font-size: 13px;
  color: var(--text);
}

@media (max-width: 720px) {
  .lsp__grid,
  .lsp__stat-row {
    grid-template-columns: 1fr;
  }
}
</style>