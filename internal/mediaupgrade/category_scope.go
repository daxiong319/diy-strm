package mediaupgrade

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// T27 C-8 消费者一：洗版规则「适用二级分类」
//
// 洗版要回答的问题是「这条规则管哪一类片子」。分类目录是用户给片子分堆的方式，
// 所以「只管国产剧」这种范围应该用分类目录表达，而不是让用户去写文件名正则。
//
// ⚠️ 这里依赖的是 classifyorganize.ListActiveCategories 的语义：
// **返回的是规则里配了哪些分类，不是磁盘上有哪些分类目录。**
// 对洗版来说这正是想要的：用户配了什么就有什么可筛，
// 磁盘上碰巧有个同名目录但配置里没有，它就不该被当成一个可选分类范围。
// ---------------------------------------------------------------------------

// CategoryScope 是一次扫描的分类目录范围过滤。
//
// 空 Scope（未设置）表示不过滤，与洗版其它可选条件（MinResolution、
// RequireSubtitle 等）的"没配就是不限制"一致。
type CategoryScope struct {
	// PrimaryNames 一级分类目录名（"电影"、"电视剧"、"综艺"…）。空 = 不限。
	//
	// 为什么按**目录名**而不是 slug：slug 带模板名（"media/电影"），
	// 而筛洗版范围的人心里想的是"电影这一类"，模板只是承载它的载体。
	// 同名目录跨模板出现时（region 与 genre 都有"国产"），
	// 按名字匹配会把两边的"国产"都算进来 —— 这是刻意的：
	// 用户说"国产"时说的是"国产这些片子"，不是"在地区模板下叫国产的那个目录"。
	PrimaryNames []string
	// SecondaryNames 二级分类目录名（"国产"、"科幻奇幻"…）。空 = 不限。
	//
	// 语义是**跨父级的**：只要某个一级分类下有任何一个二级分类命中，
	// 该一级分类下的所有文件都进入本次扫描。
	// 理由是二级目录名本身不带父级信息（"国产"在电影和电视剧下都有），
	// 跨父级匹配是唯一不会误伤的解释。
	SecondaryNames []string
}

// Empty 判断是否没有设置任何范围限制。
func (s CategoryScope) Empty() bool {
	return len(s.PrimaryNames) == 0 && len(s.SecondaryNames) == 0
}

// categoryFilter 按分类目录名过滤媒体库文件。
//
// 只过滤**媒体库文件**（library），不筛候选文件：候选文件的作用是
// "为库里的某部片子提供更好的版本"，它们可能还没被整理进任何分类目录，
// 按分类筛候选会让"库里的国产剧找不到候选里的国产片替换版本"。
//
// 返回 (kept, dropped) 而不是只返回 kept：扫描记录里要显示
// "因分类范围被跳过 N 个文件"，否则用户配了一个很窄的范围却看到
// 库文件数暴跌，只能自己去猜。
//
// libraryRoot 是**锚点**：分类段从媒体库根往后数，不是从 / 数。
// 第一版把"路径第一段就是一级分类"当成不变量，结果 /tmp/TestXxx/001/电影/...
// 的第一段是 tmp，于是按一级筛的用例全军覆没，而按二级筛的用例"通过"了
// —— 因为那条走的是全路径搜名字，恰好命中了我自己在注释里警告过的那个误匹配形状。
// 测试全绿、实现是错的，比一条红的测试危险得多。
func categoryFilter(files []libFile, libraryRoot string, scope CategoryScope) (kept, dropped []libFile) {
	if scope.Empty() {
		return files, nil
	}
	primaries := nameSet(scope.PrimaryNames)
	secondaries := nameSet(scope.SecondaryNames)

	kept = make([]libFile, 0, len(files))
	dropped = make([]libFile, 0)
	for _, f := range files {
		if scopeHasLibraryCategory(f.Path, libraryRoot, primaries, secondaries) {
			kept = append(kept, f)
		} else {
			dropped = append(dropped, f)
		}
	}
	return kept, dropped
}

