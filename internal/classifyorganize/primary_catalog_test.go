package classifyorganize

// T27 C-3：一级分类表化。
//
// 放开的是「同一个媒体类型可以挂多个一级目录」。以前这里写死"必须恰好两条"，
// 用户能改目录名、不能增删一级；C-8 需要一个可枚举的一级集合，而写死的两条
// 没法表达"用户自己的第三类"（例如把 movie 先拆成"电影"，再补一条兜底的
// "电影-其他"）。
//
// 这组用例钉住三件事，缺一件 C-3 就等于没做：
//   1. 新增的一级真的能被目录模板引用（配置层），
//   2. 新增的一级真的出现在分类清单里（投影层，C-8 的输入），
//   3. 新增的一级真的能被命中（判定层，一级是 first-match-wins，
//      所以"能不能用"最终取决于顺序）。

import (
	"context"
	"strings"
	"testing"

	"litepan/internal/mediaorganize/classification"
	"litepan/internal/settings"
)

// TestThirdPrimaryCategoryIsUsable 验收⑦（上半）：配置层接受第三条一级。
//
// 用 region 模板下手而不是 custom：region 的二级字段是 origin_country，
// 校验最严格，能新增才说明约束真的放开而不是绕过了某条检查。
func TestThirdPrimaryCategoryIsUsable(t *testing.T) {
	svc := newService(t, false)
	cfg := svc.Config()

	// 在电影后面追加一条同样是 movie 的一级，带两个二级。
	cfg.Templates[1].Rules = append(cfg.Templates[1].Rules, Rule{
		Name:      "电影-其他",
		Condition: "type=movie",
		Children: []Rule{
			{Name: "短片", Condition: "origin_country=CN"},
			{Name: "实验影像", Condition: "origin_country=FR"},
		},
	})
	if _, err := svc.Update(context.Background(), cfg); err != nil {
		t.Fatalf("新增第三条一级分类应被接受，实际报错：%v", err)
	}

	read := svc.Config()
	last := read.Templates[1].Rules[len(read.Templates[1].Rules)-1]
	if last.Name != "电影-其他" {
		t.Fatalf("新增的一级没有原样保存，读回 %q", last.Name)
	}
	if len(last.Children) != 2 {
		t.Fatalf("新增一级的二级应原样保存，读回 %d 个", len(last.Children))
	}
}

// TestThirdPrimaryCategoryAppearsInCatalog 验收⑦（下半）：投影层能看到它。
//
// 这一条是 C-3 与 C-8 的接合点：一级分类表化如果只改配置不投影，
// 洗版筛选与清理保护就完全看不见新增的一级 —— 而那条链路上没有任何报错。
func TestThirdPrimaryCategoryAppearsInCatalog(t *testing.T) {
	svc := catalogService(t)
	cfg := svc.Config()
	cfg.Templates[1].Rules = append(cfg.Templates[1].Rules, Rule{
		Name:      "电影-其他",
		Condition: "type=movie",
		Children:  []Rule{{Name: "短片", Condition: "origin_country=CN"}},
	})
	mustUpdate(t, svc, cfg)

	cats, err := svc.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("读取分类清单失败：%v", err)
	}

	root := catalogOf(t, cats, "region/电影-其他")
	if root.Level != 1 {
		t.Fatalf("新增的一级 level = %d，期望 1", root.Level)
	}
	// 两级同名（"电影" 与 "电影-其他" 都是 movie）在 C-3 之后是合法的，
	// 所以一级带类型键这件事不能被当成"一级必然唯一"。
	if root.PrimaryKey != "movie" {
		t.Fatalf("新增一级的 primary_key = %q，期望 movie", root.PrimaryKey)
	}

	child := catalogOf(t, cats, "region/电影-其他/短片")
	if child.Level != 2 {
		t.Fatalf("新增一级下面的二级 level = %d，期望 2", child.Level)
	}
	if child.PrimaryKey != "" {
		t.Fatalf("二级不应带 primary_key，实际 %q", child.PrimaryKey)
	}
}

