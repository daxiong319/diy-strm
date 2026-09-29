<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  createNotifyChannel,
  deleteNotifyChannel,
  fetchNotifyChannelMeta,
  fetchNotifyChannels,
  testNotifyChannel,
  updateNotifyChannel,
  type NotifyChannelFieldMeta,
  type NotifyChannelMeta,
  type NotifyChannelRecord,
} from "@/api/notifyChannels";
import AppButton from "@/components/base/AppButton.vue";
import AppBadge from "@/components/base/AppBadge.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import FormField from "@/components/base/FormField.vue";
import AdminEnableToggle from "@/components/admin/AdminEnableToggle.vue";
import AdminRowActions from "@/components/admin/AdminRowActions.vue";
import AdminTableActionBtn from "@/components/admin/AdminTableActionBtn.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import "@/styles/admin-shared.css";
import { useConfirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import "@/styles/admin-table.css";

const props = withDefaults(
  defineProps<{
    accent?: string;
  }>(),
  { accent: "var(--brand)" },
);

const { showConfirm } = useConfirm();

const loading = ref(false);
const saving = ref(false);
const testing = ref(false);
const dialogOpen = ref(false);
const editing = ref<NotifyChannelRecord | null>(null);
const channels = ref<NotifyChannelRecord[]>([]);
const metas = ref<NotifyChannelMeta[]>([]);

// 表单：type + name + enabled + 动态字段值
const form = reactive({
  type: "",
  name: "",
  enabled: true,
  values: {} as Record<string, string>,
});

// 当前选中渠道类型的元数据
const activeMeta = computed<NotifyChannelMeta | null>(
  () => metas.value.find((m) => m.id === form.type) ?? null,
);

const typeOptions = computed(() =>
  metas.value.map((m) => ({ value: m.id, label: m.title })),
);

function typeLabel(type: string): string {
  return metas.value.find((m) => m.id === type)?.title ?? type;
}

async function load() {
  loading.value = true;
  try {
    const [metaRes, listRes] = await Promise.all([
      fetchNotifyChannelMeta(),
      fetchNotifyChannels(),
    ]);
    metas.value = metaRes.items ?? [];
    channels.value = listRes.items ?? [];
  } catch (e) {
    toast.error(getApiErrorMessage(e, "加载通知渠道失败"));
  } finally {
    loading.value = false;
  }
}

function resetValues() {
  const vals: Record<string, string> = {};
  for (const f of activeMeta.value?.fields ?? []) {
    // select 默认选第一个非空 option
    if (f.type === "select" && f.options && f.options.length > 0) {
      vals[f.key] = f.options[0].value;
    } else {
      vals[f.key] = "";
    }
  }
  form.values = vals;
}

function onTypeChange() {
  resetValues();
}

function openCreate() {
  editing.value = null;
  form.type = metas.value[0]?.id ?? "";
  form.name = "";
  form.enabled = true;
  resetValues();
  dialogOpen.value = true;
}

function openEdit(ch: NotifyChannelRecord) {
  editing.value = ch;
  form.type = ch.type;
  form.name = ch.name;
  form.enabled = ch.enabled;
  form.values = { ...(ch.config ?? {}) };
  dialogOpen.value = true;
}

function validate(): string {
  const meta = activeMeta.value;
  if (!meta) return "请选择渠道类型";
  for (const req of meta.required ?? []) {
    if (!(form.values[req] ?? "").trim()) {
      const label = meta.fields.find((f) => f.key === req)?.label ?? req;
      return `请填写「${label}」`;
    }
  }
  return "";
}

async function save() {
  const err = validate();
  if (err) {
    toast.error(err);
    return;
  }
  saving.value = true;
  try {
    const payload = {
      type: form.type,
      name: form.name.trim() || typeLabel(form.type),
      config: { ...form.values },
      enabled: form.enabled,
    };
    if (editing.value?.id) {
      await updateNotifyChannel(editing.value.id, payload);
      toast.success("渠道已更新");
    } else {
      await createNotifyChannel(payload);
      toast.success("渠道已创建");
    }
    dialogOpen.value = false;
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "保存渠道失败"));
  } finally {
    saving.value = false;
  }
}

