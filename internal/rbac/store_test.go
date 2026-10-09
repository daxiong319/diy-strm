package rbac

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB 打开一个只含 0040 迁移的空库。
//
// 直接读迁移文件而不是 AutoMigrate：迁移本身也是交付物之一，
// 用 AutoMigrate 会让「迁移 SQL 写错了」这件事在测试里看不见。
// 这里刻意不复用 internal/store 的迁移器 —— 那个要先把 0001..0040 全跑一遍，
// 单元测试里没必要。
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "rbac.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	raw, err := os.ReadFile(filepath.Join("..", "store", "migrations", "0040_vyo_rbac.sql"))
	if err != nil {
		t.Fatalf("读取 0040 迁移失败: %v", err)
	}
	for _, stmt := range strings.Split(string(raw), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("执行迁移语句失败: %v\n语句: %s", err, truncate(stmt, 200))
		}
	}
	return db
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func newTestStore(t *testing.T) (*Store, *Service) {
	t.Helper()
	db := openTestDB(t)
	st := NewStore(db, db)
	svc := NewService(st, newFakeSettings(map[string]string{
		"mo_rbac_enabled": "true",
		// 超管账号名。没有这一项的话「与超管同名一律拒绝」这条分支在测试里根本走不到。
		"admin_username": "admin",
	}), nil)
	return st, svc
}

type fakeSettings map[string]string

func newFakeSettings(m map[string]string) fakeSettings {
	if m == nil {
		m = map[string]string{}
	}
	return m
}

// String/Bool 的签名与真 settings.Service 一致。
//
// 这里刻意不回落到 registry 的默认值（真的那边会回落）：
// 测试里的 map 是「这个键存了什么」的事实，没存就是没存，
// 拿默认值兜底会让「没配 mo_rbac_enabled」和「配成 false」两种情况分不开。
func (f fakeSettings) String(key string) string {
	return f[key]
}

func (f fakeSettings) Bool(key string) bool {
	return f[key] == "true"
}

var _ Settings = fakeSettings{}

// makeUser 建一个已启用、有密码的用户。
func makeUser(t *testing.T, svc *Service, name string, groups ...string) int64 {
	t.Helper()
	id, err := svc.CreateUser(context.Background(), name, name, "password123", true)
	if err != nil {
		t.Fatalf("建用户 %s 失败: %v", name, err)
	}
	for _, g := range groups {
		if err := svc.AddUserToGroupByName(context.Background(), id, g); err != nil {
			t.Fatalf("把 %s 加入组 %s 失败: %v", name, g, err)
		}
	}
	return id
}

func mustSeed(t *testing.T, svc *Service) {
	t.Helper()
	if err := svc.EnsureSeed(context.Background()); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
}

// TestSetGroupPermissionsDropsSuperOnly 钉住「静默丢弃」这个决定。
func TestSetGroupPermissionsDropsSuperOnly(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)

	groupID, err := st.CreateGroup(ctx, Group{Name: "运维"})
	if err != nil {
		t.Fatalf("建组失败: %v", err)
	}
	err = st.SetGroupPermissions(ctx, groupID, []GroupPermission{
		{Key: PermSystemManage, Effect: EffectAllow},
		{Key: PermSubscriptionCreate, Effect: EffectAllow},
		{Key: PermOfflineDownloadRun, Effect: EffectAllow},
		{Key: "没在字典里的权限项", Effect: EffectAllow},
	})
	if err != nil {
		t.Fatalf("写权限矩阵失败: %v", err)
	}
	got, err := st.GroupPermissionsOf(ctx, groupID)
	if err != nil {
		t.Fatalf("读权限矩阵失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("矩阵里应只剩 1 项，实际 %d 项: %+v", len(got), got)
	}
	if got[0].Key != PermSystemManage || got[0].Effect != EffectAllow {
		t.Fatalf("剩下的应当是 system.manage=allow，实际 %+v", got[0])
	}
}

