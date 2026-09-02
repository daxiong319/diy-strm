<template>
  <div class="mv-page mv-page-wide">
    <!-- ============ 洗版工作台（mediavault 风格：统计 + 清单 + 一键洗版） ============ -->
    <section v-if="accounts.length" class="mv-sec">
      <div class="mv-sec-head">
        <div>
          <h3 class="mv-sec-title">
            洗版工作台
            <span class="mv-pill mv-pill-primary">违规扫描 · 更优才覆盖</span>
          </h3>
          <p class="mv-sec-desc">
            扫描已整理媒体库中「不达标」（分辨率/编码低于规则）的条目生成待洗版清单；等待新的更优资源后，
            一键洗版会按质量评分逐项比较，新源更优才覆盖，落败旧版按策略删除或归档。支持定时扫描（默认每日 3 点）与系统通知。
          </p>
        </div>
        <div class="mv-sec-actions">
          <el-button size="small" :loading="loadingWash" @click="loadWashAll">刷新</el-button>
          <el-button size="small" type="primary" :loading="scanning" @click="scanAll">扫描全部账号</el-button>
        </div>
      </div>
      <div class="mv-sec-body">
        <div class="mv-hero">
          <div class="mv-stat tone-danger">
            <span class="mv-stat-num">{{ totalPending }}</span>
            <span class="mv-stat-label">待洗版</span>
          </div>
          <div class="mv-stat tone-warn">
            <span class="mv-stat-num">{{ totalAbandoned }}</span>
            <span class="mv-stat-label">已放弃</span>
          </div>
          <div class="mv-stat tone-success">
            <span class="mv-stat-num">{{ totalWashed }}</span>
            <span class="mv-stat-label">已洗版</span>
          </div>
          <div class="mv-stat tone-info">
            <span class="mv-stat-num">{{ lastScanTimeText }}</span>
            <span class="mv-stat-label">最近扫描</span>
          </div>
        </div>

        <div class="mv-toolbar" style="margin-top: 16px">
          <el-select v-model="washAccountId" size="small" style="width: 190px">
            <el-option label="全部账号" :value="0" />
            <el-option v-for="a in accounts" :key="a.id" :label="accountLabel(a)" :value="a.id" />
          </el-select>
          <el-radio-group v-model="washStatus" size="small">
            <el-radio-button value="">全部</el-radio-button>
            <el-radio-button value="pending">待洗版</el-radio-button>
            <el-radio-button value="abandoned">已放弃</el-radio-button>
            <el-radio-button value="washed">已洗版</el-radio-button>
          </el-radio-group>
          <div style="flex: 1"></div>
          <el-button size="small" type="warning" plain :disabled="!selection.length" @click="batchSetStatus('abandoned')">
            放弃所选
          </el-button>
          <el-button size="small" :disabled="!selection.length" @click="batchSetStatus('pending')">恢复所选</el-button>
        </div>

        <div class="mv-table-wrap">
          <table class="mv-table">
            <thead>
              <tr>
                <th style="width: 34px">
                  <el-checkbox :model-value="allSelected" @change="toggleAll" />
                </th>
                <th>影片</th>
                <th>媒体库位置</th>
                <th>质量快照</th>
                <th>违规说明</th>
                <th>状态</th>
                <th>更新时间</th>
                <th style="width: 108px">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in filteredItems" :key="item.id" :class="{ 'mv-row-dim': item.status === 'washed' }">
                <td>
                  <el-checkbox :model-value="selection.includes(item.id)" @change="(v: boolean) => toggleSelect(item.id, v)" />
                </td>
                <td>
                  <div style="font-weight: 600; color: var(--text)">
                    {{ item.title || '未识别标题' }}
                    <el-tag size="small" effect="plain" style="margin-left: 4px">{{ item.media_type }}</el-tag>
                  </div>
                  <div class="mv-note">
                    {{ item.year ? item.year : '' }}
                    <template v-if="item.season_num"> S{{ pad(item.season_num) }}</template>
                    <template v-if="item.episode_num">E{{ pad(item.episode_num) }}</template>
                    <template v-if="item.tmdb_id"> · TMDB {{ item.tmdb_id }}</template>
                  </div>
                </td>
                <td>
                  <div class="mv-note" style="max-width: 300px; word-break: break-all">
                    {{ item.rel_path }}{{ item.rel_path && item.file_name ? '/' : '' }}{{ item.file_name }}
                  </div>
                </td>
                <td>
                  <span v-if="item.res_tag" class="mv-pill mv-pill-muted">{{ item.res_tag }}</span>
                  <span v-if="item.codec_tag" class="mv-pill mv-pill-muted" style="margin-left: 4px">{{ item.codec_tag }}</span>
                  <span v-if="item.audio_tag" class="mv-pill mv-pill-muted" style="margin-left: 4px">{{ item.audio_tag }}</span>
                  <div v-if="item.channels" class="mv-note">{{ item.channels }} ch</div>
                </td>
                <td>
                  <div class="mv-note" style="max-width: 260px; word-break: break-all">{{ item.violations || '-' }}</div>
                </td>
                <td>
                  <span v-if="item.status === 'pending'" class="mv-pill mv-pill-danger">待洗版</span>
                  <span v-else-if="item.status === 'abandoned'" class="mv-pill mv-pill-warn">已放弃</span>
                  <span v-else class="mv-pill mv-pill-success">已洗版</span>
                </td>
                <td>
                  <span class="mv-note">{{ formatTime(item.updated_at) }}</span>
                </td>
                <td>
                  <el-button
                    v-if="item.status === 'pending'"
                    size="small"
                    link
                    type="warning"
                    @click="batchSetStatus('abandoned', [item.id])"
                  >
                    放弃
                  </el-button>
                  <el-button v-else size="small" link type="primary" @click="batchSetStatus('pending', [item.id])">
                    恢复
                  </el-button>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="!filteredItems.length" class="mv-empty">
            暂无条目。点击右上角「扫描全部账号」生成待洗版清单，或保存账号自动整理配置后点「立即扫描洗版」。
          </div>
        </div>

        <div class="mv-toolbar" style="margin-top: 14px">
          <el-button type="primary" :loading="washRunning" @click="runWashAll">
            <el-icon style="margin-right: 4px"><Refresh /></el-icon>
            一键洗版（整理新源）
          </el-button>
          <el-button @click="logsVisible = true">洗版日志</el-button>
          <el-button link type="danger" @click="clearLogs">清空日志</el-button>
          <div style="flex: 1"></div>
          <span class="mv-note">{{ washSummaryText }}</span>
        </div>
      </div>
    </section>

    <!-- ============ 每账号自动整理配置 ============ -->
    <section v-for="account in accounts" :key="account.id" class="mv-sec">
      <div class="mv-sec-head">
        <div style="display: flex; align-items: center; gap: 10px">
          <h3 class="mv-sec-title">{{ accountLabel(account) }}</h3>
          <span v-if="getConfig(account.id)?.enabled" class="mv-pill mv-pill-success">自动整理已启用</span>
          <span v-else class="mv-pill mv-pill-muted">未启用</span>
        </div>
        <div class="mv-sec-actions">
          <el-button size="small" type="primary" plain :loading="runningAccountId === account.id" @click="runNow(account.id)">
            立即整理
          </el-button>
          <el-button size="small" type="warning" plain :loading="scanningAccountId === account.id" @click="scanOne(account.id)">
            扫描洗版
          </el-button>
          <el-button
            size="small"
            v-if="getConfig(account.id)"
            type="danger"
            plain
            @click="removeConfig(account.id)"
          >
            清除配置
          </el-button>
        </div>
      </div>
      <div class="mv-sec-body">
        <div class="mv-fields">
          <div class="mv-field mv-field-wide">
            <div class="mv-field-label">自动整理开关</div>
            <div>
              <el-switch v-model="formOf(account.id).enabled" active-text="已启用" inactive-text="已禁用" />
            </div>
            <p class="mv-field-desc">启用后每 5 分钟扫描「待整理目录」新增资源；关闭不扫描（已保存配置保留）。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">待整理目录 <span style="color: var(--danger)">*</span></div>
            <div style="display: flex; gap: 8px">
              <el-input v-model="formOf(account.id).pending_dir" placeholder="例如 媒体库/待整理" class="mv-text" />
              <el-button @click="openPicker(account.id, 'pending_dir')">选择</el-button>
            </div>
            <p class="mv-field-desc">监控扫描目录：频道/订阅转存的落盘位置。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">已整理根目录</div>
            <div style="display: flex; gap: 8px">
              <el-input v-model="formOf(account.id).organized_root" placeholder="留空自动推导" class="mv-text" />
              <el-button @click="openPicker(account.id, 'organized_root')">选择</el-button>
            </div>
            <p class="mv-field-desc">整理到的根目录；留空使用「待整理目录同级/已整理」。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">失败目录</div>
            <div style="display: flex; gap: 8px">
              <el-input v-model="formOf(account.id).failed_dir" placeholder="留空自动创建" class="mv-text" />
              <el-button @click="openPicker(account.id, 'failed_dir')">选择</el-button>
            </div>
            <p class="mv-field-desc">识别失败 / TMDB 查不到的资源移入此目录。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">覆盖策略</div>
            <el-select :model-value="overwriteMode(account.id)" class="mv-select" @change="(v: string) => applyOverwriteMode(account.id, v)">
              <el-option label="跳过不覆盖（保留已有版本）" value="skip" />
              <el-option label="更优才覆盖（智能洗版，推荐）" value="wash" />
              <el-option label="任意覆盖（旧版本目录整体删除）" value="legacy" />
            </el-select>
            <p class="mv-field-desc">
              「更优才覆盖」按分辨率→编码→格式→色深→声道→制作组逐项评分，新源更优才替换；
              「任意覆盖」为旧版行为（谨慎：会删除整个旧版本目录，曾发生误删事故）。
            </p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">追更模式</div>
            <div>
              <el-switch v-model="formOf(account.id).track_renewal" active-text="开启" inactive-text="关闭" />
            </div>
            <p class="mv-field-desc">入库剧集 TMDB 判定未完结时，完成后推送「追更」提示。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">TMDB 低分过滤</div>
            <el-input-number
              v-model="formOf(account.id).min_tmdb_score"
              :min="0"
              :max="10"
              :step="0.5"
              :precision="1"
              controls-position="right"
              class="mv-num"
            />
            <p class="mv-field-desc">评分低于此值的同名内容跳过整理（0=关闭；默认 0）。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">落败新文件处置</div>
            <el-select v-model="formOf(account.id).loser_source_action" class="mv-select">
              <el-option label="保留在待整理目录（默认）" value="keep" />
              <el-option label="直接删除（避免反复比对）" value="delete" />
              <el-option label="移入归档目录" value="archive" />
            </el-select>
            <p class="mv-field-desc">新文件质量不及库内旧版时对源文件的处置（此文件不会被覆盖入库）。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">被替换旧版归档目录</div>
            <el-input v-model="formOf(account.id).loser_archive_dir" placeholder="如 洗版淘汰（留空=直接删除旧版）" class="mv-text" />
            <p class="mv-field-desc">旧版被更优新源替换后的去向：配置目录则移入归档备份，留空直接删除。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">制作组优先级</div>
            <el-input v-model="formOf(account.id).group_priority" placeholder="如 FRDS,HLJ,ZYX（逗号分隔，高→低）" class="mv-text" />
            <p class="mv-field-desc">质量评分相等时按此顺序裁定制作组，不在列表中的组最低。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">违规判定 · 最低分辨率</div>
            <el-select v-model="formOf(account.id).min_resolution" class="mv-select">
              <el-option label="不检查" :value="0" />
              <el-option label="720p" :value="720" />
              <el-option label="1080p（默认）" :value="1080" />
              <el-option label="2160p (4K)" :value="2160" />
            </el-select>
            <p class="mv-field-desc">扫描时分辨率低于该值的视频判定为「待洗版」。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">违规判定 · 首选编码</div>
            <el-input v-model="formOf(account.id).preferred_codecs" placeholder="hevc,h265,av1" class="mv-text" />
            <p class="mv-field-desc">编码不在该列表（逗号分隔）的视频视为「待洗版」；留空不检查。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">定时扫描 Cron</div>
            <el-input v-model="formOf(account.id).wash_scan_cron" placeholder="0 3 * * *（每日凌晨 3 点）" class="mv-text" />
            <p class="mv-field-desc">标准 5 段 cron；留空停用定时扫描（可手动扫描）。</p>
          </div>

          <div class="mv-field">
            <div class="mv-field-label">扫描后自动洗版</div>
            <div>
              <el-switch v-model="formOf(account.id).wash_scan_auto" active-text="开启" inactive-text="关闭" />
            </div>
            <p class="mv-field-desc">定时扫描完成后自动执行一轮整理洗版（消费待整理目录中的新源）。</p>
          </div>

          <div class="mv-field mv-field-wide">
            <div class="mv-field-label">自定义比较规则（JSON，留空=默认规则）</div>
            <div style="display: flex; gap: 8px; width: 100%">
              <el-input v-model="formOf(account.id).wash_rules_json" type="textarea" :rows="3" class="mv-text" placeholder='[{"field":"resolution","higher":true},{"field":"codec","higher":true}]' />
              <el-button @click="fillDefaultRules(account.id)">默认规则</el-button>
            </div>
            <p class="mv-field-desc">
              字段：resolution / codec / format / bitdepth / channels / group；higher=true 表示新源该字段越高/越优越先。
            </p>
          </div>

          <div class="mv-field mv-field-wide">
            <div class="mv-field-label">屏蔽词正则（每行一条，命中跳过整理）</div>
            <el-input
              v-model="formOf(account.id).blocked_words"
              type="textarea"
              :rows="3"
              placeholder="如 \b(SP|NCED|PV|CM|特典|Preview|Trailer)\b"
              class="mv-text"
            />
            <p class="mv-field-desc">垃圾内容（预告/特典/花絮等）命中后原地跳过，不移动不整理。</p>
          </div>

          <div class="mv-field mv-field-wide">
            <div class="mv-field-label">定制词表（逗号分隔，识别前剥离）</div>
            <el-input v-model="formOf(account.id).customization_words" placeholder="如 Baha,NF,Disney+" class="mv-text" />
            <p class="mv-field-desc">平台/定制词从文件名剥离，避免污染标题与目录名；留空用默认词表。</p>
          </div>

          <div class="mv-field mv-field-wide">
            <div class="mv-field-label">电影命名模板（Jinja2，留空=内置默认）</div>
            <div style="display: flex; gap: 8px; width: 100%">
              <el-input
                v-model="formOf(account.id).movie_name_template"
                type="textarea"
                :rows="2"
                placeholder="内置默认：{{ title }}{% if year %} ({{ year }}){% endif %}{% if tags %}.{{ tags }}{% endif %}{{ ext }}"
                class="mv-text"
              />
              <el-button @click="fillMovieTemplate(account.id)">默认</el-button>
            </div>
            <p class="mv-field-desc">可用变量：title / year / tmdbid / season / episode / s / e / ep / tags / resolution / codec / audio / format / hdr / bitdepth / edition / group / customization / ext</p>
          </div>

          <div class="mv-field mv-field-wide">
            <div class="mv-field-label">剧集命名模板（Jinja2，留空=内置默认）</div>
            <div style="display: flex; gap: 8px; width: 100%">
              <el-input
                v-model="formOf(account.id).tv_name_template"
                type="textarea"
                :rows="2"
                placeholder="内置默认：{{ title }}.{{ year }}.{{ s }}{{ e }}.第{{ ep }}集{{ ext }}"
                class="mv-text"
              />
              <el-button @click="fillTvTemplate(account.id)">默认</el-button>
            </div>
            <p class="mv-field-desc">同上变量；s/e 为 S01/E05 格式，ep 为集号。</p>
          </div>
        </div>

        <div class="mv-savebar">
          <div class="mv-savebar-left">
            <span v-if="saveState[account.id] === 'ok'" class="mv-save-ok">✓ 已保存</span>
            <span v-else-if="saveState[account.id] === 'err'" class="mv-save-err">✗ 保存失败</span>
          </div>
          <el-button type="primary" :loading="savingAccountId === account.id" @click="saveConfig(account.id)">
            保存配置
          </el-button>
        </div>

        <el-collapse v-if="getConfig(account.id)?.last_result || getConfig(account.id)?.last_wash_scan_result" style="margin-top: 14px">
          <el-collapse-item title="最近一次整理 / 扫描结果" name="r">
            <template v-if="getConfig(account.id)?.last_result">
              <div class="mv-note">运行时间：{{ formatTime(getConfig(account.id)?.last_run_at) }}</div>
              <div v-for="(line, idx) in parseResult(getConfig(account.id)?.last_result).details || []" :key="'d' + idx" class="mv-note">
                {{ line }}
              </div>
              <div class="mv-note" style="margin-top: 4px">
                整理成功 {{ parseResult(getConfig(account.id)?.last_result).organized || 0 }}，识别失败
                {{ parseResult(getConfig(account.id)?.last_result).unrecognized || 0 }}，跳过
                {{ parseResult(getConfig(account.id)?.last_result).skipped_overwrite || 0 }}，失败
                {{ parseResult(getConfig(account.id)?.last_result).failed || 0 }}
              </div>
            </template>
            <template v-if="getConfig(account.id)?.last_wash_scan_result">
              <el-divider style="margin: 8px 0" />
              <div class="mv-note">
                违规扫描：扫描视频 {{ scanSummary(account.id).scanned_files ?? '-' }} 个，判定待洗版
                {{ scanSummary(account.id).violation_num ?? '-' }} 个，清除条目
                {{ scanSummary(account.id).clean_removed ?? '-' }} 个
                <template v-if="scanSummary(account.id).errors">
                  ，异常 {{ scanSummary(account.id).errors }} 处
                </template>
              </div>
              <div v-for="(line, idx) in scanSummary(account.id).details || []" :key="'s' + idx" class="mv-note">
                {{ line }}
              </div>
            </template>
          </el-collapse-item>
        </el-collapse>
      </div>
    </section>

    <!-- 目录选择器 -->
    <el-dialog
      v-model="pickerVisible"
      :title="`选择 ${sourceName} 目录`"
      :width="checkIsMobile ? '92%' : '620px'"
      :close-on-click-modal="false"
      @closed="pickerField = ''"
    >
      <div v-if="pickerLabel" class="picker-form-label">保存到字段：{{ pickerLabel }}</div>
      <DirectorySelector
        :key="`${pickerAccountId}-${pickerField}-${pickerRefreshKey}`"
        v-model="pickerDir"
        :source-type="sourceType"
        :account-id="pickerAccountId"
        @select="onDirPicked"
        @cancel="pickerVisible = false"
      />
    </el-dialog>

    <!-- 洗版日志 -->
    <el-drawer v-model="logsVisible" title="洗版日志" size="560px">
      <div class="mv-toolbar">
        <el-select v-model="logAccountId" size="small" style="width: 180px">
          <el-option label="全部账号" :value="0" />
          <el-option v-for="a in accounts" :key="a.id" :label="accountLabel(a)" :value="a.id" />
        </el-select>
        <el-button size="small" @click="loadLogs">刷新</el-button>
        <el-button size="small" link type="danger" @click="clearLogs">清空</el-button>
      </div>
      <div v-if="logs.length" class="log-list">
        <div v-for="log in logs" :key="log.id" class="log-item">
          <div style="display: flex; justify-content: space-between; gap: 8px">
            <strong style="font-size: 13px">{{ logActionLabel(log.action) }}</strong>
            <span class="mv-note">{{ formatTime(log.event_time) }}</span>
          </div>
          <div class="mv-note">{{ log.title }}{{ log.season_num ? ` S${pad(log.season_num)}` : '' }}{{ log.episode_num ? `E${pad(log.episode_num)}` : '' }}</div>
          <div class="mv-note" style="word-break: break-all">{{ log.target_path }}</div>
          <div v-if="log.old_quality || log.new_quality" class="mv-note" style="word-break: break-all">
            旧：{{ log.old_quality || '-' }} → 新：{{ log.new_quality || '-' }}
          </div>
          <div v-if="log.message" class="mv-note" style="word-break: break-all">{{ log.message }}</div>
        </div>
      </div>
      <div v-else class="mv-empty">暂无洗版日志</div>
    </el-drawer>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, computed, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import { isMobile } from '@/utils/deviceUtils'