async function handleToggle(ch: NotifyChannelRecord) {
  if (!ch.id) return;
  try {
    await updateNotifyChannel(ch.id, { enabled: !ch.enabled });
    ch.enabled = !ch.enabled;
  } catch (e) {
    toast.error(getApiErrorMessage(e, "切换状态失败"));
  }
}

async function handleTest(ch: NotifyChannelRecord) {
  testing.value = true;
  try {
    await testNotifyChannel({ type: ch.type, config: ch.config });
    toast.success(`已发送测试消息到「${ch.name || typeLabel(ch.type)}」`);
  } catch (e) {
    toast.error(getApiErrorMessage(e, "测试发送失败"));
  } finally {
    testing.value = false;
  }
}

async function handleDialogTest() {
  const err = validate();
  if (err) {
    toast.error(err);
    return;
  }
  testing.value = true;
  try {
    await testNotifyChannel({ type: form.type, config: { ...form.values } });
    toast.success("测试消息已发送");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "测试发送失败"));
  } finally {
    testing.value = false;
  }
}

async function handleDelete(ch: NotifyChannelRecord) {
  if (!ch.id) return;
  const id = ch.id;
  try {
    await showConfirm({
      title: "删除通知渠道",
      message: `确定删除「${ch.name || typeLabel(ch.type)}」吗？删除后将不再向该渠道推送通知。`,
      icon: "trash",
      confirmText: "删除",
      danger: true,
    });
  } catch {
    return;
  }
  try {
    await deleteNotifyChannel(id);
    channels.value = channels.value.filter((item) => item.id !== id);
    toast.success("渠道已删除");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "删除失败"));
  }
}

function fieldComponentType(f: NotifyChannelFieldMeta): string {
  return f.type;
}

onMounted(load);

defineExpose({ openCreate });
</script>

<template>
  <SettingsCard title="通知渠道" :accent="props.accent">
    <p class="nc__meta">
      配置 Telegram / Bark / 企业微信 等外部推送渠道，系统通知（账号失效、STRM 告警等）将同步推送到已启用的渠道。
    </p>

    <AppStateBlock v-if="loading" message="加载渠道中…" loading min-height="180px" />

    <div v-else>
      <div v-if="channels.length === 0" class="nc__empty">
        尚未配置任何通知渠道，点击右上角「新增渠道」创建。
      </div>

      <div v-else class="nc__table-scroll">
        <table class="nc__table">
          <thead>
            <tr>
              <th>名称</th>
              <th>类型</th>
              <th>状态</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="ch in channels" :key="ch.id" class="nc__row">
              <td>
                <span class="nc__name">{{ ch.name || typeLabel(ch.type) }}</span>
              </td>
              <td><AppBadge tone="info">{{ typeLabel(ch.type) }}</AppBadge></td>
              <td>
                <AppBadge :tone="ch.enabled ? 'success' : 'neutral'">
                  {{ ch.enabled ? "启用" : "已禁用" }}
                </AppBadge>
              </td>
              <td>
                <AdminRowActions>
                  <div class="nc__actions">
                    <AdminEnableToggle
                      :enabled="ch.enabled"
                      aria-label="通知渠道启用切换"
                      @enable="handleToggle(ch)"
                    />
                    <AdminTableActionBtn
                      icon="play"
                      title="发送测试"
                      :disabled="testing"
                      @click="handleTest(ch)"
                    />
                    <AdminTableActionBtn icon="edit" title="编辑" @click="openEdit(ch)" />
                    <AdminTableActionBtn icon="delete" title="删除" danger @click="handleDelete(ch)" />
                  </div>
                  <template #menu>
                    <button type="button" class="admin-row-actions__item" @click="handleToggle(ch)">
                      {{ ch.enabled ? "禁用" : "启用" }}
                    </button>
                    <button type="button" class="admin-row-actions__item" :disabled="testing" @click="handleTest(ch)">
                      发送测试
                    </button>
                    <button type="button" class="admin-row-actions__item" @click="openEdit(ch)">编辑</button>
                    <button
                      type="button"
                      class="admin-row-actions__item admin-row-actions__item--danger"
                      @click="handleDelete(ch)"
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
    </div>

    <AppModal
      :open="dialogOpen"
      size="account"
      :title="editing ? '编辑通知渠道' : '新增通知渠道'"
      @close="dialogOpen = false"
    >
      <div class="nc__form">
        <div class="nc__form-row">
          <FormField label="渠道类型">
            <AppSelect
              v-model="form.type"
              :options="typeOptions"
              :disabled="!!editing"
              @update:model-value="onTypeChange"
            />
          </FormField>
          <FormField label="显示名称">
            <AppInput v-model="form.name" :placeholder="activeMeta?.title ?? ''" />
          </FormField>
        </div>

        <p v-if="activeMeta?.note" class="nc__note">{{ activeMeta.note }}</p>

        <div v-for="f in activeMeta?.fields ?? []" :key="f.key">
          <FormField :label="f.label">
            <AppSelect
              v-if="fieldComponentType(f) === 'select'"
              v-model="form.values[f.key]"
              :options="f.options ?? []"
            />
            <textarea
              v-else-if="fieldComponentType(f) === 'textarea'"
              v-model="form.values[f.key]"
              class="nc__textarea"
              :rows="f.rows ?? 3"
              :placeholder="f.placeholder"
            />
            <AppInput
              v-else
              v-model="form.values[f.key]"
              :type="fieldComponentType(f) === 'password' ? 'password' : 'text'"
              :placeholder="f.placeholder"
              autocomplete="off"
            />
          </FormField>
          <p v-if="f.hint" class="nc__field-hint">{{ f.hint }}</p>
        </div>

        <div class="modal-form__footer">
          <AppButton type="button" variant="secondary" :disabled="testing" @click="handleDialogTest">
            {{ testing ? "发送中…" : "发送测试" }}
          </AppButton>
          <AppButton type="button" variant="primary" :disabled="saving" @click="save">
            {{ saving ? "保存中…" : editing ? "保存渠道" : "创建渠道" }}
          </AppButton>
        </div>
      </div>
    </AppModal>
  </SettingsCard>