// TestSetGroupPermissionsReplacesWholesale 是全量替换语义。
func TestSetGroupPermissionsReplacesWholesale(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)
	groupID, _ := st.CreateGroup(ctx, Group{Name: "临时"})

	if err := st.SetGroupPermissions(ctx, groupID, []GroupPermission{
		{Key: PermToolManage, Effect: EffectAllow},
		{Key: PermTaskManage, Effect: EffectDeny},
	}); err != nil {
		t.Fatalf("第一次写失败: %v", err)
	}
	if err := st.SetGroupPermissions(ctx, groupID, []GroupPermission{
		{Key: PermCASManage, Effect: EffectAllow},
	}); err != nil {
		t.Fatalf("第二次写失败: %v", err)
	}
	got, _ := st.GroupPermissionsOf(ctx, groupID)
	if len(got) != 1 || got[0].Key != PermCASManage {
		t.Fatalf("全量替换后应只剩 cas.manage，实际 %+v", got)
	}
}

// TestMergedGroupEffectsPutsDenyOnTop 验证库层的组效果合并。
func TestMergedGroupEffectsPutsDenyOnTop(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)

	allowGroup, _ := st.CreateGroup(ctx, Group{Name: "运维"})
	denyGroup, _ := st.CreateGroup(ctx, Group{Name: "审计"})
	if err := st.SetGroupPermissions(ctx, allowGroup, []GroupPermission{
		{Key: PermSystemManage, Effect: EffectAllow},
		{Key: PermToolManage, Effect: EffectAllow},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupPermissions(ctx, denyGroup, []GroupPermission{
		{Key: PermSystemManage, Effect: EffectDeny},
	}); err != nil {
		t.Fatal(err)
	}
	merged, err := st.MergedGroupEffects(ctx, []int64{allowGroup, denyGroup})
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if merged[PermSystemManage] != EffectDeny {
		t.Fatalf("system.manage 合并后应为 deny，实际 %q", merged[PermSystemManage])
	}
	if merged[PermToolManage] != EffectAllow {
		t.Fatalf("tool.manage 合并后应为 allow，实际 %q", merged[PermToolManage])
	}
	// 顺序无关：deny 组在前也一样。
	merged2, _ := st.MergedGroupEffects(ctx, []int64{denyGroup, allowGroup})
	if merged2[PermSystemManage] != EffectDeny {
		t.Fatalf("顺序不应影响合并结果，实际 %q", merged2[PermSystemManage])
	}
}

// TestSetUserOverridesEmptyStringMeansInherit 三态里「继承」= 删行。
func TestSetUserOverridesEmptyStringMeansInherit(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)
	uid := makeUser(t, svc, "alice")

	if err := st.SetUserOverrides(ctx, uid, map[string]string{
		PermSystemManage: "allow",
		PermToolManage:   "deny",
	}); err != nil {
		t.Fatalf("写覆盖失败: %v", err)
	}
	got, _ := st.UserOverridesOf(ctx, uid)
	if got[PermSystemManage] != EffectAllow || got[PermToolManage] != EffectDeny {
		t.Fatalf("覆盖表内容不对: %+v", got)
	}

	// 空串表示回到继承 ⇒ 该行被删掉。
	if err := st.SetUserOverrides(ctx, uid, map[string]string{
		PermSystemManage: "allow",
		PermToolManage:   "",
	}); err != nil {
		t.Fatalf("写覆盖失败: %v", err)
	}
	got, _ = st.UserOverridesOf(ctx, uid)
	if _, ok := got[PermToolManage]; ok {
		t.Fatal("空串应当删掉这行，而不是留下一个空效果")
	}
	if got[PermSystemManage] != EffectAllow {
		t.Fatal("非空串应当保留")
	}
}

// TestUserOverridesDropSuperOnly 用户级覆盖同样丢弃超管专属项。
func TestUserOverridesDropSuperOnly(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)
	uid := makeUser(t, svc, "bob")

	if err := st.SetUserOverrides(ctx, uid, map[string]string{
		PermSubscriptionCreate: "allow",
		PermOfflineDownloadRun: "allow",
	}); err != nil {
		t.Fatalf("写覆盖失败: %v", err)
	}
	got, _ := st.UserOverridesOf(ctx, uid)
	if len(got) != 0 {
		t.Fatalf("超管专属项不该落库，实际 %+v", got)
	}
}