import DirectorySelector from '../DirectorySelector.vue'
import type { DirInfo } from '@/typing'

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
  wash_compare: boolean
  loser_source_action: string
  loser_archive_dir: string
  group_priority: string
  wash_rules_json: string
  min_resolution: number
  preferred_codecs: string
  wash_scan_cron: string
  wash_scan_auto: boolean
  blocked_words: string
  customization_words: string
  movie_name_template: string
  tv_name_template: string
  min_tmdb_score: number
  track_renewal: boolean
  last_wash_scan_at?: string
  last_wash_scan_result?: string
  last_run_at?: string
  last_result?: string
}

interface WashItem {
  id: number
  account_id: number
  rel_path: string
  file_name: string
  media_type: string
  title: string
  year: number
  season_num: number
  episode_num: number
  tmdb_id: number
  res_tag: string
  codec_tag: string
  audio_tag: string
  channels: number
  violations: string
  status: string
  updated_at: string
}

interface WashStatRow {
  account_id: number
  pending: number
  abandoned: number
  washed: number
}

interface WashLog {
  id: number
  account_id: number
  action: string
  title: string
  season_num: number
  episode_num: number
  target_path: string
  old_quality: string
  new_quality: string
  message: string
  event_time: string
}

interface WashSummary {
  account_id?: number
  org_root?: string
  scanned_files?: number
  violation_num?: number
  clean_removed?: number
  errors?: number
  details?: string[]
}

