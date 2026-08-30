package guangyapan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"resty.dev/v3"
)

// API 地址常量（逆向自光鸭云盘 Web 端，协议参考 alist GuangYaPan 驱动）
const (
	AccountBaseURL = "https://account.guangyapan.com"
	APIBASEURL     = "https://api.guangyapan.com"
	DefaultClient  = "aMe-8VSlkrbQXpUR"
)

// Client 光鸭云盘逆向 API 客户端
// 认证方式：AccessToken（Bearer）+ RefreshToken（过期自动刷新）
type Client struct {
	accountID    uint
	accessToken  string
	refreshToken string
	clientID     string
	deviceID     string

	// API 地址（默认使用线上常量，测试时可注入 mock 地址）
	accountBase string
	apiBase     string

	tokenMu sync.RWMutex
	client  *resty.Client

	limiterLock    sync.RWMutex
	limiters       map[string]*rate.Limiter
	defaultLimiter *rate.Limiter // 未显式配置的路径统一走该限流
}

// NewClient 创建光鸭云盘客户端
// accessToken 为访问令牌，refreshToken 用于令牌过期自动刷新（可为空）
func NewClient(accountID uint, accessToken, refreshToken string) *Client {
	client := resty.New()
	client.SetTimeout(60 * time.Second)
	client.SetRetryCount(0)

	deviceID := randomDeviceID()
	return &Client{
		accountID:      accountID,
		accessToken:    strings.TrimSpace(accessToken),
		refreshToken:   strings.TrimSpace(refreshToken),
		clientID:       DefaultClient,
		deviceID:       deviceID,
		accountBase:    AccountBaseURL,
		apiBase:        APIBASEURL,
		client:         client,
		limiters:       make(map[string]*rate.Limiter),
		defaultLimiter: rate.NewLimiter(2, 1), // 默认 2 QPS，防 burst 触发服务端限流
	}
}

// SetBaseURL 注入 API 地址（测试用）
func (c *Client) SetBaseURL(accountBase, apiBase string) {
	c.accountBase = accountBase
	c.apiBase = apiBase
}

// SetTokens 设置访问令牌与刷新令牌
func (c *Client) SetTokens(accessToken, refreshToken string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.accessToken = strings.TrimSpace(accessToken)
	c.refreshToken = strings.TrimSpace(refreshToken)
}

// SetAccessToken 设置访问令牌（从数据库恢复会话时使用）
func (c *Client) SetAccessToken(token string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.accessToken = token
}

// GetAccessToken 获取当前访问令牌
func (c *Client) GetAccessToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.accessToken
}

// SetRefreshToken 设置刷新令牌
func (c *Client) SetRefreshToken(token string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.refreshToken = token
}

// GetRefreshToken 获取当前刷新令牌
func (c *Client) GetRefreshToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.refreshToken
}

// SetRateLimit 为指定 API 路径设置 QPS 限流
func (c *Client) SetRateLimit(path string, qps int) {
	c.limiterLock.Lock()
	defer c.limiterLock.Unlock()
	if qps <= 0 {
		qps = 2
	}
	c.limiters[path] = rate.NewLimiter(rate.Limit(qps), 1)
}

// waitForPermission 等待限流许可（未配置的路径走默认限流）
func (c *Client) waitForPermission(ctx context.Context, path string) error {
	c.limiterLock.RLock()
	limiter, exists := c.limiters[path]
	c.limiterLock.RUnlock()
	if exists {
		return limiter.Wait(ctx)
	}
	if c.defaultLimiter != nil {
		return c.defaultLimiter.Wait(ctx)
	}
	return nil
}

// Close 关闭底层 HTTP 客户端
func (c *Client) Close() error {
	if c.client != nil {
		c.client.Close()
	}
	return nil
}

// randomDeviceID 生成 32 位十六进制设备 ID
func randomDeviceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "0123456789abcdef0123456789abcdef"
	}
	return hex.EncodeToString(b)
}
