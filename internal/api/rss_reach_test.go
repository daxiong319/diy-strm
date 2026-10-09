package api

// T16 RSS 订阅源的路由可达性守卫。
//
// 复用 T11 留下的 AST 基建（calleeName / skeletonPath / backendRoutes），
// 但**不**照抄 router.go 的注册来建测试树：照抄出来的是两份独立的真相，
// router.go 挪错位置时照抄版照样全绿 —— T10 的 library-share 就是这么漏掉的
// （挂在 /admin 之外，前端按 /admin/ 写，五个操作全 404，而测试全绿）。
// 这里让 router.go 自己当唯一真相：从它的 AST 下降出**真实**前缀再拼树。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// rssRoute 描述一个前端会调用的 RSS 端点。
type rssRoute struct {
	method string
	path   string // 相对 /api 的完整路径（含 /admin 前缀）
}

// rssRoutes 全部 RSS 端点。path 一律按 web/src/api/rss.ts 将来要写的那个
// 完整路径填 —— 路径不是猜的：下面会对账 router.go 的 AST。
func rssRoutes() []rssRoute {
	return []rssRoute{
		{http.MethodGet, "/api/admin/rss-sources"},
		{http.MethodPost, "/api/admin/rss-sources"},
		{http.MethodGet, "/api/admin/rss-sources/7"},
		{http.MethodPut, "/api/admin/rss-sources/7"},
		{http.MethodDelete, "/api/admin/rss-sources/7"},
		{http.MethodPost, "/api/admin/rss-sources/7/sync"},
		{http.MethodPost, "/api/admin/rss-sync"},
		{http.MethodGet, "/api/admin/rss-history"},
		{http.MethodDelete, "/api/admin/rss-history/9"},
		{http.MethodPost, "/api/admin/rss-preview"},
		{http.MethodGet, "/api/admin/rss-options"},
	}
}

// TestRSSRoutesReachableEveryEndpointHasACase 每个新端点都要有路由可达性
// 用例（00-master.md 硬约束 8）。
func TestRSSRoutesReachableEveryEndpointHasACase(t *testing.T) {
	router := newRSSTestRouter(t)

	// 1) 对账：路由表里注册了但用例里没有的 = 新端点漏了可达性用例。
	//
	// ⚠️ 两侧都必须归一成**骨架**再比：router.go 写 r.Get("/", …)、
	// 参数写 "/{id}"，而用例写的是真实调用路径 "/api/admin/rss-sources/7"。
	// 直接比字面量会两边都报"没有"—— 守卫自己先炸，没人看真正的结论。
	registered := rssRegisteredPaths(t)
	covered := map[string]bool{}
	for _, rt := range rssRoutes() {
		covered[rt.method+" "+caseSkeleton(rt.path)] = true
	}
	for _, p := range registered {
		if !covered[p] {
			t.Errorf("端点 %s 已注册，但 rssRoutes() 里没有对应用例 —— "+
				"每个新端点都要有路由可达性用例", p)
		}
	}
	if len(covered) == 0 {
		t.Fatal("rssRoutes() 是空的 —— 守卫自己失效了")
	}
	// 反向：用例里写了但路由表里没有的 = 前端会 404。
	for _, rt := range rssRoutes() {
		if !containsString(registered, rt.method+" "+caseSkeleton(rt.path)) {
			t.Errorf("用例要求 %s %s 可达，但 router.go 里没有这个注册 —— "+
				"前端按这个路径调会 404", rt.method, rt.path)
		}
	}

	// 2) 逐条打请求。401/403/503 都算"通"：未登录会被鉴权挡下，
	//    依赖未装配走 ensureServiceReady；404/405 才是"路径写错/没挂上"。
	for _, rt := range rssRoutes() {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Fatalf("%s %s 返回 404 —— 路由没挂上，或路径与 web/src/api/rss.ts 不一致",
					rt.method, rt.path)
			}
			if rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("%s %s 返回 405 —— 方法与前端调用方式不一致", rt.method, rt.path)
			}
		})
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// caseSkeleton 把用例里写的**真实调用路径**（/rss-sources/7/sync）归一成
// router.go 那种**路由骨架**（/rss-sources/{}/sync）。
//
// ⚠️ 复用 skeletonPath 是不够的：它只把 "{id}" 换成 "{}"，方向是
// 骨架→用例，反过来调用（传 "7"）会原样返回 "/rss-sources/7/sync"，
// 于是正向、反向两条断言同时报「没有这个注册」—— 守卫自己先炸，
// 真正的结论（哪个端点漏了）反而没人看。
//
// ⚠️ 要替换的是**每一段**纯数字，不是只有末段：只换末段的话
// /rss-sources/7/sync 的末段是 "sync" 换不掉，骨架对不上又会自炸。
func caseSkeleton(p string) string {
	parts := strings.Split(skeletonPath(p), "/")
	for i, seg := range parts {
		if _, err := strconv.Atoi(seg); err == nil {
			parts[i] = "{}"
		}
	}
	return strings.Join(parts, "/")
}

