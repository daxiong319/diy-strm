package inspection

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"litepan/internal/domain"
)

// NetdiskEntry 是网盘清单条目在巡检侧的形状。
//
// 刻意不直接用 driver.FullListEntry：那个结构体没有 IsDir，
// 而巡检必须区分目录与文件（孤儿目录判定就靠这个）。
// 用自己的类型换来「检查器拿到的就是它需要的语义」。
type NetdiskEntry struct {
	ID           string
	ParentID     string
	Name         string
	RelativePath string
	Size         int64
	Sha1         string
	IsDir        bool
}

// TreeLister 是巡检读网盘需要的最小端口。
//
// 只有一个方法：一次拉回某棵根下的完整清单。Roots 由装配层从配置算出，
// 不需要再问驱动，所以接口里没有 Roots。
type TreeLister interface {
	Tree(ctx context.Context, accountID int64, rootID, rootPath string) ([]NetdiskEntry, error)
}



// NetdiskTreeSource 把网盘清单接成巡检的 TreeSource。
//
// 用一次拉全树（ListAllFiles）而不是逐目录 List：六个检查器里五个都要全树，
// 逐目录递归会把同一个目录翻五遍，而五遍之间目录还可能变 ——
// 「孤儿目录其实有文件」这种误判就是这么来的。
//
// 代价是清单接口在部分驱动上不可用（SupportsFullList 为假）。那时
// List 返回错误，上层如实报告「这个检查器出错」，而不是拿一个空清单去
// 报告「一切正常」—— 后者是最坏的一种错。
type NetdiskTreeSource struct {
	files TreeLister
}

// NewNetdiskTreeSource 把驱动清单接到巡检的 TreeSource 上。
func NewNetdiskTreeSource(files TreeLister) *NetdiskTreeSource {
	return &NetdiskTreeSource{files: files}
}

func (s *NetdiskTreeSource) List(ctx context.Context, root Root) ([]TreeNode, error) {
	if s == nil || s.files == nil {
		return nil, fmt.Errorf("未配置网盘清单来源")
	}
	entries, err := s.files.Tree(ctx, root.AccountID, root.ID, root.Path)
	if err != nil {
		return nil, err
	}
	base := normalizePath(root.Path)
	out := make([]TreeNode, 0, len(entries))
	for _, e := range entries {
		out = append(out, TreeNode{
			ID:        e.ID,
			ParentID:  e.ParentID,
			Name:      e.Name,
			Path:      joinRelative(base, e.RelativePath),
			IsDir:     e.IsDir,
			Size:      e.Size,
			Sha1:      e.Sha1,
			AccountID: root.AccountID,
			RootID:    root.ID,
		})
	}
	return out, nil
}

// DirChecker 是删除目录时需要的端口：复核内容 + 删除。
type DirChecker interface {
	// DirItems 列出目录的直接子项（含子目录）。
	DirItems(ctx context.Context, accountID int64, dirID string) ([]DirItem, error)
	DeleteDir(ctx context.Context, accountID int64, dirID string) error
}

// DirItem 是目录里的一个条目。
type DirItem struct {
	ID    string
	Name  string
	IsDir bool
}

// DeleteDirRepairer 删除一个空目录 / 孤儿目录。
type DeleteDirRepairer struct {
	Files DirChecker
}

// Repair 执行删除。
func (r *DeleteDirRepairer) Repair(ctx context.Context, action RepairAction) (string, error) {
	accountID, err := intParam(action, "account_id")
	if err != nil {
		return "", err
	}
	dirID := action.Params["dir_id"]
	if dirID == "" {
		return "", fmt.Errorf("修复参数缺少 dir_id")
	}
	if r == nil || r.Files == nil {
		return "", fmt.Errorf("未配置删除复核通道")
	}
	// 删除前再确认一次目录确实是空的。
	//
	// 这一步不是防并发（网盘没有可靠事务），而是防「用户在扫描之后往这个目录
	// 里放了文件」—— 快照里的空目录结论到执行时可能已过期，而底层删除
	// 不会替你检查。宁可拒绝执行让用户重扫，也不能连带删掉刚放进来的东西。
	items, err := r.Files.DirItems(ctx, accountID, dirID)
	if err != nil {
		return "", fmt.Errorf("复核目录内容失败，已跳过删除: %w", err)
	}
	if len(items) > 0 {
		return "", fmt.Errorf("目录已不再为空（%d 项），已跳过删除，请重新扫描", len(items))
	}
	if err := r.Files.DeleteDir(ctx, accountID, dirID); err != nil {
		return "", err
	}
	return "已删除目录 " + action.Params["path"], nil
}

// DropIndexRowRepairer 删除一条失效索引关联。
type DropIndexRowRepairer struct {
	Emby domain.EmbyIndexRepository
}

func (r *DropIndexRowRepairer) Repair(ctx context.Context, action RepairAction) (string, error) {
	rowID, err := intParam(action, "row_id")
	if err != nil {
		return "", err
	}
	if r == nil || r.Emby == nil {
		return "", fmt.Errorf("未配置索引仓储")
	}
	if err := r.Emby.DeleteMediaSyncFileRow(ctx, rowID); err != nil {
		return "", err
	}
	return fmt.Sprintf("已删除失效索引关联 #%d", rowID), nil
}

func intParam(action RepairAction, key string) (int64, error) {
	raw := action.Params[key]
	if raw == "" {
		return 0, fmt.Errorf("修复参数缺少 %s", key)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("修复参数 %s 不是数字: %q", key, raw)
	}
	return v, nil
}