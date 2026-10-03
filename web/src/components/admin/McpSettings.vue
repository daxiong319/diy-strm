<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  mcpApi,
  type McpConfig,
  type McpConfigPatch,
  type McpTool,
} from "@/api/mcp";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import SettingsBoolSegment from "@/components/admin/SettingsBoolSegment.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import SettingsRow from "@/components/admin/SettingsRow.vue";
import SettingsRowLabel from "@/components/admin/SettingsRowLabel.vue";
import SectionTabBar from "@/components/admin/SectionTabBar.vue";
import AdminStatusPill from "@/components/admin/AdminStatusPill.vue";
import SvgIcon from "@/components/icons/SvgIcon.vue";
import { useSectionTabRoute } from "@/composables/useSectionTabRoute";
import { toast } from "@/composables/useToast";

// MCP 服务：把播放与订阅能力以 MCP 协议暴露给外部 LLM 客户端，
// 同时提供内置智能助理的模型配置。
// 挂在后台单页的「MCP 服务」分区下，内部再分三个 tab。

const SERVICE_TAB = "service";
const TOOL_TAB = "tool";
const ASSISTANT_TAB = "assistant";

const tabs = [
  { key: SERVICE_TAB, label: "服务设置" },
  { key: TOOL_TAB, label: "工具清单" },
  { key: ASSISTANT_TAB, label: "智能助理" },
];
const { activeTab, setActiveTab } = useSectionTabRoute(SERVICE_TAB, [
  SERVICE_TAB,
  TOOL_TAB,
  ASSISTANT_TAB,
]);

const loading = ref(true);
const saving = ref(false);
const testing = ref(false);
const loadError = ref("");

const config = ref<McpConfig | null>(null);
const tools = ref<McpTool[]>([]);
const serverUrl = ref("");
const copied = ref(false);

/** 表单只保存用户改过的字段；提交时只发这些 key，避免覆盖并发修改。 */
const dirty = reactive<McpConfigPatch>({});
const dirtyKeys = computed(() => Object.keys(dirty));

/** 助理密钥输入框：后端只回传「是否已配置」，不回明文。 */
const apiKeyInput = ref("");
/** 用户点「清除密钥」后置位，提交时发空串告知后端删除。 */
const clearApiKey = ref(false);

const enabled = computed({
  get: () => dirty.enabled ?? config.value?.enabled ?? false,
  set: (value: boolean) => {
    dirty.enabled = value;
  },
});

const allowWriteTools = computed({
  get: () => dirty.allow_write_tools ?? config.value?.allow_write_tools ?? false,
  set: (value: boolean) => {
    dirty.allow_write_tools = value;
  },
});

const maxToolRounds = computed({
  get: () => String(dirty.max_tool_rounds ?? config.value?.max_tool_rounds ?? ""),
  set: (value: string) => {
    const parsed = Number.parseInt(value, 10);
    dirty.max_tool_rounds = Number.isFinite(parsed) ? parsed : undefined;
  },
});

const timeout = computed({
  get: () => String(dirty.timeout ?? config.value?.timeout ?? ""),
  set: (value: string) => {
    const parsed = Number.parseInt(value, 10);
    dirty.timeout = Number.isFinite(parsed) ? parsed : undefined;
  },
});

const assistantEnabled = computed({
  get: () => dirty.assistant_enabled ?? config.value?.assistant_enabled ?? false,
  set: (value: boolean) => {
    dirty.assistant_enabled = value;
  },
});

const assistantBaseUrl = computed({
  get: () => dirty.assistant_base_url ?? config.value?.assistant_base_url ?? "",
  set: (value: string) => {
    dirty.assistant_base_url = value;
  },
});

const assistantModel = computed({
  get: () => dirty.assistant_model_name ?? config.value?.assistant_model_name ?? "",
  set: (value: string) => {
    dirty.assistant_model_name = value;
  },
});

const assistantPrompt = computed({
  get: () => dirty.assistant_prompt ?? config.value?.assistant_prompt ?? "",
  set: (value: string) => {
    dirty.assistant_prompt = value;
  },
});

