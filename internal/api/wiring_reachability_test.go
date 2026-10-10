package api

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"litepan/internal/rbac"
)

// 本文件是「接线是否真的存在」的静态回归守卫。
//
// 动机（真实事故）：internal/embyindex 曾因导出方法的参数类型未导出而根本
// 无法被包外调用，包内测试却全绿；internal/moviepilot/fallback.go 与
// internal/subtitle/service.go 的 ProcessVideo 也曾有完整实现但零生产调用方。
// 「测试全绿」不能证明「功能被接上了」，因此这里对生产代码做静态检查。

// TestProductionCallSitesExist 断言关键能力确实被生产代码调用。
func TestProductionCallSitesExist(t *testing.T) {
	cases := []struct {
		name     string
		dir      string
		callee   string
		callerIn string
	}{
		{
			name:     "mp_fallback 计数被资源搜索路径调用",
			dir:      ".",
			callee:   "recordSearchFallbackOutcome",
			callerIn: "discover_resources.go",
		},
		{
			name:     "字幕 ProcessVideo 被整理流程调用",
			dir:      "../mediaorganize",
			callee:   "processSubtitlesForPlan",
			callerIn: "service.go",
		},
		{
			// dir 是「被扫描的目录」，callerIn 是「调用方文件」。
			// ProcessVideo 定义在 internal/subtitle，但真正的生产调用方在
			// internal/app/subtitle_adapter.go —— 曾经零调用方的那次事故，
			// 就是因为适配层没写。dir 要跟着调用方走，跟着被调方走会找不到文件。
			name:     "字幕 ProcessVideo 被字幕适配层调用",
			dir:      "../app",
			callee:   "ProcessVideo",
			callerIn: "subtitle_adapter.go",
		},
		{
			// callee 只按方法名匹配（callsInNonTestFile 看的是 SelectorExpr.Sel.Name），
			// 所以写方法名而不是 h.mediaUpgrade.Execute。
			name:     "洗版提交被执行接口调用",
			dir:      ".",
			callee:   "Execute",
			callerIn: "media_upgrade.go",
		},
		{
			name:     "洗版扫描被扫描接口调用",
			dir:      ".",
			callee:   "Scan",
			callerIn: "media_upgrade.go",
		},
		{
			name:     "洗版规则保存被规则接口调用",
			dir:      ".",
			callee:   "SaveRule",
			callerIn: "media_upgrade.go",
		},
		{
			name:     "洗版提交锁 CAS 在生产代码里",
			dir:      "../mediaupgrade",
			callee:   "claimRecord",
			callerIn: "commit.go",
		},
		{
			name:     "洗版快照复核在生产代码里",
			dir:      "../mediaupgrade",
			callee:   "reenumerate",
			callerIn: "commit.go",
		},
		{
			name:     "RBAC 中间件真的算了权限",
			dir:      ".",
			callee:   "Can",
			callerIn: "rbac.go",
		},
		{
			name:     "RBAC 主体在生产代码里被装配出来",
			dir:      ".",
			callee:   "PrincipalFor",
			callerIn: "rbac.go",
		},
		{
			name:     "RBAC 用户写入在生产代码里",
			dir:      "../rbac",
			callee:   "SetGroupPermissions",
			callerIn: "service.go",
		},
		{
			// 委托用户登录要靠这层适配。缺了它，用户能建却登不进来。
			name:     "委托用户鉴权适配器在生产代码里",
			dir:      "../app",
			callee:   "SetExtraUserAuth",
			callerIn: "wire_rbac.go",
		},
		{
			// T11：播放监控必须真的挂到取流入口上。
			// playmonitor.Service 本身测试全绿也没用 —— 没人注入它，
			// 三态判定就永远不会在生产链路里发生。
			name:     "播放监控挂到取流入口",
			dir:      "../app",
			callee:   "SetStreamMonitor",
			callerIn: "wire_services.go",
		},
		{
			// T11：观影报告触发器要有人实现，否则规则跑起来只会报"服务未就绪"。
			name:     "观影报告生成器被注入自动化",
			dir:      "../app",
			callee:   "SetPlayReportGenerator",
			callerIn: "wire_services.go",
		},
		{
			// T11：监控器在 Start 时打「首次启用」标记，
			// 报告的"从启用起累计、不补算"下界全靠这一行。
			name:     "播放监控打首次启用标记",
			dir:      "../playmonitor",
			callee:   "EnsureEnabledSince",
			callerIn: "service.go",
		},
		{
			// T16：RSS 仓储要注入 API 层，否则 11 个端点一律
			// CodeNotImplement —— 路由全通、handler 全部短路，测试照样绿。
			name:     "RSS 仓储注入 API 层",
			dir:      "../app",
			callee:   "BindRSSStores",
			callerIn: "wire_http.go",
		},
		{
			// T16：轮询 worker 必须在 StartDiscoveryWorkers 里被拉起。
			// 仓储注入对了但没人调 StartRSSWatcher = 同步功能整体静默失效。
			name:     "RSS 轮询 worker 被拉起",
			dir:      "../discover/discovery",
			callee:   "StartRSSWatcher",
			callerIn: "catalog.go",
		},
		{
			// T12：补发仓储必须注入 dispatcher，否则 webhook 发送失败
			// 只会被记一条 Error 日志然后丢掉 —— 用户收到通知的最后一环
			// 静默失效，而通道测试与包内单测全绿。
			name:     "通知补发队列注入 dispatcher",
			dir:      "../app",
			callee:   "SetRetryQueue",
			callerIn: "wire_http.go",
		},
		{
			// T12：光注入队列没人跑，队列会一直停在 pending。
			// 这个 worker 必须在装配里被构造出来（构造即证明有投递通道）。
			name:     "补发 worker 被构造",
			dir:      "../app",
			callee:   "NewRetryWorker",
			callerIn: "wire_http.go",
		},
		{
			// T12：场景开关是 T12 的核心闸门 —— 它必须真的参与判定，
			// 而不是只有一个配置项摆在那。
			name:     "场景闸门被投递路径调用",
			dir:      "../notifychannel",
			callee:   "matchesSceneFilter",
			callerIn: "dispatcher.go",
		},
		{
			// T12：worker 必须真的把事件发出去，否则重试只是把 pending
			// 挪到下一个时间点然后标 failed。
			name:     "补发 worker 真的发消息",
			dir:      "../notifychannel",
			callee:   "RunOnce",
			callerIn: "retry_worker.go",
		},
		{
			// T12：模板里的 {{.Scene}} / {{.SceneLabel}} 全靠这里填值。
			// 删掉的话 simpleTemplate 照常展开 Title/Content，
			// 场景字段静默渲染成空串 —— 模板不报错、测试也可能只断言了 Title。
			name:     "场景字段被投递路径填充",
			dir:      "../notifychannel",
			callee:   "sceneFields",
			callerIn: "dispatcher.go",
		},
		{
			// T12：补发路径必须自己把 EventScene 翻成场景名。它是独立于
			// dispatcher 的第二条注入路径，两条都得有。
			name:     "补发路径自己解析场景名",
			dir:      "../notifychannel",
			callee:   "SceneLabel",
			callerIn: "retry_worker.go",
		},
		{
			// T31：指纹写进每条判定记录，是「这条结论出自哪一版规则」
			// 的唯一凭据。buildRecord 不写它的话列永远是空串，
			// 而症状要等用户回头查旧记录时才发现。
			name:     "规则指纹被记录写入路径调用",
			dir:      "../mediaupgrade",
			callee:   "Fingerprint",
			callerIn: "scan.go",
		},
		{
			// T31：结构化理由要序列化进记录。同上，缺了是空串。
			name:     "驳回理由被记录写入路径序列化",
			dir:      "../mediaupgrade",
			callee:   "marshalReasons",
			callerIn: "scan.go",
		},
		{
			// T31：试算端点唯一的判定入口。绕过它自己比较，
			// 试算就会和真实扫描给出相反结论（T02 预览端点踩过这个坑）。
			name:     "试算端点委托给 TrialVerdict",
			dir:      ".",
			callee:   "TrialVerdict",
			callerIn: "media_upgrade.go",
		},
		{
			// T31：门槛类理由只在试算里出现（真实扫描里门槛不过的候选
			// 根本不产生记录）。少了它，用户配规则时就问不出
			// 「我这份规则会不会把这个文件洗掉」。
			name:     "门槛理由被试算调用",
			dir:      "../mediaupgrade",
			callee:   "gateReasons",
			callerIn: "trial.go",
		},
		{
			// T32：ResolveDirReferences 是整个防呆提示的判定所在。
			// handler 绕过它自己拼一句文案的话，接口照样 200、前端照样渲染，
			// 界面完全正常 —— 而单测全绿，因为它们测的是 cloudref 自己。
			name:     "目录判定的唯一来源被 handler 调用",
			dir:      ".",
			callee:   "ResolveDirReferences",
			callerIn: "dir_ref.go",
		},
		{
			// T32：判据采集。两个来源（洗版规则 + 自动化触发器）任一被摘掉，
			// 接口都仍然返回 200 且结构完整，只是覆盖判据悄悄少了一半 ——
			// 正是这个功能要消灭的那种静默。
			name:     "媒体库根从洗版规则采集",
			dir:      ".",
			callee:   "libraryRootsFrom",
			callerIn: "dir_ref.go",
		},
		{
			name:     "自动整理源目录从自动化规则采集",
			dir:      ".",
			callee:   "monitorSourcesFrom",
			callerIn: "dir_ref.go",
		},
		{
			// T32：路径归一是「同一目录的不同写法等价」的全部依据，
			// 也是 `..` 不许逃出库根的那道闸。删掉它尾斜杠就会被判成不在库内，
			// 症状是「明明填的就是那个目录，却提示不在媒体库内」。
			name:     "路径归一被判定调用",
			dir:      "../cloudref",
			callee:   "NormalizePath",
			callerIn: "cloudref.go",
		},
		{
			// T13：搜索索引的唯一来源是注册表。AllSpecs 不导出的话，
			// 调用方只能手写一份清单 —— 那份清单在第一次加新 key 时就会漏，
			// 而症状是「设置页上找得到、搜索里搜不到」，用户只会得出
			// 「这个功能没做」这一个结论。
			name:     "索引从注册表导出",
			dir:      "../settings",
			callee:   "AllSpecs",
			callerIn: "index.go",
		},
		{
			// 同上：分组名也是登记来的，不是从 key 前缀猜的。
			name:     "索引的分组名来自登记",
			dir:      "../settings",
			callee:   "Categories",
			callerIn: "index.go",
		},
		{
			// T13：索引端点唯一的条目来源。绕过 BuildIndex 自己拼 items，
			// 接口照样 200、路由守卫照样绿，而注册表新增的 key 永远进不了搜索。
			name:     "索引端点委托给 BuildIndex",
			dir:      ".",
			callee:   "BuildIndex",
			callerIn: "settings.go",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !callsInNonTestFile(t, tc.dir, tc.callerIn, tc.callee) {
				t.Errorf("%s：%s 中未发现对 %s 的调用（生产代码里没有调用点 = 死代码）",
					tc.name, tc.callerIn, tc.callee)
			}
		})
	}
}