// TestThirdPrimaryCategoryIsMatchedInOrder 判定层：能用，且顺序即优先级。
//
// 一级是 first-match-wins（T02 改的），所以"新增的一条能不能生效"
// 取决于它排在电影后面 —— 排在前面的话 movie 永远命中电影，永远走不到它。
// 这里显式断言这个事实，免得后来者以为新增一级后默认就生效。
func TestThirdPrimaryCategoryIsMatchedInOrder(t *testing.T) {
	svc := newService(t, true)
	cfg := svc.Config()
	cfg.SelectedTemplate = TemplateRegion
	// 电影保留一个能兜住全部 movie 的二级，新增的一级放前面抢走 CN。
	cfg.Templates[1].Rules[0].Children = []Rule{{Name: "全部电影", Condition: "origin_country=JP"}}
	cfg.Templates[1].Rules = append([]Rule{{
		Name:      "华语电影",
		Condition: "type=movie",
		Children:  []Rule{{Name: "国产", Condition: "origin_country=CN"}},
	}}, cfg.Templates[1].Rules...)
	mustUpdate(t, svc, cfg)

	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie",
		Raw:       map[string]any{"origin_country": []string{"CN"}},
	})
	if got, want := joined(decision.RelativeSegments), "华语电影/国产"; got != want {
		t.Fatalf("新增的一级未生效：%s，期望 %s", got, want)
	}

	// 反过来把顺序换回去，同一部片子应该落到兜底那一级 ——
	// 这一条钉住"顺序即优先级"而不是"新增的那条永远赢"。
	//
	// ⚠️ 这里必须**重新分配**切片，不能用 `restored := Rules[1:]` 那种写法：
	// 那只是给同一个底层数组开了个窗口，随后 append 会把它一并改掉，
	// 校验拿到的是 [电影, 电影, 华语电影]，报的却是「存在重复一级目录：电影」——
	// 一条看起来毫不相关的错误，会把人引到「重复名校验写错了」上去查。
	cfg = svc.Config()
	rules := append([]Rule(nil), cfg.Templates[1].Rules...)
	rules[0], rules[1] = rules[1], rules[0]
	cfg.Templates[1].Rules = rules
	mustUpdate(t, svc, cfg)

	// 顺序换回去后，同一部片子落到「电影」这一级；它的二级条件是 JP，
	// CN 匹配不上，于是停在父级目录不再往下走 —— 顺带钉住了"二级不命中就
	// 停在父级"这个兜底路径（换到前面那条"新增的一级"就不同了：
	// 那条有 CN 的二级，所以它能一路走到叶子）。
	decision = classifyOK(t, svc, classification.Request{
		MediaType: "movie",
		Raw:       map[string]any{"origin_country": []string{"CN"}},
	})
	if got, want := joined(decision.RelativeSegments), "电影"; got != want {
		t.Fatalf("调整顺序后结果未随之改变：%s，期望 %s", got, want)
	}
}

