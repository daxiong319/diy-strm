package rbac

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// User 是 rbac_users 的一行。
//
// 注意：这里**没有** is_super 列。超级管理员不是一行数据（见迁移 0040 的注释），
// 所以不存在「把某个用户的 is_super 改成 1」这种可能，也不需要任何入口去拦它。
type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	// Enabled=false 表示临时收回：不能登录。
	// 立即踢掉已登录设备由调用方配合 session generation 实现（见 Service 注释）。
	Enabled     bool       `json:"enabled"`
	PasswordSet bool       `json:"password_set"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Group 是 rbac_user_groups 的一行。
type Group struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Builtin 是内置组（普通用户/求片审核员）。可改名可改权限，只是不能删。
	Builtin     bool      `json:"builtin"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Builtin 组名。与 参考实现 的「普通用户」「求片审核员」同名。
const (
	BuiltinGroupUser       = "普通用户"
	BuiltinGroupRequester  = "求片审核员"
	builtinDescriptionUser = "只读看板，默认不给写操作"
)

// Store 是 RBAC 的数据访问层。
//
// 只用标准库 database/sql，不引 GORM：这几张表的查询形状都固定
// （按 id 取、按 key 取、列 join），手写 SQL 比 ORM 更好读，
// 也少一层「ORM 猜列名」的意外。
type Store struct {
	write *sql.DB
	read  *sql.DB
}

// NewStore 构造 Store。
//
// write 为 nil 时只读可用，写操作返回明确错误而不是 panic ——
// 装配顺序出错时能看清是什么问题。
func NewStore(write, read *sql.DB) *Store {
	if read == nil {
		read = write
	}
	return &Store{write: write, read: read}
}

var errNoWriteHandle = errors.New("rbac: 未配置写库句柄")

// ---------------------------------------------------------------- 权限目录

// EnsureCatalog 把权限项字典幂等写入 rbac_permissions。
//
// 只插缺失的行，不覆盖已有行：管理员可能改过 label（比如把「加订阅」改成
// 「新增订阅」），重启不该把人家的文案冲回去。super_only 与 sort_order 每次都对齐，
// 因为它们是代码语义不是文案。
func (s *Store) EnsureCatalog(ctx context.Context) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	for _, meta := range catalog {
		superOnly := 0
		if meta.SuperOnly {
			superOnly = 1
		}
		if _, err := s.write.ExecContext(ctx, `
			INSERT INTO rbac_permissions(key, label, category, super_only, sort_order)
			VALUES(?, ?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				super_only = excluded.super_only,
				sort_order = excluded.sort_order`,
			meta.Key, meta.Label, meta.Category, superOnly, meta.SortOrder); err != nil {
			return fmt.Errorf("seed permission %s: %w", meta.Key, err)
		}
	}
	return nil
}

