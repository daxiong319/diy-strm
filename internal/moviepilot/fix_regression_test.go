package moviepilot

import (
	"io"
	"log"
	"strings"
	"testing"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// 测试基建：内存库 + 静默 logger（washHandleNewLoser / applyDeferredWashLosers 落 WashLog 需要）
func setupWashRegressionEnv(t *testing.T) {
	t.Helper()
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := testDB.AutoMigrate(&models.WashLog{}); err != nil {
		t.Fatalf("迁移 WashLog 失败: %v", err)
	}
	oldDB := db.Db
	db.Db = testDB
	oldLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		db.Db = oldDB
		helpers.AppLogger = oldLogger
	})
}

// 回归（三批修复）：washCompareAndApply 不再「先删旧后移新」——旧文件处置生成延后动作清单
// （pendingLosers），由调用方在新文件成功移入后执行 applyDeferredWashLosers。
// 契约：比较通过时 proceed=true 且不触发任何网盘删除/移动；pendingLosers 携带匹配的旧文件。
func TestWashCompareAndApplyDefersLoserDisposal(t *testing.T) {
	setupWashRegressionEnv(t)
	cfg := &models.AutoOrganizeConfig{AccountID: 1, LoserSourceAction: "delete"}
	newQ := ParseQualityFromName("Film.2024.2160p.BluRay.REMUX.H265.DV.mkv")
	entries := []organizeEntry{
		{ID: "old-1", Name: "Film.2024.1080p.WEB-DL.H264.mkv", ParentID: "dir-1", Size: 1024},
		{ID: "keep-1", Name: "Other.Show.S01E01.1080p.mkv", ParentID: "dir-1", Size: 2048},
	}

	decision := washCompareAndApply(nil, nil, cfg, &organizeEntry{ID: "new-1", Name: "Film.2024.2160p.BluRay.REMUX.H265.DV.mkv"}, &IdentifyResult{}, "Film", 2024, 123, "电影/Film (2024)", "Film.2024.2160p.BluRay.REMUX.H265.DV.mkv", newQ, entries)

	if !decision.proceed {
		t.Fatalf("新文件更优应 proceed=true，skipMessage=%s", decision.skipMessage)
	}
	if len(decision.pendingLosers) != 1 {
		t.Fatalf("应生成 1 条延后处置（匹配 1 个旧版本），实际 %d", len(decision.pendingLosers))
	}
	if decision.pendingLosers[0].entry.ID != "old-1" {
		t.Fatalf("延后处置应指向旧版本 old-1，实际 %s", decision.pendingLosers[0].entry.ID)
	}
	if decision.pendingLosers[0].action != "delete" {
		t.Fatalf("延后处置动作应为配置的 delete，实际 %s", decision.pendingLosers[0].action)
	}
	if decision.pendingLosers[0].log == nil || decision.pendingLosers[0].log.OldName == "" {
		t.Fatal("延后处置应携带 wash_replace 日志（含旧文件名）")
	}
	if len(decision.treatments) == 0 {
		t.Fatalf("treatments 不应为空，实际 %v", decision.treatments)
	}
	joined := strings.Join(decision.treatments, "\n")
	if !strings.Contains(joined, "1 个旧版本") {
		t.Fatalf("treatments 应含匹配数量摘要，实际 %v", decision.treatments)
	}
	// 明细行应含新旧文件对比与决出项（洗版日志可归因）
	if !strings.Contains(joined, "分辨率 2160p>1080p") || !strings.Contains(joined, "vs") {
		t.Fatalf("treatments 应含新旧版本对比明细，实际 %v", decision.treatments)
	}
}

// 回归（三批修复）：新文件落败时 proceed=false 且不产生延后处置（走 washHandleNewLoser 原路径）。
func TestWashCompareAndApplyNewLoserNoPending(t *testing.T) {
	setupWashRegressionEnv(t)
	cfg := &models.AutoOrganizeConfig{AccountID: 1, LoserSourceAction: "keep"}
	newQ := ParseQualityFromName("Film.2024.720p.HDTV.X264.mkv")
	entries := []organizeEntry{
		{ID: "old-1", Name: "Film.2024.1080p.WEB-DL.H264.mkv", ParentID: "dir-1", Size: 1024},
	}
	decision := washCompareAndApply(nil, nil, cfg, &organizeEntry{ID: "new-1", Name: "Film.2024.720p.HDTV.X264.mkv"}, &IdentifyResult{}, "Film", 2024, 123, "电影/Film (2024)", "Film.2024.720p.HDTV.X264.mkv", newQ, entries)

	if decision.proceed {
		t.Fatal("新文件更差应 proceed=false")
	}
	if len(decision.pendingLosers) != 0 {
		t.Fatalf("新文件落败不应有延后处置，实际 %d", len(decision.pendingLosers))
	}
}

// 回归（三批修复）：applyDeferredWashLosers 的 keep 动作零网络操作且日志照常落（动作=配置值）。
func TestApplyDeferredWashLosersKeepNoop(t *testing.T) {
	setupWashRegressionEnv(t)
	log := &models.WashLog{OldName: "old.mkv"}
	decision := washDecision{
		proceed: true,
		pendingLosers: []washLoserOp{
			{entry: organizeEntry{ID: "old-1"}, action: "keep", log: log},
		},
	}
	// account=nil 时 keep 分支不触网（delete/archive 会 panic/报错，keep 必须无操作）
	applyDeferredWashLosers(nil, nil, &models.AutoOrganizeConfig{}, decision)
	if log.LoserTreated != "keep" {
		t.Fatalf("keep 动作日志应保持 keep，实际 %s", log.LoserTreated)
	}
	if log.Message == "" {
		t.Fatal("keep 动作应落日志消息")
	}
}
