package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"diy-strm/internal/discovery"
	"diy-strm/internal/guanying"
	"diy-strm/internal/hdhive"
	"diy-strm/internal/seedhub"

	"github.com/gin-gonic/gin"
)

// 影视发现「关联资源」聚合搜索（对齐 tgto123 新版 /api/media/resources/* 形状）。
// 来源 source：re0（原影巢/HDHive，复用四通道客户端）+ guanying/seedhub（预留，
// 未配置时返回 RESOURCE_SOURCE_UNAVAILABLE）。

// resourceSearchRequest POST /media-discovery/resources/search 请求体
type resourceSearchRequest struct {
	Title     string   `json:"title"`
	Aliases   []string `json:"aliases"`
	TmdbID    int64    `json:"tmdb_id"`
	MediaType string   `json:"media_type"`
	Year      string   `json:"year"`
	Sources   []string `json:"sources"`
	Provider  string   `json:"provider"`
	Season    int      `json:"season"`
	Episode   int      `json:"episode"`
}

// resourceItem 关联资源卡片字段（对齐 tgto123 media_discovery.js 渲染所需全集）
type resourceItem struct {
	ItemKey          string           `json:"item_key"`
	Source           string           `json:"source"`
	Provider         string           `json:"provider"`
	ProviderLabel    string           `json:"provider_label"`
	Title            string           `json:"title"`
	Slug             string           `json:"slug"`
	ShareURL         string           `json:"share_url"`
	LinkType         string           `json:"link_type"`
	Size             string           `json:"size"`
	Episode          *resourceEpisode `json:"episode,omitempty"`
	IsUnlocked       bool             `json:"is_unlocked"`
	PointsKnown      bool             `json:"points_known"`
	UnlockPoints     int              `json:"unlock_points"`
	UnlockedUsersCt  int              `json:"unlocked_users_count"`
	Remark           string           `json:"remark"`
	ValidateMessage  string           `json:"validate_message"`
	IsOfficial       bool             `json:"is_official"`
	Sharer           string           `json:"sharer"`
	SpecTags         []string         `json:"resource_spec_tags"`
	SubtitleLangs    []string         `json:"subtitle_languages"`
	SubtitleTypes    []string         `json:"subtitle_types"`
	SupportedTargets []string         `json:"supported_targets"`
	TargetProvider   string           `json:"target_provider"`
}

// resourceEpisode 季集信息（对齐 tgto123 卡片 SxxExx 渲染）
type resourceEpisode struct {
	SeasonNum       *int `json:"season_num"`
	EpisodeNum      *int `json:"episode_num"`
	EndEpisodeNum   *int `json:"end_episode_num"`
	TotalEpisodeNum *int `json:"total_episode_num"`
	IsComplete      bool `json:"is_complete"`
	IsUpdated       bool `json:"is_updated"`
}

// providerLabel 把网盘类型映射为展示名（对齐 tgto123：guangyapan→光鸭 等）
func resourceProviderLabel(panType string) string {
	switch strings.ToLower(strings.TrimSpace(panType)) {
	case "115":
		return "115"
	case "123", "123pan":
		return "123"
	case "guangyapan", "guangya", "gy":
		return "光鸭"
	case "magnet":
		return "磁力"
	case "ed2k":
		return "ED2K"
	case "139":
		return "移动云盘"
	default:
		return panType
	}
}

// offlineSupportedTargets 磁力/ED2K 可离线的网盘（对齐 tgto123 前端约定）
func offlineSupportedTargets(linkType string) []string {
	lt := strings.ToLower(strings.TrimSpace(linkType))
	if lt == "ed2k" {
		return []string{"115", "guangya"}
	}
	if lt == "magnet" {
		return []string{"115", "123", "guangya"}
	}
	return nil
}