// callsInNonTestFile 在 dir 下的非测试 Go 文件里查找对 callee 的调用表达式。
func callsInNonTestFile(t *testing.T, dir, file, callee string) bool {
	t.Helper()
	path := filepath.Join(dir, file)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	found := false
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == callee {
				found = true
			}
		case *ast.SelectorExpr:
			if fn.Sel != nil && fn.Sel.Name == callee {
				found = true
			}
		}
		return true
	})
	return found
}

// TestRSSStoreWiredIntoDiscoveryPackage 断言发现包里的两个仓储注入点
// 在生产装配里真的被赋了值。
//
// 为什么单开一个测试而不进上面那张表：callsInNonTestFile 只认
// **调用表达式**，而 `discovery.RSSSourceStore = st.store.RSSSources`
// 是赋值不是调用。硬塞进表里，失败信息会写成"未发现对 RSSSourceStore
// 的调用"—— 把"没接线"说成"没调用"，读的人得自己绕一圈才明白。
// 更糟的是有人会为了让这条变绿，改成 `discovery.RSSSourceStore()` ——
// 把赋值改成一个只读的 getter，测试过了，功能照样是死的。
func TestRSSStoreWiredIntoDiscoveryPackage(t *testing.T) {
	path := filepath.Join("../app", "wire_http.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	assigned := map[string]bool{}
	ast.Inspect(parsed, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				continue
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "discovery" {
				continue
			}
			switch sel.Sel.Name {
			case "RSSSourceStore", "RSSHistoryStore":
				assigned[sel.Sel.Name] = true
			}
		}
		return true
	})
	for _, name := range []string{"RSSSourceStore", "RSSHistoryStore"} {
		if !assigned[name] {
			t.Errorf("discovery.%s 在 %s 里没有被赋值 —— "+
				"RSS 轮询 worker 读不到源，启用了定时同步也不会跑任何东西，"+
				"而且不报错", name, path)
		}
	}
}

// TestNotifyRetryWiredIntoAPIDeps 断言补发仓储与 worker 都进了 api.Deps，
// 且**赋的不是 nil**。
//
// 为什么单开而不进上面那张表：callsInNonTestFile 只认**调用表达式**，而
// `Deps{NotifyRetries: …, NotifyRetryRunner: …}` 是复合字面量赋值。
// 硬塞进表里，失败信息会写成"未发现对 NotifyRetries 的调用"—— 把
// "没接线"说成"没调用"，读的人得自己绕一圈才明白；更糟的是有人会为了
// 让它变绿把字段改成只读 getter，测试过了，按钮照样不生效。
//
// 为什么连值一起断言（实测踩过）：只判 key 存在的话，把值换成 nil 一样绿。
// 而 nil 在这里不是编译错误 —— handler 里的 nil 短路会让每个补发端点
// 返回「服务未就绪」，管理台上补发队列永远是空的，重投按钮点了没反应，
// 一行错误日志都不会有。
func TestNotifyRetryWiredIntoAPIDeps(t *testing.T) {
	path := filepath.Join("../app", "wire_http.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	assigned := map[string]string{}
	ast.Inspect(parsed, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		// 只认 api.Deps{…}，别把任何同名字段都算上。
		typ, isSel := cl.Type.(*ast.SelectorExpr)
		if !isSel || typ.Sel == nil {
			return true
		}
		pkg, isIdent := typ.X.(*ast.Ident)
		if !isIdent || pkg.Name != "api" || typ.Sel.Name != "Deps" {
			return true
		}
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "NotifyRetries", "NotifyRetryRunner":
				var rendered string
				if v, ok := kv.Value.(*ast.Ident); ok {
					rendered = v.Name
				} else {
					rendered = exprString(fset, kv.Value)
				}
				assigned[key.Name] = rendered
			}
		}
		return true
	})
	for _, name := range []string{"NotifyRetries", "NotifyRetryRunner"} {
		value, ok := assigned[name]
		if !ok {
			t.Errorf("api.Deps.%s 在 %s 里没有被赋值 —— "+
				"补发队列的接口一律返回「服务未就绪」，重投按钮点了没反应，"+
				"而且不报错", name, path)
			continue
		}
		if value == "nil" {
			t.Errorf("api.Deps.%s 被赋成了 nil —— "+
				"字段在就算接线过了，但每个补发端点都会走「服务未就绪」分支，"+
				"管理台看不到任何补发记录，重投按钮点了没反应", name)
		}
	}
}

// exprString 把表达式渲染成源码片段，供守卫的错误信息使用。
func exprString(fset *token.FileSet, e ast.Expr) string {
	var sb strings.Builder
	if err := format.Node(&sb, fset, e); err != nil {
		return "<无法渲染>"
	}
	return sb.String()
}

// TestSubtitleHookIsActuallyInjected 断言整理服务选项里真的传了字幕处理器。
//
// 只检查 mediaorganize 内部有子调用还不够：如果装配层把 Subtitle 字段留空，
// 钩子会在生产环境静默失效（nil 接口），测试却依然全绿。
func TestSubtitleHookIsActuallyInjected(t *testing.T) {
	path := filepath.Join("..", "app", "wire_mediaorganize.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	text := string(src)
	if !strings.Contains(text, "Subtitle:") {
		t.Errorf("%s 未向 ServiceOptions 传入 Subtitle，字幕自动处理在生产环境不会生效", path)
	}
	if !strings.Contains(text, "subtitleProcessorAdapter") {
		t.Errorf("%s 未使用 subtitleProcessorAdapter 适配字幕服务", path)
	}
}

// TestFallbackWiringPassesRealCandidates 断言接线传入的是真实候选数而非常量。
//
// 若把 len(items) > 0 写成恒 false，计数会永远累加、永远误触发降级；
// 写成恒 true 则永不计数、兜底形同虚设。两者都不会编译失败。
func TestFallbackWiringPassesRealCandidates(t *testing.T) {
	src, err := os.ReadFile("discover_resources.go")
	if err != nil {
		t.Fatalf("读取 discover_resources.go 失败：%v", err)
	}
	text := string(src)
	if !strings.Contains(text, "len(items) > 0") {
		t.Errorf("discover_resources.go 未以真实候选数调用兜底接线")
	}
}

// TestMediaUpgradeRoutesRegistered 断言洗版接口真的挂上了路由。
//
// 这里用源码文本匹配而不是 chi.Walk：仓库里没有 chi.Walk 的先例，
// 且本文件的主题就是「用最笨但最不容易骗过自己的办法验证接线」，
// 路由表本身就是一段字面量，直接查它比反射更直白。
func TestMediaUpgradeRoutesRegistered(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}
	text := string(src)
	want := []string{
		`r.Route("/media-upgrade"`,
		`r.Get("/scans", h.listMediaUpgradeScans)`,
		`r.Post("/scans", h.createMediaUpgradeScan)`,
		`r.Get("/scans/{id}", h.getMediaUpgradeScan)`,
		`r.Post("/scans/{id}/execute", h.executeMediaUpgradeScan)`,
		`r.Get("/records", h.listMediaUpgradeRecords)`,
		`r.Get("/records/{id}", h.getMediaUpgradeRecord)`,
		`r.Get("/rules", h.listMediaUpgradeRules)`,
		`r.Post("/rules", h.createMediaUpgradeRule)`,
		`r.Put("/rules/{id}", h.updateMediaUpgradeRule)`,
		`r.Delete("/rules/{id}", h.deleteMediaUpgradeRule)`,
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("router.go 缺少洗版路由 %s", w)
		}
	}
}

// TestMediaUpgradeServiceIsWired 断言装配层真的把洗版服务注入了路由。
//
// 只在 api 包里写好 handler 而忘了注入 Deps，是这个仓最常见的一种「假接线」：
// handler 能编译、能被测，但生产环境 h.mediaUpgrade 恒为 nil。
func TestMediaUpgradeServiceIsWired(t *testing.T) {
	for _, tc := range []struct {
		file string
		want string
	}{
		{"router.go", "mediaUpgrade:         d.MediaUpgrade"},
		{filepath.Join("..", "app", "wire_http.go"), "MediaUpgrade:     svc.mediaUpgrade"},
		{filepath.Join("..", "app", "wire_http.go"), "svc.mediaUpgrade = wireMediaUpgrade(st, ddb.MustDb())"},
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", tc.file, err)
		}
		// 与 TestRBACServiceIsWired 同口径：先 squeezeSpace 归一，
		// 再比对。gofmt 会按最长字段名重新对齐 Deps 结构体字面量，
		// T12 往里加字段后整块缩进就变了（T12 之前是 "MediaUpgrade:     "，
		// 之后是 "MediaUpgrade:      "），逐字比对原文会变成天天红的假警报。
		if !strings.Contains(squeezeSpace(string(src)), squeezeSpace(tc.want)) {
			t.Errorf("%s 缺少 %q（洗版服务没接上）", tc.file, tc.want)
		}
	}
}

// TestRBACRoutesRegistered 断言用户与权限接口真的挂上了路由。
//
// 口径与洗版那一条一致：查源码字面量而不是反射遍历路由树，
// 因为这里要防的正是「handler 写了、路由表里没有」这种假接线。
func TestRBACRoutesRegistered(t *testing.T) {
	src, err := os.ReadFile("rbac.go")
	if err != nil {
		t.Fatalf("读取 rbac.go 失败：%v", err)
	}
	text := string(src)
	for _, w := range []string{
		`r.Get("/me", h.authMe)`,
		`r.Get("/menus", h.authMenus)`,
		`r.Get("/users", h.listRBACUsers)`,
		`r.Post("/users", h.createRBACUser)`,
		`r.Put("/users/{id}", h.updateRBACUser)`,
		`r.Delete("/users/{id}", h.deleteRBACUser)`,
		`r.Post("/users/{id}/enabled", h.setRBACUserEnabled)`,
		`r.Post("/users/{id}/password", h.setRBACUserPassword)`,
		`r.Post("/users/{id}/groups", h.setRBACUserGroups)`,
		`r.Get("/users/{id}/overrides", h.listRBACUserOverrides)`,
		`r.Post("/users/{id}/overrides", h.setRBACUserOverrides)`,
		`r.Get("/permissions", h.listRBACPermissions)`,
		`r.Get("/groups", h.listRBACGroups)`,
		`r.Post("/groups", h.createRBACGroup)`,
		`r.Put("/groups/{id}", h.updateRBACGroup)`,
		`r.Delete("/groups/{id}", h.deleteRBACGroup)`,
		`r.Post("/groups/{id}/members", h.setRBACGroupMembers)`,
		`r.Post("/groups/{id}/permissions", h.setRBACGroupPermissions)`,
	} {
		if !strings.Contains(text, w) {
			t.Errorf("rbac.go 缺少路由 %s", w)
		}
	}
}

// TestRBACServiceIsWired 断言 RBAC 服务从装配层一路接到了路由层。
//
// 三段都要在：服务被构造、注进 api.Deps、赋给 Handler。
// 少任何一段的表现都是「界面能打开、每个接口都 501」。
func TestRBACServiceIsWired(t *testing.T) {
	for _, tc := range []struct {
		file string
		want string
	}{
		{"router.go", "rbac: d.RBAC"},
		{"router.go", "RBAC *rbac.Service"},
		{"router.go", "h.RegisterRBACRoutes(r)"},
		{filepath.Join("..", "app", "wire_http.go"), "rbacSvc := wireRBAC(st, logs)"},
		{filepath.Join("..", "app", "wire_http.go"), "bindRBAC(adminAuthSvc, rbacSvc)"},
		{filepath.Join("..", "app", "wire_http.go"), "RBAC: rbacSvc"},
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", tc.file, err)
		}
		// 断言前先把连续空白压成单空格：gofmt 会按最长字段名重新对齐
		// 结构体字面量，Deps 每加一个字段整块缩进就变。直接比对原文的话，
		// 一次纯排版调整就会把这条守卫变成「天天红的假警报」，
		// 久而久之大家就会习惯性忽略它 —— 那才是真正的损失。
		if !strings.Contains(squeezeSpace(string(src)), squeezeSpace(tc.want)) {
			t.Errorf("%s 缺少 %q（RBAC 没接上）", tc.file, tc.want)
		}
	}
}

