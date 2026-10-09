package rbac

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"litepan/internal/settings"
	"litepan/pkg/security"
)

// Settings 是本包读配置所需的最小面。
//
// 只声明 String/Bool 两个方法而不是直接吃 *settings.Service：
// 单元测试里塞个 map 就能跑，不必为了测权限判定去起一个真配置仓库。
//
// 签名与 settings.Service 的同名方法一致、且不带 fallback 参数：
// 真正的 settings.Service 在键缺失时会回落到 registry 里登记的默认值，
// 再传一个 fallback 就是第二个真相来源，两边不一致时更难查。
// 这两个键的默认值（false 与空串）就登记在 internal/settings/registry.go。
type Settings interface {
	String(key string) string
	Bool(key string) bool
}

// Service 是 RBAC 的门面：开关判定、身份装配、用户与组的增删改。
type Service struct {
	store *Store
	cfg   Settings
	log   Logger

	// now 供测试注入固定时间。
	now func() time.Time
}

// Logger 是本包需要的最小日志面（避免把 slog 泄漏到包外 API 上）。
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any) {}
func (nopLogger) Warn(string, ...any) {}

// NewService 构造 Service。cfg 为 nil 时 Enabled 永远返回 false，
// 也就是整个功能处于关闭状态。
func NewService(store *Store, cfg Settings, log Logger) *Service {
	if log == nil {
		log = nopLogger{}
	}
	return &Service{
		store: store,
		cfg:   cfg,
		log:   log,
		now:   time.Now,
	}
}

// SetClock 注入固定时钟（仅测试用）。
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// Enabled 报告 RBAC 总开关。
//
// 这是整个功能的唯一入口判断：关着的时候本包的每个方法都直接放行，
// 调用方也压根不会来问权限 —— 所以「关闭时行为与上线前完全一致」
// 不是靠逐处 if，而是靠根本不进入这条路径。
func (s *Service) Enabled(ctx context.Context) bool {
	if s == nil || s.store == nil || s.cfg == nil {
		return false
	}
	return s.cfg.Bool(settings.KeyMORBACEnabled)
}

// AdminUsername 返回当前配置项管理员的用户名，也就是超管账号名。
func (s *Service) AdminUsername(ctx context.Context) string {
	if s == nil || s.cfg == nil {
		return ""
	}
	return strings.TrimSpace(s.cfg.String(AdminUsernameSettingKey))
}

// SettingsWithRaw 把一个「按 registry 读」的 Settings 包成也能读裸配置的版本。
//
// 只对 AdminUsernameSettingKey 这一个键生效，其余键原样透传。
//
// 为什么需要它：`admin_username` 是 adminauth 的凭据项，**有意不进**
// internal/settings 的 registry（它不参与设置页的展示与批量更新）。
// 而 settings.Service 只认登记在册的键，未登记的键一律返回 ""。
// 于是 rbac 拿 settings.Service 直接读这个键会永远得到 ""，
// isSuperSession 就永远判 false —— 功能一开启，超管自己被关在门外。
//
// 这个包装放在 rbac 包里而不是装配层，是为了让生产装配和接口层测试
// 用的是**同一份实现**。曾经把它放在装配层，结果接口层测试复制了一份简化版，
// 测试全绿而生产超管被锁死 —— 复制实现的测试什么都测不出来。
//
// raw 为 nil 时退化成只透传 cfg（此时读 admin_username 仍返回 ""，
// 对应「配置项读不出来」，isSuperSession 会一律判非超管，fail closed）。
func SettingsWithRaw(cfg Settings, raw RawConfig) Settings {
	if raw == nil {
		return cfg
	}
	return settingsWithRaw{base: cfg, raw: raw}
}

// AdminUsernameSettingKey 是判定超管身份的会话用户名应当匹配的配置键。
//
// 与 adminauth.KeyAdminUsername 同值。写字符串常量而不是 import adminauth，
// 是为了不让 adminauth 反过来依赖 rbac（那会成一个环）。
// 这一处重复是有意的，测试里有断言盯住两者相等。
const AdminUsernameSettingKey = "admin_username"

// RawConfig 直接读配置仓库，绕过 settings 的登记校验。
//
// 与 domain.ConfigRepository 的 Get 方法签名一致，所以配置仓库可以直接传进来。
type RawConfig interface {
	Get(ctx context.Context, key string) (string, bool, error)
}

type settingsWithRaw struct {
	base Settings
	raw  RawConfig
}