</template>

<style scoped>
.nc__meta {
  margin: 0 0 4px;
  padding-bottom: 12px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--text-muted);
  border-bottom: 1px solid var(--border-soft);
}

.nc__empty {
  padding: 32px 0;
  text-align: center;
  color: var(--text-muted);
  font-size: 13px;
}

.nc__table-scroll {
  overflow-x: auto;
  margin: 0 -22px;
  padding-bottom: 16px;
}

.nc__table {
  width: 100%;
  border-collapse: collapse;
  table-layout: fixed;
  font-size: 13px;
}

.nc__table th:nth-child(1),
.nc__table td:nth-child(1) {
  width: 34%;
}

.nc__table th:nth-child(2),
.nc__table td:nth-child(2) {
  width: 24%;
}

.nc__table th:nth-child(3),
.nc__table td:nth-child(3) {
  width: 16%;
}

.nc__table th:last-child,
.nc__table td:last-child {
  width: 26%;
  text-align: center;
}

.nc__table th,
.nc__table td {
  padding: 12px 22px;
  text-align: left;
  vertical-align: middle;
  border-bottom: 1px solid var(--border-soft);
}

.nc__table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
}

.nc__table tbody tr:last-child td {
  border-bottom: none;
}

.nc__row {
  transition: background-color 0.18s ease;
}

.nc__row:hover {
  background: color-mix(in srgb, var(--brand) 4%, transparent);
}

.nc__name {
  font-weight: 700;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.nc__actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  align-items: center;
  gap: 8px;
}

.nc__form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.nc__form-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.nc__note {
  margin: 0;
  padding: 10px 12px;
  border-radius: var(--radius-sm);
  background: var(--surface-sunken);
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-muted);
}

.nc__textarea {
  width: 100%;
  padding: 9px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface);
  color: var(--text);
  font-family: inherit;
  font-size: 13px;
  line-height: 1.5;
  resize: vertical;
}

.nc__textarea:focus {
  outline: none;
  border-color: var(--brand);
}

.nc__field-hint {
  margin: 4px 0 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--text-muted);
}

@media (max-width: 720px) {
  .nc__form-row {
    grid-template-columns: 1fr;
  }
}
</style>
