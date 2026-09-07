package models

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"diy-strm/internal/db"
	"diy-strm/internal/pan123"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupFixRegressionTestDB(t *testing.T) {
	t.Helper()
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDB
	if err := db.Db.AutoMigrate(&DbUploadTask{}); err != nil {
		t.Fatalf("迁移上传任务表失败: %v", err)
	}
	t.Cleanup(func() { db.Db = nil })
}

// 回归（三批修复）：123 上传 fileId=0 时回落 Info.FileId，都无效返回 0（由调用方判失败，
// 不再把 "0" 当成完成 ID 落库——STRM/整理/秒传链路都依赖真实文件 ID）。
func TestPan123EffectiveFileIDFallback(t *testing.T) {
	mkInfo := func(id int64) *struct {
		FileId   int64  `json:"FileId"`
		FileName string `json:"FileName"`
		Size     int64  `json:"Size"`
		Type     int    `json:"Type"`
	} {
		return &struct {
			FileId   int64  `json:"FileId"`
			FileName string `json:"FileName"`
			Size     int64  `json:"Size"`
			Type     int    `json:"Type"`
		}{FileId: id, FileName: "x.mkv"}
	}

	// 顶层 FileId 有效：直接采用
	resp := &pan123.UploadResp{}
	resp.Data.FileId = 424242
	if got := pan123EffectiveFileID(resp); got != 424242 {
		t.Fatalf("顶层 FileId 应直接采用，实际 %d", got)
	}

	// 顶层为 0：回落 Info.FileId（目录/占位对象场景）
	resp2 := &pan123.UploadResp{}
	resp2.Data.Info = mkInfo(515151)
	if got := pan123EffectiveFileID(resp2); got != 515151 {
		t.Fatalf("顶层为 0 应回落 Info.FileId=515151，实际 %d", got)
	}

	// 两者都无：返回 0（调用方 task.Fail）
	resp3 := &pan123.UploadResp{}
	if got := pan123EffectiveFileID(resp3); got != 0 {
		t.Fatalf("两者都无效应返回 0，实际 %d", got)
	}

	// nil 防护
	if got := pan123EffectiveFileID(nil); got != 0 {
		t.Fatalf("nil 响应应返回 0，实际 %d", got)
	}

	// 顶层优先于 Info（顶层是权威值时不得被 Info 覆盖）
	resp4 := &pan123.UploadResp{}
	resp4.Data.FileId = 111
	resp4.Data.Info = mkInfo(222)
	if got := pan123EffectiveFileID(resp4); got != 111 {
		t.Fatalf("顶层 FileId 优先，实际 %d", got)
	}
}

// 回归（三批修复）：跨盘中转临时文件在失败/取消终态也必须清理（原仅成功路径清理，GB 级残留）。
func TestCrossTransferTempFileCleanupOnTerminalStates(t *testing.T) {
	setupFixRegressionTestDB(t)

	mkTask := func(name string) (*DbUploadTask, string) {
		tmp := filepath.Join(t.TempDir(), name+".part")
		if err := os.WriteFile(tmp, []byte("data"), 0o644); err != nil {
			t.Fatalf("写临时文件失败：%v", err)
		}
		task := &DbUploadTask{
			Source:        UploadSourceCrossTransfer,
			LocalFullPath: tmp,
			Status:        UploadStatusPending,
			FileName:      name,
		}
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatalf("创建任务失败：%v", err)
		}
		return task, tmp
	}

	// Fail 终态清理
	task, tmp := mkTask("fail-case")
	task.Fail(errors.New("模拟上传失败"))
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("Fail 后跨盘中转临时文件应被清理：%s", tmp)
	}
	if task.Status != UploadStatusFailed {
		t.Fatalf("任务状态应为 failed，实际 %s", task.Status)
	}

	// Cancel 终态清理
	task2, tmp2 := mkTask("cancel-case")
	task2.Cancel()
	if _, err := os.Stat(tmp2); !os.IsNotExist(err) {
		t.Fatalf("Cancel 后跨盘中转临时文件应被清理：%s", tmp2)
	}

	// 非跨盘来源不动临时文件
	task3, tmp3 := mkTask("normal-case")
	task3.Source = UploadSourceStrm
	task3.Fail(errors.New("普通任务失败"))
	if _, err := os.Stat(tmp3); err != nil {
		t.Fatalf("非跨盘任务的本地文件不应被清理：%v", err)
	}

	// cancelWithError 终态清理
	task4, tmp4 := mkTask("cancel-err-case")
	task4.cancelWithError(errors.New("模拟取消"))
	if _, err := os.Stat(tmp4); !os.IsNotExist(err) {
		t.Fatalf("cancelWithError 后临时文件应被清理：%s", tmp4)
	}
}

// 回归（三批修复）：ParseCronDescription 用 Fields 切分——双空格表达式不再误判"无效"
// （与 ValidateCronExpression 的 cron.ParseStandard 行为对齐）。
func TestParseCronDescriptionToleratesDoubleSpace(t *testing.T) {
	sp := &ScrapePath{}
	if desc := sp.ParseCronDescription("*/5  *  *  *  *"); desc == "无效的 Cron 表达式" {
		t.Fatalf("双空格 Cron 表达式不应被判无效（Fields 切分修复未生效），desc=%s", desc)
	}
	if desc := sp.ParseCronDescription("0 3 * * *"); desc == "无效的 Cron 表达式" {
		t.Fatalf("单空格正常表达式不应被判无效，desc=%s", desc)
	}
	// token 内容合法性归 ValidateCronExpression 管，本函数只做段数校验与描述生成
	if desc := sp.ParseCronDescription("* * * * * * *"); desc != "无效的 Cron 表达式" {
		t.Fatalf("非 5 段表达式应报无效，desc=%s", desc)
	}
}
