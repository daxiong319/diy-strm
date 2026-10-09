<script setup lang="ts">
// 求片中心（管理台侧）。
//
// 这个页面是「审核台 + 规则台 + 自己的单」三件事放在一个页面里，
// 分四个 tab：待审核 / 全部 / 我的 / 规则。
//
// 三条设计决定：
//
// 1. **审核前把「会发生什么」写清楚**。点「通过」不是简单改个状态 ——
//    服务端会立刻建一条资源订阅，之后由订阅流水线接手转存。
//    所以详情里显式列出：片名、季、申请人、标签、备注、是否已建订阅。
//    「已通过但订阅还没建起来」是真实发生过的故障态（保存目录没配），
//    所以详情里把服务端给的 warning 原样显示，不自己猜。
//
// 2. **驳回必须填理由**，前端也拦一道。不是流程洁癖：
//    家人只会看到「被拒了」，理由是他唯一能行动的信息（改片名？换写法？再求一次？）。
//
// 3. **规则是整份保存**（PUT 语义）。规则表本来就小，与其做增删改三套接口，
//    不如一次提交完整列表、服务端 diff —— 逐条保存的中间态会让某个用户的
//    求片「临时变成要审核」，而他此刻并不知道为什么。
import { computed, onMounted, ref, watch } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  REQUEST_MEDIA_TYPE_OPTIONS,
  REQUEST_STATUS_OPTIONS,
  requestCenterApi,
  type MediaRequestReviewItem,
  type MediaRequestRule,
} from "@/api/requestCenter";
import type { PortalRequestItem, PortalStatsRow } from "@/api/request";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import AppStateBlock from "@/components/base/AppStateBlock.vue";
import AppTabBar from "@/components/base/AppTabBar.vue";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import AdminStatusPill from "@/components/admin/AdminStatusPill.vue";
import type { AdminStatusPillTone } from "@/components/admin/AdminStatusPill.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import SettingsBoolSegment from "@/components/admin/SettingsBoolSegment.vue";
import FormField from "@/components/base/FormField.vue";
import { toast } from "@/composables/useToast";
import { formatTime } from "@/utils/format";
import "@/styles/admin-table.css";

const TABS = [
  { key: "pending", label: "待审核" },
  { key: "all", label: "全部" },
  { key: "mine", label: "我的" },
  { key: "rules", label: "规则" },
  { key: "stats", label: "统计" },
];

const tab = ref("pending");

const STATUS_META: Record<string, { tone: AdminStatusPillTone }> = {
  pending: { tone: "warning" },
  approved: { tone: "brand" },
  rejected: { tone: "danger" },
  fulfilled: { tone: "success" },
};

function statusTone(status: string): AdminStatusPillTone {
  return STATUS_META[status]?.tone ?? "muted";
}

function mediaLabel(mediaType: string): string {
  return mediaType === "movie" ? "电影" : "剧集";
}

function scopeLabel(item: MediaRequestReviewItem | PortalRequestItem): string {
  if (item.media_type !== "tv") return mediaLabel(item.media_type);
  // 季为 0 表示「整部都要」。前端不要自己推断 0 的含义 ——
  // 后端把 0 定成整部，这里就照着显示。
  return item.season > 0 ? `${mediaLabel(item.media_type)} 第 ${item.season} 季` : "剧集（全季）";
}

// ---------------------------------------------------------------- 待审核 / 全部

const reviewLoading = ref(false);
const reviewError = ref("");
const pendingItems = ref<MediaRequestReviewItem[]>([]);
const allItems = ref<MediaRequestReviewItem[]>([]);
const statusFilter = ref("");

// 审核弹窗
const reviewOpen = ref(false);
const reviewTarget = ref<MediaRequestReviewItem | null>(null);
const reviewWarning = ref("");
const approve = ref(true);
const rejectReason = ref("");
const reviewNote = ref("");
const reviewSubmitting = ref(false);
const reviewSubmitError = ref("");

