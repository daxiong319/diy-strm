package inspection

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubTree 是巡检测试用的只读树源。
//
// 它刻意只实现 List 一个方法：TreeSource 上如果哪天被人加回 Roots，
// 这个 stub 会立刻编译失败 —— 而那正是我们要防的回归（真实清单实现
// NetdiskTreeSource 根本答不出范围）。
type stubTree struct {
	roots []Root
	nodes map[string][]TreeNode
	err   error
}

func (s *stubTree) List(_ context.Context, root Root) ([]TreeNode, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.nodes[root.Path], nil
}

func stubRoots(t *testing.T, source *stubTree) func(context.Context) ([]Root, error) {
	t.Helper()
	return func(context.Context) ([]Root, error) { return source.roots, nil }
}

func node(id, parent, name, path string, isDir bool, size int64) TreeNode {
	return TreeNode{ID: id, ParentID: parent, Name: name, Path: path, IsDir: isDir, Size: size, Sha1: "sha-" + id}
}

func TestEmptyDirCheckerReportsVacuousDirs(t *testing.T) {
	// 目录节点由适配层从「文件的父目录」反推而来（见 internal/app 的
	// inspectionFileLister.Tree），这里给的是已经反推好的树：
	// /media/电影 是真空壳，/media/剧/Season 01 下面有两个文件。
	source := &stubTree{roots: []Root{{ID: "r1", Path: "/media"}}, nodes: map[string][]TreeNode{
		"/media": {
			node("dir1", "", "电影", "/media/电影", true, 0),
			node("dir2", "dir1", "剧", "/media/电影/剧", true, 0),
			node("dir3", "dir2", "Season 01", "/media/电影/剧/Season 01", true, 0),
			node("f1", "dir3", "e01.mkv", "/media/电影/剧/Season 01/e01.mkv", false, 1<<30),
			node("f3", "dir3", "e03.mkv", "/media/电影/剧/Season 01/e03.mkv", false, 1<<30),
			node("dir4", "", "空壳", "/media/空壳", true, 0),
		},
	}}
	c := &EmptyDirChecker{KeyName: KeyEmptyDir, LabelText: "目录树清理", Roots: stubRoots(t, source), Source: source}
	findings, err := c.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan 返回错误：%v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("报了 %d 条，期望 1 条（只有 /media/空壳，Season 01 里有文件不能报）", len(findings))
	}
	if !strings.Contains(findings[0].Detail["path"].(string), "空壳") {
		t.Fatalf("报的是 %v，期望 /media/空壳", findings[0].Detail)
	}
	f := findings[0]
	if f.Repair.Kind != RepairDeleteDir {
		t.Errorf("修复动作是 %q，应该是 %q", f.Repair.Kind, RepairDeleteDir)
	}
	if !IsDestructive(f.Repair.Kind) {
		t.Errorf("%s 被标成可逆动作 —— 前端不会对它弹二次确认", f.Target)
	}
}

func TestOrphanDirCheckerSkipsDirsThatStillHaveMedia(t *testing.T) {
	source := &stubTree{roots: []Root{{ID: "r1", Path: "/media"}}, nodes: map[string][]TreeNode{
		"/media": {
			node("dir1", "", "活着的", "/media/活着的", true, 0),
			node("f1", "dir1", "movie.mkv", "/media/活着的/movie.mkv", false, 1<<30),
			// 只有 nfo 和字幕，没有媒体正文：这是孤儿不是空目录。
			node("f2", "dir1", "movie.nfo", "/media/活着的/movie.nfo", false, 1024),
			node("dir2", "", "残骸", "/media/残骸", true, 0),
			node("f3", "dir2", "cover.jpg", "/media/残骸/cover.jpg", false, 1024),
		},
	}}
	c := &OrphanDirChecker{KeyName: KeyOrphanDir, LabelText: "孤儿目录清理", Roots: stubRoots(t, source), Source: source}
	findings, err := c.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan 返回错误：%v", err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].Detail["path"].(string), "残骸") {
		t.Fatalf("只该报 /media/残骸，实际 %+v", findings)
	}
}

