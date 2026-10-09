// 「以媒体库为真值」的去重判定。
//
// 为什么要有这一层：litepan 原来是**永久去重** —— 只要订阅历史上有一条
// status='transferred' 的同 scope 记录，这个 scope 就永远不会再被碰。
// 用户在网盘删掉一部剧，订阅不会拉回来；洗版换版本也被挡住。
// 参考实现 的做法是「**以媒体库实际有没有为准**」：用户删了就重转（这正是他想要的），
// 换版本也重转。代价是必须有 3 小时保护期，因为网盘写入有延迟、文件清单可能还没刷新，
// 刚转存完再去动同一批文件容易重复。
//
// 真值从哪来：**emby_media_items**（迁移 internal/store/migrations/0026_emby_index.sql:4）。
// 这是 litepan 唯一始终存在的本地媒体库索引 —— 装了 Emby 就有，没有 Emby 就是空表。
//
//	⚠️ 两个库是同一个文件：internal/app/wire_store.go:42 的
//	store.Open(ctx, store.Options{Path: cfg.DBPath}) 与
//	internal/app/wire_http.go:301 的 ddb.Init(cfg.DBPath, log) 用的是同一个
//	cfg.DBPath（internal/config/config.go:28 = dataDir/litepan.db），
//	所以这里可以直接用 ddb.Db 查主库表，不需要跨连接。
//
//	⚠️ emby_media_items **没有 TMDB ID 列**，只能按「归一化标题 + 年份 + 季/集号」匹配。
//	Emby 的 type 列是原生字符串：Movie / Series / Season / Episode
//	（见 internal/embyindex/helpers_test.go:270-273、internal/embyindex/model.go:62）。
//
// 查不到（表空 / 没开 Emby）时判为「不在」放行 —— 方向正确：用户删了就该重转。
// 反复转存的风险由 3 小时保护期兜住，那本身就是个限流器。
package discovery

import (
	"database/sql"
	"strconv"
	"strings"
	"sync"
	"time"

	"litepan/internal/discover/ddb"
	"litepan/internal/mediaorganize/rules"
)

// 缓存参数照 internal/classifyorganize/service.go:16-17 的既有做法（30 分钟 / 256 条）：
// TMDB detail 缓存用同样的量级。保护期 3h 本身已经限制了查询频率，
// 缓存只是避免一轮订阅里对同一个作品反复查。
const (
	mediaLibraryCacheTTL  = 30 * time.Minute
	mediaLibraryCacheSize = 256
)

type mediaLibraryEntry struct {
	present   bool
	expiresAt time.Time
}

var mediaLibraryCache = struct {
	mu    sync.Mutex
	cache map[string]mediaLibraryEntry
}{cache: map[string]mediaLibraryEntry{}}

// MediaLibraryProbe 查询回调。抽出来是为了让单测能注入假实现，
// 而生产路径默认走 emby_media_items。
var MediaLibraryProbe = probeMediaLibraryFromEmby

// ---------------------------------------------------------------------------
// 查询入口
// ---------------------------------------------------------------------------

// MediaLibraryHit 媒体库命中结果。
type MediaLibraryHit struct {
	// Present 媒体库里有没有
	Present bool
	// Available 索引本身能不能用。false = 查不到真相（表空 / 没开 Emby / 查询报错），
	// 这时 Present 恒为 false，调用方应当**放行**，不要拿它当「确定没有」。
	Available bool
	// ReasonCode 判定依据，写进账本便于事后复盘
	ReasonCode string
	// CacheHit 是不是命中缓存（巡检与调参用）
	CacheHit bool
}

// MediaLibraryPresent 查「媒体库里是不是已经这部作品了」。
//
// 这是三层去重的第二层，也是唯一以「用户网盘的真实状态」为准的一层。
func MediaLibraryPresent(sub *DiscoverySubscription, cand resourceCandidate) MediaLibraryHit {
	scope := ledgerMediaScope(sub, cand)
	return MediaLibraryPresentByScope(sub, scope, cand)
}

// MediaLibraryPresentByScope 按 media_scope 查，命中缓存。
func MediaLibraryPresentByScope(sub *DiscoverySubscription, scope string, cand resourceCandidate) MediaLibraryHit {
	cacheKey := scope + "\x00" + candidateEpisodeToken(cand)

	mediaLibraryCache.mu.Lock()
	if entry, ok := mediaLibraryCache.cache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		mediaLibraryCache.mu.Unlock()
		return MediaLibraryHit{Present: entry.present, Available: true, ReasonCode: ReasonCodePresentInLibrary, CacheHit: true}
	}
	mediaLibraryCache.mu.Unlock()

	hit := MediaLibraryProbe(sub, scope, cand)

	// 索引不可用时不写缓存：用户刚配好 Emby 时应该立刻能看到新条目，
	// 把「查不到」缓存 30 分钟会拖慢用户感知到的修复速度。
	if hit.Available {
		mediaLibraryCache.mu.Lock()
		if len(mediaLibraryCache.cache) >= mediaLibraryCacheSize {
			evictOldestLocked(mediaLibraryCache.cache)
		}
		mediaLibraryCache.cache[cacheKey] = mediaLibraryEntry{present: hit.Present, expiresAt: time.Now().Add(mediaLibraryCacheTTL)}
		mediaLibraryCache.mu.Unlock()
	}
	return hit
}

func evictOldestLocked(cache map[string]mediaLibraryEntry) {
	oldestKey := ""
	var oldest time.Time
	for k, v := range cache {
		if oldestKey == "" || v.expiresAt.Before(oldest) {
			oldestKey, oldest = k, v.expiresAt
		}
	}
	if oldestKey != "" {
		delete(cache, oldestKey)
	}
}

