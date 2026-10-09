package executor

import (
	"fmt"
	"strings"

	"litepan/internal/mediaorganize/moplan"
)

// QuarantinePolicy 描述「低于最小体积的媒体文件该怎么处理」（T15）。
//
// 三个字段各自回答一个不能替用户决定的问题：
//
//   - Enabled  阈值开着、move 模式、且用户勾过知情确认 —— 三者同时成立才真搬文件。
//   - ReportOnly 阈值开着但用户还没勾确认 —— 只在计划里留一条记录，一个字节都不动。
//     这一档是刻意留的：用户把阈值填小之后第一反应是「跑一遍看看会命中什么」，
//     而不是「先搬走几百个文件再说」。
//   - DirPath  隔离目录相对整理目标根的路径；为空则用默认的「_隔离」。
//
// 注意 ReportOnly 与 Enabled 互斥，且 ReportOnly 优先级更高：
// 「我知道会发生什么」这句话必须由用户亲口说出来，不能由程序推断。
type QuarantinePolicy struct {
	Enabled    bool
	ReportOnly bool
	MinBytes   int64
	DirPath    string
	// DirName 是隔离目录的末级名字，创建时用。
	DirName string
}

const defaultQuarantineDirName = "_隔离"

// SetMoveMode 声明本轮是不是 move 模式。
//
// 隔离只在 move 模式下有意义：rename 不动文件、copy 的语义就是源文件不许少。
// 默认 false 而不是默认 true —— 这个开关的缺省值必须是最保守的那一档，
// 因为它的错误方向是「搬走用户没让搬的东西」。
func (e *Executor) SetMoveMode(v bool) {
	e.moveMode = v
}

func (p QuarantinePolicy) active() bool {
	return p.Enabled && !p.ReportOnly && p.MinBytes > 0
}

// SetQuarantine 装上小文件隔离策略。为 nil（零值）时整条通道关闭。
func (e *Executor) SetQuarantine(p QuarantinePolicy) {
	e.quarantine = p
}

// quarantineSmallFiles 在 relocate 之前把过小的媒体文件移出流程。
//
// 为什么排在 prescanConflicts 之后而不是之前：预扫描会把这些文件的「目标同名冲突」
// 解决掉（可能标记为覆盖/跳过）。先隔离再预扫描的话，被隔离的文件已经不在源目录里，
// 预扫描再去比对目标目录会凭空造出一条覆盖删除。顺序反了会多删文件，不是多报一条。
//
// 为什么排得比 executeRelocates 早：一旦文件被移进隔离目录，relocate 动作就该
// 从这一轮的 pending 里退出去，否则 executor 会拿着一个已经不存在的源 ID 去搬。
func (e *Executor) quarantineSmallFiles(relocateActions []*moplan.PlanAction) error {
	// 三道门任一不成立就完全不碰文件：阈值开着、move 模式、用户勾过知情确认
	// （未勾时走只报告分支，见下）。
	if e.quarantine.MinBytes <= 0 || !e.moveMode {
		return nil
	}
	var (
		dirID  string
		moved  int
		report []map[string]any
	)
	for _, action := range relocateActions {
		if action.Status != "" {
			continue
		}
		if action.SourceID == "" {
			continue
		}
		item, err := e.files.Info(e.ctx, e.accountID, action.SourceID)
		if err != nil {
			// 读不到体积就不动它：宁可漏隔离，也不要把「不知道多大」当成「很小」。
			e.log(fmt.Sprintf("[隔离] 读取文件信息失败，跳过 %s: %v", action.SourceID, err))
			continue
		}
		if item == nil || item.IsDir {
			continue
		}
		if item.Size >= e.quarantine.MinBytes {
			continue
		}
		record := map[string]any{
			"name":   item.Name,
			"size":   item.Size,
			"source": action.SourceName,
		}
		if !e.quarantine.active() {
			// 只报告：把命中写进诊断，计划照常生成，用户能先看清楚会搬什么。
			report = append(report, record)
			action.Status = "skipped"
			action.Error = fmt.Sprintf("小于「媒体文件最小体积」%d 字节（未确认隔离，仅跳过）", e.quarantine.MinBytes)
			action.ExecutedAt = nowStr()
			continue
		}
		if dirID == "" {
			id, err := e.ensureQuarantineDir()
			if err != nil {
				e.log(fmt.Sprintf("[隔离] 准备隔离目录失败，本轮不隔离: %v", err))
				return nil
			}
			dirID = id
		}
		if err := e.files.MoveFiles(e.ctx, e.accountID, []string{item.ID}, dirID, action.SourceParentID); err != nil {
			e.log(fmt.Sprintf("[隔离] 移动 %s 失败: %v", item.Name, err))
			continue
		}
		action.Status = "skipped"
		action.Error = fmt.Sprintf("小于「媒体文件最小体积」%d 字节，已移到隔离目录（可恢复）", e.quarantine.MinBytes)
		action.ExecutedAt = nowStr()
		record["quarantined"] = true
		moved++
		report = append(report, record)
		e.invalidateDirCache(action.SourceParentID)
	}
	if len(report) > 0 {
		e.plan.Diagnostics = ensureMeta(e.plan.Diagnostics)
		e.plan.Diagnostics["small_files"] = report
		detail := fmt.Sprintf("%d 个文件低于最小体积", len(report))
		if moved > 0 {
			detail = fmt.Sprintf("%d 个已移入隔离目录（可随时搬回）", moved)
		}
		e.log("[隔离] " + detail)
	}
	return nil
}

// ensureQuarantineDir 找到或建出隔离目录，返回它的 ID。
//
// 隔离目录挂在整理目标根下面，而不是根的上一级：往库外搬文件等于把文件
// 移出所有 Emby/刮削规则的视野，而「可恢复」的前提是先得在同一个盘上看得见。
func (e *Executor) ensureQuarantineDir() (string, error) {
	if e.plan == nil || e.plan.TargetRootID == "" {
		return "", fmt.Errorf("计划缺少目标根，无法定位隔离目录")
	}
	name := e.quarantine.DirName
	rel := e.quarantine.DirPath
	if strings.TrimSpace(name) == "" {
		if rel == "" {
			rel = defaultQuarantineDirName
		}
		name = rel
		rel = ""
	}
	parentID := e.plan.TargetRootID
	for _, part := range strings.Split(rel, "/") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", fmt.Errorf("隔离目录路径不允许向上跳")
		}
		childID, err := e.findChildDir(parentID, part, false)
		if err != nil {
			return "", err
		}
		if childID == "" {
			created, err := e.files.CreateFolder(e.ctx, e.accountID, parentID, part)
			if err != nil {
				return "", fmt.Errorf("创建隔离目录 %s 失败: %w", part, err)
			}
			childID = created.ID
			e.invalidateDirCache(parentID)
		}
		parentID = childID
	}
	childID, err := e.findChildDir(parentID, name, false)
	if err != nil {
		return "", err
	}
	if childID != "" {
		return childID, nil
	}
	created, err := e.files.CreateFolder(e.ctx, e.accountID, parentID, name)
	if err != nil {
		return "", fmt.Errorf("创建隔离目录 %s 失败: %w", name, err)
	}
	e.invalidateDirCache(parentID)
	return created.ID, nil
}
