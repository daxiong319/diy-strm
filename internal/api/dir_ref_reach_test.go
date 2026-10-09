package api

// T32 目录配置防呆提示的路由可达性守卫。
//
// 复用 T16/T12 留下的 AST 基建（findRouteFuncLit / collectRoutes /
// adminSubtreePrefix / caseSkeleton），让 router.go 自己当唯一真相。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"litepan/internal/cloudref"
)

// dirRefRoutes 描述 T32 的新端点。
func dirRefRoutes() []notifyT12Route {
	return []notifyT12Route{{http.MethodGet, "/api/admin/dir-refs"}}
}

// dirRefRegisteredPaths 从 router.go 的 /admin 闭包 AST 里抽出
// dir-refs 的 (METHOD 完整路径)。
func dirRefRegisteredPaths(t *testing.T) []string {
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
		if strings.Contains(p, "/dir-refs") {
			sp := strings.SplitN(p, " ", 2)
			out = append(out, sp[0]+" "+skeletonPath(sp[1]))
		}
	}
	return out
}

// TestWiringDirRefRoutesReachable 每个新端点都要有路由可达性用例
// （00-master.md 硬约束 8）。正反双向对账：router.go 注册了但用例没覆盖的、
// 用例写了但 router.go 没注册的，两边都红。
func TestWiringDirRefRoutesReachable(t *testing.T) {
	registered := dirRefRegisteredPaths(t)
	covered := map[string]bool{}
	for _, rt := range dirRefRoutes() {
		covered[rt.method+" "+caseSkeleton(rt.path)] = true
	}
	if len(covered) == 0 {
		t.Fatal("dirRefRoutes() 是空的 —— 守卫自己失效了")
	}
	for _, p := range registered {
		if !covered[p] {
			t.Errorf("端点 %s 已注册，但 dirRefRoutes() 里没有对应用例 —— "+
				"每个新端点都要有路由可达性用例", p)
		}
	}
	for _, rt := range dirRefRoutes() {
		want := rt.method + " " + caseSkeleton(rt.path)
		if !containsString(registered, want) {
			t.Errorf("用例要求 %s %s 可达，但 router.go 里没有这个注册 —— "+
				"前端 web/src/api/dirRefs.ts 按这个路径调会 404", rt.method, rt.path)
		}
	}
}

// TestWiringDirRefHandlerProducesHints proves the handler is actually mounted
// on a router the frontend can call, and that it answers with hint text.
//
// 光有 AST 对账不够：那条守卫只看 router.go 的**注册**，把 handler 换成
// 一个返回 200 空 JSON 的桩，它照样绿。而这个功能的全部价值在那段文字里 ——
// 空响应在前端就是「什么都不显示」，正是它要消灭的那种静默。
func TestWiringDirRefHandlerProducesHints(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 router.go 失败：%v", err)
	}
	prefix := adminSubtreePrefix(t, fset, file, "/dir-refs")
	if prefix != "/api/admin/dir-refs" {
		t.Fatalf("dir-refs 注册在 %q，期望 /api/admin/dir-refs —— "+
			"挂到 /admin 之外前端就是 404", prefix)
	}

	// Handler{} 零值即可：collectDirRefs 对两个 service 都判空。
	h := &Handler{}
	r := chi.NewRouter()
	r.Get(prefix, h.resolveDirRef)

	req := httptest.NewRequest(http.MethodGet, prefix+"?path="+urlQueryEscape("/media/影视/2026"), nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s 返回 %d，期望 200：%s", prefix, rec.Code, rec.Body.String())
	}

	var payload struct {
		Path       string                 `json:"path"`
		References cloudref.DirReferences `json:"references"`
		Refs       struct {
			EmbyLocations []string `json:"emby_locations"`
			Notes         []string `json:"notes"`
		} `json:"refs"`
		Configured bool `json:"configured"`
	}
	decodeEnvelope(t, rec.Body.Bytes(), &payload)
	if payload.Path != "/media/影视/2026" {
		t.Errorf("回显 path = %q，期望 /media/影视/2026", payload.Path)
	}
	// 零值 Handler 下没有任何媒体库根，正确行为是**沉默**而不是编一句
	// 「不在任何媒体库内」—— 没配过 ≠ 用户配错了。
	if payload.Configured {
		t.Errorf("没有任何配置时 configured 应当为 false")
	}
	if len(payload.References.Hints) != 0 {
		t.Errorf("没有任何媒体库根时给出了提示 %v —— 没配过不等于配错了，"+
			"凭空造一句警告比不提示更糟", payload.References.Hints)
	}
	if len(payload.References.Hint) != 0 {
		t.Errorf("没有提示时主提示应为空，实际 %q", payload.References.Hint)
	}
	// 边界说明必须随响应一起给，前端才能诚实地告诉用户「这里没覆盖」。
	if !containsNote(payload.Refs.Notes, "Emby/Jellyfin") {
		t.Errorf("refs.notes 里没有 Emby 索引库边界的说明，实际 = %v", payload.Refs.Notes)
	}
	if len(payload.Refs.EmbyLocations) != 0 {
		t.Errorf("EmbyLocations 应当为空（本项目不保存库位置），实际 %v", payload.Refs.EmbyLocations)
	}
}

