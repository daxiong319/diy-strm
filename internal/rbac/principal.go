package rbac

import "strings"

// Effect 是权限覆盖的效果。
//
// 三态里的「继承」在存储上就是**没有行**，不是 effect='inherit'。
// 这是 参考实现 的 mv_user_permission_overrides 的语义：只存非继承的覆盖。
type Effect string

const (
	// EffectAllow 允许。
	EffectAllow Effect = "allow"
	// EffectDeny 拒绝。
	EffectDeny Effect = "deny"
)

// ParseEffect 解析 effect 字符串，空串视为继承（调用方应理解为「删掉这行」）。
func ParseEffect(raw string) (Effect, bool) {
	switch Effect(strings.TrimSpace(strings.ToLower(raw))) {
	case EffectAllow:
		return EffectAllow, true
	case EffectDeny:
		return EffectDeny, true
	default:
		return "", false
	}
}

// SuperUserID 是超级管理员的哨兵 UserID。
//
// 超管不是 rbac_users 里的一行（见迁移 0040 的注释），它是虚拟主体，
// UserID 恒为 0。这样「不可停用/不可删除/不可改权限」由数据结构保证，
// 而不是靠每个入口都记得加一次校验。
const SuperUserID int64 = 0

// Principal 是一次请求的主体及其权限快照。
//
// 这是**快照**而不是活查询：会话建立时算一次，之后同一请求内不再查库。
// 代价是改权限后已登录的用户要重新登录才生效（参考实现 的停用也是「立即踢掉所有设备」），
// 换来的是每个请求判定只做一次 map 查找，不给数据库添压力。
type Principal struct {
	// UserID 受管用户 ID；超管为 SuperUserID(0)。
	UserID int64
	// Username 登录名。超管是当前 admin_username。
	Username string
	// DisplayName 显示名，可空。
	DisplayName string
	// IsSuper 是否超级管理员。为真时 Can 永远返回 true。
	IsSuper bool
	// Groups 用户所属的用户组 ID 列表。
	Groups []int64
	// GroupEffects 组权限矩阵的合并结果：权限 key → 该用户所在组给出的效果。
	//
	// 合并规则：任一组 deny ⇒ deny；否则任一组 allow ⇒ allow；
	// 都没给 ⇒ key 不存在（继承，交由用户级覆盖决断）。
	GroupEffects map[string]Effect
	// Overrides 用户级覆盖：权限 key → 效果。只存非继承的。
	Overrides map[string]Effect
}

// NewSuperPrincipal 构造超管主体。
func NewSuperPrincipal(username string) Principal {
	return Principal{
		UserID:      SuperUserID,
		Username:    username,
		DisplayName: username,
		IsSuper:     true,
	}
}

// Clone 返回深拷贝，避免调用方改到共享的 map。
func (p Principal) Clone() Principal {
	out := p
	out.Groups = append([]int64(nil), p.Groups...)
	if p.GroupEffects != nil {
		out.GroupEffects = make(map[string]Effect, len(p.GroupEffects))
		for k, v := range p.GroupEffects {
			out.GroupEffects[k] = v
		}
	}
	if p.Overrides != nil {
		out.Overrides = make(map[string]Effect, len(p.Overrides))
		for k, v := range p.Overrides {
			out.Overrides[k] = v
		}
	}
	return out
}

// EffectOf 返回该主体在某个权限项上的**直接覆盖**效果，
// 不做判定。找不到覆盖时返回 ok=false（即「继承」）。
func (p Principal) EffectOf(perm string) (Effect, bool) {
	e, ok := p.Overrides[perm]
	return e, ok
}

// GroupEffectOf 返回组矩阵对该权限项给出的效果。
func (p Principal) GroupEffectOf(perm string) (Effect, bool) {
	e, ok := p.GroupEffects[perm]
	return e, ok
}