const apiKeyConfigured = computed(() => config.value?.assistant_api_key_set ?? false);

/** 助理模型配置是否留空并回落到「AI 识别设置」。 */
const assistantInherits = computed(
  () => !config.value?.assistant_base_url && !config.value?.assistant_model_name,
);

const enabledToolCount = computed(() => tools.value.filter((t) => t.enabled).length);

const serverUrlText = computed(() => serverUrl.value || "/api/mcp");

async function load() {
  loading.value = true;
  loadError.value = "";
  try {
    // 后端把配置、工具清单与服务地址包在同一响应里，一次取回即可。
    const data = await mcpApi.getConfig();
    config.value = data.config;
    tools.value = data.tools ?? [];
    serverUrl.value = data.server_url ?? "";
  } catch (error) {
    loadError.value = getApiErrorMessage(error, "加载 MCP 配置失败");
  } finally {
    loading.value = false;
  }
}

function buildPatch(): McpConfigPatch {
  const patch: McpConfigPatch = { ...dirty };
  // 传空串表示清除已保存的密钥；没动过输入框就不发这个字段。
  if (clearApiKey.value) {
    patch.assistant_api_key = "";
  } else if (apiKeyInput.value) {
    patch.assistant_api_key = apiKeyInput.value;
  }
  return patch;
}

async function save() {
  if (saving.value) return;
  saving.value = true;
  try {
    const data = await mcpApi.updateConfig(buildPatch());
    config.value = data.config;
    tools.value = data.tools ?? [];
    serverUrl.value = data.server_url ?? "";
    for (const key of Object.keys(dirty)) delete dirty[key as keyof McpConfigPatch];
    apiKeyInput.value = "";
    clearApiKey.value = false;
    toast.success("MCP 设置已保存");
  } catch (error) {
    toast.error(getApiErrorMessage(error, "保存 MCP 设置失败"));
  } finally {
    saving.value = false;
  }
}

async function testAssistant() {
  if (testing.value) return;
  testing.value = true;
  try {
    const data = await mcpApi.testAssistant();
    toast.success(data.message || "智能助理连接正常");
  } catch (error) {
    toast.error(getApiErrorMessage(error, "智能助理连接失败"));
  } finally {
    testing.value = false;
  }
}

async function copyServerUrl() {
  try {
    await navigator.clipboard.writeText(serverUrlText.value);
    copied.value = true;
    window.setTimeout(() => {
      copied.value = false;
    }, 1500);
  } catch {
    toast.error("复制失败，请手动选择复制");
  }
}

function resetDirty() {
  for (const key of Object.keys(dirty)) delete dirty[key as keyof McpConfigPatch];
  apiKeyInput.value = "";
  clearApiKey.value = false;
}

onMounted(load);
</script>

