package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"litepan/internal/classifyorganize"
	"litepan/internal/domain"
	"litepan/internal/spacecleanup"
)

// T27 C-8 的装配适配层：把分类引擎的分类清单喂给两个消费者。
//
// 单独一个文件而不是塞进 wire_services.go / wire_http.go，是因为这段代码的价值
// 全在两个语义决定上，放进几百行的装配文件里会被埋掉：
//
//  1. 拿不到清单时**不能**当成"没有分类"。分类引擎可能被配置成关掉、
//     可能读配置失败、也可能压根没接进来（测试里就是最后一种）。
//     这三种都必须退化成"不筛选"（洗版扫全库、清理器不保护），
//     不能退化成"筛掉一切"或"保护一切" —— 后者两个方向都能删掉用户的文件。
//
//  2. 分类根不是一个全局常量。整理任务的目标根存在任务配置的 target_root 里
//     （internal/mediaorganize/task_defaults.go），不同任务可以有不同根，
//     而分类目录是相对那个根逐段 ensureDirAction 拼出来的
//     （internal/mediaorganize/planner/classification.go）。
//     所以必须按任务枚举并把每个根都登记进保护名单 —— 假定只有一个根，
//     后果是另一个任务下的分类目录完全失去保护。
//
// 洗版侧的分类范围不走这里：它是每条规则自己的 category_scope 列
// （见 internal/mediaupgrade/rules.go 的 RuleSet.CategoryNames），
// 在扫描时按该规则行解析。这里只服务清理保护那一个消费者 ——
// 它的保护名单是全局的（清理器扫的是全部整理任务的目标根），
// 没有"按规则"这个维度。

// listClassificationCategories 读分类清单，第二个返回值是"清单可用"。
//
// 分开返回而不是把 err 一起传出去，是因为三个失败点的处理方式完全一样：
// 引擎为 nil、读出错、空清单 —— 都意味着"没有可筛的分类"，
// 而把 err 往上抛会让调用方各自决定怎么处理，然后三处决定不一致。
func listClassificationCategories(ctx context.Context, svc *classifyorganize.Service) ([]classifyorganize.Category, bool) {
	if svc == nil {
		return nil, false
	}
	cats, err := svc.ListActiveCategories(ctx)
	if err != nil || len(cats) == 0 {
		return nil, false
	}
	return cats, true
}

// classifyCategoryPaths 列出分类目录的相对段（"电影"、"电影/国产"…）。
//
// 相对段而不是绝对路径：分类根在不同任务上可能不同（见文件头第 2 点），
// 拼绝对路径要等到知道当前是哪个根，而 CategoryPath.Path 会被
// spacecleanup 按各根分别匹配。
func classifyCategoryPaths(ctx context.Context, svc *classifyorganize.Service) []spacecleanup.CategoryPath {
	cats, ok := listClassificationCategories(ctx, svc)
	if !ok {
		return nil
	}
	out := make([]spacecleanup.CategoryPath, 0, len(cats))
	for _, cat := range cats {
		segment := cat.SegmentPath()
		if segment == "" {
			continue
		}
		out = append(out, spacecleanup.CategoryPath{Path: segment, Level: cat.Level})
	}
	return out
}

// classifyCategoryGuard 构造清理器的分类保护名单。
//
// roots 是所有分类整理任务的目标根。清单取不到、或一个根都取不到时返回 nil
// （= 不保护），理由见文件头第 1 点。
func classifyCategoryGuard(ctx context.Context, svc *classifyorganize.Service, roots []string) *spacecleanup.CategoryGuard {
	paths := classifyCategoryPaths(ctx, svc)
	if len(paths) == 0 {
		return nil
	}
	cleanRoots := cleanTargetRoots(roots)
	if len(cleanRoots) == 0 {
		return nil
	}
	return spacecleanup.NewCategoryGuard(cleanRoots, paths)
}

// cleanTargetRoots 清洗整理任务的目标根：去空白、去重、丢掉相对路径。
//
// 去重不是为了好看：同一个根登记两次会让 Names() 里的分类段重复出现，
// 报告摘要上的数字会虚高，而用户没有任何办法验证那个数字。
func cleanTargetRoots(roots []string) []string {
	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))
	for _, raw := range roots {
		root := strings.TrimSpace(raw)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if root == "." || !filepath.IsAbs(root) {
			// 相对路径根没法和清理目标做 Rel 比较，留着只会让保护静默失效。
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	return out
}

// organizeTaskTargetRoots 从整理任务配置里取出所有 target_root。
//
// 只看 target_root 这一个键，不去解析 target_root_id —— 后者是网盘目录 ID，
// 要变成路径得走一遍驱动解析，而分类目录在盘上是否存在取决于 driver。
// 拿不到路径时的退化是"这个根没被登记"，方向是漏保护而不是误删，
// 因为未登记的根不会被 CategoryGuard 当成分类根。
func organizeTaskTargetRoots(tasks []*domain.MediaOrganizeTask) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if task == nil || len(task.Config) == 0 {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal(task.Config, &cfg); err != nil {
			continue
		}
		if root := classifyCfgString(cfg["target_root"]); root != "" {
			out = append(out, root)
		}
	}
	return out
}

// classifyCfgString 从任务配置里取一个字符串字段。
//
// 配置 JSON 是运行期可编辑的，目标根这种字段可能存成数字或写成 null
// （从别的任务复制过来时很常见）。硬断 type switch 只认 string 会让这个根
// 不被登记 —— 而漏保护的后果是清理器把分类目录报成可删孤儿目录。
func classifyCfgString(v any) string {
	switch got := v.(type) {
	case string:
		return strings.TrimSpace(got)
	case json.Number:
		return strings.TrimSpace(got.String())
	default:
		return ""
	}
}
