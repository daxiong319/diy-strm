package rbac

import "sort"

// Menu 声明一个一级导航菜单项与它所需权限的关系。
type Menu struct {
	// Key 与 AdminView.vue 的 nav[].key 一致。
	Key string `json:"key"`
	// Permissions 默认是「全部满足」才可见；Any 为真时改成「任一满足」。
	Permissions []string `json:"permissions"`
	// Any 把语义从「全部满足」翻成「任一满足」。
	Any bool `json:"any,omitempty"`
}

// menuCatalog 把 15 个一级导航绑到权限项上。
//
// 默认口径是「全部满足」。翻成「任一满足」的有两个：
//   - 用户与权限：它是一个带三个标签页（用户 / 用户组 / 权限矩阵）的页面，
//     三个标签各自被后端独立接口挡住（/rbac/users 要 user.manage，
//     /rbac/groups 与 /rbac/permissions 要 permission.manage），
//     所以只持有其中一项的人点进来不会撞 403，只是看不到自己没权限的那一栏。
//   - 求片中心：审核员只需要 request.review 就能干活，
//     超级管理员（把求片交给自己管的人）只需要 request.center.view。
//     用「全部满足」的话，这两种角色都点不进这个页面。
//
// 早期版本把用户与权限拆成 users / permissions 两个菜单 key，那反而更糟：
// 页面只有一个，导航却出现两个入口，点进去还是同一页；
// 而且「改矩阵但不管人」的运维角色在导航上根本找不到入口。
var menuCatalog = []Menu{
	{Key: "dashboard", Permissions: []string{PermDashboardView}},
	{Key: "accounts", Permissions: []string{PermAccountManage}},
	{Key: "settings", Permissions: []string{PermSystemManage}},
	{Key: "tasks", Permissions: []string{PermTaskManage}},
	{Key: "tools", Permissions: []string{PermToolManage}},
	{Key: "cross-transfer", Permissions: []string{PermTransferManage}},
	{Key: "cas", Permissions: []string{PermCASManage}},
	{Key: "share", Permissions: []string{PermShareManage}},
	{Key: "discover", Permissions: []string{PermDiscoverView}},
	{Key: "media-upgrade", Permissions: []string{PermMediaUpgradeManage}},
	{Key: "subtitle", Permissions: []string{PermSubtitleManage}},
	{Key: "mcp", Permissions: []string{PermMCPManage}},
	{Key: "assistant", Permissions: []string{PermAssistantUse}},
	// 用户与权限：user.manage（管人）与 permission.manage（管矩阵）任一即可见。
	{Key: "rbac", Permissions: []string{PermUserManage, PermPermissionManage}, Any: true},
	// 求片中心：审核（request.review）与查看/规则（request.center.view）任一即可见。
	// 页面本身也是这个口径：五个标签各自被后端独立接口挡住。
	{Key: "request", Permissions: []string{PermRequestReview, PermRequestCenterView}, Any: true},
}

// AllMenuKeys 返回全部菜单 key（已排序），界面上用来对齐已知菜单清单。
func AllMenuKeys() []string {
	out := make([]string, 0, len(menuCatalog))
	for _, m := range menuCatalog {
		out = append(out, m.Key)
	}
	sort.Strings(out)
	return out
}

// MenuKeysFor 返回某个用户可见的菜单 key（保持 menuCatalog 声明顺序）。
//
// 超管返回全部 —— 免得以后加了菜单忘了给它配权限项，把超管自己挡在门外。
func (p Principal) MenuKeys() []string {
	if p.IsSuper {
		return AllMenuKeys()
	}
	out := make([]string, 0, len(menuCatalog))
	for _, m := range menuCatalog {
		if p.canSee(m) {
			out = append(out, m.Key)
		}
	}
	return out
}

// canSee 按菜单声明的口径判定可见性。
func (p Principal) canSee(m Menu) bool {
	if len(m.Permissions) == 0 {
		// 没配权限项的菜单默认不给出。
		// 放行的话，将来加菜单忘配权限就等于白送一个入口。
		return p.IsSuper
	}
	if m.Any {
		for _, perm := range m.Permissions {
			if p.Can(perm) {
				return true
			}
		}
		return false
	}
	for _, perm := range m.Permissions {
		if !p.Can(perm) {
			return false
		}
	}
	return true
}

// MenuEntries 返回可见菜单的完整声明，供前端对齐「有哪些菜单」与「我能看哪些」。
func MenuEntries() []Menu {
	out := make([]Menu, len(menuCatalog))
	for i, m := range menuCatalog {
		out[i] = Menu{
			Key:         m.Key,
			Permissions: append([]string(nil), m.Permissions...),
			Any:         m.Any,
		}
	}
	return out
}
