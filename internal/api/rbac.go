package api

// 用户与权限（RBAC）管理接口。
//
// 安全姿态：
//   - 整个功能由 mo_rbac_enabled 开关把关，关着时下面所有接口都返回
//     「该功能未启用」且不做任何权限判定，行为与本文件上线前完全一致；
//   - 超管不是一个用户行，而是「会话用户名 == 配置里的 admin_username」推导出来的，
//     所以不存在「把超管停用掉」这种操作；
//   - 加订阅与离线下载是超管专属，写入接口会静默丢弃这两项而不是接受它们。
//
// 路由挂在 /api/admin/rbac 与 /api/admin/auth/menus 下（管理员鉴权层内）。

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"litepan/internal/adminauth"
	"litepan/internal/domain"
	"litepan/internal/rbac"
)

type principalCtxKey struct{}

// Principal 是本包对外的权限主体别名。
//
// 别名而不是新类型：让路由层可以直接把 rbac.Principal 塞进 context，
// 而 RequirePermission 与各 handler 读的是同一个类型，不用来回转。
type Principal = rbac.Principal

func principalFromContext(ctx context.Context) Principal {
	p, _ := ctx.Value(principalCtxKey{}).(Principal)
	return p
}

// rbacSession 把管理员会话翻译成本包的会话载荷。
//
// 超管的 UserID 恒为 0（会话里就没有 UserID），委托用户才有值 —— 这个区分
// 与 rbac.SuperUserID 的哨兵约定是同一件事的两面。
func rbacSession(sess *adminauth.Session) rbac.Session {
	if sess == nil {
		return rbac.Session{}
	}
	return rbac.Session{
		UserID:             sess.UserID,
		Username:           sess.Username,
		MustChangePassword: sess.MustChangePassword,
	}
}

// rbacReady 在 RBAC 未装配或未启用时给出明确回应。
//
// 返回 true 表示可以继续。没有装配（h.rbac == nil）也走这条路，
// 免得路由层到处判 nil —— 少一处判断就少一处漏判。
func (h *Handler) rbacReady(w http.ResponseWriter, r *http.Request) bool {
	if h.rbac != nil && h.rbac.Enabled(r.Context()) {
		return true
	}
	writeErr(w, domain.Errorf(domain.CodeNotImplement, "用户与权限功能未启用"))
	return false
}

// loadPrincipalFromSession 依据当前会话算出主体。
func (h *Handler) loadPrincipalFromSession(ctx context.Context, sess *adminauth.Session) (Principal, error) {
	if h.rbac == nil {
		return Principal{}, nil
	}
	return h.rbac.PrincipalFor(ctx, rbacSession(sess))
}

// RequirePermission 返回一个要求 perm 权限的中间件。
//
// 用法：r.With(h.RequirePermission(rbac.PermSystemManage).Handler)。
//
// 关闭 RBAC 时**直接放行**，不做任何判定 —— 这是验收 ① 的关键：
// 关着的时候请求路径与改动前逐字一致，既不查用户表也不查权限矩阵。
func (h *Handler) RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h.rbac == nil || !h.rbac.Enabled(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}
			sess := adminSessionFromContext(r.Context())
			p, err := h.loadPrincipalFromSession(r.Context(), sess)
			if err != nil {
				writeErr(w, domain.Wrap(domain.CodeInternal, err))
				return
			}
			if !p.Can(perm) {
				writeErr(w, h.permissionDenied(r, p, perm))
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalCtxKey{}, p)))
		})
	}
}

// permissionDenied 生成拒绝响应，并附带 Explain 结果供界面显示。
//
// Details 里给「为什么拒绝」而不是只给「权限不足」：管理员配错了权限时，
// 「这一项是超管专属」和「你所在的组里有拒绝项」是两种完全不同的修法。
func (h *Handler) permissionDenied(r *http.Request, p Principal, perm string) error {
	ex := p.Explain(perm)
	msg := ex.Message
	if msg == "" {
		msg = "权限不足"
	}
	if h.log != nil {
		h.log.Info("RBAC 拒绝访问",
			"path", r.URL.Path, "permission", perm,
			"user", p.Username, "source", ex.Source)
	}
	return domain.Errorf(domain.CodePermissionDenied, "%s", msg)
}

// requirePermission 是 RequirePermission 的便捷包装，返回一个中间件函数。
func (h *Handler) requirePermission(perm string) func(http.Handler) http.Handler {
	return h.RequirePermission(perm)
}

