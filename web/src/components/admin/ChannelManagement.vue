<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  channelLinkFullURL,
  deleteChannel,
  fetchChannels,
  previewChannel,
  providerLabel,
  resetChannelCursor,
  runChannelsNow,
  saveChannel,
  toggleChannel,
  DISCOVERY_PROVIDER_OPTIONS,
  type ChannelPost,
  type DiscoveryChannel,
} from "@/api/discovery";
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

const { showConfirm } = useConfirm();

const loading = ref(false);
const errorMsg = ref("");
const records = ref<DiscoveryChannel[]>([]);
const sourceFilter = ref("");
const runningAll = ref(false);

// 表单里只允许选具体网盘（不能是"全部"），所以单独定义一份不带空选项的列表
const formProviderOptions = DISCOVERY_PROVIDER_OPTIONS.filter((o) => o.value !== "");

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    const res = await fetchChannels(sourceFilter.value || undefined);
    records.value = res.items ?? [];
  } catch (e) {
    errorMsg.value = getApiErrorMessage(e, "加载频道列表失败");
    records.value = [];
  } finally {
    loading.value = false;
  }
}

/** 立即抓取一轮全部频道 */
async function handleRunAll() {
  runningAll.value = true;
  try {
    const res = await runChannelsNow(sourceFilter.value || undefined);
    toast.success(res.summary || "频道抓取完成");
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "频道抓取失败"));
  } finally {
    runningAll.value = false;
  }
}

async function handleToggle(rec: DiscoveryChannel, enabled: boolean) {
  try {
    await toggleChannel(rec.id, enabled);
    rec.enabled = enabled;
    toast.success(enabled ? "频道已启用" : "频道已停用");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "切换启用状态失败"));
  }
}

async function handleDelete(rec: DiscoveryChannel) {
  try {
    await showConfirm({
      title: "删除频道",
      message: `确定删除频道「${rec.channel}」吗？已有的转存记录不会被删除。`,
      icon: "trash",
      confirmText: "删除",
      danger: true,
    });
  } catch {
    // showConfirm 在用户关闭弹窗时 reject，必须直接 return 以免把取消当成确认。
    return;
  }
  try {
    await deleteChannel(rec.id);
    records.value = records.value.filter((x) => x.id !== rec.id);
    toast.success("频道已删除");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "删除失败"));
  }
}

async function handleResetCursor(rec: DiscoveryChannel) {
  try {
    await showConfirm({
      title: "重置抓取游标",
      message: `确定重置频道「${rec.channel}」的抓取游标吗？下一轮将从头回溯抓帖，可能重复转存已有资源。`,
      icon: "warning",
      confirmText: "重置游标",
      danger: true,
    });
  } catch {
    return;
  }
  try {
    await resetChannelCursor(rec.id);
    toast.success("游标已重置，下一轮将从头抓取");
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "重置游标失败"));
  }
}

// ------------------------------ 新增 / 编辑弹窗 ------------------------------

const formOpen = ref(false);
const formSaving = ref(false);
const formError = ref("");
const form = ref<{ id?: number; source_type: string; channel: string; enabled: boolean }>({
  source_type: "123",
  channel: "",
  enabled: true,
});

function openCreate() {
  form.value = { source_type: sourceFilter.value || "123", channel: "", enabled: true };
  formError.value = "";
  previewPosts.value = [];
  formOpen.value = true;
}

function openEdit(rec: DiscoveryChannel) {
  form.value = {
    id: rec.id,
    source_type: rec.source_type,
    channel: rec.channel,
    enabled: rec.enabled,
  };
  formError.value = "";
  previewPosts.value = [];
  formOpen.value = true;
}

