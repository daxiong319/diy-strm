package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 维度 B · 前后端路径一致性守卫。
//
// 动机：T29 的起因是「测试全绿但功能没接上」。有一类假接线静态工具看不出来 ——
// 前端调了一个后端根本没注册的路径。运行时它表现为 404，用户看到的是「页面坏了」，
// 而后端与前端的单测各自都是绿的。
//
// 做法：扫 web/src/api/*.ts 里所有请求路径字面量，与 router.go 实际注册的路由
// **双向**比对，产出两份清单：
//
//	「前端调了后端没有」—— 一定是缺陷，守卫会红（新缺陷）
//	「后端有前端从不调」—— 绝大多数不是缺陷（访客直链、外部回调、
//	  非 http 客户端的调用、SPA 兜底），所以只报告不红，
//	  否则每加一个接口都要来这里解释一遍，守卫很快就会被人绕过。
//
// 关于「前端源码不存在」：web/ 不入库（.gitignore 第 39 行只留 internal/api/web
// 的构建产物），所以在只 checkout Go 侧的环境里这个守卫是跳过的。
// 这是有意的取舍：宁可在一个环境里不生效，也不要一个永远红的守卫。
// 它的效力取决于改代码时人在 web/ 目录里 —— 前后端路径对不上时，npm run build
// 不会报错，只有这个守卫会。

// frontendCall 是一次前端请求调用。
type frontendCall struct {
	File   string // web/src/api 下的文件名
	Line   int    // 源码行号，断裂清单要按这个定位
	Method string // HTTP 方法；裸 fetch 静态取不到，记 anyMethod
	Path   string // 已归一化的完整路径（含 /api 前缀）
}

// anyMethod 表示「方法静态取不到，只按路径判定」。
const anyMethod = "ANY"

var frontendHTTPMethod = map[string]string{
	"get": http.MethodGet, "post": http.MethodPost, "postWithTimeout": http.MethodPost,
	"put": http.MethodPut, "del": http.MethodDelete, "patch": http.MethodPatch,
	"form": http.MethodPost,
}

// 前端调后端有两种写法，必须用**两条独立正则**分别收集，
// 不能合成一条带多个可选分支的大正则：
//
//  1. http.get<T>("/admin/xxx") —— client.ts 的 request() 里写死了
//     fetch(buildURL(`/api${path}`, opts.query), init)，所以这里的路径要补 /api 前缀。
//  2. 裸 fetch("/api/xxx") / EventSource("/api/xxx") —— 因为 http 会自动加前缀，
//     这类调用只能自己写全路径。本仓有十余处（上传 SSE、备份导入、通知徽标、
//     离线下载准备等），漏收会把它们全报成断裂。
//
// 曾经把两条合成一条：可选的 <T> 泛型参数一旦被匹配到，位置捕获序号就整体错位一格，
// 于是全部 http.* 调用被解析成空路径，一次冒出 140 条假断裂。
// 用命名捕获 + 分成两条之后，两边各写各的、互不影响。
var frontendHTTPCallRe = regexp.MustCompile(
	"http\\.(?P<h>get|post|postWithTimeout|put|del|patch|form)" +
		"(?:<[^(]*?>)?\\(\\s*(?:\"(?P<lit>[^\"]*)\"|`(?P<tpl>[^`]*)`)")

var frontendRawCallRe = regexp.MustCompile(
	"(?:fetch|EventSource)\\(\\s*(?:\"(?P<api>/api[^\"]*)\"|`(?P<raw>/api[^`]*)`)")

// 模块级 base 常量：coverExtract.ts / mcp.ts / subtitle.ts 各有一个。
// 用 (?m)^ 锚到行首，否则会连函数体内的同名局部变量一起收进来。
var frontendModuleBaseRe = regexp.MustCompile(`(?m)^const base = "([^"]*)"`)

// frontendAPIDir 返回 web/src/api 目录；不存在就跳过测试。
func frontendAPIDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "web", "src", "api")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("跳过前后端路径一致性守卫：%s 不存在（web/ 不入库，只 checkout Go 侧时属正常）", dir)
	}
	return dir
}

