<template>
  <div class="main-content-container cloud-page">
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <div class="card-header">
          <span>{{ sourceName }} · 自动整理分类</span>
          <div class="header-actions">
            <el-button type="primary" size="small" :loading="runningAny" @click="runAllNow">整理全部账号</el-button>
            <el-button size="small" :loading="loadingAccounts" @click="loadData">刷新</el-button>
          </div>
        </div>
      </template>

      <el-alert
        type="info"
        :show-icon="true"
        style="margin-bottom: 12px"
        :title="`${sourceName} 自动整理说明`"
        description="监控程序扫描「待整理目录」中新增的转存资源，按本页配置的分类策略 yaml 自动整理到「已整理根目录」下的分类目录，并重命名（保留 2160p.WEB-DL.H.265.60fps-Ocat 等质量标签）。识别失败或 TMDB 查不到的资源移入「失败目录」；目标目录已有同一部影片时按「覆盖（洗版）」设置处理。每 5 分钟自动执行一次，也可手动触发。"
      />

      <div v-if="loadingAccounts" class="loading-tip">加载账号中...</div>

      <el-empty v-else-if="accounts.length === 0" :description="`暂无可配置的${sourceName}账号，请先在「网盘账号管理」中添加`" />

      <div v-else class="account-card-list">
        <el-card v-for="account in accounts" :key="account.id" class="account-card" shadow="hover">
          <template #header>
            <div class="card-header">
              <div class="card-title">
                <span class="account-badge">{{ account.name || account.username || '未命名' }}</span>
                <el-tag v-if="getConfig(account.id)?.enabled" type="success" size="small" effect="light">已启用</el-tag>
                <el-tag v-else type="info" size="small" effect="light">未启用</el-tag>
              </div>
              <div class="card-actions">
                <el-button
                  size="small"
                  type="primary"
                  :loading="runningAccountId === account.id"
                  @click="runNow(account.id)"
                >
                  立即整理
                </el-button>
                <el-button
                  v-if="getConfig(account.id)"
                  size="small"
                  type="danger"
                  plain
                  @click="removeConfig(account.id)"
                >
                  清除配置
                </el-button>
              </div>
            </div>
          </template>

          <el-form
            :model="formOf(account.id)"
            label-position="top"
            class="auto-form"
          >
            <el-form-item label="启用自动整理">
              <div class="enable-switch">
                <el-switch
                  v-model="formOf(account.id).enabled"
                  active-text="已启用"
                  inactive-text="已禁用"
                />
              </div>
            </el-form-item>

            <el-form-item label="待整理目录" required>
              <el-input
                v-model="formOf(account.id).pending_dir"
                placeholder="例如 媒体库/待整理（频道/订阅转存的落盘目录）"
              />
              <div class="form-help">监控程序扫描该目录下新增资源；请填写网盘中的路径（不含开头的 /）</div>
            </el-form-item>

            <el-form-item label="已整理根目录">
              <el-input
                v-model="formOf(account.id).organized_root"
                placeholder="留空自动推导：所在父目录/已整理"
              />
              <div class="form-help">资源整理到的根目录；例如待整理目录为「媒体库/待整理」，留空则使用「媒体库/已整理」</div>
            </el-form-item>

            <el-form-item label="失败目录">
              <el-input
                v-model="formOf(account.id).failed_dir"
                placeholder="例如 媒体库/整理失败（留空则识别失败原地保留，仅记录）"
              />
              <div class="form-help">识别失败 / TMDB 查不到 / 有非视频残留的资源整体移入该目录；不存在会自动创建</div>
            </el-form-item>

            <el-form-item label="覆盖（洗版）">
              <div class="enable-switch">
                <el-switch
                  v-model="formOf(account.id).overwrite"
                  active-text="覆盖旧版本"
                  inactive-text="跳过不覆盖"
                />
              </div>
              <div class="form-help">已整理目录中存在同一部影片（同一 TMDB）时：开启则删除旧文件重新整理；关闭则跳过并在结果中提示</div>
            </el-form-item>

            <el-form-item label="分类策略（yaml）">
              <el-input
                v-model="formOf(account.id).category_config"
                type="textarea"
                :rows="10"
                placeholder="MoviePilot category.yaml 风格，留空使用默认分类（每个账号可单独配置）"
              />
              <div class="form-help">
                movie/tv 两段，按顺序匹配，无条件项为兜底分类。
                整理到 已整理根目录/分类名/标题 (年份) {tmdb=xxx}[/Season NN]
              </div>
            </el-form-item>

            <div class="form-footer">
              <el-button
                type="primary"
                :loading="savingAccountId === account.id"
                @click="saveConfig(account.id)"
              >
                保存配置
              </el-button>
            </div>
          </el-form>

          <el-collapse v-if="getConfig(account.id)?.last_result" class="last-result">
            <el-collapse-item title="最近一次整理结果" name="result">
              <div class="result-line">运行时间：{{ formatTime(getConfig(account.id)?.last_run_at) }}</div>
              <div v-for="(line, idx) in parseResult(getConfig(account.id)?.last_result).details || []" :key="idx" class="result-line">
                {{ line }}
              </div>
              <el-divider style="margin: 8px 0" />
              <div class="result-meta">
                整理成功 {{ parseResult(getConfig(account.id)?.last_result).organized || 0 }} 个，
                识别失败 {{ parseResult(getConfig(account.id)?.last_result).unrecognized || 0 }} 个
                （移入失败目录 {{ parseResult(getConfig(account.id)?.last_result).moved_to_failed || 0 }}），
                跳过 {{ parseResult(getConfig(account.id)?.last_result).skipped_overwrite || 0 }} 个，
                失败 {{ parseResult(getConfig(account.id)?.last_result).failed || 0 }} 个
              </div>
            </el-collapse-item>
          </el-collapse>
        </el-card>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, onMounted, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'