// TestRSSRoutesInsideAdminSubtree RSS 的注册必须落在 r.Route("/admin", …)
// 闭包内。真实路径是 /api/admin/rss-*。
//
// ⚠️ 判断"在不在某子树里"必须走 AST：r.Route("/admin", …) 外层与
// r.Group(func(r){ r.Use(h.requireAdmin) … }) 里缩进完全一样，
// 靠缩进或行号区间判断会得出"在里面"的假答案（00-master.md 硬约束 13）。
// 也不能直接套 routesInsideAdminSubtree —— 它只看 /admin 闭包的**顶层**语句，
// 而 RSS 那段包在一个 r.Group 闭包里（为了挂 requirePermission），顶层看不到。
func TestRSSRoutesInsideAdminSubtree(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 router.go 失败：%v", err)
	}
	for _, seg := range []string{"/rss-sources", "/rss-history", "/rss-options"} {
		got := adminSubtreePrefix(t, fset, file, seg)
		want := "/api/admin" + seg
		if got != want {
			t.Errorf("%s 注册在 %s，期望 %s。\n"+
				"RSS 必须在 /admin 子树里 —— 前端按 /api/admin/rss-* 写，"+
				"挂到 /admin 之外就是全套操作 404（T10 的 library-share 教训）",
				seg, got, want)
		}
	}
	// /rss-preview 与 /rss-sync 是字面量单段注册（r.Post("/rss-preview", …)），
	// 没有自己的 Route 前缀可下降，改用 handler 名反查它挂在哪个闭包里。
	for _, handler := range []string{"rssPreview", "rssSyncAll"} {
		if !handlerInsideAdminSubtree(file, handler) {
			t.Errorf("handler %s 没有注册在 r.Route(\"/admin\", ...) 子树内 —— "+
				"前端按 /api/admin/ 写，挂到 /admin 之外会 404", handler)
		}
	}
}

// handlerInsideAdminSubtree 判断 h.<name> 是否在 /admin 闭包的字符区间内注册。
//
// 用区间而不是递归下降：chi 的注册是一棵语法树，但"某个 handler 名出现在
// /admin 闭包体内"这件事用位置区间判断就够，且不必关心它嵌了几层 Group。
func handlerInsideAdminSubtree(file *ast.File, handler string) bool {
	adminStart, adminEnd, ok := adminFuncLitSpan(file)
	if !ok {
		return false
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		switch calleeName(call) {
		case "Get", "Post", "Put", "Patch", "Delete":
		default:
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		sel, isSel := call.Args[1].(*ast.SelectorExpr)
		if !isSel || sel.Sel.Name != handler {
			return true
		}
		if call.Pos() >= adminStart && call.Pos() <= adminEnd {
			found = true
		}
		return !found
	})
	return found
}

// adminFuncLitSpan 返回 r.Route("/admin", fn) 里 fn 的区间。
func adminFuncLitSpan(file *ast.File) (token.Pos, token.Pos, bool) {
	var span [2]token.Pos
	ok := false
	ast.Inspect(file, func(n ast.Node) bool {
		if ok {
			return false
		}
		call, isCall := n.(*ast.CallExpr)
		if !isCall || calleeName(call) != "Route" || len(call.Args) < 2 {
			return true
		}
		lit, isLit := call.Args[0].(*ast.BasicLit)
		if !isLit || lit.Kind != token.STRING {
			return true
		}
		if val, err := strconv.Unquote(lit.Value); err != nil || val != "/admin" {
			return true
		}
		fn, isFn := call.Args[1].(*ast.FuncLit)
		if !isFn {
			return true
		}
		span = [2]token.Pos{fn.Pos(), fn.End()}
		ok = true
		return false
	})
	return span[0], span[1], ok
}