async function submitForm() {
  const channel = form.value.channel.trim();
  if (!channel) {
    formError.value = "请填写频道名";
    return;
  }
  if (!form.value.source_type) {
    formError.value = "请选择来源网盘";
    return;
  }
  formSaving.value = true;
  formError.value = "";
  try {
    await saveChannel({
      id: form.value.id,
      source_type: form.value.source_type,
      channel,
      enabled: form.value.enabled,
    });
    toast.success(form.value.id ? "频道已更新" : "频道已添加");
    formOpen.value = false;
    await load();
  } catch (e) {
    formError.value = getApiErrorMessage(e, "保存频道失败");
  } finally {
    formSaving.value = false;
  }
}

// ------------------------------ 预览 ------------------------------

const previewPosts = ref<ChannelPost[]>([]);
const previewLoading = ref(false);
const previewError = ref("");
/** 预览用的频道名：表单里可能还没保存，单独存一份用于展示标题 */
const previewChannelName = ref("");

/**
 * 预览频道最新帖：添加前先确认内容质量，避免加错频道。
 * 只读接口，不改游标、不转存。
 */
async function runPreview(channel: string) {
  const name = channel.trim();
  if (!name) {
    toast.warning("请先填写频道名再预览");
    return;
  }
  previewChannelName.value = name;
  previewLoading.value = true;
  previewError.value = "";
  previewPosts.value = [];
  try {
    const res = await previewChannel(name, 10);
    previewPosts.value = res.items ?? [];
    if (!previewPosts.value.length) {
      previewError.value = "该频道最近没有解析到帖子（可能是频道不存在、非公开频道或没有网盘分享链接）";
    }
  } catch (e) {
    previewError.value = getApiErrorMessage(e, "预览失败");
  } finally {
    previewLoading.value = false;
  }
}

const previewModalOpen = ref(false);

async function openPreview(channel: string) {
  previewModalOpen.value = true;
  await runPreview(channel);
}

function openPreviewFromForm() {
  previewModalOpen.value = true;
  void runPreview(form.value.channel);
}

/** 帖子正文摘要：去掉多余空行，便于单行截断展示 */
function postSummary(text: string): string {
  return (text || "").replace(/\s+/g, " ").trim();
}

function formatPostTime(v: string): string {
  if (!v) return "-";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return v;
  return d.toLocaleString();
}

const previewModalTitle = computed(() => `频道预览 · ${previewChannelName.value || "-"}`);

onMounted(() => {
  void load();
});
</script>

