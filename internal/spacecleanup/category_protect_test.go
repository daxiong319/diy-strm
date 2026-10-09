package spacecleanup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 分类目录保护的用例。
//
// 这一组用例里最该防的回归是「保护范围悄悄变大」：把 /strm/下载/电影
// 因为含「电影」二字保护起来，看上去更安全，实际是让清理器对用户没配过的
// 目录失去判断力。所以每个用例都同时写一条「同名的、但不该保护」的对照。

func categoryGuardFor(root string, paths ...string) func() *CategoryGuard {
	cats := make([]CategoryPath, 0, len(paths))
	for i, path := range paths {
		cats = append(cats, CategoryPath{Path: path, Level: i + 1})
	}
	guard := NewCategoryGuard([]string{root}, cats)
	return func() *CategoryGuard { return guard }
}

func TestCategoryGuardProtectsClassifiedTrees(t *testing.T) {
	root := t.TempDir()
	guard := NewCategoryGuard([]string{root}, []CategoryPath{
		{Path: "电影", Level: 1},
		{Path: "电影/国产", Level: 2},
		{Path: "电视剧", Level: 1},
	})
	if guard == nil {
		t.Fatal("有分类时不应返回 nil")
	}
	protected := []string{
		filepath.Join(root, "电影"),
		filepath.Join(root, "电影", "国产"),
		filepath.Join(root, "电影", "国产", "2019"),
		filepath.Join(root, "电影", "欧美"),
		filepath.Join(root, "电影", "孤独摇滚"),
		filepath.Join(root, "电视剧", "国产剧", "庆余年"),
		// 二级分类的祖先段也受保护：配了「电影/国产」时 /strm/电影 整棵树
		// 都得免检，否则那条二级分类在磁盘上根本走不到。
		filepath.Join(root, "电影", "国产", "一部电影", "movie.mkv"),
	}
	for _, path := range protected {
		if !guard.Protected(path) {
			t.Fatalf("分类目录应受保护：%s", path)
		}
	}
	notProtected := []string{
		root,
		filepath.Join(root, "下载"),
		filepath.Join(root, "下载", "电影"),
		filepath.Join(root, "电影备份"),
		filepath.Join(root, "动漫"),
		filepath.Join(root, "电影2"),
	}
	for _, path := range notProtected {
		if guard.Protected(path) {
			t.Fatalf("非分类目录不应受保护：%s", path)
		}
	}
}

func TestCategoryGuardRequiresRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	guard := NewCategoryGuard([]string{root}, []CategoryPath{{Path: "电影", Level: 1}})
	if guard.Protected(filepath.Join(outside, "电影")) {
		t.Fatal("分类根之外的同名目录不应受保护")
	}
	if guard.Protected("") || guard.Protected(".") {
		t.Fatal("空路径与当前目录不该被判成分类目录")
	}
}

func TestNewCategoryGuardReturnsNilWithoutCategoriesOrRoots(t *testing.T) {
	root := t.TempDir()
	if NewCategoryGuard([]string{root}, nil) != nil {
		t.Fatal("没有分类时返回 nil，调用方少写一处判空")
	}
	if NewCategoryGuard(nil, []CategoryPath{{Path: "电影"}}) != nil {
		t.Fatal("没有分类根时返回 nil")
	}
	if NewCategoryGuard([]string{""}, []CategoryPath{{Path: "电影"}}) != nil {
		t.Fatal("空分类根应当被丢弃而不是当成「根是空串」")
	}
	var nilGuard *CategoryGuard
	if nilGuard.Protected("/strm/电影") {
		t.Fatal("nil guard 的语义是「什么都不保护」")
	}
	if nilGuard.Len() != 0 || nilGuard.Names() != nil {
		t.Fatal("nil guard 的摘要字段也要能安全调用")
	}
}

