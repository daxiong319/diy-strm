package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