<template>
  <div class="chanm">
    <SettingsCard title="TG 频道订阅" accent="var(--brand)">
      <div class="chanm__toolbar">
        <AppSelect
          v-model="sourceFilter"
          :options="DISCOVERY_PROVIDER_OPTIONS"
          class="chanm__filter"
          @update:model-value="load"
        />
        <AppButton type="button" variant="secondary" :disabled="loading" @click="load">刷新</AppButton>
        <AppButton type="button" variant="secondary" :disabled="runningAll" @click="handleRunAll">
          {{ runningAll ? "抓取中…" : "全部立即抓取" }}
        </AppButton>
        <AppButton type="button" variant="primary" @click="openCreate">新增频道</AppButton>
      </div>

      <p class="chanm__hint">
        频道名支持 <code>@name</code>、<code>t.me/s/xxx</code> 或裸名，保存时会自动归一化去重；
        点击行内「预览」可先确认抓到的帖子质量再添加。
      </p>

      <AppStateBlock v-if="loading" message="加载中…" loading min-height="200px" />
      <AppStateBlock v-else-if="errorMsg" :message="errorMsg" min-height="200px" />

      <div v-else-if="records.length" class="chanm__table-scroll">
        <table class="chanm__table admin-table">
          <thead>
            <tr>
              <th>ID</th>
              <th>来源网盘</th>
              <th>频道名</th>
              <th>启用</th>
              <th>游标</th>
              <th>上次运行</th>
              <th class="admin-table__actions">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="rec in records" :key="rec.id" class="chanm__row">
              <td class="chanm__muted">{{ rec.id }}</td>
              <td><AppBadge tone="info">{{ providerLabel(rec.source_type) }}</AppBadge></td>
              <td>
                <span class="chanm__name" :title="rec.channel">@{{ rec.channel }}</span>
              </td>
              <td>
                <AdminEnableToggle
                  :enabled="rec.enabled"
                  aria-label="频道启用切换"
                  @enable="handleToggle(rec, $event)"
                />
              </td>
              <td class="chanm__muted">
                <span v-if="rec.last_post_id">{{ rec.last_post_id }}</span>
                <span v-else class="chanm__warn">未初始化</span>
              </td>
              <td class="chanm__muted">{{ formatPostTime(rec.last_run_at) }}</td>
              <td class="admin-table__actions">
                <AdminRowActions>
                  <div class="chanm__actions">
                    <AdminTableActionBtn icon="log" title="预览" @click="openPreview(rec.channel)" />
                    <AdminTableActionBtn icon="rotate" title="重置游标" @click="handleResetCursor(rec)" />
                    <AdminTableActionBtn icon="edit" title="编辑" @click="openEdit(rec)" />
                    <AdminTableActionBtn icon="delete" title="删除" danger @click="handleDelete(rec)" />
                  </div>
                  <template #menu>
                    <button type="button" class="admin-row-actions__item" @click="openPreview(rec.channel)">
                      预览
                    </button>
                    <button type="button" class="admin-row-actions__item" @click="handleResetCursor(rec)">
                      重置游标
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
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <AppStateBlock v-else message="暂无频道，点击「新增频道」添加 TG 公开频道" min-height="200px" />
    </SettingsCard>

    <!-- 新增 / 编辑 -->
    <AppModal
      :open="formOpen"
      size="account"
      :title="form.id ? '编辑频道' : '新增频道'"
      @close="formOpen = false"
    >
      <div class="chanm__form">
        <FormField label="来源网盘" required>
          <AppSelect v-model="form.source_type" :options="formProviderOptions" />
        </FormField>
        <FormField label="频道名" required>
          <div class="chanm__form-row">
            <AppInput
              v-model="form.channel"
              placeholder="支持 @name 或 t.me/s/xxx 链接，会自动归一化"
            />
            <AppButton type="button" variant="secondary" :disabled="previewLoading" @click="openPreviewFromForm">
              预览
            </AppButton>
          </div>
        </FormField>
        <FormField label="状态">
          <label class="chanm__check">
            <input v-model="form.enabled" type="checkbox" />
            <span>启用该频道（停用后抓取会跳过，但保留游标便于恢复）</span>
          </label>
        </FormField>

        <p v-if="formError" class="chanm__error">{{ formError }}</p>

        <div class="modal-form__footer">
          <AppButton type="button" variant="secondary" @click="formOpen = false">取消</AppButton>
          <AppButton type="button" variant="primary" :disabled="formSaving" @click="submitForm">
            {{ formSaving ? "保存中…" : "保存" }}
          </AppButton>
        </div>
      </div>
    </AppModal>

    <!-- 预览 -->
    <AppModal :open="previewModalOpen" size="lg" :title="previewModalTitle" @close="previewModalOpen = false">
      <div class="chanm__preview">
        <div class="chanm__preview-head">
          <span class="chanm__preview-meta">最近 {{ previewPosts.length }} 条帖子</span>
          <AppButton
            type="button"
            variant="secondary"
            size="sm"
            :disabled="previewLoading"
            @click="runPreview(previewChannelName)"
          >
            重新预览
          </AppButton>
        </div>

        <AppStateBlock v-if="previewLoading" message="正在抓取频道…" loading min-height="180px" />
        <AppStateBlock v-else-if="previewError" :message="previewError" min-height="180px" />

        <div v-else-if="previewPosts.length" class="chanm__posts">
          <article v-for="(p, i) in previewPosts" :key="p.PostID || i" class="chanm__post">
            <header class="chanm__post-head">
              <span class="chanm__post-id">#{{ p.PostID }}</span>
              <span class="chanm__post-time">{{ formatPostTime(p.Time) }}</span>
              <AppBadge :tone="p.Links?.length ? 'success' : 'neutral'">
                {{ p.Links?.length ? `${p.Links.length} 个分享链接` : "无分享链接" }}
              </AppBadge>
            </header>
            <p class="chanm__post-text">{{ postSummary(p.Text) || "（无正文）" }}</p>
            <ul v-if="p.Links?.length" class="chanm__links">
              <li v-for="(l, j) in p.Links" :key="j" class="chanm__link">
                <AppBadge tone="info">{{ providerLabel(l.Type) }}</AppBadge>
                <a :href="channelLinkFullURL(l)" target="_blank" rel="noopener noreferrer" class="chanm__link-a">
                  {{ channelLinkFullURL(l) }}
                </a>
              </li>
            </ul>
          </article>
        </div>

        <AppStateBlock v-else message="该频道没有可预览的帖子" min-height="180px" />
      </div>
    </AppModal>
  </div>
