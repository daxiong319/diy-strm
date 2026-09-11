package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/hdhive"
)

// ---------------------------------------------------------------------------
// tgto123 反代通道：通过用户自建的 tgto123 实例（同机部署）调用其已实现的
// RE0 榜单/日历接口，绕开 RE0 直连的 IP 白名单与 feeds 上游 404 问题。
// 登录：POST /api/login（账号密码直登，无验证码），Flask session cookie 维持会话。
// ---------------------------------------------------------------------------


// Tgto123FeedClient tgto123 反代 Feed 客户端（hdhive.FeedClient 实现）
type Tgto123FeedClient struct {
	baseURL  string
	username string
	password string

	mu       sync.Mutex
	http     *http.Client
	loggedIn bool
	loginFai time.Time
}

// newTgto123FeedClient 从设置构建客户端（未配置 URL 返回 nil）
func newTgto123FeedClient() *Tgto123FeedClient {
	base := strings.TrimRight(SettingString(SettingTgto123URL, tgto123DefaultURL), "/")
	if base == "" {
		return nil
	}
	jar, _ := cookiejar.New(nil)
	return &Tgto123FeedClient{
		baseURL:  base,
		username: SettingString(SettingTgto123Username, "admin"),
		password: SettingString(SettingTgto123Password, ""),
		http:     &http.Client{Timeout: 30 * time.Second, Jar: jar},
	}
}

// tgto123FeedCall 通过 tgto123 反代执行一次 Feed 调用。
// ok=false 表示反代未配置或调用失败（调用方回退到 RE0 通道 failover）。
func tgto123FeedCall(ctx context.Context, call func(fc hdhive.FeedClient) (*hdhive.OAuthAPIResponse, error)) (*hdhive.OAuthAPIResponse, bool, error) {
	client := newTgto123FeedClient()
	if client == nil {
		return nil, false, nil
	}
	resp, err := client.callWithLogin(ctx, call)
	if err != nil {
		return nil, false, err
	}
	return resp, true, nil
}

// callWithLogin 带登录重试的调用（401/302 → 重登录 → 重试一次）
func (c *Tgto123FeedClient) callWithLogin(ctx context.Context, call func(fc hdhive.FeedClient) (*hdhive.OAuthAPIResponse, error)) (*hdhive.OAuthAPIResponse, error) {
	resp, err := call(c)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusFound {
		if lerr := c.login(ctx); lerr != nil {
			return nil, lerr
		}
		return call(c)
	}
	return resp, nil
}

