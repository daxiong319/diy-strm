<template>
  <div class="params-form">
    <div class="pf-grid">
      <div class="pf-item pf-wide">
        <label class="pf-label">订阅关键词</label>
        <el-input v-model="form.search_keyword" placeholder="留空则使用 TMDB 标题" size="default" clearable />
        <p class="pf-tip">搜索影巢资源时使用的关键词；留空按 TMDB 标题（剧集自动附加季号写法）搜索。</p>
      </div>

      <div class="pf-item">
        <label class="pf-label">目标网盘</label>
        <el-select v-model="form.source_type" class="w-full">
          <el-option value="123" label="123 云盘" />
          <el-option value="guangyapan" label="光鸭云盘" />
          <el-option value="pan139" label="中国移动云盘" />
        </el-select>
        <p class="pf-tip">仅影巢上该网盘类型的资源会被转存。</p>
      </div>

      <div class="pf-item">
        <label class="pf-label">云盘路径</label>
        <div class="pf-dir-row">
          <el-input v-model="form.target_dir" placeholder="例如：/媒体库/影视" clearable />
          <el-button size="default" @click="pickerVisible = true">选择目录</el-button>
        </div>
        <p class="pf-tip">留空使用根目录；开启「按订阅名建子目录」后将在其下创建「片名 (年份)」目录。</p>
      </div>

      <div class="pf-item">
        <label class="pf-label">分辨率</label>
        <el-select v-model="form.resolution" class="w-full" clearable placeholder="不限">
          <el-option v-for="r in resolutionOptions" :key="r" :value="r" :label="r" />
        </el-select>
        <p class="pf-tip">只转存包含该清晰度的资源；留空不限。</p>
      </div>

      <div class="pf-item">
        <label class="pf-label">特效字幕</label>
        <el-select v-model="form.effect" class="w-full" clearable placeholder="不限">
          <el-option v-for="e in effectOptions" :key="e" :value="e" :label="e" />
        </el-select>
        <p class="pf-tip">只转存标题/字幕信息中包含该关键词的资源；留空不限。</p>
      </div>

      <div class="pf-item">
        <label class="pf-label">搜索渠道</label>
        <div class="pf-chips">
          <el-tag
            v-for="opt in sourceOptions"
            :key="opt.value"
            :type="selectedSources.includes(opt.value) ? 'primary' : 'info'"
            :effect="selectedSources.includes(opt.value) ? 'dark' : 'plain'"
            disable-transitions
            class="pf-chip"
            @click="toggleSource(opt.value)"
          >{{ opt.label }}</el-tag>
        </div>
        <p class="pf-tip">不选则使用全局启用渠道（Telegram / 影巢 / 盘搜）。</p>
      </div>

      <div class="pf-item">
        <label class="pf-label">标题包含正则</label>
        <el-input v-model="form.include_regex" class="pf-mono" placeholder="如: 国语|简中" clearable />
      </div>

      <div class="pf-item">
        <label class="pf-label">标题/文件名排除正则</label>
        <el-input v-model="form.exclude_regex" placeholder="如: 预告|抢先" clearable />
      </div>
    </div>

    <el-divider style="margin: 4px 0 12px" />

    <div class="pf-grid">
      <div class="pf-item">
        <label class="pf-label">收录完成自动完结</label>
        <el-switch v-model="form.auto_finish" />
        <p class="pf-tip">影片/剧集收录完毕后自动停用订阅，避免重复转存。</p>
      </div>
      <div class="pf-item">
        <label class="pf-label">洗版模式</label>
        <el-switch v-model="form.wash" />
      </div>
      <template v-if="form.wash">
        <div class="pf-item">
          <label class="pf-label">洗版目标</label>
          <el-select v-model="form.wash_target" class="w-full">
            <el-option value="" label="无限制（持续升级到最好规格）" />
            <el-option value="1080p" label="1080p（达 1080p 即止）" />
            <el-option value="4k" label="4K（达 4K 即止）" />
            <el-option value="4k_remux" label="4K REMUX（达 4K 原盘即止）" />
          </el-select>
        </div>
        <div class="pf-item">
          <label class="pf-label">旧版处理</label>
          <el-radio-group v-model="form.replace_old">
            <el-radio :value="true">删除旧版本</el-radio>
            <el-radio :value="false">保留共存</el-radio>
          </el-radio-group>
        </div>
      </template>
    </div>

    <CloudDirPicker
      :visible="pickerVisible"
      :source-type="form.source_type"
      :source-name="'影巢订阅转存'"
      @update:visible="pickerVisible = $event"
      @select="onDirSelected"
    />
  </div>
</template>

<script lang="ts">
// 普通 script 块：可被其他组件 import 的共享类型与工厂
export interface HiveSubParams {
  source_type: string
  target_dir: string
  search_keyword: string
  resolution: string
  effect: string
  search_sources: string
  include_regex: string
  exclude_regex: string
  auto_finish: boolean
  wash: boolean
  wash_target: string
  replace_old: boolean
}

export const emptyParams = (): HiveSubParams => ({
  source_type: '123',
  target_dir: '/媒体库/待整理',
  search_keyword: '',
  resolution: '',
  effect: '',
  search_sources: '',
  include_regex: '',
  exclude_regex: '',
  auto_finish: true,
  wash: false,
  wash_target: '',
  replace_old: true,
})
</script>

<script setup lang="ts">
import { computed, ref } from 'vue'
import CloudDirPicker from './CloudDirPicker.vue'

// 由父组件持有的响应式表单对象（props.modelValue 需为 reactive 对象）
const props = defineProps<{ modelValue: HiveSubParams }>()
const form = props.modelValue

const resolutionOptions = ['720P', '1080P', '2160P']
const effectOptions = ['特效', '双语', '国语']

// 搜索渠道 chips（对齐 mediavault SIM 选择器：不选则使用全局启用渠道）
const sourceOptions = [
  { label: 'Telegram', value: 'telegram' },
  { label: '影巢', value: 'hdhive' },
  { label: '盘搜', value: 'pansou' },
]
const selectedSources = computed(() => (form.search_sources || '').split(',').filter(Boolean))
const toggleSource = (v: string) => {
  const cur = selectedSources.value
  const next = cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v]
  form.search_sources = next.join(',')
}

const pickerVisible = ref(false)
const onDirSelected = (path: string) => {
  form.target_dir = path
}
</script>

<style scoped>
.params-form {
  width: 100%;
}
.pf-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
  gap: 12px 14px;
  align-items: start;
}
.pf-item {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.pf-wide {
  grid-column: 1 / -1;
}
.pf-label {
  font-size: 12.5px;
  font-weight: 500;
  color: var(--text);
}
.pf-tip {
  font-size: 11px;
  line-height: 1.5;
  color: var(--text-muted);
}
.pf-dir-row {
  display: flex;
  gap: 6px;
  width: 100%;
}
.pf-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.pf-chip {
  cursor: pointer;
  user-select: none;
}
.pf-mono :deep(.el-input__inner) {
  font-family: var(--font-mono);
  font-size: 12px;
}
.w-full {
  width: 100%;
}
</style>