const http = useHttpClient()
const checkIsMobile = ref(isMobile())
const loadingAccounts = ref(false)
const accounts = ref<NetdiskAccount[]>([])
const configs = ref<AutoOrganizeConfig[]>([])
const savingAccountId = ref(0)
const runningAccountId = ref(0)
const saveState = reactive<Record<number, string>>({})

// ---- 洗版工作台状态 ----
const loadingWash = ref(false)
const scanning = ref(false)
const scanningAccountId = ref(0)
const washRunning = ref(false)
const washAccountId = ref(0)
const washStatus = ref('')
const washItems = ref<WashItem[]>([])
const washStats = ref<WashStatRow[]>([])
const selection = ref<number[]>([])
const logsVisible = ref(false)
const logAccountId = ref(0)
const logs = ref<WashLog[]>([])

// 目录选择器状态
const pickerVisible = ref(false)
const pickerAccountId = ref(0)
const pickerField = ref('')
const pickerRefreshKey = ref(0)
const pickerDir = ref<DirInfo | null>(null)
const pickerLabel = computed(() => {
  switch (pickerField.value) {
    case 'pending_dir':
      return '待整理目录'
    case 'organized_root':
      return '已整理根目录'
    case 'failed_dir':
      return '失败目录'
    default:
      return ''
  }
})

