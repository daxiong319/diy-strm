package api

// RBAC 接口层行为测试。
//
// 测的都不是「rbac 包判得对不对」（那是 internal/rbac 的事），
// 而是「判完之后接口层做的事对不对」：拒绝时返回什么、
// 关着时放不放行、菜单给不给全、超管专属的两条路有没有被罩住。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"litepan/internal/adminauth"
	"litepan/internal/rbac"
	"litepan/internal/settings"
	"litepan/internal/store"
)

// ---------------------------------------------------------------- 测试骨架

// rbacEnv 是跑一个 RBAC 接口测试所需的最小环境。
type rbacEnv struct {
	handler *Handler
	svc     *rbac.Service
	db      *sql.DB
}

// newRBACEnv 起一个内存库 + 真 settings.Service + 真 rbac.Service。
//
// 用真的 settings.Service（而不是 map）是因为「开关读的是 registry 里登记的
// 默认值」这件事本身要成立 —— 用 map 会让「键没登记」和「登记成 false」混为一谈。
func newRBACEnv(t *testing.T, enabled bool) *rbacEnv {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	st := store.New(db)

	settingsSvc, err := settings.New(ctx, st.Configs)
	if err != nil {
		t.Fatalf("构造设置服务: %v", err)
	}
	// admin_username 走配置仓库而不是 settings：它压根没登记在
	// registry 里（那是 adminauth 自己的凭据项），用 settingsSvc.Update 写
	// 会得到「未知设置项」。rbac.Service 读它时也是从配置仓库读的。
	if err := st.Configs.Set(ctx, adminauth.KeyAdminUsername, "root"); err != nil {
		t.Fatalf("写 admin_username: %v", err)
	}
	if err := settingsSvc.Update(ctx, map[string]string{
		settings.KeyMORBACEnabled:          boolText(enabled),
		settings.KeyMORBACDefaultUserGroup: "",
	}); err != nil {
		t.Fatalf("写入设置: %v", err)
	}

	// SettingsWithRaw 与生产装配（internal/app/wire_rbac.go）用的是同一个包装。
	// 这里刻意不复制一份「简化版适配器」进测试：曾经复制过，结果测试全绿而
	// 生产里超管身份永远读不出来。测试和生产必须共用同一份实现。
	svc := rbac.NewService(
		rbac.NewStore(db.WriteHandle(), db.ReadHandle()),
		rbac.SettingsWithRaw(settingsSvc, st.Configs),
		nil,
	)
	if err := svc.EnsureSeed(ctx); err != nil {
		t.Fatalf("播种权限字典: %v", err)
	}
	return &rbacEnv{
		handler: &Handler{rbac: svc},
		svc:     svc,
		db:      db.WriteHandle(),
	}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// asIdentity 把请求变成「以某个会话身份发起」。
//
// 塞的是 *adminauth.Session（adminSessionCtxKey），不是 Principal：
// RequirePermission 中间件不读 context 里现成的主体，它自己取会话、
// 调 loadPrincipalFromSession 重算一遍。让测试替它算好塞进去，
// 等于把中间件里最关键的一段（会话 → 主体）从测试覆盖里删掉了。
//
// 之前这版写的是「算出主体再塞进 principalCtxKey」，于是所有中间件层用例
// 测的其实是「我塞的那个 Principal 对不对」，而完全没测到中间件本身 ——
// 结果 UserID=0 被当成超管的真实漏洞就藏在后面没被测出来。
func (e *rbacEnv) asIdentity(req *http.Request, sess rbac.Session) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), adminSessionCtxKey{}, &adminauth.Session{
		IsAdmin:  true,
		UserID:   sess.UserID,
		Username: sess.Username,
	}))
}