// re0ResourceItems 把 RE0（HDHive）官方资源响应映射为统一资源卡片
func re0ResourceItems(resp *hdhive.OAuthAPIResponse) ([]resourceItem, error) {
	var resources []hdhive.Resource
	if len(resp.Data) > 0 && string(resp.Data) != "null" {
		if err := json.Unmarshal(resp.Data, &resources); err != nil {
			return nil, err
		}
	}
	items := make([]resourceItem, 0, len(resources))
	for _, r := range resources {
		linkType := strings.ToLower(strings.TrimSpace(r.PanType))
		isOffline := linkType == "magnet" || linkType == "ed2k"
		item := resourceItem{
			ItemKey:         "re0:" + r.Slug,
			Source:          "re0",
			Provider:        r.PanType,
			ProviderLabel:   resourceProviderLabel(r.PanType),
			Title:           r.Title,
			Slug:            r.Slug,
			LinkType:        r.PanType,
			Size:            r.ShareSize,
			IsUnlocked:      r.IsUnlocked,
			PointsKnown:     !isOffline && r.UnlockPoints > 0,
			UnlockPoints:    r.UnlockPoints,
			UnlockedUsersCt: r.UnlockedUsersCount,
			Remark:          r.Remark,
			ValidateMessage: r.ValidateMessage,
			IsOfficial:      r.IsOfficial,
			SpecTags:        append(append([]string{}, r.VideoResolution...), r.Source...),
			SubtitleLangs:   r.SubtitleLanguage,
			SubtitleTypes:   r.SubtitleType,
		}
		if r.User != nil && r.User.Nickname != "" {
			item.Sharer = r.User.Nickname
		}
		if isOffline {
			// 磁力/ED2K：链接本身即 share_url，可离线
			item.ShareURL = r.Slug
			item.SupportedTargets = offlineSupportedTargets(linkType)
		}
		items = append(items, item)
	}
	return items, nil
}

// SearchMediaResourcesAPI POST /api/media-discovery/resources/search
// body: {title, aliases, tmdb_id, media_type, year, sources:['re0'|'guanying'|'seedhub']}
// 每来源独立返回（前端并发合并），单源失败不影响其它来源。
func SearchMediaResourcesAPI(c *gin.Context) {
	var req resourceSearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error()})
		return
	}
	if strings.TrimSpace(req.Title) == "" && req.TmdbID <= 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "标题与 TMDB ID 至少提供一个"})
		return
	}
	sources := normalizeResourceSources(req.Sources)
	if len(sources) == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "未指定资源来源"})
		return
	}

	items := make([]resourceItem, 0)
	errs := make([]gin.H, 0)
	mediaType := strings.ToLower(strings.TrimSpace(req.MediaType))
	if mediaType != "tv" {
		mediaType = "movie"
	}

	wantProvider := strings.ToLower(strings.TrimSpace(req.Provider))
	for _, source := range sources {
		sourceItems, err := searchResourceBySource(c.Request.Context(), source, mediaType, req.TmdbID, req.Title)
		if err != nil {
			errs = append(errs, gin.H{"source": source, "code": resourceSourceErrorCode(err), "error": err.Error()})
			continue
		}
		// 目标网盘过滤（对齐参考实现：provider 非 offline 时须精确匹配）
		for i := range sourceItems {
			if wantProvider == "" || wantProvider == "offline" {
				continue
			}
			itemProvider := strings.ToLower(strings.TrimSpace(sourceItems[i].Provider))
			if itemProvider == "magnet" || itemProvider == "ed2k" {
				continue
			}
			if normalizeProviderFilter(itemProvider) != normalizeProviderFilter(wantProvider) {
				sourceItems[i].Provider = "" // 标记剔除
			}
		}
		filtered := sourceItems[:0]
		for _, it := range sourceItems {
			if it.Provider == "" {
				continue
			}
			filtered = append(filtered, it)
		}
		items = append(items, filtered...)
	}

	// 季集过滤（对齐参考实现：season 相等；episode 落在 [begin,end]）
	if req.Season > 0 || req.Episode > 0 {
		items = filterResourcesByEpisode(items, req.Season, req.Episode)
	}
	// 排序：已解锁优先 → 有积分低优先
	sort.SliceStable(items, func(i, j int) bool {
		return resourceCandidateWeight(items[i]) > resourceCandidateWeight(items[j])
	})

	c.JSON(http.StatusOK, APIResponse[gin.H]{
		Code:    Success,
		Message: "",
		Data:    gin.H{"items": items, "errors": errs},
	})
}

// normalizeResourceSources 归一化来源：hdhive→re0，去重，仅保留已知来源
func normalizeResourceSources(raw []string) []string {
	known := map[string]bool{"re0": true, "guanying": true, "seedhub": true, "tg": true}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, s := range raw {
		key := strings.ToLower(strings.TrimSpace(s))
		if key == "hdhive" {
			key = "re0"
		}
		if !known[key] || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// resourceSourceErrorCode 错误码（对齐 tgto123：RESOURCE_SOURCE_UNAVAILABLE 等）
func resourceSourceErrorCode(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "未配置"), strings.Contains(msg, "未授权"), strings.Contains(msg, "auth required"):
		return "RESOURCE_SOURCE_UNAVAILABLE"
	case strings.Contains(msg, "超时"):
		return "RESOURCE_SOURCE_TIMEOUT"
	default:
		return "RESOURCE_SOURCE_ERROR"
	}
}

