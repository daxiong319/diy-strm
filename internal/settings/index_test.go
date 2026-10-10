package settings_test

import (
	"strings"
	"testing"

	"litepan/internal/settings"
)

// T13 · 设置项搜索索引。
//
// 索引的核心承诺只有一句：**注册表里有什么，索引里就有什么**。
// 下面这组用例全部围绕这一句 —— 凡是「索引漏了注册表里的东西」或
// 「索引编出了注册表里没有的东西」，都算破坏承诺。

func TestWiringSettingsIndexDerivesFromRegistry(t *testing.T) {
	idx := settings.BuildIndex()
	specs := settings.AllSpecs()
	if len(specs) == 0 {
		t.Fatal("注册表是空的，下面的断言没有意义")
	}
	// Hidden 的键不进索引（界面上没有对应的行，跳过去也没用）。
	// 所以断言是「每个可见键都进了索引」，而不是「条目数相等」。
	want := make(map[string]bool)
	for _, sp := range specs {
		if sp.Hidden {
			continue
		}
		want[sp.Key] = true
	}
	if len(want) == 0 {
		t.Fatal("注册表里一个可见键都没有，测试前提不成立")
	}
	got := make(map[string]bool, len(idx.Items))
	for _, it := range idx.Items {
		if got[it.Key] {
			t.Errorf("索引里 key %q 重复了", it.Key)
		}
		got[it.Key] = true
	}
	for key := range want {
		if !got[key] {
			t.Errorf("注册表里的可见键 %q 没有出现在索引里", key)
		}
	}
	for key := range got {
		if !want[key] {
			t.Errorf("索引里有注册表里没有的键 %q —— 索引不许凭空多出条目", key)
		}
	}
	// 反向保证：Hidden 键确实被排除了。这条独立写，是因为「漏排除」和
	// 「漏收录」是同一个 bug 的两面，只断言一面会让人以为自己看全了。
	for _, sp := range specs {
		if !sp.Hidden {
			continue
		}
		if got[sp.Key] {
			t.Errorf("Hidden 键 %q 不该出现在索引里（界面上没有对应的行）", sp.Key)
		}
	}
}

// TestWiringSettingsIndexCarriesRegistryMetadata 索引条目的元数据必须来自注册表本身。
//
// 这条断言挡的是「索引里另抄一份 label」这种改法 —— 改了注册表的 label，
// 设置页上显示新名字而搜索结果里还是旧名字，用户按看到的名字搜不到。
func TestWiringSettingsIndexCarriesRegistryMetadata(t *testing.T) {
	byKey := make(map[string]string)
	byCat := make(map[string]string)
	for _, sp := range settings.AllSpecs() {
		byKey[sp.Key] = sp.Label
		byCat[sp.Key] = sp.Category
	}
	for _, it := range settings.BuildIndex().Items {
		if want, ok := byKey[it.Key]; ok && it.Label != want {
			t.Errorf("%s 的索引 label = %q，注册表里是 %q", it.Key, it.Label, want)
		}
		if want, ok := byCat[it.Key]; ok && it.Category != want {
			t.Errorf("%s 的索引分组 = %q，注册表里是 %q", it.Key, it.Category, want)
		}
	}
}

// TestWiringSettingsIndexKeywordsAlwaysContainTheKey 搜 key 名一定搜得到。
//
// 这是索引的最低承诺：即便一个设置没有中文标签（Hidden 的遗留键），
// 用户把 key 粘进搜索框也得能命中 —— 否则这类项等于悄悄消失了。
func TestWiringSettingsIndexKeywordsAlwaysContainTheKey(t *testing.T) {
	for _, it := range settings.BuildIndex().Items {
		found := false
		for _, kw := range it.Keywords {
			if kw == it.Key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s 的关键词里没有自己的 key：%v", it.Key, it.Keywords)
		}
	}
}

// TestWiringSettingsIndexSplitsKeyIntoWords cache_ttl 要能拆成 cache 和 ttl。
func TestWiringSettingsIndexSplitsKeyIntoWords(t *testing.T) {
	for _, it := range settings.BuildIndex().Items {
		if !strings.Contains(it.Key, "_") {
			continue
		}
		head, _, ok := strings.Cut(it.Key, "_")
		if !ok || head == "" {
			continue
		}
		if !hasKeyword(it.Keywords, head) {
			t.Fatalf("%s 的关键词里没有拆出来的 %q：%v", it.Key, head, it.Keywords)
		}
	}
}

// TestWiringSettingsIndexAnchorMatchesFrontendConvention 锚点形如 setting-<key>。
//
// 前端按同一个约定渲染 id，两边只有这一个约定 —— 锚点规则散到两处维护时，
// 症状是「搜索能跳到页面但滚不到那一行」，很难看出是哪边改了。
func TestWiringSettingsIndexAnchorMatchesFrontendConvention(t *testing.T) {
	for _, it := range settings.BuildIndex().Items {
		want := "setting-" + it.Key
		if it.Label == "" {
			// 没有标签的项界面上没有对应的行，不能给出一个假的锚点。
			if it.Anchor != "" || it.Anchored {
				t.Errorf("%s 没有标签却带了锚点 %q", it.Key, it.Anchor)
			}
			continue
		}
		if it.Anchor != want {
			t.Errorf("%s 的锚点 = %q，期望 %q", it.Key, it.Anchor, want)
		}
	}
}

