package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// T11 播放监控 + 观影报告的路由可达性守卫。
//
// 背景是 T10 的踩坑：library-share 的管理路由挂到了 /admin 子树**之外**，
// 真实路径成了 /api/library-shares/*，而前端按 /api/admin/ 写，
// 五个操作全部 404 —— 而当时的测试全绿，因为没有任何一条用例问过
// 「前端写的那个路径真的能通吗」。
//
// 所以这里不重复写「路由已注册」这种同义反复的断言，而是钉两件事：
//   1. 六个端点各自的**真实路径**（拼完整前缀后打请求，不通就红）；
//   2. `/play-monitor` 注册在 `/admin` 子树**内**（AST 判断，见
//      shareRoutesInsideAdminSubtree 的注释：靠缩进或行号判断会得出假答案）。
//
// 「六个端点都通」这条只能证明路由表里有它们，证明不了它们在
// /admin 之下；反过来子树断言只证明注册位置，证明不了每条子路径都写对。
// 两条一起才把「前端按 /api/admin/play-monitor/… 写就一定能通」钉住。

// playMonitorRoute 描述一个前端会调用的播放监控端点。
type playMonitorRoute struct {
	method string
	path   string // 相对 /api 的完整路径（含 /admin 前缀）
	want   int
}

func playMonitorRoutes() []playMonitorRoute {
	return []playMonitorRoute{
		{http.MethodGet, "/api/admin/play-monitor/sessions", http.StatusOK},
		{http.MethodGet, "/api/admin/play-monitor/traffic", http.StatusOK},
		{http.MethodPost, "/api/admin/play-monitor/traffic/clear", http.StatusOK},
		{http.MethodGet, "/api/admin/play-monitor/report", http.StatusOK},
		{http.MethodGet, "/api/admin/play-monitor/report/chart", http.StatusOK},
		{http.MethodGet, "/api/admin/play-monitor/options", http.StatusOK},
	}
}

// TestPlayMonitorRoutesReachableEveryEndpointHasACase
// 每个新端点都要有路由可达性用例（00-master.md 硬约束 8）。
//
// 这里做两件事：把上面那张表和「真实路由表」对账（路由表里有、
// 用例里没有 = 新端点漏了可达性用例），再逐条打请求。
func TestPlayMonitorRoutesReachableEveryEndpointHasACase(t *testing.T) {
	router := newPlayMonitorTestRouter(t)

	// 1) 对账：路由表里的 /play-monitor 端点，每个都得有用例。
	registered := playMonitorRegisteredPaths(t)
	covered := map[string]bool{}
	for _, rt := range playMonitorRoutes() {
		covered[rt.method+" "+rt.path] = true
	}
	for _, p := range registered {
		if !covered[p] {
			t.Errorf("端点 %s 已注册，但 playMonitorRoutes() 里没有对应用例 —— "+
				"每个新端点都要有路由可达性用例", p)
		}
	}

	// 2) 逐条打请求。注意这里 401/403/503 都算"通"——
	//    未登录时路由可达就会被鉴权挡下，依赖未装配则走 ensureServiceReady，
	//    而 404/405 才是"路径写错了/没挂上"，那才是这条用例要抓的东西。
	for _, rt := range playMonitorRoutes() {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Fatalf("%s %s 返回 404 —— 路由没挂上，或路径与 web/src/api/playMonitor.ts 不一致",
					rt.method, rt.path)
			}
			if rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("%s %s 返回 405 —— 方法与前端调用方式不一致", rt.method, rt.path)
			}
		})
	}
}

// parsePlayMonitorBody 取回 r.Route("/play-monitor", ...) 的那个闭包体。
func parsePlayMonitorBody(fset *token.FileSet, file *ast.File, t *testing.T) *ast.FuncLit {
	t.Helper()
	var out *ast.FuncLit
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || calleeName(call) != "Route" || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if val, err := strconv.Unquote(lit.Value); err != nil || val != "/play-monitor" {
			return true
		}
		if fn, ok := call.Args[1].(*ast.FuncLit); ok {
			out = fn
			return false
		}
		return true
	})
	if out == nil {
		t.Fatal("router.go 里找不到 r.Route(\"/play-monitor\", ...) 注册")
	}
	return out
}

