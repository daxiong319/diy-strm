package spacecleanup

import (
	"path/filepath"
	"strings"
)

// 分类目录保护（gap-analysis §1.1 C-8 的消费者二：目录监控清理保护）。
//
// 这一段全部价值在一个反向命题上：分类目录里的东西**不该**被清理器当成垃圾。
// 一级目录「电影」下面没有任何 STRM 任务，里面全是按「剧名/文件」组织的真实媒体，
// 现有保护一条都认不出它 —— 已有的保护全是「某个任务正在用的路径」
// （UploadActivePaths / OfflineActivePaths / BackupTempScan），而分类目录的身份
// 来自规则表而不是任务。少了这段，清理器会把整棵分类树报成「未关联 STRM 目录」，
// 而这类目录恰好是用户最不敢让它删的那种。
//
// 形状照抄 cleanupUploadTemp 的双闸：扫描期跳过（不出现在报告里），
// 清理期再拒一次（挡住「扫描之后、点确认之前改了分类配置」这一种）。
// 只做扫描期那一道不够：报告已经摊在屏幕上了，用户此时把分类模板改了，
// 清理时那份名单已经是新的，而报告里那条「未关联目录」还在。
//
// 语义边界（C-8 任务书点名要求交代的）：名单来自**规则**而不是磁盘。
// 配过的分类一定保护 —— 即使那个目录现在还不存在；没配过的目录不享受保护 ——
// 即使磁盘上碰巧有同名目录。所以 /strm/备份/电影 在规则没有「电影」一级分类时
// 照样按孤儿目录处理，/strm/电影 在规则配了但目录还没建出来时照样受保护。
// 这正是 ListActiveCategories 返回「规则里有哪些分类」的后果；
// 消费者若误以为它说的是「库里有哪些分类」，就会在这两种情况下保护错。
// 钉住这层语义的是 internal/classifyorganize/catalog_test.go 的
// TestListActiveCategoriesDescribesRulesNotLibrary。
//
// 匹配用的是「相对分类根的前缀段」而不是「目录名」：目录名会在任何一层出现
// （/strm/电影 命中，/strm/备份/电影 也命中），前缀段只认「从根往下走的那几段」。

// CategoryPath 是分类目录的最小描述。
//
// 这里用本地结构体而不是直接引用 classifyorganize.Category，是为了不让
// 清理器为了几个目录名把分类引擎拖进来 —— 分类引擎带 settings 和数据库句柄，
// 而清理器在测试里是纯文件系统可跑的。适配发生在装配层。
type CategoryPath struct {
	// Path 是相对分类根的分类目录段，例如「电影」「电影/国产」「电影/国产/2019」。
	// 一条分类会保护它自己的整棵子树，也会保护它的所有祖先段（配了「电影/国产」
	// 而没配「电影」时，/strm/电影 仍要受保护，否则那条二级分类在磁盘上根本走不到），
	// 所以判定只看相对根的第一段。
	Path string
	// Level 是 1|2|3。它不参与判定 —— 判定粒度就是段的位置 ——
	// 留着是因为装配层要按 level 过滤（关掉二级后表里那些行不该继续保护），
	// 而把过滤放在调用方比塞在这里更直白。
	Level int
}

// CategoryGuard 是分类保护名单。
//
// prefix 集合用「相对根的前 k 段拼成的路径」做键，而不是目录名集合：
// 键里带了从根开始的顺序，/strm/下载/电影 的第一段是「下载」而不是「电影」，
// 于是不会因为深层同名而被误判。
type CategoryGuard struct {
	roots  []string
	prefix map[string]struct{}
}

