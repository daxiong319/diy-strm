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
import {
  NOTIFY_SCENES_CONFIG_KEY,
  encodeSceneSubscriptions,
  fetchNotifyScenes,
  parseSceneSubscriptions,
  type NotifyScene,
  type NotifySceneList,
} from "@/api/notifyScenes";
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

// T12 · 通知场景清单。清单本身由后端下发（见 api/notifyScenes.ts 的注释）：
// 「哪些分类归哪个场景」是后端投递判定的事实，前端自己写一份必然漂移。
const scenes = ref<NotifyScene[]>([]);
const unmappedScenes = ref<NotifySceneList["unmapped"]>([]);
const sceneNotes = ref<string[]>([]);

// 表单：type + name + enabled + 动态字段值 + 场景订阅
const form = reactive({
  type: "",
  name: "",
  enabled: true,
  values: {} as Record<string, string>,
  // ⚠️ null = 「未配置过」= 全订阅；[] = 用户显式清空，也按全订阅处理
  // （后端 ParseSceneSubscriptions 对空值返回「订阅全部」，这里必须同口径，
  //  否则用户一个都不勾会被存成空串，下次打开又变成全订阅，像是勾没用）。
  scenes: null as string[] | null,
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

/**
 * 场景是否处于「已订阅」态。
 *
 * 关键在 allScenesSelected()：后端把空字符串解释成「订阅全部」，
 * 所以复选框全亮时，保存下去也必须是空串 —— 若把全亮写成
 * "upload,strm,organize,…"，将来后端加第 8 个场景，这个渠道就会
 * 静默收不到新场景的通知，而界面上看不出任何区别。
 */
function allScenesSelected(): boolean {
  return form.scenes === null;
}

function isSceneSelected(scene: string): boolean {
  return allScenesSelected() || (form.scenes?.includes(scene) ?? false);
}

/** 单点切换：从「全订阅」第一次点掉某个框时，先把全集摊开再取消它 */
function toggleScene(scene: string, checked: boolean) {
  const current = allScenesSelected() ? scenes.value.map((s) => s.scene) : [...(form.scenes ?? [])];
  const next = checked ? [...new Set([...current, scene])] : current.filter((s) => s !== scene);
  // 取消到只剩「全亮」时回写成 null，让存储侧保持"未配置 = 全订阅"的语义
  form.scenes = next.length === scenes.value.length ? null : next;
}

function selectAllScenes() {
  form.scenes = null;
}

function clearSceneSelection() {
  form.scenes = scenes.value.length ? [] : null;
}

/**
 * 列表页展示：该渠道当前订阅了哪些场景。
 *
 * 认不出来的场景 ID（后端改名或下线过）照实显示原值 —— 悄悄吞掉的话，
 * 用户以为订阅着，实际那条通知一条都收不到。
 */
function channelSceneSummary(ch: NotifyChannelRecord): string {
  const picked = parseSceneSubscriptions(ch.config?.[NOTIFY_SCENES_CONFIG_KEY]);
  if (picked.length === 0) return "全部场景";
  return picked.map(sceneLabel).join("、");
}

function sceneLabel(id: string): string {
  return scenes.value.find((s) => s.scene === id)?.label ?? id;
}

async function load() {
  loading.value = true;
  try {
    const [metaRes, listRes, sceneRes] = await Promise.all([
      fetchNotifyChannelMeta(),
      fetchNotifyChannels(),
      fetchNotifyScenes(),
    ]);
    metas.value = metaRes.items ?? [];
    channels.value = listRes.items ?? [];
    scenes.value = sceneRes.items ?? [];
    unmappedScenes.value = sceneRes.unmapped ?? [];
    sceneNotes.value = sceneRes.notes ?? [];
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
  form.scenes = null;
  resetValues();
  dialogOpen.value = true;
}

function openEdit(ch: NotifyChannelRecord) {
  editing.value = ch;
  form.type = ch.type;
  form.name = ch.name;
  form.enabled = ch.enabled;
  form.values = { ...(ch.config ?? {}) };
  // 空串 = 未配置 = 全订阅，用 null 表示"全订阅态"而不是 []
  const picked = parseSceneSubscriptions(ch.config?.[NOTIFY_SCENES_CONFIG_KEY]);
  form.scenes = picked.length === 0 ? null : picked;
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
    // 场景订阅写进 config.scenes（英文逗号分隔）。全订阅时写空串 ——
    // 与「这个渠道建于场景化之前、根本没这个键」是同一个语义，
    // 后端也就无需区分"显式全选"和"没配过"。
    const config = { ...form.values };
    if (form.scenes === null) {
      delete config[NOTIFY_SCENES_CONFIG_KEY];
    } else {
      config[NOTIFY_SCENES_CONFIG_KEY] = encodeSceneSubscriptions(form.scenes);
    }
    const payload = {
      type: form.type,
      name: form.name.trim() || typeLabel(form.type),
      config,
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
              <th>场景</th>
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
                <span class="nc__scenes" :title="channelSceneSummary(ch)">
                  {{ channelSceneSummary(ch) }}
                </span>
              </td>
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

        <!-- T12：场景订阅。全订阅态下后端把空串读成"全订阅"，
             所以全亮时就该保持空串，不要把七个 ID 写进去。 -->
        <div class="nc__scenes-box">
          <div class="nc__scenes-head">
            <span class="nc__scenes-title">订阅场景</span>
            <span class="nc__scenes-actions">
              <button type="button" class="nc__scenes-link" @click="selectAllScenes">全选</button>
              <button
                type="button"
                class="nc__scenes-link"
                :disabled="!scenes.length"
                @click="clearSceneSelection"
              >
                清空
              </button>
            </span>
          </div>

          <div v-if="scenes.length === 0" class="nc__field-hint">
            场景清单加载中…若持续为空，请检查后端版本。
          </div>

          <div v-else class="nc__scene-grid">
            <label
              v-for="s in scenes"
              :key="s.scene"
              class="nc__scene"
              :class="{ 'nc__scene--off': s.categories.length === 0 }"
              :title="s.trigger"
            >
              <input
                type="checkbox"
                :checked="isSceneSelected(s.scene)"
                @change="toggleScene(s.scene, ($event.target as HTMLInputElement).checked)"
              />
              <span class="nc__scene-body">
                <span class="nc__scene-label">{{ s.label }}</span>
                <span class="nc__scene-hint">{{ s.trigger }}</span>
                <span v-if="s.categories.length === 0" class="nc__scene-hint nc__scene-hint--warn">
                  暂无通知产出点
                </span>
              </span>
            </label>
          </div>

          <div v-if="unmappedScenes.length" class="nc__scenes-note">
            以下告警始终送达，不受场景开关影响：
            <span v-for="(u, i) in unmappedScenes" :key="u.scene">
              {{ i > 0 ? "、" : "" }}{{ u.label }}
            </span>
          </div>

          <ul v-if="sceneNotes.length" class="nc__scenes-notes">
            <li v-for="(n, i) in sceneNotes" :key="i">{{ n }}</li>
          </ul>
        </div>

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
  width: 26%;
}

.nc__table th:nth-child(2),
.nc__table td:nth-child(2) {
  width: 18%;
}

.nc__table th:nth-child(3),
.nc__table td:nth-child(3) {
  width: 22%;
}

.nc__table th:nth-child(4),
.nc__table td:nth-child(4) {
  width: 12%;
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

.nc__scenes {
  display: block;
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.5;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.nc__scenes-box {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--border-soft);
  border-radius: var(--radius-sm);
  background: var(--surface-sunken);
}

.nc__scenes-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.nc__scenes-title {
  font-size: 13px;
  font-weight: 700;
  color: var(--text);
}

.nc__scenes-actions {
  display: flex;
  gap: 10px;
}

.nc__scenes-link {
  padding: 0;
  border: none;
  background: none;
  color: var(--brand);
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}

.nc__scenes-link:disabled {
  color: var(--text-muted);
  cursor: not-allowed;
}

.nc__scene-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.nc__scene {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 9px 10px;
  border: 1px solid var(--border-soft);
  border-radius: var(--radius-sm);
  background: var(--surface);
  font-size: 12px;
  cursor: pointer;
  transition: border-color 0.18s ease;
}

.nc__scene:hover {
  border-color: color-mix(in srgb, var(--brand) 32%, var(--border-soft));
}

.nc__scene input {
  margin: 2px 0 0;
  accent-color: var(--brand);
  cursor: pointer;
}

.nc__scene-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.nc__scene-label {
  font-weight: 700;
  color: var(--text);
}

.nc__scene-hint {
  font-size: 11px;
  line-height: 1.5;
  color: var(--text-muted);
}

.nc__scene-hint--warn {
  color: var(--warning);
}

.nc__scenes-note,
.nc__scenes-notes {
  margin: 0;
  font-size: 11px;
  line-height: 1.6;
  color: var(--text-muted);
}

.nc__scenes-notes {
  padding-left: 16px;
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

  .nc__scene-grid {
    grid-template-columns: 1fr;
  }
}
</style>