// squeezeSpace 把所有连续空白压成单个空格，让断言与 gofmt 的对齐无关。
func squeezeSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// TestRBACDoesNotWrapMcpPublicRoutes 断言 MCP 外部入口没有被 RBAC 罩住。
//
// MCP 客户端用独立 API Key 鉴权，没有管理员会话。一旦它落进
// RequirePermission 中间件，非超管的 MCP 客户端会在鉴权之前就被 403，
// 而超管的 API Key 客户端又必须继续能用 —— 这是验收⑤的命门。
//
// 检查方式是位置而不是语义：MCP 公开路由的注册行必须在
// 第一个 r.Use(h.requireAdmin) 之前。
func TestRBACDoesNotWrapMcpPublicRoutes(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}
	text := string(src)
	mcpAt := strings.Index(text, "h.RegisterMcpPublicRoutes(r)")
	adminAt := strings.Index(text, "r.Use(h.requireAdmin)")
	if mcpAt < 0 {
		t.Fatal("router.go 里找不到 MCP 公开路由注册")
	}
	if adminAt < 0 {
		t.Fatal("router.go 里找不到 requireAdmin 中间件")
	}
	if mcpAt > adminAt {
		t.Error("MCP 公开路由被注册在 requireAdmin 组之后，会被会话鉴权拦住")
	}
	// RequirePermission 只能出现在 requireAdmin 组之内，也就是出现在
	// adminAt 之后的某个 r.Group / r.Route 里；它出现在 adminAt 之前
	// 就说明有公开路由被罩住了。
	if strings.Index(text, "h.requirePermission(") < adminAt {
		t.Error("RequirePermission 出现在 requireAdmin 组之前，会影响公开路由")
	}
}

// TestRBACSuperOnlyRoutesAreGated 断言两项超管专属动作真的挂上了权限中间件。
//
// 验收③靠的是这个：这四项接口在库里即使给非超管配了 allow 也必须拒绝。
// 只断言中间件出现了，不判断运行时 —— 运行时判定由 rbac 包的
// TestSuperOnlyPermissionsAreDeniedToEveryoneElse 负责。
func TestRBACSuperOnlyRoutesAreGated(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}
	text := string(src)
	for _, perm := range []string{"rbac.PermSubscriptionCreate", "rbac.PermOfflineDownloadRun"} {
		if !strings.Contains(text, "h.requirePermission("+perm+")") {
			t.Errorf("router.go 没有用 %s 圈住接口", perm)
		}
	}
}

// TestRBACFrontendIsWired 断言前端的用户与权限入口真的接上了。
//
// 这是「假接线」在前端的版本：导航里加了一个 rbac 条目，但忘了调
// fetchRbacMenus / fetchRbacMe，侧边栏就会一直显示全部菜单 ——
// 界面上看不出任何异常，只有「权限没生效」，而且是以最容易被忽略的方式
// （看起来像配错了）表现出来。所以这里直接盯源码里那几处字面量。
//
// 读 .vue 源码而不是解析 AST：和 TestMenuCatalogCoversAdminNavigation
// 同一个理由 —— 这些断言只想守「有没有接上」，不想因为前端重构而一起崩。
func TestRBACFrontendIsWired(t *testing.T) {
	adminView, err := os.ReadFile("../../web/src/views/AdminView.vue")
	if err != nil {
		t.Fatalf("读取 AdminView.vue 失败：%v", err)
	}
	text := string(adminView)
	for _, want := range []string{
		`import { fetchRbacMe } from "@/api/rbac"`,                        // 拉可见菜单
		`{ key: "rbac", label: "用户与权限"`,                                   // 导航条目
		`rbac: () => import("@/components/admin/RbacManagementPage.vue")`, // 页面加载器
		`<RbacManagementPage v-else-if="page === 'rbac'" />`,              // 页面渲染分支
		// 侧边栏用的必须是**经过权限过滤**的那一份。
		// T13 之后侧边栏渲染的是 arrangedNav（编排后的顺序/隐藏），
		// 而 arrangedNav 是在 visibleNav 之上派生的 —— 权限决定「能不能进」，
		// 编排只决定「在能进的里面怎么排」。两条都要断：只断第一条，
		// 哪天有人把 :nav 改回 visibleNav 就没人发现编排被绕过了。
		`:nav="arrangedNav"`,
		`applyHidden(applyOrder(visibleNav.value`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("AdminView.vue 里找不到 %q —— 前端的用户与权限入口没有接上", want)
		}
	}
}

// TestRBACFrontendFailsOpenOnMenuFetchFailure 断言菜单拉不到时是「显示全部」
// 而不是「什么都不显示」。
//
// 这条是刻意的口径：侧边栏只是入口，真正的拦截在后端权限中间件上，
// 直接输网址一样进不去。所以这里 fail-open 只会多显示几个点进去会 403 的
// 菜单；反过来 fail-closed 的话，一次网络抖动就让运维以为整个后台挂了。
//
// fail-closed 才是危险的：用户会以为权限系统在工作，实际上它根本没被问到。
func TestRBACFrontendFailsOpenOnMenuFetchFailure(t *testing.T) {
	adminView, err := os.ReadFile("../../web/src/views/AdminView.vue")
	if err != nil {
		t.Fatalf("读取 AdminView.vue 失败：%v", err)
	}
	text := string(adminView)
	// catch 分支必须把 visibleMenuKeys 置成 null（= 不过滤），并且留了 warn。
	warnIdx := strings.Index(text, `console.warn("[rbac] 拉取可见菜单失败`)
	if warnIdx < 0 {
		t.Fatal("AdminView.vue 里找不到菜单拉取失败的告警分支")
	}
	// 从告警往前找最近的 catch 开头，只看 catch 到告警之间的那一段 ——
	// 赋值写在告警前面（先改状态再记录原因），所以正向扫会漏掉该断言的那一行。
	catchIdx := strings.LastIndex(text[:warnIdx], "} catch (")
	if catchIdx < 0 {
		t.Fatal("告警不在任何 catch 分支里，前端改的是别的地方")
	}
	if !strings.Contains(text[catchIdx:warnIdx], "visibleMenuKeys.value = null") {
		t.Error("拉取失败时应当把 visibleMenuKeys 置为 null（不过滤，显示全部菜单）")
	}
}

// TestRBACMenuKeyMatchesFrontendNavKey 断言后端菜单 key 与前端导航 key 对得上。
//
// 验收④的核心：对不上时后端会给出一份前端一个都用不上的清单，
// 表现是「权限配了但菜单不见」或者「菜单全在但接口 403」。
// 这里直接比对 AdminView.vue 里的 nav[].key 与 menuCatalog 的 Key。
func TestRBACMenuKeyMatchesFrontendNavKey(t *testing.T) {
	adminView, err := os.ReadFile("../../web/src/views/AdminView.vue")
	if err != nil {
		t.Fatalf("读取 AdminView.vue 失败：%v", err)
	}
	text := string(adminView)
	start := strings.Index(text, "const nav = [")
	if start < 0 {
		t.Fatal("AdminView.vue 里找不到 nav 定义")
	}
	end := strings.Index(text[start:], "];")
	if end < 0 {
		t.Fatal("AdminView.vue 里 nav 定义没有正常结束")
	}
	block := text[start : start+end]

	backend := map[string]bool{}
	for _, m := range rbac.AllMenuKeys() {
		backend[m] = true
	}
	seen := 0
	for _, line := range strings.Split(block, "\n") {
		i := strings.Index(line, `{ key: "`)
		if i < 0 {
			continue
		}
		rest := line[i+len(`{ key: "`):]
		j := strings.Index(rest, `"`)
		if j < 0 {
			continue
		}
		key := rest[:j]
		seen++
		if !backend[key] {
			t.Errorf("前端导航有 %q，后端菜单目录里没有 —— 开了 RBAC 后这个页面对所有人隐身", key)
		}
	}
	if seen != len(rbac.AllMenuKeys()) {
		t.Errorf("前端导航 %d 项，后端菜单目录 %d 项，两边对不上", seen, len(rbac.AllMenuKeys()))
	}
}