// TestWiringDirRefHintsAppearWhenConfigured is the positive half of the test
// above: with a real media library configured, the handler must actually emit
// the warning text. 只验「没配时不说话」的话，一个永远返回空结果的桩
// 也能过 —— 那样的话功能在真实使用里等于没有。
func TestWiringDirRefHintsAppearWhenConfigured(t *testing.T) {
	h := &Handler{mediaUpgrade: newTestMediaUpgrade(t, []string{"/media/影视"}, "")}
	r := chi.NewRouter()
	r.Get("/api/admin/dir-refs", h.resolveDirRef)

	for _, tc := range []struct {
		path string
		want string
	}{
		{"/media/影视/2026", "这个目录在媒体库"},
		{"/media/影视2/某剧", "这个目录不在任何媒体库内"},
	} {
		req := httptest.NewRequest(http.MethodGet,
			"/api/admin/dir-refs?path="+urlQueryEscape(tc.path), nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET ?path=%s 返回 %d：%s", tc.path, rec.Code, rec.Body.String())
		}
		var payload struct {
			Configured bool `json:"configured"`
			References cloudref.DirReferences
		}
		decodeEnvelope(t, rec.Body.Bytes(), &payload)
		if !payload.Configured {
			t.Errorf("配了媒体库根 %s 之后 configured 仍为 false —— "+
				"collectDirRefs 没读到洗版规则", "/media/影视")
		}
		if !strings.Contains(payload.References.Hint, tc.want) {
			t.Errorf("?path=%s 的主提示 = %q，期望包含 %q",
				tc.path, payload.References.Hint, tc.want)
		}
		if payload.References.Tone != cloudref.ToneWarn && tc.want == "这个目录不在任何媒体库内" {
			t.Errorf("?path=%s 的提示级别 = %q，静默失败必须标 warn",
				tc.path, payload.References.Tone)
		}
	}
}

// TestWiringResolveDirRefCallsResolver is the 接线断言 required by
// 00-master.md: 决定行为的内部闸门必须在生产路径上真的被调用。
//
// cloudref.ResolveDirReferences 是全部判定的所在；如果 handler 绕过它自己
// 拼一句提示，这个功能会照常返回 200，前端照常渲染，看起来完全正常 ——
// 单元测试却全绿，因为它们测的是 cloudref 自己。
func TestWiringResolveDirRefCallsResolver(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dir_ref.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 dir_ref.go 失败：%v", err)
	}
	var calls int
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "resolveDirRef" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "cloudref" {
				return true
			}
			if sel.Sel.Name == "ResolveDirReferences" {
				calls++
			}
			return true
		})
	}
	if calls != 1 {
		t.Errorf("resolveDirRef 里 cloudref.ResolveDirReferences 被调用 %d 次，期望恰好 1 次 —— "+
			"绕过它自己拼提示的话，单元测试会全绿而线上判据是错的", calls)
	}
}

// TestWiringDirRefFrontendCallPathMatchesBackend pins web/src/api/dirRefs.ts 的
// 路径与 router.go 的注册。前端路径写错时唯一症状是 404 + 界面静默，
// 没有编译错误也没有 500，值得单独钉一条。
func TestWiringDirRefFrontendCallPathMatchesBackend(t *testing.T) {
	dir := filepath.Join("..", "..", "web", "src", "api")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("跳过前后端路径守卫：%s 不存在（web/ 不入库，只 checkout Go 侧时属正常）", dir)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "dirRefs.ts"))
	if err != nil {
		t.Fatalf("读取 web/src/api/dirRefs.ts 失败：%v", err)
	}
	src := squeezeSpace(string(raw))
	if !strings.Contains(src, `"/admin/dir-refs"`) {
		t.Errorf("web/src/api/dirRefs.ts 里没有 /admin/dir-refs 字面量 —— " +
			"前端与后端路径不一致时症状是 404 + 提示静默消失")
	}
}