// searchResourceBySource 按来源执行搜索
func searchResourceBySource(ctx context.Context, source, mediaType string, tmdbID int64, title string) ([]resourceItem, error) {
	switch source {
	case "re0":
		return searchRe0Resources(ctx, mediaType, tmdbID, title)
	case "guanying":
		return searchGuanyingResources(ctx, mediaType, tmdbID, title)
	case "seedhub":
		return searchSeedhubResources(ctx, mediaType, tmdbID, title)
	case "tg":
		return searchTGChannelResources(ctx, mediaType, title)
	default:
		return nil, fmt.Errorf("未知资源来源：%s", source)
	}
}

// searchGuanyingResources 观影源检索：会话保存后走上游搜索，
// 结果映射为统一资源卡片（guanying 分享无 RE0 积分语义）
func searchGuanyingResources(ctx context.Context, mediaType string, tmdbID int64, title string) ([]resourceItem, error) {
	if !guanyingEnabled() {
		return nil, fmt.Errorf("观影未启用：请先在发现-基础配置中开启观影")
	}
	client := guanying.SharedClient()
	rawItems, err := client.SearchResources(ctx, title, mediaType, tmdbID, "")
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(rawItems))
	for _, raw := range rawItems {
		str := func(key string) string {
			if v, ok := raw[key].(string); ok {
				return v
			}
			return ""
		}
		panType := str("provider")
		if panType == "" {
			panType = str("pan_type")
		}
		linkType := strings.ToLower(panType)
		isOffline := linkType == "magnet" || linkType == "ed2k"
		slug := str("slug")
		item := resourceItem{
			ItemKey:       "guanying:" + firstNonEmptyStr(slug, str("share_url"), str("title")),
			Source:        "guanying",
			Provider:      panType,
			ProviderLabel: resourceProviderLabel(panType),
			Title:         str("title"),
			Slug:          slug,
			ShareURL:      str("share_url"),
			LinkType:      panType,
			Size:          str("size"),
			IsUnlocked:    true, // 观影公开分享无解锁概念
			Remark:        str("remark"),
		}
		if isOffline {
			item.ShareURL = firstNonEmptyStr(item.ShareURL, slug)
			item.SupportedTargets = offlineSupportedTargets(linkType)
		}
		items = append(items, item)
	}
	return items, nil
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// searchRe0Resources 走 tgto123 反代搜索 RE0 资源（四通道已失效下线）
func searchRe0Resources(ctx context.Context, mediaType string, tmdbID int64, title string) ([]resourceItem, error) {
	if tmdbID <= 0 && strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("RE0 检索需要 TMDB ID 或标题")
	}
	resources, err := discovery.Tgto123SearchResources(ctx, title, tmdbID, mediaType, "")
	if err != nil {
		return nil, fmt.Errorf("tgto123 反代不可用：%w", err)
	}
	items := make([]resourceItem, 0, len(resources))
	for _, r := range resources {
		linkType := strings.ToLower(strings.TrimSpace(r.PanType))
		isOffline := linkType == "magnet" || linkType == "ed2k"
		item := resourceItem{
			ItemKey:         "re0:" + firstNonEmptyStr(r.Slug, r.Title),
			Source:          "re0",
			Provider:        r.PanType,
			ProviderLabel:   resourceProviderLabel(r.PanType),
			Title:           r.Title,
			Slug:            r.Slug,
			LinkType:        r.PanType,
			Size:            r.ShareSize,
			IsUnlocked:      r.IsUnlocked,
			PointsKnown:     !isOffline && r.UnlockPoints > 0,
			UnlockPoints:    r.UnlockPoints,
			UnlockedUsersCt: r.UnlockedUsersCount,
			Remark:          r.Remark,
			ValidateMessage: r.ValidateMessage,
			IsOfficial:      r.IsOfficial,
			SpecTags:        append(append([]string{}, r.VideoResolution...), r.Source...),
			SubtitleLangs:   r.SubtitleLanguage,
			SubtitleTypes:   r.SubtitleType,
		}
		if r.User != nil && r.User.Nickname != "" {
			item.Sharer = r.User.Nickname
		}
		if isOffline {
			item.ShareURL = r.Slug
			item.SupportedTargets = offlineSupportedTargets(linkType)
		}
		items = append(items, item)
	}
	return items, nil
}

