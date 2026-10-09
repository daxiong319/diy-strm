<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  createRssSource,
  deleteRssHistory,
  deleteRssSource,
  getRssOptions,
  listRssHistory,
  listRssSources,
  previewRssSource,
  syncAllRssSources,
  syncRssSource,
  updateRssSource,
  type RssHistory,
  type RssHistoryListResult,
  type RssOptions,
  type RssPreviewResult,
  type RssSource,
  type RssSourceListResult,
} from "@/api/rss";
import AppBadge from "@/components/base/AppBadge.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import FormField from "@/components/base/FormField.vue";
import AdminEnableToggle from "@/components/admin/AdminEnableToggle.vue";
import AdminRowActions from "@/components/admin/AdminRowActions.vue";
import AdminTableActionBtn from "@/components/admin/AdminTableActionBtn.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import { useConfirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import "@/styles/admin-table.css";

// RSS 订阅源：贴一个 feed URL，定时抓取新条目并自动提交离线下载。

const { showConfirm } = useConfirm();

const loading = ref(false);
const errorMsg = ref("");
const records = ref<RssSource[]>([]);
const syncingAll = ref(false);

const options = ref<RssOptions | null>(null);

// ---------------------------------------------------------------------------
// 列表
// ---------------------------------------------------------------------------

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    const res: RssSourceListResult = await listRssSources({ limit: 200 });
    records.value = res.items ?? [];
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载 RSS 订阅源失败");
    records.value = [];
  } finally {
    loading.value = false;
  }
}

async function handleToggle(rec: RssSource, enabled: boolean) {
  try {
    await updateRssSource(rec.id, { enabled });
    rec.enabled = enabled;
    toast.success(enabled ? "已启用" : "已停用");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "切换启用状态失败"));
    // 回滚开关：后端没改成功，本地也别假装改了。
    rec.enabled = !enabled;
  }
}

const syncingId = ref(0);

async function handleSync(rec: RssSource) {
  syncingId.value = rec.id;
  try {
    const res = await syncRssSource(rec.id);
    const c = res.counters;
    if (res.status === "failed") {
      toast.error(res.message || "同步失败");
    } else {
      toast.success(res.message || `同步完成：新增 ${c?.added ?? 0}`);
    }
    // 追赶模式要把「只取最新一条」这件事说给用户听，否则会以为漏抓了。
    if (res.catchup) {
      for (const n of res.notices ?? []) toast.info(n);
    }
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "同步失败"));
  } finally {
    syncingId.value = 0;
  }
}

async function handleSyncAll() {
  syncingAll.value = true;
  try {
    const res = await syncAllRssSources();
    const c = res.counters;
    toast.success(`全部同步完成：新增 ${c?.added ?? 0}，跳过 ${c?.skipped ?? 0}，失败 ${c?.failed ?? 0}`);
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "批量同步失败"));
  } finally {
    syncingAll.value = false;
  }
}

// ---------------------------------------------------------------------------
// 表单
// ---------------------------------------------------------------------------

interface RssForm {
  id: number;
  name: string;
  rss_url: string;
  target_path: string;
  storage: string;
  media_server: string;
  poster_url: string;
  include_regex: string;
  exclude_regex: string;
  media_type: string;
  action: string;
  enabled: boolean;
}

function emptyForm(): RssForm {
  return {
    id: 0,
    name: "",
    rss_url: "",
    target_path: "",
    storage: "",
    media_server: "",
    poster_url: "",
    include_regex: "",
    exclude_regex: "",
    media_type: "tv",
    action: "transfer",
    enabled: true,
  };
}

const form = ref<RssForm>(emptyForm());
const formOpen = ref(false);
const formSaving = ref(false);
const formError = ref("");

function openCreate() {
  form.value = emptyForm();
  formError.value = "";
  formOpen.value = true;
}