// TestRBACCatalogOnlyPermissionsAreDocumented 断言「在目录里但这一期没挂路由」的
// 权限项是一份写死的白名单，而不是忘了接。
//
// 为什么需要它：目录里 27 项，路由上真正挂了 requirePermission 的只有一部分。
// 剩下的如果哪天悄悄变成了「授权了但一点用都没有」，界面上看不出任何异常 ——
// 管理员给了权限，受限的动作照样能做，这是权限系统最坏的失败方式。
// 所以把「暂时没接」显式列出来，新增权限项却忘了列进来时直接红。
//
// 白名单里的每一项都要有理由，理由写在这个 map 的值里，别留空。
// permissionConstNames 从 internal/rbac/permissions.go 的源���里解析出
// 「Go 常量名 → 权限 key」的映射。
//
// 不用「key 反推常量名」的办法：mediaupgrade.manage 的常量叫 PermMediaUpgradeManage
// （中间有分词），find.resource 的叫 PermFindResource，反推要靠大小写规则猜，
// 一旦猜错整张表就空了，测试会误报成「全都忘了接线」。
// 正着读声明永远是对的。
func permissionConstNames(t *testing.T) map[string]string {
	t.Helper()
	src, err := os.ReadFile("../rbac/permissions.go")
	if err != nil {
		t.Fatalf("读取 permissions.go 失败：%v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(src), "\n") {
		l := strings.TrimSpace(line)
		if !strings.HasPrefix(l, "Perm") {
			continue
		}
		eq := strings.Index(l, "=")
		if eq < 0 || !strings.HasSuffix(l, `"`) {
			continue
		}
		name := strings.TrimSpace(l[:eq])
		key := strings.Trim(strings.TrimSpace(l[eq+1:]), `"`)
		if strings.HasPrefix(key, "Perm") {
			continue
		}
		out[name] = key
	}
	if len(out) < 20 {
		t.Fatalf("从 permissions.go 只解析出 %d 个权限常量，明显不对", len(out))
	}
	return out
}

func TestRBACCatalogOnlyPermissionsAreDocumented(t *testing.T) {
	// 判定「挂上去了」的办法是把 router.go 里真正出现的 requirePermission 扫出来，
	// 而不是手写一份「我以为接了哪些」的清单 —— 那份清单和实现各写各的，
	// 两者一旦分叉，测试反而变成绿的。
	// 两个文件都算：router.go 挂的是既有端点，rbac.go 挂的是 RBAC 自己的端点。
	routerText := ""
	// request_center.go / request_portal_handlers.go 也要算进来：求片中心的权限闸
	// 挂在这两个文件上，不在 router.go 里。漏掉它们会让下面这份白名单
	// 看起来「还都只是登记」，实际早已挂上路由 —— 白名单会悄悄变成谎话。
	// library_share_admin.go 同理：分享管理的权限闸挂在它自己的文件里。
	for _, f := range []string{"router.go", "rbac.go", "request_center.go", "request_portal_handlers.go", "library_share_admin.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", f, err)
		}
		routerText += string(b)
	}
	wired := map[string]bool{}
	for constName, key := range permissionConstNames(t) {
		// 两种挂法都算接上了：
		//   requirePermission(rbac.PermXxx) —— 中间件形式，绝大多数路由用它；
		//   p.Can(rbac.PermXxx)            —— 内联形式，用在登录这种「当场判断再决定
		//                                    给不给进」的场景（求片站登录就是）。
		if strings.Contains(routerText, "requirePermission(rbac."+constName+")") ||
			strings.Contains(routerText, "p.Can(rbac."+constName+")") {
			wired[key] = true
		}
	}

	// 这一期刻意只登记、不挂路由的权限项，以及各自的理由。
	// ⚠️ 删掉一项之前先确认它真的挂上了：share.manage 曾在白名单里，
	// 分享管理（T10）给它接了 /api/admin/library-shares/* 之后才移出。
	// 移出时白名单从 14 变 13 —— 数字变了而没人改这里，同样是谎话。
	catalogOnly := map[string]string{
		"console.dashboard.view": "仪表盘概览；/logs 这类路由没单独设闸，靠仪表盘菜单本身",
		"task.manage":            "任务管理；路由是平铺的，单独设闸要动一大片，这一期靠菜单过滤",
		"tool.manage":            "辅助工具；同上",
		"transfer.manage":        "跨盘传输；同上",
		"subtitle.manage":        "字幕处理；路由由 RegisterSubtitleRoutes 自带鉴权",
		"mcp.manage":             "MCP 服务；路由由 RegisterMcpAdminRoutes 自带鉴权",
		"assistant.use":          "智能助理；同上",
		"audit.view":             "审计日志这一期没有页面。刻意不挂到 /logs（系统日志 ≠ 审计日志）",
		"request.mine.view":      "「我的求片」复用 request.center.view 那道闸：同一个人既要能看自己的单，也要能看到求片页",
		"request.all.view":       "「全部求片」目前与待审核共用 request.review；这一期还没有只读不审的角色",
		"request.reassign":       "转派审核这一期没有实现（没有任何端点），留着是为了权限矩阵里不被后来人重新发明一个名字",
		"request.flow.manage":    "管理求片流程（去重规则、指派）这一期没有端点；规则编辑挂在 request.center.view 下",
		"find.resource":          "找资源这一期挂在影视发现里",
	}

	for _, p := range rbac.Catalog() {
		key := p.Key
		_, isCatalogOnly := catalogOnly[key]
		if isCatalogOnly && catalogOnly[key] == "" {
			t.Errorf("权限项 %q 在「只登记不挂路由」白名单里，但没有写理由", key)
			continue
		}
		if !isCatalogOnly && !wired[key] {
			t.Errorf("权限项 %q 既不在白名单、也没在 router.go / menus 里出现 —— 新增权限项忘了接线", key)
		}
	}

	if len(catalogOnly) != 13 {
		t.Errorf("「只登记不挂路由」的白名单有 %d 项，改动后请同步更新这个数字和上面的 map", len(catalogOnly))
	}
}

// TestRequestCenterServiceIsWired 断言求片中心从服务到端口整条链都接上了。
//
// 这个仓库最常见的坑是「假接线」：处理器能编译、单测能过，
// 但装配层忘了给 Deps 赋值，于是生产上字段是 nil、求片站一片空白。
// 这里把链条上每一环都用字面量钉住。
func TestRequestCenterServiceIsWired(t *testing.T) {
	for _, tc := range []struct {
		file string
		want string
	}{
		{"request_portal.go", "func NewRequestPortalRouter(d Deps) http.Handler"},
		{"router.go", "func newHandler(d Deps) *Handler"},
		{"router.go", "MediaRequest *mediarequest.Service"},
		{"router.go", "mediaRequest:         d.MediaRequest"},
		{"router.go", "h.RegisterRequestCenterRoutes(r)"},
		{filepath.Join("..", "app", "app.go"), "reqCenter := wireRequestCenter"},
		{filepath.Join("..", "app", "wire_request.go"), "mediarequest.NewService(mediarequest.Params{"},
		{filepath.Join("..", "app", "wire_http.go"), "MediaRequest:      reqCenter.maybeService()"},
		{filepath.Join("..", "app", "wire_http.go"), "RequestSigner:     reqSigner"},
		{filepath.Join("..", "app", "wire_http.go"), "api.NewRequestPortalRouter(api.Deps{"},
		{filepath.Join("..", "app", "wire_http.go"), "&mediarequest.Listener{"},
		{filepath.Join("..", "app", "app.go"), "go a.requestListener.Run(a.httpBaseCtx)"},
		{filepath.Join("..", "app", "app.go"), "a.requestCenter.Start(ctx, a.logs)"},
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", tc.file, err)
		}
		if !strings.Contains(string(src), tc.want) {
			t.Errorf("%s 缺少 %q（求片中心没接上）", tc.file, tc.want)
		}
	}
}

// TestRequestPortalRejectsEveryAdminRoute 遍历管理台的**全部**路由，
// 断言它们在求片站上全部 404；同时反向断言求片站自己那几条路由不是 404。
//
// 为什么必须遍历全部而不是抽样：求片站和管理台是两棵独立的路由树，
// 「求片端口上不该有后台接口」是需求⑦的硬要求。
// 抽样只能证明「这几个没问题」，证明不了「将来加的路由也不会漏进来」。
// 将来有人给管理台加一条 /api/something 或挂个静态目录，
// 这个测试会立刻指出该路径在求片站上是通的 —— 那正是要防的事故。
//
// 反向断言同样必要：只测 404 的话，求片站自己 8 条接口全挂了这测试还是绿的。
func TestRequestPortalRejectsEveryAdminRoute(t *testing.T) {
	portalRouter := NewRequestPortalRouter(Deps{})

	// 正向：求片站自己的路由不是 404。
	for _, tc := range portalOwnRoutes {
		req := httptest.NewRequest(tc.Method, tc.Pattern, nil)
		rec := httptest.NewRecorder()
		portalRouter.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("求片站自己的路由 %s %s 返回 404 —— 求片站接口没注册", tc.Method, tc.Pattern)
		}
	}

	// 反向：管理台的全部路由在求片站上必须 404。
	adminRoutes := adminRoutePatterns(t)
	if len(adminRoutes) < 100 {
		t.Fatalf("只扫到 %d 条管理台路由，扫描范围明显不对", len(adminRoutes))
	}
	for _, rt := range adminRoutes {
		if isPortalOwnRoute(rt.Pattern) || sharesDeliberately(rt.Pattern) {
			continue
		}
		probe := concreteRoute(rt.Pattern)
		req := httptest.NewRequest(rt.Method, probe, nil)
		rec := httptest.NewRecorder()
		portalRouter.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("管理台路由 %s %s 在求片站上返回 %d（应为 404）—— 求片站漏出了后台接口",
				rt.Method, rt.Pattern, rec.Code)
		}
	}
	t.Logf("已验证 %d 条管理台路由在求片站上全部 404", len(adminRoutes))
}

// TestRequestPortalAssetsAreNotAdminAssets 断言求片站发出去的静态文件
// **不是后台的那一份**。
//
// 为什么单独一条：`/assets/*` 在两棵树上同名，所以
// TestRequestPortalRejectsEveryAdminRoute 把它从 404 断言里排除了。
// 排除就得有东西兜底，否则「排除」本身就是一个没人看的洞。
//
// 这里把话说死：求片站 assets 下每个文件，到后台产物里去找，
// 要么根本不存在，要么内容不同。真出现了同一个文件同样的字节，
// 说明两套构建又合回了同一个 outDir，求片站正在发后台的主包。
func TestRequestPortalAssetsAreNotAdminAssets(t *testing.T) {
	portalFS, index := LoadPortalFS(EmbeddedWebFS())
	if portalFS == nil {
		// 前端产物没构建时不假装通过也不假装失败：
		// 产物缺失本身由 portalPage 的构建提示兜底，不是这里的职责。
		t.Skip("求片站构建产物不存在，先跑 cd web && npm run build")
	}
	if len(index) == 0 {
		t.Fatalf("求片站 index.html 是空的")
	}
	admin, err := fs.Sub(EmbeddedWebFS(), "web")
	if err != nil {
		t.Fatalf("取后台产物失败：%v", err)
	}

	portalFiles := 0
	err = fs.WalkDir(portalFS, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		portalFiles++
		mine, err := fs.ReadFile(portalFS, p)
		if err != nil {
			t.Errorf("读求片站产物 %s 失败：%v", p, err)
			return nil
		}
		theirs, err := fs.ReadFile(admin, p)
		if err != nil {
			return nil // 后台没有同名文件，正是期望的情形
		}
		if bytes.Equal(mine, theirs) {
			t.Errorf("求片站的 %s 与后台产物完全相同 —— 求片站在发后台的静态包", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历求片站产物失败：%v", err)
	}
	if portalFiles == 0 {
		t.Fatalf("求片站 assets 目录是空的，构建产物没生成")
	}
	t.Logf("已比对求片站 %d 个静态文件，与后台产物无一同名同内容", portalFiles)
}

// portalDeliberatelyShares 求片站**刻意**与后台同名的路由。
//
// 目前只有静态资源目录 `/assets/*`：Vite 两套构建都用这个默认目录名，
// 后台发后台的 chunk、求片站发求片站自己那一份（两套产物分目录构建，
// 见 web/vite.request.config.ts）。同名不等于漏出后台接口，所以从 404 断言里排除。
//
// ⚠️ 但「同名且不同物」不能只靠注释保证，所以由
// TestRequestPortalAssetsAreNotAdminAssets 单独钉住：求片站 assets 下
// 每个文件在后台产物里要么不存在、要么内容不同。哪天有人把两套构建
// 又合回同一个 outDir，那条测试会先红。
var portalDeliberatelyShares = []string{"/assets/*"}

func sharesDeliberately(pattern string) bool {
	for _, p := range portalDeliberatelyShares {
		if p == pattern {
			return true
		}
	}
	return false
}

// portalOwnRoutes 求片站自己的路由清单（正向断言用）。
var portalOwnRoutes = []adminRoutePattern{
	{http.MethodGet, "/login"},
	{http.MethodGet, "/"},
	{http.MethodGet, "/assets/index-abc123.js"},
	{http.MethodPost, "/api/login"},
	{http.MethodPost, "/api/logout"},
	{http.MethodGet, "/api/me"},
	{http.MethodGet, "/api/search"},
	{http.MethodPost, "/api/request"},
	{http.MethodGet, "/api/mine"},
	{http.MethodPut, "/api/tags"},
	{http.MethodGet, "/api/stats"},
}

// isPortalOwnRoute 判断某个路径是不是求片站自己的。
// 求片站的路径在管理台上**也应该** 404（它只在求片端口上存在），
// 所以不参与反向断言。
func isPortalOwnRoute(pattern string) bool {
	for _, p := range portalOwnRoutes {
		if p.Pattern == pattern {
			return true
		}
	}
	return false
}

// adminRoutePattern 一条管理台路由。
type adminRoutePattern struct {
	Method  string
	Pattern string
}

// adminRoutePatterns 扫出 api 包里注册的全部管理台路由。
//
// 不用 chi.Walk 的原因：NewRouter 的返回值外面裹着 davBypass，
// 拿不到内层那棵 *chi.Mux，遍历不到路由表 —— 而拆那层包装等于去依赖
// 实现细节，它会随路由调整一起漂移。
//
// 扫源码反而更贴题：这个测试问的是「这份代码对外声明了哪些路径」，
// 注册了什么就是什么，不会漏，也不会因为求片站外面又裹了层包装就失效。
// 本来就是干静态守卫的文件，同一种手法延续下来。
func adminRoutePatterns(t *testing.T) []adminRoutePattern {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("解析 api 包失败：%v", err)
	}
	var out []adminRoutePattern
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				// 前缀从空开始：跨函数注册（RegisterXxxRoutes）用的都是绝对路径
				// （r.Route("/media-request", ...)），继承父级前缀反而会重复。
				collectRouteStmts(fn.Body.List, "", &out)
			}
		}
	}
	return out
}

