package rules

import (
	"regexp"
	"strings"
)

// T06 · 季集证据抽取 + 全角归一化（参考实现 media_parser 的 Go 对应物）。
//
// 抽成公共函数而不是留在订阅包里，是因为三处都要用：
//   - 订阅候选的季集证据（internal/discover/discovery/subscriptions.go）
//   - 身份校验（internal/discover/identity，媒体类型判定依赖季集）
//   - 洗版（内部接口 mediaorganize 的升级扫描按季分组）
// 三处各写一套正则的结果一定是三处慢慢分叉 —— 这次的起因就是：
// 整理侧的解析器早就支持中文数字，而订阅侧的正则不支持，
// 于是中文剧名的候选被判成「电影、无集号」，同轮去重直接把后续集数全挡掉。
//
// 词表/字面量的来源见本文件底部「推测值」说明。

// cnNumberChars 中文数字字符集，与 helpers.go 的 ChineseNumberToInt 保持一致。
const cnNumberChars = "零〇一二两三四五六七八九十百"

// SeasonEpisode 是从任意文本（标题 / 备注 / 目录名）里抽出来的季集证据。
//
// 为什么和 ParsedMedia 分开：ParsedMedia 要求能给出**片名**，
// 而订阅侧只关心「这条资源属于第几季第几集」—— 片名在那条链路里根本不需要，
// 硬套 ParsedMedia 会把「猜不出片名」和「不是剧集」混为一谈。
type SeasonEpisode struct {
	// MediaType "tv" 命中任一季集证据；"movie" 什么都没命中。
	// 与订阅侧 resourceCandidate.MediaType 用同一组取值。
	MediaType  string
	Season     *int
	Episode    *int
	EndEpisode *int
	// TotalEpisodes 来自「全 N 集」/「更新至 N 集」这类整季标记。
	TotalEpisodes *int
	// Complete 命中「全 N 集」/「更新至 N 集」（整季已出齐）。
	Complete bool
	// Updated 命中「更新中」/「连载中」，即资源站还在持续更新这一季。
	Updated bool
}

// IsTV 是否判定为剧集。
func (s SeasonEpisode) IsTV() bool { return s.MediaType == "tv" }

const (
	// cnNumber 匹配一段中文数字（含百，与 ChineseNumberToInt 的取值域一致）。
	cnNumber = "[" + cnNumberChars + "]{1,6}"
	// arNumber 匹配阿拉伯数字。季号上限取 2 位、后续沿用原正则的 1~3 位，
	// 不放宽 —— 放宽会把「2024」「2160」这类数字误当集号。
	arNumberS = `\d{1,2}`
	arNumberE = `\d{1,3}`
)

