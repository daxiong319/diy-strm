package pan123

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"resty.dev/v3"
)

// Client 123 云盘逆向 API 客户端
// 协议参考 alist 的 123Pan 驱动（yun.123pan.com web 接口）
type Client struct {
	accountID   uint
	username    string
	password    string
	accessToken string
	userID      string

	// API 地址（默认使用线上常量，测试时可注入 mock 地址）
	loginBase string
	mainBase  string

	tokenMu  sync.RWMutex
	loginMu  sync.Mutex // 串行化登录/重登录，防止并发 401 触发登录风暴
	client   *resty.Client
	authFunc func(newToken string) // 令牌变化回调（重登录后持久化）

	limiterLock    sync.RWMutex
	limiters       map[string]*rate.Limiter
	defaultLimiter *rate.Limiter // 未显式配置的路径统一走该限流（123 API 高频会被拒）
}

// NewClient 创建 123 云盘客户端
// username 支持邮箱或手机号，password 为登录密码
func NewClient(accountID uint, username, password string) *Client {
	client := resty.New()
	client.SetTimeout(60 * time.Second)
	client.SetRetryCount(0)

	return &Client{
		accountID:      accountID,
		username:       username,
		password:       password,
		loginBase:      LoginApi,
		mainBase:       MainApi,
		client:         client,
		limiters:       make(map[string]*rate.Limiter),
		defaultLimiter: rate.NewLimiter(3, 1), // 默认 3 QPS，防 burst 触发服务端限流
	}
}

// SetBaseURL 注入 API 地址（测试用）
func (c *Client) SetBaseURL(loginBase, mainBase string) {
	c.loginBase = loginBase
	c.mainBase = mainBase
}

// SetAccessToken 设置登录后的访问令牌（从数据库恢复会话时使用）
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

// SetUserID 设置用户 ID（登录成功后的 userId）
func (c *Client) SetUserID(userID string) {
	c.userID = userID
}

// GetUserID 获取用户 ID
func (c *Client) GetUserID() string {
	return c.userID
}

// SetAuthChanged 设置令牌变化回调（登录或 401 自动重登录成功后触发）
// 调用方可借此将新令牌持久化到数据库，避免会话过期后每次都要重新授权
func (c *Client) SetAuthChanged(fn func(newToken string)) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.authFunc = fn
}

// notifyAuthChanged 触发令牌变化回调（仅在令牌真正变化时）
func (c *Client) notifyAuthChanged(token string) {
	c.tokenMu.RLock()
	fn := c.authFunc
	c.tokenMu.RUnlock()
	if fn != nil {
		fn(token)
	}
}

// SetRateLimit 为指定 API 路径设置 QPS 限流
func (c *Client) SetRateLimit(path string, qps int) {
	c.limiterLock.Lock()
	defer c.limiterLock.Unlock()
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