func TestEmptyDirCheckerSkipsRootErrorWithoutFailing(t *testing.T) {
	source := &stubTree{roots: []Root{{ID: "r1", Path: "/media"}}, err: errors.New("网盘不可用")}
	c := &EmptyDirChecker{KeyName: KeyEmptyDir, LabelText: "目录树清理", Roots: stubRoots(t, source), Source: source}
	if _, err := c.Scan(context.Background()); err == nil {
		t.Fatal("清单读失败时 Scan 必须返回错误，否则六个检查器会一致读成「一切正常」")
	}
}

func TestIndexAbnormalCheckerFlagsIndexRowsMissingFromNetdisk(t *testing.T) {
	source := &stubTree{roots: []Root{{ID: "r1", Path: "/media"}}, nodes: map[string][]TreeNode{
		"/media": {node("f1", "d1", "kept.mkv", "/media/kept.mkv", false, 1<<30)},
	}}
	c := &IndexAbnormalChecker{
		KeyName: KeyIndexAbnormal, LabelText: "115 索引异常文件",
		Roots: stubRoots(t, source), Source: source,
		IndexRows: func(context.Context) ([]IndexRow, error) {
			return []IndexRow{
				{ID: 1, AccountID: 0, RootID: "r1", RelativePath: "kept.mkv", FileName: "kept.mkv"},
				{ID: 2, AccountID: 0, RootID: "r1", RelativePath: "gone.mkv", FileName: "gone.mkv"},
			}, nil
		},
	}
	findings, err := c.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan 返回错误：%v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("报了 %d 条，期望 1 条（只报 gone.mkv，kept.mkv 是好的）", len(findings))
	}
	f := findings[0]
	if f.Repair.Kind != RepairDropIndexRow || f.Repair.Params["row_id"] != "2" {
		t.Fatalf("修复动作 = %+v，期望 drop_index_row 且 row_id=2", f.Repair)
	}
	if IsDestructive(f.Repair.Kind) {
		t.Error("删除一条索引关联行不该被标成破坏性动作 —— 它可逆")
	}
}

func TestDuplicateCheckerOnlyReports(t *testing.T) {
	source := &stubTree{roots: []Root{{ID: "r1", Path: "/media"}}, nodes: map[string][]TreeNode{
		"/media": {
			node("f1", "d1", "a.mkv", "/media/a.mkv", false, 700<<20),
			node("f2", "d2", "b.mkv", "/media/b.mkv", false, 700<<20),
		},
	}}
	c := &DuplicateChecker{KeyName: KeyDuplicate, LabelText: "重复排查", Roots: stubRoots(t, source), Source: source}
	findings, err := c.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan 返回错误：%v", err)
	}
	if len(findings) == 0 {
		t.Fatal("同体积的两个媒体文件应该被报成重复候选")
	}
	for _, f := range findings {
		if f.Repair.Kind != RepairNone {
			t.Errorf("重复排查给出了修复动作 %q —— 同体积不等于同内容，系统不该替用户删", f.Repair.Kind)
		}
	}
}

func TestTMDBCheckerOnlyReportsInvalidIDOnNotFound(t *testing.T) {
	items := func(context.Context) ([]TMDBCheckItem, error) {
		return []TMDBCheckItem{
			{Kind: "movie", TMDBID: 1, LocalName: "A (2019)"},
			{Kind: "movie", TMDBID: 2, LocalName: "B (2020)"},
			{Kind: "movie", TMDBID: 3, LocalName: "C (2021)"},
		}, nil
	}
	client := stubTMDB{fail: map[int64]error{
		1: ErrTMDBNotFound,
		2: errors.New("tmdb: http status 503"),
	}}
	c := &TMDBChecker{KeyName: KeyTMDB, LabelText: "TMDB 检查与修正", Items: items, Client: client}
	findings, err := c.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan 返回错误：%v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("报了 %d 条，期望 1 条：只有 ErrTMDBNotFound 才是编号失效，"+
			"503 是网络问题，报成「编号失效」会误导用户去改编号（实际 %+v）", len(findings), findings)
	}
}

