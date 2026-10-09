package rules

import (
	"strings"
	"sync"
)

// T06 · 可配置解析词表（分辨率别名 / HDR / DV / SDR 识别文本）。
//
// 为什么要做成可配置：参考实现 的 media_parser 里这几张表**不可读取** ——
// so_modules 开了 CYTHON_COMPRESS_STRINGS，反编译只能拿到符号名
// （RES_ALIASES / HDR_TEXT / DV_TEXT / SDR_TEXT），拿不到任何字面量。
// 也就是说这一段的取值全是**推测**：按主流资源站的命名习惯补齐，
// 线上碰到没覆盖的写法时应当改配置，而不是改代码发版。
//
// 默认值见下方常量，均为推测值。装配时由调用方把 settings 的配置项注入
// （SetParseLexicon），rules 包自己不 import settings —— 否则形成包依赖环。
//
// 与仓库里已有的 lexicon.go（MediaExtensions / KnownReleaseGroups 等常量表）
// 分开放：那个文件是既有代码，本次不动它。

// 推测值：分辨率别名 → 归一化分辨率。
// 覆盖到的写法：4K/UHD/超高清 → 2160p、2K → 1440p、FHD/FullHD/蓝光 → 1080p 等。
var defaultResAliases = map[string]string{
	"4320p": "4320p", "8k": "4320p",
	"2160p": "2160p", "4k": "2160p", "uhd": "2160p", "超高清": "2160p",
	"1440p": "1440p", "2k": "1440p",
	"1080p": "1080p", "fhd": "1080p", "fullhd": "1080p", "1080i": "1080p", "1080P": "1080p", "蓝光": "1080p",
	"720p": "720p", "hd": "720p", "高清": "720p",
	"576p": "576p", "540p": "540p",
	"480p": "480p", "sd": "480p", "标清": "480p",
	"360p": "360p", "240p": "240p",
}

// 推测值：HDR / DV / SDR 识别文本。
var (
	defaultHDRTexts = []string{"hdr", "hdr10", "hdr10+", "hlg"}
	defaultDVTexts  = []string{"dolby vision", "dovi", "dolbyvision", "dv", "杜比视界"}
	defaultSDRTexts = []string{"sdr", "standard dynamic range"}
)

// ParseLexicon 是一份解析词表快照。零值不可用，请用 DefaultParseLexicon。
type ParseLexicon struct {
	ResAliases map[string]string
	HDRTexts   []string
	DVTexts    []string
	SDRTexts   []string
}

var (
	parseLexiconMu  sync.RWMutex
	activeLexicon   = DefaultParseLexicon()
	lexiconRevision = 0
)

// DefaultParseLexicon 返回内置默认词表（推测值）。
func DefaultParseLexicon() ParseLexicon {
	return ParseLexicon{
		ResAliases: copyStringMap(defaultResAliases),
		HDRTexts:   append([]string(nil), defaultHDRTexts...),
		DVTexts:    append([]string(nil), defaultDVTexts...),
		SDRTexts:   append([]string(nil), defaultSDRTexts...),
	}
}

// LexiconRevision 返回词表版本号，每次 SetParseLexicon 递增，供缓存失效判断。
func LexiconRevision() int {
	parseLexiconMu.RLock()
	defer parseLexiconMu.RUnlock()
	return lexiconRevision
}

// ActiveParseLexicon 返回当前生效的词表副本；修改副本不影响全局。
func ActiveParseLexicon() ParseLexicon {
	parseLexiconMu.RLock()
	defer parseLexiconMu.RUnlock()
	return activeLexicon
}

// SetParseLexicon 用配置项覆盖词表。四个参数为空时对应部分保持默认。
//
// 分辨率别名是**追加**而不是替换：用户只想补一个写法时，
// 不应该连带把内置的 4K/UHD 之类覆盖掉。
// 解析失败时返回错误且**不改全局** —— 一条写错的配置如果把解析器搞坏，
// 影响的是所有媒体入库，必须让调用方把它报出来而不是静默降级。
func SetParseLexicon(resAliasRaw, hdrRaw, dvRaw, sdrRaw string) error {
	lex := DefaultParseLexicon()

	if strings.TrimSpace(resAliasRaw) != "" {
		aliases, err := ParseResAliases(resAliasRaw)
		if err != nil {
			return err
		}
		for k, v := range aliases {
			lex.ResAliases[k] = v
		}
	}

	lex.HDRTexts = mergeLexiconTexts(defaultHDRTexts, splitLexiconRaw(hdrRaw))
	lex.DVTexts = mergeLexiconTexts(defaultDVTexts, splitLexiconRaw(dvRaw))
	lex.SDRTexts = mergeLexiconTexts(defaultSDRTexts, splitLexiconRaw(sdrRaw))

	parseLexiconMu.Lock()
	activeLexicon = lex
	lexiconRevision++
	parseLexiconMu.Unlock()
	return nil
}