<template>
  <div class="mcp-settings">
    <SectionTabBar :tabs="tabs" :model-value="activeTab" @update:model-value="setActiveTab">
      <template #actions>
        <span v-if="dirtyKeys.length" class="mcp-settings__pending">
          {{ dirtyKeys.length }} 项待保存
        </span>
        <AppButton v-if="dirtyKeys.length" variant="ghost" size="sm" @click="resetDirty">
          放弃修改
        </AppButton>
        <AppButton
          variant="primary"
          size="sm"
          :disabled="saving || !dirtyKeys.length"
          @click="save"
        >
          {{ saving ? "保存中…" : "保存" }}
        </AppButton>
      </template>
    </SectionTabBar>

    <AppStateBlock v-if="loading" loading message="正在加载 MCP 配置…" />
    <AppStateBlock v-else-if="loadError" :message="loadError" />

    <template v-else-if="config">
      <!-- 服务设置 -->
      <SettingsCard v-if="activeTab === SERVICE_TAB" title="MCP 服务">
        <SettingsRow :changed="'enabled' in dirty">
          <SettingsRowLabel
            label="启用 MCP 服务"
            :changed="'enabled' in dirty"
            help-title="MCP 服务"
            help-text="开启后外部 LLM 客户端（Claude Desktop、Cursor 等）可通过下方地址调用本工具集。"
          />
          <SettingsBoolSegment v-model="enabled" label="启用 MCP 服务" />
        </SettingsRow>

        <SettingsRow>
          <SettingsRowLabel
            label="服务地址"
            help-title="如何接入"
            help-text="把该地址填入支持 MCP 的客户端，并在请求头 X-API-Key 中携带「API 秘钥」页签里创建的密钥。"
          />
          <div class="mcp-settings__url">
            <code class="mcp-settings__code">{{ serverUrlText }}</code>
            <AppButton variant="ghost" size="sm" @click="copyServerUrl">
              <SvgIcon :name="copied ? 'check' : 'copy'" />
              {{ copied ? "已复制" : "复制" }}
            </AppButton>
          </div>
        </SettingsRow>

        <SettingsRow :changed="'allow_write_tools' in dirty">
          <SettingsRowLabel
            label="允许写操作工具"
            :changed="'allow_write_tools' in dirty"
            help-title="写操作"
            help-text="关闭后模型只能查询，无法执行网盘重命名、移动、新建目录与删除等不可逆操作。"
          />
          <SettingsBoolSegment v-model="allowWriteTools" label="允许写操作工具" />
        </SettingsRow>

        <SettingsRow :changed="'max_tool_rounds' in dirty">
          <SettingsRowLabel
            label="单轮最大工具调用次数"
            :changed="'max_tool_rounds' in dirty"
            help-text="一次提问中模型最多可以连续调用多少次工具，次数越多越慢但能完成更复杂的任务。"
          />
          <AppInput v-model="maxToolRounds" type="number" class="mcp-settings__input" />
        </SettingsRow>

        <SettingsRow :changed="'timeout' in dirty">
          <SettingsRowLabel
            label="单次请求超时"
            :changed="'timeout' in dirty"
            help-text="单位秒。工具调用耗时较长时应适当放宽，避免长任务被提前中断。"
          />
          <AppInput v-model="timeout" type="number" class="mcp-settings__input" />
        </SettingsRow>
      </SettingsCard>

      <!-- 工具清单 -->
      <SettingsCard v-else-if="activeTab === TOOL_TAB" title="可用工具">
        <div class="mcp-settings__summary">
          共 {{ tools.length }} 个工具，其中 {{ enabledToolCount }} 个已启用。
        </div>
        <div v-if="!tools.length" class="mcp-settings__empty">当前没有可用工具。</div>
        <div v-else class="mcp-settings__tools">
          <div v-for="tool in tools" :key="tool.name" class="mcp-settings__tool">
            <div class="mcp-settings__tool-head">
              <code class="mcp-settings__tool-name">{{ tool.name }}</code>
              <AdminStatusPill :tone="tool.enabled ? (tool.read_only ? 'brand' : 'warning') : 'muted'">
                {{ tool.enabled ? (tool.read_only ? "只读" : "写操作") : "已禁用" }}
              </AdminStatusPill>
            </div>
            <p class="mcp-settings__tool-desc">{{ tool.description }}</p>
          </div>
        </div>
      </SettingsCard>

      <!-- 智能助理 -->
      <SettingsCard v-else title="智能助理">
        <SettingsRow :changed="'assistant_enabled' in dirty">
          <SettingsRowLabel
            label="启用站内助理"
            :changed="'assistant_enabled' in dirty"
            help-text="关闭后，「智能助理」页面不可用（外部 MCP 客户端不受影响）。"
          />
          <SettingsBoolSegment v-model="assistantEnabled" label="启用站内助理" />
        </SettingsRow>

        <div v-if="!assistantEnabled" class="mcp-settings__hint">
          站内助理已关闭。以下模型配置仍会保存，重新开启后生效。
        </div>

        <div v-if="assistantInherits" class="mcp-settings__hint">
          当前未单独配置模型，助理将回落到「AI 识别设置」中已保存的接口与模型。
        </div>

        <SettingsRow :changed="'assistant_base_url' in dirty">
          <SettingsRowLabel
            label="接口地址"
            :changed="'assistant_base_url' in dirty"
            help-text="留空则复用「AI 识别设置」的接口地址。需兼容 OpenAI 的对话接口。"
          />
          <AppInput
            v-model="assistantBaseUrl"
            placeholder="https://api.openai.com/v1"
            class="mcp-settings__input mcp-settings__input--wide"
          />
        </SettingsRow>

        <SettingsRow :changed="'assistant_model_name' in dirty">
          <SettingsRowLabel
            label="模型名称"
            :changed="'assistant_model_name' in dirty"
            help-text="留空则复用「AI 识别设置」的模型。需支持工具调用（Function Calling）。"
          />
          <AppInput
            v-model="assistantModel"
            placeholder="gpt-4o-mini"
            class="mcp-settings__input"
          />
        </SettingsRow>

        <SettingsRow :changed="'assistant_api_key' in dirty || clearApiKey">
          <SettingsRowLabel
            label="接口密钥"
            :changed="'assistant_api_key' in dirty || clearApiKey"
            help-text="留空则复用「AI 识别设置」的密钥。已保存的密钥不会回显。"
          />
          <div class="mcp-settings__key">
            <AppInput
              v-model="apiKeyInput"
              type="password"
              :placeholder="apiKeyConfigured ? '已配置（留空则不修改）' : '未配置'"
              :disabled="clearApiKey"
              class="mcp-settings__input mcp-settings__input--wide"
            />
            <AppButton
              v-if="apiKeyConfigured && !clearApiKey"
              variant="ghost"
              size="sm"
              @click="clearApiKey = true"
            >
              清除
            </AppButton>
            <AppButton v-else-if="clearApiKey" variant="ghost" size="sm" @click="clearApiKey = false">
              取消清除
            </AppButton>
          </div>
        </SettingsRow>

        <SettingsRow :changed="'assistant_prompt' in dirty">
          <SettingsRowLabel
            label="系统提示词"
            :changed="'assistant_prompt' in dirty"
            help-text="留空则使用内置默认提示词。"
          />
          <textarea
            v-model="assistantPrompt"
            rows="5"
            class="mcp-settings__textarea"
            placeholder="留空使用内置默认提示词"
          />
        </SettingsRow>

        <div class="mcp-settings__actions">
          <AppButton variant="secondary" size="sm" :disabled="testing" @click="testAssistant">
            {{ testing ? "测试中…" : "测试连接" }}
          </AppButton>
        </div>
      </SettingsCard>
    </template>
  </div>
