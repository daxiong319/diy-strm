package api

// 免登录分享页的接口层行为测试。
//
// 与 medialibshare/service_test.go 互不重叠：那边测的是「服务怎么算」，
// 这边测的是「HTTP 上看到的是什么」—— 状态码、请求头、访问面。
// 差别都在这几处，只有打真实请求才测得到：
//
//	验收② 令牌缺失/错误/过期 → 401，而且**不带** WWW-Authenticate；
//	验收③ 令牌只从 X-Share-Token 头里读，query/cookie 都换不到授权；
//	验收⑦ 访客路由树里一个管理接口都没有；
//	验收⑨ 开关关掉时访客路由整体消失（404，不是 503）；
//	管理端每条路由都要 share.manage 权限。
//
// ⚠️ 这里不打 NewRouter(Deps{})：它装配 WebDAV 时会无条件调
// d.Uploads.TempRegistry()，Deps 为空必 panic。所以改成拼一棵
// 只挂分享路由的 chi 树，与 request_portal_test.go / request_center_test.go
// 同一约定。真正的「注册了什么」由 wiring_reachability_test.go 的
// AST 扫描和 TestShareGuestRouteSurfaceIsPinned 盯住。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"litepan/internal/adminauth"
	"litepan/internal/core/driverexec"
	"litepan/internal/domain"
	"litepan/internal/driver"
	"litepan/internal/medialibshare"
	"litepan/internal/playback"
	"litepan/internal/rbac"
	"litepan/internal/settings"
	"litepan/internal/store"
)

// ---------------------------------------------------------------- 测试骨架

type shareEnv struct {
	t     *testing.T
	guest http.Handler
	admin http.Handler
	svc   *medialibshare.Service
	db    *store.DB
	deps  Deps
	cfg   shareSettings
	now   time.Time
	// mediaPath 是播放网关后面那个本地媒体文件的真实路径。
	mediaPath string
}

// shareSettings 是 medialibshare.Settings 的测试实现。
type shareSettings map[string]any

func (s shareSettings) String(k string) string {
	v, _ := s[k].(string)
	return v
}

func (s shareSettings) Bool(k string) bool {
	v, _ := s[k].(bool)
	return v
}

func (s shareSettings) Int(k string) int {
	v, _ := s[k].(int)
	return v
}

func newShareEnv(t *testing.T, tweak func(shareSettings)) *shareEnv {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "share.db")})
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
		settings.KeyMORBACEnabled:                   "true",
		settings.KeyMOLibraryShareEnabled:           "true",
		settings.KeyMOLibraryShareDefaultExpireDays: "7",
		settings.KeyMOLibraryShareDefaultMaxDevices: "5",
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
	// 分享管理员：有 share.manage。
	mgrID, err := rbacSvc.CreateUser(ctx, "fan", "放片的", "pw-fan-123456", true)
	if err != nil {
		t.Fatalf("建分享管理员: %v", err)
	}
	if err := rbacSvc.SetUserOverrides(ctx, mgrID, map[string]string{
		rbac.PermShareManage: string(rbac.EffectAllow),
	}); err != nil {
		t.Fatalf("授权 share.manage: %v", err)
	}
	// 什么权限都没有的账号：用来验证管理端真被挡下。
	if _, err := rbacSvc.CreateUser(ctx, "ops", "运维", "pw-ops-12345", true); err != nil {
		t.Fatalf("建运维账号: %v", err)
	}

	cfg := shareSettings{
		settings.KeyMOLibraryShareEnabled:           true,
		settings.KeyMOLibraryShareDefaultExpireDays: medialibshare.DefaultExpireDays,
		settings.KeyMOLibraryShareDefaultMaxDevices: medialibshare.DefaultMaxDevices,
	}
	if tweak != nil {
		tweak(cfg)
	}

	now := time.Now().UTC()
	svc := medialibshare.NewService(medialibshare.Params{
		Store: medialibshare.NewStore(db.WriteHandle(), db.ReadHandle()),
		Cfg:   cfg,
		Log:   shareLog{},
	})

	adminAuthSvc := adminauth.New(st.Configs, []byte("share-test-secret-32-bytes!"), nil)
	adminAuthSvc.SetExtraUserAuth(portalExtraUserAuth{svc: rbacSvc})

	e := &shareEnv{t: t, svc: svc, db: db, now: now, cfg: cfg}
	// 时钟走 shareEnv.now 这个字段，而不是闭包捕获局部 now ——
	// 测试里要靠 e.now = e.now.Add(25*time.Hour) 往前拨时间。
	svc.SetClock(func() time.Time { return e.now })
	e.deps = Deps{
		Settings:     settingsSvc,
		AdminAuth:    adminAuthSvc,
		RBAC:         rbacSvc,
		Playback:     e.newPlayback(),
		LibraryShare: svc,
	}

	// 访客树与管理端树分开挂：
	//   访客侧在生产里挂在 NewRouter 的顶层（/api 之外、不在 requireAdmin 组内），
	//   管理端在 requireAdmin 组内。两者的隔离性正是这里要测的东西，
	//   所以连挂法都得跟生产一致。
	guestMux := chi.NewRouter()
	h := newHandler(e.deps)
	h.RegisterLibraryShareGuestRoutes(guestMux)
	h.RegisterLibrarySharePageRoutes(guestMux)
	e.guest = guestMux

	adminMux := chi.NewRouter()
	adminMux.Group(func(r chi.Router) {
		r.Use(h.requireAdmin)
		h.RegisterLibraryShareRoutes(r)
	})
	e.admin = adminMux
	return e
}

