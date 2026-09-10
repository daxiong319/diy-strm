package moviepilot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
)

// washDecision 洗版比较结果
type washDecision struct {
	proceed     bool     // 是否继续放置新文件到目标目录
	skipMessage string   // proceed=false 时的原因（供调用方写入明细）
	treatments  []string // 处置摘要（供调用方写入明细）
	// 旧文件处置延后执行：删除/归档在新文件成功移入后由调用方调 applyDeferredWashLosers，
	// 避免「先删旧、移动失败」导致库内缺集
	pendingLosers []washLoserOp
	pendingLogs   []*models.WashLog
}

// washLoserOp 一条延后执行的旧文件处置动作
type washLoserOp struct {
	entry  organizeEntry
	action string // delete / archive
	log    *models.WashLog
	tm     time.Time
}

// splitWordList 拆分逗号/换行分隔的词表（清洗空白与空项）
func splitWordList(raw string) []string {
	raw = strings.NewReplacer("\r", "", "\n", ",").Replace(raw)
	var out []string
	for _, w := range strings.Split(raw, ",") {
		w = strings.TrimSpace(w)
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// matchWashWords 检测文件名/标题中是否命中词表；返回命中的词，未命中返回空串
func matchWashWords(rawWords string, candidates ...string) string {
	words := splitWordList(rawWords)
	if len(words) == 0 {
		return ""
	}
	for _, w := range words {
		wl := strings.ToLower(w)
		for _, c := range candidates {
			if c == "" {
				continue
			}
			if strings.Contains(strings.ToLower(c), wl) {
				return w
			}
		}
	}
	return ""
}

// splitGroupPriority 解析制作组优先级列表（逗号/换行分隔，越靠前越高）
func splitGroupPriority(raw string) []string {
	return splitWordList(raw)
}

// washCompareAndApply 洗版比较与处置（P0-1/P0-2/P0-3）：
//   - 目标影片目录已存在（existingBaseID != ""）时由调用方在放置新文件前调用；
//   - 对目录内旧视频按「同名/同集」（忽略扩展名与质量后缀）匹配；
//   - 新文件质量更优 → 旧文件按 loser_source_action 处置（keep=保留共存 / delete=删除 / archive=归档到 loser_archive_dir），落 wash_replace 日志；
//   - 新文件质量不高于现版本 → 不对现库做任何改动，新文件按 loser_source_action 处置
//     （keep=留在待整理目录 / delete=删除新文件 / archive=归档新文件），落 wash_no_better 日志。
//
// 返回 proceed=false 表示调用方不应再把新文件移动进目标目录。
func washCompareAndApply(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig, newEntry *organizeEntry, media *IdentifyResult, officialTitle string, year int, tmdbID int64, relDir, newName string, newQ *FileQuality, entries []organizeEntry) washDecision {
	if cfg == nil || newEntry == nil || newQ == nil {
		return washDecision{proceed: true}
	}
	rules := ParseWashRules(cfg.WashRulesJSON)
	groupPrio := splitGroupPriority(cfg.GroupPriority)
	targets := findWashTargets(newName, newQ, entries)
	decision := washDecision{proceed: true}
	loserTreated := cfg.LoserSourceAction
	if loserTreated == "" {
		loserTreated = "keep"
	}

	if len(targets) == 0 {
		// 未匹配到同名/同集旧文件：直接放置（多版本共存或新增集数，均不动旧文件）
		return washDecision{proceed: true}
	}

	newBetter := true
	loserIdx := -1
	var loserQ *FileQuality
	for _, idx := range targets {
		old := &entries[idx]
		oldQ := ParseQualityFromName(old.Name)
		cmp := CompareQuality(newQ, oldQ, groupPrio, rules)
		if cmp <= 0 {
			newBetter = false
			loserIdx = idx
			loserQ = oldQ
			break
		}
	}
	if !newBetter {
		// 新文件更差/持平：处置新文件（默认 keep 留在待整理目录，绝不自动删源）
		// 消息带上与现库文件（哪一集、哪个版本）的逐项对比，归因一目了然
		return washHandleNewLoser(ctx, account, cfg, newEntry, media, officialTitle, year, tmdbID, relDir, &entries[loserIdx], loserQ, newQ, groupPrio, rules)
	}

	// 新文件更优：不立即处置旧文件（落败方），生成延后动作清单，
	// 调用方在新文件成功移入目标目录后执行 applyDeferredWashLosers。
	// （原先「先删旧后移新」：移动失败时旧文件已删，库内缺集）
	treated := len(targets)
	treatmentCounts := make(map[string]int) // 配置动作计数（delete/archive/keep，按配置而非执行结果）
	const maxTreatDetail = 3
	for _, idx := range targets {
		old := &entries[idx]
		oldQ := ParseQualityFromName(old.Name)
		trace := qualityCompareTrace(newQ, oldQ, groupPrio, rules)
		treatmentCounts[loserTreated]++
		decision.pendingLosers = append(decision.pendingLosers, washLoserOp{
			entry:  *old,
			action: loserTreated,
			log: &models.WashLog{
				AccountID:    cfg.AccountID,
				Action:       "wash_replace",
				TargetPath:   relDir,
				Title:        officialTitle,
				MediaType:    media.Category,
				SeasonNum:    media.Season,
				EpisodeNum:   media.Episode,
				TMDBID:       tmdbID,
				OldName:      old.Name,
				OldQuality:   oldQ.Summary(),
				NewName:      newName,
				NewQuality:   newQ.Summary(),
				LoserTreated: loserTreated,
				Message: fmt.Sprintf("洗版替换%s：新版「%s」%s vs 旧版「%s」%s：%s → 新版更优，旧版待新版就位后%s",
					washEpisodeLabel(newName), newName, sizeGBText(newEntry.Size), old.Name, sizeGBText(old.Size), trace, washLoserActionDesc(loserTreated, loserTreated)),
				EventTime: time.Now(),
			},
		})
		if len(decision.treatments) < maxTreatDetail {
			decision.treatments = append(decision.treatments, fmt.Sprintf("洗版替换%s：新版「%s」%s vs 旧版「%s」%s：%s → 旧版待新版就位后%s",
				washEpisodeLabel(newName), newName, sizeGBText(newEntry.Size), old.Name, sizeGBText(old.Size), trace, washLoserActionDesc(loserTreated, loserTreated)))
		}
	}
	if treated > maxTreatDetail {
		decision.treatments = append(decision.treatments, fmt.Sprintf("洗版替换：其余 %d 个旧版本同批处置（明细见洗版记录）", treated-maxTreatDetail))
	}
	decision.treatments = append(decision.treatments, fmt.Sprintf("洗版替换：共匹配 %d 个旧版本（%s）", treated, func() string {
		summaries := make([]string, 0, len(treatmentCounts))
		for k, v := range treatmentCounts {
			summaries = append(summaries, fmt.Sprintf("%s×%d", k, v))
		}
		return strings.Join(summaries, "，")
	}()))
	return decision
}

// washEpisodeLabel 从文件名提取「S01E08」式集号标签，无则空串（洗版日志定位用）
func washEpisodeLabel(fileName string) string {
	if ep := episodeKeyOf(fileName); ep != "" {
		return "（" + ep + "）"
	}
	return ""
}

// sizeGBText 字节数转「3.21GB」展示文本
func sizeGBText(n int64) string {
	return fmt.Sprintf("%.2fGB", float64(n)/(1<<30))
}

// applyDeferredWashLosers 新文件成功移入后执行延后的旧文件处置（delete/archive）并落日志。
// 单个处置失败不影响其它：最坏结果是双份共存（可再次整理收敛），不会缺集。
func applyDeferredWashLosers(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig, decision washDecision) {
	for _, op := range decision.pendingLosers {
		actual := op.action
		switch op.action {
		case "delete":
			if err := deleteNetdiskFileInternal(account, op.entry.ID, op.entry.ParentID); err != nil {
				helpers.AppLogger.Warnf("洗版删除旧文件失败（账号 %d）：%s：%v", cfg.AccountID, op.entry.Name, err)
				actual = "delete_failed"
			}
		case "archive":
			targetID, err := washArchiveDirID(ctx, account, cfg)
			if err != nil || targetID == "" || targetID == "0" {
				helpers.AppLogger.Warnf("洗版归档旧文件失败（账号 %d）：归档目录不可用：%v", cfg.AccountID, err)
				actual = "archive_failed_keep"
			} else {
				if err := moveNetdiskFileInternal(account, op.entry.ID, op.entry.ParentID, targetID); err != nil {
					helpers.AppLogger.Warnf("洗版归档旧文件失败（账号 %d）：%s：%v", cfg.AccountID, op.entry.Name, err)
					actual = "archive_failed_keep"
				}
			}
		default: // keep：无需动作
		}
		if op.log != nil {
			op.log.LoserTreated = actual
			op.log.Message = fmt.Sprintf("新版本质量更优，旧文件%s", washLoserActionDesc(op.action, actual))
			_ = models.AddWashLog(op.log)
		}
	}
}

// washHandleNewLoser 新文件落败时的处置（默认 keep：留待整理目录，改由用户决定）。
// 消息带上与现库文件（哪一集、哪个版本）的逐项对比明细：哪集 vs 哪集、体积、
// 各维度（分辨率/编码/来源/声道/色深/组名）对比与决出项，归因一目了然。
func washHandleNewLoser(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig, newEntry *organizeEntry, media *IdentifyResult, officialTitle string, year int, tmdbID int64, relDir string, loser *organizeEntry, loserQ, newQ *FileQuality, groupPriority []string, rules []WashRule) washDecision {
	action := cfg.LoserSourceAction
	if action == "" {
		action = "keep"
	}
	if newQ == nil {
		newQ = ParseQualityFromName(newEntry.Name)
	}
	// 对比明细：新版/现版文件与体积 + 逐项质量对比
	trace := "现库版本质量不可解析（按可覆盖处理）"
	if loserQ != nil {
		trace = qualityCompareTrace(newQ, loserQ, groupPriority, rules)
	}
	loserName := ""
	loserSize := ""
	if loser != nil {
		loserName = loser.Name
		if loser.Size > 0 {
			loserSize = sizeGBText(loser.Size)
		}
	}
	newSize := ""
	if newEntry.Size > 0 {
		newSize = sizeGBText(newEntry.Size)
	}
	base := fmt.Sprintf("洗版判定%s：新版「%s」%s vs 现版「%s」%s：%s", washEpisodeLabel(newEntry.Name), newEntry.Name, newSize, loserName, loserSize, trace)
	actionTail := "，保留现有版本（新文件留在待整理目录）"
	if action == "delete" {
		// 用户显式配置 delete 时才删除来源文件（危险操作，仅按配置执行）
		if err := deleteNetdiskFileInternal(account, newEntry.ID, newEntry.ParentID); err == nil {
			actionTail = "，已按配置删除来源文件"
		} else {
			helpers.AppLogger.Warnf("洗版删除来源文件失败（账号 %d）：%s：%v", cfg.AccountID, newEntry.Name, err)
			actionTail = "，保留现有版本（来源文件删除失败已保留）"
		}
	} else if action == "archive" {
		targetID, err := washArchiveDirID(ctx, account, cfg)
		if err == nil && targetID != "" && targetID != "0" {
			if err := moveNetdiskFileInternal(account, newEntry.ID, newEntry.ParentID, targetID); err == nil {
				actionTail = fmt.Sprintf("，已按配置归档来源文件 → %s", cfg.LoserArchiveDir)
			} else {
				helpers.AppLogger.Warnf("洗版归档来源文件失败（账号 %d）：%s：%v", cfg.AccountID, newEntry.Name, err)
				actionTail = "，保留现有版本（来源文件归档失败已保留）"
			}
		} else {
			helpers.AppLogger.Warnf("洗版归档来源文件失败（账号 %d）：归档目录不可用：%v", cfg.AccountID, err)
			if err != nil {
				helpers.AppLogger.Errorf("洗版归档来源文件：%v", err)
			}
			actionTail = "，保留现有版本（来源文件归档失败已保留）"
		}
	}
	skipMessage := base + " → 质量不高于现版本" + actionTail
	oldName, oldQuality := "", ""
	if loser != nil {
		oldName = loser.Name
	}
	if loserQ != nil {
		oldQuality = loserQ.Summary()
	}
	_ = models.AddWashLog(&models.WashLog{
		AccountID:    cfg.AccountID,
		Action:       "wash_no_better",
		TargetPath:   relDir,
		Title:        officialTitle,
		MediaType:    media.Category,
		SeasonNum:    media.Season,
		EpisodeNum:   media.Episode,
		TMDBID:       tmdbID,
		OldName:      oldName,
		OldQuality:   oldQuality,
		NewName:      newEntry.Name,
		NewQuality:   newQ.Summary(),
		LoserTreated: "saved_source",
		Message:      skipMessage,
		EventTime:    time.Now(),
	})
	return washDecision{proceed: false, skipMessage: skipMessage}
}

// washArchiveDirID 解析（必要时创建）洗版归档目录，返回目录 ID
func washArchiveDirID(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig) (string, error) {
	dir := strings.Trim(cfg.LoserArchiveDir, "/")
	if dir == "" {
		return "", fmt.Errorf("未配置归档目录")
	}
	id, err := EnsureRemoteDir(ctx, account, dir)
	if err != nil {
		return "", err
	}
	if id == "" || id == "0" {
		// 部分网盘创建接口返回的 ID 不可靠，重新按路径解析
		if rid, fErr := findRemoteDirID(ctx, account, dir); fErr == nil && rid != "" && rid != "0" {
			return rid, nil
		}
		return "", fmt.Errorf("归档目录 ID 解析失败")
	}
	return id, nil
}

func washLoserActionDesc(configAction, actualAction string) string {
	switch actualAction {
	case "delete":
		return "已删除"
	case "archive":
		return "已归档"
	case "delete_failed", "archive_failed_keep":
		return fmt.Sprintf("处置失败已保留（按 %s 配置）", configAction)
	default:
		return "已保留（与新版共存）"
	}
}