// PermissionRows 读取字典表里已登记的 key（诊断用：与 catalog 代码比对）。
func (s *Store) PermissionRows(ctx context.Context) ([]string, error) {
	if s.read == nil {
		return nil, errNoWriteHandle
	}
	rows, err := s.read.QueryContext(ctx, `SELECT key FROM rbac_permissions ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- 用户

const userColumns = `id, username, display_name, password_hash, enabled, last_login_at, created_at, updated_at`

func scanUser(scan func(dest ...any) error) (User, string, error) {
	var u User
	var hash string
	var enabled int
	var lastLogin sql.NullTime
	var created, updated time.Time
	err := scan(&u.ID, &u.Username, &u.DisplayName, &hash, &enabled, &lastLogin, &created, &updated)
	if err != nil {
		return User{}, "", err
	}
	u.Enabled = enabled != 0
	u.PasswordSet = strings.TrimSpace(hash) != ""
	u.LastLoginAt = nil
	if lastLogin.Valid {
		t := lastLogin.Time
		u.LastLoginAt = &t
	}
	u.CreatedAt = created
	u.UpdatedAt = updated
	return u, hash, nil
}

// ListUsers 返回全部受管用户（按 id 升序）。
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	if s.read == nil {
		return nil, errNoWriteHandle
	}
	rows, err := s.read.QueryContext(ctx, `SELECT `+userColumns+` FROM rbac_users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, _, err := scanUser(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUser 按 id 取用户。
func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	if s.read == nil {
		return User{}, errNoWriteHandle
	}
	u, _, err := s.GetUserWithHash(ctx, id)
	return u, err
}

// GetUserWithHash 按 id 取用户并返回密码哈希（只在登录与改密路径用）。
func (s *Store) GetUserWithHash(ctx context.Context, id int64) (User, string, error) {
	if s.read == nil {
		return User{}, "", errNoWriteHandle
	}
	row := s.read.QueryRowContext(ctx, `SELECT `+userColumns+` FROM rbac_users WHERE id=?`, id)
	u, hash, err := scanUser(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrNotFound
	}
	return u, hash, err
}

// GetUserByName 按登录名取用户（大小写不敏感）。
func (s *Store) GetUserByName(ctx context.Context, username string) (User, string, error) {
	if s.read == nil {
		return User{}, "", errNoWriteHandle
	}
	row := s.read.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM rbac_users WHERE username_fold=?`, FoldUsername(username))
	u, hash, err := scanUser(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrNotFound
	}
	return u, hash, err
}

// FoldUsername 是登录名的比较口径：去首尾空白 + 转小写。
//
// 中文用户名不受影响（本来就没有大小写），ASCII 用户名则 Alice 与 alice 是同一个。
func FoldUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// ErrNotFound 表示目标不存在。
var ErrNotFound = errors.New("rbac: 记录不存在")

// ErrDuplicateUsername 表示登录名已被占用。
var ErrDuplicateUsername = errors.New("rbac: 登录名已存在")

// CreateUser 新建用户。username 重复返回 ErrDuplicateUsername。
func (s *Store) CreateUser(ctx context.Context, u User, passwordHash string) (int64, error) {
	if s.write == nil {
		return 0, errNoWriteHandle
	}
	enabled := 0
	if u.Enabled {
		enabled = 1
	}
	res, err := s.write.ExecContext(ctx, `
		INSERT INTO rbac_users(username, username_fold, display_name, password_hash, enabled)
		VALUES(?, ?, ?, ?, ?)`,
		u.Username, FoldUsername(u.Username), u.DisplayName, passwordHash, enabled)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrDuplicateUsername
		}
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateUser 更新用户的资料字段（不含密码、不含启停）。
func (s *Store) UpdateUser(ctx context.Context, u User) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	res, err := s.write.ExecContext(ctx, `
		UPDATE rbac_users SET username=?, username_fold=?, display_name=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		u.Username, FoldUsername(u.Username), u.DisplayName, u.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateUsername
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserEnabled 启停用户。
func (s *Store) SetUserEnabled(ctx context.Context, id int64, enabled bool) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	v := 0
	if enabled {
		v = 1
	}
	res, err := s.write.ExecContext(ctx,
		`UPDATE rbac_users SET enabled=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, v, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserPassword 更新密码哈希。
func (s *Store) SetUserPassword(ctx context.Context, id int64, passwordHash string) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	res, err := s.write.ExecContext(ctx,
		`UPDATE rbac_users SET password_hash=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, passwordHash, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchUserLogin 记录最近登录时间。
func (s *Store) TouchUserLogin(ctx context.Context, id int64) {
	if s.write == nil {
		return
	}
	_, _ = s.write.ExecContext(ctx,
		`UPDATE rbac_users SET last_login_at=CURRENT_TIMESTAMP WHERE id=?`, id)
}

// DeleteUser 删除用户（连带组成员关系与用户级覆盖）。
//
// 权限矩阵里的授权记录按用户 ID 存，删用户时不清理的话，
// 将来新建的用户碰巧拿到同一个 ID 就会继承上一个用户残留的权限。
// 所以这里在一个事务里把三张表的该用户行全删掉。
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DELETE FROM rbac_group_members WHERE user_id=?`,
		`DELETE FROM rbac_user_overrides WHERE user_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, id); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM rbac_users WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- 用户组

// ListGroups 返回全部用户组（按 id 升序），带成员数。
func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	if s.read == nil {
		return nil, errNoWriteHandle
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT g.id, g.name, g.description, g.builtin, g.created_at, g.updated_at,
		       (SELECT COUNT(*) FROM rbac_group_members m WHERE m.group_id = g.id)
		FROM rbac_user_groups g ORDER BY g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		var builtin, members int
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &builtin, &g.CreatedAt, &g.UpdatedAt, &members); err != nil {
			return nil, err
		}
		g.Builtin = builtin != 0
		g.MemberCount = members
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetGroup 按 id 取组。
func (s *Store) GetGroup(ctx context.Context, id int64) (Group, error) {
	if s.read == nil {
		return Group{}, errNoWriteHandle
	}
	var g Group
	var builtin, members int
	err := s.read.QueryRowContext(ctx, `
		SELECT g.id, g.name, g.description, g.builtin, g.created_at, g.updated_at,
		       (SELECT COUNT(*) FROM rbac_group_members m WHERE m.group_id = g.id)
		FROM rbac_user_groups g WHERE g.id=?`, id).
		Scan(&g.ID, &g.Name, &g.Description, &builtin, &g.CreatedAt, &g.UpdatedAt, &members)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	if err != nil {
		return Group{}, err
	}
	g.Builtin = builtin != 0
	g.MemberCount = members
	return g, nil
}

// CreateGroup 新建用户组。
func (s *Store) CreateGroup(ctx context.Context, g Group) (int64, error) {
	if s.write == nil {
		return 0, errNoWriteHandle
	}
	builtin := 0
	if g.Builtin {
		builtin = 1
	}
	res, err := s.write.ExecContext(ctx, `
		INSERT INTO rbac_user_groups(name, description, builtin) VALUES(?, ?, ?)`,
		g.Name, g.Description, builtin)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("%w: 用户组名「%s」", ErrDuplicateUsername, g.Name)
		}
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateGroup 更新用户组名与说明（builtin 标记不可改）。
func (s *Store) UpdateGroup(ctx context.Context, g Group) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	res, err := s.write.ExecContext(ctx, `
		UPDATE rbac_user_groups SET name=?, description=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		g.Name, g.Description, g.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: 用户组名「%s」", ErrDuplicateUsername, g.Name)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteGroup 删除用户组（连带成员关系与权限矩阵）。
func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DELETE FROM rbac_group_members WHERE group_id=?`,
		`DELETE FROM rbac_group_permissions WHERE group_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, id); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM rbac_user_groups WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- 组成员

// GroupIDsOfUser 返回用户所属的组 ID（已排序）。
func (s *Store) GroupIDsOfUser(ctx context.Context, userID int64) ([]int64, error) {
	if s.read == nil {
		return nil, errNoWriteHandle
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT group_id FROM rbac_group_members WHERE user_id=? ORDER BY group_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetGroupMembers 全量替换某组的成员列表。
func (s *Store) SetGroupMembers(ctx context.Context, groupID int64, userIDs []int64) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM rbac_group_members WHERE group_id=?`, groupID); err != nil {
		return err
	}
	for _, uid := range dedupIDs(userIDs) {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO rbac_group_members(group_id, user_id) VALUES(?, ?)`, groupID, uid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddUserToGroup 把用户加入组（幂等）。
func (s *Store) AddUserToGroup(ctx context.Context, groupID, userID int64) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	_, err := s.write.ExecContext(ctx,
		`INSERT OR IGNORE INTO rbac_group_members(group_id, user_id) VALUES(?, ?)`, groupID, userID)
	return err
}

// RemoveUserFromGroup 把用户移出组。
//
// 没有「移出后组空了就删组」的逻辑：组的价值不只在于现在有没有人。
func (s *Store) RemoveUserFromGroup(ctx context.Context, groupID, userID int64) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	_, err := s.write.ExecContext(ctx,
		`DELETE FROM rbac_group_members WHERE group_id=? AND user_id=?`, groupID, userID)
	return err
}

// MembersOfGroup 返回组内用户 ID（已排序）。
func (s *Store) MembersOfGroup(ctx context.Context, groupID int64) ([]int64, error) {
	if s.read == nil {
		return nil, errNoWriteHandle
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT user_id FROM rbac_group_members WHERE group_id=? ORDER BY user_id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func dedupIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// ---------------------------------------------------------------- 组权限矩阵

// GroupPermission 是一个 (组, 权限项, 效果) 三元组。
type GroupPermission struct {
	Key    string `json:"permission_key"`
	Effect Effect `json:"effect"`
}

// GroupPermissionsOf 返回某组的权限矩阵（按 key 升序）。
func (s *Store) GroupPermissionsOf(ctx context.Context, groupID int64) ([]GroupPermission, error) {
	if s.read == nil {
		return nil, errNoWriteHandle
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT permission_key, effect FROM rbac_group_permissions
		WHERE group_id=? ORDER BY permission_key`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupPermission
	for rows.Next() {
		var k, e string
		if err := rows.Scan(&k, &e); err != nil {
			return nil, err
		}
		eff, ok := ParseEffect(e)
		if !ok {
			continue
		}
		out = append(out, GroupPermission{Key: k, Effect: eff})
	}
	return out, rows.Err()
}

// GroupPermissionsOfUsers 批量取多个组的权限矩阵，合并成「用户 → key → 效果」。
//
// 合并规则与 Principal.GroupEffects 一致：任一组 deny ⇒ deny；
// 否则任一组 allow ⇒ allow。
func (s *Store) GroupPermissionsOfUsers(ctx context.Context, groupIDs []int64) (map[int64]map[string]Effect, error) {
	out := map[int64]map[string]Effect{}
	if len(groupIDs) == 0 {
		return out, nil
	}
	if s.read == nil {
		return nil, errNoWriteHandle
	}
	placeholders := make([]string, 0, len(groupIDs))
	args := make([]any, 0, len(groupIDs))
	for _, id := range groupIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT group_id, permission_key, effect FROM rbac_group_permissions
		WHERE group_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var gid int64
		var k, e string
		if err := rows.Scan(&gid, &k, &e); err != nil {
			return nil, err
		}
		eff, ok := ParseEffect(e)
		if !ok {
			continue
		}
		if out[gid] == nil {
			out[gid] = make(map[string]Effect)
		}
		out[gid][k] = eff
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, rows.Err()
}

// MergedGroupEffects 取若干个组对同一批权限项的合并效果。
//
// 合并规则与 Principal.GroupEffects 的解读一致：
// 任一组 deny ⇒ deny（deny 最高），否则任一组 allow ⇒ allow，都没有 ⇒ 没有行（继承）。
//
// 这个函数只给「读一个用户的 Principal」用；界面上要逐组看矩阵时用 GroupPermissionsOf。
func (s *Store) MergedGroupEffects(ctx context.Context, groupIDs []int64) (map[string]Effect, error) {
	perGroup, err := s.GroupPermissionsOfUsers(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	merged := map[string]Effect{}
	for _, m := range perGroup {
		for k, eff := range m {
			if eff == EffectDeny {
				merged[k] = EffectDeny
				continue
			}
			if _, ok := merged[k]; !ok {
				merged[k] = EffectAllow
			}
		}
	}
	return merged, nil
}

// SetGroupPermissions 全量替换某组的权限矩阵。
//
// 超管专属项在这里**被静默丢弃**，不是报错：界面上这两行应当只读，
// 万一是旧界面或手改库写进来的，静默丢弃比整次保存失败更好用
// （那样用户会以为权限没保存，转头把整个矩阵重填一遍）。
// 丢弃这件事由 TestSetGroupPermissionsDropsSuperOnly 钉住。
func (s *Store) SetGroupPermissions(ctx context.Context, groupID int64, perms []GroupPermission) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM rbac_group_permissions WHERE group_id=?`, groupID); err != nil {
		return err
	}
	for _, p := range perms {
		if IsSuperOnly(p.Key) || !IsKnownPermission(p.Key) {
			continue
		}
		eff, ok := ParseEffect(string(p.Effect))
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO rbac_group_permissions(group_id, permission_key, effect)
			VALUES(?, ?, ?)`, groupID, p.Key, string(eff)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- 用户级覆盖

// UserOverridesOf 返回某用户的覆盖表（按 key 升序）。
func (s *Store) UserOverridesOf(ctx context.Context, userID int64) (map[string]Effect, error) {
	if s.read == nil {
		return nil, errNoWriteHandle
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT permission_key, effect FROM rbac_user_overrides WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Effect{}
	for rows.Next() {
		var k, e string
		if err := rows.Scan(&k, &e); err != nil {
			return nil, err
		}
		if eff, ok := ParseEffect(e); ok {
			out[k] = eff
		}
	}
	return out, rows.Err()
}

// SetUserOverrides 全量替换某用户的覆盖表。
//
// 传入 map[string]string：值是空串表示「继承」，即删掉这行。
// 用 string 而不是 Effect 是为了让界面能直接把三态下拉的值原样传上来。
// 超管专属项同样被静默丢弃（理由同 SetGroupPermissions）。
func (s *Store) SetUserOverrides(ctx context.Context, userID int64, overrides map[string]string) error {
	if s.write == nil {
		return errNoWriteHandle
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM rbac_user_overrides WHERE user_id=?`, userID); err != nil {
		return err
	}
	for key, raw := range overrides {
		if IsSuperOnly(key) || !IsKnownPermission(key) {
			continue
		}
		eff, ok := ParseEffect(raw)
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO rbac_user_overrides(user_id, permission_key, effect)
			VALUES(?, ?, ?)`, userID, key, string(eff)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// isUniqueViolation 判断是不是 UNIQUE 约束冲突。
//
// 不引 sqlite 驱动的错误类型（那会把依赖钉死在具体驱动上），
// 退化成看错误文本：本仓只有 UNIQUE 冲突会命中这段，
// 且插入前已经用 FoldUsername 做过应用层预检，这里是第二道防线。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed")
}