// ---------------------------------------------------------------- 身份

// rbacMeDTO 是 GET /auth/me 的响应。
type rbacMeDTO struct {
	// Enabled RBAC 总开关。前端据此决定要不要去拉菜单过滤结果。
	Enabled bool `json:"enabled"`
	// UserID 0 表示超管。
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	// DisplayName 受管用户的显示名，超管为空。
	DisplayName string `json:"display_name,omitempty"`
	IsSuper     bool   `json:"is_super"`
	// Groups 受管用户所属的用户组名（超管为空）。
	Groups []string `json:"groups,omitempty"`
	// Menus 可见的一级导航 key。
	Menus []string `json:"menus"`
	// Permissions 有效权限项清单。
	Permissions []string `json:"permissions"`
}

// authMe 返回当前身份与可见范围。
//
// 前端登录后调它：一次拿齐「我是谁 + 我能看到哪些菜单 + 我有哪些权限」，
// 省掉再拉一次权限目录的往返。
func (h *Handler) authMe(w http.ResponseWriter, r *http.Request) {
	sess := adminSessionFromContext(r.Context())
	if h.rbac == nil || !h.rbac.Enabled(r.Context()) {
		// 关闭时也要返回结构一致的响应，只是 menus/permissions 给全量 ——
		// 前端不需要区分「RBAC 关着」和「我是超管」，导航都必须显示出来。
		writeOK(w, rbacMeDTO{
			Enabled:     false,
			Username:    sessUsername(sess),
			IsSuper:     true,
			Menus:       rbac.AllMenuKeys(),
			Permissions: nil,
		})
		return
	}
	p, err := h.loadPrincipalFromSession(r.Context(), sess)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, rbacMeDTO{
		Enabled:     true,
		UserID:      p.UserID,
		Username:    p.Username,
		DisplayName: p.DisplayName,
		IsSuper:     p.IsSuper,
		Groups:      h.groupNamesOf(r.Context(), p.Groups),
		Menus:       p.MenuKeys(),
		Permissions: p.EffectivePermissions(),
	})
}

// groupNamesOf 把组 ID 翻译成组名，翻译不到的直接跳过。
//
// 找不到的组按「不存在」处理而不是报错：删组和改成员关系本来就在别处发生，
// 身份接口不该因为一次并发删除而整个失败。
func (h *Handler) groupNamesOf(ctx context.Context, ids []int64) []string {
	if len(ids) == 0 {
		return []string{}
	}
	groups, err := h.rbac.ListGroups(ctx)
	if err != nil {
		return []string{}
	}
	names := make(map[int64]string, len(groups))
	for _, g := range groups {
		names[g.ID] = g.Name
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := names[id]; ok {
			out = append(out, name)
		}
	}
	return out
}

func sessUsername(sess *adminauth.Session) string {
	if sess == nil {
		return ""
	}
	return sess.Username
}

// authMenus 返回可见菜单，是验收 ④ 的接口。
//
// 与 authMe 分开是有意的：菜单在会话建立后就基本不变，
// 单独一个轻量接口让前端可以在刷新时只重新拉这一小份。
func (h *Handler) authMenus(w http.ResponseWriter, r *http.Request) {
	sess := adminSessionFromContext(r.Context())
	if h.rbac == nil || !h.rbac.Enabled(r.Context()) {
		writeOK(w, map[string]any{
			"enabled": false,
			"menus":   rbac.AllMenuKeys(),
		})
		return
	}
	p, err := h.loadPrincipalFromSession(r.Context(), sess)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, map[string]any{
		"enabled": true,
		"menus":   p.MenuKeys(),
	})
}

// ---------------------------------------------------------------- 权限目录

// rbacPermissionDTO 是权限目录一行。
type rbacPermissionDTO struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Category string `json:"category"`
	// SuperOnly 为真时该项永远不下放。界面应当把这两行显示为只读。
	SuperOnly bool `json:"super_only"`
	SortOrder int  `json:"sort_order"`
	// Allowed 标记当前会话是否拥有这一项，便于界面直接置灰。
	Allowed bool `json:"allowed"`
	// Source 拒绝/放行的归因，便于排查为什么某一项是灰的。
	Source string `json:"source,omitempty"`
}