// TestWiringSettingsIndexAdmitsItsOwnLimits 索引必须自带覆盖说明。
//
// 一个不声明边界的索引，前端就会默认「搜不到 = 没收录」，于是把
// 「这个功能没做」告诉用户。诚实的做法是把边界随索引一起发过去。
func TestWiringSettingsIndexAdmitsItsOwnLimits(t *testing.T) {
	idx := settings.BuildIndex()
	if strings.TrimSpace(idx.Coverage) == "" {
		t.Fatal("索引没有声明覆盖范围")
	}
	for _, want := range []string{"注册表", "Hidden"} {
		if !strings.Contains(idx.Coverage, want) {
			t.Errorf("覆盖说明里没有提到 %q：%s", want, idx.Coverage)
		}
	}
}

// TestWiringSettingsIndexEveryCategoryIsRegistered 注册表里用到的分组必须在
// categories() 里登记过。
//
// ⚠️ 这条断言是本组用例里唯一会**红给人看**的：历史上 fnos(5 个 key) 与
// mediaorganize(4 个 key，media_organize 的笔误) 都用了没登记的分类，
// 于是这些设置在界面上没有分组标题、在索引里也没有可用的分组名。
// 新增一个分类却忘了登记，第一时间就会在这里暴露出来。
func TestWiringSettingsIndexEveryCategoryIsRegistered(t *testing.T) {
	declared := make(map[string]bool)
	for _, c := range settings.Categories() {
		declared[c.ID] = true
	}
	var undeclared []string
	for _, sp := range settings.AllSpecs() {
		// 空分组是刻意的（Hidden 的遗留键不属于任何界面分区）。
		if sp.Category == "" || declared[sp.Category] {
			continue
		}
		undeclared = append(undeclared, sp.Category)
	}
	if len(undeclared) > 0 {
		t.Fatalf("这些设置分组没有在 categories() 里登记：%v —— "+
			"它们在界面上没有分组标题。补一条登记，或改用已登记的分组", undeclared)
	}
}

// TestWiringSettingsIndexEveryCategoryHasALabel 索引里的分组名不能是空串。
func TestWiringSettingsIndexEveryCategoryHasALabel(t *testing.T) {
	for _, it := range settings.BuildIndex().Items {
		if it.Category == "" {
			continue
		}
		if strings.TrimSpace(it.CategoryHit) == "" {
			t.Errorf("%s 的分组名是空的", it.Key)
		}
	}
}

// TestWiringSettingsIndexPointsAtRealPages 索引给出的页面必须是真实存在的一级页面。
//
// 手写这张映射表最大的风险就是写出一个不存在的 page —— 症状是「点搜索结果
// 跳过去是空白」，而这种问题在开发时很容易被「页面默认渲染」掩盖过去。
func TestWiringSettingsIndexPointsAtRealPages(t *testing.T) {
	known := map[string]bool{
		"dashboard": true, "accounts": true, "settings": true, "tasks": true,
		"tools": true, "cross-transfer": true, "cas": true, "share": true,
		"discover": true, "media-upgrade": true, "subtitle": true,
		"mcp": true, "assistant": true, "rbac": true, "request": true,
		"telegram": true,
		"wecom":    true,
	}
	for _, it := range settings.BuildIndex().Items {
		if it.Page == "" {
			t.Errorf("%s 没有给出目标页面", it.Key)
			continue
		}
		if !known[it.Page] {
			t.Errorf("%s 指向了不存在的页面 %q", it.Key, it.Page)
		}
		if it.Anchored && it.Page != "settings" {
			// 目前只有通用设置表单的行带锚点；如果哪天别的页面也加了
			// data-setting-key，这条要跟着放宽 —— 那时它才有意义。
			t.Errorf("%s 声明可定位，但 %q 页并没有通用设置表单", it.Key, it.Page)
		}
	}
}

// TestWiringSettingsIndexDoesNotLeakValues 索引里绝不能出现配置值。
//
// 索引只用于搜索。带上值就等于给搜索端点加了一条「读全部配置」的旁路，
// 而它挂在 system.manage 之下，比逐项读设置宽松得多（一次拿到 190 项）。
func TestWiringSettingsIndexDoesNotLeakValues(t *testing.T) {
	idx := settings.BuildIndex()
	// 敏感键的默认值必须一个都不出现在索引里。
	for _, sp := range settings.AllSpecs() {
		if !sp.Sensitive || sp.Default == "" {
			continue
		}
		for _, it := range idx.Items {
			if it.Key != sp.Key {
				continue
			}
			blob := strings.Join(it.Keywords, " ") + " " + it.Label + " " + it.Description
			if strings.Contains(blob, sp.Default) {
				t.Errorf("敏感键 %s 的默认值出现在了索引里", sp.Key)
			}
		}
	}
}