// shareLog 是 medialibshare.Logger 的测试实现（不落盘）。
type shareLog struct{}

func (shareLog) Info(string, ...any) {}
func (shareLog) Warn(string, ...any) {}

// newPlayback 造一个可用的播放网关。
//
// 真实实现需要 driverexec.Executor + driver.Provider。测试里用一个假驱动
// 把整条链路接起来：**这不是为了绕过播放，而是为了证明访客播放确实走到了
// playback.ServeHTTP**（验收⑧）—— 走 LocalPath 分支时 serveStream 会真的
// 读盘并写出 Accept-Ranges / ETag / Content-Type，
// 而这些头页面路由不会给（页面路由给的是 text/html）。
// 换句话说，断言这些头就是在断言「第二套播放链路没有被偷偷写出来」。
func (e *shareEnv) newPlayback() *playback.Service {
	e.t.Helper()
	dir := e.t.TempDir()
	path := filepath.Join(dir, "孤独摇滚 S01E01.mp4")
	// 内容不用像视频，只要扩展名能推出 video/mp4 就行。
	if err := os.WriteFile(path, []byte("fake-media-bytes"), 0o644); err != nil {
		e.t.Fatalf("造测试媒体文件: %v", err)
	}
	e.mediaPath = path
	exec := driverexec.New(fakeDriverProvider{media: path}, nil)
	return playback.NewService(exec, nil)
}

// fakeDriverProvider 只会吐出那一个本地文件。
type fakeDriverProvider struct{ media string }

func (p fakeDriverProvider) Get(context.Context, int64) (driver.Driver, error) {
	return fakeShareDriver{media: p.media}, nil
}

// fakeShareDriver 是最小可用驱动：Meta + Lister + Downloader 三件套。
type fakeShareDriver struct{ media string }

func (d fakeShareDriver) Config() driver.Config {
	return driver.Config{Name: "fake", DisplayName: "假驱动", AuthType: driver.AuthNone}
}

func (d fakeShareDriver) GetAddition() any { return &struct{}{} }
func (d fakeShareDriver) Init(context.Context) error {
	return nil
}
func (d fakeShareDriver) Drop(context.Context) error { return nil }
func (d fakeShareDriver) Ping(context.Context) error { return nil }

func (d fakeShareDriver) ListFiles(context.Context, string) ([]domain.FileItem, error) {
	return nil, nil
}

// ResolveDownload 走 LocalPath：playback.serveStream 会 serveLocalFile，
// 从磁盘读出真实字节。走 URL 分支则要起真的上游服务器才能测。
func (d fakeShareDriver) ResolveDownload(context.Context, driver.DownloadRequest) (*domain.DownloadInfo, error) {
	info, err := os.Stat(d.media)
	if err != nil {
		return nil, err
	}
	return &domain.DownloadInfo{
		LocalPath: d.media,
		FileName:  filepath.Base(d.media),
		Size:      info.Size(),
		Mode:      domain.DownloadProxy,
	}, nil
}

