package api

// T13 设置项搜索索引的路由可达性与接线守卫。
//
// 复用 T12/T16/T32 留下的 AST 基建（findRouteFuncLit / collectRoutes /
// adminSubtreePrefix / caseSkeleton / mentionsSelector / decodeEnvelope）。

import (
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"litepan/internal/settings"
)

func settingsIndexRoutes() []notifyT12Route {
	return []notifyT12Route{{http.MethodGet, "/api/admin/settings/index"}}
}

// settingsIndexRegisteredPaths 从 router.go 的 /admin 闭包里抽出 settings/index 的注册。
func settingsIndexRegisteredPaths(t *testing.T) []string {
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
		if strings.Contains(p, "/settings/index") {
			sp := strings.SplitN(p, " ", 2)
			out = append(out, sp[0]+" "+skeletonPath(sp[1]))
		}
	}
	return out
}

// TestWiringSettingsIndexRoutesReachable 双向对账：router.go 注册了但没用例、
// 用例要了但 router.go 没注册，两边都红。
func TestWiringSettingsIndexRoutesReachable(t *testing.T) {
	registered := settingsIndexRegisteredPaths(t)
	covered := map[string]bool{}
	for _, rt := range settingsIndexRoutes() {
		covered[rt.method+" "+caseSkeleton(rt.path)] = true
	}
	if len(covered) == 0 {
		t.Fatal("settingsIndexRoutes() 是空的 —— 守卫自己失效了")
	}
	for _, p := range registered {
		if !covered[p] {
			t.Errorf("端点 %s 已注册，但没有对应用例", p)
		}
	}
	for _, rt := range settingsIndexRoutes() {
		want := rt.method + " " + caseSkeleton(rt.path)
		if !containsString(registered, want) {
			t.Errorf("用例要求 %s %s 可达，但 router.go 里没有注册 —— "+
				"前端 web/src/api/settings.ts 按这个路径调会 404", rt.method, rt.path)
		}
	}
}

// TestWiringSettingsIndexSitsInAdminSubtree 索引必须挂在 /admin 之下。
//
// 索引本身不含配置值，但条目就是一张功能清单；挂到 / 之下就等于
// 任何登录用户都能读到完整功能地图，管理员专属的设置名也一并泄露。
func TestWiringSettingsIndexSitsInAdminSubtree(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 router.go 失败：%v", err)
	}
	prefix := adminSubtreePrefix(t, fset, file, "/settings/index")
	if prefix != "/api/admin/settings/index" {
		t.Fatalf("settings/index 注册在 %q，期望 /api/admin/settings/index", prefix)
	}
}

// TestWiringSettingsIndexHandlerAnswers proves the handler really produces an
// index, and that it answers with a zero-value Handler.
//
// 零值 Handler 是有意选的：这个端点刻意不依赖 h.settings（理由见 handler 注释），
// 所以它**必须**在服务没装配时也答得上来。用带服务的 handler 测，
// 一旦哪天有人图方便改成读 Snapshot，路由守卫照样绿 —— 而那时这个端点
// 会在服务未就绪时静默返回空索引，⌘G 面板变成一个永远空着的框。
func TestWiringSettingsIndexHandlerAnswers(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/api/admin/settings/index", (&Handler{}).getSettingsIndex)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings/index", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/settings/index 返回 %d，期望 200：%s", rec.Code, rec.Body.String())
	}
	var payload settings.Index
	decodeEnvelope(t, rec.Body.Bytes(), &payload)
	if len(payload.Items) == 0 {
		t.Fatal("索引为空 —— 端点没真的派生索引")
	}
	// 抽查一条真实存在的键：证明它派生的不是一份编出来的清单。
	// 抽查对象选 system 分组的一项 —— 那组在通用设置表单里有真实的一行，
	// 所以「目标页 + 锚点」两件事都能被这个端点的断言看见。
	found := false
	for _, it := range payload.Items {
		if it.Key != settings.KeyLogRetentionDays {
			continue
		}
		found = true
		if it.Page != "settings" || it.Tab != "services" {
			t.Errorf("log_retention_days 指向 %q?tab=%q，期望 settings/services", it.Page, it.Tab)
		}
		if it.Anchor != "setting-"+it.Key || !it.Anchored {
			t.Errorf("log_retention_days 的锚点 = %q（anchored=%v），期望 setting-%s 且可定位",
				it.Anchor, it.Anchored, it.Key)
		}
	}
	if !found {
		t.Error("索引里没有 log_retention_days —— 索引不是从注册表派生的")
	}
}

// TestWiringSettingsIndexHandlerDelegatesToBuildIndex 端点必须把派生完全委托出去。
//
// 端点里自己拼一份 items 会立刻漂移：注册表加一个 key，索引不加，
// 用户搜不到 —— 而这条守卫会让那个改动变红。
func TestWiringSettingsIndexHandlerDelegatesToBuildIndex(t *testing.T) {
	if !mentionsSelector(t, "settings.go", "BuildIndex") {
		t.Fatal("settings.go 没有引用 settings.BuildIndex —— " +
			"端点不得自行组装索引条目")
	}
}

// TestWiringSettingsIndexFrontEndCallsThisPath 前端 API 层真的调这个路径。
func TestWiringSettingsIndexFrontEndCallsThisPath(t *testing.T) {
	dir := frontendAPIDir(t)
	if !strings.Contains(string(mustReadFile(t, dir+"/settings.ts")), "/admin/settings/index") {
		t.Fatal("web/src/api/settings.ts 里没有 /admin/settings/index —— " +
			"前端调的是别的路径，守卫以为的路径只是纸上谈兵")
	}
}
