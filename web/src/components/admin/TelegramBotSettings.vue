<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  fetchBotConfig,
  saveBotConfig,
  TIER_LABELS,
  type BotCommand,
  type BotConfig,
} from "@/api/telegramBot";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import WeComBotSettings from "@/components/admin/WeComBotSettings.vue";
import SettingsBoolSegment from "@/components/admin/SettingsBoolSegment.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import SettingsHelpTooltip from "@/components/admin/SettingsHelpTooltip.vue";
import SettingsRow from "@/components/admin/SettingsRow.vue";
import { useConfirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import "@/styles/settings-panel.css";

// Telegram Bot 的入站端点配置。
//
// 这页管的只有「谁能跟 Bot 说话」和 token —— Bot 本身是外部入口，
// 拿到 token + 白名单就等于拿到一个能用本站网盘账号转存的通道，
// 所以权限单独挂在 telegram.manage 上，不跟通知渠道共用。
const loading = ref(false);
const saving = ref(false);
const config = ref<BotConfig | null>(null);
const error = ref("");

// 凭证「可写不可读」：这一栏读回来是空的，只显示「已配置」。
// 留空提交 = 保持原值 —— 用户改一下白名单就把自己的 token 抹掉，
// 是这类「打码字段」最常见的一种自伤。
const tokenInput = ref("");
const tokenDirty = ref(false);
const usersInput = ref("");
const chatsInput = ref("");
const enabled = ref(false);
const groupLink = ref(false);

const { showConfirm } = useConfirm();

const commands = computed<BotCommand[]>(() => config.value?.commands ?? []);
const supportedCount = computed(() => commands.value.filter((c) => c.supported).length);
const tokenPlaceholder = computed(() =>
  config.value?.token_configured ? "已配置，留空表示不修改" : "尚未配置，从 @BotFather 获取",
);

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const data = await fetchBotConfig();
    config.value = data;
    enabled.value = data.enabled;
    groupLink.value = data.group_link;
    usersInput.value = data.allowed_users.join(", ");
    chatsInput.value = data.allowed_chats.join(", ");
    tokenInput.value = "";
    tokenDirty.value = false;
  } catch (e) {
    error.value = getApiErrorMessage(e, "读取 Bot 配置失败");
  } finally {
    loading.value = false;
  }
}

async function save() {
  // 两个白名单里有一个是空的，就等于「这一侧完全没人能说话」。
  // 这是配置页最容易犯的错：先开 Bot 再填白名单，中间这段时间任何人都能敲 Bot。
  const emptyUsers = !usersInput.value.trim();
  const emptyChats = !chatsInput.value.trim();
  if (enabled.value && emptyUsers && emptyChats) {
    await showConfirm({
      title: "两个白名单都是空的",
      message: "留空意味着「拒绝所有人」，不是「不限制」。现在开启 Bot，等于没有一个人能用它。",
      hint: "至少填一个 Telegram 用户 ID 或群 ID 再开启。",
      confirmText: "仍然开启",
      danger: true,
    });
  }
  saving.value = true;
  try {
    await saveBotConfig({
      enabled: enabled.value,
      group_link: groupLink.value,
      allowed_users: usersInput.value.trim(),
      allowed_chats: chatsInput.value.trim(),
      ...(tokenDirty.value && tokenInput.value.trim()
        ? { token: tokenInput.value.trim() }
        : {}),
    });
    toast.success("已保存，Bot 配置已同步到 Telegram");
    tokenInput.value = "";
    tokenDirty.value = false;
    await load();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "保存失败"));
  } finally {
    saving.value = false;
  }
}

onMounted(load);
</script>

