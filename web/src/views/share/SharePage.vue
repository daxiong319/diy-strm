<script setup lang="ts">
// 免登录分享页（访客侧）。
//
// 三条硬要求，决定了这个页面为什么长这样：
//
// 1. **不进管理台那套壳**。访客没有会话，AdminView 会把他踢去 /login，
//    所以这是一个独立的顶层路由，样式自己写（不引 admin 的皮肤）。
//
// 2. **令牌只在 X-Share-Token 头里**（验收③）。于是有两个直接后果：
//      a. 令牌绝不能出现在 URL 里 —— 所以播放地址是干净的，
//         令牌靠 <video> 的请求头带过去；
//      b. **原生 `<video src=...>` 带不了自定义请求头**，
//         所以只能 fetch 成 blob 再喂给播放器。这就是下面那段
//         blob 取流代码存在的唯一理由，不是为了「显得高级」。
//
// 3. **访客看不到任何内部字段**（账号、网盘文件 ID、别的访客的统计）。
//    后端 libraryShareVisitorDTO 已经裁干净了，前端也不去猜。

import { computed, onMounted, ref, watch } from "vue";
import { useRoute } from "vue-router";
import {
  clearShareToken,
  ensureShareVisitorID,
  readShareToken,
  shareIssueToken,
  shareReportEvent,
  shareStreamHeaders,
  shareStreamURL,
  ShareGuestError,
  type ShareTokenResult,
  type ShareVisitorInfo,
} from "@/api/libraryShare";

const route = useRoute();
const code = computed(() => String(route.params.code ?? ""));

type Phase = "loading" | "password" | "ready" | "error";

const phase = ref<Phase>("loading");
const errorText = ref("");
const password = ref("");
const share = ref<ShareVisitorInfo | null>(null);
const needsPassword = ref(false);

/** 一次性 URL：fetch 成 blob 后 revoke，否则长视频会一直占着内存。 */
const streamURL = ref("");
const streaming = ref(false);
const streamError = ref("");

/**
 * 「第 N 季」只在剧集且季号大于 0 时出现。
 *
 * 放在 computed 里而不是模板里写 `share?.season > 0`：
 * 那种写法在 TS 看来每次访问 share 都可能为 null，vue-tsc 会报
 * 「possibly undefined / possibly null」—— 而这两个警告是在提醒我
 * 同一个风险出现两次，却都指向同一个根因（季号判断和取值写在两处）。
 * 算一次既消掉警告，也消掉「判断了 -1 却仍显示 -1」的可能。
 */
const seasonText = computed(() => {
  const s = share.value;
  if (!s || !s.season || s.season <= 0) return "";
  return `第 ${s.season} 季`;
});

const expiresText = computed(() => {
  if (!share.value) return "";
  const d = share.value.expires_in_days;
  if (d <= 0) return "";
  if (d <= 1) return "今天内有效";
  return `${d} 天后失效`;
});

function fail(text: string) {
  errorText.value = text;
  phase.value = "error";
}

/**
 * 401 的处理比「显示一句报错」更重要：令牌可能是 24 小时前发的，
 * 此时**换一枚**就恢复了，而不是让访客去问发链接的人要新链接。
 */
function onUnauthorized(err: ShareGuestError) {
  clearShareToken();
  if (needsPassword.value) {
    errorText.value = err.message;
    phase.value = "password";
    return;
  }
  // 无口令的分享：清掉旧令牌再换一次。
  void retrySilently();
}

let retrying = false;

async function retrySilently() {
  if (retrying) return;
  retrying = true;
  try {
    await issue();
  } catch (e) {
    fail(e instanceof ShareGuestError ? e.message : "分享链接无法打开");
  } finally {
    retrying = false;
  }
}

/** 换令牌 + 拉流。 */
async function issue() {
  errorText.value = "";
  const res: ShareTokenResult = await shareIssueToken(code.value, password.value);
  share.value = res.share;
  needsPassword.value = res.share.has_password;
  if (res.share.has_password && !res.token && !readShareToken()) {
    // 有口令但没拿到令牌：还卡在口令框。
    phase.value = "password";
    return;
  }
  phase.value = "ready";
  await startStream();
}

/**
 * 拉流：fetch 带 X-Share-Token，拿 blob 交给 <video>。
 *
 * 为什么不用 <video src>：原生标签发不出自定义请求头，令牌就等于没带，
 * 后端一律 401。这也是「令牌走 header 不走 query」这条决策的**直接代价** ——
 * 代价是没法用 src 的原生流式与 Range，改成一次性 blob。
 *
 * 代价换来的好处：这个页面拿到的是一段完全可控的字节，
 * 令牌从来没有出现在任何一处会被记录的地方。
 */
async function startStream() {
  streamError.value = "";
  streaming.value = true;
  try {
    const resp = await fetch(shareStreamURL(code.value), {
      headers: shareStreamHeaders(),
      credentials: "omit",
    });
    if (resp.status === 401) {
      const err = new ShareGuestError("分享链接需要重新打开", 401, "AUTH_EXPIRED");
      onUnauthorized(err);
      return;
    }
    if (!resp.ok) {
      let msg = `取流失败 (${resp.status})`;
      try {
        const payload = await resp.json();
        if (payload?.message) msg = payload.message;
      } catch {
        /* 非 JSON 就用默认文案 */
      }
      streamError.value = msg;
      return;
    }
    const blob = await resp.blob();
    if (streamURL.value) URL.revokeObjectURL(streamURL.value);
    streamURL.value = URL.createObjectURL(blob);
    void shareReportEvent(code.value, "play");
  } catch {
    streamError.value = "网络请求失败，请检查网络后重试";
  } finally {
    streaming.value = false;
  }
}