func (w settingsWithRaw) String(key string) string {
	if key == AdminUsernameSettingKey {
		v, _, err := w.raw.Get(context.Background(), AdminUsernameSettingKey)
		if err != nil {
			return ""
		}
		return v
	}
	return w.base.String(key)
}

func (w settingsWithRaw) Bool(key string) bool { return w.base.Bool(key) }

// EnsureSeed 幂等播种内置数据：权限字典 + 两个内置组。
//
// 每次进程启动都跑一遍。刻意放在 Go 代码里而不是迁移 SQL 里：
// 权限清单是代码里的一张表，写两遍迟早会漂移。
// 失败只记日志不阻断启动 —— 播种失败意味着权限判定会 fail closed（谁都不许），
// 但把服务起不来让运维连后台都进不去，损失更大。
func (s *Service) EnsureSeed(ctx context.Context) error {
	if !s.Enabled(ctx) {
		return nil
	}
	if err := s.store.EnsureCatalog(ctx); err != nil {
		return err
	}
	for _, g := range []Group{
		{Name: BuiltinGroupUser, Description: "只读看板，默认不给写操作", Builtin: true},
		{Name: BuiltinGroupRequester, Description: "处理求片审核，默认不给系统设置", Builtin: true},
	} {
		if _, err := s.store.CreateGroup(ctx, g); err != nil && !errors.Is(err, ErrDuplicateUsername) {
			return err
		}
	}
	s.log.Info("RBAC 权限目录已就绪", "permissions", len(catalog), "builtin_groups", 2)
	return nil
}

// ---------------------------------------------------------------- 身份

// Session 是本包对外的会话身份载荷。
//
// 声明成独立结构而不是直接用 *adminauth.Session：
// rbac 不能 import adminauth（adminauth 通过 ExtraUserAuth 反向依赖本包的语义，
// 直接 import 会成环），装配处负责把两者对上字段。
type Session struct {
	// UserID 0 表示配置项管理员（超管），>0 表示受管用户。
	UserID   int64
	Username string
	// DisplayName 仅用于日志与界面，不参与判定。
	DisplayName string
	// MustChangePassword 恒为 false：委托用户不进强改密分支，理由见 Authenticate。
	MustChangePassword bool
}

// isSuperSession 判断一个会话是不是「当前配置项管理员本人」。
//
// 判定是**用户名比对**，不是 UserID：超管在 rbac_users 里没有行，
// 超管身份完全由「本次会话的用户名 == 当前 admin_username」决定。
// UserID <= 0 只是「查不到受管用户」的哨兵，单独出现时什么都不是。
func (s *Service) isSuperSession(sess Session) bool {
	if sess.UserID > 0 {
		return false
	}
	admin := s.AdminUsername(context.Background())
	if admin == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(sess.Username), admin)
}

// PrincipalFor 依据一次会话算出该用户的权限主体。
//
// 超管 ⇒ 直接给一个绕过一切的 Principal，**不查库**。
// 不查库很重要：超管路径上任何一次数据库故障都不应该把管理员挡在门外。
//
// 三条路径：
//   - 超管（见 isSuperSession）：不查库。
//   - 受管用户（UserID > 0）：查组、组权限、用户级覆盖。
//   - 其它（UserID <= 0 但用户名不是当前超管）：什么都不给。
//     这一条是 fail closed 的关键：UserID <= 0 只是一个「查不到受管用户」
//     的哨兵值，拿它当超管通行证等于「只要能让会话带上 UserID=0 就能接管」。
//     会话 cookie 本身是签名的，伪造不了 UserID，但一个空会话（没有
//     requireAdmin 保护的路径上）落进来就是 UserID=0 —— 那种情况下
//     必须拒绝而不是放行。
func (s *Service) PrincipalFor(ctx context.Context, sess Session) (Principal, error) {
	if !s.Enabled(ctx) {
		return Principal{}, nil
	}
	if sess.UserID <= 0 {
		if s.isSuperSession(sess) {
			return NewSuperPrincipal(sess.Username), nil
		}
		// 不是超管又没有用户 ID：没有任何可依据的身份，全部拒绝。
		return Principal{Username: sess.Username}, nil
	}
	u, hash, err := s.store.GetUserWithHash(ctx, sess.UserID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// 会话还在但用户被删了：返回一个什么都做不了的 Principal。
			// 这里不能 panic 也不能放行 —— 会话 cookie 还没过期的那段时间里，
			// 「用户不存在」必须等价于「没有任何权限」。
			return Principal{UserID: sess.UserID, Username: sess.Username}, nil
		}
		return Principal{}, err
	}
	if !u.Enabled {
		return Principal{UserID: u.ID, Username: u.Username, DisplayName: u.DisplayName}, nil
	}
	if strings.TrimSpace(hash) == "" {
		return Principal{UserID: u.ID, Username: u.Username, DisplayName: u.DisplayName}, nil
	}
	groupIDs, err := s.store.GroupIDsOfUser(ctx, u.ID)
	if err != nil {
		return Principal{}, err
	}
	groupEffects, err := s.store.MergedGroupEffects(ctx, groupIDs)
	if err != nil {
		return Principal{}, err
	}
	overrides, err := s.store.UserOverridesOf(ctx, u.ID)
	if err != nil {
		return Principal{}, err
	}
	return Principal{
		UserID:       u.ID,
		Username:     u.Username,
		DisplayName:  u.DisplayName,
		Groups:       groupIDs,
		GroupEffects: groupEffects,
		Overrides:    overrides,
	}, nil
}

