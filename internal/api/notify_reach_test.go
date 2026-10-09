package api

// T12 通知场景化与 Webhook 补发的路由可达性守卫。
//
// 复用 T16 留下的 AST 基建（findRouteFuncLit / collectRoutes / skeletonPath），
// 让 router.go 自己当唯一真相 —— 照抄注册会写出第二份真相，router.go 挪错
// 位置时照抄版照样全绿（T10 的 library-share 教训）。

import (
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// notifyT12Route 描述 T12 的 5 个新端点。
type notifyT12Route struct {
	method string
	path   string
}

func notifyT12Routes() []notifyT12Route {
	return []notifyT12Route{
		{http.MethodGet, "/api/admin/notify-scenes"},
		{http.MethodGet, "/api/admin/notify-retries"},
		{http.MethodGet, "/api/admin/notify-retries/7"},
		{http.MethodPost, "/api/admin/notify-retries/7/redrive"},
		{http.MethodPost, "/api/admin/notify-retries/clear"},
	}
}

// notifyT12RegisteredPaths 从 router.go 的 /admin 闭包 AST 里抽出所有
// notify-scenes / notify-retries 的 (METHOD 完整路径)。
func notifyT12RegisteredPaths(t *testing.T) []string {
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
		if strings.Contains(p, "/notify-scenes") || strings.Contains(p, "/notify-retries") {
			sp := strings.SplitN(p, " ", 2)
			out = append(out, sp[0]+" "+skeletonPath(sp[1]))
		}
	}
	return out
}

// TestNotifyT12RoutesReachable 每个新端点都要有路由可达性用例
// （00-master.md 硬约束 8）。正反双向对账：router.go 里注册了但用例没覆盖的，
// 以及用例写了但 router.go 没注册的，两边都会红。
func TestNotifyT12RoutesReachable(t *testing.T) {
	router := newNotifyT12TestRouter(t)

	registered := notifyT12RegisteredPaths(t)
	covered := map[string]bool{}
	for _, rt := range notifyT12Routes() {
		covered[rt.method+" "+caseSkeleton(rt.path)] = true
	}
	if len(covered) == 0 {
		t.Fatal("notifyT12Routes() 是空的 —— 守卫自己失效了")
	}
	for _, p := range registered {
		if !covered[p] {
			t.Errorf("端点 %s 已注册，但 notifyT12Routes() 里没有对应用例 —— "+
				"每个新端点都要有路由可达性用例", p)
		}
	}
	for _, rt := range notifyT12Routes() {
		if !containsString(registered, rt.method+" "+caseSkeleton(rt.path)) {
			t.Errorf("用例要求 %s %s 可达，但 router.go 里没有这个注册 —— "+
				"前端按这个路径调会 404", rt.method, rt.path)
		}
	}

	for _, rt := range notifyT12Routes() {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Fatalf("%s %s 返回 404 —— 路由没挂上，或路径与 web/src/api/notifyChannels.ts 不一致",
					rt.method, rt.path)
			}
			if rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("%s %s 返回 405 —— 方法与前端调用方式不一致", rt.method, rt.path)
			}
		})
	}
}

// newNotifyT12TestRouter 按 router.go 的**真实**前缀建测试路由树。
func newNotifyT12TestRouter(t *testing.T) http.Handler {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 router.go 失败：%v", err)
	}
	scenePrefix := adminSubtreePrefix(t, fset, file, "/notify-scenes")
	retryPrefix := adminSubtreePrefix(t, fset, file, "/notify-retries")
	// adminSubtreePrefix 解析失败时返回一句说明文字，拿去拼 chi 路由会 panic。
	for name, got := range map[string]string{"/notify-scenes": scenePrefix, "/notify-retries": retryPrefix} {
		if !strings.HasPrefix(got, "/") {
			t.Fatalf("路由 %s 在 router.go 的 /admin 子树里没有注册 —— "+
				"前端按 /api/admin/notify-* 调会 404", name)
		}
	}
	if scenePrefix == retryPrefix {
		t.Fatalf("两个前缀解析成同一个（%q）—— AST 下降写错了，守卫不可信", scenePrefix)
	}
	h0 := &Handler{}
	r := chi.NewRouter()
	r.Route(scenePrefix, func(r chi.Router) {
		r.Get("/", h0.notifySceneList)
	})
	r.Route(retryPrefix, func(r chi.Router) {
		r.Get("/", h0.notifyRetryList)
		r.Get("/{id}", h0.notifyRetryDetail)
		r.Post("/{id}/redrive", h0.notifyRetryRedrive)
		r.Post("/clear", h0.notifyRetryClear)
	})
	return r
}