// scopeHasLibraryCategory 判断一个媒体库文件的路径是否落在筛选范围内。
//
// 路径形态：<LibraryRoot>/<一级>/[<二级>/[<三级>/]]<文件名>，
// 也就是 mediaorganize/planner 把 classification.RelativeSegments
// 逐段 ensureDirAction 拼出来的结果（planner/classification.go）。
// 所以**相对库根的第一段就是一级分类目录名**，第二段是二级 ——
// 固定位置，不需要认规则。
//
// ⚠️ "相对库根"是硬要求。库根是 /data/媒体库 这种多段绝对路径时，
// 直接数整条路径的第 N 段数到的会是盘符和用户目录。
//
// 一个例外：用户可能把分类根直接设成媒体库根（target_root == library_root），
// 此时第一段就是一级分类名，仍然成立。
// 另一种情况是媒体库里还混着没经过分类整理的文件，它们第一段是片名、
// 后面没有分类段 —— 这些在任何筛选范围下都会被排除掉。
// 这是刻意的：按分类筛洗版只对已经分类整理过的库有意义，
// 没整理过的文件连属于哪一类都不知道。
func scopeHasLibraryCategory(path, libraryRoot string, primaries, secondaries map[string]bool) bool {
	segs := libraryCategorySegments(path, libraryRoot)
	if len(segs) == 0 {
		return false
	}
	if len(primaries) > 0 && !primaries[segs[0]] {
		return false
	}
	if len(secondaries) == 0 {
		return true
	}
	// 二级按跨父级匹配：命中任意一段即可。
	for _, seg := range segs[1:] {
		if secondaries[seg] {
			return true
		}
	}
	return false
}

