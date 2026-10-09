// Package rbac 实现 T08 的用户与权限体系。
//
// 移植自 参考实现 的五表 RBAC（mv_permissions / mv_user_groups /
// mv_user_group_members / mv_group_permissions / mv_user_permission_overrides）。
// 语义原样落地：
//
//	拒绝 > 允许 > 组矩阵，其中任何一层的 deny 都压过其它层的 allow
//	超级管理员绕过一切检查，且不可停用/删除/改权限
//	加订阅与离线下载始终只有超管能做
//	没有权限的菜单不出现在侧边栏，直接输网址也进不去
package rbac

// 权限项 key。
//
// 命名分三段：域.动作，域与 AdminView 的一级导航对齐，方便把权限直接绑到菜单项上。
const (
	// —— 控制台可见性（每个一级导航一个）——

	// PermDashboardView 仪表盘可见。
	PermDashboardView = "console.dashboard.view"
	// PermAccountManage 存储管理（网盘账号增删改、刷新授权）。
	PermAccountManage = "account.manage"
	// PermSystemManage 系统设置（含管理员凭据修改）。
	PermSystemManage = "system.manage"
	// PermTaskManage 任务管理（STRM 任务、清理任务、备份任务）。
	PermTaskManage = "task.manage"
	// PermToolManage 辅助工具。
	PermToolManage = "tool.manage"
	// PermTransferManage 跨盘传输。
	PermTransferManage = "transfer.manage"
	// PermCASManage CAS 秒传。
	PermCASManage = "cas.manage"
	// PermShareManage 文件共享（分享链接增删）。
	PermShareManage = "share.manage"
	// PermDiscoverView 影视发现（发现页与订阅列表可见）。
	PermDiscoverView = "discover.view"
	// PermMediaUpgradeManage 洗版管理。洗版会删文件，权限项独立于发现页。
	PermMediaUpgradeManage = "mediaupgrade.manage"
	// PermSubtitleManage 字幕处理。
	PermSubtitleManage = "subtitle.manage"
	// PermMCPManage MCP 服务配置（不含助理对话）。
	PermMCPManage = "mcp.manage"
	// PermAssistantUse 智能助理对话。
	PermAssistantUse = "assistant.use"
	// PermFileManage 文件管理（浏览、上传、重命名、删除、收藏）。
	PermFileManage = "file.manage"

	// —— 用户与权限自身 ——

	// PermUserManage 用户管理（增删改、启停、重置密码）。
	PermUserManage = "user.manage"
	// PermPermissionManage 权限管理（用户组、权限矩阵、用户级覆盖）。
	PermPermissionManage = "permission.manage"
	// PermAuditView 审计日志。
	PermAuditView = "audit.view"

	// —— 求片域（T09 会挂上来，本期只登记权限项）——

	// PermRequestCenterView 查看求片中心。
	PermRequestCenterView = "request.center.view"
	// PermRequestSubmit 提交求片。
	PermRequestSubmit = "request.submit"
	// PermRequestMineView 查看我的求片。
	PermRequestMineView = "request.mine.view"
	// PermRequestAllView 查看全部求片。
	PermRequestAllView = "request.all.view"
	// PermRequestReview 求片审核。
	PermRequestReview = "request.review"
	// PermRequestReassign 转派审核。
	PermRequestReassign = "request.reassign"
	// PermRequestFlowManage 管理求片流程（指派、去重规则等）。
	PermRequestFlowManage = "request.flow.manage"

	// —— 找资源（可下放）——

	// PermFindResource 找资源：顶栏搜索影视 + 资源推荐页可见。
	//
	// 参考实现 明确说「找资源」可以下放给普通用户，而加订阅不行 —— 前者只是看看，
	// 后者花用户的钱/配额。这个区分是产品决策，不是技术限制。
	PermFindResource = "find.resource"

	// —— 超管专属（硬编码，见 superOnlyPermissions）——

	// PermSubscriptionCreate 加订阅。
	PermSubscriptionCreate = "subscription.create"
	// PermOfflineDownloadRun 离线下载。
	PermOfflineDownloadRun = "offlinedownload.run"
)

// PermissionMeta 是权限项的元数据。
type PermissionMeta struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Category string `json:"category"`
	// SuperOnly 只是给界面显示用的标记。
	//
	// **判定不读它**：真正生效的是 superOnlyPermissions 这个 Go 里的硬编码集合。
	// 两者的一致性由 TestSuperOnlySetMatchesCatalog 钉住。
	SuperOnly bool `json:"super_only"`
	SortOrder int  `json:"sort_order"`
}

// 权限分类。分组只影响界面排版，不影响判定。
const (
	CategoryConsole  = "console"
	CategoryRequest  = "request"
	CategoryResource = "resource"
	CategoryIdentity = "identity"
)