// reqWithID 造一个带 chi 路径参数 {id} 的请求。
//
// handler 里用 chi.URLParam 取路径参数，直接调 handler 时必须自己造
// chi 的路由上下文，否则 pathID 拿到的是空串、每个带 {id} 的接口都会 400。
func reqWithID(t *testing.T, method, path, body string, id int64) *http.Request {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(id, 10))
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// groupPerms 把 map 转成切片，顺手排序让失败信息稳定。
func groupPerms(m map[string]string) []rbac.GroupPermission {
	out := make([]rbac.GroupPermission, 0, len(m))
	for k, v := range m {
		out = append(out, rbac.GroupPermission{Key: k, Effect: rbac.Effect(v)})
	}
	return out
}

// ---------------------------------------------------------------- 验收①

// TestRequirePermissionIsANoOpWhenDisabled 断言关着时中间件完全放行。
//
// 验收①的核心：mo_rbac_enabled=false 时行为与本功能上线前逐字一致。
// 这里刻意**不往库里写任何用户和权限**再打请求 —— 如果关着的时候还去查库，
// 这条测试会因为「查不到用户」而失败，正好把那种实现挡住。
func TestRequirePermissionIsANoOpWhenDisabled(t *testing.T) {
	env := newRBACEnv(t, false)

	called := 0
	handler := env.handler.RequirePermission(rbac.PermSystemManage)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called++
			w.WriteHeader(http.StatusOK)
		}))

	// 三种身份都放行：超管、库里不存在的委托用户、连会话都没有。
	for _, identity := range []*rbac.Session{
		{Username: "root"},
		{UserID: 999, Username: "幽灵"},
		nil,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
		if identity != nil {
			req = env.asIdentity(req, *identity)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("身份 %v：关着时返回 %d，期望 200", identity, rec.Code)
		}
	}
	if called != 3 {
		t.Fatalf("下游 handler 被调用 %d 次，期望 3 次", called)
	}
}