// TestBuiltInRootsStillRequired C-3 的下界：movie 与 tv 两条基准不能删。
//
// 「放开下界」只针对新增；删基准会让 region / genre / custom 三个模板的
// 二级字段失去挂载点，而那种配置表面上完全合法、直到某天某部片子
// 找不到该去哪个目录才暴露。
func TestBuiltInRootsStillRequired(t *testing.T) {
	svc := newService(t, false)

	for name, mutate := range map[string]func(*Config){
		"删掉电影": func(cfg *Config) {
			cfg.Templates[1].Rules = cfg.Templates[1].Rules[1:]
		},
		"删掉电视剧": func(cfg *Config) {
			cfg.Templates[1].Rules = cfg.Templates[1].Rules[:1]
		},
		"只剩电影": func(cfg *Config) {
			cfg.Templates[1].Rules = cfg.Templates[1].Rules[:1]
			cfg.Templates[1].Rules = append(cfg.Templates[1].Rules, Rule{
				Name: "影片", Condition: "type=movie",
				Children: []Rule{{Name: "国产", Condition: "origin_country=CN"}},
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := svc.Config()
			mutate(&cfg)
			if _, err := svc.Update(context.Background(), cfg); err == nil {
				t.Fatal("删掉 movie / tv 基准一级应被拒绝")
			}
		})
	}
}

// TestThirdPrimaryMustStillUseMovieOrTVType C-3 没有放开的部分。
//
// 放开的是"同一媒体类型多个一级"，不是"凭空多一种媒体类型"：
// 一级的匹配条件仍然是 type=movie / type=tv，写别的类型键一律拒绝。
// 少了这一条，"新增一级"会退化成"可以编一个不存在的媒体类型"，
// 而那类规则永远不会命中，也不会报错。
func TestThirdPrimaryMustStillUseMovieOrTVType(t *testing.T) {
	svc := newService(t, false)
	for _, cond := range []string{"type=other", "type=", "genres=动作", "type=movie,tv"} {
		cfg := svc.Config()
		cfg.Templates[1].Rules = append(cfg.Templates[1].Rules, Rule{
			Name: "新增", Condition: cond,
			Children: []Rule{{Name: "子类", Condition: "origin_country=CN"}},
		})
		if _, err := svc.Update(context.Background(), cfg); err == nil {
			t.Fatalf("一级条件 %q 应被拒绝（类型键只收 movie / tv）", cond)
		}
	}
}

// TestExistingConfigsStillLoad C-3 验收⑥：存量模板必须能加载。
//
// 存量配置里的一级只有电影/电视剧两条，走的是新校验的"通过"分支。
// 这一条的存在意义是反向的：如果 requireBuiltInRoots 的判据写错
// （比如按目录名而不是按类型键找基准），存量配置会全部加载失败，
// 而那种失败发生在**加载**阶段，会让用户看到分类功能直接消失。
func TestExistingConfigsStillLoad(t *testing.T) {
	svc := newService(t, false)
	for _, kind := range []string{TemplateMedia, TemplateRegion, TemplateGenre, TemplateCustom} {
		cfg := svc.Config()
		cfg.SelectedTemplate = kind
		for _, tpl := range cfg.Templates {
			if tpl.Kind != kind {
				continue
			}
			var movies, tvs int
			for _, r := range tpl.Rules {
				if strings.Contains(r.Condition, "type=movie") {
					movies++
				}
				if strings.Contains(r.Condition, "type=tv") {
					tvs++
				}
			}
			if movies == 0 || tvs == 0 {
				t.Fatalf("内置模板 %s 缺基准一级：movie=%d tv=%d", kind, movies, tvs)
			}
		}
		if _, err := svc.Update(context.Background(), cfg); err != nil {
			t.Fatalf("模板 %s 的存量配置应能原样加载：%v", kind, err)
		}
	}
}

// TestPrimarySwitchOffFlattensPath T27 C-3：一级开关真的会生效。
//
// 这条用例存在的直接原因：`levels.primary` 这个字段从 T02 引入起就没人读过，
// 而 T27 又在 registry 里登记了 mo_classification_primary_enabled ——
// 一个登记了却没人消费的设置项，等于给用户一个拧了没反应的旋钮。
// 它是本任务唯一一个"不测就一定会漏"的地方，所以单独钉住。
func TestPrimarySwitchOffFlattensPath(t *testing.T) {
	svc := newServiceWithValues(t, map[string]string{
		settings.KeyMOClassificationEnabled:          boolString(true),
		settings.KeyMOClassificationPrimaryEnabled:   boolString(false),
		settings.KeyMOClassificationSecondaryEnabled: boolString(true),
		settings.KeyMOClassificationTertiaryEnabled:  boolString(true),
		settings.KeyMOClassificationSeriesEnabled:    boolString(true),
	})
	cfg := svc.Config()
	cfg.SelectedTemplate = TemplateRegion
	// 内置模板必须有三级/系列才测得出来它们有没有跟着关。
	cfg.Templates[1].Rules[0].Children[0].Children = []Rule{{Name: "2020年代", Condition: "year=2020-2029"}}
	cfg.Series = []SeriesRule{{Name: "流浪地球", DirName: "流浪地球系列", SeriesKeywords: []string{"流浪地球"}}}
	mustUpdate(t, svc, cfg)

	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie",
		Title:     "流浪地球",
		Year:      2019,
		Raw:       map[string]any{"origin_country": []string{"CN"}, "keywords": []string{"流浪地球"}},
	})
	if len(decision.RelativeSegments) != 0 {
		t.Fatalf("关掉一级后应直接落到分类根目录，实际路径 %q", joined(decision.RelativeSegments))
	}
	if !decision.Matched {
		t.Fatal("关掉一级仍应完成分类判定（Matched=true），否则统计里会出现「未命中分类」")
	}
	if decision.Evidence["primary_disabled"] != true {
		t.Fatalf("证据里应标明一级被关，实际 %v", decision.Evidence)
	}
}

// TestSecondarySwitchOffStillCascades 回归：加了一级开关之后，
// 原本的「关二级连带关三级与系列」不能被改坏。
func TestSecondarySwitchOffStillCascades(t *testing.T) {
	got := resolveLevels(true, false, true, true)
	if got.secondary || got.tertiary || got.series {
		t.Fatalf("关二级应连带关三级与系列，实际 %+v", got)
	}
	if !got.primary {
		t.Fatal("关二级不该影响一级")
	}
}