// TestDeleteUserClearsGrantsAndOverrides 删用户时必须连带清干净授权。
//
// 这条是有真实后果的：授权按 user_id 存，SQLite 复用主键时
// 新用户会继承上一个用户残留的权限。
func TestDeleteUserClearsGrantsAndOverrides(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)
	groupID, _ := st.CreateGroup(ctx, Group{Name: "组"})
	uid := makeUser(t, svc, "carol")
	if err := st.AddUserToGroup(ctx, groupID, uid); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserOverrides(ctx, uid, map[string]string{PermSystemManage: "allow"}); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteUser(ctx, uid); err != nil {
		t.Fatalf("删用户失败: %v", err)
	}
	for table, key := range map[string]any{
		"rbac_group_members":  groupID,
		"rbac_user_overrides": uid,
	} {
		var n int
		if err := st.write.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+table+" WHERE "+map[string]string{
				"rbac_group_members":  "group_id=? AND user_id=?",
				"rbac_user_overrides": "user_id=?",
			}[table], key, uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s 还残留 %d 行", table, n)
		}
	}
	// 用户本身也没了。
	if _, err := st.GetUser(ctx, uid); err == nil {
		t.Fatal("用户应当已被删除")
	}
}

// TestDeleteGroupClearsMembersAndPermissions 对称的一条。
func TestDeleteGroupClearsMembersAndPermissions(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)
	gid, _ := st.CreateGroup(ctx, Group{Name: "临时组"})
	uid := makeUser(t, svc, "dave")
	_ = st.AddUserToGroup(ctx, gid, uid)
	_ = st.SetGroupPermissions(ctx, gid, []GroupPermission{{Key: PermToolManage, Effect: EffectAllow}})

	if err := st.DeleteGroup(ctx, gid); err != nil {
		t.Fatalf("删组失败: %v", err)
	}
	members, _ := st.MembersOfGroup(ctx, gid)
	if len(members) != 0 {
		t.Fatalf("组成员关系没清干净: %v", members)
	}
	perms, _ := st.GroupPermissionsOf(ctx, gid)
	if len(perms) != 0 {
		t.Fatalf("组权限矩阵没清干净: %+v", perms)
	}
}

// TestBuiltinGroupsCannotBeDeleted 内置组受保护。
func TestBuiltinGroupsCannotBeDeleted(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)
	groups, err := st.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("播种后应有两个内置组，实际 %d 个: %+v", len(groups), groups)
	}
	for _, g := range groups {
		if !g.Builtin {
			t.Fatalf("组 %q 应标记为 builtin", g.Name)
		}
		if err := svc.DeleteGroup(ctx, g.ID); err == nil {
			t.Fatalf("内置组 %q 不该被删掉", g.Name)
		}
	}
	// EnsureSeed 幂等：跑两遍不会多出重复组。
	if err := svc.EnsureSeed(ctx); err != nil {
		t.Fatal(err)
	}
	groups2, _ := st.ListGroups(ctx)
	if len(groups2) != 2 {
		t.Fatalf("EnsureSeed 不幂等，第二次跑完有 %d 个组", len(groups2))
	}
}

// TestEnsureCatalogIsIdempotent 权限字典重复播种不报错、不重复。
func TestEnsureCatalogIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)
	mustSeed(t, svc)
	rows, err := st.PermissionRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(catalog) {
		t.Fatalf("字典里应恰好有 %d 项，实际 %d", len(catalog), len(rows))
	}
	missing, extra, err := svc.CatalogDrift(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("字典与代码漂移: missing=%v extra=%v", missing, extra)
	}
}

