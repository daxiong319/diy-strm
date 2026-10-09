<script setup lang="ts">
/**
 * 目录配置防呆提示（T32）。
 *
 * 受控组件：只吃一个 `path`，提示文案全部来自后端
 * （`GET /admin/dir-refs`，文案在 cloudref 里算好 —— 见 internal/cloudref
 * 的包注释，前端自己拼等于把判断复制一份，两端迟早分叉）。
 *
 * 为什么是一个独立组件而不是在 MediaUpgradePage 里写一个函数：
 * 三处挂载点（库根 / 候选目录 / 移动目标）都会重复「防抖 + 取消在途请求 +
 * 渲染多层提示」这三件事，抄三遍就要三份防抖计时器，其中一份写错了症状是
 * 「编辑时发一串请求把后端打爆」，而这种错在开发机上根本看不出来。
 *
 * 组件内有一条硬规则：**没有配置时不渲染任何东西**。`configured === false`
 * 意味着后端一个媒体库根都没读到，此时任何「不在媒体库内」都是凭空捏造。
 */
import { onBeforeUnmount, ref, watch } from "vue";
import { getApiErrorMessage } from "@/api/client";
import { fetchDirReferences, type DirRefResult } from "@/api/dirRefs";
import AppBadge from "@/components/base/AppBadge.vue";

const props = withDefaults(
  defineProps<{
    /** 当前输入框里的原始路径（不归一，原样传给后端）。 */
    path: string;
    /** 输入框标签，用于「凭什么这么判」里说清是哪个字段。 */
    field?: string;
  }>(),
  { field: "这个目录" },
);

/** 输入停顿多久才发请求。跟 AppInput 的用法一致：300ms 足够把一次连续输入合成一次。 */
const DEBOUNCE_MS = 300;

/** 响应序号：只有最新一次请求的结果会被采纳。 */
let seq = 0;
let timer: number | undefined;
let inFlight = false;

const result = ref<DirRefResult | null>(null);
const errMsg = ref("");
const showWhy = ref(false);

/** 提示只在有配置时才存在 —— 没配过就说「不在」，用户会去找一个不存在的问题。 */
function current() {
  if (errMsg.value) return null;
  const r = result.value;
  if (!r || !r.configured) return null;
  return r;
}

function hints() {
  return current()?.references.Hints ?? [];
}

function stopTimer() {
  if (timer !== undefined) {
    window.clearTimeout(timer);
    timer = undefined;
  }
}

function load(raw: string) {
  const dir = raw.trim();
  if (dir === "") {
    // 空路径：撤掉所有提示，也不发请求。留着一个基于旧路径的结果
    // 等于「用户已经清空了输入框，界面还在按上一个路径警告」。
    result.value = null;
    errMsg.value = "";
    showWhy.value = false;
    return;
  }
  if (inFlight) return; // 上一次还没回来就先不排队，避免连删带打变成串行瀑布
  const mine = ++seq;
  inFlight = true;
  fetchDirReferences(dir)
    .then((data) => {
      if (mine !== seq) return; // 有更新的请求了，这份结果作废
      result.value = data;
      errMsg.value = "";
    })
    .catch((e: unknown) => {
      if (mine !== seq) return;
      // 拿不到判据时**不显示任何结论**：显示成「不在媒体库内」等于把
      // 网络故障说成配置错误，用户会去改一个没错的配置。
      result.value = null;
      errMsg.value = getApiErrorMessage(e, "读取目录归属失败，暂不判断");
    })
    .finally(() => {
      if (mine === seq) inFlight = false;
    });
}

watch(
  () => props.path,
  (val) => {
    stopTimer();
    // 清空错误态：上一条「读取失败」不该跟着新的输入一直挂着。
    errMsg.value = "";
    timer = window.setTimeout(() => {
      timer = undefined;
      load(val ?? "");
    }, DEBOUNCE_MS);
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  stopTimer();
  seq += 1; // 让在途请求的 then 回调知道自己作废了
});

/** 配色：ok = 成功绿，warn = 警告琥珀，和 AppBadge 的 tone 对齐。 */
function badgeTone(tone: string) {
  return tone === "warn" ? "warning" : "success";
}
</script>

<template>
  <div v-if="errMsg" class="drh__error">{{ errMsg }}</div>

  <div v-else-if="hints().length" class="drh">
    <div class="drh__list">
      <span v-for="(h, i) in hints()" :key="i" class="drh__line">
        <AppBadge :tone="badgeTone(h.tone)" dot>{{ h.text }}</AppBadge>
      </span>
    </div>

    <!-- 凭什么这么判：提示是一句断言，不给依据的话用户只能照着改、
         改完还是不对就只能放弃这个功能。把判据摊开才排得动。 -->
    <button type="button" class="drh__why" @click="showWhy = !showWhy">
      {{ showWhy ? "收起判断依据" : "凭什么这么判？" }}
    </button>
    <div v-if="showWhy" class="drh__why-body">
      <div v-if="current()?.refs.library_roots?.length" class="drh__why-row">
        <span class="drh__why-key">媒体库根</span>
        <span class="drh__why-val">{{ current()?.refs.library_roots?.join("、") }}</span>
      </div>
      <div v-if="current()?.refs.monitor_sources?.length" class="drh__why-row">
        <span class="drh__why-key">自动整理源目录</span>
        <span class="drh__why-val">{{ current()?.refs.monitor_sources?.join("、") }}</span>
      </div>
      <p v-for="(n, i) in current()?.refs.notes ?? []" :key="`note-${i}`" class="drh__note">{{ n }}</p>
      <p class="drh__note drh__note--target">
        本次判定的是 <code>{{ current()?.path }}</code>（{{ props.field }}）。
      </p>
    </div>
  </div>
</template>

<style scoped>
.drh {
  margin-top: 6px;
}

.drh__list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  align-items: flex-start;
}

.drh__line {
  display: inline-flex;
  max-width: 100%;
}

/* 徽章里的长文案要能换行：路径类提示经常超过一行，
   不换行会被父容器裁掉尾部，而尾部往往正是用户要核对的那半句。 */
.drh__line :deep(.app-badge) {
  white-space: normal;
  height: auto;
  min-height: 22px;
  padding: 3px 10px;
  line-height: 1.5;
  font-weight: 500;
}

.drh__why {
  margin-top: 6px;
  padding: 0;
  border: 0;
  background: none;
  color: var(--text-muted);
  font-size: 12px;
  line-height: 1.6;
  text-decoration: underline;
  cursor: pointer;
}

.drh__why:hover {
  color: var(--text-regular);
}

.drh__why-body {
  margin-top: 6px;
  padding: 8px 10px;
  border: 1px solid var(--border-soft);
  border-radius: var(--radius-sm);
  background: var(--surface-sunken);
  font-size: 12px;
  line-height: 1.6;
}

.drh__why-row {
  display: flex;
  gap: 8px;
  margin-bottom: 4px;
}

.drh__why-key {
  flex: none;
  color: var(--text-muted);
}

.drh__why-val {
  word-break: break-all;
}

.drh__note {
  margin: 4px 0 0;
  color: var(--text-muted);
}

.drh__note--target {
  color: var(--text-regular);
  word-break: break-all;
}

.drh__error {
  margin-top: 6px;
  color: var(--danger);
  font-size: 12px;
  line-height: 1.6;
}
</style>