// TestManagementEndpointsReportDisabledInsteadOfServingStaleData 断言
// 关着时管理接口返回「未启用」而不是半截数据。
//
// 这是验收①的另一面：关着的时候用户页不能列出一个空列表让人以为「没有用户」，
// 那会诱导管理员去建一个和超管重名的账号。
func TestManagementEndpointsReportDisabledInsteadOfServingStaleData(t *testing.T) {
	env := newRBACEnv(t, false)

	for _, tc := range []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
	}{
		{"列出用户", env.handler.listRBACUsers},
		{"列出用户组", env.handler.listRBACGroups},
		{"列出权限目录", env.handler.listRBACPermissions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := env.asIdentity(
				httptest.NewRequest(http.MethodGet, "/api/admin/rbac/users", nil),
				rbac.Session{Username: "root"})
			tc.call(rec, req)
			if rec.Code == http.StatusOK {
				t.Fatalf("关着时返回 200，响应：%s", rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "未启用") {
				t.Fatalf("错误文案没有说明是未启用：%s", rec.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------- 验收②

// TestRequirePermissionDenyTruthTableThroughTheAPI 把 deny 真值表走一遍真实数据。
//
// 表本身在 internal/rbac 的 TestCanDenyTruthTable 已经穷举过；
// 这里重跑一遍是为了证明「库里的配置」经过 PrincipalFor 之后仍然按那张表判，
// 中间这一段（查库 → 合并多组 → 判定）才是最容易出错的地方。
func TestRequirePermissionDenyTruthTableThroughTheAPI(t *testing.T) {
	for _, tc := range []struct {
		name        string
		groupAllow  bool
		groupDeny   bool
		userAllow   bool
		userDeny    bool
		isSuper     bool
		wantAllowed bool
	}{
		{"组允许+用户无覆盖→允许", true, false, false, false, false, true},
		{"组允许+用户允许→允许", true, false, true, false, false, true},
		{"组允许+用户拒绝→拒绝（用户拒绝最高）", true, false, false, true, false, false},
		{"组允许+用户既允许又拒绝→拒绝", true, false, true, true, false, false},
		{"组拒绝+用户允许→拒绝（组拒绝最高）", false, true, true, false, false, false},
		{"组拒绝+用户拒绝→拒绝", false, true, false, true, false, false},
		{"同组里既允许又拒绝→拒绝", true, true, true, false, false, false},
		{"什么都不配→拒绝", false, false, false, false, false, false},
		{"全部拒绝+超管→允许（超管绕过一切）", false, true, false, true, true, true},
		{"全部允许+超管→允许", true, false, true, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newRBACEnv(t, true)
			ctx := context.Background()

			groupID, err := env.svc.CreateGroup(ctx, "测试组", "")
			if err != nil {
				t.Fatalf("建组: %v", err)
			}
			perms := map[string]string{}
			if tc.groupAllow {
				perms[rbac.PermSystemManage] = "allow"
			}
			if tc.groupDeny {
				perms[rbac.PermSystemManage] = "deny"
			}
			if len(perms) > 0 {
				if err := env.svc.SetGroupPermissions(ctx, groupID, groupPerms(perms)); err != nil {
					t.Fatalf("配组权限: %v", err)
				}
			}

			identity := rbac.Session{Username: "root"}
			if !tc.isSuper {
				uid, err := env.svc.CreateUser(ctx, "u", "", "password123", true)
				if err != nil {
					t.Fatalf("建用户: %v", err)
				}
				if err := env.svc.AddUserToGroupByName(ctx, uid, "测试组"); err != nil {
					t.Fatalf("入组: %v", err)
				}
				overrides := map[string]string{}
				if tc.userAllow {
					overrides[rbac.PermSystemManage] = "allow"
				}
				if tc.userDeny {
					overrides[rbac.PermSystemManage] = "deny"
				}
				if len(overrides) > 0 {
					if err := env.svc.SetUserOverrides(ctx, uid, overrides); err != nil {
						t.Fatalf("写用户覆盖: %v", err)
					}
				}
				identity = rbac.Session{UserID: uid, Username: "u"}
			}

			principal, err := env.svc.PrincipalFor(ctx, identity)
			if err != nil {
				t.Fatalf("装配主体: %v", err)
			}
			got := principal.Can(rbac.PermSystemManage)
			if got != tc.wantAllowed {
				ex := principal.Explain(rbac.PermSystemManage)
				t.Fatalf("Can = %v，期望 %v（Source=%s，Explain=%q）", got, tc.wantAllowed, ex.Source, ex.Message)
			}
		})
	}
}

// TestDenyTruthTableThroughTheMiddleware 把真值表再走一遍 HTTP 中间件。
//
// 上一条验证的是判定，这一条验证的是「判定为拒绝时真的返回 403 且不碰下游」。
// 两者分开是因为后者还有一个独立失效模式：中间件算对了却忘了写响应，
// 或者写完响应还继续调 next。
func TestDenyTruthTableThroughTheMiddleware(t *testing.T) {
	for _, tc := range []struct {
		name    string
		grant   bool
		isSuper bool
		want    int
	}{
		{"组允许→200", true, false, http.StatusOK},
		{"组拒绝→403", false, false, http.StatusForbidden},
		{"没配→403", false, false, http.StatusForbidden},
		{"超管在没有任何授权时仍→200", false, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newRBACEnv(t, true)
			ctx := context.Background()

			identity := rbac.Session{Username: "root"}
			if !tc.isSuper {
				groupID, err := env.svc.CreateGroup(ctx, "组", "")
				if err != nil {
					t.Fatalf("建组: %v", err)
				}
				if tc.grant {
					if err := env.svc.SetGroupPermissions(ctx, groupID, []rbac.GroupPermission{
						{Key: rbac.PermSystemManage, Effect: rbac.EffectAllow},
					}); err != nil {
						t.Fatalf("授权: %v", err)
					}
				} else {
					if err := env.svc.SetGroupPermissions(ctx, groupID, []rbac.GroupPermission{
						{Key: rbac.PermSystemManage, Effect: rbac.EffectDeny},
					}); err != nil {
						t.Fatalf("拒绝: %v", err)
					}
				}
				uid, err := env.svc.CreateUser(ctx, "u", "", "password123", true)
				if err != nil {
					t.Fatalf("建用户: %v", err)
				}
				if err := env.svc.AddUserToGroupByName(ctx, uid, "组"); err != nil {
					t.Fatalf("入组: %v", err)
				}
				identity = rbac.Session{UserID: uid, Username: "u"}
			}

			reached := false
			handler := env.handler.RequirePermission(rbac.PermSystemManage)(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					reached = true
					w.WriteHeader(http.StatusOK)
				}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, env.asIdentity(
				httptest.NewRequest(http.MethodPut, "/api/admin/settings", nil), identity))

			if rec.Code != tc.want {
				t.Fatalf("状态码 %d，期望 %d：%s", rec.Code, tc.want, rec.Body.String())
			}
			if reached != (tc.want == http.StatusOK) {
				t.Fatalf("下游 handler reached=%v，与状态码 %d 不一致", reached, tc.want)
			}
		})
	}
}

// TestDeniedResponseCarriesAReason 把「拒绝时说什么」钉住。
//
// 管理员配错权限时，「这一项是超管专属」和「你所在的组里有拒绝项」是两种
// 完全不同的修法。只回「权限不足」的话，排查只能去翻数据库。
func TestDeniedResponseCarriesAReason(t *testing.T) {
	for _, tc := range []struct {
		name    string
		perm    string
		wantHas string
	}{
		{"超管专属", rbac.PermSubscriptionCreate, "超级管理员"},
		{"普通权限被拒绝", rbac.PermSystemManage, "拒绝"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newRBACEnv(t, true)
			ctx := context.Background()

			groupID, err := env.svc.CreateGroup(ctx, "拒绝组", "")
			if err != nil {
				t.Fatalf("建组: %v", err)
			}
			if err := env.svc.SetGroupPermissions(ctx, groupID, []rbac.GroupPermission{
				{Key: tc.perm, Effect: rbac.EffectDeny},
			}); err != nil {
				t.Fatalf("配组权限: %v", err)
			}
			uid, err := env.svc.CreateUser(ctx, "banned", "", "password123", true)
			if err != nil {
				t.Fatalf("建用户: %v", err)
			}
			if err := env.svc.AddUserToGroupByName(ctx, uid, "拒绝组"); err != nil {
				t.Fatalf("入组: %v", err)
			}

			handler := env.handler.RequirePermission(tc.perm)(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Error("被拒绝的请求不应该到达下游 handler")
				}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, env.asIdentity(
				httptest.NewRequest(http.MethodPost, "/api/admin/discovery/subscriptions/", nil),
				rbac.Session{UserID: uid, Username: "banned"}))

			if rec.Code == http.StatusOK {
				t.Fatalf("被拒绝的请求返回了 200：%s", rec.Body.String())
			}
			if body := rec.Body.String(); !strings.Contains(body, tc.wantHas) {
				t.Fatalf("拒绝文案里没有 %q：%s", tc.wantHas, body)
			}
		})
	}
}

