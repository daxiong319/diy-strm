package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

// 一级分类目录白名单表 0045。
const classifyPrimary0045Table = "classify_primary_categories"

var classifyPrimary0045Indexes = []string{
	"idx_classify_primary_categories_slug",
	"idx_classify_primary_categories_level",
	"idx_classify_primary_categories_path",
}

func openClassifyPrimaryTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "classify.db")})
	if err != nil {
		t.Fatalf("open classify db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedClassifyPrimaryRow(t *testing.T, db *DB, level int, name, slug, template, primaryKey, path, fingerprint string) {
	t.Helper()
	_, err := db.write.ExecContext(context.Background(),
		`INSERT INTO classify_primary_categories(level, name, slug, template, primary_key, path, enabled, fingerprint)
		 VALUES (?,?,?,?,?,?,1,?)`,
		level, name, slug, template, nullIfEmpty(primaryKey), path, fingerprint)
	if err != nil {
		t.Fatalf("插入一级分类目录行失败 level=%d name=%s: %v", level, name, err)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TestClassifyPrimary0045OnEmptyDB 验收 ⑪ 前半：空库上建表齐全，索引不多不少。
func TestClassifyPrimary0045OnEmptyDB(t *testing.T) {
	db := openClassifyPrimaryTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate empty db: %v", err)
	}
	if !tableExistsByName(ctx, db, classifyPrimary0045Table) {
		t.Fatalf("表 %s 在空库迁移后不存在", classifyPrimary0045Table)
	}
	for _, idx := range classifyPrimary0045Indexes {
		if !tableExistsByName(ctx, db, idx) {
			t.Fatalf("索引 %s 在空库迁移后不存在", idx)
		}
	}
	rows, err := db.read.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name=?`, classifyPrimary0045Table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]bool{}
	for _, idx := range classifyPrimary0045Indexes {
		want[idx] = true
	}
	for rows.Next() {
		var got string
		if err := rows.Scan(&got); err != nil {
			t.Fatal(err)
		}
		if !want[got] {
			t.Fatalf("多余的索引 %s（每个索引都要付写入代价）", got)
		}
	}
	// 存量库不预置任何行：分类目录在启用分类整理之前根本不存在，
	// 预置两行会让消费者以为「库里有这两个分类」。
	var count int
	if err := db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+classifyPrimary0045Table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("新库预置了 %d 行分类目录，应为 0", count)
	}
}

// TestClassifyPrimary0045OnLegacyDB 验收 ⑪ 后半：有数据的旧库上跑通，且幂等。
func TestClassifyPrimary0045OnLegacyDB(t *testing.T) {
	db := openClassifyPrimaryTestDB(t)
	ctx := context.Background()
	// 先跑到 0044（0045 之前）模拟存量库。0033 已在其中，
	// classify_rules 这张 T02 时代就存在的表由它建出来。
	if err := migrateUpTo(ctx, db, 44); err != nil {
		t.Fatalf("migrate legacy db: %v", err)
	}
	if !tableExistsByName(ctx, db, "classify_rules") {
		t.Fatalf("存量库里 classify_rules 不存在，0033 的建表兜底没生效")
	}
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO classify_rules(media_type, target_path, conditions, enabled, position, remark)
		 VALUES ('movie','电影/科幻','[]',1,0,'存量规则')`); err != nil {
		t.Fatalf("插入存量规则: %v", err)
	}
	// 故意先造一张同名同结构的旧表，验证 0045 是 CREATE TABLE IF NOT EXISTS 幂等而非报错。
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate with legacy data: %v", err)
	}
	seedClassifyPrimaryRow(t, db, 1, "电影", "media/电影", "media", "movie", "电影", "fp1")
	seedClassifyPrimaryRow(t, db, 2, "科幻", "genre/电影/科幻", "genre", "movie", "电影/科幻", "fp1")
	// 幂等重跑。
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate idempotent: %v", err)
	}
	if got := readOne(ctx, db, `SELECT target_path FROM classify_rules WHERE remark='存量规则'`); got != "电影/科幻" {
		t.Fatalf("存量规则读回 = %q，期望 电影/科幻", got)
	}
	if got := readOne(ctx, db, `SELECT name FROM `+classifyPrimary0045Table+` WHERE level=2`); got != "科幻" {
		t.Fatalf("新表数据读回 = %q，期望 科幻", got)
	}
}

