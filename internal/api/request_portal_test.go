package api

// 求片站（独立端口那一侧）的接口层行为测试。
//
// 测的是三件在 mediarequest 包里测不到的事：
//
//  1. 凭据是真的过 adminauth.Authenticate，cookie 是求片站自己签的；
//  2. 限流错误在 HTTP 上是 **429** 而不是 400（前端据此提示「明天再来」）；
//  3. 管理台的接口在求片站上**根本不存在**（404，而不是 403/503/200）。
//
// 第 3 条是这轮最花力气的一条：中间件写错一点点，
// 「求片站没有后台接口」就从结构性质退化成一句口头承诺。

import (
	"context"
	"encoding/json"

	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"litepan/internal/adminauth"
	"litepan/internal/discover/discovery"
	"litepan/internal/mediarequest"
	"litepan/internal/rbac"
	"litepan/internal/settings"
	"litepan/internal/store"
)

// ---------------------------------------------------------------- 测试骨架

type portalEnv struct {
	t      *testing.T
	router http.Handler
	svc    *mediarequest.Service
	subs   *portalSaver
	// deps 复用同一套装配，让管理台那侧的测试（request_center_test.go）
	// 能拿它拼出一棵**只挂求片中心路由**的 chi 树，不必启动整个后端。
	deps Deps
}

// portalSaver 记录建订阅的入参。
type portalSaver struct {
	calls int
	last  *discovery.SubscriptionUpsertPayload
}

func (s *portalSaver) SaveSubscription(p *discovery.SubscriptionUpsertPayload) (*discovery.DiscoverySubscription, string, error) {
	s.calls++
	s.last = p
	return &discovery.DiscoverySubscription{ID: 42, Title: p.Title}, "", nil
}

// portalItems 固定「东西还没入库」。
type portalItems struct{}

func (portalItems) ListSubscriptionItems(uint, string, int) ([]discovery.DiscoverySubscriptionItem, error) {
	return nil, nil
}

// portalSearcher 返回一条固定的搜索结果。
type portalSearcher struct{}

func (portalSearcher) SearchMedia(context.Context, string, string, int, bool) (*discovery.ActorsPage, error) {
	return &discovery.ActorsPage{Items: []discovery.Item{
		{TMDBID: 900, Title: "孤独摇滚", MediaType: "tv", Year: 2022},
	}}, nil
}

// portalExtraUserAuth 是 internal/app/wire_rbac.go 里 rbacExtraUserAuth 的测试侧等价物。
//
// 为什么不能直接复用生产那个：它住在 internal/app，而 internal/api 不能 import
// internal/app（那边 import 这边，会成环）。所以这里按同一份契约重写一遍 ——
// IsAdmin 必须是 true，否则委托用户登录成功了却过不了 requireAdmin 这层会话鉴权。
type portalExtraUserAuth struct{ svc *rbac.Service }

func (a portalExtraUserAuth) Enabled(ctx context.Context) bool {
	return a.svc != nil && a.svc.Enabled(ctx)
}

func (a portalExtraUserAuth) Authenticate(ctx context.Context, username, password string) (adminauth.Session, bool, error) {
	if a.svc == nil {
		return adminauth.Session{}, false, nil
	}
	sess, ok, err := a.svc.Authenticate(ctx, username, password)
	if err != nil || !ok {
		return adminauth.Session{}, false, err
	}
	return adminauth.Session{IsAdmin: true, Username: sess.Username, UserID: sess.UserID}, true, nil
}

type portalCfg map[string]any

func (c portalCfg) String(string) string { return "" }
func (c portalCfg) Bool(k string) bool {
	v, _ := c[k].(bool)
	return v
}
func (c portalCfg) Int(k string) int {
	v, _ := c[k].(int)
	return v
}