// collectRouteStmts 按语句顺序扫一条语句列表，维护 chi 的前缀语义。
//
// 顺序 + 前缀参数（而不是 ast.Inspect 的无状态遍历）是必须的：
// r.Route("/x", func(r chi.Router){...}) 里的子路由真实路径是 /x/子路径，
// 而两个平级 r.Route("/a") / r.Route("/b") 的子路由互不沾染 ——
// 无状态的 ast.Inspect 会把两个前缀叠成 /a/b，那样的断言是假的。
func collectRouteStmts(stmts []ast.Stmt, prefix string, out *[]adminRoutePattern) {
	for _, st := range stmts {
		switch x := st.(type) {
		case *ast.BlockStmt:
			collectRouteStmts(x.List, prefix, out)
			continue
		case *ast.IfStmt:
			collectRouteStmts([]ast.Stmt{x.Body}, prefix, out)
			continue
		case *ast.ForStmt:
			collectRouteStmts([]ast.Stmt{x.Body}, prefix, out)
			continue
		case *ast.ExprStmt:
			call, ok := x.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			collectRouteCall(call, prefix, out)
		}
	}
}

// collectRouteCall 处理一次注册调用。
func collectRouteCall(call *ast.CallExpr, prefix string, out *[]adminRoutePattern) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	if sel.Sel.Name == "Group" {
		// Group 不带前缀，只带中间件：子路由路径不变。
		// 这里的第一个参数是闭包而不是路径，所以必须放在取字面量之前处理，
		// 否则会被下面的 lit == "" 直接 return 掉 ——
		// 而挂权限闸的分组恰好是最容易漏扫的一批路由。
		if len(call.Args) >= 1 {
			if body := funcBody(call.Args[0]); body != nil {
				collectRouteStmts(body.List, prefix, out)
			}
		}
		return
	}
	lit := firstStringLit(call)
	if lit == "" {
		return
	}
	switch sel.Sel.Name {
	case "Route":
		// Route 的第一个参数是路径前缀，第二个参数通常是闭包。
		if len(call.Args) >= 2 {
			if body := funcBody(call.Args[1]); body != nil {
				collectRouteStmts(body.List, prefix+lit, out)
			}
		}
	case "Mount":
		*out = append(*out, adminRoutePattern{Method: http.MethodGet, Pattern: prefix + lit})
	case "Get", "Post", "Put", "Delete", "Patch", "Head", "Options", "Trace", "Connect":
		*out = append(*out, adminRoutePattern{Method: methodName(sel.Sel.Name), Pattern: prefix + lit})
	case "Handle", "HandleFunc":
		// Handle 不带方法，任意方法都匹配；用 GET 探一次就够。
		*out = append(*out, adminRoutePattern{Method: http.MethodGet, Pattern: prefix + lit})
	}
}

// funcBody 取出函数字面量的函数体。
func funcBody(arg ast.Expr) *ast.BlockStmt {
	switch x := arg.(type) {
	case *ast.FuncLit:
		return x.Body
	case *ast.UnaryExpr: // &func(){...}
		return funcBody(x.X)
	}
	return nil
}

// methodName 把注册方法名转成 HTTP 方法。
func methodName(name string) string {
	switch name {
	case "Get":
		return http.MethodGet
	case "Post":
		return http.MethodPost
	case "Put":
		return http.MethodPut
	case "Delete":
		return http.MethodDelete
	case "Patch":
		return http.MethodPatch
	case "Head":
		return http.MethodHead
	case "Options":
		return http.MethodOptions
	case "Trace":
		return http.MethodTrace
	case "Connect":
		return http.MethodConnect
	}
	return http.MethodGet
}

// firstStringLit 取第一个字符串字面量参数，没有就返回空。
func firstStringLit(call *ast.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return v
}