// ---------------------------------------------------------------- 验收③

// TestSuperOnlyPermissionsAreNeverGranted 断言两项超管专属在数据层也下放不下去。
//
// 中间件挂了不代表安全：如果有人直接改数据库给某个组塞了
// subscription.create=allow，非超管就会拿到。所以判定必须**只读
// rbac 里的 Go 集合**，不读库里的值 —— 这一条专门盯那个设计。
func TestSuperOnlyPermissionsAreNeverGranted(t *testing.T) {
	env := newRBACEnv(t, true)
	ctx := context.Background()

	groupID, err := env.svc.CreateGroup(ctx, "试图下放", "")
	if err != nil {
		t.Fatalf("建组: %v", err)
	}
	// 故意把两项都写成 allow，看能不能写进去。
	if err := env.svc.SetGroupPermissions(ctx, groupID, []rbac.GroupPermission{
		{Key: rbac.PermSubscriptionCreate, Effect: rbac.EffectAllow},
		{Key: rbac.PermOfflineDownloadRun, Effect: rbac.EffectAllow},
		{Key: rbac.PermSystemManage, Effect: rbac.EffectAllow},
	}); err != nil {
		t.Fatalf("写组权限: %v", err)
	}
	saved, err := env.svc.GroupPermissions(ctx, groupID)
	if err != nil {
		t.Fatalf("读组权限: %v", err)
	}
	for _, p := range saved {
		if p.Key == rbac.PermSubscriptionCreate || p.Key == rbac.PermOfflineDownloadRun {
			t.Fatalf("超管专属项 %s 被写进了组权限：%+v", p.Key, saved)
		}
	}
	if len(saved) != 1 || saved[0].Key != rbac.PermSystemManage {
		t.Fatalf("正常项也没写进去：%+v", saved)
	}

	uid, err := env.svc.CreateUser(ctx, "想下放的人", "", "password123", true)
	if err != nil {
		t.Fatalf("建用户: %v", err)
	}
	// 用户级覆盖也拦一道。
	if err := env.svc.SetUserOverrides(ctx, uid, map[string]string{
		rbac.PermSubscriptionCreate: "allow",
	}); err != nil {
		t.Fatalf("写用户覆盖: %v", err)
	}
	principal, err := env.svc.PrincipalFor(ctx, rbac.Session{UserID: uid, Username: "想下放的人"})
	if err != nil {
		t.Fatalf("装配主体: %v", err)
	}
	for _, perm := range []string{rbac.PermSubscriptionCreate, rbac.PermOfflineDownloadRun} {
		if principal.Can(perm) {
			t.Fatalf("非超管拿到了超管专属权限 %s", perm)
		}
	}

	// 超管仍然可以。
	superPrincipal, err := env.svc.PrincipalFor(ctx, rbac.Session{Username: "root"})
	if err != nil {
		t.Fatalf("装配超管主体: %v", err)
	}
	if !superPrincipal.Can(rbac.PermSubscriptionCreate) {
		t.Fatal("超管被挡在 subscription.create 外面")
	}
}