// NewCategoryGuard 用分类根列表 + 分类目录段构造保护名单。
//
// roots 为空或没有任何分类时返回 nil —— 分类没启用、或规则表里一条分类都没有，
// 两种情况都不该让清理器开始删目录。返回 nil 而不是空 guard，是为了让
// 调用方少写一处判空：nil 上调 Protected 会返回 false，语义正好是「不保护」。
//
// 一个分类会保护它自己的整棵子树，也会保护它的所有祖先段：
// 配了「电影/国产」而没配「电影」时，/strm/电影 仍要受保护，
// 否则那条二级分类在磁盘上根本走不到。
func NewCategoryGuard(roots []string, categories []CategoryPath) *CategoryGuard {
	guard := &CategoryGuard{prefix: map[string]struct{}{}}
	for _, raw := range roots {
		root := filepath.Clean(strings.TrimSpace(raw))
		if root == "" || root == "." {
			continue
		}
		guard.roots = append(guard.roots, root)
	}
	for _, cat := range categories {
		segments := splitCategoryPath(cat.Path)
		if len(segments) == 0 {
			continue
		}
		for i := range segments {
			key := filepath.Join(segments[:i+1]...)
			guard.prefix[key] = struct{}{}
		}
	}
	if len(guard.roots) == 0 || len(guard.prefix) == 0 {
		return nil
	}
	return guard
}

// splitCategoryPath 把「电影/国产」切成段，顺手丢掉空段 ——
// 配置里写成「电影//国产」不该让前缀永远匹配不上真实路径。
func splitCategoryPath(path string) []string {
	raw := strings.Split(filepath.ToSlash(strings.TrimSpace(path)), "/")
	out := make([]string, 0, len(raw))
	for _, segment := range raw {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		out = append(out, segment)
	}
	return out
}

// Protected 判断一个清理目标是否落在分类目录里。
//
// 路径必须位于某个分类根之下，且**相对根的第一段**整段命中名单。
// 等价于「这棵树的第一层就是某个分类目录」，一级/二级/三级都算。
//
// 刻意不接受「路径里任意一段命中」：那样 /strm/下载/电影 会因为含「电影」
// 而被保护，可它其实是下载目录下的同名子目录，用户没配这个分类，
// 它应该按孤儿目录处理。
func (g *CategoryGuard) Protected(path string) bool {
	if g == nil || len(g.roots) == 0 || len(g.prefix) == 0 {
		return false
	}
	target := filepath.Clean(strings.TrimSpace(path))
	if target == "" || target == "." {
		return false
	}
	for _, root := range g.roots {
		rel := relativeUnder(root, target)
		if rel == "" {
			continue
		}
		segments := splitCategoryPath(rel)
		if len(segments) == 0 {
			continue
		}
		if _, ok := g.prefix[segments[0]]; ok {
			return true
		}
	}
	return false
}

// relativeUnder 返回 target 相对 root 的路径；不在 root 之下时返回空串。
// 用 filepath.Rel 而不是字符串前缀 —— 前缀会把 /media/电影 和 /media/电影备份
// 判成「在里面」，而备份目录在真实部署里非常常见。
func relativeUnder(root, target string) string {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

// Names 返回受保护的分类段名（含各级前缀）。报告里说明「因为分类目录被跳过」
// 时用它，让用户知道清理器看见了那些目录、只是不动它们，而不是漏扫了。
func (g *CategoryGuard) Names() []string {
	if g == nil {
		return nil
	}
	out := make([]string, 0, len(g.prefix))
	for key := range g.prefix {
		out = append(out, key)
	}
	return out
}

// Len 返回受保护的分类段数量，供报告摘要。
func (g *CategoryGuard) Len() int {
	if g == nil {
		return 0
	}
	return len(g.prefix)
}

// categoryGuard 取当前分类保护名单。三种情况都返回 nil 上层行为：
// 注入函数没给（分类没接）、函数返回 nil（规则表里没有分类）、
// 规则读取出错时装配层也返回 nil。判据一律走 nil 接收者，
// 所以上面三处调用点不需要各自判空。
func (s *Service) categoryGuard() *CategoryGuard {
	if s == nil || s.opts.CategoryProtection == nil {
		return nil
	}
	return s.opts.CategoryProtection()
}
