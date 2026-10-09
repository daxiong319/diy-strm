package mediaupgrade

// T27 C-8 验收③的**端到端**用例：分类范围写在洗版规则行上
// （category_scope 列，迁移 0046），走真实的 SaveRule → LoadRuleSet → Scan 路径。
//
// 与 category_scope_test.go 里那些用例的区别：那边直接构造 ScanOptions.Category
// 传给 Scanner，测的是筛选逻辑本身；这里只填规则列、完全不碰 ScanOptions，
// 测的是**那条链路真的接上了** —— 而这一段正是最容易出现「假接线」的地方：
// 列加了、DTO 加了、界面加了，但 SaveRule 的更新白名单里漏了这个字段，
// 于是用户保存时静默丢失，扫描按不筛选跑，界面显示一切正常。
//
// 本仓已经因为同一种形态踩过坑（见 internal/api/library_share_admin.go
// 里规则入参字段名的注释），所以这里断言的是**往返**：存进去再读出来。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"litepan/internal/settings"
)

// seedScopedRule 存一条带分类范围的规则，返回它的 ID。
func seedScopedRule(t *testing.T, db *gorm.DB, libRoot string, categories []string) uint {
	t.Helper()
	svc := New(db, newFakeSettings(map[string]string{
		settings.KeyMOMediaUpgradeEnabled: "true",
	}))
	row := &Rule{
		Name:          "只洗国产剧",
		Source:        SourceLocal,
		LoserAction:   LoserActionKeep,
		LibraryRoot:   libRoot,
		CategoryScope: EncodeRuleCategoryScope(categories),
		Enabled:       true,
	}
	saved, err := svc.SaveRule(context.Background(), row)
	if err != nil {
		t.Fatalf("保存洗版规则失败：%v", err)
	}
	return saved.ID
}

// TestRuleCategoryScopeRoundTripsThroughTheDB 验收③（存得进去读得回来）。
//
// 这一条单独存在是因为 SaveRule 的更新路径用的是**显式列白名单**
// （service.go 的 Select("Name", ... ,"Enabled")）。GORM 的 Create 会把
// 结构体所有字段写进去，但 Updates 只会写白名单里的列 ——
// 白名单漏一列的症状不是报错，而是「保存成功、刷新后没了」。
//
// 所以这里刻意**先建一条空的、再改**：只走 Create 的那条路永远测不到白名单，
// 而白名单恰恰是这条链路上唯一会静默丢数据的地方
// （第一版就是只 Create，变异测试删掉白名单里那一项时它照样绿）。
func TestRuleCategoryScopeRoundTripsThroughTheDB(t *testing.T) {
	db := openTestDB(t)
	lib := t.TempDir()

	id := seedScopedRule(t, db, lib, nil)

	row, err := New(db, newFakeSettings(map[string]string{})).GetRule(context.Background(), id)
	if err != nil {
		t.Fatalf("读取规则失败：%v", err)
	}
	row.CategoryScope = EncodeRuleCategoryScope([]string{"国产剧", " 国产剧 ", "美剧", ""})
	if _, err := New(db, newFakeSettings(map[string]string{})).SaveRule(context.Background(), row); err != nil {
		t.Fatalf("更新规则失败：%v", err)
	}

	read, err := New(db, newFakeSettings(map[string]string{})).GetRule(context.Background(), id)
	if err != nil {
		t.Fatalf("读取规则失败：%v", err)
	}
	got := ParseRuleCategoryScope(read.CategoryScope)
	want := []string{"国产剧", "美剧"} // 去重、去空白、去空
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("读回的分类范围 = %v，期望 %v（去重归一后）", got, want)
	}
}

// TestUpdatingARuleClearsItsCategoryScope 钉住「取消适用分类」也是一次真更新。
//
// 不显式断言这一条的话，一个更省事的实现（只在列表非空时才写这一列）
// 会让用户在界面上清空筛选框再保存，数据库里还留着旧分类 ——
// 而界面上一切正常，直到下一次扫描按一个已经不存在的范围筛出 0 条。
func TestUpdatingARuleClearsItsCategoryScope(t *testing.T) {
	db := openTestDB(t)
	lib := t.TempDir()

	id := seedScopedRule(t, db, lib, []string{"国产剧"})

	svc := New(db, newFakeSettings(map[string]string{}))
	row, err := svc.GetRule(context.Background(), id)
	if err != nil {
		t.Fatalf("读取规则失败：%v", err)
	}
	row.CategoryScope = EncodeRuleCategoryScope(nil)
	if _, err := svc.SaveRule(context.Background(), row); err != nil {
		t.Fatalf("保存规则失败：%v", err)
	}

	after, err := svc.GetRule(context.Background(), id)
	if err != nil {
		t.Fatalf("重新读取规则失败：%v", err)
	}
	if after.CategoryScope != "" {
		t.Fatalf("清空后 category_scope = %q，期望空串（=不按分类筛选）", after.CategoryScope)
	}
}

