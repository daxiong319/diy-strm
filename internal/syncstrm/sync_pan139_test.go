package syncstrm

import (
	"io"
	"log"
	"testing"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// 剪枝判定单测：指纹命中（TTL 内 UTime 未变）+ 上次数据背书 + 非入口目录才允许跳过

func setupPan139PruneTest(t *testing.T) *SyncStrm {
	t.Helper()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := testDb.AutoMigrate(&models.Pan139DirCache{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	db.Db = testDb
	t.Cleanup(func() {
		if sqlDb, err := testDb.DB(); err == nil {
			sqlDb.Close()
		}
	})

	lastSync := time.Now().Unix() - 24*3600
	s := &SyncStrm{
		Account:      &models.Account{BaseModel: models.BaseModel{ID: 7}, SourceType: models.SourceTypePan139},
		LastSyncAt:   lastSync,
		SourcePathId: "root-id",
	}
	s.memSyncCache = NewMemorySyncCache(1)
	// 预载「上次同步见过 d2 的子项」
	s.memSyncCache.Insert(&SyncFileCache{
		FileId:     "f1",
		ParentId:   "d2",
		FileName:   "a.mp4",
		Path:       "/d2",
		SourceType: models.SourceTypePan139,
	})
	return s
}

func TestCanSkipPan139Dir(t *testing.T) {
	s := setupPan139PruneTest(t)
	oldMtime := s.LastSyncAt - 3600

	// 入口目录永不剪
	if s.canSkipPan139Dir(pathQueueItem{PathId: "root-id", Mtime: oldMtime}) {
		t.Fatal("入口目录不应被剪枝")
	}
	// UTime=0 不可信，不剪
	if s.canSkipPan139Dir(pathQueueItem{PathId: "d1", Mtime: 0}) {
		t.Fatal("UTime=0 的目录不应被剪枝")
	}
	// 修改时间晚于上次同步（含安全窗）→ 必须扫描
	if s.canSkipPan139Dir(pathQueueItem{PathId: "d1", Mtime: s.LastSyncAt - 60}) {
		t.Fatal("安全窗内修改过的目录不应被剪枝")
	}
	// UTime 足够旧但没有指纹快照 → 不剪
	if s.canSkipPan139Dir(pathQueueItem{PathId: "d2", Mtime: oldMtime}) {
		t.Fatal("没有指纹快照的目录不应被剪枝")
	}
	// 写入指纹快照（UTime 匹配、未过期）→ 允许剪
	models.UpsertPan139DirCache(7, "d2", "/d2", oldMtime)
	if !s.canSkipPan139Dir(pathQueueItem{PathId: "d2", Mtime: oldMtime}) {
		t.Fatal("指纹命中且有数据背书的目录应被剪枝")
	}
	// UTime 变化（指纹不匹配）→ 不剪
	if s.canSkipPan139Dir(pathQueueItem{PathId: "d2", Mtime: oldMtime + 10}) {
		t.Fatal("UTime 变化后的目录不应被剪枝")
	}
	// 恰好等于安全窗边界：UTime 是边界值本身，与指纹快照的 oldMtime 不一致 → 不剪
	if s.canSkipPan139Dir(pathQueueItem{PathId: "d2", Mtime: s.LastSyncAt - pan139PruneSafetyWindow}) {
		t.Fatal("UTime 与指纹不一致的目录不应被剪枝")
	}
	// 安全窗边界 +1 秒 → 不剪（边界内必须扫描）
	if s.canSkipPan139Dir(pathQueueItem{PathId: "d2", Mtime: s.LastSyncAt - pan139PruneSafetyWindow + 1}) {
		t.Fatal("UTime 在安全窗内的目录不应被剪枝")
	}
	// 非 139 账号类型不剪
	s.Account.SourceType = models.SourceType123
	if s.canSkipPan139Dir(pathQueueItem{PathId: "d2", Mtime: oldMtime}) {
		t.Fatal("非 139 账号不应启用剪枝")
	}
}

func TestCanSkipPan139DirStaleSnapshot(t *testing.T) {
	s := setupPan139PruneTest(t)
	oldMtime := s.LastSyncAt - 3600
	models.UpsertPan139DirCache(7, "d2", "/d2", oldMtime)
	// 快照人为老化超过 TTL → 强制重列
	db.Db.Model(&models.Pan139DirCache{}).Where("account_id = ? AND dir_id = ?", 7, "d2").
		Update("scanned_at", time.Now().Unix()-pan139CacheTTLSeconds-60)
	if s.canSkipPan139Dir(pathQueueItem{PathId: "d2", Mtime: oldMtime}) {
		t.Fatal("快照超过 TTL 的目录不应被剪枝（需重列刷新）")
	}
}

func TestUpsertPan139DirCacheRefresh(t *testing.T) {
	setupPan139PruneTest(t)
	models.UpsertPan139DirCache(7, "d1", "/d1", 100)
	row := models.FindPan139DirCache(7, "d1")
	if row == nil || row.MTime != 100 {
		t.Fatal("首次写入指纹快照失败")
	}
	// 二次写入应更新而非新增
	time.Sleep(1100 * time.Millisecond)
	models.UpsertPan139DirCache(7, "d1", "/d1-new", 200)
	row = models.FindPan139DirCache(7, "d1")
	if row == nil || row.MTime != 200 || row.DirPath != "/d1-new" {
		t.Fatalf("指纹快照应被覆盖更新，实际 %+v", row)
	}
	var count int64
	db.Db.Model(&models.Pan139DirCache{}).Where("account_id = ? AND dir_id = ?", 7, "d1").Count(&count)
	if count != 1 {
		t.Fatalf("同目录指纹应只有一行，实际 %d", count)
	}
	// mtime<=0 不写
	models.UpsertPan139DirCache(7, "d0", "/d0", 0)
	if models.FindPan139DirCache(7, "d0") != nil {
		t.Fatal("UTime=0 不应写指纹")
	}
}

func TestPan139DirCacheFresh(t *testing.T) {
	row := &models.Pan139DirCache{MTime: 100, ScannedAt: time.Now().Unix()}
	if !row.Pan139DirCacheFresh(100, 3600) {
		t.Fatal("UTime 匹配且未过期应命中")
	}
	if row.Pan139DirCacheFresh(101, 3600) {
		t.Fatal("UTime 不匹配不应命中")
	}
	old := &models.Pan139DirCache{MTime: 100, ScannedAt: time.Now().Unix() - 7200}
	if old.Pan139DirCacheFresh(100, 3600) {
		t.Fatal("超 TTL 不应命中")
	}
	zero := &models.Pan139DirCache{MTime: 0, ScannedAt: time.Now().Unix()}
	if zero.Pan139DirCacheFresh(0, 3600) {
		t.Fatal("MTime=0 不应命中")
	}
	var nilRow *models.Pan139DirCache
	if nilRow.Pan139DirCacheFresh(100, 3600) {
		t.Fatal("空快照不应命中")
	}
}
