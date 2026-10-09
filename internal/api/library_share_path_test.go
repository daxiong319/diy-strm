package api

// T30 · 分享管理端路由挂回 /admin 子树 —— 验收测试。
//
// 与 library_share_test.go 的分工：那边手拼一棵 chi 树挂分享路由，
// 测的是「处理器行为对不对」；**这里必须打真实的 NewRouter**，
// 测的是「真实路径到底是什么」—— 而这正是 T30 要修的东西。
// 手拼树测不到前缀：那条树里 RegisterLibraryShareRoutes 挂在根上，
// 无论它在 router.go 里被挪到哪儿，手拼树都一样能通。
//
// ⚠️ NewRouter(Deps{}) 直接调会 panic：装配 WebDAV 那一步无条件调
// d.Uploads.TempRegistry()（router.go:913），而 TempRegistry 是
// (*upload.Manager) 的指针接收者，nil 会直接段错误。
// 所以这里必须造一个真的 upload.NewManager —— 不是为了测上传，
// 是为了让路由树能装配出来。

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"litepan/internal/upload"
)

// realShareRouter 造一棵**真实的** NewRouter 路由树。
//
// 能造出来是有前提的：shareEnv 已经备好 Settings / AdminAuth / RBAC /
// Playback / LibraryShare，缺的只有 Uploads。
func realShareRouter(t *testing.T, e *shareEnv) http.Handler {
	t.Helper()
	deps := e.deps
	deps.Uploads = upload.NewManager(upload.Options{
		DataDir: t.TempDir(),
		Log:     slog.Default(),
	})
	return NewRouter(deps)
}

// realRouterDo 打一发真实路径的请求。
func (e *shareEnv) realRouterDo(t *testing.T, router http.Handler, method, path string, body string, jar *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
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
	router.ServeHTTP(rec, req)
	return rec
}

// TestWiringShareAdminRoutesLiveUnderAdminSubtree 验收①：
// 五条路径在真实路由树上都命中处理器。
//
// 判据用「有会话时拿到什么状态码」而不是「404 与否」：
// 404 只能证明路径没注册，而 200/2xx 才证明命中了**正确的**处理器。
// 一个更弱的版本是「不带 cookie 打一发，不是 404 就算过」——
// 那会把「路径存在但挂到了别的地方」也算通过（比如注册在了访客树上），
// 而这个任务改的恰恰就是位置。
func TestWiringShareAdminRoutesLiveUnderAdminSubtree(t *testing.T) {
	e := newShareEnv(t, nil)
	router := realShareRouter(t, e)
	cookie := e.loginAdmin("fan", "pw-fan-123456")

	// 先真建一条出来，后面的改期/撤销/统计才有对象。
	createBody := `{"account_id":7,"file_id":"f-001","title":"孤独摇滚 S01E01","expire_days":7,"max_devices":5}`
	rec := e.realRouterDo(t, router, http.MethodPost, "/api/admin/library-shares/", createBody, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/admin/library-shares/ = %d，期望 200：%s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
			Code string `json:"code"`
			URL  string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析创建响应失败：%v", err)
	}
	id := created.Data.Item.ID
	if id == "" {
		t.Fatalf("创建成功但没回 id：%s", rec.Body.String())
	}
	if !strings.HasSuffix(created.Data.URL, "/share/"+created.Data.Code) {
		t.Fatalf("分享 URL 不对：%q", created.Data.URL)
	}

	for _, tc := range []struct {
		what   string
		method string
		path   string
		body   string
	}{
		{"列表", http.MethodGet, "/api/admin/library-shares/", ""},
		{"统计", http.MethodGet, "/api/admin/library-shares/" + id + "/stats", ""},
		{"改有效期", http.MethodPatch, "/api/admin/library-shares/" + id, `{"expire_days":3}`},
	} {
		rec := e.realRouterDo(t, router, tc.method, tc.path, tc.body, cookie)
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s = %d，期望 200：%s", tc.what, tc.method+" "+tc.path, rec.Code, rec.Body.String())
		}
	}
	// 删除放在最后：前面几条还要用这条分享。
	rec = e.realRouterDo(t, router, http.MethodDelete, "/api/admin/library-shares/"+id, "", cookie)
	if rec.Code != http.StatusOK {
		t.Errorf("撤销 %s = %d，期望 200：%s", "/api/admin/library-shares/"+id, rec.Code, rec.Body.String())
	}
}