// Authenticate 实现 adminauth.ExtraUserAuth 的认证部分。
//
// 注意方法名与签名：adminauth 那边声明的是一个接口，
// 装配时把本服务塞进去即可（*Service 的方法集满足它）。
func (s *Service) Authenticate(ctx context.Context, username, password string) (Session, bool, error) {
	if !s.Enabled(ctx) {
		return Session{}, false, nil
	}
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return Session{}, false, nil
	}
	// 保留 TrimSpace 之后再比较：用户名尾部一个空格不该构成另一个账号。
	if FoldUsername(username) == FoldUsername(s.AdminUsername(ctx)) {
		return Session{}, false, nil
	}
	u, hash, err := s.store.GetUserByName(ctx, username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Session{}, false, nil
		}
		return Session{}, false, err
	}
	if !u.Enabled || strings.TrimSpace(hash) == "" {
		return Session{}, false, nil
	}
	if !security.CheckPasswordHash(hash, password) {
		return Session{}, false, nil
	}
	s.store.TouchUserLogin(ctx, u.ID)
	s.log.Info("RBAC 用户登录成功", "username", u.Username, "user_id", u.ID)
	return Session{
		UserID:      u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		// 委托用户不进 must_change_password 分支：
		// 那个分支是为管理员密码准备的，套到委托用户身上会要求每个用户
		// 都去系统设置页改密码，而那里只有超管能进。改密走专用端点。
		MustChangePassword: false,
	}, true, nil
}

// ---------------------------------------------------------------- 用户增删改

// ValidateUsername 检查登录名是否可用。
func ValidateUsername(username string) error {
	u := strings.TrimSpace(username)
	switch {
	case u == "":
		return errors.New("登录名不能为空")
	case len([]rune(u)) > 64:
		return errors.New("登录名过长（最多 64 个字符）")
	case strings.ContainsAny(u, " \t\r\n"):
		return errors.New("登录名不能包含空格")
	}
	return nil
}

// ValidatePassword 检查密码强度。
//
// 底线 8 位：不设复杂度规则（大小写+符号+数字的组合规则实测只会把人推向
// `Passw0rd!` 这种写在便利贴上的密码），长度是唯一真正有效的约束。
func ValidatePassword(password string) error {
	if len(password) < 8 {
		return errors.New("密码至少 8 位")
	}
	if len(password) > 128 {
		return errors.New("密码过长（最多 128 个字符）")
	}
	return nil
}

// CreateUser 新建委托用户，并按配置把它放进默认组。
func (s *Service) CreateUser(ctx context.Context, username, displayName, password string, enabled bool) (int64, error) {
	if err := ValidateUsername(username); err != nil {
		return 0, err
	}
	if err := ValidatePassword(password); err != nil {
		return 0, err
	}
	if FoldUsername(username) == FoldUsername(s.AdminUsername(ctx)) {
		return 0, fmt.Errorf("登录名「%s」已被超级管理员占用", strings.TrimSpace(username))
	}
	hash := security.HashPassword(password)
	id, err := s.store.CreateUser(ctx, User{
		Username:    strings.TrimSpace(username),
		DisplayName: strings.TrimSpace(displayName),
		Enabled:     enabled,
	}, hash)
	if err != nil {
		return 0, err
	}
	// 默认组不存在时静默跳过：报错会让「建用户」这个动作整体失败，
	// 而用户其实已经建好了 —— 半个操作比没有操作更难收拾。
	group := strings.TrimSpace(s.cfg.String(settings.KeyMORBACDefaultUserGroup))
	if group != "" {
		if err := s.AddUserToGroupByName(ctx, id, group); err != nil {
			s.log.Warn("新用户加入默认组失败", "user_id", id, "group", group, "err", err)
		}
	}
	return id, nil
}

