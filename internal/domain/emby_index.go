package domain

import (
	"context"
	"time"
)

// EmbyMediaItem 是同步下来的 Emby 媒体条目本地索引。
// 它把 Emby 的 ItemId 映射回网盘路径与 PickCode，供删除联动、外部解析等功能使用。
type EmbyMediaItem struct {
	ID                int64
	ItemID            string
	ItemIDInt         int64
	ServerID          string
	Name              string
	Type              string
	ParentID          string
	SeriesID          string
	SeriesName        string
	SeasonID          string
	SeasonName        string
	LibraryID         string
	Path              string
	PickCode          string
	MediaSourcePath   string
	IndexNumber       int
	ParentIndexNumber int
	ProductionYear    int
	PremiereDate      string
	DateCreated       string
	DateCreatedTime   int64
	DateModified      string
	DateModifiedTime  int64
	IsFolder          bool
	LastSeenSyncRun   string
	LastSeenAt        int64
}

// EmbyMediaSyncFile 关联 Emby 条目与网盘文件。
// 现版没有「每文件一行」的 strm 表，因此这里保存反查所需的账号 + 根目录 + 相对路径。
type EmbyMediaSyncFile struct {
	ID           int64
	EmbyItemID   int64
	SyncFileID   int64
	PickCode     string
	SyncPathID   int64
	AccountID    int64
	RootID       string
	RelativePath string
	FileName     string
}

// EmbyLibrarySyncPath 关联 Emby 媒体库与 STRM 同步任务（对应老版 SyncPath）。
type EmbyLibrarySyncPath struct {
	ID          int64
	LibraryID   string
	SyncPathID  int64
	LibraryName string
}

// EmbyLibrary 是 Emby 媒体库基础记录。
type EmbyLibrary struct {
	ID         int64
	Name       string
	LibraryID  string
	SyncPathID int64
}

// EmbyIndexRepository 是 Emby 本地索引的仓储。
// 索引本身是纯派生数据，删除应为幂等操作。
type EmbyIndexRepository interface {
	// UpsertLibraries 按 library_id 更新或创建媒体库记录。
	UpsertLibraries(ctx context.Context, libs []EmbyLibrary) error
	// CleanupDeletedLibraries 清理已不在 Emby 中存在的媒体库及其同步路径关联。
	CleanupDeletedLibraries(ctx context.Context, activeLibraryIDs []string) error
	// ListLibraries 返回全部媒体库记录。
	ListLibraries(ctx context.Context) ([]EmbyLibrary, error)

	// CreateOrUpdateItem 按 item_id upsert 单个媒体条目。
	CreateOrUpdateItem(ctx context.Context, item *EmbyMediaItem) error
	// GetItem 按 Emby ItemId 读取条目。
	GetItem(ctx context.Context, itemID string) (*EmbyMediaItem, error)
	// ItemsBySeasonID / ItemsBySeriesID 返回季/剧下的条目，用于删除级联。
	ItemsBySeasonID(ctx context.Context, seasonID string) ([]EmbyMediaItem, error)
	ItemsBySeriesID(ctx context.Context, seriesID string) ([]EmbyMediaItem, error)
	// ItemsByLibraryID 返回某媒体库下的全部条目。
	ItemsByLibraryID(ctx context.Context, libraryID string) ([]EmbyMediaItem, error)
	// CountItems 返回索引条目总数。
	CountItems(ctx context.Context) (int64, error)
	// CleanupOrphanedItems 删除不在 validItemIDs 中的条目（全量同步后的孤儿清理）。
	CleanupOrphanedItems(ctx context.Context, validItemIDs []string) (int64, error)
	// CleanupStaleItemsByLibrarySyncRun 按全量同步批次清理指定媒体库内未出现的旧条目。
	CleanupStaleItemsByLibrarySyncRun(ctx context.Context, libraryID, syncRunID string) (int64, error)
	// DeleteItemByID 删除单个条目的索引及其文件关联。
	DeleteItemByID(ctx context.Context, itemID string) error
	// DeleteItemsBySeasonID / DeleteItemsBySeriesID 删除季/剧下条目及其关联。
	DeleteItemsBySeasonID(ctx context.Context, seasonID string) error
	DeleteItemsBySeriesID(ctx context.Context, seriesID string) error

	// CreateMediaSyncFile 创建条目↔文件关联（已存在则跳过）。
	CreateMediaSyncFile(ctx context.Context, rel *EmbyMediaSyncFile) error
	// MediaSyncFilesByItemID 返回某条目的全部文件关联。
	MediaSyncFilesByItemID(ctx context.Context, embyItemID int64) ([]EmbyMediaSyncFile, error)
	// ListMediaSyncFiles 返回全部关联，供巡检做「索引 vs 网盘」差集比对。
	ListMediaSyncFiles(ctx context.Context) ([]EmbyMediaSyncFile, error)
	// DeleteMediaSyncFilesBySyncFileID 按网盘文件删除关联。
	DeleteMediaSyncFilesBySyncFileID(ctx context.Context, syncFileID int64) error
	// DeleteMediaSyncFilesByPickCode 按 PickCode 删除关联。
	DeleteMediaSyncFilesByPickCode(ctx context.Context, pickCode string) error
	// DeleteMediaSyncFileRow 按行号删除单条关联，供巡检精确清理失效记录。
	DeleteMediaSyncFileRow(ctx context.Context, rowID int64) error

	// CreateOrUpdateLibrarySyncPath 创建或更新媒体库↔同步任务关联（已存在则跳过）。
	CreateOrUpdateLibrarySyncPath(ctx context.Context, libraryID string, syncPathID int64, libraryName string) error
	// DeleteLibrarySyncPathsBySyncPathID 按同步任务删除关联。
	DeleteLibrarySyncPathsBySyncPathID(ctx context.Context, syncPathID int64) error
	// LibraryIDsBySyncPathID 返回同步任务关联到的 媒体库ID→媒体库名。
	LibraryIDsBySyncPathID(ctx context.Context, syncPathID int64) (map[string]string, error)

	// CleanupAllLibraryData 清理全部 Emby 索引数据。
	CleanupAllLibraryData(ctx context.Context) error
	// CleanupUnselectedLibraryData 清理未选中媒体库的数据；selector 为空时走全量清理。
	CleanupUnselectedLibraryData(ctx context.Context, selectedLibraryIDs []string) error

	// GetSyncCursor 读取某配置的增量同步游标；不存在时返回零值。
	GetSyncCursor(ctx context.Context, configID string) (EmbySyncCursor, error)
	// AdvanceSyncCursor 在增量同步成功后推进游标。
	AdvanceSyncCursor(ctx context.Context, configID string, cursorAt int64, incrementalAt int64) error
	// TouchSyncTime 记录一次成功同步的完成时间（全量与增量共用）。
	TouchSyncTime(ctx context.Context, configID string, syncTime int64) error
}

// EmbySyncCursor 是增量同步游标的持久化状态。
// 现版把 Emby 配置存在 settings 的 emby_proxy_instances 里，不宜再塞入同步态，
// 因此单独存一张表（emby_sync_state），按配置 ID 记录游标与上次同步时间。
type EmbySyncCursor struct {
	ConfigID             string
	LastSavedCursorAt    int64
	LastSyncTime         int64
	LastIncrementalAt    int64
	UpdatedAt            time.Time
}