// TestWiringShareAdminOldPrefixIsGone 验收①的对照组：
// 旧的 /api/library-shares/* 不再是入口 —— 证明没有留下重复路由。
//
// 两套路径都能访问同一功能是**新的假接线来源**：日后改一处漏一处，
// 谁也不知道线上到底走的是哪一套。任务书第 28 行专门点名了这条。
func TestWiringShareAdminOldPrefixIsGone(t *testing.T) {
	e := newShareEnv(t, nil)
	router := realShareRouter(t, e)
	cookie := e.loginAdmin("fan", "pw-fan-123456")

	for _, path := range []string{
		"/api/library-shares/",
		"/api/library-shares/abc/stats",
	} {
		rec := e.realRouterDo(t, router, http.MethodGet, path, "", cookie)
		if rec.Code != http.StatusNotFound {
			t.Errorf("旧路径 %s = %d，期望 404（挪走之后不该再有第二套路径）：%s", path, rec.Code, rec.Body.String())
		}
	}
	// 对照组：/admin 子树本身仍然是通的，证明上面那几条 404 不是整棵子树塌了。
	rec := e.realRouterDo(t, router, http.MethodGet, "/api/admin/settings", "", cookie)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("/api/admin/settings 也 404了 —— 不是分享路由的问题，是 /admin 子树整体不通")
	}
}

