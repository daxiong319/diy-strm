package planner

import (
	"strings"

	"litepan/internal/mediaorganize/moplan"
	"litepan/internal/mediaorganize/rules"
	"litepan/internal/mediaorganize/unrecognized"
)

// 未识别兜底：识别不出标题的文件往哪放。
//
// 这里刻意只做「算出一个目标 + 在计划里留下痕迹」，**不直接建任何目录**：
// 计划阶段的所有父目录都是符号引用（"ref:<id>"），真实 ID 要到执行期由
// ensure_dir 动作创建。兜底目录与普通目标目录因此走同一套 ensureDirAction，
// 预览里看到的顺序也就是真实的执行顺序。
//
// ⚠️ 为什么用 relocate 而不是「建目录 + 移动」两个动作拼：
// 未识别的文件名本来就是乱的，重命名它没有任何价值，而 relocate 允许
// 目标名与源名相同。这样兜底路径上只有「确保目录存在」和「移动」两类动作，
// 与已识别文件的路径形状一致，用户在预览里不会看到两套语义。

// planUnrecognizedFallback 给一组未识别文件安排兜底落点。
//
// 返回 true 表示「已经给这组文件安排过动作」，调用方应就此结束这一组；
// 返回 false 表示什么都没安排，调用方应保留原来的 needs_match + skip。
func (p *Planner) planUnrecognizedFallback(key groupKey, items []batchEntry, reason string) bool {
	// rename-only 任务的约定是「只改名、不动位置」，而移到兜底目录本身
	// 就是一次移动。这时保留原来的 skip，不擅自改动作类型。
	if p.actionType != "move" || len(items) == 0 {
		return false
	}
	opts := p.unrecognizedOptions(key, items)
	target := unrecognized.ResolveUnrecognizedTarget(opts)
	if target.Kind == unrecognized.TargetKeep {
		return false
	}
	parentRef, ok := p.unrecognizedParentRef(target, opts, key, items)
	if !ok {
		return false
	}
	for _, entry := range items {
		p.add(moplan.PlanAction{
			ID:             p.nextID(),
			Kind:           moplan.ActionKindRelocate,
			SourceID:       entry.item.ID,
			SourceName:     entry.item.Name,
			SourceParentID: entry.sourceDirID,
			TargetParentID: parentRef,
			TargetName:     entry.item.Name,
			Reason:         target.SkipReason,
			Confidence:     0,
			Metadata: map[string]any{
				"unrecognized_fallback": true,
				"target_kind":           string(target.Kind),
			},
		})
	}
	// 仍然记 needs_match：兜底只是让文件有个确定去处，用户匹配之后
	// 可以再整理一次到真正的作品目录里。删掉它会让用户以为「已经处理好了」。
	p.recordNeedsMatch(key, items, reason, map[string]any{
		"unrecognized_fallback": true,
		"target_kind":           string(target.Kind),
		"target_dir":            target.Path,
		"target_dir_name":       target.DirName,
	})
	return true
}

// unrecognizedOptions 读出兜底判定的全部输入。
//
// 三个设置键都是全局的（任务不覆盖），所以直接读 settings ——
// 与 mo_file_extensions 那批一样的方式。
func (p *Planner) unrecognizedOptions(key groupKey, items []batchEntry) unrecognized.Options {
	return unrecognized.Options{
		SkipAction:      strSetting(p.settings, "mo_scrape_skip_action", unrecognized.UnrecognizedKeep),
		UnrecognizedDir: strSetting(p.settings, "mo_scrape_unrecognized_dir", unrecognized.UnrecognizedDefaultDir),
		TargetDirectory: p.unrecognizedTargetDirPath(),
		Category:        p.unrecognizedCategory(key, items),
		// 开关打开但没注入反查器（Emby 离线 / 不支持）时 ExistingLocation 为空，
		// 判定自动落回「兜底目录」那三条分支 —— 反查失败不该改变行为。
		FollowExistingLocation: rules.SettingBool(p.settings["mo_scrape_follow_existing_location"], false),
		ExistingLocation:       p.embyExistingLocation(key),
	}
}