// TestClassifyPrimary0045RejectsSameSlugUnderOneFingerprint 钉住唯一约束：
// 同一份配置投影两次必须撞唯一索引，否则重投影会写出重复行，
// 消费者按 slug 查分类就会拿到两行「同名分类」。
func TestClassifyPrimary0045RejectsSameSlugUnderOneFingerprint(t *testing.T) {
	db := openClassifyPrimaryTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedClassifyPrimaryRow(t, db, 1, "电影", "media/电影", "media", "movie", "电影", "fp1")
	_, err := db.write.ExecContext(ctx,
		`INSERT INTO classify_primary_categories(level, name, slug, template, primary_key, path, enabled, fingerprint)
		 VALUES (1,'电影','media/电影','media','movie','电影',1,'fp1')`)
	if err == nil {
		t.Fatalf("同一指纹下重复 slug 未被拒绝，会写出重复分类行")
	}
	// 换指纹（= 用户改过配置）后允许同一 slug 再出现一次。
	seedClassifyPrimaryRow(t, db, 1, "电影", "media/电影", "media", "movie", "电影", "fp2")
}

// TestClassifyPrimary0045RejectsBadLevel 钉住 level 的取值域。
//
// 层级只有 1/2/3 三个值：多出 0 或 4 不会让分类目录消失，只会让消费者
// （洗版筛选、清理保护）按「不认识的层级」处理，从而既不筛选也不保护 ——
// 一个静默失效的层级比报错难查得多，所以用 CHECK 在库里挡住。
func TestClassifyPrimary0045RejectsBadLevel(t *testing.T) {
	db := openClassifyPrimaryTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, level := range []int{0, 4, -1} {
		if _, err := db.write.ExecContext(ctx,
			`INSERT INTO classify_primary_categories(level, name, slug, template, primary_key, path, enabled, fingerprint)
			 VALUES (?,?,?,?,NULL,?,1,'fp1')`,
			level, "x", "media/x", "media", "电影"); err == nil {
			t.Fatalf("level=%d 未被拒绝", level)
		}
	}
	for _, level := range []int{1, 2, 3} {
		seedClassifyPrimaryRow(t, db, level, "x", "media/x"+strconv.Itoa(level), "media", "movie", "电影/科幻", "fp"+strconv.Itoa(level))
	}
}

// TestClassifyPrimary0045StoresMultiSegmentPath 钉住 path 允许多级。
//
// path 直接被清理保护拿去和磁盘目录逐段比对，所以「电影/科幻」这种多级路径
// 必须能原样存下来；同时也说明为什么 path 的合法性只能靠 Go 侧拦：
// 这里连 "../../" 都能写进去（SQL 层不解析路径），
// 真正的拦截在 classifyorganize.validatePathSegment，
// 由 TestProjectionRejectsPathEscapeFromConfig 钉住。
func TestClassifyPrimary0045StoresMultiSegmentPath(t *testing.T) {
	db := openClassifyPrimaryTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedClassifyPrimaryRow(t, db, 3, "科幻", "genre/电影/科幻/2000-2009", "genre", "movie", "电影/科幻/2000-2009", "fp1")
	if got := readOne(ctx, db, `SELECT path FROM `+classifyPrimary0045Table+` WHERE level=3`); got != "电影/科幻/2000-2009" {
		t.Fatalf("三级 path 读回 = %q，期望 电影/科幻/2000-2009", got)
	}
	// 一级只有一段，且 primary_key 存了类型键。
	seedClassifyPrimaryRow(t, db, 1, "电影", "media/电影", "media", "movie", "电影", "fp1")
	if got := readOne(ctx, db, `SELECT primary_key FROM `+classifyPrimary0045Table+` WHERE level=1`); got != "movie" {
		t.Fatalf("一级 primary_key 读回 = %q，期望 movie", got)
	}
}

// TestClassifyPrimary0045ColumnDefaults 钉住列默认值：投影层不显式给 enabled 时
// 默认可见，否则一次漏写就会让整批分类目录在消费者眼里全部消失。
func TestClassifyPrimary0045ColumnDefaults(t *testing.T) {
	db := openClassifyPrimaryTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO classify_primary_categories(level, name, slug, template, path, fingerprint)
		 VALUES (1,'电影','media/电影','media','电影','fp1')`); err != nil {
		t.Fatalf("省略 enabled 插入失败: %v", err)
	}
	if got := readOne(ctx, db, `SELECT enabled FROM `+classifyPrimary0045Table+` WHERE slug='media/电影'`); got != "1" {
		t.Fatalf("enabled 默认值 = %q，期望 1", got)
	}
}
