// Package qbittorrent qBittorrent WebUI API 最小客户端（自动删种联动专用）：
// 登录（SID Cookie）→ 按做种时长筛种子 → 删除种子及本地文件。
package qbittorrent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

// Client qB WebUI 客户端
type Client struct {
	baseURL     string
	username    string
	password    string
	http        *http.Client
	loggedIn    bool
	loginFailAt time.Time
	mu          chan struct{}
}

// NewClient 创建客户端（baseURL 如 http://192.168.16.1:8080）
func NewClient(baseURL, username, password string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http:     &http.Client{Timeout: 30 * time.Second, Jar: jar},
		mu:       make(chan struct{}, 1),
	}
}

// Torrent 下载任务信息（本功能所需字段）
type Torrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	Progress    float64 `json:"progress"`
	SeedingTime int64   `json:"seeding_time"` // 已做种秒数
	Ratio       float64 `json:"ratio"`
	Category    string  `json:"category"`
}

func (c *Client) lock() func() {
	c.mu <- struct{}{}
	return func() { <-c.mu }
}

// login 登录拿 SID Cookie（成功后缓存；失败 5 分钟内不重试）
func (c *Client) login(ctx context.Context) error {
	if c.loggedIn {
		return nil
	}
	if !c.loginFailAt.IsZero() && time.Since(c.loginFailAt) < 5*time.Minute {
		return fmt.Errorf("qBittorrent 登录近期失败，等待重试")
	}
	form := fmt.Sprintf("username=%s&password=%s", c.username, c.password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/auth/login", strings.NewReader(form))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("连接 qBittorrent 失败：%w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Ok.") {
		c.loginFailAt = time.Now()
		return fmt.Errorf("qBittorrent 登录失败（HTTP %d：%s）——请检查 WebUI 账号密码", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	c.loggedIn = true
	return nil
}

// ListTorrents 全部种子
func (c *Client) ListTorrents(ctx context.Context) ([]Torrent, error) {
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v2/torrents/info?limit=0", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.loggedIn = false
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		c.loggedIn = false
		return nil, fmt.Errorf("qBittorrent 会话失效，已重置（下轮重登）")
	}
	var out []Torrent
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteTorrent 删除种子及本地文件（deleteFiles=true）
func (c *Client) DeleteTorrent(ctx context.Context, hash string, deleteFiles bool) error {
	if err := c.login(ctx); err != nil {
		return err
	}
	del := "false"
	if deleteFiles {
		del = "true"
	}
	form := fmt.Sprintf("hashes=%s&deleteFiles=%s", hash, del) // qB 4.3.x 必须带 deleteFiles（camelCase），缺失返回 400
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/torrents/delete", strings.NewReader(form))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qBittorrent 删除种子失败（HTTP %d：%s）", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// AddMagnet 提交磁力/种子链接下载任务（影视发现离线动作）。
// savePath 为空时使用 qB 默认保存路径。
func (c *Client) AddMagnet(ctx context.Context, magnetURL, savePath string) error {
	if strings.TrimSpace(magnetURL) == "" {
		return fmt.Errorf("链接内容为空")
	}
	if err := c.login(ctx); err != nil {
		return err
	}
	body := &strings.Builder{}
	body.WriteString("urls=" + magnetURL)
	if strings.TrimSpace(savePath) != "" {
		body.WriteString("&savepath=" + savePath)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/torrents/add", strings.NewReader(body.String()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		c.loggedIn = false
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		c.loggedIn = false
		return fmt.Errorf("qBittorrent 添加任务失败（HTTP %d：%s）", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}