const props = defineProps<{
  sourceType: string
  sourceName: string
}>()

interface NetdiskAccount {
  id: number
  name: string
  username: string
  source_type: string
}

interface AutoOrganizeConfig {
  id?: number
  account_id: number
  enabled: boolean
  pending_dir: string
  organized_root: string
  failed_dir: string
  category_config: string
  overwrite: boolean
  last_run_at?: string
  last_result?: string
}

const http = useHttpClient()
const loadingAccounts = ref(false)
const accounts = ref<NetdiskAccount[]>([])
const configs = ref<AutoOrganizeConfig[]>([])
const savingAccountId = ref(0)
const runningAccountId = ref(0)
const runningAny = computed(() => runningAccountId.value !== 0)

const defaultCategoryYaml = `# 电影分类策略（留空使用默认分类）
movie:
  纪录片:
    genre_ids: "99,-10402"
  演唱会:
    genre_ids: "10402"
  动画电影:
    genre_ids: "16"
  华语电影:
    original_language: "zh,cn,bo,za"
  日韩电影:
    original_language: "ja,ko,th"
  欧美电影:

# 剧集分类策略
tv:
  国产动漫:
    genre_ids: "16"
    origin_country: "CN,TW,HK"
  日番动漫:
    genre_ids: "16"
    origin_country: "JP"
  欧美动漫:
    genre_ids: "16"
    origin_country: "US,FR,GB,DE,ES,IT,NL,PT,RU,UK"
  纪录片:
    genre_ids: "99"
  综艺节目:
    genre_ids: "10764,10767"
  国产剧集:
    origin_country: "CN,TW,HK,SG"
  日韩剧集:
    origin_country: "JP,KP,KR,TH,IN"
  欧美剧集:
    origin_country: "US,FR,GB,DE,ES,IT,NL,PT,RU,UK,CO"
  未分类:
`

const getConfig = (accountId: number) =>
  configs.value.find((c) => c.account_id === accountId)

const form = reactive<Record<number, AutoOrganizeConfig>>({})
const formOf = (accountId: number): AutoOrganizeConfig => {
  if (!form[accountId]) {
    form[accountId] = {
      account_id: accountId,
      enabled: false,
      pending_dir: '',
      organized_root: '',
      failed_dir: '',
      category_config: defaultCategoryYaml,
      overwrite: true,
    }
  }
  return form[accountId]
}

const loadData = async () => {
  loadingAccounts.value = true
  try {
    const accountResp = await http.get(`${SERVER_URL}/account/list`)
    const allAccounts = (accountResp.data.data || []) as NetdiskAccount[]
    accounts.value = allAccounts.filter((a) => a.source_type === props.sourceType)
    const configResp = await http.get(`${SERVER_URL}/auto-organize/configs`)
    configs.value = ((configResp.data.data || []) as AutoOrganizeConfig[]).filter(
      (c) => accounts.value.some((a) => a.id === c.account_id),
    )
    for (const c of configs.value) {
      form[c.account_id] = {
        id: c.id,
        account_id: c.account_id,
        enabled: c.enabled,
        pending_dir: c.pending_dir || '',
        organized_root: c.organized_root || '',
        failed_dir: c.failed_dir || '',
        category_config: c.category_config || defaultCategoryYaml,
        overwrite: c.overwrite,
      }
    }
  } catch (error) {
    console.error('加载自动整理配置错误：', error)
  } finally {
    loadingAccounts.value = false
  }
}

