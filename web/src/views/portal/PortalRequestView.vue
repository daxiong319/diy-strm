<script setup lang="ts">
// 求片站主界面：搜索 → 提交 → 看自己的单。
//
// 三段式（搜索 / 我的 / 我的统计）而不是一个长页面：手机竖屏下
// 「我想求个新的」和「上次那个怎么样了」是两个完全不同的心情，
// 混在一屏里会让人以为还没提交。
//
// 全程遵守一条纪律：**不在前端算规则**。
//   - 每日还剩几次：直接显示服务端回的 used_today / daily_limit；
//   - 要不要审核：直接显示 require_review；
//   - 标签能不能存：submit 时后端会拒（400），页面只负责把话转出来。
// 任何「前端先拦一道」的做法都会在配置改了之后变成第二套真相。
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  portalApi,
  portalCan,
  type PortalMe,
  type PortalRequestItem,
  type PortalSearchItem,
  type PortalStatsRow,
} from "@/api/request";

const props = defineProps<{ me: PortalMe }>();
const emit = defineEmits<{ logout: []; meChange: [PortalMe] }>();

type Tab = "search" | "mine" | "stats";
const tab = ref<Tab>("search");

const TABS: { key: Tab; label: string }[] = [
  { key: "search", label: "求片" },
  { key: "mine", label: "我的" },
  { key: "stats", label: "统计" },
];

// ---------------------------------------------------------------- 搜索

const query = ref("");
const mediaType = ref<"movie" | "tv">("tv");
const page = ref(1);
const totalPages = ref(0);
const searching = ref(false);
const searchError = ref("");
const results = ref<PortalSearchItem[]>([]);

/**
 * 能不能真的提交。搜索不需要这个权限 —— 让没有提交权的人也能翻一翻，
 * 顺便看到「我缺这个权限」，比登录进来一片空白强。
 * 服务端 submit 也会独立拒（403/400），这里只决定按钮要不要灰。
 */
const canSubmit = computed(() => portalCan(props.me, "request.submit"));

async function runSearch(nextPage = 1) {
  const q = query.value.trim();
  if (!q) {
    results.value = [];
    searchError.value = "";
    return;
  }
  searching.value = true;
  searchError.value = "";
  try {
    const res = await portalApi.search(q, mediaType.value, nextPage);
    results.value = res.items ?? [];
    page.value = res.page || nextPage;
    totalPages.value = res.total_pages || 0;
  } catch (e) {
    searchError.value = getApiErrorMessage(e, "搜索失败");
    results.value = [];
  } finally {
    searching.value = false;
  }
}

// ---------------------------------------------------------------- 提交

/** 每条搜索结果各自的表单（按 media_type-tmdb_id 存，避免互相串台）。 */
const askSeason = ref<Record<string, number>>({});
const askTags = ref<Record<string, string>>({});
const askNotes = ref<Record<string, string>>({});

function draftKey(item: PortalSearchItem): string {
  return `${item.media_type}-${item.tmdb_id}`;
}

function buildDraft(item: PortalSearchItem): SubmitDraft {
  const key = draftKey(item);
  return {
    item,
    season: item.media_type === "movie" ? 0 : askSeason.value[key] ?? 0,
    notes: askNotes.value[key] ?? "",
    tagsText: askTags.value[key] ?? "",
  };
}

interface SubmitDraft {
  item: PortalSearchItem;
  season: number;
  notes: string;
  tagsText: string;
}

const submittingFor = ref(0);
const submitError = ref("");
const submitNotice = ref("");

const remainingToday = computed(() => {
  if (props.me.daily_limit < 0) return null;
  return Math.max(0, props.me.daily_limit - props.me.used_today);
});