// playMonitorRegisteredPaths 用 AST 从 router.go 里抽出 /play-monitor
// 子树下所有 (METHOD, 完整路径)。
//
// 前缀由 playMonitorRealPrefix 给出（它自己也是从 AST 推出来的），
// 路径不是猜的 —— 猜前缀就绕回了「测试通过但前端 404」的老问题。
func playMonitorRegisteredPaths(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 router.go 失败：%v", err)
	}
	prefix := playMonitorRealPrefix(t)
	var out []string
	var walk func(p string, fn *ast.FuncLit)
	walk = func(p string, fn *ast.FuncLit) {
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
				out = append(out, strings.ToUpper(kind)+" "+p+seg)
			case "Route", "Group":
				if len(call.Args) < 1 {
					continue
				}
				seg := ""
				var inner *ast.FuncLit
				if kind == "Route" {
					if len(call.Args) < 2 {
						continue
					}
					lit, ok := call.Args[0].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					if seg, err = strconv.Unquote(lit.Value); err != nil {
						continue
					}
					inner, _ = call.Args[1].(*ast.FuncLit)
				} else {
					inner, _ = call.Args[0].(*ast.FuncLit)
				}
				if inner != nil {
					walk(p+seg, inner)
				}
			}
		}
	}
	// /play-monitor 本身的闭包：前缀补上这一段再往下走。
	sub := parsePlayMonitorBody(fset, file, t)
	walk(prefix, sub)
	return out
}

// TestPlayMonitorRoutesInsideAdminSubtree /play-monitor 必须落在
// r.Route("/admin", ...) 闭包内。
//
// 用 AST 找子树边界而不是缩进/行号：`r.Route("/admin", ...)` 外层与
// `r.Group(func(r){ r.Use(h.requireAdmin) ... })` 里缩进完全一样，
// 按行判断会得出「在 /admin 里面」的假答案 —— 那正是 T10 错位的形态。
func TestPlayMonitorRoutesInsideAdminSubtree(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 router.go 失败：%v", err)
	}
	inside := routesInsideAdminSubtree(fset, file, "play-monitor")
	if len(inside) == 0 {
		t.Fatal("router.go 的 r.Route(\"/admin\", ...) 子树里没有 /play-monitor 注册。\n" +
			"播放监控必须挂在 /admin 子树里 —— 真实路径是 /api/admin/play-monitor/*，" +
			"与 web/src/api/playMonitor.ts 一致（T10 的 library-share 就是在 /admin 之外挂的，" +
			"前端按 /admin/ 写，五个操作全 404）")
	}
	if len(inside) > 1 {
		t.Fatalf("/play-monitor 在 /admin 子树内注册了 %d 次（第 %v 行），期望恰好 1 次。\n"+
			"挂两处 = 同一功能两个入口，日后改一处漏一处", len(inside), inside)
	}
}

// routesInsideAdminSubtree 返回 r.Route("/admin", ...) 闭包体**顶层语句**里
// r.Route("<segment>", ...) 的 segment 匹配 needle 的行号。
//
// 只看顶层语句：嵌在更深一层的注册同样在这个子树内，但 /admin 下的
// 其它注册都在顶层，递归进去会让「挪进某个子组」也算通过。
func routesInsideAdminSubtree(fset *token.FileSet, file *ast.File, needle string) []int {
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
			if !ok || calleeName(inner) != "Route" || len(inner.Args) < 2 {
				continue
			}
			seg, ok := inner.Args[0].(*ast.BasicLit)
			if !ok || seg.Kind != token.STRING {
				continue
			}
			if val, err := strconv.Unquote(seg.Value); err != nil || val != "/"+needle {
				continue
			}
			out = append(out, fset.Position(inner.Pos()).Line)
		}
		return false
	})
	return out
}

// newPlayMonitorTestRouter 从 router.go 的 AST 里长出测试路由树。
//
// ⚠️ 这里**不能照抄** router.go 的注册来建测试树：照抄出来的树
// 和 router.go 是两份独立的真相 —— 把 router.go 里的路由挪错位置
// （T10 的 library-share 就是这么错的）时，照抄版照样全绿。
// 所以这里让 router.go 自己成为唯一真相：从它的 AST 里读出
// /api/admin 之后的完整前缀，再照着真实前缀拼树。
func newPlayMonitorTestRouter(t *testing.T) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Route(playMonitorRealPrefix(t), func(r chi.Router) {
		r.Get("/sessions", h0.playMonitorSessions)
		r.Get("/traffic", h0.playMonitorTraffic)
		r.Post("/traffic/clear", h0.playMonitorTrafficClear)
		r.Get("/report", h0.playReport)
		r.Get("/report/chart", h0.playReportChart)
		r.Get("/options", h0.playMonitorOptions)
	})
	return r
}

