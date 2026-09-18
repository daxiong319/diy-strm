package models

import (
	"io"
	"log"
	"testing"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupCheckinTestDB(t *testing.T) {
	t.Helper()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&HiveCheckinRecord{}, &HiveOAuthAccount{}); err != nil {
		t.Fatalf("迁移表失败: %v", err)
	}
}

// 回归：HasCheckedInToday 必须按本地时区「今天 0 点」判定。
// 旧实现 time.Now().Truncate(24h) 是 UTC 截断，UTC+8 下得到本地 08:00，
// 08:00 前的成功签到会被误判为非今日 → 每小时整点兜底重复签到。
func TestHasCheckedInTodayLocalMidnightBoundary(t *testing.T) {
	setupCheckinTestDB(t)

	rec := &HiveCheckinRecord{AccountID: 1, Label: "主账号", OK: true, Trigger: "daily", Message: "签到成功"}
	// 模拟北京时间凌晨 00:30 的成功签到（UTC 截断判定下 start=08:00 会漏掉它）
	earlyMorning := time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 30, 0, 0, time.Local)
	rec.CheckinAt = earlyMorning
	if err := AddHiveCheckinRecord(rec); err != nil {
		t.Fatalf("写入签到记录失败: %v", err)
	}

	if !HasCheckedInToday(1) {
		t.Fatalf("本地 00:30 的成功签到应判定为「今日已签到」（防每小时重复签到）")
	}
	if HasCheckedInToday(2) {
		t.Fatalf("未签到账号不应判定为已签到")
	}
}

// 昨天的成功签到不应算作今日已签到
func TestHasCheckedInTodayIgnoresYesterday(t *testing.T) {
	setupCheckinTestDB(t)

	rec := &HiveCheckinRecord{AccountID: 1, Label: "主账号", OK: true, Trigger: "daily", Message: "签到成功"}
	rec.CheckinAt = time.Now().AddDate(0, 0, -1)
	if err := AddHiveCheckinRecord(rec); err != nil {
		t.Fatalf("写入签到记录失败: %v", err)
	}

	if HasCheckedInToday(1) {
		t.Fatalf("昨天的签到不应判定为今日已签到")
	}
}

// 失败的签到记录不能算作已签到
func TestHasCheckedInTodayIgnoresFailedRecord(t *testing.T) {
	setupCheckinTestDB(t)

	rec := &HiveCheckinRecord{AccountID: 1, Label: "主账号", OK: false, Trigger: "daily", Message: "签到失败"}
	rec.CheckinAt = time.Now()
	if err := AddHiveCheckinRecord(rec); err != nil {
		t.Fatalf("写入签到记录失败: %v", err)
	}

	if HasCheckedInToday(1) {
		t.Fatalf("失败记录不应判定为已签到")
	}
}

// 扫描间隔归一化：0=默认 5，越界夹紧 [1,1440]，正常值透传
func TestAutoOrganizeNormalizedScanInterval(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 5},
		{-3, 5},
		{1, 1},
		{5, 5},
		{30, 30},
		{1440, 1440},
		{1441, 1440},
	}
	for _, c := range cases {
		cfg := AutoOrganizeConfig{ScanIntervalMinutes: c.in}
		if got := cfg.NormalizedScanIntervalMinutes(); got != c.want {
			t.Errorf("NormalizedScanIntervalMinutes(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