func newPortalEnv(t *testing.T, tweak func(portalCfg)) *portalEnv {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "portal.db")})
	if err != nil {
		t.Fatalf("打开测试库: %v", err)
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
	if err := st.Configs.Set(ctx, adminauth.KeyAdminUsername, "root"); err != nil {
		t.Fatalf("写 admin_username: %v", err)
	}
	if err := settingsSvc.Update(ctx, map[string]string{
		settings.KeyMORBACEnabled:               "true",
		settings.KeyMOMediaRequestEnabled:       "true",
		settings.KeyMOMediaRequestRequireReview: "true",
		settings.KeyMOMediaRequestDailyLimit:    "5",
		settings.KeyMOMediaRequestPort:          "7812",
		settings.KeyMOMediaRequestTagMaxPerUser: "20",
		settings.KeyMOMediaRequestTagMaxLength:  "100",
	}); err != nil {
		t.Fatalf("写入设置: %v", err)
	}

	rbacSvc := rbac.NewService(
		rbac.NewStore(db.WriteHandle(), db.ReadHandle()),
		rbac.SettingsWithRaw(settingsSvc, st.Configs),
		nil,
	)
	if err := rbacSvc.EnsureSeed(ctx); err != nil {
		t.Fatalf("播种权限字典: %v", err)
	}
	// 家人账号：有「提交求片」，没有「审核求片」。
	familyID, err := rbacSvc.CreateUser(ctx, "xiaoming", "小明", "pw-xiaoming-1", true)
	if err != nil {
		t.Fatalf("建家人账号: %v", err)
	}
	if err := rbacSvc.SetUserOverrides(ctx, familyID, map[string]string{
		rbac.PermRequestSubmit: string(rbac.EffectAllow),
	}); err != nil {
		t.Fatalf("授权: %v", err)
	}
	// 运维账号：什么求片权限都没有 —— 用来验证登录时被挡下。
	if _, err := rbacSvc.CreateUser(ctx, "ops", "运维", "pw-ops-12345", true); err != nil {
		t.Fatalf("建运维账号: %v", err)
	}

	cfg := portalCfg{
		settings.KeyMOMediaRequestEnabled:       true,
		settings.KeyMOMediaRequestRequireReview: true,
		settings.KeyMOMediaRequestDailyLimit:    5,
		settings.KeyMOMediaRequestTagMaxPerUser: 20,
		settings.KeyMOMediaRequestTagMaxLength:  100,
	}
	if tweak != nil {
		tweak(cfg)
	}

	subs := &portalSaver{}
	svc := mediarequest.NewService(mediarequest.Params{
		Store:  mediarequest.NewStore(db.WriteHandle(), db.ReadHandle()),
		Cfg:    cfg,
		Subs:   subs,
		Items:  portalItems{},
		Search: portalSearcher{},
	})

	adminAuthSvc := adminauth.New(st.Configs, []byte("portal-test-secret-32-bytes!!"), nil)
	// 少了这一步，委托用户（xiaoming）在 adminauth 眼里压根不存在 ——
	// 生产里对应 internal/app/wire_rbac.go 的 bindRBAC。
	adminAuthSvc.SetExtraUserAuth(portalExtraUserAuth{svc: rbacSvc})

	d := Deps{
		Settings:  settingsSvc,
		AdminAuth: adminAuthSvc,
		RBAC:      rbacSvc,
	}
	d.MediaRequest = svc
	d.RequestSigner = mediarequest.NewSessionSigner([]byte("portal-test-secret-32-bytes!!"))
	return &portalEnv{t: t, router: NewRequestPortalRouter(d), svc: svc, subs: subs, deps: d}
}

