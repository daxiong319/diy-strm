package mediaupgrade

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"litepan/internal/settings"
)

// T27 C-8 消费者一的端到端用例：洗版规则按二级分类筛选。
//
// 这些用例依赖 classifyorganize.ListActiveCategories 的语义：
// 筛选范围来自**配置里的分类目录**，与磁盘上有没有那个目录无关。

// writeLibraryVideo 在媒体库根下按分类段建目录并写入一个视频文件。
//
// 路径形态刻意和 mediaorganize/planner 拼出来的分类目录一致：
// <库根>/<一级>/[<二级>/]<文件名>。
func writeLibraryVideo(t *testing.T, libRoot string, categorySegments []string, name string) string {
	t.Helper()
	dir := libRoot
	for _, seg := range categorySegments {
		dir = filepath.Join(dir, seg)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建分类目录失败：%v", err)
	}
	return writeVideo(t, filepath.Join(dir, name), 1<<30)
}

func scanLibraryCount(t *testing.T, libRoot string, scope CategoryScope) (int, string) {
	t.Helper()
	db := openTestDB(t)
	svc := newFakeSettings(map[string]string{
		settings.KeyMOMediaUpgradeEnabled:        "true",
		settings.KeyMOMediaUpgradeSource:         SourceLocal,
		settings.KeyMOMediaUpgradeLibraryRoot:    libRoot,
		settings.KeyMOMediaUpgradeCandidateRoots: "",
	})
	scan, err := NewScanner(db, svc).Scan(context.Background(), ScanOptions{Category: scope})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	return scan.LibraryFiles, scan.Message
}

// TestScanFiltersLibraryBySecondaryCategory 验收 ③（主用例）。
//
// 一个媒体库里同时有国产剧和美剧，只筛"国产"应当只看到国产剧那几个文件。
func TestScanFiltersLibraryBySecondaryCategory(t *testing.T) {
	lib := t.TempDir()
	// 两个不同作品，避免被 MaxRecordsPerSeries 之类的规则合并。
	writeLibraryVideo(t, lib, []string{"电视剧", "国产剧"}, "国产剧A.S01E01.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电视剧", "国产剧"}, "国产剧B.S01E01.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电视剧", "美剧"}, "美剧C.S01E01.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电影", "动作片"}, "动作片D.2020.1080p.mkv")

	got, msg := scanLibraryCount(t, lib, CategoryScope{SecondaryNames: []string{"国产剧"}})
	if got != 2 {
		t.Fatalf("按二级分类筛选后库文件数 = %d，期望 2（%s）", got, msg)
	}
	if !strings.Contains(msg, "分类范围外跳过 2 个") {
		t.Errorf("扫描消息 %q 应说明被跳过的数量，否则用户会以为媒体库变小。", msg)
	}
}

// TestScanFiltersLibraryByPrimaryCategory 验收 ③（按一级）。
func TestScanFiltersLibraryByPrimaryCategory(t *testing.T) {
	lib := t.TempDir()
	writeLibraryVideo(t, lib, []string{"电影", "动作片"}, "动作片D.2020.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电影", "科幻片"}, "科幻片E.2019.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电视剧", "国产剧"}, "国产剧A.S01E01.1080p.mkv")

	got, _ := scanLibraryCount(t, lib, CategoryScope{PrimaryNames: []string{"电影"}})
	if got != 2 {
		t.Fatalf("按一级分类筛选后库文件数 = %d，期望 2", got)
	}
}

// TestScanCategoryScopeCombinesPrimaryAndSecondary 验收 ③（两级组合）。
//
// 同时给一级和二级时是"与"：一级限定范围，二级在该范围内再筛。
func TestScanCategoryScopeCombinesPrimaryAndSecondary(t *testing.T) {
	lib := t.TempDir()
	writeLibraryVideo(t, lib, []string{"电视剧", "国产剧"}, "国产剧A.S01E01.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电视剧", "美剧"}, "美剧C.S01E01.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电影", "国产"}, "某电影F.2020.1080p.mkv")

	got, _ := scanLibraryCount(t, lib, CategoryScope{
		PrimaryNames:   []string{"电视剧"},
		SecondaryNames: []string{"国产剧"},
	})
	if got != 1 {
		t.Fatalf("两级组合筛选后库文件数 = %d，期望 1", got)
	}
}