// unrecognizedTargetDirPath 解析整理目录自己的路径。
//
// 只有「沿用媒体库已有位置」和「纯目录名形态」这两种判定需要它，而后者
// 拼出的完整路径只用于展示与提示 —— 真正建目录走的是符号引用。所以这里
// 解析失败一律当空，绝不让一次网络错误把整个整理任务带崩。
func (p *Planner) unrecognizedTargetDirPath() string {
	if p.unrecognizedDirPathDone {
		return p.unrecognizedDirPath
	}
	p.unrecognizedDirPathDone = true
	dirID := p.targetRootID
	if dirID == "" {
		dirID = p.parentID
	}
	if dirID == "" {
		return ""
	}
	if got, err := p.files.ResolveDirPath(p.ctx, p.accountID, dirID); err == nil {
		p.unrecognizedDirPath = got
	}
	return p.unrecognizedDirPath
}

// unrecognizedCategory 取整理目录下的第一层分类目录名（「电影」/「剧集」/…）。
//
// 只取一层：兜底目录要落在「整理目录/一级/未识别」而不是按原路径逐层复刻，
// 否则一个乱目录树会把兜底目录复制成一个更难清理的乱目录树。
func (p *Planner) unrecognizedCategory(key groupKey, items []batchEntry) string {
	if len(items) == 0 {
		return ""
	}
	for _, anc := range p.categoryAncestors(key, items) {
		if name := strings.TrimSpace(anc.Name); name != "" {
			return name
		}
	}
	return ""
}

// embyExistingLocation 反查这个作品在媒体库里已有的位置。
//
// 未识别意味着没有 TMDB ID，只能按标题 + 年份找。这一步是有成本的网络
// 往返，而且只有 Emby 支持、需要在线，所以开关默认关闭。查不到返回空。
func (p *Planner) embyExistingLocation(key groupKey) string {
	if !rules.SettingBool(p.settings["mo_scrape_follow_existing_location"], false) || p.embyLookup == nil {
		return ""
	}
	title := strings.TrimSpace(key.title)
	if title == "" {
		return ""
	}
	// year=0 是「解析不出年份」，反查时不能当成 1970 年的作品去查。
	var year *int
	if key.year > 0 {
		year = intPtr(key.year)
	}
	return p.embyLookup(p.ctx, p.accountID, title, year)
}

// unrecognizedParentRef 解析兜底目标的父目录引用，并建出沿途目录。
func (p *Planner) unrecognizedParentRef(
	target unrecognized.UnrecognizedTarget,
	opts unrecognized.Options,
	key groupKey,
	items []batchEntry,
) (string, bool) {
	switch target.Kind {
	case unrecognized.TargetRelative:
		if target.DirName == "" {
			return "", false
		}
		rootRef := p.targetRootID
		if rootRef == "" {
			rootRef = p.parentID
		}
		parentRef := rootRef
		if category := p.unrecognizedCategory(key, items); category != "" {
			parentRef = p.ensureDirAction(parentRef, category)
		}
		return p.ensureDirAction(parentRef, target.DirName), true
	case unrecognized.TargetAbsolute:
		dirID, ok := p.findDirIDByPath(target.Path)
		if !ok {
			return "", false
		}
		return dirID, true
	}
	return "", false
}

// findDirIDByPath 按路径逐层找到网盘目录的 ID。
//
// 找不到就返回 false（而不是退回相对形态）：用户显式填了绝对路径，
// 悄悄改成落另一个目录，比什么都不做更难排查。
func (p *Planner) findDirIDByPath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	current := ""
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
		items, err := p.files.List(p.ctx, p.accountID, current, false)
		if err != nil {
			return "", false
		}
		next := ""
		for _, item := range items {
			if item.IsDir && item.Name == part {
				next = item.ID
				break
			}
		}
		if next == "" {
			return "", false
		}
		current = next
	}
	if current == "" {
		return "", false
	}
	return current, true
}