// jsonOf 解包 writeOK 的 {"data": ...} 信封。
func jsonOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Code int            `json:"code"`
		Msg  string         `json:"message"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败 %q：%v", rec.Body.String(), err)
	}
	return env.Data
}

// do 打一发请求，可选地带 cookie jar。
func (e *portalEnv) do(method, path, body string, jar *http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if jar != nil {
		req.AddCookie(jar)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *portalEnv) login(username, password string) (*http.Cookie, *httptest.ResponseRecorder) {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/api/login",
		`{"username":"`+username+`","password":"`+password+`"}`, nil)
	if rec.Code != http.StatusOK {
		return nil, rec
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == mediarequest.PortalCookieName {
			return c, rec
		}
	}
	e.t.Fatalf("登录成功但没拿到 %s cookie：%s", mediarequest.PortalCookieName, rec.Body.String())
	return nil, rec
}

// ---------------------------------------------------------------- 登录

func TestPortalLoginIssuesItsOwnCookie(t *testing.T) {
	env := newPortalEnv(t, nil)
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	if cookie == nil {
		t.Fatal("没拿到求片站 cookie")
	}
	if cookie.Name != mediarequest.PortalCookieName {
		t.Fatalf("cookie 名 = %q", cookie.Name)
	}
	// 与管理台的 cookie 必须分开：浏览器会把两个端口的 cookie 一起带上，
	// 共用一个名字的话求片站会把管理台会话当成自己的。
	// adminauth.cookieName 是私有的，这里用字面量钉住这条性质：
	// 一旦两边同名，浏览器会把两个端口的 cookie 一起带上，
	// 求片站就会把管理台会话当成自己的（或反过来）。
	if cookie.Name == "admin_session" {
		t.Fatalf("求片站不能和管理台共用 cookie 名：%q", cookie.Name)
	}
	if cookie.Path != "/" {
		t.Logf("cookie path = %q（不影响正确性，只是记一下）", cookie.Path)
	}
}

func TestPortalLoginRejectsBadPassword(t *testing.T) {
	env := newPortalEnv(t, nil)
	_, rec := env.login("xiaoming", "错的密码")
	if rec.Code == http.StatusOK {
		t.Fatalf("错密码却登录成功：%s", rec.Body.String())
	}
}

func TestPortalLoginRefusesAccountWithoutRequestPermission(t *testing.T) {
	env := newPortalEnv(t, nil)
	_, rec := env.login("ops", "pw-ops-12345")
	if rec.Code == http.StatusOK {
		t.Fatalf("没有求片权限的账号却登录成功：%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "求片权限") {
		t.Fatalf("错误文案没说明原因：%s", rec.Body.String())
	}
}

func TestPortalMeRequiresSession(t *testing.T) {
	env := newPortalEnv(t, nil)
	rec := env.do(http.MethodGet, "/api/me", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录访问 /api/me 返回 %d，期望 401", rec.Code)
	}
}

// TestPortalSessionIsNotAdminSession 钉住 cookie 不通用这条性质：
// 管理台的 cookie 拿到求片站上必须无效。
func TestPortalSessionIsNotAdminSession(t *testing.T) {
	env := newPortalEnv(t, nil)
	rec := env.do(http.MethodGet, "/api/me", "",
		&http.Cookie{Name: "admin_session", Value: "not-a-real-session-token"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("拿管理台 cookie 就能进求片站：%d", rec.Code)
	}
}

func TestPortalMeReturnsLimitsFromServer(t *testing.T) {
	env := newPortalEnv(t, nil)
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	rec := env.do(http.MethodGet, "/api/me", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/me 返回 %d：%s", rec.Code, rec.Body.String())
	}
	dto := jsonOf(t, rec)
	if dto["username"] != "xiaoming" {
		t.Fatalf("username = %v", dto["username"])
	}
	// 额度必须来自服务端；前端不许自己算。
	if got := dto["daily_limit"]; got != float64(5) {
		t.Fatalf("daily_limit = %v，期望 5", got)
	}
	if dto["require_review"] != true {
		t.Fatalf("require_review = %v", dto["require_review"])
	}
	if got := dto["tag_max_per_user"]; got != float64(20) {
		t.Fatalf("tag_max_per_user = %v", got)
	}
}

// ---------------------------------------------------------------- 提交

func TestPortalSubmitAndMine(t *testing.T) {
	env := newPortalEnv(t, nil)
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")

	rec := env.do(http.MethodPost, "/api/request",
		`{"tmdb_id":900,"title":"孤独摇滚","media_type":"tv","tags":["4K"]}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("提交返回 %d：%s", rec.Code, rec.Body.String())
	}
	if env.subs.calls != 0 {
		t.Fatalf("开着审核时不该建订阅，实际建了 %d 次", env.subs.calls)
	}

	rec = env.do(http.MethodGet, "/api/mine", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/mine 返回 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "孤独摇滚") {
		t.Fatalf("我的求片里看不到刚提交的那条：%s", rec.Body.String())
	}
	// 状态文案由后端给（requestStatusText），两边页面才不会各说各话。
	if !strings.Contains(rec.Body.String(), "等待审核") {
		t.Fatalf("没返回状态文案：%s", rec.Body.String())
	}
}