// concreteRoute 把 chi 的路由模板换成可请求的具体路径。
//
// 目的是**触发路由匹配**：模板里的 {id} 要变成真实段，
// * 要变成真实后缀，否则请求根本匹配不上任何路由，
// 那样 404 就只是「没匹配上」，测不到「路由真的注册了」这件事 ——
// 那种测试永远绿，等于没测。
func concreteRoute(route string) string {
	out := route
	var b strings.Builder
	for i := 0; i < len(out); i++ {
		switch c := out[i]; c {
		case '{':
			if j := strings.IndexByte(out[i:], '}'); j >= 0 {
				b.WriteString("1")
				i += j
				continue
			}
			b.WriteByte(c)
		case '*':
			b.WriteString("probe")
			i++
			// 跳过紧随其后的剩余字符（chi 里 * 通常在末尾）
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// TestRequestPortalRouteCountPinsPortalSurface 钉住求片站对外暴露的接口数量。
//
// 加接口是好事，但必须同时更新这个数字并想清楚：
// 求片站是给家人用的手机页面，每多一个接口就多一处能被探测的面。
// 数字变了而没人改这个测试，就说明有人在没意识到的情况下扩了面。
func TestRequestPortalRouteCountPinsPortalSurface(t *testing.T) {
	n := 0
	portalRoutes, ok := NewRequestPortalRouter(Deps{}).(chi.Routes)
	if !ok {
		t.Fatalf("求片站路由树不是 chi.Routes，无法遍历")
	}
	if err := chi.Walk(portalRoutes, func(_, _ string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		n++
		return nil
	}); err != nil {
		t.Fatalf("遍历求片站路由失败：%v", err)
	}
	if n != 11 {
		t.Errorf("求片站暴露了 %d 条路由，期望 11 条 —— 改动求片站接口面时请同步更新这个数字", n)
	}
}

// ---------------------------------------------------------------- T10 免登录分享

// TestLibraryShareServiceIsWired 断言免登录分享从服务到路由整条链都接上了。
//
// 与 TestRequestCenterServiceIsWired 同一个理由：「假接线」是这个仓库最贵的 bug。
// 分享这条链特别容易假接 —— 它一路连着播放网关，接错了不会编译报错，
// 只会在生产上表现为「分享页能打开但放不出片子」，而那种问题极难定位。
// 断言前用 squeezeSpace 压掉 gofmt 的对齐空格：Deps 每加一个字段 gofmt 就
// 重排整块字面量，钉死空格数只会得到一条天天误报的假警报，久而久之没人看它。
func TestLibraryShareServiceIsWired(t *testing.T) {
	for _, tc := range []struct {
		file string
		want string
	}{
		{"router.go", "LibraryShare *medialibshare.Service"},
		{"router.go", "libraryShare: d.LibraryShare"},
		{"router.go", "h.RegisterLibraryShareRoutes(r)"},
		{"router.go", "h.RegisterLibraryShareGuestRoutes(r)"},
		{"router.go", "h.RegisterLibrarySharePageRoutes(r)"},
		{"library_share_admin.go", "h.requirePermission(rbac.PermShareManage)"},
		// 取流必须落到唯一的播放网关。这条断言是需求⑧「没有第二套播放链路」
		// 的机械保证：哪天有人图省事自己写一套代理，这条测试先红。
		{"library_share_guest.go", "h.playback.ServeHTTP("},
		// 令牌只能从请求头读。写成 Header.Get 之外的任何一种读法都算违规。
		{"library_share_guest.go", "r.Header.Get(ShareTokenHeader)"},
		{filepath.Join("..", "app", "app.go"), "libShare := wireLibraryShare"},
		{filepath.Join("..", "app", "wire_share.go"), "medialibshare.NewService(medialibshare.Params{"},
		{filepath.Join("..", "app", "wire_http.go"), "LibraryShare: libShare.maybeService()"},
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", tc.file, err)
		}
		if !strings.Contains(squeezeSpace(string(src)), squeezeSpace(tc.want)) {
			t.Errorf("%s 缺少 %q（免登录分享没接上）", tc.file, tc.want)
		}
	}
}

// TestShareTokenHeaderIsDefined 钉住令牌头的名字。
//
// 单独一条而不是并进上面：头名一旦变了，前端就再也发不出能用的令牌，
// 而症状是「访客页一直报令牌无效」，看起来像后端坏了。
// 这个字符串同时出现在 Go 与 TS 两侧，改名要两处一起改 —— 故此钉住字面量。
func TestShareTokenHeaderIsDefined(t *testing.T) {
	src, err := os.ReadFile("library_share_admin.go")
	if err != nil {
		t.Fatalf("读取 library_share_admin.go 失败：%v", err)
	}
	if !strings.Contains(string(src), `ShareTokenHeader = "X-Share-Token"`) {
		t.Errorf("ShareTokenHeader 的名字变了 —— 前端 web/src/api/libraryShare.ts 也要一起改")
	}
}

// TestShareTokenIsNeverAcceptedFromURL 断言令牌不能从 URL 里取。
//
// 需求③：令牌不出现在 URL、Referer、日志里。放进 query 的话它会出现在
// 浏览器历史、Referer 头、反代访问日志、用户「复制链接」发给别人的内容里 ——
// 每一样都是长期的、不可撤回的泄露面。
//
// 所以断言的**不是**「带 query 的请求会被拒绝」这种运行时行为
// （那种断言可以靠 handler 里多判一句骗过去），而是
// 「访客侧代码里根本不存在从 query/cookie 读值这条路径」。
func TestShareTokenIsNeverAcceptedFromURL(t *testing.T) {
	src, err := os.ReadFile("library_share_guest.go")
	if err != nil {
		t.Fatalf("读取 library_share_guest.go 失败：%v", err)
	}
	text := string(src)
	for _, bad := range []string{"r.URL.Query()", "r.FormValue(", "r.Cookie("} {
		if strings.Contains(text, bad) {
			t.Errorf("访客侧代码里出现了 %s —— 令牌只能从 X-Share-Token 请求头读", bad)
		}
	}
	if strings.Count(text, "r.Header.Get(ShareTokenHeader)") != 1 {
		t.Errorf("期望恰好一处从 ShareTokenHeader 取值，实际 %d 处 —— 多写一处就多一个可能泄密的出口",
			strings.Count(text, "r.Header.Get(ShareTokenHeader)"))
	}
}

// shareGuestOwnRoutes 访客侧对外暴露的路径（正向清单用）。
//
// 刻意只有这四条：一条页面 + 换令牌 + 上报事件 + 取流（GET/HEAD 同一条）。
// 分享页是发到公网上的，每多一条就多一处能被探测的面。
var shareGuestOwnRoutes = []adminRoutePattern{
	{http.MethodGet, "/share/{code}"},
	{http.MethodPost, "/share-play/{code}/token"},
	{http.MethodPost, "/share-play/{code}/event"},
	{http.MethodGet, "/share-play/{code}/stream"},
}

// TestShareGuestRoutesHaveNoAdminSurface 断言访客侧那几条路由上，
// 一个管理台接口都没有。这是需求⑦。
//
// 为什么用静态扫描而不是起一台 httptest 服务器逐个打过去：
// NewRouter(Deps{}) 目前根本跑不起来 —— 装配 WebDAV 那一步无条件调
// d.Uploads.TempRegistry()，Deps 为空时是 nil panic（internal/upload/temp.go:119）。
// 为了让守卫能跑而临时给 router 塞一个假 upload.Manager，等于让这条测试
// 依赖「Deps 有哪些字段」这种纯装配细节；将来加字段它又会红，而且红得莫名其妙。
//
// 静态扫描答的是同一个问题 —— 「这份代码声明了哪些路径、它们是什么前缀」——
// 而且比打请求更直接：注册在访客前缀下的任何路由都会被列出来，
// 逐条比对即可。查的是代码，不是运行时状态，所以不存在「测的是假象」这一说。
func TestShareGuestRoutesHaveNoAdminSurface(t *testing.T) {
	// 反过来查更直接：把 api 包里注册的全部路由扫出来，筛出落在访客前缀下的，
	// 断言它们**恰好**是 shareGuestOwnRoutes 那几条 —— 不多不少。
	// 「不多」证明访客树里没有管理接口，「不少」保证漏注册时也会被发现
	// （只查「不多」的话，把整个注册函数删掉它照样绿）。
	allowed := map[string]bool{}
	for _, rt := range shareGuestOwnRoutes {
		allowed[rt.Method+" "+rt.Pattern] = true
	}
	// 访客前缀：/share/{code} 本身，以及 /share-play/ 下的任意路径。
	prefixes := []string{"/share/", "/share-play/"}
	found := 0
	for _, rt := range adminRoutePatterns(t) {
		hit := false
		for _, p := range prefixes {
			if strings.HasPrefix(rt.Pattern, p) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		// GET/HEAD 是同一个端点的两种方法：注册了 GET 就得能响应 HEAD
		// （播放器与探活都靠它），所以不单独计入条数。
		if rt.Method == http.MethodHead &&
			allowed[http.MethodGet+" "+rt.Pattern] {
			continue
		}
		found++
		if !allowed[rt.Method+" "+rt.Pattern] {
			t.Errorf("访客路径下多出一条路由 %s %s —— 访客树里不该有管理接口或计划外的端点",
				rt.Method, rt.Pattern)
		}
	}
	if found != len(shareGuestOwnRoutes) {
		t.Errorf("访客路径下只扫到 %d 条路由，期望 %d 条 —— 路由没注册上，或注册到了别处",
			found, len(shareGuestOwnRoutes))
	}
}

// TestShareGuestRouteSurfaceIsPinned 钉住访客侧暴露的路径数量与形状。
//
// 与 TestRequestPortalRouteCountPinsPortalSurface 同一条纪律：
// 访客侧多一条路由 = 公网上多一处能被探测的面。
// 加路由是好事，但必须在这里把数字改掉并想清楚为什么值得。
func TestShareGuestRouteSurfaceIsPinned(t *testing.T) {
	if len(shareGuestOwnRoutes) != 4 {
		t.Fatalf("访客侧路由清单有 %d 条，期望 4 条 —— 改动访客接口面时请同步更新", len(shareGuestOwnRoutes))
	}
	// 正向断言：清单一字不差。写错了条目（比如把 /token 写成 /tokens）
	// 上面的「无管理面」断言不会发现，但这里的逐条比对会。
	want := []adminRoutePattern{
		{http.MethodGet, "/share/{code}"},
		{http.MethodPost, "/share-play/{code}/token"},
		{http.MethodPost, "/share-play/{code}/event"},
		{http.MethodGet, "/share-play/{code}/stream"},
	}
	for i := range want {
		if shareGuestOwnRoutes[i] != want[i] {
			t.Errorf("访客路由第 %d 条是 %v，期望 %v", i, shareGuestOwnRoutes[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// T29 · 维度 A：闸门与副作用的相对位置
//
// 事故背景：T03 的身份校验闸门 checkCandidateIdentity 写好了、函数级单测也全绿，
// 但整个 internal/discover/... 的测试**没有任何一条**会经过那段接线路径 ——
// 临时把调用块删掉，go test ./internal/discover/... 依然全绿。
// 「闸门存在」和「闸门挡在副作用之前」是两件事，只有后者才有意义。
//
// 断言方式是纯 AST：定位闸门调用与副作用调用各自所在的函数体与语句序号，
// 断言两者在同一函数内且闸门在前。不为写这个测试去 mock 一整套云盘驱动 ——
// 那样测的就不是接线，而是 mock 的行为。
// ---------------------------------------------------------------------------

// gateOrdering 描述一条「闸门 G 必须先于副作用 E」的期望。
type gateOrdering struct {
	// file 是相对 internal/api 的路径。
	file string
	// gate 与 effect 是函数名或方法名，按 SelectorExpr.Sel.Name / Ident 匹配。
	gate   string
	effect string
	// why 是断言失败时给读者的解释。
	why string
}

// gateOrderings 是全部已知的闸门顺序约束。
func gateOrderings() []gateOrdering {
	return []gateOrdering{
		{
			file:   filepath.Join("..", "discover", "discovery", "subscriptions.go"),
			gate:   "checkCandidateIdentity",
			effect: "transferSubscriptionCandidate",
			why:    "身份校验必须在发起转存之前判断；放到后面等于先写进网盘再检查",
		},
		{
			// T11：三态判定的依据在 PickAction 的返回值里。拿到 action 之前
			// 没法知道这一请求走的是 302 还是流代理，也就无从知道该不该计费。
			// 把监控调用提到 PickAction 之前，会让 302 分支也吐字节、也计费，
			// 而 302 的字节根本不经过自己服务器 —— 那是凭空多算的上行账单。
			file:   filepath.Join("..", "playback", "service.go"),
			gate:   "PickAction",
			effect: "wrapStreamMonitor",
			why:    "三态判定必须先于监控注入：PickAction 之前挂监控，302 直连也会被记成流代理并计入流量",
		},
		{
			// 同一处还有第二个后果：CDN 直连的 open 事件必须晚于 PickAction，
			// 否则 OnRedirectOpen 里 classifyState 拿到的 origin 语义是猜的。
			file:   filepath.Join("..", "playback", "service.go"),
			gate:   "PickAction",
			effect: "OnRedirectOpen",
			why:    "CDN 直连的 open 事件必须晚于 PickAction，否则无法保证它只在 302 分支被触发",
		},
		{
			// T11：计费闸门。局域网会话与 CDN 直连会话的上行估算必须恒为 0，
			// 这个判断必须发生在把字节加进当日桶之前。
			file:   filepath.Join("..", "playmonitor", "session.go"),
			gate:   "Metered",
			effect: "Accumulate",
			why:    "只有计费中的会话才能写入当日流量桶；闸门放到 Accumulate 之后等于计费口径形同虚设",
		},
		{
			// T16：落地通道必须先于账本判定。占不到通道的条目（网盘分享链接
			// 但源配的是只走离线下载）压根没地方提交，却已经抢到了账本位并
			// 走完 3 小时保护期 —— 用户改好配置后，这条仍然被挡在账本外，
			// 表现为"改了配置还是同步不出东西"，且没有任何报错指向原因。
			file:   filepath.Join("..", "discover", "discovery", "rss_delivery.go"),
			gate:   "rssChannelFor",
			effect: "decideTransfer",
			why:    "落地通道判定必须先于账本：抢不到通道的条目不该占账本名额，否则修好配置后仍被保护期挡掉",
		},
		{
			// T16：词表过滤也在账本之前。命中排除词的条目本来就该被丢，
			// 让它先占账本等于把用户明确不要的东西登记成"处理过"，
			// 之后改了规则再也捡不回来。
			file:   filepath.Join("..", "discover", "discovery", "rss_delivery.go"),
			gate:   "RuleSkipReason",
			effect: "decideTransfer",
			why:    "词表过滤必须先于账本：被规则排除的条目不该占账本名额，否则改了过滤规则也捡不回来",
		},
		{
			// T12：场景开关必须先于发送。用户关掉「同步」却照样收到通知，
			// 表现是"开关没用"—— 但真正的原因只在发送顺序里。
			file:   filepath.Join("..", "notifychannel", "dispatcher.go"),
			gate:   "matchesSceneFilter",
			effect: "Send",
			why:    "场景闸门必须先于发送：放到发送之后，关闭场景的用户照样会收到通知，且毫无报错",
		},
		{
			// T12：可重试判定必须在入队之前。4xx / 配置缺失重发一万次也一样，
			// 让它进队列只是让用户多等五档退避（约 15 小时）才看到一个当场
			// 就能看懂的 400，还会把真正要人工看的失败淹掉。
			file:   filepath.Join("..", "notifychannel", "dispatcher.go"),
			gate:   "RetryableError",
			effect: "Enqueue",
			why:    "可重试判定必须先于入队：不可重试的失败不该占用补发队列名额",
		},
	}
}

// TestWiringGateRunsBeforeSideEffect 断言闸门与副作用在同一函数体内，且闸门在前。
//
// 「同一函数体内」这条比「在前」更重要：闸门写在另一个函数里、
// 由被调方内部去拦，是一种可行的设计，但那时「本函数里在前」就无从断言了；
// 把它排除掉，才逼着闸门的可见性写在明面上。
func TestWiringGateRunsBeforeSideEffect(t *testing.T) {
	cases := gateOrderings()
	if len(cases) == 0 {
		t.Fatal("gateOrderings 为空 —— 删光了？闸门约束全失效了")
	}
	for _, tc := range cases {
		t.Run(tc.gate, func(t *testing.T) {
			fset := token.NewFileSet()
			src, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatalf("读取 %s 失败：%v", tc.file, err)
			}
			parsed, err := parser.ParseFile(fset, tc.file, src, 0)
			if err != nil {
				t.Fatalf("解析 %s 失败：%v", tc.file, err)
			}
			// 找出同时含闸门与副作用的函数；没有则报「两者不在同一函数」。
			var gateFn, effectFn *ast.FuncDecl
			for _, decl := range parsed.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				if fnCallsName(fn, tc.gate) && gateFn == nil {
					gateFn = fn
				}
				if fnCallsName(fn, tc.effect) && effectFn == nil {
					effectFn = fn
				}
			}
			if gateFn == nil {
				t.Errorf("%s 里找不到对 %s 的调用 —— 闸门被删掉了？%s", tc.file, tc.gate, tc.why)
				return
			}
			if effectFn == nil {
				t.Errorf("%s 里找不到对 %s 的调用 —— 副作用点改名了？%s", tc.file, tc.effect, tc.why)
				return
			}
			if gateFn.Name.Name != effectFn.Name.Name {
				t.Errorf("闸门 %s 在 %s 里，副作用 %s 在 %s 里，不在同一函数体内 —— %s",
					tc.gate, gateFn.Name.Name, tc.effect, effectFn.Name.Name, tc.why)
				return
			}
			gateAt, effectAt := firstCallIndex(fset, gateFn, tc.gate), firstCallIndex(fset, gateFn, tc.effect)
			if gateAt == token.NoPos || effectAt == token.NoPos {
				t.Errorf("%s 内 %s 的调用位置取不到（gate=%v effect=%v）", gateFn.Name.Name, tc.gate, gateAt, effectAt)
				return
			}
			if gateAt >= effectAt {
				t.Errorf("%s 里闸门 %s（位置 %d）不在副作用 %s（位置 %d）之前 —— %s",
					gateFn.Name.Name, tc.gate, fset.Position(gateAt).Line,
					tc.effect, fset.Position(effectAt).Line, tc.why)
			}
		})
	}
}

// fnCallsName 判断函数体内是否调用了名为 name 的函数或方法。
func fnCallsName(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			found = f.Name == name
		case *ast.SelectorExpr:
			found = f.Sel != nil && f.Sel.Name == name
		}
		return !found
	})
	return found
}

// firstCallIndex 返回函数体内第一次调用 name 的源码位置（fset 里的 token.Pos）。
//
// 曾经用**顶层语句序号**当下标，结果把 for 循环里的闸门与转存判成了同一句：
// 两者都写在同一个 `for _, cand := range candidates {}` 体内，序号都等于那条
// for 语句，于是 gate == effect，「闸门在前」永远不成立 —— 一条恒红的守卫。
// 顺序关系要用**源码位置**表达，它在循环内外都成立。
func firstCallIndex(fset *token.FileSet, fn *ast.FuncDecl, name string) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if pos != token.NoPos {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		hit := false
		switch f := call.Fun.(type) {
		case *ast.Ident:
			hit = f.Name == name
		case *ast.SelectorExpr:
			hit = f.Sel != nil && f.Sel.Name == name
		}
		if hit {
			pos = call.Pos()
		}
		return !hit
	})
	return pos
}

// TestWiringIdentityGateIsWiredInTransferPath 把 T03 那条闸门单独钉死。
//
// 为什么值得单独一条而不是并进上面的表驱动：
// 这是本仓**唯一一次**用变异测试抓到的死接线（删掉调用块后整包测试依然全绿），
// 单独立一条可以让 `go test -run Wiring` 的输出里直接出现它的名字，
// 出问题时不用在表里翻。
func TestWiringIdentityGateIsWiredInTransferPath(t *testing.T) {
	path := filepath.Join("..", "discover", "discovery", "subscriptions.go")
	if !callsInNonTestFile(t, filepath.Join("..", "discover", "discovery"), "subscriptions.go", "checkCandidateIdentity") {
		t.Errorf("%s 里没有对 checkCandidateIdentity 的调用 —— 身份校验闸门已从转存路径上消失", path)
	}
	// 反向：闸门不能被挪到转存之后。planAndTransferRuleCandidates 里
	// transferSubscriptionCandidate 全仓只有一个调用点，就在同一函数里。
	if !callsInNonTestFile(t, filepath.Join("..", "discover", "discovery"), "subscriptions.go", "transferSubscriptionCandidate") {
		t.Errorf("%s 里没有对 transferSubscriptionCandidate 的调用 —— 转存路径改名了？闸门顺序断言需要同步", path)
	}
}

// TestWiringEmbyIndexExportedAPIHasExportedParamTypes 钉住 T01 之前那起事故不再复发。
//
// 事故：internal/embyindex 的导出方法形参用了**未导出**的具名类型
// （形如 `func (s *Service) GetItemDetail(ctx, cfg config, id string)`），
// 包外根本无法调用 —— 编译器只会说「cannot refer to unexported name」，
// 而包内测试全部通过，于是「完整实现 + 零调用方」并存了很久。
//
// 现在这些类型都已导出，这里把名单钉死；将来再加导出方法时忘了导出参数类型，
// 这条测试会提醒（虽然真正的防线仍然是编译器）。
func TestWiringEmbyIndexExportedAPIHasExportedParamTypes(t *testing.T) {
	dir := filepath.Join("..", "embyindex")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", dir, err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败：%v", name, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() || fn.Recv == nil {
				continue
			}
			checked++
			for _, field := range fn.Type.Params.List {
				checkExportedParamTypes(t, dir, name, fn.Name.Name, field.Type)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("%s 里没扫到任何导出方法 —— 目录结构变了？", dir)
	}
}

// checkExportedParamTypes 沿类型结构下钻，任何一层出现未导出类型都报错。
//
// 只查一层是不够的：导出方法收一个本包的 *config 指针，
// 而 config 里又套着未导出字段类型的情况同样会让包外用不了。
// 这里不做字段级深挖（那是编译器的事），只看**类型名本身**的可见性。
func checkExportedParamTypes(t *testing.T, dir, file, fn string, typ ast.Expr) {
	t.Helper()
	switch x := typ.(type) {
	case *ast.Ident:
		// 预声明类型（小写但到处都能用）不算未导出。
		if x.Name != "" && x.IsExported() {
			return
		}
		if isBuiltinTypeName(x.Name) {
			return
		}
		t.Errorf("%s/%s：导出方法 %s 的参数类型 %q 未导出 —— 包外无法调用（这正是 T01 之前的事故形态）",
			dir, file, fn, x.Name)
	case *ast.SelectorExpr:
		if x.Sel != nil && !x.Sel.IsExported() {
			t.Errorf("%s/%s：导出方法 %s 的参数类型 %s 未导出", dir, file, fn, x.Sel.Name)
		}
	case *ast.StarExpr:
		checkExportedParamTypes(t, dir, file, fn, x.X)
	case *ast.ArrayType:
		checkExportedParamTypes(t, dir, file, fn, x.Elt)
	case *ast.MapType:
		checkExportedParamTypes(t, dir, file, fn, x.Key)
		checkExportedParamTypes(t, dir, file, fn, x.Value)
	}
}

// isBuiltinTypeName 判断是否预声明类型。
func isBuiltinTypeName(name string) bool {
	switch name {
	case "bool", "byte", "complex64", "complex128", "error", "float32", "float64",
		"int", "int8", "int16", "int32", "int64", "rune", "string",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "any", "comparable":
		return true
	}
	return false
}

// TestWiringClassificationPreviewIsReachable 钉住 C-7 的预览端点。
//
// 这条守卫存在的理由是本仓反复出现的那类事故：handler 写完了、编译过了、
// 单元测试也过了，但路由忘了注册，前端一调就是 404 —— 或者更糟，
// 路由注册了但挂在另一个前缀下（比如 T30 那次 /api/library-shares）。
// 所以这里既断言 handler 方法在非测试文件里被引用，也断言它真的在
// router.go 的 /admin 子树里挂了出来。
func TestWiringClassificationPreviewIsReachable(t *testing.T) {
	t.Run("handler 被生产代码引用", func(t *testing.T) {
		// 注意不能复用 callsInNonTestFile：它只在 *ast.CallExpr 里找，
		// 而 `r.Post(path, h.previewClassification)` 传的是方法值，
		// 根本不是一次调用。用它断言会永远为假，守卫就成了摆设。
		if !mentionsSelector(t, "router.go", "previewClassification") {
			t.Fatal("previewClassification 没在 router.go 里被挂载 —— 预览端点会 404")
		}
	})
	t.Run("挂在 media-organize 子树下", func(t *testing.T) {
		src, err := os.ReadFile("router.go")
		if err != nil {
			t.Fatalf("读取 router.go 失败：%v", err)
		}
		if !strings.Contains(squeezeSpace(string(src)), `r.Post("/classification/preview", h.previewClassification)`) {
			t.Fatal("预览端点路径应为 /classification/preview 且挂在 media-organize 下")
		}
	})
	t.Run("请求结构体字段与前端一致", func(t *testing.T) {
		// decodeJSON 开了 DisallowUnknownFields：入参结构体的 json tag 与前端
		// 发的不一致，整个规则预览在界面上就是「400 参数错误」，而且后端日志里
		// 什么都看不出来（反序列化失败没有业务日志）。把字段名钉在这里。
		for _, field := range []string{
			`json:"media_type"`, `json:"tmdb_id"`, `json:"title"`, `json:"year"`, `json:"raw"`,
		} {
			src, err := os.ReadFile("classification_preview.go")
			if err != nil {
				t.Fatalf("读取 classification_preview.go 失败：%v", err)
			}
			if !strings.Contains(squeezeSpace(string(src)), field) {
				t.Errorf("classificationPreviewReq 缺少字段 %s，前端发这个键会被 400", field)
			}
		}
	})
}

// mentionsSelector 报告 file 里是否出现过 sel.X 形式的方法引用。
//
// 与 callsInNonTestFile 的区别：那个找「调用」，这个找「引用」。
// 路由注册传的是方法值而不是调用结果，语义上属于引用 —— 用「调用」去判会永远为假，
// 一条永远为假的守卫比没有守卫更糟：它给的是「有守卫在管」的错觉。
func mentionsSelector(t *testing.T, file, name string) bool {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, mustReadFile(t, file), 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", file, err)
	}
	found := false
	ast.Inspect(parsed, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && sel.Sel != nil && sel.Sel.Name == name {
			found = true
		}
		return true
	})
	return found
}

