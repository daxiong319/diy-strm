<script setup lang="ts">
/**
 * 初始化向导（T13 · F-4）。
 *
 * 只问四个问题，且**每一步都能跳过**：这个产品是给已经在用的人装的
 * （第二个账号、第五个任务、换了个网盘），一上来强制走完向导
 * 只会变成关不掉的弹窗。所以：
 *   - 只在「从没走过」时自动弹一次，之后记在 localStorage；
 *   - 每一步都有「跳过」，跳过的项不写任何设置；
 *   - 「完成」这一步才真的写设置，且写完再标完成 ——
 *     中途失败就还会再问一次，而不是记成已完成然后什么都不做。
 */
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import AppModal from "@/components/base/AppModal.vue";
import { clearLayoutPrefs } from "@/composables/layoutPrefs";

const emit = defineEmits<{ done: [] }>();

const KEY_WIZARD = "litepan-admin-setup-wizard-done";

function alreadyDone(): boolean {
  try {
    return localStorage.getItem(KEY_WIZARD) === "1";
  } catch {
    // 隐私模式下 localStorage 会抛错：视为「还没走」，
    // 多弹一次比再也不弹好。
    return false;
  }
}

function markDone(): void {
  try {
    localStorage.setItem(KEY_WIZARD, "1");
  } catch {
    /* 存不下就每次都问；不能因为存不下就让流程报错 */
  }
}

const open = ref(false);
const step = ref(0);
const saving = ref(false);
const errorMsg = ref("");
const router = useRouter();

/** 令牌/通知/缓存三类；每类只是一个「要不要现在去配」的入口。 */
const STEPS = [
  {
    key: "strm_token",
    title: "设置播放令牌",
    body: "不设置的话，任何拿到链接的人都能直接播放你的媒体。建议先去「其他设置」填一个。",
    page: "settings",
    tab: "services",
  },
  {
    key: "emby_webhook_enabled",
    title: "接上 Emby 通知",
    body: "Emby 入库与删除事件会推成站内通知。接之前先想清楚要不要顺带删网盘文件。",
    page: "settings",
    tab: "services",
  },
  {
    key: "cache_enabled",
    title: "确认元数据缓存",
    body: "缓存关着时每次列目录都直连网盘，很容易被限流。一般保持开启。",
    page: "dashboard",
    tab: "",
  },
] as const;

const current = computed(() => STEPS[step.value]);
const last = computed(() => step.value === STEPS.length - 1);

onMounted(() => {
  if (!alreadyDone()) open.value = true;
});

function go(target: number): void {
  step.value = Math.max(0, Math.min(STEPS.length - 1, target));
}

function skip(): void {
  if (last.value) {
    markDone();
    open.value = false;
    return;
  }
  go(step.value + 1);
}

function finish(): void {
  saving.value = true;
  errorMsg.value = "";
  try {
    // 这里刻意**不写任何设置**：当前形态里用户能改的只是
    // 「这一步要不要现在去看」，没有需要代填的值 ——
    // 与其发明一批默认值替他决定，不如什么都不写。
    // 也不发 PUT /admin/settings：空批次过去只会让后端跑一遍
    // 全量副作用（刷新调度、清缓存、代理同步），换来零个设置变化。
    markDone();
    open.value = false;
    emit("done");
  } finally {
    saving.value = false;
  }
}

async function jumpToCurrent(): Promise<void> {
  const s = current.value;
  markDone();
  open.value = false;
  await router.push({
    path: "/admin",
    query: { page: s.page, ...(s.tab ? { tab: s.tab } : {}), field: s.key },
  });
}

function resetLayout(): void {
  clearLayoutPrefs();
}
</script>

<template>
  <AppModal :open="open" title="快速上手" size="sm" bare @close="skip">
    <div class="wz">
      <ol class="wz__dots" aria-label="步骤">
        <li v-for="(s, i) in STEPS" :key="s.key" :class="{ 'wz__dot--on': i === step }">
          <button type="button" class="wz__dot" :aria-label="`第 ${i + 1} 步：${s.title}`" @click="go(i)">
            {{ i + 1 }}
          </button>
        </li>
      </ol>

      <h3 class="wz__title">{{ current.title }}</h3>
      <p class="wz__body">{{ current.body }}</p>

      <p v-if="errorMsg" class="wz__err">{{ errorMsg }}</p>

      <footer class="wz__foot">
        <button type="button" class="wz__btn wz__btn--ghost" @click="resetLayout">菜单恢复默认</button>
        <span class="wz__spacer" />
        <button type="button" class="wz__btn" @click="skip">{{ last ? "稍后再说" : "跳过这一步" }}</button>
        <button type="button" class="wz__btn wz__btn--primary" @click="jumpToCurrent">去设置</button>
        <button v-if="last" type="button" class="wz__btn wz__btn--primary" :disabled="saving" @click="finish">
          {{ saving ? "保存中…" : "完成" }}
        </button>
      </footer>
    </div>
  </AppModal>
</template>

<style scoped>
.wz {
  padding: 18px 20px 16px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.wz__dots {
  display: flex;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.wz__dot {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  border: 1px solid var(--border-soft, rgba(15, 23, 42, 0.12));
  background: transparent;
  color: var(--text-muted, #64748b);
  font-size: 12px;
  cursor: pointer;
}

.wz__dot--on .wz__dot {
  border-color: var(--brand, #3b82f6);
  color: var(--brand, #3b82f6);
}

.wz__title {
  margin: 0;
  font-size: 15px;
}

.wz__body {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
  color: var(--text-regular, #334155);
}

.wz__err {
  margin: 0;
  font-size: 12px;
  color: var(--danger, #b3261e);
}

.wz__foot {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
}

.wz__spacer {
  flex: 1;
}

.wz__btn {
  padding: 5px 12px;
  border: 1px solid var(--border-soft, rgba(15, 23, 42, 0.12));
  border-radius: var(--radius-pill, 999px);
  background: transparent;
  color: inherit;
  font-size: 12px;
  cursor: pointer;
}

.wz__btn--ghost {
  border-color: transparent;
  color: var(--text-muted, #64748b);
}

.wz__btn--primary {
  border-color: var(--brand, #3b82f6);
  color: var(--brand, #3b82f6);
}

.wz__btn:disabled {
  opacity: 0.6;
  cursor: default;
}
</style>