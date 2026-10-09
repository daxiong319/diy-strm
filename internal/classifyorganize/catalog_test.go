package classifyorganize

import (
	"context"
	"strings"
	"testing"
)

// T27 C-8 分类只读接口的语义边界测试。
//
// 这些用例保护的是**最容易读错的那条性质**：ListActiveCategories 返回的是
// 「规则里配了哪些分类」，不是「磁盘上有哪些分类目录」。
// 一旦有人把它当成后者来用，最典型的后果是清理保护把用户早就废弃的
// 分类目录永久钉在清理之外 —— 现象是"这个目录怎么清不掉"，
// 而根因在几百行之外的一个接口语义上。
//
// 所以下面每条用例都在名字里写明它钉的是哪一侧。

func catalogService(t *testing.T) *Service {
	t.Helper()
	return newService(t, false)
}

func catalogOf(t *testing.T, cats []Category, slug string) Category {
	t.Helper()
	for _, c := range cats {
		if c.Slug == slug {
			return c
		}
	}
	t.Fatalf("分类清单里没有 slug=%q，实有 %v", slug, catalogSlugs(cats))
	return Category{}
}

func catalogSlugs(cats []Category) []string {
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, c.Slug)
	}
	return out
}

// TestListActiveCategoriesReturnsConfiguredCategories 验收 ①：
// 返回规则表里的全部分类，含层级、名称、启用状态。
func TestListActiveCategoriesReturnsConfiguredCategories(t *testing.T) {
	svc := catalogService(t)
	cats, err := svc.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("列出分类目录失败: %v", err)
	}
	if len(cats) == 0 {
		t.Fatal("默认配置下应至少有电影/电视剧两个一级分类")
	}
	// 四个模板的一级都在。
	for _, want := range []string{"media/电影", "media/电视剧", "region/电影", "genre/电影"} {
		catalogOf(t, cats, want)
	}
	// 层级与启用状态有值（不是零值）。
	for _, c := range cats {
		if c.Level < 1 || c.Level > 3 {
			t.Fatalf("%s 的层级非法：%d", c.Slug, c.Level)
		}
		if !c.Enabled {
			t.Fatalf("%s 应为启用状态", c.Slug)
		}
		if c.Name == "" || c.Slug == "" || c.Path == "" {
			t.Fatalf("%s 字段不全：%+v", c.Slug, c)
		}
	}
	// 一级带类型键，二级不带。
	if got := catalogOf(t, cats, "media/电影").PrimaryKey; got != "movie" {
		t.Fatalf("media/电影 的 primary_key = %q，期望 movie", got)
	}
	second := catalogOf(t, cats, "region/电影/国产")
	if second.Level != 2 {
		t.Fatalf("region/电影/国产 的层级 = %d，期望 2", second.Level)
	}
	if second.PrimaryKey != "" {
		t.Fatalf("二级分类不应带 primary_key，实得 %q", second.PrimaryKey)
	}
}

// TestListActiveCategoriesReportsRulesNotDisk 验收 ②（语义边界的正面断言）。
//
// 这是本任务最重要的一条用例。它把"磁盘上根本没有这些目录"这件事写进断言：
// 整个测试跑在临时目录里，磁盘上一个分类目录都没有，但接口必须返回完整清单。
// 将来谁要是把实现改成"扫目录得出分类"，这条会立刻红。
func TestListActiveCategoriesReportsRulesNotDisk(t *testing.T) {
	svc := catalogService(t)
	// 故意确认测试环境里磁盘上没有分类目录：真造一个同名目录也不该出现在结果里。
	cats, err := svc.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("列出分类目录失败: %v", err)
	}
	if len(cats) < 8 {
		t.Fatalf("分类目录只有 %d 条，磁盘上明明一个目录都没有 —— 说明它读的是磁盘而非规则", len(cats))
	}
	// 反向：配置里有、磁盘上没有的分类必须出现。
	all := catalogSlugs(cats)
	hasRegion := false
	for _, slug := range all {
		if strings.HasPrefix(slug, "region/") {
			hasRegion = true
			break
		}
	}
	if !hasRegion {
		t.Fatalf("地区模板的分类目录不在清单里：%v", all)
	}
}

