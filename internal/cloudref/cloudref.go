// Package cloudref 解析「一个目录在 litepan 配置里被谁引用了」。
//
// 存在的理由：litepan 的目录配置散落在四五个子系统里，而配错目录的表现
// 是**静默失败** —— 文件确实落盘了、整理任务也确实跑完了，但用户看不到：
//
//  1. 整理目标不在媒体库内 → 文件落在一个 Emby/Vyo 不扫描的目录，界面里永远不出现；
//  2. 转存目标不是自动整理的源目录 → 转存成功了，但没有任何规则会因它触发整理，
//     文件堆在网盘里没人管。
//
// 两种失败都不报错，日志里也看不出来，用户只能自己猜。这个包把「这个目录
// 被哪些配置覆盖」这件事变成一个纯函数调用，好让配置页在用户敲路径的时候
// 就把话说清楚。
//
// 三个刻意的设计约束：
//
//   - **纯函数**：只读调用方传进来的配置切片，不落库、不碰网盘、不发网络请求。
//     所以同一个函数既能跑在 HTTP 请求里（带真实配置），也能跑在单测里（带字面量）。
//   - **文案在后端算**：Hint 由这里生成，前端只渲染。前端自己拼文案等于把
//     「在不在媒体库内」的判断复制一份，两端迟早分叉，而分叉的表现是
//     「界面说在媒体库内，Emby 里就是没有」。
//   - **不硬造概念**：没有权威配置的概念就**不出提示**，而不是编一个开关出来。
//     半吊子的假提示比没有提示更糟 —— 用户会照着假提示排错，越排越乱。
package cloudref

import (
	"context"
	"path"
	"strings"
)

// HintTone 提示的严重程度。前端只按它选颜色，不自己判断轻重。
type HintTone string

const (
	// ToneOK 说明「配置是对的」，中性提示。
	ToneOK HintTone = "ok"
	// ToneWarn 说明「这里会静默失败」，需要用户去改配置。
	ToneWarn HintTone = "warn"
)

// DirRefs 是一个目录可能被引用的几处配置的快照。
//
// 三个字段对应三套独立的配置来源，**没有任何一套是全局唯一的**，所以它们
// 各自成切片而不是单值 —— 媒体库根可以是多条，自动整理源目录可以有多条规则，
// Emby 物理路径按库逐个给。
type DirRefs struct {
	// LibraryRoots 媒体库根目录。
	//
	// 来源：`settings.KeyMOMediaUpgradeLibraryRoot`（全局）与
	// `media_upgrade_rules.library_root`（按规则行）。注意这个值**取决于扫描源**：
	// `mo_media_upgrade_source=local` 时它是本地路径，=`emby`/`jellyfin` 时
	// 它记的是网盘路径（见 registry 里那条说明）。
	LibraryRoots []string
	// MonitorSources 自动整理的源目录。
	//
	// 来源：`automation_rules.trigger_config["path"]`，触发器类型为
	// `cas_autosave`（界面上叫「待整理目录」）与 `offline_download`（叫「监控目录」）。
	// 两者的目录语义完全一致，`matchCasAutoSave` 与离线下载触发器共用同一条前缀匹配规则。
	//
	// ⚠️ **留空的 path 不进这个列表**。`cas_autosave` 的目录留空表示
	// 「任意目录的转存都触发」（见 service_validate.go 的注释），把它算作源目录
	// 会让任何一个目录都判成「是源目录」，提示就永远不会触发 —— 那是假提示。
	MonitorSources []string
	// EmbyLocations Emby 媒体库的物理路径。
	//
	// 目前**没有任何调用方会填它**：litepan 从不持久化 Emby 的库位置
	// （`emby_libraries` 只有 name/library_id/sync_path_id），而
	// `VirtualFolderDto.Locations` 只能现场调 Emby 接口拿 —— 那违反纯函数约束，
	// 而且用户每敲一个字符就打一次 Emby 也不现实。
	// 字段先留着：等哪天有了权威快照，这里直接生效，函数语义不用改。
	EmbyLocations []string
}

// DirHint 一条提示。Text 是可以直接展示的中文，决策全在后端。
type DirHint struct {
	Text string   `json:"text"`
	Tone HintTone `json:"tone"`
}

