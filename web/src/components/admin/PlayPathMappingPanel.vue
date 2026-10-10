<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  fetchPlayPathMapping,
  savePlayPathMapping,
  testPlayPathMapping,
  type PlayPathConflict,
  type PlayPathRule,
  type PlayPathRuleReport,
  type PlayPathTestResult,
} from "@/api/strm";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import SettingsCard from "@/components/admin/SettingsCard.vue";
import SettingsHelpTooltip from "@/components/admin/SettingsHelpTooltip.vue";
import { toast } from "@/composables/useToast";
import { formatTime } from "@/utils/format";

/**
 * T17 播放路径映射规则编辑器。
 *
 * 这个组件存在的理由只有一条：**映射是静默失败的功能**。
 * 匹配不上不报错、写反了也不报错，所以「我配了规则但没生效」这句话，
 * 光看配置是永远得不出结论的。这里提供两个自查口：
 *
 *   - 每条规则的命中次数与最近命中时间（回答「刚才到底生效没有」）；
 *   - 「测试路径」按钮（回答「这一条路径会被改成什么」）。
 *
 * 测试按钮**必须调服务端**、不能在前端重写一遍匹配：前端重写的那份
 * 一旦和后端的顺序/大小写语义分叉，绿色对勾就成了误导。
 */
const emit = defineEmits<{ saved: []; loading: [value: boolean] }>();

const accent = "#7c3aed";

const enabled = ref(false);
const reports = ref<PlayPathRuleReport[]>([]);
const conflicts = ref<PlayPathConflict[]>([]);
const saving = ref(false);
const loading = ref(false);

/** 草稿与已保存值的差异只体现在这三条上，够用且不会误报。 */
const savedRules = ref<PlayPathRule[]>([]);
const draftRules = ref<PlayPathRule[]>([]);
const savedEnabled = ref(false);

const dirty = computed(
  () =>
    enabled.value !== savedEnabled.value ||
    JSON.stringify(usableRules()) !== JSON.stringify(savedRules.value),
);

/** 统计按规则 ID 取，顺序变了统计仍跟着同一条规则走。 */
function statOf(id: string): PlayPathRuleReport {
  return reports.value.find((r) => r.id === id) ?? { id, source: "", target: "", hits: 0, last_hit: "" };
}

// ── 测试路径 ──
const testPath = ref("");
const testBusy = ref(false);
const testResult = ref<PlayPathTestResult | null>(null);

let nextRuleSeq = 0;

function newRuleId(): string {
  nextRuleSeq += 1;
  return `rule-${Date.now().toString(36)}-${nextRuleSeq}`;
}

function toDraft(list: PlayPathRule[]): PlayPathRule[] {
  return list.map((r) => ({ id: r.id, source: r.source, target: r.target, note: r.note ?? "" }));
}

async function load() {
  loading.value = true;
  emit("loading", true);
  try {
    const data = await fetchPlayPathMapping();
    enabled.value = !!data.enabled;
    savedEnabled.value = !!data.enabled;
    reports.value = data.rules ?? [];
    savedRules.value = toDraft(data.rules ?? []);
    draftRules.value = toDraft(data.rules ?? []);
    conflicts.value = data.conflicts ?? [];
  } catch (e) {
    toast.error(getApiErrorMessage(e, "加载播放路径映射失败"));
  } finally {
    loading.value = false;
    emit("loading", false);
  }
}

function addRule() {
  draftRules.value.push({ id: newRuleId(), source: "", target: "", note: "" });
}

function removeRule(index: number) {
  draftRules.value.splice(index, 1);
}

function moveRule(index: number, delta: number) {
  const next = index + delta;
  if (next < 0 || next >= draftRules.value.length) return;
  const [row] = draftRules.value.splice(index, 1);
  draftRules.value.splice(next, 0, row);
}

/** 只保存有源有目标的行；半截的行静默丢掉会让用户以为「保存了但没生效」。 */
function usableRules(): PlayPathRule[] {
  return draftRules.value
    .filter((r) => r.source.trim() !== "" && r.target.trim() !== "")
    .map((r) => ({ ...r, source: r.source.trim(), target: r.target.trim() }));
}

async function save() {
  saving.value = true;
  try {
    const payload = usableRules();
    const data = await savePlayPathMapping(enabled.value, payload);
    reports.value = data.rules ?? [];
    savedRules.value = toDraft(data.rules ?? []);
    draftRules.value = toDraft(data.rules ?? []);
    conflicts.value = data.conflicts ?? [];
    savedEnabled.value = !!data.enabled;
    if ((data.conflicts ?? []).length > 0) {
      // 不拦截保存，但必须说出来：顺序匹配下重复源意味着后面那条是死规则。
      toast.warning(`已保存，其中 ${(data.conflicts ?? []).length} 条规则被前面的同源规则盖住，不会生效`);
    } else {
      toast.success("播放路径映射已保存");
    }
    emit("saved");
  } catch (e) {
    toast.error(getApiErrorMessage(e, "保存播放路径映射失败"));
  } finally {
    saving.value = false;
  }
}