var (
	// seRangeRe 原有写法，**只加宽区间分隔符**：S01E01 / S01E01-E05 / S1E2。
	// 末尾多出的 －～ 是全角连字符与全角波浪号。中文站点的区间写法两种都常见，
	// 而全角归一刻意不碰它们（见 NormalizeFullWidth），所以只能在这里认。
	seRangeRe = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,3})(?:[-~－～]\s*E?(\d{1,3}))?`)

	// seCNRangeRe 中文季集：第二季 第十二集 / 第 2 季 第 12 集 / 第十季.E03。
	// 季号与集号之间允许空格与 . ． _ · - 作分隔符：中文资源名的分隔符极不统一，
	// 「剧名.第十季.E03.mkv」这种点分隔的写法很常见，只允许 \s* 会把集号丢掉
	// ——表现是识别成「第 10 季」而不是「第 10 季第 3 集」，很隐蔽。
	// 分隔符集合刻意不含中文标点：「第二季。第三季」不应该被判成 S2E3。
	seCNRangeRe = regexp.MustCompile(
		`第\s*(` + cnNumber + `|` + arNumberS + `)\s*[季部部]` +
			`(?:[\s.．_·\-－～]*第\s*(` + cnNumber + `|\d{1,3})\s*[集话話回期]` +
			`|[\s.．_·\-－～]*[Ee]\s*(` + cnNumber + `|\d{1,3}))?`)

	// seCNEpisodeRe 中文集号（不带季号）：第二十四集 / 第 24 集。
	// 站点把单集资源单独挂出来时就是这个形态，不认它的话这类候选会被判成电影 ——
	// 而 candidateScopeKey 对「movie」只允许每轮转存一条，于是同一部剧的
	// E01…E24 会被自己的去重挡掉只剩第一条（这正是 T06 要修的实际故障）。
	seCNEpisodeRe = regexp.MustCompile(`第\s*(` + cnNumber + `|\d{1,3})\s*[集话話回期]`)

	// seTotalRe 整季标记：全24集 / 全 24 集 / 更新至24集 / 连载中。
	// 「更新中」单独一条，因为它不带数字。
	seTotalRe = regexp.MustCompile(`(?:全|更新至|更新到|完结至)\s*(` + cnNumber + `|\d{1,4})\s*[集话話回期]`)
	seLiveRe  = regexp.MustCompile(`更新中|连载中|更新中$`)
	// seCNSeasonRe 中文季号（无集号）：第二季 / 第 12 季 / 第三部。
	seCNSeasonRe = regexp.MustCompile(`第\s*(` + cnNumber + `|` + arNumberS + `)\s*[季部]`)
	// seSeasonOnlyRe 英文季号（无集号），与订阅侧原 seasonOnlyRe 逐字一致。
	seSeasonOnlyRe = regexp.MustCompile(`(?i)(?:第\s*(` + arNumberS + `)\s*季|S(\d{1,2})\b)`)
)

// SeasonEpisodeFromText 从一段文本里抽季集证据。
//
// 优先级是**严格超集**于订阅侧原来的两条正则：原来能匹配的输入，这里结果完全一样；
// 原来匹配不到的（中文数字、全角、「全 N 集」）这里才多认。
// 顺序也有讲究 —— 集号优先于季号，先判 S01E01 再判 S01，
// 否则「Show.S01.2160p」会先撞上季号分支把后面的集号丢掉。
//
// 识别不了就返回 MediaType="movie" 的零值，绝不猜：猜错的季集会污染
// 订阅的去重 scope 和身份校验的季号维度，比不识别更糟。
func SeasonEpisodeFromText(parts ...string) SeasonEpisode {
	text := NormalizeFullWidth(strings.Join(nonEmptyParts(parts), " "))
	if strings.TrimSpace(text) == "" {
		return SeasonEpisode{MediaType: "movie"}
	}

	// 1) S01E01 / S01E01-E05 —— 与改动前完全一致的最强证据。
	if m := seRangeRe.FindStringSubmatch(text); m != nil {
		season := parseEpisodeInt(m[1], 1)
		episode := parseEpisodeInt(m[2], 0)
		out := SeasonEpisode{MediaType: "tv", Season: &season, Episode: &episode}
		if m[3] != "" {
			if end := parseEpisodeInt(m[3], episode); end > episode {
				out.EndEpisode = &end
			}
		}
		return out
	}

	// 2) 第二季 第十二集 —— 中文数字与阿拉伯数字混写都吃。
	if m := seCNRangeRe.FindStringSubmatch(text); m != nil {
		season := parseEpisodeInt(m[1], 1)
		out := SeasonEpisode{MediaType: "tv", Season: &season}
		raw := firstNonEmptyText(m[2], m[3])
		if raw != "" {
			episode := parseEpisodeInt(raw, 0)
			out.Episode = &episode
		}
		applyTotalMarker(&out, text)
		return out
	}

	// 3) 「全 24 集」这类整季包：剧集，但集号未知。放在季号分支之前，
	//    否则「三体 第三季 全24集」会被 4) 判成只有季号、丢掉总集数。
	if m := seTotalRe.FindStringSubmatch(text); m != nil {
		total := parseEpisodeInt(m[1], 0)
		out := SeasonEpisode{MediaType: "tv", TotalEpisodes: &total, Complete: true}
		if s := seCNSeasonRe.FindStringSubmatch(text); s != nil {
			season := parseEpisodeInt(s[1], 1)
			out.Season = &season
		} else if s := seSeasonOnlyRe.FindStringSubmatch(text); s != nil {
			season := parseEpisodeInt(firstNonEmptyText(s[1], s[2]), 1)
			out.Season = &season
		} else if s := seRangeRe.FindStringSubmatch(text); s != nil {
			// 到这里说明有 SxxExx，但 1) 没匹配上（集号超 3 位之类的畸形写法），
			// 仍然把季号捡回来。
			season := parseEpisodeInt(s[1], 1)
			out.Season = &season
		}
		return out
	}

	// 4) 只有集号：第二十四集 / 第 24 集。必须排在季号分支前面 ——
	//    「第二十四集」里没有季号，但季号分支也认不出它，两个分支互不干扰，
	//    放在这里是为了让「集号」的语义先于「季号」。
	if m := seCNEpisodeRe.FindStringSubmatch(text); m != nil {
		episode := parseEpisodeInt(m[1], 0)
		out := SeasonEpisode{MediaType: "tv", Episode: &episode}
		if s := seCNSeasonRe.FindStringSubmatch(text); s != nil {
			season := parseEpisodeInt(s[1], 1)
			out.Season = &season
		}
		applyTotalMarker(&out, text)
		return out
	}

	// 5) 只有季号：第二季 / 第 12 季 / S01 / Season 1。
	if m := seCNSeasonRe.FindStringSubmatch(text); m != nil {
		season := parseEpisodeInt(m[1], 1)
		out := SeasonEpisode{MediaType: "tv", Season: &season}
		applyTotalMarker(&out, text)
		return out
	}
	if m := seSeasonOnlyRe.FindStringSubmatch(text); m != nil {
		season := parseEpisodeInt(firstNonEmptyText(m[1], m[2]), 1)
		out := SeasonEpisode{MediaType: "tv", Season: &season}
		applyTotalMarker(&out, text)
		return out
	}

	// 6) 「更新中」没有任何数字，但仍说明这是剧集。
	if seLiveRe.MatchString(text) {
		return SeasonEpisode{MediaType: "tv", Updated: true}
	}

	return SeasonEpisode{MediaType: "movie"}
}

// applyTotalMarker 把整季标记（更新至 N 集 / 更新中）补到已有的季证据上。
func applyTotalMarker(out *SeasonEpisode, text string) {
	if m := seTotalRe.FindStringSubmatch(text); m != nil {
		total := parseEpisodeInt(m[1], 0)
		out.TotalEpisodes = &total
		out.Complete = true
		return
	}
	if seLiveRe.MatchString(text) {
		out.Updated = true
	}
}

// parseEpisodeInt 解析季/集号：阿拉伯数字走 strconv，中文数字走 ChineseNumberToInt。
// 两边都失败才回落到 def。
func parseEpisodeInt(raw string, def int) int {
	text := strings.TrimSpace(raw)
	if text == "" {
		return def
	}
	if n, err := parseInt(text); err == nil {
		return n
	}
	if n := ChineseNumberToInt(text); n != nil {
		return *n
	}
	return def
}

// ---------------------------------------------------------------------------
// 全角归一化
// ---------------------------------------------------------------------------

// NormalizeFullWidth 全角 → 半角，**只折叠会破坏正则匹配的字符**。
//
// 只映射两类：
//  1. 全角字母与数字（Ａ–Ｚ ａ–ｚ ０–９）——「Ｓ０１Ｅ０１」这类写法在所有
//     \d / [A-Za-z] 正则下都失配，这是本函数存在的主要理由。
//  2. 全角空格 U+3000 —— 它在文件名里的角色是分隔符但不在 CJK 码位段里，
//     不换的话「剧名　Ｓ０１Ｅ０１」会粘成一个整词。空白不是标题内容，换掉无损失。
//
// **刻意不映射全角标点**：！？＃＆（）～ － 等（U+FF01–FF5E 的非字母数字部分）
// 在中文片名里是标题的一部分，仓内既有测试就钉住了它 ——「特别篇 吹响吧！上低音号～合奏比赛～」
// （internal/mediaorganize/rules/rules_behavior_test.go:404）。换成半角会改掉整理侧的
// 片名输出与分组键，而那是 00-master 明令禁止在本系列里动的「标题归一化 / 分组键」。
// 我第一版映射整个 U+FF01–FF5E，当场打挂 parse_test.go 的「孤独摇滚！」
// 「吹响吧！上低音号」两个既有用例。
//
// 需要用全角连字符/波浪号写集数区间的（「Ｓ０１Ｅ０１－Ｅ０５」），改由正则的
// 分隔符字符类直接认（seRangeRe / seCNRangeRe 里带了 －～），不靠折叠。
//
// 归一化放在 ParseFilenameStrict / ParseDirName 的**入口**，
// 只影响本次解析的输入，不会回写库里任何字段。
func NormalizeFullWidth(s string) string {
	if !strings.ContainsFunc(s, needsFullWidthFold) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\u3000':
			b.WriteRune(' ')
		case isFullWidthAlnum(r):
			// Ａ-Ｚ → A-Z，ａ-ｚ → a-z，０-９ → 0-9。
			// 三段码位各差 0x10 / 0x20，减去同一个 base 统一落到半角。
			b.WriteRune(r - fullWidthAlnumBase)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// fullWidthAlnumBase = 0xFF01 - '!'，把 Ａ(FF21)-Ｚ(FF3A) 映射到 A-Z。
const fullWidthAlnumBase = 0xFF01 - '!'

// isFullWidthAlnum 只认全角字母与全角数字两段码位：
// U+FF10–FF19 ０-９、U+FF21–FF3A Ａ-Ｚ、U+FF41–FF5A ａ-ｚ。
// 中间的 U+FF01–FF0F、U+FF1A–FF20、U+FF3B–FF40、U+FF5B–FF5E 是全角标点，
// 一律不碰。
func isFullWidthAlnum(r rune) bool {
	switch {
	case r >= '\uFF10' && r <= '\uFF19':
		return true
	case r >= '\uFF21' && r <= '\uFF3A':
		return true
	case r >= '\uFF41' && r <= '\uFF5A':
		return true
	}
	return false
}

// needsFullWidthFold 是 ContainsFunc 的快路径谓词：整串没这几个字符时直接原样返回。
func needsFullWidthFold(r rune) bool {
	return r == '\u3000' || isFullWidthAlnum(r)
}

func nonEmptyParts(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmptyText(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 推测值说明（任务书要求区分「逆向确认」与「推测」）
//
// 逆向确认（参考实现 官网 docs，逐字可查）：
//   - 执行强度四档的候选尝试数与间隔区间：保守 1/45-65s、均衡 2/25-40s、激进 3/10-18s
//   - 完结宽限期默认 7 天
//   - 时段窗口「结束早于开始表示跨午夜」「时段外到点那轮直接跳过不补跑」
//   - SearchConnector 分享码正则 ^[0-9A-Za-z]{1,16}$（T05 已用）
//
// 推测（本文件无法从 .so 或 docs 取到字面量，以下按语义补齐）：
//   - cnNumber 的取值域：docs 只给了中文数字的用法，字符集按
//     helpers.go 里 ChineseNumberToInt 已有的「零〇一二两三四五六七八九十百」取，
//     没有额外加「廿卅」等罕见写法。
//   - seCNRangeRe / seTotalRe / seLiveRe 的具体分隔符组合：docs 没有样本，
//     按中文资源站常见的「第N季」「全N集」「更新至N集」「更新中」四种写法补。
//   - seRangeRe / seSeasonOnlyRe 是**沿用 litepan 改动前的原文**，不是移植值。
//
// 上面三处「推测」都做成了可配置词表 / 独立正则，见 internal/mediaorganize/rules/lexicon.go
// 与 mo_media_parse_res_aliases；线上碰到没覆盖的写法时改配置即可，不用改代码。