// DirReferences 一个目录的引用解析结果。
type DirReferences struct {
	// InLibrary 命中的媒体库根是否覆盖了这个目录（根本身或其任意子目录）。
	InLibrary bool
	// LibraryName 命中的那个媒体库根对应的名字。
	//
	// 多个媒体库根同时覆盖时取**最长**的那个：它才是这个目录实际所在的那个库
	// （`/media/影视` 与 `/media/影视/4K` 同时配置、目录是 `/media/影视/4K/某剧` 时，
	// 短的那个只是碰巧覆盖，报短名会让用户去错的地方核对）。
	LibraryName string
	// IsMonitorSource 这个目录是否落在某个自动整理源目录内（含其子目录）。
	IsMonitorSource bool
	// Hint 主提示：优先给会静默失败的那条。Hints 为空时它是空串。
	Hint string
	// Tone 主提示的严重程度，与 Hint 配对。
	Tone HintTone
	// Hints 全部命中的提示，按「会静默失败的排前面」排序。
	Hints []DirHint
}

// 文案表。
//
// 「这个目录不在任何媒体库内」只在**确实配了媒体库根**时才出。没配媒体库根时
// 我们并不知道有没有媒体库，这时报「不在任何媒体库内」是编造 ——
// 界面照着这句话去核对，用户会以为自己漏配了什么。
const (
	hintNotInLibrary  = "这个目录不在任何媒体库内，整理后不会自动出现在媒体库"
	hintInLibrary     = "这个目录在媒体库「%s」内"
	hintNotMonitor    = "这个目录不是自动整理的源目录，转存后不会自动整理"
	hintIsMonitor     = "这个目录是自动整理的源目录"
	hintUnusablePath  = "目录路径为空或无法解析"
	libraryNameSingle = "媒体库"
)

// ResolveDirReferences 解析一个目录的引用情况。纯函数：只读 refs。
//
// ctx 目前用不上：这是纯函数，不落库、不碰网盘、不发网络请求（见包注释）。
// 签名里保留 ctx 是为了让将来真的需要读权威快照时不必改调用方
// （单测里传 context.Background() 即可）。
func ResolveDirReferences(ctx context.Context, dirPath string, refs DirRefs) DirReferences {
	_ = ctx

	target := NormalizePath(dirPath)
	if target == "" {
		// 路径本身不可用时一条提示都不给：此时「在不在媒体库内」这个问题
		// 根本无法提问，给一句「不在任何媒体库内」纯属误导。
		return DirReferences{}
	}

	// 先把配置归一再决定出不出提示。用原始切片的长度做判断是不够的：
	// `mo_media_upgrade_library_root` 存的是**文本框内容**，用户只要在框里
	// 留一个空格，库里就是 `[" "]` —— 长度 1 但归一后是空。此时报一句
	// 「不在任何媒体库内」是凭空捏造的，用户会跑去设置里找一个自己明明配了的东西。
	roots := cleanRoots(refs.LibraryRoots)
	sources := cleanRoots(refs.MonitorSources)
	locations := cleanRoots(refs.EmbyLocations)

	libraryName, inLibrary := matchLibrary(target, roots, locations)
	monitored := len(sources) > 0 && matchAny(target, sources)

	out := DirReferences{
		InLibrary:       inLibrary,
		LibraryName:     libraryName,
		IsMonitorSource: monitored,
	}

	// 每一条提示都带一个前提：前提不成立（没配媒体库根 / 没配源目录）
	// 就整条不出现，而不是出现一条改写过的版本。
	if len(roots) > 0 || len(locations) > 0 {
		if inLibrary {
			out.Hints = append(out.Hints, DirHint{
				Text: strings.Replace(hintInLibrary, "%s", libraryName, 1),
				Tone: ToneOK,
			})
		} else {
			out.Hints = append(out.Hints, DirHint{Text: hintNotInLibrary, Tone: ToneWarn})
		}
	}
	if len(sources) > 0 {
		if monitored {
			out.Hints = append(out.Hints, DirHint{Text: hintIsMonitor, Tone: ToneOK})
		} else {
			out.Hints = append(out.Hints, DirHint{Text: hintNotMonitor, Tone: ToneWarn})
		}
	}

	// 主提示取第一条「会静默失败」的。上面 append 的顺序是「媒体库在前」，
	// 而媒体库那条恰恰常常是 ok —— 那时真正要命的是源目录那条，
	// 埋在下面等于没提示。
	for _, h := range out.Hints {
		if h.Tone == ToneWarn {
			out.Hint, out.Tone = h.Text, h.Tone
			return out
		}
	}
	if len(out.Hints) > 0 {
		out.Hint, out.Tone = out.Hints[0].Text, out.Hints[0].Tone
	}
	return out
}

