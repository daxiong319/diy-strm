package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/douban"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 影视探索目录流：豆瓣 / AniList / Bangumi 目录浏览。
// 架构对齐参考实现「后台目录预抓 + TMDB 匹配 + 目录状态」：
//   - 豆瓣（有反爬）走后台 Worker 分页预抓入库，前端请求最多等待 wait 秒；
//   - AniList / Bangumi（无反爬）请求时直取并落库，TMDB 匹配由后台异步补齐；
//   - 目录条目统一存 discovery_subject_cache（含 TMDB 匹配结果）。
// ---------------------------------------------------------------------------

// DiscoveryCatalogState 目录抓取状态（catalog_key 唯一）
type DiscoveryCatalogState struct {
	CatalogKey    string     `gorm:"primaryKey;size:128" json:"catalog_key"`
	Source        string     `gorm:"size:16;index" json:"source"`
	Status        string     `gorm:"size:16" json:"status"` // pending/fetching/ready/stale/error
	TotalItems    int        `json:"total_items"`
	FetchedCount  int        `json:"fetched_count"`
	Stale         bool       `json:"stale"`
	LastError     string     `gorm:"size:512" json:"last_error,omitempty"`
	LastFetchedAt *time.Time `json:"last_fetched_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (DiscoveryCatalogState) TableName() string { return "discovery_catalog_states" }

// DiscoveryExternalCache 外部 API 统一 KV 缓存（对齐参考实现 media_external_cache）
type DiscoveryExternalCache struct {
	CacheKey  string    `gorm:"primaryKey;size:190" json:"cache_key"`
	ValueJSON string    `gorm:"type:text" json:"value_json"`
	ExpiresAt time.Time `json:"expires_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (DiscoveryExternalCache) TableName() string { return "discovery_external_cache" }

// externalCacheGet 读取未过期的外部缓存（命中返回 JSON 原文）
func externalCacheGet(key string) string {
	var row DiscoveryExternalCache
	if err := db.Db.Where("cache_key = ?", key).First(&row).Error; err != nil {
		return ""
	}
	if time.Now().After(row.ExpiresAt) {
		db.Db.Delete(&row)
		return ""
	}
	return row.ValueJSON
}

// externalCacheSet 写外部缓存
func externalCacheSet(key string, value any, ttl time.Duration) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	row := DiscoveryExternalCache{CacheKey: key, ValueJSON: string(raw), ExpiresAt: time.Now().Add(ttl), UpdatedAt: time.Now()}
	db.Db.Save(&row)
}

// CatalogPage 目录源分页响应（在 PageResult 上补充目录状态字段）
type CatalogPage struct {
	Items            []Item `json:"items"`
	Page             int    `json:"page"`
	TotalPages       int    `json:"total_pages"`
	TotalItems       int    `json:"total_items"`
	HasNextPage      bool   `json:"has_next_page"`
	CatalogStatus    string `json:"catalog_status"`     // pending/partial/ready/stale/error
	PageState        string `json:"page_state"`         // pending/partial/matching/ready
	PendingCount     int    `json:"pending_count"`      // 当前展示页还差多少条
	CachedItemCount  int    `json:"cached_item_count"`  // 目录已缓存条数
	IsStale          bool   `json:"is_stale"`
	SourceExhausted  bool   `json:"source_exhausted"` // 目录已抓到底
	CatalogTotal     int    `json:"catalog_total"`
	FallbackSource   string `json:"fallback_source,omitempty"` // 上游故障时自动回退的数据源
	MatchedCount     int    `json:"matched_count"` // 已匹配 TMDB 的条数
}

// ---------------------------------------------------------------------------
// 豆瓣目录（分类/排序选项 + rexxar recommend 预抓）
// ---------------------------------------------------------------------------

// DoubanCategoryTags 豆瓣分类 tag（对齐参考实现豆瓣探索的分类行）
var DoubanCategoryTags = map[string][]string{
	"movie": {"热门", "华语", "欧美", "韩国", "日本", "动画", "纪录片", "经典", "文艺", "喜剧", "爱情", "科幻", "悬疑", "惊悚", "恐怖", "奇幻", "治愈", "黑色幽默"},
	"tv":    {"热门", "国产剧", "美剧", "英剧", "日剧", "韩剧", "港剧", "台剧", "泰剧", "动画", "纪录片", "综艺"},
}

