<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  mcpApi,
  streamMcpAssistant,
  type McpSession,
  type McpToolCall,
} from "@/api/mcp";
import AppButton from "@/components/base/AppButton.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import AdminStatusPill from "@/components/admin/AdminStatusPill.vue";
import SvgIcon from "@/components/icons/SvgIcon.vue";
import { toast } from "@/composables/useToast";
import { formatTime } from "@/utils/format";

// 内置智能助理对话页：自然语言查询影库、控制订阅与频道订阅。
// 挂在后台单页的「MCP 服务」分区下，内部再分「对话」与「历史会话」两个 tab。

/** 供渲染的一条消息：助理回复带工具调用过程。 */
interface ChatBubble {
  role: "user" | "assistant";
  content: string;
  toolCalls: McpToolCall[];
  error?: string;
  pending?: boolean;
}

const sessions = ref<McpSession[]>([]);
const sessionsLoading = ref(false);
const activeSessionID = ref("");
const bubbles = ref<ChatBubble[]>([]);

const input = ref("");
const sending = ref(false);
const messagesLoading = ref(false);
const historyError = ref("");

/** 当前正在流式输出的气泡下标，-1 表示没有。 */
const streamingIndex = ref(-1);
/** 中断当前流式请求。 */
let abortStream: (() => void) | null = null;
const scrollHost = ref<HTMLElement | null>(null);

const canSend = computed(() => !sending.value && input.value.trim().length > 0);

const activeSession = computed(() =>
  sessions.value.find((s) => s.session_id === activeSessionID.value),
);

async function loadSessions() {
  sessionsLoading.value = true;
  try {
    const data = await mcpApi.sessions();
    sessions.value = data.sessions ?? [];
  } catch (error) {
    historyError.value = getApiErrorMessage(error, "加载历史会话失败");
  } finally {
    sessionsLoading.value = false;
  }
}

async function openSession(sessionID: string) {
  if (sending.value) return;
  messagesLoading.value = true;
  historyError.value = "";
  try {
    const data = await mcpApi.sessionMessages(sessionID);
    activeSessionID.value = sessionID;
    bubbles.value = (data.messages ?? []).map((m) => ({
      role: m.role === "user" ? "user" : "assistant",
      content: m.content,
      toolCalls: m.tool_calls ?? [],
    }));
    await scrollToBottom();
  } catch (error) {
    historyError.value = getApiErrorMessage(error, "加载会话内容失败");
  } finally {
    messagesLoading.value = false;
  }
}

function startNewSession() {
  if (sending.value) return;
  stopStream();
  activeSessionID.value = "";
  bubbles.value = [];
  historyError.value = "";
}

async function removeSession(session: McpSession) {
  if (sending.value) return;
  try {
    await mcpApi.deleteSession(session.session_id);
    sessions.value = sessions.value.filter((s) => s.session_id !== session.session_id);
    if (activeSessionID.value === session.session_id) startNewSession();
    toast.success("已删除会话");
  } catch (error) {
    toast.error(getApiErrorMessage(error, "删除会话失败"));
  }
}

function stopStream() {
  if (abortStream) {
    abortStream();
    abortStream = null;
  }
  streamingIndex.value = -1;
  sending.value = false;
}

async function send() {
  const text = input.value.trim();
  if (!text || sending.value) return;

  input.value = "";
  bubbles.value.push({ role: "user", content: text, toolCalls: [] });
  // 先落一个空气泡，流式增量往它里面追加。
  bubbles.value.push({ role: "assistant", content: "", toolCalls: [], pending: true });
  const index = bubbles.value.length - 1;
  streamingIndex.value = index;
  sending.value = true;
  await scrollToBottom();

  // 流结束（正常 done 或异常）由 finishStream 统一收尾，只跑一次。
  let settled = false;
  const settle = () => {
    if (settled) return;
    settled = true;
    finishStream(index);
  };

  abortStream = streamMcpAssistant(
    {
      message: text,
      session_id: activeSessionID.value || undefined,
    },
    (event) => {
      const bubble = bubbles.value[index];
      if (!bubble) return;

      switch (event.type) {
        case "init":
          if (event.session_id) activeSessionID.value = event.session_id;
          break;
        case "delta":
          bubble.content += event.content;
          void scrollToBottom();
          break;
        case "tool_call":
          bubble.toolCalls.push({
            name: event.tool_name,
            arguments: event.tool_args,
          });
          void scrollToBottom();
          break;
        case "tool_result": {
          // 工具结果与最近的同名调用配对；配对不上就单独记一条。
          const owned = [...bubble.toolCalls]
            .reverse()
            .find(
              (c) => c.name === event.tool_name && c.result === undefined && c.error === undefined,
            );
          if (owned) {
            owned.result = event.tool_result;
            owned.error = event.tool_error;
          } else {
            bubble.toolCalls.push({
              name: event.tool_name,
              result: event.tool_result,
              error: event.tool_error,
            });
          }
          void scrollToBottom();
          break;
        }
        case "error":
          bubble.error = event.message;
          break;
        case "done":
          // done 是流正常结束的唯一信号；refresh 参数为 false 表示不再续传。
          if (event.reply && !bubble.content) bubble.content = event.reply;
          settle();
          break;
      }
    },
    (message) => {
      const bubble = bubbles.value[index];
      if (bubble) bubble.error = message;
      settle();
    },
  );
}