// TestPrincipalForBuildsFromDatabase 端到端装配一次 Principal。
func TestPrincipalForBuildsFromDatabase(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)

	opsID, _ := st.CreateGroup(ctx, Group{Name: "运维"})
	auditID, _ := st.CreateGroup(ctx, Group{Name: "审计"})
	_ = st.SetGroupPermissions(ctx, opsID, []GroupPermission{
		{Key: PermSystemManage, Effect: EffectAllow},
		{Key: PermToolManage, Effect: EffectAllow},
	})
	_ = st.SetGroupPermissions(ctx, auditID, []GroupPermission{
		{Key: PermSystemManage, Effect: EffectDeny},
	})
	uid := makeUser(t, svc, "eve", "运维", "审计")

	p, err := svc.PrincipalFor(ctx, Session{UserID: uid, Username: "eve"})
	if err != nil {
		t.Fatalf("装配主体失败: %v", err)
	}
	if p.IsSuper {
		t.Fatal("委托用户不该是超管")
	}
	if p.UserID != uid {
		t.Fatalf("UserID = %d, 期望 %d", p.UserID, uid)
	}
	if len(p.Groups) != 2 {
		t.Fatalf("应属于两个组，实际 %v", p.Groups)
	}
	if p.Can(PermSystemManage) {
		t.Fatal("两个组合并后 system.manage 应当是拒绝")
	}
	if !p.Can(PermToolManage) {
		t.Fatal("只被允许的 tool.manage 应当放行")
	}
	// 超管专属即便组里给了也不行。
	if p.Can(PermSubscriptionCreate) {
		t.Fatal("委托用户不能加订阅")
	}
}

// TestPrincipalForSuperDoesNotTouchDatabase 超管路径必须不查库。
//
// 这条用「把 read 句柄换成会报错的」来证明：超管请求不能因为数据库异常被挡在门外。
func TestPrincipalForSuperDoesNotTouchDatabase(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)

	broken := NewStore(nil, nil) // write=nil, read=nil ⇒ 所有查询返回 errNoWriteHandle
	// 注意 fakeSettings 里必须带 admin_username：超管身份是靠用户名比对
	// 认出来的，光有 UserID=0 不算数（UserID<=0 只是个「查不到受管用户」的哨兵）。
	svc2 := NewService(broken, fakeSettings{"mo_rbac_enabled": "true", "admin_username": "admin"}, nil)
	p, err := svc2.PrincipalFor(ctx, Session{UserID: 0, Username: "admin"})
	if err != nil {
		t.Fatalf("超管路径不该查库，却拿到错误: %v", err)
	}
	if !p.IsSuper || !p.Can(PermSystemManage) || !p.Can(PermSubscriptionCreate) {
		t.Fatalf("超管主体不对: %+v", p)
	}
	_ = st
}

// TestDisabledPrincipalHasNoPermissions 关闭时不产生任何权限。
func TestDisabledPrincipalHasNoPermissions(t *testing.T) {
	ctx := context.Background()
	st := NewStore(nil, nil)
	svc := NewService(st, fakeSettings{"mo_rbac_enabled": "false"}, nil)
	if svc.Enabled(ctx) {
		t.Fatal("mo_rbac_enabled=false 时不该开启")
	}
	p, err := svc.PrincipalFor(ctx, Session{UserID: 3, Username: "u"})
	if err != nil {
		t.Fatalf("关闭时不该报错: %v", err)
	}
	if p.IsSuper {
		t.Fatal("关闭时不应凭空造一个超管")
	}
	// 关闭时 authenticate 永远失败 —— 由调用方（adminauth）走原来的单管理员路径。
	if _, ok, err := svc.Authenticate(ctx, "anyone", "password123"); err != nil || ok {
		t.Fatalf("关闭时不该允许任何委托登录 (ok=%v err=%v)", ok, err)
	}
	// 关闭时 EnsureSeed 什么也不做。
	if err := svc.EnsureSeed(ctx); err != nil {
		t.Fatalf("关闭时 EnsureSeed 不该报错: %v", err)
	}
}