// login POST /api/login（账号密码直登）
func (c *Tgto123FeedClient) login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loggedIn {
		return nil
	}
	if !c.loginFai.IsZero() && time.Since(c.loginFai) < time.Minute {
		return fmt.Errorf("tgto123 登录近期失败，等待重试")
	}
	payload, _ := json.Marshal(map[string]string{"username": c.username, "password": c.password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/login", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("连接 tgto123 失败：%w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if !out.Success {
		c.loginFai = time.Now()
		return fmt.Errorf("tgto123 登录失败（HTTP %d）", resp.StatusCode)
	}
	c.loggedIn = true
	return nil
}

// do 已登录 GET，返回 body 与状态码
func (c *Tgto123FeedClient) do(ctx context.Context, path string) ([]byte, int, error) {
	if err := c.login(ctx); err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusFound {
		c.mu.Lock()
		c.loggedIn = false
		c.mu.Unlock()
	}
	return body, resp.StatusCode, nil
}

// ---- tgto123 响应 → Feed 载荷的中间形状（JSON tag 与 FeedItemPayload /
// FeedEpisodePayload 一致，上层按 tag 解析） ----

type tgto123FeedItem struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	MediaType    string  `json:"media_type"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	VoteAvg      float64 `json:"vote_average"`
	ReleaseDate  string  `json:"release_date"`
	Overview     string  `json:"overview"`
	TMDBID       int64   `json:"tmdb_id"`
	Provider     string  `json:"provider"`
	Rank         int     `json:"rank"`
}

type tgto123FeedItemPayload struct {
	Items []tgto123FeedItem `json:"items"`
	AvailableRegions []struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	} `json:"available_regions"`
}

type tgto123FeedEpisode struct {
	ID            int64   `json:"id"`
	TMDBID        int64   `json:"tmdb_id"`
	TVDBID        int64   `json:"tvdb_id"`
	SeriesName    string  `json:"series_name"`
	MediaType     string  `json:"media_type"`
	SeasonNumber  int     `json:"season_number"`
	EpisodeNumber int     `json:"episode_number"`
	Name          string  `json:"name"`
	AirDate       string  `json:"air_date"`
	AirTimestamp  int64   `json:"air_timestamp"`
	PosterPath    string  `json:"poster_path"`
	VoteAvg       float64 `json:"vote_average"`
	Overview      string  `json:"overview"`
}

type tgto123FeedEpisodePayload struct {
	Episodes []tgto123FeedEpisode `json:"episodes"`
}

// GetStreamingTop 流媒体榜单（GET /api/media/rankings/{provider}，FeedClient 实现）
func (c *Tgto123FeedClient) GetStreamingTop(ctx context.Context, provider, region, mediaType string) (*hdhive.OAuthAPIResponse, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "netflix"
	}
	q := []string{"page=1"}
	if mt := strings.ToLower(strings.TrimSpace(mediaType)); mt != "" {
		q = append(q, "media_type="+mt)
	}
	if region = strings.ToUpper(strings.TrimSpace(region)); region != "" {
		q = append(q, "region="+region)
	}
	path := fmt.Sprintf("/api/media/rankings/%s?%s", provider, strings.Join(q, "&"))
	body, status, err := c.do(ctx, path)
	if err != nil {
		return nil, err
	}
	var out struct {
		Success bool `json:"success"`
		Data    struct {
			Items []struct {
				Rank        int     `json:"rank"`
				Title       string  `json:"title"`
				MediaType   string  `json:"media_type"`
				PosterURL   string  `json:"poster_url"`
				BackdropURL string  `json:"backdrop_url"`
				Score       float64 `json:"score"`
				ReleaseDate string  `json:"release_date"`
				Overview    string  `json:"overview"`
				TMDBID      int64   `json:"tmdb_id"`
				ExternalID  string  `json:"external_id"`
				Provider    string  `json:"provider"`
				ProviderLbl string  `json:"provider_label"`
			} `json:"items"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析 tgto123 榜单响应失败：%v", err)
	}
	if !out.Success {
		return &hdhive.OAuthAPIResponse{Success: false, StatusCode: status, Message: firstNonEmptyStr(out.Message, "tgto123 榜单无数据")}, nil
	}
	payload := tgto123FeedItemPayload{Items: make([]tgto123FeedItem, 0, len(out.Data.Items))}
	for _, it := range out.Data.Items {
		payload.Items = append(payload.Items, tgto123FeedItem{
			ID:           firstNonEmptyStr(it.ExternalID, fmt.Sprintf("%s-%s", provider, it.Title)),
			Title:        it.Title,
			MediaType:    it.MediaType,
			PosterPath:   it.PosterURL,
			BackdropPath: it.BackdropURL,
			VoteAvg:      it.Score,
			ReleaseDate:  it.ReleaseDate,
			Overview:     it.Overview,
			TMDBID:       it.TMDBID,
			Provider:     firstNonEmptyStr(it.Provider, it.ProviderLbl, provider),
			Rank:         it.Rank,
		})
	}
	raw, _ := json.Marshal(payload)
	return &hdhive.OAuthAPIResponse{Success: true, StatusCode: http.StatusOK, Data: raw}, nil
}