// TestWiringShareAdminStillChecksPermission 验收②：
// 没有 share.manage 的会话调这五条 → 403，不是 404 也不是 200。
//
// 挪注册位置只挪了「路径」，不能顺手把权限闸弄丢 —— 403 而不是 404
// 同时说明两件事：路径注册着，且 requirePermission 还在生效。
func TestWiringShareAdminStillChecksPermission(t *testing.T) {
	e := newShareEnv(t, nil)
	router := realShareRouter(t, e)
	ops := e.loginAdmin("ops", "pw-ops-12345")

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/admin/library-shares/", ""},
		{http.MethodPost, "/api/admin/library-shares/", `{"account_id":1,"file_id":"f","title":"t"}`},
		{http.MethodPatch, "/api/admin/library-shares/x", `{"expire_days":3}`},
		{http.MethodDelete, "/api/admin/library-shares/x", ""},
		{http.MethodGet, "/api/admin/library-shares/x/stats", ""},
	} {
		rec := e.realRouterDo(t, router, tc.method, tc.path, tc.body, ops)
		if rec.Code != http.StatusForbidden {
			t.Errorf("无 share.manage 权限时 %s %s = %d，期望 403：%s",
				tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

// TestWiringShareAdminUnauthenticatedIs401 补一条边界：
// 完全没有会话时是 401 而不是 404。
//
// 401/403/404 三者都「访问不到」，但含义完全不同：
// 404 会被访客理解成「没这条路径」，401 才是「要登录」。
func TestWiringShareAdminUnauthenticatedIs401(t *testing.T) {
	e := newShareEnv(t, nil)
	router := realShareRouter(t, e)
	rec := e.realRouterDo(t, router, http.MethodGet, "/api/admin/library-shares/", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无会话 GET /api/admin/library-shares/ = %d，期望 401：%s", rec.Code, rec.Body.String())
	}
}

// TestWiringShareAdminDisabledStillReportsNotImplemented 验收③：
// 开关关掉时回「未启用」，不是 404。
//
// 挪注册位置最容易碰坏的就是这个语义：路径写错/树挪错 ⇒ 404，
// 而「功能没开」是另一回事，libraryShareReady 负责回它。
// 两者混在一起，用户会以为分享页不存在，而实际上管理员忘了开开关。
func TestWiringShareAdminDisabledStillReportsNotImplemented(t *testing.T) {
	e := newShareEnv(t, nil)
	router := realShareRouter(t, e)
	cookie := e.loginAdmin("fan", "pw-fan-123456")
	e.setEnabled(false)

	rec := e.realRouterDo(t, router, http.MethodGet, "/api/admin/library-shares/", "", cookie)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("开关关掉后 = 404，说明路由没注册上（路径又错位了）：%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "未启用") {
		t.Fatalf("开关关掉后应回「未启用」，实际 = %d %s", rec.Code, rec.Body.String())
	}
}

// TestWiringShareAdminRouterSourceStillMounts 静态确认：
// router.go 里这一行注册确实待在 /admin 子树**内部**。
//
// 前面的用例都是行为断言（打真实请求看结果）。这条从源码上钉死注册位置，
// 因为「行为对了」有两种可能：真的挪对了，或者被别处的路径碰巧接住了
// （比如有人日后在别处又挂一份同样能通的路由 —— 那正是重复路由的起点）。
// 这条会先于行为测试给出可读的报错：谁把这一行挪出 /admin 了。
//
// 判据是「/admin 子树内有没有它」+「整棵树上它出现几次」：
// 只查前者查不出「挪对了但多挂了一份」。
func TestWiringShareAdminRouterSourceStillMounts(t *testing.T) {
	// NewRouter 还能装配 —— 挪动没写坏路由树。
	realShareRouter(t, newShareEnv(t, nil))

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 router.go 失败：%v", err)
	}

	// 1) 整棵文件里 RegisterLibraryShareRoutes 被调了几次。
	//    多于一次 = 挂了两处 = 同一功能两个入口，日后改一处漏一处。
	var all []int
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && calleeName(call) == "RegisterLibraryShareRoutes" {
			all = append(all, fset.Position(call.Pos()).Line)
		}
		return true
	})
	if len(all) != 1 {
		t.Fatalf("router.go 里 RegisterLibraryShareRoutes 被调用 %d 次（第 %v 行），期望恰好 1 次。\n"+
			"挂两处 = 同一功能两个入口，日后改一处漏一处，是新的假接线来源（T30 任务书第 28 行专门点名了这条）。",
			len(all), all)
	}

	// 2) 那唯一一次调用，是否在 r.Route("/admin", ...) 的闭包体内。
	//
	// 用 AST 找子树边界，而不是靠缩进或行号区间：闭包前面那层
	// r.Group(func(r){ r.Use(h.requireAdmin) ... }) 里的行缩进完全一样，
	// 按行判断会得出「在 /admin 里面」这种看起来对的假答案 ——
	// 而那正是 T10 之前错位的形态。
	inside := shareRoutesInsideAdminSubtree(fset, file)
	if len(inside) != 1 || inside[0] != all[0] {
		t.Fatalf("RegisterLibraryShareRoutes 在 router.go 第 %d 行被调用，"+
			"但 r.Route(\"/admin\", ...) 子树内命中 %v 行。\n"+
			"分享管理端必须挂在 /admin 子树里 —— 真实路径是 /api/admin/library-shares/*，"+
			"与 T10 任务书 §2 和 web/src/api/libraryShare.ts 一致（T30 修的就是这个错位）",
			all[0], inside)
	}
}

// shareRoutesInsideAdminSubtree 返回落在 r.Route("/admin", ...) 闭包
// **顶层语句**里的 RegisterLibraryShareRoutes 调用行号。
//
// 只看顶层语句：嵌在更深一层里的注册同样在这个子树内，
// 但我们不打算支持那种写法（/admin 里的其它注册都在顶层），
// 所以不递归进去 —— 递归会让「挪到某个子组里」也算通过。
func shareRoutesInsideAdminSubtree(fset *token.FileSet, file *ast.File) []int {
	var out []int
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || calleeName(call) != "Route" || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if val, err := strconv.Unquote(lit.Value); err != nil || val != "/admin" {
			return true
		}
		fn, ok := call.Args[1].(*ast.FuncLit)
		if !ok {
			return true
		}
		for _, stmt := range fn.Body.List {
			expr, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			inner, ok := expr.X.(*ast.CallExpr)
			if !ok || calleeName(inner) != "RegisterLibraryShareRoutes" {
				continue
			}
			out = append(out, fset.Position(inner.Pos()).Line)
		}
		return true
	})
	return out
}

// calleeName 取调用点的方法名（x.Y(...) => "Y"，Y(...) => "Y"）。
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	}
	return ""
}