// Can 判定主体是否拥有某个权限项。
//
// 真值表（IsSuper 之外的部分，顺序不可换）：
//
//	未知权限项                → false（fail closed）
//	超管专属项                → false（非超管永远拿不到，不看数据）
//	用户级 deny               → false（拒绝优先级最高，压过任何组权限与用户级 allow）
//	任一组 deny               → false（拒绝压过用户级 allow）
//	任一组 allow              → true
//	用户级 allow              → true（用户级允许可以授予组矩阵没给的东西）
//	其余（两边都没有记录）      → false（默认不给）
//
// 超管（IsSuper）在第一行就直接返回 true，绕过以上全部检查 —— 包括超管专属项，
// 因为超管专属的语义是「只有超管能做」，而不是「非超管一定不能做」以外的什么。
func (p Principal) Can(perm string) bool {
	perm = strings.TrimSpace(perm)
	if p.IsSuper {
		return true
	}
	if !IsKnownPermission(perm) {
		return false
	}
	if IsSuperOnly(perm) {
		return false
	}
	if p.Overrides[perm] == EffectDeny {
		return false
	}
	if p.GroupEffects[perm] == EffectDeny {
		return false
	}
	if p.GroupEffects[perm] == EffectAllow {
		return true
	}
	return p.Overrides[perm] == EffectAllow
}

// Explain 是 Can 的可读版本，给界面上的「为什么没有这个权限」用。
//
// 判定逻辑与 Can 严格同源（两者共用下面的 decide），
// 避免出现「后端说不行、前端说行」的分裂。
type Explain struct {
	Permission string `json:"permission"`
	Allowed    bool   `json:"allowed"`
	// Source 最终结论来自哪一层：super / super_only / unknown /
	// user_deny / group_deny / group_allow / user_allow / none。
	Source string `json:"source"`
	// Message 人话说明。
	Message string `json:"message"`
}

// 判定来源常量。
const (
	SourceSuper     = "super"
	SourceSuperOnly = "super_only"
	SourceUnknown   = "unknown"
	SourceUserDeny  = "user_deny"
	SourceGroupDeny = "group_deny"
	SourceGroupAllw = "group_allow"
	SourceUserAllw  = "user_allow"
	SourceNone      = "none"
)

// Explain 返回可读的判定结论。
func (p Principal) Explain(perm string) Explain {
	perm = strings.TrimSpace(perm)
	allowed, source := p.decide(perm)
	e := Explain{Permission: perm, Allowed: allowed, Source: source}
	label := permissionLabel(perm)
	switch source {
	case SourceSuper:
		e.Message = "超级管理员，拥有全部权限"
	case SourceSuperOnly:
		e.Message = label + "只有超级管理员能做，这是产品决策不是技术限制（会花用户的钱/配额）"
	case SourceUnknown:
		e.Message = "未登记的权限项，按无权限处理"
	case SourceUserDeny:
		e.Message = "该用户被单独拒绝了「" + label + "」，用户级拒绝优先级最高"
	case SourceGroupDeny:
		e.Message = "所在用户组拒绝了「" + label + "」，组级拒绝压过用户级允许"
	case SourceGroupAllw:
		e.Message = "所在用户组允许「" + label + "」"
	case SourceUserAllw:
		e.Message = "该用户被单独允许了「" + label + "」"
	default:
		e.Message = "没有任何一层授予「" + label + "」，默认不给"
	}
	return e
}

// decide 是 Can 与 Explain 共用的判定内核。
func (p Principal) decide(perm string) (bool, string) {
	if p.IsSuper {
		return true, SourceSuper
	}
	if !IsKnownPermission(perm) {
		return false, SourceUnknown
	}
	if IsSuperOnly(perm) {
		return false, SourceSuperOnly
	}
	if p.Overrides[perm] == EffectDeny {
		return false, SourceUserDeny
	}
	if p.GroupEffects[perm] == EffectDeny {
		return false, SourceGroupDeny
	}
	if p.GroupEffects[perm] == EffectAllow {
		return true, SourceGroupAllw
	}
	if p.Overrides[perm] == EffectAllow {
		return true, SourceUserAllw
	}
	return false, SourceNone
}

// EffectivePermissions 返回该主体当前实际拥有的全部权限 key（已排序）。
//
// 只在需要展示「这个用户到底能干什么」时调用；逐请求判定请用 Can。
func (p Principal) EffectivePermissions() []string {
	out := make([]string, 0, len(catalog))
	for _, meta := range catalog {
		if p.Can(meta.Key) {
			out = append(out, meta.Key)
		}
	}
	return out
}