// libraryCategorySegments 从一个媒体库文件的绝对路径里取出分类目录段。
//
// 只取到"看起来像目录"的最后一段之前的全部段。做法是按分隔符切开后，
// 去掉最后一段（文件名），剩下的是分类目录 + 可能的更深层目录。
// 更深层（系列目录之类）也参与二级匹配：用户把系列名配进筛选里
// 不算错配，因为系列目录同样落在"电影/国产"这条链上。
//
// filepath.ToSlash 保证 Windows 上反斜杠也按段切；
// 相对路径和绝对路径都能用，因为只关心段的位置不关心前缀。
func libraryCategorySegments(path, libraryRoot string) []string {
	rel := relativeUnder(path, libraryRoot)
	if rel == "" {
		return nil
	}
	segs := strings.Split(rel, "/")
	// 末段是文件名，不是分类段。
	out := make([]string, 0, len(segs)-1)
	for _, seg := range segs[:len(segs)-1] {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// relativeUnder 返回 path 相对 libraryRoot 的部分（斜杠分隔）。
//
// 三种情况返回空串，调用方据此判定"这个文件不在媒体库里"：
//   - 根为空：配置有问题，锚点都不存在，任何相对位置都是猜的。
//   - 文件不在根下（Emby 索引里可能混着别的库的文件）。
//   - 文件就是根下的根（理论上不会，媒体库根是目录不是文件）。
//
// 用 filepath.Rel 而不是字符串前缀比较：前缀比较会把
// /media/电影 和 /media/电影备份 判成"在里面"，然后按备份目录的
// 名字去筛——这类备份目录在真实环境里非常常见。
func relativeUnder(path, libraryRoot string) string {
	p := filepath.Clean(strings.TrimSpace(path))
	root := filepath.Clean(strings.TrimSpace(libraryRoot))
	if p == "" || root == "" || root == "." {
		return ""
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

// nameSet 把目录名列表变成查找集合。目录名按小写匹配：
// 配置里写 "国产" 而盘上是 "国产 "（带尾空格）或全角字符的情况都出现过，
// 而这一层是**粗筛**，真正的精确判定由 classifyorganize 的分类链路负责。
func nameSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" {
			out[n] = true
		}
	}
	return out
}

// CategoryName 是分类清单项的最小描述。
//
// mediaupgrade 不 import classifyorganize：分类引擎带 settings 与（可能）一个
// 数据库句柄，而洗版模块的测试全是纯文件系统可跑的。适配发生在装配层，
// 见 internal/app/wire_mediaupgrade_category.go。
type CategoryName struct {
	// Name 是分类目录名（一级或二级）。
	Name string
	// Level 是 1|2|3。1 进 PrimaryNames，其余进 SecondaryNames。
	//
	// 三级也进 SecondaryNames：三级目录同样是"某个二级下面的一类片子"，
	// 而用户填筛选框时不会先想"这是几级"——他只想"管国产剧"。
	// 真要严格区分就得让筛选框按级分组，那是另一个取舍。
	Level int
}

// ScopeFromCategories 从分类清单构造筛选范围。
//
// nil 输入返回零值 Scope，也就是"不筛选"：分类引擎没启用或没接进来时，
// 洗版不能因为拿不到分类清单就扫不出任何东西。
func ScopeFromCategories(cats []CategoryName) CategoryScope {
	var scope CategoryScope
	for _, cat := range cats {
		name := strings.TrimSpace(cat.Name)
		if name == "" {
			continue
		}
		if cat.Level == 1 {
			scope.PrimaryNames = append(scope.PrimaryNames, name)
			continue
		}
		scope.SecondaryNames = append(scope.SecondaryNames, name)
	}
	return scope
}

// ScopeFromNames 从规则里填的分类名列表构造筛选范围。
//
// **不分层级**：规则列存的就是目录名数组，用户填"国产"时没告诉我们
// 那是哪一级。全部进 SecondaryNames，因为匹配那一层是跨父级扫所有
// 二三级目录段的（见 CategoryScope.SecondaryNames 的说明）。
// 命中得宽一点是安全的方向：多扫几部片子的代价是多花点时间，
// 漏扫的代价是用户以为洗过了。
func ScopeFromNames(names []string) CategoryScope {
	var scope CategoryScope
	scope.SecondaryNames = NormalizeCategoryNames(names)
	return scope
}

// NormalizeCategoryNames 把分类名列表去重归一（去空白、小写、去空）。
//
// 洗版规则存的是目录名数组，用户可以在两个规则里填同一个分类；
// 去重让统计出来的"适用分类"不会因为重复而变长。
func NormalizeCategoryNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		key := strings.ToLower(strings.TrimSpace(n))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// ---------------------------------------------------------------------------
// 规则列的编解码
// ---------------------------------------------------------------------------

// ParseRuleCategoryScope 解出规则里存的分类名列表。
//
// 刻意做成**永不返回错误**：这一列是用户在 UI 上顺手填的筛选条件，
// 它坏了（手改过库、某个版本的界面存了别的格式）唯一合理的后果是
// "这条规则不按分类筛选"，而不是"整条规则扫不了"。
//
// 与之配套的取舍：DecodeRuleCategoryScope 解析失败时返回 nil
// 而不是部分结果——半截列表会让用户看到"我配的三个分类只认了两个"，
// 而排查一个静默丢项的问题远比忽略它困难。
func ParseRuleCategoryScope(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil
	}
	return NormalizeCategoryNames(names)
}

// EncodeRuleCategoryScope 把分类名列表写成规则列。
//
// 空列表写空串而不是 "[]"：让"没配"在库里、在 API 响应里、在界面上
// 都是同一个空值，省掉一层"空数组和没配到底算不算两种状态"的判断。
func EncodeRuleCategoryScope(names []string) string {
	norm := NormalizeCategoryNames(names)
	if len(norm) == 0 {
		return ""
	}
	raw, err := json.Marshal(norm)
	if err != nil {
		// 元素都是 string，Marshal 不可能失败。真失败就写空串 ——
		// 退化方向是"不过滤"而不是"拦下所有文件"。
		return ""
	}
	return string(raw)
}
