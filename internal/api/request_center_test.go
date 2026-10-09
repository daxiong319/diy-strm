package api

// 求片中心（管理台那一侧）的接口层行为测试。
//
// 与 request_portal_test.go 共用同一套装配脚手架，但**换一棵路由树**：
// 这里只挂 RegisterRequestCenterRoutes，不启动整个后端。
//
// 测的是闸门，不是业务：
//
//  1. 没有 request.review 的人点不动审核（403）；
//  2. 驳回必须给理由 —— 空理由 400，不然求片的人只会看到一句「已驳回」；
//  3. 两个人同时点「通过」，第二个不能把订阅建第二遍。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"litepan/internal/mediarequest"
	"litepan/internal/rbac"
)

// centerRouter 拼一棵只挂求片中心路由的 chi 树。
//
// requireAdmin 必须手工挂上：注册函数只负责在每条路由上套 requirePermission，
// 而 requirePermission 是**从 context 里读会话**的（adminSessionFromContext），
// 那个 context 值由外层 requireAdmin 注入。少了它，审核员会被当成匿名用户 →
// 一律 403。这类「中间件靠上游约定」的地方，单测必须把上游也摆出来，
// 否则测出来的 403 是假象。
func (e *portalEnv) centerRouter() http.Handler {
	e.t.Helper()
	r := chi.NewRouter()
	h := newHandler(e.deps)
	r.Group(func(r chi.Router) {
		r.Use(h.requireAdmin)
		h.RegisterRequestCenterRoutes(r)
	})
	return r
}

// center 打一发管理台求片请求。
func (e *portalEnv) center(method, path, body string, jar *http.Cookie) *httptest.ResponseRecorder {
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
	e.centerRouter().ServeHTTP(rec, req)
	return rec
}

// loginAdmin 走 adminauth.Login 拿一枚 admin_session cookie。
//
// 这里刻意复用生产那条登录路径（含 RBAC 委托），而不是手搓一个 cookie：
// 「审核员能不能登进管理台」和「审核员能不能审核」是两件事，
// 测试要的是后者，但它得经由前者的真实路径才可信。
func (e *portalEnv) loginAdmin(username, password string) *http.Cookie {
	e.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login", nil)
	if _, err := e.deps.AdminAuth.Login(context.Background(), req, rec, username, password, false); err != nil {
		e.t.Fatalf("登录 %s 失败：%v", username, err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "admin_session" {
			return c
		}
	}
	e.t.Fatalf("登录 %s 成功但没拿到 admin_session：%s", username, rec.Body.String())
	return nil
}

// seedReviewer 造一个带审核权的账号，返回它的登录 cookie。
func (e *portalEnv) seedReviewer(t *testing.T, username, display, password string, perms ...string) *http.Cookie {
	t.Helper()
	overrides := make(map[string]string, len(perms))
	for _, p := range perms {
		overrides[p] = string(rbac.EffectAllow)
	}
	id, err := e.deps.RBAC.CreateUser(context.Background(), username, display, password, true)
	if err != nil {
		t.Fatalf("建审核账号 %s：%v", username, err)
	}
	if len(overrides) > 0 {
		if err := e.deps.RBAC.SetUserOverrides(context.Background(), id, overrides); err != nil {
			t.Fatalf("授权 %s：%v", username, err)
		}
	}
	return e.loginAdmin(username, password)
}

// submitViaPortal 让家人账号提一条求片单，返回求片单 id。
func (e *portalEnv) submitViaPortal(t *testing.T, cookie *http.Cookie, body string) int64 {
	t.Helper()
	rec := e.do(http.MethodPost, "/api/request", body, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("求片提交失败 %d：%s", rec.Code, rec.Body.String())
	}
	dto := jsonOf(t, rec)
	v, _ := dto["id"].(float64)
	if v == 0 {
		t.Fatalf("求片单 id = 0：%s", rec.Body.String())
	}
	return int64(v)
}

// ---------------------------------------------------------------- 闸门

func TestRequestCenterRequiresReviewPermission(t *testing.T) {
	env := newPortalEnv(t, nil)
	// 小明只有 request.submit，没有 request.review。
	cookie, _ := env.login("xiaoming", "pw-xiaoming-1")
	if cookie == nil {
		t.Fatal("小明没登录上")
	}
	admin := env.loginAdmin("xiaoming", "pw-xiaoming-1")
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/media-request/pending"},
		{http.MethodGet, "/media-request/requests"},
		{http.MethodGet, "/media-request/rules"},
		{http.MethodPost, "/media-request/reconcile"},
	} {
		rec := env.center(tc.method, tc.path, "", admin)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d，期望 403/401：%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	_ = cookie
}

func TestRequestCenterIsAbsentWithoutService(t *testing.T) {
	// 装配漏了 MediaRequest 时，注册函数应当直接不注册任何路由（404），
	// 而不是注册上一排会 panic 的 handler —— 这是「假接线」的最后一道兜底。
	d := Deps{}
	d.AdminAuth = nil
	r := chi.NewRouter()
	newHandler(Deps{}).RegisterRequestCenterRoutes(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/media-request/pending", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未接线时 /media-request/pending = %d，期望 404", rec.Code)
	}
	_ = d
}