function revert() {
  enabled.value = savedEnabled.value;
  draftRules.value = toDraft(savedRules.value);
  testResult.value = null;
}

async function runTest() {
  const path = testPath.value.trim();
  if (!path) {
    toast.error("请先填一条要试的播放路径");
    return;
  }
  testBusy.value = true;
  try {
    testResult.value = await testPlayPathMapping(path);
  } catch (e) {
    toast.error(getApiErrorMessage(e, "测试失败"));
  } finally {
    testBusy.value = false;
  }
}

async function applyTest(path: string) {
  testPath.value = path;
  await runTest();
}

/** 命中统计只在本进程内，重启归零；「从未命中」要如实说，不要画成失败。 */
function hitText(hits: number, lastHit: string): string {
  if (!hits) return "从未命中";
  return `命中 ${hits} 次 · 最近 ${formatTime(lastHit)}`;
}

function hitClass(hits: number): string {
  return hits > 0 ? "ppm-rule__hit ppm-rule__hit--yes" : "ppm-rule__hit ppm-rule__hit--no";
}

onMounted(() => {
  void load();
});

defineExpose({ load, isDirty: dirty, save, revert });
</script>

<template>
  <SettingsCard title="播放路径映射" :accent="accent">
    <template #head-aside>
      <span class="ppm-note">按顺序匹配，命中第一条即生效</span>
    </template>

    <div class="ppm-intro">
      <p>
        规则把 STRM 播放链接里的路径改写成网盘真实路径，用于「挂载目录与
        <code>strm_root</code> 不一致」这类场景。
      </p>
      <p class="ppm-intro__warn">
        匹配不上、或者源和目标写反了，<strong>都不会报错</strong>，播放照常进行但路径原样传入 ——
        症状就是「配了没生效」。所以下面每条规则都显示命中统计，并用「测试路径」按钮验证。
      </p>
    </div>

    <div class="ppm-switch">
      <SettingsHelpTooltip title="播放路径映射说明">
        <p><strong>顺序语义：</strong>自上而下第一条匹配的规则生效，后面的即使更精确也轮不到。</p>
        <p><strong>区分大小写：</strong><code>/Media/</code> 与 <code>/media/</code> 是两条不同的路径。</p>
        <p><strong>边界对齐：</strong>源 <code>/media</code> 命中 <code>/media/x</code>，但不命中 <code>/media-old</code>。</p>
        <p><strong>统计只在本进程内</strong>，重启归零；它回答的是「刚才有没有生效过」。</p>
      </SettingsHelpTooltip>
      <label class="ppm-switch__row">
        <input v-model="enabled" type="checkbox" />
        <span>{{ enabled ? "映射已启用" : "映射未启用（路径原样传入）" }}</span>
      </label>
    </div>

    <div v-if="loading" class="ppm-loading">加载中…</div>

    <template v-else>
      <table class="ppm-table">
        <thead>
          <tr>
            <th style="width: 34px">#</th>
            <th>源路径</th>
            <th>目标路径</th>
            <th style="width: 150px">最近命中</th>
            <th style="width: 108px"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, index) in draftRules" :key="row.id" :class="{ 'ppm-row--shadowed': conflicts.some((c) => c.id === row.id) }">
            <td class="ppm-idx">{{ index + 1 }}</td>
            <td><AppInput v-model="row.source" placeholder="/media/movies" /></td>
            <td><AppInput v-model="row.target" placeholder="/实际/目录" /></td>
            <td>
              <span :class="hitClass(statOf(row.id).hits)">{{ hitText(statOf(row.id).hits, statOf(row.id).last_hit) }}</span>
              <p v-if="conflicts.some((c) => c.id === row.id)" class="ppm-shadowed">
                被前面的同源规则盖住，永远不会生效
              </p>
            </td>
            <td class="ppm-ops">
              <AppButton type="button" variant="ghost" :disabled="index === 0" @click="moveRule(index, -1)">上移</AppButton>
              <AppButton type="button" variant="ghost" :disabled="index === draftRules.length - 1" @click="moveRule(index, 1)">下移</AppButton>
              <AppButton type="button" variant="ghost" @click="removeRule(index)">删除</AppButton>
            </td>
          </tr>
          <tr v-if="draftRules.length === 0">
            <td colspan="5" class="ppm-empty">还没有规则。不需要映射就保持关闭。</td>
          </tr>
        </tbody>
      </table>

      <div class="ppm-actions">
        <AppButton type="button" variant="secondary" @click="addRule">添加规则</AppButton>
        <AppButton type="button" variant="secondary" :disabled="!dirty" @click="revert">撤销改动</AppButton>
        <AppButton type="button" variant="primary" :disabled="saving" @click="save">
          {{ saving ? "保存中…" : "保存规则" }}
        </AppButton>
      </div>

      <div class="ppm-test">
        <div class="ppm-test__head">
          <SettingsHelpTooltip title="测试路径说明">
            <p>用<strong>与真实播放完全相同</strong>的匹配逻辑试一条路径，结果与线上播放一致。</p>
            <p>结果分三态：命中、被后面的规则遮蔽、从未命中。第三种就是「规则白写了」，此时先检查大小写和源/目标有没有写反。</p>
          </SettingsHelpTooltip>
          <strong>测试路径</strong>
        </div>
        <div class="ppm-test__row">
          <AppInput v-model="testPath" placeholder="/media/movies/阿凡达 (2009)/阿凡达.mkv" />
          <AppButton type="button" variant="secondary" :disabled="testBusy" @click="runTest">
            {{ testBusy ? "测试中…" : "测试" }}
          </AppButton>
        </div>
        <div class="ppm-test__samples">
          试试现成的：
          <button
            v-for="sample in ['/media/movies/x.mkv', '/Media/movies/x.mkv', '/media-old/x.mkv']"
            :key="sample"
            type="button"
            class="ppm-sample"
            @click="applyTest(sample)"
          >
            {{ sample }}
          </button>
        </div>
        <div v-if="testResult" class="ppm-test__result" :class="{ 'ppm-test__result--miss': !testResult.matched }">
          <p class="ppm-test__verdict">{{ testResult.message }}</p>
          <p class="ppm-test__path">
            最终路径：<code>{{ testResult.result }}</code>
          </p>
          <p v-if="testResult.matched" class="ppm-test__detail">
            规则 {{ testResult.rule_id }}：{{ testResult.source }} → {{ testResult.to }}
          </p>
        </div>
      </div>
    </template>
  </SettingsCard>
