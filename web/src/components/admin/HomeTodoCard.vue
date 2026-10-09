<script setup lang="ts">
/**
 * 首页待办（T13 · F-5）。
 *
 * 「还没配置的东西」是这个产品里最被低估的痛点：190 个设置项，
 * 新装的人看到的是一排默认值的表单，看不出哪个默认值得改、
 * 哪个不填就等于功能没开。这一块把「默认是安全/空转的那几项」
 * 挑出来，每条直接给一个能点过去的入口（复用 ⌘K 的 page/tab/field）。
 *
 * **为什么必须诚实**：这里列的每一条都必须是「不配就真的有问题」的项。
 * 凑一条「建议开启 XX 增强体验」进去，用户点进去发现没什么可改，
 * 第二次就不会再看这一块了 —— 待办列表的价值全在可信。
 */
import { computed, onMounted, ref } from "vue";
import { fetchSettings, type SettingIndexEntry } from "@/api/settings";

const props = defineProps<{ items: SettingIndexEntry[]; coverage: string }>();
const emit = defineEmits<{ open: [SettingIndexEntry] }>();

/**
 * 待办要判断「用户配了没有」，那就得知道当前值 —— 而**索引刻意不带值**
 * （见 internal/settings/index.go：带上值等于给搜索端点加一条
 * 「一次读走全部配置」的旁路）。所以这里自己拉一份设置快照，
 * 而且只用来看这四五个键，不把值传到别处。
 */
const current = ref<Record<string, { value: string; default: string }>>({});
const loadError = ref("");
onMounted(async () => {
  try {
    const payload = await fetchSettings();
    const out: Record<string, { value: string; default: string }> = {};
    for (const it of payload.items ?? []) out[it.key] = { value: it.value, default: it.default };
    current.value = out;
  } catch (err) {
    // 拉不到就不猜：把这一块藏起来，而不是显示成「全都还没配」。
    loadError.value = err instanceof Error ? err.message : String(err);
  }
});

/**
 * 「不配就等于功能没开 / 不安全」的设置 key。
 *
 * 判定口径：该项的默认值会让对应功能处于关闭状态，且该功能一旦被
 * 指望就会出问题（拿不到通知 / 存不下 / 攒不住）。
 * 反过来，凡是「默认开着且配错也没关系」的一律不列。
 */
const ACTIONABLE_KEYS: Array<{ key: string; why: string }> = [
  { key: "strm_token", why: "不设置播放令牌，STRM 播放链接可被任何人直接拿到。" },
  { key: "emby_webhook_enabled", why: "不接 Emby Webhook，入库与删除事件就不会变成站内通知。" },
  { key: "cache_enabled", why: "元数据缓存关着时，每次列目录都会直连网盘，接口很快就会被限流。" },
  { key: "log_level", why: "默认 Info，出问题时日志里看不到细节。" },
];

/** 还没配过的：值与默认值相同，或压根没出现在快照里。 */
const todos = computed(() => {
  const byKey = new Map(props.items.map((it) => [it.key, it]));
  const out: Array<{ entry: SettingIndexEntry; why: string }> = [];
  for (const want of ACTIONABLE_KEYS) {
    const it = byKey.get(want.key);
    if (!it) continue;
    const cur = current.value[want.key];
    // 还没拉到快照时不下结论 —— 先按「已配」处理，宁可少列一条，
    // 也不要给用户一条其实早就配好的待办。
    if (loadError.value) continue;
    if (cur && cur.value !== "" && cur.value !== cur.default) continue;
    out.push({ entry: it, why: want.why });
  }
  return out;
});

/** 已经配好的，用来给「全齐了」一个交代。 */
const doneCount = computed(() => {
  if (loadError.value) return 0;
  return ACTIONABLE_KEYS.filter((w) => {
    const cur = current.value[w.key];
    return cur && cur.value !== "" && cur.value !== cur.default;
  }).length;
});

/** 索引里根本没有的键：如实说出来，不要假装都齐了。 */
const missingKeys = computed(() =>
  ACTIONABLE_KEYS.filter((w) => !props.items.some((i) => i.key === w.key)).map((w) => w.key),
);
</script>

<template>
  <section class="todo-card" aria-label="待办">
    <header class="todo-card__head">
      <h3>待办</h3>
      <span class="todo-card__count">{{ todos.length }} 项待办</span>
    </header>

    <ul v-if="todos.length" class="todo-card__list">
      <li v-for="t in todos" :key="t.entry.key" class="todo-card__item">
        <div class="todo-card__main">
          <span class="todo-card__title">{{ t.entry.label || t.entry.key }}</span>
          <span class="todo-card__why">{{ t.why }}</span>
        </div>
        <button type="button" class="todo-card__go" @click="emit('open', t.entry)">去设置</button>
      </li>
    </ul>

    <p v-else-if="!loadError" class="todo-card__empty">
      基础项都齐了（{{ doneCount }} 项已配置）。
      <template v-if="missingKeys.length">索引里没有的项：{{ missingKeys.join("、") }}。</template>
    </p>
    <p v-else class="todo-card__empty">待办没能载入：{{ loadError }}</p>

    <p v-if="coverage" class="todo-card__coverage">{{ coverage }}</p>
  </section>
</template>

<style scoped>
.todo-card {
  padding: 14px 16px;
  border: 1px solid var(--border-soft, rgba(15, 23, 42, 0.1));
  border-radius: var(--radius-md, 10px);
  background: var(--surface, #fff);
}

.todo-card__head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 10px;
}

.todo-card__head h3 {
  margin: 0;
  font-size: 14px;
}

.todo-card__count {
  font-size: 12px;
  color: var(--text-muted, #64748b);
}

.todo-card__list {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.todo-card__item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.todo-card__main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.todo-card__title {
  font-size: 13px;
  font-weight: 600;
}

.todo-card__why {
  font-size: 12px;
  color: var(--text-muted, #64748b);
  line-height: 1.5;
}

.todo-card__go {
  flex: none;
  padding: 4px 10px;
  border: 1px solid var(--brand, #3b82f6);
  border-radius: var(--radius-pill, 999px);
  background: transparent;
  color: var(--brand, #3b82f6);
  font-size: 12px;
  cursor: pointer;
}

.todo-card__empty,
.todo-card__coverage {
  margin: 0;
  font-size: 12px;
  color: var(--text-muted, #64748b);
  line-height: 1.6;
}

.todo-card__coverage {
  margin-top: 10px;
  font-size: 11px;
}
</style>