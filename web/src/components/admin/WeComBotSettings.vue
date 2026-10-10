<script setup lang="ts">
// 企业微信智能机器人的入站配置。
//
// 与 Telegram Bot 板共用同一份命令表和同一套白名单语义，
// 差别只在「凭证从哪来、怎么验」：
// Telegram 用自己登记的 secret 头，企微用 sha1 签名 + AES 解密，
// 强度更高，所以这里不需要额外解释 webhook 口令。
//
// ⚠️ 一个必须在页面上说清的现实：本版只实现了**Webhook 短连接**。
// 企微的智能机器人还支持 WebSocket 长连接（不需要公网 IP、不用加解密），
// 但那要求主动出站维持一条长连接，与本项目「只开入站、不长轮询」的取向相反。
// 用长连接请等后续版本。
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import { fetchSettings, saveSettings } from "@/api/settings";
import {
  fetchWeComBotConfig,
  saveWeComBotConfig,
  TIER_LABELS,
  type BotCommand,
  type WeComBotConfig,
} from "@/api/weComBot";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import SettingsBoolSegment from "@/components/admin/SettingsBoolSegment.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import SettingsHelpTooltip from "@/components/admin/SettingsHelpTooltip.vue";
import SettingsRow from "@/components/admin/SettingsRow.vue";
import { useConfirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import "@/styles/settings-panel.css";

const loading = ref(false);
const saving = ref(false);
const config = ref<WeComBotConfig | null>(null);
const error = ref("");

// 凭证「可写不可读」：读回来是空的，只显示「已配置」。
// 留空提交 = 保持原值 —— 否则用户改一下白名单就把自己的机器人弄哑了。
const secretInput = ref("");
const aesInput = ref("");
const secretDirty = ref(false);
const aesDirty = ref(false);
const usersInput = ref("");
const chatsInput = ref("");
const corpInput = ref("");
const agentInput = ref("");
const enabled = ref(false);
const groupLink = ref(false);
// 可信 IP 自维护（N-4-a）是**出站通知**侧的能力，与入站机器人无关。
// 但它的两项开关登记在通用设置表里，所以这里读同一份注册表，不另开一套存法。
const trustIPEnabled = ref<boolean>(false);
const trustIPAuto = ref<boolean>(true);
// 脏标记：只认「用户在 load 之后动过」。
// 不能用「load 里赋完值就置 false」—— watch 的回调是 post-flush 的，
// 会在 load 整个函数跑完之后才触发，那时置的 false 已经被它覆盖成 true 了，
// 于是打开页面什么都不改直接点保存，也会白白发一次请求。
let trustIPSaved = { enabled: false, auto: true };
const trustIPDirty = computed(() =>
  trustIPEnabled.value !== trustIPSaved.enabled || trustIPAuto.value !== trustIPSaved.auto,
);

// 这两个开关走通用设置表（saveSettings），与上面的机器人配置不是同一条保存路径，
// 所以要用独立的脏标记。用 watch 而不是 @update:model-value：
// SettingsBoolSegment 的 model 是 defineModel<boolean>，它不显式声明 emits，
// 额外的 @update:model-value 监听在类型上落不到 boolean 上。
const { showConfirm } = useConfirm();

const commands = computed<BotCommand[]>(() => config.value?.commands ?? []);
const supportedCount = computed(() => commands.value.filter((c) => c.supported).length);
const secretPlaceholder = computed(() =>
  config.value?.token_configured ? "已配置，留空表示不修改" : "尚未配置，企微后台「Token」",
);
const aesPlaceholder = computed(() =>
  config.value?.aes_configured ? "已配置，留空表示不修改" : "尚未配置，企微后台「EncodingAESKey」",
);

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const data = await fetchWeComBotConfig();
    config.value = data;
    enabled.value = data.enabled;
    groupLink.value = data.group_link;
    usersInput.value = data.allowed_users.join(", ");
    chatsInput.value = data.allowed_chats.join(", ");
    corpInput.value = data.corp_id;
    agentInput.value = data.agent_id;
    secretInput.value = "";
    aesInput.value = "";
    secretDirty.value = false;
    aesDirty.value = false;
    // 这两项可能还没配过，取不到就保持默认，不要因此让整页报错。
    const snap = await fetchSettings();
    const byKey = new Map(snap.items.map((i) => [i.key, i.value]));
    trustIPSaved = {
      enabled: byKey.get("mo_wecom_trusted_ip_enabled") === "true",
      auto: (byKey.get("mo_wecom_trusted_ip_auto") ?? "true") === "true",
    };
    trustIPEnabled.value = trustIPSaved.enabled;
    trustIPAuto.value = trustIPSaved.auto;
  } catch (e) {
    error.value = getApiErrorMessage(e, "读取企业微信机器人配置失败");
  } finally {
    loading.value = false;
  }
}