// AddUserToGroupByName 按组名加人（组名不存在时报一个能看懂的错）。
func (s *Service) AddUserToGroupByName(ctx context.Context, userID int64, groupName string) error {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return nil
	}
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.Name == groupName {
			return s.store.AddUserToGroup(ctx, g.ID, userID)
		}
	}
	return fmt.Errorf("用户组「%s」不存在", groupName)
}

// UpdateUserProfile 改显示名（不改登录名）。
//
// 登录名不给接口改：它同时出现在登录页输入框、旧会话里和成员的审计记录上，
// 改名会让「这个组里原来那个人是谁」这件事变得不可追溯。
func (s *Service) UpdateUserProfile(ctx context.Context, id int64, displayName string) error {
	u, err := s.store.GetUser(ctx, id)
	if err != nil {
		return err
	}
	u.DisplayName = strings.TrimSpace(displayName)
	return s.store.UpdateUser(ctx, u)
}

// SetPassword 改某个用户的密码。
func (s *Service) SetPassword(ctx context.Context, id int64, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	return s.store.SetUserPassword(ctx, id, security.HashPassword(password))
}

// DeleteUser 删除用户。超管不是一行用户数据，所以这里没有任何「不能删超管」的分支需要写。
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("不能删除超级管理员")
	}
	return s.store.DeleteUser(ctx, id)
}

// ---------------------------------------------------------------- 组增删改

// CreateGroup 新建用户组。
func (s *Service) CreateGroup(ctx context.Context, name, description string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("用户组名不能为空")
	}
	return s.store.CreateGroup(ctx, Group{Name: name, Description: strings.TrimSpace(description)})
}

// UpdateGroup 改组名与说明。
func (s *Service) UpdateGroup(ctx context.Context, id int64, name, description string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("用户组名不能为空")
	}
	return s.store.UpdateGroup(ctx, Group{ID: id, Name: name, Description: strings.TrimSpace(description)})
}

// DeleteGroup 删除用户组。
func (s *Service) DeleteGroup(ctx context.Context, id int64) error {
	g, err := s.store.GetGroup(ctx, id)
	if err != nil {
		return err
	}
	if g.Builtin {
		return errors.New("内置用户组不可删除")
	}
	return s.store.DeleteGroup(ctx, id)
}

// ---------------------------------------------------------------- 查询
//
// 这些方法不做任何开关判断，也不写库。开关只在两处有意义：
// 接口入口（路由中间件）与鉴权（Authenticate / PrincipalFor）。
// 查询层再判一次开关只会让「开关开着但数据是空的」和
// 「开关关着」变成同一个响应，排障时分不出来。

// ListUsers 列出全部用户，按登录名排序。
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	return s.store.ListUsers(ctx)
}

// GetUser 读单个用户。
func (s *Service) GetUser(ctx context.Context, id int64) (User, error) {
	return s.store.GetUser(ctx, id)
}

// ListGroups 列出全部用户组，按内置优先、名称次之排序。
func (s *Service) ListGroups(ctx context.Context) ([]Group, error) {
	return s.store.ListGroups(ctx)
}

// GetGroup 读单个用户组。
func (s *Service) GetGroup(ctx context.Context, id int64) (Group, error) {
	return s.store.GetGroup(ctx, id)
}

// MembersOfGroup 列出组内成员的 user_id。
func (s *Service) MembersOfGroup(ctx context.Context, groupID int64) ([]int64, error) {
	return s.store.MembersOfGroup(ctx, groupID)
}

// GroupPermissions 列出某个组的权限矩阵（按 key 排序）。
func (s *Service) GroupPermissions(ctx context.Context, groupID int64) ([]GroupPermission, error) {
	return s.store.GroupPermissionsOf(ctx, groupID)
}

// UserOverrides 读某个用户的覆盖表，返回 key → effect。
//
// 没有行就等于「继承」，所以这里返回的是稀疏表而不是补全的 27 项。
func (s *Service) UserOverrides(ctx context.Context, userID int64) (map[string]Effect, error) {
	return s.store.UserOverridesOf(ctx, userID)
}