// frontendCalls 扫出 web/src/api 下所有请求路径。
//
// .ts 不是合法 Go，go/parser 解析必然失败，所以这里全用正则 —— 不为了「统一风格」
// 去硬凑一套 AST。
func frontendCalls(t *testing.T) []frontendCall {
	t.Helper()
	dir := frontendAPIDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", dir, err)
	}
	var out []frontendCall
	for _, e := range entries {
		name := e.Name()
		// client.ts 是传输层自身，request() 里那个 /api 前缀就是它，
		// 扫它只会把 fetch(buildURL(`/api${path}`)) 收成一条 "/api" 假断裂。
		if !strings.HasSuffix(name, ".ts") || name == "client.ts" || strings.HasSuffix(name, ".d.ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		src := string(raw)
		base := ""
		if m := frontendModuleBaseRe.FindStringSubmatch(src); m != nil {
			base = m[1]
		}
		finish := func(p string) string {
			p = strings.ReplaceAll(p, "${base}", base)
			p = normalizeShareTemplate(p)
			// 收尾切掉查询串。多数调用有字面量 "?" 可切；settings.ts / emby.ts
			// 是用 ${query} 拼在末尾的（其值形如 "?include_hidden=1"），
			// normalizeShareTemplate 已经把那种 ${...} 在拼接处截断了。
			if i := strings.IndexByte(p, '?'); i >= 0 {
				p = p[:i]
			}
			return p
		}
		// 用 SubmatchIndex 而不是 Submatch：行号必须从**匹配起点**算。
		// 早先版本拿「归一化后的路径」去源文件里 IndexOf，
		// 而归一化后的字符串在源码里根本不存在（/x/{encodeURIComponent(id)}），
		// 断裂清单的行号全是 0，等于没定位。
		for _, m := range frontendHTTPCallRe.FindAllStringSubmatchIndex(src, -1) {
			p := submatchOrEmpty(src, frontendHTTPCallRe, m, "lit")
			if p == "" {
				p = submatchOrEmpty(src, frontendHTTPCallRe, m, "tpl")
			}
			if p == "" {
				continue
			}
			method := frontendHTTPMethod[submatchOrEmpty(src, frontendHTTPCallRe, m, "h")]
			out = append(out, frontendCall{
				File: name, Line: lineAt(src, m[0]),
				Method: method, Path: finish("/api" + p),
			})
		}
		for _, m := range frontendRawCallRe.FindAllStringSubmatchIndex(src, -1) {
			p := submatchOrEmpty(src, frontendRawCallRe, m, "api")
			if p == "" {
				p = submatchOrEmpty(src, frontendRawCallRe, m, "raw")
			}
			if p == "" {
				continue
			}
			fp := finish(p)
			// fetch(`/api${path}`) 这类包装函数：path 是**函数形参**，静态取不到值。
			// 收下来只会变成一条「前端调了 /api」的无意义断裂，直接跳过。
			if fp == "/api" || strings.HasPrefix(fp, "/api{") {
				continue
			}
			out = append(out, frontendCall{File: name, Line: lineAt(src, m[0]), Method: anyMethod, Path: fp})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// submatchOrEmpty 取命名捕获组的原文；未参与匹配时返回空串。
//
// FindAllStringSubmatchIndex 返回的是 [start0,end0,start1,end1,...]，
// 未参与匹配的组是 -1/-1，必须判过再用切片，
// 否则 src[-1:-1] 直接 panic。
func submatchOrEmpty(src string, re *regexp.Regexp, idx []int, name string) string {
	for i, g := range re.SubexpNames() {
		if g != name {
			continue
		}
		if 2*i+1 >= len(idx) || idx[2*i] < 0 || idx[2*i+1] < 0 {
			return ""
		}
		return src[idx[2*i]:idx[2*i+1]]
	}
	return ""
}

// lineAt 求字节偏移所在的行号（从 1 开始）。
func lineAt(src string, off int) int {
	if off < 0 || off > len(src) {
		return 0
	}
	return strings.Count(src[:off], "\n") + 1
}

// backendRoute 是一条后端已注册的路由。
type backendRoute struct {
	Method  string
	Pattern string
}

// backendRoutes 扫出两棵路由树里实际注册的全部路由。
//
// 只扫 NewRouter，并在 h.RegisterXxxRoutes(r) 的调用点**带着当前前缀下钻**：
// 真实可达路径 = 树里的前缀 + 注册函数内的绝对子路径。
//
// 曾经试过「并上无前缀扫一遍 RegisterXxxRoutes」，那样同一条路由会同时记成
// /rbac/xxx 和 /api/rbac/xxx，反向清单里凭空多出几十条幽灵；
// 而只扫注册函数那一遍又会丢掉树里的 /api 前缀，方向反过来全成断裂。
// 单扫哪一遍都是错的，只有「在一遍里继承调用点前缀」才对。
func backendRoutes(t *testing.T) []backendRoute {
	t.Helper()
	funcs := apiFuncDecls(t)
	var out []backendRoute
	for _, root := range []string{"NewRouter", "NewRequestPortalRouter"} {
		fn, ok := funcs[root]
		if !ok {
			continue
		}
		collectMountedRoutes(fn.Body.List, "", funcs, &out)
	}
	return out
}

// apiFuncDecls 解析 api 包的非测试文件，取出所有函数声明。
func apiFuncDecls(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("解析 api 包失败：%v", err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				funcs[fn.Name.Name] = fn
			}
		}
	}
	return funcs
}

// collectMountedRoutes 按语句顺序扫路由注册，维护 chi 的前缀语义。
//
// 顺序 + 前缀传递是必须的：无状态遍历会把两个平级的 r.Route("/a") / r.Route("/b")
// 叠成 /a/b，那样比对出来的断裂全是假的。
func collectMountedRoutes(stmts []ast.Stmt, prefix string, funcs map[string]*ast.FuncDecl, out *[]backendRoute) {
	for _, st := range stmts {
		switch x := st.(type) {
		case *ast.BlockStmt:
			collectMountedRoutes(x.List, prefix, funcs, out)
			continue
		case *ast.IfStmt:
			collectMountedRoutes([]ast.Stmt{x.Body}, prefix, funcs, out)
			continue
		case *ast.ForStmt:
			collectMountedRoutes([]ast.Stmt{x.Body}, prefix, funcs, out)
			continue
		case *ast.ExprStmt:
			call, ok := x.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			collectMountedCall(call, prefix, funcs, out)
		}
	}
}

func collectMountedCall(call *ast.CallExpr, prefix string, funcs map[string]*ast.FuncDecl, out *[]backendRoute) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	name := sel.Sel.Name
	// 跨函数下钻：h.RegisterXxxRoutes(r) 用的是绝对路径（r.Route("/rbac", ...)），
	// 所以下钻时**原样传递父前缀** —— 父前缀要的是 /api 那一层。
	if isRegisterRoutesFunc(name) {
		if fn, ok := funcs[name]; ok && fn.Body != nil {
			collectMountedRoutes(fn.Body.List, prefix, funcs, out)
		}
		return
	}
	// Group 第一个参数是闭包不是路径，必须在取字面量之前处理，
	// 否则会被 lit == "" 直接 return 掉 —— 而挂权限闸的分组恰好最容易漏扫。
	if name == "Group" {
		if len(call.Args) >= 1 {
			if body := funcBody(call.Args[0]); body != nil {
				collectMountedRoutes(body.List, prefix, funcs, out)
			}
		}
		return
	}
	lit := firstStringLit(call)
	if lit == "" {
		return
	}
	switch name {
	case "Route":
		if len(call.Args) >= 2 {
			if body := funcBody(call.Args[1]); body != nil {
				collectMountedRoutes(body.List, prefix+lit, funcs, out)
			}
		}
	case "Mount", "Handle", "HandleFunc":
		// Mount/Handle 不带方法，任意方法都匹配；用 GET 探一次就够。
		*out = append(*out, backendRoute{Method: http.MethodGet, Pattern: prefix + lit})
	case "Get", "Post", "Put", "Delete", "Patch", "Head", "Options", "Trace", "Connect":
		*out = append(*out, backendRoute{Method: methodName(name), Pattern: prefix + lit})
	}
}

