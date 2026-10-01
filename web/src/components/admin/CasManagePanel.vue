<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  deleteCasRecord,
  fetchCasConfig,
  fetchCasPlayURL,
  fetchCasRecords,
  restoreCasRecord,
  runCasOnce,
  saveCasConfig,
  type CasConfig,
  type CasPlayURLResult,
  type CasRecord,
} from "@/api/cas";
import AppButton from "@/components/base/AppButton.vue";
import AppBadge from "@/components/base/AppBadge.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import FormField from "@/components/base/FormField.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import { useConfirm } from "@/composables/useConfirm";
import { copyTextToClipboard, toast } from "@/composables/useToast";
import "@/styles/admin-table.css";

const { showConfirm } = useConfirm();

const loading = ref(false);
const errorMsg = ref("");
const records = ref<CasRecord[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = 50;
const keyword = ref("");
const status = ref("");

const cfg = reactive<CasConfig>({
  enabled: true,
  age_days: 30,
  delete_source: true,
  delay_delete_hours: 2,
  write_back_cloud: false,
  cas_notify_auto_save: true,
  cas_notify_auto_save_dir: "CAS",
  cas_notify_auto_save_drives: {},
});
const cfgSaving = ref(false);
const running = ref(false);

const playModalOpen = ref(false);
const playTarget = ref<CasRecord | null>(null);
const playURLInfo = ref<CasPlayURLResult | null>(null);
const playURLLoading = ref(false);

async function openPlayModal(rec: CasRecord) {
  playTarget.value = rec;
  playURLInfo.value = null;
  playModalOpen.value = true;
  playURLLoading.value = true;
  try {
    const res = await fetchCasPlayURL(rec.id);
    playURLInfo.value = res;
    if (res.restored) {
      toast.info("源文件已被删除，已触发秒传恢复并登记延时清理");
      await load();
    }
  } catch (e) {
    toast.error(getApiErrorMessage(e, "获取播放地址失败"));
  } finally {
    playURLLoading.value = false;
  }
}

function fullPlayURL(path?: string): string {
  if (!path) return "";
  return `${window.location.origin}${path}`;
}

function copyPlayURL() {
  if (!playURLInfo.value?.play_url) return;
  copyTextToClipboard(fullPlayURL(playURLInfo.value.play_url));
  toast.success("播放地址已复制到剪贴板");
}

function testPlay() {
  if (!playURLInfo.value?.play_url) return;
  window.open(fullPlayURL(playURLInfo.value.play_url), "_blank");
}

// 手动触发一轮 CAS 化（扫描已完成影视上传任务）
async function runOnce() {
  running.value = true;
  try {
    const res = await runCasOnce();
    toast.success(
      `CAS 化完成：生成 ${res.generated} / 删源 ${res.deleted} / 跳过 ${res.skipped} / 失败 ${res.failed}`,
    );
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "CAS 化执行失败"));
  } finally {
    running.value = false;
  }
}

const restoreOpen = ref(false);
const restoreTarget = ref<CasRecord | null>(null);
const restoreFolderID = ref("");
const restoring = ref(false);

const statusOptions = [
  { value: "", label: "全部状态" },
  { value: "active", label: "已删源" },
  { value: "restored", label: "已恢复" },
  { value: "pending_delete", label: "待删源" },
  { value: "pending", label: "待处理" },
];

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)));

function statusTone(s?: string): "success" | "info" | "warning" | "neutral" {
  switch (s) {
    case "active":
      return "success";
    case "restored":
      return "info";
    case "pending_delete":
    case "pending":
      return "warning";
    default:
      return "neutral";
  }
}

function statusLabel(s?: string): string {
  switch (s) {
    case "active":
      return "已删源";
    case "restored":
      return "已恢复";
    case "pending_delete":
      return "待删源";
    case "pending":
      return "待处理";
    default:
      return s || "-";
  }
}

function driveLabel(t?: string): string {
  switch (t) {
    case "189cloud":
    case "cloud189":
      return "天翼";
    case "139_cloud":
    case "pan139":
    case "cloud139":
      return "移动";
    case "quark":
      return "夸克";
    default:
      return t || "-";
  }
}

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    const res = await fetchCasRecords({
      page: page.value,
      page_size: pageSize,
      status: status.value,
      keyword: keyword.value.trim() || undefined,
    });
    records.value = res.items ?? [];
    total.value = res.total ?? 0;
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载 CAS 清单失败");
    records.value = [];
  } finally {
    loading.value = false;
  }
}

async function loadConfig() {
  try {
    const c = await fetchCasConfig();
    Object.assign(cfg, c);
  } catch {
    /* 配置加载失败用默认值 */
  }
}