// TestScanHonoursTheRulesOwnCategoryScope 验收③（端到端主用例）。
//
// 库里同时有国产剧和美剧；一条规则只声明"国产剧"，按那条规则扫描
// 应当只看到国产剧的文件。全程不构造 ScanOptions.Category ——
// 范围只能从规则列来，否则这条用例就退化成 category_scope_test.go
// 里已经有的那种直接注入。
func TestScanHonoursTheRulesOwnCategoryScope(t *testing.T) {
	db := openTestDB(t)
	lib := t.TempDir()
	writeLibraryVideo(t, lib, []string{"电视剧", "国产剧"}, "国产剧A.S01E01.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电视剧", "国产剧"}, "国产剧B.S01E01.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电视剧", "美剧"}, "美剧C.S01E01.1080p.mkv")

	id := seedScopedRule(t, db, lib, []string{"国产剧"})

	scan, err := New(db, newFakeSettings(map[string]string{})).Scan(context.Background(), id)
	if err != nil {
		t.Fatalf("按规则扫描失败：%v", err)
	}
	if scan.LibraryFiles != 2 {
		t.Fatalf("按规则里的分类范围扫描，库文件数 = %d，期望 2（%s）", scan.LibraryFiles, scan.Message)
	}
	if !strings.Contains(scan.Message, "分类范围外跳过 1 个") {
		t.Errorf("扫描消息 %q 应说明跳过了多少个，否则用户以为媒体库变小", scan.Message)
	}
}

// TestScanWithoutRuleCategoryScopeSeesEverything 存量兼容：
// 没填这一列的规则（升级前的全部存量规则）行为与 T27 之前完全一致。
func TestScanWithoutRuleCategoryScopeSeesEverything(t *testing.T) {
	db := openTestDB(t)
	lib := t.TempDir()
	writeLibraryVideo(t, lib, []string{"电视剧", "国产剧"}, "国产剧A.S01E01.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电视剧", "美剧"}, "美剧C.S01E01.1080p.mkv")

	id := seedScopedRule(t, db, lib, nil)

	scan, err := New(db, newFakeSettings(map[string]string{})).Scan(context.Background(), id)
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scan.LibraryFiles != 2 {
		t.Fatalf("规则没填分类范围时库文件数 = %d，期望 2（存量规则行为不变）", scan.LibraryFiles)
	}
	if strings.Contains(scan.Message, "分类范围") {
		t.Errorf("未配范围时不该提分类范围：%s", scan.Message)
	}
}

// TestBrokenCategoryScopeDoesNotBlockTheScan 钉住退化方向。
//
// 这一列是用户在界面上顺手填的筛选条件。它坏了（手改过库、某个版本
// 存了别的格式）唯一合理的后果是"这条规则不按分类筛选"，
// 而不是"整条规则扫不了" —— 后者会让一次手滑升级成洗版功能不可用。
func TestBrokenCategoryScopeDoesNotBlockTheScan(t *testing.T) {
	db := openTestDB(t)
	lib := t.TempDir()
	writeLibraryVideo(t, lib, []string{"电视剧", "国产剧"}, "国产剧A.S01E01.1080p.mkv")
	writeLibraryVideo(t, lib, []string{"电视剧", "美剧"}, "美剧C.S01E01.1080p.mkv")

	id := seedScopedRule(t, db, lib, nil)
	if err := db.Model(&Rule{}).Where("id = ?", id).
		Update("category_scope", "这不是 JSON").Error; err != nil {
		t.Fatalf("写入坏值失败：%v", err)
	}

	scan, err := New(db, newFakeSettings(map[string]string{})).Scan(context.Background(), id)
	if err != nil {
		t.Fatalf("分类范围列内容非法时扫描应仍然成功，实际报错：%v", err)
	}
	if scan.LibraryFiles != 2 {
		t.Fatalf("坏值退化后库文件数 = %d，期望 2（退化成不过筛）", scan.LibraryFiles)
	}
}

// TestGlobalRuleHasNoCategoryScope 钉住「不出现第二个权威源」。
//
// 全局设置那一套（KeyMOMediaUpgrade*）刻意没有分类范围这一项：
// 两个地方都能改同一件事时，用户改哪个生效取决于代码里谁先被读到。
func TestGlobalRuleHasNoCategoryScope(t *testing.T) {
	db := openTestDB(t)
	row, err := New(db, newFakeSettings(map[string]string{
		settings.KeyMOMediaUpgradeEnabled:     "true",
		settings.KeyMOMediaUpgradeLibraryRoot: filepath.Join(t.TempDir(), "lib"),
	})).GlobalRule()
	if err != nil {
		t.Fatalf("读取全局规则失败：%v", err)
	}
	if row.CategoryScope != "" {
		t.Fatalf("全局视图的 category_scope = %q，应恒为空", row.CategoryScope)
	}
}
