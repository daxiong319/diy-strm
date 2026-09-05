// Package moviepilot 提供 MoviePilot 对接：订阅管理、下载任务检测、下载完成目录上传 139。
package moviepilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/helpers"
)

// Client MoviePilot HTTP 客户端
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient 创建 MoviePilot 客户端
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// do 发送请求，自动附加 token，错误时返回含状态码/响应体的错误
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	if c == nil || c.BaseURL == "" || c.Token == "" {
		return fmt.Errorf("MoviePilot 配置不完整（地址或 API Token 为空）")
	}
	u := c.BaseURL + path
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("序列化请求失败：%v", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return fmt.Errorf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	q := req.URL.Query()
	q.Set("token", c.Token)
	req.URL.RawQuery = q.Encode()

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("请求 MoviePilot 失败：%v", err)
	}
	defer resp.Body.Close()
	bodyText, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MoviePilot 接口 %s %s 返回 %d：%s", method, path, resp.StatusCode, strings.TrimSpace(string(bodyText)))
	}
	if out != nil && len(bodyText) > 0 {
		if err := json.Unmarshal(bodyText, out); err != nil {
			return fmt.Errorf("解析 MoviePilot 响应失败：%v", err)
		}
	}
	return nil
}

// Subscribe MoviePilot 订阅（与官方 API 字段对齐）
type Subscribe struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Year         any    `json:"year"`
	Type         string `json:"type"` // movie/tv
	Keyword      string `json:"keyword"`
	TmdbId       int64  `json:"tmdbid"`
	Season       int    `json:"season"`
	TotalEpisode int    `json:"total_episode"`
	LackEpisode  int    `json:"lack_episode"`
	State        string `json:"state"` // R-订阅中 P-完成 S-停止
	SavePath     string `json:"save_path"`
	Sites        []int  `json:"sites"`
	Poster       string `json:"poster"`
	MediaSource  string `json:"media_source"`
	MediaID      string `json:"media_id"`
	Include      string `json:"include"` // 包含正则（促销优选锚定促销名）
}

// ListSubscribes 查询所有订阅
func (c *Client) ListSubscribes(ctx context.Context) ([]*Subscribe, error) {
	var out []*Subscribe
	if err := c.do(ctx, http.MethodGet, "/api/v1/subscribe/list", nil, &out); err != nil {
		return nil, err
	}
	for _, s := range out {
		if s != nil {
			s.Type = fromMoviePilotType(s.Type)
		}
	}
	return out, nil
}

// CreateSubscribeRequest 添加订阅请求
type CreateSubscribeRequest struct {
	Name         string `json:"name"`
	Year         string `json:"year,omitempty"`
	Type         string `json:"type"`
	TmdbId       int64  `json:"tmdbid"`
	Season       int    `json:"season,omitempty"`
	TotalEpisode int    `json:"total_episode,omitempty"`
	SavePath     string `json:"save_path,omitempty"`
	Sites        []int  `json:"sites,omitempty"`
	// Include MP 订阅「包含」正则：过滤内容含促销串（普通/免费/2X免费/50%/2X 50%…），
	// 促销优选即通过锚定正则匹配该串实现；空=不过滤
	Include string `json:"include,omitempty"`
}

// Response MoviePilot 通用响应
type Response struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

// CreateSubscribe 添加订阅，返回订阅 ID
func (c *Client) CreateSubscribe(ctx context.Context, req *CreateSubscribeRequest) (int64, error) {
	mpReq := struct {
		Name         string `json:"name"`
		Year         string `json:"year,omitempty"`
		Type         string `json:"type"`
		TmdbId       int64  `json:"tmdbid"`
		Season       int    `json:"season,omitempty"`
		TotalEpisode int    `json:"total_episode,omitempty"`
		SavePath     string `json:"save_path,omitempty"`
		Sites        []int  `json:"sites,omitempty"`
		Include      string `json:"include,omitempty"`
	}{
		Name:         req.Name,
		Year:         req.Year,
		Type:         toMoviePilotType(req.Type),
		TmdbId:       req.TmdbId,
		Season:       req.Season,
		TotalEpisode: req.TotalEpisode,
		SavePath:     req.SavePath,
		Sites:        req.Sites,
		Include:      req.Include,
	}
	var out Response
	if err := c.do(ctx, http.MethodPost, "/api/v1/subscribe/", &mpReq, &out); err != nil {
		return 0, err
	}
	if !out.Success {
		return 0, fmt.Errorf("MoviePilot 添加订阅失败:%s", out.Message)
	}
	id := int64(0)
	switch v := out.Data.(type) {
	case float64:
		id = int64(v)
	case map[string]any:
		if n, ok := v["id"].(float64); ok {
			id = int64(n)
		}
	}
	return id, nil
}

// toMoviePilotType 将内部类型（movie/tv）转为 MP 类型值（电影/电视剧）
func toMoviePilotType(t string) string {
	switch t {
	case "movie":
		return "电影"
	case "tv":
		return "电视剧"
	}
	return t
}