// ---------------------------------------------------------------- 审核

func TestRequestCenterApproveCreatesSubscription(t *testing.T) {
	env := newPortalEnv(t, nil)
	family, _ := env.login("xiaoming", "pw-xiaoming-1")
	id := env.submitViaPortal(t, family, `{"tmdb_id":903,"title":"孤独摇滚","media_type":"tv"}`)

	reviewer := env.seedReviewer(t, "reviewer", "审核员", "pw-reviewer-1", rbac.PermRequestReview)

	rec := env.center(http.MethodPost, revPath(id),
		`{"approve":true}`, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("审核返回 %d：%s", rec.Code, rec.Body.String())
	}
	dto := jsonOf(t, rec)
	row, _ := dto["item"].(map[string]any)
	if row == nil || row["status"] != string(mediarequest.StatusApproved) {
		t.Fatalf("审核后状态不对：%s", rec.Body.String())
	}
	if linked, _ := row["subscription_linked"].(bool); !linked {
		t.Fatalf("审核通过却没建订阅：%s", rec.Body.String())
	}
	if env.subs.calls != 1 {
		t.Fatalf("SaveSubscription 调了 %d 次，期望 1 次", env.subs.calls)
	}
	// 列表页读到的状态要和详情一致（两个页面文案不同是很糟的体验）。
	pending := env.center(http.MethodGet, "/media-request/pending", "", reviewer)
	if strings.Contains(pending.Body.String(), `"id":`+itoa(id)) {
		t.Fatalf("已审的单还留在待审核里：%s", pending.Body.String())
	}
}

func TestRequestCenterRejectRequiresReason(t *testing.T) {
	env := newPortalEnv(t, nil)
	family, _ := env.login("xiaoming", "pw-xiaoming-1")
	id := env.submitViaPortal(t, family, `{"tmdb_id":904,"title":"葬送的芙莉莲","media_type":"tv"}`)
	reviewer := env.seedReviewer(t, "reviewer", "审核员", "pw-reviewer-1", rbac.PermRequestReview)

	rec := env.center(http.MethodPost, revPath(id),
		`{"approve":false,"reason":"   "}`, reviewer)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空驳回理由返回 %d，期望 400：%s", rec.Code, rec.Body.String())
	}
	// 拒绝了却把单子动了 —— 那就等于「不给理由也推了」。
	rows, err := env.svc.ListByStatus(context.Background(), string(mediarequest.StatusPending), 50)
	if err != nil {
		t.Fatalf("读待审：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("空理由驳回后待审单变成 %d 条", len(rows))
	}
	if env.subs.calls != 0 {
		t.Fatalf("驳回却建了订阅（%d 次）", env.subs.calls)
	}
}

func TestRequestCenterRejectStoresReason(t *testing.T) {
	env := newPortalEnv(t, nil)
	family, _ := env.login("xiaoming", "pw-xiaoming-1")
	id := env.submitViaPortal(t, family, `{"tmdb_id":905,"title":"剧名","media_type":"movie"}`)
	reviewer := env.seedReviewer(t, "reviewer", "审核员", "pw-reviewer-1", rbac.PermRequestReview)

	rec := env.center(http.MethodPost, revPath(id),
		`{"approve":false,"reason":"  库里已经有了  ","note":"先等等"}`, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("驳回返回 %d：%s", rec.Code, rec.Body.String())
	}
	dto := jsonOf(t, rec)
	row, _ := dto["item"].(map[string]any)
	if row["reject_reason"] != "库里已经有了" {
		t.Fatalf("驳回理由没去掉首尾空白：%v", row["reject_reason"])
	}
	detail := env.center(http.MethodGet, "/media-request/requests/"+itoa(id), "", reviewer)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "库里已经有了") {
		t.Fatalf("详情里看不到驳回理由：%s", detail.Body.String())
	}
}