</template>

<style scoped>
.ppm-note {
  font-size: 12px;
  color: var(--text-muted);
}
.ppm-intro p {
  margin: 0 0 6px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-muted);
}
.ppm-intro__warn strong {
  color: var(--danger, #dc2626);
}
.ppm-switch {
  margin: 10px 0 4px;
}
.ppm-switch__row {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
}
.ppm-loading,
.ppm-empty {
  padding: 12px 0;
  font-size: 12px;
  color: var(--text-muted);
  text-align: center;
}
.ppm-table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 10px;
}
.ppm-table th {
  padding: 6px 8px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  text-align: left;
  border-bottom: 1px solid var(--border-soft);
}
.ppm-table td {
  padding: 6px 8px;
  vertical-align: top;
  border-bottom: 1px solid var(--border-soft);
}
.ppm-row--shadowed {
  opacity: 0.72;
}
.ppm-idx {
  font-size: 12px;
  color: var(--text-muted);
}
.ppm-hit {
  font-size: 12px;
}
.ppm-hit--yes {
  color: var(--success, #16a34a);
}
.ppm-hit--no {
  color: var(--text-muted);
}
.ppm-shadowed {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--warning, #d97706);
}
.ppm-ops {
  display: flex;
  gap: 2px;
  flex-wrap: wrap;
}
.ppm-actions {
  display: flex;
  gap: 8px;
  margin-top: 10px;
}
.ppm-test {
  margin-top: 18px;
  padding-top: 14px;
  border-top: 1px dashed var(--border-soft);
}
.ppm-test__head {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  margin-bottom: 8px;
}
.ppm-test__row {
  display: flex;
  gap: 8px;
  align-items: center;
}
.ppm-test__samples {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-top: 8px;
  font-size: 12px;
  color: var(--text-muted);
}
.ppm-sample {
  border: 1px solid var(--border-soft);
  border-radius: 4px;
  background: transparent;
  color: var(--brand);
  font-size: 11px;
  padding: 2px 6px;
  cursor: pointer;
  font-family: ui-monospace, monospace;
}
.ppm-sample:hover {
  border-color: var(--brand);
}
.ppm-test__result {
  margin-top: 10px;
  padding: 10px 12px;
  border-radius: 6px;
  background: var(--border-soft);
}
.ppm-test__result--miss {
  background: rgba(220, 38, 38, 0.08);
}
.ppm-test__verdict {
  margin: 0 0 4px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.ppm-test__path,
.ppm-test__detail {
  margin: 0;
  font-size: 12px;
  color: var(--text-muted);
  word-break: break-all;
}
.ppm-test__result code {
  padding: 1px 4px;
  border-radius: 4px;
  background: var(--bg-soft, rgba(0, 0, 0, 0.05));
  font-size: 11px;
}
</style>