</template>

<style scoped>
.mcp-settings__pending {
  margin-right: 8px;
  font-size: 12px;
  color: var(--text-muted);
}

.mcp-settings__url,
.mcp-settings__key {
  display: flex;
  align-items: center;
  gap: 8px;
}

.mcp-settings__code {
  padding: 4px 8px;
  border-radius: 6px;
  background: var(--surface-sunken);
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  word-break: break-all;
}

.mcp-settings__input {
  width: 120px;
}

.mcp-settings__input--wide {
  width: 280px;
}

.mcp-settings__textarea {
  width: 100%;
  padding: 8px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface);
  color: var(--text);
  font-family: inherit;
  font-size: 13px;
  resize: vertical;
}

.mcp-settings__summary {
  margin-bottom: 12px;
  font-size: 13px;
  color: var(--text-muted);
}

.mcp-settings__empty {
  padding: 24px 0;
  text-align: center;
  color: var(--text-muted);
  font-size: 13px;
}

.mcp-settings__tools {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.mcp-settings__tool {
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
}

.mcp-settings__tool-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.mcp-settings__tool-name {
  font-family: var(--font-mono, monospace);
  font-size: 13px;
  font-weight: 600;
}

.mcp-settings__tool-desc {
  margin: 6px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-muted);
}

.mcp-settings__hint {
  margin-bottom: 12px;
  padding: 8px 12px;
  border-radius: 8px;
  background: var(--surface-sunken);
  font-size: 12px;
  color: var(--text-muted);
}

.mcp-settings__actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 12px;
}
</style>