func toPermissionDTOs(p Principal) []rbacPermissionDTO {
	out := make([]rbacPermissionDTO, 0, len(rbac.Catalog()))
	for _, meta := range rbac.Catalog() {
		ex := p.Explain(meta.Key)
		out = append(out, rbacPermissionDTO{
			Key:       meta.Key,
			Label:     meta.Label,
			Category:  meta.Category,
			SuperOnly: meta.SuperOnly,
			SortOrder: meta.SortOrder,
			Allowed:   ex.Allowed,
			Source:    ex.Source,
		})
	}
	return out
}

// listRBACPermissions 列出权限目录（带当前会话的判定结果）。
//
// 要求 permission.manage：这份清单等于「系统里所有能做的事」，
// 给一个普通用户看会让他知道有哪些权限项可以争取，也没什么意义。
func (h *Handler) listRBACPermissions(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	p := principalFromContext(r.Context())
	writeOK(w, map[string]any{
		"items":      toPermissionDTOs(p),
		"categories": rbac.PermissionCategories(),
		"super_only": rbac.SuperOnlyKeys(),
	})
}

// ---------------------------------------------------------------- 用户

type rbacUserDTO struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	PasswordSet bool   `json:"password_set"`
	// Groups 用户所属组名，给界面直接显示，避免再发一次请求。
	Groups []string `json:"groups"`
	// LastLoginAt 空表示从未登录。
	LastLoginAt string `json:"last_login_at,omitempty"`
}

// listRBACUsers 列出全部受管用户。
func (h *Handler) listRBACUsers(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	users, err := h.rbac.ListUsers(r.Context())
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	groups, err := h.rbac.ListGroups(r.Context())
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	byUser, err := h.rbac.GroupMembershipMap(r.Context())
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	names := make(map[int64]string, len(groups))
	for _, g := range groups {
		names[g.ID] = g.Name
	}
	out := make([]rbacUserDTO, 0, len(users))
	for _, u := range users {
		items := make([]string, 0, len(byUser[u.ID]))
		for _, gid := range byUser[u.ID] {
			if name, ok := names[gid]; ok {
				items = append(items, name)
			}
		}
		dto := rbacUserDTO{
			ID:          u.ID,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			Enabled:     u.Enabled,
			PasswordSet: u.PasswordSet,
			Groups:      items,
		}
		if u.LastLoginAt != nil {
			dto.LastLoginAt = u.LastLoginAt.Format(timeLayoutForRBAC)
		}
		out = append(out, dto)
	}
	writeOK(w, map[string]any{"items": out})
}

// createRBACUser 新建用户。
func (h *Handler) createRBACUser(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	var req struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Enabled     *bool  `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	id, err := h.rbac.CreateUser(r.Context(), req.Username, req.DisplayName, req.Password, enabled)
	if err != nil {
		writeErr(w, writeRBACErr(err))
		return
	}
	u, err := h.rbac.GetUser(r.Context(), id)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, toUserDTO(u, nil))
}

// updateRBACUser 改用户资料（显示名）。
func (h *Handler) updateRBACUser(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.rbac.UpdateUserProfile(r.Context(), id, req.DisplayName); err != nil {
		writeErr(w, writeRBACErr(err))
		return
	}
	u, err := h.rbac.GetUser(r.Context(), id)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, toUserDTO(u, nil))
}

// setRBACUserEnabled 启停用户。
func (h *Handler) setRBACUserEnabled(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.rbac.SetUserEnabled(r.Context(), id, req.Enabled); err != nil {
		writeErr(w, writeRBACErr(err))
		return
	}
	writeOK(w, map[string]any{"id": id, "enabled": req.Enabled})
}

// setRBACUserPassword 重置某个用户的密码。
func (h *Handler) setRBACUserPassword(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.rbac.SetPassword(r.Context(), id, req.Password); err != nil {
		writeErr(w, writeRBACErr(err))
		return
	}
	writeOK(w, map[string]any{"id": id})
}

// deleteRBACUser 删除用户。
func (h *Handler) deleteRBACUser(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.rbac.DeleteUser(r.Context(), id); err != nil {
		writeErr(w, writeRBACErr(err))
		return
	}
	writeOK(w, map[string]any{"id": id})
}

// setRBACUserGroups 全量设置某用户的组成员关系。
func (h *Handler) setRBACUserGroups(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		GroupIDs []int64 `json:"group_ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.rbac.SetUserGroups(r.Context(), id, req.GroupIDs); err != nil {
		writeErr(w, writeRBACErr(err))
		return
	}
	writeOK(w, map[string]any{"id": id, "group_ids": req.GroupIDs})
}