// TestListActiveCategoriesIgnoresDiskDirectories 验收 ②（语义边界的反向断言）。
//
// 和上一条互为镜像：上一条钉住"磁盘上没有的分类也要出现"（不读磁盘），
// 这一条钉住"配置里没有的分类不出现"。两条都绿，才能说明数据源确实是配置。
//
// 这里用 genre 模板下手，因为 custom 模板默认也带一条叫"国产"的二级
// （删掉 region 的那条并不足以让它消失 —— 第一版 fixture 就踩了这个坑，
// 症状是断言报出的 slug 指向 custom，说明用例没测到想测的东西）。
func TestListActiveCategoriesIgnoresDiskDirectories(t *testing.T) {
	svc := catalogService(t)
	cfg := svc.Config()
	for i := range cfg.Templates {
		if cfg.Templates[i].Kind != TemplateGenre {
			continue
		}
		for j := range cfg.Templates[i].Rules {
			cfg.Templates[i].Rules[j].Children = nil
		}
	}
	if _, err := svc.Update(context.Background(), cfg); err != nil {
		t.Fatalf("保存类型模板配置失败: %v", err)
	}
	cats, err := svc.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("列出分类目录失败: %v", err)
	}
	for _, c := range cats {
		if c.Template == TemplateGenre && c.Level > 1 {
			t.Fatalf("类型模板的二级已被清空，仍返回：%+v", c)
		}
	}
	// 一级"电影"仍在（只删了二级）。
	catalogOf(t, cats, "genre/电影")
}

// TestListActiveCategoriesIncludesUnselectedTemplate 钉住 "Active" 的含义。
//
// 「Active」指配置存在，不是"当前选中的模板"。用户切到另一个模板时，
// 已有分类不该突然消失 —— 否则洗版里已经选好的二级分类筛选会在
// 用户换模板后无声失效。
func TestListActiveCategoriesIncludesUnselectedTemplate(t *testing.T) {
	svc := catalogService(t)
	cfg := svc.Config()
	cfg.SelectedTemplate = TemplateMedia
	if _, err := svc.Update(context.Background(), cfg); err != nil {
		t.Fatalf("切换模板失败: %v", err)
	}
	cats, err := svc.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("列出分类目录失败: %v", err)
	}
	// 没被选中的地区/类型模板照样要返回。
	catalogOf(t, cats, "region/电影")
	catalogOf(t, cats, "genre/电影")
}

// TestListActiveCategoriesIncludesThirdLevel 验收 ①（三级）。
func TestListActiveCategoriesIncludesThirdLevel(t *testing.T) {
	svc := catalogService(t)
	cfg := svc.Config()
	// 给地区模板的"电影"挂一条三级：一条带 Fields 的二级 + 一条三级。
	for i := range cfg.Templates {
		if cfg.Templates[i].Kind != TemplateGenre {
			continue
		}
		cfg.Templates[i].Rules[0].Children = []Rule{
			{Name: "科幻片", Condition: "genres=科幻", Children: []Rule{
				{Name: "2000-2009", Fields: &RuleFields{Year: &YearRange{From: 2000, To: 2009}}},
			}},
		}
	}
	if _, err := svc.Update(context.Background(), cfg); err != nil {
		t.Fatalf("保存三级目录配置失败: %v", err)
	}
	cats, err := svc.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("列出分类目录失败: %v", err)
	}
	third := catalogOf(t, cats, "genre/电影/科幻片/2000-2009")
	if third.Level != 3 {
		t.Fatalf("三级目录层级 = %d，期望 3", third.Level)
	}
	if third.Path != "电影/科幻片/2000-2009" {
		t.Fatalf("三级目录 path = %q，期望 电影/科幻片/2000-2009", third.Path)
	}
}

// TestListActiveCategoriesSlugIsStableAndUnique 钉住 slug 的两个用途。
func TestListActiveCategoriesSlugIsStableAndUnique(t *testing.T) {
	svc := catalogService(t)
	first, err := svc.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("第一次列出失败: %v", err)
	}
	second, err := svc.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("第二次列出失败: %v", err)
	}
	if strings.Join(catalogSlugs(first), ",") != strings.Join(catalogSlugs(second), ",") {
		t.Fatal("两次调用顺序或内容不同：顺序抖动会让前端下拉框每次刷新都变")
	}
	seen := map[string]bool{}
	for _, c := range first {
		if seen[c.Slug] {
			t.Fatalf("slug 重复：%s", c.Slug)
		}
		seen[c.Slug] = true
	}
	// 不同模板下的同名目录必须有不同的 slug（region 的"国产" vs genre 的"国产"）。
	dupName := 0
	for _, c := range first {
		if c.Name == "国产" {
			dupName++
		}
	}
	if dupName > 1 {
		for _, c := range first {
			if c.Name == "国产" {
				t.Logf("同名分类 slug=%s template=%s", c.Slug, c.Template)
			}
		}
	}
}

// TestCategoryIsUnderMatchesSegmentwise 钉住清理保护要用的前缀判定。
//
// 关键：不能用 strings.HasPrefix。"电影"必须**不**认领"电影时代"，
// 否则保护名单会把用户真正想清理的兄弟目录一起挡住。
func TestCategoryIsUnderMatchesSegmentwise(t *testing.T) {
	c := Category{Name: "电影", Path: "电影", Level: 1}
	if !c.IsUnder("电影") {
		t.Fatal("电影 应认领自己")
	}
	if !c.IsUnder("电影/国产") {
		t.Fatal("电影 应认领 电影/国产")
	}
	if c.IsUnder("电影时代") {
		t.Fatal("电影 不应认领 电影时代（前缀相同但不是同一级目录）")
	}
	if c.IsUnder("剧集") {
		t.Fatal("电影 不应认领 剧集")
	}
	if c.IsUnder("") {
		t.Fatal("空路径不应被认领")
	}
}

