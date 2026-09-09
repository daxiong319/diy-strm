package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"diy-strm/internal/guanying"
	"diy-strm/internal/hdhive"
	"diy-strm/internal/models"

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
	SeasonNum      *int `json:"season_num"`
	EpisodeNum     *int `json:"episode_num"`
	EndEpisodeNum  *int `json:"end_episode_num"`
	TotalEpisodeNum *int `json:"total_episode_num"`
	IsComplete     bool `json:"is_complete"`
	IsUpdated      bool `json:"is_updated"`
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

	for _, source := range sources {
		sourceItems, err := searchResourceBySource(c.Request.Context(), source, mediaType, req.TmdbID, req.Title)
		if err != nil {
			errs = append(errs, gin.H{"source": source, "code": resourceSourceErrorCode(err), "error": err.Error()})
			continue
		}
		items = append(items, sourceItems...)
	}

	c.JSON(http.StatusOK, APIResponse[gin.H]{
		Code: Success,
		Message: "",
		Data: gin.H{"items": items, "errors": errs},
	})
}

// normalizeResourceSources 归一化来源：hdhive→re0，去重，仅保留已知来源
func normalizeResourceSources(raw []string) []string {
	known := map[string]bool{"re0": true, "guanying": true, "seedhub": true}
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
		return searchRe0Resources(ctx, mediaType, tmdbID)
	case "guanying":
		return searchGuanyingResources(ctx, mediaType, tmdbID, title)
	case "seedhub":
		return nil, fmt.Errorf("SeedHub 未配置")
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

// searchRe0Resources 复用 RE0（原影巢）四通道负载均衡查询资源
func searchRe0Resources(ctx context.Context, mediaType string, tmdbID int64) ([]resourceItem, error) {
	if tmdbID <= 0 {
		return nil, fmt.Errorf("RE0 检索需要 TMDB ID")
	}
	query, err := models.HiveQueryResourcesWithFailover(ctx, mediaType, fmt.Sprintf("%d", tmdbID))
	if err != nil {
		return nil, fmt.Errorf("RE0 通道不可用：%w", err)
	}
	if !query.Resp.Success {
		msg := query.Resp.Message
		if msg == "" {
			msg = query.Resp.Description
		}
		if strings.Contains(strings.ToLower(query.Resp.Code), "auth") {
			return nil, fmt.Errorf("RE0 未授权：%s", msg)
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return re0ResourceItems(query.Resp)
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