const saveConfig = async (accountId: number) => {
  const f = formOf(accountId)
  if (!f.pending_dir.trim()) {
    ElMessage.warning('请填写待整理目录')
    return
  }
  savingAccountId.value = accountId
  try {
    const resp = await http.post(`${SERVER_URL}/auto-organize/config`, {
      id: getConfig(accountId)?.id || 0,
      account_id: accountId,
      enabled: f.enabled,
      pending_dir: f.pending_dir.trim(),
      organized_root: f.organized_root.trim(),
      failed_dir: f.failed_dir.trim(),
      category_config: f.category_config,
      overwrite: f.overwrite,
    })
    if (resp.data.code === 200) {
      ElMessage.success('配置已保存')
      await loadData()
    } else {
      ElMessage.error(resp.data.message || '保存失败')
    }
  } catch (error) {
    console.error(error)
    ElMessage.error('保存失败')
  } finally {
    savingAccountId.value = 0
  }
}

const removeConfig = async (accountId: number) => {
  const cfg = getConfig(accountId)
  if (!cfg) return
  try {
    await ElMessageBox.confirm('确定清除该账号的自动整理配置吗？不影响已整理的文件。', '提示', { type: 'warning' })
  } catch {
    return
  }
  try {
    const resp = await http.post(`${SERVER_URL}/auto-organize/config/${cfg.id}/delete`)
    if (resp.data.code === 200) {
      ElMessage.success('配置已清除')
      await loadData()
    } else {
      ElMessage.error(resp.data.message || '清除失败')
    }
  } catch (error) {
    console.error(error)
    ElMessage.error('清除失败')
  }
}

const runNow = async (accountId: number) => {
  runningAccountId.value = accountId
  try {
    const resp = await http.post(`${SERVER_URL}/auto-organize/run`, { account_id: accountId })
    if (resp.data.code === 200) {
      const results = (resp.data.data || []) as any[]
      const r = results[0]
      if (r && r.organized) {
        ElMessage.success(`整理完成：成功 ${r.organized} 个，识别失败 ${r.unrecognized || 0} 个`)
      } else if (r && (r.unrecognized || r.moved_to_failed)) {
        ElMessage.warning(`整理完成：无成功项，识别失败 ${r.unrecognized || 0} 个`)
      } else {
        ElMessage.info('整理完成：待整理目录为空或全部跳过')
      }
      await loadData()
    } else {
      ElMessage.error(resp.data.message || '整理失败')
    }
  } catch (error) {
    console.error(error)
    ElMessage.error('整理失败')
  } finally {
    runningAccountId.value = 0
  }
}

const runAllNow = async () => {
  const enabledOnes = accounts.value.filter((a) => getConfig(a.id)?.enabled)
  if (enabledOnes.length === 0) {
    ElMessage.warning('请先在账号卡片中启用自动整理并保存配置')
    return
  }
  runningAccountId.value = -1
  try {
    const resp = await http.post(`${SERVER_URL}/auto-organize/run`, {})
    if (resp.data.code === 200) {
      const results = (resp.data.data || []) as any[]
      const sum = results.reduce(
        (acc, r) => ({
          organized: acc.organized + (r.organized || 0),
          unrecognized: acc.unrecognized + (r.unrecognized || 0),
          failed: acc.failed + (r.failed || 0),
        }),
        { organized: 0, unrecognized: 0, failed: 0 },
      )
      ElMessage.success(`整理完成：成功 ${sum.organized} 个，识别失败 ${sum.unrecognized} 个，失败 ${sum.failed} 个`)
      await loadData()
    } else {
      ElMessage.error(resp.data.message || '整理失败')
    }
  } catch (error) {
    console.error(error)
    ElMessage.error('整理失败')
  } finally {
    runningAccountId.value = 0
  }
}

const parseResult = (raw?: string) => {
  if (!raw) return {} as any
  try {
    return JSON.parse(raw)
  } catch {
    return {}
  }
}

const formatTime = (raw?: string) => {
  if (!raw) return '-'
  return raw.replace('T', ' ').slice(0, 19)
}

onMounted(loadData)
</script>

<style scoped>
.cloud-page {
  padding: 16px;
}

.cloud-card {
  max-width: 1100px;
}

.account-card-list {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.account-card .card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.card-title {
  display: flex;
  align-items: center;
  gap: 10px;
}

.account-badge {
  font-weight: 600;
  font-size: 15px;
}

.form-help {
  font-size: 12px;
  color: #909399;
  line-height: 1.5;
  margin-top: 4px;
}

.enable-switch {
  display: flex;
  align-items: center;
  gap: 12px;
}

.form-footer {
  margin-top: 8px;
}

.last-result {
  margin-top: 12px;
}

.result-line {
  font-size: 13px;
  color: #606266;
  line-height: 1.7;
  word-break: break-all;
}

.result-meta {
  font-size: 12px;
  color: #909399;
}

.loading-tip {
  padding: 24px;
  text-align: center;
  color: #909399;
}

.header-actions {
  display: flex;
  gap: 8px;
}
</style>