const accountLabel = (a: NetdiskAccount) => a.name || a.username || `账号 ${a.id}`

const defaultRules = JSON.stringify(
  [
    { field: 'resolution', higher: true },
    { field: 'codec', higher: true },
    { field: 'format', higher: true },
    { field: 'bitdepth', higher: true },
    { field: 'channels', higher: true },
    { field: 'group', higher: true },
  ],
  null,
  2,
)

const defaultMovieTemplate = '{{ title }}{% if year %} ({{ year }}){% endif %}{% if tags %}.{{ tags }}{% endif %}{{ ext }}'
const defaultTvTemplate = '{{ title }}{% if year %}.{{ year }}{% endif %}.{{ s }}{{ e }}.第{{ ep }}集{% if tags %}.{{ tags }}{% endif %}{{ ext }}'

const getConfig = (accountId: number) => configs.value.find((c) => c.account_id === accountId)

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
      wash_compare: true,
      loser_source_action: 'keep',
      loser_archive_dir: '',
      group_priority: '',
      wash_rules_json: '',
      min_resolution: 1080,
      preferred_codecs: 'hevc,h265,av1',
      wash_scan_cron: '0 3 * * *',
      wash_scan_auto: false,
      blocked_words: '',
      customization_words: '',
      movie_name_template: '',
      tv_name_template: '',
      min_tmdb_score: 0,
      track_renewal: false,
    }
  }
  return form[accountId]
}

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

