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
	}{
		Name:         req.Name,
		Year:         req.Year,
		Type:         toMoviePilotType(req.Type),
		TmdbId:       req.TmdbId,
		Season:       req.Season,
		TotalEpisode: req.TotalEpisode,
		SavePath:     req.SavePath,
		Sites:        req.Sites,
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