function openEdit(rec: RssSource) {
  form.value = {
    id: rec.id,
    name: rec.name,
    rss_url: rec.rss_url,
    target_path: rec.target_path,
    storage: rec.storage,
    media_server: rec.media_server,
    poster_url: rec.poster_url,
    include_regex: rec.include_regex,
    exclude_regex: rec.exclude_regex,
    media_type: rec.media_type || "tv",
    action: rec.action || "transfer",
    enabled: rec.enabled,
  };
  formError.value = "";
  formOpen.value = true;
}

async function submitForm() {
  formError.value = "";
  if (!form.value.name.trim()) {
    formError.value = "请填写订阅名称";
    return;
  }
  if (!form.value.rss_url.trim()) {
    formError.value = "请填写 RSS 地址";
    return;
  }
  formSaving.value = true;
  try {
    const payload = { ...form.value };
    if (form.value.id) {
      await updateRssSource(form.value.id, payload);
      toast.success("订阅源已更新");
    } else {
      await createRssSource(payload);
      toast.success("订阅源已添加");
    }
    formOpen.value = false;
    await load();
  } catch (e) {
    formError.value = getApiErrorMessage(e, "保存订阅源失败");
  } finally {
    formSaving.value = false;
  }
}

// ---------------------------------------------------------------------------
// 预览
// ---------------------------------------------------------------------------

const previewOpen = ref(false);
const previewLoading = ref(false);
const previewError = ref("");
const preview = ref<RssPreviewResult | null>(null);

async function runPreview() {
  const url = form.value.rss_url.trim();
  if (!url) {
    previewError.value = "请先填写 RSS 地址再预览";
    return;
  }
  previewLoading.value = true;
  previewError.value = "";
  preview.value = null;
  try {
    const res = await previewRssSource({
      rss_url: url,
      include_regex: form.value.include_regex || "",
      exclude_regex: form.value.exclude_regex || "",
    });
    preview.value = res;
    if (!(res.items?.length)) {
      const why: string[] = [];
      if (res.filtered) why.push(`${res.filtered} 条被过滤规则排除`);
      if (res.no_resource) why.push(`${res.no_resource} 条没有可下载链接`);
      previewError.value = why.length
        ? `解析到 ${res.total} 条，但没有可下载的条目（${why.join("；")}）`
        : "解析到 0 条。源不可访问、不是 RSS/Atom 格式，或该 feed 当前为空。";
    }
  } catch (e) {
    previewError.value = getApiErrorMessage(e, "预览失败");
  } finally {
    previewLoading.value = false;
  }
}

function openPreviewFromForm() {
  previewOpen.value = true;
  void runPreview();
}

// ---------------------------------------------------------------------------
// 历史
// ---------------------------------------------------------------------------

const historyOpen = ref(false);
const historyLoading = ref(false);
const historyError = ref("");
const historyRows = ref<RssHistory[]>([]);
const historySourceId = ref(0);
const historyFor = ref<RssSource | null>(null);
const historyFilterOptions = computed(() => [
  { value: 0, label: "全部订阅源" },
  ...records.value.map((r) => ({ value: r.id, label: r.name })),
]);

async function openHistory(rec: RssSource | null) {
  historyFor.value = rec;
  historySourceId.value = rec?.id ?? 0;
  historyOpen.value = true;
  await loadHistory();
}

async function loadHistory() {
  historyLoading.value = true;
  historyError.value = "";
  try {
    const res: RssHistoryListResult = await listRssHistory({
      source_id: historySourceId.value || undefined,
      limit: 200,
    });
    historyRows.value = res.items ?? [];
  } catch (e) {
    historyError.value = getApiErrorMessage(e, "加载历史记录失败");
    historyRows.value = [];
  } finally {
    historyLoading.value = false;
  }
}

