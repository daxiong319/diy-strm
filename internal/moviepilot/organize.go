package moviepilot

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"litepan/internal/domain"
)

// OrganizeTarget 一个本地文件识别并规划后的整理目标。
type OrganizeTarget struct {
	// SourcePath 本地源文件绝对路径。
	SourcePath string
	// RelDir 目标相对目录（相对整理根，如「剧集/遮天 (2023) {tmdb=123}/Season 01」）。
	RelDir string
	// NewName 目标文件名（含扩展名）。
	NewName string
	// Media 识别结果。
	Media *IdentifyResult
	// Quality 新文件的质量画像。
	Quality *FileQuality
}

// OrganizePlan 一次整理规划的结果摘要。
type OrganizePlan struct {
	// Targets 规划成功的整理目标。
	Targets []OrganizeTarget
	// Failed 无法识别/无法规划的文件（源路径 → 原因）。
	Failed []FailedEntry
	// Skipped 未处理的文件（非视频/临时文件等）。
	Skipped []string
}

// FailedEntry 一条待记录的失败文件。
type FailedEntry struct {
	SourcePath string
	Reason     string
}

// organizeLocalFile 识别单个本地视频文件并规划整理目标（不落盘、不上传）。
// 返回 (规划结果, 是否成功)；失败时 error 说明原因，调用方据此记入失败文件表。
func (s *Service) organizeLocalFile(ctx context.Context, client *Client, srcPath, pendingRoot string) (*OrganizeTarget, error) {
	fileName := filepath.Base(srcPath)
	if !IsVideoFile(fileName) {
		return nil, fmt.Errorf("非视频文件")
	}
	dirName := filepath.Base(filepath.Dir(srcPath))
	// 目录名与文件名相同时（文件直接位于待整理根下）不作为识别上下文，避免把根目录名当标题
	if dirName == filepath.Base(strings.TrimRight(pendingRoot, "/")) {
		dirName = ""
	}
	media, ok := s.IdentifyLocalFile(ctx, client, fileName, dirName)
	if !ok || media == nil {
		return nil, errMediaUnrecognized
	}
	official := strings.TrimSpace(media.Title)
	if official == "" {
		return nil, errMediaUnrecognized
	}
	if media.TmdbId <= 0 {
		return nil, fmt.Errorf("%w：缺少 TMDB ID", errMediaUnrecognized)
	}
	categoryName := s.categoryName()
	relDir, ok := BuildOrganizeRelDir(media.Category, official, media.Year, media.Season, media.TmdbId, categoryName)
	if !ok {
		return nil, fmt.Errorf("%w：媒体信息不完整", errMediaUnrecognized)
	}
	ext := filepath.Ext(fileName)
	baseName := BuildOrganizeNewName(media.Category, official, media.Season, media.Episode, media.Year, ext)
	tags := ExtractQualityTags(fileName, official)
	newName := AppendQualityTagsToName(media.Category, official, media.Season, media.Episode, media.Year, ext, tags)
	if strings.TrimSpace(newName) == "" || newName == ext {
		newName = baseName
	}
	quality := ParseQualityFromName(fileName)
	return &OrganizeTarget{
		SourcePath: srcPath,
		RelDir:     relDir,
		NewName:    newName,
		Media:      media,
		Quality:    quality,
	}, nil
}

// errMediaUnrecognized 文件名无法识别（与旧实现语义一致，用于区分「识别失败」与流程错误）。
var errMediaUnrecognized = fmt.Errorf("文件名无法识别")

// isUnrecognized 判断错误是否为「无法识别」类。
func isUnrecognized(err error) bool {
	return err != nil && strings.Contains(err.Error(), errMediaUnrecognized.Error())
}

// PlanOrganize 扫描待整理目录，规划所有视频文件的整理目标。
// 规划阶段不访问网盘、不移动文件，仅做识别与命名，便于测试与失败登记。
func (s *Service) PlanOrganize(ctx context.Context, client *Client, pendingRoot string) *OrganizePlan {
	plan := &OrganizePlan{}
	files, err := CollectLocalFiles(pendingRoot)
	if err != nil {
		s.log.Warn("MoviePilot 读取待整理目录失败", "dir", pendingRoot, "err", err)
		return plan
	}
	for _, f := range files {
		if ctx.Err() != nil {
			return plan
		}
		if !IsVideoFile(filepath.Base(f.AbsPath)) {
			plan.Skipped = append(plan.Skipped, f.AbsPath)
			continue
		}
		target, oErr := s.organizeLocalFile(ctx, client, f.AbsPath, pendingRoot)
		if oErr != nil {
			if isUnrecognized(oErr) {
				s.log.Info("MoviePilot 整理无法识别", "file", f.AbsPath)
			} else {
				s.log.Warn("MoviePilot 整理失败", "file", f.AbsPath, "err", oErr)
			}
			reason := oErr.Error()
			if isUnrecognized(oErr) {
				reason = "文件名无法识别"
			}
			plan.Failed = append(plan.Failed, FailedEntry{SourcePath: f.AbsPath, Reason: reason})
			continue
		}
		plan.Targets = append(plan.Targets, *target)
	}
	return plan
}