// TestAuthenticate 委托登录的各条分支。
func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)

	uid := makeUser(t, svc, "frank")
	sess, ok, err := svc.Authenticate(ctx, "frank", "password123")
	if err != nil || !ok {
		t.Fatalf("正确密码应当登录成功 (ok=%v err=%v)", ok, err)
	}
	if sess.UserID != uid || sess.Username != "frank" {
		t.Fatalf("会话身份不对: %+v", sess)
	}
	if sess.MustChangePassword {
		t.Fatal("委托用户不该被要求强制改密")
	}

	// 密码错。
	if _, ok, _ := svc.Authenticate(ctx, "frank", "wrong-password"); ok {
		t.Fatal("错误密码不应登录成功")
	}
	// 用户不存在。
	if _, ok, _ := svc.Authenticate(ctx, "nobody", "password123"); ok {
		t.Fatal("不存在的用户不应登录成功")
	}
	// 大小写不敏感。
	if _, ok, _ := svc.Authenticate(ctx, "FRANK", "password123"); !ok {
		t.Fatal("用户名应当大小写不敏感")
	}
	// 已停用。
	if err := st.SetUserEnabled(ctx, uid, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := svc.Authenticate(ctx, "frank", "password123"); ok {
		t.Fatal("停用用户不应登录成功")
	}
	_ = st.SetUserEnabled(ctx, uid, true)
	// 与超管同名一律拒绝。
	if _, ok, _ := svc.Authenticate(ctx, "admin", "admin"); ok {
		t.Fatal("与超管同名的委托账号不应能登录")
	}
}