// 删一条历史 = 把这个条目加入豁免：下一轮同步还会重新处理它。
// 所以按钮文案不说「删除」，说「重新处理」。
async function handleDeleteHistory(row: RssHistory) {
  try {
    await showConfirm({
      title: "重新处理这条条目",
      message: `确认让「${row.title || row.guid}」在下一轮同步时重新处理？`,
      confirmText: "重新处理",
      icon: "question",
    });
  } catch {
    return; // 取消/关闭是 reject，必须吃掉
  }
  try {
    await deleteRssHistory(row.id);
    toast.success("已加入豁免，下一轮会重新处理");
    await loadHistory();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "操作失败"));
  }
}

// ---------------------------------------------------------------------------
// 删除订阅源
// ---------------------------------------------------------------------------

async function handleDelete(rec: RssSource) {
  let deleteHistory = false;
  try {
    const res = await showConfirm({
      title: "删除订阅源",
      message: `确认删除「${rec.name}」？`,
      hint: "勾选后会连同这个源的下载历史一起清除。",
      checkboxLabel: "同时清除该订阅源的下载历史（清除后已下载的条目可能会被重新提交离线下载）",
      confirmText: "删除",
      icon: "trash",
      danger: true,
    });
    deleteHistory = Boolean(res?.checked);
  } catch {
    return;
  }
  try {
    await deleteRssSource(rec.id, deleteHistory);
    toast.success("订阅源已删除");
    if (historyFor.value?.id === rec.id) {
      historyFor.value = null;
      historySourceId.value = 0;
      await loadHistory();
    }
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "删除订阅源失败"));
  }
}

// ---------------------------------------------------------------------------
// 展示辅助
// ---------------------------------------------------------------------------

const STATUS_TONE: Record<string, "neutral" | "info" | "success" | "warning" | "danger"> = {
  success: "success",
  partial: "warning",
  failed: "danger",
  skipped: "neutral",
  completed: "info",
};

const STATUS_LABEL: Record<string, string> = {
  success: "成功",
  partial: "部分成功",
  failed: "失败",
  skipped: "已跳过",
  completed: "已完成",
};

function statusTone(s: string) {
  return STATUS_TONE[s] ?? "neutral";
}

function statusLabel(s: string) {
  return STATUS_LABEL[s] ?? (s || "-");
}

function formatTime(v: string) {
  if (!v) return "-";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return v;
  return d.toLocaleString();
}

// 这三份选项由后端 /admin/rss-options 下发，前端不硬编码一份：
// 硬编码的文案/取值必然会跟后端口径漂移（上次漂移的是媒体库三态的哨兵值）。
const actionOptions = computed(() => options.value?.actions ?? []);
const mediaTypeOptions = computed(() => options.value?.media_types ?? []);
const mediaServerOptions = computed(() => options.value?.media_servers ?? []);

const supportedText = computed(() => {
  const o = options.value;
  if (!o) return "";
  const parts = [
    `支持格式：${o.formats.join("、")}`,
    `编码：${o.charsets.join("、")}`,
    `可识别资源：${o.kinds.join("、")}`,
  ];
  return parts.join(" · ");
});

onMounted(async () => {
  await load();
  try {
    options.value = await getRssOptions();
  } catch {
    // 支持范围拿不到不该挡住主流程：表单照常能填，只是没有那行提示。
    options.value = null;
  }
});
</script>