// DoubanSortOptions 豆瓣排序（推荐/最新/高分 → rexxar sort T/S/R）
var DoubanSortOptions = []map[string]string{
	{"key": "T", "label": "推荐"},
	{"key": "S", "label": "最新"},
	{"key": "R", "label": "高分"},
}

const doubanCatalogPageSize = 24

// doubanCatalogKey 目录键 douban:v1:{type}:{tag}:{sort}
func doubanCatalogKey(mediaType, tag, sort string) string {
	return fmt.Sprintf("douban:v1:%s:%s:%s", mediaType, tag, sort)
}

// doubanRecommendFetch 拉取一页豆瓣推荐目录。
// rexxar recommend 匿名态会忽略 selected_categories（2026-09 实测固定返回同一
// feed），故改用匿名可用的 j/search_subjects（tag 体系与筛选行一致，稳定有效）。
func doubanRecommendFetch(mediaType, tag, sort string, start, count int) ([]douban.RecommendItem, int, error) {
	client := douban.NewClient()
	subjects, err := client.GetSubjects(mediaType, tag, start, count)
	if err != nil {
		return nil, 0, err
	}
	items := make([]douban.RecommendItem, 0, len(subjects))
	for _, sub := range subjects {
		rate, _ := strconv.ParseFloat(strings.TrimSpace(sub.Rate), 64)
		items = append(items, douban.RecommendItem{
			ID:    sub.ID,
			Title: sub.Title,
			Rating: struct {
				Value float64 `json:"value"`
			}{Value: rate},
			Cover: struct {
				URL string `json:"url"`
			}{URL: sub.Cover},
			Subtype: sub.Subtype,
		})
	}
	return items, count, nil
}

// fetchDoubanCatalogPage 抓一页并合并入 discovery_subject_cache（追加式，按 external_id 去重）
func fetchDoubanCatalogPage(catalogKey, mediaType, tag, sort string) (added int, total int, err error) {
	var state DiscoveryCatalogState
	if err := db.Db.Where("catalog_key = ?", catalogKey).First(&state).Error; err != nil {
		state = DiscoveryCatalogState{CatalogKey: catalogKey, Source: "douban", Status: "fetching"}
		db.Db.Create(&state)
	}
	db.Db.Model(&state).Updates(map[string]any{"status": "fetching", "updated_at": time.Now()})

	start := state.FetchedCount
	items, total, err := doubanRecommendFetch(mediaType, tag, sort, start, doubanCatalogPageSize)
	if err != nil {
		db.Db.Model(&state).Updates(map[string]any{
			"status": ternaryStr(state.FetchedCount > 0, "stale", "error"),
			"stale":  state.FetchedCount > 0,
			"last_error": truncateStr(err.Error(), 500),
			"updated_at": time.Now(),
		})
		return 0, state.FetchedCount, err
	}
	now := time.Now()
	for _, raw := range items {
		if raw.ID == "" {
			continue
		}
		var exist DiscoverySubjectCache
		if err := db.Db.Where("catalog_key = ? AND source = ? AND external_id = ?", catalogKey, "douban", raw.ID).First(&exist).Error; err == nil {
			continue // 已存在（豆瓣翻页可能重叠）
		}
		row := DiscoverySubjectCache{
			CatalogKey:  catalogKey,
			Source:      "douban",
			ExternalID:  raw.ID,
			Title:       raw.Title,
			MediaType:   mediaType,
			Poster:      raw.Cover.URL,
			Rating:      raw.Rating.Value,
			ReleaseDate: raw.Year,
			Payload:     marshalJSON(raw),
		}
		if raw.Year != "" && len(raw.Year) >= 4 {
			row.ReleaseDate = raw.Year
		}
		db.Db.Create(&row)
		added++
	}
	newCount := state.FetchedCount + added
	updates := map[string]any{
		"fetched_count":   newCount,
		"last_fetched_at": &now,
		"updated_at":      now,
		"last_error":      "",
	}
	if total > 0 {
		updates["total_items"] = total
	}
	if start == 0 && len(items) == 0 {
		// 首页 0 条：豆瓣风控/接口异常，标记错误而非完成
		updates["status"] = "error"
		updates["last_error"] = "豆瓣未返回条目（可能触发风控，稍后自动重试）"
	} else if len(items) < doubanCatalogPageSize { // 抓到底
		updates["status"] = "ready"
	} else {
		updates["status"] = "pending"
	}
	db.Db.Model(&state).Updates(updates)
	return added, total, nil
}