// playMonitorRealPrefix 算出 router.go 里 /play-monitor 的**真实**父前缀。
//
// 从 r.Route("/api") 开始逐层下降，记录中间经过的 Route 段，
// 直到命中 "/play-monitor" 为止 —— 于是它返回 "/api/admin/play-monitor"。
// 路由一旦被挪到 /admin 之外，这里就返回 "/api/play-monitor"，
// 打请求随之 404，守卫转红。
func playMonitorRealPrefix(t *testing.T) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 router.go 失败：%v", err)
	}
	var found string
	var descend func(path string, fn *ast.FuncLit) bool
	descend = func(path string, fn *ast.FuncLit) bool {
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
			// Route 加一段前缀；Group 不加（它只挂中间件）。
			// 两条都得走进去：/play-monitor 落在 /admin 里面那层
			// r.Group(中间件) 的闭包里，只认 Route 会漏掉它。
			//
			// ⚠️ 两者参数个数不同：Route(路径, 闭包) 是两个，
			// Group(闭包) 只有一个 —— 用统一的 len(Args)>=2 判会把
			// Group 全部漏掉，表现是"router.go 里没有这个路由"。
			var seg string
			var fn *ast.FuncLit
			switch kind {
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
				fn, ok = call.Args[1].(*ast.FuncLit)
				if !ok {
					continue
				}
			case "Group":
				if len(call.Args) < 1 {
					continue
				}
				fn, ok = call.Args[0].(*ast.FuncLit)
				if !ok {
					continue
				}
			default:
				continue
			}
			if seg == "/play-monitor" {
				found = path + seg
				return true
			}
			if descend(path+seg, fn) {
				return true
			}
		}
		return false
	}
	ast.Inspect(file, func(n ast.Node) bool {
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
			return !descend("/api", fn)
		}
		return true
	})
	if found == "" {
		t.Fatal("router.go 的 /api 子树里没有 /play-monitor 注册")
	}
	return found
}

// h0 是路由可达性用例用的空 Handler（全部依赖为 nil）。
// 路由表照抄 router.go 的注册 —— 若这里与 router.go 漂移，
// TestPlayMonitorRoutesReachableEveryEndpointHasACase 的对账会红。
var h0 = &Handler{}

// TestPlayMonitorChartURLIsCoveredByAPathGuard 补上通用守卫的盲区。
//
// wiring_frontend_paths_test.go 的 TestWiringFrontendCallPathsAllExistInBackend
// 用正则扫 web/src/api 下的**字面量**路径，只认
// http.get<T>("/admin/…") 和裸 fetch("/api/…") 两种写法。
// 而排行图地址是用 URLSearchParams 拼出来的
// （web/src/api/playMonitor.ts 的 playReportChartURL 返回
// `/api/admin/play-monitor/report/chart?${q.toString()}`），
// 正则一条都收不到 —— 也就是说这个端点被通用守卫漏掉了：
// 把它改名，前端图直接 404，而全套测试仍然全绿（已实测：
// 把 router.go 的 /report/chart 改成 /report-chart，守卫通过）。
//
// 这里单独钉一条：通用守卫收不到的那些前缀，必须在这里对账。
// 用通用守卫的 backendRoutes 索引，不自己抄一份路由表。
func TestPlayMonitorChartURLIsCoveredByAPathGuard(t *testing.T) {
	dir := frontendAPIDir(t)

	// 1) 前端源码里必须真的出现这个前缀。
	//    从 Go 侧把路径拼出来再在 web/src 里找，而不是把路径写在测试里 ——
	//    写死一份的话，前端改路径而测试没改，守卫就成了同义反复。
	const fnName = "playReportChartURL"
	apiDir, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", dir, err)
	}
	var chartFn string
	for _, e := range apiDir {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", e.Name(), err)
		}
		src := string(raw)
		if !strings.Contains(src, fnName) {
			continue
		}
		// 取 `return `/api/…` 这行里的前缀。
		for _, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "return `/api/") {
				continue
			}
			end := strings.Index(trimmed, "`")
			if end < 0 {
				continue
			}
			chartFn = strings.TrimSuffix(strings.TrimPrefix(trimmed, "return `"), "`")
			// 砍掉 ?${…} 之后的查询串，只留路径骨架。
			if q := strings.Index(chartFn, "?"); q >= 0 {
				chartFn = chartFn[:q]
			}
		}
	}
	if chartFn == "" {
		t.Fatalf("web/src/api 下没找到 %s 里以 /api/ 开头的返回路径 —— "+
			"要么函数被改名/挪走，要么改用了别的取址方式；"+
			"请同步更新本守卫，别让它默默失效", fnName)
	}

	// 2) 这个前缀必须在后端路由表里真的注册了，且方法对得上。
	//    复用通用守卫的索引，避免在测试里再抄一份路由表。
	skeleton := skeletonPath(chartFn)
	var methods []string
	for _, r := range backendRoutes(t) {
		if skeletonPath(r.Pattern) == skeleton {
			methods = append(methods, r.Method)
		}
	}
	if len(methods) == 0 {
		t.Fatalf("前端 %s() 返回 %s，但后端没有注册这个路径 —— 图会 404，"+
			"而通用守卫按正则扫不到这条路径（它不是字面量）", fnName, chartFn)
	}
	found := false
	for _, m := range methods {
		if m == http.MethodGet {
			found = true
		}
	}
	if !found {
		t.Fatalf("前端 %s() 返回 %s（GET），后端只注册了 %s —— "+
			"浏览器会拿到 405", fnName, chartFn, strings.Join(methods, "/"))
	}
	t.Logf("已覆盖：%s → %s（GET）", fnName, chartFn)
}