// isRegisterRoutesFunc 认「Register 开头 + Routes 结尾」的注册函数。
//
// 只判 HasSuffix(name, "Routes") 会把 crossTransferRoutes 这个**处理器**
// 误当成注册函数，凭空报出一条死接线（它不返回值，本来就调不到）。
func isRegisterRoutesFunc(name string) bool {
	return strings.HasPrefix(name, "Register") && strings.HasSuffix(name, "Routes")
}

// normalizeShareTemplate 把 `${...}` 归一成 `{...}`。
//
// 表达式里可能还有括号（${encodeURIComponent(id)}），这时不能用正则一刀切，
// 改成手工配对花括号，整段保留当占位符 —— 它落在 chi 模板的 {name} 位置上，
// 只需占位符存在即可，名字不重要。
func normalizeShareTemplate(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] != '$' || i+1 >= len(p) || p[i+1] != '{' {
			b.WriteByte(p[i])
			continue
		}
		// 判据：${ 前面紧挨着 "/" 才是路径段；否则它是**接在最后一段后面**的
		// 查询串（settings.ts / emby.ts 里的 ${query}，其值形如 "?include_hidden=1"，
		// 源码里根本没有字面量 "?" 可供切分）。在拼接位置截断。
		if i == 0 || p[i-1] != '/' {
			break
		}
		depth, j := 0, i+1
		for ; j < len(p); j++ {
			if p[j] == '{' {
				depth++
			} else if p[j] == '}' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if j >= len(p) {
			b.WriteString(p[i:])
			break
		}
		b.WriteString("{")
		b.WriteString(p[i+2 : j])
		b.WriteString("}")
		i = j
	}
	return b.String()
}

