package embyindex

import (
	"context"
	"strconv"
	"strings"
	"time"

	"litepan/internal/discover/embyclient"
	"litepan/internal/domain"
)

// parseItemIDInt 尽力把 Emby 条目 ID 转成整数，转不动时返回 0。
//
// 老版用 helpers.StringToInt64（即 strconv.ParseInt(s, 10, 64)）写入 ItemIdInt，
// 而 Emby 的 Id 基本是 32 位十六进制串，因此绝大多数条目该字段都是 0。
// 这里保持同样语义：ItemIdInt 只是「能解析出十进制就记下来」的便利字段，
// 真正的关联键始终是文本形式的 item_id。
func parseItemIDInt(itemID string) int64 {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return 0
	}
	value, err := strconv.ParseInt(itemID, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// parseRFC3339Unix 把 RFC3339 时间串转成 Unix 秒；解析失败返回 0。
func parseRFC3339Unix(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// buildMediaItem 把 Emby 条目投影成本地索引记录。
//
// 字段与老版 internal/emby/emby.go:155 的构造逐项对应：
//   - Path 优先取条目的 Path，取不到时回落到媒体源路径；
//   - PickCode / MediaSourcePath 由媒体源解析而来，解析不到时留空；
//   - DateCreatedTime / DateModifiedTime 是 DateCreated / DateModified 的 Unix 投影；
//   - ServerId 老版一律写空串，这里保持一致。
func buildMediaItem(item embyclient.BaseItemDtoV2, libraryID, syncRunID string, lastSeenAt int64) *domain.EmbyMediaItem {
	pickCode, mediaSourcePath, _ := ExtractPickCode(item.MediaSources)
	path := strings.TrimSpace(item.Path)
	if path == "" {
		path = mediaSourcePath
	}
	return &domain.EmbyMediaItem{
		ItemID:            strings.TrimSpace(item.Id),
		ItemIDInt:         parseItemIDInt(item.Id),
		ServerID:          "",
		Name:              item.Name,
		Type:              item.Type,
		ParentID:          item.ParentId,
		SeriesID:          item.SeriesId,
		SeriesName:        item.SeriesName,
		SeasonID:          item.SeasonId,
		SeasonName:        item.SeasonName,
		LibraryID:         libraryID,
		Path:              path,
		PickCode:          pickCode,
		MediaSourcePath:   mediaSourcePath,
		IndexNumber:       item.IndexNumber,
		ParentIndexNumber: item.ParentIndexNumber,
		ProductionYear:    item.ProductionYear,
		PremiereDate:      item.PremiereDate,
		DateCreated:       item.DateCreated,
		DateCreatedTime:   parseRFC3339Unix(item.DateCreated),
		DateModified:      item.DateModified,
		DateModifiedTime:  parseRFC3339Unix(item.DateModified),
		IsFolder:          item.IsFolder,
		LastSeenSyncRun:   syncRunID,
		LastSeenAt:        lastSeenAt,
	}
}

// backingFile 描述一条 Emby 条目背后的网盘文件位置。
//
// 老版通过 SyncFile 表把 PickCode 换成 (SyncFileId, SyncPathId, Path, ParentId)；
// 现版没有逐文件的 STRM 表，因此只记录「网盘账号 + STRM 任务根目录 + 相对路径」，
// 需要真实文件 ID 时再用 file.Service.ResolvePath 反查。
type backingFile struct {
	AccountID    int64
	TaskID       int64
	RootID       string
	RelativePath string
	FileName     string
}

// pickCodeLookup 是可选注入的「PickCode → 网盘文件位置」查询实现。
//
// 现版没有等价于老版 SyncFile 的逐文件表（STRM 只落盘，网盘文件信息来自驱动实时调用），
// 因此默认没有实现；引擎在缺少实现时把映射降级为「不可用」，
// 只记录 PickCode 与条目，不做任何删除动作。
type pickCodeLookup interface {
	// LookupBackingFile 返回 PickCode 对应的网盘文件位置；
	// 第二个返回值为 false 表示「查不到或存在歧义」，调用方必须跳过删除。
	LookupBackingFile(ctx context.Context, pickCode string) (backingFile, bool)
}

// resolveBackingFile 把 PickCode 解析成网盘文件位置。
//
// 【已知限制 — 本次移植最不确定的一处】
// 当前 schema 里没有逐文件的 STRM / 网盘文件表：strm_tasks 只有任务级信息
// （账号 + 父目录 + 路径），strm_branches 按目录建分支，strm_remote_dir_cache 只缓存目录。
// 老版 SyncFile 表（FileId / PickCode / ParentId / Path / IsVideo / IsMeta / SourceType）
// 在现版没有对应物，因此：
//   - 调用方可以注入 pickCodeLookup 提供真实查询；
//   - 未注入时返回 false，让调用方走「跳过并记录」的安全路径，
//     而不是凭空造表或猜路径（猜路径会误删网盘文件）。
func (s *Service) resolveBackingFile(ctx context.Context, pickCode string) (backingFile, bool) {
	pickCode = strings.TrimSpace(pickCode)
	if pickCode == "" || s.lookup == nil {
		return backingFile{}, false
	}
	return s.lookup.LookupBackingFile(ctx, pickCode)
}