// TestAuthenticateRejectsUserWithoutPasswordHash 没有密码的账号不能登录。
func TestAuthenticateRejectsUserWithoutPasswordHash(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)

	uid, err := st.CreateUser(ctx, User{Username: "nohash", Enabled: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := svc.Authenticate(ctx, "nohash", ""); ok {
		t.Fatal("空密码哈希的账号不应能登录")
	}
	// 也不能因为空哈希而被 PrincipalFor 当成有权限。
	p, err := svc.PrincipalFor(ctx, Session{UserID: uid, Username: "nohash"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Can(PermSystemManage) {
		t.Fatal("空哈希账号不该有任何权限")
	}
}

// TestPrincipalForDisabledUserHasNoPermissions 已停用的用户即使会话还在也没有权限。
func TestPrincipalForDisabledUserHasNoPermissions(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)

	ops, _ := st.CreateGroup(ctx, Group{Name: "运维2"})
	_ = st.SetGroupPermissions(ctx, ops, []GroupPermission{{Key: PermSystemManage, Effect: EffectAllow}})
	uid := makeUser(t, svc, "grace")
	_ = st.AddUserToGroup(ctx, ops, uid)

	p, _ := svc.PrincipalFor(ctx, Session{UserID: uid, Username: "grace"})
	if !p.Can(PermSystemManage) {
		t.Fatal("启用状态下应当有权限")
	}

	if err := st.SetUserEnabled(ctx, uid, false); err != nil {
		t.Fatal(err)
	}
	p2, _ := svc.PrincipalFor(ctx, Session{UserID: uid, Username: "grace"})
	if p2.Can(PermSystemManage) {
		t.Fatal("停用后不该再有权限")
	}
}

// TestPrincipalForDeletedUserIsPowerless 用户被删但会话还在 ⇒ 零权限而不是报错。
func TestPrincipalForDeletedUserIsPowerless(t *testing.T) {
	ctx := context.Background()
	_, svc := newTestStore(t)
	mustSeed(t, svc)
	p, err := svc.PrincipalFor(ctx, Session{UserID: 99999, Username: "ghost"})
	if err != nil {
		t.Fatalf("用户不存在时不该报错（会话还没过期），却拿到: %v", err)
	}
	if p.Can(PermDashboardView) {
		t.Fatal("已删除的用户必须零权限")
	}
	if p.IsSuper {
		t.Fatal("已删除的用户不能是超管")
	}
}

// TestDefaultUserGroupOnCreate 配置的默认组要真的生效。
func TestDefaultUserGroupOnCreate(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	st := NewStore(db, db)
	svc := NewService(st, fakeSettings{
		"mo_rbac_enabled":            "true",
		"mo_rbac_default_user_group": BuiltinGroupUser,
	}, nil)
	mustSeed(t, svc)

	uid := makeUser(t, svc, "heidi")
	groups, err := st.GroupIDsOfUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("新用户应自动加入默认组，实际组数 %d", len(groups))
	}
	all, _ := st.ListGroups(ctx)
	for _, g := range all {
		if g.ID == groups[0] && g.Name != BuiltinGroupUser {
			t.Fatalf("进错组了: %q", g.Name)
		}
	}
}

// TestDefaultUserGroupMissingIsNotFatal 指向不存在的组不报错。
func TestDefaultUserGroupMissingIsNotFatal(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	st := NewStore(db, db)
	svc := NewService(st, fakeSettings{
		"mo_rbac_enabled":            "true",
		"mo_rbac_default_user_group": "还没建的组",
	}, nil)
	mustSeed(t, svc)
	// 用户依然建得出来。
	if _, err := svc.CreateUser(ctx, "ivan", "", "password123", true); err != nil {
		t.Fatalf("默认组不存在时建用户仍应成功: %v", err)
	}
}

// TestDuplicateUsername 登录名唯一。
func TestDuplicateUsername(t *testing.T) {
	ctx := context.Background()
	_, svc := newTestStore(t)
	mustSeed(t, svc)
	makeUser(t, svc, "judy")
	if _, err := svc.CreateUser(ctx, "judy", "", "password123", true); err == nil {
		t.Fatal("重名应被拒绝")
	}
	// 大小写不同也算重名。
	if _, err := svc.CreateUser(ctx, "JUDY", "", "password123", true); err == nil {
		t.Fatal("大小写不同的重名应被拒绝")
	}
	// 与超管同名也算。
	if _, err := svc.CreateUser(ctx, "admin", "", "password123", true); err == nil {
		t.Fatal("与超管同名应被拒绝")
	}
}

// TestUsernameAndPasswordValidation 输入校验。
func TestUsernameAndPasswordValidation(t *testing.T) {
	ctx := context.Background()
	_, svc := newTestStore(t)
	mustSeed(t, svc)
	for _, bad := range []string{"", "   ", "有 空格"} {
		if _, err := svc.CreateUser(ctx, bad, "", "password123", true); err == nil {
			t.Fatalf("登录名 %q 应当被拒绝", bad)
		}
	}
	for _, bad := range []string{"", "short", "1234567"} {
		if _, err := svc.CreateUser(ctx, "okname", "", bad, true); err == nil {
			t.Fatalf("密码 %q 应当被拒绝", bad)
		}
	}
}

// TestSetMembersReplacesWholesale 组成员是全量替换。
func TestSetMembersReplacesWholesale(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)
	gid, _ := st.CreateGroup(ctx, Group{Name: "成员组"})
	a := makeUser(t, svc, "user1")
	b := makeUser(t, svc, "user2")
	c := makeUser(t, svc, "user3")

	if err := st.SetGroupMembers(ctx, gid, []int64{a, b, a, b}); err != nil {
		t.Fatal(err)
	}
	members, _ := st.MembersOfGroup(ctx, gid)
	if len(members) != 2 {
		t.Fatalf("去重后应有 2 人，实际 %v", members)
	}
	if err := st.SetGroupMembers(ctx, gid, []int64{c}); err != nil {
		t.Fatal(err)
	}
	members, _ = st.MembersOfGroup(ctx, gid)
	if len(members) != 1 || members[0] != c {
		t.Fatalf("全量替换后应只剩 user3，实际 %v", members)
	}
}

// TestCatalogDriftDetectsManualEdits 手改数据库后能看出来。
func TestCatalogDriftDetectsManualEdits(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)
	if _, err := st.write.ExecContext(ctx,
		`INSERT INTO rbac_permissions(key, label, category, super_only, sort_order)
		 VALUES('手加的权限项', 'x', 'console', 0, 999)`); err != nil {
		t.Fatal(err)
	}
	missing, extra, err := svc.CatalogDrift(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 1 || extra[0] != "手加的权限项" {
		t.Fatalf("应检出库里多出来的权限项，实际 %v", extra)
	}
	if len(missing) != 0 {
		t.Fatalf("不该有缺失项，实际 %v", missing)
	}
}