// ParseResAliases 解析分辨率别名配置。
// 格式：`别名=标准值`，多个用逗号、分号、竖线或换行分隔。例如：
//
//	超高清=2160p,蓝光原盘=2160p,BD=1080p
func ParseResAliases(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, item := range splitLexiconRaw(raw) {
		key, val, ok := strings.Cut(item, "=")
		if !ok {
			return nil, &LexiconError{Field: "mo_media_parse_res_aliases", Value: item,
				Reason: "分辨率别名必须写成 别名=标准值"}
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key == "" || val == "" {
			return nil, &LexiconError{Field: "mo_media_parse_res_aliases", Value: item,
				Reason: "等号两侧都不能为空"}
		}
		out[key] = val
	}
	return out, nil
}

// LexiconError 词表配置错误。
type LexiconError struct {
	Field  string
	Value  string
	Reason string
}

func (e *LexiconError) Error() string {
	return "词表配置 " + e.Field + " 中 " + quoteLexiconValue(e.Value) + " 无效：" + e.Reason
}

func quoteLexiconValue(v string) string {
	if len(v) > 40 {
		v = v[:40] + "…"
	}
	return "「" + v + "」"
}

// splitLexiconRaw 按逗号/分号/竖线/换行切分配置串（含全角逗号分号）。
func splitLexiconRaw(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '，', ';', '；', '\n', '\r', '|', '｜':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// mergeLexiconTexts 把配置项追加到默认表后面（归一化后去重）。
func mergeLexiconTexts(base, extra []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(base)+len(extra))
	for _, list := range [][]string{base, extra} {
		for _, t := range list {
			t = strings.TrimSpace(t)
			key := NormalizeLexiconText(t)
			if key == "" {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}

// NormalizeLexiconText 归一化识别文本：分隔符（. _ - + / 与全角空格）统一成单空格并转小写。
// 「Dolby.Vision」「DOLBY_VISION」「Dolby Vision」必须能互相匹配 —— 这三种写法资源站都常见。
func NormalizeLexiconText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.TrimSpace(s) {
		switch r {
		case '.', '_', '-', '+', '\u3000', '/', '\\', '|':
			b.WriteByte(' ')
		default:
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// MatchesLexiconText 判断文本里是否命中词表中的任一识别文本。
//
// 匹配口径按**条目语种**分两种，不是按词数：
//
//   - 纯 ASCII 条目按**词边界**匹配（token 相等）。旧实现一律用
//     strings.Contains，"DVDRip" 归一化成 "dvdrip" 会被 "dv" 命中 ——
//     于是一批 DVD 片源被误判成杜比视界，而 dv / sdr 两条特效闸门都建在它上面。
//     词边界匹配下 "dvdrip" 是完整一词，与 "dv" 不相等，判不出 DV。
//   - 含中日韩字符、或含空格的条目按**子串**匹配。前者是因为中文没有词边界
//     （"锐目" 在「某片 锐目版」里是完整词，在「锐目HDR版」里只是词的一部分，
//     强制 token 相等的话，用户自己配的中文词条几乎必然不生效，表现为
//     「配了没用」而界面上看不出哪里错）；后者是因为归一化已把 . _ - 统一成空格，
//     "dolby vision" 这种短语必须能整体命中，不能拆成两个独立词。
//     中文子串误判的代价也小得多：「锐目」这种词本来就是用户指着某部片子配的。
func MatchesLexiconText(text string, terms []string) bool {
	norm := NormalizeLexiconText(text)
	if norm == "" {
		return false
	}
	tokens := strings.Split(norm, " ")
	for _, term := range terms {
		key := NormalizeLexiconText(term)
		if key == "" {
			continue
		}
		if containsCJK(key) || strings.Contains(key, " ") {
			// CJK 条目、或多词 ASCII 条目（如 "dolby vision"、"standard dynamic range"）
			// 都按子串匹配。归一化已经把 . _ - 统一成空格，所以
			// 「Dolby.Vision」「DOLBY_VISION」「Dolby Vision」三种写法都会命中。
			if strings.Contains(norm, key) {
				return true
			}
			continue
		}
		for _, tok := range tokens {
			if tok == key {
				return true
			}
		}
	}
	return false
}

// containsCJK 判断条目里是否含中日韩字符（含 CJK 标点与全角）。
func containsCJK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x2E80 && r <= 0x9FFF: // 部首扩展 + CJK 统一表意文字
			return true
		case r >= 0x3000 && r <= 0x303F: // CJK 标点
			return true
		case r >= 0xFF00 && r <= 0xFF65: // 全角字母数字与标点
			return true
		}
	}
	return false
}

// HasHDRText / HasDVText / HasSDRText 是基于当前生效词表的识别入口。
func HasHDRText(text string) bool { return MatchesLexiconText(text, ActiveParseLexicon().HDRTexts) }
func HasDVText(text string) bool  { return MatchesLexiconText(text, ActiveParseLexicon().DVTexts) }
func HasSDRText(text string) bool { return MatchesLexiconText(text, ActiveParseLexicon().SDRTexts) }

// NormalizeResolutionToken 按词表把一个分辨率 token 归一成标准写法。
// 识别不出返回 ok=false，调用方应保持原值而不是猜。
func NormalizeResolutionToken(token string) (string, bool) {
	lex := ActiveParseLexicon()
	if v, ok := lex.ResAliases[token]; ok {
		return v, true
	}
	key := NormalizeLexiconText(token)
	if key == "" {
		return "", false
	}
	if v, ok := lex.ResAliases[key]; ok {
		return v, true
	}
	for alias, v := range lex.ResAliases {
		if NormalizeLexiconText(alias) == key {
			return v, true
		}
	}
	return "", false
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
