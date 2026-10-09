package rbac

import (
	"sort"
	"strings"
	"testing"
)

// TestMenuCatalogCoversAdminNavigation 是验收 ④ 的前半段：
// 后端菜单目录必须覆盖 AdminView.vue 里的每一个一级导航，一个不漏、一个不多。
//
// 这里把前端导航硬编码一份快照。理由：这个断言的价值恰恰在于
// 「前端加了菜单而后端忘了配权限」时会红 —— 用 AST 去读 .vue 文件当然更优雅，
// 但那会让这条测试在 vue 文件格式变动时一起崩，而它本来只想守权限映射。
var adminNavSnapshot = []string{
	"dashboard", "accounts", "settings", "tasks", "tools",
	"cross-transfer", "cas", "share", "discover", "media-upgrade",
	"subtitle", "mcp", "assistant",
	// T08 新增的一个：用户与权限（后端 key = 前端 nav key = "rbac"）
	"rbac",
	// T09 新增的：求片中心。
	//
	// 这一项用 Any 而不是「全部满足」：审核员只要 request.review 就该点得进来，
	// 把求片交给自己管的人只要 request.center.view 也该进得来 ——
	// 两个角色只拿其中一个，用「全部满足」的话两边都进不去。
	"request",
}

func TestMenuCatalogCoversAdminNavigation(t *testing.T) {
	backend := map[string]bool{}
	for _, m := range menuCatalog {
		backend[m.Key] = true
	}
	for _, key := range adminNavSnapshot {
		if !backend[key] {
			t.Errorf("AdminView 有导航 %q，但 menuCatalog 里没有 —— 开了 RBAC 后这个页面会对所有人隐身", key)
		}
	}
	if len(menuCatalog) != len(adminNavSnapshot) {
		t.Fatalf("menuCatalog 有 %d 项，AdminView 导航有 %d 项，两边对不上", len(menuCatalog), len(adminNavSnapshot))
	}
	// 重复 key 会让界面上出现两个同名入口。
	seen := map[string]bool{}
	for _, m := range menuCatalog {
		if seen[m.Key] {
			t.Errorf("菜单 key 重复: %q", m.Key)
		}
		seen[m.Key] = true
		if len(m.Permissions) == 0 {
			t.Errorf("菜单 %q 没配任何权限项 —— canSee 对空列表默认拒绝，这个页面谁都看不到", m.Key)
		}
	}
}

func TestMenuCatalogPermissionsAreKnown(t *testing.T) {
	for _, m := range menuCatalog {
		for _, perm := range m.Permissions {
			if !IsKnownPermission(perm) {
				t.Errorf("菜单 %q 绑的权限 %q 不在字典里 —— 任何人都过不了这一关", m.Key, perm)
			}
		}
	}
}

// TestMenuKeysForSuperIsEverything 超管看到全部菜单。
func TestMenuKeysForSuperIsEverything(t *testing.T) {
	p := NewSuperPrincipal("admin")
	got := p.MenuKeys()
	want := AllMenuKeys()
	if len(got) != len(want) {
		t.Fatalf("超管应看到全部 %d 个菜单，实际 %d 个", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("菜单列表不一致: 位置 %d 期望 %q 实际 %q", i, want[i], got[i])
		}
	}
}

// TestMenuKeysForDelegatedUser 委托用户只看到自己有权限的菜单。
func TestMenuKeysForDelegatedUser(t *testing.T) {
	p := Principal{
		UserID: 7,
		GroupEffects: map[string]Effect{
			PermDashboardView:  EffectAllow,
			PermDiscoverView:   EffectAllow,
			PermSubtitleManage: EffectAllow,
		},
	}
	got := p.MenuKeys()
	want := []string{"dashboard", "discover", "subtitle"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("菜单过滤不对: 期望 %v，实际 %v", want, got)
	}
	// 特别是：没给权限的那些不该出现。
	for _, m := range got {
		switch m {
		case "dashboard", "discover", "subtitle":
		default:
			t.Errorf("不该出现的菜单: %q", m)
		}
	}
}

// TestDenyHidesMenu 组级 deny 会让菜单消失，这是 deny 优先级在界面上的体现。
func TestDenyHidesMenu(t *testing.T) {
	p := Principal{
		UserID: 7,
		GroupEffects: map[string]Effect{
			PermDashboardView: EffectAllow,
			PermDiscoverView:  EffectDeny,
			// 用户级允许想盖掉组级 deny —— 不行。
		},
		Overrides: map[string]Effect{PermDiscoverView: EffectAllow},
	}
	got := p.MenuKeys()
	for _, m := range got {
		if m == "discover" {
			t.Fatal("组级 deny + 用户级 allow 应当隐藏该菜单")
		}
	}
	found := false
	for _, m := range got {
		if m == "dashboard" {
			found = true
		}
	}
	if !found {
		t.Fatal("允许的 dashboard 菜单应当出现")
	}
}