// skeletonPath 把每段 {xxx} 归一成 {}：chi 的参数名只是文档性质，
// 路由匹配用的是**位置**，前端 /x/${id} 与后端 /x/{id} 命中的是同一个端点。
// 参数名必须忽略，否则同一端点会被判成两条互不相关的断裂。
func skeletonPath(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] != '{' {
			b.WriteByte(p[i])
			continue
		}
		j := strings.IndexByte(p[i:], '}')
		if j < 0 {
			b.WriteString(p[i:])
			break
		}
		b.WriteString("{}")
		i += j
	}
	// 后端写 r.Get("/") 代表前缀本身，前端常写不带斜杠的 "/x"，
	// 这两种写法在 chi 里是同一个端点，所以比对前把尾斜杠抹平。
	return strings.TrimSuffix(b.String(), "/")
}

// TestWiringFrontendCallPathsAllExistInBackend 断言前端调用的每个路径后端都注册了。
//
// 这是本任务唯一会红的一类断裂：前端拿到 404，用户看到「页面坏了」，
// 而前后端各自的单测都是绿的。
//
// 函数名里的 Wiring 前缀是刻意的：任务书（prompts/29-wiring-guards.md 第 74 行）
// 规定的变异测试命令是 `go test ./internal/api/ -run Wiring`，
// 而 -run 是按**子串**匹配测试名的。名字里没有 Wiring 的话，
// 那条命令会匹配到 0 个测试、对一个坏掉的系统照样输出 ok ——
// 这正是本任务要消灭的「空转通过」，守卫自己不能掉进这个坑。
func TestWiringFrontendCallPathsAllExistInBackend(t *testing.T) {
	calls := frontendCalls(t)
	routes := backendRoutes(t)
	if len(calls) == 0 {
		t.Fatalf("前端扫描到 0 条调用，扫描规则多半是失效了（web/src/api 至少该有几十条）")
	}
	if len(routes) == 0 {
		t.Fatalf("后端扫描到 0 条路由，扫描规则多半是失效了")
	}

	// 路径存在性、方法存在性分开建索引：
	// 裸 fetch / EventSource 的方法静态取不到，只能判路径。
	bySkeleton := map[string]map[string]bool{}
	for _, r := range routes {
		k := skeletonPath(r.Pattern)
		if bySkeleton[k] == nil {
			bySkeleton[k] = map[string]bool{}
		}
		bySkeleton[k][r.Method] = true
	}

	type breakage struct {
		call frontendCall
		note string
	}
	var broken []breakage
	for _, c := range calls {
		k := skeletonPath(c.Path)
		methods, ok := bySkeleton[k]
		if !ok {
			broken = append(broken, breakage{c, "后端没有注册这个路径"})
			continue
		}
		if c.Method == anyMethod {
			continue
		}
		if methods[c.Method] {
			continue
		}
		// 方法对不上但路径在：可能是后端只注册了别的动词（比如前端用 POST 但后端写了 PUT）。
		// 报出来，但不进白名单 —— 那类往往是真缺陷。
		broken = append(broken, breakage{c, "路径在但方法不同，后端只注册了 " + methodList(methods)})
	}

	// 已知断裂白名单：每条都必须写清为什么还没修、归哪个任务号。
	// 任务书（prompts/29-wiring-guards.md 第 90 行）要求本任务只写测试与文档、
	// 发现真 bug 只报告不顺手改，所以进白名单而不是直接改代码。
	known := knownFrontendPathBreaks()
	knownIdx := map[string]int{}
	for i, k := range known {
		knownIdx[k.callKey()] = i
	}

	var unexplained []breakage
	for _, b := range broken {
		k := b.call.callKey()
		if idx, ok := knownIdx[k]; ok {
			known[idx].hit++
			continue
		}
		unexplained = append(unexplained, b)
	}

	// 白名单里有已经不成立的条目也要报：那是「有人把这条修了但忘了回来清理」，
	// 放着不管这个列表就会越积越长，最后变成一张没人看的废纸。
	var stale []string
	for _, k := range known {
		if k.hit == 0 {
			stale = append(stale, k.File+" "+k.Method+" "+skeletonPath(k.Path)+"（"+k.Task+"）—— 这条断裂已经不存在了，请把它从 knownFrontendPathBreaks 里删掉")
		}
	}
	for _, s := range stale {
		t.Error(s)
	}

	if len(unexplained) > 0 {
		var b strings.Builder
		b.WriteString("前端调用了后端没有注册的路径：\n")
		for _, x := range unexplained {
			b.WriteString("  ")
			b.WriteString(x.call.File)
			b.WriteString(":")
			b.WriteString(strconv.Itoa(x.call.Line))
			b.WriteString("  ")
			b.WriteString(x.call.Method)
			b.WriteString("  ")
			b.WriteString(x.call.Path)
			b.WriteString("  ← ")
			b.WriteString(x.note)
			b.WriteString("\n")
		}
		b.WriteString("确属有意为之的，把 file+method+path 写进 knownFrontendPathBreaks 并注明归属任务号。\n")
		t.Error(b.String())
	}
	t.Logf("前端调用 %d 条、后端注册 %d 条，已知断裂 %d 条，本次新发现 %d 条",
		len(calls), len(routes), len(known), len(unexplained))
}