<template>
  <div class="rssm">
    <SettingsCard title="RSS 订阅源" accent="var(--brand)">
      <template #head-aside>
        <p v-if="supportedText" class="rssm__support">{{ supportedText }}</p>
      </template>

      <div class="rssm__toolbar">
        <AppButton type="button" variant="secondary" :disabled="loading" @click="load">刷新</AppButton>
        <AppButton type="button" variant="secondary" :disabled="syncingAll" @click="handleSyncAll">
          {{ syncingAll ? "同步中…" : "全部立即同步" }}
        </AppButton>
        <AppButton type="button" variant="primary" @click="openCreate">新增订阅源</AppButton>
      </div>

      <p class="rssm__hint">
        支持 Mikan、dmhy、nyaa 等常见 BT RSS，定时同步后自动提交到网盘离线下载。
        解析器是自研的，只支持 UTF-8 编码的 RSS 2.0 / Atom / RDF；GBK 等非 UTF-8
        源、需登录或需执行 JS 的站点无法解析，添加前建议先「预览」确认。
        条目按去重键（guid → 资源哈希 → 链接 → 合成键）去重，同一部片子只提交一次。
      </p>

      <AppStateBlock v-if="loading" message="加载中…" loading min-height="200px" />
      <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="200px" />

      <div v-else-if="records.length" class="rssm__table-scroll">
        <table class="rssm__table admin-table">
          <thead>
            <tr>
              <th>ID</th>
              <th>名称</th>
              <th>启用</th>
              <th>上次同步</th>
              <th>结果</th>
              <th class="admin-table__actions">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="rec in records" :key="rec.id">
              <td class="rssm__muted">{{ rec.id }}</td>
              <td>
                <span class="rssm__name">{{ rec.name }}</span>
                <span class="rssm__url" :title="rec.rss_url">{{ rec.rss_url }}</span>
              </td>
              <td>
                <AdminEnableToggle
                  :enabled="rec.enabled"
                  aria-label="订阅源启用切换"
                  @enable="handleToggle(rec, $event)"
                />
              </td>
              <td class="rssm__muted">{{ formatTime(rec.last_sync_at) }}</td>
              <td>
                <AppBadge :tone="statusTone(rec.last_status)">{{ statusLabel(rec.last_status) }}</AppBadge>
                <span v-if="rec.last_message" class="rssm__msg" :title="rec.last_message">
                  {{ rec.last_message }}
                </span>
              </td>
              <td class="admin-table__actions">
                <AdminRowActions>
                  <div class="rssm__actions">
                    <AdminTableActionBtn icon="rotate" title="立即同步" @click="handleSync(rec)" />
                    <AdminTableActionBtn icon="log" title="历史记录" @click="openHistory(rec)" />
                    <AdminTableActionBtn icon="edit" title="编辑" @click="openEdit(rec)" />
                    <AdminTableActionBtn icon="delete" title="删除" danger @click="handleDelete(rec)" />
                  </div>
                  <template #menu>
                    <button type="button" class="admin-row-actions__item" @click="handleSync(rec)">
                      立即同步
                    </button>
                    <button type="button" class="admin-row-actions__item" @click="openHistory(rec)">
                      历史记录
                    </button>
                    <button type="button" class="admin-row-actions__item" @click="openEdit(rec)">
                      编辑
                    </button>
                    <button
                      type="button"
                      class="admin-row-actions__item admin-row-actions__item--danger"
                      @click="handleDelete(rec)"
                    >
                      删除
                    </button>
                  </template>
                </AdminRowActions>
                <span v-if="syncingId === rec.id" class="rssm__syncing">同步中…</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <AppStateBlock v-else message="暂无订阅源，点击「新增订阅源」贴一个 RSS 地址" min-height="200px" />
    </SettingsCard>

    <!-- 新增 / 编辑 -->
    <AppModal
      :open="formOpen"
      size="account"
      :title="form.id ? '编辑订阅源' : '新增订阅源'"
      @close="formOpen = false"
    >
      <div class="rssm__form">
        <FormField label="订阅名称" required>
          <AppInput v-model="form.name" placeholder="例如：[Lilith-Raws] 某番剧" />
        </FormField>

        <FormField label="RSS 地址" required>
          <div class="rssm__form-row">
            <AppInput
              v-model="form.rss_url"
              placeholder="https://mikanani.me/RSS/Bangumi?bangumiId=xxxx&subgroupid=xxx"
            />
            <AppButton type="button" variant="secondary" :disabled="previewLoading" @click="openPreviewFromForm">
              预览
            </AppButton>
          </div>
          <template #help>先预览再保存：可以确认这个地址真的是 feed、能看到条目、能提取到磁力链接。</template>
        </FormField>

        <FormField label="云盘保存路径">
          <AppInput v-model="form.target_path" placeholder="留空则使用「云盘管理 → 云下载默认路径」" />
        </FormField>

        <FormField label="落地方式">
          <AppSelect v-model="form.action" :options="actionOptions" />
          <template #help>
            磁力、ed2k 等种子类条目统一走离线下载；网盘分享链接才会走转存。
            「只走离线下载」表示遇到网盘分享链接也不转存。
          </template>
        </FormField>

        <FormField label="媒体类型">
          <AppSelect v-model="form.media_type" :options="mediaTypeOptions" />
        </FormField>

        <FormField label="媒体库已有集判定">
          <AppSelect v-model="form.media_server" :options="mediaServerOptions" />
          <template #help>
            识别标题集号，命中所选媒体库已有的集则跳过下载；识别不出或库内查不到则照常下载。
            选「不判定」则不查媒体库、一律下载，重复版本交给洗版规则处理。
          </template>
        </FormField>

        <FormField label="包含过滤（正则，可选）">
          <AppInput v-model="form.include_regex" placeholder="如 1080p|2160p，命中标题才下载" />
        </FormField>

        <FormField label="排除过滤（正则，可选）">
          <AppInput v-model="form.exclude_regex" placeholder="如 720p|繁体，命中标题则跳过" />
        </FormField>

        <FormField label="状态">
          <label class="rssm__check">
            <input v-model="form.enabled" type="checkbox" />
            <span>启用（定时任务会同步此订阅）</span>
          </label>
        </FormField>

        <p v-if="formError" class="rssm__error">{{ formError }}</p>

        <div class="modal-form__footer">
          <AppButton type="button" variant="secondary" @click="formOpen = false">取消</AppButton>
          <AppButton type="button" variant="primary" :disabled="formSaving" @click="submitForm">
            {{ formSaving ? "保存中…" : "保存" }}
          </AppButton>
        </div>
      </div>
    </AppModal>

    <!-- 预览 -->
    <AppModal :open="previewOpen" size="lg" title="RSS 预览" @close="previewOpen = false">
      <div class="rssm__preview">
        <div class="rssm__preview-head">
          <span class="rssm__preview-meta">
            {{ preview ? `${preview.feed_title || "（无标题）"} · 解析到 ${preview.total} 条` : "尚未解析" }}
          </span>
          <AppButton type="button" variant="secondary" size="sm" :disabled="previewLoading" @click="runPreview">
            重新预览
          </AppButton>
        </div>

        <p v-if="preview && preview.charsets?.length" class="rssm__preview-note">
          该解析器支持的编码：{{ preview.charsets.join("、") }}。非 UTF-8 的源会在这一步直接报「暂不支持」。
        </p>

        <AppStateBlock v-if="previewLoading" message="正在抓取并解析…" loading min-height="180px" />
        <AppStateBlock v-else-if="previewError" :message="previewError" min-height="180px" />

        <div v-else-if="preview?.items?.length" class="rssm__preview-list">
          <div v-for="(it, i) in preview.items" :key="i" class="rssm__preview-item">
            <span class="rssm__preview-title">{{ it.title }}</span>
            <a v-if="it.link" :href="it.link" target="_blank" rel="noopener noreferrer" class="rssm__preview-link">
              {{ it.link }}
            </a>
            <span v-else class="rssm__muted">(无下载链接)</span>
          </div>
        </div>
      </div>
    </AppModal>

    <!-- 历史 -->
    <AppModal
      :open="historyOpen"
      size="lg"
      :title="historyFor ? `历史记录 · ${historyFor.name}` : '历史记录'"
      @close="historyOpen = false"
    >
      <div class="rssm__history">
        <div class="rssm__toolbar">
          <AppSelect
            v-model="historySourceId"
            :options="historyFilterOptions"
            class="rssm__filter"
            @update:model-value="loadHistory"
          />
          <AppButton type="button" variant="secondary" :disabled="historyLoading" @click="loadHistory">
            刷新
          </AppButton>
        </div>
        <p class="rssm__hint">最近 200 条记录，按时间倒序。删除一条 = 让它在下一轮同步时重新处理。</p>

        <AppStateBlock v-if="historyLoading" message="加载中…" loading min-height="180px" />
        <AppStateBlock v-else-if="historyError" :message="historyError" min-height="180px" />

        <div v-else-if="historyRows.length" class="rssm__table-scroll">
          <table class="rssm__table admin-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>订阅源</th>
                <th>标题</th>
                <th>状态</th>
                <th class="admin-table__actions">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in historyRows" :key="row.id">
                <td class="rssm__muted">{{ formatTime(row.created_at) }}</td>
                <td class="rssm__muted">{{ row.source_name || "-" }}</td>
                <td>
                  <span class="rssm__name">{{ row.title || row.guid }}</span>
                </td>
                <td><AppBadge :tone="statusTone(row.status)">{{ statusLabel(row.status) }}</AppBadge></td>
                <td class="admin-table__actions">
                  <AppButton type="button" variant="ghost" size="sm" @click="handleDeleteHistory(row)">
                    重新处理
                  </AppButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <AppStateBlock v-else message="暂无历史记录" min-height="180px" />
      </div>
    </AppModal>
  </div>