// CopyRe0ResourceLinkAPI POST /api/media-discovery/resources/copy-link
// 生成 RE0 资源分享页地址（对齐 tgto123：仅 source=re0 需要，slug 拼站点链接）
func CopyRe0ResourceLinkAPI(c *gin.Context) {
	var req struct {
		Source   string `json:"source"`
		Provider string `json:"provider"`
		Slug     string `json:"slug"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少资源 slug"})
		return
	}
	base := hdhive.DefaultOfficialBaseURL
	link := fmt.Sprintf("%s/resource/%s", strings.TrimRight(base, "/"), slug)
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{"link": link, "copied_at": time.Now().Format(time.RFC3339)}})
}

// searchSeedhubResources SeedHub 源检索（未配置返回标准化不可用错误）
func searchSeedhubResources(ctx context.Context, mediaType string, tmdbID int64, title string) ([]resourceItem, error) {
	cfg, ok := seedhub.GetConfig()
	if !ok {
		return nil, fmt.Errorf("SeedHub 未配置：请先在发现-基础配置中填写 API 地址与令牌")
	}
	rawItems, err := seedhub.SearchResources(ctx, cfg, title, mediaType, tmdbID)
	if err != nil {
		return nil, err
	}
	items := make([]resourceItem, 0, len(rawItems))
	for _, raw := range rawItems {
		item := resourceItem{
			ItemKey:       raw.ItemKey,
			Source:        "seedhub",
			Provider:      raw.LinkType,
			ProviderLabel: resourceProviderLabel(raw.LinkType),
			Title:         raw.Title,
			Slug:          raw.Slug,
			ShareURL:      raw.ShareURL,
			LinkType:      raw.LinkType,
			Size:          raw.Size,
			Remark:        raw.Remark,
			IsUnlocked:    true, // SeedHub 分享无积分语义
		}
		if raw.IsOffline {
			item.SupportedTargets = offlineSupportedTargets(raw.LinkType)
		}
		items = append(items, item)
	}
	return items, nil
}


// normalizeProviderFilter 网盘过滤键归一
func normalizeProviderFilter(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "guangyapan", "guangya", "gy":
		return "guangya"
	case "123", "123pan":
		return "123"
	case "139", "pan139":
		return "pan139"
	}
	return strings.ToLower(strings.TrimSpace(p))
}

// searchTGChannelResources 公开 TG 频道资源检索（免登录直查 t.me/s）
func searchTGChannelResources(ctx context.Context, mediaType, title string) ([]resourceItem, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("TG 频道检索需要标题")
	}
	matches, errs := discovery.SearchTGChannelResources(ctx, title, nil, "")
	items := make([]resourceItem, 0, len(matches))
	for i, m := range matches {
		linkType := strings.ToLower(m.Link.Type)
		isOffline := linkType == "magnet" || linkType == "ed2k"
		item := resourceItem{
			ItemKey:       fmt.Sprintf("tg:%s:%s:%d", m.Channel, m.PostID, i),
			Source:        "tg",
			Provider:      m.Link.Type,
			ProviderLabel: resourceProviderLabel(m.Link.Type),
			Title:         firstTGTitle(m.PostText, title),
			ShareURL:      m.Link.URL,
			LinkType:      linkType,
			IsUnlocked:    true,
			Remark:        truncateTGText(m.PostText, 200),
		}
		if isOffline {
			item.SupportedTargets = offlineSupportedTargets(linkType)
		}
		items = append(items, item)
	}
	if len(items) == 0 && len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error)
		}
		return nil, fmt.Errorf("%s", strings.Join(msgs, "；"))
	}
	return items, nil
}

// firstTGTitle TG 帖子标题（取首行，截断）
func firstTGTitle(postText, fallback string) string {
	for _, line := range strings.Split(postText, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len([]rune(line)) > 80 {
				line = string([]rune(line)[:80])
			}
			return line
		}
	}
	return fallback
}

func truncateTGText(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// filterResourcesByEpisode 季集过滤
func filterResourcesByEpisode(items []resourceItem, season, episode int) []resourceItem {
	out := items[:0]
	for _, item := range items {
		ep := item.Episode
		if ep == nil {
			out = append(out, item)
			continue
		}
		if season > 0 && ep.SeasonNum != nil && *ep.SeasonNum != season {
			continue
		}
		if episode > 0 && ep.EpisodeNum != nil {
			begin := *ep.EpisodeNum
			end := begin
			if ep.EndEpisodeNum != nil {
				end = *ep.EndEpisodeNum
			}
			if episode < begin || episode > end {
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

// resourceCandidateWeight 排序权重
func resourceCandidateWeight(item resourceItem) float64 {
	w := 0.0
	if item.IsUnlocked {
		w += 100
	}
	if item.PointsKnown && item.UnlockPoints > 0 {
		w += float64(1000-item.UnlockPoints) / 10
	}
	if item.UnlockedUsersCt > 0 {
		w += float64(item.UnlockedUsersCt) / 100
	}
	return w
}