// adminSubtreePrefix 返回 segment 在 router.go 里的**真实**完整路径。
//
// 两种注册形态都要能解析：
//   - r.Route("/rss-sources", fn) → 从 /api 逐层下降，Route 加前缀、Group 不加；
//   - r.Get("/rss-options", fn)   → 在 /admin 闭包里按字面量段找，直接拼
//     "/api/admin" + segment。
//
// 找不到返回可读的说明文字（不是空串 —— 空串会被拿去拼请求，
// chi 直接 panic "routing pattern must begin with '/'"，报错信息还很难读）。
//
// ⚠️ Group 只有一个参数、Route(path, fn) 有两个 —— 用统一的 len(Args)>=2
// 判会把 Group 全部漏掉，表现是"router.go 里没有这个路由"（T11 已踩过）。
func adminSubtreePrefix(t *testing.T, fset *token.FileSet, file *ast.File, segment string) string {
	t.Helper()
	if found := descendForPrefix(file, "/api", segment); found != "" {
		return found
	}
	// 字面量单段：在 /admin 闭包里查 r.Get/Post/Put/Patch/Delete("/rss-options")。
	if literalInAdminSubtree(file, segment) {
		return "/api/admin" + segment
	}
	return "(在 /api 子树里找不到 " + segment + " 的注册)"
}

// literalInAdminSubtree 判断 `/admin` 闭包内是否有字面量段等于 segment 的
// 方法注册。
func literalInAdminSubtree(file *ast.File, segment string) bool {
	adminStart, adminEnd, ok := adminFuncLitSpan(file)
	if !ok {
		return false
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		switch calleeName(call) {
		case "Get", "Post", "Put", "Patch", "Delete":
		default:
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		lit, isLit := call.Args[0].(*ast.BasicLit)
		if !isLit || lit.Kind != token.STRING {
			return true
		}
		if val, err := strconv.Unquote(lit.Value); err != nil || val != segment {
			return true
		}
		if call.Pos() >= adminStart && call.Pos() <= adminEnd {
			found = true
		}
		return !found
	})
	return found
}

func descendForPrefix(file *ast.File, path, segment string) string {
	var found string
	var descend func(string, *ast.FuncLit) bool
	descend = func(p string, fn *ast.FuncLit) bool {
		for _, stmt := range fn.Body.List {
			expr, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expr.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			var (
				seg   string
				inner *ast.FuncLit
			)
			switch calleeName(call) {
			case "Route":
				if len(call.Args) < 2 {
					continue
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				seg = v
				inner, _ = call.Args[1].(*ast.FuncLit)
			case "Group":
				if len(call.Args) < 1 {
					continue
				}
				inner, _ = call.Args[0].(*ast.FuncLit)
			default:
				continue
			}
			if seg == segment {
				found = p + seg
				return true
			}
			if inner != nil && descend(p+seg, inner) {
				return true
			}
		}
		return false
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || calleeName(call) != "Route" || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if val, err := strconv.Unquote(lit.Value); err != nil || val != "/api" {
			return true
		}
		if fn, ok := call.Args[1].(*ast.FuncLit); ok {
			return !descend(path, fn)
		}
		return true
	})
	return found
}

// rssRegisteredPaths 从 router.go 的 AST 里抽出所有 RSS 端点的
// (METHOD 完整路径)。
//
// 走 /admin 闭包 → 递归进 Group 闭包 → 收集 (METHOD, path)，
// 再按 "/rss-" 前缀过滤。这样字面量单段注册（/rss-preview、/rss-options、
// /rss-sync）与 Route 子树（/rss-sources、/rss-history）都能收到，
// 不必为两种形态各写一套对账。
func rssRegisteredPaths(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 router.go 失败：%v", err)
	}
	body := findRouteFuncLit(file, "/admin")
	if body == nil {
		t.Fatal("router.go 里找不到 r.Route(\"/admin\", …)")
	}
	var all []string
	collectRoutes(body, "/api/admin", &all)
	var out []string
	for _, p := range all {
		// 归一成骨架：这里留 "/{id}"、调用侧用真实 id，两侧才能对上。
		if strings.Contains(p, "/rss-") {
			sp := strings.SplitN(p, " ", 2)
			out = append(out, sp[0]+" "+skeletonPath(sp[1]))
		}
	}
	return out
}

