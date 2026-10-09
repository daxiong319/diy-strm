package api

import (
	"context"
	"net/http"
	"strings"

	"litepan/internal/automation"
	"litepan/internal/cloudref"
	"litepan/internal/domain"
	"litepan/internal/mediaupgrade"
)

// dirRefSnapshotDTO 回给前端的「这次判断用到了哪些配置」。
//
// 前端只渲染后端算好的文案（见 cloudref 的包注释：文案在前端再拼一份等于
// 把判断复制一次，两端迟早分叉，而分叉的表现是「界面说在媒体库内，
// Emby 里就是没有」）。但把判据一起给出去有两个实际好处：
//   - 用户能看懂提示凭什么这么判（悬停展开），而不是对着一句断言发愣；
//   - 排查「为什么说不在库内」时能看到后端到底读到了什么。
type dirRefSnapshotDTO struct {
	LibraryRoots   []string `json:"library_roots"`
	MonitorSources []string `json:"monitor_sources"`
	EmbyLocations  []string `json:"emby_locations"`
	// Notes 边界说明。只在真的影响到判据时才非空，见 collectDirRefs。
	Notes []string `json:"notes"`
}

// dirRefResp 是 GET /admin/dir-refs 的响应。
type dirRefResp struct {
	// Path 是归一后的路径（`/media/影视/` → `/media/影视`）。
	// 不可用时是空串，与 References 的零值一起表示「这个问题无法回答」。
	Path string `json:"path"`
	// References 就是 cloudref.ResolveDirReferences 的返回值，
	// 直接内嵌（不转 DTO）：这个结构体已经是纯数据 + json tag，
	// 再包一层只会多一处可能忘记同步的映射。
	References cloudref.DirReferences `json:"references"`
	Refs       dirRefSnapshotDTO      `json:"refs"`
	// Configured 标注这次判断有没有用到配置。
	//
	// 为什么要单独给一个布尔：前端要区分「配了、但不匹配」和「什么都没配」。
	// 后者根本没有依据下任何结论（Hints 为空），此时界面必须**什么都不显示**，
	// 显示一句「不在媒体库内」就是凭空捏造，用户会跑去设置里找一个
	// 自己明明配过（或压根不存在）的东西。
	Configured bool `json:"configured"`
}

// dirRefRuleLister 只取 automation 服务的读接口，方便单测替身。
type dirRefRuleLister interface {
	ListRules(ctx context.Context) ([]automation.RuleView, error)
}

// resolveDirRef 处理 `GET /admin/dir-refs?path=/media/影视`。
//
// 只读接口：它不改任何配置，只回答「这个目录被哪些配置覆盖」。
//
// 路径不可用（空、控制字符、多行粘贴）时返回 **200 + 零值结果**而不是 400：
// 「空路径在不在媒体库内」这个问题本身无法回答，报错会让前端弹一个红框，
// 而用户在输入框里按下第一个字符的瞬间就会撞上这个红框。正确做法是安静地
// 不给提示。
func (h *Handler) resolveDirRef(w http.ResponseWriter, r *http.Request) {
	dir := strings.TrimSpace(r.URL.Query().Get("path"))

	refs, notes := h.collectDirRefs(r.Context())
	resp := dirRefResp{
		Path:       cloudref.NormalizePath(dir),
		References: cloudref.ResolveDirReferences(r.Context(), dir, refs),
		Configured: len(refs.LibraryRoots) > 0 || len(refs.MonitorSources) > 0 || len(refs.EmbyLocations) > 0,
	}
	resp.Refs = dirRefSnapshotDTO{
		LibraryRoots:   nonNilStrings(refs.LibraryRoots),
		MonitorSources: nonNilStrings(refs.MonitorSources),
		EmbyLocations:  nonNilStrings(refs.EmbyLocations),
		Notes:          notes,
	}
	writeOK(w, resp)
}