// GetCalendar 追剧日历（GET /api/media/calendar，FeedClient 实现）
func (c *Tgto123FeedClient) GetCalendar(ctx context.Context, days int) (*hdhive.OAuthAPIResponse, error) {
	if days <= 0 {
		days = 7
	}
	body, status, err := c.do(ctx, fmt.Sprintf("/api/media/calendar?days=%d", days))
	if err != nil {
		return nil, err
	}
	var out struct {
		Success bool `json:"success"`
		Data    struct {
			Items []struct {
				CalendarDate    string  `json:"calendar_date"`
				FirstAired      string  `json:"first_aired"`
				MediaType       string  `json:"media_type"`
				Title           string  `json:"title"`
				PosterURL       string  `json:"poster_url"`
				BackdropURL     string  `json:"backdrop_url"`
				Score           float64 `json:"score"`
				TMDBID          int64   `json:"tmdb_id"`
				Overview        string  `json:"overview"`
				CalendarEpisode *struct {
					AirDate       string `json:"air_date"`
					EpisodeNumber int    `json:"episode_number"`
					Name          string `json:"name"`
					SeasonNumber  int    `json:"season_number"`
				} `json:"calendar_episode"`
			} `json:"items"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析 tgto123 日历响应失败：%v", err)
	}
	if !out.Success {
		return &hdhive.OAuthAPIResponse{Success: false, StatusCode: status, Message: firstNonEmptyStr(out.Message, "tgto123 日历无数据")}, nil
	}
	payload := tgto123FeedEpisodePayload{Episodes: make([]tgto123FeedEpisode, 0, len(out.Data.Items))}
	for _, it := range out.Data.Items {
		ep := tgto123FeedEpisode{
			TMDBID:     it.TMDBID,
			SeriesName: it.Title,
			MediaType:  it.MediaType,
			Name:       it.Title,
			AirDate:    it.CalendarDate,
			PosterPath: it.PosterURL,
			VoteAvg:    it.Score,
			Overview:   it.Overview,
		}
		if it.CalendarEpisode != nil {
			ep.SeasonNumber = it.CalendarEpisode.SeasonNumber
			ep.EpisodeNumber = it.CalendarEpisode.EpisodeNumber
			ep.Name = it.CalendarEpisode.Name
			if it.CalendarEpisode.AirDate != "" {
				ep.AirDate = it.CalendarEpisode.AirDate
			}
		}
		payload.Episodes = append(payload.Episodes, ep)
	}
	raw, _ := json.Marshal(payload)
	return &hdhive.OAuthAPIResponse{Success: true, StatusCode: http.StatusOK, Data: raw}, nil
}

// ---------------------------------------------------------------------------
// RE0 资源搜索 / 解锁转存（替代失效的四通道：symedia/tgtodrive/nanshare/official）
// ---------------------------------------------------------------------------

// tgto123ResourceItem tgto123 资源搜索条目（/api/media/resources/search）
type tgto123ResourceItem struct {
	ItemKey          string  `json:"item_key"`
	Title            string  `json:"title"`
	Source           string  `json:"source"`
	Provider         string  `json:"provider"`
	ProviderLabel    string  `json:"provider_label"`
	Slug             string  `json:"slug"`
	LinkType         string  `json:"link_type"`
	ShareURL         string  `json:"share_url"`
	Size             string  `json:"size"`
	IsUnlocked       bool    `json:"is_unlocked"`
	PointsKnown      bool    `json:"points_known"`
	UnlockPoints     int     `json:"unlock_points"`
	UnlockedUsersCt  int     `json:"unlocked_users_count"`
	Remark           string  `json:"remark"`
	ValidateMessage  string  `json:"validate_message"`
	IsOfficial       bool    `json:"is_official"`
	Sharer           string  `json:"sharer"`
	SpecTags         []string `json:"resource_spec_tags"`
	SubtitleLangs    []string `json:"subtitle_languages"`
	SubtitleTypes    []string `json:"subtitle_types"`
	Episode          *struct {
		SeasonNum       *int `json:"season_num"`
		EpisodeNum      *int `json:"episode_num"`
		EndEpisodeNum   *int `json:"end_episode_num"`
		TotalEpisodeNum *int `json:"total_episode_num"`
		IsComplete      bool `json:"is_complete"`
		IsUpdated       bool `json:"is_updated"`
	} `json:"episode"`
}

// Tgto123SearchResources 通过 tgto123 反代搜索 RE0 资源（替代 HiveQueryResourcesWithFailover）
func Tgto123SearchResources(ctx context.Context, title string, tmdbID int64, mediaType, year string) ([]hdhive.Resource, error) {
	client := newTgto123FeedClient()
	if client == nil {
		return nil, fmt.Errorf("tgto123 反代未配置")
	}
	if mediaType != "tv" {
		mediaType = "movie"
	}
	payload, _ := json.Marshal(map[string]any{
		"title":      title,
		"tmdb_id":    tmdbID,
		"media_type": mediaType,
		"year":       year,
		"sources":    []string{"hdhive"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/api/media/resources/search", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := client.login(ctx); err != nil {
		return nil, err
	}
	resp, err := client.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusFound {
		client.mu.Lock()
		client.loggedIn = false
		client.mu.Unlock()
		return nil, fmt.Errorf("tgto123 会话失效，请重新填写会话")
	}
	var out struct {
		Success bool `json:"success"`
		Data    struct {
			Items []tgto123ResourceItem `json:"items"`
			Errors []struct {
				Source string `json:"source"`
				Error  string `json:"error"`
			} `json:"errors"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析 tgto123 资源响应失败：%v", err)
	}
	if !out.Success {
		return nil, fmt.Errorf("%s", firstNonEmptyStr(out.Message, "tgto123 资源搜索失败"))
	}
	resources := make([]hdhive.Resource, 0, len(out.Data.Items))
	for _, it := range out.Data.Items {
		res := hdhive.Resource{
			Slug:               it.Slug,
			Title:              it.Title,
			PanType:            it.Provider,
			ShareSize:          it.Size,
			Remark:             it.Remark,
			UnlockPoints:       it.UnlockPoints,
			UnlockedUsersCount: it.UnlockedUsersCt,
			ValidateMessage:    it.ValidateMessage,
			IsOfficial:         it.IsOfficial,
			IsUnlocked:         it.IsUnlocked,
			SubtitleLanguage:   it.SubtitleLangs,
			SubtitleType:       it.SubtitleTypes,
		}
		// 规格标签按「键:值」分桶到分辨率/片源/字幕
		for _, tag := range it.SpecTags {
			k, v, ok := strings.Cut(tag, ":")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch {
			case strings.Contains(k, "分辨率"):
				res.VideoResolution = append(res.VideoResolution, v)
			case strings.Contains(k, "片源"), strings.Contains(k, "来源"):
				res.Source = append(res.Source, v)
			case strings.Contains(k, "字幕"):
				res.SubtitleLanguage = append(res.SubtitleLanguage, v)
			}
		}
		resources = append(resources, res)
	}
	return resources, nil
}

// Tgto123TransferResource 通过 tgto123 反代解锁并转存 RE0 资源
// （tgto123 侧完成解锁与转存，落盘目录由 tgto123 基础配置的保存目录决定）
func Tgto123TransferResource(ctx context.Context, slug, provider string) (string, error) {
	client := newTgto123FeedClient()
	if client == nil {
		return "", fmt.Errorf("tgto123 反代未配置")
	}
	if err := client.login(ctx); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]any{
		"source":   "hdhive",
		"provider": provider,
		"slug":     slug,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/api/media/resources/transfer", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	var out struct {
		Success bool `json:"success"`
		Data    struct {
			Message string `json:"message"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("解析 tgto123 转存响应失败：%v", err)
	}
	if !out.Success {
		return "", fmt.Errorf("%s", firstNonEmptyStr(out.Message, out.Data.Message, "tgto123 转存失败"))
	}
	return firstNonEmptyStr(out.Data.Message, out.Message, "转存成功"), nil
}

// Tgto123ResourceProviderKey RE0 网盘类型 → tgto123 转存 provider 键
func Tgto123ResourceProviderKey(panType string) string {
	switch strings.ToLower(strings.TrimSpace(panType)) {
	case "guangyapan", "guangya", "gy":
		return "guangya"
	case "123", "123pan":
		return "123"
	case "115":
		return "115"
	}
	return strings.ToLower(strings.TrimSpace(panType))
}