</template>

<style scoped>
.rssm__support {
  margin: 0;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}

.rssm__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.rssm__filter {
  width: 200px;
}

.rssm__hint {
  margin: 0 0 14px;
  font-size: 12px;
  line-height: 1.7;
  color: var(--text-muted, #6b7280);
}

.rssm__table-scroll {
  overflow-x: auto;
}

.rssm__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
  table-layout: fixed;
}

.rssm__table th:nth-child(1),
.rssm__table td:nth-child(1) {
  width: 56px;
}
.rssm__table th:nth-child(3),
.rssm__table td:nth-child(3) {
  width: 72px;
}
.rssm__table th:nth-child(4),
.rssm__table td:nth-child(4) {
  width: 150px;
}
.rssm__table th:nth-child(5),
.rssm__table td:nth-child(5) {
  width: 90px;
}

.rssm__table th,
.rssm__table td {
  padding: 10px 12px;
  text-align: left;
  vertical-align: middle;
  border-bottom: 1px solid var(--border-soft, #232733);
}

.rssm__table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted, #6b7280);
  white-space: nowrap;
}

.rssm__name {
  display: block;
  font-weight: 600;
  color: var(--text, #e5e7eb);
}

.rssm__url {
  display: block;
  margin-top: 2px;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rssm__muted {
  color: var(--text-muted, #6b7280);
  font-size: 12px;
}

.rssm__msg {
  display: block;
  margin-top: 3px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--text-muted, #6b7280);
}

.rssm__actions {
  display: flex;
  gap: 6px;
}

.rssm__syncing {
  margin-left: 6px;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}

.rssm__form,
.rssm__preview,
.rssm__history {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.rssm__form-row {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 8px;
  align-items: center;
}

.rssm__check {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text, #e5e7eb);
  cursor: pointer;
}

.rssm__error {
  margin: 0;
  font-size: 13px;
  color: var(--danger, #ef4444);
}

.rssm__preview-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.rssm__preview-meta {
  font-size: 13px;
  color: var(--text, #e5e7eb);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rssm__preview-note {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-muted, #6b7280);
}

.rssm__preview-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 420px;
  overflow-y: auto;
}

.rssm__preview-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px 10px;
  border: 1px solid var(--border-soft, #232733);
  border-radius: var(--radius-sm, 6px);
}

.rssm__preview-title {
  font-size: 13px;
  color: var(--text, #e5e7eb);
  word-break: break-all;
}

.rssm__preview-link {
  font-size: 12px;
  color: var(--brand, #3b82f6);
  word-break: break-all;
}

@media (max-width: 640px) {
  .rssm__form-row {
    grid-template-columns: 1fr;
  }
}
</style>