func TestPortalSubmitWithoutReviewCreatesSubscription(t *testing.T) {
	env := newPortalEnv(t, func(c portalCfg) {
		c[settings.KeyMOMediaRequestRequireReview] = false
	})
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	rec := env.do(http.MethodPost, "/api/request",
		`{"tmdb_id":900,"title":"孤独摇滚","media_type":"tv"}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("提交返回 %d：%s", rec.Code, rec.Body.String())
	}
	if env.subs.calls != 1 {
		t.Fatalf("免审时应立刻建订阅，实际 %d 次", env.subs.calls)
	}
	if env.subs.last.Source != "request" {
		t.Fatalf("订阅 source = %q", env.subs.last.Source)
	}
	if !strings.Contains(rec.Body.String(), `"status":"approved"`) {
		t.Fatalf("免审提交的状态应是 approved：%s", rec.Body.String())
	}
}

// TestPortalDailyLimitIs429 验收点：超每日上限必须是 429。
//
// 用 400 也能把消息显示出来，但前端会把它当成「你填错了」，
// 于是提示变成「请求失败，请重试」——家人会以为是自己操作有问题，
// 而实际上要等明天。
func TestPortalDailyLimitIs429(t *testing.T) {
	env := newPortalEnv(t, func(c portalCfg) {
		c[settings.KeyMOMediaRequestDailyLimit] = 1
	})
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	first := env.do(http.MethodPost, "/api/request",
		`{"tmdb_id":901,"title":"甲","media_type":"movie"}`, cookie)
	if first.Code != http.StatusOK {
		t.Fatalf("首次提交返回 %d：%s", first.Code, first.Body.String())
	}
	second := env.do(http.MethodPost, "/api/request",
		`{"tmdb_id":902,"title":"乙","media_type":"movie"}`, cookie)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("超每日上限返回 %d，期望 429：%s", second.Code, second.Body.String())
	}
}

func TestPortalSubmitRejectsDuplicatePending(t *testing.T) {
	env := newPortalEnv(t, nil)
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	if rec := env.do(http.MethodPost, "/api/request",
		`{"tmdb_id":903,"title":"丙","media_type":"movie"}`, cookie); rec.Code != http.StatusOK {
		t.Fatalf("首次提交返回 %d", rec.Code)
	}
	rec := env.do(http.MethodPost, "/api/request",
		`{"tmdb_id":903,"title":"丙","media_type":"movie"}`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("重复提交返回 %d，期望 400：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "小明") {
		t.Fatalf("错误文案没说是谁先求的：%s", rec.Body.String())
	}
}

func TestPortalSubmitRejectsMissingTitle(t *testing.T) {
	env := newPortalEnv(t, nil)
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	rec := env.do(http.MethodPost, "/api/request",
		`{"tmdb_id":904,"title":"   ","media_type":"tv"}`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空片名返回 %d，期望 400：%s", rec.Code, rec.Body.String())
	}
}

// TestPortalDuplicateSubmitWritesOnlyOneRow 补一条存储侧断言：
// 重复求片不能靠「先查再插」，靠的是 partial unique index。
func TestPortalDuplicateSubmitWritesOnlyOneRow(t *testing.T) {
	env := newPortalEnv(t, nil)
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	for i := 0; i < 3; i++ {
		env.do(http.MethodPost, "/api/request",
			`{"tmdb_id":905,"title":"丁","media_type":"tv"}`, cookie)
	}
	list, err := env.svc.ListByStatus(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("重复提交写进了 %d 行", len(list))
	}
}

// ---------------------------------------------------------------- 标签与统计

func TestPortalSaveTagsReplacesAndReadsBack(t *testing.T) {
	env := newPortalEnv(t, nil)
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	if rec := env.do(http.MethodPut, "/api/tags",
		`{"tmdb_id":906,"media_type":"tv","tags":["4K","4k","中字"]}`, cookie); rec.Code != http.StatusOK {
		t.Fatalf("保存标签返回 %d：%s", rec.Code, rec.Body.String())
	}
	rec := env.do(http.MethodPut, "/api/tags",
		`{"tmdb_id":906,"media_type":"tv","tags":["中字"]}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("覆盖标签返回 %d：%s", rec.Code, rec.Body.String())
	}
	dto := jsonOf(t, rec)
	tags, _ := dto["tags"].([]any)
	if len(tags) != 1 || tags[0] != "中字" {
		t.Fatalf("标签是替换语义，回读应为 [中字]，实际 %v", dto["tags"])
	}
}