// TestScanWithoutCategoryScopeKeepsEverything 钉住零值语义：没配范围就不筛。
//
// 这条是向后兼容的保证：不传分类范围的用户行为必须和 T27 之前完全一致。
func TestScanWithoutCategoryScopeKeepsEverything(t *testing.T) {
	lib := t.TempDir()
	writeLibraryVideo(t, lib, []string{"电影", "动作片"}, "动作片D.2020.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电视剧", "国产剧"}, "国产剧A.S01E01.1080p.mkv")
	// 未整理的文件（第一段就是片名，没有分类段）。
	writeLibraryVideo(t, lib, nil, "散落E.2021.1080p.mkv")

	got, msg := scanLibraryCount(t, lib, CategoryScope{})
	if got != 3 {
		t.Fatalf("未设分类范围时库文件数 = %d，期望 3（%s）", got, msg)
	}
	if strings.Contains(msg, "分类范围") {
		t.Errorf("未设分类范围时不该出现分类范围相关消息：%q", msg)
	}
}

// TestScanCategoryScopeExcludesUnorganizedFiles 钉住一个必须显式说明的取舍。
//
// 按分类筛洗版时，媒体库里没经过分类整理的文件会被排除掉：
// 它们连属于哪一类都不知道，猜一个就是把别的类型的片子拉进来比画质。
// 这条用例把这个行为钉死，免得将来有人"顺手修复"成全盘保留，
// 那会让"只洗国产剧"悄悄扫到整个库。
func TestScanCategoryScopeExcludesUnorganizedFiles(t *testing.T) {
	lib := t.TempDir()
	writeLibraryVideo(t, lib, []string{"电视剧", "国产剧"}, "国产剧A.S01E01.1080p.mkv")
	writeLibraryVideo(t, lib, nil, "散落E.2021.1080p.mkv")

	got, msg := scanLibraryCount(t, lib, CategoryScope{PrimaryNames: []string{"电视剧"}})
	if got != 1 {
		t.Fatalf("库文件数 = %d，期望 1 —— 未整理的文件不该被分类筛选收进来（%s）", got, msg)
	}
	if !strings.Contains(msg, "跳过 1 个") {
		t.Errorf("被排除的未整理文件应在消息里可见：%q", msg)
	}
}

// TestScanCategoryScopeDoesNotFilterCandidates 钉住候选不被筛。
//
// 候选目录里的文件可能还没被整理进任何分类目录。按分类筛候选会让
// 「库里某部片子找不到候选里更好的版本」—— 那是洗版最核心的用途。
func TestScanCategoryScopeDoesNotFilterCandidates(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")

	// 库里有一部国产剧（低码率）。
	writeLibraryVideo(t, lib, []string{"电视剧", "国产剧"}, "国产剧A.S01E01.1080p.x264.mkv")
	// 候选目录里躺着它的高码率版本，而且**没有分类目录**（还没整理进去）。
	newPath := writeVideo(t, filepath.Join(cand, "国产剧A.S01E01.2160p.x265.mkv"), 9<<30)

	db := openTestDB(t)
	svc := newFakeSettings(map[string]string{
		settings.KeyMOMediaUpgradeEnabled:             "true",
		settings.KeyMOMediaUpgradeSource:              SourceLocal,
		settings.KeyMOMediaUpgradeLibraryRoot:         lib,
		settings.KeyMOMediaUpgradeCandidateRoots:      cand,
		settings.KeyMOMediaUpgradeLoserAction:         LoserActionKeep,
		settings.KeyMOMediaUpgradeMaxRecordsPerSeries: "1",
	})
	scan, err := NewScanner(db, svc).Scan(context.Background(), ScanOptions{
		Category: CategoryScope{SecondaryNames: []string{"国产剧"}},
	})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scan.LibraryFiles != 1 {
		t.Fatalf("库文件数 = %d，期望 1", scan.LibraryFiles)
	}
	if scan.CandidateFiles != 2 {
		t.Fatalf("候选文件数 = %d，期望 2（库里 1 + 候选目录 1，候选不该被分类筛选裁掉）", scan.CandidateFiles)
	}
	if !fileExists(newPath) {
		t.Fatal("扫描不该动文件")
	}
	if len(recordsOf(t, db, scan.ID)) != 1 {
		t.Fatalf("应产出 1 条洗版记录（候选里确实有更好的版本）")
	}
}