// collectDirRefs 从各子系统读出目录配置的快照。
//
// ⚠️ 这里是**真实配置**，不是硬编码样例，所以每个来源都得自己决定
// 「读不到怎么办」。统一策略：读不到就当没有，降级成「不给提示」，
// 而不是「报不在媒体库内」。理由见 cloudref 包注释：
// 半吊子的假提示比没有提示更糟 —— 用户会照着假提示排错，越排越乱。
//
// 刻意没有返回 error：某个来源读失败不该让整个接口 500，因为剩下的来源
// 仍然能给出**部分**正确的提示。失败以 Notes 的形式如实告诉前端。
func (h *Handler) collectDirRefs(ctx context.Context) (cloudref.DirRefs, []string) {
	refs := cloudref.DirRefs{}
	var notes []string

	// 1) 媒体库根：洗版规则行 + 全局兜底设置。
	if h.mediaUpgrade != nil {
		roots, note := libraryRootsFrom(ctx, h.mediaUpgrade)
		refs.LibraryRoots = roots
		notes = append(notes, note...)
	}

	// 2) 自动整理源目录：触发器规则里的 path。
	if h.automation != nil {
		sources, note := monitorSourcesFrom(h.automation)
		refs.MonitorSources = sources
		notes = append(notes, note...)
	}

	// 3) Emby 媒体库的物理路径：**目前没有任何权威快照可用**。
	//
	// litepan 从不持久化库位置（emby_libraries 表只有 name/library_id/
	// sync_path_id），而 VirtualFolderDto.Locations 只能现场调 Emby 接口拿 ——
	// 那是网络调用，违反 cloudref 的纯函数约束，而且用户每敲一个字符就
	// 打一次 Emby 既慢又可能把 Emby 打挂。
	//
	// 所以这里留空，并且在响应里**明说边界**。用户看到「不在媒体库内」
	// 应当知道这是在说洗版媒体库根，而不是断言 Emby 扫不到那个目录。
	refs.EmbyLocations = nil
	notes = append(notes,
		"媒体库覆盖率只统计洗版的媒体库根目录，不含 Emby/Jellyfin 索引库的物理路径"+
			"（本项目不保存库位置）。所以这里的「不在媒体库内」指的是"+
			"「不在洗版媒体库根内」，不等于「Emby 扫不到这个目录」。")

	notes = append(notes, monitorSourceSemanticsNote)
	return refs, notes
}

// monitorSourceSemanticsNote 说明源目录的覆盖范围（也是本功能最大的盲区）。
var monitorSourceSemanticsNote = "自动整理的源目录只统计自动化规则里「待整理目录（CAS 转存）」与" +
	"「监控目录（离线下载）」两类触发器。整理任务自己的「整理目录」在数据模型里是一个" +
	"网盘目录 ID（source_dir_id），没有可比较的路径，因此不参与覆盖率判断。"

// libraryRootsFrom 读洗版配置里的全部媒体库根。
//
// ⚠️ 必须同时读**规则行**与**全局兜底设置**：界面上改的是规则行
// （MediaUpgradePage 的输入框绑的是 media_upgrade_rules 行），
// 而 `mo_media_upgrade_library_root` 是「没有规则时」的默认值
// （mediaupgrade.LoadRuleSet 在 ruleID==0 时才用它）。只读其中之一的表现
// 是「用户在规则页把库根改完了，提示还按旧的全局值判」——
// 那正是这个功能要消灭的那类静默误导。
//
// ⚠️ library_root 在生产代码里是**单值**：mediaupgrade 的两个读取点
// （rules.go:138 与 service.go:225）都只做 TrimSpace，不按逗号切。
// 所以这里也不切。用 cloudref.SplitRoots 切会把 `/media/我的 影视`
// 从中间劈开（空格是路径的一部分），凭空造出两个不存在的库根，
// 然后理直气壮地报「不在媒体库内」。
// candidate_roots 才是列表值，但它不是库根（那是「去哪儿找候选文件」），
// 不该拿来判定覆盖率。
//
// 读失败不返回 error：读不到全局兜底不影响规则行的判定，只是覆盖面小一点，
// 这种情况在 Notes 里如实说明。
func libraryRootsFrom(ctx context.Context, svc *mediaupgrade.Service) ([]string, []string) {
	var (
		notes []string
		out   = newRootCollector()
	)

	global, err := svc.GlobalRule()
	switch {
	case err != nil:
		notes = append(notes, "读取洗版全局兜底目录失败："+err.Error()+
			"。下面的媒体库覆盖率只覆盖规则里配置的媒体库根。")
	case global != nil:
		out.add(global.LibraryRoot)
	}

	rules, err := svc.ListRules(ctx)
	if err != nil {
		notes = append(notes, "读取洗版规则失败："+err.Error()+
			"。下面的媒体库覆盖率可能不完整。")
		return out.list(), notes
	}
	for i := range rules {
		out.add(rules[i].LibraryRoot)
	}
	return out.list(), notes
}