function finishStream(index: number) {
  const bubble = bubbles.value[index];
  if (bubble) {
    bubble.pending = false;
    if (bubble.error && !bubble.content) bubble.content = "";
  }
  streamingIndex.value = -1;
  sending.value = false;
  abortStream = null;
  void scrollToBottom();
  // 新会话在首轮结束后才拥有服务端 session_id，拉一次列表把它带出来。
  void loadSessions();
}

/**
 * 后端在 done 事件后并不额外通知，因此这里用「无新事件超时」判定收尾。
 * 只要气泡还在 pending 且没有错误，就认为仍在生成。
 */
async function scrollToBottom() {
  await nextTick();
  const host = scrollHost.value;
  if (host) host.scrollTop = host.scrollHeight;
}

function toolSummary(call: McpToolCall): string {
  if (call.error) return `执行失败：${call.error}`;
  if (call.result) return call.result;
  return "正在执行…";
}

function prettyJSON(raw?: string): string {
  if (!raw) return "";
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

watch(activeSession, () => {
  void scrollToBottom();
});

onBeforeUnmount(() => {
  stopStream();
});

void loadSessions();
</script>

<template>
  <div class="mcp-assistant">
    <aside class="mcp-assistant__side">
      <div class="mcp-assistant__side-head">
        <span class="mcp-assistant__side-title">历史会话</span>
        <AppButton variant="ghost" size="sm" :disabled="sending" @click="startNewSession">
          <SvgIcon name="plus" />
          新对话
        </AppButton>
      </div>

      <AppStateBlock v-if="sessionsLoading" loading message="加载会话中…" min-height="120px" />
      <div v-else-if="historyError" class="mcp-assistant__side-error">{{ historyError }}</div>
      <div v-else-if="!sessions.length" class="mcp-assistant__side-empty">
        还没有历史会话。左侧提问后会自动保存。
      </div>
      <ul v-else class="mcp-assistant__sessions">
        <li
          v-for="s in sessions"
          :key="s.session_id"
          class="mcp-assistant__session"
          :class="{ 'mcp-assistant__session--active': s.session_id === activeSessionID }"
        >
          <button type="button" class="mcp-assistant__session-btn" @click="openSession(s.session_id)">
            <span class="mcp-assistant__session-title">{{ s.title || "未命名会话" }}</span>
            <span class="mcp-assistant__session-meta">
              {{ s.count }} 条 · {{ formatTime(s.updated_at) }}
            </span>
          </button>
          <button
            type="button"
            class="mcp-assistant__session-del"
            title="删除会话"
            :disabled="sending"
            @click="removeSession(s)"
          >
            <SvgIcon name="trash" />
          </button>
        </li>
      </ul>
    </aside>

    <section class="mcp-assistant__main">
      <div ref="scrollHost" class="mcp-assistant__scroll">
        <AppStateBlock v-if="messagesLoading" loading message="加载会话内容…" />
        <div v-else-if="!bubbles.length" class="mcp-assistant__welcome">
          <SvgIcon name="robot" class="mcp-assistant__welcome-icon" />
          <p class="mcp-assistant__welcome-title">我是内置智能助理</p>
          <p class="mcp-assistant__welcome-desc">
            可以用自然语言查询影库、查看缺失剧集、控制订阅与 TG 频道订阅。
          </p>
          <div class="mcp-assistant__examples">
            <button
              v-for="example in [
                '我订阅的剧集最近有更新吗？',
                'Emby 里有哪些剧集缺集？',
                '列出当前所有启用中的订阅',
                '帮我看看下载队列里有什么在跑',
              ]"
              :key="example"
              type="button"
              class="mcp-assistant__example"
              @click="input = example"
            >
              {{ example }}
            </button>
          </div>
        </div>

        <template v-else>
          <div
            v-for="(bubble, index) in bubbles"
            :key="index"
            class="mcp-assistant__row"
            :class="`mcp-assistant__row--${bubble.role}`"
          >
            <div class="mcp-assistant__bubble" :class="`mcp-assistant__bubble--${bubble.role}`">
              <div
                v-if="bubble.role === 'assistant' && bubble.toolCalls.length"
                class="mcp-assistant__tools"
              >
                <details v-for="(call, ci) in bubble.toolCalls" :key="ci" class="mcp-assistant__tool">
                  <summary class="mcp-assistant__tool-head">
                    <AdminStatusPill :tone="call.error ? 'danger' : call.result ? 'success' : 'brand'">
                      {{ call.error ? "失败" : call.result ? "完成" : "调用中" }}
                    </AdminStatusPill>
                    <code class="mcp-assistant__tool-name">{{ call.name }}</code>
                  </summary>
                  <pre v-if="prettyJSON(call.arguments)" class="mcp-assistant__tool-body">{{ prettyJSON(call.arguments) }}</pre>
                  <pre class="mcp-assistant__tool-body">{{ toolSummary(call) }}</pre>
                </details>
              </div>

              <p v-if="bubble.content" class="mcp-assistant__text">{{ bubble.content }}</p>
              <span
                v-else-if="bubble.pending && index === streamingIndex"
                class="mcp-assistant__typing"
              >
                <SvgIcon name="spinner" class="mcp-assistant__spin" />
                正在思考…
              </span>

              <p v-if="bubble.error" class="mcp-assistant__error">{{ bubble.error }}</p>
            </div>
          </div>
        </template>
      </div>

      <div class="mcp-assistant__composer">
        <textarea
          v-model="input"
          rows="2"
          class="mcp-assistant__input"
          placeholder="输入问题，Enter 发送，Shift+Enter 换行"
          :disabled="sending"
          @keydown.enter.exact.prevent="send"
        />
        <AppButton
          v-if="sending"
          variant="secondary"
          size="sm"
          @click="stopStream"
        >
          停止
        </AppButton>
        <AppButton v-else variant="primary" size="sm" :disabled="!canSend" @click="send">
          <SvgIcon name="plane-departure" />
          发送
        </AppButton>
      </div>
    </section>
  </div>