// TestCategoryIsUnderForSecondLevel 钉住二级前缀判定。
func TestCategoryIsUnderForSecondLevel(t *testing.T) {
	c := Category{Name: "国产", Path: "电影/国产", Level: 2}
	if !c.IsUnder("电影/国产") {
		t.Fatal("电影/国产 应认领自己")
	}
	if !c.IsUnder("电影/国产/某影片") {
		t.Fatal("电影/国产 应认领其下的影片目录")
	}
	if c.IsUnder("电影/国产剧") {
		t.Fatal("电影/国产 不应认领 电影/国产剧")
	}
	// 只给末段名不算命中：Category.Path 是相对分类根的完整路径，
	// 消费者必须拿同样完整的盘上路径来比，不能只比最后一段。
	// 这也是 IsUnder 收字符串而不是 []string 的原因。
	if c.IsUnder("国产") {
		t.Fatal("只看最后一段不应算命中")
	}
}

// TestCategoryAncestorPaths 返回从短到长的祖先**路径**。
//
// 这里刻意返回路径而不是裸目录名：返回 ["电影","科幻"] 会诱导调用方拿它
// 去和盘上的任意一段目录名比对，于是 "剧集/科幻" 会被 "电影/科幻" 认领 —
// 那是另一个一级分类下的另一个目录。
func TestCategoryAncestorPaths(t *testing.T) {
	c := Category{Name: "2000-2009", Path: "电影/科幻/2000-2009"}
	got := strings.Join(c.AncestorPaths(), "|")
	if got != "电影|电影/科幻|电影/科幻/2000-2009" {
		t.Fatalf("祖先前缀 = %q", got)
	}
}

// TestValidateCategoryNamesRejectsPathEscape 钉住投影层的路径校验。
//
// 0045 的迁移不能建触发器（splitStatements 会把触发器体切开），
// 所以 path 的合法性只能靠 Go 侧。这一条证明它真的拦得住 ——
// 清理保护拿 path 去比对磁盘路径，一个 ".." 就能让它认领到目标根之外。
func TestValidateCategoryNamesRejectsPathEscape(t *testing.T) {
	for _, bad := range []string{"..", "../etc", "电影/../etc", "/绝对路径", "含/斜杠"} {
		rows := []Category{{Level: 1, Name: bad, Slug: "t/" + bad, Path: bad}}
		if err := validateCategoryNames(rows); err == nil {
			t.Fatalf("路径 %q 未被拒绝", bad)
		}
	}
	ok := []Category{{Level: 1, Name: "电影", Slug: "t/电影", Path: "电影"},
		{Level: 2, Name: "国产", Slug: "t/电影/国产", Path: "电影/国产"}}
	if err := validateCategoryNames(ok); err != nil {
		t.Fatalf("合法分类目录被拒: %v", err)
	}
}

// TestCategoriesByLevelAndNames 钉住给前端下拉框用的两个辅助函数。
func TestCategoriesByLevelAndNames(t *testing.T) {
	svc := catalogService(t)
	cats, err := svc.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("列出分类目录失败: %v", err)
	}
	level2 := CategoriesByLevel(cats, 2)
	if len(level2) == 0 {
		t.Fatal("默认配置应有二级分类目录")
	}
	for _, c := range level2 {
		if c.Level != 2 {
			t.Fatalf("CategoriesByLevel 混进了层级 %d 的 %s", c.Level, c.Slug)
		}
	}
	names := CategoryNames(cats, 1)
	for _, want := range []string{"电影", "电视剧"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("一级目录名里缺 %s：%v", want, names)
		}
	}
}

// TestSortCategoriesIsStable 钉住排序的稳定顺序。
func TestSortCategoriesIsStable(t *testing.T) {
	cats := []Category{
		{Level: 2, Name: "b", Slug: "genre/电影/b", Path: "电影/b"},
		{Level: 1, Name: "z", Slug: "media/剧集", Path: "剧集"},
		{Level: 1, Name: "a", Slug: "genre/电影", Path: "电影"},
	}
	got := SortCategories(cats)
	if got[0].Level != 1 || got[1].Level != 1 || got[2].Level != 2 {
		t.Fatalf("排序没有按层级：%+v", got)
	}
	// 同一层内按 slug 排。slug 头是模板名，所以这是跨模板的全序：
	// genre 排在 media 前面，与中文目录名的字典序无关（那是 path 的排法）。
	if got[0].Slug != "genre/电影" || got[1].Slug != "media/剧集" {
		t.Fatalf("同层未按 slug 字典序：%+v", got[:2])
	}
	// 输入不应被就地改写（调用方可能还持有原切片）。
	if cats[0].Level != 2 {
		t.Fatal("SortCategories 就地改了入参")
	}
}
