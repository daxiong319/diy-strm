package discovery

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/models"
	"diy-strm/internal/tmdb"
)

// newSHA1 SHA1 摘要器
func newSHA1() hash.Hash { return sha1.New() }

// ---------------------------------------------------------------------------
// 目录流内部工具（请求/JSON/字符串助手）
// ---------------------------------------------------------------------------

// tmdbClient TMDB 客户端类型别名
type tmdbClient = tmdb.Client

// TMDB 人物类型别名
type tmdbPersonResult = tmdb.PersonSearchResult
type tmdbCreditItem = tmdb.PersonCreditItem

// tmdbGenre TMDB 类型别名
type tmdbGenre = tmdb.Genre

// models_globalTmdbClient TMDB 客户端快捷获取
func models_globalTmdbClient() *tmdb.Client {
	return models.GlobalScrapeSettings.GetTmdbClient()
}

// models_globalTmdbLanguage TMDB 语言快捷获取
func models_globalTmdbLanguage() string {
	return models.GlobalScrapeSettings.GetTmdbLanguage()
}

// jsonUnmarshal json.Unmarshal 别名
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// httpTimeoutClient 带 UA 的简易 HTTP 客户端
type httpTimeoutClient struct {
	timeout time.Duration
	client  *http.Client
	once    sync.Once
}

func (c *httpTimeoutClient) lazy() *http.Client {
	c.once.Do(func() {
		c.client = &http.Client{Timeout: c.timeout}
	})
	return c.client
}

// Get 带 UA/Referer 的 GET（返回 body 字节）
func (c *httpTimeoutClient) Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", maoyanUserAgent)
	req.Header.Set("Referer", "https://www.maoyan.com/")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := c.lazy().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := ioReadAllLimit(resp.Body, 4<<20)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// ioReadAllLimit 限量读取
func ioReadAllLimit(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}

// maoyanRequestHeat 请求猫眼热度接口并用容错解析提取「标题+热度」条目
func maoyanRequestHeat(ctx context.Context, endpoint string) ([]maoyanHeatEntry, error) {
	body, err := maoyanHTTP.Get(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("请求猫眼接口失败：%v", err)
	}
	var root any
	if err := jsonUnmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("猫眼响应解析失败：%v", err)
	}
	entries := scanHeatObjects(root, 0)
	if len(entries) == 0 {
		return nil, fmt.Errorf("猫眼响应中未识别到榜单数据")
	}
	sortEntriesByHeat(entries)
	for i := range entries {
		if entries[i].Rank == 0 {
			entries[i].Rank = i + 1
		}
	}
	if len(entries) > 30 {
		entries = entries[:30]
	}
	return entries, nil
}

// scanHeatObjects 递归扫描 JSON 树，收集「标题+热度」对象（对上游字段形状变化有韧性）
func scanHeatObjects(node any, depth int) []maoyanHeatEntry {
	const maxDepth = 8
	if depth > maxDepth {
		return nil
	}
	switch v := node.(type) {
	case map[string]any:
		title := firstJSONString(v, "title", "name", "subjectName", "showName", "movieName", "tvName", "videoName")
		heat := firstJSONNumber(v, "heat", "heatValue", "realtimeHeat", "value", "webHeat", "heatTotal", "playCount")
		if title != "" && heat > 0 {
			year := int(firstJSONNumber(v, "year", "releaseYear", "pubYear", "releaseDateYear"))
			if year < 1900 || year > 2100 {
				year = parseYearFromString(firstJSONString(v, "releaseDate", "publishDate", "pubDate"))
			}
			return []maoyanHeatEntry{{Title: title, Heat: heat, Year: year}}
		}
		out := []maoyanHeatEntry{}
		// 优先进入 list/items/data 子键，其余按 map 顺序
		for _, key := range []string{"list", "items", "data", "rows", "records", "rankList", "result"} {
			if child, ok := v[key]; ok {
				out = append(out, scanHeatObjects(child, depth+1)...)
			}
		}
		if len(out) > 0 {
			return out
		}
		for _, child := range v {
			out = append(out, scanHeatObjects(child, depth+1)...)
		}
		return out
	case []any:
		out := []maoyanHeatEntry{}
		for _, child := range v {
			out = append(out, scanHeatObjects(child, depth+1)...)
		}
		return out
	default:
		return nil
	}
}

func firstJSONString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstJSONNumber(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return v
		case string:
			if n, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(v), ",", ""), 64); err == nil {
				return n
			}
		}
	}
	return 0
}

// parseYearFromString 从日期串提取年份
func parseYearFromString(s string) int {
	if len(s) >= 4 {
		if y, err := strconv.Atoi(s[:4]); err == nil && y >= 1900 && y <= 2100 {
			return y
		}
	}
	return 0
}

// timeNowPtr 当前时间指针
func timeNowPtr() *time.Time {
	now := time.Now()
	return &now
}

// dbUpdateSubjectTMDB 按主键写入 TMDB 匹配结果
func dbUpdateSubjectTMDB(id uint, tmdbID int64, score float64, matchedAt *time.Time) error {
	return db.Db.Model(&DiscoverySubjectCache{}).Where("id = ?", id).Updates(map[string]any{
		"tmdb_id": tmdbID, "match_score": score, "matched_at": matchedAt,
	}).Error
}

