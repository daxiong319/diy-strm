package moviepilot

import (
	"context"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"litepan/internal/discover/mediaparse"
)

// IdentifyResult 单个文件的识别结果（本地侧对应物）。
// 该结构体会直接作为 HTTP 响应体返回给前端，因此显式声明 json tag：
// 沿用仓库内其它结构体的 snake_case 命名，避免依赖 Go 默认的字段名编码
// （否则前端必须按首字母大写的键名取值，后端一加 tag 就会破坏契约）。
type IdentifyResult struct {
	// Category 媒体类别：movie / tv。
	Category string `json:"category"`
	// Title 标题（识别出的，未必是 TMDB 正式名）。
	Title string `json:"title"`
	// Season 季号（剧集）。
	Season int `json:"season"`
	// Episode 集号（剧集）。
	Episode int `json:"episode"`
	// Year 年份。
	Year int `json:"year"`
	// TmdbId TMDB 作品 ID（0 表示未知）。
	TmdbId int64 `json:"tmdb_id"`
	// AiQuality 外部识别补充的质量线索（MoviePilot 识别结果）。
	AiQuality *FileQuality `json:"ai_quality,omitempty"`
}

// leadingIndexRe 分享名的前导批次序号（如「2-遮.天」的「2-」）。
var leadingIndexRe = regexp.MustCompile(`^\d{1,3}[.\-_ ]+\s*`)

// yearFanRe 「年番N」季标记（年番第 N 部 → TMDB 第 N 季）。
var yearFanRe = regexp.MustCompile(`年番\s*(\d{1,2})`)

// cjkDotRe 中文之间的点分隔（分享名的敏感词规避符号，如「遮.天」=「遮天」）。
// 必须在 ParseMedia 之前移除：path.Ext 会把名字里最后一个点当扩展名分隔符，
// 导致 stem 被截断（「遮.天 (2026)」→ stem「遮」，「天 (2026)」被当扩展名丢弃）。
var cjkDotRe = regexp.MustCompile(`([\p{Han}])\.([\p{Han}])`)

// stripCjkDots 删除中文夹点场景的点（循环处理多段；英文/数字/扩展名的点不受影响）。
func stripCjkDots(s string) string {
	for cjkDotRe.MatchString(s) {
		s = cjkDotRe.ReplaceAllString(s, "$1$2")
	}
	return s
}

// startsWithDigit 判断字符串是否以 ASCII 数字开头（批次序号剥离的防误伤护栏）。
func startsWithDigit(s string) bool {
	if s == "" {
		return false
	}
	return s[0] >= '0' && s[0] <= '9'
}

// normalizeAutoDirName 归一化转存目录名/独立文件名：
//  1. 「年番N」提取为季号并从名字移除（「2-遮.天 年番4 (2026)」→「遮.天 (2026)」+ S4）
//  2. 剥前导批次序号「N-」（剥后非空且不以数字开头才采用——避免误伤「2.5D」这类标题）
//  3. 移除中文夹点
//
// 返回归一化后的名字与年番季号（0=非年番）。
func normalizeAutoDirName(name string) (string, int) {
	name = strings.TrimSpace(name)
	fanSeason := 0
	if m := yearFanRe.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			fanSeason = n
		}
		name = strings.TrimSpace(yearFanRe.ReplaceAllString(name, " "))
	}
	if leadingIndexRe.MatchString(name) {
		trimmed := leadingIndexRe.ReplaceAllString(name, "")
		if t := strings.TrimSpace(trimmed); t != "" && !startsWithDigit(t) {
			name = trimmed
		}
	}
	name = stripCjkDots(name)
	return strings.Join(strings.Fields(name), " "), fanSeason
}

// joinRelPath 拼接相对路径段（跳过空段，自动补 /）。
func joinRelPath(parts ...string) string {
	var out []string
	for _, p := range parts {
		p = strings.Trim(p, "/")
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "/")
}

// extractReleaseGroup 从文件名提取发布组（最后一个「-」之后的标签，如 xxx.H.265-Ocat → Ocat）。
func extractReleaseGroup(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if idx := strings.LastIndex(base, "-"); idx > 0 {
		g := strings.TrimSpace(base[idx+1:])
		if g != "" && len(g) <= 32 && !strings.ContainsAny(g, ".0123456789") {
			return g
		}
	}
	return ""
}

// buildMediaFromFile 用文件名与（可选的）所在目录名构造识别结果。
// 目录名常比单一文件名更完整（剧集目录名带标题与年份），故 dirName 非空时优先用于
// 补齐缺失的标题/年份/季号。
func buildMediaFromFile(fileName, dirName string) *IdentifyResult {
	clean := stripCjkDots(fileName)
	category, title, season, episode, year := mediaparse.ParseMedia(clean)
	res := &IdentifyResult{
		Category: category,
		Title:    title,
		Season:   season,
		Episode:  episode,
		Year:     year,
	}
	if res.Category == "unknown" {
		res.Category = ""
	}
	if strings.TrimSpace(dirName) == "" {
		return res
	}
	dName, fanSeason := normalizeAutoDirName(dirName)
	dCat, dTitle, dSeason, dEpisode, dYear := mediaparse.ParseMedia(dName)
	if strings.TrimSpace(res.Title) == "" && strings.TrimSpace(dTitle) != "" {
		res.Title = dTitle
	}
	if res.Category == "" && dCat != "" && dCat != "unknown" {
		res.Category = dCat
	}
	if res.Year <= 0 && dYear > 0 {
		res.Year = dYear
	}
	// 季号优先取目录：「年番N」是比文件名更可靠的季号来源
	if fanSeason > 0 {
		res.Season = fanSeason
	} else if res.Season <= 0 && dSeason > 0 {
		res.Season = dSeason
	}
	if res.Episode <= 0 && dEpisode > 0 {
		res.Episode = dEpisode
	}
	return res
}