func TestScanSkipsCategoryDirectories(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	strmDir := filepath.Join(root, "strm")
	movieDir := filepath.Join(strmDir, "电影", "国产", "孤独摇滚")
	orphanDir := filepath.Join(strmDir, "旧任务")
	// scanStrm 遇到「不活跃也不在活跃前缀下」的目录就整棵当作一条清理项，
	// 不再往下递归，所以这里被报出来的是 /strm/下载 而不是 /strm/下载/电影。
	downloadDir := filepath.Join(strmDir, "下载")
	sameNameUnderDownload := filepath.Join(downloadDir, "电影")
	writeTestFile(t, filepath.Join(movieDir, "a.mkv"), "movie")
	writeTestFile(t, filepath.Join(orphanDir, "b.mkv"), "orphan")
	writeTestFile(t, filepath.Join(sameNameUnderDownload, "c.mkv"), "download")

	service, err := New(Options{
		DataDir:            dataDir,
		StrmDir:            strmDir,
		StrmTasks:          &strmTaskRepoStub{},
		CategoryProtection: categoryGuardFor(strmDir, "电影", "电影/国产", "电视剧"),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items := reportItems(report)
	for _, path := range []string{movieDir, filepath.Join(strmDir, "电影"), filepath.Join(strmDir, "电影", "国产")} {
		if item, ok := findItemByPath(items, path); ok {
			t.Fatalf("分类目录不应出现在清理报告里：%s %+v", path, item)
		}
	}
	if item, ok := findItemByPath(items, orphanDir); !ok {
		t.Fatalf("非分类目录仍应被报出来：%+v", item)
	}
	// 同名但不在分类根下第一段的目录必须照样报出来。
	// 少了这条断言，一个「路径里含任一分类名就跳过」的实现也能全绿。
	if item, ok := findItemByPath(items, downloadDir); !ok {
		t.Fatalf("下载目录下的同名子目录不是分类目录，应照常清理：%+v", item)
	}
}

func TestCleanupRefusesCategoryDirectoryEvenWhenListedInReport(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	strmDir := filepath.Join(root, "strm")
	movieDir := filepath.Join(strmDir, "电影")
	writeTestFile(t, filepath.Join(movieDir, "旧任务", "a.mkv"), "movie")

	// 报告在「还没有分类保护」时生成，那一刻这个目录确实是个孤儿目录。
	// 保护名单用一个可改的闭包持有，因为现实中前后是同一个进程里的同一个
	// 清理器（Cleanup 认的是 ScanID，换实例会被判「扫描结果不存在」）。
	var guard *CategoryGuard
	service, err := New(Options{
		DataDir:            dataDir,
		StrmDir:            strmDir,
		StrmTasks:          &strmTaskRepoStub{},
		CategoryProtection: func() *CategoryGuard { return guard },
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	item, ok := findItemByPath(reportItems(report), movieDir)
	if !ok {
		t.Fatal("没有分类保护时该目录应当被报为孤儿目录")
	}

	// 用户在报告摊开之后才把「电影」配成一级分类（或开关了分类）。
	// 点确认时清理器必须重新判断，否则这条旧报告会把刚配好的分类目录删掉。
	guard = NewCategoryGuard([]string{strmDir}, []CategoryPath{{Path: "电影", Level: 1}})
	result, err := service.Cleanup(context.Background(), CleanupRequest{
		ScanID:  report.ScanID,
		ItemIDs: []string{item.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(movieDir); statErr != nil {
		t.Fatalf("分类目录不得被删除：%v", statErr)
	}
	if len(result.Results) != 1 {
		t.Fatalf("应返回一条结果：%+v", result.Results)
	}
	if result.Results[0].Status != "skipped" {
		t.Fatalf("拦截分类目录应是 skipped 而不是 failed（failed 会让用户以为出错并重试）：%+v", result.Results[0])
	}
}

func TestScanWithoutCategoryProtectionKeepsReportingOrphans(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	strmDir := filepath.Join(root, "strm")
	movieDir := filepath.Join(strmDir, "电影")
	writeTestFile(t, filepath.Join(movieDir, "国产", "a.mkv"), "movie")

	for name, opts := range map[string]Options{
		"没注入保护函数": {DataDir: dataDir, StrmDir: strmDir, StrmTasks: &strmTaskRepoStub{}},
		"保护函数返回 nil": {
			DataDir:            dataDir,
			StrmDir:            strmDir,
			StrmTasks:          &strmTaskRepoStub{},
			CategoryProtection: func() *CategoryGuard { return nil },
		},
	} {
		service, err := New(opts)
		if err != nil {
			t.Fatal(err)
		}
		report, err := service.Scan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := findItemByPath(reportItems(report), movieDir); !ok {
			t.Fatalf("%s：没有分类保护时孤儿目录必须照常报出，否则清理器变成瞎子", name)
		}
	}
}

func TestCategoryGuardToleratesMessyConfiguredPaths(t *testing.T) {
	root := t.TempDir()
	guard := NewCategoryGuard([]string{root}, []CategoryPath{
		{Path: " 电影 ", Level: 1},
		{Path: "电视剧//国产", Level: 2},
		{Path: "", Level: 1},
	})
	if guard == nil {
		t.Fatal("脏路径不该让整份名单作废")
	}
	if !guard.Protected(filepath.Join(root, "电影")) {
		t.Fatal("首尾空格应被容忍")
	}
	if !guard.Protected(filepath.Join(root, "电视剧", "国产")) {
		t.Fatal("重复分隔符应被容忍")
	}
}