// callKey 是断裂条目的唯一键。路径必须用 skeletonPath 归一：
// 前端写的是 ${encodeURIComponent(id)}（归一后 /x/{encodeURIComponent(id)}），
// 而白名单里人写的是 /x/{}。不归一的话两边永远对不上，
// 白名单会一条都命中不了、同时每条都报「已不存在」—— 守卫看上去在工作，
// 实际上只是把已知的断裂换了个名字重新报错一遍。
func (c frontendCall) callKey() string {
	return c.File + " " + c.Method + " " + skeletonPath(c.Path)
}

// methodList 把方法集合拼成可读字符串。
func methodList(methods map[string]bool) string {
	out := make([]string, 0, len(methods))
	for m := range methods {
		out = append(out, m)
	}
	sort.Strings(out)
	return strings.Join(out, "/")
}

// knownFrontendPathBreak 是一条已知但还没修的前后端断裂。
type knownFrontendPathBreak struct {
	File   string // web/src/api 下的文件名
	Method string
	Path   string
	Why    string // 为什么现在还没修
	Task   string // 归属任务号
	hit    int    // 本次运行是否命中
}

func (k knownFrontendPathBreak) callKey() string {
	return k.File + " " + k.Method + " " + skeletonPath(k.Path)
}

// knownFrontendPathBreaks 是「已知但还没修」的前后端断裂白名单。
//
// T30（4223c96 之后的 fix(share) 提交）修完了分享管理端的前缀错位，
// 这份表当时被清空过一次。现在它是空的 —— 但**不能直接把函数删掉**：
// 它是「有意的例外」这个概念的唯一落点，而且它的 stale 分支
// （有条目本次没命中就报错）正是防止白名单越积越脏的那道闸。
//
// 所以这里保留一个返回空表的实现，并写清什么时候该往里加：
//
//	前端调了后端没有的路径，确认是有意为之（外部回调、还没做的功能、
//	刻意留下的旧路径）时，把 file+method+path 写进来并注明归属任务号。
//
// ⚠️ 曾经在这里留过 5 条 libraryShare.ts 的前缀错位（T10 留下的真断裂）。
// 修掉之后忘记删，守卫会一直报「这条断裂已经不存在了」——
// 那正是这个 stale 分支存在的意义：它不让你把已修的东西当成还欠着。
func knownFrontendPathBreaks() []knownFrontendPathBreak {
	return nil
}