// fromMoviePilotType 将 MP 类型值（电影/电视剧）转为内部类型（movie/tv）
func fromMoviePilotType(t string) string {
	switch t {
	case "电影":
		return "movie"
	case "电视剧":
		return "tv"
	}
	return t
}

// SearchSubscribe 立即搜索指定订阅
func (c *Client) SearchSubscribe(ctx context.Context, subscribeID int64) error {
	var out Response
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/subscribe/search/%d", subscribeID), nil, &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("MoviePilot 触发搜索失败：%s", out.Message)
	}
	return nil
}

// DeleteSubscribe 删除订阅
func (c *Client) DeleteSubscribe(ctx context.Context, subscribeID int64) error {
	var out Response
	if err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/subscribe/%d", subscribeID), nil, &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("MoviePilot 删除订阅失败：%s", out.Message)
	}
	return nil
}

// UpdateSubscribeStatus 更新订阅状态（R-订阅中 P-完成 S-停止）
func (c *Client) UpdateSubscribeStatus(ctx context.Context, subscribeID int64, state string) error {
	var out Response
	path := fmt.Sprintf("/api/v1/subscribe/status/%d?state=%s", subscribeID, url.QueryEscape(state))
	if err := c.do(ctx, http.MethodPut, path, nil, &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("MoviePilot 更新订阅状态失败：%s", out.Message)
	}
	return nil
}

// PromotionIncludeRegex 促销优选（单选模式）→ MP 订阅 include 正则。
// MP 的订阅附加过滤把「标题 副标题 标签 促销名」拼成一段内容做正则匹配（re.I，无 MULTILINE），
// 促销名（普通/免费/2X免费/50%/2X 50%…）恒为最后一段，故用 $ 锚定精确匹配：
//   - free:   排除「2X免费/4X免费」（它们以 X 结尾再接免费）
//   - half:   排除「2X 50%」（50% 前是 "X "）
// 返回空串表示不限促销。
func PromotionIncludeRegex(promotion string) string {
	switch promotion {
	case "free":
		return `(?<![Xx])免费$`
	case "normal":
		return `普通$`
	case "2xfree":
		return `2X免费$`
	case "half":
		return `(?<!X )50%$`
	case "2xhalf":
		return `2X 50%$`
	default:
		return ""
	}
}

// PromotionTierIncludeRegex 促销优先阶梯（多级回退模式）→ MP 订阅 include 正则。
// order 为促销优先级（高→低），tier 为当前允许到的层下标（0=最高层）：
// 放行第 0..tier 层的全部促销状态（正则 or 连接），配合促销监督的逐层回退实现
// 「优先下载高价值促销，没有才退而求其次」。tier 越界或 order 为空返回空串（不限）。
func PromotionTierIncludeRegex(order []string, tier int) string {
	if len(order) == 0 || tier < 0 || tier >= len(order) {
		return ""
	}
	// 各促销状态的单选正则（与 PromotionIncludeRegex 一致，显式列出避免顺序耦合）
	statePatterns := map[string]string{
		"free":   `(?<![Xx])免费$`,
		"2xfree": `2X免费$`,
		"normal": `普通$`,
		"half":   `(?<!X )50%$`,
		"2xhalf": `2X 50%$`,
	}
	parts := make([]string, 0, tier+1)
	for i := 0; i <= tier; i++ {
		if p, ok := statePatterns[order[i]]; ok {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "|")
}

// PromotionMonitorState 推导促销监督的正则与描述：给定优先级与当前层，
// 返回 include 正则与当前层名（用于日志）。
func PromotionMonitorState(order []string, tier int) (include, tierName string) {
	include = PromotionTierIncludeRegex(order, tier)
	if tier >= 0 && tier < len(order) {
		tierName = order[tier]
	}
	return include, tierName
}

// UpdateSubscribeInclude 更新订阅的「包含」过滤正则（促销阶梯用）。
// MP 的 PUT /api/v1/subscribe/ 是全量更新（缺 name 等必填字段会 500），
// 故先取订阅全量 JSON，补丁式修改 include 后整体回传，避免清空其它字段。
func (c *Client) UpdateSubscribeInclude(ctx context.Context, subscribeID int64, include string) error {
	var subs []map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/v1/subscribe/list", nil, &subs); err != nil {
		return err
	}
	var target map[string]any
	for _, s := range subs {
		if s == nil {
			continue
		}
		switch idv := s["id"].(type) {
		case float64:
			if int64(idv) == subscribeID {
				target = s
			}
		case string:
			if idv == strconv.FormatInt(subscribeID, 10) {
				target = s
			}
		}
		if target != nil {
			break
		}
	}
	if target == nil {
		return fmt.Errorf("MoviePilot 订阅 %d 不存在", subscribeID)
	}
	target["include"] = include
	var out Response
	if err := c.do(ctx, http.MethodPut, "/api/v1/subscribe/", &target, &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("MoviePilot 更新订阅过滤失败：%s", out.Message)
	}
	return nil
}

