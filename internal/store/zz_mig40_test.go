package store

import (
	"context"
	"path/filepath"
	"testing"
)

// RBAC 六张表在 0040 里的全部索引名。
//
// 这份清单与 internal/rbac/store.go 的查询形状一一对应：
// 少一个索引不会让功能出错，但会让「列出用户」这类查询在用户多起来之后全表扫。
// 多一个索引则说明迁移文件里留了没用的东西 —— 每一个索引都要付写入代价。
var rbac0040Indexes = []string{
	"idx_rbac_users_enabled",
	"idx_rbac_users_username",
	"idx_rbac_users_username_fold",
	"idx_rbac_user_groups_builtin",
	"idx_rbac_user_groups_name",
	"idx_rbac_group_members_user",
	"idx_rbac_group_permissions_key",
	"idx_rbac_user_overrides_key",
}

// TestRBAC0040OnEmptyDB 验收 ⑧ 前半：空库上 0040 建表齐全且索引不多不少。
func TestRBAC0040OnEmptyDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "empty.db")})
	if err != nil {
		t.Fatalf("open empty db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate empty db: %v", err)
	}
	for _, tbl := range []string{
		"rbac_users",
		"rbac_permissions",
		"rbac_user_groups",
		"rbac_group_members",
		"rbac_group_permissions",
		"rbac_user_overrides",
	} {
		if !tableExistsByName(ctx, db, tbl) {
			t.Fatalf("表 %s 在空库迁移后不存在", tbl)
		}
	}
	for _, idx := range rbac0040Indexes {
		if !tableExistsByName(ctx, db, idx) {
			t.Fatalf("索引 %s 在空库迁移后不存在", idx)
		}
	}
	// 反向：不许有多余的 rbac 索引。
	rows, err := db.read.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND name LIKE 'idx_rbac%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]bool{}
	for _, n := range rbac0040Indexes {
		want[n] = true
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !want[name] {
			t.Fatalf("出现了未预期的 RBAC 索引 %s（索引清单变了请同步更新用例）", name)
		}
	}
}

// TestRBAC0040OnLegacyDB 验收 ⑧ 后半：存量库（停在 0037）上补跑 0040。
//
// 0040 全部是 CREATE TABLE IF NOT EXISTS，不碰任何已有表，所以存量库不该受影响。
// 这条用例盯的就是「别不小心 ALTER 到别人家的表上」—— T05 就在这一步栽过。
func TestRBAC0040OnLegacyDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "legacy.db")})
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	// 存量库里已经有的表：0037 建的三张洗版表 + 一个数据行。
	if err := migrateLegacyTo(ctx, db, 37); err != nil {
		t.Fatalf("迁移到 0037: %v", err)
	}
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO media_upgrade_rules(id, name, source, library_root, enabled)
		 VALUES(1, '存量规则', 'local', '/media', 1)`); err != nil {
		t.Fatalf("写存量数据: %v", err)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("在存量库上继续迁移: %v", err)
	}
	for _, tbl := range []string{"rbac_users", "rbac_permissions", "rbac_user_groups"} {
		if !tableExistsByName(ctx, db, tbl) {
			t.Fatalf("存量库迁移后缺表 %s", tbl)
		}
	}
	// 存量数据没被动过。
	got := readOne(ctx, db, `SELECT name FROM media_upgrade_rules WHERE id=1`)
	if got != "存量规则" {
		t.Fatalf("存量数据被改坏了: %q", got)
	}
	// 洗版表的列也没变（0040 不该 ALTER 它）。
	var enabled int
	if err := db.read.QueryRowContext(ctx,
		`SELECT enabled FROM media_upgrade_rules WHERE id=1`).Scan(&enabled); err != nil {
		t.Fatalf("读存量列失败: %v", err)
	}
}

// TestRBAC0040IsIdempotent 重复跑 0040 不该报错（IF NOT EXISTS 语义）。
func TestRBAC0040IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "idem.db")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("首次迁移: %v", err)
	}
	m, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var rbacSQL string
	for _, one := range m {
		if one.version == 40 {
			rbacSQL = one.sql
		}
	}
	if rbacSQL == "" {
		t.Fatal("找不到 0040 迁移")
	}
	for round := 0; round < 2; round++ {
		for _, stmt := range splitStatements(rbacSQL) {
			if _, err := db.write.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("第 %d 轮重跑 0040 失败: %v\n%s", round, err, stmt)
			}
		}
	}
}

// TestRBACUsersHasNoIsSuperColumn 把「超管不是一行数据」钉在数据库层面。
//
// 如果哪天有人给 rbac_users 加了 is_super 列，这条会红 —— 那意味着
// 「超管可以被停用/删除/降权」这个保证消失了，而所有相关代码都不会有编译错误。
func TestRBACUsersHasNoIsSuperColumn(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "nosuper.db")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.read.QueryContext(ctx, `PRAGMA table_info(rbac_users)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	for _, banned := range []string{"is_super", "role", "is_admin"} {
		if cols[banned] {
			t.Errorf("rbac_users 不该有 %s 列 —— 超管是虚拟主体，不是一行数据", banned)
		}
	}
	for _, want := range []string{"username", "username_fold", "password_hash", "enabled"} {
		if !cols[want] {
			t.Errorf("rbac_users 缺少 %s 列", want)
		}
	}
}

// TestRBACGroupPermissionsHasEffectColumn 把「二元组 + effect」这个超集钉住。
func TestRBACGroupPermissionsHasEffectColumn(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "effect.db")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readOne(ctx, db,
		`SELECT effect FROM rbac_group_permissions LIMIT 1`); got != "" {
		_ = got // 空表读不到是正常的，下面用信息模式确认列存在
	}
	rows, err := db.read.QueryContext(ctx, `PRAGMA table_info(rbac_group_permissions)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	hasEffect := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "effect" {
			hasEffect = true
		}
	}
	if !hasEffect {
		t.Fatal("rbac_group_permissions 缺少 effect 列 —— deny 优先级真值表将无法表达")
	}

	// 真的写一组 allow/deny 进去，确认 effect 值被接受。
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO rbac_user_groups(id, name, builtin) VALUES(1, 'g', 0)`); err != nil {
		t.Fatal(err)
	}
	for i, eff := range []string{"allow", "deny"} {
		if _, err := db.write.ExecContext(ctx,
			`INSERT INTO rbac_group_permissions(group_id, permission_key, effect) VALUES(1, ?, ?)`,
			"perm."+string(rune('a'+i)), eff); err != nil {
			t.Fatalf("写入 effect=%s 失败: %v", eff, err)
		}
	}
	var n int
	if err := db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rbac_group_permissions WHERE effect='deny'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deny 行数不对: %d", n)
	}
}

// TestRBACUsernameFoldIsUniqueFolded 登录名按小写唯一。
func TestRBACUsernameFoldIsUniqueFolded(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "fold.db")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO rbac_users(username, username_fold, display_name, password_hash, enabled)
		 VALUES('Alice', 'alice', '', 'x', 1)`); err != nil {
		t.Fatal(err)
	}
	// 大小写不同但 fold 相同 ⇒ 必须撞唯一索引。
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO rbac_users(username, username_fold, display_name, password_hash, enabled)
		 VALUES('ALICE', 'alice', '', 'x', 1)`); err == nil {
		t.Fatal("alice 与 ALICE 应当撞唯一索引")
	}
}