// GroupMembershipMap 一次读出所有用户的组成员关系。
//
// 管理界面要渲染整张用户表，每行都要显示组名；逐个用户查会退化成 N+1。
func (s *Service) GroupMembershipMap(ctx context.Context) (map[int64][]int64, error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]int64, len(users))
	for _, u := range users {
		ids, err := s.store.GroupIDsOfUser(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			out[u.ID] = ids
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- 授权写入

// SetUserGroups 全量设置某个用户的组成员关系。
//
// 先确认用户存在再写，否则一个不存在的 user_id 也能往组里塞一行
// 悬空成员记录 —— 那行记录既查不出名字也不会有人清理。
//
// 注意写库的仍是 SetGroupMembers（按组全量替换）而不是「按用户替换」：
// 两个方向的替换在不同并发下会互相覆盖 —— 界面同时改两个人的组时，
// 按组全量替换会让后一次提交把前一次对同组其他人的修改吃掉。
// 所以这里逐个组调 AddUserToGroup，并在事务外先删掉不在新列表里的组。
func (s *Service) SetUserGroups(ctx context.Context, userID int64, groupIDs []int64) error {
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return err
	}
	unique := dedupIDs(groupIDs)
	for _, gid := range unique {
		if _, err := s.store.GetGroup(ctx, gid); err != nil {
			return err
		}
	}
	current, err := s.store.GroupIDsOfUser(ctx, userID)
	if err != nil {
		return err
	}
	keep := make(map[int64]struct{}, len(unique))
	for _, gid := range unique {
		keep[gid] = struct{}{}
	}
	for _, gid := range current {
		if _, ok := keep[gid]; ok {
			continue
		}
		if err := s.store.RemoveUserFromGroup(ctx, gid, userID); err != nil {
			return err
		}
	}
	for _, gid := range unique {
		if err := s.store.AddUserToGroup(ctx, gid, userID); err != nil {
			return err
		}
	}
	return nil
}

// SetUserEnabled 启停用户。
//
// 与删除的区别要留着：停用之后这个人不能登录，但他在组里的位置、
// 以及他名下的覆盖表都还在 —— 账号共享、临时外协人员停用时，
// 重新启用应当什么都不用重配。
func (s *Service) SetUserEnabled(ctx context.Context, userID int64, enabled bool) error {
	if userID <= 0 {
		return errors.New("不能停用超级管理员")
	}
	return s.store.SetUserEnabled(ctx, userID, enabled)
}

// SetGroupMembers 全量设置某个组的成员。
func (s *Service) SetGroupMembers(ctx context.Context, groupID int64, userIDs []int64) error {
	if _, err := s.store.GetGroup(ctx, groupID); err != nil {
		return err
	}
	return s.store.SetGroupMembers(ctx, groupID, userIDs)
}

// SetGroupPermissions 全量替换某个组的权限矩阵。
//
// 超管专属项与未知 key 会被 Store 静默丢弃，所以这里不重复过滤 —
// 过滤规则要改就只有 Store 里那一个地方。
func (s *Service) SetGroupPermissions(ctx context.Context, groupID int64, perms []GroupPermission) error {
	if _, err := s.store.GetGroup(ctx, groupID); err != nil {
		return err
	}
	return s.store.SetGroupPermissions(ctx, groupID, perms)
}

// SetUserOverrides 写用户级覆盖。
//
// 值是三态字符串：allow / deny / 空串（继承，即删掉这一行）。
// 空串当继承是接口约定，也是为了让「全量提交整个界面」天然能表达
// 「取消这一项的个人特例」。
func (s *Service) SetUserOverrides(ctx context.Context, userID int64, overrides map[string]string) error {
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return err
	}
	return s.store.SetUserOverrides(ctx, userID, overrides)
}

// ---------------------------------------------------------------- 诊断

// CatalogDrift 报告「代码里的权限清单」与「库里的字典表」差了什么。
//
// 正常情况下 EnsureSeed 每次启动都会把两者对齐，所以这个只在
// 「数据库被手工改过」或者「EnsureSeed 没跑成功」时才会非空。
func (s *Service) CatalogDrift(ctx context.Context) (missingInDB, extraInDB []string, err error) {
	rows, err := s.store.PermissionRows(ctx)
	if err != nil {
		return nil, nil, err
	}
	inDB := make(map[string]struct{}, len(rows))
	for _, k := range rows {
		inDB[k] = struct{}{}
	}
	inCode := make(map[string]struct{}, len(catalog))
	for _, meta := range catalog {
		inCode[meta.Key] = struct{}{}
		if _, ok := inDB[meta.Key]; !ok {
			missingInDB = append(missingInDB, meta.Key)
		}
	}
	for _, k := range rows {
		if _, ok := inCode[k]; !ok {
			extraInDB = append(extraInDB, k)
		}
	}
	return missingInDB, extraInDB, nil
}