function submitPassword() {
  if (!password.value) {
    fail("请输入访问口令");
    return;
  }
  phase.value = "loading";
  void issue();
}

onMounted(open);

/**
 * 换链接（访客在同一标签页里从 A 点进 B）要整体重来。
 *
 * 刻意不在 onMounted 里一次性写死：分享页常见于「群里连发两条链接」，
 * 复用同一个路由组件换 code 时 Vue 不会重建组件，
 * 于是第二条链接会拿着第一条的令牌去播 —— 症状是「点开新链接，播的还是上一个」。
 */
watch(code, (next, prev) => {
  if (!prev || next === prev) return;
  reset();
  void open();
});

function reset() {
  share.value = null;
  needsPassword.value = false;
  password.value = "";
  errorText.value = "";
  streamError.value = "";
  if (streamURL.value) {
    URL.revokeObjectURL(streamURL.value);
    streamURL.value = "";
  }
}

/** 打开一份分享：记 open 事件，然后换令牌。 */
async function open() {
  ensureShareVisitorID();
  phase.value = "loading";
  // 打开页面本身就是一个「打开」事件（计 view）。
  void shareReportEvent(code.value, "open");
  try {
    await issue();
  } catch (e) {
    if (e instanceof ShareGuestError) {
      if (e.status === 400 && !needsPassword.value) {
        // 无口令分享却要口令 —— 说明后端认为这条分享有口令。
        needsPassword.value = true;
      }
      if (needsPassword.value || e.status === 400) {
        errorText.value = e.message;
        phase.value = "password";
        return;
      }
      fail(e.message);
      return;
    }
    fail("分享链接无法打开");
  }
}
</script>

<template>
  <div class="share-page">
    <header class="share-head">
      <h1 v-if="share?.title">{{ share.title }}</h1>
      <h1 v-else>正在打开分享…</h1>
      <p v-if="seasonText" class="season">{{ seasonText }}</p>
      <p v-if="share?.comment" class="comment">{{ share.comment }}</p>
      <p v-if="expiresText" class="expires">{{ expiresText }}</p>
    </header>

    <!-- 口令 -->
    <section v-if="phase === 'password'" class="panel">
      <p class="hint">这个分享需要访问口令</p>
      <p v-if="errorText" class="error">{{ errorText }}</p>
      <input
        v-model="password"
        type="password"
        inputmode="text"
        autocomplete="off"
        placeholder="访问口令"
        @keyup.enter="submitPassword"
      />
      <button type="button" @click="submitPassword">打开</button>
    </section>

    <!-- 出错 -->
    <section v-else-if="phase === 'error'" class="panel">
      <p class="error">{{ errorText }}</p>
      <button type="button" @click="retrySilently">重试</button>
    </section>

    <!-- 加载中 -->
    <section v-else-if="phase === 'loading'" class="panel">
      <p class="hint">正在准备播放…</p>
    </section>

    <!-- 播放器 -->
    <section v-else class="player-wrap">
      <p v-if="streamError" class="error">{{ streamError }}</p>
      <p v-else-if="streaming" class="hint">正在拉取视频…</p>
      <!--
        视频本体没有 controls 时部分移动端浏览器不显示任何提示，
        所以保留 controls：访客要能自己暂停、拖进度、全屏。
      -->
      <video
        v-if="streamURL"
        :src="streamURL"
        controls
        playsinline
        preload="metadata"
        @error="streamError = '视频加载失败'"
      />
    </section>

    <footer class="share-foot">
      <p class="muted">通过 diy-strm 分享</p>
    </footer>
  </div>
</template>

<style scoped>
/*
 * 移动端优先：不引 admin 皮肤，页面只有一列、上下留白给手机浏览器。
 * 字号用 rem、容器用 dvh，避开 iOS Safari 的地址栏高度问题。
 */
.share-page {
  min-height: 100dvh;
  display: flex;
  flex-direction: column;
  gap: 1rem;
  padding: 1.25rem 1rem 2rem;
  background: #0b0d10;
  color: #e8eaed;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC",
    "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
}

.share-head h1 {
  margin: 0;
  font-size: 1.25rem;
  line-height: 1.4;
  word-break: break-word;
}

.share-head .season,
.share-head .comment,
.share-head .expires {
  margin: 0.35rem 0 0;
  font-size: 0.875rem;
  color: #9aa0a6;
  word-break: break-word;
}

.panel {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  padding: 1rem;
  border-radius: 0.75rem;
  background: #171a1f;
}

.panel input {
  width: 100%;
  padding: 0.7rem 0.85rem;
  border: 1px solid #2b3038;
  border-radius: 0.5rem;
  background: #0f1115;
  color: inherit;
  font-size: 1rem;
}

.panel button {
  padding: 0.7rem 1rem;
  border: 0;
  border-radius: 0.5rem;
  background: #2f6feb;
  color: #fff;
  font-size: 1rem;
  cursor: pointer;
}

.hint {
  margin: 0;
  color: #9aa0a6;
  font-size: 0.9375rem;
}

.error {
  margin: 0;
  color: #ff8a80;
  font-size: 0.9375rem;
  word-break: break-word;
}

.player-wrap {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}

.player-wrap video {
  width: 100%;
  background: #000;
  border-radius: 0.5rem;
}

.share-foot {
  margin-top: auto;
  text-align: center;
}

.share-foot .muted {
  margin: 0;
  font-size: 0.75rem;
  color: #5f6368;
}
</style>