function openReview(item: MediaRequestReviewItem) {
  reviewTarget.value = item;
  // 默认选「通过」：多数情况下家人的请求是合理的，驳回是例外。
  // 但**每次打开都重置**，否则上一次留下的「驳回 + 半句理由」会跟着走 ——
  // 那会导致一个本该通过的请求被误驳，而审核员根本没意识到。
  approve.value = true;
  rejectReason.value = "";
  reviewNote.value = "";
  reviewWarning.value = "";
  reviewSubmitError.value = "";
  reviewOpen.value = true;
  void loadDetailWarning(item);
}

async function loadPending() {
  reviewLoading.value = true;
  reviewError.value = "";
  try {
    const res = await requestCenterApi.pending();
    pendingItems.value = res.items ?? [];
  } catch (e) {
    reviewError.value = getApiErrorMessage(e, "加载待审核列表失败");
  } finally {
    reviewLoading.value = false;
  }
}

async function loadAll() {
  reviewLoading.value = true;
  reviewError.value = "";
  try {
    const res = await requestCenterApi.list(statusFilter.value);
    allItems.value = res.items ?? [];
  } catch (e) {
    reviewError.value = getApiErrorMessage(e, "加载求片列表失败");
  } finally {
    reviewLoading.value = false;
  }
}

async function submitReview() {
  const target = reviewTarget.value;
  if (!target) return;
  if (!approve.value && !rejectReason.value.trim()) {
    // 与后端的校验同一条：驳回必须给理由。
    reviewSubmitError.value = "驳回时请填写原因，方便对方调整";
    return;
  }
  reviewSubmitting.value = true;
  reviewSubmitError.value = "";
  try {
    const res = await requestCenterApi.review(target.id, {
      approve: approve.value,
      reason: approve.value ? "" : rejectReason.value.trim(),
      note: reviewNote.value.trim(),
    });
    reviewOpen.value = false;
    // 服务端给的告警必须转达：比如「没配保存目录，订阅没建起来」。
    // 吞掉它的话，审核员会以为一切正常，然后用户等一周发现什么都没有。
    if (res.warning) {
      toast.warning(`已${approve.value ? "通过" : "驳回"}，但有件事要说：${res.warning}`);
    } else {
      toast.success(approve.value ? "已通过，正在建资源订阅" : "已驳回");
    }
    await Promise.all([loadPending(), loadAll()]);
  } catch (e) {
    reviewSubmitError.value = getApiErrorMessage(e, "审核失败");
  } finally {
    reviewSubmitting.value = false;
  }
}

async function loadDetailWarning(item: MediaRequestReviewItem) {
  // 详情接口会带上服务端算好的告警（例如「已通过但订阅还没建起来」）。
  // 这里失败也不打断界面 —— 拿不到告警只是少一句话，不该让审核员做不了事。
  try {
    const res = await requestCenterApi.detail(item.id);
    reviewWarning.value = res.warning ?? "";
  } catch {
    reviewWarning.value = "";
  }
}

// ---------------------------------------------------------------- 我的

const mineItems = ref<PortalRequestItem[]>([]);
const mineLoading = ref(false);

// ---------------------------------------------------------------- 规则

const rules = ref<MediaRequestRule[]>([]);
const rulesLoading = ref(false);
const rulesSaving = ref(false);
const rulesError = ref("");

function emptyRule(): MediaRequestRule {
  return {
    id: 0,
    name: "",
    media_type: "",
    daily_limit: 0,
    pending_limit: 0,
    auto_approve: false,
    applies_to_user_id: 0,
    enabled: true,
    priority: 100,
  };
}

const draftRules = ref<MediaRequestRule[]>([]);

function editRules() {
  draftRules.value = rules.value.map((r) => ({ ...r }));
}

function addRule() {
  draftRules.value = [...draftRules.value, emptyRule()];
}

function removeRule(index: number) {
  draftRules.value = draftRules.value.filter((_, i) => i !== index);
}

async function loadRules() {
  rulesLoading.value = true;
  rulesError.value = "";
  try {
    const res = await requestCenterApi.rules();
    rules.value = res.items ?? [];
  } catch (e) {
    rulesError.value = getApiErrorMessage(e, "加载求片规则失败");
  } finally {
    rulesLoading.value = false;
  }
}

async function saveRules() {
  rulesSaving.value = true;
  rulesError.value = "";
  try {
    const res = await requestCenterApi.saveRules(draftRules.value);
    rules.value = res.items ?? [];
    draftRules.value = [];
    toast.success("规则已保存");
  } catch (e) {
    rulesError.value = getApiErrorMessage(e, "保存规则失败");
  } finally {
    rulesSaving.value = false;
  }
}