// TestTouchUserLogin 登录后记下最近登录时间。
func TestTouchUserLogin(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestStore(t)
	mustSeed(t, svc)
	uid := makeUser(t, svc, "kate")
	before, _ := st.GetUser(ctx, uid)
	if before.LastLoginAt != nil {
		t.Fatalf("刚建的用户不该有登录时间: %v", before.LastLoginAt)
	}
	if _, ok, _ := svc.Authenticate(ctx, "kate", "password123"); !ok {
		t.Fatal("登录应当成功")
	}
	after, _ := st.GetUser(ctx, uid)
	if after.LastLoginAt == nil {
		t.Fatal("登录后应当记下时间")
	}
	if after.LastLoginAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("登录时间不对: %v", after.LastLoginAt)
	}
}

// TestUserIDZeroIsNotSuperByItself 钉住「UserID<=0 不等于超管」。
//
// 这是一条真实修过的漏洞：早先 PrincipalFor 只看 sess.UserID <= 0 就返回
// 超管 Principal。任何一条拿不到用户 ID 的会话（空会话、只有 Cookie 没有
// 用户记录的会话）都会因此变成超管、绕过一切权限。
//
// 现在的规则：超管 = UserID <= 0 **且** 用户名 == 当前 admin_username。
func TestUserIDZeroIsNotSuperByItself(t *testing.T) {
	ctx := context.Background()
	st, _ := newTestStore(t)

	// 把当前超管名设成 root。
	adminNamed := NewService(st, fakeSettings{"mo_rbac_enabled": "true", "admin_username": "root"}, nil)
	mustSeed(t, adminNamed)

	for _, tc := range []struct {
		name      string
		username  string
		wantSuper bool
	}{
		{"用户名对得上超管", "root", true},
		{"大小写不同也算对上", "ROOT", true},
		{"前后空格不算数", " root ", true},
		{"UserID=0 但名字不是超管", "", false},
		{"UserID=0 且是别人", "alice", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := adminNamed.PrincipalFor(ctx, Session{UserID: 0, Username: tc.username})
			if err != nil {
				t.Fatalf("PrincipalFor: %v", err)
			}
			if p.IsSuper != tc.wantSuper {
				t.Fatalf("用户名 %q → IsSuper=%v，期望 %v", tc.username, p.IsSuper, tc.wantSuper)
			}
			if p.Can(PermSystemManage) != tc.wantSuper {
				t.Fatalf("用户名 %q → Can(system.manage)=%v，期望 %v",
					tc.username, p.Can(PermSystemManage), tc.wantSuper)
			}
		})
	}
}

// TestSuperCheckNeedsConfiguredAdminUsername 断言读不出 admin_username 时不做超管。
//
// 「超管 = 与当前 admin_username 同名」这条规则有个边界：配置项读不出来
// （没配、读失败）时，「同名」就变成了「与空串同名」。此时必须一个都不放过。
func TestSuperCheckNeedsConfiguredAdminUsername(t *testing.T) {
	ctx := context.Background()
	st, _ := newTestStore(t)
	// 故意不配 admin_username。
	svc := NewService(st, fakeSettings{"mo_rbac_enabled": "true"}, nil)
	mustSeed(t, svc)

	for _, username := range []string{"admin", "root", ""} {
		p, err := svc.PrincipalFor(ctx, Session{UserID: 0, Username: username})
		if err != nil {
			t.Fatalf("PrincipalFor(%q): %v", username, err)
		}
		if p.IsSuper {
			t.Fatalf("读不出 admin_username 时，用户名 %q 被当成了超管", username)
		}
	}
}