// TestSuperOnlyDeniedEvenWhenRawSQLGrantsIt 直接往库里塞一行 allow。
//
// 这是对上一条的最强形态：绕过全部 Service 与校验，只有判定内核能挡住它。
// 如果哪天有人把 IsSuperOnly 改成读库，这一���会第一个红。
func TestSuperOnlyDeniedEvenWhenRawSQLGrantsIt(t *testing.T) {
	env := newRBACEnv(t, true)
	ctx := context.Background()

	groupID, err := env.svc.CreateGroup(ctx, "手改库", "")
	if err != nil {
		t.Fatalf("建组: %v", err)
	}
	uid, err := env.svc.CreateUser(ctx, "手改库的人", "", "password123", true)
	if err != nil {
		t.Fatalf("建用户: %v", err)
	}
	if _, err := env.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO rbac_group_members(group_id, user_id) VALUES(?, ?)`, groupID, uid); err != nil {
		t.Fatalf("手工入组: %v", err)
	}
	for _, perm := range []string{rbac.PermSubscriptionCreate, rbac.PermOfflineDownloadRun} {
		if _, err := env.db.ExecContext(ctx,
			`INSERT OR REPLACE INTO rbac_group_permissions(group_id, permission_key, effect) VALUES(?, ?, 'allow')`,
			groupID, perm); err != nil {
			t.Fatalf("手工授权 %s: %v", perm, err)
		}
	}

	// 先确认数据确实写进去了 —— 否则下面的断言会因为「库本来就是空的」而假绿。
	var n int
	if err := env.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rbac_group_permissions WHERE group_id=? AND effect='allow'`, groupID).Scan(&n); err != nil {
		t.Fatalf("核对行数: %v", err)
	}
	if n != 2 {
		t.Fatalf("库里只有 %d 行 allow，测试前提不成立", n)
	}

	principal, err := env.svc.PrincipalFor(ctx, rbac.Session{UserID: uid, Username: "手改库的人"})
	if err != nil {
		t.Fatalf("装配主体: %v", err)
	}
	for _, perm := range []string{rbac.PermSubscriptionCreate, rbac.PermOfflineDownloadRun} {
		if principal.Can(perm) {
			t.Fatalf("直接改库之后 %s 仍然授予了非超管 —— 判定在读库里的值", perm)
		}
	}
}