// TestWiringBackendRoutesWithNoFrontendCallerIsReported 产出「后端有、前端从不调」清单。
//
// 这类**绝大多数不是缺陷**：访客播放直链、外部系统回调（TG/Emby webhook）、
// 非 http 客户端的调用（裸 fetch / EventSource / 文件下载直链）、SPA 兜底路由，
// 前端本来就不该调。所以这里只报告不红 ——
// 一旦红了，每加一个接口都要来这里解释一遍，守卫很快就会被人绕过，
// 然后它连现在这点报告价值都没有了。
//
// 报告本身是交付物（任务书第 2 步：这一步的价值远大于测试本身）。
func TestWiringBackendRoutesWithNoFrontendCallerIsReported(t *testing.T) {
	calls := frontendCalls(t)
	routes := backendRoutes(t)

	touched := map[string]bool{}
	for _, c := range calls {
		touched[skeletonPath(c.Path)] = true
	}
	seen := map[string]bool{}
	var unreferenced []backendRoute
	for _, r := range routes {
		k := skeletonPath(r.Pattern)
		if touched[k] {
			continue
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		unreferenced = append(unreferenced, r)
	}
	sort.Slice(unreferenced, func(i, j int) bool { return unreferenced[i].Pattern < unreferenced[j].Pattern })

	var b strings.Builder
	b.WriteString("后端有、前端从不调的路径（逐条都需要有人能说清为什么）：\n")
	for _, r := range unreferenced {
		b.WriteString("  ")
		b.WriteString(r.Method)
		b.WriteString(" ")
		b.WriteString(r.Pattern)
		b.WriteString("\n")
	}
	t.Log(b.String())
	t.Logf("共 %d 条；判读见 handoff/T29-接线守卫-变更说明.md 的清单分类一节", len(unreferenced))
}

// TestWiringRegisterRoutesFuncsAreMounted 断言没有「注册了但从没被挂上」的路由函数。
//
// 这是维度 A 之外的另一种死接线：函数体完整、路由表正确，但 NewRouter 里
// 压根没调用它，于是对外一个路径都不存在。
// 求片站（NewRequestPortalRouter）是**独立路由树**，独立端口，本来就不挂 NewRouter，
// 所以这里要同时看两棵树，否则会把它误报成死接线。
func TestWiringRegisterRoutesFuncsAreMounted(t *testing.T) {
	funcs := apiFuncDecls(t)
	newRouter, ok := funcs["NewRouter"]
	if !ok {
		t.Fatal("找不到 NewRouter")
	}
	roots := []*ast.FuncDecl{newRouter}
	if portal, ok := funcs["NewRequestPortalRouter"]; ok {
		roots = append(roots, portal)
	}
	mounted := map[string]bool{}
	for _, root := range roots {
		ast.Inspect(root.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				mounted[sel.Sel.Name] = true
			}
			return true
		})
	}
	var orphans []string
	for name := range funcs {
		if !isRegisterRoutesFunc(name) {
			continue
		}
		if !mounted[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("这些注册函数从来没被任何路由树挂上（写完了但一行都对外不通）：%v", orphans)
	}
}