// TestSuperCheckDoesNotNeedDatabase 断言超管判定只读配置项，不查库。
//
// 与 TestPrincipalForSuperDoesNotTouchDatabase 互补：那条换掉数据库句柄，
// 这条直接把 store 置 nil，确认「超管身份来自配置项而不是数据库」。
func TestSuperCheckDoesNotNeedDatabase(t *testing.T) {
	ctx := context.Background()
	// 非 nil 但句柄为 nil 的 store：任何一次查库都会直接报错。
	// 刻意不用 nil store —— nil store 会让 Enabled() 先返回 false，
	// 那条路走的是「功能没装」而不是「超管不查库」，测的不是同一件事。
	svc := NewService(NewStore(nil, nil), fakeSettings{"mo_rbac_enabled": "true", "admin_username": "root"}, nil)
	p, err := svc.PrincipalFor(ctx, Session{UserID: 0, Username: "root"})
	if err != nil {
		t.Fatalf("超管路径不该查库，却拿到错误: %v", err)
	}
	if !p.IsSuper {
		t.Fatal("nil store 下超管身份没立住")
	}
}

// TestSettingsWithRawReadsAdminUsernameOutsideRegistry 钉住 SettingsWithRaw。
//
// 背景：`admin_username` 是 adminauth 的凭据项，**有意不进**
// internal/settings 的 registry（它不参与设置页的展示与批量更新）。
// 而 settings.Service 只认登记在册的键，未登记的键一律返回 ""。
// rbac 正是用这个键判定超管，所以一旦读不到，功能一开启就会把超管锁在门外。
//
// 这个 bug 真实发生过，而且 rbac 自己的单元测试全绿 —— 因为它用的是
// map 型 fakeSettings，没有 registry 那层保护。所以这个用例要模拟出
// 「registry 里没有这个键」这件事本身。
func TestSettingsWithRawReadsAdminUsernameOutsideRegistry(t *testing.T) {
	// base：模拟 registry 型 settings —— 只有登记过的键才有值。
	base := fakeSettings{"mo_rbac_enabled": "true"}
	raw := fakeRawConfig{"admin_username": "root"}

	cfg := SettingsWithRaw(base, raw)
	if got := cfg.String(AdminUsernameSettingKey); got != "root" {
		t.Fatalf("从裸配置读 admin_username = %q，期望 root", got)
	}
	// 其余键仍然透传给 base，不能被 raw 抢走。
	if got := cfg.String("mo_rbac_enabled"); got != "true" {
		t.Fatalf("普通键透传失败: %q", got)
	}
	if !cfg.Bool("mo_rbac_enabled") {
		t.Fatal("Bool 没有透传")
	}

	// 只读 admin_username 的 BaseService（典型的 registry 型实现）：
	// 对没登记的键直接返回空串。用它证明「不包一层就真的读不出来」。
	if got := base.String(AdminUsernameSettingKey); got != "" {
		t.Fatalf("前提不成立：fakeSettings 本该对 admin_username 返回空串，却返回了 %q", got)
	}
}

// TestSettingsWithRawNilRawFallsBackToBase 断言 raw 为 nil 时退化成透传。
//
// 对应「配置源还没装配好」的情况：宁可让超管判定 fail closed，
// 也不要 panic，更不要误判成超管。
func TestSettingsWithRawNilRawFallsBackToBase(t *testing.T) {
	base := fakeSettings{"admin_username": "root"}
	cfg := SettingsWithRaw(base, nil)
	if got := cfg.String(AdminUsernameSettingKey); got != "root" {
		t.Fatalf("raw=nil 时应透传给 base，得到 %q", got)
	}
}

// fakeRawConfig 是 domain.ConfigRepository.Get 的最小实现。
type fakeRawConfig map[string]string

func (f fakeRawConfig) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := f[key]
	return v, ok, nil
}

// TestSettingsWithRawPropagatesReadFailureAsEmpty 断言读失败时不猜。
//
// 读不到配置与读到空值在这里必须等价（都返回 ""），
// 因为两者的正确处置都是「不认超管」，不是「随便认一个」。
func TestSettingsWithRawPropagatesReadFailureAsEmpty(t *testing.T) {
	cfg := SettingsWithRaw(fakeSettings{}, failingRawConfig{})
	if got := cfg.String(AdminUsernameSettingKey); got != "" {
		t.Fatalf("读失败时返回了 %q，期望空串", got)
	}
}

type failingRawConfig struct{}

func (failingRawConfig) Get(context.Context, string) (string, bool, error) {
	return "", false, errors.New("boom")
}