// do 打一发访客请求。token 非空时写进 X-Share-Token 头。
func (e *shareEnv) do(method, path, body, token string) *httptest.ResponseRecorder {
	e.t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if token != "" {
		req.Header.Set(ShareTokenHeader, token)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone) AppleWebKit/605.1.15 Safari/604.1")
	rec := httptest.NewRecorder()
	e.guest.ServeHTTP(rec, req)
	return rec
}

// admin 打一发管理台请求。
func (e *shareEnv) adminDo(method, path, body string, jar *http.Cookie) *httptest.ResponseRecorder {
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
	e.admin.ServeHTTP(rec, req)
	return rec
}

// issue 走一次真实路径换令牌，返回明文令牌。
func (e *shareEnv) issue(code, password string) string {
	e.t.Helper()
	body := `{"visitor_id":"visitor-a"}`
	if password != "" {
		body = `{"visitor_id":"visitor-a","password":"` + password + `"}`
	}
	rec := e.do(http.MethodPost, "/share-play/"+code+"/token", body, "")
	if rec.Code != http.StatusOK {
		e.t.Fatalf("换令牌失败 %d：%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Token    string `json:"token"`
			HasToken bool   `json:"has_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		e.t.Fatalf("解析令牌响应失败：%v", err)
	}
	if !env.Data.HasToken {
		e.t.Fatalf("换令牌成功但 has_token=false：%s", rec.Body.String())
	}
	return env.Data.Token
}

// createShare 在管理端建一条分享，返回短码。
func (e *shareEnv) createShare(extra string) string {
	e.t.Helper()
	cookie := e.loginAdmin("fan", "pw-fan-123456")
	body := `{"account_id":7,"file_id":"f-001","title":"孤独摇滚 S01E01","expire_days":7,"max_devices":5` + extra + `}`
	rec := e.adminDo(http.MethodPost, "/library-shares/", body, cookie)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("创建分享失败 %d：%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Code string `json:"code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		e.t.Fatalf("解析创建响应失败：%v", err)
	}
	if env.Data.Code == "" {
		e.t.Fatalf("创建成功但没回短码：%s", rec.Body.String())
	}
	return env.Data.Code
}

// loginAdmin 借 request_center_test.go 的那条真实登录路径拿 admin_session。
func (e *shareEnv) loginAdmin(username, password string) *http.Cookie {
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
	e.t.Fatalf("登录成功但没拿到 admin_session：%s", rec.Body.String())
	return nil
}

func (e *shareEnv) count(table string) string {
	e.t.Helper()
	row := e.db.ReadHandle().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table)
	var n int
	if err := row.Scan(&n); err != nil {
		e.t.Fatalf("统计 %s：%v", table, err)
	}
	return strconv.Itoa(n)
}

// shareListItem 管理端列表里的一条分享（只留测试要断言的字段）。
type shareListItem struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	ExpireDays   int    `json:"expire_days"`
	MaxDevices   int    `json:"max_devices"`
	VisitorCount int    `json:"visitor_count"`
	ViewCount    int    `json:"view_count"`
	PlayCount    int    `json:"play_count"`
}

// listItems 打一次管理端列表并解包。
func (e *shareEnv) listItems(cookie *http.Cookie) []shareListItem {
	e.t.Helper()
	rec := e.adminDo(http.MethodGet, "/library-shares/", "", cookie)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("列出分享失败 %d：%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Items []shareListItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		e.t.Fatalf("解析列表失败：%s", rec.Body.String())
	}
	return env.Data.Items
}

// listItemID 拿第 i 条分享的 id。
func (e *shareEnv) listItemID(cookie *http.Cookie, i int) string {
	e.t.Helper()
	items := e.listItems(cookie)
	if len(items) <= i {
		e.t.Fatalf("列表只有 %d 条，取不到第 %d 条", len(items), i)
	}
	return items[i].ID
}

// ---------------------------------------------------------------- 验收①：不用登录就能播

func TestShareGuestNeedsNoAccount(t *testing.T) {
	e := newShareEnv(t, nil)
	code := e.createShare("")
	token := e.issue(code, "")

	// 整条请求没有任何 cookie、没有 admin_session、没有 Bearer。
	req := httptest.NewRequest(http.MethodGet, "/share-play/"+code+"/stream", nil)
	req.Header.Set(ShareTokenHeader, token)
	rec := httptest.NewRecorder()
	e.guest.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("免登录取流 = %d，期望 200：%s", rec.Code, rec.Body.String())
	}
	// 断言播放网关设的头 —— 页面路由也会给 200，但不会给这些。
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "video/") {
		t.Fatalf("Content-Type = %q，期望 video/*（说明没走 playback.serveStream）", got)
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("缺 Accept-Ranges，播放器无法拖动进度条：%v", rec.Header())
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("缺 ETag，播放器无法做断点续播")
	}
}