const reconciling = ref(false);

async function reconcile() {
  reconciling.value = true;
  try {
    const res = await requestCenterApi.reconcile();
    toast.success(`对账完成，处理了 ${res.handled} 条`);
    await Promise.all([loadPending(), loadAll()]);
  } catch (e) {
    toast.error(getApiErrorMessage(e, "对账失败"));
  } finally {
    reconciling.value = false;
  }
}

// ---------------------------------------------------------------- 统计

const stats = ref<PortalStatsRow[]>([]);
const statsLoading = ref(false);

/** 按天汇总（后端给的是「天 × 人 × 类型」一行，这里按天合并）。 */
const statsByDay = computed(() => {
  const map = new Map<string, { day: string; submitted: number; approved: number; rejected: number; fulfilled: number }>();
  for (const row of stats.value) {
    const cur = map.get(row.day) ?? { day: row.day, submitted: 0, approved: 0, rejected: 0, fulfilled: 0 };
    cur.submitted += row.submitted_count;
    cur.approved += row.approved_count;
    cur.rejected += row.rejected_count;
    cur.fulfilled += row.fulfilled_count;
    map.set(row.day, cur);
  }
  return [...map.values()].sort((a, b) => (a.day < b.day ? 1 : -1));
});

async function loadMine() {
  mineLoading.value = true;
  try {
    const res = await requestCenterApi.mine();
    mineItems.value = res.items ?? [];
  } catch (e) {
    toast.error(getApiErrorMessage(e, "加载我的求片失败"));
  } finally {
    mineLoading.value = false;
  }
}

async function loadStats() {
  statsLoading.value = true;
  try {
    const res = await requestCenterApi.stats();
    stats.value = res.items ?? [];
  } catch (e) {
    toast.error(getApiErrorMessage(e, "加载统计失败"));
  } finally {
    statsLoading.value = false;
  }
}

// tab 切换才拉数据：一次加载五张表没人等得了，
// 而且「待审核」在切到别的 tab 之后仍然可能变多。
watch(tab, (next) => {
  if (next === "pending") loadPending();
  if (next === "all") loadAll();
  if (next === "mine") loadMine();
  if (next === "rules") loadRules();
  if (next === "stats") loadStats();
});
watch(statusFilter, () => {
  if (tab.value === "all") loadAll();
});

onMounted(() => loadPending());
</script>