// 覆盖策略三档 ⇄ (overwrite, wash_compare)
const overwriteMode = (accountId: number): string => {
  const f = formOf(accountId)
  if (!f.overwrite) return 'skip'
  return f.wash_compare ? 'wash' : 'legacy'
}
const applyOverwriteMode = (accountId: number, mode: string) => {
  const f = formOf(accountId)
  if (mode === 'skip') {
    f.overwrite = false
    f.wash_compare = true
  } else if (mode === 'legacy') {
    f.overwrite = true
    f.wash_compare = false
  } else {
    f.overwrite = true
    f.wash_compare = true
  }
}

const fillDefaultRules = (accountId: number) => {
  formOf(accountId).wash_rules_json = defaultRules
}
const fillMovieTemplate = (accountId: number) => {
  formOf(accountId).movie_name_template = defaultMovieTemplate
}
const fillTvTemplate = (accountId: number) => {
  formOf(accountId).tv_name_template = defaultTvTemplate
}

// ---- 加载 ----
const loadData = async () => {
  loadingAccounts.value = true
  try {
    const accountResp = await http.get(`${SERVER_URL}/account/list`)
    const allAccounts = (accountResp.data.data || []) as NetdiskAccount[]
    accounts.value = allAccounts.filter((a) => a.source_type === props.sourceType)
    const configResp = await http.get(`${SERVER_URL}/auto-organize/configs`)
    configs.value = ((configResp.data.data || []) as AutoOrganizeConfig[]).filter((c) =>
      accounts.value.some((a) => a.id === c.account_id),
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
        wash_compare: c.wash_compare ?? true,
        loser_source_action: c.loser_source_action || 'keep',
        loser_archive_dir: c.loser_archive_dir || '',
        group_priority: c.group_priority || '',
        wash_rules_json: c.wash_rules_json || '',
        min_resolution: c.min_resolution ?? 1080,
        preferred_codecs: c.preferred_codecs ?? 'hevc,h265,av1',
        wash_scan_cron: c.wash_scan_cron || '',
        wash_scan_auto: c.wash_scan_auto || false,
        blocked_words: c.blocked_words || '',
        customization_words: c.customization_words || '',
        movie_name_template: c.movie_name_template || '',
        tv_name_template: c.tv_name_template || '',
        min_tmdb_score: c.min_tmdb_score ?? 0,
        track_renewal: c.track_renewal || false,
        last_wash_scan_at: c.last_wash_scan_at,
        last_wash_scan_result: c.last_wash_scan_result,
        last_run_at: c.last_run_at,
        last_result: c.last_result,
      }
    }
  } catch (error) {
    console.error('加载自动整理配置错误：', error)
  } finally {
    loadingAccounts.value = false
  }
}