</template>

<style scoped>
.mcp-assistant {
  display: flex;
  gap: 16px;
  min-height: 520px;
}

.mcp-assistant__side {
  display: flex;
  flex-direction: column;
  flex: 0 0 240px;
  gap: 8px;
  padding-right: 12px;
  border-right: 1px solid var(--border);
}

.mcp-assistant__side-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.mcp-assistant__side-title {
  font-size: 13px;
  font-weight: 600;
}

.mcp-assistant__side-empty {
  padding: 12px 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-muted);
}

.mcp-assistant__side-error {
  padding: 12px 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--danger, #e5484d);
}

.mcp-assistant__sessions {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
  overflow-y: auto;
}

.mcp-assistant__session {
  display: flex;
  align-items: center;
  border-radius: 8px;
}

.mcp-assistant__session--active {
  background: var(--surface-sunken);
}

.mcp-assistant__session-btn {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  padding: 8px;
  border: 0;
  background: none;
  color: inherit;
  text-align: left;
  cursor: pointer;
}

.mcp-assistant__session-title {
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mcp-assistant__session-meta {
  font-size: 11px;
  color: var(--text-muted);
}

.mcp-assistant__session-del {
  padding: 6px;
  border: 0;
  background: none;
  color: var(--text-muted);
  cursor: pointer;
}

.mcp-assistant__session-del:hover {
  color: var(--danger, #e5484d);
}

.mcp-assistant__main {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}

.mcp-assistant__scroll {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 14px;
  max-height: 560px;
  overflow-y: auto;
}

.mcp-assistant__welcome {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 48px 16px;
  text-align: center;
}

.mcp-assistant__welcome-icon {
  width: 40px;
  height: 40px;
  color: var(--brand);
}

.mcp-assistant__welcome-title {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}

.mcp-assistant__welcome-desc {
  margin: 0;
  font-size: 13px;
  color: var(--text-muted);
}

.mcp-assistant__examples {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  justify-content: center;
  margin-top: 8px;
}

.mcp-assistant__example {
  padding: 6px 12px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: none;
  color: var(--text-muted);
  font-size: 12px;
  cursor: pointer;
}

.mcp-assistant__example:hover {
  color: var(--text);
  border-color: var(--brand);
}

.mcp-assistant__row {
  display: flex;
}

.mcp-assistant__row--user {
  justify-content: flex-end;
}

.mcp-assistant__bubble {
  max-width: 82%;
  padding: 10px 12px;
  border-radius: 10px;
  font-size: 13px;
  line-height: 1.7;
}

.mcp-assistant__bubble--user {
  background: var(--brand);
  color: #fff;
}

.mcp-assistant__bubble--assistant {
  background: var(--surface-sunken);
}

.mcp-assistant__text {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
}

.mcp-assistant__typing {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--text-muted);
}

.mcp-assistant__spin {
  width: 13px;
  height: 13px;
  animation: mcp-spin 1s linear infinite;
}

@keyframes mcp-spin {
  to {
    transform: rotate(360deg);
  }
}

.mcp-assistant__error {
  margin: 8px 0 0;
  color: var(--danger, #e5484d);
  font-size: 12px;
}

.mcp-assistant__tools {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 8px;
}

.mcp-assistant__tool {
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface);
}

.mcp-assistant__tool-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  cursor: pointer;
  list-style: none;
}

.mcp-assistant__tool-name {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}

.mcp-assistant__tool-body {
  margin: 0;
  padding: 8px;
  border-top: 1px solid var(--border);
  font-family: var(--font-mono, monospace);
  font-size: 11px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 200px;
  overflow: auto;
}

.mcp-assistant__composer {
  display: flex;
  align-items: flex-end;
  gap: 8px;
}

.mcp-assistant__input {
  flex: 1;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface);
  color: var(--text);
  font-family: inherit;
  font-size: 13px;
  resize: vertical;
}
</style>