// findRouteFuncLit 取出 r.Route(segment, fn) 的闭包。
func findRouteFuncLit(file *ast.File, segment string) *ast.FuncLit {
	var out *ast.FuncLit
	ast.Inspect(file, func(n ast.Node) bool {
		if out != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || calleeName(call) != "Route" || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if val, err := strconv.Unquote(lit.Value); err != nil || val != segment {
			return true
		}
		if fn, ok := call.Args[1].(*ast.FuncLit); ok {
			out = fn
			return false
		}
		return true
	})
	return out
}

// collectRoutes 把闭包体内的 (METHOD, 完整路径) 收集出来。
// Route 加一段前缀，Group 只挂中间件所以不加前缀。
func collectRoutes(fn *ast.FuncLit, prefix string, out *[]string) {
	for _, stmt := range fn.Body.List {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		kind := calleeName(call)
		switch kind {
		case "Get", "Post", "Put", "Patch", "Delete":
			if len(call.Args) < 2 {
				continue
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			seg, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			*out = append(*out, strings.ToUpper(kind)+" "+prefix+seg)
		case "Route", "Group":
			var (
				seg   string
				inner *ast.FuncLit
			)
			if kind == "Route" {
				if len(call.Args) < 2 {
					continue
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				seg = v
				inner, _ = call.Args[1].(*ast.FuncLit)
			} else {
				if len(call.Args) < 1 {
					continue
				}
				inner, _ = call.Args[0].(*ast.FuncLit)
			}
			if inner != nil {
				collectRoutes(inner, prefix+seg, out)
			}
		}
	}
}

// newRSSTestRouter 按 router.go 的**真实**前缀建测试路由树。
//
// handler 绑定照 router.go 的注册（若与 router.go 漂移，上面的对账会红）。
func newRSSTestRouter(t *testing.T) http.Handler {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 router.go 失败：%v", err)
	}
	srcPrefix := adminSubtreePrefix(t, fset, file, "/rss-sources")
	histPrefix := adminSubtreePrefix(t, fset, file, "/rss-history")
	optPrefix := adminSubtreePrefix(t, fset, file, "/rss-options")
	// 解析失败时 adminSubtreePrefix 返回的是一句说明文字，拿去拼 chi 路由
	// 会 panic "routing pattern must begin with '/' in '(在 /api 子树里找不到…)'"，
	// 报错信息把真正的原因埋在括号里。先拦一道，失败才是人能读的。
	for _, seg := range []string{"/rss-sources", "/rss-history", "/rss-options"} {
		got := adminSubtreePrefix(t, fset, file, seg)
		if !strings.HasPrefix(got, "/") {
			t.Fatalf("RSS 路由 %s 在 router.go 的 /admin 子树里没有注册 —— "+
				"前端按 /api/admin/rss-* 调会 404", seg)
		}
	}
	if srcPrefix == histPrefix || srcPrefix == optPrefix || histPrefix == optPrefix {
		t.Fatalf("RSS 三段解析出相同前缀（%q / %q / %q）—— AST 下降写错了，守卫不可信",
			srcPrefix, histPrefix, optPrefix)
	}
	r := chi.NewRouter()
	r.Route(srcPrefix, func(r chi.Router) {
		r.Get("/", h0.rssSources)
		r.Post("/", h0.rssSourceCreate)
		r.Get("/{id}", h0.rssSourceDetail)
		r.Put("/{id}", h0.rssSourceUpdate)
		r.Delete("/{id}", h0.rssSourceDelete)
		r.Post("/{id}/sync", h0.rssSourceSync)
	})
	r.Route(histPrefix, func(r chi.Router) {
		r.Get("/", h0.rssHistory)
		r.Delete("/{id}", h0.rssHistoryDelete)
	})
	r.Post("/api/admin/rss-sync", h0.rssSyncAll)
	r.Post("/api/admin/rss-preview", h0.rssPreview)
	r.Get(optPrefix, h0.rssOptions)
	return r
}