// TestCategoryFilterKeepsSegmentPositionSemantics 钉住"第一段就是一级分类"。
//
// 路径里的分类段是**位置确定**的：第一段一定是一级。
// 这里的用例防的是"按目录名 anywhere 匹配"那种实现 ——
// 它会让 /剧集/国产剧 里的文件被一个名叫"电影/国产"的分类认领。
func TestCategoryFilterKeepsSegmentPositionSemantics(t *testing.T) {
	files := []libFile{
		{Path: "/lib/电影/国产/某片.mkv", Name: "某片.mkv"},
		{Path: "/lib/电视剧/国产剧/某剧.mkv", Name: "某剧.mkv"},
		{Path: "/lib/散落/某片.mkv", Name: "某片.mkv"},
	}
	kept, dropped := categoryFilter(files, "/lib", CategoryScope{SecondaryNames: []string{"国产"}})
	if len(kept) != 1 || kept[0].Name != "某片.mkv" {
		t.Fatalf("按二级筛选应只留下 电影/国产 下的文件，实得 %v", kept)
	}
	if len(dropped) != 2 {
		t.Fatalf("被跳过的文件数 = %d，期望 2", len(dropped))
	}
}

// TestLibraryCategorySegmentsDropsFileName 钉住段提取：末段是文件名不是分类段。
func TestLibraryCategorySegmentsDropsFileName(t *testing.T) {
	got := libraryCategorySegments("/lib/电影/国产/某片.mkv", "/lib")
	if strings.Join(got, "/") != "电影/国产" {
		t.Fatalf("段提取 = %v，期望 相对库根的两段", got)
	}
	// 根目录下的散落文件：相对路径只有文件名 ⇒ 没有分类段。
	if len(libraryCategorySegments("/lib/某片.mkv", "/lib")) != 0 {
		t.Fatal("根目录下的文件不应被判为有分类段")
	}
	if libraryCategorySegments("", "/lib") != nil {
		t.Fatal("空路径应返回 nil")
	}
	// 文件不在媒体库根下（Emby 索引里可能混着别的库的文件）⇒ 没有分类段。
	if libraryCategorySegments("/other/电影/某片.mkv", "/lib") != nil {
		t.Fatal("不在库根下的文件不应被判为有分类段")
	}
	// 备份目录陷阱：/media/电影备份 以字符串前缀看在 /media/电影 里面。
	if libraryCategorySegments("/media/电影备份/某片.mkv", "/media/电影") != nil {
		t.Fatal("兄弟目录不该被前缀比较判成在库根内")
	}
	// 反斜杠按段切是编译目标相关的：filepath.ToSlash 只替换 os.PathSeparator，
	// Windows 二进制里它是 \ ，Linux 里是 /。所以这条只在 Windows 上断言 ——
	// 在 Linux 上拿反斜杠路径去测，得到的是一个与生产无关的假失败。
	if runtime.GOOS == "windows" {
		if got := strings.Join(libraryCategorySegments(`D:\lib\电影\某片.mkv`, `D:\lib`), "/"); got != "电影" {
			t.Fatalf("Windows 路径段提取 = %q，期望 %q", got, "电影")
		}
	}
}