// marshalJSON 紧凑 JSON 序列化（失败返回空串）
func marshalJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

// httpNewRequestWithContext 构造带 body 的请求
func httpNewRequestWithContext(ctx context.Context, method, url string, body []byte) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if body == nil {
		return http.NewRequestWithContext(ctx, method, url, nil)
	}
	return http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
}

// strconvAtoi 宽松整数解析（失败返回 0）
func strconvAtoi(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}

// searchSubjectTMDB 按标题(+原题)+年份搜索 TMDB 并选最佳候选。
// 候选评分：标题完全一致 +3 / 前缀包含 +1.5 / 年份一致 +1 / 年份 ±1 +0.5；
// 达到 1.5 分才认为匹配。
func searchSubjectTMDB(client *tmdbClient, language, mediaType, title, originalTitle string, year int) (int64, float64) {
	if client == nil || strings.TrimSpace(title) == "" {
		return 0, 0
	}
	const minScore = 1.5
	bestID, bestScore := int64(0), 0.0
	tryCandidates := func(results []searchSubjectCandidate) {
		for _, r := range results {
			score := scoreSubjectCandidate(r.title, r.originalTitle, r.year, title, originalTitle, year)
			if score > bestScore {
				bestScore = score
				bestID = r.id
			}
		}
	}
	for _, kw := range dedupSubjectKeywords(title, originalTitle) {
		if mediaType == "tv" {
			resp, err := client.SearchTv(kw, year, language, true)
			if err == nil {
				cands := make([]searchSubjectCandidate, 0, len(resp.Results))
				for _, v := range resp.Results {
					cands = append(cands, searchSubjectCandidate{id: v.ID, title: v.Name, originalTitle: v.OriginalName, year: parseYear(v.FirstAirDate)})
				}
				tryCandidates(cands)
			}
		} else {
			resp, err := client.SearchMovie(kw, year, language, true, true)
			if err == nil {
				cands := make([]searchSubjectCandidate, 0, len(resp.Results))
				for _, v := range resp.Results {
					cands = append(cands, searchSubjectCandidate{id: v.ID, title: v.Title, originalTitle: v.OriginalTitle, year: parseYear(v.ReleaseDate)})
				}
				tryCandidates(cands)
			}
		}
		if bestScore >= minScore {
			return bestID, bestScore
		}
	}
	if bestScore >= minScore {
		return bestID, bestScore
	}
	return 0, 0
}

type searchSubjectCandidate struct {
	id           int64
	title        string
	originalTitle string
	year         int
}

// dedupSubjectKeywords 搜索关键词（主标题 + 原题，去重、限 2 个）
func dedupSubjectKeywords(title, originalTitle string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, kw := range []string{title, originalTitle} {
		kw = strings.TrimSpace(kw)
		if kw == "" || seen[strings.ToLower(kw)] {
			continue
		}
		seen[strings.ToLower(kw)] = true
		out = append(out, kw)
		if len(out) >= 2 {
			break
		}
	}
	if len(out) == 0 {
		out = append(out, title)
	}
	return out
}

func scoreSubjectCandidate(candTitle, candOriginal string, candYear int, title, originalTitle string, year int) float64 {
	score := 0.0
	candTitle = normalizeMatchText(candTitle)
	candOriginal = normalizeMatchText(candOriginal)
	wantTitle := normalizeMatchText(title)
	wantOriginal := normalizeMatchText(originalTitle)
	if wantTitle != "" && (candTitle == wantTitle || candOriginal == wantTitle) {
		score += 3
	} else if wantTitle != "" && (strings.Contains(candTitle, wantTitle) || strings.Contains(wantTitle, candTitle) && candTitle != "") {
		score += 1.5
	}
	if wantOriginal != "" && wantOriginal != wantTitle && (candOriginal == wantOriginal || candTitle == wantOriginal) {
		score += 2
	}
	if year > 0 && candYear > 0 {
		switch {
		case candYear == year:
			score += 1
		case absInt(candYear-year) == 1:
			score += 0.5
		}
	}
	return score
}

func normalizeMatchText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacer := strings.NewReplacer(" ", "", "：", ":", "·", "", "-", "", "_", "", "　", "", "第", "", "季", "")
	return replacer.Replace(s)
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ExternalTMDBMatch 外部手动匹配入口（详情页「TMDB 匹配结果」面板重试）
func ExternalTMDBMatch(source, externalID, mediaType, title, originalTitle string, year int) (int64, error) {
	client := models.GlobalScrapeSettings.GetTmdbClient()
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	id, score := matchSubjectTMDB(client, language, mediaType, title, originalTitle, year)
	if id <= 0 {
		return 0, fmt.Errorf("未找到匹配的 TMDB 条目：%s", title)
	}
	if err := UpdateSubjectCacheTMDB(normalizeEntityKey(source, mediaType, externalID), id, title, score); err != nil {
		return id, err
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// 锁与通用小件
// ---------------------------------------------------------------------------

var keyLocks sync.Map

// lockForKey 按键取互斥锁（同订阅串行等）
func lockForKey(key string) *sync.Mutex {
	if v, ok := keyLocks.Load(key); ok {
		return v.(*sync.Mutex)
	}
	m := &sync.Mutex{}
	actual, _ := keyLocks.LoadOrStore(key, m)
	return actual.(*sync.Mutex)
}
