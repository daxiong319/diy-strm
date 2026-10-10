package settings

import (
	"sort"
	"strings"
	"unicode"
)

// 设置项搜索索引（后台 ⌘G 直达）。
//
// 为什么索引必须从注册表派生而不是前端手写清单：注册表是唯一真相，
// 而手写清单必然会漏。漏掉的症状特别难查 —— 设置页上找得到、搜索里搜不到，
// 用户只会得出「这个功能没做」这一个结论。所以本文件只做「注册表 ⇒ 索引」的
// 派生与**少量人工判断**（这个设置在哪一页），不做任何清单。
//
// 派生规则：
//   - key / type / label / description / 分组：全部来自注册表，
//     改动注册表即刻反映到索引，无需同步第二处。
//   - 锚点：稳定地由 key 生成，形如 setting-<key>。前端按同一规则渲染 id，
//     两边只有这一个约定，不额外维护映射表。
//   - 关键词：key 拆词 + 中文标签 + 分组名 + 少量人工补充的别名。

// IndexEntry 是索引里的一条设置项。
type IndexEntry struct {
	Key         string `json:"key"`
	Type        Type   `json:"type"`
	Category    string `json:"category"`
	CategoryHit string `json:"category_label"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Page 是承载这个设置的后台页面 key（/admin?page=<Page>）。
	Page string `json:"page,omitempty"`
	// Tab 是该页面内的 tab key（/admin?page=<Page>&tab=<Tab>）。可空。
	Tab string `json:"tab,omitempty"`
	// Anchor 是页内定位锚点，形如 setting-<key>。可空表示该页上没有对应行
	// （例如这个设置被专用面板消费，不在通用设置表单里）。
	Anchor string `json:"anchor,omitempty"`
	// Anchored 说明 Anchor 真的指向一个可滚动的行。为 false 时前端不应该
	// 假装滚到了那个设置上 —— 用户看到「已经定位」但其实停在页首，
	// 比明确只跳到页面更让人困惑。
	Anchored bool `json:"anchored"`
	// Keywords 是小写的搜索词，含 key 拆词、标签、分组名与人工别名。
	Keywords []string `json:"keywords"`
}

// Index 是 GET /admin/settings/index 的返回体。
type Index struct {
	Categories []Category   `json:"categories"`
	Items      []IndexEntry `json:"items"`
	// Coverage 说明本次索引覆盖到哪一步 —— 直接把边界回给前端，
	// 比让前端自己猜「搜不到就是没收录」要诚实。
	Coverage string `json:"coverage"`
}

// indexCoverage 如实描述索引的覆盖范围。这段话会随索引一起发到前端。
const indexCoverage = "索引由注册表自动派生：新增一个设置项即自动出现在搜索里，无需同步任何清单。" +
	"Hidden 设置项（有 key 但界面上没有对应行，例如旧功能遗留键）**不收录** —— 搜到了也无处可去。" +
	"少数由专用面板消费的性能类设置不在通用设置表单里，跳转后只能到页面级定位，不会滚动到具体那一行。"

// categoryRoute 登记「这个分组的设置在哪一页」。
//
// ⚠️ 这张表是**人工判断**，不是从注册表能推出来的东西：注册表只知道分组，
// 不知道哪个页面消费它。宁可显式写在这里，也不要在前端散落 —— 两处都会写
// 就会漂移，而漂移的症状是「跳转过去是空页」。
type categoryRoute struct {
	Page string
	Tab  string
	// Anchored 表示该分组的设置在目标页上有真实的行锚点。
	Anchored bool
}

var categoryRoutes = map[string]categoryRoute{
	"system":          {Page: "settings", Tab: "services", Anchored: true},
	"account_display": {Page: "settings", Tab: "services", Anchored: true},
	"fnos":            {Page: "settings", Tab: "services", Anchored: true},
	"discover":        {Page: "discover", Anchored: false},
	"subtitle":        {Page: "subtitle", Anchored: false},
	"strm":            {Page: "tasks", Tab: "strm", Anchored: false},
	// T17 的播放路径映射 / 302 直连 / 跨账户转移配在 STRM 设置页，
	// 所以走 tasks→strm 这条路，而不是 organize 分组（那是整理页）。
	"playback":       {Page: "tasks", Tab: "strm", Anchored: false},
	"telegram":       {Page: "telegram", Anchored: false},
	"media_organize": {Page: "tasks", Tab: "organize", Anchored: false},
	"emby":           {Page: "dashboard", Anchored: false},
	"performance":    {Page: "dashboard", Anchored: false},
}

// keyRoutes 覆盖分组级判断对个别 key 不成立的情况。
//
// ⚠️ 这张表只放「确实知道」的例子。分组级判断错了的其它 key 宁可沿用
// 分组级判断（跳到页面而不是跳到行）—— 跳错页面的代价是一个多余点击，
// 而编一个不存在的行锚点会让用户以为已经定位到了。
var keyRoutes = map[string]categoryRoute{
	// FUSE 读缓存那几个键的描述里明写「在『文件共享 → 本地挂载』页配置」，
	// 而它们被登记在 performance 分组下 —— 按分组跳会落到仪表盘的性能面板。
	KeyFuseReadCacheEnabled:        {Page: "share", Tab: "fuse", Anchored: false},
	KeyFuseReadCacheMaxGB:          {Page: "share", Tab: "fuse", Anchored: false},
	KeyFuseReadCacheRetentionDays:  {Page: "share", Tab: "fuse", Anchored: false},
	KeyFuseReadCacheEvictionPolicy: {Page: "share", Tab: "fuse", Anchored: false},
}

// categoryLabels 分组 ID → 中文名。取自 categories()，同时兜住未登记的分组。
func categoryLabels() map[string]string {
	out := make(map[string]string, len(categoryRoutes)+4)
	for _, c := range Categories() {
		out[c.ID] = c.Label
	}
	return out
}

// keyAliases 人工补充的中文别名。
//
// 只放注册表推不出来的词：key 与 label 能覆盖「缓存」/「cache_ttl」这类，
// 覆盖不了的是用户脑子里实际在搜的叫法（「内存占用」「磁盘占用」「网盘限速」）。
// 别名错了最坏只是多搜出几条结果，所以宁多勿缺。
var keyAliases = map[string][]string{
	KeyCacheMemoryLimitMB:       {"内存", "内存占用", "占内存", "memory"},
	KeyCacheMaxItems:            {"条目", "条数", "条目数", "缓存条数"},
	KeyCacheTTL:                 {"有效期", "过期时间", "ttl"},
	KeyBuiltinOfflineMaxSpeedMB: {"限速", "下载限速", "速度", "限速"},
	KeyUploadTaskConcurrency:    {"并发", "并行", "同时上传", "concurrency"},
	KeyBuiltinOfflineTempDir:    {"临时目录", "缓存目录", "下载目录"},
	KeyBuiltinOfflineBTPort:     {"bt", "磁力端口", "端口"},
}

// BuildIndex 从注册表派生搜索索引。
func BuildIndex() Index {
	specs := AllSpecs()
	cats := Categories()
	labels := categoryLabels()

	items := make([]IndexEntry, 0, len(specs))
	for _, sp := range specs {
		// Hidden 的键不进索引。
		//
		// 它们界面上没有对应的行（多数是旧功能遗留键，由专用面板或后端
		// 直接消费），收录进来只能给出一个跳不动的结果 —— 用户搜到了却
		// 没有任何可做的事，那比搜不到更让人困惑。索引是「帮我找到它并过去」，
		// 不是「让我知道系统里还有这个键」。
		if sp.Hidden {
			continue
		}
		route, ok := keyRoutes[sp.Key]
		if !ok {
			route = categoryRoutes[sp.Category]
		}
		catLabel := labels[sp.Category]
		if catLabel == "" {
			// 分组没登记时不要编一个名字出来：编出来的名字会被用户当成
			// 正式分类，而实际上界面上这一组连标题都没有。
			catLabel = sp.Category
		}
		entry := IndexEntry{
			Key:         sp.Key,
			Type:        sp.Type,
			Category:    sp.Category,
			CategoryHit: catLabel,
			Label:       sp.Label,
			Description: sp.Description,
			Page:        route.Page,
			Tab:         route.Tab,
			Anchored:    route.Anchored,
			Keywords:    buildKeywords(sp, catLabel),
		}
		entry.Anchor = AnchorFor(sp)
		items = append(items, entry)
	}

	// 无 label 的项（几乎都是 Hidden 的遗留键）在搜索里没法用中文命中，
	// 但用户搜 key 名是搜得到的，所以照样收录；只是把它们挪到末尾 ——
	// 用户搜中文时它们一个都命中不了，排在前面只会把真正的结果挤下去。
	// 分区而不是排序：保持注册表声明顺序对可读性也有好处。
	ordered := make([]IndexEntry, 0, len(items))
	for _, it := range items {
		if it.Label != "" {
			ordered = append(ordered, it)
		}
	}
	for _, it := range items {
		if it.Label == "" {
			ordered = append(ordered, it)
		}
	}

	return Index{Categories: cats, Items: ordered, Coverage: indexCoverage}
}

// buildKeywords 组装一条索引项的搜索词：小写、去重。
func buildKeywords(sp Spec, catLabel string) []string {
	set := make(map[string]bool, 16)
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			set[s] = true
		}
	}
	add(sp.Key)
	for _, w := range splitKeyWords(sp.Key) {
		add(w)
	}
	add(sp.Label)
	add(catLabel)
	// select 的选项名也是用户会搜的：「lru」「大文件优先」。
	for _, o := range sp.Options {
		add(o.Label)
		add(o.Value)
	}
	for _, a := range keyAliases[sp.Key] {
		add(a)
	}
	// Unit 也算一个词：「分钟」「MB/s」—— 用户经常直接搜单位。
	add(sp.Unit)

	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AnchorFor 决定这个条目能不能给出可跳转的锚点。
//
// 刻意提成独立函数，而不是把 `if sp.Label != ""` 留在 BuildIndex 里：
// 今天注册表里「可见但没有中文标签」的设置恰好一个都没有（没标签的 35 个
// 全是 Hidden，而 Hidden 不进索引），所以留在原地的话那个分支永远走不到，
// 写了也测不了 —— 变异实测：把条件改成无条件赋值，全绿。
// 提成函数后可以直接拿一个合成的无标签 Spec 调它，
// 断言由 TestWiringSettingsIndexAnchorForSkipsUnlabeled 兜住。
func AnchorFor(sp Spec) string {
	// 没有标签 = 界面上没有对应的一行 = 给出一个 id 也是落空。
	// 「搜到了却什么都没发生」比「搜不到」更让人困惑。
	if sp.Label == "" {
		return ""
	}
	return "setting-" + sp.Key
}

// splitKeyWords 把 snake_case 的 key 拆成词。
//
// 只按 _ 和数字边界拆，不按大小写拆：注册表里的 key 全是小写下划线风格，
// 而把驼峰也当作边界会在别的命名风格下切出无意义的碎片。
func splitKeyWords(key string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = nil
		}
	}
	runes := []rune(key)
	for i, r := range runes {
		if r == '_' {
			flush()
			continue
		}
		// 数字边界：cache_ttl30 这种写法拆成 cache/ttl/30。
		if i > 0 && unicode.IsDigit(r) && !unicode.IsDigit(runes[i-1]) {
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return out
}