async function saveConfig() {
  cfgSaving.value = true;
  try {
    await saveCasConfig({ ...cfg });
    toast.success("CAS 配置已保存");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "保存配置失败"));
  } finally {
    cfgSaving.value = false;
  }
}

function search() {
  page.value = 1;
  void load();
}

function openRestore(rec: CasRecord) {
  restoreTarget.value = rec;
  restoreFolderID.value = "";
  restoreOpen.value = true;
}

async function doRestore() {
  if (!restoreTarget.value?.id) return;
  restoring.value = true;
  try {
    const res = await restoreCasRecord(restoreTarget.value.id, restoreFolderID.value.trim());
    toast.success(`「${res.file_name}」秒传恢复成功`);
    restoreOpen.value = false;
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "秒传恢复失败"));
  } finally {
    restoring.value = false;
  }
}

async function handleDelete(rec: CasRecord) {
  if (!rec.id) return;
  try {
    await showConfirm({
      title: "删除 CAS 记录",
      message: `确定删除「${rec.file_name}」的 CAS 清单记录吗？（不影响云端文件）`,
      icon: "trash",
      confirmText: "删除",
      danger: true,
    });
  } catch {
    return;
  }
  try {
    await deleteCasRecord(rec.id);
    records.value = records.value.filter((x) => x.id !== rec.id);
    toast.success("记录已删除");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "删除失败"));
  }
}

function formatSize(bytes?: number): string {
  if (!bytes) return "-";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = bytes;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

function nextPage() {
  if (page.value < totalPages.value) {
    page.value++;
    void load();
  }
}
function prevPage() {
  if (page.value > 1) {
    page.value--;
    void load();
  }
}

onMounted(() => {
  void loadConfig();
  void load();
});
</script>

<template>
  <div class="cas">
    <SettingsCard title="CAS 自动化配置" accent="var(--brand)">
      <div class="cas__cfg">
        <label class="cas__cfg-item">
          <input v-model="cfg.enabled" type="checkbox" />
          <span>启用 CAS 自动化（影视上传满 N 天生成 .cas 清单并删源）</span>
        </label>
        <FormField label="满 N 天触发" class="cas__cfg-days">
          <AppInput v-model.number="cfg.age_days" type="number" min="1" />
        </FormField>
        <label class="cas__cfg-item">
          <input v-model="cfg.delete_source" type="checkbox" />
          <span>生成清单后自动删除源视频</span>
        </label>
        <FormField label="延时删除(时)">
          <AppInput v-model.number="cfg.delay_delete_hours" type="number" min="0" placeholder="默认2" />
        </FormField>
        <label class="cas__cfg-item">
          <input v-model="cfg.write_back_cloud" type="checkbox" />
          <span>.cas 清单写回网盘（默认只存本地库）</span>
        </label>
        <AppButton type="button" variant="primary" :disabled="cfgSaving" @click="saveConfig">
          {{ cfgSaving ? "保存中…" : "保存配置" }}
        </AppButton>
        <AppButton type="button" variant="secondary" :disabled="running" @click="runOnce">
          {{ running ? "执行中…" : "立即执行 CAS 化" }}
        </AppButton>
      </div>
    </SettingsCard>

    <SettingsCard title="CAS 清单记录" accent="var(--brand)" class="cas__list-card">
      <div class="cas__toolbar">
        <AppInput v-model="keyword" placeholder="搜索文件名…" class="cas__search" @keyup.enter="search" />
        <AppSelect v-model="status" :options="statusOptions" class="cas__status" @update:model-value="search" />
        <AppButton type="button" variant="secondary" @click="search">查询</AppButton>
        <AppButton type="button" variant="secondary" :disabled="loading" @click="load">刷新</AppButton>
      </div>

      <AppStateBlock v-if="loading" message="加载中…" loading min-height="200px" />
      <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="200px" />

      <div v-else-if="records.length" class="cas__table-scroll">
        <table class="cas__table">
          <thead>
            <tr>
              <th>文件名</th>
              <th>网盘</th>
              <th>大小</th>
              <th>秒传盘</th>
              <th>状态</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="rec in records" :key="rec.id" class="cas__row">
              <td>
                <span class="cas__name" :title="rec.file_name">{{ rec.file_name }}</span>
              </td>
              <td><AppBadge tone="info">{{ driveLabel(rec.source_type) }}</AppBadge></td>
              <td class="cas__muted">{{ formatSize(rec.file_size) }}</td>
              <td class="cas__muted">{{ rec.rapid_drive_types || "-" }}</td>
              <td><AppBadge :tone="statusTone(rec.status)">{{ statusLabel(rec.status) }}</AppBadge></td>
              <td>
                <div class="cas__actions">
                  <AppButton type="button" variant="secondary" size="sm" @click="openPlayModal(rec)">播放直链</AppButton>
                  <AppButton type="button" variant="primary" size="sm" @click="openRestore(rec)">秒传恢复</AppButton>
                  <AppButton type="button" variant="danger" size="sm" @click="handleDelete(rec)">删除</AppButton>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <AppStateBlock v-else message="暂无 CAS 清单记录" min-height="200px" />

      <div v-if="totalPages > 1 && records.length" class="cas__pager">
        <AppButton type="button" variant="secondary" :disabled="page <= 1 || loading" @click="prevPage">上一页</AppButton>
        <span class="cas__page-info">{{ page }} / {{ totalPages }}（共 {{ total }} 条）</span>
        <AppButton type="button" variant="secondary" :disabled="page >= totalPages || loading" @click="nextPage">下一页</AppButton>
      </div>
    </SettingsCard>

    <AppModal :open="restoreOpen" size="account" title="秒传恢复" @close="restoreOpen = false">
      <div class="cas__restore">
        <p class="cas__restore-tip">
          将「{{ restoreTarget?.file_name }}」通过秒传恢复到目标网盘目录（需云端仍存在同哈希文件）。
        </p>
        <FormField label="目标目录 ID（留空为根目录）">
          <AppInput v-model="restoreFolderID" placeholder="如 0 或目录 fileId" />
        </FormField>
        <div class="modal-form__footer">
          <AppButton type="button" variant="primary" :disabled="restoring" @click="doRestore">
            {{ restoring ? "恢复中…" : "开始秒传恢复" }}
          </AppButton>
        </div>
      </div>
    </AppModal>

    <AppModal :open="playModalOpen" size="account" title="CAS 播放恢复直链" @close="playModalOpen = false">
      <div class="cas__restore">
        <AppStateBlock v-if="playURLLoading" message="正在探测/秒传恢复并获取播放直链…" loading min-height="120px" />
        <template v-else-if="playURLInfo">
          <p class="cas__restore-tip">
            <strong>{{ playURLInfo.file_name }}</strong>
            <br />
            <span v-if="playURLInfo.restored" style="color: var(--brand, #e50914)">
              ★ 源文件原先已被删除，刚刚已触发秒传恢复，并在 {{ cfg.delay_delete_hours }} 小时后自动延时删除。
            </span>
            <span v-else style="color: #34d399">
              ✓ 云端文件尚存，可直接流畅播放。
            </span>
          </p>
          <FormField label="播放直链 / STRM 路径">
            <AppInput :model-value="fullPlayURL(playURLInfo.play_url)" readonly />
          </FormField>
          <div class="modal-form__footer">
            <AppButton type="button" variant="secondary" @click="copyPlayURL">
              复制完整链接
            </AppButton>
            <AppButton type="button" variant="primary" @click="testPlay">
              在新窗口播放测试
            </AppButton>
          </div>
        </template>
      </div>
    </AppModal>
  </div>
</template>

<style scoped>
.cas__cfg {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 16px;
}

.cas__cfg-item {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text, #d1d5db);
  cursor: pointer;
}

.cas__cfg-days {
  width: 120px;
}

.cas__list-card {
  margin-top: 18px;
}

.cas__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}

