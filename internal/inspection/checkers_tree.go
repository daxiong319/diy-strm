package inspection

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// 修复动作的 kind 常量。
//
// 放在这里而不是各自的修复器里，是因为 Service 要在预览阶段判断
// 「这条动作破坏性吗」，那需要一张全局的破坏性表 —— 如果每种动作的风险
// 只写在实现方内部，预览就只能靠猜。
const (
	RepairDeleteDir    = "delete_dir"
	RepairDropIndexRow = "drop_index_row"
	RepairRenameFile   = "rename_file"
	RepairNone         = ""
)

// destructiveKinds 标记不可逆的动作。
//
// 空目录用删除而不是「搬到回收站」：它没有内容，搬走只是让一个空壳换个地方
// 继续污染下一次扫描的结果，而它本来就能被下一次整理重建。
// 反过来，索引行删除算可逆 —— 网盘文件没动，下次同步会重新建立这条关联。
var destructiveKinds = map[string]bool{
	RepairDeleteDir:    true,
	RepairDropIndexRow: false,
	RepairRenameFile:   true,
}

// IsDestructive 判断一类修复动作是否不可逆。
func IsDestructive(kind string) bool { return destructiveKinds[kind] }

// IndexRow 是巡检看到的索引行（emby_media_sync_files 的一行）。
type IndexRow struct {
	ID           int64
	EmbyItemID   int64
	AccountID    int64
	RootID       string
	RelativePath string
	FileName     string
}

// IndexAbnormalChecker 巡检「115 索引异常文件」。
//
// 判据：索引表里记着某文件存在于 root_id 下的 relative_path，
// 但网盘清单里没有这条路径 —— 即「索引说有、盘上没有」。
// 常见成因是用户在网盘端删了或移走了文件，而 Emby 索引还留着。
//
// 修复选择「删掉这条索引关联」而不是「从 Emby 里删媒体」：这张表是缓存，
// 事实来源是网盘清单。删错的后果是下次同步补回这条关联；
// 而留着不删的后果是 Emby 一直去读一个不存在的文件。
type IndexAbnormalChecker struct {
	KeyName   string
	LabelText string
	// Roots 每次扫描现取。整理任务配置是运行期可改的：把 Roots 在接线时
	// 冻成切片，会让用户新建的整理根在巡检里永远不出现。
	Roots     func(ctx context.Context) ([]Root, error)
	Source    TreeSource
	// IndexRows 注入 emby_media_sync_files 的全量行。
	IndexRows func(ctx context.Context) ([]IndexRow, error)
}

func (c *IndexAbnormalChecker) Key() string   { return c.KeyName }
func (c *IndexAbnormalChecker) Label() string { return c.LabelText }

func (c *IndexAbnormalChecker) Scan(ctx context.Context) ([]Finding, error) {
	if c.IndexRows == nil || c.Source == nil || c.Roots == nil {
		return nil, nil
	}
	rows, err := c.IndexRows(ctx)
	if err != nil {
		return nil, err
	}
	roots, err := c.Roots(ctx)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]Root, len(roots))
	for _, r := range roots {
		wanted[rootKey(r.AccountID, r.ID)] = r
	}
	// 一个根读不到不等于整轮失败：其余根的结论仍然成立，跳过即可。
	trees, err := listTrees(ctx, roots, c.Source)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]*Tree, len(trees))
	for _, tree := range trees {
		seen[rootKey(tree.Root.AccountID, tree.Root.ID)] = tree
	}
	var out []Finding
	for _, row := range rows {
		tree, ok := seen[rootKey(row.AccountID, row.RootID)]
		if !ok {
			continue
		}
		path := joinRelative(tree.Root.Path, row.RelativePath)
		if _, exists := tree.byPath[normalizePath(path)]; exists {
			continue
		}
		out = append(out, Finding{
			CheckerKey: c.Key(),
			Kind:       "index_missing_file",
			Target:     fmt.Sprintf("account=%d root=%s path=%s", row.AccountID, row.RootID, path),
			Detail: map[string]any{
				"row_id":  row.ID,
				"item_id": row.EmbyItemID,
				"name":    row.FileName,
			},
			Repair: RepairAction{
				Kind:   RepairDropIndexRow,
				Label:  "删除这条失效索引关联",
				Params: map[string]string{"row_id": fmt.Sprintf("%d", row.ID)},
				Preview: fmt.Sprintf("删除索引关联 #%d（%s）。网盘上已经找不到这个文件，"+
					"下次同步若它重新出现会自动补回关联。", row.ID, path),
				Reversible: true,
			},
		})
	}
	return out, nil
}