// ---------------------------------------------------------------- 验收②：令牌不对就明确拒绝

func TestShareStreamRejectsMissingToken(t *testing.T) {
	e := newShareEnv(t, nil)
	code := e.createShare("")
	rec := e.do(http.MethodGet, "/share-play/"+code+"/stream", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌取流 = %d，期望 401：%s", rec.Code, rec.Body.String())
	}
	// 带 WWW-Authenticate 会让浏览器弹登录框 —— 那会把访客推向
	// 「找管理员要账号」，而他明明有链接。断言这个头不存在。
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("访客端点不该带 WWW-Authenticate，实际 = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "令牌") {
		t.Fatalf("文案没说清是令牌问题：%s", rec.Body.String())
	}
}

func TestShareStreamRejectsWrongToken(t *testing.T) {
	e := newShareEnv(t, nil)
	code := e.createShare("")
	rec := e.do(http.MethodGet, "/share-play/"+code+"/stream", "", "根本不是令牌")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错令牌取流 = %d，期望 401：%s", rec.Code, rec.Body.String())
	}
}

func TestShareStreamRejectsTokenOfAnotherShare(t *testing.T) {
	e := newShareEnv(t, nil)
	codeA := e.createShare("")
	_ = e.issue(codeA, "")
	// B 是另一条分享，取它的令牌配 A 的链接。
	codeB := e.createShare(`,"title":"别的片子"`)
	tokenB := e.issueWith(codeB, "visitor-b", "")

	rec := e.do(http.MethodGet, "/share-play/"+codeA+"/stream", "", tokenB)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("用 B 的令牌取 A 的流 = %d，期望 401：%s", rec.Code, rec.Body.String())
	}
}

func TestShareTokenExpiresAfterADay(t *testing.T) {
	e := newShareEnv(t, nil)
	code := e.createShare("")
	token := e.issue(code, "")
	// 令牌本身 24 小时后失效：否则「设备数上限」形同虚设 ——
	// 访客存下令牌，隔一个月再拿来照样过。
	e.now = e.now.Add(25 * time.Hour)
	rec := e.do(http.MethodGet, "/share-play/"+code+"/stream", "", token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("过期令牌取流 = %d，期望 401：%s", rec.Code, rec.Body.String())
	}
}

func TestShareTokenIsRejectedWhenShareExpires(t *testing.T) {
	e := newShareEnv(t, nil)
	code := e.createShare("")
	token := e.issue(code, "")
	// 分享自己是 7 天后过期，令牌只有 24 小时，所以先把有效期压到 1 天再跨过去。
	cookie := e.loginAdmin("fan", "pw-fan-123456")
	list := e.adminDo(http.MethodGet, "/library-shares/", "", cookie)
	var env struct {
		Data struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &env); err != nil || len(env.Data.Items) == 0 {
		t.Fatalf("列出分享失败：%s", list.Body.String())
	}
	id := env.Data.Items[0].ID
	if rec := e.adminDo(http.MethodPatch, "/library-shares/"+id, `{"expire_days":1}`, cookie); rec.Code != http.StatusOK {
		t.Fatalf("改有效期失败 %d：%s", rec.Code, rec.Body.String())
	}
	e.now = e.now.Add(25 * time.Hour)
	rec := e.do(http.MethodGet, "/share-play/"+code+"/stream", "", token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("分享过期后取流 = %d，期望 404：%s", rec.Code, rec.Body.String())
	}
}