// TestWiringSettingsIndexRegistryHasNotSilentlyShrunk AllSpecs() 必须是注册表的
// 全量，不能悄悄少几项。
//
// ⚠️ 这条用例是「自我参照」陷阱的补丁：DerivesFromRegistry 拿 AllSpecs() 当
// 期望值，而 BuildIndex() 也是从 AllSpecs() 派生的 —— 两边同时少一项时，
// 那个用例照样绿。变异实测过：把 AllSpecs 改成 `return out[:len(out)-1]`
// 仍然全绿。所以这里必须有一份**不经过这两个函数**的独立锚点：
// 条目总数 + 几个跨分组、跨历史时期的哨兵 key。
func TestWiringSettingsIndexRegistryHasNotSilentlyShrunk(t *testing.T) {
	specs := settings.AllSpecs()
	// 总数：新增设置时这个数会变，届时把它改成新值并在提交信息里说明；
	// 「静默少一项」则会直接变红。190 → 196 是 T14 加的 6 个整理设置
	//（mo_scrape_nfo_enabled / mo_scrape_nfo_target / mo_scrape_unrecognized_dir /
	//  mo_scrape_follow_existing_location / mo_scrape_skip_action / mo_backup_target）；
	// 196 → 202 是 T15 加的 6 个风控阈值
	//（mo_scrape_max_calls_per_window / mo_scrape_call_window_seconds / mo_scrape_call_pause_seconds /
	//  mo_scrape_max_work_minutes / mo_scrape_work_pause_minutes / mo_min_media_size_bytes）。
	const wantTotal = 225
	if len(specs) != wantTotal {
		t.Errorf("注册表条目数 = %d，期望 %d（新增设置请同步改这个数；"+
			"若真的少了条目，说明 AllSpecs 漏导出或 defaultSpecs 被改坏）",
			len(specs), wantTotal)
	}
	byKey := make(map[string]bool, len(specs))
	for _, sp := range specs {
		byKey[sp.Key] = true
	}
	// 哨兵：性能组最早的开关、system 组、fnos 组、strm 组。
	// 取这几个是因为它们分布在注册表的首尾与中段，
	// 任何「截断头部/尾部/整组漏导出」都会撞上其中至少一个。
	for _, key := range []string{
		settings.KeyCacheEnabled,     // performance，最靠前
		settings.KeyLogRetentionDays, // system
		settings.KeyFnosURL,          // fnos 组，中段
		settings.KeyStrmToken,        // strm
	} {
		if !byKey[key] {
			t.Errorf("注册表里少了哨兵 key %q", key)
		}
	}
}

// TestWiringSettingsIndexAnchorForSkipsUnlabeled 直接测锚点规则本身。
//
// ⚠️ 这条用例是为一个**曾经的假通行证**补的：把 BuildIndex 里的
// `if sp.Label != "" { entry.Anchor = ... }` 改成无条件赋值时全绿。
// 根因是今天注册表里「可见但无标签」的设置一个都没有
// （没标签的 35 个恰好全是 Hidden，而 Hidden 不进索引），
// 留在 BuildIndex 里那个分支永远走不到。
//
// 所以规则被提成了 anchorFor，这里用**合成的** Spec 造出无标签的情形来测 ——
// 索引端到端那条路今天确实覆盖不到，假装覆盖只会骗下一个人。
func TestWiringSettingsIndexAnchorForSkipsUnlabeled(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec settings.Spec
		want string
	}{
		{
			name: "有标签就给约定锚点",
			spec: settings.Spec{Key: "cache_ttl", Label: "全局缓存时间"},
			want: "setting-cache_ttl",
		},
		{
			name: "无标签就没有锚点（界面上没有这一行）",
			spec: settings.Spec{Key: "legacy_key"},
			want: "",
		},
	} {
		if got := settings.AnchorFor(tc.spec); got != tc.want {
			t.Errorf("%s：AnchorFor(%s/%q) = %q，期望 %q",
				tc.name, tc.spec.Key, tc.spec.Label, got, tc.want)
		}
	}
}

// TestWiringSettingsIndexEveryEntryHasAnchorWhenLabeled 端到端那条路：
// 索引里凡是带标签的条目都必须有锚点，缺一个就跳不过去。
func TestWiringSettingsIndexEveryEntryHasAnchorWhenLabeled(t *testing.T) {
	for _, it := range settings.BuildIndex().Items {
		if it.Label == "" {
			// 无标签的项不给锚点（今天索引里没有这类条目，
			// 规则由上面那条合成用例守住）。
			if it.Anchor != "" {
				t.Errorf("%s 没有标签却带了锚点 %q", it.Key, it.Anchor)
			}
			continue
		}
		if it.Anchor != "setting-"+it.Key {
			t.Errorf("%s 有标签却没有按约定给锚点（%q）", it.Key, it.Anchor)
		}
	}
}

func hasKeyword(kws []string, want string) bool {
	for _, k := range kws {
		if k == want {
			return true
		}
	}
	return false
}