// DiscoverDoubanCatalog 豆瓣目录浏览（等待后台预抓补齐当前页）
func DiscoverDoubanCatalog(mediaType, tag, sort string, page int, waitSeconds int, force bool) (*CatalogPage, error) {
	if mediaType != "tv" {
		mediaType = "movie"
	}
	if page <= 0 {
		page = 1
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		tag = "热门"
	}
	if sort == "" {
		sort = "T"
	}
	catalogKey := doubanCatalogKey(mediaType, tag, sort)
	need := page * doubanCatalogPageSize

	ensureCatalogState(catalogKey, "douban")
	kickDoubanPrefetch(catalogKey, mediaType, tag, sort, need, force)

	state := waitCatalogProgress(catalogKey, need, waitSeconds)
	return doubanCatalogPage(catalogKey, page, state)
}

// doubanCatalogPage 从缓存切片生成目录分页
func doubanCatalogPage(catalogKey string, page int, state *DiscoveryCatalogState) (*CatalogPage, error) {
	offset := (page - 1) * doubanCatalogPageSize
	var rows []DiscoverySubjectCache
	if err := db.Db.Where("catalog_key = ?", catalogKey).Order("sort asc, id asc").
		Offset(offset).Limit(doubanCatalogPageSize).Find(&rows).Error; err != nil {
		return nil, err
	}
	var cachedCount int64
	db.Db.Model(&DiscoverySubjectCache{}).Where("catalog_key = ?", catalogKey).Count(&cachedCount)
	var matched int64
	db.Db.Model(&DiscoverySubjectCache{}).Where("catalog_key = ? AND tmdb_id > 0", catalogKey).Count(&matched)

	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, subjectToItem(row))
	}
	st := "ready"
	pending := 0
	if state != nil {
		switch {
		case state.Status == "error" && cachedCount == 0:
			st = "error"
		case int(cachedCount) < page*doubanCatalogPageSize && state.Status != "ready":
			st = "partial"
			pending = page*doubanCatalogPageSize - int(cachedCount)
			if state.Status == "fetching" || state.Status == "pending" {
				st = "pending"
			}
		case state.Stale:
			st = "stale"
		}
	}
	pageState := "ready"
	if st == "pending" || st == "partial" {
		pageState = st
	} else if matched < int64(len(rows)) {
		pageState = "matching"
	}
	cp := &CatalogPage{
		Items:           items,
		Page:            page,
		TotalPages:      0, // 目录流不确定总页数，前端按 has_next_page 翻页
		TotalItems:      int(cachedCount),
		HasNextPage:     true,
		CatalogStatus:   st,
		PageState:       pageState,
		PendingCount:    pending,
		CachedItemCount: int(cachedCount),
		IsStale:         state != nil && state.Stale,
		SourceExhausted: state != nil && state.Status == "ready" && state.FetchedCount >= state.TotalItems && state.TotalItems > 0,
		CatalogTotal:    stateTotal(state),
		MatchedCount:    int(matched),
	}
	if state != nil && state.TotalItems > 0 {
		cp.HasNextPage = offset+len(rows) < state.TotalItems
	}
	if state != nil && state.Status == "error" && cachedCount == 0 {
		return cp, fmt.Errorf("豆瓣目录暂不可用：%s", state.LastError)
	}
	return cp, nil
}

func stateTotal(state *DiscoveryCatalogState) int {
	if state == nil {
		return 0
	}
	return state.TotalItems
}