// setRBACUserOverrides 写用户级覆盖（allow/deny/空串=继承）。
func (h *Handler) setRBACUserOverrides(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		// Overrides 的值是三态字符串："allow" / "deny" / ""（继承）。
		Overrides map[string]string `json:"overrides"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.rbac.SetUserOverrides(r.Context(), id, req.Overrides); err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, map[string]any{"id": id})
}

// ---------------------------------------------------------------- 用户组

type rbacGroupDTO struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Builtin     bool    `json:"builtin"`
	MemberCount int     `json:"member_count"`
	MemberIDs   []int64 `json:"member_ids"`
	// Permissions 是 key → "allow"/"deny" 的稀疏映射。
	// 没出现的键表示这个组在这一项上是「没有表态」，继承用户级覆盖。
	Permissions map[string]string `json:"permissions"`
}

// listRBACGroups 列出全部用户组。
func (h *Handler) listRBACGroups(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	groups, err := h.rbac.ListGroups(r.Context())
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	out := make([]rbacGroupDTO, 0, len(groups))
	for _, g := range groups {
		members, err := h.rbac.MembersOfGroup(r.Context(), g.ID)
		if err != nil {
			writeErr(w, domain.Wrap(domain.CodeInternal, err))
			return
		}
		perms, err := h.rbac.GroupPermissions(r.Context(), g.ID)
		if err != nil {
			writeErr(w, domain.Wrap(domain.CodeInternal, err))
			return
		}
		out = append(out, toGroupDTO(g, members, perms))
	}
	writeOK(w, map[string]any{"items": out})
}

// createRBACGroup 新建用户组。
func (h *Handler) createRBACGroup(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	id, err := h.rbac.CreateGroup(r.Context(), req.Name, req.Description)
	if err != nil {
		writeErr(w, writeRBACErr(err))
		return
	}
	g, err := h.rbac.GetGroup(r.Context(), id)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, toGroupDTO(g, nil, nil))
}

// updateRBACGroup 改组名与说明。
func (h *Handler) updateRBACGroup(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.rbac.UpdateGroup(r.Context(), id, req.Name, req.Description); err != nil {
		writeErr(w, writeRBACErr(err))
		return
	}
	writeOK(w, map[string]any{"id": id})
}

// deleteRBACGroup 删除用户组。
func (h *Handler) deleteRBACGroup(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.rbac.DeleteGroup(r.Context(), id); err != nil {
		writeErr(w, writeRBACErr(err))
		return
	}
	writeOK(w, map[string]any{"id": id})
}

// setRBACGroupMembers 全量设置组成员。
func (h *Handler) setRBACGroupMembers(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		UserIDs []int64 `json:"user_ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.rbac.SetGroupMembers(r.Context(), id, req.UserIDs); err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, map[string]any{"id": id, "user_ids": req.UserIDs})
}