// applyMPRecognizeResult 把 MoviePilot 识别结果折算进 IdentifyResult（仅补空缺，不覆盖已有值）。
func applyMPRecognizeResult(res *IdentifyResult, mp *MPRecognizeResult) {
	if res == nil || mp == nil {
		return
	}
	if res.Category == "" {
		res.Category = mp.Category
	}
	if strings.TrimSpace(res.Title) == "" {
		res.Title = mp.Title
	}
	if res.Year <= 0 {
		res.Year = mp.Year
	}
	if res.Season <= 0 {
		res.Season = mp.Season
	}
	if res.Episode <= 0 {
		res.Episode = mp.Episode
	}
	if res.TmdbId <= 0 {
		res.TmdbId = mp.TmdbID
	}
}

// applyMpExtras 用 MoviePilot 识别结果补齐空缺，并按文件名补齐季集。
// 与旧实现一致：只在缺失时填充，避免覆盖文件名解析出的更精确结果。
func applyMpExtras(res *IdentifyResult, mp *MPRecognizeResult, fileName string) {
	applyMPRecognizeResult(res, mp)
	if res.Category == "" {
		res.Category = "movie"
	}
	if res.Category == "tv" {
		if res.Season <= 0 {
			res.Season = 1
		}
		if res.Episode <= 0 {
			res.Episode = 1
		}
		if parsed, ok := mediaparse.ParseEpisode(fileName); ok {
			if parsed.Season > 0 {
				res.Season = parsed.Season
			}
			if parsed.Episode > 0 {
				res.Episode = parsed.Episode
			}
		}
	}
}

// IdentifyLocalFile 识别一个本地文件：先用文件名/目录名解析，
// 缺失标题时回退 MoviePilot 识别接口，最后用 TMDB 多候选匹配校验并取正式名。
// 返回 false 表示无法识别（调用方应记入失败文件）。
func (s *Service) IdentifyLocalFile(ctx context.Context, client *Client, fileName, dirName string) (*IdentifyResult, bool) {
	res := buildMediaFromFile(fileName, dirName)
	if strings.TrimSpace(res.Title) == "" && client != nil {
		// 文件名解析不出标题：交 MoviePilot 识别（其内部有完整的文件名解析链路）
		if mp, ok := client.RecognizeMedia(ctx, fileName); ok {
			applyMPRecognizeResult(res, mp)
			s.log.Info("MoviePilot 识别兜底命中", "file", fileName, "title", mp.Title, "tmdb", mp.TmdbID)
		}
	}
	title := strings.TrimSpace(res.Title)
	if title == "" {
		return res, false
	}
	// 已知 TMDB ID：直接按 ID 取正式名（解决 TMDB 无标题搜索命中的场景）
	if res.TmdbId > 0 {
		if official, year := s.lookupTmdbByID(ctx, res.TmdbId, res.Category); official != "" {
			res.Title = official
			if year > 0 {
				res.Year = year
			}
			return res, true
		}
	}
	mediaType := res.Category
	if mediaType == "" {
		mediaType = "movie"
	}
	cand, err := matchTmdbCandidates(ctx, s.tmdb, title, res.Year, mediaType)
	if err != nil {
		// 剧集/电影判错时换另一类型重试一次（文件名无季集标记但实为剧集的情形常见）
		alt := "tv"
		if mediaType == "tv" {
			alt = "movie"
		}
		if cand2, err2 := matchTmdbCandidates(ctx, s.tmdb, title, res.Year, alt); err2 == nil {
			cand, err, mediaType = cand2, nil, alt
		}
	}
	if err != nil || cand == nil {
		return res, false
	}
	official, tmdbYear := lookupTmdbTitleAndYear(ctx, s.tmdb, cand, mediaType)
	if official == "" {
		official = cand.Name
	}
	res.Category = mediaType
	res.Title = official
	res.TmdbId = cand.ID
	if tmdbYear > 0 {
		res.Year = tmdbYear
	}
	if res.Category == "tv" {
		if res.Season <= 0 {
			res.Season = 1
		}
		if res.Episode <= 0 {
			res.Episode = 1
		}
	}
	return res, true
}

// lookupTmdbByID 按 TMDB ID 直接取正式名与年份。ID 无效或查询失败返回空名。
func (s *Service) lookupTmdbByID(ctx context.Context, tmdbID int64, category string) (string, int) {
	if s.tmdb == nil || tmdbID <= 0 {
		return "", 0
	}
	mediaType := "movie"
	if category == "tv" {
		mediaType = "tv"
	}
	cand := &tmdbCandidate{ID: tmdbID}
	name, year := lookupTmdbTitleAndYear(ctx, s.tmdb, cand, mediaType)
	if name == "" && mediaType == "movie" {
		// 类型判错：换剧集再试一次
		name, year = lookupTmdbTitleAndYear(ctx, s.tmdb, cand, "tv")
	}
	return name, year
}

// EpisodeKeyOfName 从文件名提取集键（SxxExx），用于同名/同集洗版匹配。
func EpisodeKeyOfName(fileName string) string {
	return EpisodeKeyOf(fileName)
}

// sourceRelativePath 计算文件相对待整理根目录的路径，用于日志与历史记录。
func sourceRelativePath(root, absPath string) string {
	root = strings.TrimRight(strings.TrimSpace(root), "/")
	if root == "" {
		return filepath.ToSlash(path.Base(absPath))
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return filepath.ToSlash(path.Base(absPath))
	}
	return filepath.ToSlash(rel)
}
