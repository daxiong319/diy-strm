package syncstrm

import (
	"testing"

	"diy-strm/internal/models"
)

// 剪枝判定单测：只信「UTime 足够旧 + 上次同步见过子项 + 非入口目录」的组合

func TestCanPrunePan139Dir(t *testing.T) {
	lastSync := int64(1000000000)
	s := &SyncStrm{
		Account:     &models.Account{SourceType: models.SourceTypePan139},
		LastSyncAt:  lastSync,
		SourcePathId: "root-id",
	}
	s.memSyncCache = NewMemorySyncCache(1)

	// 入口目录永不剪
	if s.canPrunePan139Dir(pathQueueItem{PathId: "root-id", Path: "/", Mtime: 0}) {
		t.Fatal("入口目录不应被剪枝")
	}
	// UTime=0 不可信，不剪
	if s.canPrunePan139Dir(pathQueueItem{PathId: "d1", Mtime: 0}) {
		t.Fatal("UTime=0 的目录不应被剪枝")
	}
	// 修改时间晚于上次同步（含安全窗）→ 必须扫描
	if s.canPrunePan139Dir(pathQueueItem{PathId: "d1", Mtime: lastSync - 60}) {
		t.Fatal("安全窗内修改过的目录不应被剪枝")
	}
	// UTime 足够旧但没有上次数据背书 → 不剪
	if s.canPrunePan139Dir(pathQueueItem{PathId: "d2", Mtime: lastSync - 3600}) {
		t.Fatal("没有上次同步数据背书的目录不应被剪枝")
	}
	// UTime 足够旧 + 上次同步见过子项 → 允许剪
	s.memSyncCache.Insert(&SyncFileCache{
		FileId:     "f1",
		ParentId:   "d2",
		FileName:   "a.mp4",
		Path:       "/d2",
		SourceType: models.SourceTypePan139,
	})
	if !s.canPrunePan139Dir(pathQueueItem{PathId: "d2", Mtime: lastSync - 3600}) {
		t.Fatal("UTime 足够旧且有数据背书的目录应被剪枝")
	}
	// 恰好等于安全窗边界：UTime == LastSyncAt - window → 允许剪
	if !s.canPrunePan139Dir(pathQueueItem{PathId: "d2", Mtime: lastSync - pan139PruneSafetyWindow}) {
		t.Fatal("UTime 恰好等于安全窗边界的目录应被剪枝")
	}
	// 安全窗边界 +1 秒 → 不剪
	if s.canPrunePan139Dir(pathQueueItem{PathId: "d2", Mtime: lastSync - pan139PruneSafetyWindow + 1}) {
		t.Fatal("UTime 在安全窗内的目录不应被剪枝")
	}
	// 非 139 账号类型不剪
	s.Account.SourceType = models.SourceType123
	if s.canPrunePan139Dir(pathQueueItem{PathId: "d2", Mtime: lastSync - 3600}) {
		t.Fatal("非 139 账号不应启用剪枝")
	}
}
