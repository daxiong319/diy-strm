package models

import (
	"io"
	"log"
	"testing"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupMatchSyncPathTestDB(t *testing.T) {
	t.Helper()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&SyncPath{}); err != nil {
		t.Fatalf("迁移 SyncPath 失败: %v", err)
	}
}

// 回归：MoviePilot 整理/上传完成后触发的临时 STRM 同步（sync_path_id=0），
// 提交 Emby 刷新任务时需按 账号+路径前缀 反查真实同步目录，否则
// 「同步目录 N 未关联 Emby 媒体库」导致新剧首次生成 STRM 不刷新 Emby。
func TestMatchSyncPathForTempSync按路径前缀匹配真实同步目录(t *testing.T) {
	setupMatchSyncPathTestDB(t)

	paths := []SyncPath{
		{SourceType: SourceType123, AccountId: 4, LocalPath: "/media", RemotePath: "媒体库/已整理", BaseCid: "cid-123"},
		{SourceType: SourceTypePan139, AccountId: 3, LocalPath: "/media", RemotePath: "影视/已整理", BaseCid: "cid-139"},
	}
	for i := range paths {
		if err := db.Db.Create(&paths[i]).Error; err != nil {
			t.Fatalf("创建 SyncPath 失败: %v", err)
		}
	}

	// 交锋场景：临时同步源路径在 媒体库/已整理 之下，应命中 sync_path 3（123 账号 4）
	matched := MatchSyncPathForTempSync(4, "媒体库/已整理/国产剧集/交锋 (2026) {tmdb=294486}/Season 01", "/media")
	if matched == nil || matched.ID != paths[0].ID {
		t.Fatalf("应匹配到 123 同步目录，实际=%+v", matched)
	}

	// 139 的目录不能串到 123 的同步目录
	matched139 := MatchSyncPathForTempSync(3, "影视/已整理/国产剧集/炽夏 (2026) {tmdb=288603}/Season 01", "/media")
	if matched139 == nil || matched139.ID != paths[1].ID {
		t.Fatalf("应匹配到 139 同步目录，实际=%+v", matched139)
	}

	// 无匹配返回 nil
	if got := MatchSyncPathForTempSync(4, "别的盘/根目录", "/media"); got != nil {
		t.Fatalf("不相关路径不应匹配，实际=%+v", got)
	}
}

func TestMatchSyncPathForTempSync取最长前缀(t *testing.T) {
	setupMatchSyncPathTestDB(t)

	paths := []SyncPath{
		{SourceType: SourceType123, AccountId: 4, LocalPath: "/media", RemotePath: "媒体库/已整理", BaseCid: "cid-1"},
		{SourceType: SourceType123, AccountId: 4, LocalPath: "/media2", RemotePath: "媒体库/已整理/国产剧集", BaseCid: "cid-2"},
	}
	for i := range paths {
		if err := db.Db.Create(&paths[i]).Error; err != nil {
			t.Fatalf("创建 SyncPath 失败: %v", err)
		}
	}

	matched := MatchSyncPathForTempSync(4, "媒体库/已整理/国产剧集/交锋 (2026) {tmdb=294486}", "/media2")
	if matched == nil || matched.ID != paths[1].ID {
		t.Fatalf("应取最长前缀的同步目录，实际=%+v", matched)
	}
}