func TestPortalSaveTagsOverLimitIsRejected(t *testing.T) {
	env := newPortalEnv(t, func(c portalCfg) {
		c[settings.KeyMOMediaRequestTagMaxPerUser] = 2
	})
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	rec := env.do(http.MethodPut, "/api/tags",
		`{"tmdb_id":907,"media_type":"tv","tags":["A","B","C"]}`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超标签数返回 %d，期望 400（不是 429）：%s", rec.Code, rec.Body.String())
	}
}

func TestPortalStatsOnlyShowsOwnRows(t *testing.T) {
	env := newPortalEnv(t, nil)
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	env.do(http.MethodPost, "/api/request",
		`{"tmdb_id":908,"title":"戊","media_type":"tv"}`, cookie)
	rec := env.do(http.MethodGet, "/api/stats", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/stats 返回 %d：%s", rec.Code, rec.Body.String())
	}
	// 统计行只带 requester_id（不含姓名，DTO 就是这么设计的）。
	// 这里断言的是**服务端按本人过滤过**这件事：之前这里是全量统计，
	// 家人之间会互相看到对方的数字。
	dto := jsonOf(t, rec)
	items, _ := dto["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("期望 1 行本人统计，实际 %d 行：%s", len(items), rec.Body.String())
	}
	row, _ := items[0].(map[string]any)
	if row["requester_id"] == nil || row["requester_name"] != nil {
		t.Fatalf("统计行形状不对：%v", row)
	}
	if got := row["submitted_count"]; got != float64(1) {
		t.Fatalf("submitted_count = %v", got)
	}
}

// ---------------------------------------------------------------- 开关与隔离