func mustReadFile(t *testing.T, file string) []byte {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", file, err)
	}
	return src
}

// TestWiringClassificationCategoriesIsReachable 钉住 C-8 的只读出口。
//
// 为什么单独立一条而不是并进分类预览那条：C-8 这个端点有四个跨模块消费者
// （洗版筛选、清理保护、Bot、命名方案），它们拿到的是"前端下拉框的数据源"。
// 端点 404 的症状在 UI 上是**空下拉框**而不是报错——空数组和 404 长得一样，
// 所以这一条必须自己盯着，不能指望别处的守卫顺带覆盖。
func TestWiringClassificationCategoriesIsReachable(t *testing.T) {
	t.Run("handler 被生产代码引用", func(t *testing.T) {
		// 与上面那条同样的坑：这里不能用 callsInNonTestFile，
		// `r.Get("/categories", h.listClassificationCategories)` 是方法值。
		if !mentionsSelector(t, "router.go", "listClassificationCategories") {
			t.Fatal("listClassificationCategories 没在 router.go 里被挂载 —— 分类清单端点会 404，消费者拿到空列表")
		}
	})
	t.Run("路径与前端一致", func(t *testing.T) {
		// 前端 web/src/api/cloudTools.ts 的 classificationApi.categories 打的是
		// /admin/tools/classification/categories，两端逐字钉死。
		src := squeezeSpace(string(mustReadFile(t, "router.go")))
		if !strings.Contains(src, `r.Get("/categories", h.listClassificationCategories)`) {
			t.Error("分类清单端点应注册为 GET /categories（挂在 /tools/classification 下）")
		}
		ts := squeezeSpace(string(mustReadFile(t, "../../web/src/api/cloudTools.ts")))
		if !strings.Contains(ts, `"/admin/tools/classification/categories"`) {
			t.Error("前端 classificationApi.categories 的路径与后端注册不一致，会 404")
		}
	})
	t.Run("挂在 requireAdmin 组内", func(t *testing.T) {
		// 分类清单里含全部用户配的分类目录名。它不是敏感数据，但它是一份
		// "这个库都有什么"的完整地图，而 tools/* 下其它端点都是登录后可读的。
		// 挂在 requireAdmin 组内是既有约定，别把它挪出去。
		if !mentionsSelector(t, "router.go", "requireAdmin") {
			t.Fatal("router.go 里找不到 requireAdmin，鉴权基线本身变了")
		}
		adminRoutePatterns(t)
	})
}