<template>
  <div class="bot-settings">
    <AppStateBlock v-if="loading && !config" message="正在读取 Bot 配置…" />
    <AppStateBlock v-else-if="error" :message="error" />

    <template v-else-if="config">
      <SettingsCard title="Bot 总开关">
        <template #head-aside>
          <span class="bot-status" :class="{ 'bot-status--on': config.enabled }">
            {{ config.enabled ? "运行中" : "已停用" }}
          </span>
        </template>

        <SettingsRow setting-key="mo_telegram_bot_enabled">
          <template #info>
            <span>启用入站 Bot</span>
            <SettingsHelpTooltip
              title="只管「别人能对这台机器下命令」。停用它不会影响你现有的出站通知（Telegram 通知渠道照常发）。"
            />
          </template>
          <template #control>
            <SettingsBoolSegment v-model="enabled" label="是否启用入站 Bot" />
          </template>
        </SettingsRow>

        <SettingsRow setting-key="mo_telegram_bot_token">
          <template #info>
            <span>Bot Token</span>
            <SettingsHelpTooltip
              title="从 @BotFather 用 /newbot 拿到。token 是凭证，保存后读不回来 —— 只能覆盖，不能查看。填错只能去 BotFather 重新拿。"
            />
          </template>
          <template #control>
            <div class="bot-token">
              <AppInput
                v-model="tokenInput"
                type="password"
                :placeholder="tokenPlaceholder"
                autocomplete="new-password"
                @update:model-value="tokenDirty = true"
              />
            </div>
          </template>
        </SettingsRow>

        <div class="bot-note">
          <strong>Token 保存后不再显示。</strong>
          如果以后换了新 token，在这里重新填一次即可；留空不会清空原有配置。
        </div>
      </SettingsCard>

      <SettingsCard title="谁能跟 Bot 说话">
        <SettingsRow setting-key="mo_telegram_bot_allowed_users">
          <template #info>
            <span>放行的 Telegram 用户 ID</span>
            <SettingsHelpTooltip
              title="多个用英文逗号分隔。填「你的 ID」私聊直接跟 Bot 对话；填「群的 ID」则群里所有人都能用 Bot —— 这两件事请分开想清楚。"
            />
          </template>
          <template #control>
            <div class="bot-ids">
              <AppInput v-model="usersInput" placeholder="例如 123456789" />
            </div>
          </template>
        </SettingsRow>

        <SettingsRow setting-key="mo_telegram_bot_allowed_chats">
          <template #info>
            <span>放行的群 / 频道 ID</span>
            <SettingsHelpTooltip
              title="群 ID 通常是 -100 开头的一串负数（频道同样如此）。在群里打开 BotFather 的 /setprivacy 后，Bot 只能收到以斜杠开头的命令和被 @ 提及的消息。"
            />
          </template>
          <template #control>
            <div class="bot-ids">
              <AppInput v-model="chatsInput" placeholder="例如 -1001234567890" />
            </div>
          </template>
        </SettingsRow>

        <div class="bot-warn">
          <strong>留空 = 拒绝所有人，不是「不限制」。</strong>
          两个白名单都为空时 Bot 会拒绝所有消息。这是刻意的：Bot 能调用本站的网盘账号，
          「默认不信任任何人」比「默认信任所有能发消息的人」安全。
        </div>

        <SettingsRow setting-key="mo_telegram_bot_group_link_enabled">
          <template #info>
            <span>允许群里直接发链接转存</span>
            <SettingsHelpTooltip
              title="打开后，只要消息来自放行的群、且发消息的人在「放行的用户 ID」里，带链接的消息就会触发转存。只认已知的网盘链接，不会把任意网址都拿来转存。"
            />
          </template>
          <template #control>
            <SettingsBoolSegment v-model="groupLink" label="是否允许群链接转存" />
          </template>
        </SettingsRow>

        <div class="bot-actions">
          <AppButton variant="primary" :disabled="saving" @click="save">
            {{ saving ? "保存中…" : "保存并同步" }}
          </AppButton>
          <span class="bot-actions__hint">
            保存时会立即把 webhook 地址与校验口令重新登记给 Telegram。
          </span>
        </div>
      </SettingsCard>

      <SettingsCard title="隐私模式（要在 BotFather 里改）">
        <p class="bot-text">
          拿到 webhook 地址：<code>{{ config.webhook_path }}</code>（前面拼上本站对外可访问的域名）。
        </p>
        <ol class="bot-steps">
          <li>在 Telegram 里跟 <code>@BotFather</code> 对话，发送 <code>/setprivacy</code>。</li>
          <li>选你的 Bot，再选 <code>Disable</code>（关闭隐私模式）。</li>
          <li>BotFather 会提示：<em>注意，关闭后机器人会看到群里的所有消息。</em></li>
        </ol>
        <div class="bot-warn">
          <strong>隐私模式打开时（默认），Bot 只能收到以斜杠开头的命令和被 @ 提及的消息</strong>，
          普通聊天内容它完全看不到。关闭隐私模式才能在群里接住随手贴的链接，
          代价是它能读到群里的一切 —— 这台机器上是你的网盘账号，请自己权衡。
        </div>
        <p class="bot-text">
          在 Bot 里发送 <code>/setprivacy</code>，会得到同样的一串操作步骤。
        </p>
      </SettingsCard>

      <SettingsCard title="命令一览">
        <template #head-aside>
          <span class="bot-count">
            已实现 {{ supportedCount }} / {{ commands.length }} 条
          </span>
        </template>
        <AdminEmptyState v-if="!commands.length" description="没有取到命令表，请稍后刷新" />
        <table v-else class="bot-cmds">
          <thead>
            <tr>
              <th>命令</th>
              <th>说明</th>
              <th>用法</th>
              <th>权限</th>
              <th>状态</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="cmd in commands" :key="cmd.name">
              <td>
                <code>{{ cmd.name }}</code>
                <span v-if="cmd.aliases.length" class="bot-alias">
                  / {{ cmd.aliases.join(" / ") }}
                </span>
              </td>
              <td>{{ cmd.summary }}</td>
              <td><code>{{ cmd.usage }}</code></td>
              <td>{{ TIER_LABELS[cmd.tier] }}</td>
              <td>
                <span :class="cmd.supported ? 'bot-ok' : 'bot-soon'">
                  {{ cmd.supported ? "已实现" : "暂未支持" }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </SettingsCard>

      <!--
        企业微信机器人与可信 IP 自维护挂在同一页，而不是新开一个一级导航：
        它们与 Telegram Bot 共享同一份命令表、同一套白名单语义和一个权限位
        （telegram.manage），拆成两个页面只会让用户以为权限也是分开的。
      -->
      <WeComBotSettings />
    </template>
  </div>
</template>

<style scoped>
.bot-settings {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.bot-status,
.bot-count {
  font-size: 12px;
  color: var(--text-3);
}

.bot-status--on {
  color: var(--success);
}

.bot-token,
.bot-ids {
  width: 320px;
  max-width: 100%;
}

.bot-note,
.bot-warn,
.bot-text,
.bot-actions__hint {
  font-size: 13px;
  line-height: 1.7;
  color: var(--text-2);
}

.bot-warn {
  margin: 4px 0 12px;
  padding: 10px 12px;
  border-left: 3px solid var(--warning);
  background: var(--warning-soft, transparent);
  border-radius: 4px;
}

.bot-note {
  margin-bottom: 12px;
}

.bot-steps {
  margin: 0 0 12px;
  padding-left: 20px;
  font-size: 13px;
  line-height: 1.9;
  color: var(--text-2);
}

.bot-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.bot-cmds {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.bot-cmds th,
.bot-cmds td {
  text-align: left;
  padding: 8px 10px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}

.bot-cmds th {
  color: var(--text-3);
  font-weight: 500;
}

.bot-alias {
  margin-left: 6px;
  color: var(--text-3);
  font-size: 12px;
}

.bot-ok {
  color: var(--success);
}

.bot-soon {
  color: var(--text-3);
}

code {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
</style>