// stubTMDB 按编号给不同的结果：1 编号失效、2 服务错误、3 标题一致（不该报）。
type stubTMDB struct {
	fail map[int64]error
}

func (s stubTMDB) Lookup(_ context.Context, id int64, mediaType string) (TMDBMeta, error) {
	if err := s.fail[id]; err != nil {
		return TMDBMeta{}, err
	}
	return TMDBMeta{Title: "C (2021)", MediaType: mediaType}, nil
}

func TestMissingCheckerSkipsSeasonWhenExpectIsEmpty(t *testing.T) {
	source := &stubTree{roots: []Root{{ID: "r1", Path: "/media"}}, nodes: map[string][]TreeNode{
		"/media": {
			node("f1", "d1", "Show.S01E01.1080p.mkv", "/media/剧/Season 01/Show.S01E01.mkv", false, 1<<30),
		},
	}}
	c := &MissingChecker{
		KeyName: KeyMissing, LabelText: "查漏补缺",
		Roots: stubRoots(t, source), Source: source,
		// 搜不到作品时返回空集合 —— 巡检宁可漏报也不去猜编号。
		Expect: func(context.Context, string, int) ([]int, error) { return nil, nil },
	}
	findings, err := c.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan 返回错误：%v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("查不到集数时不该报缺集，实际报了 %+v", findings)
	}
}

func TestMissingCheckerReportsGaps(t *testing.T) {
	source := &stubTree{roots: []Root{{ID: "r1", Path: "/media"}}, nodes: map[string][]TreeNode{
		"/media": {
			node("f1", "d1", "Show.S01E01.mkv", "/media/剧/Season 01/Show.S01E01.mkv", false, 1<<30),
			node("f3", "d1", "Show.S01E03.mkv", "/media/剧/Season 01/Show.S01E03.mkv", false, 1<<30),
		},
	}}
	c := &MissingChecker{
		KeyName: KeyMissing, LabelText: "查漏补缺",
		Roots: stubRoots(t, source), Source: source,
		Expect: func(_ context.Context, _ string, _ int) ([]int, error) {
			return []int{1, 2, 3}, nil
		},
	}
	findings, err := c.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan 返回错误：%v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("报了 %d 条，期望 1 条（第 2 集缺失）", len(findings))
	}
	if findings[0].Kind != "missing_episodes" || findings[0].Repair.Kind != RepairNone {
		t.Errorf("缺集条目 = %+v，期望 kind=missing_episodes 且不带修复动作", findings[0])
	}
}

func TestParseMediaNameRejectsTrailingJunk(t *testing.T) {
	cases := []struct {
		name     string
		wantOK   bool
		wantEp   int
	}{
		{name: "Show.S01E02.mkv", wantOK: true, wantEp: 2},
		{name: "Show.S01.E03.mkv", wantOK: true, wantEp: 3},
		{name: "Show.1080p.mkv"},
		{name: "Show.S01E02.1080p.WEB-DL.mkv"},
	}
	for _, tc := range cases {
		_, season, episode, ok := parseMediaName(tc.name)
		if ok != tc.wantOK {
			t.Errorf("parseMediaName(%q) ok = %v，期望 %v", tc.name, ok, tc.wantOK)
			continue
		}
		if ok && (episode != tc.wantEp || season != 1) {
			t.Errorf("parseMediaName(%q) = S%02dE%02d，期望 S01E%02d", tc.name, season, episode, tc.wantEp)
		}
	}
}