// ResetMediaLibraryCache 清空缓存。测试与「用户刚配好 Emby 后想立刻生效」时用。
func ResetMediaLibraryCache() {
	mediaLibraryCache.mu.Lock()
	defer mediaLibraryCache.mu.Unlock()
	mediaLibraryCache.cache = map[string]mediaLibraryEntry{}
}

// candidateEpisodeToken 给同一 scope 下的不同集号一个独立的缓存槽，
// 否则「第 1 集已有、第 2 集没有」会被第 1 集的查询结果盖住。
// 季集缺失时返回空串：单集电影与整剧都走 scope 本身，槽可以共用。
func candidateEpisodeToken(cand resourceCandidate) string {
	if cand.Episode == nil {
		return ""
	}
	return "s" + intToken(cand.Episode.SeasonNum) + "e" + intToken(cand.Episode.EpisodeNum)
}

func intToken(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

// ---------------------------------------------------------------------------
// emby_media_items 查询
// ---------------------------------------------------------------------------

// probeMediaLibraryFromEmby 默认实现：查 emby_media_items。
func probeMediaLibraryFromEmby(sub *DiscoverySubscription, scope string, cand resourceCandidate) MediaLibraryHit {
	if ddb.Db == nil {
		return MediaLibraryHit{ReasonCode: ReasonCodeLedgerRaceLost}
	}

	title := firstNonEmptyStr(cand.Title, cand.ChannelTitle)
	if sub != nil {
		title = firstNonEmptyStr(sub.Title, sub.OriginalTitle, title)
	}
	year := ledgerCandidateYear(cand)
	mediaType := ""
	if sub != nil {
		mediaType = strings.ToLower(strings.TrimSpace(sub.MediaType))
	}
	season, episode := ledgerCandidateSeasonEpisode(cand)

	switch mediaType {
	case "movie":
		return probeMovie(title, year)
	default:
		// tv / 空类型：season 有值按剧查，没有就按单集查。
		if episode > 0 {
			return probeEpisode(title, season, episode, year)
		}
		return probeSeries(title, year)
	}
}

func probeMovie(title string, year int) MediaLibraryHit {
	// 名称精确匹配最可靠；Emby 的 name 里不含年份，所以靠 production_year 收窄。
	q := `SELECT production_year FROM emby_media_items WHERE type = 'Movie' AND name = ? LIMIT 20`
	rows, err := ddb.Db.Raw(q, title).Rows()
	if err != nil {
		return MediaLibraryHit{ReasonCode: ReasonCodeLedgerRaceLost}
	}
	defer rows.Close()
	found, total := false, 0
	for rows.Next() {
		total++
		var py sql.NullInt64
		if err := rows.Scan(&py); err != nil {
			continue
		}
		if year == 0 || py.Int64 == int64(year) {
			found = true
		}
	}
	if total == 0 {
		return MediaLibraryHit{Available: true}
	}
	return MediaLibraryHit{Present: found, Available: true, ReasonCode: reasonForPresence(found)}
}

func probeSeries(title string, year int) MediaLibraryHit {
	q := `SELECT production_year FROM emby_media_items WHERE type = 'Series' AND series_name = ? LIMIT 20`
	rows, err := ddb.Db.Raw(q, title).Rows()
	if err != nil {
		return MediaLibraryHit{ReasonCode: ReasonCodeLedgerRaceLost}
	}
	defer rows.Close()
	found, total := false, 0
	for rows.Next() {
		total++
		var py sql.NullInt64
		if err := rows.Scan(&py); err != nil {
			continue
		}
		if year == 0 || py.Int64 == int64(year) {
			found = true
		}
	}
	if total == 0 {
		return MediaLibraryHit{Available: true}
	}
	return MediaLibraryHit{Present: found, Available: true, ReasonCode: reasonForPresence(found)}
}

func probeEpisode(title string, season, episode, year int) MediaLibraryHit {
	q := `SELECT series_name, parent_index_number, index_number FROM emby_media_items
	      WHERE type = 'Episode' AND series_name = ? LIMIT 200`
	rows, err := ddb.Db.Raw(q, title).Rows()
	if err != nil {
		return MediaLibraryHit{ReasonCode: ReasonCodeLedgerRaceLost}
	}
	defer rows.Close()
	found, total := false, 0
	for rows.Next() {
		total++
		var name string
		var parentIdx, idx sql.NullInt64
		if err := rows.Scan(&name, &parentIdx, &idx); err != nil {
			continue
		}
		if int(parentIdx.Int64) != season {
			continue
		}
		if int(idx.Int64) == episode {
			found = true
		}
	}
	if total == 0 {
		return MediaLibraryHit{Available: true}
	}
	return MediaLibraryHit{Present: found, Available: true, ReasonCode: reasonForPresence(found)}
}

func reasonForPresence(found bool) string {
	if found {
		return ReasonCodePresentInLibrary
	}
	return ""
}

// ledgerCandidateYear 从候选标题里抽年份。拿不到就是 0 = 不按年份收窄。
func ledgerCandidateYear(cand resourceCandidate) int {
	for _, text := range []string{cand.Title, cand.Remark, cand.ChannelTitle} {
		if parsed := rules.NormalizeParsedMedia(rules.ParseFilenameStrict(text)); parsed.Year != nil && *parsed.Year > 0 {
			return *parsed.Year
		}
	}
	return 0
}

func ledgerCandidateSeasonEpisode(cand resourceCandidate) (season, episode int) {
	if cand.Episode == nil {
		return 0, 0
	}
	season = 1
	if cand.Episode.SeasonNum != nil {
		season = *cand.Episode.SeasonNum
	}
	if cand.Episode.EpisodeNum != nil {
		episode = *cand.Episode.EpisodeNum
	}
	return season, episode
}