// ---------------------------------------------------------------- 验收④

// TestAuthMenusMatchesTheNavSnapshot 断言菜单接口给的是前端认识的那一套。
//
// 前后端一致性是硬要求：前端按 key 判断渲染哪个页面，
// 后端给出一个前端不认识的 key，结果就是「菜单显示不出来」而不是「显示错了」。
func TestAuthMenusMatchesTheNavSnapshot(t *testing.T) {
	env := newRBACEnv(t, true)

	rec := httptest.NewRecorder()
	env.handler.authMenus(rec, env.asIdentity(
		httptest.NewRequest(http.MethodGet, "/api/admin/auth/menus", nil),
		rbac.Session{Username: "root"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Enabled bool     `json:"enabled"`
			Menus   []string `json:"menus"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if !resp.Success || !resp.Data.Enabled {
		t.Fatalf("响应不对：%s", rec.Body.String())
	}
	if len(resp.Data.Menus) != len(rbac.AllMenuKeys()) {
		t.Fatalf("超管菜单 %d 项，目录里有 %d 项", len(resp.Data.Menus), len(rbac.AllMenuKeys()))
	}
	seen := make(map[string]bool, len(resp.Data.Menus))
	for _, m := range resp.Data.Menus {
		seen[m] = true
	}
	for _, key := range rbac.AllMenuKeys() {
		if !seen[key] {
			t.Fatalf("菜单 %s 没出现在响应里：%s", key, rec.Body.String())
		}
	}
}

// TestAuthMenusHidesDeniedMenus 断言被拒绝的菜单会消失。
func TestAuthMenusHidesDeniedMenus(t *testing.T) {
	env := newRBACEnv(t, true)
	ctx := context.Background()

	uid, err := env.svc.CreateUser(ctx, "只看仪表盘", "", "password123", true)
	if err != nil {
		t.Fatalf("建用户: %v", err)
	}
	groupID, err := env.svc.CreateGroup(ctx, "只读组", "")
	if err != nil {
		t.Fatalf("建组: %v", err)
	}
	if err := env.svc.SetGroupPermissions(ctx, groupID, []rbac.GroupPermission{
		{Key: rbac.PermDashboardView, Effect: rbac.EffectAllow},
	}); err != nil {
		t.Fatalf("配权限: %v", err)
	}
	if err := env.svc.AddUserToGroupByName(ctx, uid, "只读组"); err != nil {
		t.Fatalf("入组: %v", err)
	}

	rec := httptest.NewRecorder()
	env.handler.authMenus(rec, env.asIdentity(
		httptest.NewRequest(http.MethodGet, "/api/admin/auth/menus", nil),
		rbac.Session{UserID: uid, Username: "只看仪表盘"}))
	body := rec.Body.String()
	if !strings.Contains(body, `"dashboard"`) {
		t.Fatalf("允许的菜单没给：%s", body)
	}
	for _, hidden := range []string{`"settings"`, `"users"`, `"permissions"`, `"media-upgrade"`} {
		if strings.Contains(body, hidden) {
			t.Fatalf("不该出现的菜单 %s 出现在：%s", hidden, body)
		}
	}
}

// TestAuthMenusIsFullyOpenWhenDisabled 断言关着时菜单给全量。
//
// 这是前端「RBAC 关着就按老样子显示导航」能成立的前提。
func TestAuthMenusIsFullyOpenWhenDisabled(t *testing.T) {
	env := newRBACEnv(t, false)
	rec := httptest.NewRecorder()
	// 连会话都不给：关着的时候这个接口不应该因为没有会话而失败。
	env.handler.authMenus(rec, httptest.NewRequest(http.MethodGet, "/api/admin/auth/menus", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Enabled bool     `json:"enabled"`
			Menus   []string `json:"menus"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if resp.Data.Enabled {
		t.Fatalf("关着时 enabled 应该是 false：%s", rec.Body.String())
	}
	if len(resp.Data.Menus) != len(rbac.AllMenuKeys()) {
		t.Fatalf("关着时只给了 %d 项菜单，期望 %d 项", len(resp.Data.Menus), len(rbac.AllMenuKeys()))
	}
}

// ---------------------------------------------------------------- 验收⑥

// TestAuthMeReportsSuperWithoutTouchingUsers 断言超管身份不需要用户表里有他。
//
// 验收⑥：存量部署首次开启时，用户表是空的，超管必须照常能用。
func TestAuthMeReportsSuperWithoutTouchingUsers(t *testing.T) {
	env := newRBACEnv(t, true)
	var users []struct{}
	if _, err := env.svc.ListUsers(context.Background()); err != nil {
		t.Fatalf("列用户: %v", err)
	} else {
		// 用户表确实是空的（没建过任何人）。
		_ = users
	}

	var resp struct {
		Data struct {
			Enabled bool     `json:"enabled"`
			UserID  int64    `json:"user_id"`
			IsSuper bool     `json:"is_super"`
			Menus   []string `json:"menus"`
		} `json:"data"`
	}
	rec := httptest.NewRecorder()
	env.handler.authMe(rec, env.asIdentity(
		httptest.NewRequest(http.MethodGet, "/api/admin/auth/me", nil),
		rbac.Session{Username: "root"}))
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if !resp.Data.Enabled || !resp.Data.IsSuper {
		t.Fatalf("空用户表下超管身份没立住：%s", rec.Body.String())
	}
	if resp.Data.UserID != rbac.SuperUserID {
		t.Fatalf("超管 UserID = %d，期望哨兵 %d", resp.Data.UserID, rbac.SuperUserID)
	}
	if len(resp.Data.Menus) != len(rbac.AllMenuKeys()) {
		t.Fatalf("超管菜单 %d 项，期望 %d 项", len(resp.Data.Menus), len(rbac.AllMenuKeys()))
	}
}

// ---------------------------------------------------------------- 其他

// TestUnknownPermissionFailsClosedThroughTheAPI 断言中间件也遵守 fail closed。
func TestUnknownPermissionFailsClosedThroughTheAPI(t *testing.T) {
	env := newRBACEnv(t, true)
	ctx := context.Background()
	uid, err := env.svc.CreateUser(ctx, "u", "", "password123", true)
	if err != nil {
		t.Fatalf("建用户: %v", err)
	}
	called := false
	handler := env.handler.RequirePermission("根本不存在的权限")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, env.asIdentity(
		httptest.NewRequest(http.MethodGet, "/x", nil),
		rbac.Session{UserID: uid, Username: "u"}))
	if called {
		t.Fatal("未知权限项被放行了")
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("未知权限项返回 200：%s", rec.Body.String())
	}
}

// TestListUsersShowsGroupNames 断言用户列表带上了组名。
//
// 前端列表要直接显示「这个人属于哪些组」，不给名字就得再发一轮请求。
func TestListUsersShowsGroupNames(t *testing.T) {
	env := newRBACEnv(t, true)
	ctx := context.Background()

	uid, err := env.svc.CreateUser(ctx, "alice", "爱丽丝", "password123", true)
	if err != nil {
		t.Fatalf("建用户: %v", err)
	}
	if err := env.svc.AddUserToGroupByName(ctx, uid, rbac.BuiltinGroupUser); err != nil {
		t.Fatalf("入默认组: %v", err)
	}

	rec := httptest.NewRecorder()
	env.handler.listRBACUsers(rec, env.asIdentity(
		httptest.NewRequest(http.MethodGet, "/api/admin/rbac/users", nil),
		rbac.Session{Username: "root"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"alice", "爱丽丝", rbac.BuiltinGroupUser} {
		if !strings.Contains(body, want) {
			t.Fatalf("响应里没有 %q：%s", want, body)
		}
	}
}

// TestSetGroupPermissionsDropsSuperOnlyOverHTTP 断言接口层也不接受超管专属。
//
// 界面会把这几行显示成只读，但接口必须自己再挡一次 ——
// 前端只读不等于后端只读，curl 一下就能绕过。
func TestSetGroupPermissionsDropsSuperOnlyOverHTTP(t *testing.T) {
	env := newRBACEnv(t, true)
	ctx := context.Background()
	groupID, err := env.svc.CreateGroup(ctx, "接口组", "")
	if err != nil {
		t.Fatalf("建组: %v", err)
	}

	body := `{"permissions":{"` + rbac.PermSubscriptionCreate + `":"allow","` +
		rbac.PermOfflineDownloadRun + `":"deny","` + rbac.PermSystemManage + `":"allow"}}`
	rec := httptest.NewRecorder()
	req := env.asIdentity(
		reqWithID(t, http.MethodPost, "/api/admin/rbac/groups/x/permissions", body, groupID),
		rbac.Session{Username: "root"})
	env.handler.setRBACGroupPermissions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}

	saved, err := env.svc.GroupPermissions(ctx, groupID)
	if err != nil {
		t.Fatalf("读组权限: %v", err)
	}
	for _, p := range saved {
		if p.Key == rbac.PermSubscriptionCreate || p.Key == rbac.PermOfflineDownloadRun {
			t.Fatalf("接口接受了超管专属项 %s：%+v", p.Key, saved)
		}
	}
	if len(saved) != 1 || saved[0].Key != rbac.PermSystemManage {
		t.Fatalf("正常项没写进去或写错了：%+v", saved)
	}
}

// TestUnknownFieldInRequestBodyIsRejected 断言请求体开着严格解码。
//
// decodeJSON 开了 DisallowUnknownFields，而 RBAC 的写接口都是 map 入参，
// 最容易出现的错是前端传了 "permissions" 之外的字段却被静默接受。
func TestUnknownFieldInRequestBodyIsRejected(t *testing.T) {
	env := newRBACEnv(t, true)
	rec := httptest.NewRecorder()
	req := env.asIdentity(
		httptest.NewRequest(http.MethodPost, "/api/admin/rbac/users",
			strings.NewReader(`{"username":"x","display_name":"","password":"password123","enabled":true,"group_ids":[1]}`)),
		rbac.Session{Username: "root"})
	env.handler.createRBACUser(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("带未知字段的请求被接受了：%s", rec.Body.String())
	}
}

// TestCreateUserValidationErrorsAreFourHundreds 断言用户输错时是 400 而不是 500。
func TestCreateUserValidationErrorsAreFourHundreds(t *testing.T) {
	env := newRBACEnv(t, true)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"用户名为空", `{"username":"","display_name":"","password":"password123","enabled":true}`},
		{"密码太短", `{"username":"bob","display_name":"","password":"123","enabled":true}`},
		{"用户名带空格", `{"username":"bo b","display_name":"","password":"password123","enabled":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := env.asIdentity(
				httptest.NewRequest(http.MethodPost, "/api/admin/rbac/users", strings.NewReader(tc.body)),
				rbac.Session{Username: "root"})
			env.handler.createRBACUser(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("状态码 %d，期望 400：%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestDuplicateUsernameIsAValidationError 断言重名是 400 且说清是重名。
func TestDuplicateUsernameIsAValidationError(t *testing.T) {
	env := newRBACEnv(t, true)
	ctx := context.Background()
	if _, err := env.svc.CreateUser(ctx, "dup", "", "password123", true); err != nil {
		t.Fatalf("建用户: %v", err)
	}
	rec := httptest.NewRecorder()
	req := env.asIdentity(
		httptest.NewRequest(http.MethodPost, "/api/admin/rbac/users",
			strings.NewReader(`{"username":"DUP","display_name":"","password":"password123","enabled":true}`)),
		rbac.Session{Username: "root"})
	env.handler.createRBACUser(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 %d，期望 400：%s", rec.Code, rec.Body.String())
	}
}