// TestEmptyPermissionListFailsClosed 未配权限的菜单默认隐身。
func TestEmptyPermissionListFailsClosed(t *testing.T) {
	p := Principal{UserID: 7, Username: "u", IsSuper: false}
	if p.canSee(Menu{}) {
		t.Fatal("空权限列表对非超管必须拒绝")
	}
	if p.canSee(Menu{Permissions: []string{}}) {
		t.Fatal("空切片（非 nil）与 nil 语义应当一致，都拒绝")
	}
}

// TestRBACMenuIsVisibleToEitherRole 用户与权限这一个入口对两种角色都可见。
//
// 这一页是三个标签页（用户 / 用户组 / 权限矩阵），三个标签各自被后端独立接口
// 挡住：/rbac/users 要 user.manage，/rbac/groups 与 /rbac/permissions 要
// permission.manage。所以「只持有其中一项的人」点进来不会撞 403，
// 只是看不到自己没权限的那一栏 —— 值得给他一个入口，而不是让他在导航上
// 完全找不到这个功能。
func TestRBACMenuIsVisibleToEitherRole(t *testing.T) {
	byKey := map[string]Menu{}
	for _, m := range menuCatalog {
		byKey[m.Key] = m
	}
	m, ok := byKey["rbac"]
	if !ok {
		t.Fatal("menuCatalog 里应当有 rbac 这个 key（与 AdminView.vue 的 nav key 一致）")
	}
	if !m.Any {
		t.Fatal("rbac 菜单应当是「任一满足」语义（Any=true）")
	}
	if len(m.Permissions) != 2 {
		t.Fatalf("rbac 菜单应当同时挂 user.manage 与 permission.manage，实际 %v", m.Permissions)
	}

	// 只有 user.manage：看得到入口。
	p := Principal{UserID: 7, GroupEffects: map[string]Effect{PermUserManage: EffectAllow}}
	if !contains(p.MenuKeys(), "rbac") {
		t.Error("有 user.manage 的人应当看到用户与权限入口")
	}
	// 只有 permission.manage：同样看得到入口。
	p2 := Principal{UserID: 7, GroupEffects: map[string]Effect{PermPermissionManage: EffectAllow}}
	if !contains(p2.MenuKeys(), "rbac") {
		t.Error("有 permission.manage 的人应当看到用户与权限入口")
	}
	// 两项都没有：看不到。
	p3 := Principal{UserID: 7, GroupEffects: map[string]Effect{PermDashboardView: EffectAllow}}
	if contains(p3.MenuKeys(), "rbac") {
		t.Error("两项都没有的人不该看到用户与权限入口")
	}
}

// TestRBACMenuNeedsBothToDeny 组级 deny 优先于用户级 allow。
//
// rbac 菜单是「任一满足」语义，所以只有**两项都被拒**才会隐身。
// 这一条守的是「任一满足」不能被绕过成「什么都能看见」：
// 哪怕两项都在用户级显式写了允许，只要组里都拒了，入口必须消失。
func TestRBACMenuNeedsBothToDeny(t *testing.T) {
	p := Principal{
		UserID: 7,
		GroupEffects: map[string]Effect{
			PermUserManage:       EffectDeny,
			PermPermissionManage: EffectDeny,
		},
		Overrides: map[string]Effect{
			PermUserManage:       EffectAllow,
			PermPermissionManage: EffectAllow,
		},
	}
	if contains(p.MenuKeys(), "rbac") {
		t.Fatal("两项都被组级拒绝时，用户级的允许不该把入口点亮")
	}

	// 只要还有一项没被拒（且被允许），入口就还在 —— 他还有一半的活能干。
	p2 := Principal{
		UserID: 7,
		GroupEffects: map[string]Effect{
			PermUserManage:       EffectDeny,
			PermPermissionManage: EffectAllow,
		},
	}
	if !contains(p2.MenuKeys(), "rbac") {
		t.Fatal("permission.manage 仍被允许时，入口应当保留（他能改矩阵，只是管不了人）")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestAllMenuKeysIsSorted 保证发给前端的清单顺序稳定，前端可以直接 diff。
func TestAllMenuKeysIsSorted(t *testing.T) {
	keys := AllMenuKeys()
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("AllMenuKeys 应已排序: %v", keys)
	}
}