// setRBACGroupPermissions 写组权限矩阵。
//
// 超管专属项在这里被静默丢弃（Store.SetGroupPermissions 的注释解释了原因）。
func (h *Handler) setRBACGroupPermissions(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		// Permissions 是 key → "allow"/"deny" 的映射，
		// 没给的键表示清除（即这一项回到继承）。
		Permissions map[string]string `json:"permissions"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	perms := make([]rbac.GroupPermission, 0, len(req.Permissions))
	for key, raw := range req.Permissions {
		perms = append(perms, rbac.GroupPermission{Key: key, Effect: rbac.Effect(raw)})
	}
	if err := h.rbac.SetGroupPermissions(r.Context(), id, perms); err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	saved, err := h.rbac.GroupPermissions(r.Context(), id)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, map[string]any{"id": id, "permissions": groupPermissionMap(saved)})
}

// ---------------------------------------------------------------- DTO 与错误

func toUserDTO(u rbac.User, groupNames []string) rbacUserDTO {
	dto := rbacUserDTO{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Enabled:     u.Enabled,
		PasswordSet: u.PasswordSet,
		Groups:      groupNames,
	}
	if dto.Groups == nil {
		dto.Groups = []string{}
	}
	if u.LastLoginAt != nil {
		dto.LastLoginAt = u.LastLoginAt.Format(timeLayoutForRBAC)
	}
	return dto
}

func toGroupDTO(g rbac.Group, members []int64, perms []rbac.GroupPermission) rbacGroupDTO {
	if members == nil {
		members = []int64{}
	}
	return rbacGroupDTO{
		ID:          g.ID,
		Name:        g.Name,
		Description: g.Description,
		Builtin:     g.Builtin,
		MemberCount: g.MemberCount,
		MemberIDs:   members,
		Permissions: groupPermissionMap(perms),
	}
}

// groupPermissionMap 把矩阵摊平成 key → effect 的映射，便于前端直接读。
func groupPermissionMap(perms []rbac.GroupPermission) map[string]string {
	out := make(map[string]string, len(perms))
	for _, p := range perms {
		out[p.Key] = string(p.Effect)
	}
	return out
}

// writeRBACErr 把本包的错误翻译成 API 响应。
//
// 判定口径：ErrNotFound ⇒ 404，其余「用户输错了」的错（用户名重复、
// 组名重复、密码太短、内置组不可删）⇒ 400，其余按内部错误。
// 不认识的错误一律 500，宁可漏判也不要把内部错误说成「参数不对」。
func writeRBACErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, rbac.ErrNotFound):
		return domain.Wrap(domain.CodeNotFound, err)
	case errors.Is(err, rbac.ErrDuplicateUsername):
		return domain.Wrap(domain.CodeValidation, err)
	}
	if isValidationMessage(err) {
		return domain.Wrap(domain.CodeValidation, err)
	}
	return domain.Wrap(domain.CodeInternal, err)
}

func isValidationMessage(err error) bool {
	msg := err.Error()
	for _, marker := range []string{
		"不能为空", "不能包含空格", "过长", "至少 8 位", "不存在", "不可删除", "已被超级管理员占用",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// timeLayoutForRBAC 与其他管理接口保持一致的时间格式。
const timeLayoutForRBAC = "2006-01-02 15:04:05"

// ---------------------------------------------------------------- 路由

// RegisterRBACRoutes 注册用户与权限相关的路由。
//
// 分两组：用户组操作与权限矩阵都挂在 permission.manage 下，
// 用户管理挂在 user.manage 下 —— 这样「只管人」和「只改矩阵」是两种可分配的角色。
func (h *Handler) RegisterRBACRoutes(r chi.Router) {
	// 身份：任何已登录的管理员会话都能读自己的身份与菜单。
	r.Route("/auth", func(r chi.Router) {
		r.Get("/me", h.authMe)
		r.Get("/menus", h.authMenus)
	})

	r.Route("/rbac", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(h.requirePermission(rbac.PermUserManage))
			r.Get("/users", h.listRBACUsers)
			r.Post("/users", h.createRBACUser)
			r.Put("/users/{id}", h.updateRBACUser)
			r.Delete("/users/{id}", h.deleteRBACUser)
			r.Post("/users/{id}/enabled", h.setRBACUserEnabled)
			r.Post("/users/{id}/password", h.setRBACUserPassword)
			r.Post("/users/{id}/groups", h.setRBACUserGroups)
		})
		r.Group(func(r chi.Router) {
			r.Use(h.requirePermission(rbac.PermPermissionManage))
			r.Get("/permissions", h.listRBACPermissions)
			r.Get("/users/{id}/overrides", h.listRBACUserOverrides)
			r.Post("/users/{id}/overrides", h.setRBACUserOverrides)
			r.Get("/groups", h.listRBACGroups)
			r.Post("/groups", h.createRBACGroup)
			r.Put("/groups/{id}", h.updateRBACGroup)
			r.Delete("/groups/{id}", h.deleteRBACGroup)
			r.Post("/groups/{id}/members", h.setRBACGroupMembers)
			r.Post("/groups/{id}/permissions", h.setRBACGroupPermissions)
		})
	})
}

// listRBACUserOverrides 读某个用户的覆盖表。
func (h *Handler) listRBACUserOverrides(w http.ResponseWriter, r *http.Request) {
	if !h.rbacReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	overrides, err := h.rbac.UserOverrides(r.Context(), id)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	out := make(map[string]string, len(overrides))
	for k, v := range overrides {
		out[k] = string(v)
	}
	writeOK(w, map[string]any{"user_id": id, "overrides": out})
}