.cas__search {
  width: 220px;
}

.cas__status {
  width: 130px;
}

.cas__table-scroll {
  overflow-x: auto;
  margin: 0 -22px;
}

.cas__table {
  width: 100%;
  border-collapse: collapse;
  table-layout: fixed;
  font-size: 13px;
}

.cas__table th:nth-child(1),
.cas__table td:nth-child(1) {
  width: 34%;
}
.cas__table th:nth-child(2),
.cas__table td:nth-child(2) {
  width: 10%;
}
.cas__table th:nth-child(3),
.cas__table td:nth-child(3) {
  width: 12%;
}
.cas__table th:nth-child(4),
.cas__table td:nth-child(4) {
  width: 18%;
}
.cas__table th:nth-child(5),
.cas__table td:nth-child(5) {
  width: 12%;
}
.cas__table th:last-child,
.cas__table td:last-child {
  width: 14%;
  text-align: center;
}

.cas__table th,
.cas__table td {
  padding: 10px 22px;
  text-align: left;
  vertical-align: middle;
  border-bottom: 1px solid var(--border-soft, #232733);
}

.cas__table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted, #6b7280);
}

.cas__name {
  display: block;
  font-weight: 600;
  color: var(--text, #e5e7eb);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.cas__muted {
  color: var(--text-muted, #6b7280);
}

.cas__actions {
  display: flex;
  justify-content: center;
  gap: 8px;
}

.cas__pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 14px;
  margin-top: 16px;
}

.cas__page-info {
  font-size: 13px;
  color: var(--text-muted, #6b7280);
}

.cas__restore {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.cas__restore-tip {
  margin: 0;
  font-size: 13px;
  color: var(--text-muted, #6b7280);
  line-height: 1.6;
}
</style>