// ensureCatalogState 确保目录状态行存在
func ensureCatalogState(catalogKey, source string) {
	var count int64
	db.Db.Model(&DiscoveryCatalogState{}).Where("catalog_key = ?", catalogKey).Count(&count)
	if count == 0 {
		db.Db.Create(&DiscoveryCatalogState{CatalogKey: catalogKey, Source: source, Status: "pending", UpdatedAt: time.Now()})
	}
}

// waitCatalogProgress 等待目录抓取进度（最多 waitSeconds，每 600ms 轮询）
func waitCatalogProgress(catalogKey string, need int, waitSeconds int) *DiscoveryCatalogState {
	deadline := time.Now().Add(time.Duration(waitSeconds) * time.Second)
	for {
		var state DiscoveryCatalogState
		if err := db.Db.Where("catalog_key = ?", catalogKey).First(&state).Error; err != nil {
			return nil
		}
		// 抓够 / 抓到底 / 出错 / 非进行中 → 立即返回
		if state.FetchedCount >= need ||
			state.Status == "error" ||
			(state.TotalItems > 0 && state.FetchedCount >= state.TotalItems) ||
			(state.Status != "fetching" && state.Status != "pending") {
			return &state
		}
		if !time.Now().Before(deadline) {
			return &state
		}
		time.Sleep(600 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// AniList / Bangumi 目录浏览（请求时直取，落库 + 异步 TMDB 匹配）
// ---------------------------------------------------------------------------

// ANIME_GENRES 动漫类型（对齐参考实现动漫筛选行）
var AnimeGenreOptions = []string{"动作", "冒险", "喜剧", "剧情", "奇幻", "恐怖", "悬疑", "爱情", "科幻", "运动", "音乐", "日常", "超自然"}

// AniList/动漫地区选项
var AnimeRegionOptions = []map[string]string{
	{"key": "", "label": "全部"},
	{"key": "JP", "label": "日本"},
	{"key": "CN", "label": "大陆"},
	{"key": "KR", "label": "韩国"},
}

// 动漫排序（对齐参考实现：热度/最新/社区评分）
var AnimeSortOptions = []map[string]string{
	{"key": "popular", "label": "热度"},
	{"key": "latest", "label": "最新"},
	{"key": "rating", "label": "社区评分"},
}

// animeGenreEN 动漫类型中文 → AniList 英文类型 / Bangumi 标签
var animeGenreEN = map[string]string{
	"动作": "Action", "冒险": "Adventure", "喜剧": "Comedy", "剧情": "Drama",
	"奇幻": "Fantasy", "恐怖": "Horror", "悬疑": "Mystery", "爱情": "Romance",
	"科幻": "Sci-Fi", "运动": "Sports", "音乐": "Music", "日常": "Slice of Life",
	"超自然": "Supernatural",
}

// AnimeBrowseFilter 动漫目录筛选（genre/region/year/sort）
type AnimeBrowseFilter struct {
	Genre  string
	Region string
	Year   string
	Sort   string
}

func (f AnimeBrowseFilter) catalogKey(source string) string {
	return fmt.Sprintf("anime:v1:%s:%s:%s:%s:%s", source, f.Genre, f.Region, f.Year, f.Sort)
}

// DiscoverAnimeCatalog 动漫目录（source=anilist/bangumi）
func DiscoverAnimeCatalog(ctx context.Context, source string, f AnimeBrowseFilter, page int, waitSeconds int, force bool) (*CatalogPage, error) {
	if page <= 0 {
		page = 1
	}
	source = strings.ToLower(source)
	if source != "anilist" && source != "bangumi" {
		source = "anilist"
	}
	catalogKey := f.catalogKey(source)
	ensureCatalogState(catalogKey, source)
	need := page * 24

	// 缓存是否已够当前页
	var state DiscoveryCatalogState
	db.Db.Where("catalog_key = ?", catalogKey).First(&state)
	if force || state.FetchedCount < need {
		var (
			items []Item
			total int
			err   error
		)
		if source == "anilist" {
			items, total, err = anilistBrowsePage(ctx, f, page)
		} else {
			items, total, err = bangumiBrowsePage(ctx, f, page)
		}
		if err != nil {
			db.Db.Model(&state).Updates(map[string]any{"last_error": truncateStr(err.Error(), 500), "updated_at": time.Now()})
			return nil, err
		}
		appendAnimeCatalog(catalogKey, source, items, (page-1)*24)
		db.Db.Model(&state).Updates(map[string]any{
			"fetched_count":   (page-1)*24 + len(items),
			"total_items":     total,
			"status":          "ready",
			"last_fetched_at": time.Now(),
			"last_error":      "",
			"updated_at":      time.Now(),
		})
		kickTMDBMatch()
		db.Db.Where("catalog_key = ?", catalogKey).First(&state)
	}
	return animeCatalogPage(catalogKey, page, &state)
}

// appendAnimeCatalog 目录条目入库（去重追加）
func appendAnimeCatalog(catalogKey, source string, items []Item, sortBase int) {
	for i, item := range items {
		var exist DiscoverySubjectCache
		if err := db.Db.Where("catalog_key = ? AND source = ? AND external_id = ?", catalogKey, source, item.ExternalID).First(&exist).Error; err == nil {
			continue
		}
		row := DiscoverySubjectCache{
			CatalogKey:    catalogKey,
			Source:        source,
			ExternalID:    item.ExternalID,
			Title:         item.Title,
			OriginalTitle: item.OriginalTitle,
			MediaType:     "tv",
			Poster:        item.Poster,
			Rating:        item.VoteAvg,
			ReleaseDate:   item.ReleaseDate,
			Sort:          sortBase + i,
			Payload:       marshalJSON(item),
		}
		db.Db.Create(&row)
	}
}

// animeCatalogPage 目录分页（从缓存切片）
func animeCatalogPage(catalogKey string, page int, state *DiscoveryCatalogState) (*CatalogPage, error) {
	const pageSize = 24
	offset := (page - 1) * pageSize
	var rows []DiscoverySubjectCache
	if err := db.Db.Where("catalog_key = ?", catalogKey).Order("sort asc, id asc").Offset(offset).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, err
	}
	var cachedCount int64
	db.Db.Model(&DiscoverySubjectCache{}).Where("catalog_key = ?", catalogKey).Count(&cachedCount)
	var matched int64
	db.Db.Model(&DiscoverySubjectCache{}).Where("catalog_key = ? AND tmdb_id > 0", catalogKey).Count(&matched)

	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, subjectToItem(row))
	}
	total := stateTotal(state)
	cp := &CatalogPage{
		Items:           items,
		Page:            page,
		TotalPages:      0,
		TotalItems:      int(cachedCount),
		HasNextPage:     true,
		CatalogStatus:   "ready",
		PageState:       "ready",
		CachedItemCount: int(cachedCount),
		MatchedCount:    int(matched),
		CatalogTotal:    total,
		SourceExhausted: total > 0 && offset+len(rows) >= total,
	}
	if total > 0 {
		cp.HasNextPage = offset+len(rows) < total
	}
	if cp.MatchedCount < int(cachedCount) {
		cp.PageState = "matching"
	}
	return cp, nil
}