async function save() {
  // 空白名单 = 拒绝一切，和 Telegram 侧同一语义。
  // 开启之前必须点一次确认：先开开关再填白名单的那段时间，任何人都能敲 Bot。
  if (enabled.value && !usersInput.value.trim() && !chatsInput.value.trim()) {
    await showConfirm({
      title: "两个白名单都是空的",
      message: "留空意味着「拒绝所有人」，不是「不限制」。现在开启机器人，等于没有一个人能用它。",
      hint: "至少填一个成员 ID 或群 chatid 再开启。",
      confirmText: "仍然开启",
      danger: true,
    });
  }
  if (enabled.value && (!secretInput.value.trim() && !config.value?.token_configured)) {
    await showConfirm({
      title: "还没有配置 Secret",
      message: "Secret 是企微后台用来算回调签名的，没配的话所有回调都会被判为伪造并丢弃。",
      hint: "请到企微后台「接收消息」页面查看 Token 与 EncodingAESKey。",
      confirmText: "仍然开启",
      danger: true,
    });
  }
  saving.value = true;
  try {
    if (trustIPDirty.value) {
      await saveSettings({
        mo_wecom_trusted_ip_enabled: String(trustIPEnabled.value),
        mo_wecom_trusted_ip_auto: String(trustIPAuto.value),
      });
      trustIPSaved = { enabled: trustIPEnabled.value, auto: trustIPAuto.value };
    }
    await saveWeComBotConfig({
      enabled: enabled.value,
      group_link: groupLink.value,
      allowed_users: usersInput.value.trim(),
      allowed_chats: chatsInput.value.trim(),
      corp_id: corpInput.value.trim(),
      agent_id: agentInput.value.trim(),
      ...(secretDirty.value && secretInput.value.trim()
        ? { token: secretInput.value.trim() }
        : {}),
      ...(aesDirty.value && aesInput.value.trim() ? { aes_key: aesInput.value.trim() } : {}),
    });
    toast.success("已保存，企业微信机器人配置已生效");
    secretInput.value = "";
    aesInput.value = "";
    secretDirty.value = false;
    aesDirty.value = false;
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
  <div class="wecombot-settings">
    <AppStateBlock v-if="loading && !config" message="正在读取企业微信机器人配置…" />
    <AppStateBlock v-else-if="error" :message="error" />

    <template v-else-if="config">
      <SettingsCard title="企业微信机器人总开关">
        <template #head-aside>
          <span class="wecombot-status" :class="{ 'wecombot-status--on': config.enabled }">
            {{ config.enabled ? "运行中" : "已停用" }}
          </span>
        </template>

        <SettingsRow setting-key="mo_wecom_bot_enabled">
          <template #info>
            <span>启用入站机器人</span>
            <SettingsHelpTooltip
              title="只管「别人能对企业微信这台机器人下命令」。停用它不会影响你现有的出站通知（企业微信应用通知渠道照常发）。"
            />
          </template>
          <template #control>
            <SettingsBoolSegment v-model="enabled" label="是否启用企业微信入站机器人" />
          </template>
        </SettingsRow>

        <SettingsRow setting-key="mo_wecom_bot_corp_id">
          <template #info>
            <span>企业 ID（corpid）</span>
            <SettingsHelpTooltip title="企业微信管理后台「我的企业」页面顶部。" />
          </template>
          <template #control>
            <div class="wecombot-ids">
              <AppInput v-model="corpInput" placeholder="例如 ww1a2b3c4d5e6f" />
            </div>
          </template>
        </SettingsRow>

        <SettingsRow setting-key="mo_wecom_bot_agent_id">
          <template #info>
            <span>应用 AgentId</span>
            <SettingsHelpTooltip title="智能机器人的应用 AgentId，不是自建应用的。" />
          </template>
          <template #control>
            <div class="wecombot-ids">
              <AppInput v-model="agentInput" placeholder="例如 1000002" />
            </div>
          </template>
        </SettingsRow>

        <SettingsRow setting-key="mo_wecom_bot_token">
          <template #info>
            <span>Token（回调签名用）</span>
            <SettingsHelpTooltip
              title="企微后台「接收消息」页面的 Token。它只用于算 msg_signature，不是 access_token，保存后读不回来。"
            />
          </template>
          <template #control>
            <AppInput
              v-model="secretInput"
              type="password"
              :placeholder="secretPlaceholder"
              autocomplete="new-password"
              @update:model-value="secretDirty = true"
            />
          </template>
        </SettingsRow>

        <SettingsRow setting-key="mo_wecom_bot_aes_key">
          <template #info>
            <span>EncodingAESKey</span>
            <SettingsHelpTooltip
              title="43 个字符的消息加解密密钥。回包要原样返回明文，不能加引号或换行，所以这一栏填错时企微会直接判定配置无效。"
            />
          </template>
          <template #control>
            <AppInput
              v-model="aesInput"
              type="password"
              :placeholder="aesPlaceholder"
              autocomplete="new-password"
              @update:model-value="aesDirty = true"
            />
          </template>
        </SettingsRow>

        <div class="wecombot-note">
          <strong>Secret 与 EncodingAESKey 保存后不再显示。</strong>
          留空不会清空原有配置；要换就重新填一次。
        </div>
      </SettingsCard>

      <SettingsCard title="谁能跟机器人说话">
        <SettingsRow setting-key="mo_wecom_bot_allowed_chats">
          <template #info>
            <span>放行的群 chatid</span>
            <SettingsHelpTooltip
              title="群里 @机器人 时收到的回调会带 chatid。群 ID 通常是 -100 开头的负数。请与成员白名单分开想清楚：放行一个群就等于放行群里的所有人。"
            />
          </template>
          <template #control>
            <div class="wecombot-ids">
              <AppInput v-model="chatsInput" placeholder="例如 -1001234567890" />
            </div>
          </template>
        </SettingsRow>

        <SettingsRow setting-key="mo_wecom_bot_allowed_users">
          <template #info>
            <span>放行的成员 userid</span>
            <SettingsHelpTooltip
              title="单聊的判据。注意：只有机器人创建者是超管时回调里的 from.userid 才是明文，否则是加密的 open_userid —— 那种情况下你没法自己填这一栏，群白名单仍然可用。"
            />
          </template>
          <template #control>
            <div class="wecombot-ids">
              <AppInput v-model="usersInput" placeholder="例如 zhangsan" />
            </div>
          </template>
        </SettingsRow>

        <div class="wecombot-warn">
          <strong>留空 = 拒绝所有人，不是「不限制」。</strong>
          两个白名单都为空时所有消息都会被忽略。这是刻意的：机器人能调用本站的网盘账号，
          「默认不信任任何人」比「默认信任所有能发消息的人」安全。
        </div>

        <SettingsRow setting-key="mo_wecom_bot_group_link_enabled">
          <template #info>
            <span>允许群里直接发链接转存</span>
            <SettingsHelpTooltip
              title="打开后，消息来自放行的群、且发消息的人在「放行的成员 userid」里，带链接的消息就会触发转存。只认已知的网盘链接。"
            />
          </template>
          <template #control>
            <SettingsBoolSegment v-model="groupLink" label="是否允许群链接转存" />
          </template>
        </SettingsRow>

        <div class="wecombot-actions">
          <AppButton variant="primary" :disabled="saving" @click="save">
            {{ saving ? "保存中…" : "保存" }}
          </AppButton>
          <span class="wecombot-actions__hint">保存后立即生效，无需重启。</span>
        </div>
      </SettingsCard>

      <SettingsCard title="可信 IP 自维护（出站通知）">
        <SettingsRow setting-key="mo_wecom_trusted_ip_enabled">
          <template #info>
            <span>自动维护可信 IP</span>
            <SettingsHelpTooltip
              title="自建应用在换网络后会收到 60020「不安全的访问IP」。打开后会自动把当前出口 IP 并入白名单。关掉它时什么也不做，连 DNS 查询都不会发。"
            />
          </template>
          <template #control>
            <SettingsBoolSegment v-model="trustIPEnabled" label="是否自动维护可信 IP" />
          </template>
        </SettingsRow>

        <SettingsRow setting-key="mo_wecom_trusted_ip_auto">
          <template #info>
            <span>自动写回</span>
            <SettingsHelpTooltip
              title="关掉就只读不写。写回是「读现有白名单 → 合并新出口 IP → 整表写回」：手工加的条目会保留，绝不会被覆盖掉。"
            />
          </template>
          <template #control>
            <SettingsBoolSegment v-model="trustIPAuto" label="是否自动写回白名单" />
          </template>
        </SettingsRow>

        <div class="wecombot-note">
          修的是<strong>自建应用</strong>的出口 IP 白名单，凭证取自你已配置的企业微信应用通知渠道，
          不用再填第二遍。企微接口在配置完成后约 1 分钟才生效，所以修好后的第一两条请求仍可能报 60020。
        </div>
      </SettingsCard>

      <SettingsCard title="企微后台怎么填">
        <p class="wecombot-text">
          把 <code>{{ config.callback_path }}</code> 拼上本站对外可访问的域名，
          填进智能机器人后台的「接收消息 → 接收消息服务器配置」。
        </p>
        <div class="wecombot-warn">
          <strong>本版只实现 Webhook 短连接，需要一台有公网地址的机器。</strong>
          企微还提供 WebSocket 长连接（不需要公网 IP、不用加解密、心跳 30 秒），
          但它要求主动出站维持连接，与本项目「只开入站、不长轮询」的取向相反，尚未实现。
        </div>
        <ul class="wecombot-steps">
          <li>URL 填好后，企微会发一次带 echostr 的校验请求；必须 1 秒内原样返回解密后的明文，加引号或换行一律判失败。</li>
          <li>URL 本身要带查询参数：<code>msg_signature</code>、<code>timestamp</code>、<code>nonce</code>、<code>echostr</code>。</li>
          <li>「Token」与「EncodingAESKey」就是上面两栏；填错时企微只会报「配置无效」，不会给出更细的原因。</li>
        </ul>
        <p class="wecombot-text">
          与 Telegram 不同，<strong>企微没有隐私模式开关</strong>：群里必须 @机器人 才会收到消息，
          所以「群里随手贴链接就转存」这件事，天然只有在你明确打开上面那个开关之后才会发生。
        </p>
      </SettingsCard>

      <SettingsCard title="命令一览">
        <template #head-aside>
          <span class="wecombot-count">
            已实现 {{ supportedCount }} / {{ commands.length }} 条
          </span>
        </template>
        <AdminEmptyState v-if="!commands.length" description="没有取到命令表，请稍后刷新" />
        <table v-else class="wecombot-cmds">
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
                <span v-if="cmd.aliases.length" class="wecombot-alias">
                  / {{ cmd.aliases.join(" / ") }}
                </span>
              </td>
              <td>{{ cmd.summary }}</td>
              <td><code>{{ cmd.usage }}</code></td>
              <td>{{ TIER_LABELS[cmd.tier] }}</td>
              <td>
                <span :class="cmd.supported ? 'wecombot-ok' : 'wecombot-soon'">
                  {{ cmd.supported ? "已实现" : "暂未支持" }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
        <div class="wecombot-warn">
          <strong>「仅管理员」的命令目前对所有人都关闭。</strong>
          企微身份与站内账号之间没有系统性的对应关系，在提供映射配置之前，
          系统把所有人一律按普通用户对待 —— 拿不到权限信息不等于「是超管」。
        </div>
      </SettingsCard>
    </template>
  </div>
</template>

<style scoped>
.wecombot-settings {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.wecombot-status,
.wecombot-count {
  font-size: 12px;
  color: var(--text-3);
}

.wecombot-status--on {
  color: var(--success);
}

.wecombot-ids {
  width: 320px;
  max-width: 100%;
}

.wecombot-note,
.wecombot-warn,
.wecombot-text,
.wecombot-actions__hint {
  font-size: 13px;
  line-height: 1.7;
  color: var(--text-2);
}

.wecombot-warn {
  margin: 4px 0 12px;
  padding: 10px 12px;
  border-left: 3px solid var(--warning);
  background: var(--warning-soft, transparent);
  border-radius: 4px;
}

.wecombot-note {
  margin-bottom: 12px;
}

.wecombot-steps {
  margin: 0 0 12px;
  padding-left: 20px;
  font-size: 13px;
  line-height: 1.9;
  color: var(--text-2);
}

.wecombot-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.wecombot-cmds {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.wecombot-cmds th,
.wecombot-cmds td {
  text-align: left;
  padding: 8px 10px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}

.wecombot-cmds th {
  color: var(--text-3);
  font-weight: 500;
}

.wecombot-alias {
  margin-left: 6px;
  color: var(--text-3);
  font-size: 12px;
}

.wecombot-ok {
  color: var(--success);
}

.wecombot-soon {
  color: var(--text-3);
}

code {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
</style>