// WashDecision 洗版（质量升级）判定结果。
type WashDecision struct {
	// Proceed 是否继续整理（false 表示新版质量不高于现版，应放弃）。
	Proceed bool
	// Reason 判定说明（中文，用于前端与历史记录）。
	Reason string
	// RuleNames 生效的洗版规则字段名。
	RuleNames []string
}

// DecideWash 比较新文件与目标目录内既有版本的质量，决定是否洗版。
// 采用与旧实现一致的「更优才覆盖」策略：
//   - 目标目录内无同名/同集旧版 → 直接放行
//   - 新版质量更优 → 放行
//   - 质量持平 → 由文件大小兜底（更大者视为更优）
//   - 新版更差 → 放弃，并给出逐项对比说明
func DecideWash(newQ *FileQuality, newName string, newSize int64, oldFiles []LocalFile, rules []WashRule) WashDecision {
	if len(rules) == 0 {
		rules = DefaultWashRules
	}
	newKey := WashCoreKey(newName)
	newEp := EpisodeKeyOf(newName)
	var loser *LocalFile
	var trace string
	for i := range oldFiles {
		old := &oldFiles[i]
		oldName := filepath.Base(old.AbsPath)
		// 匹配条件：同集（SxxExx 一致）或同名（忽略扩展名与质量后缀）
		sameEp := newEp != "" && newEp == EpisodeKeyOf(oldName)
		sameCore := newKey != "" && newKey == WashCoreKey(oldName)
		if !sameEp && !sameCore {
			continue
		}
		oldQ := ParseQualityFromName(oldName)
		cmp := CompareQuality(newQ, oldQ, nil, rules)
		if cmp > 0 {
			continue
		}
		if cmp == 0 {
			// 逐项持平：用文件大小兜底（体积与分辨率/码率正相关）
			if newSize > old.Size {
				continue
			}
			trace = QualityCompareTrace(newQ, oldQ, nil, rules)
			loser = old
			continue
		}
		trace = QualityCompareTrace(newQ, oldQ, nil, rules)
		loser = old
	}
	if loser == nil {
		return WashDecision{Proceed: true, Reason: "新版质量不低于现版，可整理", RuleNames: washRuleNames(rules)}
	}
	label := WashEpisodeLabel(filepath.Base(loser.AbsPath))
	reason := fmt.Sprintf("新版质量不高于现版%s，跳过整理（%s，新版 %s / 现版 %s）",
		label, trace, SizeGBText(newSize), SizeGBText(loser.Size))
	return WashDecision{Proceed: false, Reason: reason, RuleNames: washRuleNames(rules)}
}

// washRuleNames 提取规则字段名列表。
func washRuleNames(rules []WashRule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Field)
	}
	return out
}

// CategoryName 返回默认分类名（未配置分类规则时使用）。
func (s *Service) categoryName() string {
	return "未分类"
}

// RecordFailedFile 把无法识别/无法整理的文件写入失败文件表（同一任务同名文件去重）。
func (s *Service) RecordFailedFile(ctx context.Context, taskID int64, srcPath, reason string, media *IdentifyResult) error {
	fileName := filepath.Base(srcPath)
	if s.repo == nil {
		return nil
	}
	if existing, err := s.repo.FindPendingFailedFile(ctx, taskID, fileName); err == nil && existing != nil {
		return nil // 已有待处理记录，幂等
	}
	rec := &domain.MoviePilotFailedFile{
		TaskID:   taskID,
		FileName: fileName,
		RootPath: filepath.Dir(srcPath),
		Status:   domain.MoviePilotFailedPending,
		Reason:   reason,
	}
	if media != nil {
		rec.MediaType = media.Category
		rec.Title = media.Title
		rec.TmdbId = media.TmdbId
		rec.Year = media.Year
		rec.Season = media.Season
	}
	if _, err := s.repo.CreateFailedFile(ctx, rec); err != nil {
		s.log.Error("保存 MoviePilot 识别失败记录失败", "file", fileName, "err", err)
		return err
	}
	return nil
}