func TestRequestCenterSecondReviewDoesNotDuplicateSubscription(t *testing.T) {
	env := newPortalEnv(t, nil)
	family, _ := env.login("xiaoming", "pw-xiaoming-1")
	id := env.submitViaPortal(t, family, `{"tmdb_id":906,"title":"剧名二","media_type":"tv"}`)
	first := env.seedReviewer(t, "reviewer1", "审核员甲", "pw-reviewer-11", rbac.PermRequestReview)
	second := env.seedReviewer(t, "reviewer2", "审核员乙", "pw-reviewer-22", rbac.PermRequestReview)

	if rec := env.center(http.MethodPost, revPath(id),
		`{"approve":true}`, first); rec.Code != http.StatusOK {
		t.Fatalf("首次审核失败 %d：%s", rec.Code, rec.Body.String())
	}
	rec := env.center(http.MethodPost, revPath(id),
		`{"approve":true}`, second)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("第二次审核返回 %d，期望 400：%s", rec.Code, rec.Body.String())
	}
	if env.subs.calls != 1 {
		t.Fatalf("两个审核员点了两次通过，SaveSubscription 被调 %d 次", env.subs.calls)
	}
}

// ---------------------------------------------------------------- 规则与对账

func TestRequestCenterRulesRoundTrip(t *testing.T) {
	env := newPortalEnv(t, nil)
	admin := env.seedReviewer(t, "center", "管理员", "pw-center-1",
		rbac.PermRequestReview, rbac.PermRequestCenterView)

	body := `{"items":[{"name":"电影免审","media_type":"movie","auto_approve":true,"enabled":true,"daily_limit":0}]}`
	if rec := env.center(http.MethodPut, "/media-request/rules", body, admin); rec.Code != http.StatusOK {
		t.Fatalf("保存规则返回 %d：%s", rec.Code, rec.Body.String())
	}
	rec := env.center(http.MethodGet, "/media-request/rules", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("读规则返回 %d：%s", rec.Code, rec.Body.String())
	}
	dto := jsonOf(t, rec)
	items, _ := dto["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("规则应有 1 条，实际 %d 条：%s", len(items), rec.Body.String())
	}
	row, _ := items[0].(map[string]any)
	if row["media_type"] != "movie" {
		t.Fatalf("规则 media_type = %v", row["media_type"])
	}
	if on, _ := row["auto_approve"].(bool); !on {
		t.Fatalf("规则 auto_approve 没存住：%s", rec.Body.String())
	}

	// 存回去再读一遍，行数不该翻倍（覆盖保存的经典坑）。
	if rec := env.center(http.MethodPut, "/media-request/rules", body, admin); rec.Code != http.StatusOK {
		t.Fatalf("二次保存规则返回 %d：%s", rec.Code, rec.Body.String())
	}
	rec = env.center(http.MethodGet, "/media-request/rules", "", admin)
	dto = jsonOf(t, rec)
	items, _ = dto["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("同一份规则存了两次变成 %d 条", len(items))
	}
}

func TestRequestCenterReconcileEndpoint(t *testing.T) {
	env := newPortalEnv(t, nil)
	admin := env.seedReviewer(t, centerPermAdmin, "管理员", "pw-center-1",
		rbac.PermRequestReview, rbac.PermRequestCenterView)
	rec := env.center(http.MethodPost, "/media-request/reconcile", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("对账返回 %d：%s", rec.Code, rec.Body.String())
	}
	// 空队列时对账必须是 0 而不是报错 —— 定时任务每 15 分钟跑一次，
	// 每次都报错会把真正的故障淹没在日志里。
	if !strings.Contains(rec.Body.String(), "handled") {
		t.Fatalf("对账响应形状不对：%s", rec.Body.String())
	}
}

const centerPermAdmin = "center"

// ---------------------------------------------------------------- 小工具

// revPath 审核端点的真实路径。
func revPath(id int64) string {
	return "/media-request/requests/" + itoa(id) + "/review"
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