func TestShareIssueTokenRequiresPassword(t *testing.T) {
	e := newShareEnv(t, nil)
	code := e.createShare(`,"password":"letmein"`)
	rec := e.do(http.MethodPost, "/share-play/"+code+"/token", `{"visitor_id":"v1"}`, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("无口令换令牌 = %d，期望 400：%s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPost, "/share-play/"+code+"/token", `{"visitor_id":"v1","password":"错的"}`, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("错口令换令牌 = %d，期望 400：%s", rec.Code, rec.Body.String())
	}
	if token := e.issue(code, "letmein"); token == "" {
		t.Fatal("对口令却没拿到令牌")
	}
}

// ---------------------------------------------------------------- 验收③：令牌只在头里

func TestShareTokenIsNotAcceptedFromURLOrCookie(t *testing.T) {
	e := newShareEnv(t, nil)
	code := e.createShare("")
	token := e.issue(code, "")

	// query 参数
	rec := e.do(http.MethodGet, "/share-play/"+code+"/stream?token="+token, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("query 里的令牌被接受了 = %d：%s", rec.Code, rec.Body.String())
	}
	// cookie
	req := httptest.NewRequest(http.MethodGet, "/share-play/"+code+"/stream", nil)
	req.AddCookie(&http.Cookie{Name: ShareTokenHeader, Value: token})
	rec = httptest.NewRecorder()
	e.guest.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cookie 里的令牌被接受了 = %d：%s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------- 验收④：24 小时访客去重

func TestShareVisitorCountedOncePerDay(t *testing.T) {
	e := newShareEnv(t, nil)
	code := e.createShare("")
	// 换三次令牌 + 上报多次 open，最后 visitor_count 必须是 1。
	e.issueWith(code, "visitor-a", "")
	e.issueWith(code, "visitor-a", "")
	e.issueWith(code, "visitor-a", "")
	for i := 0; i < 4; i++ {
		if rec := e.do(http.MethodPost, "/share-play/"+code+"/event",
			`{"visitor_id":"visitor-a","event":"open"}`, ""); rec.Code != http.StatusOK {
			t.Fatalf("上报 open 失败 %d：%s", rec.Code, rec.Body.String())
		}
	}
	cookie := e.loginAdmin("fan", "pw-fan-123456")
	items := e.listItems(cookie)
	if items[0].VisitorCount != 1 {
		t.Fatalf("同一访客反复访问后 visitor_count = %d，期望 1", items[0].VisitorCount)
	}
	// open 事件每次都计 view（view 是「打开过几次」，visitor 是「来过几个人」，
	// 这两个口径不能混 —— 混了就没法区分刷页面和换人看）。
	if items[0].ViewCount != 4 {
		t.Fatalf("4 次上报 open 后 view_count = %d，期望 4", items[0].ViewCount)
	}

	// 换个浏览器 = 换个人，visitor 加一。
	e.issueWith(code, "visitor-b", "")
	if items = e.listItems(cookie); items[0].VisitorCount != 2 {
		t.Fatalf("第二个访客到来后 visitor_count = %d，期望 2", items[0].VisitorCount)
	}

	// 跨过 24 小时再回来：同一个访客算「又来了一次」。
	e.now = e.now.Add(25 * time.Hour)
	e.issueWith(code, "visitor-a", "")
	if items = e.listItems(cookie); items[0].VisitorCount != 3 {
		t.Fatalf("跨过 24 小时后 visitor_count = %d，期望 3", items[0].VisitorCount)
	}
}

// issueWith 走真实路径换令牌，返回明文令牌。
//
// 注意返回值的语义：**只有新建会话时 token 才非空**。
// 复用同一个 visitor 的会话时，服务端不重发明文（重发会让正在播放的页面 401），
// 响应里只有 has_token=true + token=""。所以想断言「这个访客能拿到令牌」，
// 要看 is_new_visit 而不是看 token 非空 —— 复用路径另有一个用例
// （TestShareTokenIsNotReissuedOnRefresh）专门钉住。
func (e *shareEnv) issueWith(code, visitorID, password string) string {
	e.t.Helper()
	rec := e.issueRaw(code, visitorID, password)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("换令牌失败 %d：%s", rec.Code, rec.Body.String())
	}
	return e.tokenOf(rec)
}

// tokenOf 从换令牌响应里取出明文令牌。
func (e *shareEnv) tokenOf(rec *httptest.ResponseRecorder) string {
	e.t.Helper()
	var env struct {
		Data struct {
			Token      string `json:"token"`
			HasToken   bool   `json:"has_token"`
			IsNewVisit bool   `json:"is_new_visit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		e.t.Fatalf("解析响应失败：%v", err)
	}
	if !env.Data.HasToken {
		e.t.Fatalf("换令牌成功但 has_token=false：%s", rec.Body.String())
	}
	return env.Data.Token
}

// issueOK 换令牌并断言「这个访客能进来」（新会话还是复用都算过）。
func (e *shareEnv) issueOK(code, visitorID, password string) {
	e.t.Helper()
	rec := e.issueRaw(code, visitorID, password)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("访客 %s 被拒 %d：%s", visitorID, rec.Code, rec.Body.String())
	}
	// tokenOf 内部已断言 has_token=true。
	e.tokenOf(rec)
}

// issueRaw 只做请求不解释结果，交给调用方断言。
func (e *shareEnv) issueRaw(code, visitorID, password string) *httptest.ResponseRecorder {
	e.t.Helper()
	body := `{"visitor_id":"` + visitorID + `"`
	if password != "" {
		body += `,"password":"` + password + `"`
	}
	body += `}`
	return e.do(http.MethodPost, "/share-play/"+code+"/token", body, "")
}

// ---------------------------------------------------------------- 验收⑤：五种有效期

func TestShareExpireDayOptions(t *testing.T) {
	e := newShareEnv(t, nil)
	cookie := e.loginAdmin("fan", "pw-fan-123456")
	for _, days := range medialibshare.ExpireDayOptions {
		body := `{"account_id":7,"file_id":"f-","title":"t","expire_days":` + strconv.Itoa(days) + `}`
		rec := e.adminDo(http.MethodPost, "/library-shares/", body, cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("expire_days=%d 创建失败 %d：%s", days, rec.Code, rec.Body.String())
		}
		var env struct {
			Data struct {
				Item struct {
					ExpiresAt  string `json:"expires_at"`
					ExpireDays int    `json:"expire_days"`
				} `json:"item"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("解析失败：%v", err)
		}
		if days == 0 {
			if env.Data.Item.ExpiresAt != "" {
				t.Fatalf("expire_days=0 应为永久，却拿到 expires_at=%q", env.Data.Item.ExpiresAt)
			}
			continue
		}
		if env.Data.Item.ExpireDays != days {
			t.Fatalf("expire_days=%d 回读 = %d", days, env.Data.Item.ExpireDays)
		}
	}
}

// ---------------------------------------------------------------- 验收⑥：设备数上限

func TestShareDeviceLimitRejectsExtraDevice(t *testing.T) {
	e := newShareEnv(t, nil)
	code := e.createShare(`,"max_devices":2`)
	e.issueWith(code, "phone", "")
	e.issueWith(code, "tablet", "")

	rec := e.do(http.MethodPost, "/share-play/"+code+"/token", `{"visitor_id":"laptop"}`, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("第三台设备 = %d，期望 429：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "设备") {
		t.Fatalf("文案没提示是设备数问题：%s", rec.Body.String())
	}
	// 已经进来过的设备不该被拒（刷新页面不等于换设备）。
	// 复用会话拿不到明文令牌是**设计如此**，所以只断言「没被拒」。
	e.issueOK(code, "phone", "")
}

func TestShareDeviceLimitFreesUpWhenDeviceGoesIdle(t *testing.T) {
	e := newShareEnv(t, nil)
	code := e.createShare(`,"max_devices":1`)
	e.issueWith(code, "phone", "")
	if rec := e.do(http.MethodPost, "/share-play/"+code+"/token", `{"visitor_id":"tablet"}`, ""); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("第二台设备 = %d，期望 429", rec.Code)
	}
	// 24 小时后第一台没再来 —— 设备数用活跃窗口算而不是累计行数，
	// 否则「发出去 5 天的链接，第二天就谁都打不开了」。
	e.now = e.now.Add(25 * time.Hour)
	// tablet 在 25 小时前被拒过，没有会话行；现在它该拿到一个**新**会话。
	rec := e.issueRaw(code, "tablet", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("闲置设备让位后新设备仍被拒 %d：%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			IsNewVisit bool `json:"is_new_visit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !env.Data.IsNewVisit {
		t.Fatal("让位后 tablet 应该拿到新会话，结果复用了旧的")
	}
}

// ---------------------------------------------------------------- 验收⑦⑨：访问面

func TestShareGuestTreeHasNoAdminEndpoint(t *testing.T) {
	e := newShareEnv(t, nil)
	e.createShare("")
	// 带着有效的管理 cookie 打访客树：即便如此，管理接口也必须 404。
	cookie := e.loginAdmin("fan", "pw-fan-123456")
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/library-shares/", ""},
		{http.MethodPost, "/library-shares/", `{"account_id":1,"file_id":"f","title":"t"}`},
		{http.MethodGet, "/library-shares/abc/stats", ""},
		{http.MethodDelete, "/library-shares/abc", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body == "" {
			req = httptest.NewRequest(tc.method, tc.path, nil)
		}
		req.AddCookie(cookie)
		req.Header.Set(ShareTokenHeader, "whatever")
		rec := httptest.NewRecorder()
		e.guest.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s 在访客树上 = %d，期望 404：%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestShareRoutesAreAbsentWhenServiceIsNil(t *testing.T) {
	// Deps.LibraryShare 为 nil 时一个端点都不注册（验收⑨的前一半）。
	h := newHandler(Deps{})
	mux := chi.NewRouter()
	h.RegisterLibraryShareGuestRoutes(mux)
	h.RegisterLibrarySharePageRoutes(mux)
	h.RegisterLibraryShareRoutes(mux)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/share-play/abc/token"},
		{http.MethodPost, "/share-play/abc/event"},
		{http.MethodGet, "/share-play/abc/stream"},
		{http.MethodGet, "/share/abc"},
		{http.MethodGet, "/library-shares/"},
	} { // 注意：管理端挂在 /api/admin 之下，这里用相对路径访问即可。
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s 在未装配时 = %d，期望 404", tc.method, tc.path, rec.Code)
		}
	}
}

func TestShareGuestRoutesAre404WhenDisabled(t *testing.T) {
	e := newShareEnv(t, nil)
	// 先在开关还开着的时候建好分享，拿到短码；
	// 再把开关关掉。这样「短码确实存在但开关关了 ⇒ 404」这件事
	// 才是真的被测到了 —— 否则可能只是因为短码压根没建。
	code := e.createShare("")
	e.setEnabled(false)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/share-play/" + code + "/token", `{"visitor_id":"v1"}`},
		{http.MethodPost, "/share-play/" + code + "/event", `{"visitor_id":"v1","event":"open"}`},
		{http.MethodGet, "/share-play/" + code + "/stream", ""},
		{http.MethodGet, "/share/" + code, ""},
	} {
		rec := e.do(tc.method, tc.path, tc.body, "some-token")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("开关关闭时 %s %s = %d，期望 404（不是 503）：%s",
				tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

// setEnabled 翻开关。
//
// 改的是传给 medialibshare 的那份 shareSettings —— 那是服务**实际读**的东西。
// 写 settings 表虽然更「真实」，但服务读的是构造时注入的配置源，
// 两边不通，写了也不会生效。这种「配置从哪读」的错，
// 正是 T09 里把 constants 改成直接引 settings.KeyXxx 的原因。
func (e *shareEnv) setEnabled(on bool) {
	e.t.Helper()
	e.cfg[settings.KeyMOLibraryShareEnabled] = on
}

// ---------------------------------------------------------------- 管理端闸门

func TestShareAdminRequiresPermission(t *testing.T) {
	e := newShareEnv(t, nil)
	// ops 没有任何权限。
	ops := e.loginAdmin("ops", "pw-ops-12345")
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/library-shares/", ""},
		{http.MethodPost, "/library-shares/", `{"account_id":1,"file_id":"f","title":"t"}`},
		{http.MethodDelete, "/library-shares/x", ""},
		{http.MethodPatch, "/library-shares/x", `{"expire_days":3}`},
		{http.MethodGet, "/library-shares/x/stats", ""},
	} {
		rec := e.adminDo(tc.method, tc.path, tc.body, ops)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 无权限时 = %d，期望 403/401：%s",
				tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	// 有 share.manage 的人应该过得去。
	fan := e.loginAdmin("fan", "pw-fan-123456")
	if rec := e.adminDo(http.MethodGet, "/library-shares/", "", fan); rec.Code != http.StatusOK {
		t.Fatalf("有 share.manage 却读不到列表 = %d：%s", rec.Code, rec.Body.String())
	}
}

func TestShareAdminRoundTrip(t *testing.T) {
	e := newShareEnv(t, nil)
	cookie := e.loginAdmin("fan", "pw-fan-123456")

	rec := e.adminDo(http.MethodPost, "/library-shares/",
		`{"account_id":7,"file_id":"f-1","title":"三体","expire_days":3,"max_devices":2}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建失败 %d：%s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data struct {
			Code string `json:"code"`
			URL  string `json:"url"`
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !strings.HasSuffix(created.Data.URL, "/share/"+created.Data.Code) {
		t.Fatalf("url = %q，短码 = %q", created.Data.URL, created.Data.Code)
	}

	// 列表里**不该**再出现明文短码以外的敏感字段。
	rec = e.adminDo(http.MethodGet, "/library-shares/", "", cookie)
	if strings.Contains(rec.Body.String(), "password_hash") {
		t.Fatalf("列表响应里出现了 password_hash：%s", rec.Body.String())
	}

	// 改有效期
	if rec := e.adminDo(http.MethodPatch, "/library-shares/"+created.Data.Item.ID,
		`{"expire_days":30}`, cookie); rec.Code != http.StatusOK {
		t.Fatalf("改有效期失败 %d：%s", rec.Code, rec.Body.String())
	}

	// 统计
	rec = e.adminDo(http.MethodGet, "/library-shares/"+created.Data.Item.ID+"/stats", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("统计失败 %d：%s", rec.Code, rec.Body.String())
	}
	var stats struct {
		Data struct {
			ShareID      string `json:"share_id"`
			MaxDevices   int    `json:"max_devices"`
			RecentVisits []struct {
				VisitorID string `json:"visitor_id"`
			} `json:"recent_visits"`
			RecentPlays      []map[string]any `json:"recent_plays"`
			RecentVisitsNull bool             `json:"-"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("解析统计失败：%v", err)
	}
	if stats.Data.ShareID != created.Data.Item.ID {
		t.Fatalf("统计里的 share_id = %q", stats.Data.ShareID)
	}

	// 删除后短码立即失效
	if rec := e.adminDo(http.MethodDelete, "/library-shares/"+created.Data.Item.ID, "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("删除失败 %d：%s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodPost, "/share-play/"+created.Data.Code+"/token",
		`{"visitor_id":"v1"}`, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("删除后还能换令牌 = %d：%s", rec.Code, rec.Body.String())
	}
}

func TestShareStatsNeverLeaksTokenHash(t *testing.T) {
	e := newShareEnv(t, nil)
	cookie := e.loginAdmin("fan", "pw-fan-123456")
	code := e.createShare("")
	e.issueWith(code, "visitor-a", "")

	rec := e.adminDo(http.MethodGet, "/library-shares/", "", cookie)
	var list struct {
		Data struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	rec = e.adminDo(http.MethodGet, "/library-shares/"+list.Data.Items[0].ID+"/stats", "", cookie)
	if strings.Contains(rec.Body.String(), "token_hash") {
		t.Fatalf("统计响应里出现了 token_hash：%s", rec.Body.String())
	}
	// token_hash 是 SHA-256 的 hex，明文令牌不能出现在任何响应里。
	if strings.Contains(rec.Body.String(), "token") {
		t.Fatalf("统计响应里出现了 token 字样：%s", rec.Body.String())
	}
}

func TestShareCreateRejectsMissingTitle(t *testing.T) {
	e := newShareEnv(t, nil)
	cookie := e.loginAdmin("fan", "pw-fan-123456")
	rec := e.adminDo(http.MethodPost, "/library-shares/",
		`{"account_id":7,"file_id":"f-1","title":"   "}`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空片名 = %d，期望 400：%s", rec.Code, rec.Body.String())
	}
}

func TestWrapLibraryShareErrMapsKinds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code domain.ErrorCode
	}{
		{"设备满", medialibshare.ErrDeviceLimit, domain.CodeRateLimited},
		{"口令错", medialibshare.ErrBadPassword, domain.CodeValidation},
		{"令牌无效", medialibshare.ErrBadToken, domain.CodeAuthExpired},
		{"不存在", medialibshare.ErrNotFound, domain.CodeNotFound},
		{"未启用", medialibshare.ErrDisabled, domain.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapLibraryShareErr(tc.err)
			ae, ok := domain.AsAppError(got)
			if !ok {
				t.Fatalf("没有翻成 AppError：%v", got)
			}
			if ae.Code != tc.code {
				t.Fatalf("错误码 = %q，期望 %q", ae.Code, tc.code)
			}
			if ae.Message == "" {
				t.Fatal("文案为空：newAppErr 会拿错误码的默认文案顶替自定义文案")
			}
		})
	}
}