// catalog 是全部权限项的声明。新增权限项只改这里。
//
// catalog 的顺序就是界面展示顺序（按 SortOrder 升序，同值按声明顺序）。
var catalog = []PermissionMeta{
	// 控制台
	{Key: PermDashboardView, Label: "查看仪表盘", Category: CategoryConsole, SortOrder: 10},
	{Key: PermAccountManage, Label: "存储管理", Category: CategoryConsole, SortOrder: 20},
	{Key: PermSystemManage, Label: "系统设置", Category: CategoryConsole, SortOrder: 30},
	{Key: PermTaskManage, Label: "任务管理", Category: CategoryConsole, SortOrder: 40},
	{Key: PermToolManage, Label: "辅助工具", Category: CategoryConsole, SortOrder: 50},
	{Key: PermTransferManage, Label: "跨盘传输", Category: CategoryConsole, SortOrder: 60},
	{Key: PermCASManage, Label: "CAS 秒传", Category: CategoryConsole, SortOrder: 70},
	{Key: PermShareManage, Label: "文件共享", Category: CategoryConsole, SortOrder: 80},
	{Key: PermDiscoverView, Label: "影视发现", Category: CategoryConsole, SortOrder: 90},
	{Key: PermMediaUpgradeManage, Label: "洗版管理", Category: CategoryConsole, SortOrder: 100},
	{Key: PermSubtitleManage, Label: "字幕处理", Category: CategoryConsole, SortOrder: 110},
	{Key: PermMCPManage, Label: "MCP 服务", Category: CategoryConsole, SortOrder: 120},
	{Key: PermAssistantUse, Label: "智能助理", Category: CategoryConsole, SortOrder: 130},
	{Key: PermFileManage, Label: "文件管理", Category: CategoryConsole, SortOrder: 140},

	// 用户与权限
	{Key: PermUserManage, Label: "用户管理", Category: CategoryIdentity, SortOrder: 200},
	{Key: PermPermissionManage, Label: "权限管理", Category: CategoryIdentity, SortOrder: 210},
	{Key: PermAuditView, Label: "审计日志", Category: CategoryIdentity, SortOrder: 220},

	// 求片域
	{Key: PermRequestCenterView, Label: "查看求片中心", Category: CategoryRequest, SortOrder: 300},
	{Key: PermRequestSubmit, Label: "提交求片", Category: CategoryRequest, SortOrder: 310},
	{Key: PermRequestMineView, Label: "我的求片", Category: CategoryRequest, SortOrder: 320},
	{Key: PermRequestAllView, Label: "查看全部求片", Category: CategoryRequest, SortOrder: 330},
	{Key: PermRequestReview, Label: "求片审核", Category: CategoryRequest, SortOrder: 340},
	{Key: PermRequestReassign, Label: "转派审核", Category: CategoryRequest, SortOrder: 350},
	{Key: PermRequestFlowManage, Label: "管理求片流程", Category: CategoryRequest, SortOrder: 360},

	// 找资源
	{Key: PermFindResource, Label: "找资源", Category: CategoryResource, SortOrder: 400},

	// 超管专属
	{Key: PermSubscriptionCreate, Label: "加订阅", Category: CategoryResource, SuperOnly: true, SortOrder: 500},
	{Key: PermOfflineDownloadRun, Label: "离线下载", Category: CategoryResource, SuperOnly: true, SortOrder: 510},
}

// superOnlyPermissions 是「加订阅」与「离线下载」的**唯一**判定依据。
//
// 为什么硬编码而不是读 rbac_permissions.super_only 那一列：
// 这两个动作花用户的钱和配额。把「能不能花钱」的开关做成可以在界面上勾选的数据，
// 等于把这个开关交到任何一个能编辑用户组的人手里 —— 而「权限管理」本身
// 可以下放给运维角色，一旦下放，勾两下框就能花别人的配额。
// 权限矩阵接口对这两项一律返回 403，不接受写入，也不回显。
//
// 反向的漂移防护：TestSuperOnlySetMatchesCatalog 断言这个集合与 catalog 里
// SuperOnly=true 的项完全一致，两边必须一起改。
var superOnlyPermissions = map[string]struct{}{
	PermSubscriptionCreate: {},
	PermOfflineDownloadRun: {},
}

// IsSuperOnly 判断权限项是否超管专属。
func IsSuperOnly(key string) bool {
	_, ok := superOnlyPermissions[key]
	return ok
}

// Catalog 返回权限项字典的副本（按声明顺序）。
func Catalog() []PermissionMeta {
	out := make([]PermissionMeta, len(catalog))
	copy(out, catalog)
	return out
}

// IsKnownPermission 判断 key 是否是已登记的权限项。
//
// 未知权限项一律判不可用（fail closed）：数据里手写了一个不存在的 key 时，
// 宁可当成没权限，也不要当成有权限。
func IsKnownPermission(key string) bool {
	for _, p := range catalog {
		if p.Key == key {
			return true
		}
	}
	return false
}

// CategoryMeta 是一个权限分类的展示信息。
type CategoryMeta struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// SortOrder 分类之间的展示顺序，与权限项的 SortOrder 不是一套排序。
	SortOrder int `json:"sort_order"`
}

// categoryOrder 是分类的展示顺序。放在这里而不是散在 catalog 里，
// 是因为分类本身没有「默认出现在哪个权限项旁边」的概念。
var categoryOrder = []CategoryMeta{
	{Key: CategoryConsole, Label: "控制台", SortOrder: 10},
	{Key: CategoryRequest, Label: "求片中心", SortOrder: 20},
	{Key: CategoryResource, Label: "资源与任务", SortOrder: 30},
	{Key: CategoryIdentity, Label: "用户与权限", SortOrder: 40},
}

// PermissionCategories 返回权限分类的展示元数据。
//
// 顺序固定为 categoryOrder 的声明顺序 —— 界面按这个顺序分组，
// 顺序乱了会让每次刷新都重排一遍还没保存的编辑。
func PermissionCategories() []CategoryMeta {
	out := make([]CategoryMeta, len(categoryOrder))
	copy(out, categoryOrder)
	return out
}

// SuperOnlyKeys 返回超管专属权限项的 key（已按 SortOrder 排序）。
//
// 只给界面显示用。真正的判定读的是 superOnlyPermissions 这个 map，
// 不查这个返回值 —— 少一条「两处可能不一致」的路径。
func SuperOnlyKeys() []string {
	out := make([]string, 0, len(superOnlyPermissions))
	for _, meta := range catalog {
		if meta.SuperOnly {
			out = append(out, meta.Key)
		}
	}
	return out
}

func permissionLabel(key string) string {
	for _, p := range catalog {
		if p.Key == key {
			return p.Label
		}
	}
	return key
}