<template>
  <div class="mrc">
    <AppTabBar v-model="tab" :tabs="TABS">
      <template #actions>
        <AppButton size="sm" :disabled="reconciling" @click="reconcile">
          {{ reconciling ? "对账中…" : "对账" }}
        </AppButton>
      </template>
    </AppTabBar>

    <!-- 待审核 -->
    <template v-if="tab === 'pending'">
      <AppStateBlock v-if="reviewLoading" message="加载中…" loading />
      <div v-else-if="reviewError" class="mrc__error">{{ reviewError }}</div>
      <AdminEmptyState
        v-else-if="pendingItems.length === 0"
        icon="inbox"
        title="没有待审核的求片"
        description="家人的新求片会自动出现在这里。"
      />
      <div v-else class="admin-panel-table-wrap">
        <table class="admin-table">
          <thead>
            <tr>
              <th>作品</th>
              <th>申请人</th>
              <th>标签 / 备注</th>
              <th>提交时间</th>
              <th class="admin-table__actions">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in pendingItems" :key="item.id">
              <td>
                <div class="mrc__title">{{ item.title }}</div>
                <div class="mrc__sub">
                  {{ scopeLabel(item) }}
                  <span v-if="item.year"> · {{ item.year }}</span>
                </div>
              </td>
              <td>{{ item.requester_name || "—" }}</td>
              <td>
                <div v-if="item.tags.length" class="mrc__tags">
                  <span v-for="t in item.tags" :key="t" class="mrc__tag">{{ t }}</span>
                </div>
                <div v-if="item.notes" class="mrc__sub">{{ item.notes }}</div>
                <span v-if="!item.tags.length && !item.notes" class="mrc__sub">—</span>
              </td>
              <td class="mrc__sub">{{ formatTime(item.created_at) }}</td>
              <td class="admin-table__actions">
                <AppButton size="sm" variant="primary" @click="openReview(item)">审核</AppButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 全部 -->
    <template v-else-if="tab === 'all'">
      <div class="mrc__filter">
        <AppSelect v-model="statusFilter" :options="REQUEST_STATUS_OPTIONS" placeholder="全部" />
      </div>
      <AppStateBlock v-if="reviewLoading" message="加载中…" loading />
      <div v-else-if="reviewError" class="mrc__error">{{ reviewError }}</div>
      <AdminEmptyState
        v-else-if="allItems.length === 0"
        icon="inbox"
        title="没有求片记录"
        description="换个状态筛选看看，或者等家人提交第一条。"
      />
      <div v-else class="admin-panel-table-wrap">
        <table class="admin-table">
          <thead>
            <tr>
              <th>作品</th>
              <th>申请人</th>
              <th>状态</th>
              <th>审核</th>
              <th>订阅</th>
              <th class="admin-table__actions">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in allItems" :key="item.id">
              <td>
                <div class="mrc__title">{{ item.title }}</div>
                <div class="mrc__sub">{{ scopeLabel(item) }}</div>
              </td>
              <td>{{ item.requester_name || "—" }}</td>
              <td>
                <AdminStatusPill :tone="statusTone(item.status)">{{ item.status_text }}</AdminStatusPill>
              </td>
              <td class="mrc__sub">
                <template v-if="item.reviewer_name">{{ item.reviewer_name }} · {{ formatTime(item.reviewed_at) }}</template>
                <div v-if="item.reject_reason" class="mrc__reject">{{ item.reject_reason }}</div>
              </td>
              <td>
                <AdminStatusPill :tone="item.subscription_id > 0 ? 'success' : 'muted'">
                  {{ item.subscription_id > 0 ? `#${item.subscription_id}` : "未建" }}
                </AdminStatusPill>
              </td>
              <td class="admin-table__actions">
                <AppButton
                  size="sm"
                  :disabled="item.status !== 'pending'"
                  @click="openReview(item)"
                >
                  审核
                </AppButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 我的 -->
    <template v-else-if="tab === 'mine'">
      <AppStateBlock v-if="mineLoading" message="加载中…" loading />
      <AdminEmptyState
        v-else-if="mineItems.length === 0"
        icon="inbox"
        title="你还没有求过片"
        description="去求片站（独立端口，默认 7812）搜一部想看的。"
      />
      <div v-else class="admin-panel-table-wrap">
        <table class="admin-table">
          <thead>
            <tr>
              <th>作品</th>
              <th>状态</th>
              <th>说明</th>
              <th>提交时间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in mineItems" :key="item.id">
              <td>
                <div class="mrc__title">{{ item.title }}</div>
                <div class="mrc__sub">{{ scopeLabel(item) }}</div>
              </td>
              <td>
                <AdminStatusPill :tone="statusTone(item.status)">{{ item.status_text }}</AdminStatusPill>
              </td>
              <td class="mrc__sub">
                <span v-if="item.reject_reason">{{ item.reject_reason }}</span>
                <span v-else-if="item.subscription_linked">已在盯着资源</span>
                <span v-else>—</span>
              </td>
              <td class="mrc__sub">{{ formatTime(item.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 规则 -->
    <template v-else-if="tab === 'rules'">
      <SettingsCard title="求片规则" accent="var(--brand)">
        <template #head-actions>
          <AppButton size="sm" @click="editRules">编辑</AppButton>
          <AppButton size="sm" variant="primary" @click="addRule">新增</AppButton>
        </template>
        <p class="mrc__hint">
          按「用户 + 媒体类型」覆盖全局设置。优先级数字小的先匹配；都不命中就用系统设置里的默认值。
        </p>
        <AppStateBlock v-if="rulesLoading" message="加载中…" loading />
        <div v-else-if="rulesError" class="mrc__error">{{ rulesError }}</div>
        <AdminEmptyState
          v-else-if="rules.length === 0"
          icon="sliders-h"
          title="还没有规则"
          description="不配规则也能用：所有人共用系统设置里的每日上限与审核开关。"
        />
        <table v-else class="admin-table">
          <thead>
            <tr>
              <th>名称</th>
              <th>对象</th>
              <th>类型</th>
              <th>每日上限</th>
              <th>待审上限</th>
              <th>自动通过</th>
              <th>优先级</th>
              <th>启用</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rules" :key="r.id">
              <td>{{ r.name || `规则 ${r.id}` }}</td>
              <td>{{ r.applies_to_user_id > 0 ? `用户 #${r.applies_to_user_id}` : "所有人" }}</td>
              <td>{{ r.media_type === "movie" ? "电影" : r.media_type === "tv" ? "剧集" : "全部" }}</td>
              <td>{{ r.daily_limit > 0 ? r.daily_limit : "不限" }}</td>
              <td>{{ r.pending_limit > 0 ? r.pending_limit : "不限" }}</td>
              <td>{{ r.auto_approve ? "是" : "否" }}</td>
              <td>{{ r.priority }}</td>
              <td>{{ r.enabled ? "是" : "否" }}</td>
            </tr>
          </tbody>
        </table>
      </SettingsCard>
    </template>

    <!-- 统计 -->
    <template v-else-if="tab === 'stats'">
      <SettingsCard title="最近 30 天求片统计" accent="var(--info)">
        <AppStateBlock v-if="statsLoading" message="加载中…" loading />
        <AdminEmptyState
          v-else-if="statsByDay.length === 0"
          icon="chart-bar"
          title="还没有统计数据"
          description="提交过求片之后这里会有数据。"
        />
        <table v-else class="admin-table">
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
            <tr v-for="row in statsByDay" :key="row.day">
              <td>{{ row.day }}</td>
              <td>{{ row.submitted }}</td>
              <td>{{ row.approved }}</td>
              <td>{{ row.rejected }}</td>
              <td>{{ row.fulfilled }}</td>
            </tr>
          </tbody>
        </table>
      </SettingsCard>
    </template>

    <!-- 审核弹窗 -->
    <AppModal :open="reviewOpen" title="审核求片" size="sm" @close="reviewOpen = false">
      <div v-if="reviewTarget" class="modal-form">
        <div class="mrc__review-head">
          <div class="mrc__title">{{ reviewTarget.title }}</div>
          <div class="mrc__sub">
            {{ scopeLabel(reviewTarget) }} · 申请人 {{ reviewTarget.requester_name || "—" }}
          </div>
        </div>

        <div v-if="reviewTarget.tags.length" class="mrc__tags">
          <span v-for="t in reviewTarget.tags" :key="t" class="mrc__tag">{{ t }}</span>
        </div>
        <div v-if="reviewTarget.notes" class="mrc__notes">备注：{{ reviewTarget.notes }}</div>

        <div class="mrc__review-effect">
          通过后会自动建一条资源订阅，之后由订阅流水线负责找到资源并转存。
        </div>

        <div v-if="reviewWarning" class="mrc__warn">{{ reviewWarning }}</div>

        <FormField label="审核结果">
          <SettingsBoolSegment
            v-model="approve"
            label="审核结果"
            on-label="通过"
            off-label="驳回"
          />
        </FormField>

        <FormField v-if="!approve" label="驳回原因" required>
          <AppInput
            v-model="rejectReason"
            placeholder="例：片名写错了，换成《XXX》再求一次"
            ignore-autofill
          />
          <span class="mrc__hint">这是对方唯一能行动的信息 —— 写清楚他下一步该做什么。</span>
        </FormField>

        <FormField label="内部备注（可选）">
          <AppInput v-model="reviewNote" placeholder="只有审核员看得到" ignore-autofill />
        </FormField>

        <div v-if="reviewSubmitError" class="mrc__error">{{ reviewSubmitError }}</div>

        <div class="modal-form__footer">
          <AppButton variant="cancel" @click="reviewOpen = false">取消</AppButton>
          <AppButton
            :variant="approve ? 'primary' : 'danger'"
            :disabled="reviewSubmitting"
            @click="submitReview"
          >
            {{ reviewSubmitting ? "提交中…" : approve ? "通过" : "驳回" }}
          </AppButton>
        </div>
      </div>
    </AppModal>

    <!-- 规则编辑弹窗 -->
    <AppModal
      :open="draftRules.length > 0"
      title="编辑求片规则"
      size="lg"
      @close="draftRules = []"
    >
      <div class="modal-form">
        <div v-for="(r, i) in draftRules" :key="r.id || `new-${i}`" class="mrc__rule">
          <div class="modal-form__row">
            <FormField label="名称">
              <AppInput v-model="r.name" placeholder="例：给孩子放宽" ignore-autofill />
            </FormField>
            <FormField label="媒体类型">
              <AppSelect v-model="r.media_type" :options="REQUEST_MEDIA_TYPE_OPTIONS" />
            </FormField>
          </div>
          <div class="modal-form__row">
            <FormField label="用户 ID（0 = 所有人）">
              <AppInput v-model="r.applies_to_user_id" type="number" />
            </FormField>
            <FormField label="优先级（小者优先）">
              <AppInput v-model="r.priority" type="number" />
            </FormField>
          </div>
          <div class="modal-form__row">
            <FormField label="每日上限（0 = 不限）">
              <AppInput v-model="r.daily_limit" type="number" />
            </FormField>
            <FormField label="待审上限（0 = 不限）">
              <AppInput v-model="r.pending_limit" type="number" />
            </FormField>
          </div>
          <div class="modal-form__row">
            <FormField label="自动通过">
              <SettingsBoolSegment
                v-model="r.auto_approve"
                label="自动通过"
                on-label="是"
                off-label="否"
              />
            </FormField>
            <FormField label="启用">
              <SettingsBoolSegment
                v-model="r.enabled"
                label="启用"
                on-label="是"
                off-label="否"
              />
            </FormField>
          </div>
          <div class="mrc__rule-actions">
            <AppButton size="sm" variant="danger" @click="removeRule(i)">删除这条</AppButton>
          </div>
        </div>
        <div v-if="!draftRules.length" class="mrc__hint">还没有待保存的规则。</div>
        <div class="modal-form__footer">
          <AppButton variant="cancel" @click="draftRules = []">取消</AppButton>
          <AppButton variant="primary" :disabled="rulesSaving" @click="saveRules">
            {{ rulesSaving ? "保存中…" : "整份保存" }}
          </AppButton>
        </div>
      </div>
    </AppModal>
  </div>
</template>

<style scoped>
.mrc__title {
  font-weight: 600;
  color: var(--text);
}

.mrc__sub {
  font-size: 12px;
  color: var(--text-muted);
}

.mrc__filter {
  display: flex;
  gap: 10px;
  margin-bottom: 14px;
}

.mrc__error {
  padding: 14px 16px;
  border-radius: var(--radius-md);
  background: color-mix(in srgb, var(--danger) 10%, transparent);
  color: var(--danger);
  font-size: 13px;
}

.mrc__tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.mrc__tag {
  display: inline-flex;
  padding: 1px 7px;
  border-radius: var(--radius-pill);
  background: color-mix(in srgb, var(--brand) 12%, transparent);
  color: var(--brand-strong);
  font-size: 11px;
}

.mrc__reject {
  color: var(--danger);
}

.mrc__review-head {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.mrc__notes {
  font-size: 13px;
  color: var(--text-regular);
  background: var(--surface-sunken);
  border-radius: var(--radius-sm);
  padding: 8px 10px;
}

/* 「点通过会发生什么」：审核是不可逆的状态迁移，得写在按钮上方而不是让审核员自己记。 */
.mrc__review-effect {
  font-size: 12px;
  color: var(--text-muted);
  border-left: 3px solid var(--brand);
  padding: 6px 10px;
  background: var(--surface-sunken);
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
}

.mrc__warn {
  font-size: 12px;
  color: var(--warning);
  border-left: 3px solid var(--warning);
  padding: 6px 10px;
  background: color-mix(in srgb, var(--warning) 8%, transparent);
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
}

.mrc__hint {
  font-size: 12px;
  color: var(--text-muted);
}

.mrc__rule {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px;
  border: 1px solid var(--border-soft);
  border-radius: var(--radius-md);
}

.mrc__rule-actions {
  display: flex;
  justify-content: flex-end;
}

@media (max-width: 640px) {
  .mrc__filter {
    width: 100%;
  }
}
</style>
