// Package moviepilot 移植自 diy-strm 的 MoviePilot 集成：
// 轮询 MoviePilot 订阅与下载、按促销阶梯放宽过滤、下载完成后取回本地文件、
// 上传网盘、整理归档并触发 STRM 生成。
package moviepilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client 是 MoviePilot REST API 的最小客户端。
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient 构造客户端，baseURL 末尾斜杠会被裁剪。
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// do 执行一次 API 调用。token 以查询参数传递（MoviePilot 的鉴权方式）。
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	if c.BaseURL == "" || c.Token == "" {
		return fmt.Errorf("MoviePilot 配置不完整（地址或 API Token 为空）")
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("序列化请求体失败：%w", err)
		}
		reader = bytes.NewReader(buf)
	}
	u := c.BaseURL + path
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	u = u + sep + "token=" + url.QueryEscape(c.Token)
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MoviePilot 接口 %s %s 返回 %d：%s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("解析 MoviePilot 响应失败：%w", err)
	}
	return nil
}

// Subscribe MoviePilot 订阅项。
type Subscribe struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Year         any    `json:"year"`
	Type         string `json:"type"`
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
	Include      string `json:"include"`
}

// toMoviePilotType 把内部类型转为 MoviePilot 的中文类型。
func toMoviePilotType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "movie":
		return "电影"
	case "tv":
		return "电视剧"
	default:
		return t
	}
}

// fromMoviePilotType 把 MoviePilot 的中文类型转为内部类型。
func fromMoviePilotType(t string) string {
	switch strings.TrimSpace(t) {
	case "电影":
		return "movie"
	case "电视剧":
		return "tv"
	default:
		return strings.TrimSpace(t)
	}
}

// ListSubscribes 获取订阅列表。
func (c *Client) ListSubscribes(ctx context.Context) ([]*Subscribe, error) {
	var raw []*Subscribe
	if err := c.do(ctx, http.MethodGet, "/api/v1/subscribe/list", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]*Subscribe, 0, len(raw))
	for _, s := range raw {
		if s == nil {
			continue
		}
		s.Type = fromMoviePilotType(s.Type)
		out = append(out, s)
	}
	return out, nil
}

// CreateSubscribeRequest 新建订阅请求。
type CreateSubscribeRequest struct {
	Name         string `json:"name"`
	Year         string `json:"year"`
	Type         string `json:"type"`
	TmdbId       int64  `json:"tmdbid"`
	Season       int    `json:"season"`
	TotalEpisode int    `json:"total_episode"`
	SavePath     string `json:"save_path"`
	Sites        []int  `json:"sites"`
	Include      string `json:"include"`
}

// Response MoviePilot 通用响应包。
type Response struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

// CreateSubscribe 新建订阅，返回订阅 ID。
func (c *Client) CreateSubscribe(ctx context.Context, req *CreateSubscribeRequest) (int64, error) {
	payload := *req
	payload.Type = toMoviePilotType(req.Type)
	var resp Response
	if err := c.do(ctx, http.MethodPost, "/api/v1/subscribe/", payload, &resp); err != nil {
		return 0, err
	}
	if !resp.Success {
		if resp.Message != "" {
			return 0, fmt.Errorf("MoviePilot 创建订阅失败：%s", resp.Message)
		}
		return 0, fmt.Errorf("MoviePilot 创建订阅失败")
	}
	switch v := resp.Data.(type) {
	case float64:
		return int64(v), nil
	case map[string]any:
		if id, ok := v["id"]; ok {
			return toInt64(id), nil
		}
	}
	return 0, nil
}

// SearchSubscribe 触发订阅搜索。
func (c *Client) SearchSubscribe(ctx context.Context, subscribeID int64) error {
	return c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/subscribe/search/%d", subscribeID), nil, nil)
}

// DeleteSubscribe 删除订阅。
func (c *Client) DeleteSubscribe(ctx context.Context, subscribeID int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/subscribe/%d", subscribeID), nil, nil)
}

// UpdateSubscribeStatus 更新订阅状态（R-订阅中 P-完成 S-停止）。
func (c *Client) UpdateSubscribeStatus(ctx context.Context, subscribeID int64, state string) error {
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/v1/subscribe/status/%d?state=%s", subscribeID, url.QueryEscape(state)), nil, nil)
}

// promotionPatterns 各促销状态对应的 MoviePilot include 正则。
// MoviePilot 会把促销名作为拼接过滤文本的最后一段，因此用 $ 锚定；
// 负向后顾用于排除 2X免费/4X免费（free）与 2X 50%（half）。
var promotionPatterns = map[string]string{
	"free":   `(?<![Xx])免费$`,
	"normal": `普通$`,
	"2xfree": `2X免费$`,
	"half":   `(?<!X )50%$`,
	"2xhalf": `2X 50%$`,
}

// PromotionIncludeRegex 单个促销状态对应的 include 正则，未知状态返回空串。
func PromotionIncludeRegex(promotion string) string {
	return promotionPatterns[strings.ToLower(strings.TrimSpace(promotion))]
}

// PromotionTierIncludeRegex 阶梯多级回退：把第 0..tier 层的促销正则用 | 连接，
// 越靠前的层优先级越高，MoviePilot 侧按此过滤资源。
func PromotionTierIncludeRegex(order []string, tier int) string {
	if len(order) == 0 || tier < 0 || tier >= len(order) {
		return ""
	}
	parts := make([]string, 0, tier+1)
	for i := 0; i <= tier; i++ {
		if p := promotionPatterns[strings.ToLower(strings.TrimSpace(order[i]))]; p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "|")
}