/** 标签按中文顿号 / 英文逗号 / 分号都切 —— 家人在手机上更可能随手打逗号。 */
function parseTagLine(line: string): string[] {
  return line
    .split(/[,，、;；]/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

async function submitRequest(draft: SubmitDraft) {
  submitError.value = "";
  submitNotice.value = "";
  submittingFor.value = draft.item.tmdb_id ?? 0;
  try {
    const res = await portalApi.submit({
      tmdb_id: draft.item.tmdb_id ?? 0,
      title: draft.item.title,
      media_type: draft.item.media_type === "movie" ? "movie" : "tv",
      season: draft.season,
      notes: draft.notes.trim(),
      tags: parseTagLine(draft.tagsText),
    });
    // 三态反馈（直接生效 / 待审核 / 生效了但有隐患）由后端给，
    // 这里只负责把它整句显示出来 —— 尤其是 warning，
    // 免审但订阅没建起来时如果不说，家人会以为已经在盯着了。
    submitNotice.value = res.warning ? `${res.message}（${res.warning}）` : res.message;
    await refreshMe();
    await loadMine();
  } catch (e) {
    submitError.value = getApiErrorMessage(e, "提交失败");
  } finally {
    submittingFor.value = 0;
  }
}

// ---------------------------------------------------------------- 我的

const mine = ref<PortalRequestItem[]>([]);
const mineLoading = ref(false);

async function loadMine() {
  mineLoading.value = true;
  try {
    const res = await portalApi.mine();
    mine.value = res.items ?? [];
  } catch (e) {
    submitError.value = getApiErrorMessage(e, "加载我的求片失败");
  } finally {
    mineLoading.value = false;
  }
}

const tagDrafts = ref<Record<string, string>>({});

async function saveTags(item: PortalRequestItem) {
  submitError.value = "";
  submitNotice.value = "";
  const text = tagDrafts.value[`${item.tmdb_id}|${item.media_type}`] ?? item.tags.join("、");
  try {
    const res = await portalApi.saveTags({
      tmdb_id: item.tmdb_id,
      media_type: item.media_type,
      tags: parseTagLine(text),
    });
    // 替换语义：以后端真存下的那份为准。
    // 前端「乐观更新」在这里会骗人 —— 超限时后端会截断/拒绝，
    // 而页面显示的那份并不在库里。
    item.tags = res.tags ?? [];
    tagDrafts.value[`${item.tmdb_id}|${item.media_type}`] = item.tags.join("、");
    submitNotice.value = "标签已保存";
  } catch (e) {
    submitError.value = getApiErrorMessage(e, "保存标签失败");
  }
}

// ---------------------------------------------------------------- 统计

const stats = ref<PortalStatsRow[]>([]);
const statsLoading = ref(false);

const statsRows = computed(() => {
  // 后端已按本人过滤，这里只做「按天合并」，不再拼全家的视图。
  const map = new Map<string, PortalStatsRow>();
  for (const row of stats.value) {
    const cur = map.get(row.day);
    if (!cur) {
      map.set(row.day, { ...row });
      continue;
    }
    cur.submitted_count += row.submitted_count;
    cur.approved_count += row.approved_count;
    cur.rejected_count += row.rejected_count;
    cur.fulfilled_count += row.fulfilled_count;
  }
  return [...map.values()].sort((a, b) => (a.day < b.day ? 1 : -1));
});

async function loadStats() {
  statsLoading.value = true;
  try {
    const res = await portalApi.stats(30);
    stats.value = res.items ?? [];
  } catch (e) {
    submitError.value = getApiErrorMessage(e, "加载统计失败");
  } finally {
    statsLoading.value = false;
  }
}

async function switchTab(next: Tab) {
  tab.value = next;
  // 数据在切到那一页时才拉：求片站最常见的用法是「搜两下就走了」，
  // 一进来就把三个接口全打一遍在弱网下反而更慢。
  if (next === "mine") await loadMine();
  if (next === "stats") await loadStats();
}

async function refreshMe() {
  try {
    const next = await portalApi.me();
    emit("meChange", next);
  } catch {
    /* 保持旧值：这里的失败不影响正在做的事 */
  }
}

onMounted(loadMine);
</script>

<template>
  <div class="portal">
    <header class="portal__head">
      <div class="portal__head-main">
        <span class="portal__brand">求片中心</span>
        <span class="portal__who">{{ me.display_name || me.username }}</span>
      </div>
      <button class="portal__logout" type="button" @click="emit('logout')">退出</button>
    </header>

    <nav class="portal__tabs">
      <button
        v-for="t in TABS"
        :key="t.key"
        type="button"
        class="portal__tab"
        :class="{ 'portal__tab--active': tab === t.key }"
        @click="switchTab(t.key)"
      >
        {{ t.label }}
      </button>
    </nav>

    <p v-if="submitError" class="portal__error">{{ submitError }}</p>
    <p v-else-if="submitNotice" class="portal__notice">{{ submitNotice }}</p>

    <!-- 求片 -->
    <section v-if="tab === 'search'" class="portal__pane">
      <div class="portal__meta">
        <span v-if="remainingToday === null">今天不限次数，随便求</span>
        <span v-else-if="remainingToday > 0">今天还能求 {{ remainingToday }} 部</span>
        <span v-else class="portal__meta--warn">今天的额度用完了，明天再来</span>
        <span class="portal__meta-sep">·</span>
        <span>{{ me.require_review ? "提交后要等管理员审核" : "提交后直接开始找资源" }}</span>
      </div>

      <div class="portal__search">
        <input
          v-model="query"
          type="search"
          placeholder="片名，例：孤独摇滚"
          @keyup.enter="runSearch(1)"
        />
        <div class="portal__search-side">
          <button
            type="button"
            class="portal__seg"
            :class="{ 'portal__seg--active': mediaType === 'tv' }"
            @click="mediaType = 'tv'"
          >
            剧集
          </button>
          <button
            type="button"
            class="portal__seg"
            :class="{ 'portal__seg--active': mediaType === 'movie' }"
            @click="mediaType = 'movie'"
          >
            电影
          </button>
        </div>
      </div>

      <p v-if="searchError" class="portal__error">{{ searchError }}</p>
      <p v-else-if="searching" class="portal__muted">搜索中…</p>

      <ul v-if="results.length" class="portal__results">
        <li v-for="r in results" :key="draftKey(r)" class="portal__result">
          <div class="portal__poster">
            <img v-if="r.poster" :src="r.poster" :alt="r.title" loading="lazy" />
          </div>
          <div class="portal__result-main">
            <div class="portal__result-title">{{ r.title }}</div>
            <div class="portal__result-sub">
              <span v-if="r.year">{{ r.year }} · </span>
              <span>{{ r.media_type === "movie" ? "电影" : "剧集" }}</span>
              <span v-if="r.vote_avg"> · {{ r.vote_avg.toFixed(1) }} 分</span>
            </div>
            <p v-if="r.overview" class="portal__result-ov">{{ r.overview }}</p>

            <details class="portal__ask">
              <summary>求这部</summary>
              <div class="portal__ask-form">
                <label v-if="r.media_type !== 'movie'" class="portal__field">
                  <span>第几季（不填 = 整部都要）</span>
                  <input v-model.number="askSeason[draftKey(r)]" type="number" min="0" />
                </label>
                <label class="portal__field">
                  <span>标签（例：1080P、中文字幕）</span>
                  <input v-model="askTags[draftKey(r)]" placeholder="用顿号或逗号分开" />
                </label>
                <label class="portal__field">
                  <span>备注（想说的话）</span>
                  <input v-model="askNotes[draftKey(r)]" placeholder="可留空" />
                </label>
                <p class="portal__hint">
                  标签是给「整部剧」打的（比如「4K 中文字幕」），不会只挂在某一季上 ——
                  资源通常是整部一起出的。
                </p>
                <button
                  type="button"
                  class="portal__submit"
                  :disabled="submittingFor === r.tmdb_id || !canSubmit"
                  @click="submitRequest(buildDraft(r))"
                >
                  {{ submittingFor === r.tmdb_id ? "提交中…" : canSubmit ? "提交求片" : "你没有求片权限" }}
                </button>
              </div>
            </details>
          </div>
        </li>
      </ul>

      <p v-else-if="query.trim() && !searching && !searchError" class="portal__muted">
        没搜到。换个字再试试？
      </p>

      <div v-if="totalPages > 1" class="portal__pager">
        <button type="button" :disabled="page <= 1" @click="runSearch(page - 1)">上一页</button>
        <span>{{ page }} / {{ totalPages }}</span>
        <button type="button" :disabled="page >= totalPages" @click="runSearch(page + 1)">下一页</button>
      </div>
    </section>

    <!-- 我的 -->
    <section v-else-if="tab === 'mine'" class="portal__pane">
      <p v-if="mineLoading" class="portal__muted">加载中…</p>
      <p v-else-if="!mine.length" class="portal__muted">还没求过片。去「求片」搜一部。</p>
      <ul v-else class="portal__mine">
        <li v-for="item in mine" :key="item.id" class="portal__mine-item">
          <div class="portal__mine-head">
            <span class="portal__mine-title">{{ item.title }}</span>
            <span class="portal__pill" :class="`portal__pill--${item.status}`">
              {{ item.status_text }}
            </span>
          </div>
          <div class="portal__result-sub">
            {{ item.media_type === "movie" ? "电影" : "剧集" }}
            <template v-if="item.media_type !== 'movie' && item.season > 0"> 第 {{ item.season }} 季</template>
            · {{ item.created_at.slice(0, 16).replace("T", " ") }}
          </div>
          <p v-if="item.reject_reason" class="portal__reject">{{ item.reject_reason }}</p>
          <p v-else-if="item.subscription_linked" class="portal__muted">已经在盯着资源了。</p>

          <details class="portal__ask">
            <summary>标签</summary>
            <div class="portal__ask-form">
              <input
                v-model="tagDrafts[`${item.tmdb_id}|${item.media_type}`]"
                :placeholder="item.tags.join('、') || '例：1080P、中文字幕'"
              />
              <button type="button" class="portal__submit" @click="saveTags(item)">保存标签</button>
            </div>
          </details>
        </li>
      </ul>
    </section>

    <!-- 统计 -->
    <section v-else class="portal__pane">
      <p class="portal__muted">最近 30 天你自己求的片（只统计你，不显示别人）。</p>
      <p v-if="statsLoading" class="portal__muted">加载中…</p>
      <p v-else-if="!statsRows.length" class="portal__muted">还没有数据。</p>
      <table v-else class="portal__table">
        <thead>
          <tr>
            <th>日期</th>
            <th>提交</th>
            <th>通过</th>
            <th>驳回</th>
            <th>已转存</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in statsRows" :key="row.day">
            <td>{{ row.day }}</td>
            <td>{{ row.submitted_count }}</td>
            <td>{{ row.approved_count }}</td>
            <td>{{ row.rejected_count }}</td>
            <td>{{ row.fulfilled_count }}</td>
          </tr>
        </tbody>
      </table>
    </section>
  </div>
</template>