// matchLibrary 在媒体库根与 Emby 物理路径里找命中的那个，返回名字。
//
// 媒体库根没有自带名字（它是一个裸路径设置），所以给一个通用名；
// Emby 物理路径同样只有路径，名字要等真的有权威快照时才有意义 —— 现在
// 调用方不会填这个字段，填了也只是多一个可匹配的根。
//
// 命中多个时取最长根：见 DirReferences.LibraryName 的注释。
func matchLibrary(target string, libraryRoots, embyLocations []string) (string, bool) {
	name := ""
	best := -1
	consider := func(root string) {
		n := len(root)
		if n > best && contains(target, root) {
			best, name = n, libraryNameSingle
		}
	}
	for _, root := range libraryRoots {
		consider(NormalizePath(root))
	}
	for _, loc := range embyLocations {
		consider(NormalizePath(loc))
	}
	return name, best >= 0
}

// matchAny 判断 target 是否等于或落在 roots 里的某一个之下。
// roots 应当已经过 cleanRoots 归一；这里再归一次是为了让这个函数
// 单独被调用时也不会踩到未归一的输入。
func matchAny(target string, roots []string) bool {
	for _, root := range roots {
		if contains(target, NormalizePath(root)) {
			return true
		}
	}
	return false
}

// cleanRoots 归一并去重一组配置里的目录，丢掉归一后为空的项。
//
// 这一步是提示的**前提**：配置里有一条空白不等于配了媒体库。见
// ResolveDirReferences 里对 `[" "]` 场景的说明。
func cleanRoots(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		norm := NormalizePath(item)
		if norm == "" {
			continue
		}
		if _, dup := seen[norm]; dup {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out
}

// contains 判断 target 是否在 root 内（含 root 自身）。
//
// 这里刻意**不用** strings.HasPrefix：前缀比较会把 `/media/电影` 和
// `/media/电影备份` 判成同一个（`电影` 是 `电影备份` 的字符串前缀），
// 于是备份目录被当成在媒体库内，用户把「洗版」搬到备份目录之后界面还一直说
// 在库里，删文件之前就不会有人察觉。仓库里 internal/mediaupgrade 的
// relativeUnder、internal/spacecleanup 的 pathWithin 都是为这件事改用
// filepath.Rel 的。
func contains(target, root string) bool {
	if target == "" || root == "" {
		return false
	}
	if root == "/" {
		return true
	}
	return target == root || strings.HasPrefix(target, root+"/")
}

// NormalizePath 把用户敲进来的目录归一成「首斜杠、无尾斜杠、无 . / .. 」的形式。
//
// 处理项：
//   - 首尾空白；
//   - 缺前导斜杠（用户在输入框里敲 `影视/待整理` 是常态）；
//   - 尾斜杠（`/media/影视/` 与 `/media/影视` 必须等价）；
//   - `.` 与 `..`：走 path.Clean，因此 `/media/影视/../..` 归一成 `/`。
//     这是安全的 —— Clean 只会把 `..` 折成上一级，永远不会折出根外，
//     所以不存在「配 `/media/影视`、输入 `/media/影视/../../影视2` 却命中」的情况。
//
// 返回空串表示这个路径不可用（空、只有斜杠之外的全空白、控制字符等）。
func NormalizePath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if hasControlChar(trimmed) {
		return ""
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "" || cleaned == "." {
		return ""
	}
	if !strings.HasPrefix(cleaned, "/") {
		// path.Clean 对相对输入可能留下 `..` 开头的结果；加过前导斜杠后
		// 理论上不会出现，留这道闸是因为它是唯一能挡住目录逃逸的地方，
		// 而这道闸一旦被后人改错，症状是「整理写到媒体库外面去了」。
		return ""
	}
	return cleaned
}

// hasControlChar 判断是否含控制字符（含换行与制表符：路径里出现它们
// 基本是粘贴多行文本的结果，后续任何按行处理的逻辑都会因此错乱）。
func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// rootListSeps 目录列表的切分符。
//
// 与 mediaupgrade.rootListSeps 的差别只有一个：**不含半角空格**。
// 空格在目录名里是合法的（`/media/我的 影视`），而库根这个设置在生产代码里
// 是当单值用的（mediaupgrade 两个读取点都只做 TrimSpace），
// 按空格切会把一个合法路径切成两个不存在的根，然后理直气壮地报「不在媒体库内」。
// 逗号/顿号/分号/换行在设置说明里被明确写成列表分隔符，这几个照切。
const rootListSeps = "，,、;；\n\r"

// SplitRoots 把一个可能含多值的目录设置切成一组根目录。
//
// 每项都过 NormalizePath，空项与重复项丢弃。
func SplitRoots(raw string) []string {
	items := strings.FieldsFunc(raw, func(r rune) bool {
		return strings.ContainsRune(rootListSeps, r)
	})
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		norm := NormalizePath(item)
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out
}