// anilistBrowsePage AniList 目录翻页（按类型/地区/年份/排序）
func anilistBrowsePage(ctx context.Context, f AnimeBrowseFilter, page int) ([]Item, int, error) {
	sortMap := map[string]string{"popular": "POPULARITY_DESC", "latest": "START_DATE_DESC", "rating": "SCORE_DESC"}
	sortKey := sortMap[f.Sort]
	if sortKey == "" {
		sortKey = "POPULARITY_DESC"
	}
	vars := map[string]any{"page": page}
	mediaArgs := fmt.Sprintf("type: ANIME, sort: %s", sortKey)
	if genre := animeGenreEN[f.Genre]; f.Genre != "" && genre != "" {
		mediaArgs += fmt.Sprintf(", genre: %q", genre)
	}
	switch f.Region {
	case "JP", "CN", "KR":
		mediaArgs += fmt.Sprintf(", countryOfOrigin: %s", f.Region)
	}
	if len(f.Year) == 4 {
		year := 0
		fmt.Sscanf(f.Year, "%d", &year)
		if year > 1900 {
			mediaArgs += fmt.Sprintf(", startDate_greater: %d, startDate_lesser: %d", year*10000, year*10000+1231)
		}
	}
	query := fmt.Sprintf(`
query ($page: Int) {
  Page(page: $page, perPage: 24) {
    pageInfo { currentPage lastPage total }
    media(%s) {
      id
      title { romaji english native }
      coverImage { large extraLarge }
      averageScore
      description(asHtml: false)
      genres
      startDate { year month day }
      episodes
      format
    }
  }
}`, mediaArgs)
	vars["query"] = query
	payload := map[string]any{"query": query, "variables": vars}
	var out struct {
		Data struct {
			Page struct {
				PageInfo struct {
					LastPage int `json:"lastPage"`
					Total    int `json:"total"`
				} `json:"pageInfo"`
				Media []anilistMedia `json:"media"`
			} `json:"Page"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := anilistRequest(ctx, payload, &out); err != nil {
		return nil, 0, err
	}
	if len(out.Errors) > 0 {
		return nil, 0, fmt.Errorf("AniList 查询失败：%s", out.Errors[0].Message)
	}
	items := make([]Item, 0, len(out.Data.Page.Media))
	for _, m := range out.Data.Page.Media {
		vote := 0.0
		if m.AverageScore > 0 {
			vote = float64(m.AverageScore) / 10.0
		}
		item := Item{
			Source:        "anilist",
			MediaType:     "tv",
			ExternalID:    fmt.Sprintf("%d", m.ID),
			EntityKey:     normalizeEntityKey("anilist", "tv", fmt.Sprintf("%d", m.ID)),
			Title:         firstNonEmptyStr(m.Title.English, m.Title.Romaji, m.Title.Native),
			OriginalTitle: firstNonEmptyStr(m.Title.Romaji, m.Title.Native),
			Poster:        firstNonEmptyStr(m.CoverImage.ExtraLarge, m.CoverImage.Large),
			Overview:      strings.TrimSpace(m.Description),
			VoteAvg:       vote,
			ReleaseDate:   anilistDate(m),
			Genres:        m.Genres,
		}
		if len(item.ReleaseDate) >= 4 {
			item.Year, _ = strconvAtoi(item.ReleaseDate[:4])
		}
		items = append(items, item)
	}
	return items, out.Data.Page.PageInfo.Total, nil
}

// anilistRequest 执行 AniList GraphQL 请求
func anilistRequest(ctx context.Context, payload, out any) error {
	body, _ := json.Marshal(payload)
	req, err := httpNewRequestWithContext(ctx, "POST", anilistGraphQLURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := animeHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求 AniList 失败：%v", err)
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// bangumiBrowsePage Bangumi 目录翻页（v0 search/subjects + heat 排序）
func bangumiBrowsePage(ctx context.Context, f AnimeBrowseFilter, page int) ([]Item, int, error) {
	const pageSize = 24
	sortMap := map[string]string{"popular": "heat", "latest": "match", "rating": "score"}
	sortKey := sortMap[f.Sort]
	if sortKey == "" {
		sortKey = "heat"
	}
	filter := map[string]any{}
	tags := []string{}
	if genre := animeGenreEN[f.Genre]; f.Genre != "" && genre != "" {
		// Bangumi 用中文标签更准
		tags = append(tags, f.Genre)
	}
	switch f.Region {
	case "JP":
		tags = append(tags, "日本")
	case "CN":
		tags = append(tags, "中国")
	case "KR":
		tags = append(tags, "韩国")
	}
	if len(tags) > 0 {
		filter["tags"] = tags
	}
	if len(f.Year) == 4 {
		filter["air_date"] = []string{">=" + f.Year + "-01-01", "<=" + f.Year + "-12-31"}
	}
	payload := map[string]any{
		"sort":   sortKey,
		"filter": filter,
		"limit":  pageSize,
		"offset": (page - 1) * pageSize,
	}
	body, _ := json.Marshal(payload)
	req, err := httpNewRequestWithContext(ctx, "POST", bangumiBaseURL+"/v0/search/subjects", body)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range bangumiHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := animeHTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求 Bangumi 目录失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("Bangumi 目录失败：HTTP %d", resp.StatusCode)
	}
	var out struct {
		Total int           `json:"total"`
		Data  []bangumiSubject `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, 0, fmt.Errorf("解析 Bangumi 目录失败：%v", err)
	}
	items := make([]Item, 0, len(out.Data))
	for _, s := range out.Data {
		item := Item{
			Source:        "bangumi",
			MediaType:     "tv",
			ExternalID:    fmt.Sprintf("%d", s.ID),
			EntityKey:     normalizeEntityKey("bangumi", "tv", fmt.Sprintf("%d", s.ID)),
			Title:         firstNonEmptyStr(s.NameCN, s.Name),
			OriginalTitle: s.Name,
			Overview:      s.Summary,
			VoteAvg:       s.Rating.Score,
			ReleaseDate:   s.Date,
		}
		if item.Poster = s.Images.Large; item.Poster == "" {
			item.Poster = s.Images.Common
		}
		for i, tag := range s.Tags {
			if i >= 5 {
				break
			}
			item.Genres = append(item.Genres, tag.Name)
		}
		if len(item.ReleaseDate) >= 4 {
			item.Year, _ = strconvAtoi(item.ReleaseDate[:4])
		}
		items = append(items, item)
	}
	return items, out.Total, nil
}

// ---------------------------------------------------------------------------
// 后台 Worker：豆瓣目录预抓 + TMDB 匹配
// ---------------------------------------------------------------------------

type doubanPrefetchTask struct {
	catalogKey string
	mediaType  string
	tag        string
	sort       string
	need       int
	force      bool
}

// 通道在包初始化时创建：若懒创建，Worker 先启动会在 nil channel 上
// 永久阻塞（接收表达式不会因全局变量重新赋值而恢复），预抓任务永远无法送达
var (
	workerOnce       sync.Once
	doubanPrefetchCh = make(chan doubanPrefetchTask, 64)
	tmdbMatchCh      = make(chan struct{}, 8)
)

// kickDoubanPrefetch 投递豆瓣预抓任务（通道满则丢弃最旧任务，保留最新）
func kickDoubanPrefetch(catalogKey, mediaType, tag, sort string, need int, force bool) {
	task := doubanPrefetchTask{catalogKey: catalogKey, mediaType: mediaType, tag: tag, sort: sort, need: need, force: force}
	select {
	case <-doubanPrefetchCh: // 丢弃一个旧任务腾位
	default:
	}
	select {
	case doubanPrefetchCh <- task:
	default:
	}
}

// kickTMDBMatch 触发一次 TMDB 匹配
func kickTMDBMatch() {
	select {
	case tmdbMatchCh <- struct{}{}:
	default:
	}
}

// StartDiscoveryWorkers 启动发现页后台 Worker（目录预抓 + TMDB 匹配 + 订阅调度 + 缺集扫描）
func StartDiscoveryWorkers() {
	workerOnce.Do(func() {
		go doubanPrefetchWorker()
		go tmdbMatchWorker()
		go subscriptionWorker()
		go embyMissingWorker()
		log.Println("[discovery] 后台 Worker 已启动（目录预抓/TMDB匹配/订阅调度/缺集扫描）")
	})
}

// doubanPrefetchWorker 豆瓣目录预抓循环
func doubanPrefetchWorker() {
	for {
		task, ok := <-doubanPrefetchCh
		if !ok {
			return
		}
		runDoubanPrefetch(task)
	}
}

// runDoubanPrefetch 抓取直到满足 need 或抓到底（每轮最多 6 页，防豆瓣风控）
func runDoubanPrefetch(task doubanPrefetchTask) {
	for round := 0; round < 6; round++ {
		var state DiscoveryCatalogState
		if err := db.Db.Where("catalog_key = ?", task.catalogKey).First(&state).Error; err != nil {
			return
		}
		if !task.force && state.FetchedCount >= task.need {
			return
		}
		if task.force {
			// 手动刷新只补抓缺页，不重置
			task.force = false
		}
		if state.TotalItems > 0 && state.FetchedCount >= state.TotalItems {
			db.Db.Model(&state).Updates(map[string]any{"status": "ready", "updated_at": time.Now()})
			return
		}
		_, _, err := fetchDoubanCatalogPage(task.catalogKey, task.mediaType, task.tag, task.sort)
		if err != nil {
			log.Printf("[discovery] 豆瓣目录预抓失败 %s：%v", task.catalogKey, err)
			time.Sleep(2 * time.Second)
			return
		}
		kickTMDBMatch()
		time.Sleep(1200 * time.Millisecond) // 0.75rps 内的间隔，规避豆瓣风控
	}
	// 未抓够，继续排队下一轮
	var state DiscoveryCatalogState
	if err := db.Db.Where("catalog_key = ?", task.catalogKey).First(&state).Error; err == nil && state.FetchedCount < task.need {
		kickDoubanPrefetch(task.catalogKey, task.mediaType, task.tag, task.sort, task.need, false)
	}
}

// tmdbMatchWorker TMDB 匹配循环：每次取 12 条未匹配目录条目
func tmdbMatchWorker() {
	for {
		<-tmdbMatchCh
		matchPendingSubjects(12)
		time.Sleep(600 * time.Millisecond)
	}
}

// matchPendingSubjects 匹配未处理的目录条目到 TMDB
func matchPendingSubjects(limit int) {
	var rows []DiscoverySubjectCache
	if err := db.Db.Where("source IN ? AND matched_at IS NULL", []string{"douban", "anilist", "bangumi"}).
		Order("id asc").Limit(limit).Find(&rows).Error; err != nil || len(rows) == 0 {
		return
	}
	client := models.GlobalScrapeSettings.GetTmdbClient()
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	for _, row := range rows {
		title := row.TMDBTitle
		if title == "" {
			title = row.Title
		}
		if strings.TrimSpace(title) == "" {
			db.Db.Model(&row).Updates(map[string]any{"matched_at": time.Now()})
			continue
		}
		year := 0
		if len(row.ReleaseDate) >= 4 {
			year, _ = strconvAtoi(row.ReleaseDate[:4])
		}
		tmdbID, score := matchSubjectTMDB(client, language, row.MediaType, title, row.OriginalTitle, year)
		now := time.Now()
		if tmdbID > 0 {
			db.Db.Model(&row).Updates(map[string]any{
				"tmdb_id": tmdbID, "match_score": score, "matched_at": &now,
			})
		} else {
			db.Db.Model(&row).Updates(map[string]any{"match_score": 0, "matched_at": &now})
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// matchSubjectTMDB 标题+年份评分匹配（先剧集后电影）
func matchSubjectTMDB(client *tmdbClient, language, mediaType, title, originalTitle string, year int) (int64, float64) {
	if mediaType == "tv" {
		if id, score := searchSubjectTMDB(client, language, "tv", title, originalTitle, year); id > 0 {
			return id, score
		}
		return searchSubjectTMDB(client, language, "movie", title, originalTitle, year)
	}
	if id, score := searchSubjectTMDB(client, language, "movie", title, originalTitle, year); id > 0 {
		return id, score
	}
	return searchSubjectTMDB(client, language, "tv", title, originalTitle, year)
}

// subjectToItem 缓存条目 → 前端条目
func subjectToItem(row DiscoverySubjectCache) Item {
	return Item{
		Source:        row.Source,
		MediaType:     row.MediaType,
		EntityKey:     normalizeEntityKey(row.Source, row.MediaType, row.ExternalID),
		ExternalID:    row.ExternalID,
		TMDBID:        row.TMDBID,
		Title:         row.Title,
		OriginalTitle: row.OriginalTitle,
		Poster:        row.Poster,
		VoteAvg:       row.Rating,
		ReleaseDate:   row.ReleaseDate,
		Year:          parseYear(row.ReleaseDate),
	}
}

func parseYear(date string) int {
	if len(date) >= 4 {
		y, _ := strconvAtoi(date[:4])
		return y
	}
	return 0
}

func ternaryStr(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