// monitorSourcesFrom 从自动化触发器规则里抽「自动整理的源目录」。
//
// 参与的触发器只有两类，因为只有它们的 path 字段真的是目录：
//   - cas_autosave（界面叫「待整理目录」）：CAS 转存到该目录即触发整理；
//   - offline_download（界面叫「监控目录」）：离线下载完成到该目录即触发。
//
// ⚠️ **留空的 path 一律不进列表**，理由各不同但后果一样：
// cas_autosave 留空 = 任意目录的转存都触发（见 automation 校验处的注释），
// offline_download 留空则是无效配置。两种情况都算作「源目录」的话，
// 任何路径都判成是源目录，「不是源目录」这条 warn 永远不会出现 ——
// 用户看到的是一个永远亮绿灯的假提示，比没有提示更糟。
//
// 另外只取**未暂停**的规则（ListRules 内部 includePaused=true，
// 这里显式过滤 status）暂停的规则不会触发任何东西，把它算成源目录
// 会让用户以为「我明明配了这条规则」。反过来讲，运行中的规则我们
// 宁可多算一条（多算的后果只是少报一条 warn），所以这里对
// status 的判断是宽松的：只要不是明确的 paused 就收。
func monitorSourcesFrom(lister dirRefRuleLister) ([]string, []string) {
	var notes []string
	rules, err := lister.ListRules(context.Background())
	if err != nil {
		return nil, []string{"读取自动化规则失败：" + err.Error() +
			"。下面的「源目录」判定不可用，相关提示已省略。"}
	}

	out := newRootCollector()
	for _, rule := range rules {
		if rule.Status == domain.AutomationStatusPaused {
			continue
		}
		switch rule.TriggerType {
		case domain.AutomationTriggerCasAutoSave, domain.AutomationTriggerOfflineDownload:
			out.add(anyToString(rule.TriggerConfig["path"]))
		default:
			// webhook 触发器的 path_prefix 是 URL 路径前缀，不是目录；
			// 定时触发器没有目录。判定放在 switch 的 default 里而不是
			// 外面过滤，是为了让「以后新增触发器类型默认不参与」成为默认行为。
		}
	}
	return out.list(), notes
}

// anyToString 把 trigger_config（map[string]any）里的值取成字符串。
//
// 非字符串值返回空串：这些配置都是用户在 JSON 表单里手填的，
// 粘进来一个数字或数组是有可能的，那不是合法目录，
// 按空处理（等于「不参与覆盖率判断」）比 fmt.Sprintf 硬转靠谱。
func anyToString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// rootCollector 收集目录并归一去重，顺序保持首次出现。
type rootCollector struct {
	seen map[string]struct{}
	out  []string
}

func newRootCollector() *rootCollector {
	return &rootCollector{seen: map[string]struct{}{}}
}

// add 加一个配置里读到的原始路径：归一后为空或重复就丢掉。
func (c *rootCollector) add(raw string) {
	norm := cloudref.NormalizePath(raw)
	if norm == "" {
		return
	}
	if _, dup := c.seen[norm]; dup {
		return
	}
	c.seen[norm] = struct{}{}
	c.out = append(c.out, norm)
}

func (c *rootCollector) list() []string {
	if len(c.out) == 0 {
		return nil
	}
	return c.out
}