// DownloadTorrent MP 下载器任务
type DownloadTorrent struct {
	Hash        string  `json:"hash"`
	Title       string  `json:"title"`
	Name        string  `json:"name"`
	Year        string  `json:"year"`
	SeasonEpisode string `json:"season_episode"`
	Path        string  `json:"path"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
	State       string  `json:"state"`
	Progress    float64 `json:"progress"`
	Category    string  `json:"category"`
	Media       map[string]any `json:"media"`
}

// ListDownloads 查询所有下载任务
func (c *Client) ListDownloads(ctx context.Context) ([]*DownloadTorrent, error) {
	var out []*DownloadTorrent
	if err := c.do(ctx, http.MethodGet, "/api/v1/download/", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteDownload 删除下载任务（MP DELETE /api/v1/download/{hash}?name=）。
// MP 侧 remove_torrents 的 delete_file 默认为 True，即同时删除种子与本地文件，
// 用于做种保留期满后释放磁盘空间。
func (c *Client) DeleteDownload(ctx context.Context, hash, name string) error {
	path := fmt.Sprintf("/api/v1/download/%s?name=%s", hash, url.QueryEscape(name))
	var out Response
	if err := c.do(ctx, http.MethodDelete, path, nil, &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("MoviePilot 删除下载任务失败：%s", out.Message)
	}
	return nil
}

// DownloadHistory MP 下载历史记录（对应 /api/v1/history/download 返回项）
// 下载任务完成后会从下载列表移除，历史记录是可靠的完成事件来源。
type DownloadHistory struct {
	ID            int64  `json:"id"`
	Path          string `json:"path"` // MP 侧保存路径，如 alist:/中国移动云盘/影视/待整理/日韩剧集/xxx
	Type          string `json:"type"` // 电视剧/电影
	Title         string `json:"title"`
	Year          string `json:"year"`
	TmdbId        int64  `json:"tmdbid"`
	MediaSource   string `json:"media_source"`
	MediaID       string `json:"media_id"`
	Seasons       string `json:"seasons"`  // S01
	Episodes      string `json:"episodes"` // 1-12
	Poster        string `json:"poster"`
	DownloadHash  string `json:"download_hash"`
	TorrentName   string `json:"torrent_name"`
	Date          string `json:"date"`
	MediaCategory string `json:"media_category"`
}

// ListDownloadHistory 分页查询下载历史（page 从 1 开始）
func (c *Client) ListDownloadHistory(ctx context.Context, page, count int) ([]*DownloadHistory, error) {
	var out []*DownloadHistory
	path := fmt.Sprintf("/api/v1/history/download?page=%d&count=%d", page, count)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MPRecognizeResult MP /api/v1/media/recognize 的识别结果（只取整理所需字段）
type MPRecognizeResult struct {
	Category string // movie/tv
	Title    string // 中文标题（TMDB 官方名）
	Year     int
	Season   int
	Episode  int
	TmdbID   int64
}

// RecognizeMedia 调 MP 文件名识别接口（GET /api/v1/media/recognize?title=）。
// MP 侧用自身元数据链识别并查 TMDB，识别失败（查不到媒体）返回 ok=false。
func (c *Client) RecognizeMedia(ctx context.Context, fileName string) (*MPRecognizeResult, bool) {
	path := fmt.Sprintf("/api/v1/media/recognize?title=%s", url.QueryEscape(fileName))
	var out struct {
		MediaInfo map[string]any `json:"media_info"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, false
	}
	mi := out.MediaInfo
	if mi == nil {
		return nil, false
	}
	// MP 识别未命中媒体时 type 为「未知」/ tmdb_id 为空
	if t, _ := mi["type"].(string); t == "" || t == "未知" {
		return nil, false
	}
	tmdbID := toInt64(mi["tmdb_id"])
	if tmdbID <= 0 {
		return nil, false
	}
	res := &MPRecognizeResult{TmdbID: tmdbID}
	if t, _ := mi["type"].(string); t == "电视剧" {
		res.Category = "tv"
	} else {
		res.Category = "movie"
	}
	if s, ok := mi["title"].(string); ok {
		res.Title = s
	}
	if res.Title == "" {
		return nil, false
	}
	res.Year = int(toInt64(mi["year"]))
	res.Season = int(toInt64(mi["season"]))
	res.Episode = int(toInt64(mi["episode"]))
	return res, true
}

// toInt64 宽松转换 JSON 数值（int/float64/string 数字均接受）
func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%g", &f); err == nil {
			return int64(f)
		}
	}
	return 0
}

// TestConnection 测试连接（查询下载器列表，失败时回退订阅列表）
func (c *Client) TestConnection(ctx context.Context) error {
	var out any
	err := c.do(ctx, http.MethodGet, "/api/v1/download/clients", nil, &out)
	if err == nil {
		helpers.AppLogger.Infof("MoviePilot 连接测试成功")
		return nil
	}
	var subs []*Subscribe
	if err2 := c.do(ctx, http.MethodGet, "/api/v1/subscribe/list", nil, &subs); err2 != nil {
		return fmt.Errorf("%v；%v", err, err2)
	}
	return nil
}