// OrphanDirChecker 巡检「孤儿目录」：目录里没有任何媒体正文文件。
//
// 这类是刮削失败、用户手动删片、整理中途被打断留下的残骸。
//
// 与 EmptyDirChecker 的分工：那个查**一个文件都没有**的空壳，
// 这个查**还剩点东西但没有活**的半成品。两者可能命中同一个目录，
// 那时只保留孤儿目录这条 —— 它的信息量更大（还能看出目录叫什么）。
type OrphanDirChecker struct {
	KeyName   string
	LabelText string
	// Roots 每次扫描现取。整理任务配置是运行期可改的：把 Roots 在接线时
	// 冻成切片，会让用户新建的整理根在巡检里永远不出现。
	Roots     func(ctx context.Context) ([]Root, error)
	Source    TreeSource
}

func (c *OrphanDirChecker) Key() string   { return c.KeyName }
func (c *OrphanDirChecker) Label() string { return c.LabelText }

func (c *OrphanDirChecker) Scan(ctx context.Context) ([]Finding, error) {
	if c.Source == nil || c.Roots == nil {
		return nil, nil
	}
	roots, err := c.Roots(ctx)
	if err != nil {
		return nil, err
	}
	trees, err := listTrees(ctx, roots, c.Source)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, tree := range trees {
		root := tree.Root
		for _, dir := range tree.Dirs() {
			if dirHasMedia(tree, dir.ID) {
				continue
			}
			// 只报最深的一层：父目录会作为容器被一起处理，
			// 单独再报一条只是让人多点几下，还容易让人以为要删两次。
			if len(tree.Descendants(dir.ID)) > 0 {
				continue
			}
			out = append(out, Finding{
				CheckerKey: c.Key(),
				Kind:       "orphan_dir",
				Target:     fmt.Sprintf("account=%d path=%s", root.AccountID, dir.Path),
				Detail:     map[string]any{"name": dir.Name, "path": dir.Path},
				Repair: RepairAction{
					Kind:   RepairDeleteDir,
					Label:  "删除这个孤儿目录",
					Params: map[string]string{"account_id": fmt.Sprintf("%d", root.AccountID), "dir_id": dir.ID, "path": dir.Path},
					Preview: fmt.Sprintf("删除目录 %s。目录里没有媒体正文文件，"+
						"残留的 nfo 与字幕会一并删除，删除后不可撤销。", dir.Path),
					Reversible: false,
				},
			})
		}
	}
	return out, nil
}

func dirHasMedia(tree *Tree, dirID string) bool {
	for _, child := range tree.Children(dirID) {
		if !child.IsDir && IsMediaName(child.Name) {
			return true
		}
	}
	return false
}

// EmptyDirChecker 巡检「目录树清理」：彻底空的目录。
type EmptyDirChecker struct {
	KeyName   string
	LabelText string
	// Roots 每次扫描现取。整理任务配置是运行期可改的：把 Roots 在接线时
	// 冻成切片，会让用户新建的整理根在巡检里永远不出现。
	Roots     func(ctx context.Context) ([]Root, error)
	Source    TreeSource
}

func (c *EmptyDirChecker) Key() string   { return c.KeyName }
func (c *EmptyDirChecker) Label() string { return c.LabelText }

func (c *EmptyDirChecker) Scan(ctx context.Context) ([]Finding, error) {
	if c.Source == nil || c.Roots == nil {
		return nil, nil
	}
	roots, err := c.Roots(ctx)
	if err != nil {
		return nil, err
	}
	trees, err := listTrees(ctx, roots, c.Source)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, tree := range trees {
		root := tree.Root
		for _, dir := range tree.Dirs() {
			if len(tree.Children(dir.ID)) > 0 || len(tree.Descendants(dir.ID)) > 0 {
				continue
			}
			out = append(out, Finding{
				CheckerKey: c.Key(),
				Kind:       "empty_dir",
				Target:     fmt.Sprintf("account=%d path=%s", root.AccountID, dir.Path),
				Detail:     map[string]any{"name": dir.Name, "path": dir.Path},
				Repair: RepairAction{
					Kind:      RepairDeleteDir,
					Label:     "删除这个空目录",
					Params:    map[string]string{"account_id": fmt.Sprintf("%d", root.AccountID), "dir_id": dir.ID, "path": dir.Path},
					Preview:   fmt.Sprintf("删除空目录 %s。目录内没有任何文件，下次整理会按需自动重建。", dir.Path),
					Reversible: false,
				},
			})
		}
	}
	return out, nil
}

