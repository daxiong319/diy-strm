// Package ddb 为发现板块提供 GORM 数据库句柄，复用 LitePan 主 SQLite 文件。
// discovery 包的表（favorites/subscriptions/emby_missing 等）通过 AutoMigrate 自动建表，
// 与 LitePan 手写 SQL 的表在同一库共存（WAL 模式，modernc sqlite）。
package ddb

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Db 是发现板块使用的 GORM 句柄（对应老 diy-strm/internal/db.Db）。
var Db *gorm.DB

var (
	mu       sync.Mutex
	initErr  error
	inited   bool
)

// Init 用 LitePan 的数据库路径初始化 GORM 句柄；幂等，可重复调用。
func Init(dbPath string, log *slog.Logger) error {
	mu.Lock()
	defer mu.Unlock()
	if inited {
		return initErr
	}
	inited = true

	gcfg := &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	}
	// 与 LitePan 主库一致的 WAL/超时参数；glearez sqlite 底层同为 modernc。
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(on)", dbPath)
	db, err := gorm.Open(sqlite.Open(dsn), gcfg)
	if err != nil {
		initErr = fmt.Errorf("打开发现板块数据库失败: %w", err)
		return initErr
	}
	// GORM 连接池对齐：限制写并发，避免 SQLITE_BUSY。
	if sqldb, err := db.DB(); err == nil {
		sqldb.SetMaxOpenConns(1)
		sqldb.SetMaxIdleConns(1)
	}
	Db = db
	return nil
}

// MustDb 返回已初始化的句柄；未初始化时返回 nil（调用方应判空）。
func MustDb() *gorm.DB { return Db }
