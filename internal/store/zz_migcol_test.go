package store

import (
	"context"
	"testing"
)

// TestMigrateAppliesAllMigrationsOnce 复现/防止「同号迁移」缺陷。
//
// 迁移版本号取自文件名首个下划线前的数字（internal/store/migrate.go:36-38 的
// `strconv.Atoi(e.Name()[:idx])`），而 schema_migrations 的 version 是 INTEGER PRIMARY KEY。
// 因此两个同号迁移（如 0030_mcp.sql 与 0030_subtitle.sql）会在第二条 INSERT 时撞主键，
// 报 `record 0030_xxx.sql: UNIQUE constraint failed: schema_migrations.version`，
// 使整个 Migrate 失败、服务起不来。
func TestMigrateAppliesAllMigrationsOnce(t *testing.T) {
	db, err := Open(context.Background(), Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移失败（若为同号迁移撞主键，说明版本号有重复）：%v", err)
	}
}