// DuplicateChecker 巡检「重复排查」。
//
// 判据：同一棵树里出现两份以上**体积相同**的媒体文件。
//
// 只看体积、不看文件名：文件被改过名（加了季号、搬进版本目录）之后
// 文件名不同而内容相同，才是重复的主要形态；而 SHA-1 多数网盘不给。
// 体积相同只是**候选**，所以这一项不提供自动删除 —— 两个 700MB 的不同电影
// 是常态，系统替用户猜哪份该留，就是替用户删片了。
type DuplicateChecker struct {
	KeyName   string
	LabelText string
	// Roots 每次扫描现取。整理任务配置是运行期可改的：把 Roots 在接线时
	// 冻成切片，会让用户新建的整理根在巡检里永远不出现。
	Roots     func(ctx context.Context) ([]Root, error)
	Source    TreeSource
	// MinSizeBytes 小于这个体积的文件不参与判定，默认 1MB：
	// 预告片、花絮、小样片的同体积噪声远大于信号。
	MinSizeBytes int64
}

func (c *DuplicateChecker) Key() string   { return c.KeyName }
func (c *DuplicateChecker) Label() string { return c.LabelText }

func (c *DuplicateChecker) Scan(ctx context.Context) ([]Finding, error) {
	if c.Source == nil || c.Roots == nil {
		return nil, nil
	}
	roots, err := c.Roots(ctx)
	if err != nil {
		return nil, err
	}
	min := c.MinSizeBytes
	if min <= 0 {
		min = 1 << 20
	}
	trees, err := listTrees(ctx, roots, c.Source)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, tree := range trees {
		root := tree.Root
		groups := map[int64][]TreeNode{}
		for _, f := range tree.Files() {
			if f.Size < min || !IsMediaName(f.Name) {
				continue
			}
			groups[f.Size] = append(groups[f.Size], f)
		}
		sizes := make([]int64, 0, len(groups))
		for size := range groups {
			sizes = append(sizes, size)
		}
		sort.Slice(sizes, func(i, j int) bool { return sizes[i] < sizes[j] })
		for _, size := range sizes {
			group := groups[size]
			if len(group) < 2 {
				continue
			}
			sort.Slice(group, func(i, j int) bool { return group[i].Path < group[j].Path })
			paths := make([]string, len(group))
			for i, g := range group {
				paths[i] = g.Path
			}
			out = append(out, Finding{
				CheckerKey: c.Key(),
				Kind:       "duplicate_candidate",
				Target:     fmt.Sprintf("account=%d size=%d", root.AccountID, size),
				Detail: map[string]any{
					"size_bytes": size,
					"count":      len(group),
					"paths":      paths,
				},
				Repair: RepairAction{
					Kind:      RepairNone,
					Label:     "仅报告，不自动处理",
					Preview:   fmt.Sprintf("以下 %d 个文件体积相同（%s）：%s。体积相同不等于内容相同，需人工确认后手动处理。", len(group), humanSize(size), strings.Join(paths, "、")),
					Reversible: false,
				},
			})
		}
	}
	return out, nil
}


// listTrees 逐根取清单并整理成视图。
//
// 「某个根读不到就跳过」是对的：多根配置时一个网盘离线不该让整轮巡检
// 什么都没产出。但**全部根都读不到**必须返回错误 —— 那种情况下跳过逻辑
// 会把六个检查器一致变成「零条发现」，用户看到的是一片绿的「一切正常」，
// 而实际上一个文件都没检查。空根（没配任何整理任务）不算失败。
func listTrees(ctx context.Context, roots []Root, source TreeSource) ([]*Tree, error) {
	var (
		trees []*Tree
		first error
		ok    int
	)
	for _, root := range roots {
		nodes, err := source.List(ctx, root)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		ok++
		trees = append(trees, NewTree(root, nodes))
	}
	if len(roots) > 0 && ok == 0 {
		return nil, first
	}
	return trees, nil
}

func rootKey(accountID int64, rootID string) string {
	return fmt.Sprintf("%d/%s", accountID, rootID)
}

func joinRelative(base, rel string) string {
	rel = strings.Trim(rel, "/")
	if base == "" {
		return rel
	}
	if rel == "" {
		return base
	}
	return base + "/" + rel
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 3; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}