// TestPortalReturns503WhenDisabled 验收 ⑧ 的这一半：
// 端口确实不监听（见 listener），但万一请求还是打到了（例如端口是旧进程占着的），
// 回的必须是「未启用」而不是继续服务。
func TestPortalReturns503WhenDisabled(t *testing.T) {
	env := newPortalEnv(t, func(c portalCfg) {
		c[settings.KeyMOMediaRequestEnabled] = false
	})
	cookie, rec := env.login("xiaoming", "pw-xiaoming-1")
	if cookie != nil {
		t.Fatal("求片关着时不该登录成功")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("关着时登录返回 %d，期望 503：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "未启用") {
		t.Fatalf("文案没说明未启用：%s", rec.Body.String())
	}
}

// TestPortalRejectsAdminEndpoints 手工点几条最容易被误开的后台接口。
// 完整清单由 TestRequestPortalRejectsEveryAdminRoute 用 AST 扫出来核对，
// 这里留几条当人读的样本。
func TestPortalRejectsAdminEndpoints(t *testing.T) {
	env := newPortalEnv(t, nil)
	for _, p := range []string{
		"/api/admin/settings",
		"/api/admin/rbac/users",
		"/api/admin/subscriptions",
		"/api/media-request/pending",
		"/api/dav/foo",
		"/api/media-request/rules",
	} {
		rec := env.do(http.MethodGet, p, "", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 在求片站上返回 %d，期望 404：%s", p, rec.Code, rec.Body.String())
		}
	}
	// 审核动作也必须 404：求片站不是审核台。
	rec := env.do(http.MethodPost, "/api/media-request/review", `{"id":1}`, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("审核接口在求片站上返回 %d，期望 404：%s", rec.Code, rec.Body.String())
	}
}

func TestPortalOwnRoutesStillWork(t *testing.T) {
	env := newPortalEnv(t, nil)
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/me", http.StatusOK},
		{http.MethodGet, "/api/mine", http.StatusOK},
		{http.MethodGet, "/api/stats", http.StatusOK},
		{http.MethodGet, "/api/search?q=" + "孤", http.StatusOK},
	} {
		rec := env.do(tc.method, tc.path, "", cookie)
		if rec.Code != tc.want {
			t.Errorf("%s %s 返回 %d，期望 %d：%s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// TestPortalSearchRejectsEmptyQuery 保证空查询不进搜索器。
func TestPortalSearchRejectsEmptyQuery(t *testing.T) {
	env := newPortalEnv(t, nil)
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	rec := env.do(http.MethodGet, "/api/search?q=", "", cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空查询返回 %d，期望 400：%s", rec.Code, rec.Body.String())
	}
}

// TestPortalPagesServeWithoutSession 页面本身（/、/login）不该要求登录 —
// 否则未登录的人看到的是浏览器 JSON 报错而不是登录页。
func TestPortalPagesServeWithoutSession(t *testing.T) {
	env := newPortalEnv(t, nil)
	for _, p := range []string{"/", "/login"} {
		rec := env.do(http.MethodGet, p, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 返回 %d：%s", p, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Fatalf("%s 的 Content-Type = %q", p, ct)
		}
	}
}

// ---------------------------------------------------------------- 兜底

// TestPortalHandlerDoesNotPanicOnMissingDeps 固化「装配没填全」时的表现：
// 返回可读的错误，而不是打穿进程。
func TestPortalHandlerDoesNotPanicOnMissingDeps(t *testing.T) {
	r := NewRequestPortalRouter(Deps{})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{}`)))
	if rec.Code == http.StatusOK {
		t.Fatalf("依赖没配却登录成功：%s", rec.Body.String())
	}
}

func TestWrapMediaRequestErrMapsKinds(t *testing.T) {
	cases := []struct {
		kind string
		want int
	}{
		{string(mediarequest.LimitKindDaily), http.StatusTooManyRequests},
		{string(mediarequest.LimitKindPending), http.StatusBadRequest},
		{string(mediarequest.LimitKindTagCount), http.StatusBadRequest},
		{string(mediarequest.LimitKindTagLength), http.StatusBadRequest},
	}
	for _, c := range cases {
		err := &mediarequest.LimitError{Kind: c.kind, Limit: 3, Actual: 4}
		rec := httptest.NewRecorder()
		writeErr(rec, wrapMediaRequestErr(err))
		if rec.Code != c.want {
			t.Errorf("%s → %d，期望 %d", c.kind, rec.Code, c.want)
		}
	}
}

// ---------------------------------------------------------------- 兜底