// 仅刷新运行结果字段，不覆盖正在编辑的表单项
const refreshRunResult = async () => {
  try {
    const resp = await http.get(`${SERVER_URL}/auto-organize/configs`)
    const all = (resp.data.data || []) as AutoOrganizeConfig[]
    configs.value = all.filter((c) => accounts.value.some((a) => a.id === c.account_id))
    for (const c of configs.value) {
      const f = form[c.account_id]
      if (f) {
        f.id = c.id
        f.last_run_at = c.last_run_at
        f.last_result = c.last_result
        f.last_wash_scan_at = c.last_wash_scan_at
        f.last_wash_scan_result = c.last_wash_scan_result
      }
    }
  } catch (error) {
    console.error('刷新整理结果失败：', error)
  }
}

// ---- 保存 / 删除配置 ----
const saveConfig = async (accountId: number) => {
  const f = formOf(accountId)
  if (!f.pending_dir.trim()) {
    ElMessage.warning('请填写待整理目录')
    return
  }
  const existing = getConfig(accountId)
  savingAccountId.value = accountId
  try {
    const resp = await http.post(`${SERVER_URL}/auto-organize/config`, {
      id: existing?.id || 0,
      account_id: accountId,
      enabled: f.enabled,
      pending_dir: f.pending_dir.trim(),
      organized_root: f.organized_root.trim(),
      failed_dir: f.failed_dir.trim(),
      category_config: f.category_config,
      overwrite: f.overwrite,
      wash_compare: f.wash_compare,
      loser_source_action: f.loser_source_action,
      loser_archive_dir: f.loser_archive_dir.trim(),
      group_priority: f.group_priority.trim(),
      wash_rules_json: f.wash_rules_json,
      min_resolution: f.min_resolution ?? 0,
      preferred_codecs: f.preferred_codecs.trim(),
      wash_scan_cron: f.wash_scan_cron.trim(),
      wash_scan_auto: f.wash_scan_auto,
      blocked_words: f.blocked_words,
      customization_words: f.customization_words.trim(),
      movie_name_template: f.movie_name_template,
      tv_name_template: f.tv_name_template,
      min_tmdb_score: f.min_tmdb_score || 0,
      track_renewal: f.track_renewal,
      // 运行时字段回传，避免全量保存覆盖
      ...(existing?.last_run_at ? { last_run_at: existing.last_run_at } : {}),
      ...(existing?.last_result ? { last_result: existing.last_result } : {}),
      ...(existing?.last_wash_scan_at ? { last_wash_scan_at: existing.last_wash_scan_at } : {}),
      ...(existing?.last_wash_scan_result ? { last_wash_scan_result: existing.last_wash_scan_result } : {}),
    })
    if (resp.data.code === 200) {
      ElMessage.success('配置已保存')
      saveState[accountId] = 'ok'
      await loadData()
      await loadWashAll()
    } else {
      saveState[accountId] = 'err'
      ElMessage.error(resp.data.message || '保存失败')
    }
  } catch (error) {
    console.error(error)
    saveState[accountId] = 'err'
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

// ---- 手动整理 ----
const runNow = async (accountId: number) => {
  runningAccountId.value = accountId
  const before = getConfig(accountId)?.last_run_at || ''
  try {
    const resp = await http.post(`${SERVER_URL}/auto-organize/run`, { account_id: accountId })
    if (resp.data.code === 200) {
      ElMessage.success('已开始整理，正在后台执行…')
      await waitForRunResult(accountId, before)
    } else {
      ElMessage.error(resp.data.message || '整理失败')
    }
  } catch (error) {
    console.error(error)
    ElMessage.error('整理请求失败，请稍后重试')
  } finally {
    runningAccountId.value = 0
  }
}

const waitForRunResult = async (accountId: number, before: string) => {
  for (let i = 0; i < 50; i++) {
    await new Promise((r) => setTimeout(r, 3000))
    await refreshRunResult()
    const cfg = getConfig(accountId)
    if (cfg?.last_run_at && cfg.last_run_at !== before) {
      const res = parseResult(cfg.last_result)
      const total =
        (res.organized || 0) + (res.unrecognized || 0) + (res.moved_to_failed || 0) + (res.failed || 0) + (res.skipped_overwrite || 0)
      if (total === 0) {
        ElMessage.info('整理完成：待整理目录为空或全部跳过')
      } else {
        const parts = [`整理完成：成功 ${res.organized || 0} 个`]
        if (res.unrecognized) parts.push(`识别失败 ${res.unrecognized} 个`)
        if (res.moved_to_failed) parts.push(`移入失败目录 ${res.moved_to_failed} 个`)
        if (res.skipped_overwrite) parts.push(`跳过 ${res.skipped_overwrite} 个`)
        if (res.failed) parts.push(`失败 ${res.failed} 个`)
        ElMessage.success(parts.join('，'))
      }
      return
    }
  }
  ElMessage.info('整理任务仍在后台执行，可稍后点击「刷新」查看')
}

// ---- 洗版工作台 ----
const loadWashAll = async () => {
  loadingWash.value = true
  try {
    const itemsResp = await http.get(`${SERVER_URL}/wash/items`, {
      params: { account_id: washAccountId.value || undefined, status: washStatus.value || undefined },
    })
    washItems.value = (itemsResp.data.data || []) as WashItem[]
    const statsResp = await http.get(`${SERVER_URL}/wash/stats`)
    washStats.value = (statsResp.data.data || []) as WashStatRow[]
  } catch (error) {
    console.error('加载洗版清单失败：', error)
  } finally {
    loadingWash.value = false
  }
}

const totalPending = computed(() => washStats.value.reduce((s, r) => s + r.pending, 0))
const totalAbandoned = computed(() => washStats.value.reduce((s, r) => s + r.abandoned, 0))
const totalWashed = computed(() => washStats.value.reduce((s, r) => s + r.washed, 0))

const filteredItems = computed(() =>
  washItems.value.filter((i) => {
    if (washAccountId.value && i.account_id !== washAccountId.value) return false
    if (washStatus.value && i.status !== washStatus.value) return false
    return true
  }),
)

const allSelected = computed(() => filteredItems.value.length > 0 && filteredItems.value.every((i) => selection.value.includes(i.id)))
const toggleAll = (v: boolean) => {
  selection.value = v ? filteredItems.value.map((i) => i.id) : []
}
const toggleSelect = (id: number, v: boolean) => {
  const idx = selection.value.indexOf(id)
  if (v && idx < 0) selection.value.push(id)
  if (!v && idx >= 0) selection.value.splice(idx, 1)
}

const scanOne = async (accountId: number) => {
  scanningAccountId.value = accountId
  try {
    const resp = await http.post(`${SERVER_URL}/wash/scan`, { account_id: accountId })
    if (resp.data.code === 200) {
      ElMessage.success('扫描已开始（后台执行，完成后有系统通知）')
      await refreshRunResult()
      setTimeout(() => loadWashAll(), 5000)
    } else {
      ElMessage.error(resp.data.message || '扫描启动失败')
    }
  } catch (error) {
    console.error(error)
    ElMessage.error('扫描启动失败')
  } finally {
    scanningAccountId.value = 0
  }
}

const scanAll = async () => {
  const cfgAccounts = accounts.value.filter((a) => getConfig(a.id))
  if (!cfgAccounts.length) {
    ElMessage.warning('请先保存任一账号的自动整理配置（含已整理根目录），再进行违规扫描')
    return
  }
  scanning.value = true
  try {
    let ok = 0
    for (const a of cfgAccounts) {
      const resp = await http.post(`${SERVER_URL}/wash/scan`, { account_id: a.id })
      if (resp.data.code === 200) ok++
    }
    ElMessage.success(`已提交 ${ok}/${cfgAccounts.length} 个账号的违规扫描（后台执行）`)
    await refreshRunResult()
    setTimeout(() => loadWashAll(), 8000)
  } catch (error) {
    ElMessage.error('扫描启动失败')
  } finally {
    scanning.value = false
  }
}

const batchSetStatus = async (status: string, ids?: number[]) => {
  const target = ids || selection.value
  if (!target.length) return
  if (status === 'abandoned') {
    try {
      await ElMessageBox.confirm(`放弃 ${target.length} 个条目？放弃后重新扫描仍保留（不会自动洗版）。`, '放弃待洗版', { type: 'warning' })
    } catch {
      return
    }
  }
  try {
    const resp = await http.post(`${SERVER_URL}/wash/items/status`, { ids: target, status })
    if (resp.data.code === 200) {
      ElMessage.success(status === 'abandoned' ? '已放弃' : '已恢复为待洗版')
      selection.value = []
      await loadWashAll()
    } else {
      ElMessage.error(resp.data.message || '更新失败')
    }
  } catch (error) {
    ElMessage.error('更新失败')
  }
}

const runWashAll = async () => {
  const target = accounts.value.filter((a) => getConfig(a.id))
  if (!target.length) {
    ElMessage.warning('请先保存自动整理配置')
    return
  }
  try {
    await ElMessageBox.confirm(
      '一键洗版将执行一轮自动整理，只处理「待整理目录」中的新源；新源质量更优才覆盖库内旧版（更优才覆盖模式）。',
      '一键洗版',
      { type: 'info' },
    )
  } catch {
    return
  }
  washRunning.value = true
  try {
    let ok = 0
    for (const a of target) {
      const resp = await http.post(`${SERVER_URL}/wash/run`, { account_id: a.id })
      if (resp.data.code === 200) ok++
    }
    ElMessage.success(`已开始 ${ok}/${target.length} 个账号的洗版整理（后台执行）`)
  } catch (error) {
    ElMessage.error('洗版启动失败')
  } finally {
    washRunning.value = false
  }
}

const loadLogs = async () => {
  try {
    const resp = await http.get(`${SERVER_URL}/wash/logs`, {
      params: { account_id: logAccountId.value || undefined, limit: 200 },
    })
    logs.value = (resp.data.data || []) as WashLog[]
  } catch (error) {
    ElMessage.error('加载日志失败')
  }
}

const clearLogs = async () => {
  try {
    await ElMessageBox.confirm('确定清空洗版日志吗？', '清空日志', { type: 'warning' })
  } catch {
    return
  }
  try {
    await http.delete(`${SERVER_URL}/wash/logs`, { params: { account_id: logAccountId.value || undefined } })
    ElMessage.success('已清空')
    logs.value = []
  } catch (error) {
    ElMessage.error('清空失败')
  }
}

// ---- 目录选择器 ----
const openPicker = (accountId: number, field: 'pending_dir' | 'organized_root' | 'failed_dir') => {
  pickerAccountId.value = accountId
  pickerField.value = field
  pickerDir.value = null
  pickerRefreshKey.value++
  pickerVisible.value = true
}

const onDirPicked = () => {
  const f = formOf(pickerAccountId.value)
  if (pickerDir.value?.path) {
    const field = pickerField.value as 'pending_dir' | 'organized_root' | 'failed_dir'
    f[field] = pickerDir.value.path
    ElMessage.success(`已选择：${pickerDir.value.path}`)
  }
  pickerVisible.value = false
}

// ---- 工具 ----
const parseResult = (raw?: string) => {
  if (!raw) return {} as any
  try {
    return JSON.parse(raw)
  } catch {
    return {}
  }
}

const scanSummary = (accountId: number): WashSummary => {
  const raw = getConfig(accountId)?.last_wash_scan_result
  if (!raw) return {}
  try {
    return JSON.parse(raw) as WashSummary
  } catch {
    return {}
  }
}

const lastScanTimeText = computed(() => {
  const times = configs.value.map((c) => c.last_wash_scan_at).filter((t): t is string => !!t)
  if (!times.length) return '-'
  const latest = times.sort().reverse()[0]
  const d = new Date(latest)
  if (isNaN(d.getTime())) return '-'
  return `${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
})

const washSummaryText = computed(() => {
  const items = washStats.value
  if (!items.length) return '上次扫描：未扫描'
  const scanned = configs.value
    .map((c) => scanSummary(c.account_id).scanned_files)
    .filter((n): n is number => typeof n === 'number')
  const total = scanned.reduce((s, n) => s + n, 0)
  return total > 0 ? `上次扫描共 ${total} 个视频文件` : '上次扫描：未扫描'
})

const logActionLabel = (action: string) => {
  const map: Record<string, string> = {
    wash_replace: '洗版替换（新源更优）',
    wash_no_better: '洗版跳过（新源不更优）',
    wash_skip: '洗版跳过',
    wash_archive: '旧版归档',
    loser_source_deleted: '落败新源已删除',
    scan_summary: '违规扫描',
    filter_blocked: '屏蔽词拦截',
    score_filter: '低分过滤',
    renewal_tip: '追更提示',
  }
  return map[action] || action
}

const formatTime = (raw?: string) => {
  if (!raw) return '-'
  return raw.replace('T', ' ').slice(0, 19)
}

const pad = (n?: number) => (n && n > 0 ? String(n).padStart(2, '0') : '')

onMounted(async () => {
  await loadData()
  await loadWashAll()
})
</script>

<style scoped>
.picker-form-label {
  font-size: 13px;
  color: var(--text-muted);
  margin-bottom: 8px;
}

.log-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.log-item {
  border: 1px solid var(--border-soft);
  border-radius: var(--radius-md);
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 3px;
  background: var(--surface-sunken);
}
</style>