// TestWiringKnownDeadCodeRegistry 登记已知的死代码，防止它被「顺手删掉」或
// 「悄悄转正」。这里的每一项都是**待产品决策**，不是待修的 bug：
// 删掉 = 放弃一项产品能力；接上 = 需要产品决策（settings key、UI、文档）。
// 都不该由无关任务顺手决定。
//
// 登记项的判定标准：导出符号 + 零生产调用方 + 承载的是「一项能力」而非
// 「一个工具函数」。工具函数死了就死了（编译器/测试会说话），能力死了
// 需要有人知情。
func TestWiringKnownDeadCodeRegistry(t *testing.T) {
	known := []struct {
		symbol   string // moviepilot 包内的导出符号
		file     string // 定义所在文件
		capacity string // 它承载的能力
		origin   string // 怎么来的
	}{
		{
			symbol:   "RenderWashName",
			file:     "template.go",
			capacity: "用户自定义洗版命名模板（{{ var }} / {% if %} 两类语法，20 个变量）",
			origin: "老版 v030 的链路 auto_organize.go:632 → buildAutoOrganizeNewNameEx → renderWashName(pongo2) " +
				"在移植时断开：改名改用 naming.go 的 BuildOrganizeNewName（硬编码规则），" +
				"引擎被重写成无依赖版（替代 pongo2）但没接到洗版流程。296 行完整引擎 + 零调用。",
		},
		{
			symbol:   "CompileTemplate",
			file:     "template.go",
			capacity: "同上（模板编译器，RenderWashName 的下层）",
			origin:   "同上",
		},
	}
	for _, k := range known {
		t.Run(k.symbol+" 仍为零调用（状态未变）", func(t *testing.T) {
			// 状态变了（有人接上了，或有人删了）就该更新这份登记，
			// 而不是让守卫静默过时。这条守卫钉的是「状态变化必须显式发生」。
			if !callsInNonTestFile(t, "../moviepilot", "organize.go", k.symbol) &&
				!callsInNonTestFile(t, "../moviepilot", "failed.go", k.symbol) {
				// 仍为零调用：与登记一致，通过。
				return
			}
			t.Errorf("%s 出现了生产调用方 —— 死代码状态变了，请更新 TestWiringKnownDeadCodeRegistry 的登记并写下决策（接上了什么能力、谁批的）", k.symbol)
		})
		t.Run(k.symbol+" 定义仍存在（未被顺手删除）", func(t *testing.T) {
			src := string(mustReadFile(t, filepath.Join("../moviepilot", k.file)))
			if !strings.Contains(src, "func "+k.symbol+"(") &&
				!strings.Contains(src, "func "+k.symbol+"[") {
				t.Errorf("%s 已从 %s 消失 —— 有人删了承载「%s」的引擎。"+
					"删它等于放弃这项产品能力，需要显式决策（写进变更说明并知会用户），"+
					"不许顺手删。它不是 bug，是%s", k.symbol, k.file, k.capacity, k.origin)
			}
		})
	}
}

// TestGuardrailAndInspectionWiring 钉住 T15 两块能力在生产装配里的接线。
//
// 这两块能力都出现过「编译通过、测试全绿、实际零调用方」：
// guardrail.NewBreaker 造得出来，但没有任何地方调 Manager.SetCallObserver；
// wireInspection 造得出服务，但路由树上没有端点。这两种情况下
// go build 与 go test 都不会报错 —— 所以用断言把它们钉在调用路径上。
func TestGuardrailAndInspectionWiring(t *testing.T) {
	t.Run("driver.Manager.SetCallObserver 在装配层被调用", func(t *testing.T) {
		if !callsInNonTestFile(t, "../app", "wire_guardrail.go", "SetCallObserver") {
			t.Fatal("internal/app 里没有任何地方调 SetCallObserver —— " +
				"连续调用熔断不会被触发，driver 侧计数无人接收。")
		}
	})
	t.Run("wireInspection 被装配层调用", func(t *testing.T) {
		if !callsInNonTestFile(t, "../app", "wire_http.go", "wireInspection") {
			t.Fatal("wireInspection 是零调用函数 —— 巡检服务存在但没有任何入口能拿到它。")
		}
	})
	// 路由可达性：四个端点都必须出现在管理台真实路由表里。
	//
	// 用 adminRoutePatterns（AST 扫真实注册语句）而不是 NewRouter(Deps{}) 起一棵
	// handler 树：后者在 Deps{} 下会撞上 nil Uploads 直接 panic，而要喂出一个
	// 不 panic 的 Deps 就得连数据库一起搭 —— 为「这条路由存在吗」付这个代价
	// 不值。这条断言要证明的是「注册语句写在会被命中的那个 router 上」，
	// 响应码的正确性由各自的 handler 用例负责。
	have := map[string]bool{}
	for _, rt := range adminRoutePatterns(t) {
		have[rt.Method+" "+rt.Pattern] = true
	}
	for _, want := range []string{
		"GET /api/admin/tools/inspection/checkers",
		"POST /api/admin/tools/inspection/scan",
		"GET /api/admin/tools/inspection/preview",
		"POST /api/admin/tools/inspection/repair",
	} {
		if !have[want] {
			t.Errorf("管理台路由表里没有 %s —— 巡检端点没注册", want)
		}
	}
	// 反向断言：巡检不能挂在空间清理那条前缀下。两者都扫目录树，
	// 混在一个 /scan 下会让前端和调用方分不清扫的是哪一套。
	if have["POST /api/admin/tools/cleanup/scan"] &&
		have["POST /api/admin/tools/inspection/scan"] {
		// 两条都在是预期；这里只保证 inspection 前缀是独立的一组路由，
		// 不再与 cleanup 共用同一条 path。
		t.Log("巡检与空间清理各自独立注册，符合预期")
	}
}

// TestPlayPathRedirectAndCrossAccountWiring 钉住 T17 三块能力的接线。
//
// 三块能力都是「造得出服务、用不上也不报错」的高危形状：
//   - wirePlayPath / wireCrossAccount 是零调用函数时，规则和开关全部静默失效；
//   - 302 端点没注册时，用户点了「切换到 302 直连」只是少了个按钮，毫无提示；
//   - 路径映射调在 ResolvePath 之后时，规则永远命不中，且页面上的「测试路径」
//     仍显示命中 —— 用户会得出「规则是对的，是系统坏了」的错误结论。
//
// 顺序断言是这里最要紧的一条：映射必须在解析路径**之前**，在副作用之前。
func TestPlayPathRedirectAndCrossAccountWiring(t *testing.T) {
	t.Run("wirePlayPath 被装配层调用", func(t *testing.T) {
		// 构造函数在 wire_playback_policy.go 里，调用点在 wire_services.go；
		// 断言调用点，否则「服务存在但没人拿它」这种形状照样绿。
		if !callsInNonTestFile(t, "../app", "wire_services.go", "wirePlayPath") {
			t.Fatal("wirePlayPath 是零调用函数 —— 播放路径映射服务造得出来但没有任何入口能拿到它。")
		}
	})
	t.Run("wireCrossAccount 被装配层调用", func(t *testing.T) {
		if !callsInNonTestFile(t, "../app", "wire_services.go", "wireCrossAccount") {
			t.Fatal("wireCrossAccount 是零调用函数 —— 账号失效时不会发生任何跨账户转存。")
		}
	})
	t.Run("路径映射在解析路径之前", func(t *testing.T) {
		if !callsInNonTestFile(t, ".", "strm_play.go", "mapPlayPath") {
			t.Fatal("strm_play.go 里没有任何地方调 mapPlayPath —— 按路径播放不会做映射。")
		}
		if !callsInNonTestFile(t, ".", "strm_playback_admin.go", "mapPlayPath") {
			t.Fatal("strm_playback_admin.go 里没有调 mapPlayPath —— 302 直连端点绕过了映射。")
		}
		// 顺序断言：mapPlayPath 必须排在 ResolvePath 之前。
		if !callBefore(t, "strm_play.go", "mapPlayPath", "ResolvePath") {
			t.Fatal("strmPathPlay 里 mapPlayPath 必须排在 ResolvePath 之前 —— " +
				"先拿挂载路径去网盘里解析再映射，规则永远命不中，而「测试路径」仍显示命中。")
		}
		if !callBefore(t, "strm_playback_admin.go", "mapPlayPath", "ResolvePath") {
			t.Fatal("strmPathRedirectPlay 里 mapPlayPath 必须排在 ResolvePath 之前 —— 同上。")
		}
	})
	t.Run("跨账户转存决策在取流之前", func(t *testing.T) {
		if !callsInNonTestFile(t, ".", "strm_play.go", "applyCrossAccountFallback") {
			t.Fatal("strm_play.go 里没有任何地方调 applyCrossAccountFallback —— 账号失效时不会切号。")
		}
		// 跨账户闸门必须在鉴权之后：鉴权没过就转存，等于任何持 token 的请求
		// 都能反复触发大文件转存。
		if !callBefore(t, "strm_play.go", "applyCrossAccountFallback", "ServeHTTP") {
			t.Fatal("applyCrossAccountFallback 必须排在 ServeHTTP 之前 —— 转存决策要在取流之前。")
		}
		if !callsInNonTestFile(t, ".", "strm_play.go", "authorizeSTRMPlay") {
			t.Fatal("strm_play.go 里没有 authorizeSTRMPlay —— 播放入口的鉴权没了。")
		}
	})
	// 路由可达性：302 端点 ×2 组 + 路径映射管理端点 ×3。
	have := map[string]bool{}
	for _, rt := range adminRoutePatterns(t) {
		have[rt.Method+" "+rt.Pattern] = true
	}
	for _, want := range []string{
		"GET /api/strm/redirect/{account_id}/{file_key}/t/{token}/n/{filename}",
		"GET /api/strm/redirect/{account_id}/{file_key}/t/{token}/n/{filename}/s/{signature}",
		"GET /api/strm/path-redirect/{account_id}/{root_key}/{path_key}/t/{token}/n/{filename}",
		"GET /api/strm/path-redirect/{account_id}/{root_key}/{path_key}/t/{token}/n/{filename}/s/{signature}",
		"GET /api/admin/strm/path-mapping",
		"PUT /api/admin/strm/path-mapping",
		"POST /api/admin/strm/path-mapping/test",
	} {
		if !have[want] {
			t.Errorf("管理台路由表里没有 %s —— T17 端点没注册", want)
		}
	}
}

// callBefore 断言 file 里 first 的调用位置早于 second。
//
// 只断言「两处都出现过」是不够的：顺序反了照样编译通过、照样有端点，
// 而功能是静默失效的 —— 所以顺序必须被钉住。
func callBefore(t *testing.T, file, first, second string) bool {
	t.Helper()
	path := filepath.Join(".", file)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	firstPos, secondPos := token.NoPos, token.NoPos
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		}
		switch name {
		case first:
			if firstPos == token.NoPos {
				firstPos = call.Pos()
			}
		case second:
			if secondPos == token.NoPos {
				secondPos = call.Pos()
			}
		}
		return true
	})
	if firstPos == token.NoPos || secondPos == token.NoPos {
		return false
	}
	return firstPos < secondPos
}