// PromotionMonitorState 返回该层对应的 include 正则与层名。
func PromotionMonitorState(order []string, tier int) (string, string) {
	if tier < 0 || tier >= len(order) {
		return "", ""
	}
	return PromotionTierIncludeRegex(order, tier), order[tier]
}

// UpdateSubscribeInclude 更新订阅的 include 过滤。
// MoviePilot 的 PUT /api/v1/subscribe/ 是全量更新（缺必填字段会 500），
// 因此先取回完整对象、只改 include 字段后再整体提交。
func (c *Client) UpdateSubscribeInclude(ctx context.Context, subscribeID int64, include string) error {
	var raw []map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/v1/subscribe/list", nil, &raw); err != nil {
		return err
	}
	var target map[string]any
	for _, item := range raw {
		if toInt64(item["id"]) == subscribeID {
			target = item
			break
		}
	}
	if target == nil {
		return fmt.Errorf("MoviePilot 未找到订阅 %d", subscribeID)
	}
	target["include"] = include
	return c.do(ctx, http.MethodPut, "/api/v1/subscribe/", target, nil)
}

// DownloadTorrent MoviePilot 下载器中的任务。
type DownloadTorrent struct {
	Hash          string         `json:"hash"`
	Title         string         `json:"title"`
	Name          string         `json:"name"`
	Year          string         `json:"year"`
	SeasonEpisode string         `json:"season_episode"`
	Path          string         `json:"path"`
	SavePath      string         `json:"save_path"`
	ContentPath   string         `json:"content_path"`
	State         string         `json:"state"`
	Progress      float64        `json:"progress"` // MoviePilot 侧为 0~100
	Category      string         `json:"category"`
	Media         map[string]any `json:"media"`
}

// ListDownloads 获取下载器任务列表。
func (c *Client) ListDownloads(ctx context.Context) ([]*DownloadTorrent, error) {
	var raw []*DownloadTorrent
	if err := c.do(ctx, http.MethodGet, "/api/v1/download/", nil, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// DeleteDownload 删除下载任务。MoviePilot 的 delete_file 默认为 true，
// 会同时删除种子与本地文件——用于做种保留到期后释放磁盘。
func (c *Client) DeleteDownload(ctx context.Context, hash, name string) error {
	path := fmt.Sprintf("/api/v1/download/%s?name=%s", url.PathEscape(hash), url.QueryEscape(name))
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// DownloadHistory 下载历史记录。
// 已完成的下载会从下载列表中移除，因此历史是可靠的"下载完成"事件来源。
type DownloadHistory struct {
	ID            int64  `json:"id"`
	Path          string `json:"path"`
	Type          string `json:"type"`
	Title         string `json:"title"`
	Year          string `json:"year"`
	TmdbId        int64  `json:"tmdbid"`
	MediaSource   string `json:"media_source"`
	MediaID       string `json:"media_id"`
	Seasons       string `json:"seasons"`
	Episodes      string `json:"episodes"`
	Poster        string `json:"poster"`
	DownloadHash  string `json:"download_hash"`
	TorrentName   string `json:"torrent_name"`
	Date          string `json:"date"`
	MediaCategory string `json:"media_category"`
}

// ListDownloadHistory 获取下载历史（分页）。
func (c *Client) ListDownloadHistory(ctx context.Context, page, count int) ([]*DownloadHistory, error) {
	var raw []*DownloadHistory
	path := fmt.Sprintf("/api/v1/history/download?page=%d&count=%d", page, count)
	if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// MPRecognizeResult MoviePilot 媒体识别结果。
type MPRecognizeResult struct {
	Category string
	Title    string
	Year     int
	Season   int
	Episode  int
	TmdbID   int64
}

// RecognizeMedia 调用 MoviePilot 识别文件名。识别不可靠时返回 ok=false。
func (c *Client) RecognizeMedia(ctx context.Context, fileName string) (*MPRecognizeResult, bool) {
	var resp struct {
		MediaInfo struct {
			Type    string `json:"type"`
			Title   string `json:"title"`
			Year    any    `json:"year"`
			Season  any    `json:"season"`
			Episode any    `json:"episode"`
			TmdbID  any    `json:"tmdb_id"`
		} `json:"media_info"`
	}
	path := "/api/v1/media/recognize?title=" + url.QueryEscape(fileName)
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, false
	}
	info := resp.MediaInfo
	mediaType := strings.TrimSpace(info.Type)
	if mediaType == "" || mediaType == "未知" {
		return nil, false
	}
	tmdbID := toInt64(info.TmdbID)
	if tmdbID <= 0 || strings.TrimSpace(info.Title) == "" {
		return nil, false
	}
	category := "movie"
	if mediaType == "电视剧" {
		category = "tv"
	}
	return &MPRecognizeResult{
		Category: category,
		Title:    strings.TrimSpace(info.Title),
		Year:     int(toInt64(info.Year)),
		Season:   int(toInt64(info.Season)),
		Episode:  int(toInt64(info.Episode)),
		TmdbID:   tmdbID,
	}, true
}

// TestConnection 测试连通性：优先下载器接口，失败回退订阅列表。
func (c *Client) TestConnection(ctx context.Context) error {
	err1 := c.do(ctx, http.MethodGet, "/api/v1/download/clients", nil, nil)
	if err1 == nil {
		return nil
	}
	err2 := c.do(ctx, http.MethodGet, "/api/v1/subscribe/list", nil, nil)
	if err2 == nil {
		return nil
	}
	return fmt.Errorf("%v；%v", err1, err2)
}

// toInt64 宽松地把 JSON 数值/字符串转为 int64。
func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		var out int64
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%g", &out); err == nil {
			return out
		}
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%g", &f); err == nil {
			return int64(f)
		}
	}
	return 0
}