</template>

<style scoped>
.chanm__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.chanm__filter {
  width: 140px;
}

.chanm__hint {
  margin: 0 0 14px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-muted, #6b7280);
}

.chanm__hint code {
  padding: 1px 5px;
  border-radius: var(--radius-xs, 4px);
  background: var(--surface-sunken, #14161c);
  font-size: 12px;
}

.chanm__table-scroll {
  overflow-x: auto;
}

.chanm__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
  table-layout: fixed;
}

.chanm__table th:nth-child(1),
.chanm__table td:nth-child(1) {
  width: 56px;
}
.chanm__table th:nth-child(2),
.chanm__table td:nth-child(2) {
  width: 110px;
}
.chanm__table th:nth-child(3),
.chanm__table td:nth-child(3) {
  width: 26%;
}
.chanm__table th:nth-child(4),
.chanm__table td:nth-child(4) {
  width: 100px;
}
.chanm__table th:nth-child(5),
.chanm__table td:nth-child(5) {
  width: 16%;
}
.chanm__table th:nth-child(6),
.chanm__table td:nth-child(6) {
  width: 160px;
}

.chanm__table th,
.chanm__table td {
  padding: 10px 12px;
  text-align: left;
  vertical-align: middle;
  border-bottom: 1px solid var(--border-soft, #232733);
}

.chanm__table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted, #6b7280);
  white-space: nowrap;
}

.chanm__name {
  display: block;
  font-weight: 600;
  color: var(--text, #e5e7eb);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.chanm__muted {
  color: var(--text-muted, #6b7280);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.chanm__warn {
  color: var(--warning, #f59e0b);
}

.chanm__actions {
  display: flex;
  justify-content: center;
  gap: 4px;
}

.chanm__form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.chanm__form-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.chanm__form-row :deep(.app-input) {
  flex: 1;
}

.chanm__check {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text, #d1d5db);
  cursor: pointer;
}

.chanm__error {
  margin: 0;
  font-size: 13px;
  color: var(--danger, #ef4444);
}

.chanm__preview {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.chanm__preview-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.chanm__preview-meta {
  font-size: 13px;
  color: var(--text-muted, #6b7280);
}

.chanm__posts {
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 56vh;
  overflow-y: auto;
}

.chanm__post {
  padding: 10px 12px;
  border: 1px solid var(--border-soft, #232733);
  border-radius: var(--radius-sm, 8px);
  background: var(--surface-sunken, #14161c);
}

.chanm__post-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 6px;
}

.chanm__post-id {
  font-size: 12px;
  font-weight: 700;
  color: var(--text, #e5e7eb);
}

.chanm__post-time {
  font-size: 12px;
  color: var(--text-muted, #6b7280);
}

.chanm__post-text {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
  color: var(--text, #d1d5db);
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.chanm__links {
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 5px;
}

.chanm__link {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.chanm__link-a {
  font-size: 12px;
  color: var(--brand, #e50914);
  text-decoration: none;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.chanm__link-a:hover {
  text-decoration: underline;
}
</style>