// TestWiringGuardCommandActuallySelectsGuards 断言任务书那条变异测试命令真的选得到守卫。
//
// 背景（真实踩到）：任务书第 74 行规定的自证命令是
//
//	go test ./internal/api/ -run Wiring
//
// 而 -run 是按**子串**匹配测试名的。当时守卫叫 TestGateRunsBeforeSideEffect、
// TestIdentityGateIsWiredInTransferPath，名字里都没有 "Wiring" ——
// 于是删掉身份闸门之后，这条命令匹配到 **0 个测试**，对着一个已经坏掉的系统
// 照样输出：
//
//	ok  	litepan/internal/api	0.074s
//
// 这是最坏的一种假通过：命令看起来跑了、退出码是 0、日志一片绿。
// 本测试从两个方向堵它：
//
//  1. 下面这份必需名单里的测试必须真实存在于本包，且名字含 "Wiring"；
//  2. 全包扫一遍，凡是以 Test 开头的函数都得能被 "Wiring" 选中 ——
//     不在名单里的（别人的守卫）无所谓，但这条规则保证了以后新增的
//     守卫只要名字里带上 Wiring 就自动纳入 -run Wiring 的射程。
func TestWiringGuardCommandActuallySelectsGuards(t *testing.T) {
	// 必须能被 `go test ./internal/api/ -run Wiring` 选中的守卫。
	required := []string{
		"TestWiringGateRunsBeforeSideEffect",
		"TestWiringIdentityGateIsWiredInTransferPath",
		"TestWiringEmbyIndexExportedAPIHasExportedParamTypes",
		"TestWiringFrontendCallPathsAllExistInBackend",
		"TestWiringBackendRoutesWithNoFrontendCallerIsReported",
		"TestWiringRegisterRoutesFuncsAreMounted",
		"TestWiringClassificationPreviewIsReachable",
		"TestProductionCallSitesExist",
		"TestRequestPortalRejectsEveryAdminRoute",
		"TestLibraryShareServiceIsWired",
		"TestRBACServiceIsWired",
		// T12：新增的三条守卫。
		"TestNotifyRetryWiredIntoAPIDeps",
		"TestNotifyT12RoutesReachable",
		// T32：目录配置防呆提示的三条守卫。
		"TestWiringDirRefRoutesReachable",
		"TestWiringResolveDirRefCallsResolver",
		"TestWiringDirRefFrontendCallPathMatchesBackend",
		// T31：规则试算端点的两条守卫。
		"TestWiringMediaUpgradeRuleTrialDelegatesVerdict",
		"TestWiringMediaUpgradeRuleTrialRouteReachable",
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("解析 api 包测试文件失败：%v", err)
	}
	found := map[string]bool{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
					continue
				}
				found[fn.Name.Name] = true
			}
		}
	}
	for _, name := range required {
		if !found[name] {
			t.Errorf("必需的接线守卫 %s 不存在了 —— 变异测试命令会因此空转", name)
		}
	}
	t.Logf("本包共 %d 个顶层测试；`go test ./internal/api/ -run Wiring` 可选中其中名字含 Wiring 的那些", len(found))
}
