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

	if len(targets) == 0 {
		// 未匹配到同名/同集旧文件：直接放置（多版本共存或新增集数，均不动旧文件）
		return washDecision{proceed: true}
	}

	newBetter := true
	for _, idx := range targets {
		old := &entries[idx]
		oldQ := ParseQualityFromName(old.Name)
		cmp := CompareQuality(newQ, oldQ, groupPrio, rules)
		if cmp <= 0 {
			newBetter = false
			break
		}
	}
	if !newBetter {
		// 新文件更差/持平：处置新文件（默认 keep 留在待整理目录，绝不自动删源）
		return washHandleNewLoser(ctx, account, cfg, newEntry, media, officialTitle, year, tmdbID, relDir)
	}

	// 新文件更优：处置匹配到的旧文件（落败方）
	treated := 0
	treatmentLogs := make(map[string]int) // 处置类型计数（none/delete/archive）
	loserTreated := cfg.LoserSourceAction
	if loserTreated == "" {
		loserTreated = "keep"
	}
	var dbLogs []*models.WashLog
	for _, idx := range targets {
		old := &entries[idx]
		oldQ := ParseQualityFromName(old.Name)
		treated++
		action := loserTreated
		switch loserTreated {
		case "delete":
			if err := deleteNetdiskFileInternal(account, old.ID, old.ParentID); err != nil {
				helpers.AppLogger.Warnf("洗版删除旧文件失败（账号 %d）：%s：%v", cfg.AccountID, old.Name, err)
				action = "delete_failed"
			} else {
				treatmentLogs["delete"]++
			}
		case "archive":
			targetID, err := washArchiveDirID(ctx, account, cfg)
			if err != nil || targetID == "" || targetID == "0" {
				helpers.AppLogger.Warnf("洗版归档旧文件失败（账号 %d）：归档目录不可用：%v", cfg.AccountID, err)
				action = "archive_failed_keep"
			} else {
				if err := moveNetdiskFileInternal(account, old.ID, old.ParentID, targetID); err != nil {
					helpers.AppLogger.Warnf("洗版归档旧文件失败（账号 %d）：%s：%v", cfg.AccountID, old.Name, err)
					action = "archive_failed_keep"
				} else {
					treatmentLogs["archive"]++
				}
			}
		default: // keep
			treatmentLogs["keep"]++
		}
		msg := fmt.Sprintf("新版本质量更优，旧文件%s", washLoserActionDesc(loserTreated, action))
		dbLogs = append(dbLogs, &models.WashLog{
			AccountID:   cfg.AccountID,
			Action:      "wash_replace",
			TargetPath:  relDir,
			Title:       officialTitle,
			MediaType:   media.Category,
			SeasonNum:   media.Season,
			EpisodeNum:  media.Episode,
			TMDBID:      tmdbID,
			OldName:     old.Name,
			OldQuality:  oldQ.Summary(),
			NewName:     newName,
			NewQuality:  newQ.Summary(),
			LoserTreated: action,
			Message:     msg,
			EventTime:   time.Now(),
		})
	}
	for _, l := range dbLogs {
		_ = models.AddWashLog(l)
	}

	summaries := make([]string, 0, len(treatmentLogs))
	for k, v := range treatmentLogs {
		summaries = append(summaries, fmt.Sprintf("%s×%d", k, v))
	}
	if treated > 0 {
		return washDecision{
			proceed:    true,
			treatments: []string{fmt.Sprintf("洗版替换：匹配到 %d 个旧版本（%s）", treated, strings.Join(summaries, "，"))},
		}
	}
	return washDecision{proceed: true}
}

// washHandleNewLoser 新文件落败时的处置（默认 keep：留待整理目录，改由用户决定）
func washHandleNewLoser(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig, newEntry *organizeEntry, media *IdentifyResult, officialTitle string, year int, tmdbID int64, relDir string) washDecision {
	action := cfg.LoserSourceAction
	if action == "" {
		action = "keep"
	}
	newQ := ParseQualityFromName(newEntry.Name)
	skipMessage := "新版本质量不高于现版本，不覆盖（保留现有版本）"
	if action == "delete" {
		// 用户显式配置 delete 时才删除来源文件（危险操作，仅按配置执行）
		if err := deleteNetdiskFileInternal(account, newEntry.ID, newEntry.ParentID); err == nil {
			skipMessage = fmt.Sprintf("新版本质量不高于现版本，已按配置删除来源文件：%s", newEntry.Name)
		} else {
			helpers.AppLogger.Warnf("洗版删除来源文件失败（账号 %d）：%s：%v", cfg.AccountID, newEntry.Name, err)
		}
	} else if action == "archive" {
		targetID, err := washArchiveDirID(ctx, account, cfg)
		if err == nil && targetID != "" && targetID != "0" {
			if err := moveNetdiskFileInternal(account, newEntry.ID, newEntry.ParentID, targetID); err == nil {
				skipMessage = fmt.Sprintf("新版本质量不高于现版本，已按配置归档来源文件：%s → %s", newEntry.Name, cfg.LoserArchiveDir)
			} else {
				helpers.AppLogger.Warnf("洗版归档来源文件失败（账号 %d）：%s：%v", cfg.AccountID, newEntry.Name, err)
			}
		} else {
			helpers.AppLogger.Warnf("洗版归档来源文件失败（账号 %d）：归档目录不可用：%v", cfg.AccountID, err)
			if err != nil {
				helpers.AppLogger.Errorf("洗版归档来源文件：%v", err)
			}
		}
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
		OldName:      "",
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